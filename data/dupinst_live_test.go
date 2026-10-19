// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestApplyDiffDupInstAfterDelete: lyd_dup_inst_next builds each first instance's duplicate set
// from the live siblings (tree_data_common.c:108-143). After a diff deletes the first of three
// equal state leaf-list instances, the next first instance must not match the freed node, with
// a children table (a, b, d present) and without one.
func TestApplyDiffDupInstAfterDelete(t *testing.T) {
	set := round12Set(t, false, "pv2-dupinst")
	for _, tc := range []struct{ name, leaves string }{
		{"table", `"a":"x","b":"y","d":"z",`},
		{"no table", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := round12Tree(t, set, `{"pv2-dupinst:c":{`+tc.leaves+`"sl":[1,1,1]}}`)
			diff, ds, err := parseWith(context.Background(), strings.NewReader(`{"pv2-dupinst:c":{"@":{"yang:operation":"none"},`+
				`"sl":[1,1,1],"@sl":[{"yang:operation":"delete"},`+
				`{"yang:operation":"none","yang:meta-create":"pv2-dupinst:note=x"},`+
				`{"yang:operation":"none","yang:meta-create":"pv2-dupinst:note=y"}]}}`), set,
				parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
			if err != nil {
				t.Fatal(err, ds)
			}
			if err := tr.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := tr.PrintJSON(&out, PrintOptions{Shrink: true}); err != nil {
				t.Fatal(err)
			}
			want := `"sl":[1,1],"@sl":[{"pv2-dupinst:note":"x"},{"pv2-dupinst:note":"y"}]`
			if !strings.Contains(out.String(), want) {
				t.Fatalf("got %s, want %s", out.String(), want)
			}
		})
	}
}
