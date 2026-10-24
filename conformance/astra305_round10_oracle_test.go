// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestAstra305Round10OpaqueMixedSchemaParents is a live native-oracle fixture
// for lyd_find_sibling_first's initial schema-parent guard.
func TestAstra305Round10OpaqueMixedSchemaParents(t *testing.T) {
	base := filepath.Join("corpus", "any-core")
	params := map[string]any{
		"op":              "sequence",
		"searchdirs":      []any{"schemas"},
		"modules":         []any{map[string]any{"name": "ac-r6"}, map[string]any{"name": "ac-r10"}},
		"context_options": []any{"no_yanglibrary"},
		"steps": []any{map[string]any{
			"do": "any_direct", "case": "merge_opaque_mixed_schema_parents",
		}},
	}
	want, err := nativeRequestRunner(t)(base, params)
	if err != nil {
		t.Fatal(err)
	}
	steps, ok := want["steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("oracle response = %#v, want one step", want)
	}
	step, ok := steps[0].(map[string]any)
	if !ok {
		t.Fatalf("oracle step = %#v, want object", steps[0])
	}
	direct, ok := step["direct"].(map[string]any)
	if !ok {
		t.Fatalf("oracle direct result = %#v, want object", step["direct"])
	}
	if direct["first_child"] != "x" || direct["first_schema_parent"] != "a" ||
		direct["children"] != json.Number("3") || direct["list_instances"] != json.Number("2") {
		t.Fatalf("oracle direct result = %#v, want x under a followed by two list instances", direct)
	}
}

// TestAstra305Round10RepeatedDateCollisions pins libyang's successful boolean
// equality verdict beyond the Go port's charged-comparison allowance.
func TestAstra305Round10RepeatedDateCollisions(t *testing.T) {
	base := filepath.Join("corpus", "any-core")
	params := map[string]any{
		"op":              "sequence",
		"searchdirs":      []any{"schemas"},
		"modules":         []any{map[string]any{"name": "ac-r6"}, map[string]any{"name": "ac-r10"}},
		"context_options": []any{"no_yanglibrary"},
		"steps": []any{map[string]any{
			"do": "any_direct", "case": "equal_repeated_date_collisions",
		}},
	}
	want, err := nativeRequestRunner(t)(base, params)
	if err != nil {
		t.Fatal(err)
	}
	steps, ok := want["steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("oracle response = %#v, want one step", want)
	}
	step, ok := steps[0].(map[string]any)
	if !ok {
		t.Fatalf("oracle step = %#v, want object", steps[0])
	}
	direct, ok := step["direct"].(map[string]any)
	if !ok {
		t.Fatalf("oracle direct result = %#v, want object", step["direct"])
	}
	compare, ok := direct["compare_rc"].(map[string]any)
	if !ok || compare["name"] != "LY_SUCCESS" || direct["copies_per_value"] != json.Number("6000") {
		t.Fatalf("oracle direct result = %#v, want LY_SUCCESS for 6000 copies of each date", direct)
	}
}
