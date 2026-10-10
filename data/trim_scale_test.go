// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

// trimLinkedTree makes count top-level referrers and one target, in either top-level order.
func trimLinkedTree(t *testing.T, count int, targetFirst bool) (*Tree, *Node, []*Node) {
	t.Helper()
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	target := f.term(t, f.z, "target")
	tr.insert(c, target, insertDefault)
	if targetFirst {
		tr.insert(nil, c, insertLast)
	}

	lrefs := make([]*Node, 0, count)
	refSchema := f.first // module a sorts before the target's module b
	if targetFirst {
		refSchema = f.top // after b:c in schema order
	}
	for i := range count {
		lref := f.term(t, refSchema, fmt.Sprint(i))
		tr.insert(nil, lref, insertLast)
		linkLeafrefNode(target, lref)
		lrefs = append(lrefs, lref)
	}
	if !targetFirst {
		tr.insert(nil, c, insertLast)
	}
	if (tr.top.list[0] == c) != targetFirst {
		t.Fatal("fixture did not put target and referrers in the requested order")
	}
	return tr, target, lrefs
}

func trimWork(t *testing.T, tr *Tree, expr string, count int) {
	t.Helper()
	gen := tr.top.gen
	tr.work.Store(0)
	tr.work.visits = true
	if diags, err := tr.TrimXPath(expr, XPathOptions{}); err != nil {
		t.Fatal(err, diags)
	}
	if tr.top.gen != gen+1 {
		t.Fatalf("generation changed by %d", tr.top.gen-gen)
	}
	if work := tr.work.Load(); work > 12*int64(count+1) {
		t.Fatalf("trimming %d referrers took %d units of work", count, work)
	}
}

// TestTrimXPathLinearRemoval: false() removes a wide top-level list with one bulk compaction.
// The target precedes all of its referrers, so freeing it first walks each counterpart once.
func TestTrimXPathLinearRemoval(t *testing.T) {
	const count = 2000
	tr, target, lrefs := trimLinkedTree(t, count, true)
	trimWork(t, tr, "false()", count)
	if tr.top.len() != 0 {
		t.Fatalf("%d nodes remain", tr.top.len())
	}
	if target.links != nil {
		t.Fatal("removed subtree kept leafref links")
	}
	for _, lref := range lrefs {
		if lref.links != nil {
			t.Fatal("removed leafref kept links")
		}
	}
}

// TestTrimXPathReferrersBeforeTarget covers the formerly quadratic order: every referrer is
// freed before the target whose counterpart array it leaves.
func TestTrimXPathReferrersBeforeTarget(t *testing.T) {
	const count = 4000 // the old per-removal array charge exceeded the default XPath budget
	tr, target, lrefs := trimLinkedTree(t, count, false)
	trimWork(t, tr, "false()", count)
	if tr.top.len() != 0 {
		t.Fatalf("%d nodes remain", tr.top.len())
	}
	for _, n := range append(lrefs, target) {
		if n.links != nil {
			t.Fatalf("removed %s kept leafref links", n.Name())
		}
	}
}

// TestTrimXPathDefaultPropagationLinear covers a wide non-presence container whose children
// each become default after one explicit leaf is trimmed. Recomputing the root after every child
// would visit about count²/2 siblings.
func TestTrimXPathDefaultPropagationLinear(t *testing.T) {
	const count = 10_000
	f := newFixture()
	rSchema := &schema.Node{Kind: schema.Container, Name: "r", Module: f.b, Config: true}
	f.b.Top = append(f.b.Top, rSchema)
	tr := newTree(f.set)
	r := newInner(rSchema)
	tr.insert(nil, r, insertDefault)
	for i := range count {
		cSchema := &schema.Node{Kind: schema.Container, Name: fmt.Sprintf("c%d", i), Module: f.b, Parent: rSchema, Config: true}
		keepSchema := &schema.Node{Kind: schema.Leaf, Name: "keep", Module: f.b, Parent: cSchema, Type: f.str, Config: true}
		dropSchema := &schema.Node{Kind: schema.Leaf, Name: "drop", Module: f.b, Parent: cSchema, Type: f.str, Config: true}
		cSchema.Children = []*schema.Node{keepSchema, dropSchema}
		rSchema.Children = append(rSchema.Children, cSchema)
		c := newInner(cSchema)
		keep := f.term(t, keepSchema, "default")
		keep.flags = FlagDefault
		tr.insert(c, keep, insertDefault)
		tr.insert(c, f.term(t, dropSchema, "explicit"), insertDefault)
		tr.insert(r, c, insertDefault)
	}

	tr.work.Store(0)
	tr.work.visits = true
	if diags, err := tr.TrimXPath("/b:r/*/keep", XPathOptions{}); err != nil {
		t.Fatal(err, diags)
	}
	if r.flags&FlagDefault == 0 {
		t.Fatal("root container did not become default")
	}
	if work := tr.work.Load(); work > 30*count {
		t.Fatalf("default propagation took %d units of work for %d containers", work, count)
	}
	for c := range r.Children() {
		if c.flags&FlagDefault == 0 || c.kids.len() != 1 || c.kids.list[0].Name() != "keep" {
			t.Fatal("trimmed container did not retain only its default leaf")
		}
	}
}

// TestTrimXPathRetainedTarget covers the adversarial batch from #208: a retained target has a
// large counterpart array, and all of its referrers are removed from it.
func TestTrimXPathRetainedTarget(t *testing.T) {
	const count = 2000
	tr, target, lrefs := trimLinkedTree(t, count, true)
	trimWork(t, tr, "/b:c", count)
	if tr.top.len() != 1 || tr.top.list[0].kids.list[0] != target || target.links != nil {
		t.Fatal("target was not retained without links")
	}
	for _, n := range lrefs {
		if n.links != nil {
			t.Fatal("removed referrer kept links")
		}
	}
}

// denseTrimLinkedTree makes count retained leafrefs that each point to all count targets in a
// subtree. The record slices are filled directly to avoid making fixture construction quadratic
// in linkLeafrefNode's duplicate checks.
func denseTrimLinkedTree(t *testing.T, count int) (*Tree, *Node, []*Node, []*Node) {
	t.Helper()
	f := newFixture()
	f.first.Kind = schema.LeafList
	f.ll.Type = &schema.Type{Base: schema.Uint32}
	tr := newTree(f.set)
	refs := make([]*Node, 0, count)
	for i := range count {
		n := f.term(t, f.first, fmt.Sprint(i))
		tr.insert(nil, n, insertLast)
		refs = append(refs, n)
	}
	c := newInner(f.c)
	targets := make([]*Node, 0, count)
	for i := range count {
		n := f.term(t, f.ll, fmt.Sprint(i))
		tr.insert(c, n, insertLast)
		targets = append(targets, n)
	}
	tr.insert(nil, c, insertLast)
	for _, ref := range refs {
		ref.links = &leafrefLinks{targets: append([]*Node(nil), targets...)}
	}
	for _, target := range targets {
		target.links = &leafrefLinks{leafrefs: append([]*Node(nil), refs...)}
	}
	return tr, c, refs, targets
}

// TestTrimXPathDenseLinks covers N retained leafrefs each referencing N targets. A modest graph
// completes with work linear in its link entries; a graph whose cleanup exceeds the step budget
// is rejected during incremental planning, before either the tree or any link record is mutated.
func TestTrimXPathDenseLinks(t *testing.T) {
	t.Run("last-target-work", func(t *testing.T) {
		work := func(count int) int64 {
			t.Helper()
			tr, c, refs, targets := denseTrimLinkedTree(t, count)
			tr.work.Store(0)
			tr.work.visits = true
			expr := fmt.Sprintf("/a:first | /b:c/ll[. != %d]", count-1)
			if diags, err := tr.TrimXPath(expr, XPathOptions{}); err != nil {
				t.Fatal(err, diags)
			}
			if c.kids.len() != count-1 || targets[count-1].links != nil {
				t.Fatal("last target was not removed")
			}
			for _, ref := range refs {
				if ref.links == nil || len(ref.links.targets) != count-1 {
					t.Fatal("retained leafref link record changed unexpectedly")
				}
			}
			return tr.work.Load()
		}

		// Doubling each side makes four times as many link entries. The deterministic work
		// counter must stay linear in those entries; quadratic growth in their number is 16x.
		const count = 40
		wN, w4N := work(count), work(2*count)
		if w4N > 6*wN {
			t.Fatalf("work(4N) = %d, want <= 6 * work(N) = %d", w4N, 6*wN)
		}
	})

	t.Run("first-target", func(t *testing.T) {
		const count = 200
		tr, c, refs, targets := denseTrimLinkedTree(t, count)
		tr.work.Store(0)
		tr.work.visits = true
		if diags, err := tr.TrimXPath("/a:first | /b:c/ll[. != 0]", XPathOptions{}); err != nil {
			t.Fatal(err, diags)
		}
		if work := tr.work.Load(); work > 16*count {
			t.Fatalf("isolated dense-link removal took %d units of work, want O(N)", work)
		}
		if c.kids.len() != count-1 || targets[0].links != nil {
			t.Fatal("isolated target was not removed")
		}
		for _, ref := range refs {
			if ref.links == nil || len(ref.links.targets) != count-1 {
				t.Fatal("retained leafref link record changed unexpectedly")
			}
		}
	})

	t.Run("linear", func(t *testing.T) {
		const count = 40
		tr, _, refs, targets := denseTrimLinkedTree(t, count)
		tr.work.visits = true
		if diags, err := tr.TrimXPath("/a:first", XPathOptions{}); err != nil {
			t.Fatal(err, diags)
		}
		if tr.top.len() != count {
			t.Fatalf("%d top-level nodes remain, want %d", tr.top.len(), count)
		}
		if work := tr.work.Load(); work > 8*count*count {
			t.Fatalf("dense cleanup took %d units of work for %d links", work, count*count)
		}
		for _, n := range append(refs, targets...) {
			if n.links != nil {
				t.Fatalf("removed links left a record on %s", n.Name())
			}
		}
	})

	t.Run("budget", func(t *testing.T) {
		const count = 12
		defer func(b int64) { trimStepBudget = b }(trimStepBudget)
		// This reaches dense-link planning but cannot pay its first few worst-case scans.
		trimStepBudget = 16*count + 8
		tr, c, refs, targets := denseTrimLinkedTree(t, count)
		tr.work.Store(0)
		tr.work.visits = true
		topBefore := tr.top.nodes()
		childrenBefore := c.kids.nodes()
		topGen, childGen := tr.top.gen, c.kids.gen
		linksBefore := make(map[*Node]*leafrefLinks, len(refs)+len(targets))
		for _, n := range append(refs, targets...) {
			linksBefore[n] = n.links
		}
		diags, err := tr.TrimXPath("/a:first", XPathOptions{})
		if !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("TrimXPath error = %v, want ErrBudget (diagnostics %v)", err, diags)
		}
		if work := tr.work.Load(); work > 8*count {
			t.Fatalf("budget planning visited %d records for %d-by-%d links", work, count, count)
		}
		if tr.top.gen != topGen || c.kids.gen != childGen ||
			!reflect.DeepEqual(tr.top.nodes(), topBefore) || !reflect.DeepEqual(c.kids.nodes(), childrenBefore) {
			t.Fatal("budget failure changed the tree")
		}
		for _, ref := range refs {
			if ref.links != linksBefore[ref] || ref.links == nil || !slices.Equal(ref.links.targets, targets) {
				t.Fatalf("link record for %s changed", ref.Name())
			}
		}
		for _, target := range targets {
			if target.links != linksBefore[target] || target.links == nil || !slices.Equal(target.links.leafrefs, refs) {
				t.Fatalf("link record for %s changed", target.Name())
			}
		}
	})
}

// TestTrimXPathBudgetStopsSiblingWalk checks that budget exhaustion propagates out of every DFS
// frame instead of visiting the rest of a wide sibling list after no more work can be charged.
func TestTrimXPathBudgetStopsSiblingWalk(t *testing.T) {
	const (
		width   = 5000
		stepCap = int64(128)
	)
	defer func(b int64) { trimStepBudget = b }(trimStepBudget)
	trimStepBudget = stepCap

	f := newFixture()
	rSchema := &schema.Node{Kind: schema.Container, Name: "wide", Module: f.b, Config: true}
	keepSchema := &schema.Node{Kind: schema.Leaf, Name: "keep", Module: f.b, Parent: rSchema, Type: f.str, Config: true}
	rSchema.Children = append(rSchema.Children, keepSchema)
	for i := 1; i < width; i++ {
		rSchema.Children = append(rSchema.Children, &schema.Node{Kind: schema.Leaf, Name: fmt.Sprintf("drop%d", i),
			Module: f.b, Parent: rSchema, Type: f.str, Config: true})
	}
	f.b.Top = append(f.b.Top, rSchema)
	tr := newTree(f.set)
	r := newInner(rSchema)
	tr.insert(nil, r, insertDefault)
	for _, sn := range rSchema.Children {
		tr.insert(r, f.term(t, sn, sn.Name), insertDefault)
	}
	before := slices.Clone(r.kids.list)
	topGen, childGen := tr.top.gen, r.kids.gen

	tr.work.Store(0)
	tr.work.visits = true
	if _, diags, err := tr.EvalXPath("/b:wide/keep", XPathOptions{}); err != nil {
		t.Fatal(err, diags)
	}
	evalVisits := tr.work.Load()

	tr.work.Store(0)
	diags, err := tr.TrimXPath("/b:wide/keep", XPathOptions{})
	if !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("TrimXPath error = %v, want ErrBudget (diagnostics %v)", err, diags)
	}
	if trimVisits := tr.work.Load() - evalVisits; trimVisits < 0 || trimVisits > stepCap+8 {
		t.Fatalf("budget %d exhausted after %d trim sibling visits, want at most %d", stepCap, trimVisits, stepCap+8)
	}
	if tr.top.gen != topGen || r.kids.gen != childGen || !slices.Equal(r.kids.list, before) {
		t.Fatal("budget failure changed the tree")
	}
	for _, n := range before {
		if n.parent != r {
			t.Fatal("budget failure detached a child")
		}
	}
}

// TestTrimXPathBudgetStopsRemovedSubtreeWalk checks that preflighting a whole removed subtree
// does not first copy all of a wide node's children into Node.All's traversal stack.
func TestTrimXPathBudgetStopsRemovedSubtreeWalk(t *testing.T) {
	const (
		width   = 50_000
		stepCap = int64(128)
	)
	defer func(b int64) { trimStepBudget = b }(trimStepBudget)
	trimStepBudget = stepCap

	f := newFixture()
	tr := newTree(f.set)
	first := f.term(t, f.first, "keep")
	c := newInner(f.c)
	tr.insert(nil, first, insertDefault)
	tr.insert(nil, c, insertDefault)
	for i := 0; i < width; i++ {
		tr.insert(c, f.term(t, f.ul, "1"), insertLast)
	}
	topBefore := tr.top.nodes()
	childrenBefore := slices.Clone(c.kids.list)
	topGen, childGen := tr.top.gen, c.kids.gen

	tr.work.Store(0)
	tr.work.visits = true
	if _, diags, err := tr.EvalXPath("/a:first", XPathOptions{}); err != nil {
		t.Fatal(err, diags)
	}
	evalVisits := tr.work.Load()

	tr.work.Store(0)
	diags, err := tr.TrimXPath("/a:first", XPathOptions{})
	if !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("TrimXPath error = %v, want ErrBudget (diagnostics %v)", err, diags)
	}
	trimVisits := tr.work.Load() - evalVisits
	if trimVisits < stepCap/2 || trimVisits > stepCap+8 {
		t.Fatalf("budget %d exhausted after %d trim sibling visits, want near the stepCap", stepCap, trimVisits)
	}
	if tr.top.gen != topGen || c.kids.gen != childGen ||
		!slices.Equal(tr.top.nodes(), topBefore) || !slices.Equal(c.kids.list, childrenBefore) {
		t.Fatal("budget failure changed the tree")
	}
	for _, n := range childrenBefore {
		if n.parent != c {
			t.Fatal("budget failure detached a child")
		}
	}
}

// TestTrimXPathBulkLinksOrder checks that indexed cleanup retains removeValue's swap order: the
// same referrers freed through freeLinks (linear removeValue scans) and through
// bulkLinkCleaner (indexed once the array is long) leave the target's leafref array identical.
func TestTrimXPathBulkLinksOrder(t *testing.T) {
	const count = 4 * bulkLinkIndexMin
	positions := func(target *Node, lrefs []*Node) []int {
		at := map[*Node]int{}
		for i, n := range lrefs {
			at[n] = i
		}
		var out []int
		for _, n := range target.links.leafrefs {
			out = append(out, at[n])
		}
		return out
	}
	_, refTarget, refLrefs := trimLinkedTree(t, count, true)
	_, bulkTarget, bulkLrefs := trimLinkedTree(t, count, true)
	var cleaner bulkLinkCleaner
	for i := 0; i < count; i += 3 { // every third referrer, so swaps move later items forward
		freeLinks(refLrefs[i])
		cleaner.free(bulkLrefs[i])
	}
	want, got := positions(refTarget, refLrefs), positions(bulkTarget, bulkLrefs)
	if len(want) == 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("leafref order after bulk removals %v, want removeValue order %v", got, want)
	}
}

// TestTrimXPathManyReferrersBudget: removing more than 255 referrers of one retained target is
// charged linearly. The preflight's per-side hit count saturates; a wrapping counter re-charged
// the full side every 256 removals (about 228k steps for 4000 referrers instead of about 48k).
func TestTrimXPathManyReferrersBudget(t *testing.T) {
	const count = 4000
	defer func(b int64) { trimStepBudget = b }(trimStepBudget)
	trimStepBudget = 16 * count
	tr, target, lrefs := trimLinkedTree(t, count, true)
	if diags, err := tr.TrimXPath("/b:c", XPathOptions{}); err != nil {
		t.Fatal(err, diags)
	}
	if target.links != nil {
		t.Fatal("retained target kept links to removed referrers")
	}
	for _, n := range lrefs {
		if n.links != nil {
			t.Fatal("removed referrer kept links")
		}
	}
}
