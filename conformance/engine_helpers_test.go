// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
	"github.com/vibe-ports/yang/internal/contextopts"
	"github.com/vibe-ports/yang/internal/snap"
)

func engineHelperSchema(t testing.TB) (*yang.Schema, Request) {
	t.Helper()
	r := Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: map[string]any{
		"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
		"context_options": []any{"no_yanglibrary"},
	}}
	s, _, _, verdict, err := buildSchemaContext(r)
	if err != nil || verdict != "valid" {
		t.Fatalf("build context = %s, %v", verdict, err)
	}
	return s, r
}

func TestEngineNodeHelpers(t *testing.T) {
	s, r := engineHelperSchema(t)
	p := map[string]any{"format": "json", "parse_only": true, "first": `{"pv2:c":{"mode":"on"}}`}
	tree, _, err := parseOne(r, s, p, execDiff, "first", "first", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		tree *data.Tree
		p    map[string]any
		want string
		fail bool
	}{
		{"nil omitted", nil, nil, "", false},
		{"empty omitted", data.NewTree(s), nil, "", false},
		{"populated omitted", tree, nil, "/pv2:c", false},
		{"explicit path", tree, map[string]any{"node": "/pv2:c/mode"}, "/pv2:c/mode", false},
		{"empty path", tree, map[string]any{"node": ""}, "", true},
		{"nil explicit path", nil, map[string]any{"node": "/pv2:c"}, "", true},
		{"missing path", tree, map[string]any{"node": "/pv2:c/missing"}, "", true},
		{"malformed path", tree, map[string]any{"node": "["}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := r.selectedNode(tc.tree, tc.p, "node", "test node")
			if tc.fail {
				want := "test node " + tc.p["node"].(string) + " not found (a request-error)"
				if n != nil || err == nil || err.Error() != want {
					t.Fatalf("node = %v, error = %v, want %q", n, err, want)
				}
				return
			}
			if err != nil || n == nil && tc.want != "" || n != nil && n.Path() != tc.want {
				t.Fatalf("node = %v, error = %v, want path %q", n, err, tc.want)
			}
			if tc.p == nil && n != topNode(tc.tree) {
				t.Fatal("omitted selector did not return the first top-level node")
			}
		})
	}
}

func TestEngineParseHelpers(t *testing.T) {
	s, r := engineHelperSchema(t)
	st := map[string]any{"format": "json", "data": `{"pv2:c":{"mode":"on"}}`}
	before := map[string]any{"format": "json", "data": st["data"]}
	tree, diags, err := parseDiff(r, s, st, execStepDiffParse)
	if err != nil || len(diags) != 0 || topNode(tree) == nil {
		t.Fatalf("parseDiff = %v, %v, %v", tree, diags, err)
	}
	if n, err := tree.Find("/pv2:c/d"); err != nil || n != nil {
		t.Fatalf("parse-only diff added a default: node = %v, error = %v", n, err)
	}
	if !reflect.DeepEqual(st, before) {
		t.Fatalf("parseDiff changed its input: %v", st)
	}
	failed, diags, err := stepParse(r, s, map[string]any{"format": "json", "parse_only": true,
		"data": `{"pv2:c":{"mode":"invalid"}}`}, execStepParse, tree)
	if err == nil || failed != tree || len(diags) == 0 || diags[0].Phase != "parse" {
		t.Fatalf("failed stepParse = %v, %v, %v; want retained tree and parse diagnostics", failed, diags, err)
	}
	if n, err := tree.Find("/pv2:c/mode"); err != nil || n == nil {
		t.Fatalf("failed parse lost the retained node: %v, %v", n, err)
	}
	replaced, diags, err := stepParse(r, s, map[string]any{"format": "json", "parse_only": true,
		"data": `{}`}, execStepParse, tree)
	if err != nil || len(diags) != 0 || replaced == tree || topNode(replaced) != nil || topNode(tree) != nil {
		t.Fatalf("successful stepParse = %v, %v, %v; want replacement and discarded old tree", replaced, diags, err)
	}
}

func TestEngineEmptyTreeRetainsForeignDiagnostics(t *testing.T) {
	base, p := diagnosticOwnershipSetup(t)
	r := Request{BaseDir: base, Params: p}
	source, _, _, verdict, err := buildSchemaContext(r)
	if err != nil || verdict != "valid" {
		t.Fatalf("source context = %s, %v", verdict, err)
	}
	target, _, _, verdict, err := buildSchemaContext(r)
	if err != nil || verdict != "valid" {
		t.Fatalf("target context = %s, %v", verdict, err)
	}
	src, _, err := parseOne(r, source, map[string]any{"format": "json", "parse_only": true,
		"data": diagnosticOwnershipData}, execStepParse, "data", "parse", nil)
	if err != nil {
		t.Fatal(err)
	}
	dst := emptyTree(target)
	if topNode(dst) != nil || snap.Set(contextopts.TreeSchema(dst)) != snap.Set(target) {
		t.Fatal("empty tree has nodes or the wrong schema")
	}
	if _, _, err := dst.Dup(topNode(src), nil, data.DupOptions{Recursive: true}); err != nil {
		t.Fatal(err)
	}
	if _, diags, err := dst.EvalXPath("string(/ac:data)", data.XPathOptions{}); err != nil || len(diags) != 0 {
		t.Fatalf("XPath = %v, %v; want the foreign printer log retained separately", diags, err)
	}
	logs := contextopts.DrainContextDiagnostics(dst)
	if len(logs) != 1 || snap.Set(logs[0].Context) != snap.Set(source) ||
		!strings.Contains(logs[0].Diagnostic.Msg, `prefix "missing"`) {
		t.Fatalf("foreign context diagnostics = %+v", logs)
	}
	if logs := contextopts.DrainContextDiagnostics(dst); len(logs) != 0 {
		t.Fatalf("diagnostics were not drained: %+v", logs)
	}
}

func BenchmarkEngineEmptyTree(b *testing.B) {
	s, _ := engineHelperSchema(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		tree := emptyTree(s)
		if topNode(tree) != nil {
			b.Fatalf("emptyTree has nodes: %v", tree)
		}
	}
}

func engineEmptyTreeRequests() map[string]map[string]any {
	requests := map[string]map[string]any{}
	for _, step := range []map[string]any{
		{"do": "edit", "set": map[string]any{"path": "/pv2:c/mode", "value": "on"}},
		{"do": "validate"},
		{"do": "insert", "format": "json", "parse_only": true, "data": `{"pv2:c":{"mode":"on"}}`},
		{"do": "diff_merge", "format": "json", "data": `{}`},
		{"do": "diff_apply"},
	} {
		requests[step["do"].(string)] = map[string]any{
			"op": "sequence", "searchdirs": []any{"schemas"},
			"modules": []any{map[string]any{"name": "pv2"}},
			"steps":   []any{step},
		}
	}
	return requests
}

func TestEngineEmptyTreeSequences(t *testing.T) {
	for name, p := range engineEmptyTreeRequests() {
		t.Run(name, func(t *testing.T) {
			got, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: p})
			if err != nil || got.Verdict() != "valid" {
				t.Fatalf("empty-tree sequence = %v, %v", got, err)
			}
		})
	}
}
