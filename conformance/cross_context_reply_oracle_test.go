// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"testing"
)

// TestCrossContextReplyDiffOracle: identical replies from separately compiled contexts. Once the
// output children have a children table (four instances), lyd_find_sibling_first matches the
// foreign output leaf-list by schema lineage, so the diff is empty, as it is without the table
// (three instances).
func TestCrossContextReplyDiffOracle(t *testing.T) {
	base := filepath.Join("corpus", "ut-parser")
	target := map[string]any{"searchdirs": []any{"rb.1"}, "modules": []any{map[string]any{"name": "rb"}}}
	for _, tc := range []struct{ id, data string }{
		{"output-leaflist-3", `{"rb:v":[1,2,3]}`},
		{"output-leaflist-4", `{"rb:v":[1,2,3,4]}`},
		{"output-leaflist-4-reordered", `{"rb:v":[4,3,2,1]}`},
	} {
		t.Run(tc.id, func(t *testing.T) {
			params := map[string]any{
				"op": "sequence", "searchdirs": []any{"rb.1"},
				"modules": []any{map[string]any{"name": "rb"}},
				"steps": []any{
					map[string]any{"do": "parse", "format": "json", "data_type": "reply", "parse_only": true,
						"rpc": `{"rb:r":{}}`, "data": tc.data},
					map[string]any{"do": "diff", "format": "json", "data_type": "reply", "parse_only": true,
						"rpc": `{"rb:r":{}}`, "data": `{"rb:v":[1,2,3,4]}`, "target": target},
					map[string]any{"do": "dump"},
				},
			}
			if tc.id == "output-leaflist-3" {
				params["steps"].([]any)[1].(map[string]any)["data"] = `{"rb:v":[1,2,3]}`
			}
			got, err := (Yang{}).Run(Request{ID: tc.id, BaseDir: base, Params: params})
			if err != nil {
				t.Fatal(err)
			}
			want, err := nativeRequestRunner(t)(base, params)
			if err != nil {
				t.Fatal(err)
			}
			want, _ = withoutSkipped(want, Yang{}.SkippedFields("sequence"))
			got, _ = withoutSkipped(got, Yang{}.SkippedFields("sequence"))
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s\noracle: %v\nengine: %v", diff, want, got)
			}
			t.Logf("engine: %v", got)
		})
	}
}
