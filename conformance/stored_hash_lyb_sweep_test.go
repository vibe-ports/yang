// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"strings"
	"testing"
)

// These oracle requests also run without generated goldens: source terminal hashes
// survive duplication into a different type. The lyb-* nodes have equal canonical
// text but unequal native payloads; text-* nodes are equal-payload controls.
// Replacing HashLYBWork with canonical-text hashing must fail every lyb-* subtest.
func TestStoredHashLYBSweep(t *testing.T) {
	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, fixture := range m.Fixtures {
		if !strings.HasPrefix(fixture.ID, "protocol-v2/hash-lyb-sweep-") {
			continue
		}
		matched++
		t.Run(strings.TrimPrefix(fixture.ID, "protocol-v2/hash-lyb-sweep-"), func(t *testing.T) {
			response, err := (Yang{}).Run(Request{ID: fixture.ID, BaseDir: fixtureDir(m, fixture), Params: fixture.Request})
			if err != nil {
				t.Fatal(err)
			}
			if response["verdict"] != "valid" {
				t.Fatalf("request failed: %#v", response)
			}
			steps := response["steps"].([]any)
			requests := fixture.Request["steps"].([]any)
			for i, raw := range steps {
				step := raw.(map[string]any)
				comparison, ok := step["compare"].(map[string]any)
				if !ok {
					if i >= 2 {
						t.Fatalf("step %d has no comparison: %#v", i, step)
					}
					continue
				}
				path := requests[i].(map[string]any)["first"].(string)
				t.Run(strings.TrimPrefix(path, "/pv2-hash-sweep:r/"), func(t *testing.T) {
					want := "LY_ENOT"
					if strings.Contains(path, "/text-") {
						want = "LY_SUCCESS"
					}
					if comparison["name"] != want {
						t.Errorf("compare = %v, want %s", comparison, want)
					}
				})
			}
			if len(steps) != len(requests) {
				t.Fatalf("only %d of %d steps executed", len(steps), len(requests))
			}
		})
	}
	if matched == 0 {
		t.Fatal("no LYB sweep fixtures found")
	}
}
