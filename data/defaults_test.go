// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// dfltFixture is
//
//	module d { container top {          (NP)
//	  leaf a { type uint8; default 1; }
//	  leaf-list ll { type uint8; default 7; default 8; }
//	  container inner { leaf b { type uint8; default 2; } }   (NP)
//	  container p { presence ""; leaf c { type uint8; default 3; } }
//	  choice ch { default one;
//	    case one { leaf x { type uint8; default 4; } }
//	    case two { leaf y { type uint8; default 5; } leaf z { type uint8; } } }
//	  leaf st { config false; type uint8; default 6; }
//	  list l { key k; leaf k { type uint8; } }
//	  leaf w { when "../a = 1"; type uint8; default 9; } } }
type dfltFixture struct {
	set                                      *schema.Set
	m                                        *schema.Module
	top, a, ll, inner, b, p, c, ch, one, two *schema.Node
	x, y, z, st, l, k, w                     *schema.Node
}

func newDfltFixture() *dfltFixture {
	f := &dfltFixture{set: &schema.Set{}}
	f.m = &schema.Module{Name: "d", Implemented: true}
	f.set.Modules = []*schema.Module{f.m}
	u8 := &schema.Type{Base: schema.Uint8}
	add := func(p *schema.Node, k schema.Kind, name string, dflt ...string) *schema.Node {
		n := &schema.Node{Kind: k, Name: name, Module: f.m, Parent: p, Config: true}
		if k == schema.Leaf || k == schema.LeafList {
			n.Type = u8
		}
		for _, d := range dflt {
			n.Default = append(n.Default, schema.DefaultValue{Lex: d})
		}
		if p == nil {
			f.m.Top = append(f.m.Top, n)
		} else {
			p.Children = append(p.Children, n)
		}
		return n
	}
	f.top = add(nil, schema.Container, "top")
	f.a = add(f.top, schema.Leaf, "a", "1")
	f.ll = add(f.top, schema.LeafList, "ll", "7", "8")
	f.inner = add(f.top, schema.Container, "inner")
	f.b = add(f.inner, schema.Leaf, "b", "2")
	f.p = add(f.top, schema.Container, "p")
	f.p.Presence = true
	f.c = add(f.p, schema.Leaf, "c", "3")
	f.ch = add(f.top, schema.Choice, "ch")
	f.one = add(f.ch, schema.Case, "one")
	f.x = add(f.one, schema.Leaf, "x", "4")
	f.two = add(f.ch, schema.Case, "two")
	f.y = add(f.two, schema.Leaf, "y", "5")
	f.z = add(f.two, schema.Leaf, "z")
	f.ch.DefaultCase = f.one
	f.st = add(f.top, schema.Leaf, "st", "6")
	f.st.Config = false
	f.l = add(f.top, schema.List, "l")
	f.k = add(f.l, schema.Leaf, "k")
	f.l.Keys = []*schema.Node{f.k}
	f.w = add(f.top, schema.Leaf, "w", "9")
	f.w.Whens = []*schema.When{{Src: "../a = 1"}}
	return f
}

func (f *dfltFixture) term(t *testing.T, s *schema.Node, lex string, flags Flags) *Node {
	t.Helper()
	v, d := types.Store(s.Type, lex, types.FormatJSON, types.JSONHints("number"), nil, s)
	if d != nil {
		t.Fatal(d.Msg)
	}
	n := newTerm(s, v)
	n.flags = flags
	return n
}

type diffRec struct {
	op   diffOp
	path string
}

func (f *dfltFixture) ctx(t *testing.T, tr *Tree, multi bool) (*valCtx, *[]diffRec, *nodeSet, *nodeSet) {
	t.Helper()
	var diffs []diffRec
	when, typ := &nodeSet{}, &nodeSet{}
	vc := &valCtx{t: tr, log: &logger{set: f.set}, opts: ValidateOptions{MultiError: multi},
		nodeWhen: when, nodeTypes: typ}
	vc.diff = func(n *Node, op diffOp) error {
		diffs = append(diffs, diffRec{op, lydPath(f.set, n, false)})
		return nil
	}
	return vc, &diffs, when, typ
}

func dump(n *Node) []string {
	var s []string
	for d := range n.All() {
		e := lydPathRel(d)
		if d.flags&FlagDefault != 0 {
			e += " D"
		}
		if d.flags&FlagWhenTrue != 0 {
			e += " W"
		}
		s = append(s, e)
	}
	return s
}

// lydPathRel is the name with the value of a term, for compact dumps.
func lydPathRel(n *Node) string {
	if n.isTerm() {
		return n.Name() + "=" + n.value.Canonical()
	}
	return n.Name()
}

// TestNewImplicit: lyd_new_implicit_r — NP containers recursively, leaf and all leaf-list
// defaults, the default case of a choice without data, no presence-container content, state
// skipped with NoState, when-seeded flags and queue, diff in creation order.
func TestNewImplicit(t *testing.T) {
	f := newDfltFixture()
	tr := newTree(f.set)
	vc, diffs, when, _ := f.ctx(t, tr, false)
	if err := vc.newImplicitR(nil, nil, f.m, implNoState); err != nil {
		t.Fatal(err)
	}
	top := tr.top.list[0]
	want := []string{"top D", "a=1 D", "ll=7 D", "ll=8 D", "inner D", "b=2 D", "x=4 D", "w=9 D W"}
	if got := dump(top); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	if when.len() != 1 || when.items[0].Name() != "w" {
		t.Fatalf("when queue %v", when.items)
	}
	// choices first (lyd_new_implicit), then the other children, then the default containers
	wantDiff := []diffRec{{diffCreate, "/d:top"}, {diffCreate, "/d:top/x"}, {diffCreate, "/d:top/a"},
		{diffCreate, "/d:top/ll[.='7']"}, {diffCreate, "/d:top/ll[.='8']"}, {diffCreate, "/d:top/inner"},
		{diffCreate, "/d:top/w"}, {diffCreate, "/d:top/inner/b"}}
	if !reflect.DeepEqual(*diffs, wantDiff) {
		t.Fatalf("diff %v", *diffs)
	}

	// an existing case gets its own defaults, not the default case's; NoDefaults keeps only
	// containers
	tr = newTree(f.set)
	vc, _, _, _ = f.ctx(t, tr, false)
	top = newInner(f.top)
	tr.insert(nil, top, insertDefault)
	tr.insert(top, f.term(t, f.z, "1", FlagNew), insertDefault)
	if err := vc.newImplicitR(top, nil, nil, implNoDefaults); err != nil {
		t.Fatal(err)
	}
	if got := dump(top); !reflect.DeepEqual(got, []string{"top", "inner D", "z=1"}) {
		t.Fatalf("no defaults: %v", got)
	}
	vc.getnext = nil
	if err := vc.newImplicitR(top, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if got := dump(top); !reflect.DeepEqual(got, []string{"top", "a=1 D", "ll=7 D", "ll=8 D", "inner D", "b=2 D", "y=5 D", "z=1",
		"st=6 D", "w=9 D W"}) {
		t.Fatalf("existing case: %v", got)
	}
}

// TestValidateNew: lyd_validate_new — explicit instances replace defaults, an old default of a
// leaf goes for a new one, old case data goes for a new case (with its defaults), two new cases
// are an error at the choice, FlagNew is cleared.
func TestValidateNew(t *testing.T) {
	f := newDfltFixture()
	tr := newTree(f.set)
	vc, diffs, _, _ := f.ctx(t, tr, false)
	if err := vc.newImplicitR(nil, nil, f.m, 0); err != nil {
		t.Fatal(err)
	}
	top := tr.top.list[0]
	*diffs = nil
	tr.insert(top, f.term(t, f.ll, "9", FlagNew), insertDefault)
	tr.insert(top, f.term(t, f.a, "1", FlagNew), insertDefault) // explicit a beside the default one
	tr.insert(top, f.term(t, f.y, "3", FlagNew), insertDefault) // new case two
	if err := vc.validateNew(top, nil, nil); err != nil {
		t.Fatal(err, vc.log.diags)
	}
	if got := dump(top); !reflect.DeepEqual(got, []string{"top", "a=1", "ll=9", "inner D", "b=2 D", "y=3", "st=6 D", "w=9 D W"}) {
		t.Fatalf("got %v", got)
	}
	wantDiff := []diffRec{{diffDelete, "/d:top/x"}, {diffDelete, "/d:top/a"}, {diffDelete, "/d:top/ll[.='7']"},
		{diffDelete, "/d:top/ll[.='8']"}}
	if !reflect.DeepEqual(*diffs, wantDiff) {
		t.Fatalf("diff %v", *diffs)
	}
	for c := range top.Children() {
		if c.flags&FlagNew != 0 {
			t.Fatalf("%s still new", c.Name())
		}
	}

	// two cases with new data: error at the choice's schema path
	tr.insert(top, f.term(t, f.x, "1", FlagNew), insertDefault)
	tr.insert(top, f.term(t, f.z, "1", FlagNew), insertDefault)
	vc.log.diags = nil
	if err := vc.validateNew(top, nil, nil); err == nil || diagCodes(vc.log.diags)[0] !=
		`LY_EVALID LYVE_DATA /d:top/ch: Data for both cases "one" and "two" exist.` {
		t.Fatalf("both cases: %v %v", err, diagCodes(vc.log.diags))
	}
}

func diagCodes(ds []yang.Diagnostic) []string {
	var s []string
	for _, d := range ds {
		s = append(s, d.Err+" "+d.Code+" "+d.DataPath+d.SchemaPath+": "+d.Msg)
	}
	return s
}

// TestDuplicates: both members of a duplicate pair report (D-0050), operational lists warn,
// state leaf-lists may repeat, at the top level too; multi-error goes on.
func TestDuplicates(t *testing.T) {
	f := newDfltFixture()
	tr := newTree(f.set)
	vc, _, _, _ := f.ctx(t, tr, true)
	top := newInner(f.top)
	tr.insert(nil, top, insertDefault)
	for _, v := range []string{"5", "5", "6"} {
		tr.insert(top, f.term(t, f.ll, v, FlagNew), insertDefault)
	}
	l1, l2 := newInner(f.l), newInner(f.l)
	for _, l := range []*Node{l1, l2} {
		tr.insert(l, f.term(t, f.k, "1", 0), insertDefault)
		l.flags = FlagNew
		tr.insert(top, l, insertDefault)
	}
	if err := vc.validateNew(top, nil, nil); err == nil {
		t.Fatal("no error")
	}
	want := []string{
		`LY_EVALID LYVE_DATA /d:top/ll[.='5']: Duplicate instance of "ll".`,
		`LY_EVALID LYVE_DATA /d:top/ll[.='5']: Duplicate instance of "ll".`,
		`LY_EVALID LYVE_DATA /d:top/l[k='1']: Duplicate instance of "l".`,
		`LY_EVALID LYVE_DATA /d:top/l[k='1']: Duplicate instance of "l".`,
	}
	if got := diagCodes(vc.log.diags); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	// operational: lists and leaf-lists warn
	vc.log.diags, vc.opts.Operational = nil, true
	for c := range top.Children() {
		c.flags |= FlagNew
	}
	if err := vc.validateNew(top, nil, nil); err != nil || len(vc.log.diags) != 4 || !vc.log.diags[0].Warning {
		t.Fatalf("operational: %v %v", err, diagCodes(vc.log.diags))
	}
}

// TestDuplicateWork: the duplicate check of n new siblings costs O(n) bucket probes, with and
// without a children table (counted, not timed).
func TestDuplicateWork(t *testing.T) {
	f := newDfltFixture()
	for _, top := range []bool{false, true} {
		tr := newTree(f.set)
		vc, _, _, _ := f.ctx(t, tr, false)
		var parent *Node
		var mod *schema.Module
		sn := f.ll
		if top {
			sn = &schema.Node{Kind: schema.LeafList, Name: "tl", Module: f.m, Type: &schema.Type{Base: schema.Uint32}, Config: true}
			f.m.Top = append(f.m.Top, sn)
			mod = f.m
		} else {
			parent = newInner(f.top)
			tr.insert(nil, parent, insertDefault)
			sn.Type = &schema.Type{Base: schema.Uint32}
		}
		const n = 20000
		for i := range n {
			v, _ := types.Store(sn.Type, fmt.Sprint(i), types.FormatJSON, types.JSONHints("number"), nil, sn)
			x := newTerm(sn, v)
			tr.insert(parent, x, insertDefault)
			x.flags = FlagNew
		}
		tr.work = 0
		if err := vc.validateNew(parent, nil, mod); err != nil {
			t.Fatal(err)
		}
		if tr.work > 2*n {
			t.Fatalf("top %v: %d probes for %d siblings", top, tr.work, n)
		}
	}
}

// TestValStop: LY_VAL_ERR_GOTO goes on after LY_EVALID under multi-error even when a warning was
// logged after the error, and stops on any other LY_ERR.
func TestValStop(t *testing.T) {
	vc := &valCtx{log: &logger{}, opts: ValidateOptions{MultiError: true}}
	_ = vc.log.val(nil, "", 9, "e")
	vc.log.warn("w")
	if vc.stop(errLogged) {
		t.Fatal("LY_EVALID then a warning must go on")
	}
	_ = vc.log.logErr("LY_EINVAL", "x")
	vc.log.warn("w")
	if !vc.stop(errLogged) {
		t.Fatal("LY_EINVAL must stop")
	}
	vc.opts.MultiError = false
	if vc.stop(nil) || !vc.stop(errLogged) {
		t.Fatal("single error")
	}
}

// TestHasWhen: lysc_has_when climbs only through choice and case; the data parent's when is not
// the node's.
func TestHasWhen(t *testing.T) {
	f := newDfltFixture()
	f.top.Whens = []*schema.When{{Src: "true()"}}
	f.ch.Whens = []*schema.When{{Src: "true()"}}
	if hasWhen(f.a) || !hasWhen(f.top) || !hasWhen(f.x) || !hasWhen(f.w) {
		t.Fatal("hasWhen")
	}
}

// TestAutodelWork: replacing n default instances and dropping their node_types entries costs
// linear work (counted, not timed); the implicit nodes are charged to the node budget.
func TestAutodelWork(t *testing.T) {
	f := newDfltFixture()
	tr := newTree(f.set)
	vc, _, _, typ := f.ctx(t, tr, false)
	top := newInner(f.top)
	tr.insert(nil, top, insertDefault)
	f.ll.Type = &schema.Type{Base: schema.Uint32}
	const n = 20000
	for i := range n {
		d := f.term(t, f.ll, fmt.Sprint(i), FlagDefault)
		tr.insert(top, d, insertDefault)
		typ.add(d)
	}
	tr.insert(top, f.term(t, f.ll, fmt.Sprint(n), FlagNew), insertDefault)
	tr.work = 0
	if err := vc.validateNew(top, nil, nil); err != nil {
		t.Fatal(err)
	}
	if top.kids.len() != 1 || typ.len() != 0 || tr.work > 4*n {
		t.Fatalf("%d left, %d queued, %d steps", top.kids.len(), typ.len(), tr.work)
	}
	charged := 0
	vc.charge = func() error { charged++; return nil }
	if err := vc.newImplicitR(top, nil, nil, 0); err != nil || charged == 0 {
		t.Fatalf("charge: %v %d", err, charged)
	}
}
