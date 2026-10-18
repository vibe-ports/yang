// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSequenceXPath(t *testing.T) {
	for _, tc := range []struct {
		expr string
		want int
	}{
		{"/pv2-hash-lyb:c/ll", 2},
		{"ll[.='a']", 1},
		{"/pv2-hash-lyb:c/ll[.='missing']", 0},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			resp, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: map[string]any{
				"op": "sequence", "searchdirs": []any{"schemas"},
				"modules": []any{map[string]any{"name": "pv2-hash-lyb"}}, "context_options": []any{"no_yanglibrary"},
				"steps": []any{
					map[string]any{"do": "parse", "parse_only": true, "data": `{"pv2-hash-lyb:c":{"ll":["a","b"]}}`},
					map[string]any{"do": "xpath", "xpath": tc.expr},
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Verdict() != "valid" {
				t.Fatalf("XPath step failed: %v", resp)
			}
			steps := resp["steps"].([]any)
			before, after := steps[0].(map[string]any), steps[1].(map[string]any)
			result := after["result"].(map[string]any)
			if result["type"] != "node-set" || len(result["nodes"].([]any)) != tc.want {
				t.Errorf("result = %v, want node-set with %d nodes", result, tc.want)
			}
			if !reflect.DeepEqual(before["typed"], after["typed"]) {
				t.Error("XPath step changed the retained tree")
			}
		})
	}
}

func TestSequenceXPathEmptyTree(t *testing.T) {
	for _, parseEmpty := range []bool{false, true} {
		name := "no retained tree"
		steps := []any{}
		if parseEmpty {
			name = "parsed empty tree"
			steps = append(steps, map[string]any{"do": "parse", "parse_only": true, "data": `{}`})
		}
		t.Run(name, func(t *testing.T) {
			steps = append(steps, map[string]any{"do": "xpath", "xpath": "/pv2-hash-lyb:c"})
			resp, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: map[string]any{
				"op": "sequence", "searchdirs": []any{"schemas"},
				"modules": []any{map[string]any{"name": "pv2-hash-lyb"}}, "context_options": []any{"no_yanglibrary"},
				"steps": steps,
			}})
			if err != nil {
				t.Fatal(err)
			}
			out := resp["steps"].([]any)
			step := out[len(out)-1].(map[string]any)
			if resp.Verdict() != "invalid" || step["rc"].(map[string]any)["name"] != "LY_EINVAL" {
				t.Fatalf("empty-tree XPath response = %v, want LY_EINVAL", resp)
			}
			if result, exists := step["result"]; !exists || result != nil || len(step["diagnostics"].([]any)) != 0 {
				t.Fatalf("empty-tree XPath step = %v, want null result and no diagnostics", step)
			}
		})
	}
}

func TestSequenceCompareSelf(t *testing.T) {
	for _, tc := range []struct {
		name, want, err string
		step            map[string]any
		empty           bool
	}{
		{"empty tree", "LY_SUCCESS", "", map[string]any{"self": true}, true},
		{"same root", "LY_SUCCESS", "", map[string]any{"self": true, "options": []any{"full_recursion"}}, false},
		{"different nodes", "LY_ENOT", "", map[string]any{"self": true, "first": "/pv2-hash-lyb:c/ll[.='a']", "second": "/pv2-hash-lyb:c/ll[.='b']"}, false},
		{"false needs data", "", "compare step needs data", map[string]any{"self": false}, false},
		{"non-boolean needs data", "", "compare step needs data", map[string]any{"self": "true"}, false},
		{"self refuses data", "", "unknown key", map[string]any{"self": true, "data": "{}"}, false},
		{"self refuses parse options", "", "unknown key", map[string]any{"self": true, "parse_only": true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step := maps.Clone(tc.step)
			step["do"] = "compare"
			steps := []any{}
			if !tc.empty {
				steps = append(steps, map[string]any{"do": "parse", "parse_only": true,
					"data": `{"pv2-hash-lyb:c":{"ll":["a","b"]}}`})
			}
			steps = append(steps, step)
			resp, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: map[string]any{
				"op": "sequence", "searchdirs": []any{"schemas"},
				"modules":         []any{map[string]any{"name": "pv2-hash-lyb"}},
				"context_options": []any{"no_yanglibrary"}, "steps": steps,
			}})
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("error = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := resp["steps"].([]any)[len(steps)-1].(map[string]any)["compare"].(map[string]any)["name"]
			if got != tc.want {
				t.Fatalf("comparison = %v, want %s", got, tc.want)
			}
		})
	}
}

// Run these requests before the oracle goldens are generated. The source-backed
// expectations exercise the XPath index directly, followed by an independent
// NewPath lookup that must create a separate list.
func TestStoredHashRepeatedKeyAfterTable(t *testing.T) {
	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, fixture := range m.Fixtures {
		if !strings.HasPrefix(fixture.ID, "protocol-v2/hash-repeated-key-after-table") {
			continue
		}
		matched++
		t.Run(fixture.ID, func(t *testing.T) {
			response, err := (Yang{}).Run(Request{ID: fixture.ID, BaseDir: fixtureDir(m, fixture), Params: fixture.Request})
			if err != nil {
				t.Fatal(err)
			}
			if response.Verdict() != "valid" {
				t.Fatalf("request failed: %#v", response)
			}
			steps := response["steps"].([]any)
			result := steps[2].(map[string]any)["result"].(map[string]any)
			if result["type"] != "node-set" || len(result["nodes"].([]any)) != 0 {
				t.Errorf("original-key XPath lookup = %v, want empty node-set", result)
			}
			comparison := steps[3].(map[string]any)["compare"].(map[string]any)
			if comparison["name"] != "LY_SUCCESS" {
				t.Errorf("recursive self-comparison = %v, want LY_SUCCESS", comparison)
			}
			printed := steps[5].(map[string]any)["tree"].(map[string]any)["json"].(string)
			var output struct {
				Container struct {
					Pairs []struct {
						Value string `json:"v"`
					} `json:"pair"`
				} `json:"pv2-hash-lyb:c"`
			}
			if err := json.Unmarshal([]byte(printed), &output); err != nil {
				t.Fatal(err)
			}
			values := map[string]int{}
			for _, pair := range output.Container.Pairs {
				values[pair.Value]++
			}
			if len(output.Container.Pairs) != 2 || values["original"] != 1 || values["lookup-created"] != 1 {
				t.Errorf("original-key lookup did not create a second pair: %s", printed)
			}
		})
	}
	if matched != 2 {
		t.Fatalf("found %d repeated-key fixtures, want 2", matched)
	}
}
