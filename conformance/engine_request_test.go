// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"os"
	"strings"
	"testing"
)

func TestRequestTables(t *testing.T) {
	tables := map[string][]requestRule{
		"dparams_of": dparamsRules, "data prelude": dataPreludeRules, "data init": dataInitRules, "op_data": dataRequestRules,
		"op_diff": diffRequestRules, "op_sequence": sequenceRequestRules, "check_step entry": stepEntryRules,
	}
	for kind := range seqKeys {
		rows, exists := stepRequestRules[kind]
		if !exists || len(rows) == 0 {
			t.Errorf("no request table for step %s", kind)
		}
		tables[kind] = rows
	}
	for kind, rows := range stepExecutionRules {
		tables["execute "+kind] = rows
	}
	source, err := os.ReadFile("oracle/lyoracle.c")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(source), "\n")
	total := 0
	for name, rows := range tables {
		for i, row := range rows {
			total++
			if row.check == nil || row.line < 1 || row.line > len(lines) || strings.TrimSpace(lines[row.line-1]) == "" {
				t.Errorf("%s row %d lacks a check or a source line: %+v", name, i, row)
			}
		}
	}
	t.Logf("%d tables, %d rows with lyoracle.c line citations", len(tables), total)
}

func TestRequestInputSemantics(t *testing.T) {
	for _, tc := range []struct {
		name         string
		fields       map[string]any
		present, bad bool
	}{
		{"absent", map[string]any{}, false, false},
		{"null", map[string]any{"data": nil, "data_file": nil}, false, false},
		{"empty", map[string]any{"data": ""}, true, false},
		{"false", map[string]any{"data": false}, false, true},
		{"true", map[string]any{"data": true}, false, true},
		{"file false", map[string]any{"data_file": false}, false, true},
		{"both empty", map[string]any{"data": "", "data_file": ""}, false, true},
		{"missing file", map[string]any{"data_file": "request-test-missing.json"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, present, err := requestInput(Request{BaseDir: t.TempDir()}, tc.fields, "data")
			if (err != nil) != tc.bad || present != tc.present {
				t.Fatalf("present=%v, error=%v", present, err)
			}
			if err != nil && !strings.HasSuffix(err.Error(), "(a request-error)") {
				t.Fatal(err)
			}
		})
	}
}

func TestRequestChecksBeforeSkippedSteps(t *testing.T) {
	for _, st := range []map[string]any{
		{"do": "validate", "final_only": nil},
		{"do": "parse", "data": "{}", "data_file": "missing.json"},
		{"do": "diff", "keep_input": false},
		{"do": "parse", "data_type": "rpc", "data": "{}", "parse_only": "true"},
	} {
		r := Request{BaseDir: "corpus/protocol-v2", Params: map[string]any{
			"op": "sequence", "context_options": []any{"no_yanglibrary"},
			"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
			"steps": []any{map[string]any{"do": "edit", "delete": "/pv2:c/nope"}, st},
		}}
		_, err := (Yang{}).Run(r)
		if err == nil || !strings.HasSuffix(err.Error(), "(a request-error)") {
			t.Errorf("%v: %v", st, err)
		}
	}
}

func TestSkippedStepNeedsNoCapability(t *testing.T) {
	for _, st := range []map[string]any{
		{"do": "validate", "module": "pv2", "final_only": true},
		{"do": "change_term", "node": "/pv2:c/mode", "value": "on", "canon": true},
		{"do": "dup", "node": "/pv2:c", "options": []any{"no_lyds"}},
		{"do": "edit", "insert_opaq": map[string]any{"name": "x", "module": "pv2"}},
	} {
		r := Request{BaseDir: "corpus/protocol-v2", Params: map[string]any{
			"op": "sequence", "context_options": []any{"no_yanglibrary"},
			"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
			"steps": []any{map[string]any{"do": "edit", "delete": "/missing:x"}, st},
		}}
		resp, err := (Yang{}).Run(r)
		if err != nil {
			t.Errorf("%v: %v", st, err)
			continue
		}
		steps := resp["steps"].([]any)
		if resp["verdict"] != "invalid" || resp["rc"].(map[string]any)["name"] != "LY_ENOTFOUND" ||
			steps[0].(map[string]any)["rc"].(map[string]any)["name"] != "LY_ENOTFOUND" || steps[1].(map[string]any)["skipped"] != true {
			t.Errorf("%v: %v", st, resp)
		}
	}
}

func TestValidateModuleMissingAfterDup(t *testing.T) {
	r := Request{BaseDir: "corpus/protocol-v2", Params: map[string]any{
		"op": "sequence", "context_options": []any{"no_yanglibrary"},
		"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}, map[string]any{"name": "pv2-dummy"}},
		"steps": []any{
			map[string]any{"do": "parse", "data_type": "data", "parse_only": true, "data": `{"pv2:c":{"mode":"on"}}`},
			map[string]any{"do": "dup", "node": "/pv2:c", "options": []any{"recursive"}, "target": map[string]any{
				"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}}, "context_options": []any{"no_yanglibrary"},
			}},
			map[string]any{"do": "validate", "module": "pv2-dummy"},
		},
	}}
	resp, err := (Yang{}).Run(r)
	if err != nil {
		t.Fatal(err)
	}
	step := resp["steps"].([]any)[2].(map[string]any)
	if step["rc"].(map[string]any)["name"] != "LY_EINVAL" || len(step["diagnostics"].([]any)) != 0 {
		t.Fatalf("validate after dup: %v", step)
	}
}

// Multiple invalid fields exercise the order of the C calls, not merely the
// final request-error class of each individual field.
func TestRequestCheckOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    map[string]any
		rows []requestRule
		want string
	}{
		{"data before rpc", map[string]any{"rpc_file": "missing.json"}, dataRequestRules, "missing data"},
		{"rpc input before parse_one", map[string]any{"data": "{}", "rpc_file": "missing.json", "rpc_parent": ""}, dataRequestRules, "cannot read file"},
		{"parse rpc before data", map[string]any{"do": "parse", "rpc": "", "data": false}, stepRequestRules["parse"], "rpc only with data_type reply"},
		{"diff rpc before parse_only", map[string]any{"do": "diff", "data_type": "rpc", "rpc": false}, stepRequestRules["diff"], `field "rpc" must be a string`},
		{"validate module before dparams", map[string]any{"module": false, "data_type": "x"}, stepRequestRules["validate"], `field "module" must be a string`},
		{"validate dparams before final_only", map[string]any{"data_type": "x", "final_only": nil}, stepRequestRules["validate"], "unknown data_type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkRequest(Request{BaseDir: t.TempDir()}, nil, tc.p, execUnknown, tc.rows)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.HasSuffix(err.Error(), "(a request-error)") {
				t.Fatalf("error = %v; want %q request-error", err, tc.want)
			}
		})
	}
}

// These requests also have independent oracle fixtures in protocol-v2.
func TestOperationInputAndDiscardedFlags(t *testing.T) {
	for _, field := range []string{"operational", "parse_options", "validate_options"} {
		t.Run(field, func(t *testing.T) {
			p := map[string]any{"op": "data", "searchdirs": []any{"../ut-parser/a.1"}, "modules": []any{map[string]any{"name": "a"}}, "context_options": []any{"no_yanglibrary"}}
			if field == "operational" {
				p["data_type"], p["data"], p[field] = "rpc", `{"a:r1":{"l1":"x"}}`, nil
			} else {
				flag := "when_true"
				if field == "validate_options" {
					flag = "not_final"
				}
				p["op"], p["steps"] = "sequence", []any{map[string]any{"do": "parse", "data_type": "rpc", "parse_only": true, "data": `{"a:r1":{"l1":"x"}}`, field: []any{flag}}}
			}
			resp, err := (Yang{}).Run(Request{BaseDir: "corpus/protocol-v2", Params: p})
			if err != nil || resp["verdict"] != "valid" {
				t.Fatalf("response=%v error=%v", resp, err)
			}
		})
	}
}
