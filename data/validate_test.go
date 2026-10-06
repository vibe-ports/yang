// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
)

type schemaBuilder struct {
	t   *testing.T
	set *schema.Set
}

func (b *schemaBuilder) module(name string) *schema.Module {
	m := &schema.Module{Name: name, Namespace: "urn:" + name, Implemented: true}
	b.set.Modules = append(b.set.Modules, m)
	return m
}

func (b *schemaBuilder) add(m *schema.Module, p *schema.Node, k schema.Kind, name string, ty *schema.Type) *schema.Node {
	n := &schema.Node{Kind: k, Name: name, Module: m, Parent: p, Type: ty, Config: true}
	if p == nil {
		m.Top = append(m.Top, n)
	} else {
		p.Children = append(p.Children, n)
	}
	return n
}

func (b *schemaBuilder) term(sn *schema.Node, lex string) *Node {
	v, d := types.Store(sn.Type, lex, types.FormatJSON, types.JSONHints("string")|types.HintStringDatatypes,
		types.ModuleNames{Set: b.set}, sn)
	if d != nil {
		b.t.Fatal(d.Msg)
	}
	n := newTerm(sn, v)
	n.flags = FlagNew
	return n
}

// TestValidateOrder: the cross-module probe of design 07 §1.5 (modules za then aa in context
// order; input aa:r, za:r, za:ll[d,d], aa:ll[e,e], za:w). Parse path: the first module traversed
// drains the shared queues (the when, then the leafrefs from the end), then each module's
// top-level duplicates; with Present the modules go in tree order (aa first); the Validate path
// drains per module.
func TestValidateOrder(t *testing.T) {
	b := &schemaBuilder{t: t, set: &schema.Set{}}
	b.module("x0") // implemented, no data: first in context order
	za, aa := b.module("za"), b.module("aa")
	str := &schema.Type{Base: schema.String}
	nodes := map[string]*schema.Node{}
	for _, m := range []*schema.Module{za, aa} {
		tl := b.add(m, nil, schema.LeafList, "t", str)
		_ = tl
		nodes[m.Name+":r"] = b.add(m, nil, schema.Leaf, "r", &schema.Type{Base: schema.Leafref, Path: "/" + m.Name + ":t",
			Prefixes: schema.NSCtx{"": m, m.Name: m}, RequireInstance: true, Realtype: str})
		nodes[m.Name+":ll"] = b.add(m, nil, schema.LeafList, "ll", str)
	}
	w := b.add(za, nil, schema.Leaf, "w", str)
	e, _ := xpath.Compile("false()", testNS("za"))
	w.Whens = []*schema.When{{Src: "false()", ContextNode: w, Compiled: e}}
	nodes["za:w"] = w

	input := []struct{ sn, v string }{{"aa:r", "1"}, {"za:r", "1"}, {"za:ll", "d"}, {"za:ll", "d"}, {"aa:ll", "e"},
		{"aa:ll", "e"}, {"za:w", "x"}}
	parsed := func(present bool) *lydCtx {
		o := parseOpts{ParseOptions: ParseOptions{Validate: ValidateOptions{MultiError: true, Present: present}}}
		lc := &lydCtx{ctx: context.Background(), tree: newTree(b.set), log: &logger{set: b.set}, opts: o}
		for _, in := range input {
			n := b.term(nodes[in.sn], in.v)
			lc.nodeInsert(nil, nil, n)
			if n.value.NeedsTree() {
				lc.nodeTypes.add(n)
			}
			lc.setDataFlags(n, &n.meta)
		}
		return lc
	}
	when := `LY_EVALID LYVE_DATA /za:w: When condition "false()" not satisfied.`
	lref := func(m string) string {
		return `LY_EVALID LYVE_DATA /` + m + `:r: Invalid leafref value "1" - no target instance "/` + m + `:t" with the same value.`
	}
	dup := func(m, v string) string {
		return `LY_EVALID LYVE_DATA /` + m + `:ll[.='` + v + `']: Duplicate instance of "ll".`
	}
	for _, tc := range []struct {
		name    string
		present bool
		want    []string
	}{
		{"parse", false, []string{when, lref("za"), lref("aa"), dup("za", "d"), dup("za", "d"), dup("aa", "e"), dup("aa", "e")}},
		{"parse-present", true, []string{dup("aa", "e"), dup("aa", "e"), when, lref("za"), lref("aa"), dup("za", "d"), dup("za", "d")}},
	} {
		lc := parsed(tc.present)
		_ = lc.validateParsed()
		if got := diagCodes(lc.log.diags); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got  %q\n want %q", tc.name, got, tc.want)
		}
	}
	// Validate path: per module, own queues
	lc := parsed(false)
	diags, err := lc.tree.validateAll(context.Background(), ValidateOptions{MultiError: true}, Budget{}, nil)
	want := []string{dup("za", "d"), dup("za", "d"), when, lref("za"), dup("aa", "e"), dup("aa", "e"), lref("aa")}
	if got := diagCodes(diags); err == nil || !reflect.DeepEqual(got, want) {
		t.Errorf("validate:\n got  %q\n want %q", got, want)
	}
}

// TestFinal: lyd_validate_final_r — top-level mandatory (schema path), unexpected state node,
// mandatory choice (missing-choice), mandatory leaf at its parent, too few at the last instance,
// unique among three instances reported at the earlier one (lyht_insert's callback order).
func TestFinal(t *testing.T) {
	b := &schemaBuilder{t: t, set: &schema.Set{}}
	m := b.module("f")
	str := &schema.Type{Base: schema.String}
	c := b.add(m, nil, schema.Container, "c", nil)
	mand := b.add(m, c, schema.Leaf, "m", str)
	mand.Mandatory = true
	ch := b.add(m, c, schema.Choice, "ch", nil)
	ch.Mandatory = true
	b.add(m, b.add(m, ch, schema.Case, "x", nil), schema.Leaf, "x", str)
	ll := b.add(m, c, schema.LeafList, "ll", str)
	ll.Min, ll.Max = 2, 3
	l := b.add(m, c, schema.List, "l", nil)
	k := b.add(m, l, schema.Leaf, "k", str)
	u := b.add(m, l, schema.Leaf, "u", str)
	l.Keys, l.Uniques = []*schema.Node{k}, [][]*schema.Node{{u}}
	st := b.add(m, c, schema.Leaf, "st", str)
	st.Config = false
	tm := b.add(m, nil, schema.Leaf, "tm", str)
	tm.Mandatory = true

	tr := newTree(b.set)
	cn := newInner(c)
	tr.insert(nil, cn, insertDefault)
	tr.insert(cn, b.term(ll, "1"), insertDefault)
	for _, kv := range [][2]string{{"a", "1"}, {"b", "2"}, {"c", "1"}} {
		li := newInner(l)
		tr.insert(li, b.term(k, kv[0]), insertDefault)
		tr.insert(li, b.term(u, kv[1]), insertDefault)
		tr.insert(cn, li, insertDefault)
	}
	tr.insert(cn, b.term(st, "s"), insertDefault)
	diags, err := tr.validateAll(context.Background(), ValidateOptions{MultiError: true, NoState: true}, Budget{}, nil)
	want := []string{
		`LY_EVALID LYVE_DATA /f:tm: Mandatory node "tm" instance does not exist.`,
		`LY_EVALID LYVE_DATA /f:c/st: Unexpected data state node "st" found.`,
		`LY_EVALID LYVE_DATA /f:c: Mandatory choice "ch" data do not exist.`,
		`LY_EVALID LYVE_DATA /f:c: Mandatory node "m" instance does not exist.`,
		`LY_EVALID LYVE_DATA /f:c/ll[.='1']: Too few "ll" instances.`,
		`LY_EVALID LYVE_DATA /f:c/l[k='a']: Unique data leaf(s) "u" not satisfied in "/f:c/l[k='c']" and "/f:c/l[k='a']".`,
	}
	if got := diagCodes(diags); err == nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	var tags []string
	for _, d := range diags {
		tags = append(tags, d.AppTag)
	}
	if !reflect.DeepEqual(tags, []string{"", "", "missing-choice", "", "too-few-elements", "data-not-unique"}) {
		t.Fatalf("app-tags %q", tags)
	}
	// operational: the same findings are warnings (the state node stays an error)
	diags, err = tr.validateAll(context.Background(), ValidateOptions{MultiError: true, Operational: true}, Budget{}, nil)
	if err != nil {
		t.Fatalf("operational: %v %q", err, diagCodes(diags))
	}
	for _, d := range diags {
		if !d.Warning {
			t.Fatalf("operational error %q", d.Msg)
		}
	}
}

// TestFinalOpaqueOnly: a tree of only top-level opaque nodes reports the opaque error once per
// implemented module (D-0057 candidate); with schema data, once.
func TestFinalOpaqueOnly(t *testing.T) {
	b := &schemaBuilder{t: t, set: &schema.Set{}}
	m1, _ := b.module("m1"), b.module("m2")
	leaf := b.add(m1, nil, schema.Leaf, "a", &schema.Type{Base: schema.String})
	for _, withData := range []bool{false, true} {
		tr := newTree(b.set)
		tr.insert(nil, newOpaque(opaque{Name: "x", ModuleNS: "zz", Format: types.FormatJSON}), insertDefault)
		if withData {
			tr.insert(nil, b.term(leaf, "v"), insertDefault)
		}
		diags, _ := tr.validateAll(context.Background(), ValidateOptions{MultiError: true}, Budget{}, nil)
		n := 2
		if withData {
			n = 1
		}
		if len(diags) != n {
			t.Errorf("with data %v: %q", withData, diagCodes(diags))
		}
	}
}

// TestParseValidates: the parse wiring — the parser's close runs lyd_validate_new and the
// implicit nodes, charged to MaxNodes, and the final validation reports a missing mandatory node.
func TestParseValidates(t *testing.T) {
	b := &schemaBuilder{t: t, set: &schema.Set{}}
	m := b.module("p")
	str := &schema.Type{Base: schema.String}
	c := b.add(m, nil, schema.Container, "c", nil)
	d := b.add(m, c, schema.Leaf, "d", str)
	d.Default = []schema.DefaultValue{{Lex: "dv"}}
	mand := b.add(m, c, schema.Leaf, "m", str)
	mand.Mandatory = true
	fp := func(lc *lydCtx, _ []byte) error {
		cn, err := lc.createInner(c)
		if err != nil {
			return err
		}
		lc.nodeInsert(nil, nil, cn)
		return lc.closeInner(cn, nil)
	}
	o := parseOpts{ParseOptions: ParseOptions{Validate: ValidateOptions{MultiError: true}}}
	_, diags, err := parseWith(context.Background(), strings.NewReader(""), b.set, o, fp, nil)
	if err == nil || len(diags) != 1 || diags[0].Msg != `Mandatory node "m" instance does not exist.` {
		t.Fatalf("%v %q", err, diagCodes(diags))
	}
	mand.Mandatory = false
	tr, _, err := parseWith(context.Background(), strings.NewReader(""), b.set, o, fp, nil)
	if err != nil {
		t.Fatal(err)
	}
	cn := tr.top.list[0]
	if cn.kids.len() != 1 || cn.kids.list[0].value.Canonical() != "dv" || cn.flags&FlagDefault == 0 {
		t.Fatalf("implicit default: %d children, flags %x", cn.kids.len(), cn.flags)
	}
	o.Budget.MaxNodes = 1
	if _, _, err := parseWith(context.Background(), strings.NewReader(""), b.set, o, fp, nil); err == nil {
		t.Fatal("implicit node not charged")
	}
}

// TestUniqueWork: n list instances with distinct unique values are checked through the
// per-unique tables, one comparison per collision (counted, not timed).
func TestUniqueWork(t *testing.T) {
	b := &schemaBuilder{t: t, set: &schema.Set{}}
	m := b.module("q")
	str := &schema.Type{Base: schema.String}
	l := b.add(m, nil, schema.List, "l", nil)
	k := b.add(m, l, schema.Leaf, "k", str)
	u := b.add(m, l, schema.Leaf, "u", str)
	l.Keys, l.Uniques = []*schema.Node{k}, [][]*schema.Node{{u}}
	tr := newTree(b.set)
	const n = 5000
	for i := range n {
		li := newInner(l)
		tr.insert(li, b.term(k, fmt.Sprint(i)), insertDefault)
		tr.insert(li, b.term(u, fmt.Sprint(i%(n-1))), insertDefault) // one collision: 0 and n-1
		tr.insert(nil, li, insertDefault)
	}
	tr.work = 0
	diags, _ := tr.validateAll(context.Background(), ValidateOptions{MultiError: true}, Budget{}, nil)
	if len(diags) != 1 || tr.work > 8*n {
		t.Fatalf("%d diagnostics, %d steps", len(diags), tr.work)
	}
}

// TestUniqueMissingDefault: list instances all lacking a unique leaf with a default share one
// table key but never compare equal (libyang's canon2 quirk), so each meets every earlier one;
// the default's canonical is computed once and every comparison is charged to MaxXPathSteps.
// On the parse path the budget error outranks a logged parse error (U-0042).
func TestUniqueMissingDefault(t *testing.T) {
	b := &schemaBuilder{t: t, set: &schema.Set{}}
	m := b.module("q")
	str := &schema.Type{Base: schema.String}
	l := b.add(m, nil, schema.List, "l", nil)
	k := b.add(m, l, schema.Leaf, "k", str)
	u := b.add(m, l, schema.Leaf, "u", str)
	u.Default = []schema.DefaultValue{{Lex: "dv"}}
	l.Keys, l.Uniques = []*schema.Node{k}, [][]*schema.Node{{u}}
	fill := func(tr *Tree, n int) {
		for i := range n {
			li := newInner(l)
			tr.insert(li, b.term(k, fmt.Sprint(i)), insertDefault)
			tr.insert(nil, li, insertDefault)
		}
	}
	work := func(n int) (int, []yang.Diagnostic, error) {
		tr := newTree(b.set)
		fill(tr, n)
		tr.work = 0
		diags, err := tr.validateAll(context.Background(), ValidateOptions{NoDefaults: true}, Budget{}, nil)
		return tr.work, diags, err
	}
	l.Uniques = nil
	base, _, _ := work(50) // the work of the other checks
	l.Uniques = [][]*schema.Node{{u}}
	w, diags, err := work(50)
	if err != nil || len(diags) != 0 || w-base != 1+50*49/2 { // one default, every pair compared
		t.Fatalf("%v %q, %d steps over %d", err, diagCodes(diags), w, base)
	}
	tr := newTree(b.set)
	fill(tr, 3000)
	tr.work = 0
	_, err = tr.validateAll(context.Background(), ValidateOptions{NoDefaults: true}, Budget{MaxXPathSteps: 1000}, nil)
	if !errors.Is(err, yang.ErrBudget) || tr.work > 1001+3000*2 {
		t.Fatalf("%v, %d steps", err, tr.work)
	}
	fp := func(lc *lydCtx, _ []byte) error {
		fill(lc.tree, 3000)
		return lc.log.val(nil, "", ly.Data, "parse error")
	}
	o := parseOpts{ParseOptions: ParseOptions{Validate: ValidateOptions{MultiError: true}}}
	o.Budget.MaxXPathSteps = 1000
	if _, _, err := parseWith(context.Background(), strings.NewReader(""), b.set, o, fp, nil); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("parse error hides the budget: %v", err)
	}
}
