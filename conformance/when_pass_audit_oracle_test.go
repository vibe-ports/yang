// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Native v5.8.6: lyd_validate_autodel_node_del, lyd_validate_unres_when,
// lyd_validate_unres and lyoracle's step_validate fallback on native success.
// Source: https://github.com/CESNET/libyang/tree/v5.8.6 (BSD-3-Clause).
// RFC7950 sections 7.21.5 (when), 9.9.3 (leafref), and 7.5.3 (must).
// Go-only cancellation and quota boundaries are tested in data; these live
// requests pin the successful deletion and mixed-error semantics without goldens.
func TestWhenPassAuditLiveOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	base := filepath.Join("corpus", "protocol-v2")
	for _, module := range []bool{false, true} {
		for _, kind := range []string{"when-leafref", "when", "leafref", "must", "payload", "prefix", "nested-passes", "final-rebuild"} {
			t.Run(fmt.Sprintf("module=%v/%s", module, kind), func(t *testing.T) {
				body := `"pv2-when-audit:x":["a","a","b"]`
				flags := []any{"not_final"}
				switch kind {
				case "when-leafref", "leafref":
					body += `,"pv2-when-audit:r":"missing"`
				case "must":
					body += `,"pv2-when-audit:must":"v"`
					flags = []any{}
				case "payload":
					var payload []string
					for i := range 64 {
						payload = append(payload, fmt.Sprintf(`"payload:n%d":"v"`, i))
					}
					body = `"pv2-when-audit:a":{` + strings.Join(payload, ",") + `}`
				case "prefix":
					var vals, deleted []string
					for i := range 64 {
						vals = append(vals, strconv.Itoa(i))
						deleted = append(deleted, strconv.Quote(strconv.Itoa(i)))
					}
					array := `[` + strings.Join(vals, ",") + `]`
					body = `"pv2-when-audit:c":{"q":` + array + `,"keyed":` + array + `},"pv2-when-audit:x":[` + strings.Join(deleted, ",") + `],"pv2-when-audit:gate":"on","pv2-when-audit:g":[{"k":"g","v":"on"}],"pv2-when-audit:f":` + array
				case "final-rebuild":
					body = `"pv2-when-audit:x":["a","b","c","d"]`
				case "nested-passes":
					body = `"pv2-when-audit:chain":{"d1":"d","d2":"d","d3":"d"}`
				}
				steps := []any{map[string]any{"do": "parse", "data_type": "data", "parse_only": true,
					"parse_options": []any{"when_true"}, "unknown": "opaque", "data": `{` + body + `}`}}
				if kind == "when-leafref" || kind == "when" {
					flags = append(flags, "multi_error")
					steps = append(steps, map[string]any{"do": "edit", "insert_term": map[string]any{
						"module": "pv2-when-audit", "name": "bad", "value": "bad"}})
				}
				if kind == "nested-passes" {
					for i := 1; i <= 3; i++ {
						steps = append(steps, map[string]any{"do": "edit", "insert_term": map[string]any{
							"module": "pv2-when-audit", "parent": "/pv2-when-audit:chain", "name": fmt.Sprintf("g%d", i), "value": "v"}})
					}
				}
				validate := map[string]any{"do": "validate", "data_type": "data"}
				if module {
					validate["module"] = "pv2-when-audit"
				} else {
					flags = append(flags, "present")
				}
				validate["validate_options"] = flags
				steps = append(steps, validate, map[string]any{"do": "dump"})
				p := map[string]any{"op": "sequence", "searchdirs": []any{"schemas"}, "context_options": []any{"no_yanglibrary"},
					"modules": []any{map[string]any{"name": "pv2-when-audit"}}, "steps": steps}
				want, err := oracle(base, p)
				if err != nil {
					t.Fatal(err)
				}
				// Reject a vacuous agreement on setup/parse failure: the native
				// validation step must run and return the intended operation code.
				nativeSteps, ok := want["steps"].([]any)
				if !ok || len(nativeSteps) != len(steps) {
					t.Fatalf("native setup did not reach validation: %#v", want)
				}
				validated := nativeSteps[len(nativeSteps)-2].(map[string]any)
				code, ok := validated["rc"].(map[string]any)
				expected := "LY_EVALID"
				if kind == "payload" || kind == "prefix" || kind == "nested-passes" || kind == "final-rebuild" {
					expected = "LY_SUCCESS"
				}
				if !ok || code["name"] != expected {
					t.Fatalf("native validation = %#v, want %s", validated, expected)
				}
				got, err := (Yang{}).Run(Request{BaseDir: base, Params: p})
				if err != nil {
					t.Fatal(err)
				}
				if diff := diffResponses(want, got); diff != "" {
					t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
				}
			})
		}
	}
}
