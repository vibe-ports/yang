// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"path/filepath"
	"slices"
	"testing"
)

// The fixture is compared live while its golden is deliberately absent.
func TestSortedJoinedCopyOracle(t *testing.T) {
	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range m.Fixtures {
		if f.ID != "protocol-v2/sorted-joined-copy-insert" {
			continue
		}
		base := filepath.Join(m.Corpus(), f.Dir)
		got, err := (Yang{}).Run(Request{ID: f.ID, BaseDir: base, Params: f.Request})
		if err != nil {
			t.Fatal(err)
		}
		if got.Verdict() != "valid" {
			t.Fatalf("engine verdict: %v", got)
		}
		steps := got["steps"].([]any)
		last := steps[len(steps)-1].(map[string]any)
		var values []string
		for _, raw := range last["typed"].([]any) {
			node := raw.(map[string]any)
			if node["schema"] == "/m:r/u" {
				values = append(values, node["value"].(map[string]any)["canonical"].(string))
			}
		}
		if want := []string{"2", "3", "4", "7", "8", "6"}; !slices.Equal(values, want) {
			t.Fatalf("joined copied runs = %v, want %v", values, want)
		}
		oracle, _ := requestOracle(t)
		want, err := inventoryNativeResponse(oracle, base, f.Request)
		if err != nil {
			t.Fatal(err)
		}
		want, _ = withoutSkipped(want, (Yang{}).SkippedFields("sequence"))
		got, _ = withoutSkipped(got, (Yang{}).SkippedFields("sequence"))
		if status, detail := classify(f, want, got); status != Agree {
			t.Fatalf("oracle mismatch: %s\nengine: %v\noracle: %v", detail, got, want)
		}
		return
	}
	t.Fatal("sorted joined copy fixture missing")
}
