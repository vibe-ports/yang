// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type requestInteraction struct {
	ID         string         `json:"id"`
	Request    map[string]any `json:"request"`
	Verdict    string
	Gap        string
	ErrorClass string `json:"request_error_class"`
}

func missingAnyDirectTargetRequest(which string, skipped bool) map[string]any {
	steps := []any{}
	if skipped {
		steps = append(steps, map[string]any{"do": "edit", "delete": "/missing:x"})
	}
	steps = append(steps, map[string]any{"do": "any_direct", "case": which})
	return map[string]any{
		"op": "sequence", "searchdirs": []any{"schemas"},
		"modules":         []any{map[string]any{"name": "ac-r6"}},
		"context_options": []any{"no_yanglibrary"}, "steps": steps,
	}
}

func TestAnyDirectMissingTargetInteraction(t *testing.T) {
	for _, which := range []string{"equal_repeated_collisions", "dup_payload_parent_context", "trim_payload_links"} {
		for _, skipped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/skipped=%v", which, skipped), func(t *testing.T) {
				p := missingAnyDirectTargetRequest(which, skipped)
				got, err := inventoryRun(Request{BaseDir: "corpus/any-core", Params: p})
				if skipped {
					if err != nil || got.Verdict() != "invalid" || got["steps"].([]any)[1].(map[string]any)["skipped"] != true {
						t.Fatalf("skipped step: %v, %v", got, err)
					}
					return
				}
				want := "any_direct " + which + " needs target (a request-error)"
				if which == "equal_repeated_collisions" {
					want = "equal_repeated_collisions needs target (a request-error)"
				}
				if err == nil || err.Error() != want || errors.Is(err, ErrUnsupported) {
					t.Fatalf("execution error = %v, want %q", err, want)
				}
			})
		}
	}
}

func TestAnyDirectMissingTargetDifferential(t *testing.T) {
	oracle, _ := requestOracle(t)
	for _, which := range []string{"equal_repeated_collisions", "dup_payload_parent_context", "trim_payload_links"} {
		for _, skipped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/skipped=%v", which, skipped), func(t *testing.T) {
				p := missingAnyDirectTargetRequest(which, skipped)
				want, nativeErr := inventoryNativeResponse(oracle, "corpus/any-core", p)
				got, err := inventoryRun(Request{BaseDir: "corpus/any-core", Params: p})
				category, class := inventoryCaseClassification(p, want, nativeErr, got, err, "")
				if category != "MATCH" {
					t.Fatalf("%s/%s: oracle=%v (%v), engine=%v (%v)", category, class, want, nativeErr, got, err)
				}
			})
		}
	}
}

func TestUnsupportedStepSelectorOrder(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		step       map[string]any
	}{
		{"change_term", "change_term node /pv2:c/mode not found", map[string]any{"do": "change_term", "node": "/pv2:c/mode", "value": "on", "canon": true}},
		{"dup", "dup node /pv2:c not found", map[string]any{"do": "dup", "node": "/pv2:c", "options": []any{"no_lyds"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := map[string]any{"op": "sequence", "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
				"context_options": []any{"no_yanglibrary"}, "steps": []any{tc.step}}
			_, err := inventoryRun(Request{BaseDir: "corpus/protocol-v2", Params: p})
			if err == nil || err.Error() != tc.want+" (a request-error)" || errors.Is(err, ErrUnsupported) {
				t.Fatalf("selector error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestUnsupportedStepSelectorOrderDifferential(t *testing.T) {
	oracle, _ := requestOracle(t)
	for _, step := range []map[string]any{
		{"do": "change_term", "node": "/pv2:c/mode", "value": "on", "canon": true},
		{"do": "dup", "node": "/pv2:c", "options": []any{"no_lyds"}},
	} {
		p := map[string]any{"op": "sequence", "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
			"context_options": []any{"no_yanglibrary"}, "steps": []any{step}}
		t.Run(str(step, "do", ""), func(t *testing.T) {
			want, nativeErr := inventoryNativeResponse(oracle, "corpus/protocol-v2", p)
			got, err := inventoryRun(Request{BaseDir: "corpus/protocol-v2", Params: p})
			category, class := inventoryCaseClassification(p, want, nativeErr, got, err, "")
			if category != "MATCH" {
				t.Fatalf("%s/%s: oracle=%v (%v), engine=%v (%v)", category, class, want, nativeErr, got, err)
			}
		})
	}
}

func shortUnsupportedInsertListRequest() map[string]any {
	return map[string]any{"op": "sequence", "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
		"context_options": []any{"no_yanglibrary"}, "steps": []any{
			map[string]any{"do": "parse", "data": `{"pv2:c":{}}`, "parse_only": true},
			map[string]any{"do": "edit", "insert_list": map[string]any{"parent": "/pv2:c", "name": "l", "keys": []any{}, "options": []any{"canon"}}},
		}}
}

func TestUnsupportedInsertListKeyCount(t *testing.T) {
	p := shortUnsupportedInsertListRequest()
	_, err := inventoryRun(Request{BaseDir: "corpus/protocol-v2", Params: p})
	if err == nil || err.Error() != "insert_list l has fewer keys than the list (a request-error)" || errors.Is(err, ErrUnsupported) {
		t.Fatalf("insert_list error = %v", err)
	}
}

func TestUnsupportedInsertListKeyCountDifferential(t *testing.T) {
	oracle, _ := requestOracle(t)
	p := shortUnsupportedInsertListRequest()
	want, nativeErr := inventoryNativeResponse(oracle, "corpus/protocol-v2", p)
	got, err := inventoryRun(Request{BaseDir: "corpus/protocol-v2", Params: p})
	category, class := inventoryCaseClassification(p, want, nativeErr, got, err, "")
	if category != "MATCH" {
		t.Fatalf("%s/%s: oracle=%v (%v), engine=%v (%v)", category, class, want, nativeErr, got, err)
	}
}

// Request-errors cannot be corpus goldens (cmd/golden rejects them). These
// request-layer fixtures retain their source/license and compare against the
// native runner separately, using the same cache and A/B/C classifier.
func requestInteractions(t *testing.T) []requestInteraction {
	t.Helper()
	paths, err := filepath.Glob("testdata/request-interactions/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no request interaction fixtures")
	}
	var out []requestInteraction
	for _, path := range paths {
		b, err := os.ReadFile(path) //nolint:gosec // Paths come from the fixed testdata glob, never request input.
		if err != nil {
			t.Fatal(err)
		}
		var fixture requestInteraction
		if err := json.Unmarshal(b, &fixture); err != nil {
			t.Fatal(err)
		}
		out = append(out, fixture)
	}
	for _, tc := range []struct{ name, verdict, gap string }{
		{"insert-output-fewer-keys", "valid", ""},
		{"prelude-xpath-bogus-flag", "schema-error", ""},
		{"reply-non-operation-parent", "", "reply-non-operation-parent"},
	} {
		b, err := os.ReadFile("corpus/manifest.d/protocol-v2/" + tc.name + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		var fragment struct {
			Fixtures []Fixture `yaml:"fixtures"`
		}
		if err := yaml.Unmarshal(b, &fragment); err != nil {
			t.Fatal(err)
		}
		if len(fragment.Fixtures) != 1 {
			t.Fatal("expected one interaction fixture")
		}
		f := fragment.Fixtures[0]
		f.Request["base_dir"] = "."
		out = append(out, requestInteraction{ID: f.ID, Request: f.Request, Verdict: tc.verdict, Gap: tc.gap})
	}
	return out
}

func TestRequestInteractions(t *testing.T) {
	for _, fixture := range requestInteractions(t) {
		t.Run(fixture.ID, func(t *testing.T) {
			got, err := inventoryRun(Request{BaseDir: "corpus/protocol-v2", Params: fixture.Request})
			if fixture.Gap != "" {
				if class := inventoryGap(err); class != fixture.Gap {
					t.Fatalf("gap %q (%v), want %q", class, err, fixture.Gap)
				}
				return
			}
			if fixture.Verdict != "" {
				if err != nil || got.Verdict() != fixture.Verdict {
					t.Fatalf("verdict %q (%v), want %q", got.Verdict(), err, fixture.Verdict)
				}
				switch fixture.ID {
				case "protocol-v2/diff-input-output-same-name":
					steps := got["steps"].([]any)
					diff := steps[1].(map[string]any)["diff_typed"].([]any)
					if len(diff) != 3 || diff[1].(map[string]any)["schema"] != "/pv2-sort-anchor:r/output/pre" ||
						diff[2].(map[string]any)["schema"] != "/pv2-sort-anchor:r/input/pre" {
						t.Fatalf("input/output diff schema identity: %v", diff)
					}
					applied := steps[2].(map[string]any)["typed"].([]any)
					if len(applied) != 2 || applied[1].(map[string]any)["schema"] != "/pv2-sort-anchor:r/output/pre" {
						t.Fatalf("applied output schema identity: %v", applied)
					}
				case "protocol-v2/xpath-mixed-context-list-hash":
					steps := got["steps"].([]any)
					result := steps[3].(map[string]any)["result"].(map[string]any)
					if nodes := result["nodes"].([]any); len(nodes) != 2 ||
						!slices.Contains(nodes, any("/pv2-ctx-list:l[k='a']/v")) ||
						!slices.Contains(nodes, any("/pv2-ctx-list:l[k='b']/v")) {
						t.Fatalf("mixed-context XPath nodes: %v", nodes)
					}
				}
				return
			}
			if err == nil || !strings.HasSuffix(err.Error(), " (a request-error)") {
				t.Fatalf("want request-error, got %v", err)
			}
			if got := inventoryErrorClass(strings.TrimSuffix(err.Error(), " (a request-error)")); got != fixture.ErrorClass {
				t.Fatalf("request-error class %q, want %q", got, fixture.ErrorClass)
			}
		})
	}
}

// Deliberately outside Differential$: the existing single-field response cache
// predates these requests. Capture/replay with this test's explicit name.
func TestRequestInteractionsOracle(t *testing.T) {
	oracle, base := requestOracle(t)
	contexts := newInventoryContextCache()
	for _, fixture := range requestInteractions(t) {
		t.Run(fixture.ID, func(t *testing.T) {
			compareRequestOutcome(t, oracle, contexts, base, fixture.Request)
			if fixture.ID == "protocol-v2/diff-apply-cross-context-child-create" ||
				fixture.ID == "protocol-v2/diff-apply-cross-context-userordered-create" ||
				fixture.ID == "protocol-v2/diff-apply-cross-context-userordered-anchor" ||
				fixture.ID == "protocol-v2/diff-merge-cross-context-parent" ||
				fixture.ID == "protocol-v2/diff-merge-cross-context-matching-child" ||
				fixture.ID == "protocol-v2/diff-merge-cross-context-metadata-twice" ||
				fixture.ID == "protocol-v2/xpath-detached-root-axes" ||
				fixture.ID == "protocol-v2/xpath-detached-state-position" ||
				fixture.ID == "protocol-v2/diff-input-output-same-name" ||
				fixture.ID == "protocol-v2/xpath-mixed-context-list-hash" {
				want, err := inventoryNativeResponse(oracle, base, fixture.Request)
				if err != nil {
					t.Fatal(err)
				}
				got, err := inventoryRunWithContextCache(Request{BaseDir: base, Params: fixture.Request}, contexts)
				if err != nil {
					t.Fatal(err)
				}
				field := "typed"
				step := 3
				switch fixture.ID {
				case "protocol-v2/diff-merge-cross-context-parent":
					field, step = "diff_typed", 1
				case "protocol-v2/diff-merge-cross-context-matching-child":
					field, step = "diff_typed", 1
				case "protocol-v2/diff-merge-cross-context-metadata-twice":
					field, step = "diff_typed", 2
				case "protocol-v2/xpath-detached-root-axes":
					field, step = "detached_path", 9
				case "protocol-v2/xpath-detached-state-position":
					field, step = "detached_path", 4
				case "protocol-v2/diff-input-output-same-name":
					field, step = "diff_typed", 1
				case "protocol-v2/xpath-mixed-context-list-hash":
					field, step = "result", 3
				}
				wantValue := want["steps"].([]any)[step].(map[string]any)[field]
				gotValue := got["steps"].([]any)[step].(map[string]any)[field]
				if field == "typed" || field == "diff_typed" {
					project := func(v any) any {
						var nodes []any
						for _, raw := range v.([]any) {
							n := raw.(map[string]any)
							projected := map[string]any{"path": n["path"], "value": n["value"]}
							if fixture.ID == "protocol-v2/diff-input-output-same-name" {
								projected["schema"] = n["schema"]
								projected["meta"] = n["meta"]
							}
							if fixture.ID == "protocol-v2/diff-merge-cross-context-metadata-twice" {
								projected["meta"] = n["meta"]
							}
							nodes = append(nodes, projected)
						}
						return nodes
					}
					wantValue, gotValue = project(wantValue), project(gotValue)
				}
				wb, _ := json.Marshal(wantValue)
				gb, _ := json.Marshal(gotValue)
				if string(wb) != string(gb) {
					t.Errorf("step %d %s: oracle %s, engine %s", step, field, wb, gb)
				}
			}
		})
	}
}
