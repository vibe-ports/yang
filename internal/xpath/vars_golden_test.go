// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"encoding/json"
	"os"
	"testing"
)

// TestVarsGoldens replays the oracle fixtures protocol-v2/xpath-vars-* (lyd_eval_xpath4 with
// lyxp_vars_set, context /pv2:c of data/xpath.json) against the evaluator: results, and the
// LY_ERR / LYVE code and message of errors. Where they are logged is data's part (TestFindXPath).
func TestVarsGoldens(t *testing.T) {
	vars := map[string]string{"abc": "1 + 2", "s": "'x'", "bad": "1 +", "und": "$nope", "lx": "'"}
	cases := map[string]string{ // fixture id suffix → expression (conformance/corpus/manifest.yaml)
		"value":                  "$abc * 2",
		"prefix-match":           "$ab",
		"in-predicate":           "ll[. = $s]",
		"undefined":              "$abcd",
		"skip-and":               "false() and $x",
		"skip-or-nested":         "true() or $und",
		"skip-ok":                "true() or $s",
		"empty-predicate":        "l[k = 'zz'][$x]",
		"value-reparse-error":    "$bad",
		"value-lex-error":        "$lx",
		"eval-error-location":    "/zz:c",
		"reparse-error-location": "l[",
		"lex-error-location":     "l['",
	}
	var ocs []oracleCase
	for id, x := range cases {
		b, err := os.ReadFile("../../conformance/corpus/protocol-v2/golden/xpath-vars-" + id + ".json") //nolint:gosec // fixed ids above
		if err != nil {
			t.Fatal(err)
		}
		var g struct {
			Verdict     string
			Result      json.RawMessage
			Diagnostics []struct {
				Code struct{ Name string }
				VE   string          `json:"vecode_name"`
				Msg  json.RawMessage `json:"msg"`
			}
		}
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatal(err)
		}
		c := oracleCase{CP: "/pv2:c", X: x, Vars: vars}
		if g.Verdict == "valid" {
			c.Res = g.Result
		} else {
			d := g.Diagnostics[0]
			c.Error, _ = json.Marshal(map[string]any{"err": d.Code.Name, "vecode": d.VE, "msg": d.Msg})
		}
		ocs = append(ocs, c)
	}
	replay(t, ocs)
}
