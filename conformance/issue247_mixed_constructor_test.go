// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"fmt"
	"testing"
)

// Native provenance: libyang v5.8.6 tree_data_new.c, _lyd_new_term,
// lyd_new_inner, lyd_new_list3 and lyd_new_list2. step_edit collects the first
// root's context; these constructors log on their parent's context instead.
func issue247MixedConstructorRequest(t *testing.T, kind string, literal, reverse bool) (string, map[string]any) {
	t.Helper()
	base, p := diagnosticOwnershipSetup(t)
	first, parent := `{"ac:s":"foreign"}`, `{"ac:parent":{}}`
	if reverse {
		// Schema ordering puts parent first: its own context is collected. This
		// control catches accidental suppression when the operand order changes.
		first, parent = parent, first
	}
	o := map[string]any{"parent": "/ac:parent", "name": "l"}
	switch kind {
	case "insert_term":
		o["value"] = "x"
	case "insert_list":
		o["keys"] = []any{"x"}
	case "insert_list2":
		o["keys"] = "[k='x']"
	}
	if literal {
		o["name"] = "ac:l"
		if kind == "insert_list2" {
			o["keys"] = ""
		}
	} else {
		o["module"] = "ac"
	}
	p["op"] = "sequence"
	p["steps"] = []any{
		map[string]any{"do": "diff", "format": "json", "data_type": "data", "target": diagnosticOwnershipTarget(p), "data": first},
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true, "data": parent},
		map[string]any{"do": "diff_apply"},
		map[string]any{"do": "edit", kind: o},
	}
	return base, p
}

func TestIssue247MixedConstructorContexts(t *testing.T) {
	for _, kind := range []string{"insert_term", "insert_inner", "insert_list", "insert_list2"} {
		for _, literal := range []bool{false, true} {
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/literal=%t/reverse=%t", kind, literal, reverse), func(t *testing.T) {
					base, p := issue247MixedConstructorRequest(t, kind, literal, reverse)
					got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
					if err != nil {
						t.Fatal(err)
					}
					wantRC := "LY_EINVAL"
					wantDiags, wantVerdict := 0, "invalid"
					if literal {
						wantRC = "LY_ENOTFOUND"
					}
					if reverse {
						wantRC, wantDiags = "LY_ENOTFOUND", 1
						if !literal && (kind == "insert_list" || kind == "insert_list2") {
							wantRC, wantDiags, wantVerdict = "LY_SUCCESS", 0, "valid"
						}
					}
					last := got["steps"].([]any)[3].(map[string]any)
					if got.Verdict() != wantVerdict || wantVerdict == "invalid" && fmt.Sprint(got["failed_step"]) != "3" ||
						last["rc"].(map[string]any)["name"] != wantRC || len(last["diagnostics"].([]any)) != wantDiags {
						t.Fatalf("want %s/%s with %d diagnostics; got %#v", wantVerdict, wantRC, wantDiags, last)
					}
				})
			}
		}
	}
}
