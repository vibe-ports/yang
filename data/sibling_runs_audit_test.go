// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"github.com/vibe-ports/yang"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

func TestRunOpaqueMetadataSuccessor(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, populated := range []bool{false, true} {
		for _, batch := range []bool{false, true} {
			t.Run(fmt.Sprintf("populated=%t/batch=%t", populated, batch), func(t *testing.T) {
				tr, c := runTree(t, set)
				for _, v := range []string{"1", "2", "3", "4"} {
					if _, err := tr.NewTerm(c, "ll", v, NewOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				if !populated {
					// Recursive duplication retains a genuine native NULL-root metadata slot.
					copyTree := newTree(set)
					var err error
					c, _, err = copyTree.Dup(c, nil, DupOptions{Recursive: true, WithFlags: true})
					if err != nil {
						t.Fatal(err)
					}
					tr = copyTree
				}
				s := &c.kids
				first, group := s.docFirst(), s.rbGroups[s.docFirst()]
				topology := s.rbOrders[group]
				checkTreeRetainedRB(t, tr)
				sep := runNode(t, set, c, "zz")
				p := s.physicalPlacement(sep, s.At(1))
				bound, err := tr.linkPlacedBound(c, s, sep, p)
				if err != nil {
					t.Fatal(err)
				}
				mustLink(t, tr.reserveHash(bound))
				mustLink(t, tr.linkPlaced(c, s, sep, p))
				wasUnsorted := s.unsorted[first]
				if batch {
					mustLink(t, tr.unlinkAll([]*Node{first}))
				} else {
					mustLink(t, first.Unlink())
				}
				checkTreeRetainedRB(t, tr)
				opidxAssertTreeInvariant(t, tr)
				if s.unsorted[sep] != wasUnsorted || s.rbTree[sep] != populated {
					t.Fatal("transfer changed metadata selection flags")
				}
				if populated && (s.rbGroups[sep] != group || s.rbOrders[group] != topology || len(topology.nodes) != 3) {
					t.Fatal("physical successor did not retain populated metadata")
				}
				if !populated && (s.rbGroups[first] != nil || s.rbGroups[sep] != group || s.rbOrders[group] != topology || len(first.carriedRB) != 0) {
					t.Fatal("empty selected metadata did not stay on the physical successor")
				}
			})
		}
	}
}

func TestRunSuccessiveSeparatorOwners(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, separator := range []string{"zz", "b=x"} {
		for _, mode := range []string{"single", "batch", "batch-steps", "batch-reverse"} {
			t.Run(separator+"/"+mode, func(t *testing.T) {
				tr, c := runTree(t, set, "ll=4", "ll=2", separator, "ll=9", "ll=10", "yy", "ll=20", "ll=21")
				for _, value := range []string{"3", "11"} {
					if _, err := tr.NewTerm(c, "ll", value, NewOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				s := &c.kids
				left, selected := s.docFirst(), s.runLeader(s.At(0).schema)
				if selected.Value() != "9" {
					t.Fatalf("selected %s", selected.Value())
				}
				group, topology := s.rbGroups[selected], s.rbOrders[s.rbGroups[selected]]
				records := slices.Clone(s.records(selected.schema))
				first, second := s.docSpan().At(3), s.docSpan().At(7)
				switch mode {
				case "batch":
					mustLink(t, tr.unlinkAll([]*Node{first, second}))
				case "batch-reverse":
					mustLink(t, tr.unlinkAll([]*Node{second, first}))
				default:
					if mode == "batch-steps" {
						mustLink(t, tr.unlinkAll([]*Node{first}))
					} else {
						mustLink(t, first.Unlink())
					}
					if !s.unsorted[left] || s.unsorted[selected] {
						t.Fatal("first join changed wrong owners")
					}
					checkTreeRetainedRB(t, tr)
					if mode == "batch-steps" {
						mustLink(t, tr.unlinkAll([]*Node{second}))
					} else {
						mustLink(t, second.Unlink())
					}
				}
				if !s.unsorted[left] || !s.unsorted[selected] || tr.sortedRunBatchable(s, selected) {
					t.Fatal("interior selected tree incorrectly complete after second join")
				}
				if s.rbOrders[group] != topology || len(topology.nodes) != 3 || !slices.Equal(records, s.records(selected.schema)) {
					t.Fatal("join changed registration or topology")
				}
				checkTreeRetainedRB(t, tr)
				opidxAssertTreeInvariant(t, tr)
			})
		}
	}
}

func runAuditInput(count int) []string {
	input := []string{"ll=1", "zz"}
	for i := range count {
		input = append(input, "ll="+strconv.Itoa(100+i))
	}
	return input
}

func runAuditSplit(t *testing.T, set *schema.Set, count int) (*Tree, *Node) {
	t.Helper()
	return runTree(t, set, runAuditInput(count)...)
}

func TestRunColdCreationAccounting(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	var smallAlloc float64
	for _, count := range []int{64, 4096} {
		trees, parents := make([]*Tree, 4), make([]*Node, 4)
		for i := range trees {
			trees[i], parents[i] = runAuditSplit(t, set, count)
		}
		sample := 0
		allocations := testing.AllocsPerRun(3, func() {
			tr, c := trees[sample], parents[sample]
			sample++
			index := c.kids.order
			tr.work.visits = true
			tr.work.Store(0)
			tr.work.hashUsed = 0
			if _, err := tr.NewTerm(c, "ll", "60", NewOptions{}); err != nil {
				t.Fatal(err)
			}
			wantWork, wantCharge := int64(245), uint64(124)
			if count == 4096 {
				wantWork, wantCharge = 473, 178
			}
			if tr.work.Load() != wantWork || tr.work.hashUsed != wantCharge {
				t.Fatalf("n=%d cold creation work/charge=%d/%d, want %d/%d", count, tr.work.Load(), tr.work.hashUsed, wantWork, wantCharge)
			}
			if c.kids.order != index {
				t.Fatal("cold piece creation rebuilt unrelated occurrence indexes")
			}
		})
		if count == 64 {
			smallAlloc = allocations
		} else if allocations > smallAlloc+8 {
			t.Fatalf("cold allocations grew with unrelated piece: %.2f -> %.2f", smallAlloc, allocations)
		}
		t.Logf("n=%d work=%d charge=%d allocations=%.2f", count, trees[0].work.Load(), trees[0].work.hashUsed, allocations)
		for _, tr := range trees {
			checkTreeRetainedRB(t, tr)
			opidxAssertTreeInvariant(t, tr)
		}
	}
}

func TestRunStoragePreflightPiece(t *testing.T) {
	const topSchema = `module run-test {yang-version 1.1; namespace "urn:run-test"; prefix r; leaf-list ll {type uint32;} }`
	a, b := reviewDiffSet(t, topSchema), reviewDiffSet(t, topSchema)
	for _, foreign := range []bool{false, true} {
		for _, count := range []int{64, 4096} {
			t.Run(fmt.Sprintf("foreign=%t/n=%d", foreign, count), func(t *testing.T) {
				tr := newTree(a)
				if _, err := tr.NewTerm(nil, "run-test:ll", "1", NewOptions{}); err != nil {
					t.Fatal(err)
				}
				sep := newOpaque(opaque{Name: "zz", ModuleNS: "urn:run-test", Value: "u"})
				mustLink(t, tr.link(nil, &tr.top, sep, -1))
				sn := tr.top.At(0).schema
				for i := range count {
					n := retainedRBTerm(t, sn, strconv.Itoa(100+i))
					p := tr.top.physicalPlacement(n, nil)
					mustLink(t, tr.linkPlaced(nil, &tr.top, n, p))
				}
				source := newTree(a)
				if foreign {
					source = newTree(b)
				}
				n, err := source.NewTerm(nil, "run-test:ll", "60", NewOptions{})
				if err != nil {
					t.Fatal(err)
				}
				mustLink(t, n.Unlink())
				tr.work.visits = true
				tr.work.Store(0)
				tr.work.hashUsed = 0
				defer tr.work.beginHashWork()()
				mustLink(t, tr.sortedStorageRefusalMode(&tr.top, n, insertDefault, true))
				t.Logf("preflight work=%d charge=%d", tr.work.Load(), tr.work.hashUsed)
				wantWork, wantCharge := int64(62), uint64(39)
				if count == 4096 {
					wantWork, wantCharge = 122, 63
				}
				if foreign {
					wantWork, wantCharge = 38, 10
					if count == 4096 {
						wantWork = 74
					}
				}
				if tr.work.Load() != wantWork || tr.work.hashUsed != wantCharge {
					t.Fatalf("preflight work/charge=%d/%d, want %d/%d", tr.work.Load(), tr.work.hashUsed, wantWork, wantCharge)
				}
				// Isolate the preflight's quota from Insert's separate journal/binding bounds.
				// Increasing only the unrelated tail must not exhaust this small quota.
				tr.work.hashUsed, tr.work.hashLimit = 0, 100
				mustLink(t, tr.sortedStorageRefusalMode(&tr.top, n, insertDefault, true))
				before := snapshotHashBudgetTree(tr)
				tr.work.hashUsed, tr.work.hashLimit = 0, wantCharge-1
				if err := tr.sortedStorageRefusalMode(&tr.top, n, insertDefault, true); !errors.Is(err, yang.ErrBudget) {
					t.Fatalf("short preflight budget: %v", err)
				}
				if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
					t.Fatal("refused preflight changed destination")
				}
				tr.work.hashErr, tr.work.hashLimit = nil, 0
				mustLink(t, tr.Insert(nil, n))
				checkTreeRetainedRB(t, tr)
				opidxAssertTreeInvariant(t, tr)
			})
		}
	}
}

// All changed mutation paths run under both journal modes. Refuse every observed reservation
// boundary, then force rollback after an otherwise successful mutation. Snapshots include
// registration history, every metadata slot, RB links, flags and derived occurrence indexes.
func TestRunAuditRollback(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, mode := range []string{"whole", "narrow"} {
		for _, operation := range []string{"cold", "leader", "batch-leader", "null-leader", "batch-null-leader", "separator", "batch-separator"} {
			t.Run(mode+"/"+operation, func(t *testing.T) {
				build := func() (*Tree, *Node, func() error) {
					if operation == "cold" {
						tr, c := runAuditSplit(t, set, 16)
						return tr, c, func() error { _, err := tr.NewTerm(c, "ll", "60", NewOptions{}); return err }
					}
					if operation == "leader" || operation == "batch-leader" || operation == "null-leader" || operation == "batch-null-leader" {
						tr, c := runTree(t, set)
						for _, v := range []string{"1", "2", "3", "4"} {
							if _, err := tr.NewTerm(c, "ll", v, NewOptions{}); err != nil {
								t.Fatal(err)
							}
						}
						if operation == "null-leader" || operation == "batch-null-leader" {
							copyTree := newTree(set)
							var err error
							c, _, err = copyTree.Dup(c, nil, DupOptions{Recursive: true, WithFlags: true})
							if err != nil {
								t.Fatal(err)
							}
							tr = copyTree
						}
						first := c.kids.docFirst()
						sep := runNode(t, set, c, "zz")
						p := c.kids.physicalPlacement(sep, c.kids.At(1))
						mustLink(t, tr.linkPlaced(c, &c.kids, sep, p))
						return tr, c, func() error {
							if operation == "batch-leader" || operation == "batch-null-leader" {
								return tr.unlinkAll([]*Node{first})
							}
							return first.Unlink()
						}
					}
					tr, c := runTree(t, set, "ll=4", "ll=2", "zz", "ll=9", "ll=10", "yy", "ll=20", "ll=21")
					for _, value := range []string{"3", "11"} {
						if _, err := tr.NewTerm(c, "ll", value, NewOptions{}); err != nil {
							t.Fatal(err)
						}
					}
					mustLink(t, c.kids.docSpan().At(3).Unlink())
					separator := c.kids.docSpan().At(6)
					return tr, c, func() error {
						if operation == "batch-separator" {
							return tr.unlinkAll([]*Node{separator})
						}
						return separator.Unlink()
					}
				}
				begin := func(tr *Tree) *hashTransaction {
					if mode == "narrow" {
						return beginNarrowTransaction(tr)
					}
					return beginHashTransaction(tr)
				}
				tr, _, apply := build()
				var points []uint64
				tr.work.hashTrace = &points
				j := begin(tr)
				err := apply()
				j.end(&err)
				if err != nil {
					t.Fatal(err)
				}
				checkTreeRetainedRB(t, tr)
				limits := []uint64{0} // zero: apply succeeds, then explicitly roll back
				for _, point := range points {
					if point > 1 {
						limits = append(limits, point-1)
					}
				}
				slices.Sort(limits)
				limits = slices.Compact(limits)
				for _, limit := range limits {
					tr, c, apply := build()
					before, index := snapshotHashBudgetTree(tr), s4IndexImage(&c.kids)
					j := begin(tr)
					tr.work.hashLimit = limit
					err := apply()
					if err == nil {
						err = yang.ErrBudget
					} // also checks recovery after a completed mutation
					j.end(&err)
					if !errors.Is(err, yang.ErrBudget) {
						t.Fatalf("limit %d: %v", limit, err)
					}
					tr.work.hashErr, tr.work.hashLimit = nil, 0
					if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) || index != s4IndexImage(&c.kids) {
						t.Fatalf("limit %d changed state after rollback", limit)
					}
					checkTreeRetainedRB(t, tr)
					opidxAssertTreeInvariant(t, tr)
				}
				t.Logf("%d refusal/rollback limits", len(limits))
			})
		}
	}
}

func TestRunSeparatorOriginRecovery(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	tr, c := runTree(t, set, "ll=4", "ll=2", "b=x", "ll=9", "ll=10", "z=y", "ll=20", "ll=21")
	for _, value := range []string{"3", "11"} {
		if _, err := tr.NewTerm(c, "ll", value, NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	mustLink(t, c.kids.docSpan().At(3).Unlink())
	before := snapshotHashBudgetTree(tr)
	defer tr.work.beginHashWork()()
	sep := c.kids.docSpan().At(6)
	restore, arm, err := tr.linkOrigin(sep)
	if err != nil {
		t.Fatal(err)
	}
	disarm, err := arm(&c.kids)
	if err != nil {
		t.Fatal(err)
	}
	mustLink(t, sep.Unlink())
	restore()
	disarm()
	if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
		t.Fatal("origin recovery lost interior owner state")
	}
	checkTreeRetainedRB(t, tr)
	opidxAssertTreeInvariant(t, tr)
}

// Child insertion uses the destination's existing children table; root insertion above also
// exercises foreign schema dispatch. Both must preflight only the singleton before zz.
func TestRunChildStoragePreflightPiece(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, count := range []int{64, 4096} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			tr, c := runAuditSplit(t, set, count)
			if c.kids.ht == nil {
				t.Fatal("missing children table")
			}
			n := runNode(t, set, c, "ll=60")
			end := tr.work.beginHashWork()
			defer end()
			tr.work.visits = true
			tr.work.Store(0)
			tr.work.hashLimit = 100
			mustLink(t, tr.sortedStorageRefusalMode(&c.kids, n, insertDefault, true))
			wantWork, wantCharge := int64(75), uint64(39)
			if count == 4096 {
				wantWork, wantCharge = 147, 63
			}
			if tr.work.Load() != wantWork || tr.work.hashUsed != wantCharge {
				t.Fatalf("child preflight work/charge=%d/%d, want %d/%d", tr.work.Load(), tr.work.hashUsed, wantWork, wantCharge)
			}
			tr.work.hashLimit = 0
			mustLink(t, tr.Insert(c, n))
			checkTreeRetainedRB(t, tr)
			opidxAssertTreeInvariant(t, tr)
		})
	}
}

func TestRunColdCreationRefusalUnjournaled(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	full, c := runAuditSplit(t, set, 16)
	var points []uint64
	full.work.hashTrace = &points
	if _, err := full.NewTerm(c, "ll", "60", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	checkTreeRetainedRB(t, full)
	slices.Sort(points)
	points = slices.Compact(points)
	refused := 0
	for _, point := range points {
		if point <= 1 {
			continue
		}
		tr, c := runAuditSplit(t, set, 16)
		before, index := snapshotHashBudgetTree(tr), s4IndexImage(&c.kids)
		tr.work.hashLimit = point - 1
		_, err := tr.NewTerm(c, "ll", "60", NewOptions{})
		if err != nil {
			if !errors.Is(err, yang.ErrBudget) {
				t.Fatal(err)
			}
			refused++
			tr.work.hashErr, tr.work.hashLimit = nil, 0
			if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) || index != s4IndexImage(&c.kids) {
				t.Fatalf("limit %d published a refused cold creation", point-1)
			}
		}
		checkTreeRetainedRB(t, tr)
		opidxAssertTreeInvariant(t, tr)
	}
	if refused == 0 {
		t.Fatal("no cold creation was refused")
	}
	t.Logf("%d unjournaled refusal boundaries", refused)
}

// Preserving opaque successors must not turn opaque/leaf/opaque into a schema-run join.
// The existing retained tree is unrelated; only the rank and point-removal work is needed.
func TestRunOpaqueNeighboursDoNotJoinSchemaRun(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, count := range []int{64, 4096} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			tr, c := runTree(t, set, "ll=1", "ll=2")
			if _, err := tr.NewTerm(c, "ll", "3", NewOptions{}); err != nil {
				t.Fatal(err)
			}
			for range count {
				n := runNode(t, set, c, "zz")
				mustLink(t, tr.linkPlaced(c, &c.kids, n, c.kids.physicalPlacement(n, nil)))
			}
			separator := runNode(t, set, c, "b=x")
			mustLink(t, tr.linkPlaced(c, &c.kids, separator, c.kids.physicalPlacement(separator, nil)))
			tail := runNode(t, set, c, "yy")
			mustLink(t, tr.linkPlaced(c, &c.kids, tail, c.kids.physicalPlacement(tail, nil)))
			before := snapshotHashBudgetTree(tr)
			defer tr.work.beginHashWork()()
			tr.work.visits = true
			tr.work.Store(0)
			tr.work.hashLimit = 128
			plan, err := prepareUnlink(separator)
			if err != nil {
				t.Fatal(err)
			}
			if plan.left != nil {
				t.Fatal("opaque neighbours classified as a schema-run join")
			}
			if tr.work.Load() > 128 {
				t.Fatalf("scanned unrelated opaque run: %d", tr.work.Load())
			}
			work, charge := tr.work.Load(), tr.work.hashUsed
			tr.work.visits = false
			if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
				t.Fatal("preflight changed state")
			}
			t.Logf("n=%d work=%d charge=%d", count, work, charge)
			tr.work.hashLimit = 0
			mustLink(t, separator.Unlink())
			checkTreeRetainedRB(t, tr)
			opidxAssertTreeInvariant(t, tr)
		})
	}
}
