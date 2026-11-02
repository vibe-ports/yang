// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"math/bits"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
)

// whenTrueOpts parse with every node with a when starting as WhenTrue (LYD_PARSE_WHEN_TRUE), so
// a false when deletes it silently.
var whenTrueOpts = parseOpts{ParseOptions: ParseOptions{Unknown: Opaque, ParseOnly: true, WhenTrue: true}, whenTrue: true}

func parseWhenTrue(t *testing.T, set *schema.Set, in string) *Tree {
	t.Helper()
	tr, _, err := parseWith(context.Background(), strings.NewReader(in), set, whenTrueOpts, parseJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// linkForeign links the children of in's first top-level node, parsed with set, under parent:
// a sibling set of two contexts.
func linkForeign(t *testing.T, tr *Tree, parent *Node, set *schema.Set, in string) {
	t.Helper()
	fb, _, err := parseJSONString(set, in, Reject, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range slices.Collect(fb.top.first().Children()) {
		if err := unlink(n); err != nil {
			t.Fatal(err)
		}
		if err := tr.link(parent, &parent.kids, n, parent.kids.SchemaLen()); err != nil {
			t.Fatal(err)
		}
	}
}

// assertNoTransient: no node is left flagged dead, and every linked schema node is indexed.
func assertNoTransient(t *testing.T, tr *Tree) {
	t.Helper()
	var walk func(s *siblings)
	walk = func(s *siblings) {
		for n := range s.all() {
			if n.flags&flagDead != 0 {
				t.Fatalf("%s: still dead", n.Path())
			}
			if n.schema != nil && tr.schemaLookup(s, n.schema) == nil {
				t.Fatalf("%s: linked, not indexed", n.Path())
			}
			walk(&n.kids)
		}
	}
	walk(&tr.top)
	opidxAssertTreeInvariant(t, tr)
}

func dumpJSON(t *testing.T, tr *Tree) string {
	t.Helper()
	var b strings.Builder
	if err := tr.PrintJSON(&b, PrintOptions{WithDefaults: WDAll}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestWhenDeleteCancelDiffConsistent: a validation with a diff cancelled at every cancellation
// check returns a diff that, applied to the original tree, gives the tree it left: a deletion's
// diff is emitted only once nothing can cancel it (the nested entries are found first).
func TestWhenDeleteCancelDiffConsistent(t *testing.T) {
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	  leaf guard { type string; when "true()"; }
	  container gone { presence p; when "false()"; leaf x { type string; } } }`)
	in := `{"m:guard":"g","m:gone":{"x":"x"}}`
	eachCancelPoint(t, func(ctx *cancelAtContext) {
		tr, orig := parseWhenTrue(t, set, in), parseWhenTrue(t, set, in)
		diff, _, err := tr.ValidateModuleDiff(ctx, "m", ValidateOptions{NotFinal: true})
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("at %d: %v", ctx.at, err)
		}
		if diff != nil {
			if err := orig.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
				t.Fatalf("at %d: apply: %v", ctx.at, err)
			}
		}
		if got, want := dumpJSON(t, orig), dumpJSON(t, tr); got != want {
			t.Fatalf("at %d: the diff gives\n%s\nthe tree is\n%s", ctx.at, got, want)
		}
	})
}

// deadRunModule: g's when reads l, whose instances of context A have a false when; B's instance
// live stays. With defaults, the validation creates defaults too (cancellation points after the
// when pass).
func deadRunModule(when string, defaults bool) string {
	d := ""
	if defaults {
		d = `leaf d1 { type string; default x; } leaf d2 { type string; default y; } leaf-list dl { type string; default a; default b; default c; }`
	}
	return `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	  container c { leaf g { when "../l[k='live' != 'zz']"; type string; }
	  list l { key k; ` + when + ` leaf k { type string; } } ` + d + `
	  leaf f1 { type string; } leaf f2 { type string; } leaf f3 { type string; } } }`
}

// deadRunTree is c of A holding g, three dead instances of l (a retained RB tree) and B's live
// instance: a table level of two contexts.
func deadRunTree(t *testing.T, setA, setB *schema.Set) *Tree {
	t.Helper()
	tr := parseWhenTrue(t, setA, `{"m:c":{"g":"g","l":[{"k":"dead0"},{"k":"dead1"},{"k":"dead2"}],"f1":"1","f2":"2","f3":"3"}}`)
	linkForeign(t, tr, tr.top.first(), setB, `{"m:c":{"l":[{"k":"live"}]}}`)
	return tr
}

// TestWhenDeadCancelSweep: the when pass of a mixed-context level with three consecutive dead
// members of a retained RB tree, cancelled at every cancellation check: whatever it returns, no
// node stays dead or linked and unindexed, and the cancelled pass's completed deletions are
// whole: another validation of the same tree ends where an uncancelled one does.
func TestWhenDeadCancelSweep(t *testing.T) {
	setA, setB := reviewDiffSet(t, deadRunModule(`when "false()";`, false)), reviewDiffSet(t, deadRunModule("", false))
	want := deadRunTree(t, setA, setB)
	if _, err := want.ValidateModule(context.Background(), "m", ValidateOptions{NotFinal: true}); err != nil {
		t.Fatal(err)
	}
	eachCancelPoint(t, func(ctx *cancelAtContext) {
		tr := deadRunTree(t, setA, setB)
		_, err := tr.ValidateModule(ctx, "m", ValidateOptions{NotFinal: true})
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("at %d: %v", ctx.at, err)
		}
		assertNoTransient(t, tr)
		if ctx.at == 0 {
			return
		}
		// another evaluation after the cancellation reads only the tree left
		for _, n := range tr.top.first().kids.schemaSpan().All() {
			if n.Name() == "l" && n.flags&FlagWhenTrue != 0 && ctxOf(n) == setA {
				if got, err := tr.Find("/m:c/l[k='" + n.kids.first().value.Canonical() + "']"); err != nil || got != n {
					t.Fatalf("at %d: Find of a remaining instance: %v %v", ctx.at, got, err)
				}
			}
		}
		if _, err := tr.ValidateModule(context.Background(), "m", ValidateOptions{NotFinal: true}); err != nil {
			t.Fatalf("at %d: again: %v", ctx.at, err)
		}
		assertNoTransient(t, tr)
		if got, want := dumpJSON(t, tr), dumpJSON(t, want); got != want {
			t.Fatalf("at %d: validated again\n%s\nuncancelled\n%s", ctx.at, got, want)
		}
	})
}

// TestWhenDeadCancelSweepDiff: TestWhenDeadCancelSweep through ValidateModuleDiff. Cancelled at
// every check, the returned diff applied to the original tree gives the tree left: each deletion
// is whole (its diff and its unlink), and a cancellation keeps the completed deletions and their
// diff, as any other.
func TestWhenDeadCancelSweepDiff(t *testing.T) {
	setA, setB := reviewDiffSet(t, deadRunModule(`when "false()";`, true)), reviewDiffSet(t, deadRunModule("", true))
	eachCancelPoint(t, func(ctx *cancelAtContext) {
		tr, orig := deadRunTree(t, setA, setB), deadRunTree(t, setA, setB)
		diff, _, err := tr.ValidateModuleDiff(ctx, "m", ValidateOptions{NotFinal: true})
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("at %d: %v", ctx.at, err)
		}
		assertNoTransient(t, tr)
		if diff != nil {
			if err := orig.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
				t.Fatalf("at %d: apply: %v", ctx.at, err)
			}
		}
		if got, want := dumpJSON(t, orig), dumpJSON(t, tr); got != want {
			t.Fatalf("at %d: the diff gives\n%s\nthe tree is\n%s", ctx.at, got, want)
		}
	})
}

// TestWhenDeadLeaderMixedContexts: the when pass deletes A's two instances of l, consecutive
// members of a retained RB tree, one after the other among the live neighbours; in a children
// table of two contexts the next lookup of l finds B's instance, so g's when holds and g stays.
func TestWhenDeadLeaderMixedContexts(t *testing.T) {
	setA, setB := reviewDiffSet(t, deadRunModule(`when "false()";`, false)), reviewDiffSet(t, deadRunModule("", false))
	tr := parseWhenTrue(t, setA, `{"m:c":{"g":"g","l":[{"k":"dead0"},{"k":"dead1"}],"f1":"1","f2":"2","f3":"3"}}`)
	c := tr.top.first()
	linkForeign(t, tr, c, setB, `{"m:c":{"l":[{"k":"live"}]}}`)
	if c.kids.ht == nil || !c.kids.mixedContexts() {
		t.Fatal("fixture: want a table level of two contexts")
	}
	if _, err := tr.ValidateModule(context.Background(), "m", ValidateOptions{NotFinal: true}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for n := range c.Children() {
		names = append(names, n.Name())
	}
	if !slices.Contains(names, "g") || len(names) != 5 { // g, l (B's live), f1-f3
		t.Fatalf("children after validation: %v", names)
	}
	assertNoTransient(t, tr)
}

// TestWhenDeadRootHidesNoForeignRoot: a top level guard_A, x_A, x_B (A's x has a false when):
// the reverse pass deletes x_A, then guard's "/m:x" scans the cached root view, which still
// holds x_A: a deleted root is absent there, so the scan goes on to x_B, as libyang's lookup in
// the tree x_A left, and guard stays. The view is not rebuilt for the deletion.
func TestWhenDeadRootHidesNoForeignRoot(t *testing.T) {
	mod := func(when string) string {
		return `module m { yang-version 1.1; namespace "urn:m"; prefix m;
		  leaf guard { type string; when "/m:x"; } leaf x { type string; ` + when + ` } }`
	}
	setA, setB := reviewDiffSet(t, mod(`when "false()";`)), reviewDiffSet(t, mod(""))
	trA, trB := parseWhenTrue(t, setA, `{"m:guard":"g","m:x":"dead"}`), parseWhenTrue(t, setB, `{"m:x":"live"}`)
	if err := trB.Insert(nil, trA.top.first()); err != nil {
		t.Fatal(err)
	}
	top := func() (got []string) {
		for n := range trB.top.all() {
			got = append(got, n.Name()+"="+n.value.Canonical())
		}
		return got
	}
	if got, want := top(), []string{"guard=g", "x=dead", "x=live"}; !slices.Equal(got, want) {
		t.Fatalf("fixture: top level %v, want %v", got, want)
	}
	if _, err := trB.ValidateModule(context.Background(), "m", ValidateOptions{NotFinal: true}); err != nil {
		t.Fatal(err)
	}
	if got, want := top(), []string{"guard=g", "x=live"}; !slices.Equal(got, want) {
		t.Fatalf("after the when pass %v, want %v", got, want)
	}
}

// TestWhenNestedQueuedDescendant: a WhenTrue container gone whose when is false holds a queued
// leaf q whose when is false too. Deleting gone takes q's entry out of node_when with it (libyang
// asserts there is none, release builds keep a freed node queued), so q is not evaluated or
// deleted again: one diff entry, the queue empty, the tree consistent.
func TestWhenNestedQueuedDescendant(t *testing.T) {
	f := newUnresFixture(t)
	str := &schema.Type{Base: schema.String}
	ef, _ := xpath.Compile("false()", testNS("u"))
	gone := &schema.Node{Kind: schema.Container, Name: "gone", Module: f.m, Config: true, Presence: true}
	q := &schema.Node{Kind: schema.Leaf, Name: "q", Module: f.m, Parent: gone, Type: str, Config: true}
	gone.Children = []*schema.Node{q}
	gone.Whens = []*schema.When{{Src: "false()", ContextNode: gone, Compiled: ef}}
	q.Whens = []*schema.When{{Src: "false()", ContextNode: q, Compiled: ef}}
	f.m.Top = append(f.m.Top, gone)
	vc, _, _ := f.build(t, ValidateOptions{})
	gn := newInner(gone)
	gn.flags = FlagWhenTrue
	qv, _ := types.Store(str, "v", types.FormatJSON, types.JSONHints("string"), nil, q)
	qn := newTerm(q, qv)
	qn.flags = FlagWhenTrue
	mustLink(t, vc.t.insert(gn, qn, insertDefault))
	mustLink(t, vc.t.insert(nil, gn, insertDefault))
	vc.nodeWhen.add(qn) // post-order: q before gone, so the pass (from the end) deletes gone first
	vc.nodeWhen.add(gn)
	var deleted []string
	vc.diff = func(n *Node, op diffOp) error {
		if op == diffDelete {
			deleted = append(deleted, n.Name())
		}
		return nil
	}
	if err := vc.unres(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(deleted, []string{"gone"}) || vc.nodeWhen.len() != 0 || gn.tree != nil || gn.parent != nil {
		t.Fatalf("deleted %v, %d queued, gone linked %v", deleted, vc.nodeWhen.len(), gn.tree != nil)
	}
	if qn.parent != gn || qn.flags&FlagWhenTrue == 0 {
		t.Fatalf("q left its deleted parent or was evaluated: parent %v flags %x", qn.parent, qn.flags)
	}
	assertNoTransient(t, vc.t)
}

// TestWhenRepeatedQueueLinear: g queued k times (the JSON parser queues a node once per
// metadata member) behind a WhenTrue container gone whose when is false: deleting gone first
// builds the position map, then each of g's k entries is resolved and dropped. A drop leaves a
// tombstone and the first live index skips each one once, so the position bookkeeping is linear
// in k, not a scan of g's remaining positions per drop.
func TestWhenRepeatedQueueLinear(t *testing.T) {
	const k = 1 << 14
	f := newUnresFixture(t)
	str := &schema.Type{Base: schema.String}
	et, _ := xpath.Compile("true()", testNS("u"))
	ef, _ := xpath.Compile("false()", testNS("u"))
	g := &schema.Node{Kind: schema.Leaf, Name: "g", Module: f.m, Type: str, Config: true}
	g.Whens = []*schema.When{{Src: "true()", ContextNode: g, Compiled: et}}
	gone := &schema.Node{Kind: schema.Container, Name: "gone", Module: f.m, Config: true, Presence: true}
	gone.Whens = []*schema.When{{Src: "false()", ContextNode: gone, Compiled: ef}}
	f.m.Top = append(f.m.Top, g, gone)
	vc, _, _ := f.build(t, ValidateOptions{})
	gv, _ := types.Store(str, "g", types.FormatJSON, types.JSONHints("string"), nil, g)
	gn := newTerm(g, gv)
	mustLink(t, vc.t.insert(nil, gn, insertDefault))
	gonen := newInner(gone)
	gonen.flags = FlagWhenTrue
	mustLink(t, vc.t.insert(nil, gonen, insertDefault))
	for range k {
		vc.nodeWhen.items = append(vc.nodeWhen.items, gn) // ly_set_add with duplicates
	}
	vc.nodeWhen.add(gonen)
	if err := vc.unres(); err != nil {
		t.Fatal(err)
	}
	if vc.posBuilds != 1 || vc.posReads > 2*k || vc.nodeWhen.len() != 0 || vc.t.top.len() != 2 {
		t.Fatalf("%d position maps, %d position reads for %d entries, %d queued, %d top-level left (c, g)",
			vc.posBuilds, vc.posReads, k, vc.nodeWhen.len(), vc.t.top.len())
	}
}

// TestWhenRootDeletionsKeepView: deleting n top-level roots in one pass keeps one top-level view
// (the deleted roots are read as absent, not rebuilt per deletion), and each later when reads
// the tree the deletions left.
func TestWhenRootDeletionsKeepView(t *testing.T) {
	const n = 64
	var m strings.Builder
	m.WriteString(`module m { yang-version 1.1; namespace "urn:m"; prefix m; leaf cnt { type string; when "count(/m:x) = 0"; }`)
	m.WriteString(` leaf-list x { type uint32; when "false()"; } }`)
	set := reviewDiffSet(t, m.String())
	var in strings.Builder
	in.WriteString(`{"m:cnt":"c","m:x":[`)
	for i := range n {
		if i > 0 {
			in.WriteString(",")
		}
		in.WriteString(strconv.Itoa(i))
	}
	in.WriteString(`]}`)
	tr := parseWhenTrue(t, set, in.String())
	vc := &valCtx{t: tr, log: &logger{set: set}, nodeWhen: &nodeSet{}, nodeTypes: &nodeSet{}, metaTypes: &[]*meta{},
		budget: xpathBudget{ctx: context.Background()}}
	for c := range tr.top.all() {
		vc.nodeWhen.add(c)
	}
	if err := vc.unres(); err != nil {
		t.Fatal(err)
	}
	if got := slices.Collect(tr.Top()); len(got) != 1 || got[0].Name() != "cnt" {
		t.Fatalf("top level after the pass: %d nodes", len(got))
	}
	if vc.topBuilds > 2+bits.Len(n) { // rebuilt only once more than half the view is deleted
		t.Fatalf("%d top-level view builds for %d root deletions", vc.topBuilds, n)
	}
}

// TestWhenRootDeletionsLinearSteps: a gate first, then n roots whose when reads the gate, all
// deleted: each evaluation after a deletion finds the gate at once (the root lookup skips the
// deleted roots as it walks, no filtered copy per evaluation), so the pass's XPath steps grow
// linearly in n.
func TestWhenRootDeletionsLinearSteps(t *testing.T) {
	steps := func(n int) int64 {
		set := reviewDiffSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
		  leaf gate { type string; } leaf-list x { type uint32; when "/m:gate = 'on'"; } }`)
		vals := make([]string, n)
		for i := range vals {
			vals[i] = strconv.Itoa(i)
		}
		tr := parseWhenTrue(t, set, `{"m:gate":"off","m:x":[`+strings.Join(vals, ",")+`]}`)
		vc := &valCtx{t: tr, log: &logger{set: set}, nodeWhen: &nodeSet{}, nodeTypes: &nodeSet{}, metaTypes: &[]*meta{},
			budget: xpathBudget{ctx: context.Background()}}
		for c := range tr.top.all() {
			if c.Name() == "x" {
				vc.nodeWhen.add(c)
			}
		}
		if err := vc.unres(); err != nil {
			t.Fatal(err)
		}
		if got := tr.top.len(); got != 1 {
			t.Fatalf("%d top-level nodes left, want the gate", got)
		}
		return vc.budget.steps
	}
	if small, large := steps(512), steps(4096); large > 10*small {
		t.Fatalf("XPath steps %d for 512 deletions, %d for 4096", small, large)
	}
}

// TestWhenDiffMergeFailureIgnored: the when pass deletes two equal state leaf-list instances
// and recording the second delete fails to merge; lyd_validate_autodel_node_del ignores
// lyd_val_diff_add's result, so both go, the merge diagnostics are logged and the validation
// returns their LY_EINVAL, through both diff APIs (protocol-v2/when-dup-state-delete-*).
func TestWhenDiffMergeFailureIgnored(t *testing.T) {
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	  leaf-list x { config false; type string; when "false()"; } leaf y { type string; } }`)
	for _, module := range []bool{false, true} {
		tr := parseWhenTrue(t, set, `{"m:x":["a","a","b"],"m:y":"y"}`)
		var diff *Tree
		var d []yang.Diagnostic
		var err error
		if module {
			diff, d, err = tr.ValidateModuleDiff(context.Background(), "m", ValidateOptions{NotFinal: true})
		} else {
			diff, d, err = tr.ValidateDiff(context.Background(), ValidateOptions{NotFinal: true, Present: true})
		}
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || len(d) != 2 || d[1].Msg != `Merging operation "delete" failed.` {
			t.Fatalf("module %v: %v %v", module, err, d)
		}
		if got := slices.Collect(tr.Top()); len(got) != 1 || got[0].Name() != "y" {
			t.Fatalf("module %v: %d top-level nodes left, want y", module, len(got))
		}
		if diff == nil || strings.Count(dumpJSON(t, diff), `"delete"`) != 2 {
			t.Fatalf("module %v: diff %v", module, diff)
		}
	}
}

// TestWhenDeleteHeldDescendantLink: B's when pass deletes gone, whose descendant t is the target
// of a leafref r moved into A (a retained cross-tree link), while A's ApplyDiff waits on its
// callback. Freeing t's links would edit r's record, governed by A's suspended state: the
// deletion is refused before it begins (preflightFreeLinks; D-0148), so the validation returns
// the guard's refusal and B keeps gone, rather than a deletion half done.
func TestWhenDeleteHeldDescendantLink(t *testing.T) {
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	  container c { leaf gate { type string; }
	    container gone { presence p; when "../gate = 'on'"; leaf t { type string; } }
	    container rc { presence p; leaf r { type leafref { path "../../gone/t"; } } } } }`)
	set.LeafrefLinking = true
	for _, diff := range []bool{false, true} {
		b := parseWhenTrue(t, set, `{"m:c":{"gate":"on","gone":{"t":"x"},"rc":{"r":"x"}}}`)
		if _, err := b.ValidateModule(context.Background(), "m", ValidateOptions{NotFinal: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := b.LinkLeafrefs(context.Background()); err != nil {
			t.Fatal(err)
		}
		a := parseWhenTrue(t, set, `{"m:c":{"gate":"a"}}`)
		rc, err := b.Find("/m:c/rc")
		if err != nil || rc == nil {
			t.Fatal(rc, err)
		}
		if err := a.Insert(a.top.first(), rc); err != nil {
			t.Fatal(err)
		}
		gate, _ := b.Find("/m:c/gate")
		if _, err := gate.SetValue("off"); err != nil { // gone's when becomes false
			t.Fatal(err)
		}
		target := parseWhenTrue(t, set, `{"m:c":{"gate":"b","rc":{"r":"x"}}}`)
		d, err := Diff(a, target, DiffOptions{})
		if err != nil || d == nil {
			t.Fatal(d, err)
		}
		var refused error
		called, half := false, false
		r := rc.kids.first()
		// every cancellation check looks at B: gone absent while r still has its link is a
		// deletion half done; the check then cancels, which must not mask the refusal
		ctx := &peekContext{Context: context.Background(), err: func() error {
			if g, _ := b.Find("/m:c/gone"); g == nil {
				half = half || r.links != nil && len(r.links.targets) != 0
				return context.Canceled
			}
			return nil
		}}
		err = a.ApplyDiff(d, ApplyDiffOptions{Callback: func(_, _ *Node) error {
			if !called {
				called = true
				if diff {
					_, _, refused = b.ValidateModuleDiff(ctx, "m", ValidateOptions{NotFinal: true})
				} else {
					_, refused = b.ValidateModule(ctx, "m", ValidateOptions{NotFinal: true})
				}
			}
			return nil
		}})
		if half {
			t.Fatalf("diff=%t: a check saw gone deleted with r still linked to t", diff)
		}
		if err != nil || !called {
			t.Fatalf("diff=%t: ApplyDiff %v, called %t", diff, err, called)
		}
		if !s4Refused(refused) {
			t.Fatalf("diff=%t: validation: %v, want the guard's refusal", diff, refused)
		}
		if gone, _ := b.Find("/m:c/gone/t"); gone == nil {
			t.Fatalf("diff=%t: B lost gone", diff)
		}
		if r.links == nil || len(r.links.targets) != 1 {
			t.Fatalf("diff=%t: r's link is gone", diff)
		}
	}
}

// peekContext runs err at every cancellation check.
type peekContext struct {
	context.Context
	err func() error
}

func (c *peekContext) Err() error { return c.err() }

// TestWhenDeleteLinkedDescendantBudget: TestWhenDeleteHeldDescendantLink's trees with A idle,
// B's validation given every hash-work limit until it succeeds: a refusal restores B and r's
// link, a success deletes gone and frees the link, never a half-done deletion.
func TestWhenDeleteLinkedDescendantBudget(t *testing.T) {
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	  container c { leaf gate { type string; }
	    container gone { presence p; when "../gate = 'on'"; leaf t { type string; } }
	    container rc { presence p; leaf r { type leafref { path "../../gone/t"; } } } } }`)
	set.LeafrefLinking = true
	build := func() (*Tree, *Node) {
		b := parseWhenTrue(t, set, `{"m:c":{"gate":"on","gone":{"t":"x"},"rc":{"r":"x"}}}`)
		if _, err := b.ValidateModule(context.Background(), "m", ValidateOptions{NotFinal: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := b.LinkLeafrefs(context.Background()); err != nil {
			t.Fatal(err)
		}
		a := parseWhenTrue(t, set, `{"m:c":{"gate":"a"}}`)
		rc, _ := b.Find("/m:c/rc")
		if err := a.Insert(a.top.first(), rc); err != nil {
			t.Fatal(err)
		}
		gate, _ := b.Find("/m:c/gate")
		if _, err := gate.SetValue("off"); err != nil {
			t.Fatal(err)
		}
		return b, rc.kids.first()
	}
	for limit := uint64(1); ; limit++ {
		if limit > 1<<16 {
			t.Fatal("no limit lets the validation succeed")
		}
		b, r := build()
		b.work.hashLimit = limit
		diff, _, err := b.ValidateModuleDiff(context.Background(), "m", ValidateOptions{NotFinal: true})
		gone, _ := b.Find("/m:c/gone")
		linked := r.links != nil && len(r.links.targets) != 0
		if err == nil {
			if gone != nil || linked || diff == nil {
				t.Fatalf("limit %d: success with gone %v, r linked %v, diff %v", limit, gone != nil, linked, diff != nil)
			}
			return
		}
		if !errors.Is(err, yang.ErrBudget) || gone == nil || !linked || diff != nil {
			t.Fatalf("limit %d: %v, gone %v, r linked %v, diff %v", limit, err, gone != nil, linked, diff != nil)
		}
	}
}
