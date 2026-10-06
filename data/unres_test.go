// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
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

// TestWhenQueueWork: n queued nodes with true whens cost O(n) XPath steps in one pass. The n
// leafrefs are logged, not bounded: the evaluator's moveto_node_hash_child scans the children
// (xpath.Node has no keyed lookup), so `../t[.='v']` costs O(siblings) per reference where
// libyang uses the children hash table; ponytail: an optional xpath.Node child-lookup hook.
func TestWhenQueueWork(t *testing.T) {
	f := newUnresFixture(t)
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
	f.t.Type = &schema.Type{Base: schema.Uint16}
	f.r.Type.Realtype = f.t.Type
	args = nil
	for i := range n / 4 {
		args = append(args, f.t, fmt.Sprint(i), f.r, fmt.Sprint(i))
	}
	vc, _, _ = f.build(t, ValidateOptions{}, args...)
	if err := vc.unres(); err != nil {
		t.Fatal(err, diagCodes(vc.log.diags))
	}
	t.Logf("leafrefs: %d steps for %d references", vc.budget.steps, n/4)
}
