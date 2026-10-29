// SPDX-License-Identifier: BSD-3-Clause

package opidx

import (
	"math/rand/v2"
	"slices"
	"testing"
)

func seqValues(s *Seq[int]) []int {
	var out []int
	for h := s.First(); h != nil; h = s.Next(h) {
		out = append(out, h.Value)
	}
	return out
}

// TestRepublishModel: Republish keeps the identities of the range's handles, gives new values
// fresh ones, matches a slice model, stays within its bound under a journal, and a journal
// rollback restores the sequence and detaches the fresh handles.
func TestRepublishModel(t *testing.T) {
	for _, journaled := range []bool{false, true} {
		for seed := range uint64(20) {
			r := rand.New(rand.NewPCG(seed, 5)) //nolint:gosec // a deterministic test sequence
			var j Journal
			h := &meterHooks{}
			if journaled {
				h.j = &j
			}
			var seq Seq[int]
			seq.SetHooks(h)
			var model []int
			handle := map[int]*Handle[int]{}
			next := 0
			for range 300 {
				if r.IntN(3) == 0 || len(model) == 0 {
					at := r.IntN(len(model) + 1)
					handle[next] = seq.InsertBefore(seq.Peek(at), next)
					model = slices.Insert(model, at, next)
					next++
					continue
				}
				lo := r.IntN(len(model))
				hi := lo + r.IntN(min(12, len(model)-lo)+1)
				values := slices.Clone(model[lo:hi])
				fresh := r.IntN(4)
				for range fresh {
					values = append(values, next)
					next++
				}
				r.Shuffle(len(values), func(a, b int) { values[a], values[b] = values[b], values[a] })
				mark := j.Mark()
				before := seqValues(&seq)
				bound := seq.RepublishBound(lo, hi, fresh, journaled, nil)
				h.units = 0
				got, ok := seq.Republish(lo, hi, values, func(v int) *Handle[int] { return handle[v] })
				if !ok {
					t.Fatalf("seed %d: Republish refused a valid range", seed)
				}
				if h.units > bound {
					t.Fatalf("seed %d: Republish used %d units, bound %d", seed, h.units, bound)
				}
				for i, v := range values {
					if old := handle[v]; old != nil && got[i] != old {
						t.Fatal("an existing handle lost its identity")
					}
					if got[i].Value != v || !seq.Owns(got[i]) {
						t.Fatal("wrong handle for a value")
					}
				}
				if journaled && r.IntN(3) == 0 {
					j.Rollback(mark)
					if !slices.Equal(seqValues(&seq), before) {
						t.Fatalf("seed %d: rollback did not restore the order", seed)
					}
					for i, v := range values {
						if handle[v] == nil && seq.Owns(got[i]) {
							t.Fatal("a rolled-back fresh handle is still live")
						}
					}
				} else {
					model = slices.Replace(model, lo, hi, values...)
					for i, v := range values {
						handle[v] = got[i]
					}
				}
				if err := seq.CheckInvariants(); err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(seqValues(&seq), model) {
					t.Fatalf("seed %d: order differs from the model", seed)
				}
				for i, v := range model {
					if p, ok := seq.Pos(handle[v]); !ok || p != i || seq.Peek(i) != handle[v] {
						t.Fatal("Pos/Peek")
					}
				}
			}
		}
	}
}

// TestRepublishRejectsBeforeWrite: a missing, foreign or repeated handle refuses with nothing
// changed.
func TestRepublishRejectsBeforeWrite(t *testing.T) {
	var seq, other Seq[int]
	hs := []*Handle[int]{seq.InsertBefore(nil, 0), seq.InsertBefore(nil, 1), seq.InsertBefore(nil, 2)}
	foreign := other.InsertBefore(nil, 9)
	of := func(m map[int]*Handle[int]) func(int) *Handle[int] { return func(v int) *Handle[int] { return m[v] } }
	for name, c := range map[string]struct {
		values []int
		m      map[int]*Handle[int]
	}{
		"missing":  {[]int{0}, map[int]*Handle[int]{0: hs[0]}},
		"repeated": {[]int{0, 0}, map[int]*Handle[int]{0: hs[0]}},
		"foreign":  {[]int{0, 9}, map[int]*Handle[int]{0: hs[0], 9: foreign}},
		"outside":  {[]int{0, 2}, map[int]*Handle[int]{0: hs[0], 2: hs[2]}},
	} {
		if _, ok := seq.Republish(0, 2, c.values, of(c.m)); ok {
			t.Fatalf("%s: accepted", name)
		}
		if !slices.Equal(seqValues(&seq), []int{0, 1, 2}) || seq.CheckInvariants() != nil {
			t.Fatalf("%s: changed the sequence", name)
		}
	}
}

// TestSlotJournal: a slot write is an image of the journal; rollback restores it without
// allocating.
func TestSlotJournal(t *testing.T) {
	var j Journal
	h := &testHooks{j: &j}
	var seq Seq[int]
	a, b := seq.InsertBefore(nil, 1), seq.InsertBefore(nil, 2)
	var s Slot[int]
	s.Set(nil, a)
	mark := j.Mark()
	s.Set(h, b)
	s.Set(h, nil)
	if h.units != 4 || s.Get() != nil {
		t.Fatalf("units %d, slot %v", h.units, s.Get())
	}
	if allocs := testing.AllocsPerRun(1, func() { j.Rollback(mark) }); allocs != 0 {
		t.Fatalf("rollback allocated %v", allocs)
	}
	if s.Get() != a {
		t.Fatal("rollback did not restore the slot")
	}
}

// TestKeepModel: Keep removes the rejected handles in one pass, keeps the survivors' identities
// and order, stays within its bound, and a journal rollback restores everything, labeled or not.
func TestKeepModel(t *testing.T) {
	for _, labeled := range []bool{false, true} {
		for seed := range uint64(10) {
			r := rand.New(rand.NewPCG(seed, 9)) //nolint:gosec // a deterministic test sequence
			var j Journal
			h := &meterHooks{j: &j}
			var seq Seq[int]
			seq.SetLabeled(labeled)
			seq.SetHooks(h)
			handle := map[int]*Handle[int]{}
			for i := range 200 {
				handle[i] = seq.InsertBefore(seq.Peek(r.IntN(seq.Len()+1)), i)
			}
			before := seqValues(&seq)
			mark := j.Mark()
			drop := map[int]bool{}
			for _, v := range before {
				drop[v] = r.IntN(3) == 0
			}
			bound := seq.KeepBound(true)
			h.units = 0
			var calls []int
			gone := seq.Keep(func(v int) bool { calls = append(calls, v); return !drop[v] })
			if h.units > bound {
				t.Fatalf("Keep used %d units, bound %d", h.units, bound)
			}
			if !slices.Equal(calls, before) {
				t.Fatal("Keep did not call keep once per handle in order")
			}
			var want []int
			for _, v := range before {
				if !drop[v] {
					want = append(want, v)
				}
			}
			if !slices.Equal(seqValues(&seq), want) || seq.CheckInvariants() != nil {
				t.Fatal("Keep left the wrong order")
			}
			for _, g := range gone {
				if seq.Owns(g) || !drop[g.Value] {
					t.Fatal("a removed handle is live, or a kept one was returned")
				}
			}
			for _, v := range want {
				if !seq.Owns(handle[v]) {
					t.Fatal("a survivor lost its handle")
				}
			}
			j.Rollback(mark)
			if !slices.Equal(seqValues(&seq), before) || seq.CheckInvariants() != nil {
				t.Fatal("rollback did not restore the sequence")
			}
		}
	}
}

// TestUnlabeledSequence: without labels a sequence keeps order, ranks and Compare (by rank);
// turning them on labels every handle, and a rollback turns them off again.
func TestUnlabeledSequence(t *testing.T) {
	var j Journal
	h := &meterHooks{j: &j}
	var seq Seq[int]
	seq.SetLabeled(false)
	seq.SetHooks(h)
	var hs []*Handle[int]
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // a deterministic test sequence
	for i := range 300 {
		hs = append(hs, seq.InsertBefore(seq.Peek(r.IntN(seq.Len()+1)), i))
	}
	for range 100 {
		a, b := hs[r.IntN(len(hs))], hs[r.IntN(len(hs))]
		pa, _ := seq.Pos(a)
		pb, _ := seq.Pos(b)
		if c, _ := seq.Compare(a, b); c != cmpInt(pa, pb) {
			t.Fatal("Compare disagrees with ranks without labels")
		}
	}
	if err := seq.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	mark := j.Mark()
	h.units = 0
	seq.SetLabeled(true)
	if !seq.Labeled() || h.units != 0 {
		t.Fatalf("labelling reported %d units", h.units)
	}
	if err := seq.CheckInvariants(); err != nil { // the labels are strictly increasing now
		t.Fatal(err)
	}
	seq.InsertBefore(seq.First(), -1)
	j.Rollback(mark)
	if seq.Labeled() || seq.Len() != 300 || seq.CheckInvariants() != nil {
		t.Fatal("rollback did not restore the unlabeled sequence")
	}
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// TestUnlabeledBoundsCover checks the point bounds of an unlabeled sequence against the units its
// edits report, journaled or not.
func TestUnlabeledBoundsCover(t *testing.T) {
	for _, journaled := range []bool{false, true} {
		r := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // a deterministic test sequence
		var j Journal
		h := &meterHooks{}
		if journaled {
			h.j = &j
		}
		var seq Seq[int]
		seq.SetLabeled(false)
		seq.SetHooks(h)
		var live []*Handle[int]
		check := func(op string, bound uint64, edit func()) {
			t.Helper()
			h.units = 0
			edit()
			if h.units > bound {
				t.Fatalf("%s used %d units, bound %d", op, h.units, bound)
			}
		}
		for i := range 2000 {
			if r.IntN(40) == 0 {
				j.Commit()
			}
			switch c := r.IntN(8); {
			case c < 4 || len(live) < 4:
				anchor := seq.Peek(r.IntN(seq.Len() + 1))
				var nh *Handle[int]
				check("insert", seq.InsertBound(anchor, journaled, nil), func() { nh = seq.InsertBefore(anchor, i) })
				live = append(live, nh)
			case c < 6:
				k := r.IntN(len(live))
				check("remove", seq.RemoveBound(journaled), func() { seq.Remove(live[k]) })
				live = append(live[:k], live[k+1:]...)
			case c < 7:
				x, a := live[r.IntN(len(live))], live[r.IntN(len(live))]
				check("move", seq.MoveBound(x, a, journaled, nil), func() { seq.MoveBefore(x, a) })
			default:
				lo := r.IntN(seq.Len() - 1)
				hi := lo + 2 + r.IntN(min(8, seq.Len()-lo-1))
				var values []int
				byValue := map[int]*Handle[int]{}
				for x := seq.Peek(lo); len(values) < hi-lo; x = seq.Next(x) {
					values = append(values, x.Value)
					byValue[x.Value] = x
				}
				r.Shuffle(len(values), func(a, b int) { values[a], values[b] = values[b], values[a] })
				check("republish", seq.RepublishBound(lo, hi, 0, journaled, nil), func() {
					if _, ok := seq.Republish(lo, hi, values, func(v int) *Handle[int] { return byValue[v] }); !ok {
						t.Fatal("Republish refused")
					}
				})
			}
		}
		if err := seq.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
	}
}
