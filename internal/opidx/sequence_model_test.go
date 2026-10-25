// SPDX-License-Identifier: BSD-3-Clause

package opidx

import (
	"slices"
	"strings"
	"testing"
)

func TestSequenceInvariantRejectsThreadOrder(t *testing.T) {
	var seq Seq[int]
	handles, ok := seq.Splice(0, 0, []int{0, 1, 2})
	if !ok {
		t.Fatal("initial splice failed")
	}
	assertSequenceModel(t, &seq, handles)
	// Keep tree order [a,b,c], but thread [a,c,b] with increasing labels.
	// Membership, sizes, balance, parents, and thread endpoints remain valid.
	a, b, c := handles[0], handles[1], handles[2]
	seq.touch(a)
	seq.touch(b)
	seq.touch(c)
	a.next, c.prev, c.next, b.prev, b.next = c, a, b, c, nil
	b.tag, c.tag = c.tag, b.tag
	seq.last = b
	if err := seq.CheckInvariants(); err == nil || !strings.Contains(err.Error(), "tree/thread order") {
		t.Fatalf("reordered threads with increasing labels: got %v, want tree/thread order error", err)
	}
}

func TestSequenceInvariantRejectsThreadLabels(t *testing.T) {
	for _, equal := range []bool{false, true} {
		var seq Seq[int]
		handles, ok := seq.Splice(0, 0, []int{0, 1, 2})
		if !ok {
			t.Fatal("initial splice failed")
		}
		seq.touch(handles[1])
		if equal {
			handles[1].tag = handles[0].tag
		} else {
			handles[1].tag = handles[0].tag - 1
		}
		if err := seq.CheckInvariants(); err == nil || !strings.Contains(err.Error(), "labels") {
			t.Fatalf("equal=%t: got %v, want label error", equal, err)
		}
	}
}

// Seq's rank, range-edit, label and journal primitives are original Go
// infrastructure with no direct libyang counterpart. A plain value slice is
// the reference for edits; no expected state is read back from Seq. The live
// oracle test additionally checks leaf-list parsing of these model states.
func sequenceEditModelSnapshots(t *testing.T) [][]int {
	t.Helper()
	initial := make([]int, 64)
	for i := range initial {
		initial[i] = i
	}
	var seq Seq[int]
	if _, ok := seq.Splice(0, 0, slices.Clone(initial)); !ok {
		t.Fatal("initial splice failed")
	}
	model := slices.Clone(initial)
	var snapshots [][]int
	check := func(step int, op string) {
		t.Helper()
		if seq.Len() != len(model) {
			t.Fatalf("step %d (%s): length %d, want %d", step, op, seq.Len(), len(model))
		}
		handles := make([]*Handle[int], len(model))
		for i, want := range model {
			h := seq.Select(i)
			if h == nil || h.Value != want {
				t.Fatalf("step %d (%s): position %d = %v, want value %d", step, op, i, h, want)
			}
			handles[i] = h
		}
		assertSequenceModel(t, &seq, handles)
		snapshots = append(snapshots, slices.Clone(model))
	}
	check(-1, "initial")
	type edit struct {
		op             string
		lo, hi, before int
		values         []int
	}
	var script []edit
	for range 51 {
		script = append(script, edit{op: "move", lo: 63, hi: 64, before: 1})
	}
	script = append(script,
		edit{op: "move", lo: 2, hi: 64, before: 1},
		edit{op: "insert", lo: 1, values: []int{64}},
		edit{op: "checkpoint"},
		edit{op: "splice", lo: 0, hi: 65},
		edit{op: "rollback"},
		edit{op: "checkpoint"},
		edit{op: "move", lo: 1, hi: 4, before: 65},
		edit{op: "checkpoint"},
		edit{op: "splice", lo: 2, hi: 5, values: []int{70, 71}},
		edit{op: "commit"},
		edit{op: "rollback"},
		edit{op: "checkpoint"},
		edit{op: "splice", lo: 0, hi: 65},
		edit{op: "commit"},
	)
	var checkpoints []*Checkpoint[int]
	var saved [][]int
	for step, e := range script {
		switch e.op {
		case "move":
			if !seq.MoveRange(e.lo, e.hi, e.before) {
				t.Fatalf("step %d: move failed", step)
			}
			run := slices.Clone(model[e.lo:e.hi])
			model = slices.Delete(model, e.lo, e.hi)
			at := e.before
			if at > e.hi {
				at -= e.hi - e.lo
			}
			model = slices.Insert(model, at, run...)
		case "insert":
			if seq.InsertBefore(seq.Select(e.lo), e.values[0]) == nil {
				t.Fatalf("step %d: insertion failed", step)
			}
			model = slices.Insert(model, e.lo, e.values...)
		case "splice":
			if _, ok := seq.Splice(e.lo, e.hi-e.lo, slices.Clone(e.values)); !ok {
				t.Fatalf("step %d: splice failed", step)
			}
			model = slices.Replace(model, e.lo, e.hi, e.values...)
		case "checkpoint":
			checkpoints = append(checkpoints, seq.Checkpoint())
			saved = append(saved, slices.Clone(model))
		case "rollback", "commit":
			last := len(checkpoints) - 1
			if e.op == "rollback" {
				if !seq.Rollback(checkpoints[last]) {
					t.Fatalf("step %d: rollback failed", step)
				}
				model = slices.Clone(saved[last])
			} else if !seq.Commit(checkpoints[last]) {
				t.Fatalf("step %d: commit failed", step)
			}
			checkpoints = checkpoints[:last]
			saved = saved[:last]
		default:
			t.Fatalf("unknown edit %q", e.op)
		}
		check(step, e.op)
	}
	if len(checkpoints) != 0 {
		t.Fatal("edit script left an active checkpoint")
	}
	return snapshots
}

func TestSequenceIndependentEditScript(t *testing.T) {
	sequenceEditModelSnapshots(t)
}
