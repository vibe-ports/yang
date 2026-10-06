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
