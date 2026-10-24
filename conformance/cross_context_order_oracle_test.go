// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestDiffReviewOracleCases keeps the ordering, indexed-miss, and small retained-delta request
// shapes live. Their libyang implementation behavior has no hand-written golden here.
func TestDiffReviewOracleCases(t *testing.T) {
	base := filepath.Join("corpus", "ut-diff")
	tests := []struct {
		name, module, firstDir, secondDir, first, second, order string
		foreign                                                 bool
	}{
		{
			name: "mixed-width-rb-rotation", module: "diff-cross-context-int-width-rb-order",
			firstDir: "schemas/cross-context-int-width-rb-order-a", secondDir: "schemas/cross-context-int-width-rb-order-b",
			first:  `<top xmlns="urn:vibe-ports:yang:diff-cross-context-int-width-rb-order"><v>1</v></top>`,
			second: `<top xmlns="urn:vibe-ports:yang:diff-cross-context-int-width-rb-order"><v>-2</v><v>-1</v><v>0</v></top>`,
			order:  ">1</v>|>-2</v>|>-1</v>|>0</v>", foreign: true,
		},
		{
			name: "integer-callback-zeroed-string", module: "diff-cross-context-int-string-order",
			firstDir: "schemas/cross-context-int-string-order-a", secondDir: "schemas/cross-context-int-string-order-b",
			first:  `<top xmlns="urn:vibe-ports:yang:diff-cross-context-int-string-order"><v>-1</v></top>`,
			second: `<top xmlns="urn:vibe-ports:yang:diff-cross-context-int-string-order"><v>2</v></top>`,
			order:  ">-1</v>|>2</v>", foreign: true,
		},
		{
			name: "disjoint-index-misses", module: "diff-cross-context-foreign-creates-toplevel",
			firstDir: "schemas/cross-context-foreign-creates-toplevel-a", secondDir: "schemas/cross-context-foreign-creates-toplevel-b",
			first:   `<a xmlns="urn:vibe-ports:yang:diff-cross-context-foreign-creates-toplevel">A</a><b xmlns="urn:vibe-ports:yang:diff-cross-context-foreign-creates-toplevel">B</b>`,
			second:  `<c xmlns="urn:vibe-ports:yang:diff-cross-context-foreign-creates-toplevel">C</c><d xmlns="urn:vibe-ports:yang:diff-cross-context-foreign-creates-toplevel">D</d>`,
			foreign: true,
		},
		{
			name: "same-context-delta", module: "defaults", firstDir: "schemas",
			first:  `<df xmlns="urn:libyang:tests:defaults"><foo>1</foo></df>`,
			second: `<df xmlns="urn:libyang:tests:defaults"><foo>2</foo></df>`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{
				"op": "sequence", "searchdirs": []any{tc.firstDir},
				"modules": []any{map[string]any{"name": tc.module}},
				"steps": []any{
					map[string]any{"do": "parse", "format": "xml", "data_type": "data", "parse_only": true, "data": tc.first},
					map[string]any{"do": "diff", "format": "xml", "data_type": "data", "parse_only": true,
						"data": tc.second},
				},
			}
			if tc.foreign {
				steps := params["steps"].([]any)
				steps[1].(map[string]any)["target"] = map[string]any{"searchdirs": []any{tc.secondDir},
					"modules": []any{map[string]any{"name": tc.module}}}
			}
			got, err := (Yang{}).Run(Request{ID: "ut-diff/" + tc.name, BaseDir: base, Params: params})
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
				t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
			}
			if tc.order != "" {
				steps := want["steps"].([]any)
				diff := steps[1].(map[string]any)["diff"].(map[string]any)["xml"].(string)
				at := 0
				for _, value := range strings.Split(tc.order, "|") {
					next := strings.Index(diff[at:], value)
					if next < 0 {
						t.Fatalf("oracle diff order does not contain %q after byte %d:\n%s", value, at, diff)
					}
					at += next + len(value)
				}
			}
		})
	}
}
