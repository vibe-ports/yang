// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

// A native constructor receives separate module and local-name arguments. Even a known
// module prefix in the local name must remain literal. Controls prove the child exists.
func issue247LiteralNameCases() []issue247Case {
	var cases []issue247Case
	for _, tc := range []struct {
		kind, parent, name string
		setup              []any
	}{
		{"insert_term", "/a:c", "x", []any{map[string]any{"do": "edit", "insert_inner": map[string]any{"module": "a", "name": "c"}}}},
		{"insert_inner", "/a:l2[1]", "c", []any{map[string]any{"do": "edit", "insert_list2": map[string]any{"module": "a", "name": "l2", "keys": ""}}}},
		{"insert_list", "/a:c2", "l3", []any{map[string]any{"do": "edit", "insert_inner": map[string]any{"module": "a", "name": "c2"}}}},
		{"insert_list2", "/a:c2", "l3", []any{map[string]any{"do": "edit", "insert_inner": map[string]any{"module": "a", "name": "c2"}}}},
	} {
		for _, mode := range []string{"control", "implicit", "explicit", "unknown-prefix", "double-colon", "detached"} {
			// detached_parent is not a protocol field for list2.
			if mode == "detached" && tc.kind == "insert_list2" {
				continue
			}
			name := tc.name
			if mode != "control" {
				name = "a:" + name
			}
			if mode == "unknown-prefix" {
				name = "missing:" + tc.name
			}
			if mode == "double-colon" {
				name = "a:a:" + tc.name
			}
			o := map[string]any{"parent": tc.parent, "name": name}
			switch tc.kind {
			case "insert_term":
				o["value"] = "x"
			case "insert_list":
				o["keys"] = []any{}
			case "insert_list2":
				o["keys"] = ""
			}
			if mode == "explicit" {
				o["module"] = "a"
			}
			steps := append([]any{}, tc.setup...)
			if mode == "detached" {
				steps = append(steps, map[string]any{"do": "unlink", "node": tc.parent})
				delete(o, "parent")
				o["detached_parent"] = true
			}
			steps = append(steps, map[string]any{"do": "edit", tc.kind: o})
			verdict := "invalid"
			if mode == "control" {
				verdict = "valid"
			}
			cases = append(cases, issue247Case{id: fmt.Sprintf("ut-new/issue247-literal-%s-%s", tc.kind, mode), verdict: verdict, params: map[string]any{
				"op": "sequence", "searchdirs": []any{"a.1"}, "modules": []any{map[string]any{"name": "a"}},
				"context_options": []any{"no_yanglibrary"}, "steps": steps,
			}})
		}
	}

	// lyd_new_meta is the exception: native code explicitly parses module:name.
	cases = append(cases, issue247Case{id: "ut-new/issue247-literal-new-meta-qualified", verdict: "valid", params: map[string]any{
		"op": "sequence", "searchdirs": []any{"../protocol-v2/schemas"}, "modules": []any{map[string]any{"name": "pv2-mdapi"}},
		"context_options": []any{"no_yanglibrary"}, "steps": []any{
			map[string]any{"do": "edit", "insert_inner": map[string]any{"module": "pv2-mdapi", "name": "c"}},
			map[string]any{"do": "edit", "new_meta": map[string]any{"node": "/pv2-mdapi:c", "name": "pv2-mdapi:num", "value": "2"}},
		},
	}})
	return cases
}

func TestIssue247LiteralConstructorNames(t *testing.T) {
	base := filepath.Join("corpus", "ut-new")
	for _, tc := range issue247LiteralNameCases() {
		t.Run(tc.id, func(t *testing.T) {
			got := issue247Run(t, base, tc)
			if got.Verdict() != tc.verdict {
				t.Fatalf("verdict = %q, want %q: %#v", got.Verdict(), tc.verdict, got)
			}
			steps := got["steps"].([]any)
			last := steps[len(steps)-1].(map[string]any)
			wantRC := "LY_ENOTFOUND"
			if tc.verdict == "valid" {
				wantRC = "LY_SUCCESS"
			}
			if rc := last["rc"].(map[string]any)["name"]; rc != wantRC {
				t.Fatalf("rc = %v, want %s: %#v", rc, wantRC, got)
			}
			if tc.verdict == "invalid" {
				ds := got.Diagnostics()
				if len(ds) != 1 || ds[0]["code"].(map[string]any)["name"] != "LY_EINVAL" {
					t.Fatalf("diagnostics = %#v", ds)
				}
				requestSteps := tc.params["steps"].([]any)
				edit := requestSteps[len(requestSteps)-1].(map[string]any)
				for kind, label := range map[string]string{"insert_term": "Term node", "insert_inner": "Inner node (container, notif, RPC, or action)", "insert_list": "List node", "insert_list2": "List node"} {
					if o, ok := edit[kind].(map[string]any); ok {
						if want := fmt.Sprintf(`%s "%s" not found.`, label, o["name"]); ds[0]["msg"] != want {
							t.Fatalf("diagnostic = %v, want %s", ds[0]["msg"], want)
						}
					}
				}
				before := steps[len(steps)-2].(map[string]any)["typed"]
				if !reflect.DeepEqual(before, last["typed"]) {
					t.Fatalf("rejected constructor mutated the tree: before=%#v after=%#v", before, last["typed"])
				}
			}
		})
	}
}

// main's adapter performed this qualification only for an explicit module. Count allocations
// over repeated names in descending order: normal implicit/explicit names must stay unchanged;
// preserving a literal colon may add only the one bounded string join per request.
func TestIssue247ConstructorNameAllocations(t *testing.T) {
	mainName := func(o map[string]any) string {
		name := str(o, "name", "")
		if mod := str(o, "module", ""); mod != "" {
			name = mod + ":" + name
		}
		return name
	}
	for _, n := range []int{64, 256} {
		for _, mode := range []string{"implicit", "explicit", "literal"} {
			t.Run(fmt.Sprintf("%s/%d", mode, n), func(t *testing.T) {
				requests := make([]map[string]any, n)
				for i := range requests {
					name := fmt.Sprintf("l%d", i%4) // duplicate-heavy requests
					if mode == "literal" {
						name = "a:" + name
					}
					requests[i] = map[string]any{"name": name}
					if mode == "explicit" {
						requests[i]["module"] = "a"
					}
				}
				var result string
				measure := func(adapt func(map[string]any) string) float64 {
					return testing.AllocsPerRun(10, func() {
						for i := len(requests) - 1; i >= 0; i-- {
							result = adapt(requests[i])
						}
					})
				}
				baseline, head := measure(mainName), measure(constructorName)
				t.Logf("main=%.0f head=%.0f allocations for %d requests (%s)", baseline, head, n, mode)
				allowed := baseline
				if head > allowed || result == "" {
					t.Fatalf("main=%.0f head=%.0f allowed=%.0f result=%q", baseline, head, allowed, result)
				}
			})
		}
	}
}
