// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

// interleavedModel is the document order of a set and its logical views (design 15).
type interleavedModel []*Node

func (m interleavedModel) kind(schemaNodes bool) []*Node {
	var out []*Node
	for _, n := range m {
		if (n.schema != nil) == schemaNodes {
			out = append(out, n)
		}
	}
	return out
}

func (m interleavedModel) logical() []*Node { return append(m.kind(true), m.kind(false)...) }

func (m interleavedModel) insertBefore(anchor, n *Node) interleavedModel {
	if anchor == nil {
		return append(m, n)
	}
	return slices.Insert(m, slices.Index(m, anchor), n)
}

func (m interleavedModel) next(n *Node) *Node {
	if i := slices.Index(m, n); i+1 < len(m) {
		return m[i+1]
	}
	return nil
}

// TestSiblingFacadeInterleavedModel: in a set whose schema and opaque nodes interleave, logical
// positions, spans, iterators, cursors, lookups and LowerBound keep their schema-then-opaque
// meaning, document-order reads follow the physical order, and every write lands where
// libyang's anchors put it (storeAnchor, placements), against a slice model.
func TestSiblingFacadeInterleavedModel(t *testing.T) {
	for _, seed := range []uint64{3, 99, 4099} {
		rng := rand.New(rand.NewPCG(seed, 247)) //nolint:gosec // reproducible model input
		var s siblings
		var model interleavedModel
		fresh := func(opaque bool) *Node {
			if opaque {
				return &Node{}
			}
			return &Node{schema: &schema.Node{Kind: schema.Leaf}}
		}
		for step := 0; step < 900; step++ {
			sch, opq := model.kind(true), model.kind(false)
			switch op := rng.IntN(8); {
			case op <= 1: // logical insertion (storeAnchor)
				opaque := rng.IntN(2) == 0
				n := fresh(opaque)
				var anchor *Node
				if opaque {
					j := rng.IntN(len(opq) + 2)
					switch {
					case j >= len(opq):
						j = len(opq)
					case j == 0:
						anchor = opq[0]
					default:
						anchor = model.next(opq[j-1])
					}
					mustLink(t, s.InsertAt(len(sch)+j, n))
				} else {
					at := rng.IntN(len(sch) + 1)
					switch {
					case at < len(sch):
						anchor = sch[at]
					case len(sch) > 0:
						anchor = model.next(sch[len(sch)-1])
					case len(model) > 0:
						anchor = model[0]
					}
					mustLink(t, s.InsertAt(at, n))
				}
				model = model.insertBefore(anchor, n)
			case op == 2: // LYD_INSERT_NODE_LAST
				n := fresh(rng.IntN(3) == 0)
				s.setPlace(placement{mode: placeEnd, target: n})
				mustLink(t, s.appendNode(n))
				s.clearPlace()
				model = append(model, n)
			case op == 3 && len(model) > 0: // right after or before a node
				n := fresh(rng.IntN(2) == 0)
				x := model[rng.IntN(len(model))]
				anchor, mode := x, placeBefore
				if rng.IntN(2) == 0 {
					anchor, mode = model.next(x), placeAfter
				}
				s.setPlace(placement{mode: mode, node: x, target: n})
				mustLink(t, s.appendNode(n))
				s.clearPlace()
				model = model.insertBefore(anchor, n)
			case op == 4 && len(model) > 0:
				at := rng.IntN(len(model))
				gone := model.logical()[at]
				mustLink(t, s.DeleteAt(at))
				model = slices.DeleteFunc(model, func(n *Node) bool { return n == gone })
			case op == 5 && len(sch) > 0: // replaceSchema: schema slots take the run's new order
				a := rng.IntN(len(sch))
				b := a + rng.IntN(len(sch)-a) + 1
				run := slices.Clone(sch[a:b])
				rng.Shuffle(len(run), func(i, j int) { run[i], run[j] = run[j], run[i] })
				mustLink(t, s.replaceSchema(a, b, spanOf(run)))
				members := map[*Node]bool{}
				for _, n := range run {
					members[n] = true
				}
				j := 0
				for i, n := range model {
					if members[n] {
						model[i] = run[j]
						j++
					}
				}
			case op == 6: // publishRun: the extent's schema slots, then the new members after it
				a := rng.IntN(len(sch) + 1)
				b := a + rng.IntN(len(sch)-a+1)
				run := slices.Clone(sch[a:b])
				for range rng.IntN(3) {
					run = append(run, fresh(false))
				}
				rng.Shuffle(len(run), func(i, j int) { run[i], run[j] = run[j], run[i] })
				if len(run) == 0 {
					continue
				}
				mustLink(t, s.publishRun(a, b, run))
				if a == b {
					var anchor *Node
					switch {
					case a < len(sch):
						anchor = sch[a]
					case len(sch) > 0:
						anchor = model.next(sch[len(sch)-1])
					case len(model) > 0:
						anchor = model[0]
					}
					for _, n := range run {
						model = model.insertBefore(anchor, n)
					}
					break
				}
				members := map[*Node]bool{}
				for _, n := range sch[a:b] {
					members[n] = true
				}
				j, last := 0, 0
				for i, n := range model {
					if members[n] {
						model[i], last = run[j], i
						j++
					}
				}
				model = slices.Insert(model, last+1, run[j:]...)
			}
			checkInterleavedFacade(t, &s, model, rng)
		}
		if !s.st.interleaved() {
			t.Fatalf("seed %d never interleaved", seed)
		}
	}
}

func checkInterleavedFacade(t *testing.T, s *siblings, model interleavedModel, rng *rand.Rand) {
	t.Helper()
	if err := storeCheckInvariant(s); err != nil {
		t.Fatal(err)
	}
	logical, sch := model.logical(), model.kind(true)
	if s.Len() != len(model) || s.SchemaLen() != len(sch) {
		t.Fatal("lengths")
	}
	if !slices.Equal(s.docNodes(), model) || !slices.Equal(collectSiblingIterator(s.All()), model) ||
		!slices.Equal(slices.Collect(s.all()), model) {
		t.Fatal("document order")
	}
	if !slices.Equal(collectSiblingIterator(s.reverseAll()), reversedNodes(model)) {
		t.Fatal("document order backwards")
	}
	if !slices.Equal(collectSiblingIterator(s.SchemaAll()), sch) || !slices.Equal(collectSiblingIterator(s.opaqueAll()), model.kind(false)) {
		t.Fatal("kind iteration")
	}
	for i, n := range logical {
		if s.At(i) != n || s.Index(n) != i {
			t.Fatalf("At/Index %d", i)
		}
	}
	if len(model) > 0 && (s.docFirst() != model[0] || s.docLast() != model[len(model)-1]) {
		t.Fatal("document ends")
	}
	lo := rng.IntN(len(logical) + 1)
	hi := lo + rng.IntN(len(logical)-lo+1)
	span := s.span(lo, hi)
	if !slices.Equal(span.Collect(), logical[lo:hi]) || !slices.Equal(collectSiblingIterator(span.Reverse()), reversedNodes(logical[lo:hi])) {
		t.Fatal("logical span")
	}
	c := span.cursor()
	for _, want := range logical[lo:hi] {
		if c.next() != want {
			t.Fatal("cursor")
		}
	}
	if c.next() != nil {
		t.Fatal("cursor past the end")
	}
	pos := map[*Node]int{}
	for i, n := range logical {
		pos[n] = i
	}
	cut := lo + rng.IntN(hi-lo+1)
	if got := s.LowerBound(lo, hi, func(n *Node) bool { return pos[n] >= cut }); got != cut {
		t.Fatalf("LowerBound %d, want %d", got, cut)
	}
	for i, n := range model {
		var next *Node
		if i+1 < len(model) {
			next = model[i+1]
		}
		if s.docNext(n) != next || s.docSpan().Index(n) != i {
			t.Fatal("document neighbours")
		}
	}
}

// TestSiblingFacadeInterleaveRollback: the append that interleaves a set (its counter and the
// kind threads) rolls back with the state that made it.
func TestSiblingFacadeInterleaveRollback(t *testing.T) {
	tr := newTree(nil)
	a, o, b := &Node{schema: &schema.Node{}}, &Node{}, &Node{schema: &schema.Node{}}
	mustLink(t, testBuild(&tr.top, a, o))
	tr.top.setWork(&tr.work)
	j := beginHashTransaction(tr)
	tr.top.setPlace(placement{mode: placeEnd, target: b})
	mustLink(t, tr.top.appendNode(b))
	if !tr.top.st.interleaved() || !slices.Equal(tr.top.docNodes(), []*Node{a, o, b}) {
		t.Fatal("the append did not interleave")
	}
	err := yang.ErrBudget
	j.end(&err)
	if tr.top.st.interleaved() || tr.top.st.seq.KindThreads() || !slices.Equal(tr.top.docNodes(), []*Node{a, o}) {
		t.Fatal("rollback kept the interleaving")
	}
	if err := storeCheckInvariant(&tr.top); err != nil {
		t.Fatal(err)
	}
}

func reversedNodes(nodes []*Node) []*Node {
	out := slices.Clone(nodes)
	slices.Reverse(out)
	return out
}
