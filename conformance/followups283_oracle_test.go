// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFollowups283NativeRequests keeps the post-merge any-printer and XPath-cast
// observations live against libyang v5.8.6 without adding generated goldens.
func TestFollowups283NativeRequests(t *testing.T) {
	oracle := nativeRequestRunner(t)
	compare := func(t *testing.T, base string, p map[string]any) Response {
		t.Helper()
		want, err := oracle(base, p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
		if err != nil {
			t.Fatal(err)
		}
		want, _ = withoutSkipped(want, Yang{}.SkippedFields(p["op"].(string)))
		got, _ = withoutSkipped(got, Yang{}.SkippedFields(p["op"].(string)))
		if diff := diffResponses(want, got); diff != "" {
			t.Fatalf("oracle mismatch: %s", diff)
		}
		return got
	}

	t.Run("metadata-cast-completes-with-diagnostic", func(t *testing.T) {
		p := map[string]any{
			"op":              "xpath",
			"searchdirs":      []any{"any-core/schemas", "types/schemas", "ietf"},
			"modules":         []any{map[string]any{"name": "ac"}, map[string]any{"name": "print-failure"}},
			"context_options": []any{"no_yanglibrary"},
			"format":          "json",
			"data_type":       "data",
			"parse_only":      true,
			"data":            `{"ac:data":{"print-failure:c":{"s":"value","@s":{"print-failure:select":"/missing:node"}}}}`,
			"xpath":           "string(/ac:data)",
		}
		got := compare(t, "corpus", p)
		result, _ := got["result"].(map[string]any)
		value, _ := result["value"].(string)
		if !strings.Contains(value, `select=""`) || len(got.Diagnostics()) == 0 {
			t.Fatalf("result %v, diagnostics %v", got["result"], got.Diagnostics())
		}
	})

	t.Run("hard-cast-keeps-printer-diagnostic", func(t *testing.T) {
		p := map[string]any{
			"op":              "xpath",
			"searchdirs":      []any{"any-core/schemas", "types/schemas", "ietf"},
			"modules":         []any{map[string]any{"name": "ac"}, map[string]any{"name": "print-failure"}},
			"context_options": []any{"no_yanglibrary"},
			"format":          "json",
			"data_type":       "data",
			"parse_only":      true,
			"data":            `{"ac:data":{"print-failure:c":{"xp":"/missing:node"}}}`,
			"xpath":           "string(/ac:data)",
		}
		got := compare(t, "corpus", p)
		if got["result"] != nil || len(got.Diagnostics()) == 0 || got.Verdict() == "valid" {
			t.Fatalf("verdict %s, result %v, diagnostics %v", got.Verdict(), got["result"], got.Diagnostics())
		}
	})

	t.Run("cross-context-hard-cast-diagnostic-owner", func(t *testing.T) {
		context := map[string]any{
			"searchdirs":      []any{"any-core/schemas", "types/schemas", "ietf"},
			"modules":         []any{map[string]any{"name": "ac"}, map[string]any{"name": "print-failure"}},
			"context_options": []any{"no_yanglibrary"},
		}
		p := map[string]any{
			"op":              "sequence",
			"searchdirs":      context["searchdirs"],
			"modules":         context["modules"],
			"context_options": context["context_options"],
			"steps": []any{
				map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
					"data": `{"ac:data":{"print-failure:c":{"xp":"/missing:node"}}}`},
				map[string]any{"do": "dup", "node": "/ac:data", "options": []any{}, "target": context},
				map[string]any{"do": "xpath", "xpath": "string(/ac:data)"},
			},
		}
		got := compare(t, "corpus", p)
		steps := got["steps"].([]any)
		xpathStep := steps[2].(map[string]any)
		if got.Verdict() != "invalid" || xpathStep["result"] != nil ||
			len(xpathStep["diagnostics"].([]any)) != 0 {
			t.Fatalf("verdict %s, xpath step %v", got.Verdict(), xpathStep)
		}
	})

	const diagnosticModule = `module ac {
  yang-version 1.1;
  namespace "urn:followups-283-diagnostic";
  prefix ac;
  import ietf-yang-metadata { prefix md; }
  import ietf-yang-types { prefix yang; }
  md:annotation select { type yang:xpath1.0; }
  leaf s { type string; }
  anydata data { must "string(.)"; }
}`
	diagnosticSetup := func(t *testing.T) (string, map[string]any) {
		t.Helper()
		base := t.TempDir()
		if err := os.WriteFile(filepath.Join(base, "ac.yang"), []byte(diagnosticModule), 0o644); err != nil {
			t.Fatal(err)
		}
		ietf, err := filepath.Abs(filepath.Join("corpus", "ietf"))
		if err != nil {
			t.Fatal(err)
		}
		return base, map[string]any{
			"searchdirs":      []any{".", ietf},
			"modules":         []any{map[string]any{"name": "ac"}},
			"context_options": []any{"no_yanglibrary"},
		}
	}
	const diagnosticData = `{"ac:data":{"ac:s":"value","@ac:s":{"ac:select":"/missing:node"}}}`

	t.Run("must-cast-diagnostic-retains-tree", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		p["op"] = "data"
		p["format"] = "json"
		p["data_type"] = "data"
		p["data"] = diagnosticData
		got := compare(t, base, p)
		if got.Verdict() != "valid" || got["tree"] == nil || len(got.Diagnostics()) != 1 {
			t.Fatalf("verdict %s, tree %v, diagnostics %v", got.Verdict(), got["tree"], got.Diagnostics())
		}
	})

	t.Run("cross-context-cast-diagnostic-owner", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		target := map[string]any{
			"searchdirs":      p["searchdirs"],
			"modules":         p["modules"],
			"context_options": p["context_options"],
		}
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": diagnosticData},
			map[string]any{"do": "dup", "node": "/ac:data", "options": []any{}, "target": target},
			map[string]any{"do": "xpath", "xpath": "string(/ac:data)"},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		xpathStep := steps[2].(map[string]any)
		if got.Verdict() != "valid" || len(xpathStep["diagnostics"].([]any)) != 0 || xpathStep["result"] == nil {
			t.Fatalf("verdict %s, xpath step %v", got.Verdict(), xpathStep)
		}
	})

	t.Run("cross-context-validation-retains-diagnostic", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		target := map[string]any{
			"searchdirs":      p["searchdirs"],
			"modules":         p["modules"],
			"context_options": p["context_options"],
		}
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": diagnosticData},
			map[string]any{"do": "dup", "node": "/ac:data", "options": []any{}, "target": target},
			map[string]any{"do": "validate"},
			map[string]any{"do": "dup", "node_xpath": "/ac:data/ac:s", "options": []any{}},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		validateStep := steps[2].(map[string]any)
		dupStep := steps[3].(map[string]any)
		if got.Verdict() != "invalid" || len(validateStep["diagnostics"].([]any)) != 0 ||
			len(dupStep["diagnostics"].([]any)) != 1 {
			t.Fatalf("verdict %s, validate step %v, dup step %v", got.Verdict(), validateStep, dupStep)
		}
	})

	t.Run("dup-selector-collects-replaced-source-diagnostic", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		target := map[string]any{
			"searchdirs":      p["searchdirs"],
			"modules":         p["modules"],
			"context_options": p["context_options"],
		}
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": diagnosticData},
			map[string]any{"do": "dup", "node": "/ac:data", "options": []any{}, "target": target},
			map[string]any{"do": "dup", "node_xpath": "/ac:data/ac:s[string(/ac:data)]", "options": []any{}},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		dupStep := steps[2].(map[string]any)
		if got.Verdict() != "invalid" || len(dupStep["diagnostics"].([]any)) != 1 {
			t.Fatalf("verdict %s, dup step %v", got.Verdict(), dupStep)
		}
	})

	t.Run("copied-payload-tagged-modes", func(t *testing.T) {
		base := t.TempDir()
		module := `module a {
  yang-version 1.1;
  namespace "urn:followups-283";
  prefix a;
  anydata any;
  container c { presence "c"; leaf d { type string; default "x"; } }
}`
		if err := os.WriteFile(filepath.Join(base, "a.yang"), []byte(module), 0o644); err != nil {
			t.Fatal(err)
		}
		ietf, err := filepath.Abs(filepath.Join("corpus", "ietf"))
		if err != nil {
			t.Fatal(err)
		}
		context := func(withDefaults bool) map[string]any {
			modules := []any{map[string]any{"name": "a"}}
			if withDefaults {
				modules = append(modules, map[string]any{"name": "ietf-netconf-with-defaults"})
			}
			return map[string]any{"searchdirs": []any{".", ietf}, "modules": modules,
				"context_options": []any{"no_yanglibrary"}}
		}
		for _, tc := range []struct {
			name                    string
			mode                    string
			sourceWD, destinationWD bool
			wantTag                 bool
		}{
			{"all-tagged-payload", "all-tagged", true, false, true},
			{"all-tagged-outer", "all-tagged", false, true, false},
			{"implicit-tagged-payload", "implicit-tagged", true, false, true},
			{"implicit-tagged-outer", "implicit-tagged", false, true, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				p := context(tc.sourceWD)
				p["op"] = "sequence"
				p["steps"] = []any{
					map[string]any{"do": "parse", "format": "json", "data_type": "config", "data": `{"a:any":{},"a:c":{}}`},
					map[string]any{"do": "dup", "node": "/a:c/d", "parent": "/a:any", "options": []any{"with_flags"}},
					map[string]any{"do": "dup", "node": "/a:any", "options": []any{}, "target": context(tc.destinationWD)},
					map[string]any{"do": "dump", "with_defaults": tc.mode},
				}
				got := compare(t, base, p)
				steps := got["steps"].([]any)
				tree := steps[3].(map[string]any)["tree"].(map[string]any)
				jsonTagged := strings.Contains(tree["json"].(string), "ietf-netconf-with-defaults:default")
				xmlTagged := strings.Contains(tree["xml"].(string), `:default="true"`)
				if jsonTagged != tc.wantTag || xmlTagged != tc.wantTag {
					t.Fatalf("tree %v, want tag %t", tree, tc.wantTag)
				}
			})
		}
	})

	t.Run("opaque-hash-miss", func(t *testing.T) {
		base := t.TempDir()
		for dir, module := range map[string]string{
			"source": `module a { yang-version 1.1; namespace "urn:followups-283-opaque"; prefix a;
	anydata any; container p { presence "p"; leaf y1 { type string; } leaf y2 { type string; }
	  leaf y3 { type string; } leaf y4 { type string; } } }`,
			"target": `module a { yang-version 1.1; namespace "urn:followups-283-opaque"; prefix a;
  anydata any; container p { presence "p"; leaf x { type string; } } }`,
		} {
			if err := os.Mkdir(filepath.Join(base, dir), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(base, dir, "a.yang"), []byte(module), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		data := `<any xmlns="urn:followups-283-opaque"><p><y1>v</y1><y2>v</y2><y3>v</y3><y4>v</y4>` +
			strings.Repeat(`<z/>`, 256) + `<x>v</x></p></any>`
		ctx := func(dir string) map[string]any {
			return map[string]any{"searchdirs": []any{dir}, "modules": []any{map[string]any{"name": "a"}},
				"context_options": []any{"no_yanglibrary"}}
		}
		p := ctx("source")
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "parse", "format": "xml", "data_type": "data", "parse_only": true,
				"unknown": "opaque", "data": data},
			map[string]any{"do": "dup", "node": "/a:any", "options": []any{}, "target": ctx("target")},
			map[string]any{"do": "insert", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"a:p":{"x":"real"}}`},
			map[string]any{"do": "xpath", "xpath": "count((/a:any/a:p | /a:p)/a:x)"},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		result := steps[3].(map[string]any)["result"].(map[string]any)
		if fmt.Sprint(result["value"]) != "2" {
			t.Fatalf("result %v", result)
		}
	})
}
