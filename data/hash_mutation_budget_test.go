// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/types"
)

type hashBudgetSnapshot struct {
	dump  []string
	index map[*Node]string
	sibs  map[*siblings]string
}

func snapshotHashBudgetTree(tr *Tree) hashBudgetSnapshot {
	s := hashBudgetSnapshot{dump: typedDump(tr), index: map[*Node]string{}, sibs: map[*siblings]string{}}
	s.sibs[&tr.top] = snapshotHashBudgetSiblings(&tr.top)
	snapshotHashBudgetRoots(&s, tr.top.nodes())
	return s
}

// snapshotHashBudgetRoots adds the subtrees of roots (top-level or detached) to s.
func snapshotHashBudgetRoots(s *hashBudgetSnapshot, roots []*Node) {
	for _, root := range roots {
		for n := range root.allAny() { // payload descendants and their sibling state too
			s.sibs[&n.kids] = snapshotHashBudgetSiblings(&n.kids)
			if n.any != nil {
				s.sibs[&n.any.children] = snapshotHashBudgetSiblings(&n.any.children)
			}
			sib := n.siblingsOf()
			var order uint64
			var more []uint64
			if sib != nil {
				if r := sib.exactByNode[n]; r != nil {
					order, more = r.order, r.more
				}
			}
			var history []string
			if sib != nil {
				for hash, r := range sib.history[n] {
					history = append(history, fmt.Sprintf("%d:%d:%v", hash, r.order, r.more))
				}
			}
			sort.Strings(history)
			var metadata []string
			for _, m := range n.meta {
				metadata = append(metadata, fmt.Sprintf("%p:%s:%#v", m.mod, m.name, m.value))
			}
			var links []string
			if n.links != nil {
				for _, target := range n.links.targets {
					links = append(links, fmt.Sprintf("target:%p", target))
				}
				for _, ref := range n.links.leafrefs {
					links = append(links, fmt.Sprintf("leafref:%p", ref))
				}
			}
			anyState := ""
			if n.any != nil {
				anyState = fmt.Sprintf("%p:%d", n.any, n.any.children.len())
				if n.any.text != nil {
					anyState += ":" + *n.any.text
				}
				for c := range n.any.children.all() {
					anyState += fmt.Sprintf(":%p/%p/%d", c, c.parent, c.hash)
				}
			}
			// a detached or carried graph's topology and work binding, not only its identity
			var carried []string
			for _, c := range n.carriedRB {
				carried = append(carried, fmt.Sprintf("%p:%s", c.group, snapshotOrder(c.order)))
			}
			s.index[n] = fmt.Sprintf("dtopo=%s ctopo=%v any=%s schema=%p parent=%p tree=%p value=%#v opaque=%#v meta=%v links=%#v flags=%d hash=%d hkey=%v hashed=%t order=%d more=%v history=%v key=%d/%d frozen=%v ok=%t prefix=%d dirty=%t slot=%d rb=%t group=%p detached=%p:%p carried=%v",
				snapshotOrder(n.detachedRB), carried, anyState, n.schema, n.parent, n.tree, n.value, n.opaq, metadata, links, n.flags, n.hash, n.hkey, n.hashed, order, more, history,
				n.keyN, n.lkeyN, n.lkeyFrozen, n.lkeyOK, n.lkeyPrefix, n.lkeyDirty, n.keySlot, n.inRB, n.rbGroup, n.detachedRB, n.detachedGroup, n.carriedRB)
		}
	}
}

func snapshotHashBudgetSiblings(s *siblings) string {
	defer s4Inspect(s.work)() // inspection inside an open state is class C
	var entries []string
	for key, chain := range s.ht {
		var records []string
		for r, count := chain.first, 0; r != nil && count <= chain.count; r, count = r.next, count+1 {
			records = append(records, fmt.Sprintf("%p:%d:%v", r.node, r.order, r.more))
		}
		// The ordered index is derived (built at a chain's first canonical-only move, keyed by
		// the unique registration order): its links and heights must be a valid index of the
		// chain, whether or not it has been built.
		entries = append(entries, fmt.Sprintf("%p:%x:%d=%d:%v:%s", key.s, key.key, key.hash, chain.count, records,
			checkRecordIndex(chain)))
	}
	sort.Strings(entries)
	var leaders []string
	for sn, chain := range s.leaders {
		var records []string
		for r, count := chain.first, 0; r != nil && count <= chain.count; r, count = r.next, count+1 {
			records = append(records, fmt.Sprintf("%p:%d", r.node, r.order))
		}
		leaders = append(leaders, fmt.Sprintf("%p=%v", sn, records))
	}
	sort.Strings(leaders)
	var first, rb, rbOrders, unsorted, occurrence, pending []string
	for sn, n := range s.firstRun {
		first = append(first, fmt.Sprintf("%p=%p", sn, n))
	}
	for n, live := range s.rbTree {
		rb = append(rb, fmt.Sprintf("%p=%t:%p", n, live, s.rbGroups[n]))
	}
	for n, live := range s.unsorted {
		unsorted = append(unsorted, fmt.Sprintf("%p=%t", n, live))
	}
	for group, order := range s.rbOrders {
		var nodes []string
		for n, state := range order.nodes {
			nodes = append(nodes, fmt.Sprintf("%p=%p:%p:%p:%d:%t:%t", n, dirtyOrderDataNode(state.left),
				dirtyOrderDataNode(state.right), dirtyOrderDataNode(state.parent), state.size, state.red, state.active))
		}
		sort.Strings(nodes)
		rbOrders = append(rbOrders, fmt.Sprintf("%p=%p:%v", group, dirtyOrderDataNode(order.root), nodes))
	}
	if s.order != nil {
		for sn, entry := range s.order.entries {
			occurrence = append(occurrence, fmt.Sprintf("%p=%v:%d:%d:%d:%d", sn,
				slices.Collect(s.allInstances(sn)), entry.rank, entry.direction, entry.gaps, entry.rb))
		}
	}
	for sn, order := range s.pending {
		var nodes []string
		for n, state := range order.nodes {
			nodes = append(nodes, fmt.Sprintf("%p=%p:%p:%p:%d:%t:%t", n, state.left, state.right,
				state.parent, state.size, state.red, state.active))
		}
		sort.Strings(nodes)
		pending = append(pending, fmt.Sprintf("%p=%p:%v:%v", sn, order.root, nodes, order.leaders))
	}
	for owner, groups := range s.rbMore {
		rb = append(rb, fmt.Sprintf("more:%p=%v", owner, groups))
	}
	// every ordered metadata slot (an empty primary owner too), dual memberships, topology work
	for owner, group := range s.rbGroups {
		rb = append(rb, fmt.Sprintf("slot0:%p=%p", owner, group))
	}
	for member, groups := range s.rbDual {
		rb = append(rb, fmt.Sprintf("dual:%p=%v", member, groups))
	}
	for group, count := range s.rbDualCount {
		rb = append(rb, fmt.Sprintf("dualcount:%p=%d", group, count))
	}
	for group, order := range s.rbOrders {
		rb = append(rb, fmt.Sprintf("work:%p=%p", group, order.work))
	}
	var contexts []string
	for set, count := range s.contexts {
		contexts = append(contexts, fmt.Sprintf("%p=%d", set, count))
	}
	sort.Strings(contexts)
	sort.Strings(first)
	sort.Strings(rb)
	sort.Strings(rbOrders)
	sort.Strings(unsorted)
	sort.Strings(occurrence)
	sort.Strings(pending)
	// the storage (design 14): its label mode and invariant (the slots of the nodes in order).
	// Handle identities are not compared: an unjournaled relocation's forward recovery relinks
	// its node with a fresh handle (restoreLink), which nothing outside the storage can observe.
	var store []string
	if s.st != nil {
		store = append(store, fmt.Sprintf("labeled=%t:%v", s.st.seq.Labeled(), storeCheckInvariant(s)))
	}
	return fmt.Sprintf("order=%d gen=%d seq=%d work=%p schemaUnordered=%t contexts=%v list=%v opq=%v store=%v ht=%v leaders=%v first=%v rb=%v rbOrders=%v unsorted=%v occurrences=%v pending=%v",
		s.exactOrder, s.gen, s.leaderSeq, s.work, s.schemaUnordered, contexts, s.schemaSpan().Collect(), s.opaqueSpan().Collect(), store, entries, leaders, first, rb, rbOrders, unsorted, occurrence, pending)
}

// checkRecordIndex reports a chain's ordered index (AVL) that is not a balanced search tree of
// exactly its records in chain order with correct heights ("" when valid or unbuilt).
func checkRecordIndex(c *recordChain) string {
	if c.root == nil {
		return ""
	}
	var inOrder []*record
	var walk func(r *record) (int, string)
	walk = func(r *record) (int, string) {
		if r == nil {
			return 0, ""
		}
		lh, bad := walk(r.left)
		if bad != "" {
			return 0, bad
		}
		inOrder = append(inOrder, r)
		rh, bad := walk(r.right)
		if bad != "" {
			return 0, bad
		}
		if h := 1 + max(lh, rh); r.height != h || lh-rh > 1 || rh-lh > 1 {
			return 0, fmt.Sprintf("record %d: height %d, children %d/%d", r.order, r.height, lh, rh)
		}
		return r.height, ""
	}
	if _, bad := walk(c.root); bad != "" {
		return "bad index: " + bad
	}
	i := 0
	for r := c.first; r != nil; r = r.next {
		if i >= len(inOrder) || inOrder[i] != r {
			return fmt.Sprintf("bad index: slot %d differs from the chain", i)
		}
		i++
	}
	if i != len(inOrder) {
		return fmt.Sprintf("bad index: %d indexed, %d chained", len(inOrder), i)
	}
	return ""
}

// snapshotOrder is a retained graph's root, topology and work binding.
func snapshotOrder(order *dirtyOrder) string {
	if order == nil {
		return "nil"
	}
	var nodes []string
	for n, state := range order.nodes {
		nodes = append(nodes, fmt.Sprintf("%p=%p:%p:%p:%d:%t:%t", n, dirtyOrderDataNode(state.left),
			dirtyOrderDataNode(state.right), dirtyOrderDataNode(state.parent), state.size, state.red, state.active))
	}
	sort.Strings(nodes)
	return fmt.Sprintf("%p:%v:work=%p", dirtyOrderDataNode(order.root), nodes, order.work)
}

func dirtyOrderDataNode(n *dirtyOrderNode) *Node {
	if n == nil {
		return nil
	}
	return n.node
}

func checkFindAgainstLinear(t *testing.T, tr *Tree) {
	t.Helper()
	paths := map[string]*Node{}
	duplicates := map[string]bool{}
	for _, root := range tr.top.nodes() {
		for n := range root.All() {
			if n.schema == nil {
				continue
			}
			path := lydPath(tr.set, n, false)
			if paths[path] != nil {
				duplicates[path] = true
			}
			paths[path] = n
		}
	}
	for path, want := range paths {
		if duplicates[path] {
			continue
		}
		got, err := tr.Find(path)
		if err != nil || got != want {
			t.Fatalf("Find(%q) = %p, %v; linear scan found %p", path, got, err, want)
		}
	}
}

// Every successful charge and preflight threshold is retried one unit below
// its boundary. A failed public edit must preserve both visible nodes and
// native registration order, and indexed Find must match a linear tree walk.
func checkEveryHashBudgetPoint(t *testing.T, build func() (*Tree, func() error)) {
	t.Helper()
	full, apply := build()
	var points []uint64
	full.work.hashTrace = &points
	if err := apply(); err != nil {
		t.Fatal("unlimited edit:", err)
	}
	wantSuccess := typedDump(full)
	full.work.hashTrace = nil
	unique := map[uint64]bool{}
	for _, at := range points {
		if at == 0 {
			continue
		}
		limit := at - 1
		if unique[limit] {
			continue
		}
		unique[limit] = true
		tr, edit := build()
		before := snapshotHashBudgetTree(tr)
		tr.work.hashLimit = limit
		err := edit()
		if errors.Is(err, yang.ErrBudget) {
			if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(after, before) {
				t.Fatalf("limit %d used %d points %v: budget failure changed tree or registrations\nbefore=%v\nafter=%v\nindex before=%v\nindex after=%v\nsiblings before=%v\nsiblings after=%v", limit, tr.work.hashUsed, points, before.dump, after.dump, before.index, after.index, before.sibs, after.sibs)
			}
			checkFindAgainstLinear(t, tr)
		} else if err != nil {
			t.Fatalf("limit %d: unexpected edit error: %v", limit, err)
		} else if got := typedDump(tr); !slices.Equal(got, wantSuccess) {
			t.Fatalf("limit %d: successful edit differs from unbounded model\ngot=%v\nwant=%v", limit, got, wantSuccess)
		}
	}
}

// checkEveryHashBudgetPointPair replays every charge boundary independently
// for each tree participating in a public mutation. The full snapshots also
// compare registration order, occurrence indexes, parser pending order, and RB
// group identity; successful trials are checked against an independent model.
func checkEveryHashBudgetPointPair(t *testing.T, build func() ([]*Tree, func() error),
	checkSuccess func(*testing.T, []*Tree)) {
	t.Helper()
	full, apply := build()
	points := make([][]uint64, len(full))
	for i, tr := range full {
		tr.work.hashTrace = &points[i]
	}
	if err := apply(); err != nil {
		t.Fatal("unbounded edit:", err)
	}
	checkSuccess(t, full)
	wantDumps := make([][]string, len(full))
	for i, tr := range full {
		wantDumps[i] = typedDump(tr)
	}
	for tree, trace := range points {
		seen := map[uint64]bool{}
		for _, at := range trace {
			if at == 0 || seen[at-1] {
				continue
			}
			limit := at - 1
			seen[limit] = true
			trial, edit := build()
			before := make([]hashBudgetSnapshot, len(trial))
			for i, tr := range trial {
				before[i] = snapshotHashBudgetTree(tr)
			}
			trial[tree].work.hashLimit = limit
			err := edit()
			switch {
			case errors.Is(err, yang.ErrBudget):
				for i, tr := range trial {
					if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(before[i], after) {
						t.Fatalf("tree %d, charge %d: budget failure changed tree %d\nbefore=%+v\nafter=%+v", tree, limit, i, before[i], after)
					}
					opidxAssertTreeInvariant(t, tr)
				}
			case err != nil:
				t.Fatalf("tree %d, charge %d: %v", tree, limit, err)
			default:
				checkSuccess(t, trial)
				for i, tr := range trial {
					if got := typedDump(tr); !slices.Equal(got, wantDumps[i]) {
						t.Fatalf("tree %d, charge %d: successful tree %d differs from fresh unbounded model\ngot=%v\nwant=%v",
							tree, limit, i, got, wantDumps[i])
					}
				}
			}
		}
	}
}

func TestHashBudgetMoveSubtreePair(t *testing.T) {
	set := storedHashSet(t, "string")
	build := func() ([]*Tree, func() error) {
		src, dst := newTree(set), newTree(set)
		root, err := src.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a", "b", "c"} {
			if _, err := src.NewTerm(root, name, name, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := src.NewTerm(root, "s", "old", NewOptions{}); err != nil {
			t.Fatal(err)
		}
		return []*Tree{src, dst}, func() error { return dst.Insert(nil, root) }
	}
	checkEveryHashBudgetPointPair(t, build, func(t *testing.T, pair []*Tree) {
		t.Helper()
		if pair[0].top.len() != 0 || pair[1].top.len() != 1 {
			t.Fatalf("move source/destination = %d/%d", pair[0].top.len(), pair[1].top.len())
		}
		root := pair[1].top.first()
		for n := range root.All() {
			if n.kids.work != &pair[1].work {
				t.Fatalf("descendant sibling counter still belongs to source: %p", n)
			}
		}
	})
}

func TestMovedSubtreeDoesNotChargeSource(t *testing.T) {
	set := storedHashSet(t, "string")
	src, dst := newTree(set), newTree(set)
	root, err := src.NewInner(nil, "m:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c"} {
		if _, err := src.NewTerm(root, name, name, NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := src.NewTerm(root, "s", "old", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.Insert(nil, root); err != nil {
		t.Fatal(err)
	}
	srcRoot, err := src.NewInner(nil, "m:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	src.work.hashLimit = src.work.hashUsed + 1 // below the next link's storage bound (design 14)
	if _, err := src.NewTerm(srcRoot, "s", "a value", NewOptions{}); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("source quota probe: %v", err)
	}
	if err := src.work.chargeHash(2); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("source sticky charge: %v", err)
	}
	sourceUsed := src.work.hashUsed
	if changed, err := n.SetValue("new"); err != nil || !changed {
		t.Fatalf("destination edit: changed=%t err=%v", changed, err)
	}
	if src.work.hashUsed != sourceUsed || !errors.Is(src.work.hashErr, yang.ErrBudget) {
		t.Fatalf("destination edit changed source counter: used %d -> %d, error %v", sourceUsed,
			src.work.hashUsed, src.work.hashErr)
	}
	if n.hkey.key == "" || !n.hashed {
		t.Fatal("destination index lost the edited exact record")
	}
}

func TestFindChargesStoredHashDiscriminator(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf a { type string; } leaf b { type string; } leaf c { type string; }
    leaf-list s { type string; } } }`)
	tr := newTree(set)
	root, err := tr.NewInner(nil, "m:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	value := strings.Repeat("x", 24<<20)
	if _, err := tr.NewTerm(root, "s", value, NewOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c"} {
		if _, err := tr.NewTerm(root, name, name, NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tr.Find("/m:r/s[.='" + value + "']"); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("Find long indexed leaf-list: %v; want ErrBudget", err)
	}
}

func TestHashBudgetSortBeforeInsert(t *testing.T) {
	model := `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf a { type string; } leaf b { type string; } leaf c { type string; }
    leaf-list s { type string; %s } } }`
	srcSet := opIndexSet(t, fmt.Sprintf(model, "ordered-by user;"))
	dstSet := opIndexSet(t, fmt.Sprintf(model, ""))
	build := func() (*Tree, func() error) {
		src, _, err := parseJSONString(srcSet, `{"m:r":{"a":"a","b":"b","c":"c","s":["z","a"]}}`, Reject, true)
		if err != nil {
			t.Fatal(err)
		}
		dst := newTree(dstSet)
		root, _, err := dst.Dup(src.top.first(), nil, DupOptions{Recursive: true})
		if err != nil {
			t.Fatal(err)
		}
		if root.kids.ht == nil {
			t.Fatal("fixture needs an indexed parent")
		}
		return dst, func() error {
			_, err := dst.NewTerm(root, "s", "middle", NewOptions{})
			return err
		}
	}
	checkEveryHashBudgetPoint(t, build)
}

func TestHashBudgetOpaqueComparedPrefix(t *testing.T) {
	srcSet := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { list l { key a; leaf a { type string; } } } }`)
	dstSet := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { list l { key "a b"; leaf a { type string; } leaf b { type string; } } } }`)
	build := func() ([]*Tree, func() error) {
		src, _, err := parseJSONString(srcSet, `{"m:r":{"l":[{"a":"x"}]}}`, Reject, true)
		if err != nil {
			t.Fatal(err)
		}
		dst := newTree(dstSet)
		root, err := dst.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var list *Node
		for range 4 {
			list, _, err = dst.Dup(src.top.first().kids.At(0), root, DupOptions{Recursive: true})
			if err != nil {
				t.Fatal(err)
			}
		}
		if root.kids.ht == nil {
			t.Fatal("fixture needs indexed list siblings")
		}
		moved := newTree(dstSet)
		n := newOpaque(opaque{Name: "unknown", Value: "long opaque value", Format: types.FormatJSON, set: dstSet})
		if err := moved.link(nil, &moved.top, n, -1); err != nil {
			t.Fatal(err)
		}
		return []*Tree{moved, dst}, func() error { return dst.Insert(list, n) }
	}
	checkEveryHashBudgetPointPair(t, build, func(t *testing.T, pair []*Tree) {
		t.Helper()
		if pair[0].top.len() != 0 || pair[1].top.first().kids.At(3).kids.OpaqueLen() != 1 {
			t.Fatal("opaque move did not reach the copied list")
		}
	})
}

func TestHashBudgetSingletonSetValue(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf a { type string; } leaf b { type string; } leaf c { type string; }
    leaf-list s { type string; } } }`)
	build := func() (*Tree, func() error) {
		tr := newTree(set)
		root, err := tr.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a", "b", "c"} {
			if _, err := tr.NewTerm(root, name, name, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		n, err := tr.NewTerm(root, "s", "old", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return tr, func() error { _, err := n.SetValue("new"); return err }
	}
	checkEveryHashBudgetPoint(t, build)
}

func TestHashBudgetComparedParentSetValue(t *testing.T) {
	srcSet := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { list l { key a; leaf a { type string; } } } }`)
	dstSet := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { list l { key "a b"; leaf a { type string; }
    leaf-list s { type string; } leaf b { type string; } } } }`)
	build := func() ([]*Tree, func() error) {
		src, _, err := parseJSONString(srcSet, `{"m:r":{"l":[{"a":"a"}]}}`, Reject, true)
		if err != nil {
			t.Fatal(err)
		}
		dst := newTree(dstSet)
		root, err := dst.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var list *Node
		for range 4 {
			list, _, err = dst.Dup(src.top.first().kids.At(0), root, DupOptions{Recursive: true})
			if err != nil {
				t.Fatal(err)
			}
		}
		n, err := dst.NewTerm(list, "s", "old", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if root.kids.ht == nil || !list.lkeyOK || positionalChild(list, 1) != n {
			t.Fatal("fixture needs an indexed incomplete list")
		}
		return []*Tree{dst}, func() error { _, err := n.SetValue("new value"); return err }
	}
	checkEveryHashBudgetPointPair(t, build, func(t *testing.T, trees []*Tree) {
		t.Helper()
		root := trees[0].top.first()
		list := root.kids.At(3)
		if list.kids.At(1).Value() != "new value" {
			t.Fatal("successful value change did not reach the compared prefix")
		}
		if want, ok := hashOf(list); !ok || list.hkey != want {
			t.Fatal("successful value change left a stale parent discriminator")
		}
	})
}

func TestParseOpHashBudgetChargePoints(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  rpc r { input { leaf in { type string; } }
    output { leaf p { type string; } leaf-list s { type string; } } } }`)
	for _, tc := range []struct {
		name, request, reply string
		format               Format
	}{
		{"json", `{"m:r":{"in":"keep"}}`, `{"m:p":"parsed","m:s":["value"]}`, FormatJSON},
		{"xml", `<r xmlns="urn:m"><in>keep</in></r>`, `<p xmlns="urn:m">parsed</p><s xmlns="urn:m">value</s>`, FormatXML},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := func() ([]*Tree, func() error) {
				tr, op, _, err := parseOp(context.Background(), strings.NewReader(tc.request), set,
					tc.format, opRPC, nil, Reject)
				if err != nil {
					t.Fatal(err)
				}
				return []*Tree{tr}, func() error {
					_, _, _, err := parseOp(context.Background(), strings.NewReader(tc.reply), set,
						tc.format, opReply, op, Reject)
					return err
				}
			}
			checkEveryHashBudgetPointPair(t, build, func(t *testing.T, trees []*Tree) {
				t.Helper()
				var p, s bool
				for _, n := range trees[0].top.first().kids.SchemaAll() {
					p = p || n.Name() == "p" && n.Value() == "parsed"
					s = s || n.Name() == "s" && n.Value() == "value"
				}
				if !p || !s {
					t.Fatal("successful reply omitted parsed output")
				}
			})
		})
	}
}

func TestValidateImplicitHashBudgetChargePoints(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
	  container r { leaf a { type string; } leaf b { type string; } leaf c { type string; }
	    leaf d { type string; default "v"; } leaf e { type string; default "w"; } } }`)
	build := func() ([]*Tree, func() error) {
		tr := newTree(set)
		root, err := tr.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a", "b", "c"} {
			if _, err := tr.NewTerm(root, name, name, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		return []*Tree{tr}, func() error {
			_, err := tr.Validate(context.Background(), ValidateOptions{Present: true})
			return err
		}
	}
	checkEveryHashBudgetPointPair(t, build, func(t *testing.T, trees []*Tree) {
		t.Helper()
		root := trees[0].top.first()
		if root.kids.SchemaLen() != 5 || root.kids.At(3).Name() != "d" || root.kids.At(4).Name() != "e" {
			t.Fatal("validation did not create the default")
		}
	})
}

func TestValidationPublicVariantsHashBudgetChargePoints(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf a { type string; } leaf b { type string; } leaf c { type string; }
    leaf d { type string; default "v"; } leaf e { type string; default "w"; } } }`)
	for _, variant := range []string{"ValidateModule", "ValidateDiff", "ValidateModuleDiff"} {
		t.Run(variant, func(t *testing.T) {
			build := func() ([]*Tree, func() error) {
				tr := newTree(set)
				root, err := tr.NewInner(nil, "m:r", NewOptions{})
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"a", "b", "c"} {
					if _, err := tr.NewTerm(root, name, name, NewOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				return []*Tree{tr}, func() error {
					switch variant {
					case "ValidateModule":
						_, err := tr.ValidateModule(context.Background(), "m", ValidateOptions{})
						return err
					case "ValidateDiff":
						_, _, err := tr.ValidateDiff(context.Background(), ValidateOptions{Present: true})
						return err
					default:
						_, _, err := tr.ValidateModuleDiff(context.Background(), "m", ValidateOptions{})
						return err
					}
				}
			}
			checkEveryHashBudgetPointPair(t, build, func(t *testing.T, trees []*Tree) {
				t.Helper()
				root := trees[0].top.first()
				if root.kids.SchemaLen() != 5 || root.kids.At(3).Name() != "d" || root.kids.At(4).Name() != "e" {
					t.Fatalf("%s defaults = %v", variant, typedDump(trees[0]))
				}
			})
		})
	}
}

func TestValidateOpHashBudgetChargePoints(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  rpc r { input { leaf a { type string; } leaf b { type string; } leaf c { type string; }
    leaf d { type string; default "v"; } leaf e { type string; default "w"; } } } }`)
	build := func() ([]*Tree, func() error) {
		req, _ := opIndexParseRPC(t, set, `{"m:r":{"a":"a","b":"b","c":"c"}}`)
		oper := newTree(set)
		return []*Tree{req, oper}, func() error {
			_, err := req.ValidateOp(context.Background(), OpRPC, ValidateOpOptions{Operational: oper})
			return err
		}
	}
	checkEveryHashBudgetPointPair(t, build, func(t *testing.T, trees []*Tree) {
		t.Helper()
		if trees[0].top.len() != 1 || trees[0].top.first().kids.len() != 5 || trees[1].top.len() != 0 {
			t.Fatalf("validated operation = %v, dependency = %v", typedDump(trees[0]), typedDump(trees[1]))
		}
	})
}

func TestValidateAutodeleteHashBudgetChargePoints(t *testing.T) {
	srcSet := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { list l { key a; leaf a { type string; }
    leaf d { type string; default "old"; } } } }`)
	dstSet := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf x { type string; } leaf y { type string; } leaf z { type string; }
    list l { key "a b"; leaf a { type string; } leaf b { type string; }
      leaf d { type string; default "old"; } } } }`)
	build := func() ([]*Tree, func() error) {
		src, _, err := parseJSONString(srcSet, `{"m:r":{"l":[{"a":"a"}]}}`, Reject, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := src.Validate(context.Background(), ValidateOptions{Present: true}); err != nil {
			t.Fatal(err)
		}
		dst := newTree(dstSet)
		root, err := dst.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"x", "y", "z"} {
			if _, err := dst.NewTerm(root, name, name, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		list, _, err := dst.Dup(src.top.first().kids.At(0), root, DupOptions{Recursive: true, WithFlags: true})
		if err != nil {
			t.Fatal(err)
		}
		explicit := modelFreshTerm(t, list, "d", "new value")
		explicit.flags = FlagNew
		if err := dst.link(list, &list.kids, explicit, list.kids.SchemaLen()); err != nil {
			t.Fatal(err)
		}
		if root.kids.ht == nil || list.kids.SchemaLen() != 3 || list.kids.At(1).flags&FlagDefault == 0 {
			t.Fatal("fixture needs an indexed default before an explicit leaf")
		}
		return []*Tree{dst}, func() error {
			_, err := dst.Validate(context.Background(), ValidateOptions{Present: true})
			return err
		}
	}
	checkEveryHashBudgetPointPair(t, build, func(t *testing.T, trees []*Tree) {
		t.Helper()
		list := trees[0].top.first().kids.At(3)
		if list.kids.SchemaLen() != 2 || list.kids.At(1).Value() != "new value" {
			t.Fatalf("validation children=%v", typedDump(trees[0]))
		}
		if want, ok := hashOf(list); !ok || list.hkey != want {
			t.Fatal("validation left a stale compared prefix")
		}
	})
}

func TestDupSiblingsListHashBudgetChargePoints(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf a { type string; } leaf b { type string; } leaf c { type string; }
    list l { key k; leaf k { type string; } } } }`)
	build := func() ([]*Tree, func() error) {
		dst, src := newTree(set), newTree(set)
		dr, err := dst.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a", "b", "c"} {
			if _, err := dst.NewTerm(dr, name, name, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		for _, key := range []string{"z", "a"} {
			if _, err := dst.NewList(dr, "l", []string{key}, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		sr, err := src.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{strings.Repeat("x", 64), strings.Repeat("y", 64)} {
			if _, err := src.NewList(sr, "l", []string{key}, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		return []*Tree{dst, src}, func() error {
			_, _, err := dst.DupSiblings(sr.kids.At(0), dr, DupOptions{})
			return err
		}
	}
	checkEveryHashBudgetPointPair(t, build, func(t *testing.T, trees []*Tree) {
		t.Helper()
		if got := trees[0].top.first().kids.SchemaLen(); got != 7 {
			t.Fatalf("duplicate left %d children, want 7", got)
		}
		if got := trees[1].top.first().kids.SchemaLen(); got != 2 {
			t.Fatalf("duplication changed source children: %d", got)
		}
	})
}

func TestMergeDiffPublicVariantsHashBudgetChargePoints(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf a { type string; } leaf b { type string; } leaf c { type string; }
    leaf-list s { type string; } } }`)
	for _, variant := range []string{"MergeDiff", "MergeDiffTree"} {
		t.Run(variant, func(t *testing.T) {
			build := func() ([]*Tree, func() error) {
				before, after := newTree(set), newTree(set)
				root, err := after.NewInner(nil, "m:r", NewOptions{})
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"a", "b", "c"} {
					if _, err := after.NewTerm(root, name, name, NewOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := after.NewTerm(root, "s", "value", NewOptions{}); err != nil {
					t.Fatal(err)
				}
				src, err := Diff(before, after, DiffOptions{})
				if err != nil {
					t.Fatal(err)
				}
				dst := newTree(set)
				return []*Tree{dst, src}, func() error {
					if variant == "MergeDiffTree" {
						return dst.MergeDiffTree(nil, src.top.first(), MergeDiffOptions{})
					}
					return dst.MergeDiff(src, MergeDiffOptions{})
				}
			}
			checkEveryHashBudgetPointPair(t, build, func(t *testing.T, trees []*Tree) {
				t.Helper()
				if trees[0].top.len() != 1 || trees[0].top.first().kids.len() != 4 || trees[1].top.len() != 1 {
					t.Fatalf("%s result = %v", variant, typedDump(trees[0]))
				}
			})
		})
	}
}

func TestPublicHashMutationChargePoints(t *testing.T) {
	set := storedHashSet(t, "string")
	pathSet := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf a { type string; } leaf b { type string; } leaf c { type string; }
    list l { key k; leaf k { type string; } leaf-list s { type string; } } } }`)
	base := func() (*Tree, *Node) {
		tr := newTree(set)
		root, err := tr.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a", "b", "c"} {
			if _, err := tr.NewTerm(root, name, name, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tr.NewTerm(root, "s", "old", NewOptions{}); err != nil {
			t.Fatal(err)
		}
		return tr, root
	}
	source := func(values ...string) (*Tree, *Node) {
		tr := newTree(set)
		root, err := tr.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			if _, err := tr.NewTerm(root, "s", value, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		return tr, root
	}
	cases := map[string]func() (*Tree, func() error){
		"NewInner": func() (*Tree, func() error) {
			tr := newTree(set)
			return tr, func() error { _, err := tr.NewInner(nil, "m:r", NewOptions{}); return err }
		},
		"NewList": func() (*Tree, func() error) {
			tr, root := base()
			return tr, func() error { _, err := tr.NewList(root, "l", []string{"key"}, NewOptions{}); return err }
		},
		"NewOpaque": func() (*Tree, func() error) {
			tr, root := base()
			return tr, func() error { _, err := tr.NewOpaque(root, "x", "v", OpaqueOptions{Module: "m"}); return err }
		},
		"NewList2": func() (*Tree, func() error) {
			tr, root := base()
			return tr, func() error { _, err := tr.NewList2(root, "l", "[k='key']", NewOptions{}); return err }
		},
		"NewTerm": func() (*Tree, func() error) {
			tr, root := base()
			return tr, func() error { _, err := tr.NewTerm(root, "s", "new", NewOptions{}); return err }
		},
		"NewPath": func() (*Tree, func() error) {
			tr, _ := base()
			return tr, func() error { _, err := tr.NewPath("/m:r/l[k='new']", "", NewPathOptions{}); return err }
		},
		"NewPathBatch": func() (*Tree, func() error) {
			tr := newTree(pathSet)
			root, err := tr.NewInner(nil, "m:r", NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"a", "b", "c"} {
				if _, err := tr.NewTerm(root, name, name, NewOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			return tr, func() error {
				_, err := tr.NewPath("/m:r/l[k='key']/s[.='value']", "value", NewPathOptions{})
				return err
			}
		},
		"SetValue": func() (*Tree, func() error) {
			tr, root := base()
			return tr, func() error { _, err := root.kids.At(3).SetValue("new"); return err }
		},
		"SetValueCanonical": func() (*Tree, func() error) {
			tr, root := base()
			return tr, func() error { _, err := root.kids.At(3).SetValueCanonical("new"); return err }
		},
		"Remove": func() (*Tree, func() error) { tr, root := base(); return tr, root.kids.At(3).Remove },
		"TrimXPath": func() (*Tree, func() error) {
			tr, _ := base()
			return tr, func() error { _, err := tr.TrimXPath("/m:r/a", XPathOptions{}); return err }
		},
		"Merge": func() (*Tree, func() error) {
			tr, _ := base()
			src, _ := source("new", "next")
			return tr, func() error { return tr.Merge(src) }
		},
		"Insert": func() (*Tree, func() error) {
			tr, root := base()
			_, srcRoot := source("new")
			return tr, func() error { return tr.Insert(root, srcRoot.kids.At(0)) }
		},
		"Dup": func() (*Tree, func() error) {
			tr, root := base()
			_, srcRoot := source("new")
			return tr, func() error { _, _, err := tr.Dup(srcRoot.kids.At(0), root, DupOptions{}); return err }
		},
		"DupSiblings": func() (*Tree, func() error) {
			tr, root := base()
			_, srcRoot := source("new", "next")
			return tr, func() error { _, _, err := tr.DupSiblings(srcRoot.kids.At(0), root, DupOptions{}); return err }
		},
		"ApplyDiff": func() (*Tree, func() error) {
			tr, _ := base()
			after, root := base()
			if _, err := after.NewTerm(root, "s", "new", NewOptions{}); err != nil {
				t.Fatal(err)
			}
			diff, err := Diff(tr, after, DiffOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return tr, func() error { return tr.ApplyDiff(diff, ApplyDiffOptions{}) }
		},
		"ApplyDiffBatch": func() (*Tree, func() error) {
			tr, _ := base()
			after, root := base()
			for _, value := range []string{"new", "next"} {
				if _, err := after.NewTerm(root, "s", value, NewOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			diff, err := Diff(tr, after, DiffOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return tr, func() error { return tr.ApplyDiff(diff, ApplyDiffOptions{}) }
		},
	}
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) { checkEveryHashBudgetPoint(t, cases[name]) })
	}
}

func TestParseHashBudgetChargePoints(t *testing.T) {
	set := storedHashSet(t, "string")
	input := `{"m:r":{"s":["a","b","c"]}}`
	parse := func(limit uint64, trace *[]uint64) (*Tree, error) {
		tr, _, err := parseWith(context.Background(), strings.NewReader(input), set,
			parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, func(lc *lydCtx) {
				lc.tree.work.hashLimit = limit
				lc.tree.work.hashTrace = trace
			})
		return tr, err
	}
	var points []uint64
	if tr, err := parse(0, &points); err != nil || tr == nil {
		t.Fatalf("unlimited parse: %v", err)
	}
	seen := map[uint64]bool{}
	for _, at := range points {
		if at <= 1 || seen[at] {
			continue
		}
		seen[at] = true
		tr, err := parse(at-1, nil)
		if errors.Is(err, yang.ErrBudget) {
			if tr != nil {
				t.Fatalf("limit %d returned a partial tree", at-1)
			}
		} else if err != nil {
			t.Fatalf("limit %d parse: %v", at-1, err)
		}
	}
}

func TestMergeBatchHashBudgetAtomic(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf a { type string; } leaf b { type string; } leaf c { type string; }
    leaf-list s { type string; } } }`)
	build := func() (*Tree, *Tree, *Node) {
		dst, _, err := parseJSONString(set,
			`{"m:r":{"a":"a","b":"b","c":"c","s":["before","after","middle"]}}`, Reject, false)
		if err != nil {
			t.Fatal(err)
		}
		root := dst.top.first()
		src := newTree(set)
		srcRoot, err := src.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{strings.Repeat("x", 1024), strings.Repeat("y", 1024)} {
			if _, err := src.NewTerm(srcRoot, "s", value, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if root.kids.ht == nil || !root.kids.hasRB(srcRoot.kids.At(0).schema) {
			t.Fatal("fixture needs an indexed sorted destination run")
		}
		return dst, src, root
	}
	full, source, _ := build()
	if err := full.Merge(source); err != nil {
		t.Fatal(err)
	}
	limit := full.work.hashUsed - 1
	dst, src, root := build()
	before := slices.Clone(root.kids.schemaSpan().Collect())
	dst.work.hashLimit = limit
	if err := dst.Merge(src); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("Merge at limit %d: %v; want ErrBudget", limit, err)
	}
	if !slices.Equal(root.kids.schemaSpan().Collect(), before) {
		t.Fatalf("failed Merge published part of its batch: before=%d after=%d used=%d limit=%d", len(before), root.kids.SchemaLen(), dst.work.hashUsed, limit)
	}
	for _, n := range before {
		if n.schema != nil && n.schema.Name == "s" {
			found, err := dst.Find("/m:r/s[.='" + n.value.Canonical() + "']")
			if err != nil || found != n {
				t.Fatalf("Find(%q) = %p, %v; want %p", n.value.Canonical(), found, err, n)
			}
		}
	}
}

func TestInsertBudgetPreservesFirstRegistration(t *testing.T) {
	set := storedHashSet(t, "string")
	src, dst := newTree(set), newTree(set)
	newRoot := func(tr *Tree) *Node {
		root, err := tr.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a", "b", "c"} {
			if _, err := tr.NewTerm(root, name, name, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	srcRoot, dstRoot := newRoot(src), newRoot(dst)
	key := strings.Repeat("k", 256)
	first, err := src.NewList(srcRoot, "l", []string{key}, NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := src.NewList(srcRoot, "l", []string{key}, NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dst.NewList(dstRoot, "l", []string{"other"}, NewOptions{}); err != nil {
		t.Fatal(err)
	}
	if srcRoot.kids.ht == nil || dstRoot.kids.ht == nil {
		t.Fatal("fixture needs both child indexes")
	}
	before := slices.Clone(srcRoot.kids.schemaSpan().Collect())
	firstRecord := srcRoot.kids.exactByNode[first]
	secondRecord := srcRoot.kids.exactByNode[second]
	if firstRecord == nil || secondRecord == nil {
		t.Fatal("fixture lacks exact registrations")
	}
	dst.work.hashLimit = 2*uint64(len(key)) - 1
	if err := dst.Insert(dstRoot, first); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("Insert: %v; want ErrBudget", err)
	}
	if !slices.Equal(srcRoot.kids.schemaSpan().Collect(), before) || first.parent != srcRoot ||
		srcRoot.kids.exactByNode[first] != firstRecord || srcRoot.kids.exactByNode[second] != secondRecord ||
		srcRoot.kids.exactByNode[first].order != firstRecord.order {
		t.Fatal("failed Insert changed source order or registrations")
	}
	got, err := src.Find("/m:r/l[k='" + key + "']")
	if err != nil || got != first {
		t.Fatalf("Find after failed Insert = %p, %v; want %p", got, err, first)
	}
}

func TestRemoveAfterFailedValueHashReindexesCopiedList(t *testing.T) {
	srcSet := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf p { type string; } leaf q { type string; } leaf z { type string; }
    list l { key a; leaf a { type string; } leaf x { type string; } leaf b { type string; } } } }`)
	dstSet := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf p { type string; } leaf q { type string; } leaf z { type string; }
    list l { key "a b"; leaf a { type string; } leaf x { type string; } leaf b { type string; } } } }`)
	src, _, err := parseJSONString(srcSet, `{"m:r":{"l":[{"a":"A","x":"X","b":"`+strings.Repeat("B", 256)+`"}]}}`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	dst := newTree(dstSet)
	root, err := dst.NewInner(nil, "m:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"p", "q", "z"} {
		if _, err := dst.NewTerm(root, name, name, NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	list, _, err := dst.Dup(src.top.first().kids.At(0), root, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if root.kids.ht == nil || list.kids.SchemaLen() != 3 || list.kids.At(1).schema.Name != "x" {
		t.Fatalf("fixture needs an indexed copied prefix: ht=%t children=%d", root.kids.ht != nil, list.kids.SchemaLen())
	}
	x := list.kids.At(1)
	oldKey := list.hkey
	before := slices.Clone(list.kids.schemaSpan().Collect())
	dst.work.hashLimit = 1
	if changed, err := x.SetValue("new"); changed || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("SetValue changed=%t err=%v; want ErrBudget", changed, err)
	}
	if err := x.Remove(); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("Remove with exhausted quota: %v; want ErrBudget", err)
	}
	if !slices.Equal(list.kids.schemaSpan().Collect(), before) || list.hkey != oldKey || x.parent != list {
		t.Fatal("failed Remove changed copied list or its index")
	}
	dst.work.hashLimit = 4096
	if err := x.Remove(); err != nil {
		t.Fatal(err)
	}
	if list.hkey == oldKey {
		t.Fatal("Remove left the compared prefix in its old bucket")
	}
	if want, ok := hashOf(list); !ok || want != list.hkey {
		t.Fatalf("copied list discriminator stale: stored=%v computed=%v ok=%t", list.hkey, want, ok)
	}
	// The same exposed-prefix reindex is reached through TrimXPath's bulk
	// removal path, after it has decided which nodes to free.
	src2, _, err := parseJSONString(srcSet, `{"m:r":{"l":[{"a":"A","x":"X","b":"`+strings.Repeat("B", 256)+`"}]}}`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	dst2 := newTree(dstSet)
	root2, err := dst2.NewInner(nil, "m:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"p", "q", "z"} {
		if _, err := dst2.NewTerm(root2, name, name, NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	list2, _, err := dst2.Dup(src2.top.first().kids.At(0), root2, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	x2, oldKey2 := list2.kids.At(1), list2.hkey
	beforeTrim := snapshotHashBudgetTree(dst2)
	dst2.work.hashLimit = 1
	if _, err := dst2.TrimXPath("/m:r/l/b", XPathOptions{}); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("TrimXPath with insufficient index budget: %v", err)
	}
	if after := snapshotHashBudgetTree(dst2); !reflect.DeepEqual(after, beforeTrim) {
		t.Fatal("failed TrimXPath changed tree or registrations")
	}
	dst2.work.hashLimit = 4096
	if _, err := dst2.TrimXPath("/m:r/l/b", XPathOptions{}); err != nil {
		t.Fatal(err)
	}
	if x2.parent != nil || list2.hkey == oldKey2 {
		t.Fatal("TrimXPath did not reindex the retained copied list")
	}
}

// TestMergeAnyHashBudgetChargePoints fails Merge at every charge point after an
// anydata payload was replaced (mergeMatch, copyAnyValue): the rollback journal
// must restore the old payload, its roots' parents and the any node's flags.
func TestMergeAnyHashBudgetChargePoints(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  anydata d; container r { leaf a { type string; } leaf b { type string; } leaf-list s { type string; } } }`)
	payload := func(tr *Tree, value string) {
		n, _, err := newAnyWork(context.Background(), Budget{}).createAny(tr, set.Implemented("m").Top[0], nil, &value, 0, false, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := tr.insert(nil, n, insertDefault); err != nil {
			t.Fatal(err)
		}
	}
	checkEveryHashBudgetPoint(t, func() (*Tree, func() error) {
		dst, src := newTree(set), newTree(set)
		payload(dst, `{"m:old":"x"}`)
		payload(src, `{"m:new":"y","m:other":"z"}`)
		root, err := src.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a", "b", "s"} {
			if _, err := src.NewTerm(root, name, name, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		return dst, func() error { return dst.Merge(src) }
	})
}
