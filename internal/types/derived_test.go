// SPDX-License-Identifier: BSD-3-Clause
// Test cases ported from libyang v5.8.6 tests/utests/types/{identityref,instanceid,leafref,
// union}.c (BSD-3-Clause, © CESNET).

package types

import (
	"errors"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// ---- a hand-built compiled schema: what compile produces for the C tests' modules ----

func newMod(set *schema.Set, name, ns string) *schema.Module {
	m := &schema.Module{Name: name, Namespace: "urn:tests:" + ns, Prefix: "pref", Implemented: true}
	set.Modules = append(set.Modules, m)
	return m
}

func ident(m *schema.Module, name string, bases ...*schema.Identity) *schema.Identity {
	id := &schema.Identity{Name: name, Module: m}
	for _, b := range bases {
		b.Derived = append(b.Derived, id)
	}
	m.Identities = append(m.Identities, id)
	return id
}

// node adds a config node under parent (top level of m when parent is nil).
func node(m *schema.Module, parent *schema.Node, k schema.Kind, name string, t *schema.Type) *schema.Node {
	n := &schema.Node{Kind: k, Name: name, Module: m, Parent: parent, Type: t, Config: true}
	if parent == nil {
		m.Top = append(m.Top, n)
	} else {
		parent.Children = append(parent.Children, n)
	}
	return n
}

func list(m *schema.Module, parent *schema.Node, name string, keys ...*schema.Type) *schema.Node {
	l := node(m, parent, schema.List, name, nil)
	for i, kt := range keys {
		kn := []string{"id", "id2"}[i]
		l.Keys = append(l.Keys, node(m, l, schema.Leaf, kn, kt))
	}
	return l
}

type instFixture struct {
	set              *schema.Set
	defs, mod        *schema.Module
	l1, l2           *schema.Node
	identDer1, ident *schema.Identity
}

// instanceid.c test_data_xml schema (modules defs and mod).
func newInstFixture() *instFixture {
	f := &instFixture{set: &schema.Set{}}
	d := newMod(f.set, "defs", "defs")
	f.defs = d
	f.ident = ident(d, "ident")
	f.identDer1 = ident(d, "ident-der1", f.ident)
	ident(d, "ident-der2", f.ident)
	f.l1 = node(d, nil, schema.Leaf, "l1", &schema.Type{Base: schema.InstanceID, RequireInstance: true})
	f.l2 = node(d, nil, schema.Leaf, "l2", &schema.Type{Base: schema.InstanceID})
	cont := node(d, nil, schema.Container, "cont", nil)
	node(d, cont, schema.Leaf, "l", typ(schema.Empty))
	l := list(d, nil, "list", typ(schema.String))
	node(d, l, schema.Leaf, "value", typ(schema.String))
	node(d, nil, schema.LeafList, "llist", typ(schema.Uint32))
	l = list(d, nil, "list-inst", &schema.Type{Base: schema.InstanceID, RequireInstance: true})
	node(d, l, schema.Leaf, "value", typ(schema.String))
	l = list(d, nil, "list-ident", &schema.Type{Base: schema.IdentityRef, Bases: []*schema.Identity{f.ident}})
	node(d, l, schema.Leaf, "value", typ(schema.String))
	list(d, nil, "list2", typ(schema.String), typ(schema.String))
	kl := node(d, nil, schema.List, "list-keyless", nil)
	kl.Config = false
	node(d, kl, schema.Leaf, "value", typ(schema.String)).Config = false
	f.mod = newMod(f.set, "mod", "mod")
	c := node(f.mod, nil, schema.Container, "cont", nil)
	node(f.mod, c, schema.Leaf, "l2", typ(schema.Empty))
	return f
}

func (f *instFixture) xml(ns map[string]string) PrefixCtx {
	m := map[string]string{"": "urn:tests:defs"}
	for k, v := range ns {
		m[k] = "urn:tests:" + v
	}
	return XMLNamespaces{Set: f.set, NS: m}
}

func TestInstanceIdentifier(t *testing.T) {
	f := newInstFixture()
	defsNS := map[string]string{"xdf": "defs", "a": "defs", "t": "defs"}
	cases := []struct {
		src   string
		leaf  *schema.Node
		lex   string
		ns    map[string]string
		canon string
		err   string
	}{
		{"instanceid.c:cont", f.l1, "/xdf:cont/xdf:l", defsNS, "/defs:cont/l", ""},
		{"instanceid.c:list", f.l1, "/xdf:list[xdf:id='b']/xdf:id", defsNS, "/defs:list[id='b']/id", ""},
		{"instanceid.c:llist", f.l1, "/xdf:llist[.='1']", defsNS, "/defs:llist[.='1']", ""},
		{"instanceid.c:list-inst", f.l1, `/a:list-inst[a:id="/a:llist[.='1']"]/a:value`, defsNS,
			`/defs:list-inst[id="/defs:llist[.='1']"]/value`, ""},
		{"instanceid.c:list-ident", f.l1, "/a:list-ident[a:id='a:ident-der1']/a:value", defsNS,
			"/defs:list-ident[id='defs:ident-der1']/value", ""},
		{"instanceid.c:list2", f.l1, "/a:list2[a:id='a:xxx'][a:id2='y']/a:id2", defsNS, "/defs:list2[id='a:xxx'][id2='y']/id2", ""},
		{"instanceid.c:mod", f.l1, "/m:cont/m:l2", map[string]string{"m": "mod"}, "/mod:cont/l2", ""},
		{"instanceid.c:keyless", f.l1, "/a:list-keyless[3]", defsNS, "/defs:list-keyless[3]", ""},
		{"instanceid.c:pos-config-list", f.l1, "/xdf:list[2]/xdf:value", defsNS, "",
			`Invalid instance-identifier "/xdf:list[2]/xdf:value" value - semantic error: Positional predicate defined for configuration list "list" in path.`},
		{"instanceid.c:bad-char", f.l1, "/t:cont/t:1l", defsNS, "",
			`Invalid instance-identifier "/t:cont/t:1l" value - syntax error: Invalid character 't'[9] of expression '/t:cont/t:1l'.`},
		{"instanceid.c:bad-colon", f.l1, "/t:cont:t:1l", defsNS, "",
			`Invalid instance-identifier "/t:cont:t:1l" value - syntax error: Invalid character ':'[8] of expression '/t:cont:t:1l'.`},
		{"instanceid.c:not-found", f.l1, "/xdf:cont/xdf:invalid/xdf:path", defsNS, "",
			`Invalid instance-identifier "/xdf:cont/xdf:invalid/xdf:path" value - semantic error: Not found node "invalid" in path.`},
		{"instanceid.c:eof", f.l1, "/t:llist[1", defsNS, "",
			`Invalid instance-identifier "/t:llist[1" value - syntax error: Unexpected XPath expression end.`},
		{"instanceid.c:pos-container", f.l1, "/m:cont[1]", map[string]string{"m": "mod"}, "",
			`Invalid instance-identifier "/m:cont[1]" value - semantic error: Positional predicate defined for container "cont" in path.`},
		{"instanceid.c:relative", f.l1, "[1]", nil, "",
			`Invalid instance-identifier "[1]" value - syntax error: XPath "[1]" was expected to be absolute.`},
		{"instanceid.c:prefix-missing", f.l1, "/m:cont/m:l2[l2='1']", map[string]string{"m": "mod"}, "",
			`Invalid instance-identifier "/m:cont/m:l2[l2='1']" value - syntax error: Prefix missing for "l2" in path.`},
		{"instanceid.c:leaf-pred", f.l1, "/m:cont/m:l2[m:l2='1']", map[string]string{"m": "mod"}, "",
			`Invalid instance-identifier "/m:cont/m:l2[m:l2='1']" value - semantic error: List predicate defined for leaf "l2" in path.`},
		{"instanceid.c:pos-config-llist", f.l1, "/t:llist[4]", defsNS, "",
			`Invalid instance-identifier "/t:llist[4]" value - semantic error: Positional predicate defined for configuration leaf-list "llist" in path.`},
		{"instanceid.c:no-module", f.l2, "/t:llist[6]", map[string]string{"xdf": "defs"}, "",
			`Invalid instance-identifier "/t:llist[6]" value - semantic error: No module connected with the prefix "t" found (prefix format XML prefixes).`},
		{"instanceid.c:not-key", f.l2, "/xdf:list[xdf:value='x']", defsNS, "",
			`Invalid instance-identifier "/xdf:list[xdf:value='x']" value - semantic error: Key expected instead of leaf "value" in path.`},
		{"instanceid.c:llist-pred-on-list", f.l2, "/xdf:list[.='x']", defsNS, "",
			`Invalid instance-identifier "/xdf:list[.='x']" value - semantic error: Leaf-list predicate defined for list "list" in path.`},
		{"instanceid.c:key-type", f.l1, "/t:llist[.='x']", defsNS, "",
			`Invalid instance-identifier "/t:llist[.='x']" value - semantic error: Invalid type uint32 value "x".`},
		{"instanceid.c:two-pos", f.l2, "/t:llist[1][2]", defsNS, "",
			`Invalid instance-identifier "/t:llist[1][2]" value - syntax error: Unparsed characters "[2]" left at the end of path.`},
		{"instanceid.c:two-llist", f.l2, "/t:llist[.='a'][.='b']", defsNS, "",
			`Invalid instance-identifier "/t:llist[.='a'][.='b']" value - syntax error: Unparsed characters "[.='b']" left at the end of path.`},
		{"instanceid.c:dup-key", f.l2, "/xdf:list[xdf:id='1'][xdf:id='2']/xdf:value", defsNS, "",
			`Invalid instance-identifier "/xdf:list[xdf:id='1'][xdf:id='2']/xdf:value" value - syntax error: Duplicate predicate key "id" in path.`},
		{"instanceid.c:missing-key", f.l2, "/xdf:list2[xdf:id='1']/xdf:value", defsNS, "",
			`Invalid instance-identifier "/xdf:list2[xdf:id='1']/xdf:value" value - semantic error: Predicate missing for a key of list "list2" in path.`},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			v, d := Store(c.leaf.Type, c.lex, FormatXML, HintData, f.xml(c.ns), c.leaf)
			if c.err != "" {
				if d == nil || d.Msg != c.err {
					t.Fatalf("got %v %q, want error %q", d, v.Canonical(), c.err)
				}
				return
			}
			if d != nil {
				t.Fatal(d.Msg)
			}
			if v.Canonical() != c.canon || v.NeedsTree() != c.leaf.Type.RequireInstance {
				t.Fatalf("canonical %q (needs tree %v), want %q", v.Canonical(), v.NeedsTree(), c.canon)
			}
			// the canonical form is valid JSON input and stores to itself
			v2, d := Store(c.leaf.Type, v.Canonical(), FormatJSON, JSONHints("string"), ModuleNames{f.set}, c.leaf)
			if d != nil || v2.Canonical() != c.canon || !Equal(v, v2) {
				t.Fatalf("JSON re-store: %v %q", d, v2.Canonical())
			}
		})
	}
	// JSON: the first node needs a prefix, later ones must not repeat it.
	for lex, want := range map[string]string{
		"/cont/l":                 `Invalid instance-identifier "/cont/l" value - syntax error: Prefix missing for "cont" in path.`,
		"/defs:cont/defs:l":       `Invalid instance-identifier "/defs:cont/defs:l" value - syntax error: Duplicate prefix for "defs:l" in path.`,
		"/defs:list[defs:id='a']": `Invalid instance-identifier "/defs:list[defs:id='a']" value - syntax error: Redundant prefix for "defs:id" in path.`,
	} {
		if _, d := Store(f.l1.Type, lex, FormatJSON, JSONHints("string"), ModuleNames{f.set}, f.l1); d == nil || d.Msg != want {
			t.Errorf("%s: got %v, want %q", lex, d, want)
		}
	}
}

func TestIdentityRef(t *testing.T) {
	set := &schema.Set{}
	ib := newMod(set, "ident-base", "ident-base")
	base := ident(ib, "ident-base")
	ident(ib, "ident-imp", base)
	defs := newMod(set, "defs", "defs")
	defs.Imports = []schema.Import{{Prefix: "ib", Module: ib}}
	ident(defs, "ident1", base)
	l1 := node(defs, nil, schema.Leaf, "l1", &schema.Type{Base: schema.IdentityRef, Bases: []*schema.Identity{base}})
	xml := func(ns map[string]string) PrefixCtx {
		m := map[string]string{"": "urn:tests:defs"}
		for k, v := range ns {
			m[k] = "urn:tests:" + v
		}
		return XMLNamespaces{Set: set, NS: m}
	}
	cases := []struct {
		src, lex   string
		ns         map[string]string
		canon, err string
	}{
		{"identityref.c:ident1", "ident1", nil, "defs:ident1", ""},
		{"identityref.c:ib", "ib:ident-imp", map[string]string{"ib": "ident-base"}, "ident-base:ident-imp", ""},
		{"identityref.c:not-found", "fast-ethernet", nil, "",
			`Invalid identityref "fast-ethernet" value - identity not found in module "defs".`},
		{"identityref.c:x-not-found", "x:slow-ethernet", map[string]string{"x": "defs"}, "",
			`Invalid identityref "x:slow-ethernet" value - identity not found in module "defs".`},
		{"identityref.c:not-derived", "x:ident-base", map[string]string{"x": "ident-base"}, "",
			`Invalid identityref "x:ident-base" value - identity not derived from the base "ident-base:ident-base".`},
		{"identityref.c:unknown-prefix", "x:ident-base", map[string]string{"x": "unknown"}, "",
			`Invalid identityref "x:ident-base" value - unable to map prefix to YANG schema.`},
		{"identityref-empty", "x:", map[string]string{"x": "defs"}, "", "Invalid empty identityref value."},
	}
	for _, c := range cases {
		v, d := Store(l1.Type, c.lex, FormatXML, HintData, xml(c.ns), l1)
		switch {
		case c.err != "" && (d == nil || d.Msg != c.err):
			t.Errorf("%s: got %v, want %q", c.src, d, c.err)
		case c.err == "" && (d != nil || v.Canonical() != c.canon || v.Ident() == nil):
			t.Errorf("%s: got %v %q, want %q", c.src, d, v.Canonical(), c.canon)
		}
	}
	// JSON: no prefix means the module of the context node; the prefix is a module name.
	if v, d := Store(l1.Type, "ident1", FormatJSON, HintString, ModuleNames{set}, l1); d != nil || v.Canonical() != "defs:ident1" {
		t.Errorf("JSON default module: %v %q", d, v.Canonical())
	}
	if v, d := Store(l1.Type, "ident-base:ident-imp", FormatJSON, HintString, ModuleNames{set}, l1); d != nil || v.Ident().Name != "ident-imp" {
		t.Errorf("JSON module prefix: %v", d)
	}
	// schema text: import prefixes; status of the referencing definition.
	if _, d := Store(l1.Type, "ib:ident-imp", FormatSchema, HintSchema, SchemaText{defs}, l1); d != nil {
		t.Errorf("schema prefix: %v", d)
	}
	dep := ident(defs, "old", base)
	dep.Status = schema.Deprecated
	if _, d := Store(l1.Type, "old", FormatSchema, HintSchema, SchemaText{defs}, l1); d == nil || d.Code != CodeReference ||
		d.Msg != `A current definition "l1" is not allowed to reference deprecated value "old".` {
		t.Errorf("status: %v", d)
	}
	// two bases: derived from all of them is required by the message, any of them by the code.
	b2 := ident(defs, "b2")
	two := &schema.Type{Base: schema.IdentityRef, Bases: []*schema.Identity{base, b2}}
	if _, d := Store(two, "b2", FormatJSON, HintString, ModuleNames{set}, l1); d == nil ||
		d.Msg != `Invalid identityref "b2" value - identity not derived from all the bases "ident-base:ident-base", "defs:b2".` {
		t.Errorf("two bases: %v", d)
	}
	// unimplemented module, disabled identity
	ib.Implemented = false
	if _, d := Store(l1.Type, "ident-base:ident-imp", FormatJSON, HintString, mapPrefixes{"ident-base": ib}, l1); d == nil ||
		d.Msg != `Invalid identityref "ident-base:ident-imp" value - identity found in non-implemented module "ident-base".` {
		t.Errorf("not implemented: %v", d)
	}
	ib.Implemented = true
	dis := ident(defs, "dis", base)
	dis.Disabled = true
	if _, d := Store(l1.Type, "dis", FormatJSON, HintString, ModuleNames{set}, l1); d == nil ||
		d.Msg != `Invalid identityref "dis" value - identity is disabled by if-feature.` {
		t.Errorf("disabled: %v", d)
	}
}

type mapPrefixes map[string]*schema.Module

func (m mapPrefixes) Resolve(p string) *schema.Module { return m[p] }

// fakeTree: leafref targets by path → canonical values present in the data.
type fakeTree struct {
	targets map[string][]string
	insts   map[string]bool
	err     error
}

func (f fakeTree) LeafrefTarget(t *schema.Type, v Value) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	for _, c := range f.targets[t.Path] {
		if c == v.Canonical() {
			return true, nil
		}
	}
	return false, nil
}

func (f fakeTree) InstanceExists(p Path) bool { return f.insts[p.String()] }

func lref(path string, target *schema.Type, req bool) *schema.Type {
	return &schema.Type{Base: schema.Leafref, Path: path, Realtype: target, RequireInstance: req}
}

func TestLeafref(t *testing.T) {
	lr := lref("/leaflisttarget", typ(schema.String), true)
	v, d := Store(lr, "y", FormatXML, HintData, nil, nil)
	if d != nil || !v.NeedsTree() || v.Type() != lr.Realtype {
		t.Fatalf("store: %v", d)
	}
	if _, d := ValidateTree(lr, v, fakeTree{targets: map[string][]string{"/leaflisttarget": {"x", "y"}}}); d != nil {
		t.Fatalf("target present: %v", d)
	}
	_, d = ValidateTree(lr, v, fakeTree{targets: map[string][]string{"/leaflisttarget": {"x"}}})
	if d == nil || d.AppTag != "instance-required" ||
		d.Msg != `Invalid leafref value "y" - no target instance "/leaflisttarget" with the same value.` { // leafref.c:296
		t.Fatalf("missing target: %v", d)
	}
	_, d = ValidateTree(lr, v, fakeTree{err: errors.New("boom")})
	if d == nil || d.Msg != `Invalid leafref value "y" - XPath evaluation error (boom).` {
		t.Fatalf("xpath error: %v", d)
	}
	// target type restrictions apply; require-instance false needs no tree
	lu := lref("../target", typ(schema.Uint8, urng(1, 5)), false)
	if _, d := Store(lu, "9", FormatXML, HintData, nil, nil); d == nil || d.Msg != `Unsatisfied range - value "9" is out of the allowed range.` {
		t.Fatalf("target type: %v", d)
	}
	if v, d := Store(lu, "3", FormatXML, HintData, nil, nil); d != nil || v.NeedsTree() || v.Uint() != 3 {
		t.Fatalf("no require-instance: %v", d)
	}
}

func TestUnion(t *testing.T) {
	f := newInstFixture()
	ident1 := ident(f.defs, "ident1")
	ident(f.defs, "ident2", ident1)
	i8, i64 := typ(schema.Int8, rng(10, 20)), typ(schema.Int64)
	// union.c test_data_xml: un1 (nested union flattened by compile, as libyang does)
	un1 := &schema.Type{Base: schema.Union, Union: []*schema.Type{
		lref("/int8", i8, true), lref("/int64", i64, true),
		{Base: schema.IdentityRef, Bases: []*schema.Identity{ident1}},
		{Base: schema.InstanceID, RequireInstance: true},
		typ(schema.String, length(1, 20)),
	}}
	leaf := node(f.defs, nil, schema.Leaf, "un1", un1)
	pc := f.xml(map[string]string{"x": "defs"})
	tree := fakeTree{targets: map[string][]string{"/int8": {"12"}}, insts: map[string]bool{"/defs:llist[.='1']": true}}
	check := func(src, lex string, wantIdx int, wantCanon string) {
		t.Helper()
		v, d := Store(un1, lex, FormatXML, HintData, pc, leaf)
		if d != nil {
			t.Fatalf("%s: %v", src, d)
		}
		if _, i := v.Union().Member(); v.NeedsTree() != (i <= 1 || i == 3) {
			t.Fatalf("%s: NeedsTree %v for member %d", src, v.NeedsTree(), i)
		}
		if v.NeedsTree() { // libyang: validate_tree only after LY_EINCOMPLETE
			if v, d = ValidateTree(un1, v, tree); d != nil {
				t.Fatalf("%s: %v", src, d)
			}
		}
		m, idx := v.Union().Member()
		if idx != wantIdx || v.Canonical() != wantCanon || m.Canonical() != wantCanon {
			t.Fatalf("%s: member %d %q, want %d %q", src, idx, v.Canonical(), wantIdx, wantCanon)
		}
	}
	check("union.c:12", "12", 0, "12")
	check("union.c:2", "2", 4, "2")
	check("union.c:ident2", "x:ident2", 2, "defs:ident2")
	check("union.c:ident55", "x:ident55", 4, "x:ident55")
	check("union.c:llist", "/x:llist[.='1']", 3, "/defs:llist[.='1']")
	check("union.c:llist-missing", "/x:llist[.='2']", 4, "/x:llist[.='2']")

	_, d := Store(un1, "123456789012345678901", FormatXML, HintData, pc, leaf)
	want := "Invalid union value \"123456789012345678901\" - no matching subtype found:\n" +
		"    ly2 leafref: Invalid leafref value \"123456789012345678901\" - no target instance \"/int8\" with the same value.\n" +
		"    ly2 leafref: Invalid leafref value \"123456789012345678901\" - no target instance \"/int64\" with the same value.\n" +
		"    ly2 identityref: Invalid identityref \"123456789012345678901\" value - identity not found in module \"defs\".\n" +
		"    ly2 instance-identifier: Invalid instance-identifier \"123456789012345678901\" value - syntax error.\n" +
		"    ly2 string: Unsatisfied length - string \"123456789012345678901\" length is not allowed.\n"
	if d == nil || d.Msg != want || d.AppTag != "instance-required" {
		t.Fatalf("no member: %v\nwant %q", d, want)
	}

	// RFC 7951 §6.10 via hints: a JSON string never selects the integer member.
	u := &schema.Type{Base: schema.Union, Union: []*schema.Type{typ(schema.Int8), typ(schema.String)}}
	if v, _ := Store(u, "5", FormatJSON, JSONHints("string"), nil, nil); v.Union().index != 1 {
		t.Fatal("JSON string selected int8")
	}
	if v, _ := Store(u, "5", FormatJSON, JSONHints("number"), nil, nil); v.Union().index != 0 || v.Int() != 5 {
		t.Fatal("JSON number did not select int8")
	}
	// StoreOnly falls back to a member that stores without restrictions
	r := &schema.Type{Base: schema.Union, Union: []*schema.Type{typ(schema.Int8, rng(0, 1)), typ(schema.Bool)}}
	if v, d := StoreOnly(r, "7", FormatXML, HintData, nil, nil); d != nil || v.Union().index != 0 {
		t.Fatalf("store only: %v", d)
	}
	// ordering: same member type by value, else by member order
	a, _ := Store(u, "3", FormatXML, HintData, nil, nil)
	b, _ := Store(u, "4", FormatXML, HintData, nil, nil)
	s, _ := Store(u, "x", FormatXML, HintData, nil, nil)
	if Compare(a, b) >= 0 || Compare(a, s) <= 0 || Equal(a, s) || !Equal(a, a) {
		t.Fatal("union order")
	}
}

// TestUnionDefaultReresolves: a schema default keeps its lexical form; "01" selects the
// leafref→uint8 member while the target "1" exists and falls back to the string otherwise.
func TestUnionDefaultReresolves(t *testing.T) {
	u := &schema.Type{Base: schema.Union, Union: []*schema.Type{lref("/t", typ(schema.Uint8), true), typ(schema.String)}}
	dflt := schema.DefaultValue{Lex: "01"}
	v, d := Store(u, dflt.Lex, FormatSchemaResolved, HintSchema, dflt.NS, nil)
	if d != nil || !v.NeedsTree() {
		t.Fatalf("store: %v", d)
	}
	got, d := ValidateTree(u, v, fakeTree{targets: map[string][]string{"/t": {"1"}}})
	if m, i := got.Union().Member(); d != nil || i != 0 || m.Uint() != 1 || got.Canonical() != "1" {
		t.Fatalf("target present: %v %d %q", d, i, got.Canonical())
	}
	got, d = ValidateTree(u, v, fakeTree{})
	if _, i := got.Union().Member(); d != nil || i != 1 || got.Canonical() != "01" {
		t.Fatalf("target absent: %v %d %q", d, i, got.Canonical())
	}
}

// FuzzInstanceID: instance-identifier and union parsing never panic, and a stored path's
// canonical form re-stores to the same value.
func FuzzInstanceID(f *testing.F) {
	for _, s := range []string{"/xdf:cont/xdf:l", "/a:list2[a:id='a:xxx'][a:id2='y']/a:id2", "/t:llist[1", "[1]",
		"/t:cont:t:1l", `/a:list-inst[a:id="/a:llist[.='1']"]/a:value`, "/x:a::b", "/$v", "/a:b[c:d=$x]", "/a:b[.=1.5]",
		"/", "/t:c/t:l", "/t:c/t:l[t:k='x']/t:v", "/t:c/t:ll[.='y']"} {
		f.Add(s)
	}
	fx := newInstFixture()
	pc := fx.xml(map[string]string{"xdf": "defs", "a": "defs", "t": "defs"})
	un := &schema.Type{Base: schema.Union, Union: []*schema.Type{fx.l2.Type, typ(schema.Int8)}}
	nset, _, nid, npath := nacmFixture() // node-instance-identifier mode
	npc := XMLNamespaces{Set: nset, NS: map[string]string{"t": "urn:vibe-ports:ty6", "a": "urn:vibe-ports:ty6"}}
	f.Fuzz(func(t *testing.T, lex string) {
		if nv, d := Store(nid, lex, FormatXML, HintData, npc, npath); d == nil {
			nv2, d := Store(nid, nv.Canonical(), FormatJSON, JSONHints("string"), ModuleNames{nset}, npath)
			if d != nil || !Equal(nv, nv2) {
				t.Fatalf("node-instance-identifier canonical %q of %q: %v", nv.Canonical(), lex, d)
			}
		}
		v, d := Store(fx.l2.Type, lex, FormatXML, HintData, pc, fx.l2)
		_, _ = Store(un, lex, FormatXML, HintData, pc, fx.l2)
		if d != nil {
			return
		}
		v2, d := Store(fx.l2.Type, v.Canonical(), FormatJSON, JSONHints("string"), ModuleNames{fx.set}, fx.l2)
		if d != nil || !Equal(v, v2) {
			t.Fatalf("canonical %q of %q: %v", v.Canonical(), lex, d)
		}
	})
}

// TestAccessorsCopy: slices handed out by Value never alias its storage.
func TestAccessorsCopy(t *testing.T) {
	bt := typ(schema.Bits, bits("a", 0, "b", 1))
	v, _ := Store(bt, "a b", FormatXML, HintData, nil, nil)
	v.Bits()[0] = nil
	bin, _ := Store(typ(schema.Binary), "YQ==", FormatXML, HintString, nil, nil)
	bin.Bytes()[0] = 'z'
	f := newInstFixture()
	p, d := Store(f.l2.Type, "/a:list[a:id='x']/a:value", FormatXML, HintData, f.xml(map[string]string{"a": "defs"}), f.l2)
	if d != nil {
		t.Fatal(d)
	}
	p.Path()[0].Preds[0].Key = nil
	if v.Bits()[0] == nil || bin.Bytes()[0] != 'a' || p.Path()[0].Preds[0].Key == nil {
		t.Fatal("accessor returned shared storage")
	}
}

// TestPathAnydataParent: an anydata/anyxml context node is no schema parent in
// ly_path_compile_snode (path.c:583); the next step is looked up at the module's top level.
func TestPathAnydataParent(t *testing.T) {
	f := newInstFixture()
	for _, k := range []schema.Kind{schema.AnyData, schema.AnyXML} {
		anyd := node(f.defs, nil, k, "any", nil)
		a := &storeArgs{f: FormatXML, pc: f.xml(map[string]string{"xdf": "defs"})}
		n, msg := compileSNode(a, anyd, "xdf:llist", false)
		if msg != "" || n == nil || n.Name != "llist" || n.Parent != nil {
			t.Fatalf("%v: got %v %q, want the top-level llist", k, n, msg)
		}
	}
}
