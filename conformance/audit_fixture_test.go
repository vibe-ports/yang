// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"path/filepath"
	"testing"
)

// The new sequence requests remain runnable before their native goldens are
// added. Both use a separate parsed source for DupSiblings (D-0132).
func TestAuditNodeXPathRequests(t *testing.T) {
	m, err := LoadManifest("corpus/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"ops/audit-input-node-xpath":  false,
		"ops/audit-output-node-xpath": false,
	}
	for _, f := range m.Fixtures {
		if _, ok := want[f.ID]; !ok {
			continue
		}
		t.Run(f.ID, func(t *testing.T) {
			want[f.ID] = true
			resp, err := (Yang{}).Run(Request{ID: f.ID, BaseDir: filepath.Join("corpus", f.Dir), Params: f.Request})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Verdict() != "valid" {
				t.Fatalf("request verdict %s: %v", resp.Verdict(), resp)
			}
			oracle, _ := requestOracle(t)
			want, err := inventoryNativeResponse(oracle, filepath.Join("corpus", f.Dir), f.Request)
			if err != nil {
				t.Fatal(err)
			}
			want, _ = withoutSkipped(want, (Yang{}).SkippedFields("sequence"))
			resp, _ = withoutSkipped(resp, (Yang{}).SkippedFields("sequence"))
			if status, detail := classify(f, want, resp); status != Agree {
				t.Fatalf("oracle mismatch: %s\nengine: %v\noracle: %v", detail, resp, want)
			}
		})
	}
	for id, found := range want {
		if !found {
			t.Errorf("missing fixture %s", id)
		}
	}
}
