// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

// Live request fixtures for the scans that gained cancellation polling. Native
// validation.c:lyd_validate_final_r/lyd_validate_tree/lyd_validate_unique and
// tree_data_common.c:lyd_np_cont_dflt_set remain the uncanceled reference.
// The C API has no context cancellation; Go-only tests cover that contract.
func TestSplitValidationTraversalOracle(t *testing.T) {
	oracle, _ := requestOracle(t)
	base, err := filepath.Abs("corpus/protocol-v2")
	if err != nil {
		t.Fatal(err)
	}
	for _, order := range []string{"sorted", "reverse", "duplicates"} {
		for _, phase := range []string{"parse", "all", "module", "final"} {
			t.Run(order+"/"+phase, func(t *testing.T) {
				var values []any
				var entries []any
				for i := range 512 {
					key := i
					if order == "reverse" {
						key = 511 - i
					}
					v := fmt.Sprintf("%06d", key)
					if order == "duplicates" {
						v = "same"
					}
					values = append(values, v)
					entries = append(entries, map[string]any{"k": fmt.Sprintf("%06d", key), "v": v})
				}
				body, err := json.Marshal(map[string]any{
					"split-traversal:top": values,
					"split-traversal:nested": map[string]any{
						"values": values, "case-values": values, "defaults": []any{"explicit"}, "entry": entries,
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				steps := []any{map[string]any{"do": "parse", "data_type": "data", "parse_only": phase != "parse",
					"data": string(body), "validate_options": []any{"not_final"}}}
				if phase == "all" || phase == "module" {
					st := map[string]any{"do": "validate", "data_type": "data", "validate_options": []any{"not_final"}}
					if phase == "module" {
						st["module"] = "split-traversal"
					}
					steps = append(steps, st)
				}
				steps = append(steps, map[string]any{"do": "validate", "data_type": "data", "module": "split-traversal", "final_only": true}, map[string]any{"do": "dump"})
				p := map[string]any{"op": "sequence", "searchdirs": []any{"schemas"}, "context_options": []any{"no_yanglibrary"},
					"modules": []any{map[string]any{"name": "split-traversal"}}, "steps": steps}
				got, err := (Yang{}).Run(Request{ID: "protocol-v2/split-traversal-" + order + "-" + phase, BaseDir: base, Params: p})
				if err != nil {
					t.Fatal(err)
				}
				want, err := inventoryNativeResponse(oracle, base, p)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(requestOutcome(want), requestOutcome(got)) {
					t.Fatalf("diagnostics: engine %v, oracle %v", requestOutcome(got), requestOutcome(want))
				}
				if status, detail := classify(Fixture{}, want, got); status != Agree {
					t.Fatal(detail)
				}
			})
		}
	}
}
