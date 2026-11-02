// SPDX-License-Identifier: BSD-3-Clause

package opidx

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"testing"
)

// checkKinds derives both subsequences from the independent edit model, never from Seq reads.
func checkKinds(t *testing.T, seq *Seq[int], model []*Handle[int]) {
	t.Helper()
	assertSequenceModel(t, seq, model)
	var kinds [2][]*Handle[int]
	for _, h := range model {
		if h.Heavy() != (h.Value%2 == 0) {
			t.Fatalf("handle %d has the wrong kind", h.Value)
		}
		k := 0
		if h.Value%2 == 0 {
			k = 1
		}
		kinds[k] = append(kinds[k], h)
	}
	if seq.HeavyLen() != len(kinds[1]) {
		t.Fatalf("HeavyLen %d, want %d", seq.HeavyLen(), len(kinds[1]))
	}
	for k, list := range kinds {
		heavy := k == 1
		for _, rank := range []int{-1, len(list)} {
			if got, levels := seq.PeekKind(rank, heavy); got != nil || levels != 0 {
				t.Fatal("PeekKind outside range")
			}
		}
		first, last := valueAtHandle(list, 0, nil), valueAtHandle(list, len(list)-1, nil)
		if seq.FirstKind(heavy) != first || seq.LastKind(heavy) != last {
			t.Fatal("FirstKind/LastKind")
		}
		for i, h := range list {
			if got, levels := seq.PeekKind(i, heavy); got != h || levels < 1 || uint64(levels) > seq.SelectBound() {
				t.Fatalf("PeekKind(%d, %v)", i, heavy)
			}
			if pos, ok := seq.RankKind(h); !ok || pos != i {
				t.Fatalf("RankKind of %d: %d, want %d", h.Value, pos, i)
			}
			if pos, ok := seq.PosKind(h); !ok || pos != i {
				t.Fatalf("PosKind of %d: %d, want %d", h.Value, pos, i)
			}
			next, prev := valueAtHandle(list, i+1, nil), valueAtHandle(list, i-1, nil)
			if seq.NextKind(h) != next || seq.PrevKind(h) != prev {
				t.Fatalf("NextKind/PrevKind of %d", h.Value)
			}
		}
		// Cross-kind neighbours must exclude h itself, in both directions.
		for i, h := range model {
			var prev, next *Handle[int]
			for j := i - 1; j >= 0; j-- {
				if (model[j].Value%2 == 0) == heavy {
					prev = model[j]
					break
				}
			}
			for j := i + 1; j < len(model); j++ {
				if (model[j].Value%2 == 0) == heavy {
					next = model[j]
					break
				}
			}
			if seq.PeekPrevOfKind(h, heavy) != prev || seq.PeekNextOfKind(h, heavy) != next {
				t.Fatalf("cross-kind neighbours of %d, heavy=%t", h.Value, heavy)
			}
		}
	}
}

// TestSequenceKindModel extends the reference model to every edit entry point, both label
// modes, both journals and nested rollback/commit. Expected order is edited only as a slice.
func TestSequenceKindModel(t *testing.T) {
	for _, mode := range []string{"plain", "journal", "checkpoint"} {
		for seed := range uint64(16) {
			t.Run(fmt.Sprintf("%s/%d", mode, seed), func(t *testing.T) {
				r := rand.New(rand.NewPCG(seed, 15)) //nolint:gosec // deterministic model
				var j Journal
				hooks := &meterHooks{}
				if mode == "journal" {
					hooks.j = &j
				}
				var seq Seq[int]
				seq.SetHooks(hooks)
				seq.SetWeigh(func(v int) bool { return v%2 == 0 })
				seq.SetLabeled(seed%2 == 0)
				if seed >= 8 {
					seq.SetKindThreads()
				}
				var model, all []*Handle[int]
				next := 0
				freshValues := func(n int) []int {
					values := make([]int, n)
					for i := range values {
						values[i] = next
						next++
					}
					return values
				}
				check := func() {
					t.Helper()
					checkKinds(t, &seq, model)
					live := make(map[*Handle[int]]bool, len(model))
					for _, h := range model {
						live[h] = true
					}
					for _, h := range all {
						if seq.Owns(h) != live[h] {
							t.Fatal("handle ownership differs from slice model")
						}
						if !live[h] && (h.kprev != nil || h.knext != nil || h.hsize != 0 || h.owner != nil || h.left != nil || h.right != nil || h.parent != nil || h.prev != nil || h.next != nil) {
							t.Fatal("detached handle retains links or counts")
						}
					}
				}
				for batch := range 20 {
					outerModel, outerKinds := slices.Clone(model), seq.KindThreads()
					outerMark := j.Mark()
					var outer *Checkpoint[int]
					if mode == "checkpoint" {
						outer = seq.Checkpoint()
					}
					for step := range 20 {
						before, beforeKinds := slices.Clone(model), seq.KindThreads()
						mark := j.Mark()
						var cp *Checkpoint[int]
						if mode == "checkpoint" {
							cp = seq.Checkpoint()
						}
						n := len(model)
						at := r.IntN(n + 1)
						lo := r.IntN(n + 1)
						hi := lo + r.IntN(min(8, n-lo)+1)
						op := (batch*20 + step) % 12 // every path runs for every seed
						bound := ^uint64(0)
						// Point bounds pay external journal images. Seq checkpoints charge their
						// own captures, at most one per old or fresh handle per active level.
						captures := uint64(len(seq.checkpoints) * (n + 3))
						hooks.units = 0
						switch {
						case op == 0 || n == 0 && op != 10 && op != 11:
							values := freshValues(1)
							anchor := valueAtHandle(model, at, nil)
							bound = seq.InsertBound(anchor, mode == "journal", nil)
							h := seq.InsertBefore(anchor, values[0])
							if h == nil {
								t.Fatal("InsertBefore refused")
							}
							all = append(all, h)
							model = slices.Insert(model, at, h)
						case op == 1:
							values := freshValues(1)
							h := seq.InsertAfter(valueAtHandle(model, at-1, nil), values[0])
							if h == nil {
								t.Fatal("InsertAfter refused")
							}
							all = append(all, h)
							model = slices.Insert(model, at, h)
						case op == 2:
							at = r.IntN(n)
							bound = seq.RemoveBound(mode == "journal")
							if !seq.Remove(model[at]) {
								t.Fatal("Remove refused")
							}
							model = slices.Delete(model, at, at+1)
						case op == 3 || op == 4:
							values := freshValues(r.IntN(4))
							bound = seq.SpliceWorkBound(hi-lo, len(values))
							var hs []*Handle[int]
							var ok bool
							if op == 3 {
								hs, ok = seq.Splice(lo, hi-lo, values)
							} else {
								hs, ok = seq.ReplaceRange(lo, hi, values)
							}
							if !ok {
								t.Fatal("splice/replace refused")
							}
							all = append(all, hs...)
							model = slices.Replace(model, lo, hi, hs...)
						case op == 5:
							values := make([]int, hi-lo)
							byValue := make(map[int]*Handle[int], hi-lo)
							for i, h := range model[lo:hi] {
								values[i] = h.Value
								byValue[h.Value] = h
							}
							values = append(values, freshValues(r.IntN(3))...)
							r.Shuffle(len(values), func(a, b int) { values[a], values[b] = values[b], values[a] })
							bound = seq.RepublishBound(lo, hi, len(values)-(hi-lo), mode == "journal", nil)
							hs, ok := seq.Republish(lo, hi, values, func(v int) *Handle[int] { return byValue[v] })
							if !ok {
								t.Fatal("Republish refused")
							}
							for i, v := range values {
								if old := byValue[v]; old != nil {
									if hs[i] != old {
										t.Fatal("Republish lost identity")
									}
								} else {
									all = append(all, hs[i])
								}
								if hs[i].Value != v {
									t.Fatal("Republish returned wrong value")
								}
							}
							model = slices.Replace(model, lo, hi, hs...)
						case op == 6:
							drop := r.IntN(5)
							bound = seq.KeepBound(mode == "journal")
							gone := seq.Keep(func(v int) bool { return v%5 != drop })
							var wantGone []*Handle[int]
							for _, h := range model {
								if h.Value%5 == drop {
									wantGone = append(wantGone, h)
								}
							}
							if !slices.Equal(gone, wantGone) {
								t.Fatal("Keep returned wrong handles")
							}
							model = slices.DeleteFunc(model, func(h *Handle[int]) bool { return h.Value%5 == drop })
						case op == 7 || op == 8:
							if op == 8 {
								lo = r.IntN(n)
								hi = lo + 1
							}
							bound = seq.MoveWorkBound(hi - lo)
							if op == 7 {
								if !seq.MoveRange(lo, hi, at) {
									t.Fatal("MoveRange refused")
								}
							} else {
								bound = seq.MoveBound(model[lo], valueAtHandle(model, at, nil), mode == "journal", nil)
								if !seq.MoveBefore(model[lo], valueAtHandle(model, at, nil)) {
									t.Fatal("MoveBefore refused")
								}
							}
							if lo != hi && (at < lo || at > hi) {
								run := slices.Clone(model[lo:hi])
								model = slices.Delete(model, lo, hi)
								if at > hi {
									at -= hi - lo
								}
								model = slices.Insert(model, at, run...)
							}
						case op == 9:
							hs := slices.Clone(model[lo:hi])
							r.Shuffle(len(hs), func(a, b int) { hs[a], hs[b] = hs[b], hs[a] })
							bound = seq.ReplaceHandlesBound(valueAtHandle(model, lo, nil), valueAtHandle(model, hi-1, nil), len(hs), mode == "journal", nil)
							if !seq.ReplaceHandles(lo, hi, hs) {
								t.Fatal("ReplaceHandles refused")
							}
							model = slices.Replace(model, lo, hi, hs...)
						case op == 10:
							bound = seq.KindThreadsBound(mode == "journal")
							seq.SetKindThreads()
						case op == 11:
							// Rejected requests must leave counts, threads and journal state untouched.
							entries := j.Len()
							if seq.ReplaceHandles(0, n+1, nil) || seq.MoveRange(-1, 0, 0) || seq.Remove(nil) {
								t.Fatal("invalid edit accepted")
							}
							if _, ok := seq.Republish(0, n+1, nil, func(int) *Handle[int] { return nil }); ok {
								t.Fatal("invalid republication accepted")
							}
							if j.Len() != entries {
								t.Fatal("rejected edit recorded writes")
							}
						}
						if op != 10 { // KindThreadsBound already includes checkpoint captures.
							bound = saturatingAdd(bound, captures)
						}
						if hooks.units > bound {
							t.Fatalf("batch %d step %d op %d: used %d units, bound %d", batch, step, op, hooks.units, bound)
						}
						check()
						rollback := r.IntN(4) == 0
						if mode == "journal" && rollback {
							j.Rollback(mark)
						}
						if mode == "checkpoint" {
							if rollback {
								if !seq.Rollback(cp) {
									t.Fatal("checkpoint rollback refused")
								}
							} else if !seq.Commit(cp) {
								t.Fatal("checkpoint commit refused")
							}
						}
						if mode != "plain" && rollback {
							model = before
							if seq.KindThreads() != beforeKinds {
								t.Fatal("rollback lost thread flag")
							}
							check()
						}
					}
					rollback := batch%2 == 0
					if mode == "journal" {
						if rollback {
							j.Rollback(outerMark)
						}
						j.Commit()
					}
					if mode == "checkpoint" {
						if rollback {
							if !seq.Rollback(outer) {
								t.Fatal("outer rollback refused")
							}
						} else if !seq.Commit(outer) {
							t.Fatal("outer commit refused")
						}
					}
					if mode != "plain" && rollback {
						model = outerModel
						if seq.KindThreads() != outerKinds {
							t.Fatal("outer rollback lost thread flag")
						}
					}
					check()
				}
			})
		}
	}
}

// A kind search may climb only one level yet descend the full opposite subtree. Every visited
// descent node must be paid, in both directions; enabling threads makes the same reads O(1).
func TestSequenceKindSparseSearchWork(t *testing.T) {
	for _, forward := range []bool{false, true} {
		var seq Seq[int]
		seq.SetWeigh(func(v int) bool { return v%2 == 0 })
		values := make([]int, 1023)
		for i := range values {
			values[i] = 1
		}
		values[511] = 0
		if forward {
			values[1022] = 2
		} else {
			values[0] = 2
		}
		hs, ok := seq.Splice(0, 0, values)
		if !ok {
			t.Fatal("initial splice refused")
		}
		var work uint64
		seq.SetWorkHook(func(n uint64) { work += n })
		read := func() *Handle[int] {
			if forward {
				return seq.NextKind(hs[511])
			}
			return seq.PrevKind(hs[511])
		}
		if got := read(); got == nil || got.Value != 2 {
			t.Fatal("sparse neighbour missing")
		}
		if work < 9 || work > 2*seq.SelectBound()+2 {
			t.Fatalf("forward=%t: descent work=%d, want [9, %d]", forward, work, 2*seq.SelectBound()+2)
		}
		t.Logf("forward=%t sparse read work=%d", forward, work)
		seq.SetKindThreads()
		work = 0
		if got := read(); got == nil || got.Value != 2 || work != 0 {
			t.Fatal("threaded neighbour read must use no tree visits")
		}
	}
}

func TestSequenceKindImagesAndInvalidReads(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	var seq, foreign Seq[int]
	seq.SetWeigh(func(v int) bool { return v%2 == 0 })
	var j Journal
	hooks := &meterHooks{j: &j}
	seq.SetHooks(hooks)
	hs, ok := seq.Splice(0, 0, []int{0, 1, 2, 3, 4})
	if !ok {
		t.Fatal("initial splice refused")
	}
	j.Commit()
	before := make([]Handle[int], len(hs))
	for i, h := range hs {
		before[i] = *h
	}
	mark := j.Mark()
	seq.SetKindThreads()
	if !seq.Remove(hs[2]) {
		t.Fatal("remove refused")
	}
	fresh := seq.InsertBefore(hs[1], 8)
	if fresh == nil {
		t.Fatal("insertion refused")
	}
	// Rollback must not allocate, and must clear the discarded entry slots.
	runtime.GC()
	beforeAllocs := mallocs()
	j.Rollback(mark)
	if allocs := mallocs() - beforeAllocs; allocs != 0 {
		t.Fatalf("rollback allocations=%d", allocs)
	}
	if seq.KindThreads() {
		t.Fatal("thread initialisation survived rollback")
	}
	for i, h := range hs {
		// The epoch stamp is bookkeeping; all sequence state is restored exactly.
		before[i].stamp = h.stamp
		if *h != before[i] {
			t.Fatalf("image of handle %d was not restored", i)
		}
	}
	for _, e := range j.entries[:cap(j.entries)] {
		if e.obj != nil || e.key != nil || e.ptr != [6]any{} {
			t.Fatal("discarded journal entries retain objects")
		}
	}
	for _, h := range []*Handle[int]{nil, foreign.InsertBefore(nil, 0), fresh} {
		if _, ok := seq.PosKind(h); ok {
			t.Fatal("PosKind accepted foreign/detached handle")
		}
		if _, ok := seq.RankKind(h); ok {
			t.Fatal("RankKind accepted foreign/detached handle")
		}
		if seq.NextKind(h) != nil || seq.PrevKind(h) != nil || seq.PeekNextOfKind(h, true) != nil || seq.PeekPrevOfKind(h, false) != nil {
			t.Fatal("kind neighbour accepted foreign/detached handle")
		}
	}
	checkKinds(t, &seq, hs)
	// Kind is fixed when created, even if the value later changes.
	hs[0].Value = 9
	if !hs[0].Heavy() || seq.HeavyLen() != 3 {
		t.Fatal("value change altered immutable kind")
	}
}

func TestSequenceKindInvariantRejectsCorruption(t *testing.T) {
	for _, corruption := range []string{"count", "off-thread", "prev", "next", "end"} {
		t.Run(corruption, func(t *testing.T) {
			var seq Seq[int]
			seq.SetWeigh(func(v int) bool { return v%2 == 0 })
			hs, ok := seq.Splice(0, 0, []int{0, 1, 2, 3, 4})
			if !ok {
				t.Fatal("initial splice refused")
			}
			if corruption != "off-thread" {
				seq.SetKindThreads()
			}
			switch corruption {
			case "count":
				seq.root.hsize++
			case "off-thread":
				hs[0].knext = hs[2]
			case "prev":
				hs[2].kprev = hs[1]
			case "next":
				hs[0].knext = hs[4]
			case "end":
				hs[4].knext = hs[0]
			}
			if err := seq.CheckInvariants(); err == nil {
				t.Fatal("kind corruption accepted")
			}
		})
	}
}

func TestSequenceKindBounds(t *testing.T) {
	for _, journaled := range []bool{false, true} {
		for _, labeled := range []bool{false, true} {
			for _, pattern := range []string{"light", "heavy", "alternating", "sparse"} {
				for _, length := range []int{1, 3, 16, 128, 1024} {
					t.Run(fmt.Sprintf("journal=%t/labels=%t/%s/%d", journaled, labeled, pattern, length), func(t *testing.T) {
						var seq Seq[int]
						seq.SetWeigh(func(v int) bool {
							switch pattern {
							case "light":
								return false
							case "heavy":
								return true
							case "alternating":
								return v%2 == 0
							default:
								return v%257 == 0
							}
						})
						seq.SetLabeled(labeled)
						var j Journal
						hooks := &meterHooks{}
						if journaled {
							hooks.j = &j
						}
						seq.SetHooks(hooks)
						values := make([]int, length)
						for i := range values {
							values[i] = i
						}
						if _, ok := seq.Splice(0, 0, values); !ok {
							t.Fatal("initial splice refused")
						}
						check := func(op string, bound uint64, edit func()) {
							t.Helper()
							j.Commit() // force every image to be paid again
							hooks.units = 0
							edit()
							if hooks.units > bound {
								t.Fatalf("%s used %d units, bound %d", op, hooks.units, bound)
							}
							if err := seq.CheckInvariants(); err != nil {
								t.Fatal(err)
							}
						}
						check("threads", seq.KindThreadsBound(journaled), seq.SetKindThreads)
						check("threads again", seq.KindThreadsBound(journaled), seq.SetKindThreads)
						hs := make([]*Handle[int], length)
						for i := range hs {
							hs[i] = seq.Peek(length - 1 - i)
						}
						check("replace", seq.ReplaceHandlesBound(seq.First(), seq.Last(), length, journaled, nil), func() {
							if !seq.ReplaceHandles(0, length, hs) {
								t.Fatal("ReplaceHandles refused")
							}
						})
						slices.Reverse(hs)
						republish := func() {
							i := 0
							if _, ok := seq.Republish(0, length, values, func(int) *Handle[int] { h := hs[i]; i++; return h }); !ok {
								t.Fatal("Republish refused")
							}
						}
						check("republish", seq.RepublishBound(0, length, 0, journaled, nil), republish)
						check("republish any", RepublishBoundAny(length, length, journaled, seq.KindThreads()), republish)
						anchor := seq.Peek(length / 2)
						check("insert", seq.InsertBound(anchor, journaled, nil), func() {
							if seq.InsertBefore(anchor, length) == nil {
								t.Fatal("InsertBefore refused")
							}
						})
						check("insert any", InsertBoundAny(seq.Len(), journaled), func() {
							if seq.InsertBefore(anchor, length+1) == nil {
								t.Fatal("InsertBefore refused")
							}
						})
						check("move", seq.MoveBound(seq.Last(), seq.First(), journaled, nil), func() {
							if !seq.MoveBefore(seq.Last(), seq.First()) {
								t.Fatal("MoveBefore refused")
							}
						})
						check("remove", seq.RemoveBound(journaled), func() {
							if !seq.Remove(seq.Peek(seq.Len() / 2)) {
								t.Fatal("Remove refused")
							}
						})
						check("keep", seq.KeepBound(journaled), func() { seq.Keep(func(v int) bool { return v%3 == 0 }) })
						check("empty", seq.KeepBound(journaled), func() { seq.Keep(func(int) bool { return false }) })
						check("empty threads", seq.KindThreadsBound(journaled), seq.SetKindThreads)
					})
				}
			}
		}
	}
}
