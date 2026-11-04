// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"fmt"
	"os"
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
		for _, kind := range []string{"when-leafref", "when", "leafref", "must", "payload", "ordinary", "prefix", "nested-passes", "final-rebuild"} {
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
				case "ordinary":
					var values []string
					for i := range 64 {
						values = append(values, strconv.Quote(strconv.Itoa(64-i)))
					}
					body = `"pv2-when-audit:ordinary":{"value":[` + strings.Join(values, ",") + `]}`
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
				if kind == "payload" || kind == "ordinary" || kind == "prefix" || kind == "nested-passes" || kind == "final-rebuild" {
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

// Reproduce data/TestWhenDeadRootHidesNoForeignRoot with two independently compiled
// contexts. Dumps and typed nodes prove retention; full response comparison
// includes ordered diagnostics and the validation deletion diff.
func TestWhenMixedContextRootLiveOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	for _, module := range []bool{false, true} {
		t.Run(fmt.Sprint("module=", module), func(t *testing.T) {
			base := t.TempDir()
			for _, dir := range []string{"dead", "live"} {
				path := filepath.Join(base, dir)
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				when := ""
				if dir == "dead" {
					when = `when "false()";`
				}
				source := `module m { yang-version 1.1; namespace "urn:m"; prefix m;
     leaf guard { type string; when "/m:x"; } leaf x { type string; ` + when + ` } }`
				if err := os.WriteFile(filepath.Join(path, "m.yang"), []byte(source), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			validate := map[string]any{"do": "validate", "data_type": "data", "validate_options": []any{"not_final"}}
			if module {
				validate["module"] = "m"
			} else {
				validate["validate_options"] = []any{"not_final", "present"}
			}
			p := map[string]any{"op": "sequence", "searchdirs": []any{"live"}, "context_options": []any{"no_yanglibrary"},
				"modules": []any{map[string]any{"name": "m"}}, "steps": []any{
					map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true, "data": `{"m:x":"live"}`},
					map[string]any{"do": "insert", "format": "json", "data_type": "data", "parse_only": true,
						"parse_options": []any{"when_true"}, "data": `{"m:guard":"g","m:x":"dead"}`,
						"target": map[string]any{"searchdirs": []any{"dead"}, "context_options": []any{"no_yanglibrary"}, "modules": []any{map[string]any{"name": "m"}}}},
					map[string]any{"do": "dump"}, validate, map[string]any{"do": "dump"},
				}}
			want, err := oracle(base, p)
			if err != nil {
				t.Fatal(err)
			}
			steps, ok := want["steps"].([]any)
			if !ok || len(steps) != 5 {
				t.Fatalf("native setup failed: %#v", want)
			}
			validated := steps[3].(map[string]any)
			if code, ok := validated["rc"].(map[string]any); !ok || code["name"] != "LY_SUCCESS" || validated["implicit_diff"] == nil {
				t.Fatalf("native deletion did not run: %#v", want)
			}
			if typed := validated["typed"].([]any); len(typed) != 2 {
				t.Fatalf("native retained %d nodes, want 2: %#v", len(typed), want)
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

// Successful native counterpart of the large retained-type and retained-link
// preflights: gone is one leafref, and all other queued values and links stay.
func TestWhenRetainedQueuesLiveOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	for _, subtree := range []bool{false, true} {
		t.Run(fmt.Sprint("subtree=", subtree), func(t *testing.T) {
			base := t.TempDir()
			source := `module m { yang-version 1.1; namespace "urn:m"; prefix m;
    leaf gate { type string; }
    leaf gone { type leafref { path "/m:target"; } when "../gate='on'"; }
    leaf-list target { type string; }
    leaf-list r { type leafref { path "/m:target"; } }
   }`
			var values []string
			for i := 64; i > 0; i-- {
				values = append(values, strconv.Quote(strconv.Itoa(i)))
			}
			array := `[` + strings.Join(values, ",") + `]`
			data := `{"m:gate":"on","m:gone":"1","m:target":` + array + `,"m:r":` + array + `}`
			expected := 129
			if subtree {
				source = `module m { yang-version 1.1; namespace "urn:m"; prefix m;
     leaf gate { type string; }
     container gone { presence p; when "../gate='on'";
      leaf a { type leafref { path "/m:target"; } }
      leaf b { type leafref { path "/m:target"; } }
     }
     leaf target { type string; }
     leaf-list r { config false; type leafref { path "/m:target"; } }
    }`
				data = `{"m:gate":"on","m:gone":{"a":"v","b":"v"},"m:target":"v","m:r":[` + strings.TrimSuffix(strings.Repeat(`"v",`, 64), ",") + `]}`
				expected = 66
			}
			if err := os.WriteFile(filepath.Join(base, "m.yang"), []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, linking := range []bool{false, true} {
				t.Run(fmt.Sprint("linking=", linking), func(t *testing.T) {
					options := []any{"no_yanglibrary"}
					if linking {
						options = append(options, "leafref_linking")
					}
					validate := map[string]any{"do": "validate", "module": "m", "data_type": "data", "validate_options": []any{"not_final"}}
					steps := []any{
						map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true, "parse_options": []any{"when_true"}, "data": data},
						validate,
						map[string]any{"do": "edit", "set": map[string]any{"path": "/m:gate", "value": "off", "update": true}},
						validate, map[string]any{"do": "dump"},
					}
					if linking {
						steps = append(steps, map[string]any{"do": "links"})
					}
					p := map[string]any{"op": "sequence", "searchdirs": []any{"."}, "context_options": options, "modules": []any{map[string]any{"name": "m"}}, "steps": steps}
					want, err := oracle(base, p)
					if err != nil {
						t.Fatal(err)
					}
					nativeSteps, ok := want["steps"].([]any)
					if !ok || len(nativeSteps) != len(steps) {
						t.Fatalf("native setup: %#v", want)
					}
					validated := nativeSteps[3].(map[string]any)
					if code, ok := validated["rc"].(map[string]any); !ok || code["name"] != "LY_SUCCESS" || validated["implicit_diff"] == nil || len(validated["typed"].([]any)) != expected {
						t.Fatalf("native deletion/retention did not run: %#v", want)
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
		})
	}
}
