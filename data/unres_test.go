// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
)

// testNS binds unprefixed names to one module.
type testNS string

func (n testNS) Resolve(p string) (string, bool) { return string(n), p == string(n) }
func (n testNS) Prefix(string) string            { return string(n) }
func (n testNS) Default() string                 { return string(n) }

// unresFixture is
//
//	module u { container c {
//	  leaf a { type string; }
//	  leaf b { when "../a = 'on'"; type string; }
//	  leaf e { when "../b = 'x'"; type string; }      (depends on b's when)
//	  leaf-list t { type uint8; }
//	  leaf-list r { type leafref { path "../t"; } }    (require-instance)
//	  leaf m { must "../a = 'ok'" { error-app-tag x; error-message "bad m"; } must "true()"; type string; }
//	  leaf n { must "count(../t) > 1"; type string; }
//	  leaf iid { type instance-identifier; }
//	  leaf d { when "../a = 'on'"; type string; default "dv"; } } }
type unresFixture struct {
	set                             *schema.Set
	m                               *schema.Module
	c, a, b, e, t, r, mm, n, iid, d *schema.Node
}

func newUnresFixture(t *testing.T) *unresFixture {
	f := &unresFixture{set: &schema.Set{}}
	f.m = &schema.Module{Name: "u", Implemented: true}
	f.set.Modules = []*schema.Module{f.m}
	str, u8 := &schema.Type{Base: schema.String}, &schema.Type{Base: schema.Uint8}
	add := func(p *schema.Node, k schema.Kind, name string, ty *schema.Type) *schema.Node {
		n := &schema.Node{Kind: k, Name: name, Module: f.m, Parent: p, Type: ty, Config: true}
		if p == nil {
			f.m.Top = append(f.m.Top, n)
		} else {
			p.Children = append(p.Children, n)
		}
		return n
	}
	compile := func(src string) *xpath.Expr {
		e, err := xpath.Compile(src, testNS("u"))
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	when := func(n *schema.Node, src string) {
		n.Whens = append(n.Whens, &schema.When{Src: src, ContextNode: n, Compiled: compile(src)})
	}
	f.c = add(nil, schema.Container, "c", nil)
	f.a = add(f.c, schema.Leaf, "a", str)
	f.b = add(f.c, schema.Leaf, "b", str)
	when(f.b, "../a = 'on'")
	f.e = add(f.c, schema.Leaf, "e", str)
	when(f.e, "../b = 'x'")
	f.t = add(f.c, schema.LeafList, "t", u8)
	f.r = add(f.c, schema.LeafList, "r", &schema.Type{Base: schema.Leafref, Path: "../t",
		Prefixes: schema.NSCtx{"": f.m}, RequireInstance: true, Realtype: u8})
	f.mm = add(f.c, schema.Leaf, "m", str)
	f.mm.Musts = []*schema.Must{{Src: "../a = 'ok'", AppTag: "x", Msg: "bad m", Compiled: compile("../a = 'ok'")},
		{Src: "true()", Compiled: compile("true()")}}
	f.n = add(f.c, schema.Leaf, "n", str)
	f.n.Musts = []*schema.Must{{Src: "count(../t) > 1", Compiled: compile("count(../t) > 1")}}
	f.iid = add(f.c, schema.Leaf, "iid", &schema.Type{Base: schema.InstanceID, RequireInstance: true})
	f.d = add(f.c, schema.Leaf, "d", str)
	f.d.Default = []schema.DefaultValue{{Lex: "dv"}}
	when(f.d, "../a = 'on'")
	return f
}

// build makes c with the given leaves (schema node, value) and queues like the parser.
func (f *unresFixture) build(t *testing.T, opts ValidateOptions, leaves ...any) (*valCtx, *Node, map[string]*Node) {
	t.Helper()
	tr := newTree(f.set)
	vc := &valCtx{t: tr, log: &logger{set: f.set}, opts: opts, nodeWhen: &nodeSet{}, nodeTypes: &nodeSet{}}
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	byName := map[string]*Node{}
	for i := 0; i+1 < len(leaves); i += 2 {
		sn, lex := leaves[i].(*schema.Node), leaves[i+1].(string)
		v, d := types.Store(sn.Type, lex, types.FormatJSON, types.JSONHints("string")|types.HintStringDatatypes,
			types.ModuleNames{Set: f.set}, sn)
		if d != nil {
			t.Fatal(d.Msg)
		}
		n := newTerm(sn, v)
		tr.insert(c, n, insertDefault)
		if v.NeedsTree() {
			vc.nodeTypes.add(n)
		}
		if hasWhen(sn) {
			vc.nodeWhen.add(n)
		}
		byName[sn.Name+"="+lex] = n
	}
	return vc, c, byName
}

// TestWhenQueue: from the end of the queue; a when reading an unresolved when waits for the
// next pass; false explicit → error (multi-error keeps the node as WhenFalse, descendants leave
// the queues); WhenTrue (implicit) → silently deleted; operational → warning.
func TestWhenQueue(t *testing.T) {
	f := newUnresFixture(t)
	// queue [e, b]: b is processed first, then e in the same pass
	vc, _, nodes := f.build(t, ValidateOptions{}, f.a, "on", f.e, "y", f.b, "x")
	if err := vc.unres(); err != nil {
		t.Fatal(err, vc.log.diags)
	}
	if nodes["b=x"].flags&FlagWhenTrue == 0 || nodes["e=y"].flags&FlagWhenTrue == 0 || vc.nodeWhen.len() != 0 {
		t.Fatal("whens not resolved")
	}
	// queue [b, e]: e waits for b (incomplete), resolved in the second pass
	vc, _, nodes = f.build(t, ValidateOptions{}, f.a, "on", f.b, "x", f.e, "y")
	vc.nodeWhen.items = []*Node{nodes["b=x"], nodes["e=y"]}
	steps := 0
	for vc.nodeWhen.len() > 0 && steps < 3 {
		if err := vc.unresWhen(); err != nil {
			t.Fatal(err)
		}
		steps++
	}
	if steps != 2 {
		t.Fatalf("%d passes, want 2 (e incomplete in the first)", steps)
	}

	// false when on an explicit node: error at the node; multi-error keeps it as WhenFalse
	vc, c, nodes := f.build(t, ValidateOptions{MultiError: true}, f.a, "off", f.b, "x", f.e, "y")
	if err := vc.unres(); err == nil {
		t.Fatal("no error")
	}
	got := diagCodes(vc.log.diags)
	want := []string{
		`LY_EVALID LYVE_DATA /u:c/b: When condition "../a = 'on'" not satisfied.`,
		`LY_EVALID LYVE_DATA /u:c/e: When condition "../b = 'x'" not satisfied.`,
	}
	if !reflect.DeepEqual(got, want) || nodes["b=x"].flags&FlagWhenFalse == 0 || nodes["b=x"].parent != c {
		t.Fatalf("got %v, flags %x", got, nodes["b=x"].flags)
	}

	// an implicit (WhenTrue) node whose when is false is deleted silently, into the diff
	vc, c, _ = f.build(t, ValidateOptions{}, f.a, "off")
	var deleted []string
	vc.diff = func(n *Node, op diffOp) error {
		if op == diffDelete {
			deleted = append(deleted, n.Name())
		}
		return nil
	}
	if err := vc.newImplicitR(c, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if vc.t.findSchema(&c.kids, f.d) == nil {
		t.Fatal("implicit d missing")
	}
	if err := vc.unres(); err != nil || vc.t.findSchema(&c.kids, f.d) != nil || !reflect.DeepEqual(deleted, []string{"d"}) {
		t.Fatalf("autodelete: %v %v", err, deleted)
	}

	// operational: a warning
	vc, _, _ = f.build(t, ValidateOptions{Operational: true}, f.a, "off", f.b, "x")
	if err := vc.unres(); err != nil || len(vc.log.diags) != 1 || !vc.log.diags[0].Warning {
		t.Fatalf("operational: %v %v", err, diagCodes(vc.log.diags))
	}
}

// TestMust: app-tag and message from the must, must-violation otherwise, warnings for
// operational data.
func TestMust(t *testing.T) {
	f := newUnresFixture(t)
	vc, _, nodes := f.build(t, ValidateOptions{MultiError: true}, f.a, "no", f.mm, "v", f.n, "v", f.t, "1")
	_ = vc.validateMust(nodes["m=v"])
	_ = vc.validateMust(nodes["n=v"])
	var tags []string
	for _, d := range vc.log.diags {
		tags = append(tags, d.AppTag)
	}
	want := []string{`LY_EVALID LYVE_DATA /u:c/m: bad m`, `LY_EVALID LYVE_DATA /u:c/n: Must condition "count(../t) > 1" not satisfied.`}
	if got := diagCodes(vc.log.diags); !reflect.DeepEqual(got, want) || !reflect.DeepEqual(tags, []string{"x", "must-violation"}) {
		t.Fatalf("%v %v", got, tags)
	}
	vc, _, nodes = f.build(t, ValidateOptions{Operational: true}, f.a, "no", f.mm, "v")
	if err := vc.validateMust(nodes["m=v"]); err != nil || len(vc.log.diags) != 1 || vc.log.diags[0].Msg != "bad m" ||
		!vc.log.diags[0].Warning {
		t.Fatalf("operational: %v %v", err, diagCodes(vc.log.diags))
	}
}

// TestTypesQueue: leafref require-instance through the per-value predicate, errors in reverse
// queue order (PROBED in design 07: reverse parse order); instance-identifier existence.
func TestTypesQueue(t *testing.T) {
	f := newUnresFixture(t)
	vc, _, _ := f.build(t, ValidateOptions{MultiError: true}, f.t, "1", f.t, "2", f.r, "1", f.r, "9", f.r, "7", f.r, "8",
		f.iid, "/u:c/t[.='2']")
	if err := vc.unres(); err == nil {
		t.Fatal("no error")
	}
	want := []string{
		`LY_EVALID LYVE_DATA /u:c/r[.='8']: Invalid leafref value "8" - no target instance "../t" with the same value.`,
		`LY_EVALID LYVE_DATA /u:c/r[.='7']: Invalid leafref value "7" - no target instance "../t" with the same value.`,
		`LY_EVALID LYVE_DATA /u:c/r[.='9']: Invalid leafref value "9" - no target instance "../t" with the same value.`,
	}
	if got := diagCodes(vc.log.diags); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	for _, d := range vc.log.diags {
		if d.AppTag != "instance-required" {
			t.Fatalf("app-tag %q", d.AppTag)
		}
	}
	vc, _, _ = f.build(t, ValidateOptions{}, f.t, "1", f.iid, "/u:c/t[.='3']")
	if err := vc.unres(); err == nil || vc.log.diags[0].Msg != `Invalid instance-identifier "/u:c/t[.='3']" value - required instance not found.` {
		t.Fatalf("instance-identifier: %v", diagCodes(vc.log.diags))
	}
}

// TestDummyWhen: the when of an absent node is evaluated on a stand-in at its place.
func TestDummyWhen(t *testing.T) {
	f := newUnresFixture(t)
	vc, c, _ := f.build(t, ValidateOptions{}, f.a, "off")
	w, err := vc.dummyWhen(c, f.b)
	if err != nil || w == nil || w.Src != "../a = 'on'" || c.kids.len() != 1 {
		t.Fatalf("%v %v, %d children left", w, err, c.kids.len())
	}
	vc, c, _ = f.build(t, ValidateOptions{}, f.a, "on")
	if w, err := vc.dummyWhen(c, f.b); err != nil || w != nil {
		t.Fatalf("true when: %v %v", w, err)
	}
	// D-0055: e's when reads b, whose own when is unresolved: ignored (check runs) under
	// multi-error, LY_EINT otherwise; the dummy is unlinked either way.
	vc, c, _ = f.build(t, ValidateOptions{MultiError: true}, f.b, "x")
	if w, err := vc.dummyWhen(c, f.e); err != nil || w != nil || c.kids.len() != 1 {
		t.Fatalf("multi-error: %v %v, %d children left", w, err, c.kids.len())
	}
	vc, c, _ = f.build(t, ValidateOptions{}, f.b, "x")
	if w, err := vc.dummyWhen(c, f.e); err == nil || w != nil || c.kids.len() != 1 ||
		vc.log.diags[len(vc.log.diags)-1].Err != "LY_EINT" {
		t.Fatalf("single error: %v %v, %d children left", w, err, c.kids.len())
	}
}

// TestXPathBudget: the cumulative step budget aborts with yang.ErrBudget (U-0042); cancellation
// is checked between evaluations; steps are counted per evaluation.
func TestXPathBudget(t *testing.T) {
	f := newUnresFixture(t)
	args := []any{f.a, "no"}
	for i := range 50 {
		args = append(args, f.t, fmt.Sprint(i))
	}
	args = append(args, f.n, "v")
	vc, _, nodes := f.build(t, ValidateOptions{}, args...)
	if err := vc.validateMust(nodes["n=v"]); err != nil || vc.budget.steps <= 0 {
		t.Fatalf("%v, %d steps", err, vc.budget.steps)
	}
	vc.budget.max = vc.budget.steps + 1
	if err := vc.validateMust(nodes["n=v"]); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	vc.budget = xpathBudget{ctx: ctx}
	if err := vc.validateMust(nodes["n=v"]); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

// TestWhenQueueWork: n queued nodes with true whens cost O(n) XPath steps in one pass, and so do
// n leafrefs to a keyed list: `../l[k='v']/k` finds its target through the children hash table
// (xn.LookupChild, moveto_node_hash_child), not by a scan of the siblings. A leafref to a
// leaf-list (`../t[.='v']`) scans, as libyang does: a leaf-list predicate is never hashed.
func TestWhenQueueWork(t *testing.T) {
	f, l, k, _, _, _, _ := listFixture(t)
	const n = 2000
	w := &schema.Node{Kind: schema.LeafList, Name: "w", Module: f.m, Parent: f.c, Type: &schema.Type{Base: schema.Uint16}, Config: true}
	e, _ := xpath.Compile("true()", testNS("u"))
	w.Whens = []*schema.When{{Src: "true()", ContextNode: w, Compiled: e}}
	f.c.Children = append(f.c.Children, w)
	var args []any
	for i := range n {
		args = append(args, w, fmt.Sprint(i))
	}
	vc, _, _ := f.build(t, ValidateOptions{}, args...)
	if err := vc.unres(); err != nil {
		t.Fatal(err)
	}
	if vc.budget.steps > 20*n {
		t.Fatalf("when queue: %d steps for %d nodes", vc.budget.steps, n)
	}
	str := &schema.Type{Base: schema.String}
	rl := &schema.Node{Kind: schema.LeafList, Name: "rl", Module: f.m, Parent: f.c, Config: true,
		Type: &schema.Type{Base: schema.Leafref, Path: "../l/k", Prefixes: schema.NSCtx{"": f.m}, RequireInstance: true, Realtype: str}}
	f.c.Children = append(f.c.Children, rl)
	args = nil
	for i := range n / 4 {
		args = append(args, rl, fmt.Sprint(i))
	}
	vc, c, _ := f.build(t, ValidateOptions{}, args...)
	for i := range n / 4 {
		inst := newInner(l)
		key, _ := types.Store(k.Type, fmt.Sprint(i), types.FormatJSON, types.JSONHints("string"), nil, k)
		vc.t.insert(inst, newTerm(k, key), insertDefault)
		vc.t.insert(c, inst, insertDefault)
	}
	if err := vc.unres(); err != nil {
		t.Fatal(err, diagCodes(vc.log.diags))
	}
	if vc.budget.steps > 20*n/4 {
		t.Fatalf("leafrefs to a list: %d steps for %d references", vc.budget.steps, n/4)
	}
	f.t.Type = &schema.Type{Base: schema.Uint16}
	f.r.Type.Realtype = f.t.Type
	args = nil
	for i := range n / 4 {
		args = append(args, f.t, fmt.Sprint(i), f.r, fmt.Sprint(i))
	}
	vc, _, _ = f.build(t, ValidateOptions{}, args...)
	vc.t.work.visits = true
	w0 := int(vc.t.work.Load())
	if err := vc.unres(); err != nil {
		t.Fatal(err, diagCodes(vc.log.diags))
	}
	// O(n²) sibling visits in all (each `..` result is sorted once per evaluation, its sibling list
	// read once); reading the list again per sorted node made it O(n³), outside the step budget
	if w := int(vc.t.work.Load()) - w0; w > 8*(n/2)*(n/2) {
		t.Fatalf("leafrefs to a leaf-list: %d sibling visits for %d references", w, n/4)
	}
	t.Logf("leafrefs to a leaf-list: %d steps, %d sibling visits for %d references", vc.budget.steps, int(vc.t.work.Load())-w0, n/4)
}

// TestPathEvalPositions: an instance-identifier position beyond the instances — including the
// saturated 2^64-1 of a huge number — selects nothing (no panic); key and position predicates.
func TestPathEvalPositions(t *testing.T) {
	f := newUnresFixture(t)
	vc, _, nodes := f.build(t, ValidateOptions{}, f.t, "1", f.t, "2")
	seg := func(pos uint64) types.Path {
		return types.Path{{Node: f.c}, {Node: f.t, Preds: []types.PathPred{{Kind: types.PredPosition, Position: pos}}}}
	}
	if vc.pathEval(seg(2)) != nodes["t=2"] || vc.pathEval(seg(3)) != nil || vc.pathEval(seg(0)) != nil ||
		vc.pathEval(seg(math.MaxUint64)) != nil || vc.pathEval(seg(1<<63)) != nil {
		t.Fatal("positions")
	}
	v, _ := types.Store(f.t.Type, "2", types.FormatJSON, types.JSONHints("number"), nil, f.t)
	ll := types.Path{{Node: f.c}, {Node: f.t, Preds: []types.PathPred{{Kind: types.PredLeafList, Value: v}}}}
	if vc.pathEval(ll) != nodes["t=2"] {
		t.Fatal("leaf-list value predicate")
	}
}

// listFixture adds `list l { key k; leaf k; leaf v { when "../k = 'on'"; } }`, `leaf lk { type
// leafref { path "../l/k"; } }`, `leaf sr { type leafref { path "../a"; } }` and `leaf dis { type
// leafref { path "../gone"; } }` (a target removed by if-feature) to the unres fixture.
func listFixture(t *testing.T) (*unresFixture, *schema.Node, *schema.Node, *schema.Node, *schema.Node, *schema.Node, *schema.Node) {
	f := newUnresFixture(t)
	str := &schema.Type{Base: schema.String}
	add := func(p *schema.Node, k schema.Kind, name string, ty *schema.Type) *schema.Node {
		n := &schema.Node{Kind: k, Name: name, Module: f.m, Parent: p, Type: ty, Config: true}
		p.Children = append(p.Children, n)
		return n
	}
	l := add(f.c, schema.List, "l", nil)
	k := add(l, schema.Leaf, "k", str)
	l.Keys = []*schema.Node{k}
	v := add(l, schema.Leaf, "v", str)
	e, _ := xpath.Compile("../k = 'on'", testNS("u"))
	v.Whens = []*schema.When{{Src: "../k = 'on'", ContextNode: v, Compiled: e}}
	lref := func(name, path string, rt *schema.Type) *schema.Node {
		return add(f.c, schema.Leaf, name, &schema.Type{Base: schema.Leafref, Path: path, Prefixes: schema.NSCtx{"": f.m},
			RequireInstance: true, Realtype: rt})
	}
	return f, l, k, v, lref("lk", "../l/k", str), lref("sr", "../a", f.a.Type), lref("dis", "../gone", str)
}

// TestLeafrefForms: the list-key template (`../l[k='v']/k`), the both-quotes value (plain path,
// then compared), a target removed by if-feature (success), and deref() of both kinds and of the
// removed target.
func TestLeafrefForms(t *testing.T) {
	f, l, k, _, lk, sr, dis := listFixture(t)
	vc, c, nodes := f.build(t, ValidateOptions{MultiError: true}, f.a, `x'y"z`, f.t, "5", f.iid, "/u:c/t[.='5']")
	inst := newInner(l)
	key, _ := types.Store(k.Type, "k1", types.FormatJSON, types.JSONHints("string"), nil, k)
	vc.t.insert(inst, newTerm(k, key), insertDefault)
	vc.t.insert(c, inst, insertDefault)
	tp, err := vc.template(lk.Type, lk)
	if err != nil || !tp.listKey || tp.head != "../l" || tp.key != "k" {
		t.Fatalf("template %+v %v", tp, err)
	}
	for _, tc := range []struct {
		sn    *schema.Node
		lex   string
		found bool
	}{
		{lk, "k1", true}, {lk, "k2", false},
		{sr, `x'y"z`, true}, {sr, `x'y"q`, false},
		{dis, "anything", true},
	} {
		v, d := types.Store(tc.sn.Type, tc.lex, types.FormatJSON, types.JSONHints("string"), nil, tc.sn)
		if d != nil {
			t.Fatal(d.Msg)
		}
		n := newTerm(tc.sn, v)
		vc.t.insert(c, n, insertDefault)
		found, _, err := vc.leafrefTargets(n, tc.sn.Type, v)
		if err != nil || found != tc.found {
			t.Errorf("%s=%s: found %v, %v", tc.sn.Name, tc.lex, found, err)
		}
		unlink(n)
	}
	// deref(): the targets of a leafref, the node of an instance-identifier
	v, _ := types.Store(lk.Type, "k1", types.FormatJSON, types.JSONHints("string"), nil, lk)
	ref := newTerm(lk, v)
	vc.t.insert(c, ref, insertDefault)
	got, err := vc.deref(xn{ref, f.set})
	if err != nil || len(got) != 1 || got[0].(xn).n != inst.kids.list[0] {
		t.Fatalf("deref leafref: %v %v", got, err)
	}
	got, err = vc.deref(xn{nodes["iid=/u:c/t[.='5']"], f.set})
	if err != nil || len(got) != 1 || got[0].(xn).n != nodes["t=5"] {
		t.Fatalf("deref instance-identifier: %v %v", got, err)
	}
	// deref() of a leafref whose target is gone (only after a failed recompile in libyang, which
	// then reads targets->count of a NULL set and crashes, D-0068): no nodes and no error
	v, _ = types.Store(dis.Type, "anything", types.FormatJSON, types.JSONHints("string"), nil, dis)
	dn := newTerm(dis, v)
	vc.t.insert(c, dn, insertDefault)
	if got, err = vc.deref(xn{dn, f.set}); err != nil || len(got) != 0 {
		t.Fatalf("deref disabled target: %v %v", got, err)
	}
}

// TestWhenFalseSubtree: under multi-error a when-false node keeps its queued descendants out of
// the queue (one error, not two); its descendants' types are dropped.
func TestWhenFalseSubtree(t *testing.T) {
	f, l, k, v, _, _, _ := listFixture(t)
	e, _ := xpath.Compile("../a = 'on'", testNS("u"))
	l.Whens = []*schema.When{{Src: "../a = 'on'", ContextNode: l, Compiled: e}}
	vc, c, _ := f.build(t, ValidateOptions{MultiError: true}, f.a, "off")
	inst := newInner(l)
	key, _ := types.Store(k.Type, "off", types.FormatJSON, types.JSONHints("string"), nil, k)
	vc.t.insert(inst, newTerm(k, key), insertDefault)
	val, _ := types.Store(v.Type, "x", types.FormatJSON, types.JSONHints("string"), nil, v)
	vn := newTerm(v, val)
	vc.t.insert(inst, vn, insertDefault)
	vc.t.insert(c, inst, insertDefault)
	vc.nodeWhen.add(vn) // post-order: the child before its list
	vc.nodeWhen.add(inst)
	if err := vc.unres(); err == nil {
		t.Fatal("no error")
	}
	if got := diagCodes(vc.log.diags); len(got) != 1 || got[0] != `LY_EVALID LYVE_DATA /u:c/l[k='off']: When condition "../a = 'on'" not satisfied.` {
		t.Fatalf("%v", got)
	}
	if inst.flags&FlagWhenFalse == 0 || vn.flags&(FlagWhenTrue|FlagWhenFalse) != 0 {
		t.Fatalf("flags %x %x", inst.flags, vn.flags)
	}
}

// TestUnresWork counts the work that used to be quadratic, so reverting a fix fails it:
//   - n musts over a big top level build the evaluations' top-level view once (it was rebuilt per
//     evaluation);
//   - n top-level WhenTrue nodes deleted in one pass do not rebuild that view (each deletion
//     changed it before);
//   - n when-false nodes under multi-error build one position map per pass and scan node_types
//     linearly (the map was dropped and rebuilt per removal).
func TestUnresWork(t *testing.T) {
	f := newUnresFixture(t)
	const n = 5000
	tl := &schema.Node{Kind: schema.LeafList, Name: "tl", Module: f.m, Type: &schema.Type{Base: schema.Uint16}, Config: true}
	e, _ := xpath.Compile("true()", testNS("u"))
	tl.Musts = []*schema.Must{{Src: "true()", Compiled: e}}
	f.m.Top = append(f.m.Top, tl)
	vc, _, _ := f.build(t, ValidateOptions{})
	var tops []*Node
	for i := range n {
		v, _ := types.Store(tl.Type, fmt.Sprint(i), types.FormatJSON, types.JSONHints("number"), nil, tl)
		x := newTerm(tl, v)
		vc.t.insert(nil, x, insertDefault)
		tops = append(tops, x)
	}
	for _, x := range tops {
		if err := vc.validateMust(x); err != nil {
			t.Fatal(err)
		}
	}
	if vc.topBuilds != 1 {
		t.Fatalf("musts over a big top level: %d view builds", vc.topBuilds)
	}

	// n top-level WhenTrue nodes whose when is false: deleted in one pass, the view built once
	ef, _ := xpath.Compile("false()", testNS("u"))
	tw := &schema.Node{Kind: schema.LeafList, Name: "tw", Module: f.m, Type: &schema.Type{Base: schema.Uint16}, Config: true}
	tw.Whens = []*schema.When{{Src: "false()", ContextNode: tw, Compiled: ef}}
	f.m.Top = append(f.m.Top, tw)
	vc, _, _ = f.build(t, ValidateOptions{})
	for i := range n {
		v, _ := types.Store(tw.Type, fmt.Sprint(i), types.FormatJSON, types.JSONHints("number"), nil, tw)
		x := newTerm(tw, v)
		x.flags = FlagWhenTrue
		vc.t.insert(nil, x, insertDefault)
		vc.nodeWhen.add(x)
	}
	if err := vc.unres(); err != nil {
		t.Fatal(err)
	}
	if vc.topBuilds != 1 || vc.t.top.len() != 1 {
		t.Fatalf("top-level deletions: %d view builds, %d left", vc.topBuilds, vc.t.top.len())
	}

	// n when-false nodes (multi-error) with queued values, then n WhenTrue nodes deleted
	w := &schema.Node{Kind: schema.LeafList, Name: "w", Module: f.m, Parent: f.c, Type: &schema.Type{Base: schema.Uint16}, Config: true}
	w.Whens = []*schema.When{{Src: "false()", ContextNode: w, Compiled: ef}}
	f.c.Children = append(f.c.Children, w)
	for _, whenTrue := range []bool{false, true} {
		var args []any
		for i := range n {
			args = append(args, w, fmt.Sprint(i))
		}
		vc, c, _ := f.build(t, ValidateOptions{MultiError: true}, args...)
		for x := range c.Children() {
			vc.nodeTypes.add(x)
			if whenTrue {
				x.flags |= FlagWhenTrue
			}
		}
		vc.t.work.Store(0)
		vc.t.work.visits = true
		_ = vc.unres()
		if vc.posBuilds > 2 || vc.nodeTypes.scans > 2*n || int(vc.t.work.Load()) > 4*n || vc.nodeWhen.len() != 0 {
			t.Fatalf("whenTrue %v: %d position maps, %d queue scans, %d unlink steps, %d queued",
				whenTrue, vc.posBuilds, vc.nodeTypes.scans, int(vc.t.work.Load()), vc.nodeWhen.len())
		}
		if whenTrue && c.kids.len() != 0 || !whenTrue && len(vc.log.diags) != n {
			t.Fatalf("whenTrue %v: %d left, %d errors", whenTrue, c.kids.len(), len(vc.log.diags))
		}
	}
}

// TestWhenTwiceQueued: a node queued twice (the JSON parser queues one per metadata member) whose
// when is false is deleted once, with one diff entry.
func TestWhenTwiceQueued(t *testing.T) {
	f := newUnresFixture(t)
	vc, c, nodes := f.build(t, ValidateOptions{}, f.a, "off", f.b, "x")
	b := nodes["b=x"]
	b.flags |= FlagWhenTrue
	vc.nodeWhen.add(b)
	deletes := 0
	vc.diff = func(_ *Node, op diffOp) error {
		if op == diffDelete {
			deletes++
		}
		return nil
	}
	if err := vc.unres(); err != nil || deletes != 1 || vc.t.findSchema(&c.kids, f.b) != nil {
		t.Fatalf("%v: %d deletes", err, deletes)
	}
}

// TestDeadSubtree: a top-level node deleted earlier in the same when pass is gone with its
// subtree: the descendant axis does not find its children (review repro: p with `when false()`
// and child q, z with `when "count(/descendant::q) = 0"` processed after p).
func TestDeadSubtree(t *testing.T) {
	f := newUnresFixture(t)
	str := &schema.Type{Base: schema.String}
	p := &schema.Node{Kind: schema.Container, Name: "p", Module: f.m, Config: true}
	q := &schema.Node{Kind: schema.Leaf, Name: "q", Module: f.m, Parent: p, Type: str, Config: true}
	p.Children = []*schema.Node{q}
	z := &schema.Node{Kind: schema.Leaf, Name: "z", Module: f.m, Type: str, Config: true}
	ef, _ := xpath.Compile("false()", testNS("u"))
	p.Whens = []*schema.When{{Src: "false()", ContextNode: p, Compiled: ef}}
	ec, _ := xpath.Compile("count(/descendant::q) = 0", testNS("u"))
	z.Whens = []*schema.When{{Src: "count(/descendant::q) = 0", ContextNode: z, Compiled: ec}}
	f.m.Top = append(f.m.Top, p, z)
	vc, _, _ := f.build(t, ValidateOptions{})
	pn := newInner(p)
	pn.flags = FlagWhenTrue
	qv, _ := types.Store(str, "v", types.FormatJSON, types.JSONHints("string"), nil, q)
	vc.t.insert(pn, newTerm(q, qv), insertDefault)
	vc.t.insert(nil, pn, insertDefault)
	zv, _ := types.Store(str, "v", types.FormatJSON, types.JSONHints("string"), nil, z)
	zn := newTerm(z, zv)
	vc.t.insert(nil, zn, insertDefault)
	vc.nodeWhen.add(zn) // queued before p: the pass (from the end) handles p first
	vc.nodeWhen.add(pn)
	if err := vc.unres(); err != nil {
		t.Fatal(err, diagCodes(vc.log.diags))
	}
	if pn.parent != nil || pn.tree != nil || zn.flags&FlagWhenTrue == 0 {
		t.Fatalf("p linked %v, z flags %x", pn.tree != nil, zn.flags)
	}
}

// TestXPathErrorLocation: an evaluation error of a must or when (LOGVAL_DXPATH, here count() of
// a number, which compiles) is logged at lyxp_eval's current node: the must's node, the when's
// context node (oracle fixtures protocol-v2/xperr-must, xperr-when).
func TestXPathErrorLocation(t *testing.T) {
	f := newUnresFixture(t)
	const src = "count(1) >= 0"
	e, err := xpath.Compile(src, testNS("u"))
	if err != nil {
		t.Fatal(err)
	}
	f.mm.Musts = []*schema.Must{{Src: src, Compiled: e}}
	f.a.Whens = []*schema.When{{Src: src, ContextNode: f.a, Compiled: e}}
	const msg = "Wrong type of argument #1 (number) for the XPath function count(node-set)."

	vc, _, nodes := f.build(t, ValidateOptions{}, f.mm, "v")
	if err := vc.validateMust(nodes["m=v"]); err == nil ||
		!reflect.DeepEqual(diagCodes(vc.log.diags), []string{"LY_EVALID LYVE_XPATH /u:c/m: " + msg}) {
		t.Errorf("must: %v %v", err, diagCodes(vc.log.diags))
	}
	vc, _, _ = f.build(t, ValidateOptions{}, f.a, "v")
	if err := vc.unres(); err == nil ||
		!reflect.DeepEqual(diagCodes(vc.log.diags), []string{"LY_EVALID LYVE_XPATH /u:c/a: " + msg}) {
		t.Errorf("when: %v %v", err, diagCodes(vc.log.diags))
	}
}
