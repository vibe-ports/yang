// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"testing"
)

// Live v5.8.6 oracle reproducers of the post-merge findings on #257 and #261
// (#295). They have no generated goldens.
func TestPostMerge295Requests(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	ctx := func(module string) map[string]any {
		return map[string]any{"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": module}},
			"context_options": []any{"no_yanglibrary"}}
	}
	parse := func(input string) any {
		return map[string]any{"do": "parse", "format": "json", "data_type": "config", "parse_only": true, "data": input}
	}
	const probe = `/f:lp[p='2001:0db8::/64']`
	cases := []struct {
		id     string
		params map[string]any
	}{
		// union_find_type logs nothing from its member trials: only the union error.
		{"union-member-probe-not-logged", map[string]any{"op": "data",
			"searchdirs":      []any{"schemas", "../ut-new/e.1", "../ut-new/f.1"},
			"modules":         []any{map[string]any{"name": "pv2-probe-union"}, map[string]any{"name": "e"}, map[string]any{"name": "f"}},
			"context_options": []any{"no_yanglibrary"}, "format": "json", "data_type": "data",
			"data":             `{"e:li":[{"id":"` + probe + `"}],"pv2-probe-union:c":{"selector":"` + probe + `","fallback":"` + probe + `"}}`,
			"validate_options": []any{"present"}}},
		// lyd_dup_single compares only the source and parent contexts.
		{"dup-under-foreign-parent", map[string]any{"op": "sequence", "searchdirs": []any{"schemas"},
			"modules": []any{map[string]any{"name": "pv2-detached"}}, "context_options": []any{"no_yanglibrary"},
			"steps": []any{
				parse(`{"pv2-detached:l":[{"k":"a"}]}`),
				map[string]any{"do": "insert", "format": "json", "data_type": "config", "parse_only": true,
					"target": ctx("pv2-detached"), "data": `{"pv2-detached:c":{"ll":["x","y"]}}`},
				map[string]any{"do": "dup", "node": "/pv2-detached:c/ll[.='x']", "parent": "/pv2-detached:c"},
				map[string]any{"do": "dump"},
			}}},
		// TestApplyDiffForeignSortedRunUnordered: a foreign bucket root makes the
		// top set schema-unordered, then a foreign diff creates ll=3, which sorts
		// into the existing run. The diff is built first, against an empty tree;
		// the protocol has one diff register, so the foreign root is inserted.
		{"foreign-sorted-run-unordered", map[string]any{"op": "sequence", "searchdirs": []any{"schemas"},
			"modules": []any{map[string]any{"name": "pv2-lyds"}}, "context_options": []any{"no_yanglibrary"},
			"steps": []any{
				map[string]any{"do": "diff", "format": "json", "data_type": "config", "parse_only": true,
					"target": ctx("pv2-lyds"), "data": `{"pv2-lyds:ll":[3]}`},
				parse(`{"pv2-lyds:ll":[1,5],"pv2-lyds:bucket":[{"name":"x"}]}`),
				map[string]any{"do": "insert", "format": "json", "data_type": "config", "parse_only": true,
					"target": ctx("pv2-lyds"), "data": `{"pv2-lyds:bucket":[{"name":"y"}]}`},
				map[string]any{"do": "diff_apply"},
				map[string]any{"do": "dump"},
			}}},
	}
	detachedDefault := func(wd string) map[string]any {
		return map[string]any{"op": "sequence", "searchdirs": []any{"schemas"},
			"modules": []any{map[string]any{"name": "pv2-detached-default"}}, "context_options": []any{"no_yanglibrary"},
			"steps": []any{
				parse(`{"pv2-detached-default:c":{"t":["x"],"u":"x"}}`),
				map[string]any{"do": "unlink", "node": "/pv2-detached-default:c/u"},
				map[string]any{"do": "detached_dump", "with_defaults": wd},
			}}
	}
	cases = append(cases, []struct {
		id     string
		params map[string]any
	}{
		// The default check of a detached union leaf (lysc_value_cmp) logs only the union
		// error, not the leafref member's lyxp_eval root error.
		{"detached-union-default-trim", detachedDefault("trim")},
		{"detached-union-default-tagged", detachedDefault("all-tagged")},
		// lyd_dup_r converts the B child from its own context: pv2-edit's context lacks
		// pv2-detached, so duplicating the opaque parent under an A node is LY_ENOTFOUND.
		{"dup-foreign-child-under-opaque", map[string]any{"op": "sequence", "searchdirs": []any{"schemas"},
			"modules": []any{map[string]any{"name": "pv2-edit"}}, "context_options": []any{"no_yanglibrary"},
			"steps": []any{
				map[string]any{"do": "parse", "format": "json", "data_type": "config", "parse_only": true,
					"unknown": "opaque", "data": `{"pv2-edit:unknown":{}}`},
				map[string]any{"do": "insert", "format": "json", "data_type": "config", "parse_only": true,
					"target": ctx("pv2-detached"), "data": `{"pv2-detached:c":{"base":"x"}}`, "parent": "@top"},
				map[string]any{"do": "dump"},
				map[string]any{"do": "unlink", "node": "@top"},
				parse(`{"pv2-edit:c":{}}`),
				map[string]any{"do": "dup", "detached": true, "parent": "/pv2-edit:c", "options": []any{"recursive"}},
				map[string]any{"do": "dump"},
			}}},
	}...)
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			got, err := (Yang{}).Run(Request{ID: tc.id, BaseDir: base, Params: tc.params})
			if err != nil {
				t.Fatal(err)
			}
			want, err := nativeRequestRunner(t)(base, tc.params)
			if err != nil {
				t.Fatal(err)
			}
			op := tc.params["op"].(string)
			want, _ = withoutSkipped(want, Yang{}.SkippedFields(op))
			got, _ = withoutSkipped(got, Yang{}.SkippedFields(op))
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s (%s)\noracle: %v\nengine: %v", diff,
					round12FirstDifference(normalize(want), normalize(got), ""), want, got)
			}
		})
	}
}
