// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestAstra305Round12DetachedOperationDiff pins lyd_diff_tree on an unlinked
// operation child. Both ordinary and user-ordered singleton lookups must retain
// the detached instance.
func TestAstra305Round12DetachedOperationDiff(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	for _, tc := range []struct {
		name, data, path string
	}{
		{
			name: "leaf",
			data: `{"pv2-astra305:r":{"detached-plain":"x"}}`,
			path: "/pv2-astra305:r/detached-plain",
		},
		{
			name: "user-ordered-leaf-list",
			data: `{"pv2-astra305:r":{"detached-ordered":["x"]}}`,
			path: "/pv2-astra305:r/detached-ordered[.='x']",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{
				"op":              "sequence",
				"searchdirs":      []any{"schemas"},
				"modules":         []any{map[string]any{"name": "pv2-astra305"}},
				"context_options": []any{"no_yanglibrary"},
				"steps": []any{
					map[string]any{"do": "parse", "format": "json", "data_type": "rpc", "parse_only": true,
						"data": tc.data},
					map[string]any{"do": "unlink", "node": tc.path},
					map[string]any{"do": "diff", "format": "json", "data_type": "rpc", "parse_only": true,
						"detached": true, "single": true, "data": tc.data, "data_node": tc.path},
				},
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
			steps, ok := got["steps"].([]any)
			if !ok || len(steps) != 3 {
				t.Fatalf("response steps = %#v, want three reached steps", got["steps"])
			}
			last, ok := steps[2].(map[string]any)
			if !ok || last["diff"] != nil {
				t.Fatalf("detached self-diff = %#v, want nil", steps[2])
			}
		})
	}
}

// TestAstra305Round12IndexedPaths compares the observable results of the hot
// paths whose indexes avoid rescanning operation runs and XML schema prefixes.
func TestAstra305Round12IndexedPaths(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	values := make([]string, 64)
	var prefix strings.Builder
	for i := range values {
		values[i] = strconv.Itoa(i)
		prefix.WriteString(`<opaque-prefix>`)
		prefix.WriteString(values[i])
		prefix.WriteString(`</opaque-prefix>`)
	}
	operation := `{"pv2-astra305:r":{"v":[` + strings.Join(values, ",") + `]}}`
	opaque := `<data-r xmlns="urn:vibe-ports:yang:conformance:pv2-astra305">` +
		prefix.String() + strings.Repeat(`<unknown>x</unknown>`, 64) + `</data-r>`
	for _, tc := range []struct {
		name  string
		steps []any
	}{
		{
			name: "operation-run-validate",
			steps: []any{
				map[string]any{"do": "parse", "format": "json", "data_type": "rpc", "parse_only": true,
					"data": operation},
				map[string]any{"do": "validate", "data_type": "rpc"},
				map[string]any{"do": "dump"},
			},
		},
		{
			name: "xml-opaque-anchor-after-schema-prefix",
			steps: []any{
				map[string]any{"do": "parse", "format": "xml", "data_type": "config", "parse_only": true,
					"unknown": "opaque", "data": opaque},
				map[string]any{"do": "dump"},
			},
		},
	} {
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
			steps, ok := got["steps"].([]any)
			if !ok || len(steps) != len(tc.steps) {
				t.Fatalf("response steps = %#v, want %d reached steps", got["steps"], len(tc.steps))
			}
		})
	}
}
