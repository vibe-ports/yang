// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
)

func evalAnyString(ctx context.Context, t *testing.T, n *Node, maxSteps int) (xpath.Result, error) {
	t.Helper()
	e, err := xpath.Compile("string(.)", nil)
	if err != nil {
		t.Fatal(err)
	}
	return e.Eval(xpath.EvalContext{Ctx: ctx, Node: xn{n, treeOf(n).set}, MaxSteps: maxSteps})
}

// cast_string_recursive prints the entire top-level forest for a child-backed
// any node, while lyd_any_value_str prints only the payload siblings.
func TestAnyXPathCastCommittedGoldens(t *testing.T) {
	set := corpusSet(t, []string{"../conformance/corpus/any-core/schemas"}, []string{"ac-r6"})
	x := set.Implemented("ac-r6").Top[1]
	if x.Name != "xml" {
		t.Fatalf("unexpected schema node %s", x.Name)
	}
	for _, tc := range []struct {
		golden string
		text   *string
	}{
		{golden: "xpath-anyxml-child-only"},
		{golden: "xpath-anyxml-mixed-text-first", text: strPtr("plain text")},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "conformance", "corpus", "any-core", "golden", tc.golden+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var golden struct{ Result struct{ Value string } }
			if err := json.Unmarshal(raw, &golden); err != nil {
				t.Fatal(err)
			}
			tr := newTree(set)
			n := &Node{schema: x, any: &anyValue{text: tc.text}}
			tr.insert(nil, n, insertDefault)
			tr.insert(n, newOpaque(opaque{Name: "target", Value: "value", ModuleNS: x.Module.Namespace, Format: types.FormatXML, set: set}), insertLast)
			r, err := evalAnyString(context.Background(), t, n, 1000)
			if err != nil || r.Str != golden.Result.Value {
				t.Fatalf("XPath cast = %q, %v; golden = %q", r.Str, err, golden.Result.Value)
			}
			payload, ok, err := n.AnyValueString(FormatXML)
			if err != nil || !ok || strings.Contains(payload, "<xml") {
				t.Fatalf("payload-only print = %q, %t, %v", payload, ok, err)
			}
		})
	}
}

func TestAnyXPathCastClimbsAncestorAndSiblings(t *testing.T) {
	tr, _, x := anyFixture()
	m := x.Module
	outer := &schema.Node{Name: "outer", Kind: schema.Container, Module: m}
	nested := &schema.Node{Name: "xml", Kind: schema.AnyXML, Module: m, Parent: outer}
	outer.Children = []*schema.Node{nested}
	m.Top = append(m.Top, outer)
	before := &Node{schema: x, any: &anyValue{}}
	tr.insert(nil, before, insertDefault)
	tr.insert(before, newOpaque(opaque{Name: "before", Format: types.FormatXML, ModuleNS: m.Namespace}), insertLast)
	parent := newInner(outer)
	tr.insert(nil, parent, insertDefault)
	n := &Node{schema: nested, any: &anyValue{}}
	tr.insert(parent, n, insertDefault)
	tr.insert(n, newOpaque(opaque{Name: "inside", Format: types.FormatXML, ModuleNS: m.Namespace}), insertLast)
	var want bytes.Buffer
	if err := tr.PrintXML(&want, PrintOptions{}); err != nil {
		t.Fatal(err)
	}
	r, err := evalAnyString(context.Background(), t, n, 1000)
	if err != nil || r.Str != want.String() {
		t.Fatalf("XPath cast = %q, %v; forest = %q", r.Str, err, want.String())
	}
	if payload, _, err := n.AnyValueString(FormatXML); err != nil || strings.Contains(payload, "<outer") || strings.Contains(payload, "<before") {
		t.Fatalf("payload-only print = %q, %v", payload, err)
	}
}

func TestAnyXPathCastLargeValueSmallBudget(t *testing.T) {
	tr, _, x := anyFixture()
	n := &Node{schema: x, any: &anyValue{}}
	tr.insert(nil, n, insertDefault)
	value := strings.Repeat("&", 16<<20)
	for i := 0; i < 200; i++ {
		tr.insert(n, newOpaque(opaque{Name: "v", ModuleNS: x.Module.Namespace, Value: value, Format: types.FormatXML}), insertLast)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := evalAnyString(context.Background(), t, n, 300)
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(value)
	if !errors.Is(err, xpath.ErrBudget) {
		t.Fatalf("large escaped payload: got %v, want XPath budget", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("budgeted print allocated %d bytes; want at most 8 MiB", allocated)
	}
}

// cast_string_recursive indents every line of a nested any value. The budget covers the whole
// cast string, not only the value's source text, and stops before the text is built.
func TestAnyXPathCastChargesIndentation(t *testing.T) {
	const depth, lines = 4000, 1 << 14
	m := &schema.Module{Name: "anycore", Namespace: "urn:anycore", Implemented: true}
	top := &schema.Node{Name: "c", Kind: schema.Container, Module: m}
	p := top
	for range depth - 1 {
		c := &schema.Node{Name: "c", Kind: schema.Container, Module: m, Parent: p}
		p.Children = []*schema.Node{c}
		p = c
	}
	x := &schema.Node{Name: "x", Kind: schema.AnyXML, Module: m, Parent: p}
	p.Children = []*schema.Node{x}
	m.Top = []*schema.Node{top}
	tr := newTree(&schema.Set{Modules: []*schema.Module{m}})
	root := newInner(top)
	tr.insert(nil, root, insertDefault)
	parent := root
	for sn := top; len(sn.Children) > 0 && sn.Children[0].Kind == schema.Container; sn = sn.Children[0] {
		c := newInner(sn.Children[0])
		tr.insert(parent, c, insertDefault)
		parent = c
	}
	text := strings.Repeat("a\n", lines) // 32 KiB of source, 128 MiB indented
	tr.insert(parent, &Node{schema: x, any: &anyValue{text: &text}}, insertDefault)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := evalAnyString(context.Background(), t, root, 50_000)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, xpath.ErrBudget) {
		t.Fatalf("indented cast: got %v, want XPath budget", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 32<<20 {
		t.Fatalf("budgeted cast allocated %d bytes; want at most 32 MiB", allocated)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := evalAnyString(ctx, t, root, 1<<30); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled indented cast: got %v", err)
	}
}

// These payloads mirror print/ut-xml-any-01..07. The anydata parser is still
// unsupported, so parse only an opaque wrapper, then copy its children into a
// constructed anydata node. Compare the printers with the oracle's tree output.
func TestAnyPrintOracleGoldens(t *testing.T) {
	set := corpusSet(t, []string{"../conformance/corpus/print/schemas"}, []string{"defs", "types", "defaults"})
	sn := set.Implemented("types").Top[2]
	if sn.Name != "any" {
		t.Fatalf("unexpected any schema: %s", sn.Name)
	}
	for _, tc := range []struct {
		id, payload string
		shrink      bool
	}{
		{"01", `<somexml xmlns:x="url:x" xmlns="example.com"><x:x/></somexml>`, true},
		{"02", ``, true},
		{"03", "\n  <cont>\n    <defs:elem1 xmlns:defs=\"urn:tests:defs\">\n      <elem2 xmlns:defaults=\"urn:defaults\" defs:attr1=\"defaults:val\" attr2=\"/defaults:node/defs:node2\">\n      </elem2>\n    </defs:elem1>\n  </cont>\n", false},
		{"04", "\n  <ahoj attr=\"&lt;test\">\n       ahoj jak se vede &lt; how are you  </ahoj>\n", false},
		{"05", "\n  <leaflisttarget> ahoj   </leaflisttarget>\n  <leaflisttarget> nazdar </leaflisttarget>\n  <leaflisttarget> Čau    </leaflisttarget>\n", false},
		{"06", "\n  <cont2/>\n", false},
		{"07", "\n  <cont>\n        &lt; how are you  </cont>\n", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			var golden struct{ Tree, Subtree struct{ JSON, XML string } }
			raw, err := os.ReadFile(filepath.Join("../conformance/corpus/print/golden", "ut-xml-any-"+tc.id+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &golden); err != nil {
				t.Fatal(err)
			}
			tr := newTree(set)
			n := &Node{schema: sn, any: &anyValue{}}
			tr.insert(nil, n, insertDefault)
			if tc.payload != "" {
				fragment, diags, err := parseXMLString(set, `<wrapper xmlns="urn:tests:types">`+tc.payload+`</wrapper>`, Opaque, true)
				if err != nil {
					t.Fatalf("payload parse: %v, %+v", err, diags)
				}
				if err := n.copyAnyValue(newAnyWork(context.Background(), Budget{}), fragment.top.first().kids.first(), nil, 0); err != nil {
					t.Fatalf("payload copy: %v", err)
				}
			}
			var j, xml bytes.Buffer
			opts := PrintOptions{Shrink: tc.shrink}
			if err := tr.PrintJSON(&j, opts); err != nil {
				t.Fatal(err)
			}
			if err := tr.PrintXML(&xml, opts); err != nil {
				t.Fatal(err)
			}
			if j.String() != golden.Tree.JSON || xml.String() != golden.Tree.XML {
				t.Errorf("JSON: %q, want %q; XML: %q, want %q", j.String(), golden.Tree.JSON, xml.String(), golden.Tree.XML)
			}
		})
	}
}

func TestAnyPrintJSONOpaqueParentUsesOwnContext(t *testing.T) {
	p := &schema.Module{Name: "p", Namespace: "urn:p", Prefix: "p", Implemented: true}
	v := &schema.Node{Name: "v", Kind: schema.Leaf, Module: p, Type: &schema.Type{Base: schema.String}, Config: true}
	p.Top = []*schema.Node{v}
	source := &schema.Set{Modules: []*schema.Module{p}}
	h := &schema.Module{Name: "h", Namespace: "urn:h", Prefix: "h", Implemented: true}
	a := &schema.Node{Name: "a", Kind: schema.AnyData, Module: h, Config: true}
	h.Top = []*schema.Node{a}
	target := &schema.Set{Modules: []*schema.Module{h}}
	tr := newTree(target)
	anyNode := &Node{schema: a, any: &anyValue{}}
	tr.insert(nil, anyNode, insertDefault)
	parent := newOpaque(opaque{Name: "parent", ModuleNS: p.Namespace, Format: types.FormatXML, set: source})
	tr.insert(anyNode, parent, insertLast)
	value, diag := types.Store(v.Type, "x", types.FormatJSON, types.HintString, nil, v)
	if diag != nil {
		t.Fatal(diag)
	}
	tr.insert(parent, newTerm(v, value), insertDefault)
	var out bytes.Buffer
	if err := tr.PrintJSON(&out, PrintOptions{Shrink: true}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), `{"h:a":{"parent":{"v":"x"}}}`; strings.TrimSpace(got) != want {
		t.Fatalf("cross-context payload JSON = %q, want %q", got, want)
	}
}

// Existing any-core operation goldens also exercise scalar text and absent
// content. Replaying their final dump keeps these printer paths oracle-checked.
func TestAnyScalarOracleGoldens(t *testing.T) {
	set := corpusSet(t, []string{"../conformance/corpus/any-core/schemas"}, []string{"ac"})
	var sn *schema.Node
	for _, top := range set.Implemented("ac").Top {
		if top.Name == "xml" {
			sn = top
		}
	}
	if sn == nil {
		t.Fatal("missing anyxml schema")
	}
	for _, tc := range []struct {
		id   string
		text *string
	}{
		{"merge-equal-hints", strPtr("55")},
		{"dup-text-hints", strPtr(`<item xmlns="urn:tests:opaque">value</item>`)},
		{"dup-empty-hints", nil},
	} {
		t.Run(tc.id, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("../conformance/corpus/any-core/golden", tc.id+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var golden struct {
				Steps []struct{ Tree *struct{ JSON, XML string } }
			}
			if err := json.Unmarshal(raw, &golden); err != nil {
				t.Fatal(err)
			}
			want := golden.Steps[len(golden.Steps)-1].Tree
			if want == nil {
				t.Fatal("missing oracle dump")
			}
			tr := newTree(set)
			tr.insert(nil, &Node{schema: sn, any: &anyValue{text: tc.text, hints: types.HintString}}, insertDefault)
			var j, xml bytes.Buffer
			if err := tr.PrintJSON(&j, PrintOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := tr.PrintXML(&xml, PrintOptions{}); err != nil {
				t.Fatal(err)
			}
			if j.String() != want.JSON || xml.String() != want.XML {
				t.Errorf("JSON: %q, want %q; XML: %q, want %q", j.String(), want.JSON, xml.String(), want.XML)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

func TestAnyXPathCastErrorsAndBudget(t *testing.T) {
	tr, a, x := anyFixture()
	text := "plain text"
	mixed := &Node{schema: x, any: &anyValue{text: &text}}
	tr.insert(nil, mixed, insertDefault)
	tr.insert(mixed, newOpaque(opaque{Name: "child", Format: types.FormatXML, ModuleNS: "urn:anycore"}), insertLast)
	if s, ok, err := mixed.AnyValueString(FormatXML); err != nil || !ok || !strings.Contains(s, "<child") {
		t.Fatalf("child-first AnyValueString = %q, %t, %v", s, ok, err)
	}
	if got, err := evalAnyString(context.Background(), t, mixed, 100); err != nil || got.Str != "plain text\n" {
		t.Fatalf("text-first XPath cast = %q, %v", got.Str, err)
	}

	deep := &Node{schema: a, any: &anyValue{}}
	tr.insert(nil, deep, insertDefault)
	root := newOpaque(opaque{Name: "r", Format: types.FormatXML, ModuleNS: "urn:anycore"})
	tr.insert(deep, root, insertLast)
	p := root
	for range maxPrintDepth {
		child := newOpaque(opaque{Name: "r", Format: types.FormatXML, ModuleNS: "urn:anycore"})
		p.kids.opq = append(p.kids.opq, child)
		child.parent = p
		p = child
	}
	if _, err := evalAnyString(context.Background(), t, deep, 100000); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("depth cast error = %v", err)
	}
	badType := round6PluginType("xpath1.0")
	badSchema := &schema.Node{Name: "bad", Kind: schema.Leaf, Module: a.Module, Parent: a, Type: badType}
	badValue, diag := types.Store(badType, "/missing:node", types.FormatJSON, types.HintData, types.ModuleNames{Set: tr.set}, badSchema)
	if diag != nil {
		t.Fatal(diag)
	}
	bad := &Node{schema: a, any: &anyValue{}}
	badTree := newTree(tr.set)
	badTree.insert(nil, bad, insertDefault)
	badTree.insert(bad, newTerm(badSchema, badValue), insertDefault)
	_, _, printErr := bad.AnyValueString(FormatXML)
	var printed *ValidationError
	if !errors.As(printErr, &printed) {
		t.Fatalf("unprintable payload printer = %v", printErr)
	}
	if _, err := evalAnyString(context.Background(), t, bad, 100); err == nil {
		t.Fatal("unprintable payload cast succeeded")
	} else {
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.RC() != printed.RC() || !reflect.DeepEqual(ve.Diags, printed.Diags) {
			t.Fatalf("unprintable payload cast = %v", err)
		}
	}

	wide := &Node{schema: a, any: &anyValue{}}
	wideTree := newTree(tr.set)
	wideTree.insert(nil, wide, insertDefault)
	for range 200 {
		wideTree.insert(wide, newOpaque(opaque{Name: "item", Format: types.FormatXML, ModuleNS: "urn:anycore"}), insertLast)
	}
	if _, err := evalAnyString(context.Background(), t, wide, 30); !errors.Is(err, xpath.ErrBudget) {
		t.Fatalf("wide cast error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := evalAnyString(ctx, t, wide, 1000); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cast error = %v", err)
	}
}

func TestAnyPrintTextHintsAndEmpty(t *testing.T) {
	for _, tc := range []struct {
		name, value, json, xml string
		hints                  types.Hints
	}{
		{"string", `<x>&"`, `{"anycore:x":"<x>&\""}`, `<x xmlns="urn:anycore">&lt;x&gt;&amp;"</x>` + "\n", types.HintString},
		{"number", "55", `{"anycore:x":55}`, `<x xmlns="urn:anycore">55</x>` + "\n", types.HintDecNum},
		{"boolean", "true", `{"anycore:x":true}`, `<x xmlns="urn:anycore">true</x>` + "\n", types.HintBoolean},
		{"num64", "55", `{"anycore:x":"55"}`, `<x xmlns="urn:anycore">55</x>` + "\n", types.HintNum64},
		{"empty-text-with-empty-hint", "", `{"anycore:x":""}`, `<x xmlns="urn:anycore"/>` + "\n", types.HintEmpty},
		{"explicit-empty-string", "", `{"anycore:x":""}`, `<x xmlns="urn:anycore"/>` + "\n", types.HintString},
		{"raw-array", `[1,2]`, `{"anycore:x":[1,2]}`, `<x xmlns="urn:anycore">[1,2]</x>` + "\n", hintList},
		{"generic-hint", "1", `{"anycore:x":1}`, `<x xmlns="urn:anycore">1</x>` + "\n", types.HintData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, _, x := anyFixture()
			n := anyText(t, tr, x, tc.value, tc.hints)
			var j, xml bytes.Buffer
			if err := n.PrintJSON(&j, PrintOptions{Shrink: true}); err != nil {
				t.Fatal(err)
			}
			if err := n.PrintXML(&xml, PrintOptions{}); err != nil {
				t.Fatal(err)
			}
			if j.String() != tc.json || xml.String() != tc.xml {
				t.Fatalf("JSON %q want %q; XML %q want %q", j.String(), tc.json, xml.String(), tc.xml)
			}
			got, present, err := n.AnyValueString(FormatJSON)
			if err != nil || !present || got != tc.value {
				t.Fatalf("AnyValueString = %q, %t, %v", got, present, err)
			}
		})
	}
	for _, kind := range []schema.Kind{schema.AnyData, schema.AnyXML} {
		tr, a, x := anyFixture()
		sn := a
		want := `{"anycore:a":{}}`
		if kind == schema.AnyXML {
			sn, want = x, `{"anycore:x":{}}`
		}
		n := &Node{schema: sn, any: &anyValue{}}
		tr.insert(nil, n, insertDefault)
		var b bytes.Buffer
		if err := tr.PrintJSON(&b, PrintOptions{Shrink: true}); err != nil || b.String() != want {
			t.Fatalf("empty %s: %q, %v", sn.Name, b.String(), err)
		}
		if v, ok, err := n.AnyValueString(FormatXML); err != nil || ok || v != "" {
			t.Fatalf("absent = %q, %t, %v", v, ok, err)
		}
	}
	tr, _, x := anyFixture()
	n := &Node{schema: x, any: &anyValue{hints: types.HintEmpty}}
	tr.insert(nil, n, insertDefault)
	var b bytes.Buffer
	if err := tr.PrintJSON(&b, PrintOptions{Shrink: true}); err != nil || b.String() != `{"anycore:x":null}` {
		t.Fatalf("empty hint = %q, %v", b.String(), err)
	}
	tr, a, _ := anyFixture()
	value := "text"
	bad := &Node{schema: a, any: &anyValue{text: &value}}
	tr.insert(nil, bad, insertDefault)
	for _, print := range []func(io.Writer, PrintOptions) error{tr.PrintJSON, tr.PrintXML} {
		var out bytes.Buffer
		var ve *ValidationError
		if err := print(&out, PrintOptions{}); !errors.As(err, &ve) || ve.RC() != "LY_EINT" || out.Len() != 0 {
			t.Fatalf("anydata text print = %v, %q", err, out.String())
		}
	}
	if got, ok, err := bad.AnyValueString(FormatJSON); err != nil || !ok || got != value {
		t.Fatalf("stored text = %q, %t, %v", got, ok, err)
	}
}

func TestAnyPrintPayloadSiblingsAndMetadata(t *testing.T) {
	tr, a, x := anyFixture()
	a.Module.Prefix = "ac"
	n := &Node{schema: a, any: &anyValue{}}
	tr.insert(nil, n, insertDefault)
	for _, name := range []string{"one", "two"} {
		tr.insert(n, newOpaque(opaque{Name: name, ModuleNS: "urn:anycore", Format: types.FormatXML}), insertLast)
	}
	v, diag := types.Store(&schema.Type{Base: schema.String}, "label", types.FormatJSON, types.HintString, nil, nil)
	if diag != nil {
		t.Fatal(diag)
	}
	n.meta = []*meta{{mod: a.Module, name: "note", value: v}}
	other := &Node{schema: x, any: &anyValue{}}
	tr.insert(nil, other, insertDefault)
	var j, xml bytes.Buffer
	if err := tr.PrintJSON(&j, PrintOptions{Shrink: true}); err != nil {
		t.Fatal(err)
	}
	if err := tr.PrintXML(&xml, PrintOptions{Shrink: true}); err != nil {
		t.Fatal(err)
	}
	if got, want := j.String(), `{"anycore:a":{"one":"","two":""},"@anycore:a":{"anycore:note":"label"},"anycore:x":{}}`; got != want {
		t.Fatalf("JSON = %q want %q", got, want)
	}
	if got, want := xml.String(), `<a xmlns="urn:anycore" xmlns:ac="urn:anycore" ac:note="label"><one/><two/></a><x xmlns="urn:anycore"/>`; got != want {
		t.Fatalf("XML = %q want %q", got, want)
	}
	j.Reset()
	if err := n.PrintJSON(&j, PrintOptions{Shrink: true}); err != nil || strings.Contains(j.String(), `"anycore:x"`) {
		t.Fatalf("node-only JSON = %q, %v", j.String(), err)
	}
	for _, tc := range []struct {
		format Format
		want   string
	}{
		{FormatJSON, "{\n  \"anycore:one\": \"\",\n  \"anycore:two\": \"\"\n}\n"},
		{FormatXML, `<one xmlns="urn:anycore"/>` + "\n" + `<two xmlns="urn:anycore"/>` + "\n"},
	} {
		got, ok, err := n.AnyValueString(tc.format)
		if err != nil || !ok || got != tc.want {
			t.Fatalf("AnyValueString(%v) = %q, %t, %v, want %q", tc.format, got, ok, err, tc.want)
		}
	}
}

func TestAnyPrintDepthLimitAndArguments(t *testing.T) {
	tr, a, _ := anyFixture()
	n := &Node{schema: a, any: &anyValue{}}
	tr.insert(nil, n, insertDefault)
	root := newOpaque(opaque{Name: "r", Format: types.FormatJSON})
	tr.insert(n, root, insertLast)
	p := root
	for range maxPrintDepth {
		child := newOpaque(opaque{Name: "r", Format: types.FormatJSON})
		p.kids.opq = append(p.kids.opq, child)
		child.parent = p
		p = child
	}
	for _, print := range []func(io.Writer, PrintOptions) error{tr.PrintJSON, tr.PrintXML} {
		var b bytes.Buffer
		if err := print(&b, PrintOptions{Shrink: true}); !errors.Is(err, yang.ErrBudget) || b.Len() != 0 {
			t.Fatalf("depth = %v, %q", err, b.String())
		}
	}
	if _, _, err := root.AnyValueString(FormatJSON); err == nil {
		t.Fatal("non-any receiver accepted")
	} else {
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" {
			t.Fatalf("non-any = %v", err)
		}
	}
}

func TestAnyPrintWithDefaults(t *testing.T) {
	tr, a, _ := anyFixture()
	sn := &schema.Node{Name: "leaf", Kind: schema.Leaf, Module: a.Module,
		Type: &schema.Type{Base: schema.String}, Default: []schema.DefaultValue{{Lex: "default"}}}
	a.Module.Top = append(a.Module.Top, sn)
	n := &Node{schema: a, any: &anyValue{}}
	tr.insert(nil, n, insertDefault)
	v, d := types.Store(sn.Type, "default", types.FormatJSON, types.HintString, nil, sn)
	if d != nil {
		t.Fatal(d)
	}
	child := newTerm(sn, v)
	child.flags = FlagDefault
	tr.insert(n, child, insertDefault)
	if got := (xn{n, tr.set}).Children(); len(got) != 1 || got[0].Name() != "leaf" {
		t.Fatalf("XPath payload children = %v", got)
	}
	if got := (xn{n, tr.set}).Value().String(); !strings.Contains(got, `<a xmlns="urn:anycore">`) || !strings.Contains(got, `<leaf>default</leaf>`) {
		t.Fatalf("XPath any string = %q", got)
	}
	for _, tc := range []struct {
		wd        WD
		json, xml string
	}{
		{WDTrim, `{"anycore:a":{}}`, `<a xmlns="urn:anycore"></a>`},
		{WDAll, `{"anycore:a":{"leaf":"default"}}`, `<a xmlns="urn:anycore"><leaf>default</leaf></a>`},
	} {
		var j, xml bytes.Buffer
		o := PrintOptions{Shrink: true, WithDefaults: tc.wd}
		if err := tr.PrintJSON(&j, o); err != nil {
			t.Fatal(err)
		}
		if err := tr.PrintXML(&xml, o); err != nil {
			t.Fatal(err)
		}
		if j.String() != tc.json || xml.String() != tc.xml {
			t.Fatalf("mode %d: JSON %q, XML %q", tc.wd, j.String(), xml.String())
		}
	}
}
