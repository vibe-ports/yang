// SPDX-License-Identifier: BSD-3-Clause

package opidx

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// checkKinds compares every kind read of seq (even values heavy) with the physical order.
func checkKinds(t *testing.T, seq *Seq[int]) {
	t.Helper()
	if err := seq.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	var kinds [2][]*Handle[int]
	for h := seq.First(); h != nil; h = seq.Next(h) {
		if h.Heavy() != (h.Value%2 == 0) {
			t.Fatalf("handle %d has the wrong kind", h.Value)
		}
		k := 0
		if h.Heavy() {
			k = 1
		}
		kinds[k] = append(kinds[k], h)
	}
	if seq.HeavyLen() != len(kinds[1]) {
		t.Fatalf("HeavyLen %d, want %d", seq.HeavyLen(), len(kinds[1]))
	}
	for k, list := range kinds {
		heavy := k == 1
		if got, _ := seq.PeekKind(len(list), heavy); got != nil {
			t.Fatal("PeekKind past the end")
		}
		var first, last *Handle[int]
		if len(list) > 0 {
			first, last = list[0], list[len(list)-1]
		}
		if seq.FirstKind(heavy) != first || seq.LastKind(heavy) != last {
			t.Fatal("FirstKind/LastKind")
		}
		for i, h := range list {
			if got, _ := seq.PeekKind(i, heavy); got != h {
				t.Fatalf("PeekKind(%d, %v)", i, heavy)
			}
			if pos, ok := seq.RankKind(h); !ok || pos != i {
				t.Fatalf("RankKind of %d: %d, want %d", h.Value, pos, i)
			}
			if pos, ok := seq.PosKind(h); !ok || pos != i {
				t.Fatalf("PosKind of %d: %d, want %d", h.Value, pos, i)
			}
			var next, prev *Handle[int]
			if i+1 < len(list) {
				next = list[i+1]
			}
			if i > 0 {
				prev = list[i-1]
			}
			if seq.NextKind(h) != next || seq.PrevKind(h) != prev {
				t.Fatalf("NextKind/PrevKind of %d", h.Value)
			}
		}
	}
}

// TestSequenceKindModel: the heavy counts and kind threads follow every edit (insertion, removal,
// splice, republication, move, Keep) and every journal rollback, the kind reads match the order,
// and the journaled point bounds cover what the edits report with the threads on.
func TestSequenceKindModel(t *testing.T) {
	for seed := range uint64(16) {
		r := rand.New(rand.NewPCG(seed, 15)) //nolint:gosec // a deterministic test sequence
		var j Journal
		hooks := &meterHooks{j: &j}
		var seq Seq[int]
		seq.SetHooks(hooks)
		seq.SetWeigh(func(v int) bool { return v%2 == 0 })
		seq.SetLabeled(seed%2 == 0)
		handle := map[int]*Handle[int]{}
		next := 0
		for range 400 {
			if seed >= 8 && !seq.KindThreads() && r.IntN(40) == 0 {
				hooks.units = 0
				bound := seq.KindThreadsBound(true)
				seq.SetKindThreads()
				if hooks.units > bound {
					t.Fatalf("SetKindThreads used %d, bound %d", hooks.units, bound)
				}
			}
			mark := j.Mark()
			before := seqValues(&seq)
			n := seq.Len()
			hooks.units = 0
			var bound uint64
			switch op := r.IntN(7); {
			case op <= 2 || n == 0:
				anchor := seq.Peek(r.IntN(n + 1))
				bound = seq.InsertBound(anchor, true, nil)
				handle[next] = seq.InsertBefore(anchor, next)
				next++
			case op == 3:
				bound = seq.RemoveBound(true)
				seq.Remove(seq.Peek(r.IntN(n)))
			case op == 4:
				lo := r.IntN(n)
				hi := lo + r.IntN(min(8, n-lo)+1)
				values := []int{}
				for range r.IntN(3) {
					values = append(values, next)
					next++
				}
				hs, _ := seq.Splice(lo, hi-lo, values)
				for i, v := range values {
					handle[v] = hs[i]
				}
			case op == 5:
				lo := r.IntN(n)
				hi := lo + r.IntN(min(8, n-lo)+1)
				values := slices.Clone(before[lo:hi])
				for range r.IntN(3) {
					values = append(values, next)
					next++
				}
				r.Shuffle(len(values), func(a, b int) { values[a], values[b] = values[b], values[a] })
				bound = seq.RepublishBound(lo, hi, len(values)-(hi-lo), true, nil)
				hs, ok := seq.Republish(lo, hi, values, func(v int) *Handle[int] {
					if h := handle[v]; seq.Owns(h) {
						return h
					}
					return nil
				})
				if !ok {
					t.Fatal("Republish refused")
				}
				for i, v := range values {
					handle[v] = hs[i]
				}
			case op == 6 && r.IntN(4) == 0:
				drop := r.IntN(5)
				bound = seq.KeepBound(true)
				seq.Keep(func(v int) bool { return v%5 != drop })
			default:
				lo := r.IntN(n)
				hi := lo + r.IntN(min(5, n-lo)+1)
				seq.MoveRange(lo, hi, r.IntN(n+1))
			}
			if bound != 0 && hooks.units > bound {
				t.Fatalf("seed %d: an edit used %d units, bound %d", seed, hooks.units, bound)
			}
			checkKinds(t, &seq)
			if r.IntN(4) == 0 {
				j.Rollback(mark)
				if !slices.Equal(seqValues(&seq), before) {
					t.Fatalf("seed %d: rollback did not restore the order", seed)
				}
				checkKinds(t, &seq)
			}
		}
	}
}
