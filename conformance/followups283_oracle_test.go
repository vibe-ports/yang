// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"fmt"
	"maps"
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

	diagnosticSetup := func(t *testing.T) (string, map[string]any) {
		t.Helper()
		base := t.TempDir()
		if err := os.WriteFile(filepath.Join(base, "ac.yang"), []byte(diagnosticOwnershipModule), 0o644); err != nil {
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

	t.Run("repeated-metadata-casts-retain-each-diagnostic", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		prefix := strings.Repeat("p", 4096)
		p["op"] = "xpath"
		p["format"] = "json"
		p["data_type"] = "data"
		p["parse_only"] = true
		p["data"] = strings.Replace(diagnosticData, "missing", prefix, 1)
		p["xpath"] = "concat(string(/ac:data),string(/ac:data),string(/ac:data),string(/ac:data))"
		got := compare(t, base, p)
		result, _ := got["result"].(map[string]any)
		value, _ := result["value"].(string)
		if got.Verdict() != "valid" || strings.Count(value, `select=""`) != 4 || len(got.Diagnostics()) != 4 {
			t.Fatalf("verdict %s, result length %d, diagnostics %d", got.Verdict(), len(value), len(got.Diagnostics()))
		}
	})

	t.Run("hash-predicate-speculation-suppresses-diagnostic", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": diagnosticData[:len(diagnosticData)-1] + `,"ac:parent":{"l":[{"k":"x"}]}}`},
			map[string]any{"do": "xpath", "xpath": "count(/ac:parent/l[k=substring(string(/ac:data),1,0)])"},
		}
		got := compare(t, base, p)
		step := got["steps"].([]any)[1].(map[string]any)
		result, _ := step["result"].(map[string]any)
		if got.Verdict() != "valid" || len(step["diagnostics"].([]any)) != 0 ||
			result["type"] != "number" || fmt.Sprint(result["value"]) != "0" {
			t.Fatalf("speculative hash predicate response = %v", got)
		}
	})

	t.Run("hash-predicate-speculation-restores-diagnostics", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": diagnosticData[:len(diagnosticData)-1] + `,"ac:parent":{"l":[{"k":"x"}]}}`},
			map[string]any{"do": "xpath", "xpath": "count(/ac:parent/l[k=substring(string(/ac:data),1,0)]) + string-length(string(/ac:data))"},
		}
		got := compare(t, base, p)
		step := got["steps"].([]any)[1].(map[string]any)
		if got.Verdict() != "invalid" || len(step["diagnostics"].([]any)) != 1 {
			t.Fatalf("restored hash predicate diagnostics = %v", got)
		}
	})

	t.Run("hash-predicate-speculation-discards-printer-failure", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"ac:pair":[{"k1":"present","k2":"value"}],` +
					`"ac:data":{"ac:xp":"/missing:node"}}`},
			map[string]any{"do": "xpath",
				"xpath": "count(/ac:pair[k1='absent'][k2=string(/ac:data)])"},
		}
		got := compare(t, base, p)
		step := got["steps"].([]any)[1].(map[string]any)
		result, _ := step["result"].(map[string]any)
		if got.Verdict() != "valid" || len(step["diagnostics"].([]any)) != 0 ||
			result["type"] != "number" || fmt.Sprint(result["value"]) != "0" {
			t.Fatalf("speculative hash predicate response = %v", got)
		}
	})

	t.Run("hash-predicate-speculative-failure-matrix", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		type failureCase struct {
			name, list, target, data string
		}
		failures := []failureCase{
			{"printer-item", "pair", "/ac:data",
				`{"ac:pair":[{"k1":"present","k2":"value"}],"ac:data":{"ac:xp":"/missing:node"}}`},
			{"printer-itemless", "pair", "/ac:data",
				`{"ac:pair":[{"k1":"present","k2":"value"}],"ac:data":{"ac:xp":"'&#x1;'"}}`},
			{"unresolved-prefix", "pair", "/missing:data",
				`{"ac:pair":[{"k1":"present","k2":"value"}]}`},
			{"invalid-cast", "typed-pair", "/ac:text",
				`{"ac:typed-pair":[{"k1":"present","k2":1}],"ac:text":"bad"}`},
		}
		expressions := []struct {
			name string
			make func(string) string
		}{
			{"string", func(target string) string { return "string(" + target + ")" }},
			{"bare-node-set", func(target string) string { return target }},
			{"number", func(target string) string { return "number(" + target + ")" }},
			{"concat", func(target string) string { return "concat(''," + target + ")" }},
			{"comparison", func(target string) string { return target + " = 'bad'" }},
		}
		for _, failure := range failures {
			for _, expression := range expressions {
				for _, first := range []struct {
					name, value string
				}{
					{"removes-all", "absent"},
					{"keeps-one", "present"},
				} {
					t.Run(failure.name+"/"+expression.name+"/"+first.name, func(t *testing.T) {
						request := maps.Clone(p)
						request["op"] = "sequence"
						request["steps"] = []any{
							map[string]any{"do": "parse", "format": "json", "data_type": "data",
								"parse_only": true, "data": failure.data},
							map[string]any{"do": "xpath", "xpath": fmt.Sprintf(
								"count(/ac:%s[k1='%s'][k2=%s])", failure.list, first.value,
								expression.make(failure.target))},
						}
						got := compare(t, base, request)
						if first.name == "removes-all" {
							step := got["steps"].([]any)[1].(map[string]any)
							result, _ := step["result"].(map[string]any)
							if got.Verdict() != "valid" || len(step["diagnostics"].([]any)) != 0 ||
								result["type"] != "number" || fmt.Sprint(result["value"]) != "0" {
								t.Fatalf("discarded speculative failure response = %v", got)
							}
						}
					})
				}
			}
		}
	})

	t.Run("xpath-internal-precedes-printer-diagnostic", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		longPrefix := strings.Repeat("p", 256)
		module := strings.Replace(diagnosticOwnershipModule, "prefix ac;", "prefix "+longPrefix+";", 1)
		if err := os.WriteFile(filepath.Join(base, "ac.yang"), []byte(module), 0o644); err != nil {
			t.Fatal(err)
		}
		nameTests := make([]string, 32)
		for i := range nameTests {
			nameTests[i] = "/ac:s"
		}
		expanding := strings.Join(nameTests, " | ")
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"ac:s":"outer","@ac:s":{"ac:first":"one","ac:second":"two"},` +
					`"ac:data":{"ac:parent":{},"@ac:parent":{"ac:select":"` + expanding + `"},` +
					`"ac:s":"inner","@ac:s":{"ac:select":"/missing:node"}}}`},
			map[string]any{"do": "xpath", "xpath": "count(/ac:s/@*) + string-length(string(/ac:data))"},
		}
		got := compare(t, base, p)
		step := got["steps"].([]any)[1].(map[string]any)
		diagnostics := step["diagnostics"].([]any)
		if len(diagnostics) != 2 || step["rc"].(map[string]any)["name"] != "LY_EINT" {
			t.Fatalf("XPath step = %v; want two diagnostics and LY_EINT", step)
		}
		for i, want := range []string{"LY_EINT", "LY_EVALID"} {
			if code := diagnostics[i].(map[string]any)["code"].(map[string]any)["name"]; code != want {
				t.Fatalf("diagnostic %d code = %v, want %s", i, code, want)
			}
		}
		result := step["result"].(map[string]any)
		if result["type"] != "number" || fmt.Sprint(result["value"]) == "0" {
			t.Fatalf("expanded conversion result = %v", result)
		}
	})

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

	t.Run("same-context-dup-selector-retains-diagnostic", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": diagnosticData},
			map[string]any{"do": "dup", "node_xpath": "/ac:data[string(.)]", "options": []any{}},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		dupStep := steps[1].(map[string]any)
		diagnostics := dupStep["diagnostics"].([]any)
		if got.Verdict() != "invalid" || len(diagnostics) != 1 ||
			diagnostics[0].(map[string]any)["phase"] != "edit" {
			t.Fatalf("verdict %s, dup step %v", got.Verdict(), dupStep)
		}
	})

	t.Run("same-context-change-selector-retains-diagnostic", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": diagnosticData},
			map[string]any{"do": "change_term", "node_xpath": "/ac:data/ac:s[string(/ac:data)]", "value": "changed"},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		changeStep := steps[1].(map[string]any)
		diagnostics := changeStep["diagnostics"].([]any)
		if got.Verdict() != "invalid" || len(diagnostics) != 1 ||
			diagnostics[0].(map[string]any)["phase"] != "edit" {
			t.Fatalf("verdict %s, change step %v", got.Verdict(), changeStep)
		}
	})

	t.Run("cross-context-change-selector-keeps-request-owner", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		target := map[string]any{
			"searchdirs":      p["searchdirs"],
			"modules":         p["modules"],
			"context_options": p["context_options"],
		}
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"ac:data":{"ac:s":"value"}}`},
			map[string]any{"do": "dup", "node": "/ac:data", "options": []any{}, "target": target},
			map[string]any{"do": "edit", "new_meta": map[string]any{
				"node": "/ac:data", "name": "ac:select", "value": "/missing:node"}},
			map[string]any{"do": "change_term", "node_xpath": "/ac:data/ac:s[string(/ac:data)]", "value": "changed"},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		changeStep := steps[3].(map[string]any)
		diagnostics := changeStep["diagnostics"].([]any)
		if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "3" || len(diagnostics) != 1 ||
			diagnostics[0].(map[string]any)["phase"] != "edit" {
			t.Fatalf("verdict %s, failed step %v, change step %v", got.Verdict(), got["failed_step"], changeStep)
		}
	})

	t.Run("detached-dup-collects-pending-diagnostic-as-edit", func(t *testing.T) {
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
			map[string]any{"do": "unlink", "node": "/ac:data"},
			map[string]any{"do": "dup", "detached": true, "options": []any{}},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		dupStep := steps[4].(map[string]any)
		diagnostics := dupStep["diagnostics"].([]any)
		if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "4" || len(diagnostics) != 1 ||
			diagnostics[0].(map[string]any)["phase"] != "edit" {
			t.Fatalf("verdict %s, failed step %v, dup step %v", got.Verdict(), got["failed_step"], dupStep)
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

	t.Run("dup-source-selector-diagnostic-precedes-context-failure", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		target := map[string]any{
			"searchdirs":      p["searchdirs"],
			"modules":         p["modules"],
			"context_options": p["context_options"],
		}
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "data",
				"target": target, "data": `{"ac:s":"foreign"}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"ac:data":{}}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "dup", "node_xpath": "/ac:data[string(.)]", "parent": "/ac:data",
				"options": []any{}, "source": map[string]any{
					"format": "json", "data_type": "data", "parse_only": true, "data": diagnosticData}},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		dupStep := steps[3].(map[string]any)
		diagnostics := dupStep["diagnostics"].([]any)
		if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "3" || len(diagnostics) != 2 {
			t.Fatalf("verdict %s, failed step %v, dup step %v", got.Verdict(), got["failed_step"], dupStep)
		}
		for i, wantCode := range []string{"LY_EVALID", "LY_EINVAL"} {
			item := diagnostics[i]
			if phase := item.(map[string]any)["phase"]; phase != "edit" {
				t.Fatalf("diagnostic %d phase = %v, want edit", i, phase)
			}
			if code := item.(map[string]any)["code"].(map[string]any)["name"]; code != wantCode {
				t.Fatalf("diagnostic %d code = %v, want %s", i, code, wantCode)
			}
		}
	})

	t.Run("insert-foreign-source-collects-destination-diagnostic-as-edit", func(t *testing.T) {
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
			map[string]any{"do": "dump"},
			map[string]any{"do": "insert", "format": "json", "data_type": "data", "parse_only": true,
				"target": target, "data": `{"ac:s":"foreign"}`},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		insertStep := steps[2].(map[string]any)
		diagnostics := insertStep["diagnostics"].([]any)
		if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "2" || len(diagnostics) != 1 ||
			diagnostics[0].(map[string]any)["phase"] != "edit" {
			t.Fatalf("verdict %s, failed step %v, insert step %v", got.Verdict(), got["failed_step"], insertStep)
		}
	})

	t.Run("detached-dup-source-diagnostic-precedes-failure", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		target := map[string]any{
			"searchdirs":      p["searchdirs"],
			"modules":         p["modules"],
			"context_options": p["context_options"],
		}
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"ac:parent":{}}`},
			map[string]any{"do": "insert", "format": "json", "data_type": "data", "parse_only": true,
				"target": target, "data": `{"ac:data":{"ac:s":"value"}}`},
			map[string]any{"do": "edit", "new_meta": map[string]any{
				"node": "/ac:data", "name": "ac:select", "value": "/missing:node"}},
			map[string]any{"do": "unlink", "node": "/ac:data"},
			map[string]any{"do": "xpath", "detached": true, "xpath": "string(.)"},
			map[string]any{"do": "dup", "detached": true, "parent": "/ac:parent", "options": []any{}},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		dupStep := steps[5].(map[string]any)
		diagnostics := dupStep["diagnostics"].([]any)
		if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "5" || len(diagnostics) != 2 {
			t.Fatalf("verdict %s, failed step %v, dup step %v", got.Verdict(), got["failed_step"], dupStep)
		}
		for i, wantCode := range []string{"LY_EVALID", "LY_EINVAL"} {
			item := diagnostics[i].(map[string]any)
			if item["phase"] != "edit" || item["code"].(map[string]any)["name"] != wantCode {
				t.Fatalf("diagnostic %d = %v, want %s/edit", i, item, wantCode)
			}
		}
	})

	t.Run("detached-insert-source-diagnostic-precedes-failure", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		target := map[string]any{
			"searchdirs":      p["searchdirs"],
			"modules":         p["modules"],
			"context_options": p["context_options"],
		}
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"ac:parent":{}}`},
			map[string]any{"do": "insert", "format": "json", "data_type": "data", "parse_only": true,
				"target": target, "data": `{"ac:data":{"ac:s":"value"}}`},
			map[string]any{"do": "edit", "new_meta": map[string]any{
				"node": "/ac:data", "name": "ac:select", "value": "/missing:node"}},
			map[string]any{"do": "unlink", "node": "/ac:data"},
			map[string]any{"do": "xpath", "detached": true, "xpath": "string(.)"},
			map[string]any{"do": "insert", "detached": true, "parent": "/ac:parent"},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		insertStep := steps[5].(map[string]any)
		diagnostics := insertStep["diagnostics"].([]any)
		if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "5" || len(diagnostics) != 2 {
			t.Fatalf("verdict %s, failed step %v, insert step %v", got.Verdict(), got["failed_step"], insertStep)
		}
		if code := diagnostics[0].(map[string]any)["code"].(map[string]any)["name"]; code != "LY_EVALID" {
			t.Fatalf("first diagnostic = %v, want pending LY_EVALID", diagnostics[0])
		}
		for i, item := range diagnostics {
			if phase := item.(map[string]any)["phase"]; phase != "edit" {
				t.Fatalf("diagnostic %d phase = %v, want edit", i, phase)
			}
		}
	})

	t.Run("foreign-new-meta-diagnostic-stays-with-node-context", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		target := map[string]any{
			"searchdirs":      p["searchdirs"],
			"modules":         p["modules"],
			"context_options": p["context_options"],
		}
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "data",
				"target": target, "data": `{"ac:s":"foreign"}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"ac:parent":{}}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "edit", "new_meta": map[string]any{
				"node": "/ac:parent", "name": "missing:select", "value": "x"}},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		metaStep := steps[3].(map[string]any)
		if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "3" ||
			len(metaStep["diagnostics"].([]any)) != 0 {
			t.Fatalf("verdict %s, failed step %v, metadata step %v", got.Verdict(), got["failed_step"], metaStep)
		}
	})

	t.Run("foreign-delete-diagnostic-stays-with-node-context", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		target := map[string]any{
			"searchdirs":      p["searchdirs"],
			"modules":         p["modules"],
			"context_options": p["context_options"],
		}
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "data",
				"target": target, "data": `{"ac:s":"foreign"}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"ac:parent":{"l":[{"k":"key"}]}}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "edit", "delete": "/ac:parent/l[k='key']/k"},
			map[string]any{"do": "dup", "node": "/ac:parent", "options": []any{}},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		deleteStep := steps[3].(map[string]any)
		if len(deleteStep["diagnostics"].([]any)) != 0 || deleteStep["rc"].(map[string]any)["name"] != "LY_SUCCESS" {
			t.Fatalf("delete step %v", deleteStep)
		}
		dupStep := steps[4].(map[string]any)
		if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "4" ||
			len(dupStep["diagnostics"].([]any)) != 1 ||
			dupStep["diagnostics"].([]any)[0].(map[string]any)["phase"] != "edit" ||
			dupStep["diagnostics"].([]any)[0].(map[string]any)["code"].(map[string]any)["name"] != "LY_EINVAL" {
			t.Fatalf("verdict %s, failed step %v, dup step %v", got.Verdict(), got["failed_step"], dupStep)
		}
	})

	t.Run("foreign-insert-constructor-diagnostic-stays-with-parent-context", func(t *testing.T) {
		base, p := diagnosticSetup(t)
		target := map[string]any{
			"searchdirs":      p["searchdirs"],
			"modules":         p["modules"],
			"context_options": p["context_options"],
		}
		p["op"] = "sequence"
		p["steps"] = []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "data",
				"target": target, "data": `{"ac:s":"foreign"}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"ac:parent":{}}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "edit", "insert_term": map[string]any{
				"parent": "/ac:parent", "module": "ac", "name": "s", "value": "x"}},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		insertStep := steps[3].(map[string]any)
		if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "3" ||
			len(insertStep["diagnostics"].([]any)) != 0 || insertStep["rc"].(map[string]any)["name"] != "LY_EINVAL" {
			t.Fatalf("verdict %s, failed step %v, insert step %v", got.Verdict(), got["failed_step"], insertStep)
		}
	})

	t.Run("foreign-insert-parse-failure-does-not-collect-destination", func(t *testing.T) {
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
			map[string]any{"do": "dump"},
			map[string]any{"do": "insert", "format": "json", "data_type": "data", "parse_only": true,
				"target": target, "data": `{"ac:s":{}}`},
		}
		got := compare(t, base, p)
		steps := got["steps"].([]any)
		insertStep := steps[2].(map[string]any)
		diagnostics := insertStep["diagnostics"].([]any)
		if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "2" || len(diagnostics) != 1 ||
			diagnostics[0].(map[string]any)["phase"] != "parse" {
			t.Fatalf("verdict %s, failed step %v, insert step %v", got.Verdict(), got["failed_step"], insertStep)
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
