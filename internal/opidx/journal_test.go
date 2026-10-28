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

func (h *testHooks) Work(uint64)       {}
func (h *testHooks) Journal() *Journal { return h.j }

type unitCounter struct{ units uint64 }

func (c *unitCounter) ChargeEntries(units uint64) { c.units += units }

// journalModel is one sequence with an occurrence index and a rank set, all journaled.
type journalModel struct {
	seq   Seq[int]
	occ   *Occurrences[int, int, int]
	ranks Ranks
	all   []*Handle[int] // every handle ever created
	next  int
	cells map[int]*int
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

func (m *journalModel) step(r *rand.Rand, j *Journal) {
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
			SetCell(j, m.cells, k, &v)
		} else {
			DeleteCell(j, m.cells, k)
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
	m := &journalModel{cells: map[int]*int{}}
	h := &testHooks{j: j}
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
		r := rand.New(rand.NewPCG(seed, 7))
		var j Journal
		units := &unitCounter{}
		j.SetCharger(units)
		m := newJournalModel(&j)
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
		m := map[int]*int{}
		SetCell(&j, m, 1, &v1)
		DeleteCell(&j, m, 1)
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
		m := map[int]*int{1: &v1}
		DeleteCell(&j, m, 1)
		inner := j.Mark()
		SetCell(&j, m, 1, &v2)
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
		m := map[int]*int{1: &v1}
		SetCell(&j, m, 1, &v2)
		SetCell(&j, m, 1, &v1)
		SetCell(&j, m, 1, &v2)
		j.Rollback(0)
		if m[1] != &v1 {
			t.Fatal("the oldest entry must win")
		}
	})
	t.Run("remove, recreate, commit keeps the live key", func(t *testing.T) {
		var j Journal
		m := map[int]*int{1: &v1}
		DeleteCell(&j, m, 1)
		SetCell(&j, m, 1, &v2)
		j.Commit()
		if m[1] != &v2 {
			t.Fatal("purge deleted a live key")
		}
	})
}
