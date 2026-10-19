// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"encoding/json"
	"reflect"
	"testing"
)

// These fixtures have lead-generated goldens pending. Compare the affected
// observations directly with the native oracle in the meantime.
func TestAudit261Round20NativeFixtures(t *testing.T) {
	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	oracle, _ := requestOracle(t)
	for _, tc := range []struct {
		id    string
		steps []int
		field string
	}{
		{"protocol-v2/print-detached-absolute-leafref", []int{2, 3}, "detached_tree"},
		{"protocol-v2/merge-foreign-child", []int{3}, "tree"},
		{"protocol-v2/compare-bits-unused-same-byte", []int{1}, "compare"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			f := fixture(m, tc.id)
			if f.ID == "" {
				t.Fatal("fixture missing")
			}
			base := fixtureDir(m, f)
			want, err := inventoryNativeResponse(oracle, base, f.Request)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (Yang{}).Run(Request{ID: f.ID, BaseDir: base, Params: f.Request})
			if err != nil {
				t.Fatal(err)
			}
			if got.Verdict() != "valid" || want.Verdict() != "valid" {
				t.Fatalf("verdict: Go=%s, libyang=%s", got.Verdict(), want.Verdict())
			}
			gs, ws := got["steps"].([]any), want["steps"].([]any)
			for _, i := range tc.steps {
				g, w := gs[i].(map[string]any)[tc.field], ws[i].(map[string]any)[tc.field]
				if tc.field != "compare" {
					g = semanticJSON(t, g.(map[string]any)["json"].(string))
					w = semanticJSON(t, w.(map[string]any)["json"].(string))
				}
				if !reflect.DeepEqual(g, w) {
					t.Fatalf("step %d %s: Go=%v, libyang=%v", i, tc.field, g, w)
				}
			}
		})
	}
}

func semanticJSON(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
