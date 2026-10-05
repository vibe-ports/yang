// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"path/filepath"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// TestOracleGoldensDerived replays the derived-type fixtures of conformance/corpus/types (schema
// ty2.yang, hand-compiled below) against Store + ValidateTree; the data the leaf refers to is
// given as a fakeTree.
func TestOracleGoldensDerived(t *testing.T) {
	set := &schema.Set{}
	m := &schema.Module{Name: "ty2", Namespace: "urn:vibe-ports:ty2", Prefix: "t2", Implemented: true}
	set.Modules = append(set.Modules, m)
	base := ident(m, "base")
	ident(m, "der", base)
	ident(m, "other")
	c := node(m, nil, schema.Container, "c", nil)
	idT := &schema.Type{Base: schema.IdentityRef, Bases: []*schema.Identity{base}}
	leaves := map[string]*schema.Node{
		"id":  node(m, c, schema.Leaf, "id", idT),
		"ii":  node(m, c, schema.Leaf, "ii", &schema.Type{Base: schema.InstanceID, RequireInstance: true}),
		"iif": node(m, c, schema.Leaf, "iif", &schema.Type{Base: schema.InstanceID}),
	}
	l := node(m, c, schema.List, "l", nil)
	l.Keys = []*schema.Node{node(m, l, schema.Leaf, "k", typ(schema.String))}
	node(m, l, schema.Leaf, "v", typ(schema.String))
	ll := typ(schema.Uint32)
	node(m, c, schema.LeafList, "ll", ll)
	i8 := typ(schema.Int8, rng(10, 20))
	node(m, c, schema.Leaf, "i8", i8)
	leaves["lr"] = node(m, c, schema.Leaf, "lr", lref("../ll", ll, true))
	leaves["un"] = node(m, c, schema.Leaf, "un", &schema.Type{Base: schema.Union, Union: []*schema.Type{
		lref("../i8", i8, true), idT, {Base: schema.InstanceID, RequireInstance: true}, typ(schema.String, length(1, 20)),
	}})
	xmlPC := XMLNamespaces{Set: set, NS: map[string]string{"": m.Namespace, "x": m.Namespace}}

	leaves["un2"] = node(m, c, schema.Leaf, "un2", &schema.Type{Base: schema.Union,
		Union: []*schema.Type{typ(schema.Int8), typ(schema.String)}})
	cases := []struct {
		id, leaf, lex string
		f             Format
		tree          fakeTree
		member        int // expected union member index, -1 when not a union
	}{
		{"ident-not-derived", "id", "x:other", FormatXML, fakeTree{}, -1},
		{"ident-json", "id", "der", FormatJSON, fakeTree{}, -1},
		{"instid-canonical", "ii", `/x:c/x:l[x:k="a"]/x:v`, FormatXML, fakeTree{}, -1},
		{"instid-pos-config", "iif", "/x:c/x:l[2]", FormatXML, fakeTree{}, -1},
		{"instid-key-type", "iif", "/x:c/x:ll[.='x']", FormatXML, fakeTree{}, -1},
		{"instid-no-instance", "ii", "/x:c/x:ll[.='3']", FormatXML, fakeTree{}, -1},
		{"instid-json-redundant", "iif", "/ty2:c/ty2:ll[.='1']", FormatJSON, fakeTree{}, -1},
		{"leafref-missing", "lr", "2", FormatXML, fakeTree{targets: map[string][]string{"../ll": {"1"}}}, -1},
		{"union-no-member", "un", "123456789012345678901", FormatXML, fakeTree{}, -1},
		{"union-fallback", "un", "15", FormatXML, fakeTree{targets: map[string][]string{"../i8": {"12"}}}, 3},
		{"union-leafref", "un", "12", FormatXML, fakeTree{targets: map[string][]string{"../i8": {"12"}}}, 0},
		{"union-json-ident", "un", "der", FormatJSON, fakeTree{}, 1},
		{"union-json-number", "un2", "5", FormatJSON, fakeTree{}, 0}, // RFC 7951 §6.10: a number selects int8
		{"union-json-string", "un2", "5", FormatJSON, fakeTree{}, 1}, // ... a string never does
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			g := loadGolden(t, filepath.Join("..", "..", "conformance", "corpus", "types", "golden", tc.id+".json"))
			leaf := leaves[tc.leaf]
			var pc PrefixCtx = xmlPC
			h := HintData
			if tc.f == FormatJSON {
				pc, h = ModuleNames{set}, JSONHints("string")
				if tc.id == "union-json-number" {
					h = JSONHints("number")
				}
			}
			v, d := Store(leaf.Type, tc.lex, tc.f, h, pc, leaf)
			if d == nil && v.NeedsTree() {
				v, d = ValidateTree(leaf.Type, v, tc.tree)
			}
			path := "/ty2:c/" + tc.leaf
			if g.Verdict == "invalid" {
				want := g.Diagnostics[0]
				if d == nil || d.Msg != want.Msg || d.AppTag != want.AppTag || d.Code != want.VecodeName || want.DataPath != path {
					t.Fatalf("got %+v, golden %+v", d, want)
				}
				return
			}
			if d != nil {
				t.Fatalf("%s, golden valid", d.Msg)
			}
			if want := g.canonical(path); v.Canonical() != want {
				t.Fatalf("canonical %q, golden %q", v.Canonical(), want)
			}
			if tc.member >= 0 {
				if _, i := v.Union().Member(); i != tc.member {
					t.Fatalf("member %d, want %d", i, tc.member)
				}
			}
		})
	}
}
