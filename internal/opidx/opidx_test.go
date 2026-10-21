// SPDX-License-Identifier: BSD-3-Clause

package opidx

import (
	"math"
	"math/rand"
	"slices"
	"testing"
)

func checkSequence(t *testing.T, s *Seq[int], want []*Handle[int]) {
	t.Helper()
	if s.Len() != len(want) {
		t.Fatalf("length %d, want %d", s.Len(), len(want))
	}
	if len(want) == 0 {
		if s.First() != nil || s.Last() != nil || s.root != nil {
			t.Fatal("empty sequence retained a node")
		}
		return
	}
	if s.First() != want[0] || s.Last() != want[len(want)-1] {
		t.Fatal("endpoints differ")
	}
	var previous *Handle[int]
	for i, h := range want {
		if s.Select(i) != h || s.Next(h) != valueAt(want, i+1) || s.Prev(h) != previous {
			t.Fatalf("position or links wrong at %d", i)
		}
		if rank, ok := s.Rank(h); !ok || rank != i {
			t.Fatalf("rank of %d: %d, %v", i, rank, ok)
		}
		if previous != nil {
			if order, ok := s.Compare(previous, h); !ok || order != -1 || previous.tag >= h.tag {
				t.Fatalf("order labels wrong at %d", i)
			}
		}
		previous = h
	}
	if s.Select(-1) != nil || s.Select(len(want)) != nil {
		t.Fatal("out-of-range select succeeded")
	}
	var walk func(*Handle[int], *Handle[int]) (int, int)
	walk = func(n, parent *Handle[int]) (int, int) {
		if n == nil {
			return 0, 0
		}
		if n.parent != parent || n.owner != s {
			t.Fatal("bad parent or owner")
		}
		ls, lh := walk(n.left, n)
		rs, rh := walk(n.right, n)
		if n.size != ls+rs+1 || n.height != max(lh, rh)+1 || abs(lh-rh) > 1 {
			t.Fatalf("AVL invariant at %d", n.Value)
		}
		return n.size, n.height
	}
	if size, _ := walk(s.root, nil); size != len(want) {
		t.Fatal("tree size differs")
	}
}

func valueAt[T any](items []*Handle[T], i int) *Handle[T] {
	if i < 0 || i >= len(items) {
		return nil
	}
	return items[i]
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func TestSequenceEdits(t *testing.T) {
	var s Seq[int]
	var want []*Handle[int]
	for i := range 900 {
		at := (i*97 + i/7) % (len(want) + 1)
		h := s.InsertBefore(valueAt(want, at), i)
		want = slices.Insert(want, at, h)
		if i%13 == 0 {
			checkSequence(t, &s, want)
		}
	}
	for len(want) > 0 {
		at := len(want) / 2
		h := want[at]
		if !s.Remove(h) || s.Remove(h) {
			t.Fatal("remove status wrong")
		}
		if _, ok := s.Compare(h, s.First()); ok {
			t.Fatal("removed handle still compares")
		}
		want = slices.Delete(want, at, at+1)
		if len(want)%13 == 0 {
			checkSequence(t, &s, want)
		}
	}
	checkSequence(t, &s, want)
}

func TestSequenceRandomReference(t *testing.T) {
	for seed := int64(0); seed < 64; seed++ {
		rng := rand.New(rand.NewSource(seed)) //nolint:gosec // deterministic reference model
		var s Seq[int]
		var want []*Handle[int]
		for step := range 600 {
			if len(want) > 0 && rng.Intn(4) == 0 {
				at := rng.Intn(len(want))
				if !s.Remove(want[at]) {
					t.Fatalf("seed %d step %d: remove failed", seed, step)
				}
				want = slices.Delete(want, at, at+1)
			} else {
				at := rng.Intn(len(want) + 1)
				h := s.InsertBefore(valueAt(want, at), step)
				want = slices.Insert(want, at, h)
			}
			checkSequence(t, &s, want)
		}
	}
}

func TestSequenceDenseGap(t *testing.T) {
	var s Seq[int]
	left := s.InsertAfter(nil, 0)
	right := s.InsertAfter(left, 1)
	for i := range 3000 {
		h := s.InsertBefore(right, i+2)
		if h == nil || h.tag <= left.tag || h.tag >= right.tag {
			t.Fatalf("tag order after dense insert %d", i)
		}
		left = h
	}
	if s.Len() != 3002 {
		t.Fatal("length wrong")
	}
}

func TestForeignAndRemovedHandles(t *testing.T) {
	var a, b Seq[int]
	h := a.InsertBefore(nil, 1)
	if b.InsertBefore(h, 2) != nil || b.InsertAfter(h, 2) != nil || b.Remove(h) {
		t.Fatal("foreign handle accepted")
	}
	if _, ok := b.Rank(h); ok {
		t.Fatal("foreign rank succeeded")
	}
	if !a.Remove(h) || a.InsertBefore(h, 3) != nil || a.Next(h) != nil {
		t.Fatal("removed handle accepted")
	}
	if b.Len() != 0 || a.Len() != 0 {
		t.Fatal("invalid operation changed a sequence")
	}
}

func TestRanks(t *testing.T) {
	var r Ranks
	var want []int
	for i := range 700 {
		v := (i*331)%1403 - 701
		if !r.Insert(v) || r.Insert(v) {
			t.Fatal("insert status wrong")
		}
		want = append(want, v)
	}
	slices.Sort(want)
	for q := -710; q <= 710; q++ {
		pi, si := slices.IndexFunc(want, func(v int) bool { return v >= q })-1, slices.IndexFunc(want, func(v int) bool { return v > q })
		if pi == -2 {
			pi = len(want) - 1
		}
		p, pok := r.Predecessor(q)
		s, sok := r.Successor(q)
		if pok != (pi >= 0) || pok && p != want[pi] || sok != (si >= 0) || sok && s != want[si] {
			t.Fatalf("neighbors for %d: pred=%d/%v succ=%d/%v", q, p, pok, s, sok)
		}
	}
	for _, v := range want {
		if !r.Remove(v) || r.Remove(v) || r.Contains(v) {
			t.Fatalf("remove %d failed", v)
		}
	}
	if r.Len() != 0 {
		t.Fatal("nonempty after removals")
	}
}

func TestOccurrencesSplitRuns(t *testing.T) {
	var seq Seq[int]
	idx := NewOccurrences[int, int, int](&seq)
	var all []*Handle[int]
	for i := range 500 {
		h := seq.InsertBefore(nil, i)
		all = append(all, h)
		if !idx.Insert(i%3, i%7, h) {
			t.Fatal("insert failed")
		}
	}
	for schema := range 3 {
		for key := range 7 {
			var want []*Handle[int]
			for i, h := range all {
				if i%3 == schema && i%7 == key {
					want = append(want, h)
				}
			}
			if idx.Len(schema, key) != len(want) || idx.First(schema, key) != valueAt(want, 0) || idx.Last(schema, key) != valueAt(want, len(want)-1) {
				t.Fatal("occurrence endpoints or length wrong")
			}
			for i, h := range want {
				if idx.Next(schema, key, h) != valueAt(want, i+1) {
					t.Fatal("next occurrence wrong")
				}
			}
		}
	}
	for i := len(all) - 1; i >= 0; i-- {
		if !idx.Remove(all[i]) || !seq.Remove(all[i]) {
			t.Fatal("remove failed")
		}
	}
	if len(idx.trees) != 0 || len(idx.by) != 0 {
		t.Fatal("empty index retained entries")
	}
}

func TestOccurrenceRejectsInvalidHandles(t *testing.T) {
	var seq, other Seq[int]
	idx := NewOccurrences[int, int, int](&seq)
	h := seq.InsertBefore(nil, 1)
	foreign := other.InsertBefore(nil, 2)
	if idx.Insert(1, 1, foreign) || idx.Insert(1, 1, nil) || !idx.Insert(1, 1, h) || idx.Insert(1, 2, h) {
		t.Fatal("invalid registration status")
	}
	if idx.Next(1, 1, foreign) != nil || idx.Next(1, 1, nil) != h {
		t.Fatal("invalid query status")
	}
	if !idx.Remove(h) || idx.Remove(h) || idx.Len(1, 1) != 0 {
		t.Fatal("invalid removal status")
	}
	if NewOccurrences[int, int, int](nil).Insert(1, 1, h) {
		t.Fatal("nil sequence accepted a handle")
	}
}

func TestOccurrenceRelabels(t *testing.T) {
	var seq Seq[int]
	idx := NewOccurrences[int, int, int](&seq)
	left := seq.InsertAfter(nil, 0)
	right := seq.InsertAfter(left, 1)
	idx.Insert(2, 3, left)
	idx.Insert(2, 3, right)
	var want []*Handle[int]
	want = append(want, left)
	for i := range 2000 {
		h := seq.InsertBefore(right, i+2)
		if !idx.Insert(2, 3, h) {
			t.Fatal("register after relabel failed")
		}
		want = append(want, h)
	}
	want = append(want, right)
	for i, h := range want {
		if idx.Next(2, 3, h) != valueAt(want, i+1) {
			t.Fatalf("relabel changed occurrence order at %d", i)
		}
	}
}

func TestCountedSequencePatterns(t *testing.T) {
	for _, mode := range []string{"append", "prepend", "middle-gap"} {
		t.Run(mode, func(t *testing.T) {
			for _, n := range []int{256, 1024, 4096} {
				var seq Seq[int]
				var work uint64
				seq.SetWorkHook(func(x uint64) { work += x })
				var right *Handle[int]
				if mode == "middle-gap" {
					right = seq.InsertBefore(nil, -1)
				}
				for i := range n {
					switch mode {
					case "append":
						seq.InsertBefore(nil, i)
					case "prepend":
						seq.InsertAfter(nil, i)
					default:
						seq.InsertBefore(right, i)
					}
				}
				for seq.Len() > 0 {
					seq.Remove(seq.Select(seq.Len() / 2))
				}
				bound := 100 * float64(n) * math.Log2(float64(n)+1)
				if float64(work) > bound {
					t.Fatalf("n=%d work=%d bound=%.0f", n, work, bound)
				}
			}
		})
	}
}

func TestCountedRelabelAcrossHalfUniverse(t *testing.T) {
	const initial = 4096
	var seq Seq[int]
	var handles []*Handle[int]
	for i := range initial {
		handles = append(handles, seq.InsertBefore(nil, i))
	}
	left, right := handles[initial/2-1], handles[initial/2]
	const boundary = tagUniverse / 2
	if left.prev.tag >= boundary-1 || right.next.tag <= boundary {
		t.Fatal("setup does not bracket the half-universe boundary")
	}
	left.tag, right.tag = boundary-1, boundary
	var work uint64
	seq.SetWorkHook(func(x uint64) { work += x })
	h := seq.InsertBefore(right, -1)
	if h == nil || left.tag >= h.tag || h.tag >= right.tag {
		t.Fatal("insertion did not preserve order across the boundary")
	}
	// The old both-neighbors rule relabeled all 4097 handles here.
	if work > 1000 {
		t.Fatalf("half-universe insertion cost %d counted operations", work)
	}
	seq.SetWorkHook(nil)
	checkSequence(t, &seq, slices.Insert(handles, initial/2, h))
}

func TestCountedAdversarialInsertionGaps(t *testing.T) {
	var previous uint64
	for _, initial := range []int{512, 2048, 8192} {
		var seq Seq[int]
		var handles []*Handle[int]
		for i := range initial {
			handles = append(handles, seq.InsertBefore(nil, i))
		}
		anchors := [4]*Handle[int]{handles[initial/8], handles[initial/4], handles[initial/2], handles[3*initial/4]}
		var work uint64
		seq.SetWorkHook(func(x uint64) { work += x })
		for i := range 4 * initial {
			// Keep inserting immediately before four fixed successors, so each
			// selected gap repeatedly runs out of labels.
			right := anchors[i%len(anchors)]
			h := seq.InsertBefore(right, i)
			if h == nil || h.prev != nil && h.prev.tag >= h.tag || h.tag >= right.tag {
				t.Fatalf("initial=%d insert=%d: invalid order", initial, i)
			}
		}
		operations := 4 * initial
		bound := 12 * float64(operations) * math.Log2(float64(seq.Len())+1)
		if float64(work) > bound {
			t.Fatalf("initial=%d work=%d bound=%.0f", initial, work, bound)
		}
		if previous != 0 && work > 6*previous {
			t.Fatalf("initial=%d work grew from %d to %d on 4x input", initial, previous, work)
		}
		count := 0
		for h := seq.First(); h != nil; h = seq.Next(h) {
			if h.next != nil && h.tag >= h.next.tag {
				t.Fatalf("initial=%d: labels out of order at %d", initial, count)
			}
			count++
		}
		if count != seq.Len() {
			t.Fatalf("initial=%d: walked %d of %d handles", initial, count, seq.Len())
		}
		previous = work
		t.Logf("initial=%d insertions=%d work=%d", initial, operations, work)
	}
}

func TestCountedScaling(t *testing.T) {
	var previous uint64
	for _, n := range []int{256, 1024, 4096} {
		var seq Seq[int]
		var ranks Ranks
		idx := NewOccurrences[int, int, int](&seq)
		var work uint64
		charge := func(x uint64) { work += x }
		seq.SetWorkHook(charge)
		ranks.SetWorkHook(charge)
		idx.SetWorkHook(charge)
		var hs []*Handle[int]
		for i := range n {
			at := (i * 53) % (len(hs) + 1)
			h := seq.InsertBefore(valueAt(hs, at), i)
			hs = slices.Insert(hs, at, h)
			idx.Insert(i%4, i%9, h)
			ranks.Insert(i * 101)
		}
		for i := n - 1; i >= 0; i-- {
			h := hs[(i*17)%len(hs)]
			idx.Next(i%4, i%9, h)
			ranks.Predecessor(i * 101)
			idx.Remove(h)
			seq.Remove(h)
			ranks.Remove(i * 101)
			hs = slices.DeleteFunc(hs, func(x *Handle[int]) bool { return x == h })
		}
		bound := 180 * float64(n) * math.Log2(float64(n)+1)
		if float64(work) > bound {
			t.Fatalf("n=%d: %d counted visits/rotations/relabels, bound %.0f", n, work, bound)
		}
		if previous != 0 && work > 9*previous {
			t.Fatalf("n=%d: work grew from %d to %d on 4x input", n, previous, work)
		}
		previous = work
		t.Logf("n=%d work=%d work/(n log2 n)=%.1f", n, work, float64(work)/(float64(n)*math.Log2(float64(n))))
	}
}

func TestCountedDuplicateHeavyOccurrences(t *testing.T) {
	var previous uint64
	for _, n := range []int{256, 1024, 4096} {
		var seq Seq[int]
		idx := NewOccurrences[int, int, int](&seq)
		var work uint64
		idx.SetWorkHook(func(x uint64) { work += x })
		var hs []*Handle[int]
		for i := range n {
			h := seq.InsertBefore(nil, i)
			hs = append(hs, h)
			if !idx.Insert(1, 1, h) {
				t.Fatal("insert failed")
			}
		}
		for i, h := range hs {
			if idx.Next(1, 1, h) != valueAt(hs, i+1) {
				t.Fatal("successor differs")
			}
		}
		for i := n - 1; i >= 0; i-- {
			if idx.Next(1, 1, hs[i]) != nil {
				t.Fatal("successor differs")
			}
			if !idx.Remove(hs[i]) || !seq.Remove(hs[i]) {
				t.Fatal("remove failed")
			}
		}
		bound := 30 * float64(n) * math.Log2(float64(n)+1)
		if float64(work) > bound {
			t.Fatalf("n=%d work=%d bound=%.0f", n, work, bound)
		}
		if previous != 0 && work > 9*previous {
			t.Fatalf("n=%d: work grew from %d to %d on 4x input", n, previous, work)
		}
		previous = work
	}
}
