// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"path/filepath"
	"testing"
)

func issue247API1Cases() []issue247Case {
	request := func(id, module, searchdir, verdict string, steps ...any) issue247Case {
		return issue247Case{id: "ut-new/issue247-api1-" + id, verdict: verdict, params: map[string]any{
			"op": "sequence", "searchdirs": []any{searchdir},
			"modules":         []any{map[string]any{"name": module}},
			"context_options": []any{"no_yanglibrary"}, "steps": steps,
		}}
	}
	edit := func(kind string, value map[string]any) any {
		return map[string]any{"do": "edit", kind: value}
	}
	dump := map[string]any{"do": "dump", "with_defaults": "all-tagged"}
	return []issue247Case{
		request("term-store-only", "e", "e.1", "valid",
			edit("insert_term", map[string]any{"module": "e", "name": "r", "value": "20", "options": []any{"store_only"}}), dump),
		request("term-canonical", "a", "a.1", "valid",
			edit("insert_term", map[string]any{"module": "a", "name": "foo", "value": "016", "options": []any{"canon"}}), dump),
		request("term-combined-options", "a", "a.1", "invalid",
			edit("insert_term", map[string]any{"module": "a", "name": "foo", "value": "1", "options": []any{"store_only", "canon"}}), dump),
		request("list-store-only", "e", "e.1", "valid",
			edit("insert_list", map[string]any{"module": "e", "name": "lk", "keys": []any{"20"}, "options": []any{"store_only"}}), dump),
		request("list-canonical", "a", "a.1", "valid",
			edit("insert_list", map[string]any{"module": "a", "name": "l11", "keys": []any{"01"}, "options": []any{"canon"}}), dump),
		request("list-combined-options", "a", "a.1", "invalid",
			edit("insert_list", map[string]any{"module": "a", "name": "l11", "keys": []any{"1"}, "options": []any{"store_only", "canon"}}), dump),
		request("set-canonical", "a", "a.1", "valid",
			edit("insert_term", map[string]any{"module": "a", "name": "foo", "value": "1"}),
			map[string]any{"do": "change_term", "node": "/a:foo", "value": "02", "canon": true}, dump),
		request("list2-canonical-ignored", "a", "a.1", "valid",
			edit("insert_list2", map[string]any{"module": "a", "name": "l11", "keys": "[a='01']", "options": []any{"canon"}}), dump),
		request("list2-combined-options", "a", "a.1", "valid",
			edit("insert_list2", map[string]any{"module": "a", "name": "l11", "keys": "[a='01']", "options": []any{"store_only", "canon"}}), dump),
		request("list2-store-only-does-not-skip-predicate", "a", "a.1", "invalid",
			edit("insert_list2", map[string]any{"module": "a", "name": "l11", "keys": "[a='x']", "options": []any{"store_only"}}), dump),
		request("list2-keyless-empty", "a", "a.1", "valid",
			edit("insert_list2", map[string]any{"module": "a", "name": "l2", "keys": ""}), dump),
		request("list2-union-restorage", "e", "e.1", "valid",
			edit("insert_list2", map[string]any{"module": "e", "name": "lu", "keys": "[k='01']"}), dump),
		request("list2-non-list", "a", "a.1", "invalid",
			edit("insert_list2", map[string]any{"module": "a", "name": "c", "keys": ""}), dump),
		request("list2-output-lookup", "e", "e.1", "invalid",
			edit("insert_inner", map[string]any{"module": "e", "name": "op2"}),
			edit("insert_list2", map[string]any{"parent": "/e:op2", "name": "li2", "keys": "[k='abc']", "options": []any{"output"}}), dump),
		request("list2-opaque-parent", "a", "a.1", "valid",
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"unknown": "opaque", "data": `{"a:zz":{"q":1}}`},
			edit("insert_list2", map[string]any{"parent_opaq": "zz", "module": "a", "name": "l1", "keys": "[a='x'][b='y']"}), dump),
	}
}

// These request cases keep every newly executable canonical/list2 branch active in ordinary
// tests. The oracle-tagged twin compares each complete response with libyang v5.8.6.
func TestIssue247API1Requests(t *testing.T) {
	base := filepath.Join("corpus", "ut-new")
	for _, tc := range issue247API1Cases() {
		t.Run(tc.id, func(t *testing.T) {
			got := issue247Run(t, base, tc)
			if got.Verdict() != tc.verdict {
				t.Fatalf("verdict = %q, want %q: %#v", got.Verdict(), tc.verdict, got)
			}
		})
	}
}
