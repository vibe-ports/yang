// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"path/filepath"
	"testing"
)

// lyd_find_path uses the input schema even for a reply tree. Only change_term's
// separate node_xpath selector can address an output child of that tree.
func TestReplyOutputNodeSelectors(t *testing.T) {
	base := []any{
		map[string]any{"do": "parse", "format": "json", "data_type": "reply", "unknown": "reject", "parse_only": true,
			"keep_input": true, "rpc": `{"rb:r":{"in":"keep"}}`, "data": `{"rb:before":"b","rb:v":[1,2,3]}`},
		map[string]any{"do": "dup", "node": "/rb:r", "options": []any{"recursive"}},
	}
	for _, tc := range []struct {
		name, wantError string
		step            map[string]any
	}{
		{"change path", "change_term node /rb:r/v[.='3'] not found", map[string]any{"do": "change_term", "node": "/rb:r/v[.='3']", "value": "4"}},
		{"dup path", "dup node /rb:r/v[.='3'] not found", map[string]any{"do": "dup", "node": "/rb:r/v[.='3']"}},
		{"dup parent", "dup parent /rb:r/v[.='3'] not found", map[string]any{"do": "dup", "node": "/rb:r", "parent": "/rb:r/v[.='3']"}},
		{"insert path", "insert node /rb:r/v[.='3'] not found", map[string]any{"do": "insert", "node": "/rb:r/v[.='3']"}},
		{"insert parent", "insert parent /rb:r/v[.='3'] not found", map[string]any{"do": "insert", "node": "/rb:r", "parent": "/rb:r/v[.='3']"}},
		{"metadata path", "metadata node /rb:r/v[.='3'] not found", map[string]any{"do": "edit", "new_meta": map[string]any{"node": "/rb:r/v[.='3']", "name": "rb:id", "value": "x"}}},
		{"compare path", "compare first /rb:r/v[.='3'] not found", map[string]any{"do": "compare", "self": true, "first": "/rb:r/v[.='3']"}},
		{"change XPath", "", map[string]any{"do": "change_term", "node_xpath": "/rb:r/v[.='3']", "value": "4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := append(append([]any{}, base...), tc.step)
			r, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "ut-parser"), Params: map[string]any{
				"op": "sequence", "searchdirs": []any{"rb.1"}, "modules": []any{map[string]any{"name": "rb"}}, "steps": steps,
			}})
			if tc.wantError != "" {
				if err == nil || err.Error() != tc.wantError+" (a request-error)" {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil || r["verdict"] != "valid" {
				t.Fatalf("response = %v, error = %v", r, err)
			}
			change := r["steps"].([]any)[2].(map[string]any)["change"].(map[string]any)
			if change["name"] != "LY_SUCCESS" {
				t.Fatalf("change = %v", change)
			}
			typed := r["steps"].([]any)[2].(map[string]any)["typed"].([]any)
			last := typed[len(typed)-1].(map[string]any)
			if last["schema"] != "/rb:r/output/v" || last["value"].(map[string]any)["canonical"] != "4" {
				t.Fatalf("selected output leaf-list = %v", last)
			}
		})
	}
}
