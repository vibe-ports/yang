// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

// These live request cases deliberately have no golden files. Compare complete
// responses, including implicit diffs, typed flags, retained trees and diagnostics.
// Native sources: validation.c:2043-2075,2197-2216,2313-2340;
// tree_data_common.c:538-562,1177-1204 (libyang v5.8.6).
func TestSplitValidationOracle(t *testing.T) {
	oracle, _ := requestOracle(t)
	base, err := filepath.Abs("corpus/protocol-v2")
	if err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, p map[string]any) {
		t.Helper()
		got, err := (Yang{}).Run(Request{BaseDir: base, Params: p})
		if err != nil {
			t.Fatal(err)
		}
		want, err := inventoryNativeResponse(oracle, base, p)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(requestOutcome(want), requestOutcome(got)) {
			t.Fatalf("live diagnostic mismatch:\nengine: %v\noracle: %v", requestOutcome(got), requestOutcome(want))
		}
		if status, detail := classify(Fixture{}, want, got); status != Agree {
			t.Fatalf("live oracle mismatch: %s", detail)
		}
	}
	request := func(steps ...any) map[string]any {
		return map[string]any{"op": "sequence", "searchdirs": []any{"schemas", "../ietf"},
			"context_options": []any{"no_yanglibrary"}, "modules": []any{map[string]any{"name": "split-validation"}, map[string]any{"name": "vm-b"}, map[string]any{"name": "ietf-netconf-with-defaults"}}, "steps": steps}
	}
	parse := func(body string, only bool) map[string]any {
		return map[string]any{"do": "parse", "data_type": "data", "parse_only": only,
			"data": `{"split-validation:top":` + body + `}`, "validate_options": []any{"not_final"}}
	}
	validate := func(module string, final bool, flags ...any) map[string]any {
		st := map[string]any{"do": "validate", "data_type": "data", "final_only": final}
		if len(flags) > 0 {
			st["validate_options"] = flags
		}
		if module != "" {
			st["module"] = module
		}
		return st
	}
	for name, body := range map[string]string{
		"mandatory":        `{}`,
		"must":             `{"required":"yes","values":["a","b"],"must":"bad"}`,
		"min":              `{"required":"yes"}`,
		"max":              `{"required":"yes","values":["a","b","c","d"]}`,
		"unique":           `{"required":"yes","values":["a","b"],"item":[{"k":"a","v":"same"},{"k":"b","v":"same"}]}`,
		"obsolete":         `{"required":"yes","values":["a","b"],"obsolete":"old"}`,
		"leafref":          `{"ref":"missing"}`,
		"iid":              `{"iid":"/split-validation:top/target"}`,
		"union":            `{"union-ref":"missing"}`,
		"when":             `{"conditional":"bad"}`,
		"duplicate":        `{"item":[{"k":"a"},{"k":"a"}]}`,
		"references-valid": `{"required":"yes","values":["a","b"],"target":"on","ref":"on","iid":"/split-validation:top/target","union-ref":"on","conditional":"yes"}`,
		"state":            `{"required":"yes","values":["a","b"],"state":"state"}`,
		"opaque":           `{"required":"yes","values":["a","b"],"unknown":"opaque"}`,
	} {
		for _, phase := range []string{"parse", "all", "module"} {
			t.Run(name+"/"+phase, func(t *testing.T) {
				st := parse(body, phase != "parse")
				st["unknown"] = "opaque"
				flags := []any{"not_final"}
				var finalFlags []any
				if name == "state" {
					flags = append(flags, "no_state")
					finalFlags = []any{"no_state"}
				}
				st["validate_options"] = flags
				steps := []any{st}
				if phase != "parse" {
					module := ""
					if phase == "module" {
						module = "split-validation"
					}
					steps = append(steps, validate(module, false, flags...))
				}
				steps = append(steps, validate("split-validation", true, finalFlags...), map[string]any{"do": "dump"})
				check(t, request(steps...))
			})
		}
	}
	t.Run("xml", func(t *testing.T) {
		st := parse(`{}`, false)
		st["format"], st["data"] = "xml", `<top xmlns="urn:split-validation"><must>bad</must></top>`
		check(t, request(st, validate("split-validation", true)))
	})
	for _, flags := range [][]any{{"present"}, {"not_final"}, {"present", "not_final"}} {
		t.Run(fmt.Sprint("final-arguments-", flags), func(t *testing.T) {
			check(t, request(validate("split-validation", true, flags...)))
		})
	}
	t.Run("final-flags-and-scope", func(t *testing.T) {
		check(t, request(map[string]any{"do": "parse", "data_type": "data", "parse_only": true,
			"data": `{"split-validation:flags":{"nested":{"d":"implicit"}},"vm-b:top":{}}`},
			map[string]any{"do": "diff_parse", "data_type": "data", "data": `{"split-validation:flags":{"@":{"yang:operation":"none"},"nested":{"d":"implicit","@d":{"ietf-netconf-with-defaults:default":true,"yang:orig-default":false}}}}`},
			map[string]any{"do": "diff_apply"},
			validate("split-validation", true), validate("vm-b", true)))
	})
	t.Run("final-flags-retained-on-error", func(t *testing.T) {
		check(t, request(map[string]any{"do": "parse", "data_type": "data", "parse_only": true,
			"data": `{"split-validation:flags":{"nested":{"d":"implicit"}},"split-validation:top":{}}`},
			map[string]any{"do": "diff_parse", "data_type": "data", "data": `{"split-validation:flags":{"@":{"yang:operation":"none"},"nested":{"d":"implicit","@d":{"ietf-netconf-with-defaults:default":true,"yang:orig-default":false}}}}`},
			map[string]any{"do": "diff_apply"}, validate("split-validation", true)))
	})
	t.Run("final-no-reference-checks-or-implicit", func(t *testing.T) {
		check(t, request(parse(`{"required":"yes","values":["a","b"],"ref":"missing"}`, true), validate("split-validation", true)))
	})
	for _, final := range []bool{false, true} {
		t.Run(fmt.Sprint("stored-value-", final), func(t *testing.T) {
			st := parse(`{"required":"yes","values":["a","b"],"number":999}`, true)
			st["parse_options"] = []any{"store_only"}
			flag := "not_final"
			if final {
				flag = "multi_error"
			}
			check(t, request(st, validate("split-validation", final, flag)))
		})
	}
	t.Run("no-defaults", func(t *testing.T) {
		check(t, request(parse(`{"required":"yes","values":["a","b"]}`, true), validate("split-validation", false, "not_final", "no_defaults"), validate("split-validation", true)))
	})
}
