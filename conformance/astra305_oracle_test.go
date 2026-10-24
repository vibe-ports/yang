// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"testing"
)

// TestAstra305Requests keeps the operation-child states behind the round-five
// regressions pinned to libyang v5.8.6 without generated golden files.
func TestAstra305Requests(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	foreign := map[string]any{
		"searchdirs": []any{"schemas"},
		"modules":    []any{map[string]any{"name": "pv2-astra305"}},
		"context_options": []any{
			"no_yanglibrary",
		},
	}
	keyed := map[string]any{
		"searchdirs": []any{"astra305-keyed"},
		"modules":    []any{map[string]any{"name": "pv2-astra305"}},
		"context_options": []any{
			"no_yanglibrary",
		},
	}
	parseReply := map[string]any{
		"do": "parse", "format": "json", "data_type": "reply", "parse_only": true, "keep_input": true,
		"rpc":  `{"pv2-astra305:r":{"input":"x"}}`,
		"data": `{"pv2-astra305:before":"before","pv2-astra305:l":[{"value":"a"},{"value":"b"},{"value":"c"}]}`,
	}
	cases := []struct {
		id    string
		steps []any
	}{
		{
			// A node selected from the start of a parsed top-level sibling list moves
			// that complete list, in order, beside the retained destination data.
			id: "insert-whole-top-level-sibling-list",
			steps: []any{
				map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
					"data": `{"pv2-astra305:anchor":"kept"}`},
				map[string]any{"do": "insert", "format": "json", "data_type": "data", "parse_only": true,
					"data": `{"pv2-astra305:data-r":{"a":"a"},"pv2-astra305:moved":"moved"}`},
				map[string]any{"do": "dump"},
			},
		},
		{
			id: "merge-disjoint-keyless-operation-lists",
			steps: []any{
				map[string]any{"do": "parse", "format": "json", "data_type": "rpc", "parse_only": true,
					"data": `{"pv2-astra305:r":{}}`},
				map[string]any{"do": "edit", "format": "json", "data_type": "rpc",
					"merge": `{"pv2-astra305:r":{"outer":[{"inner":[{"id":1}]},{"inner":[{"id":2}]},{"inner":[{"id":3}]},{"inner":[{"id":4}]}]}}`},
				map[string]any{"do": "dump"},
			},
		},
		{
			id: "empty-hash-copy-insert-child",
			steps: []any{
				map[string]any{"do": "parse", "format": "json", "data_type": "rpc", "parse_only": true,
					"data": `{"pv2-astra305:r":{"input":"a","in2":"b","in3":"c","in4":"d"}}`},
				map[string]any{"do": "edit", "delete": "/pv2-astra305:r/input"},
				map[string]any{"do": "edit", "delete": "/pv2-astra305:r/in2"},
				map[string]any{"do": "edit", "delete": "/pv2-astra305:r/in3"},
				map[string]any{"do": "edit", "delete": "/pv2-astra305:r/in4"},
				map[string]any{"do": "dup", "node": "/pv2-astra305:r", "options": []any{"recursive"}},
				map[string]any{"do": "edit", "insert_term": map[string]any{
					"parent": "/pv2-astra305:r", "name": "v", "value": "7"}},
				map[string]any{"do": "dump"},
			},
		},
		{
			// Recursive operation duplication creates a no-table top-level sibling
			// set with a keyless list split around the retained input. The Go merge
			// regression uses this native-reachable shape and merges its later run.
			id: "duplicated-operation-children-split-keyless-run",
			steps: []any{
				parseReply,
				map[string]any{"do": "dup", "node_xpath": `/pv2-astra305:r/l[value="a"]`,
					"siblings": true, "options": []any{"recursive"}},
				map[string]any{"do": "dump"},
			},
		},
		{
			id: "keyless-digest-invalidated-after-descendant-merge",
			steps: []any{
				map[string]any{"do": "parse", "format": "json", "data_type": "rpc", "parse_only": true,
					"data": `{"pv2-astra305:r":{"outer":[{"inner":[{"id":1},{"id":1}]},{"inner":[{"id":1},{"id":1},{"id":2}]}]}}`},
				map[string]any{"do": "edit", "format": "json", "data_type": "rpc",
					"merge": `{"pv2-astra305:r":{"outer":[{"inner":[{"id":1},{"id":2}]},{"inner":[{"id":1},{"id":1},{"id":2}]}]}}`},
				map[string]any{"do": "dump"},
			},
		},
		{
			id: "foreign-output-leaflist-delete-without-table",
			steps: []any{
				map[string]any{"do": "parse", "format": "json", "data_type": "rpc", "parse_only": true,
					"data": `{"pv2-astra305:r":{}}`},
				map[string]any{"do": "dup", "node": "/pv2-astra305:r", "options": []any{"recursive"},
					"target": foreign},
				map[string]any{"do": "edit", "insert_term": map[string]any{
					"parent": "/pv2-astra305:r", "name": "output-v", "value": "1", "options": []any{"output"}}},
				map[string]any{"do": "diff", "format": "json", "data_type": "reply", "parse_only": true,
					"rpc": `{"pv2-astra305:r":{}}`, "data": `{}`},
				map[string]any{"do": "parse", "format": "json", "data_type": "reply", "parse_only": true,
					"rpc": `{"pv2-astra305:r":{}}`, "data": `{"pv2-astra305:output-v":[1]}`},
				map[string]any{"do": "diff_apply"},
				map[string]any{"do": "dump"},
			},
		},
		{
			id: "foreign-key-declaration-digest",
			steps: []any{
				map[string]any{"do": "parse", "format": "json", "data_type": "rpc", "parse_only": true,
					"data": `{"pv2-astra305:r":{}}`},
				map[string]any{"do": "dup", "node": "/pv2-astra305:r", "options": []any{"recursive"},
					"target": keyed},
				map[string]any{"do": "edit", "insert_list": map[string]any{
					"parent": "/pv2-astra305:r", "name": "text-outer"}},
				map[string]any{"do": "edit", "insert_list": map[string]any{
					"parent": "/pv2-astra305:r/text-outer[1]", "name": "text-inner", "keys": []any{""}}},
				map[string]any{"do": "unlink", "node": "/pv2-astra305:r/text-outer[1]"},
				map[string]any{"do": "parse", "format": "json", "data_type": "rpc", "parse_only": true,
					"data": `{"pv2-astra305:r":{"input":"a","in2":"b","in3":"c"}}`},
				map[string]any{"do": "insert", "detached": true, "parent": "/pv2-astra305:r"},
				map[string]any{"do": "edit", "format": "json", "data_type": "rpc",
					"merge_into": `{"pv2-astra305:r":{"input":"a","in2":"b","in3":"c","text-outer":[{"text-inner":[{"id":""}]}]}}`},
				map[string]any{"do": "dump"},
			},
		},
		{
			id: "validate-union-leaflist-then-merge",
			steps: []any{
				map[string]any{"do": "parse", "format": "xml", "data_type": "data", "parse_only": true,
					"data": `<data-r xmlns="urn:vibe-ports:yang:conformance:pv2-astra305"><a>a</a><b>b</b><c>c</c><union-v>01</union-v></data-r>`},
				map[string]any{"do": "validate", "data_type": "data"},
				map[string]any{"do": "edit", "merge_self": true},
				map[string]any{"do": "dump"},
			},
		},
		{
			id: "foreign-keyless-list-maps-to-keyed-list",
			steps: []any{
				map[string]any{"do": "parse", "format": "json", "data_type": "rpc", "parse_only": true,
					"data": `{"pv2-astra305:r":{}}`},
				map[string]any{"do": "dup", "node": "/pv2-astra305:r", "options": []any{"recursive"},
					"target": foreign},
				map[string]any{"do": "edit", "insert_list": map[string]any{
					"parent": "/pv2-astra305:r", "name": "mapped"}},
				map[string]any{"do": "edit", "insert_term": map[string]any{
					"parent": "/pv2-astra305:r/mapped[1]", "name": "id", "value": ""}},
				map[string]any{"do": "unlink", "node": "/pv2-astra305:r/mapped[1]"},
				map[string]any{"do": "parse", "format": "json", "data_type": "rpc", "parse_only": true,
					"data": `{"pv2-astra305:r":{"input":"a","in2":"b","in3":"c"}}`},
				map[string]any{"do": "insert", "detached": true, "parent": "/pv2-astra305:r"},
				map[string]any{"do": "edit", "format": "json", "data_type": "rpc",
					"merge_into": `{"pv2-astra305:r":{"input":"a","in2":"b","in3":"c","mapped":[{"id":""}]}}`},
				map[string]any{"do": "dump"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			params := map[string]any{
				"op": "sequence", "searchdirs": []any{"schemas"},
				"modules":         []any{map[string]any{"name": "pv2-astra305"}},
				"context_options": []any{"no_yanglibrary"}, "steps": tc.steps,
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
		})
	}
}
