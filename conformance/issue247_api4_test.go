// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"path/filepath"
	"testing"
)

// issue247API4Cases are the insert_opaq edits and compare constructions Tree.NewOpaque
// (lyd_new_opaq / lyd_new_opaq2, libyang v5.8.6 src/tree_data_new.c:966, :997) makes executable.
func issue247API4Cases() []issue247Case {
	request := func(id, verdict string, steps ...any) issue247Case {
		return issue247Case{id: "ut-new/issue247-api4-" + id, verdict: verdict, params: map[string]any{
			"op": "sequence", "searchdirs": []any{"a.1"},
			"modules":         []any{map[string]any{"name": "a"}},
			"context_options": []any{"no_yanglibrary"}, "steps": steps,
		}}
	}
	opaq := func(o map[string]any) any { return map[string]any{"do": "edit", "insert_opaq": o} }
	dump := map[string]any{"do": "dump", "with_defaults": "all-tagged"}
	parseOpaque := map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
		"unknown": "opaque", "data": `{"a:zz":{"q":1}}`}
	compare := func(o map[string]any) any {
		return map[string]any{"do": "compare", "insert_opaq": o, "options": []any{"opaq"}}
	}
	return []issue247Case{
		request("json-top-literal-name", "valid",
			opaq(map[string]any{"name": "x:y", "module": "nomod", "value": "v"}), dump),
		request("json-prefix", "valid",
			opaq(map[string]any{"name": "n", "module": "a", "prefix": "a", "value": "v"}), dump),
		request("json-null-prefix", "valid",
			opaq(map[string]any{"name": "n", "module": "a", "prefix": nil}), dump),
		request("json-prefix-mismatch", "invalid",
			opaq(map[string]any{"name": "n", "module": "a", "prefix": "b"}), dump),
		request("json-empty-prefix", "invalid",
			opaq(map[string]any{"name": "n", "module": "a", "prefix": ""}), dump),
		request("json-empty-prefix-empty-module", "valid",
			opaq(map[string]any{"name": "n", "module": "", "prefix": ""}), dump),
		request("json-null-hint", "valid",
			opaq(map[string]any{"name": "n", "module": "a", "value": "[null]"}), dump),
		request("xml-namespace", "valid",
			opaq(map[string]any{"name": "n", "module": "urn:x", "prefix": "p", "value": "[null]", "xml": true}), dump),
		request("xml-empty-prefix", "valid",
			opaq(map[string]any{"name": "n", "module": "urn:x", "prefix": "", "xml": true}), dump),
		request("after-schema-sibling", "valid",
			map[string]any{"do": "edit", "insert_term": map[string]any{"module": "a", "name": "foo", "value": "1"}},
			opaq(map[string]any{"name": "o", "module": "a", "value": "v"}), dump),
		request("container-child", "valid",
			map[string]any{"do": "edit", "insert_inner": map[string]any{"module": "a", "name": "c"}},
			opaq(map[string]any{"parent": "/a:c", "name": "o", "module": "a", "value": "v"}), dump),
		request("opaque-parent-appended", "valid", parseOpaque,
			opaq(map[string]any{"parent_opaq": "zz", "name": "first", "module": "a"}),
			opaq(map[string]any{"parent_opaq": "zz", "name": "second", "module": "urn:x", "xml": true}), dump),
		request("compare-equal", "valid",
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"unknown": "opaque", "data": `{"a:zz":"v"}`},
			compare(map[string]any{"name": "zz", "module": "a", "value": "v"})),
		request("compare-different", "valid",
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"unknown": "opaque", "data": `{"a:zz":"v"}`},
			compare(map[string]any{"name": "zz", "module": "a", "value": "w"})),
		request("compare-xml", "valid",
			map[string]any{"do": "parse", "format": "xml", "data_type": "data", "parse_only": true,
				"unknown": "opaque", "data": `<zz xmlns="urn:x">v</zz>`},
			compare(map[string]any{"name": "zz", "module": "urn:x", "value": "v", "xml": true})),
		request("compare-prefix-mismatch", "invalid",
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"unknown": "opaque", "data": `{"a:zz":"v"}`},
			compare(map[string]any{"name": "zz", "module": "a", "prefix": "b", "value": "v"})),
	}
}

// issue247API4PendingCases put a retained-context printer error in the queue (issue247PendingPrefix)
// before a compare, then collect the retained context again with a later edit. compare_other
// collects only the context it builds the second root in (lyoracle.c:2497, :3372): a target's,
// none with self:true, so the queued error must survive until the later edit.
func issue247API4PendingCases() []issue247Case {
	target := map[string]any{"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "print-failure"}}}
	later := map[string]any{"do": "edit", "delete": "/print-failure:c/s"}
	request := func(id string, compare map[string]any) issue247Case {
		steps := append(issue247PendingPrefix(), compare, later, map[string]any{"do": "dump"})
		return issue247Case{id: "types/issue247-api4-pending-" + id, verdict: "invalid", params: map[string]any{
			"op": "sequence", "searchdirs": []any{"schemas"},
			"modules": []any{map[string]any{"name": "print-failure"}}, "steps": steps,
		}}
	}
	opaq := map[string]any{"name": "c", "module": "print-failure"}
	return []issue247Case{
		request("compare-opaque-target", map[string]any{"do": "compare", "insert_opaq": opaq, "target": target}),
		request("compare-opaque", map[string]any{"do": "compare", "insert_opaq": opaq}),
		request("compare-data-target", map[string]any{"do": "compare", "format": "json", "data_type": "data",
			"parse_only": true, "data": `{"print-failure:c":{}}`, "target": target}),
		request("compare-self", map[string]any{"do": "compare", "self": true}),
	}
}

// TestIssue247API4Requests keeps every newly executable insert_opaq and compare construction
// branch active in ordinary tests: before Tree.NewOpaque each one was ErrUnsupported (U-0107).
// The oracle-tagged twin compares each complete response with libyang v5.8.6.
func TestIssue247API4Requests(t *testing.T) {
	for base, cases := range map[string][]issue247Case{
		filepath.Join("corpus", "ut-new"): issue247API4Cases(),
		filepath.Join("corpus", "types"):  issue247API4PendingCases(),
	} {
		for _, tc := range cases {
			t.Run(tc.id, func(t *testing.T) {
				got := issue247Run(t, base, tc)
				if got.Verdict() != tc.verdict {
					t.Fatalf("verdict = %q, want %q: %#v", got.Verdict(), tc.verdict, got)
				}
			})
		}
	}
}
