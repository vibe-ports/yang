// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
)

func placementKeyList(t *testing.T) (*Tree, *Node) {
	t.Helper()
	tr, ds, err := parseJSONString(pv2Set(t, "pv2-sort-keys"), `{"pv2-sort-keys:c":{"l":[{"a":"A","b":"B"}]}}`, Reject, true)
	if err != nil {
		t.Fatal(err, ds)
	}
	return tr, tr.top.first().kids.At(0)
}

func placeKeyChild(t *testing.T, tr *Tree, list, child, before *Node) {
	t.Helper()
	p := list.kids.physicalPlacement(child, before)
	bound, err := tr.linkPlacedBound(list, &list.kids, child, p)
	if err != nil {
		t.Fatal(err)
	}
	mustLink(t, tr.reserveHash(bound))
	mustLink(t, tr.linkPlaced(list, &list.kids, child, p))
}

// A complete declared prefix permits a hash event even when its triggering repeated key
// follows an opaque child. Only the contiguous physical key run is hashed by lyd_hash.
func TestSiblingPlacementRepeatedKeyAfterOpaque(t *testing.T) {
	for _, journal := range []string{"none", "whole", "narrow"} {
		t.Run(journal, func(t *testing.T) {
			tr, list := placementKeyList(t)
			original := list.hash
			placeKeyChild(t, tr, list, newOpaque(opaque{Name: "zz"}), nil)
			var j *hashTransaction
			switch journal {
			case "whole":
				j = beginHashTransaction(tr)
			case "narrow":
				j = beginNarrowTransaction(tr)
			}
			child := modelFreshTerm(t, list, "b", strings.Repeat("R", 4096))
			p := list.kids.physicalPlacement(child, nil)
			bound, err := tr.work.termHashPlacedBound(list, child.schema, child.value, p.docAt)
			if err != nil {
				t.Fatal(err)
			}
			if bound != 0 {
				t.Errorf("reserved %d value units beyond the opaque prefix", bound)
			}
			placeKeyChild(t, tr, list, child, nil)
			if list.keyN != 2 || list.lkeyN != 2 || list.hash != original {
				t.Errorf("physical prefix: keyN=%d lkeyN=%d hash=%#x, want 2/2/%#x", list.keyN, list.lkeyN, list.hash, original)
			}
			if got := snapshotKeys(list).Collect(); len(got) != 2 || got[0].Name() != "a" || got[1].Name() != "b" {
				t.Errorf("snapshot crossed opaque child: %v", got)
			}
			if j != nil {
				err = yang.ErrBudget
				j.end(&err)
				if list.keyN != 2 || list.lkeyN != 2 || list.hash != original || list.kids.Len() != 3 || list.kids.st.interleaved() {
					t.Fatal("rollback did not restore prefix and storage")
				}
			}
		})
	}
}

// Opaque insertion displaces a live snapshot just like any other non-key. Removing it
// exposes keys without a hash event; an unconditional rehash resets to the physical run.
func TestSiblingPlacementKeyPrefixLifecycle(t *testing.T) {
	for _, slot := range []int{0, 1, 2} {
		for _, journaled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%t", slot, journaled), func(t *testing.T) {
				tr, list := placementKeyList(t)
				original := list.hash
				originalKids := list.kids.docNodes()
				var j *hashTransaction
				if journaled {
					j = beginNarrowTransaction(tr)
				}
				opaque := newOpaque(opaque{Name: "zz"})
				var anchor *Node
				if slot < 2 {
					anchor = originalKids[slot]
				}
				placeKeyChild(t, tr, list, opaque, anchor)
				if list.keyN != slot || list.hash != original || len(snapshotKeys(list).Collect()) != 2 {
					t.Fatalf("opaque displacement: keyN=%d hash=%#x", list.keyN, list.hash)
				}
				// Removing a key beyond the separator must neither shrink nor freeze the live run.
				if slot < 2 {
					mustLink(t, preflightUnlink(originalKids[1]))
					mustLink(t, unlink(originalKids[1]))
					if list.keyN != slot {
						t.Fatalf("suffix unlink changed prefix to %d", list.keyN)
					}
					placeKeyChild(t, tr, list, originalKids[1], nil)
				}
				mustLink(t, tr.work.checkRefreshHash(list))
				refreshInstanceHash(&tr.work, list)
				if list.keyN != slot || list.lkeyN != slot || list.lkeyFrozen != nil {
					t.Fatalf("reset crossed opaque: keyN=%d lkeyN=%d", list.keyN, list.lkeyN)
				}
				historical := list.hash
				mustLink(t, preflightUnlink(opaque))
				mustLink(t, unlink(opaque))
				if list.keyN != 2 || list.hash != historical {
					t.Fatal("separator removal lost live prefix or rehashed")
				}
				// A later native key event replaces the historical prefix with the exposed run.
				placeKeyChild(t, tr, list, modelFreshTerm(t, list, "b", "C"), nil)
				if list.keyN != 3 || list.lkeyN != 3 {
					t.Fatal("exposed keys were not hashed")
				}
				if journaled {
					err := yang.ErrBudget
					j.end(&err)
					if list.hash != original || list.keyN != 2 || list.lkeyN != 2 || !reflect.DeepEqual(list.kids.docNodes(), originalKids) {
						t.Fatal("rollback lost key history")
					}
				}
			})
		}
	}
}

func TestSiblingPlacementOpaquePrefixRefusal(t *testing.T) {
	tr, list := placementKeyList(t)
	before := snapshotHashBudgetTree(tr)
	tr.work.hashLimit = tr.work.hashUsed + 1
	n := newOpaque(opaque{Name: "zz"})
	err := tr.linkPlaced(list, &list.kids, n, list.kids.physicalPlacement(n, list.kids.docFirst()))
	if !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("refusal: %v", err)
	}
	if !reflect.DeepEqual(snapshotHashBudgetTree(tr), before) {
		t.Fatal("refused displacement mutated prefix")
	}
}

// Runnable unchanged on origin/main: descending duplicate keys before an opaque tail
// exercise prefix preflight, incremental snapshots, and both journal modes.
func TestSiblingKeyPrefixOrdinaryWork(t *testing.T) {
	set := pv2Set(t, "pv2-sort-keys")
	for _, journaled := range []bool{false, true} {
		var prevVisits int64
		var prevHash uint64
		var prevAllocs float64
		for _, count := range []int{64, 256} {
			t.Run(fmt.Sprintf("%t/%d", journaled, count), func(t *testing.T) {
				var visits int64
				var hash uint64
				allocs := testing.AllocsPerRun(3, func() {
					tr, ds, err := parseJSONString(set, `{"pv2-sort-keys:c":{"l":[{"a":"A","b":"B"}]}}`, Reject, true)
					if err != nil {
						t.Fatal(err, ds)
					}
					list := tr.top.first().kids.At(0)
					mustLink(t, tr.link(list, &list.kids, newOpaque(opaque{Name: "zz"}), -1))
					tr.work.visits = true
					tr.work.Store(0)
					tr.work.hashUsed = 0
					var j *hashTransaction
					if journaled {
						j = beginNarrowTransaction(tr)
					}
					for i := count - 1; i >= 0; i-- {
						mustLink(t, tr.link(list, &list.kids, modelFreshTerm(t, list, "b", fmt.Sprint(i%17)), list.kids.SchemaLen()))
					}
					err = nil
					j.end(&err)
					if err != nil {
						t.Fatal(err)
					}
					if list.keyN != count+2 || list.lkeyN != count+2 {
						t.Fatal("lost repeated keys")
					}
					visits, hash = tr.work.Load(), tr.work.hashUsed
				})
				wantVisits, wantHash := int64(1513), uint64(1337)
				if count == 256 {
					wantVisits, wantHash = 6963, 6292
				}
				if journaled {
					wantHash = 1878
					if count == 256 {
						wantHash = 8369
					}
				}
				if visits != wantVisits || hash != wantHash {
					t.Fatalf("main's work changed: visits=%d hash=%d, want %d/%d", visits, hash, wantVisits, wantHash)
				}
				if prevVisits != 0 && (visits > 6*prevVisits || hash > 6*prevHash || allocs > 6*prevAllocs) {
					t.Fatal("prefix accounting growth regressed")
				}
				t.Logf("n=%d visits=%d hash=%d allocations=%.0f", count, visits, hash, allocs)
				prevVisits, prevHash, prevAllocs = visits, hash, allocs
			})
		}
	}
}
