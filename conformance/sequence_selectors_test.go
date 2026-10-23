// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func reorderedSelectorRequest(t *testing.T, value string) (string, map[string]any) {
	t.Helper()
	base, p := diagnosticOwnershipSetup(t)
	module := strings.Replace(diagnosticOwnershipModule, "  anydata data", "  leaf-list ll { type string; }\n  anydata data", 1)
	if err := os.WriteFile(filepath.Join(base, "ac.yang"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	p["op"] = "sequence"
	p["steps"] = []any{
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true, "data": diagnosticOwnershipData},
		map[string]any{"do": "dup", "node": "/ac:data", "options": []any{}, "target": diagnosticOwnershipTarget(p)},
		map[string]any{"do": "edit", "set": map[string]any{"path": "/ac:ll[.='a']"}},
		map[string]any{"do": "edit", "set": map[string]any{"path": "/ac:ll[.='b']"}},
		map[string]any{"do": "change_term", "node_xpath": "/ac:ll[1][string(/ac:data)]", "value": value},
		map[string]any{"do": "dup", "node_xpath": "/ac:data/ac:s", "options": []any{}},
	}
	return base, p
}

func checkReorderedSelector(t *testing.T, got Response) {
	t.Helper()
	steps := got["steps"].([]any)
	change := steps[4].(map[string]any)
	dup := steps[5].(map[string]any)
	if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "5" ||
		len(change["diagnostics"].([]any)) != 0 || len(dup["diagnostics"].([]any)) != 1 {
		t.Fatalf("verdict %s, failed step %v, change %v, dup %v", got.Verdict(), got["failed_step"], change, dup)
	}
}

func TestSequenceReorderedSelectorEvaluatedOnce(t *testing.T) {
	for _, value := range []string{"z", "0"} {
		t.Run(value, func(t *testing.T) {
			base, p := reorderedSelectorRequest(t, value)
			got, err := (Yang{}).Run(Request{BaseDir: base, Params: p})
			if err != nil {
				t.Fatal(err)
			}
			checkReorderedSelector(t, got)
		})
	}
}

func opaqueParentSelectorRequest(t *testing.T, kind string, width int) (string, map[string]any) {
	t.Helper()
	base, p := diagnosticOwnershipSetup(t)
	p["op"] = "sequence"
	var roots []string
	for i := width - 1; i > 0; i-- {
		roots = append(roots, fmt.Sprintf("\"ac:unknown%04d\":{}", i))
	}
	roots = append(roots, `"ac:unknown":{}`) // worst-position opaque parent
	constructor := map[string]any{"parent_opaq": "unknown", "module": "ac"}
	switch kind {
	case "insert_term":
		constructor["name"], constructor["value"] = "s", "v"
	case "insert_inner":
		constructor["name"] = "parent"
	case "insert_list":
		constructor["name"], constructor["keys"] = "pair", []any{"a", "b"}
	}
	p["steps"] = []any{
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
			"data": `{"ac:s":"anchor"}`},
		map[string]any{"do": "insert", "format": "json", "data_type": "data", "parse_only": true,
			"unknown": "opaque", "data": "{" + strings.Join(roots, ",") + "}", "target": diagnosticOwnershipTarget(p)},
		map[string]any{"do": "edit", kind: constructor},
	}
	return base, p
}

func TestSequenceOpaqueParentSelectorEvaluatedOnce(t *testing.T) {
	for _, kind := range []string{"insert_term", "insert_inner", "insert_list"} {
		for _, width := range []int{2, 32, 256} {
			t.Run(fmt.Sprintf("%s/%d", kind, width), func(t *testing.T) {
				base, p := opaqueParentSelectorRequest(t, kind, width)
				got, counts, err := countedSequenceSelectors(t, Request{BaseDir: base, Params: p})
				if err != nil {
					t.Fatal(err)
				}
				step := got["steps"].([]any)[2].(map[string]any)
				if counts[2]["insert parent_opaq"] != 1 || got.Verdict() != "invalid" ||
					fmt.Sprint(got["failed_step"]) != "2" || len(step["diagnostics"].([]any)) != 0 {
					t.Fatalf("opaque parent response = %v; counts = %v", got, counts)
				}
				t.Logf("width=%d parent_opaq scans=%d", width, counts[2]["insert parent_opaq"])
			})
		}
	}
}

// Selector inventory (including the former cache users):
//
//	change_term: preflight -> SetValue -> ownership routing, including child;
//	dup: retained/source node or node_xpath, parent -> copy/source-context collect;
//	edit metadata/delete: node -> edit/free -> ownership routing;
//	edit constructors: parent/parent_opaq/detached_parent -> construction and error routing;
//	insert, unlink, find_meta: each node/parent/diff_node path, once at execution;
//	parse parent, compare first/second, diff node/data_node, diff_merge parent/src_node,
//	xpath context node: independent single lookups, observed by Request.findNode.
//
// No selector cache remains. Omitted nodes and @top/@last/detached are pointer selections.
// Exclusions: edit set/insert_list2 paths construct data; notFoundRC probes proper path
// prefixes to distinguish partial matches. trim/EvalXPath evaluate requested operations,
// not node selectors. Standalone data/xpath/atoms and parse_one's RPC/operation-parent
// checks have no repeated lookups and do not use the former per-step cache.
func countedSequenceSelectors(t *testing.T, r Request) (Response, map[int]map[string]int, error) {
	t.Helper()
	counts := map[int]map[string]int{}
	r.selectorEvaluated = func(step int, label string) {
		if counts[step] == nil {
			counts[step] = map[string]int{}
		}
		counts[step][label]++
	}
	got, err := (Yang{}).Run(r)
	for step, selectors := range counts {
		for label, count := range selectors {
			if count != 1 {
				t.Errorf("step %d selector %q evaluated %d times, want 1", step, label, count)
			}
		}
	}
	return got, counts, err
}

func TestSequenceSelectorEvaluationMatrix(t *testing.T) {
	contexts := newInventoryContextCache()
	covered := map[string]bool{}
	run := func(t *testing.T, p map[string]any) {
		t.Helper()
		got, counts, err := countedSequenceSelectors(t, Request{BaseDir: "corpus/protocol-v2", Params: p,
			buildContext: contexts.request().build})
		if err != nil {
			// The existing matrix includes missing/malformed selectors and capability
			// refusals. Counts must still hold for every selector actually reached.
			t.Logf("matrix request rejected: %v", err)
		} else if got == nil {
			t.Fatal("successful request has no response")
		}
		for _, selectors := range counts {
			for label := range selectors {
				covered[label] = true
			}
		}
	}
	for _, seed := range inventorySeeds() {
		if seed.params["op"] != "sequence" {
			continue
		}
		t.Run(seed.name, func(t *testing.T) { run(t, seed.params) })
		if seed.name == "step/dup" {
			t.Run(seed.name+"/xpath-only", func(t *testing.T) {
				p := inventoryClone(seed.params)
				st := inventorySubject(p, []string{"steps", "1"})
				delete(st, "node")
				st["node_xpath"] = "/pv2:c"
				run(t, p)
			})
		}
		for _, row := range inventoryRows(seed.scope) {
			if !slices.Contains([]string{"node", "node_xpath", "parent", "first", "second", "diff_node", "data_node", "src_node", "delete"}, row.key) {
				continue
			}
			for _, value := range inventoryValues(row, seed) {
				t.Run(seed.name+"/"+row.key+"/"+value.name, func(t *testing.T) {
					p := inventoryMutation(t, seed, row, value, "corpus/protocol-v2", false)
					run(t, p)
				})
			}
		}
	}
	// The oracle-tagged pairwise matrix also has a non-operation reply parent.
	// Include that seed here so the observer must see a real parent lookup.
	t.Run("reply/non-operation-parent", func(t *testing.T) {
		p := inventoryCommon("sequence")
		p["steps"] = []any{
			map[string]any{"do": "parse", "parse_only": true, "data": inventoryData},
			map[string]any{"do": "parse", "data_type": "reply", "parse_only": true, "parent": "/pv2:c", "data": "{}"},
		}
		run(t, p)
	})
	for _, label := range []string{"change_term node", "change_term node_xpath", "dup node", "dup node_xpath", "dup parent",
		"insert parent", "metadata node", "delete node", "insert node", "unlink node", "find_meta node",
		"xpath node", "parse parent", "compare first", "compare second", "diff node", "diff data_node", "diff_merge src_node"} {
		if !covered[label] {
			t.Errorf("step matrix did not reach selector %q", label)
		}
	}
}

// Count work at the engine boundary, including a duplicate-heavy union selector
// over roots supplied in adverse order. Parsing/mutation/FindXPath retain their
// existing data budgets; engine routing must add no further XPath evaluation.
func TestSequenceSelectorCountedWork(t *testing.T) {
	for _, width := range []int{2, 32, 256} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			base, p := reorderedSelectorRequest(t, "z")
			var values []string
			for i := width; i > 0; i-- {
				values = append(values, fmt.Sprintf("\"v%04d\"", i))
			}
			for _, selector := range []string{"node_xpath", "node"} {
				t.Run(selector, func(t *testing.T) {
					st := map[string]any{"do": "change_term", "value": "z"}
					if selector == "node_xpath" {
						st[selector] = "/ac:ll[1] | /ac:ll[1] | /ac:ll[1]"
					} else {
						st[selector] = "/ac:ll[.='v0001']"
					}
					p["steps"] = []any{
						map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
							"data": `{"ac:ll":[` + strings.Join(values, ",") + `]}`}, st,
					}
					got, counts, err := countedSequenceSelectors(t, Request{BaseDir: base, Params: p})
					if err != nil || got.Verdict() != "valid" {
						t.Fatalf("counted request = %v, %v", got, err)
					}
					count := counts[1]["change_term "+selector]
					t.Logf("width=%d %s evaluations=%d", width, selector, count)
					if count != 1 {
						t.Fatalf("width %d: %s evaluations = %d, want 1", width, selector, count)
					}
				})
			}
		})
	}
}
