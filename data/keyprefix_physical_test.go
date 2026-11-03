// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// Recreate Ordered native children only through package-internal placement. Public
// parsing and duplication deliberately keep their ordinary layout in this part.
func physicalKeyList(t *testing.T, module string, members ...[2]string) (*Tree, *Node) {
	t.Helper()
	saved := overdraftPanics
	overdraftPanics = true
	t.Cleanup(func() { overdraftPanics = saved })
	set := pv2Set(t, module)
	tr := newTree(set)
	root, err := tr.NewInner(nil, module+":c", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var sn *schema.Node
	for _, c := range root.schema.Children {
		if c.Name == "l" {
			sn = c
			break
		}
	}
	if sn == nil {
		t.Fatal("missing list schema")
	}
	list := newInner(sn, &tr.work)
	placeKeyChild(t, tr, root, list, nil)
	for _, m := range members {
		var n *Node
		if m[0] == "zz" {
			n = newOpaque(opaque{Name: "zz", Value: m[1]})
		} else {
			n = modelFreshTerm(t, list, m[0], m[1])
		}
		placeKeyChild(t, tr, list, n, nil)
	}
	return tr, list
}

func TestKeyPastTrailingOpaqueReserve(t *testing.T) {
	tr, list := physicalKeyList(t, "pv2-sorted", [2]string{"k", "x"}, [2]string{"zz", "u"})
	original := list.hash
	big := strings.Repeat("y", 1<<20)
	n := modelFreshTerm(t, list, "k", big)
	tr.work.hashLimit = tr.work.hashUsed + 64<<10
	placeKeyChild(t, tr, list, n, nil)
	if list.keyRun() != 1 || list.lkeyN != 1 || list.hash != original {
		t.Fatal("key behind the trailing opaque contributed to the hash")
	}
}

func TestKeysSplitByOpaqueReserve(t *testing.T) {
	tr, list := physicalKeyList(t, "pv2-sort-keys", [2]string{"a", strings.Repeat("x", 1<<20)}, [2]string{"zz", "u"})
	tr.work.hashLimit = tr.work.hashUsed + 64<<10
	placeKeyChild(t, tr, list, modelFreshTerm(t, list, "b", "v"), nil)
	if list.hash != 0 || list.lkeyOK || list.keyRun() != 1 {
		t.Fatal("split keys completed the physical prefix")
	}
}

func TestKeyRunPastOpaque(t *testing.T) {
	tr, list := physicalKeyList(t, "pv2-sorted", [2]string{"k", "x"}, [2]string{"zz", "u"}, [2]string{"k", "y"})
	historical := list.hash
	separator := list.kids.docSpan().At(1)
	mustLink(t, separator.Unlink())
	if list.keyRun() != 2 || list.lkeyN != 1 || list.hash != historical {
		t.Fatal("separator removal rehashed or failed to expose the suffix")
	}
	changed, err := list.kids.docFirst().SetValue("z")
	if err != nil || !changed || list.lkeyN != 2 || list.hash == historical {
		t.Fatalf("next native hash event: changed=%t keys=%d err=%v", changed, list.lkeyN, err)
	}
	opidxAssertTreeInvariant(t, tr)
}

func TestRepeatedKeysPastOpaqueScaling(t *testing.T) {
	var previous uint64
	for _, count := range []uint64{1000, 8000} {
		tr, list := physicalKeyList(t, "pv2-sorted", [2]string{"k", "x"}, [2]string{"zz", "u"})
		before := tr.work.hashUsed
		for range count {
			placeKeyChild(t, tr, list, modelFreshTerm(t, list, "k", "y"), nil)
		}
		perKey := (tr.work.hashUsed - before) / count
		if previous != 0 && perKey > 2*previous {
			t.Fatalf("work per key grew from %d to %d", previous, perKey)
		}
		if list.lkeyN != 1 || list.keyRun() != 1 {
			t.Fatal("repeated keys crossed the opaque separator")
		}
		t.Logf("n=%d hash units per key=%d", count, perKey)
		previous = perKey
	}
}

func TestKeyBookkeepingPastOpaque(t *testing.T) {
	build := func(n int) (*Tree, *Node, *Node) {
		tr, list := physicalKeyList(t, "pv2-sorted", [2]string{"k", "x"}, [2]string{"v", "v"}, [2]string{"zz", "u"})
		v := list.kids.docSpan().At(1)
		for range n {
			placeKeyChild(t, tr, list, modelFreshTerm(t, list, "k", "y"), nil)
		}
		return tr, list.kids.docFirst(), v
	}
	var previous int64
	for _, n := range []int{200, 3200} {
		tr, key, v := build(n)
		before := tr.work.Load()
		mustLink(t, v.Unlink())
		changed, err := key.SetValue("z")
		if err != nil || !changed {
			t.Fatal(changed, err)
		}
		work := tr.work.Load() - before
		if previous != 0 && work > 2*previous {
			t.Fatalf("bookkeeping grew from %d to %d", previous, work)
		}
		t.Logf("n=%d bookkeeping visits=%d", n, work)
		previous = work
	}
	// Sweep each public mutation independently: success of the unlink does not
	// license an earlier successful edit to survive refusal of a later edit.
	checkEveryHashBudgetPoint(t, func() (*Tree, func() error) {
		tr, _, v := build(16)
		return tr, v.Unlink
	})
	checkEveryHashBudgetPoint(t, func() (*Tree, func() error) {
		tr, key, _ := build(16)
		return tr, func() error { _, err := key.SetValue("z"); return err }
	})
}

// A copied list may have lkeyOK even though its destination's declared prefix
// is incomplete. Reproduce that state internally without enabling Ordered Dup.
func TestCopiedListInsertReservesPhysicalPrefix(t *testing.T) {
	saved := overdraftPanics
	overdraftPanics = true
	defer func() { overdraftPanics = saved }()
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
	container c { leaf x { type string; } leaf y { type string; } leaf z { type string; }
	list l { key "a b c"; ordered-by user; leaf a { type string; } leaf b { type string; }
	leaf c { type string; } leaf d { type string; } } } }`)
	tr := newTree(set)
	root, err := tr.NewInner(nil, "m:c", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var sn *schema.Node
	for _, c := range root.schema.Children {
		if c.Name == "l" {
			sn = c
			break
		}
	}
	if sn == nil {
		t.Fatal("missing list schema")
	}
	list := newInner(sn, &tr.work)
	placeKeyChild(t, tr, root, list, nil)
	big := strings.Repeat("v", 1<<20)
	for _, m := range [][2]string{{"a", "A"}, {"zz", "u"}, {"b", big}, {"c", "C"}, {"d", "D"}} {
		if m[0] == "zz" {
			placeKeyChild(t, tr, list, newOpaque(opaque{Name: "zz", Value: m[1]}), nil)
		} else {
			placeKeyChild(t, tr, list, modelFreshTerm(t, list, m[0], m[1]), nil)
		}
	}
	mustLink(t, tr.work.checkRefreshHash(list))
	refreshInstanceHash(&tr.work, list)
	if !list.lkeyOK || list.lkeyN != 1 {
		t.Fatal("fixture did not capture an incomplete copied prefix")
	}
	for _, name := range []string{"x", "y", "z"} {
		if _, err := tr.NewTerm(root, name, name, NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if root.kids.ht == nil {
		t.Fatal("fixture has no outer table")
	}
	oldHash := list.hash
	n := modelFreshTerm(t, list, "b", "s")
	p := list.kids.physicalPlacement(n, list.kids.docSpan().At(3))
	if p.at != 2 || p.docAt != 3 {
		t.Fatalf("fixture position: logical=%d physical=%d", p.at, p.docAt)
	}
	// The physical three-position discriminator still includes old b's MiB.
	// A logical-position reservation would replace it with the new short b,
	// underpaying the reindex and causing an overdraft when the link applies.
	bound, err := tr.linkPlacedBound(list, &list.kids, n, p)
	if err != nil {
		t.Fatal(err)
	}
	mustLink(t, tr.reserveHash(bound))
	mustLink(t, tr.linkPlaced(list, &list.kids, n, p))
	if list.hash != oldHash || list.lkeyN != 1 {
		t.Fatal("incomplete prefix triggered a native hash event")
	}
}

// rb_compare_lists compares the physical first term even if it is a non-key,
// then stops at either side's opaque boundary. Exercise both operand orders.
func TestPhysicalKeyComparison(t *testing.T) {
	for _, slot := range []int{0, 1, 2} {
		tr, a := physicalKeyList(t, "pv2-sort-keys", [2]string{"a", "A"}, [2]string{"b", "B"})
		b := newInner(a.schema, &tr.work)
		placeKeyChild(t, tr, a.parent, b, nil)
		placeKeyChild(t, tr, b, modelFreshTerm(t, b, "a", "A"), nil)
		placeKeyChild(t, tr, b, modelFreshTerm(t, b, "b", "Z"), nil)
		var anchor *Node
		if slot < 2 {
			anchor = a.kids.docSpan().At(slot)
		}
		placeKeyChild(t, tr, a, newOpaque(opaque{Name: "zz"}), anchor)
		for _, pair := range [][2]*Node{{a, b}, {b, a}} {
			t.Run(fmt.Sprintf("slot=%d/reverse=%t", slot, pair[0] == b), func(t *testing.T) {
				cmp, ok := rbCompareLists(nil, pair[0], pair[1])
				if slot == 0 {
					if ok {
						t.Error("opaque first child accepted as a native term")
					}
				} else if !ok || (cmp == 0) != (slot == 1) {
					t.Errorf("comparison %d compatible=%t", cmp, ok)
				}
				if slot == 2 && (cmp < 0) != (pair[0] == a) {
					t.Errorf("comparison operand order lost: %d", cmp)
				}
				if slot < 2 && listKeyProfile(nil, pair[0], pair[1]) {
					t.Error("incomplete physical prefix accepted as a full sort profile")
				}
			})
		}
	}
}

func TestPhysicalKeyConsumers(t *testing.T) {
	tr, list := placementKeyList(t)
	before := compareInstanceKey(list, false, false)
	placeKeyChild(t, tr, list, newOpaque(opaque{Name: "zz"}), list.kids.docLast())
	if got := listPredicate(list); got != "[a='A']" {
		t.Errorf("predicate crossed opaque child: %s", got)
	}
	if got := compareInstanceKey(list, false, false); got == before {
		t.Error("instance key crossed opaque child")
	}
}

func TestPhysicalKeyComparisonBudget(t *testing.T) {
	_, a := physicalKeyList(t, "pv2-sort-keys", [2]string{"a", "A"}, [2]string{"zz", "u"}, [2]string{"b", strings.Repeat("B", 1<<20)})
	_, b := physicalKeyList(t, "pv2-sort-keys", [2]string{"a", "A"}, [2]string{"b", "B"})
	for _, pair := range [][2]*Node{{a, b}, {b, a}} {
		want := uint64(2) + types.CompareCost(pair[0].kids.docFirst().value, pair[1].kids.docFirst().value)
		if got := rbCompareCost(pair[0], pair[1]); got != want {
			t.Fatalf("comparison reserved %d units, want %d", got, want)
		}
		w := &workCounter{hashLimit: want}
		mustLink(t, chargeCompareCost(w, pair[0], pair[1]))
		if w.hashUsed != want {
			t.Fatalf("charge=%d, want %d", w.hashUsed, want)
		}
	}
}

// A hash event behind the inner opaque child still reindexes an outer copied
// list whose compared prefix exposes that inner list. Its long first key must
// be reserved even when the repeated inner key contributes no hash bytes.
func TestPhysicalKeyAncestorReservation(t *testing.T) {
	saved := overdraftPanics
	overdraftPanics = true
	defer func() { overdraftPanics = saved }()
	mod := func(keys string) string {
		return `module m { yang-version 1.1; namespace urn:m; prefix m;
		container r { leaf p { type string; } leaf q { type string; } leaf z { type string; }
		list l { key "` + keys + `"; leaf a { type string; } leaf b { type string; }
		list v { key k; ordered-by user; leaf k { type string; } } } } }`
	}
	srcSet, dstSet := opIndexSet(t, mod("a")), opIndexSet(t, mod("a b"))
	checkEveryHashBudgetPointPair(t, func() ([]*Tree, func() error) {
		src, ds, err := parseJSONString(srcSet, `{"m:r":{"l":[{"a":"`+strings.Repeat("A", 4096)+`","v":[{"k":"old"}]}]}}`, Reject, true)
		if err != nil {
			t.Fatal(err, ds)
		}
		dst, ds, err := parseJSONString(dstSet, `{"m:r":{"p":"p","q":"q","z":"z"}}`, Reject, true)
		if err != nil {
			t.Fatal(err, ds)
		}
		list, _, err := dst.Dup(src.top.first().kids.At(0), dst.top.first(), DupOptions{Recursive: true})
		if err != nil {
			t.Fatal(err)
		}
		inner := list.kids.docLast()
		placeKeyChild(t, dst, inner, newOpaque(opaque{Name: "zz", Value: "u"}), nil)
		if list.keyRun() != 1 || dst.top.first().kids.ht == nil {
			t.Fatal("outer compared prefix is not indexed/incomplete")
		}
		return []*Tree{dst}, func() error {
			n := modelFreshTerm(t, inner, "k", strings.Repeat("S", 16384))
			p := inner.kids.physicalPlacement(n, nil)
			bound, err := dst.linkPlacedBound(inner, &inner.kids, n, p)
			if err != nil {
				return err
			}
			if err := dst.reserveHash(bound); err != nil {
				return err
			}
			return dst.linkPlaced(inner, &inner.kids, n, p)
		}
	}, func(t *testing.T, trees []*Tree) {
		opidxAssertTreeInvariant(t, trees[0])
	})
}

func indexedExposureList(t *testing.T, separator string, size int) (*Tree, *Node, *Node) {
	t.Helper()
	tr, list := physicalKeyList(t, "pv2-sorted", [2]string{"k", strings.Repeat("x", size)}, [2]string{separator, "u"}, [2]string{"k", "y"})
	for range 16 {
		placeKeyChild(t, tr, list, modelFreshTerm(t, list, "k", "y"), nil)
	}
	for _, m := range [][2]string{{"ll", "f"}, {"ll", "g"}, {"z", "z"}} {
		if _, err := tr.NewTerm(list.parent, m[0], m[1], NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if list.parent.kids.ht == nil || !list.hashed {
		t.Fatal("fixture requires an indexed parent")
	}
	return tr, list, list.kids.docSpan().At(1)
}

// The first declared key was complete before removal. Exposing repeated keys
// after it changes the live hash run, but not the positional comparison prefix.
// Neither unlink form may hash the unchanged MiB discriminator inside unpaid
// write credit, and journal rollback must restore the live run and key slots.
func TestIndexedKeyExposureNoOverdraft(t *testing.T) {
	for _, separator := range []string{"zz", "v"} {
		for _, batch := range []bool{false, true} {
			for _, journal := range []string{"none", "whole", "narrow"} {
				t.Run(fmt.Sprintf("%s/batch=%t/%s", separator, batch, journal), func(t *testing.T) {
					tr, list, sep := indexedExposureList(t, separator, 1<<20)
					before := snapshotHashBudgetTree(tr)
					hash, key, record := list.hash, list.hkey, list.parent.kids.exactByNode[list]
					tr.work.hashUsed, tr.work.hashLimit = 0, 64<<10
					var j *hashTransaction
					switch journal {
					case "whole":
						j = beginHashTransaction(tr)
					case "narrow":
						j = beginNarrowTransaction(tr)
					}
					var err error
					if batch {
						err = tr.unlinkAll([]*Node{sep})
					} else {
						err = sep.Unlink()
					}
					mustLink(t, err)
					if list.keyRun() != 18 || list.lkeyN != 1 || list.hash != hash || list.hkey != key || list.parent.kids.exactByNode[list] != record {
						t.Fatal("exposure changed the captured hash or unchanged comparison index")
					}
					if j != nil {
						err = yang.ErrBudget
						j.end(&err)
						if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(before, after) {
							t.Fatal("rollback lost exposure state")
						}
					}
					opidxAssertTreeInvariant(t, tr)
				})
			}
		}
	}
}
