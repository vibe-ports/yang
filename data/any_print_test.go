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
	"github.com/vibe-ports/yang/internal/contextopts"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/snap"
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
			mustLink(t, tr.insert(nil, n, insertDefault))
			mustLink(t, tr.insert(n, newOpaque(opaque{Name: "target", Value: "value", ModuleNS: x.Module.Namespace, Format: types.FormatXML, set: set}), insertLast))
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
	mustLink(t, tr.insert(nil, before, insertDefault))
	mustLink(t, tr.insert(before, newOpaque(opaque{Name: "before", Format: types.FormatXML, ModuleNS: m.Namespace}), insertLast))
	parent := newInner(outer)
	mustLink(t, tr.insert(nil, parent, insertDefault))
	n := &Node{schema: nested, any: &anyValue{}}
	mustLink(t, tr.insert(parent, n, insertDefault))
	mustLink(t, tr.insert(n, newOpaque(opaque{Name: "inside", Format: types.FormatXML, ModuleNS: m.Namespace}), insertLast))
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
	mustLink(t, tr.insert(nil, n, insertDefault))
	value := strings.Repeat("&", 16<<20)
	for i := 0; i < 200; i++ {
		mustLink(t, tr.insert(n, newOpaque(opaque{Name: "v", ModuleNS: x.Module.Namespace, Value: value, Format: types.FormatXML}), insertLast))
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
	mustLink(t, tr.insert(nil, root, insertDefault))
	parent := root
	for sn := top; len(sn.Children) > 0 && sn.Children[0].Kind == schema.Container; sn = sn.Children[0] {
		c := newInner(sn.Children[0])
		mustLink(t, tr.insert(parent, c, insertDefault))
		parent = c
	}
	text := strings.Repeat("a\n", lines) // 32 KiB of source, 128 MiB indented
	mustLink(t, tr.insert(parent, &Node{schema: x, any: &anyValue{text: &text}}, insertDefault))

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
			mustLink(t, tr.insert(nil, n, insertDefault))
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
	mustLink(t, tr.insert(nil, anyNode, insertDefault))
	parent := newOpaque(opaque{Name: "parent", ModuleNS: p.Namespace, Format: types.FormatXML, set: source})
	mustLink(t, tr.insert(anyNode, parent, insertLast))
	value, diag := types.Store(v.Type, "x", types.FormatJSON, types.HintString, nil, v)
	if diag != nil {
		t.Fatal(diag)
	}
	mustLink(t, tr.insert(parent, newTerm(v, value), insertDefault))
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
			mustLink(t, tr.insert(nil, &Node{schema: sn, any: &anyValue{text: tc.text, hints: types.HintString}}, insertDefault))
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
	mustLink(t, tr.insert(nil, mixed, insertDefault))
	mustLink(t, tr.insert(mixed, newOpaque(opaque{Name: "child", Format: types.FormatXML, ModuleNS: "urn:anycore"}), insertLast))
	if s, ok, err := mixed.AnyValueString(FormatXML); err != nil || !ok || !strings.Contains(s, "<child") {
		t.Fatalf("child-first AnyValueString = %q, %t, %v", s, ok, err)
	}
	if got, err := evalAnyString(context.Background(), t, mixed, 100); err != nil || got.Str != "plain text\n" {
		t.Fatalf("text-first XPath cast = %q, %v", got.Str, err)
	}

	deep := &Node{schema: a, any: &anyValue{}}
	mustLink(t, tr.insert(nil, deep, insertDefault))
	root := newOpaque(opaque{Name: "r", Format: types.FormatXML, ModuleNS: "urn:anycore"})
	mustLink(t, tr.insert(deep, root, insertLast))
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
	mustLink(t, badTree.insert(nil, bad, insertDefault))
	mustLink(t, badTree.insert(bad, newTerm(badSchema, badValue), insertDefault))
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
	if _, diags, err := badTree.EvalXPath("string(/anycore:a)", XPathOptions{}); err == nil {
		t.Fatal("public unprintable payload cast succeeded")
	} else {
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.RC() != printed.RC() || !reflect.DeepEqual(diags, printed.Diags) ||
			!reflect.DeepEqual(ve.Diags, printed.Diags) {
			t.Fatalf("public unprintable payload cast = diagnostics %+v, error %v", diags, err)
		}
	}

	// xml_print_meta is void: its xpath1.0 printer logs the unresolved prefix,
	// emits an empty attribute and the enclosing XPath cast still succeeds.
	goodSchema := &schema.Node{Name: "good", Kind: schema.Leaf, Module: a.Module, Parent: a,
		Type: &schema.Type{Base: schema.String}}
	goodValue, diag := types.Store(goodSchema.Type, "value", types.FormatJSON, types.HintString, nil, goodSchema)
	if diag != nil {
		t.Fatal(diag)
	}
	soft := &Node{schema: x, any: &anyValue{}}
	softTree := newTree(tr.set)
	mustLink(t, softTree.insert(nil, soft, insertDefault))
	good := newTerm(goodSchema, goodValue)
	good.meta = []*meta{{mod: a.Module, name: "select", value: badValue}}
	mustLink(t, softTree.insert(soft, good, insertDefault))
	result, diags, err := softTree.EvalXPath("string(/anycore:x)", XPathOptions{})
	if err != nil || result.Type != XPathString || !strings.Contains(result.String, `select=""`) ||
		len(diags) != 1 || !strings.Contains(diags[0].Msg, `prefix "missing"`) {
		t.Fatalf("metadata cast = %q, diagnostics %+v, error %v", result.String, diags, err)
	}

	wide := &Node{schema: a, any: &anyValue{}}
	wideTree := newTree(tr.set)
	mustLink(t, wideTree.insert(nil, wide, insertDefault))
	for range 200 {
		mustLink(t, wideTree.insert(wide, newOpaque(opaque{Name: "item", Format: types.FormatXML, ModuleNS: "urn:anycore"}), insertLast))
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

const anyDiagnosticModule = `module ac {
  yang-version 1.1;
  namespace "urn:any-diagnostic";
  prefix ac;
  import ietf-yang-metadata { prefix md; }
  import ietf-yang-types { prefix yang; }
  md:annotation select { type yang:xpath1.0; }
  leaf s { type string; }
  anydata data { must "string(.)"; }
}`

func anyDiagnosticContext(t *testing.T) *schema.Set {
	t.Helper()
	return xpTestCtx(t, map[string]string{
		"ac": anyDiagnosticModule,
		"ietf-yang-metadata": `module ietf-yang-metadata {
  yang-version 1.1; namespace "urn:ietf:params:xml:ns:yang:ietf-yang-metadata"; prefix md;
  extension annotation { argument name; }
}`,
		"ietf-yang-types": `module ietf-yang-types {
  yang-version 1.1; namespace "urn:ietf:params:xml:ns:yang:ietf-yang-types"; prefix yang;
  typedef xpath1.0 { type string; }
}`,
	})
}

const anyDiagnosticJSON = `{"ac:data":{"ac:s":"value","@ac:s":{"ac:select":"/missing:node"}}}`

// xml_print_meta is void, so its retained diagnostic does not change either lyd_parse_data's
// or lyd_validate_all's successful return status and does not make lyd_parse_data free the tree.
func TestAnyMustPrinterDiagnosticDoesNotFailValidation(t *testing.T) {
	set := anyDiagnosticContext(t)
	for _, tc := range []struct {
		name      string
		parseOnly bool
	}{
		{name: "parse"},
		{name: "validate", parseOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, diags, err := Parse(context.Background(), strings.NewReader(anyDiagnosticJSON), FormatJSON,
				snap.New(set), ParseOptions{ParseOnly: tc.parseOnly})
			if err != nil || tr == nil {
				t.Fatalf("Parse tree=%t diagnostics=%+v error=%v", tr != nil, diags, err)
			}
			if tc.parseOnly {
				diags, err = tr.Validate(context.Background(), ValidateOptions{})
			}
			if err != nil || len(diags) != 1 || !strings.Contains(diags[0].Msg, `prefix "missing"`) {
				t.Fatalf("diagnostics=%+v error=%v", diags, err)
			}
		})
	}
}

// xml_print_meta logs through LYD_CTX(node). A duplicated anydata payload keeps its source
// context, so its successful cast must leave the destination operation log empty.
func anyCrossContextDiagnosticTree(t *testing.T) (*Tree, *schema.Set) {
	t.Helper()
	source, target := anyDiagnosticContext(t), anyDiagnosticContext(t)
	src, diags, err := Parse(context.Background(), strings.NewReader(anyDiagnosticJSON), FormatJSON,
		snap.New(source), ParseOptions{ParseOnly: true})
	if err != nil || len(diags) != 0 {
		t.Fatalf("source Parse diagnostics=%+v error=%v", diags, err)
	}
	dst := newTree(target)
	if _, diags, err = dst.Dup(src.top.first(), nil, DupOptions{}); err != nil || len(diags) != 0 {
		t.Fatalf("Dup diagnostics=%+v error=%v", diags, err)
	}
	return dst, source
}

func TestAnyCrossContextCastDoesNotRetainPrinterDiagnosticsByDefault(t *testing.T) {
	dst, _ := anyCrossContextDiagnosticTree(t)
	for range 100 {
		result, diags, err := dst.EvalXPath("string(/ac:data)", XPathOptions{})
		if err != nil || len(diags) != 0 || !strings.Contains(result.String, `select=""`) {
			t.Fatalf("result=%q diagnostics=%+v error=%v", result.String, diags, err)
		}
	}
	dst.contextLogMu.Lock()
	if len(dst.contextLogs) != 0 {
		t.Fatalf("default retained context diagnostics=%+v", dst.contextLogs)
	}
	dst.contextLogMu.Unlock()
}

func TestAnyCrossContextCastKeepsPrinterDiagnosticOwner(t *testing.T) {
	dst, source := anyCrossContextDiagnosticTree(t)
	contextopts.EnableContextDiagnostics(dst)
	result, diags, err := dst.EvalXPath("string(/ac:data)", XPathOptions{})
	if err != nil || len(diags) != 0 || !strings.Contains(result.String, `select=""`) {
		t.Fatalf("result=%q diagnostics=%+v error=%v", result.String, diags, err)
	}
	dst.contextLogMu.Lock()
	defer dst.contextLogMu.Unlock()
	if len(dst.contextLogs) != 1 || dst.contextLogs[0].set != source ||
		!strings.Contains(dst.contextLogs[0].diag.Msg, `prefix "missing"`) {
		t.Fatalf("retained context diagnostics=%+v", dst.contextLogs)
	}
}

func anyWDContext(withDefaults bool) (*schema.Set, *schema.Node, *schema.Node) {
	set := &schema.Set{}
	m := &schema.Module{Name: "payload", Namespace: "urn:payload", Prefix: "p", Implemented: true, Owner: set}
	a := &schema.Node{Name: "any", Kind: schema.AnyData, Module: m}
	d := &schema.Node{Name: "d", Kind: schema.Leaf, Module: m, Type: &schema.Type{Base: schema.String},
		Default: []schema.DefaultValue{{Lex: "x"}}}
	m.Top = []*schema.Node{a, d}
	set.Modules = []*schema.Module{m}
	if withDefaults {
		set.Modules = append(set.Modules,
			&schema.Module{Name: wdModule, Namespace: "urn:ietf:params:xml:ns:netconf:with-defaults:1.0",
				Prefix: "wd", Implemented: true, Owner: set},
			&schema.Module{Name: "default", Namespace: "urn:ietf:params:xml:ns:netconf:default:1.0",
				Prefix: "dflt", Implemented: true, Owner: set})
	}
	return set, a, d
}

// Copied any payloads retain their source context. Both native printers ask LYD_CTX(node),
// not the outer printer context, whether ietf-netconf-with-defaults is implemented.
func TestAnyCopiedPayloadWithDefaultsUsesRetainedContext(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		sourceWD, destinationWD bool
		wantTag                 bool
	}{
		{"payload-has-module", true, false, true},
		{"outer-has-module", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, anySchema, defaultSchema := anyWDContext(tc.sourceWD)
			target, _, _ := anyWDContext(tc.destinationWD)
			src := newTree(source)
			anyNode := &Node{schema: anySchema, any: &anyValue{}}
			mustLink(t, src.insert(nil, anyNode, insertDefault))
			v, diag := types.Store(defaultSchema.Type, "x", types.FormatJSON, types.HintString, nil, defaultSchema)
			if diag != nil {
				t.Fatal(diag)
			}
			child := newTerm(defaultSchema, v)
			child.flags = FlagDefault
			mustLink(t, src.insert(anyNode, child, insertDefault))

			dst := newTree(target)
			copied, _, err := dst.Dup(anyNode, nil, DupOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if ctxOf(copied.any.children.first()) != source {
				t.Fatal("copied payload did not retain its source context")
			}
			for _, wd := range []WD{WDAllTagged, WDImplicitTagged} {
				var jsonOut, xmlOut bytes.Buffer
				if err := dst.PrintJSON(&jsonOut, PrintOptions{Shrink: true, WithDefaults: wd}); err != nil {
					t.Fatal(err)
				}
				if err := dst.PrintXML(&xmlOut, PrintOptions{Shrink: true, WithDefaults: wd}); err != nil {
					t.Fatal(err)
				}
				jsonTagged := strings.Contains(jsonOut.String(), `ietf-netconf-with-defaults:default`)
				xmlTagged := strings.Contains(xmlOut.String(), `:default="true"`)
				if jsonTagged != tc.wantTag || xmlTagged != tc.wantTag {
					t.Fatalf("mode %d: JSON %q, XML %q, want tag %t", wd, jsonOut.String(), xmlOut.String(), tc.wantTag)
				}
			}
		})
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
		mustLink(t, tr.insert(nil, n, insertDefault))
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
	mustLink(t, tr.insert(nil, n, insertDefault))
	var b bytes.Buffer
	if err := tr.PrintJSON(&b, PrintOptions{Shrink: true}); err != nil || b.String() != `{"anycore:x":null}` {
		t.Fatalf("empty hint = %q, %v", b.String(), err)
	}
	tr, a, _ := anyFixture()
	value := "text"
	bad := &Node{schema: a, any: &anyValue{text: &value}}
	mustLink(t, tr.insert(nil, bad, insertDefault))
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
	mustLink(t, tr.insert(nil, n, insertDefault))
	for _, name := range []string{"one", "two"} {
		mustLink(t, tr.insert(n, newOpaque(opaque{Name: name, ModuleNS: "urn:anycore", Format: types.FormatXML}), insertLast))
	}
	v, diag := types.Store(&schema.Type{Base: schema.String}, "label", types.FormatJSON, types.HintString, nil, nil)
	if diag != nil {
		t.Fatal(diag)
	}
	n.meta = []*meta{{mod: a.Module, name: "note", value: v}}
	other := &Node{schema: x, any: &anyValue{}}
	mustLink(t, tr.insert(nil, other, insertDefault))
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
	mustLink(t, tr.insert(nil, n, insertDefault))
	root := newOpaque(opaque{Name: "r", Format: types.FormatJSON})
	mustLink(t, tr.insert(n, root, insertLast))
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
	mustLink(t, tr.insert(nil, n, insertDefault))
	v, d := types.Store(sn.Type, "default", types.FormatJSON, types.HintString, nil, sn)
	if d != nil {
		t.Fatal(d)
	}
	child := newTerm(sn, v)
	child.flags = FlagDefault
	mustLink(t, tr.insert(n, child, insertDefault))
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
