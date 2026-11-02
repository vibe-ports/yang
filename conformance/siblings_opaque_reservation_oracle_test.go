// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The request protocol exposes opaque append, so construct both physical prefixes through
// opposite append orders. The package regression separately exercises inserting before an
// existing opaque, including the private physical placement and the legacy opaque rank.
// Native libyang has no Go work budget; compare its complete public response live, no goldens.
func TestSiblingOpaqueComparedPrefixLiveOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	base := t.TempDir()
	// State lists permit positional selectors for an incomplete copied key set.
	for dir, keys := range map[string]string{"source": "a", "target": `"a b"`} {
		if err := os.Mkdir(filepath.Join(base, dir), 0o700); err != nil {
			t.Fatal(err)
		}
		model := fmt.Sprintf(`module m { yang-version 1.1; namespace urn:m; prefix m;
		  container r { list l { config false; key %s;
		    leaf a { type string; } leaf b { type string; } } } }`, keys)
		if err := os.WriteFile(filepath.Join(base, dir, "m.yang"), []byte(model), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, longFirst := range []bool{false, true} {
		name := "short-prefix"
		order := []string{"old", "new"}
		if longFirst {
			name, order = "long-prefix", []string{"new", "old"}
		}
		t.Run(name, func(t *testing.T) {
			steps := []any{
				map[string]any{"do": "parse", "format": "json", "parse_only": true,
					"data": `{"m:r":{"l":[{"a":"x"},{"a":"x"},{"a":"x"},{"a":"x"}]}}`},
				map[string]any{"do": "dup", "node": "/m:r", "options": []any{"recursive"},
					"target": map[string]any{"searchdirs": []any{"target"},
						"modules": []any{map[string]any{"name": "m"}}, "context_options": []any{"no_yanglibrary"}}},
			}
			for _, child := range order {
				value := "x"
				if child == "new" {
					value = strings.Repeat("x", 4096)
				}
				steps = append(steps, map[string]any{"do": "edit", "insert_opaq": map[string]any{
					"parent": "/m:r/l[4]", "name": child, "value": value, "module": "m"}})
			}
			steps = append(steps, map[string]any{"do": "dump"})
			params := map[string]any{"op": "sequence", "searchdirs": []any{"source"},
				"modules": []any{map[string]any{"name": "m"}}, "context_options": []any{"no_yanglibrary"}, "steps": steps}
			want, err := oracle(base, params)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (Yang{}).Run(Request{ID: "sibling-opaque-prefix/" + name, BaseDir: base, Params: params})
			if err != nil {
				t.Fatal(err)
			}
			if want.Verdict() != "valid" {
				t.Fatalf("native request failed: %v", want)
			}
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s\nnative: %v\nGo: %v", diff, want, got)
			}
		})
	}
}
