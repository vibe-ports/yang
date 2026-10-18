// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Run these independent expectations without loading goldens: the round-six
// fixtures are deliberately awaiting native golden generation by the maintainer.
func TestStoredHashRoundSixFixtures(t *testing.T) {
	wants := map[string][]int{
		"hash-key-insert-equal-hash-order":          {1},
		"hash-key-insert-hash-return-history":       {1},
		"hash-exposed-leaflist-setvalue":            {1},
		"hash-exposed-nested-list-rehash":           {1},
		"hash-exposed-union-validation":             {1},
		"hash-xpath-repeated-first-multikey":        {0, 1},
		"hash-xpath-repeated-first-multikey-noht":   {0, 1},
		"hash-xpath-repeated-first-multikey-bucket": {1, 3},
		"hash-xpath-copied-key-child-hash":          {0, 1},
		"hash-xpath-copied-key-child-hash-noht":     {0, 1},
		"hash-xpath-copied-key-child-hash-bucket":   {0, 2},
		"hash-xpath-registration-order":             {1},
		"hash-xpath-probe-store-error":              nil,
		"hash-xpath-probe-store-error-table":        nil,
		"hash-enum-large-declaration-width":         nil,
		"hash-nonkey-edit-long-key":                 {1, 1},
		"hash-xpath-many-parent-miss":               {0},
		"hash-bits-sparse-empty-repeat":             nil,
	}
	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, f := range m.Fixtures {
		name := strings.TrimPrefix(f.ID, "protocol-v2/")
		counts, ok := wants[name]
		if !ok {
			continue
		}
		seen++
		t.Run(name, func(t *testing.T) {
			got, err := (Yang{}).Run(Request{ID: f.ID, BaseDir: filepath.Join(m.Corpus(), f.Dir), Params: f.Request})
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(name, "hash-xpath-probe-store-error") {
				if got["verdict"] != "invalid" {
					t.Fatalf("probe store must fail: %v", got)
				}
				diagnostics := got["diagnostics"].([]any)
				if len(diagnostics) != 1 {
					t.Fatalf("probe diagnostics = %v", diagnostics)
				}
				d := diagnostics[0].(map[string]any)
				path := "/e:li/id"
				if strings.HasSuffix(name, "-table") {
					path = "/e:root/li/id"
				}
				const message = `Invalid instance-identifier "/f:lp[p='2001:db8::/64']" value - semantic error: Unsatisfied pattern - "2001:db8::/64" does not match ".*0db8.*".`
				if d["msg"] != message {
					t.Errorf("probe diagnostic message = %v, want %s", d["msg"], message)
				}
				if d["schema_path"] != path || d["data_path"] != nil || d["apptag"] != nil ||
					d["vecode_name"] != "LYVE_DATA" || d["code"].(map[string]any)["name"] != "LY_EVALID" {
					t.Errorf("probe diagnostic lost location or code: %v", d)
				}
				return
			}
			if got["verdict"] != "valid" {
				t.Fatalf("fixture failed: %v", got)
			}
			idx := 0
			for i, raw := range got["steps"].([]any) {
				step := raw.(map[string]any)
				switch step["do"] {
				case "compare":
					if fmt.Sprint(step["compare"].(map[string]any)["name"]) != "LY_SUCCESS" {
						t.Errorf("step %d compare = %v", i, step["compare"])
					}
				case "xpath":
					nodes := step["result"].(map[string]any)["nodes"].([]any)
					if idx >= len(counts) || len(nodes) != counts[idx] {
						t.Errorf("step %d nodes=%v; want counts %v at %d", i, nodes, counts, idx)
					}
					idx++
				}
			}
			steps := got["steps"].([]any)
			if name == "hash-exposed-union-validation" {
				for index, want := range map[int]string{2: "2", 3: "02"} {
					found := false
					for _, raw := range steps[index].(map[string]any)["typed"].([]any) {
						n := raw.(map[string]any)
						if strings.HasSuffix(n["path"].(string), "/v") {
							found = true
							if n["value"].(map[string]any)["canonical"] != want {
								t.Errorf("step %d union value = %v, want %s", index, n["value"], want)
							}
						}
					}
					if !found {
						t.Errorf("step %d has no exposed union", index)
					}
				}
			}
			if strings.HasPrefix(name, "hash-key-insert-") || name == "hash-xpath-registration-order" {
				leaf := "/marker"
				want := []string{"sentinel", "updated-A", "B"}
				if name == "hash-xpath-registration-order" {
					leaf, want = "/v", []string{"first", "updated-second"}
				}
				var values []string
				for _, raw := range steps[len(steps)-1].(map[string]any)["typed"].([]any) {
					n := raw.(map[string]any)
					if strings.HasSuffix(n["path"].(string), leaf) {
						values = append(values, n["value"].(map[string]any)["canonical"].(string))
					}
				}
				if !slices.Equal(values, want) {
					t.Errorf("NewPath changed markers %v, want %v", values, want)
				}
			}
			if idx != len(counts) {
				t.Errorf("ran %d XPath checks; want %d", idx, len(counts))
			}
		})
	}
	if seen != len(wants) {
		t.Fatalf("ran %d fixtures, want %d", seen, len(wants))
	}
}

func TestSequenceStoredHashSelectors(t *testing.T) {
	for _, tc := range []struct {
		name string
		step map[string]any
		want string
	}{
		{"missing-selector-before-value", map[string]any{"do": "change_term", "value": 1}, "change_term needs node and value"},
		{"exclusive-selector", map[string]any{"do": "change_term", "node": "/m:r", "node_xpath": "/m:r", "value": "x"}, "takes node or node_xpath, not both"},
		{"empty-selector", map[string]any{"do": "change_term", "node_xpath": "/m:r", "value": "x"}, "must select exactly one node"},
		{"fractional-child", map[string]any{"do": "change_term", "node": "/m:r", "value": "x", "child": 1.5}, "child must be a positive integer"},
		{"string-child", map[string]any{"do": "change_term", "node": "/m:r", "value": "x", "child": "1"}, "child must be a positive integer"},
		{"zero-child", map[string]any{"do": "change_term", "node": "/m:r", "value": "x", "child": 0}, "child must be a positive integer"},
		{"merge-self-false", map[string]any{"do": "edit", "merge_self": false}, "merge_self must be true"},
		{"xpath-variable-type", map[string]any{"do": "xpath", "xpath": "/m:r", "vars": map[string]any{"a": 1}}, "vars value of a must be a string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (Yang{}).Run(Request{BaseDir: "corpus/protocol-v2", Params: map[string]any{"op": "sequence", "searchdirs": []any{"hash-history"}, "modules": []any{map[string]any{"name": "m"}}, "context_options": []any{"no_yanglibrary"}, "steps": []any{tc.step}}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %s", err, tc.want)
			}
		})
	}
}
