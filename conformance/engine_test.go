// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"testing"
)

// TestYangEngineSchema runs every fixture through package yang and logs the tally.
func TestYangEngineSchema(t *testing.T) {
	m := load(t)
	rep, err := m.Compare(Yang{})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[Status]int{}
	for _, r := range rep.Results {
		counts[r.Status]++
	}
	t.Logf("agree %d, agree (skipped fields) %d, differ %d, deviation %d, unsupported %d", counts[Agree],
		counts[AgreeSkipped], counts[Differ], counts[Deviation], counts[Unsupported])
}

// TestYangEngineTargets: the m1 schema dumps agree with the oracle (compiled printout skipped).
// Until the node walk runs in Load (design 06 C6 lifts the gate) the trees are empty and the
// test skips.
func TestYangEngineTargets(t *testing.T) {
	m := load(t)
	for _, f := range m.Fixtures {
		if f.ID != "m1/schema-tree" && f.ID != "m1/schema-tree-no-features" {
			continue
		}
		got, err := Yang{}.Run(Request{ID: f.ID, BaseDir: fixtureDir(m, f), Params: f.Request})
		if err != nil {
			t.Fatal(f.ID, err)
		}
		if mods := list(got["modules"]); len(mods) == 0 || len(list(mods[0].(map[string]any)["schema_tree"])) == 0 {
			t.Skip("no schema tree: the node walk is gated until design 06 C6")
		}
		golden, err := LoadGolden(m.GoldenPath(f))
		if err != nil {
			t.Fatal(err)
		}
		golden, _ = withoutSkipped(golden, Yang{}.SkippedFields("schema"))
		got, _ = withoutSkipped(got, Yang{}.SkippedFields("schema"))
		if d := diffResponses(golden, got); d != "" {
			t.Errorf("%s: %s", f.ID, d)
		}
	}
}
