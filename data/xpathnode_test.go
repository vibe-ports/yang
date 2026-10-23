// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
)

func TestAnyXPathHashExcludesPayload(t *testing.T) {
	set := corpusSet(t, []string{"../conformance/corpus/any-core/schemas"}, []string{"ac-r6"})
	m := set.Implemented("ac-r6")
	var data, outer *schema.Node
	for _, sn := range m.Top {
		switch sn.Name {
		case "data":
			data = sn
		case "outer":
			outer = sn
		}
	}
	if data == nil || outer == nil {
		t.Fatal("missing any-core schema nodes")
	}
	var value *schema.Node
	for _, sn := range outer.Children {
		if sn.Name == "payload-target" {
			value = sn
		}
	}
	if value == nil {
		t.Fatal("missing value schema")
	}
	tr := newTree(set)
	a := &Node{schema: data, any: &anyValue{}}
	mustLink(t, tr.insert(nil, a, insertDefault))
	mustLink(t, tr.insert(a, newOpaque(opaque{Name: "payload-target", ModuleNS: m.Namespace, Value: "hidden", Format: types.FormatXML, set: set}), insertLast))
	c := newInner(outer)
	mustLink(t, tr.insert(nil, c, insertDefault))
	v, diag := types.Store(value.Type, "real", types.FormatJSON, types.HintString, nil, value)
	if diag != nil {
		t.Fatal(diag)
	}
	// A schema-backed payload is visible to the ordinary child and descendant
	// axes, but lyd_child-based hash lookup still has no children on the any node.
	mustLink(t, tr.insert(a, newTerm(value, v), insertDefault))
	mustLink(t, tr.insert(c, newTerm(value, v), insertDefault))
	if got, ok := (xn{a, set}).LookupChild(xs{value, set}, nil); !ok || len(got) != 0 {
		t.Fatalf("any hash lookup = %v, handled=%t", got, ok)
	}
	if got, ok := (xn{a, set}).FirstChild(xs{value, set}); !ok || got == nil || got.(xn).n.schema != value {
		t.Fatalf("any first-child lookup = %v, handled=%t", got, ok)
	}
	for _, tc := range []struct {
		expr string
		want float64
	}{
		{"count((/ac-r6:data | /ac-r6:outer)/ac-r6:payload-target)", 1},
		{"count(/ac-r6:data/child::node())", 1},
		{"count(/ac-r6:data/descendant::node())", 1},
	} {
		r, diags, err := tr.EvalXPath(tc.expr, XPathOptions{})
		if err != nil || len(diags) != 0 || r.Number != tc.want {
			t.Errorf("%s = %+v, %v, %v; want %v", tc.expr, r, diags, err, tc.want)
		}
	}
}

// Predicate compilation obtains its first list instance through lyd_child_any,
// while the compiled lookup itself uses lyd_child and skips the payload.
func TestAnyXPathPredicateFirstInstanceIncludesPayload(t *testing.T) {
	set := corpusSet(t, []string{"../conformance/corpus/any-core/schemas"}, []string{"ac-lookup"})
	m := set.Implemented("ac-lookup")
	a, c := m.Top[0], m.Top[1]
	l, k := c.Children[0], c.Children[0].Children[0]
	tr := newTree(set)
	an := &Node{schema: a, any: &anyValue{}}
	mustLink(t, tr.insert(nil, an, insertDefault))
	cn := newInner(c)
	mustLink(t, tr.insert(nil, cn, insertDefault))
	var payload *Node
	for _, parent := range []*Node{an, cn} {
		ln := newInner(l)
		v, d := types.Store(k.Type, "x", types.FormatJSON, types.HintString, nil, k)
		if d != nil {
			t.Fatal(d)
		}
		mustLink(t, tr.insert(ln, newTerm(k, v), insertDefault))
		mustLink(t, tr.insert(parent, ln, insertDefault))
		if parent == an {
			payload = ln
		}
	}
	if got, ok := (xn{an, set}).FirstChild(xs{l, set}); !ok || got != (xn{payload, set}) {
		t.Fatalf("first payload list = %v, handled=%t", got, ok)
	}
	if got, ok := (xn{an, set}).LookupChild(xs{l, set}, []string{"x"}); !ok || len(got) != 0 {
		t.Fatalf("hashed payload list = %v, handled=%t", got, ok)
	}
	for _, tc := range []struct {
		expr string
		want float64
	}{
		{"count((/ac-lookup:a | /ac-lookup:c)/ac-lookup:l[k='x'])", 1},
		{"count(/ac-lookup:a/child::node())", 1},
		{"count(/ac-lookup:a/descendant::node())", 2},
	} {
		r, diags, err := tr.EvalXPath(tc.expr, XPathOptions{})
		if err != nil || len(diags) != 0 || r.Number != tc.want {
			t.Errorf("%s = %+v, %v, %v; want %v", tc.expr, r, diags, err, tc.want)
		}
	}
}

func TestAnyXPathCopiedPayloadFirstInstance(t *testing.T) {
	dir := []string{"../conformance/corpus/any-core/schemas"}
	source := corpusSet(t, dir, []string{"ac-lookup"})
	target := corpusSet(t, dir, []string{"ac-lookup"})
	sm, tm := source.Implemented("ac-lookup"), target.Implemented("ac-lookup")
	src := newTree(source)
	a := &Node{schema: sm.Top[0], any: &anyValue{}}
	mustLink(t, src.insert(nil, a, insertDefault))
	l, k := sm.Top[1].Children[0], sm.Top[1].Children[0].Children[0]
	ln := newInner(l)
	v, d := types.Store(k.Type, "x", types.FormatJSON, types.HintString, nil, k)
	if d != nil {
		t.Fatal(d)
	}
	mustLink(t, src.insert(ln, newTerm(k, v), insertDefault))
	mustLink(t, src.insert(a, ln, insertDefault))
	tr := newTree(target)
	dup, _, err := tr.Dup(a, nil, DupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := newInner(tm.Top[1])
	mustLink(t, tr.insert(nil, c, insertDefault))
	tl, tk := tm.Top[1].Children[0], tm.Top[1].Children[0].Children[0]
	other := newInner(tl)
	tv, d := types.Store(tk.Type, "x", types.FormatJSON, types.HintString, nil, tk)
	if d != nil {
		t.Fatal(d)
	}
	mustLink(t, tr.insert(other, newTerm(tk, tv), insertDefault))
	mustLink(t, tr.insert(c, other, insertDefault))
	// a payload has no children table: the evaluator's charged walk finds the payload's l
	if got, ok := (xn{dup, target}).FirstChild(xs{tl, target}); ok {
		t.Fatalf("cross-context first payload list = %v, handled=%t", got, ok)
	}
	r, diags, err := tr.EvalXPath("count((/ac-lookup:a | /ac-lookup:c)/ac-lookup:l[k='x'])", XPathOptions{})
	if err != nil || len(diags) != 0 || r.Number != 1 {
		t.Fatalf("copied payload lookup = %+v, %v, %v; want 1", r, diags, err)
	}
}

// xpTestList builds a list instance of l with the key leaf k = val.
func xpTestList(t *testing.T, tr *Tree, l *schema.Node, val string) *Node {
	t.Helper()
	k := l.Children[0]
	ln := newInner(l)
	v, d := types.Store(k.Type, val, types.FormatJSON, types.HintString, nil, k)
	if d != nil {
		t.Fatal(d)
	}
	mustLink(t, tr.insert(ln, newTerm(k, v), insertDefault))
	return ln
}

func xpTestTop(m *schema.Module, name string) *schema.Node {
	for _, sn := range m.Top {
		if sn.Name == name {
			return sn
		}
	}
	return nil
}

func xpTestCount(t *testing.T, tr *Tree, expr string, want float64) {
	t.Helper()
	r, diags, err := tr.EvalXPath(expr, XPathOptions{})
	if err != nil || len(diags) != 0 || r.Number != want {
		t.Errorf("%s = %+v, %v, %v; want %v", expr, r, diags, err, want)
	}
}

// eval_name_test_with_predicate_get_scnode skips a parent only when it is the found node's
// schema parent by pointer (xpath.c:8239): an equal parent of another context is searched,
// its match abandons hashing and the predicate is evaluated. Fixture
// any-core/xpath-copied-redundant-parent.
func TestAnyXPathRedundantParentAcrossContexts(t *testing.T) {
	dir := []string{"../conformance/corpus/any-core/schemas"}
	source := corpusSet(t, dir, []string{"ac-lookup"})
	target := corpusSet(t, dir, []string{"ac-lookup"})
	sm, tm := source.Implemented("ac-lookup"), target.Implemented("ac-lookup")
	src := newTree(source)
	a := &Node{schema: xpTestTop(sm, "a"), any: &anyValue{}}
	mustLink(t, src.insert(nil, a, insertDefault))
	pc := newInner(xpTestTop(sm, "c"))
	mustLink(t, src.insert(pc, xpTestList(t, src, pc.schema.Children[0], "x"), insertDefault))
	mustLink(t, src.insert(a, pc, insertDefault))
	tr := newTree(target)
	if _, _, err := tr.Dup(a, nil, DupOptions{}); err != nil {
		t.Fatal(err)
	}
	c := newInner(xpTestTop(tm, "c"))
	mustLink(t, tr.insert(nil, c, insertDefault))
	mustLink(t, tr.insert(c, xpTestList(t, tr, c.schema.Children[0], "x"), insertDefault))
	// hashed, [k='x' != 'zz'] would look up k = 'x' (2); evaluated it is false (0)
	xpTestCount(t, tr, "count((/ac-lookup:a/ac-lookup:c | /ac-lookup:c)/ac-lookup:l[k='x' != 'zz'])", 0)
	xpTestCount(t, tr, "count((/ac-lookup:a/ac-lookup:c | /ac-lookup:c)/ac-lookup:l[k='x'])", 2)
	// only the payload c: its l is the found node. An unprefixed key takes the list's module
	// and is hashed (k = 'x', 1); a prefixed one resolves in set->ctx, mod != key->module
	// compares pointers, and the evaluated predicate is false (0). Fixture
	// any-core/xpath-copied-prefixed-key.
	xpTestCount(t, tr, "count(/ac-lookup:a/ac-lookup:c/ac-lookup:l[k='x' != 'zz'])", 1)
	xpTestCount(t, tr, "count(/ac-lookup:a/ac-lookup:c/ac-lookup:l[ac-lookup:k='x' != 'zz'])", 0)
	xpTestCount(t, tr, "count(/ac-lookup:c/ac-lookup:l[ac-lookup:k='x' != 'zz'])", 1)
}

// lyd_find_sibling_schema walks a payload (children_ht NULL) and across contexts compares the
// module and node name only (tree_data_common.c:1127-1135): c/l's first instance under a is the
// payload's top-level l, so the step is hashed and lyd_child(a) has none. Fixture
// any-core/xpath-copied-first-instance-name.
func TestAnyXPathFirstInstanceByNameAcrossContexts(t *testing.T) {
	dir := []string{"../conformance/corpus/any-core/schemas"}
	source := corpusSet(t, dir, []string{"ac-shadow"})
	target := corpusSet(t, dir, []string{"ac-shadow"})
	sm, tm := source.Implemented("ac-shadow"), target.Implemented("ac-shadow")
	src := newTree(source)
	a := &Node{schema: xpTestTop(sm, "a"), any: &anyValue{}}
	mustLink(t, src.insert(nil, a, insertDefault))
	mustLink(t, src.insert(a, xpTestList(t, src, xpTestTop(sm, "l"), "x"), insertDefault))
	tr := newTree(target)
	dup, _, err := tr.Dup(a, nil, DupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := newInner(xpTestTop(tm, "c"))
	mustLink(t, tr.insert(nil, c, insertDefault))
	cl := c.schema.Children[0]
	mustLink(t, tr.insert(c, xpTestList(t, tr, cl, "x"), insertDefault))
	if got, ok := (xn{dup, target}).FirstChild(xs{cl, target}); ok { // declined: the charged walk
		t.Fatalf("cross-context first instance by name = %v, handled=%t", got, ok)
	}
	xpTestCount(t, tr, "count((/ac-shadow:a | /ac-shadow:c)/ac-shadow:l[k='x'])", 1)
	// within one context the walk compares pointers: no instance, no hashing, both match by name
	same := newTree(target)
	sa := &Node{schema: xpTestTop(tm, "a"), any: &anyValue{}}
	mustLink(t, same.insert(nil, sa, insertDefault))
	mustLink(t, same.insert(sa, xpTestList(t, same, xpTestTop(tm, "l"), "x"), insertDefault))
	sc := newInner(xpTestTop(tm, "c"))
	mustLink(t, same.insert(nil, sc, insertDefault))
	mustLink(t, same.insert(sc, xpTestList(t, same, cl, "x"), insertDefault))
	xpTestCount(t, same, "count((/ac-shadow:a | /ac-shadow:c)/ac-shadow:l[k='x'])", 2)
}

// A prefix whose module the payload's context does not implement leaves moveto_m NULL, and a
// JSON name test then inherits the context node's module (xpath.c:8384-8392, 8229-8233).
// Fixture any-core/xpath-copied-module-missing.
func TestAnyXPathMissingModuleInheritsAcrossContexts(t *testing.T) {
	dir := []string{"../conformance/corpus/any-core/schemas"}
	source := corpusSet(t, dir, []string{"ac-shadow"})
	target := corpusSet(t, dir, []string{"ac-shadow", "ac-other"})
	sm := source.Implemented("ac-shadow")
	src := newTree(source)
	a := &Node{schema: xpTestTop(sm, "a"), any: &anyValue{}}
	mustLink(t, src.insert(nil, a, insertDefault))
	p := newInner(xpTestTop(sm, "p"))
	x := p.schema.Children[0]
	v, d := types.Store(x.Type, "v", types.FormatJSON, types.HintString, nil, x)
	if d != nil {
		t.Fatal(d)
	}
	mustLink(t, src.insert(p, newTerm(x, v), insertDefault))
	mustLink(t, src.insert(a, p, insertDefault))
	tr := newTree(target)
	if _, _, err := tr.Dup(a, nil, DupOptions{}); err != nil {
		t.Fatal(err)
	}
	xpTestCount(t, tr, "count(/ac-shadow:a/ac-shadow:p/ac-other:x)", 1)
	xpTestCount(t, tr, "count(/ac-shadow:a/ac-shadow:p/child::ac-other:x)", 1)
	xpTestCount(t, tr, "count(/ac-shadow:a/ac-shadow:p/descendant::ac-other:x)", 0) // by name
}

func TestAnyXPathCopiedOpaqueDescendantContext(t *testing.T) {
	where := []string{"../conformance/corpus/any-core/schemas"}
	source := corpusSet(t, where, []string{"ac", "ac-xpath-ctx"})
	target := corpusSet(t, where, []string{"ac"})
	src := newTree(source)
	a := &Node{schema: source.Implemented("ac").Top[0], any: &anyValue{}}
	mustLink(t, src.insert(nil, a, insertDefault))
	c := newInner(source.Implemented("ac-xpath-ctx").Top[0])
	mustLink(t, src.insert(a, c, insertDefault))
	v := newOpaque(opaque{Name: "v", ModuleNS: "urn:tests:any-core-xpath-ctx", Value: "bad", Format: types.FormatXML, set: source})
	mustLink(t, src.insert(c, v, insertLast))
	tr := newTree(target)
	dup, _, err := tr.Dup(a, nil, DupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	child := dup.any.children.first()
	wrapped := wrap(target, child.kids.first()).(xn)
	sn, _ := wrapped.OpaqueSchema()
	if wrapped.set != source || wrapped.Module() != "ac-xpath-ctx" || sn == nil {
		t.Fatal("opaque descendant lost its source context")
	}
	r, diags, err := tr.EvalXPath("count(/ac:data/descendant::node())", XPathOptions{})
	if err != nil || len(diags) != 0 || r.Number != 2 {
		t.Fatalf("copied opaque descendants = %+v, %v, %v; want 2", r, diags, err)
	}
}

// TestLookupChild: what the children index lookup answers is what a scan of Children selects (the
// evaluator's fallback): instances by value or all of them, nothing for an absent value, dead
// nodes skipped. It answers from the hash table only and declines whatever needs a scan: no
// table, and a miss while an opaque child of the name could be the fallback.
func TestLookupChild(t *testing.T) {
	f := newUnresFixture(t)
	scan := func(p xn, sn xpath.SchemaNode, vals []string) []xpath.Node {
		var out, named []xpath.Node
		for _, c := range p.Children() {
			switch {
			case c.Schema() == sn && (vals == nil || c.Value().String() == vals[0]):
				out = append(out, c)
			case c.Schema() == nil && c.Name() == sn.Name():
				named = append(named, c)
			}
		}
		if len(named) > 0 && !slices.ContainsFunc(p.Children(), func(c xpath.Node) bool { return c.Schema() == sn }) {
			return named[:1]
		}
		return out
	}
	for _, leaves := range [][]any{
		{f.t, "1", f.t, "2", f.a, "x", f.t, "3", f.mm, "m"}, // a hash table
		{f.t, "2", f.a, "x"}, // none
	} {
		_, c, nodes := f.build(t, ValidateOptions{}, leaves...)
		p := xn{c, f.set}
		if c.kids.ht == nil == (len(leaves) > 4) {
			t.Fatalf("hash table: %v", c.kids.ht != nil)
		}
		nodes["t=2"].flags |= flagDead
		opq := newOpaque(opaque{Name: "e"})
		c.kids.opq = append(c.kids.opq, opq)
		opq.parent = c
		table := c.kids.ht != nil
		check := func(sn xpath.SchemaNode, vals []string) {
			got, ok := p.LookupChild(sn, vals)
			want := scan(p, sn, vals)
			if ok && !slices.Equal(got, want) || ok != (table && len(want) == 1 && want[0].Schema() != nil) {
				t.Errorf("%s %v: %v %v, scan %v", sn.Name(), vals, got, ok, want)
			}
		}
		for _, v := range []string{"1", "2", "3", "4"} {
			check(xs{f.t, f.set}, []string{v})
		}
		for _, sn := range []xpath.SchemaNode{xs{f.a, f.set}, xs{f.mm, f.set}, xs{f.e, f.set}} {
			check(sn, nil)
		}
	}
}

// xpTestCtx compiles modules given as text into a context of their own.
func xpTestCtx(t *testing.T, mods map[string]string) *schema.Set {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, text := range mods {
		fsys[name+".yang"] = &fstest.MapFile{Data: []byte(text)}
	}
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fsys)
	if err != nil {
		t.Fatal(err)
	}
	for name := range mods {
		if _, diags, err := c.Load(name, "", nil); err != nil {
			t.Fatal(err, diags)
		}
	}
	return c.Snapshot()
}

func xpTestTerm(t *testing.T, sn *schema.Node, val string) *Node {
	t.Helper()
	v, d := types.Store(sn.Type, val, types.FormatJSON, types.HintString, nil, sn)
	if d != nil {
		t.Fatal(d)
	}
	return newTerm(sn, v)
}

const xpTestB = `module b { namespace "urn:b"; prefix b; container q { leaf-list v { type string; } } }`

// xpTestCross builds ctx A's tree with a:p (leaf x = "v", list L k = "1") and the a:any payload
// of ctx B: a:p with leaves y1..y4 and an opaque x, then b:q with n v instances.
func xpTestCross(t *testing.T, n int, opaqueBeforeX ...int) (tr *Tree, a *schema.Set, q *Node) {
	t.Helper()
	a = xpTestCtx(t, map[string]string{"b": xpTestB, "a": `module a { namespace "urn:a"; prefix a; yang-version 1.1;
		anydata any; container p { leaf x { type string; } list L { key k; leaf k { type string; } } } }`})
	b := xpTestCtx(t, map[string]string{"b": xpTestB, "a": `module a { namespace "urn:a"; prefix a; yang-version 1.1;
		anydata any; container p { leaf y1 { type string; } leaf y2 { type string; }
		leaf y3 { type string; } leaf y4 { type string; } } }`})
	am, bm, bq := a.Implemented("a"), b.Implemented("a"), xpTestTop(b.Implemented("b"), "q")
	src := newTree(b)
	an := &Node{schema: xpTestTop(bm, "any"), any: &anyValue{}}
	mustLink(t, src.insert(nil, an, insertDefault))
	pb := newInner(xpTestTop(bm, "p"))
	for _, y := range pb.schema.Children {
		mustLink(t, src.insert(pb, xpTestTerm(t, y, "y"), insertDefault))
	}
	if len(opaqueBeforeX) != 0 {
		for i := range opaqueBeforeX[0] {
			mustLink(t, src.insert(pb, newOpaque(opaque{Name: fmt.Sprintf("before-%d", i),
				ModuleNS: "urn:a", Format: types.FormatXML, set: b}), insertLast))
		}
	}
	mustLink(t, src.insert(pb, newOpaque(opaque{Name: "x", ModuleNS: "urn:a", Value: "o", Format: types.FormatXML, set: b}), insertLast))
	qb := newInner(bq)
	for i := range n {
		mustLink(t, src.insert(qb, xpTestTerm(t, bq.Children[0], fmt.Sprint(i)), insertDefault))
	}
	mustLink(t, src.insert(an, qb, insertDefault))
	mustLink(t, src.insert(an, pb, insertDefault))
	tr = newTree(a)
	dup, _, err := tr.Dup(an, nil, DupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pa := newInner(xpTestTop(am, "p"))
	mustLink(t, tr.insert(nil, pa, insertDefault))
	mustLink(t, tr.insert(pa, xpTestTerm(t, pa.schema.Children[0], "v"), insertDefault))
	mustLink(t, tr.insert(pa, xpTestList(t, tr, pa.schema.Children[1], "1"), insertDefault))
	for c := range dup.any.children.all() {
		if c.schema != nil && c.schema.Name == "q" {
			q = c
		}
	}
	return tr, a, q
}

// A cross-context hash miss falls back to lyd_find_sibling_opaq_next (xpath.c
// moveto_node_hash_child): the payload p has a children table, no x and an opaque x.
func TestAnyXPathCrossContextHashMissOpaque(t *testing.T) {
	tr, _, _ := xpTestCross(t, 1)
	xpTestCount(t, tr, "count((/a:any/a:p | /a:p)/a:x)", 2)

	// lyd_find_sibling_opaq_next is linear. Every opaque sibling it inspects must
	// consume the evaluator's work budget instead of hiding O(N) work behind one step.
	wide, set, _ := xpTestCross(t, 1, 2000)
	e, err := xpath.Compile("count((/a:any/a:p | /a:p)/a:x)", jsonNS{set: set})
	if err != nil {
		t.Fatal(err)
	}
	vc := &valCtx{t: wide, log: &logger{set: set}, budget: xpathBudget{max: 100}}
	if _, err := vc.eval(e, nil, xpath.RootAll, true); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("large opaque fallback = %v, want XPath budget", err)
	}
	var payloadP *Node
	for c := range wide.top.first().any.children.all() {
		if c.Name() == "p" {
			payloadP = c
		}
	}
	x := xpTestTop(set.Implemented("a"), "p").Children[0]
	calls := 0
	probe := &xprobe{node: newInner(x), set: set, charge: func(int64) error {
		calls++
		if calls == 10 {
			return context.Canceled
		}
		return nil
	}}
	if _, _, err := wrap(set, payloadP).(xn).LookupPrepared(probe); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled opaque fallback = %v after %d charges", err, calls)
	}
}

// FirstChild across contexts: with a children table the lookup is O(1) (no sibling visits);
// without one it declines to the evaluator's charged walk.
func TestAnyXPathCrossContextFirstChildCharged(t *testing.T) {
	for _, n := range []int{3, 200} {
		tr, a, q := xpTestCross(t, n)
		l := xs{xpTestTop(a.Implemented("a"), "p").Children[1], a}
		tr.work.visits = true
		q.kids.work = &tr.work
		tr.work.Store(0)
		got, ok := wrap(a, q).(xn).FirstChild(l)
		switch {
		case got != nil, ok != (n >= htMinItems):
			t.Errorf("n=%d: FirstChild = %v, %t", n, got, ok)
		case tr.work.Load() > 2:
			t.Errorf("n=%d: %d uncharged sibling visits", n, tr.work.Load())
		}
		xpTestCount(t, tr, "count((/a:any/b:q | /a:p)/a:L[a:k='1'])", 1)
	}
}
