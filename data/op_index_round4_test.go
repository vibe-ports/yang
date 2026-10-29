// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

func opidxRound4CopiedRPC(t *testing.T) (*Tree, *Node) {
	t.Helper()
	return opidxRound4CopiedRPCOf(t, "3,1")
}

func opidxRound4CopiedRPCOf(t *testing.T, values string) (*Tree, *Node) {
	t.Helper()
	module := func(order string) string {
		return `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input { leaf-list v { type uint32; ` + order + ` } } } }`
	}
	source := opIndexSet(t, module("ordered-by user;"))
	target := opIndexSet(t, module(""))
	_, src := opIndexParse(t, source, `{"m:r":{"v":[`+values+`]}}`, opRPC, Reject)
	op, err := dupTo(src, target, nil, dupRecursive, false)
	if err != nil {
		t.Fatal(err)
	}
	return treeOf(op), op
}

func opidxRound4Sequence(s *siblings) []*Node {
	var nodes []*Node
	for h := s.st.seq.First(); h != nil; h = s.st.seq.Next(h) {
		nodes = append(nodes, h.Value)
	}
	return nodes
}

// Sorting a LAST-linked cross-context copy changes both the physical run and
// the operation sequence. markRB must walk the new positions; otherwise a
// later insertion can miss value 1 in the RB membership index.
func TestOpIndexSortPublishesSequenceBeforeMarkRB(t *testing.T) {
	tr, op := opidxRound4CopiedRPC(t)
	if _, err := tr.NewTerm(op, "v", "4", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	_, source := opIndexParse(t, tr.set, `{"m:r":{"v":[5,7]}}`, opRPC, Reject)
	if _, _, err := tr.DupSiblings(source.kids.At(0), op, DupOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.NewTerm(op, "v", "2", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range op.kids.SchemaAll() {
		got = append(got, n.value.Canonical())
	}
	if want := []string{"1", "2", "3", "4", "5", "7"}; !slices.Equal(got, want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
	if ordered := opidxRound4Sequence(&op.kids); !slices.Equal(ordered, op.kids.schemaSpan().Collect()) {
		t.Fatalf("ordered sequence differs from physical children: %v vs %v", ordered, op.kids.schemaSpan().Collect())
	}
}

// An opaque notification child creates the same ordered sequence used by
// operations. A merge splice must rebuild that existing index even though
// notification schema children do not satisfy opChild.
func TestOpIndexNotificationSpliceMaintainsExistingOrder(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  notification n { leaf-list v { type uint32; } } }`)
	dst, notif := opIndexParse(t, set, `{"m:n":{"v":[1,3],"unknown":"opaque"}}`, opNotif, Opaque)
	src, _ := opIndexParse(t, set, `{"m:n":{"v":[5,7]}}`, opNotif, Opaque)
	if notif.kids.order == nil || notif.kids.OpaqueLen() != 1 {
		t.Fatal("fixture needs the ordered index created by an opaque notification child")
	}
	// Notification terms compile as user-ordered. Retained foreign children can
	// nevertheless carry a system-ordered schema at this level; model that
	// otherwise unreachable parent/schema combination without changing the
	// splice and value-mutation paths under test.
	vs := notif.kids.At(0).schema
	vs.UserOrdered = false
	for _, n := range notif.kids.SchemaAll() {
		n.inRB = true
	}
	markRB(&notif.kids, notif.kids.At(0))
	// the run's retained native topology (#242), as lyds_additionally_create_rb_tree builds it
	dst.buildRBOrder(&notif.kids, notif.kids.rbGroups[notif.kids.At(0)], notif.kids.schemaSpan().Collect())
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	entry := notif.kids.order.entries[notif.kids.At(0).schema]
	if entry == nil || entry.count != 4 || !slices.Equal(opidxRound4Sequence(&notif.kids), slices.Concat(notif.kids.schemaSpan().Collect(), notif.kids.opaqueSpan().Collect())) {
		t.Fatalf("splice left a stale notification index: entry=%+v sequence=%v children=%v",
			entry, opidxRound4Sequence(&notif.kids), slices.Concat(notif.kids.schemaSpan().Collect(), notif.kids.opaqueSpan().Collect()))
	}
	one, err := dst.Find("/m:n/v[.='1']")
	if err != nil || one == nil {
		t.Fatalf("Find(v=1) = %v, %v", one, err)
	}
	if err := one.Remove(); err != nil {
		t.Fatal(err)
	}
	three, err := dst.Find("/m:n/v[.='3']")
	if err != nil || three == nil {
		t.Fatalf("Find(v=3) = %v, %v", three, err)
	}
	if rc := dst.changeTerm(three, "6"); rc != "" {
		t.Fatalf("changeTerm: %s", rc)
	}
	var got []string
	for _, n := range notif.kids.SchemaAll() {
		got = append(got, n.value.Canonical())
	}
	if want := []string{"5", "6", "7"}; !slices.Equal(got, want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
	if ordered := opidxRound4Sequence(&notif.kids); !slices.Equal(ordered, slices.Concat(notif.kids.schemaSpan().Collect(), notif.kids.opaqueSpan().Collect())) {
		t.Fatalf("ordered sequence is stale after splice and edits")
	}
}

// Recursive sibling comparison has already checked the data parents. Leaves
// with equal schema identity can therefore compare equal across contexts even
// when their non-data choice/case ancestry differs.
func TestOpIndexKeylessDigestIgnoresChoiceCaseAncestry(t *testing.T) {
	module := func(choice, kase string) string {
		return `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input { list outer { choice ` + choice + ` { case ` + kase + ` { leaf x { type uint32; } } } } } } }`
	}
	// uint32 has a native cross-context duplicate callback; strings fail the
	// subsequent leaf update with LY_ENOTFOUND even when their text matches.
	a := opIndexSet(t, module("ca", "aa"))
	b := opIndexSet(t, module("cb", "bb"))
	for _, set := range []*schema.Set{a, b} {
		for _, mod := range set.Modules {
			mod.Owner = set
		}
	}
	dst, op := opIndexParse(t, a, `{"m:r":{"outer":[{"x":1}]}}`, opRPC, Reject)
	src, srcOp := opIndexParse(t, a, `{"m:r":{}}`, opRPC, Reject)
	_, foreignOp := opIndexParse(t, b, `{"m:r":{"outer":[{"x":1}]}}`, opRPC, Reject)
	outer := foreignOp.kids.At(0)
	if err := outer.Unlink(); err != nil {
		t.Fatal(err)
	}
	if err := src.link(srcOp, &srcOp.kids, outer, 0); err != nil {
		t.Fatal(err)
	}

	candidate := op.kids.At(0)
	left, okLeft := subtreeEquivalenceKey(candidate, &dst.work)
	right, okRight := subtreeEquivalenceKey(outer, &dst.work)
	if !okLeft || !okRight || left != right {
		t.Fatal("equivalent keyless subtrees were separated by their choice/case log paths")
	}
	if !compareSingle(dst, candidate, outer, true) {
		t.Fatal("fixture subtrees are not recursively equal")
	}
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	if got := len(opidxSchemaNodes(op, candidate.schema)); got != 1 {
		t.Fatalf("outer instances after merge = %d, want 1", got)
	}
}

// The position preflight must include link's ordered-index charge. NewTerm has
// no rollback journal, so every budget refusal after a copied run is inspected
// for unchanged sibling order, RB state, and sequence records.
func TestHashBudgetCopiedRPCSortReservesOrderedLink(t *testing.T) {
	runs := []string{"3,1", opidxRound4Descending(40)}
	if raceEnabled && !fzFull {
		runs = runs[:1] // the 40-instance run is swept at every point in the normal suite
	}
	for _, values := range runs {
		testHashBudgetCopiedRPCSort(t, values)
	}
}

// opidxRound4Descending lists n descending values (a copied user-ordered run the first sorted
// insertion reorders whole).
func opidxRound4Descending(n int) string {
	var b strings.Builder
	for i := n; i >= 1; i-- {
		if i != n {
			b.WriteString(",")
		}
		fmt.Fprint(&b, 2*i+1)
	}
	return b.String()
}

func testHashBudgetCopiedRPCSort(t *testing.T, values string) {
	build := func() (*Tree, func() error) {
		tr, op := opidxRound4CopiedRPCOf(t, values)
		return tr, func() error {
			_, err := tr.NewTerm(op, "v", "4", NewOptions{})
			return err
		}
	}
	full, apply := build()
	var points []uint64
	full.work.hashTrace = &points
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	full.work.hashTrace = nil
	seenFailure := false
	points = slices.Clone(points)
	slices.Sort(points)
	for i, point := range slices.Compact(points) {
		// under the race detector every other point (the index charges doubled them); all of
		// them in the normal suite
		if point == 0 || raceEnabled && !fzFull && i%2 == 1 {
			continue
		}
		tr, edit := build()
		before := snapshotHashBudgetTree(tr)
		tr.work.hashLimit = point - 1
		err := edit()
		if !errors.Is(err, yang.ErrBudget) {
			continue
		}
		seenFailure = true
		if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(after, before) {
			t.Fatalf("limit %d changed sibling order or RB/index state\nbefore=%v\nafter=%v",
				point-1, before.sibs, after.sibs)
		}
	}
	if !seenFailure {
		t.Fatal("budget trace exposed no refusal boundary")
	}
}
