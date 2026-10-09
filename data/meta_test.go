// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// TestInternalAnnotations looks up the annotations libyang adds to yang, ietf-netconf and
// ietf-netconf-with-defaults (lysp_add_internal_*) as metadata of those modules.
func TestInternalAnnotations(t *testing.T) {
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS("../conformance/corpus/ietf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ietf-netconf", "ietf-netconf-with-defaults"} {
		if _, diags, err := c.Load(name, "", nil); err != nil {
			t.Fatal(name, err, diags)
		}
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	enums := func(t *schema.Type) string {
		var s []string
		for _, e := range t.Enums {
			s = append(s, e.Name)
		}
		return strings.Join(s, " ")
	}
	for _, tc := range []struct{ mod, name, want string }{
		{"ietf-netconf", "operation", "enum merge replace create delete remove"},
		{"ietf-netconf", "type", "enum subtree xpath"},
		{"ietf-netconf", "select", "ietf-yang-types:xpath1.0"},
		{"ietf-netconf-with-defaults", "default", "bool"},
		{"yang", "lyds_tree", "yang:lyds_tree"},
		{"yang", "insert", "enum first last before after"}, // from yang.yang itself
	} {
		a := metaAnnotation(set.Module(tc.mod, ""), tc.name)
		if a == nil || a.Type == nil {
			t.Errorf("%s:%s: no annotation", tc.mod, tc.name)
			continue
		}
		got := ""
		switch {
		case a.Type.Base == schema.Enumeration:
			got = "enum " + enums(a.Type)
		case a.Type.Base == schema.Bool:
			got = "bool"
		case a.Type.TypedefModule != nil:
			got = a.Type.TypedefModule.Name + ":" + a.Type.Typedef
		}
		if got != tc.want {
			t.Errorf("%s:%s: type %q, want %q", tc.mod, tc.name, got, tc.want)
		}
	}
	if metaAnnotation(set.Module("ietf-netconf", ""), "default") != nil {
		t.Error("ietf-netconf has no default annotation")
	}
	// the internal data nodes, last of the data (before the rpcs and notifications)
	top := func(mod, name string) *schema.Node {
		for _, n := range set.Module(mod, "").Top {
			if n.Name == name {
				return n
			}
		}
		t.Fatalf("%s: no top node %s", mod, name)
		return nil
	}
	if n := top("ietf-netconf", "rpc-error"); !n.Presence || len(n.Children) != 5 {
		t.Errorf("rpc-error: presence %v, %d children", n.Presence, len(n.Children))
	}
	if n := top("yang", "date-and-time"); n.Type.Typedef != "date-and-time" {
		t.Errorf("date-and-time: type %s", n.Type.Typedef)
	}
}

// withAnnotations adds the md:annotations ann (string), num (uint8) and iid (a required
// instance-identifier) to module b of the fixture.
func (f *fixture) withAnnotations() {
	md := &schema.Module{Name: "ietf-yang-metadata", Revision: "2016-08-05"}
	ann := func(name string, t *schema.Type) *schema.ExtInstance {
		return &schema.ExtInstance{Def: md, Name: "annotation", Argument: name, Type: t}
	}
	f.b.Exts = []*schema.ExtInstance{ann("ann", f.str), ann("num", f.u8),
		ann("iid", &schema.Type{Base: schema.InstanceID, RequireInstance: true})}
}

// TestCreateMeta: lyd_parser_create_meta — an unknown annotation is LY_EINVAL after its LOGVAL, a
// rejected value is logged at the node plus "/@mod:name", a value that needs the tree is queued
// unless parse-only; the XML form (no parent yet) logs at the schema node.
func TestCreateMeta(t *testing.T) {
	f := newFixture()
	f.withAnnotations()
	lc := &lydCtx{ctx: context.Background(), tree: newTree(f.set), log: &logger{set: f.set}}
	c, _ := lc.createInner(f.c)
	lc.nodeInsert(nil, nil, c)
	z, _ := lc.json(f.z, c, "v")
	lc.nodeInsert(c, nil, z)
	pc := types.ModuleNames{Set: f.set}
	js := func(name, lex string, h types.Hints) error {
		return lc.createMeta(z, &z.meta, f.b, name, lex, types.FormatJSON, pc, h, z.schema, z)
	}
	if err := js("nope", "x", types.JSONHints("string")); !errors.Is(err, errLoggedFatal) || !lc.fatal(err) {
		t.Fatalf("unknown annotation: %v", err)
	}
	if err := js("num", "x", types.JSONHints("string")); !lc.isEValid(err) {
		t.Fatalf("bad value: %v", err)
	}
	if err := js("ann", "a", types.JSONHints("string")); err != nil || len(z.meta) != 1 || z.meta[0].value.Canonical() != "a" {
		t.Fatalf("ann: %v %v", err, z.meta)
	}
	if err := js("iid", "/b:top", types.JSONHints("string")); err != nil || len(lc.metaTypes) != 1 || lc.metaTypes[0] != z.meta[1] {
		t.Fatalf("iid: %v %d", err, len(lc.metaTypes))
	}
	var xml []*meta
	if err := lc.createMeta(nil, &xml, f.b, "nope", "x", types.FormatXML, nil, types.HintData, f.z, c); !errors.Is(err, errLoggedFatal) || len(xml) != 0 {
		t.Fatalf("xml unknown: %v", err)
	}
	lc.opts.ParseOnly = true
	if err := js("iid", "/b:top", types.JSONHints("string")); err != nil || len(lc.metaTypes) != 1 {
		t.Fatalf("parse-only queued: %v %d", err, len(lc.metaTypes))
	}
	want := []string{
		`LY_EVALID LYVE_REFERENCE /b:c/z/@b:nope: Annotation definition for attribute "b:nope" not found.`,
		`LY_EVALID LYVE_DATA /b:c/z/@b:num: Invalid non-number-encoded uint8 value "x".`,
		`LY_EVALID LYVE_REFERENCE /b:c/z/@b:nope: Annotation definition for attribute "b:nope" not found.`,
	}
	if got := codes(lc.log.diags); !reflect.DeepEqual(got, want) || lc.log.diags[2].SchemaPath != "/b:c/z/@b:nope" {
		t.Fatalf("%q", got)
	}
}

// pjSchema is a hand-built compiled schema (design 06 §4 shapes) of
//
//	module pj { namespace urn:pj; md:annotation ann {type string;} md:annotation num {type uint8;}
//	  container c { leaf s {string} leaf n {int32} leaf e {empty} leaf-list ll {uint8}
//	    list l { key k; leaf k; leaf v; } list l2 { key "a b"; leaf a; leaf b; leaf v; }
//	    container in { leaf x; } }
//	  leaf top {string} anydata ad; }
//	module pk { namespace urn:pk; augment /pj:c { leaf ext {string} } leaf top {string} }
//
// the schema of the JSON and XML parser probes (libyang v5.8.6 oracle, linux/amd64, 2026-10-06).
func pjSchema() *schema.Set {
	str, u8 := &schema.Type{Base: schema.String}, &schema.Type{Base: schema.Uint8}
	md := &schema.Module{Name: "ietf-yang-metadata", Revision: "2016-08-05", Namespace: "urn:ietf:params:xml:ns:yang:ietf-yang-metadata"}
	pj := &schema.Module{Name: "pj", Namespace: "urn:pj", Prefix: "pj", Implemented: true, Exts: []*schema.ExtInstance{
		{Def: md, Name: "annotation", Argument: "ann", Type: str},
		{Def: md, Name: "annotation", Argument: "num", Type: u8},
	}}
	pk := &schema.Module{Name: "pk", Namespace: "urn:pk", Prefix: "pk", Implemented: true}
	add := func(m *schema.Module, p *schema.Node, k schema.Kind, name string, t *schema.Type) *schema.Node {
		n := &schema.Node{Kind: k, Name: name, Module: m, Parent: p, Type: t, Config: true}
		if p == nil {
			m.Top = append(m.Top, n)
		} else {
			p.Children = append(p.Children, n)
		}
		return n
	}
	c := add(pj, nil, schema.Container, "c", nil)
	add(pj, c, schema.Leaf, "s", str)
	add(pj, c, schema.Leaf, "n", &schema.Type{Base: schema.Int32})
	add(pj, c, schema.Leaf, "e", &schema.Type{Base: schema.Empty})
	add(pj, c, schema.LeafList, "ll", u8)
	l := add(pj, c, schema.List, "l", nil)
	l.Keys = []*schema.Node{add(pj, l, schema.Leaf, "k", str)}
	add(pj, l, schema.Leaf, "v", str)
	l2 := add(pj, c, schema.List, "l2", nil)
	l2.Keys = []*schema.Node{add(pj, l2, schema.Leaf, "a", str), add(pj, l2, schema.Leaf, "b", str)}
	add(pj, l2, schema.Leaf, "v", str)
	in := add(pj, c, schema.Container, "in", nil)
	add(pj, in, schema.Leaf, "x", str)
	add(pj, nil, schema.Leaf, "top", str)
	add(pj, nil, schema.AnyData, "ad", nil)
	add(pk, c, schema.Leaf, "ext", str) // augment
	add(pk, nil, schema.Leaf, "top", str)
	return &schema.Set{Modules: []*schema.Module{md, pj, pk}}
}

// diagLine is one diagnostic as the probes list it: err vecode data-path|schema-path|line: msg.
func diagLine(d yang.Diagnostic) string {
	return fmt.Sprintf("%s %s %s|%s|%d: %s", d.Err, d.Code, d.DataPath, d.SchemaPath, d.Line, d.Msg)
}

// dumpTree lists the tree in pre-order: the data path, the value, opaque hints, metadata and
// attributes.
func dumpTree(t *Tree) []string {
	var out []string
	for top := range t.Top() {
		for n := range top.All() {
			s := lydPath(t.set, n, false)
			switch {
			case n.schema == nil:
				s += fmt.Sprintf(" opaque=%q %#x", n.opaq.Value, uint32(n.opaq.Hints))
				for _, a := range n.opaq.Attrs {
					s += fmt.Sprintf(" @%s:%s=%s", a.Prefix, a.Name, a.Value)
				}
			case n.isTerm():
				s += "=" + n.value.Canonical()
			}
			for _, m := range n.meta {
				s += fmt.Sprintf(" @%s:%s=%s", m.mod.Name, m.name, m.value.Canonical())
			}
			out = append(out, s)
		}
	}
	return out
}

// TestMetaAPI: lyd_new_meta, lyd_change_meta, lyd_compare_meta, lyd_find_meta and lyd_new_attr
// over a parsed tree, with libyang's messages (tree_data_new.c, tree_data.c; the value error is
// the type plugin's, located at the parent's schema node as ly_err_print does without a data node).
func TestMetaAPI(t *testing.T) {
	set := pjSchema()
	pj := set.Modules[1]
	tr, diags, err := parseJSONString(set, `{"pj:c": {"s": "a"}, "zz:o": 1}`, Opaque, true)
	if err != nil {
		t.Fatalf("%v %v", err, diags)
	}
	var c, s, o *Node
	for n := range tr.Top() {
		if n.schema != nil {
			c = n
		} else {
			o = n
		}
	}
	for n := range c.Children() {
		s = n
	}
	l := &logger{set: set}
	last := func() string {
		if len(l.diags) == 0 {
			return ""
		}
		return diagLine(l.diags[len(l.diags)-1])
	}
	value := func(m *meta) string { return m.value.Canonical() }

	// lyd_new_meta
	ann, err := l.newMeta(s, nil, "pj:ann", "x", false)
	if err != nil || len(s.meta) != 1 || s.meta[0] != ann || value(ann) != "x" {
		t.Fatalf("new pj:ann: %v %v", err, l.diags)
	}
	if m, err := l.newMeta(s, pj, "num", "7", false); err != nil || len(s.meta) != 2 || value(m) != "7" {
		t.Fatalf("new num of pj: %v %v", err, l.diags)
	}
	if m, err := l.newMeta(nil, pj, "ann", "", false); err != nil || value(m) != "" || len(s.meta) != 2 {
		t.Fatalf("detached: %v", err)
	}
	c.flags |= FlagDefault
	if _, err := l.newMeta(c, nil, "pj:ann", "d", true); err != nil || c.flags&FlagDefault != 0 {
		t.Fatalf("clear default: %v %x", err, c.flags)
	}
	if _, err := l.newMeta(s, nil, "ann", "x", false); err == nil ||
		err.Error() != "Invalid argument module || strchr(name, ':') (lyd_new_meta())." {
		t.Fatalf("no module: %v", err)
	}
	for _, e := range []struct {
		parent              *Node
		name, val, want, rc string // rc: the LY_ERR returned when it is not the logged one
	}{
		{s, "pj:num", "300", `LY_EVALID LYVE_DATA |/pj:c/s|0: Value "300" is out of type uint8 min/max bounds.`, ""},
		{s, "nope:ann", "x", `LY_EINVAL LYVE_SUCCESS ||0: Module "nope" not found.`, "LY_ENOTFOUND"},
		{s, "pj:zz", "x", `LY_EVALID LYVE_REFERENCE /pj:c/s||0: Annotation definition for attribute "pj:zz" not found.`,
			"LY_EINVAL"},
		{s, "pj:", "x", `LY_EINVAL LYVE_SUCCESS ||0: Metadata name "" is not valid.`, ""},
		{s, "pj:a b", "x", `LY_EINVAL LYVE_SUCCESS ||0: Metadata name "a b" is not valid.`, ""},
		{s, "1x:ann", "x", `LY_EINVAL LYVE_SUCCESS ||0: Metadata name "(null)" is not valid.`, ""},
		{o, "pj:ann", "x", `LY_EINVAL LYVE_SUCCESS ||0: Cannot add metadata "pj:ann" to an opaque node "o".`, ""},
	} {
		_, err := l.newMeta(e.parent, nil, e.name, e.val, false)
		ok := errors.Is(err, errLogged)
		if e.rc != "" {
			ok = err == rcError(e.rc) //nolint:errorlint // newMeta returns it unwrapped
		}
		if !ok || last() != e.want {
			t.Errorf("new %s=%s: %v\n%s\nwant\n%s", e.name, e.val, err, last(), e.want)
		}
	}

	// lyd_change_meta, lyd_compare_meta
	if changed, err := l.changeMeta(s, ann, "x"); changed || err != nil {
		t.Fatalf("change to the same value: %v %v", changed, err)
	}
	if changed, err := l.changeMeta(s, ann, "y"); !changed || err != nil || value(ann) != "y" {
		t.Fatalf("change: %v %v %s", changed, err, value(ann))
	}
	num := s.meta[1]
	if _, err := l.changeMeta(s, num, "-1"); !errors.Is(err, errLogged) || value(num) != "7" ||
		last() != `LY_EVALID LYVE_DATA |/pj:c/s|0: Value "-1" is out of type uint8 min/max bounds.` {
		t.Fatalf("bad change: %v %s %s", err, value(num), last())
	}
	other, _ := l.newMeta(nil, pj, "ann", "y", false)
	if !compareMeta(nil, nil) || compareMeta(ann, nil) || !compareMeta(ann, other) || compareMeta(ann, num) {
		t.Fatal("compare")
	}
	if _, err := l.changeMeta(nil, other, "z"); err != nil || compareMeta(ann, other) {
		t.Fatal("compare after a change")
	}

	// lyd_find_meta
	if m, err := l.findMeta(s.meta, nil, "pj:num"); m != num || err != nil {
		t.Fatalf("find pj:num: %v %v", m, err)
	}
	if m, err := l.findMeta(s.meta, pj, "ann"); m != ann || err != nil {
		t.Fatalf("find ann of pj: %v %v", m, err)
	}
	if m, err := l.findMeta(s.meta, set.Modules[2], "ann"); m != nil || err != nil {
		t.Fatalf("find ann of pk: %v %v", m, err)
	}
	n := len(l.diags)
	if m, err := l.findMeta(nil, nil, "nope:x"); m != nil || err != nil || len(l.diags) != n {
		t.Fatalf("find in no metadata: %v %v", m, err)
	}
	if _, err := l.findMeta(s.meta, nil, "nope:ann"); !errors.Is(err, errLogged) ||
		last() != `LY_EINVAL LYVE_SUCCESS ||0: Module "nope" not found.` {
		t.Fatalf("find unknown module: %v %s", err, last())
	}

	// lyd_new_attr
	for _, e := range []struct {
		mod, name, val string
		want           attr
	}{
		{"", "pj:a", "1", attr{Name: "a", Prefix: "pj", ModuleNS: "pj", Value: "1"}},
		{"zz", "b", "", attr{Name: "b", ModuleNS: "zz"}},
		{"", "xml:lang", "en", attr{Name: "xml:lang", Value: "en"}},
	} {
		if err := l.newAttr(o, e.mod, e.name, e.val); err != nil {
			t.Fatal(err)
		}
		e.want.Format, e.want.Hints = types.FormatJSON, types.HintData
		if got := o.opaq.Attrs[len(o.opaq.Attrs)-1]; !reflect.DeepEqual(got, e.want) {
			t.Errorf("attr %s: %+v", e.name, got)
		}
	}
	if err := l.newAttr(o, "", "a:", "1"); !errors.Is(err, errLogged) ||
		last() != `LY_EINVAL LYVE_SUCCESS ||0: Attribute name "" is not valid.` {
		t.Fatalf("bad attribute name: %v %s", err, last())
	}
	if err := l.newAttr(s, "", "a", "1"); err == nil || err.Error() != "Invalid argument !parent->schema (lyd_new_attr())." {
		t.Fatalf("attribute of a schema node: %v", err)
	}
}
