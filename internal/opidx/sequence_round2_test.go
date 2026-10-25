// SPDX-License-Identifier: BSD-3-Clause

package opidx

import (
	"fmt"
	"math/rand"
	"runtime"
	"slices"
	"testing"
	"weak"
)

// This reproducer uses only Seq's public API: no synthetic labels or tree state.
func TestSequenceRepeatedDensityScanWorkBound(t *testing.T) {
	for _, checkpoints := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(checkpoints), func(t *testing.T) {
			var seq Seq[int]
			if _, ok := seq.Splice(0, 0, make([]int, 64)); !ok {
				t.Fatal("initial splice failed")
			}
			for range 51 {
				if !seq.MoveRange(63, 64, 1) {
					t.Fatal("point move failed")
				}
			}
			if !seq.MoveRange(2, 64, 1) {
				t.Fatal("range move failed")
			}
			tokens := make([]*Checkpoint[int], checkpoints)
			for i := range tokens {
				tokens[i] = seq.Checkpoint()
			}
			anchor := seq.Select(1)
			bound := seq.InsertWorkBound()
			var work uint64
			seq.SetWorkHook(func(amount uint64) { work += amount })
			if seq.InsertBefore(anchor, 1) == nil {
				t.Fatal("insert failed")
			}
			t.Logf("checkpoints=%d insert work=%d pre-mutation bound=%d", checkpoints, work, bound)
			if work > bound {
				t.Fatalf("insert used %d work, pre-mutation bound %d", work, bound)
			}
			seq.SetWorkHook(nil)
			for i := len(tokens) - 1; i >= 0; i-- {
				if !seq.Rollback(tokens[i]) {
					t.Fatal("rollback failed")
				}
			}
			if err := seq.CheckInvariants(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Every bound is captured before the operation. Model/invariant checks and
// argument selection run outside the measured interval. Duplicate-heavy values,
// repeated insertion at one gap, range permutations, and nested journals all
// exercise bounds independently of the density threshold chosen by the tree.
func TestSequenceRandomizedPreMutationWorkBounds(t *testing.T) {
	for seed := int64(0); seed < 32; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed)) //nolint:gosec // reproducible property test
			var seq Seq[int]
			var model []*Handle[int]
			var tokens []*Checkpoint[int]
			var saved [][]*Handle[int]
			var work uint64
			seq.SetWorkHook(func(amount uint64) { work += amount })
			measure := func(op string, bound uint64, run func() bool) {
				t.Helper()
				before := work
				if !run() {
					t.Fatalf("%s rejected valid arguments", op)
				}
				if used := work - before; used > bound {
					t.Fatalf("%s used %d work, pre-mutation bound %d", op, used, bound)
				}
			}
			for step := 0; step < 1000; step++ {
				n := len(model)
				at := rng.Intn(n + 1)
				if step%3 == 0 && n > 1 {
					at = 1 // repeatedly exhaust the same label gap
				}
				switch rng.Intn(13) {
				case 0, 1:
					var h *Handle[int]
					anchor := valueAt(model, at)
					if step%2 == 0 {
						measure("InsertBefore", seq.InsertWorkBound(), func() bool {
							h = seq.InsertBefore(anchor, step%3)
							return h != nil
						})
					} else {
						anchor = valueAt(model, at-1)
						measure("InsertAfter", seq.InsertWorkBound(), func() bool {
							h = seq.InsertAfter(anchor, step%3)
							return h != nil
						})
					}
					model = slices.Insert(model, at, h)
				case 2:
					if n == 0 {
						continue
					}
					at %= n
					measure("Remove", seq.RemoveWorkBound(), func() bool { return seq.Remove(model[at]) })
					model = slices.Delete(model, at, at+1)
				case 3, 4:
					deleted := rng.Intn(n - at + 1)
					values := make([]int, rng.Intn(65))
					var inserted []*Handle[int]
					if step%2 == 0 {
						measure("Splice", seq.SpliceWorkBound(deleted, len(values)), func() bool {
							var ok bool
							inserted, ok = seq.Splice(at, deleted, values)
							return ok
						})
					} else {
						measure("ReplaceRange", seq.ReplaceRangeWorkBound(deleted, len(values)), func() bool {
							var ok bool
							inserted, ok = seq.ReplaceRange(at, at+deleted, values)
							return ok
						})
					}
					model = slices.Replace(model, at, at+deleted, inserted...)
				case 5, 6:
					hi := at + rng.Intn(n-at+1)
					before := rng.Intn(n + 1)
					if step%2 == 0 || at == n {
						measure("MoveRange", seq.MoveWorkBound(hi-at), func() bool { return seq.MoveRange(at, hi, before) })
					} else {
						hi = at + 1
						measure("MoveBefore", seq.MoveWorkBound(1), func() bool { return seq.MoveBefore(model[at], valueAt(model, before)) })
					}
					if before < at || before > hi {
						run := slices.Clone(model[at:hi])
						model = slices.Delete(model, at, hi)
						if before > hi {
							before -= hi - at
						}
						model = slices.Insert(model, before, run...)
					}
				case 7:
					hi := at + rng.Intn(n-at+1)
					reordered := slices.Clone(model[at:hi])
					rng.Shuffle(len(reordered), func(i, j int) { reordered[i], reordered[j] = reordered[j], reordered[i] })
					measure("ReplaceHandles", seq.ReplaceHandlesWorkBound(hi-at), func() bool { return seq.ReplaceHandles(at, hi, reordered) })
					copy(model[at:hi], reordered)
				case 8:
					if len(tokens) < 4 {
						measure("Checkpoint", seq.CheckpointWorkBound(), func() bool {
							tokens = append(tokens, seq.Checkpoint())
							return true
						})
						saved = append(saved, slices.Clone(model))
					}
				case 9, 10:
					if last := len(tokens) - 1; last >= 0 {
						token := tokens[last]
						if step%2 == 0 {
							measure("Commit", seq.CommitWorkBound(), func() bool { return seq.Commit(token) })
						} else {
							measure("Rollback", seq.RollbackWorkBound(token), func() bool { return seq.Rollback(token) })
							model = saved[last]
						}
						tokens = slices.Delete(tokens, last, last+1)
						saved = slices.Delete(saved, last, last+1)
					}
				case 11, 12:
					// Constant predicates are monotone even after arbitrary permutations.
					pred := func(int) bool { return step%2 == 0 }
					want := n
					if pred(0) {
						want = at
					}
					measure("LowerBound", seq.LowerBoundWorkBound(), func() bool { return seq.LowerBound(at, pred) == want })
					measure("UpperBound", seq.UpperBoundWorkBound(), func() bool { return seq.UpperBound(at, pred) == want })
				}
				assertSequenceModel(t, &seq, model)
			}
			for last := len(tokens) - 1; last >= 0; last-- {
				token := tokens[last]
				measure("final Rollback", seq.RollbackWorkBound(token), func() bool { return seq.Rollback(token) })
				assertSequenceModel(t, &seq, saved[last])
			}
		})
	}
}

type checkpointPayload [1024]byte

// Keep all strong batch/journal references out of the GC assertion's frame.
//
//go:noinline
func completedSequenceJournal(t *testing.T, seq *Seq[*checkpointPayload], commit bool) (weak.Pointer[seqCheckpoint[*checkpointPayload]], weak.Pointer[Handle[*checkpointPayload]], weak.Pointer[checkpointPayload]) {
	t.Helper()
	values := make([]*checkpointPayload, 1024)
	for i := range values {
		values[i] = new(checkpointPayload)
	}
	var token *Checkpoint[*checkpointPayload]
	if !commit {
		token = seq.Checkpoint()
	}
	handles, ok := seq.Splice(0, 0, values)
	if !ok {
		t.Fatal("batch insertion failed")
	}
	if commit {
		token = seq.Checkpoint()
		if _, ok := seq.Splice(0, seq.Len(), nil); !ok {
			t.Fatal("batch removal failed")
		}
	}
	journal := weak.Make(seq.checkpoints[len(seq.checkpoints)-1])
	handle, value := weak.Make(handles[0]), weak.Make(values[0])
	if commit {
		if !seq.Commit(token) {
			t.Fatal("commit failed")
		}
	} else if !seq.Rollback(token) {
		t.Fatal("rollback failed")
	}
	return journal, handle, value
}

func TestSequenceCompletedJournalsCollectible(t *testing.T) {
	for _, commit := range []bool{false, true} {
		for _, nested := range []bool{false, true} {
			t.Run(fmt.Sprintf("commit=%t/nested=%t", commit, nested), func(t *testing.T) {
				seq := new(Seq[*checkpointPayload])
				var outer *Checkpoint[*checkpointPayload]
				if nested {
					outer = seq.Checkpoint()
				}
				journal, handle, value := completedSequenceJournal(t, seq, commit)
				runtime.GC()
				if journal.Value() != nil {
					t.Error("completed journal remains reachable")
				}
				if nested {
					if handle.Value() == nil || value.Value() == nil {
						t.Fatal("active outer journal lost its created handles/values")
					}
					if !seq.Rollback(outer) {
						t.Fatal("outer rollback failed")
					}
				}
				runtime.GC()
				if handle.Value() != nil || value.Value() != nil {
					t.Error("completed journals retain removed batch handles/values")
				}
				if seq.Len() != 0 {
					t.Fatalf("Len() = %d, want 0", seq.Len())
				}
				if err := seq.CheckInvariants(); err != nil {
					t.Fatal(err)
				}
				runtime.KeepAlive(seq) // its checkpoint backing array must remain live
			})
		}
	}
}
