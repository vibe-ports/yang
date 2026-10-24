// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"testing"
)

// TestAstra305Round11Requests pins the native behavior behind the round-eleven
// merge, ordinary leaf-list insertion, and keyed instance-identifier fixes.
func TestAstra305Round11Requests(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	cases := []struct {
		name  string
		steps []any
	}{
		{
			name: "asymmetric-keyless-children-table-merge",
			steps: []any{
				map[string]any{"do": "parse", "format": "json", "data_type": "rpc", "parse_only": true,
					"data": `{"pv2-astra305:r":{"input":"a","in2":"b","in3":"c","outer":[{"inner":[{"id":1},{"id":1}]}]}}`},
				map[string]any{"do": "edit", "format": "json", "data_type": "rpc",
					"merge": `{"pv2-astra305:r":{"input":"a","in2":"b","in3":"c","outer":[{"inner":[{"id":1},{"id":2}]}]}}`},
				map[string]any{"do": "dump"},
			},
		},
		{
			name: "distinct-datastore-leaf-lists",
			steps: []any{
				map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
					"data": `{"pv2-astra305:data-r":{"distinct-a":["a"],"distinct-b":["b"],"distinct-c":["c"],"distinct-d":["d"]}}`},
				map[string]any{"do": "dump"},
			},
		},
		{
			name: "keyed-instance-identifier",
			steps: []any{
				map[string]any{"do": "parse", "format": "json", "data_type": "data",
					"data": `{"pv2-astra305:probe-r":{"item":[{"id":"x"}],"ref":"/pv2-astra305:probe-r/item[id='x']","union-ref":"/pv2-astra305:probe-r/item[id='x']"}}`},
				map[string]any{"do": "dump"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{
				"op":              "sequence",
				"searchdirs":      []any{"schemas"},
				"modules":         []any{map[string]any{"name": "pv2-astra305"}},
				"context_options": []any{"no_yanglibrary"},
				"steps":           tc.steps,
			}
			got, err := (Yang{}).Run(Request{ID: tc.name, BaseDir: base, Params: params})
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
