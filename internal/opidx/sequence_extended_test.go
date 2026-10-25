// SPDX-License-Identifier: BSD-3-Clause

package opidx

import (
	"math"
	"math/rand"
	"slices"
	"testing"
)

func assertSequenceModel(t *testing.T, seq *Seq[int], model []*Handle[int]) {
	t.Helper()
	if err := seq.CheckInvariants(); err != nil {
		t.Fatalf("sequence invariant: %v", err)
	}
	if seq.Len() != len(model) {
		t.Fatalf("Len() = %d, want %d", seq.Len(), len(model))
	}
	forward, backward := seq.First(), seq.Last()
	var previous *Handle[int]
	for i, want := range model {
		if forward != want || backward != model[len(model)-1-i] {
			t.Fatalf("thread order differs from model at position %d", i)
		}
		forward, backward = seq.Next(forward), seq.Prev(backward)
		if got := seq.Select(i); got != want {
			t.Fatalf("Select(%d) = %p, want %p", i, got, want)
		}
		if rank, ok := seq.Rank(want); !ok || rank != i {
			t.Fatalf("Rank(model[%d]) = %d, %t", i, rank, ok)
		}
		if order, ok := seq.Compare(want, want); !ok || order != 0 {
			t.Fatalf("Compare(model[%d], itself) = %d, %t", i, order, ok)
		}
		if previous != nil {
			if order, ok := seq.Compare(previous, want); !ok || order != -1 {
				t.Fatalf("forward comparison differs at position %d", i)
			}
			if order, ok := seq.Compare(want, previous); !ok || order != 1 {
				t.Fatalf("reverse comparison differs at position %d", i)
			}
		}
		previous = want
	}
	if forward != nil || backward != nil {
		t.Fatal("thread traversal continues beyond model")
	}
}

func TestSequenceBoundsTreeNative(t *testing.T) {
	const count = 1 << 16
	values := make([]int, count)
	for i := range values {
		values[i] = i / 4
	}
	var seq Seq[int]
	if _, ok := seq.Splice(0, 0, values); !ok {
		t.Fatal("initial splice failed")
	}
	var work uint64
	seq.SetWorkHook(func(amount uint64) { work += amount })
	for _, test := range []struct {
		lo, key, lower, upper int
	}{
		{0, 0, 0, 4},
		{3, 0, 3, 4},
		{count / 3, count / 8, count / 2, count/2 + 4},
		{count - 2, count / 4, count, count},
	} {
		before := work
		if got := seq.LowerBound(test.lo, func(value int) bool { return value >= test.key }); got != test.lower {
			t.Fatalf("LowerBound(%d, %d) = %d, want %d", test.lo, test.key, got, test.lower)
		}
		if used := work - before; used > seq.LowerBoundWorkBound() {
			t.Fatalf("LowerBound used %d work, bound %d", used, seq.LowerBoundWorkBound())
		}
		before = work
		if got := seq.UpperBound(test.lo, func(value int) bool { return value > test.key }); got != test.upper {
			t.Fatalf("UpperBound(%d, %d) = %d, want %d", test.lo, test.key, got, test.upper)
		}
		if used := work - before; used > seq.UpperBoundWorkBound() {
			t.Fatalf("UpperBound used %d work, bound %d", used, seq.UpperBoundWorkBound())
		}
	}
}

func TestSequenceSpliceAndReplaceRange(t *testing.T) {
	var seq Seq[int]
	original, ok := seq.Splice(0, 0, []int{0, 1, 2, 3, 4, 5, 6})
	if !ok {
		t.Fatal("initial splice failed")
	}
	inserted, ok := seq.Splice(2, 3, []int{20, 21, 22, 23})
	if !ok {
		t.Fatal("splice failed")
	}
	want := []*Handle[int]{original[0], original[1], inserted[0], inserted[1], inserted[2], inserted[3], original[5], original[6]}
	assertSequenceModel(t, &seq, want)
	for _, removed := range original[2:5] {
		if _, live := seq.Rank(removed); live {
			t.Fatal("deleted splice handle is still live")
		}
	}
	replacement, ok := seq.ReplaceRange(1, 7, []int{30, 31})
	if !ok {
		t.Fatal("replace failed")
	}
	want = []*Handle[int]{original[0], replacement[0], replacement[1], original[6]}
	assertSequenceModel(t, &seq, want)
	if _, ok := seq.Splice(-1, 0, nil); ok || func() bool { _, valid := seq.ReplaceRange(3, 2, nil); return valid }() {
		t.Fatal("invalid range was accepted")
	}
	assertSequenceModel(t, &seq, want)
}

func TestSequenceMoveRangePreservesHandles(t *testing.T) {
	var seq Seq[int]
	handles, _ := seq.Splice(0, 0, []int{0, 1, 2, 3, 4, 5, 6, 7})
	if !seq.MoveRange(2, 5, 7) {
		t.Fatal("move right failed")
	}
	want := []*Handle[int]{handles[0], handles[1], handles[5], handles[6], handles[2], handles[3], handles[4], handles[7]}
	assertSequenceModel(t, &seq, want)
	if !seq.MoveRange(4, 7, 1) {
		t.Fatal("move left failed")
	}
	want = []*Handle[int]{handles[0], handles[2], handles[3], handles[4], handles[1], handles[5], handles[6], handles[7]}
	assertSequenceModel(t, &seq, want)
	if seq.MoveRange(-1, 1, 0) {
		t.Fatal("invalid move was accepted")
	}
}

func TestSequenceReplaceHandles(t *testing.T) {
	var seq Seq[int]
	handles, _ := seq.Splice(0, 0, []int{0, 1, 2, 3, 4, 5, 6})
	reordered := []*Handle[int]{handles[4], handles[2], handles[3], handles[1]}
	if !seq.ReplaceHandles(1, 5, reordered) {
		t.Fatal("replace handles failed")
	}
	want := []*Handle[int]{handles[0], handles[4], handles[2], handles[3], handles[1], handles[5], handles[6]}
	assertSequenceModel(t, &seq, want)
	if seq.ReplaceHandles(1, 5, []*Handle[int]{handles[1], handles[1], handles[2], handles[3]}) {
		t.Fatal("duplicate handle was accepted")
	}
	assertSequenceModel(t, &seq, want)
}

func TestSequenceCheckpointRollbackAndCommit(t *testing.T) {
	var seq Seq[int]
	original, _ := seq.Splice(0, 0, []int{0, 1, 2, 3, 4, 5})
	tags := make([]uint64, len(original))
	for i, h := range original {
		tags[i] = h.tag
	}
	outer := seq.Checkpoint()
	created, _ := seq.Splice(2, 2, []int{20, 21, 22})
	if !seq.MoveRange(0, 2, seq.Len()) {
		t.Fatal("move failed")
	}
	inner := seq.Checkpoint()
	innerHandle := seq.InsertBefore(seq.First(), 99)
	if !seq.Commit(inner) || seq.Rollback(inner) {
		t.Fatal("inner checkpoint completion status differs")
	}
	if !seq.Rollback(outer) || seq.Commit(outer) {
		t.Fatal("outer checkpoint completion status differs")
	}
	assertSequenceModel(t, &seq, original)
	for i, h := range original {
		if h.tag != tags[i] {
			t.Fatalf("handle %d label = %d, want exact before-image %d", i, h.tag, tags[i])
		}
	}
	for _, h := range append(created, innerHandle) {
		if h.owner != nil || h.left != nil || h.right != nil || h.parent != nil || h.prev != nil || h.next != nil {
			t.Fatal("new handle was not detached by rollback")
		}
	}
}

func TestSequenceCheckpointConstantWork(t *testing.T) {
	for _, count := range []int{1 << 10, 64 << 10} {
		var seq Seq[int]
		if _, ok := seq.Splice(0, 0, make([]int, count)); !ok {
			t.Fatal("initial build failed")
		}
		var work uint64
		seq.SetWorkHook(func(amount uint64) { work += amount })
		bound := seq.CheckpointWorkBound()
		checkpoint := seq.Checkpoint()
		if work != 1 || work > bound {
			t.Fatalf("count=%d checkpoint work=%d bound=%d", count, work, bound)
		}
		before := work
		if !seq.Commit(checkpoint) || work-before > seq.CommitWorkBound() {
			t.Fatalf("count=%d commit work=%d bound=%d", count, work-before, seq.CommitWorkBound())
		}
	}
}

func TestSequenceRandomizedTransactions(t *testing.T) {
	for seed := int64(0); seed < 24; seed++ {
		rng := rand.New(rand.NewSource(seed)) //nolint:gosec // deterministic structural test
		var seq Seq[int]
		var model []*Handle[int]
		var checkpoint *Checkpoint[int]
		var saved []*Handle[int]
		for step := 0; step < 500; step++ {
			switch rng.Intn(8) {
			case 0:
				at := rng.Intn(len(model) + 1)
				h := seq.InsertBefore(valueAt(model, at), step)
				model = slices.Insert(model, at, h)
			case 1:
				if len(model) != 0 {
					at := rng.Intn(len(model))
					if !seq.Remove(model[at]) {
						t.Fatalf("seed %d step %d: remove failed", seed, step)
					}
					model = slices.Delete(model, at, at+1)
				}
			case 2:
				if len(model) > 1 {
					lo := rng.Intn(len(model))
					hi := lo + 1 + rng.Intn(len(model)-lo)
					before := rng.Intn(len(model) + 1)
					if !seq.MoveRange(lo, hi, before) {
						t.Fatalf("seed %d step %d: move failed", seed, step)
					}
					if before < lo || before > hi {
						run := slices.Clone(model[lo:hi])
						model = slices.Delete(model, lo, hi)
						if before > hi {
							before -= hi - lo
						}
						model = slices.Insert(model, before, run...)
					}
				}
			case 3:
				lo := rng.Intn(len(model) + 1)
				deleted := rng.Intn(len(model) - lo + 1)
				values := make([]int, rng.Intn(5))
				for i := range values {
					values[i] = step*10 + i
				}
				inserted, ok := seq.Splice(lo, deleted, values)
				if !ok {
					t.Fatalf("seed %d step %d: splice failed", seed, step)
				}
				model = slices.Replace(model, lo, lo+deleted, inserted...)
			case 4:
				if checkpoint == nil {
					checkpoint = seq.Checkpoint()
					saved = slices.Clone(model)
				}
			case 5:
				if checkpoint != nil {
					if !seq.Rollback(checkpoint) {
						t.Fatalf("seed %d step %d: rollback failed", seed, step)
					}
					model, checkpoint = saved, nil
				}
			case 6:
				if checkpoint != nil {
					if !seq.Commit(checkpoint) {
						t.Fatalf("seed %d step %d: commit failed", seed, step)
					}
					checkpoint = nil
				}
			case 7:
				if len(model) > 1 {
					lo := rng.Intn(len(model) - 1)
					hi := lo + 2 + rng.Intn(len(model)-lo-1)
					reordered := slices.Clone(model[lo:hi])
					rng.Shuffle(len(reordered), func(i, j int) { reordered[i], reordered[j] = reordered[j], reordered[i] })
					if !seq.ReplaceHandles(lo, hi, reordered) {
						t.Fatalf("seed %d step %d: replace handles failed", seed, step)
					}
					copy(model[lo:hi], reordered)
				}
			}
			assertSequenceModel(t, &seq, model)
		}
		if checkpoint != nil {
			if !seq.Rollback(checkpoint) {
				t.Fatalf("seed %d: final rollback failed", seed)
			}
			assertSequenceModel(t, &seq, saved)
		}
	}
}

func TestSequenceCountedMiddleInsertScaling(t *testing.T) {
	var priorAverage float64
	for _, initial := range []int{1 << 10, 8 << 10, 64 << 10} {
		values := make([]int, initial)
		var seq Seq[int]
		if _, ok := seq.Splice(0, 0, values); !ok {
			t.Fatal("initial build failed")
		}
		var work uint64
		seq.SetWorkHook(func(amount uint64) { work += amount })
		const operations = 512
		for i := 0; i < operations; i++ {
			var at int
			switch i % 6 {
			case 0:
				at = seq.Len() / 2
			case 1:
				at = seq.Len() / 4
			case 2:
				at = 3 * seq.Len() / 4
			case 3:
				at = seq.Len()/2 + i%17
			case 4:
				at = 0
			default:
				at = seq.Len()
			}
			before := work
			bound := seq.InsertWorkBound()
			if seq.InsertBefore(seq.Select(at), i) == nil {
				t.Fatal("insert failed")
			}
			if used := work - before; used > bound {
				t.Fatalf("initial=%d operation=%d work=%d bound=%d", initial, i, used, bound)
			}
		}
		if err := seq.CheckInvariants(); err != nil {
			t.Fatalf("initial=%d: %v", initial, err)
		}
		average := float64(work) / operations
		limit := 80 * math.Log2(float64(seq.Len())+1)
		if average > limit {
			t.Fatalf("initial=%d average work %.1f, logarithmic limit %.1f", initial, average, limit)
		}
		if priorAverage != 0 && average > priorAverage*2.2 {
			t.Fatalf("initial=%d average work grew from %.1f to %.1f", initial, priorAverage, average)
		}
		priorAverage = average
	}
}

func TestSequenceMutationWorkBounds(t *testing.T) {
	var seq Seq[int]
	handles, _ := seq.Splice(0, 0, make([]int, 2048))
	var work uint64
	seq.SetWorkHook(func(amount uint64) { work += amount })

	bound := seq.SpliceWorkBound(200, 300)
	before := work
	inserted, ok := seq.Splice(700, 200, make([]int, 300))
	if !ok || work-before > bound {
		t.Fatalf("splice work=%d bound=%d ok=%t", work-before, bound, ok)
	}

	reordered := slices.Clone(inserted[:128])
	slices.Reverse(reordered)
	bound = seq.ReplaceHandlesWorkBound(len(reordered))
	before = work
	if !seq.ReplaceHandles(700, 700+len(reordered), reordered) || work-before > bound {
		t.Fatalf("replace handles work=%d bound=%d", work-before, bound)
	}

	bound = seq.MoveWorkBound(250)
	before = work
	if !seq.MoveRange(500, 750, 1600) || work-before > bound {
		t.Fatalf("move work=%d bound=%d", work-before, bound)
	}

	bound = seq.RemoveWorkBound()
	before = work
	if !seq.Remove(handles[0]) || work-before > bound {
		t.Fatalf("remove work=%d bound=%d", work-before, bound)
	}

	checkpoint := seq.Checkpoint()
	bound = seq.InsertWorkBound()
	before = work
	created := seq.InsertBefore(seq.Select(seq.Len()/2), 1)
	if created == nil || work-before > bound {
		t.Fatalf("journalled insert work=%d bound=%d", work-before, bound)
	}
	bound = seq.RollbackWorkBound(checkpoint)
	before = work
	if !seq.Rollback(checkpoint) || work-before > bound {
		t.Fatalf("rollback work=%d bound=%d", work-before, bound)
	}
	if err := seq.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestSequenceSpliceLabelGapExhaustion(t *testing.T) {
	var seq Seq[int]
	handles, _ := seq.Splice(0, 0, []int{0, 1, 2, 3})
	handles[1].tag = handles[0].tag + 1
	inserted, ok := seq.Splice(1, 0, []int{10, 11, 12, 13})
	if !ok {
		t.Fatal("splice failed")
	}
	want := slices.Concat(handles[:1], inserted, handles[1:])
	assertSequenceModel(t, &seq, want)
}

func TestSequenceMoveRangeLabelGapExhaustion(t *testing.T) {
	const initial = 4096
	var seq Seq[int]
	handles, _ := seq.Splice(0, 0, make([]int, initial))
	left, right := handles[initial/2-1], handles[initial/2]
	const boundary = tagUniverse / 2
	if left.prev.tag >= boundary-1 || right.next.tag <= boundary {
		t.Fatal("setup does not bracket the half-universe boundary")
	}
	left.tag, right.tag = boundary-1, boundary

	var work uint64
	seq.SetWorkHook(func(amount uint64) { work += amount })
	const moved = 8
	if !seq.MoveRange(0, moved, initial/2) {
		t.Fatal("move failed")
	}
	if work > 1000 {
		t.Fatalf("gap-exhausting range move cost %d counted operations", work)
	}
	seq.SetWorkHook(nil)
	want := slices.Concat(handles[moved:initial/2], handles[:moved], handles[initial/2:])
	assertSequenceModel(t, &seq, want)
}
