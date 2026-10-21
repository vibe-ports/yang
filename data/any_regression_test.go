// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

type anyGoldenStep struct {
	RC          struct{ Name string }
	Diagnostics []struct {
		Code       struct{ Name string }
		Msg        string
		VecodeName string `json:"vecode_name"`
		DataPath   string `json:"data_path"`
		SchemaPath string `json:"schema_path"`
		AppTag     string `json:"apptag"`
		Line       int
		Level      string
	}
	Typed []struct {
		Flags map[string]bool
		Any   *struct{ Text string }
	}
}

func anyGoldenSteps(t *testing.T, name string) []anyGoldenStep {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "conformance", "corpus", "any-core", "golden", name+".json")) //nolint:gosec // corpus path
	if err != nil {
		t.Fatal(err)
	}
	var golden struct{ Steps []anyGoldenStep }
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	return golden.Steps
}

// lyd_any_copy_value rejects an any node with both a child forest and text,
// even when the text would otherwise be reparsed during duplication.
func TestAnyCopyRejectsMixedInput(t *testing.T) {
	tr, _, x := anyFixture()
	yangMod := utDiffSet(t).Implemented("yang")
	if yangMod == nil {
		t.Fatal("diff schema lacks yang annotations")
	}
	tr.set.Modules = append(tr.set.Modules, yangMod)
	text := "unparsed text"
	src := &Node{schema: x, any: &anyValue{text: &text}}
	tr.insert(nil, src, insertDefault)
	child := newOpaque(opaque{Name: "child", Format: types.FormatJSON, set: tr.set})
	tr.insert(src, child, insertLast)
	if src.any.children.len() != 1 || src.any.text == nil {
		t.Fatal("fixture must have both any inputs")
	}
	check := func(name string, err error) {
		t.Helper()
		var op *opError
		var validation *ValidationError
		if errors.As(err, &validation) && validation.RC() == "LY_EINVAL" {
			return
		}
		if !errors.As(err, &op) || op.Err != "LY_EINVAL" {
			t.Fatalf("%s: got %v, want LY_EINVAL", name, err)
		}
	}
	_, _, err := newTree(tr.set).Dup(src, nil, DupOptions{})
	check("dup", err)
	check("merge new", newTree(tr.set).Merge(tr))
	mergeDst := newTree(tr.set)
	old := "old text"
	mergeDst.insert(nil, &Node{schema: x, any: &anyValue{text: &old}}, insertDefault)
	check("merge matched", mergeDst.Merge(tr))
	if err := tr.diffChangeOp(src, diffCreate); err != nil {
		t.Fatal(err)
	}
	check("diff apply", newTree(tr.set).ApplyDiff(tr, ApplyDiffOptions{}))
	dst := &Node{schema: x, any: &anyValue{}}
	check("copy", dst.copyAnyValue(newAnyWork(context.Background(), Budget{}), child, &text, 0))
	if dst.any.children.len() != 0 || dst.any.text != nil {
		t.Fatal("rejected copy changed destination")
	}
	for _, useValue := range []bool{false, true} {
		_, _, err = newAnyWork(context.Background(), Budget{}).createAny(newTree(tr.set), x, child, &text, 0, useValue, true)
		check("create", err)
	}
}

// lyd_dup_r resolves the target schema before lyd_any_copy_value rejects mixed input.
func TestAnyMixedDupMissingModulePrecedesCopyError(t *testing.T) {
	srcTree, _, x := anyFixture()
	text := "plain text"
	src := &Node{schema: x, any: &anyValue{text: &text}}
	srcTree.insert(nil, src, insertDefault)
	srcTree.insert(src, newOpaque(opaque{Name: "child", Format: types.FormatJSON, set: srcTree.set}), insertLast)
	if src.anyType() != anyValueMixed {
		t.Fatal("source must carry both child and text")
	}

	_, err := dupTo(src, &schema.Set{}, nil, 0, false)
	var op *opError
	if !errors.As(err, &op) || op.Err != "LY_ENOTFOUND" ||
		op.Msg != `Module "anycore" not present/implemented in the target context.` {
		t.Fatalf("cross-context dup: got %v, want LY_ENOTFOUND and missing-module diagnostic", err)
	}
	_, err = dupTo(src, srcTree.set, nil, 0, false)
	if !errors.As(err, &op) || op.Err != "LY_EINVAL" {
		t.Fatalf("same-context dup: got %v, want LY_EINVAL", err)
	}
}

// lyd_compare_single_data checks the first operand's child and text separately.
// Merge compares source first, then tries lyd_any_copy_value on a mismatch.
func TestAnyMixedComparedAndMergedWithTree(t *testing.T) {
	tr, _, x := anyFixture()
	text := "plain text"
	mixed := &Node{schema: x, any: &anyValue{text: &text}}
	tr.insert(nil, mixed, insertDefault)
	child := newOpaque(opaque{Name: "child", Value: "value", Format: types.FormatJSON, set: tr.set})
	tr.insert(mixed, child, insertLast)
	treeOnly := &Node{schema: x, any: &anyValue{}}
	other := newTree(tr.set)
	other.insert(nil, treeOnly, insertDefault)
	otherChild := newOpaque(opaque{Name: "child", Value: "value", Format: types.FormatJSON, set: tr.set})
	other.insert(treeOnly, otherChild, insertLast)
	if mixed.anyType() != anyValueMixed || treeOnly.anyType() != anyValueDataTree {
		t.Fatal("mixed and tree-only payloads must have distinct kinds")
	}
	if mixed.Equal(treeOnly, CompareOptions{}) {
		t.Fatal("mixed first must differ from tree-only")
	}
	if !treeOnly.Equal(mixed, CompareOptions{}) {
		t.Fatal("tree-only first must compare its children")
	}
	var op *opError
	var validation *ValidationError
	err := other.Merge(tr)
	if !errors.As(err, &op) && !errors.As(err, &validation) ||
		op != nil && op.Err != "LY_EINVAL" || validation != nil && validation.RC() != "LY_EINVAL" {
		t.Fatalf("merge mixed source into tree-only target = %v, want LY_EINVAL", err)
	}
	if treeOnly.any.text != nil || treeOnly.any.children.first() != otherChild {
		t.Fatal("rejected merge changed tree-only target")
	}
}

// This pointer-level fixture cannot yet be replayed by the engine (U-0112),
// so compare the internal payload with the oracle's flags after reparsing text.

func TestAnyReparseFlagsGolden(t *testing.T) {
	g := anyGoldenSteps(t, "dup-xml-text-reparse")
	if len(g) != 3 || len(g[0].Typed) != 1 || g[0].Typed[0].Any == nil || len(g[1].Typed) != 2 {
		t.Fatal("unexpected reparse golden shape")
	}
	for _, kind := range []string{"anyxml", "anydata"} {
		for _, action := range []string{"dup", "copy", "merge"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				tr, a, x := anyFixture()
				sn := x
				if kind == "anydata" {
					sn = a
				}
				text := g[0].Typed[0].Any.Text
				n := &Node{schema: sn, any: &anyValue{text: &text}}
				tr.insert(nil, n, insertDefault)
				dst := newTree(tr.set)
				var d *Node
				var err error
				switch action {
				case "dup":
					d, _, err = dst.Dup(n, nil, DupOptions{})
				case "copy":
					d = &Node{schema: sn, any: &anyValue{}, payloadSet: tr.set}
					err = d.copyAnyValue(newAnyWork(context.Background(), Budget{}), nil, &text, 0)
				case "merge":
					d = &Node{schema: sn, any: &anyValue{}}
					dst.insert(nil, d, insertDefault)
					err = dst.Merge(tr)
				}
				if err != nil {
					t.Fatal(err)
				}
				if d.any.text != nil || d.any.children.len() != 1 {
					t.Fatal("text was not reparsed")
				}
				payload := d.any.children.first()
				for name, mask := range map[string]Flags{"new": FlagNew, "default": FlagDefault, "when_true": FlagWhenTrue} {
					if got, want := payload.flags&mask != 0, g[1].Typed[1].Flags[name]; got != want {
						t.Errorf("payload %s = %t, oracle = %t", name, got, want)
					}
				}
				if payload.parent != d || payload.tree != nil || n.any.text == nil || *n.any.text != text {
					t.Fatal("reparse ownership or source text changed")
				}
			})
		}
	}
}

func TestAnyCreateInvalidGolden(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"text", "not a tree"}, {"json", `{"item":`}, {"xml", "<item>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := anyGoldenSteps(t, "create-invalid-"+tc.name)
			if len(steps) != 1 || len(steps[0].Diagnostics) == 0 {
				t.Fatal("missing constructor diagnostic in golden")
			}
			g := steps[0]
			for _, useValue := range []bool{false, true} {
				tr, a, _ := anyFixture()
				n, diags, err := newAnyWork(context.Background(), Budget{}).createAny(tr, a, nil, &tc.value, 0, useValue, true)
				var ve *ValidationError
				if n != nil || !errors.As(err, &ve) {
					t.Fatalf("constructor = %v, %v; want ValidationError and no node", n, err)
				}
				if ve.RC() != g.RC.Name || len(ve.Diags) != len(g.Diagnostics) {
					t.Fatalf("got %s %+v; oracle %s %+v", ve.RC(), ve.Diags, g.RC.Name, g.Diagnostics)
				}
				if !slices.Equal(diags, ve.Diags) {
					t.Fatalf("returned diagnostics %+v differ from error %+v", diags, ve.Diags)
				}
				for i, want := range g.Diagnostics {
					got := ve.Diags[i]
					if got.Err != want.Code.Name || got.Code != want.VecodeName || got.Msg != want.Msg ||
						got.DataPath != want.DataPath || got.SchemaPath != want.SchemaPath || got.AppTag != want.AppTag ||
						got.Line != want.Line || got.Warning != (want.Level == "warning") {
						t.Errorf("diagnostic %d: got %+v; oracle %+v", i, got, want)
					}
				}
				if tr.top.len() != 0 {
					t.Fatal("failed construction changed the tree")
				}
			}
		})
	}
}

// any-core/create-warning-xml-key-order exercises this successful required parse.
// tree_data_new.c:260 keeps its warning; :282 and :344 suppress speculative and
// copy-time diagnostics, including warnings from successful parses.
func TestAnyParseDiagnostics(t *testing.T) {
	set := corpusSet(t, []string{"../conformance/corpus/ut-new/a.1"}, []string{"a"})
	var a, x *schema.Node
	for _, sn := range set.Implemented("a").Top {
		switch sn.Name {
		case "any":
			a = sn
		case "anyx":
			x = sn
		}
	}
	value := `<l1 xmlns="urn:tests:a"><b>second</b><a>first</a></l1>`
	warning := []yang.Diagnostic{{Warning: true, Err: "LY_SUCCESS", Code: "LYVE_SUCCESS",
		Msg: `Invalid position of the key "a" in a list.`}}
	for _, tc := range []struct {
		name     string
		sn       *schema.Node
		hints    types.Hints
		tryParse bool
	}{
		{"anydata", a, 0, true},
		{"anydata-hinted", a, types.HintString, true},
		{"anyxml-speculative", x, 0, true},
		{"anyxml-copy", x, 0, false},
	} {
		for _, useValue := range []bool{false, true} {
			ownership := "copy"
			if useValue {
				ownership = "transfer"
			}
			t.Run(tc.name+"/"+ownership, func(t *testing.T) {
				tr := newTree(set)
				n, diags, err := newAnyWork(context.Background(), Budget{}).createAny(tr, tc.sn, nil, &value, tc.hints, useValue, tc.tryParse)
				if err != nil || n == nil {
					t.Fatalf("constructor = %v, %+v, %v", n, diags, err)
				}
				var want []yang.Diagnostic
				if tc.sn == a {
					want = warning
				}
				if !slices.Equal(diags, want) {
					t.Fatalf("diagnostics = %+v, want %+v", diags, want)
				}
				if tc.tryParse || !useValue {
					if n.anyType() != anyValueDataTree || n.any.children.first().schema.Name != "l1" {
						t.Fatal("successful parse did not construct the list payload")
					}
				} else if n.any.text == nil || *n.any.text != value {
					t.Fatal("unparsed transferred text changed")
				}
			})
		}
	}
	for _, sn := range []*schema.Node{a, x} {
		for _, text := range []string{value, "<l1>"} {
			t.Run(sn.Name+"/dup/"+text, func(t *testing.T) {
				src := newTree(set)
				n := &Node{schema: sn, any: &anyValue{text: &text}}
				src.insert(nil, n, insertDefault)
				d, diags, err := newTree(set).Dup(n, nil, DupOptions{})
				if err != nil || len(diags) != 0 {
					t.Fatalf("copy parse diagnostics = %+v, error = %v", diags, err)
				}
				if text == value && d.anyType() != anyValueDataTree || text != value && d.anyType() != anyValueText {
					t.Fatal("copy did not parse the valid list or preserve invalid text")
				}
			})
		}
	}
	for _, useValue := range []bool{false, true} {
		text := "<l1>"
		n, diags, err := newAnyWork(context.Background(), Budget{}).createAny(newTree(set), x, nil, &text, 0, useValue, true)
		if err != nil || len(diags) != 0 || n.anyType() != anyValueText {
			t.Fatalf("anyxml fallback = %v, %+v, %v", n, diags, err)
		}
	}

	// CDATA reaches ly_store_prefix_data without the XML character decoder rejecting
	// the control byte first. Required anydata construction propagates LY_EINVAL;
	// speculative anyxml construction suppresses that failed parse and keeps the text.
	for _, useValue := range []bool{false, true} {
		text := "<n><![CDATA[\x01]]></n>"
		n, diags, err := newAnyWork(context.Background(), Budget{}).createAny(newTree(set), a, nil, &text, 0, useValue, true)
		var ve *ValidationError
		if n != nil || !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || !slices.Equal(diags, ve.Diags) {
			t.Fatalf("anydata prefix scan = %v, %+v, %v", n, diags, err)
		}
		n, diags, err = newAnyWork(context.Background(), Budget{}).createAny(newTree(set), x, nil, &text, 0, useValue, true)
		if err != nil || len(diags) != 0 || n.anyType() != anyValueText || n.any.text == nil || *n.any.text != text {
			t.Fatalf("anyxml prefix scan fallback = %v, %+v, %v", n, diags, err)
		}
	}
}

func TestAnyDetachedPayloadContexts(t *testing.T) {
	set := corpusSet(t, []string{"../conformance/corpus/any-core/schemas"}, []string{"ac"})
	for _, tc := range []struct{ name, value string }{
		{"schema", `{"ac:c":{"value":"kept","@value":{"ac:note":"source-only"}}}`},
		{"opaque", `<p:item xmlns:p="urn:tests:any-core" p:note="p:kept">p:kept</p:item>`},
	} {
		for _, action := range []string{"transfer", "copy", "copy-value"} {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				w := newAnyWork(context.Background(), Budget{})
				tr, _, err := w.createAnyDatatree(set, tc.value)
				if err != nil {
					t.Fatal(err)
				}
				child := tr.top.first()
				if err := child.Remove(); err != nil {
					t.Fatal(err)
				}
				if setOf(child) != nil || ctxOf(child) != set {
					t.Fatal("test requires an ordinary detached source root")
				}
				outer, a, _ := anyFixture()
				// A detached target also needs its schema's context in copyAnyValue.
				a.Module.Owner = outer.set
				var n *Node
				if action == "copy-value" {
					n = &Node{schema: a}
					err = n.copyAnyValue(w, child, nil, 0)
				} else {
					n, _, err = w.createAny(outer, a, child, nil, 0, action == "transfer", false)
				}
				if err != nil {
					t.Fatal(err)
				}
				outer.insert(nil, n, insertDefault)
				payload := n.any.children.first()
				if (payload == child) != (action == "transfer") || payload.parent != n ||
					action != "transfer" && child.parent != nil {
					t.Fatal("payload ownership")
				}
				// A later ordinary Dup must retain the captured source context too.
				d, _, err := newTree(outer.set).Dup(n, nil, DupOptions{NoMeta: true})
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range []*Node{payload, d.any.children.first()} {
					if ctxOf(p) != set {
						t.Fatal("detached payload inherited the enclosing context")
					}
					if p.schema != nil {
						leaf := p.kids.first()
						if p.schema != child.schema || len(leaf.meta) != 1 || leaf.meta[0].mod != set.Implemented("ac") ||
							leaf.meta[0].value.Canonical() != "source-only" {
							t.Fatal("source-only metadata lost or rebound")
						}
					} else if p.opaq.set != set || p.opaq.Prefixes.(types.XMLNamespaces).Set != set ||
						len(p.opaq.Attrs) != 1 || p.opaq.Attrs[0].Prefixes.(types.XMLNamespaces).Set != set ||
						p.opaq.Attrs[0].Value != "p:kept" {
						t.Fatal("opaque namespaces or attributes lost or rebound")
					}
				}
			})
		}
	}
}
