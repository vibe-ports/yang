// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// ty24 is conformance/corpus/types/schemas/ty2.yang and ty4.yang, hand-compiled.
type ty24 struct {
	set       *schema.Set
	ty2, ty4  *schema.Module
	der, own  *schema.Identity
	leaves    map[string]*schema.Node
	key, list *schema.Node
}

func newTy24() *ty24 {
	f := &ty24{set: &schema.Set{}, leaves: map[string]*schema.Node{}}
	f.ty2 = &schema.Module{Name: "ty2", Namespace: "urn:vibe-ports:ty2", Prefix: "t2", Implemented: true}
	f.ty4 = &schema.Module{Name: "ty4", Namespace: "urn:vibe-ports:ty4", Prefix: "t4", Implemented: true,
		Imports: []schema.Import{{Prefix: "t2", Module: f.ty2}}}
	f.set.Modules = append(f.set.Modules, f.ty2, f.ty4)
	base := ident(f.ty2, "base")
	f.der = ident(f.ty2, "der", base)
	f.own = ident(f.ty4, "own", base)
	idT := &schema.Type{Base: schema.IdentityRef, Bases: []*schema.Identity{base}}
	c := node(f.ty4, nil, schema.Container, "c", nil)
	leaf := func(name string, t *schema.Type) { f.leaves[name] = node(f.ty4, c, schema.Leaf, name, t) }
	leaf("ext", idT)
	leaf("loc", idT)
	leaf("ii", &schema.Type{Base: schema.InstanceID})
	f.list = node(f.ty4, c, schema.List, "l", nil)
	f.key = node(f.ty4, f.list, schema.Leaf, "k", idT)
	f.list.Keys = []*schema.Node{f.key}
	f.leaves["k"] = f.key
	f.leaves["v"] = node(f.ty4, f.list, schema.Leaf, "v", typ(schema.String))
	f.leaves["ll"] = node(f.ty4, c, schema.LeafList, "ll", idT)
	leaf("un", &schema.Type{Base: schema.Union, Union: []*schema.Type{idT, typ(schema.Int8)}})
	leaf("lr", lref("../ext", idT, false))
	d := node(f.ty4, nil, schema.Container, "d", nil)
	dleaf := func(name string, k schema.Kind, t *schema.Type, dflt ...string) {
		n := node(f.ty4, d, k, name, t)
		ns := schema.NSCtx{"": f.ty4, "t4": f.ty4, "t2": f.ty2}
		for _, s := range dflt {
			n.Default = append(n.Default, schema.DefaultValue{Lex: s, NS: ns})
		}
		f.leaves["d/"+name] = n
	}
	dleaf("dflt", schema.Leaf, idT, "t2:der")
	dleaf("dll", schema.LeafList, idT, "t4:own", "t2:der")
	dleaf("dun", schema.Leaf, &schema.Type{Base: schema.Union, Union: []*schema.Type{typ(schema.Int8), idT}}, "t2:der")
	dleaf("dlr", schema.Leaf, lref("../dflt", idT, false), "t2:der")
	dleaf("dstr", schema.Leaf, typ(schema.String, length(1, 4)), "ab")
	return f
}

// TestOraclePrint prints the leaves of the types/print-prefixes-* fixtures and finds each result
// in the JSON and XML tree libyang printed: the XML namespace declarations come from PrintCtx.Used.
func TestOraclePrint(t *testing.T) {
	f := newTy24()
	xmlPC := func(m map[string]string) PrefixCtx { return XMLNamespaces{Set: f.set, NS: m} }
	type item struct{ leaf, lex string }
	data := []item{{"ext", "t2:der"}, {"loc", "own"}, {"ii", ""}, {"k", "t2:der"}, {"v", "1"}, {"ll", "t2:der"},
		{"ll", "own"}, {"un", "t2:der"}, {"lr", "t2:der"}}
	for _, tc := range []struct {
		id  string
		f   Format
		ii  string
		pc  PrefixCtx
		hts Hints
	}{
		{"print-prefixes-xml", FormatXML, `/x:c/x:l[x:k="y:der"]/x:v`, nil, HintData},
		{"print-prefixes-json", FormatJSON, `/ty4:c/l[k='ty2:der']/v`, ModuleNames{f.set}, JSONHints("string")},
	} {
		t.Run(tc.id, func(t *testing.T) {
			g := loadGolden(t, filepath.Join("..", "..", "conformance", "corpus", "types", "golden", tc.id+".json"))
			if g.Verdict != "valid" || g.Tree == nil {
				t.Fatalf("golden: %s", g.Verdict)
			}
			for _, it := range data {
				n := f.leaves[it.leaf]
				lex, pc := it.lex, tc.pc
				if tc.f == FormatXML {
					pc = xmlPC(map[string]string{"": f.ty4.Namespace, "t2": f.ty2.Namespace, "t4": f.ty4.Namespace})
				}
				if it.leaf == "ii" {
					lex = tc.ii
					if tc.f == FormatXML {
						pc = xmlPC(map[string]string{"x": f.ty4.Namespace, "y": f.ty2.Namespace})
					}
				}
				if lex == "own" && tc.f == FormatJSON {
					lex = "ty4:own"
				}
				if tc.f == FormatJSON && strings.HasPrefix(lex, "t2:") {
					lex = "ty2:" + lex[3:]
				}
				v, d := Store(n.Type, lex, tc.f, tc.hts, pc, n)
				if d != nil {
					t.Fatalf("%s %q: %s", it.leaf, lex, d.Msg)
				}
				// XML: <name xmlns:p="ns">text</name>; a leaf-list entry or key is an element too
				ctx := &PrintCtx{Local: f.ty4}
				text := mustPrint(t, v, FormatXML, ctx)
				decl := ""
				for _, m := range ctx.Used {
					decl += fmt.Sprintf(" xmlns:%s=%q", m.Prefix, m.Namespace)
				}
				if want := "<" + it.leaf + decl + ">" + text + "</" + it.leaf + ">"; !strings.Contains(g.Tree.XML, want) {
					t.Errorf("XML %s: %s not in\n%s", it.leaf, want, g.Tree.XML)
				}
				// JSON: the string of the member, or the array entry
				if want := fmt.Sprintf("%q", mustPrint(t, v, FormatJSON, &PrintCtx{Local: f.ty4})); !strings.Contains(g.Tree.JSON, want) {
					t.Errorf("JSON %s: %s not in\n%s", it.leaf, want, g.Tree.JSON)
				}
			}
		})
	}
}

func mustPrint(t *testing.T, v Value, f Format, pc *PrintCtx) string {
	t.Helper()
	s, err := Print(v, f, pc)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestPrintFormats: the prefix forms of identityref and instance-identifier values that the
// printers do not reach (schema text formats), the canonical form and the pass-through types.
func TestPrintFormats(t *testing.T) {
	f := newTy24()
	xmlNS := XMLNamespaces{Set: f.set, NS: map[string]string{"a": f.ty2.Namespace, "b": f.ty4.Namespace}}
	id := func(name string) Value {
		n := f.leaves["ext"]
		v, d := Store(n.Type, name, FormatXML, HintData, xmlNS, n)
		if d != nil {
			t.Fatal(d.Msg)
		}
		return v
	}
	der, own := id("a:der"), id("b:own")
	ctxNode := f.leaves["ext"]
	ii, d := Store(f.leaves["ii"].Type, `/b:c/b:l[b:k="a:der"]/b:v`, FormatXML, HintData, xmlNS, ctxNode)
	if d != nil {
		t.Fatal(d.Msg)
	}
	for _, tc := range []struct {
		name string
		v    Value
		f    Format
		pc   *PrintCtx
		want string
	}{
		{"json foreign", der, FormatJSON, &PrintCtx{Local: f.ty4}, "ty2:der"},
		{"json local", own, FormatJSON, &PrintCtx{Local: f.ty4}, "own"},
		{"json no local module", own, FormatJSON, nil, "ty4:own"},
		{"canonical", der, FormatCanon, &PrintCtx{Local: f.ty2}, "ty2:der"},
		{"xml local", der, FormatXML, &PrintCtx{Local: f.ty2}, "der"},
		{"xml no ctx", der, FormatXML, nil, "t2:der"},
		{"schema own prefix", own, FormatSchema, &PrintCtx{Local: f.ty4}, "t4:own"},
		{"schema import prefix", der, FormatSchema, &PrintCtx{Local: f.ty4}, "t2:der"},
		{"schema unreachable module", own, FormatSchema, &PrintCtx{Local: f.ty2}, "own"},
		{"instance-id json is canonical", ii, FormatJSON, &PrintCtx{Local: f.ty4}, "/ty4:c/l[k='ty2:der']/v"},
		{"instance-id xml prefixes all", ii, FormatXML, &PrintCtx{Local: f.ty4}, "/t4:c/t4:l[t4:k='t2:der']/t4:v"},
		{"instance-id schema", ii, FormatSchema, &PrintCtx{Local: f.ty4}, "/t4:c/t4:l[t4:k='t2:der']/t4:v"},
		{"instance-id unprefixable", ii, FormatSchema, &PrintCtx{Local: f.ty2}, "/(null):c/(null):l[(null):k='t2:der']/(null):v"},
	} {
		if got := mustPrint(t, tc.v, tc.f, tc.pc); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	for _, fm := range []Format{FormatSchemaResolved, Format(99)} {
		if _, err := Print(der, fm, nil); !errors.Is(err, ErrUnsupported) {
			t.Errorf("format %d: %v", fm, err)
		}
	}
	// an XML instance-identifier prefixes every node, Local's module too: it is listed in Used
	pc := &PrintCtx{Local: f.ty4}
	mustPrint(t, ii, FormatXML, pc)
	if len(pc.Used) != 2 || pc.Used[0] != f.ty4 || pc.Used[1] != f.ty2 || pc.Local != f.ty4 {
		t.Errorf("used %v local %v", pc.Used, pc.Local)
	}

	// other types and unions: the canonical form in every format
	for _, tc := range []struct {
		t        *schema.Type
		lex, out string
	}{
		{typ(schema.Int8), "+10", "10"}, {typ(schema.Dec64, fd(2)), "3.10", "3.1"},
		{typ(schema.Bits, bits("a", 0, "b", 1)), "b a", "a b"}, {typ(schema.Bool), "true", "true"},
		{typ(schema.Binary), "YQ==", "YQ=="},
	} {
		v, d := Store(tc.t, tc.lex, FormatXML, HintData, nil, nil)
		if d != nil {
			t.Fatal(d.Msg)
		}
		for _, fm := range []Format{FormatCanon, FormatJSON, FormatXML, FormatSchema} {
			if got := mustPrint(t, v, fm, nil); got != tc.out {
				t.Errorf("%s format %d: %q", tc.t.Base, fm, got)
			}
		}
	}
	un := f.leaves["un"]
	v, d := Store(un.Type, "b:own", FormatXML, HintData, xmlNS, un)
	if d != nil {
		t.Fatal(d.Msg)
	}
	if got := mustPrint(t, v, FormatJSON, &PrintCtx{Local: f.ty4}); got != "own" { // the member prints, qualified for f
		t.Errorf("union member: %q", got)
	}
	v, _ = Store(un.Type, "7", FormatXML, HintData, xmlNS, un)
	if got := mustPrint(t, v, FormatXML, nil); got != "7" {
		t.Errorf("union int member: %q", got)
	}
}

// TestOracleDefaults stores the schema defaults of the types/default-store fixture and finds them
// (value and union member) in what libyang created as implicit nodes.
func TestOracleDefaults(t *testing.T) {
	f := newTy24()
	g := loadGolden(t, filepath.Join("..", "..", "conformance", "corpus", "types", "golden", "default-store.json"))
	for _, name := range []string{"dflt", "dll", "dun", "dlr", "dstr"} {
		n := f.leaves["d/"+name]
		for _, d := range n.Default {
			v, diag := StoreDefault(n, d)
			if diag != nil {
				t.Fatalf("%s: %s", name, diag.Msg)
			}
			path := "/ty4:d/" + name
			if n.Kind == schema.LeafList {
				path += "[.='" + v.Canonical() + "']"
			}
			if want := g.canonical(path); v.Canonical() != want {
				t.Errorf("%s: canonical %q, golden %q (%s)", name, v.Canonical(), want, path)
			}
			if v.NeedsTree() {
				t.Errorf("%s: incomplete", name)
			}
		}
	}
	if v, _ := StoreDefault(f.leaves["d/dun"], f.leaves["d/dun"].Default[0]); v.Union() == nil {
		t.Error("dun is not a union value")
	} else if _, i := v.Union().Member(); i != 1 { // golden union_member.index
		t.Errorf("dun member %d", i)
	}
	// restrictions are not checked (StoreOnly), a require-instance leafref stays incomplete
	short := typ(schema.String, length(1, 2))
	sn := &schema.Node{Kind: schema.Leaf, Name: "s", Module: f.ty4, Type: short}
	if _, d := StoreDefault(sn, schema.DefaultValue{Lex: "abc"}); d != nil {
		t.Errorf("restriction checked: %s", d.Msg)
	}
	ln := &schema.Node{Kind: schema.Leaf, Name: "r", Module: f.ty4, Type: lref("../x", typ(schema.String), true)}
	if v, d := StoreDefault(ln, schema.DefaultValue{Lex: "abc"}); d != nil || !v.NeedsTree() {
		t.Errorf("require-instance default: %v", d)
	}
}
