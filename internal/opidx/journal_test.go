// SPDX-License-Identifier: BSD-3-Clause

package opidx

import (
	"math/rand/v2"
	"reflect"
	"runtime"
	"testing"
)

type testHooks struct {
	j     *Journal
	units uint64
}

func (h *testHooks) Work(uint64)          {}
func (h *testHooks) Journal() *Journal    { return h.j }
func (h *testHooks) Entries(units uint64) { h.units += units }

// journalModel is one sequence with an occurrence index and a rank set, all journaled.
type journalModel struct {
	seq   Seq[int]
	occ   *Occurrences[int, int, int]
	ranks Ranks
	all   []*Handle[int] // every handle ever created
	next  int
	cells map[int]*int
	hooks *testHooks
}

type handleState struct {
	left, right, parent, prev, next *Handle[int]
	owner                           *Seq[int]
	height, size                    int
	tag                             uint64
}

type modelState struct {
	header  seqHeader[int]
	handles map[*Handle[int]]handleState
	trees   map[occKey[int, int]]any // nil tombstone vs tree image
	by      map[*Handle[int]]any
	ranks   []int
	rlen    int
	cells   map[int]any
}

type treeImage struct {
	root        *occNode[int]
	first, last *Handle[int]
	len         int
	nodes       []occNode[int]
}

func occNodes(n *occNode[int], out *[]occNode[int]) {
	if n == nil {
		return
	}
	occNodes(n.left, out)
	c := *n
	c.stamp = 0 // bookkeeping, not state
	*out = append(*out, c)
	occNodes(n.right, out)
}

func rankKeys(n *rankNode, out *[]int) {
	if n == nil {
		return
	}
	rankKeys(n.left, out)
	*out = append(*out, n.key, n.height)
	rankKeys(n.right, out)
}

func (m *journalModel) state() modelState {
	st := modelState{header: m.seq.header(), handles: map[*Handle[int]]handleState{},
		trees: map[occKey[int, int]]any{}, by: map[*Handle[int]]any{}, rlen: m.ranks.len, cells: map[int]any{}}
	for _, h := range m.all {
		st.handles[h] = handleState{h.left, h.right, h.parent, h.prev, h.next, h.owner, h.height, h.size, h.tag}
	}
	for k, t := range m.occ.trees {
		if t == nil {
			st.trees[k] = nil
			continue
		}
		img := treeImage{root: t.root, first: t.first, last: t.last, len: t.len}
		occNodes(t.root, &img.nodes)
		st.trees[k] = img
	}
	for h, k := range m.occ.by {
		if k == nil {
			st.by[h] = nil
		} else {
			st.by[h] = *k
		}
	}
	rankKeys(m.ranks.root, &st.ranks)
	for k, v := range m.cells {
		if v == nil {
			st.cells[k] = nil
		} else {
			st.cells[k] = *v
		}
	}
	return st
}

func (m *journalModel) live() []*Handle[int] {
	var out []*Handle[int]
	for h := m.seq.First(); h != nil; h = h.next {
		out = append(out, h)
	}
	return out
}

func (m *journalModel) step(r *rand.Rand, _ *Journal) {
	live := m.live()
	pick := func() *Handle[int] {
		if len(live) == 0 {
			return nil
		}
		return live[r.IntN(len(live))]
	}
	switch op := r.IntN(9); {
	case op < 3 || len(live) == 0:
		m.next++
		var h *Handle[int]
		if r.IntN(2) == 0 {
			h = m.seq.InsertBefore(pick(), m.next)
		} else {
			h = m.seq.InsertAfter(pick(), m.next)
		}
		m.all = append(m.all, h)
		m.occ.Insert(m.next%3, m.next%2, h)
	case op == 3:
		h := pick()
		m.occ.Remove(h)
		m.seq.Remove(h)
	case op == 4:
		h, a := pick(), pick()
		m.occ.Remove(h)
		m.seq.MoveBefore(h, a)
		m.occ.Insert(h.Value%3, h.Value%2, h)
	case op == 5:
		lo := r.IntN(len(live))
		hi := lo + r.IntN(len(live)-lo+1)
		hs := append([]*Handle[int](nil), live[lo:hi]...)
		r.Shuffle(len(hs), func(a, b int) { hs[a], hs[b] = hs[b], hs[a] })
		for _, h := range hs {
			m.occ.Remove(h)
		}
		m.seq.ReplaceHandles(lo, hi, hs)
		for _, h := range hs {
			m.occ.Insert(h.Value%3, h.Value%2, h)
		}
	case op == 6:
		k := r.IntN(40)
		if !m.ranks.Insert(k) {
			m.ranks.Remove(k)
		}
	case op == 7:
		k := r.IntN(6)
		if r.IntN(2) == 0 {
			v := r.IntN(100)
			SetCell(m.hooks, m.cells, k, &v)
		} else {
			DeleteCell(m.hooks, m.cells, k)
		}
	default:
		lo := r.IntN(len(live))
		del := r.IntN(min(3, len(live)-lo) + 1)
		for _, h := range live[lo : lo+del] {
			m.occ.Remove(h)
		}
		m.next++
		ins, _ := m.seq.Splice(lo, del, []int{m.next})
		m.all = append(m.all, ins...)
		m.occ.Insert(m.next%3, m.next%2, ins[0])
	}
}

func newJournalModel(j *Journal) *journalModel {
	h := &testHooks{j: j}
	m := &journalModel{cells: map[int]*int{}, hooks: h}
	m.seq.SetHooks(h)
	m.occ = NewOccurrences[int, int, int](&m.seq)
	m.occ.SetHooks(h)
	m.ranks.SetHooks(h)
	return m
}

func mallocs() uint64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.Mallocs
}

// TestJournalNestedModel drives random index writes under nested levels: a rollback returns
// every object exactly (tombstones included) to its value at the level's mark and allocates
// nothing; a commit of the outermost level leaves no tombstone.
func TestJournalNestedModel(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1)) // as AllocsPerRun: no other allocating goroutine
	for seed := range uint64(40) {
		r := rand.New(rand.NewPCG(seed, 7)) //nolint:gosec // a deterministic test sequence
		var j Journal
		m := newJournalModel(&j)
		units := m.hooks
		for range 20 { // a populated start, committed
			m.step(r, &j)
		}
		j.Commit()
		type level struct {
			mark, handles int
			st            modelState
		}
		var levels []level
		for range 300 {
			switch c := r.IntN(10); {
			case c < 2 && len(levels) < 4:
				levels = append(levels, level{j.Mark(), len(m.all), m.state()})
			case c == 2 && len(levels) > 0:
				top := levels[len(levels)-1]
				levels = levels[:len(levels)-1]
				runtime.GC() // no pending sweep allocates during the measurement
				before := mallocs()
				j.Rollback(top.mark)
				if n := mallocs() - before; n != 0 {
					t.Fatalf("seed %d: rollback allocated %d times", seed, n)
				}
				for _, h := range m.all[top.handles:] {
					if h.owner != nil || h.left != nil || h.next != nil || h.parent != nil {
						t.Fatalf("seed %d: a handle created after the mark is not detached", seed)
					}
				}
				m.all = m.all[:top.handles]
				if got := m.state(); !reflect.DeepEqual(got, top.st) {
					t.Fatalf("seed %d: rollback did not restore the level's state", seed)
				}
			case c == 3 && len(levels) > 0:
				levels = levels[:len(levels)-1] // commit: the tail joins the parent
			default:
				m.step(r, &j)
			}
			if err := m.seq.CheckInvariants(); err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
		}
		for len(levels) > 1 {
			levels = levels[:len(levels)-1]
		}
		if len(levels) == 1 && r.IntN(2) == 0 {
			j.Rollback(levels[0].mark)
			m.all = m.all[:levels[0].handles]
			if !reflect.DeepEqual(m.state(), levels[0].st) {
				t.Fatalf("seed %d: outer rollback did not restore", seed)
			}
		}
		j.Commit()
		for k, tr := range m.occ.trees {
			if tr == nil {
				t.Fatalf("seed %d: tombstone %v survived the commit", seed, k)
			}
		}
		for h, k := range m.occ.by {
			if k == nil || h.owner != &m.seq {
				t.Fatalf("seed %d: dead by cell survived the commit", seed)
			}
		}
		for k, v := range m.cells {
			if v == nil {
				t.Fatalf("seed %d: cell tombstone %d survived the commit", seed, k)
			}
		}
		if units.units == 0 {
			t.Fatal("entries were not charged")
		}
	}
}

// TestJournalCellHistories pins the review-4 cell histories of design 13 (map cells and
// tombstones).
func TestJournalCellHistories(t *testing.T) {
	v1, v2 := 1, 2
	t.Run("insert absent, remove, commit", func(t *testing.T) {
		var j Journal
		h := &testHooks{j: &j}
		m := map[int]*int{}
		SetCell(h, m, 1, &v1)
		DeleteCell(h, m, 1)
		if v, ok := m[1]; !ok || v != nil {
			t.Fatal("removal under a journal must leave a tombstone")
		}
		j.Commit()
		if _, ok := m[1]; ok {
			t.Fatal("commit kept the tombstone")
		}
	})
	t.Run("outer remove, inner recreate, inner rollback, outer rollback", func(t *testing.T) {
		var j Journal
		h := &testHooks{j: &j}
		m := map[int]*int{1: &v1}
		DeleteCell(h, m, 1)
		inner := j.Mark()
		SetCell(h, m, 1, &v2)
		j.Rollback(inner)
		if v, ok := m[1]; !ok || v != nil {
			t.Fatal("inner rollback must restore the present nil cell")
		}
		j.Rollback(0)
		if m[1] != &v1 {
			t.Fatal("outer rollback must restore the value")
		}
	})
	t.Run("duplicate inner writes, inner commit, outer rollback", func(t *testing.T) {
		var j Journal
		h := &testHooks{j: &j}
		m := map[int]*int{1: &v1}
		SetCell(h, m, 1, &v2)
		SetCell(h, m, 1, &v1)
		SetCell(h, m, 1, &v2)
		j.Rollback(0)
		if m[1] != &v1 {
			t.Fatal("the oldest entry must win")
		}
	})
	t.Run("remove, recreate, commit keeps the live key", func(t *testing.T) {
		var j Journal
		h := &testHooks{j: &j}
		m := map[int]*int{1: &v1}
		DeleteCell(h, m, 1)
		SetCell(h, m, 1, &v2)
		j.Commit()
		if m[1] != &v2 {
			t.Fatal("purge deleted a live key")
		}
	})
}

type meterHooks struct {
	j     *Journal
	units uint64
}

func (h *meterHooks) Work(units uint64)    { h.units += units }
func (h *meterHooks) Journal() *Journal    { return h.j }
func (h *meterHooks) Entries(units uint64) { h.units += units }

// TestPointBoundsCoverJournaledEdits checks every point bound against the units its edit
// actually reports (work plus entries) under a journal, over random sequences that relabel,
// rebuild and rotate, including dense prepends and appends.
func TestPointBoundsCoverJournaledEdits(t *testing.T) {
	for _, journaled := range []bool{false, true} {
		for seed := range uint64(30) {
			r := rand.New(rand.NewPCG(seed, 11)) //nolint:gosec // a deterministic test sequence
			var j Journal
			h := &meterHooks{}
			if journaled {
				h.j = &j
			}
			var seq Seq[int]
			seq.SetHooks(h)
			occ := NewOccurrences[int, int, int](&seq)
			occ.SetHooks(h)
			var ranks Ranks
			ranks.SetHooks(h)
			var live []*Handle[int]
			check := func(op string, bound uint64, edit func()) {
				t.Helper()
				h.units = 0
				edit()
				if h.units > bound {
					t.Fatalf("seed %d: %s used %d units, bound %d (len %d)", seed, op, h.units, bound, seq.Len())
				}
			}
			for i := range 600 {
				if r.IntN(50) == 0 {
					j.Commit() // a new epoch: every object is recorded again
				}
				switch c := r.IntN(10); {
				case c < 5 || len(live) == 0:
					var anchor *Handle[int]
					switch r.IntN(3) {
					case 0:
						anchor = seq.First() // dense prepends
					case 1:
					default:
						if len(live) != 0 {
							anchor = live[r.IntN(len(live))]
						}
					}
					var nh *Handle[int]
					if b, free := seq.InsertBound(anchor, journaled, nil), InsertBoundAny(seq.Len(), journaled); b > free {
						t.Fatalf("seed %d: point bound %d above the position-free bound %d", seed, b, free)
					}
					check("insert", seq.InsertBound(anchor, journaled, nil), func() { nh = seq.InsertBefore(anchor, i) })
					live = append(live, nh)
					check("occ insert", occ.InsertBound(i%4, 0, journaled), func() { occ.Insert(i%4, 0, nh) })
				case c < 7:
					k := r.IntN(len(live))
					x := live[k]
					check("occ remove", occ.RemoveBound(x, journaled), func() { occ.Remove(x) })
					check("remove", seq.RemoveBound(true), func() { seq.Remove(x) })
					live = append(live[:k], live[k+1:]...)
				case c < 9:
					x, a := live[r.IntN(len(live))], live[r.IntN(len(live))]
					if r.IntN(4) == 0 {
						a = nil
					}
					check("occ remove", occ.RemoveBound(x, journaled), func() { occ.Remove(x) })
					check("move", seq.MoveBound(x, a, journaled, nil), func() { seq.MoveBefore(x, a) })
					check("occ insert", occ.InsertBound(x.Value%4, 0, journaled), func() { occ.Insert(x.Value%4, 0, x) })
				case c == 9 && len(live) > 2 && r.IntN(2) == 0:
					lo := r.IntN(len(live) - 1)
					var hs []*Handle[int]
					for h := seq.Select(lo); h != nil && len(hs) < 2+r.IntN(min(16, len(live)-lo-1)); h = h.next {
						hs = append(hs, h)
					}
					first, last := hs[0], hs[len(hs)-1]
					r.Shuffle(len(hs), func(a, b int) { hs[a], hs[b] = hs[b], hs[a] })
					for _, h := range hs {
						occ.Remove(h) // their relative order changes: unregister first
					}
					check("replace", seq.ReplaceHandlesBound(first, last, len(hs), journaled, nil), func() {
						if !seq.ReplaceHandles(lo, lo+len(hs), hs) {
							t.Fatal("ReplaceHandles refused a contiguous range")
						}
					})
					for _, h := range hs {
						occ.Insert(h.Value%4, 0, h)
					}
				default:
					k := r.IntN(64)
					check("ranks", ranks.UpdateBound(journaled), func() {
						if !ranks.Insert(k) {
							ranks.Remove(k)
						}
					})
				}
			}
			if err := seq.CheckInvariants(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestPlannerPaysBeforeEachVisit: the label planner charges its walk step by step through its
// Pay, so a refused budget stops it at its first units however dense the labels are, and the
// refused bound saturates (snapshot review 1, finding 5).
func TestPlannerPaysBeforeEachVisit(t *testing.T) {
	var seq Seq[int]
	anchor := seq.InsertBefore(nil, 0)
	for i := range 4096 {
		seq.InsertBefore(anchor, i+1)
	}
	tag := uint64(1)
	for h := seq.First(); h != nil; h = h.next { // packed labels: no gap anywhere
		h.tag = tag
		tag++
	}
	for _, budget := range []uint64{0, 1, 5} {
		calls := uint64(0)
		pay := func(units uint64) bool {
			calls += units
			return calls <= budget
		}
		if b := seq.InsertBound(anchor, true, PayFunc(pay)); b != ^uint64(0) && calls > budget {
			t.Fatalf("budget %d: a refused plan returned the bound %d", budget, b)
		}
		if calls > budget+1 {
			t.Fatalf("budget %d: the planner made %d paid visits", budget, calls)
		}
	}
	unpaid := seq.InsertBound(anchor, true, nil)
	paid := uint64(0)
	if seq.InsertBound(anchor, true, PayFunc(func(u uint64) bool { paid += u; return true })) != unpaid {
		t.Fatal("paying changed the bound")
	}
	if paid < 8 {
		t.Fatalf("a full plan paid only %d visits: the labels are not dense", paid)
	}
}
