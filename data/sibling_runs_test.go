// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

const runSchema = `module run-test { yang-version 1.1; namespace "urn:run-test"; prefix r;
 container c { leaf a { type string; } leaf b { type string; } leaf z { type string; }
 leaf-list ll { type uint32; } } }`

// Only package tests build schema-after-opaque siblings. Every node uses the same explicit
// placement for reservation and application; no public parse flag or producer is introduced.
func runTree(t *testing.T, set *schema.Set, input ...string) (*Tree, *Node) {
	t.Helper()
	tr := newTree(set)
	c, err := tr.NewInner(nil, "run-test:c", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range input {
		n := runNode(t, set, c, item)
		p := c.kids.physicalPlacement(n, nil)
		bound, err := tr.linkPlacedBound(c, &c.kids, n, p)
		if err != nil {
			t.Fatal(err)
		}
		mustLink(t, tr.reserveHash(bound))
		mustLink(t, tr.linkPlaced(c, &c.kids, n, p))
	}
	return tr, c
}

func runNode(t *testing.T, set *schema.Set, c *Node, item string) *Node {
	t.Helper()
	name, text, term := strings.Cut(item, "=")
	if !term {
		return newOpaque(opaque{Name: name, ModuleNS: "urn:run-test", Value: "u"})
	}
	var sn *schema.Node
	for _, child := range c.schema.Children {
		if child.Name == name {
			sn = child
			break
		}
	}
	if sn == nil {
		t.Fatalf("missing schema %s", name)
	}
	v, d := types.Store(sn.Type, text, types.FormatJSON, types.HintData, types.ModuleNames{Set: set}, sn)
	if d != nil {
		t.Fatal(d.Msg)
	}
	return newTerm(sn, v)
}

func runNames(c *Node) []string {
	var out []string
	for _, n := range c.kids.docSpan().All() {
		text := n.Name()
		if n.schema != nil {
			text += "=" + n.value.Canonical()
		}
		out = append(out, text)
	}
	return out
}

func TestSplitRunInsertScaling(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	measure := func(count int) (int64, uint64) {
		input := []string{"ll=1", "zz"}
		for i := range count {
			input = append(input, "ll="+strconv.Itoa(100+i%150))
		}
		tr, c := runTree(t, set, input...)
		if _, err := tr.NewTerm(c, "ll", "60", NewOptions{}); err != nil {
			t.Fatal(err)
		}
		tr.work.Store(0)
		tr.work.hashUsed = 0
		tr.work.visits = true
		for v := range 8 {
			if _, err := tr.NewTerm(c, "ll", strconv.Itoa(2+v), NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		work, hash := tr.work.Load(), tr.work.hashUsed
		tr.work.visits = false
		opidxAssertTreeInvariant(t, tr)
		if got := runNames(c); got[10] != "zz" {
			t.Fatalf("sorted insertion crossed separator: %v", got[:12])
		}
		return work, hash
	}
	small, sh := measure(64)
	large, lh := measure(4096)
	t.Logf("piece=9 trailing=64/4096 visits=%d/%d hash=%d/%d", small, large, sh, lh)
	if large > 2*small+64 || lh > 2*sh+2048 {
		t.Fatalf("walk escaped piece: visits %d/%d hash %d/%d", small, large, sh, lh)
	}
}

func TestSplitRunBulkUnlink(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	build := func() (*Tree, *Node, *Node) {
		tr, c := runTree(t, set, "a=x", "b=y", "ll=4", "ll=2", "zz", "ll=3", "z=z")
		var zero *Node
		for _, v := range []string{"1", "0"} {
			var err error
			zero, err = tr.NewTerm(c, "ll", v, NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
		}
		return tr, c, zero
	}
	single, sc, n := build()
	mustLink(t, n.Unlink())
	batch, bc, m := build()
	mustLink(t, batch.unlinkAll([]*Node{m}))
	for _, x := range []struct {
		tr *Tree
		c  *Node
	}{{single, sc}, {batch, bc}} {
		opidxAssertTreeInvariant(t, x.tr)
		if _, err := x.tr.NewTerm(x.c, "ll", "5", NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(runNames(sc), runNames(bc)) {
		t.Fatalf("single %v, batch %v", runNames(sc), runNames(bc))
	}
}

func TestAloneLeaderUnlink(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			tr, c := runTree(t, set, "zz", "ll=1", "yy", "ll=9", "ll=10", "ll=11")
			two, err := tr.NewTerm(c, "ll", "2", NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			mustLink(t, two.Unlink())
			n := c.kids.docSpan().At(1)
			if batch {
				mustLink(t, tr.unlinkAll([]*Node{n}))
			} else {
				mustLink(t, n.Unlink())
			}
			if n.detachedRB == nil || len(n.detachedRB.nodes) != 1 {
				t.Fatal("singleton piece lost retained metadata")
			}
			opidxAssertTreeInvariant(t, tr)
			// Reinsertion itself is reserved for the later move part; directly linking a detached
			// singleton here checks adoption without introducing a public interleaved producer.
			p := c.kids.physicalPlacement(n, c.kids.docSpan().At(1))
			mustLink(t, tr.linkPlaced(c, &c.kids, n, p))
			opidxAssertTreeInvariant(t, tr)
		})
	}
}

func TestRunRecordsReplay(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, count := range []int{2, 3, 4, 8} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			input := []string{"ll=4", "zz", "ll=9"}
			for i := 2; i < count; i++ {
				input = append(input, "ll="+strconv.Itoa(i+10))
			}
			tr, c := runTree(t, set, input...)
			s := &c.kids
			sn := s.At(0).schema
			opidxAssertTreeInvariant(t, tr)
			if s.order == nil || s.order.entries[sn].gaps != 1 {
				t.Fatal("opaque did not split indexed run")
			}
			if s.ht != nil && !slices.Equal(s.records(sn), []*Node{s.At(0), s.At(1)}) {
				t.Fatal("record build skipped separator")
			}
			if lo, hi := tr.runBounds(s, runNode(t, set, c, "ll=3")); lo != 0 || hi != 1 {
				t.Fatalf("piece bounds %d,%d", lo, hi)
			}
			for i := 0; i < s.SchemaLen(); i++ {
				if hi := s.runPiece(i, s.SchemaLen()); i == 0 && hi != 1 {
					t.Fatal("forward cut")
				}
				if lo := s.runPieceStart(i); i > 0 && lo != 1 {
					t.Fatal("backward cut")
				}
			}
			hadTable := s.ht != nil
			n, err := tr.NewTerm(c, "ll", "3", NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if hadTable && s.leaders[sn].last.node != n {
				t.Fatal("replacement must append to registration order")
			}
			opidxAssertTreeInvariant(t, tr)
			mustLink(t, n.Unlink())
			opidxAssertTreeInvariant(t, tr)
			separator := s.docSpan().At(1)
			left := s.At(0)
			group := s.rbGroups[left]
			topology := s.rbOrders[group]
			mustLink(t, separator.Unlink())
			if s.order.entries[sn].gaps != 0 || group != nil && !s.unsorted[left] {
				t.Fatal("separator removal left stale run state")
			}
			if group != nil && s.rbOrders[group] != topology {
				t.Fatal("separator removal rebuilt retained topology")
			}
			opidxAssertTreeInvariant(t, tr)
		})
	}
}

func TestRunMutationBudgetRollback(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, operation := range []string{"insert", "separator", "leader", "batch"} {
		t.Run(operation, func(t *testing.T) {
			build := func() (*Tree, *Node) {
				tr, c := runTree(t, set, "ll=4", "ll=2", "zz", "ll=9", "ll=10")
				if _, err := tr.NewTerm(c, "ll", "3", NewOptions{}); err != nil {
					t.Fatal(err)
				}
				return tr, c
			}
			apply := func(tr *Tree, c *Node) error {
				switch operation {
				case "insert":
					_, err := tr.NewTerm(c, "ll", "1", NewOptions{})
					return err
				case "separator":
					return c.kids.docSpan().At(3).Unlink()
				case "leader":
					return c.kids.docFirst().Unlink()
				default:
					return tr.unlinkAll([]*Node{c.kids.docFirst(), c.kids.docSpan().At(3)})
				}
			}
			tr, c := build()
			end := tr.work.beginHashWork()
			mustLink(t, apply(tr, c))
			used := tr.work.hashUsed
			end()
			for limit := uint64(1); limit < used; limit++ {
				tr, c = build()
				before := runNames(c)
				end = tr.work.beginHashWork()
				j := beginHashTransaction(tr)
				tr.work.hashLimit = limit
				err := apply(tr, c)
				j.end(&err)
				end()
				if err != nil && !errors.Is(err, yang.ErrBudget) {
					t.Fatal(err)
				}
				if errors.Is(err, yang.ErrBudget) && !slices.Equal(before, runNames(c)) {
					t.Fatalf("limit %d changed layout", limit)
				}
				tr.work.hashLimit = 0
				tr.work.hashErr = nil
				opidxAssertTreeInvariant(t, tr)
			}
		})
	}
}

// Split an already indexed set, and also trigger the initial index transition through an
// opaque insertion. These take different reservations from appending schema after opaque.
func TestRunOpaqueTransitionReservations(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, indexed := range []bool{false, true} {
		for _, journaled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%t/%t", indexed, journaled), func(t *testing.T) {
				input := make([]string, 129)
				for i := range input {
					input[i] = "ll=" + strconv.Itoa(i+1)
				}
				tr, c := runTree(t, set, input...)
				s := &c.kids
				sn := s.At(0).schema
				if indexed {
					tr.markUnordered(s)
					mustLink(t, s.opBuild(tr))
				}
				before := s.docNodes()
				end := tr.work.beginHashWork()
				defer end()
				var j *hashTransaction
				if journaled {
					j = beginHashTransaction(tr)
				}
				tr.work.visits = true
				for _, at := range []int{1, 3} {
					n := newOpaque(opaque{Name: "zz"})
					p := s.physicalPlacement(n, s.docSpan().At(at))
					bound, err := tr.linkPlacedBound(c, s, n, p)
					if err != nil {
						t.Fatal(err)
					}
					wasIndexed := s.order != nil
					used, work := tr.work.hashUsed, tr.work.Load()
					mustLink(t, tr.reserveHash(bound))
					mustLink(t, tr.linkPlaced(c, s, n, p))
					if !wasIndexed && tr.work.hashUsed-used+64 < uint64(tr.work.Load()-work) { //nolint:gosec // counted work only increases
						t.Fatal("transition refunded materialization or summary work")
					}
					opidxAssertTreeInvariant(t, tr)
				}
				if s.order.entries[sn].gaps != 2 {
					t.Fatal("opaque insertion did not maintain gaps")
				}
				mustLink(t, s.docSpan().At(3).Unlink())
				mustLink(t, s.docSpan().At(1).Unlink())
				if s.order.entries[sn].gaps != 0 {
					t.Fatal("opaque removal did not close gaps")
				}
				if !slices.Equal(s.docNodes(), before) {
					t.Fatal("separator round trip changed schema order")
				}
				if journaled {
					err := yang.ErrBudget
					j.end(&err)
					if s.st.interleaved() {
						t.Fatal("rollback kept interleaved state")
					}
				}
				opidxAssertTreeInvariant(t, tr)
			})
		}
	}
}

func TestRunDocumentLeaderTransitions(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	tr, c := runTree(t, set, "ll=4", "zz", "ll=9", "ll=10", "ll=11")
	s := &c.kids
	left, right := s.At(0), s.At(1)
	sn := left.schema
	n := runNode(t, set, c, "ll=8")
	p := s.physicalPlacement(n, right)
	mustLink(t, tr.linkPlaced(c, s, n, p))
	if !slices.Equal(s.records(sn), []*Node{left, n}) {
		t.Fatal("insertion used logical predecessor instead of opaque neighbour")
	}
	mustLink(t, n.Unlink())
	if !slices.Equal(s.records(sn), []*Node{left, right}) {
		t.Fatal("unlink failed to register the next piece leader")
	}
	opidxAssertTreeInvariant(t, tr)
}

func TestRunPublicationNeighbours(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	_, c := runTree(t, set, "ll=1", "zz", "ll=9", "ll=10")
	s := &c.kids
	first, second, separator := s.At(0), s.At(1), s.docSpan().At(1)
	neighbours := visibleNeighbours(s, 0, s.SchemaLen(), []*Node{first, second})
	if neighbours[first][1] != separator || neighbours[second][0] != separator {
		t.Fatal("record replay crossed opaque separator")
	}
}

// Native prev wraps at the first sibling. A one-member piece at the document head is
// therefore not ALONE when the physical tail has the same schema, past an opaque sibling.
func TestRunCyclicTailUnlink(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			tr, c := runTree(t, set, "ll=1", "zz", "ll=9", "ll=10", "ll=11")
			two, err := tr.NewTerm(c, "ll", "2", NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			mustLink(t, two.Unlink())
			first := c.kids.docFirst()
			if batch {
				mustLink(t, tr.unlinkAll([]*Node{first}))
			} else {
				mustLink(t, first.Unlink())
			}
			if first.detachedRB != nil {
				t.Fatal("cyclic physical tail was replaced by the opaque tail")
			}
			opidxAssertTreeInvariant(t, tr)
		})
	}
}

// Roll back while a changed gap is still observable. A separator round trip alone would
// hide an unjournaled increment/decrement pair, even though both final layouts agree.
func TestRunGapJournalRollback(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprint(remove), func(t *testing.T) {
			tr, c := runTree(t, set, "ll=1", "zz", "ll=9", "ll=10")
			s := &c.kids
			before := s.docNodes()
			entry := s.order.entries[s.At(0).schema]
			j := beginHashTransaction(tr)
			if remove {
				mustLink(t, s.docSpan().At(1).Unlink())
			} else {
				n := newOpaque(opaque{Name: "yy"})
				p := s.physicalPlacement(n, s.docLast())
				mustLink(t, tr.linkPlaced(c, s, n, p))
			}
			if entry.gaps == 1 {
				t.Fatal("fixture did not change the gap")
			}
			err := yang.ErrBudget
			j.end(&err)
			if entry.gaps != 1 || !slices.Equal(s.docNodes(), before) {
				t.Fatal("rollback did not restore gaps with storage")
			}
			tr.work.hashErr = nil
			opidxAssertTreeInvariant(t, tr)
		})
	}
}

func TestRunPhysicalRecordBuildCharged(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	var measured []uint64
	for _, count := range []int{32, 1024} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			input := []string{"ll=1"}
			for range count {
				input = append(input, "zz")
			}
			input = append(input, "ll=2", "ll=3")
			tr, c := runTree(t, set, input...)
			s := &c.kids
			if s.ht != nil || !s.st.interleaved() {
				t.Fatal("fixture must precede interleaved table construction")
			}
			n := runNode(t, set, c, "ll=4")
			p := s.physicalPlacement(n, nil)
			end := tr.work.beginHashWork()
			defer end()
			if count == 1024 {
				tr.work.hashLimit = 2 * uint64(len(input)) // less than the three record-build walks
				if err := tr.preflightSplice(c, s, []*Node{n}); !errors.Is(err, yang.ErrBudget) {
					t.Fatalf("shared batch bound omitted physical records: %v", err)
				}
				tr.work.hashErr = nil
				err := tr.linkPlaced(c, s, n, p)
				if !errors.Is(err, yang.ErrBudget) {
					t.Fatalf("physical record scan bypassed quota: %v", err)
				}
				if s.ht != nil || n.parent != nil || s.SchemaLen() != 3 {
					t.Fatal("refused table build changed destination")
				}
				tr.work.hashLimit = 0
				tr.work.hashErr = nil
				tr.work.hashUsed = 0
			}
			mustLink(t, tr.linkPlaced(c, s, n, p))
			measured = append(measured, tr.work.hashUsed)
			opidxAssertTreeInvariant(t, tr)
		})
	}
	if len(measured) != 2 || measured[1]-measured[0] < 3*(1024-32) {
		t.Fatalf("physical record work not charged: %v", measured)
	}
	t.Logf("opaque=32/1024 hash=%v", measured)
}
