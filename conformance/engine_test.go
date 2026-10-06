// SPDX-License-Identifier: BSD-3-Clause

package conformance

import "testing"

// agreeFloor is the number of fixtures that agree (with or without skipped fields) on main; a
// change that lowers it is a regression.
const agreeFloor = 1385

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
	for _, r := range rep.Results {
		if r.Status == Differ && testing.Verbose() {
			t.Logf("differ %s: %s", r.ID, r.Detail)
		}
	}
	if n := counts[Agree] + counts[AgreeSkipped]; n < agreeFloor {
		t.Errorf("%d fixtures agree, fewer than %d", n, agreeFloor)
	}
}

// TestYangEngineTargets: the m1 schema dumps agree with the oracle (compiled printout skipped).
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

// TestTypedCompared: the data typed dump is compared without the skipped type details only.
func TestTypedCompared(t *testing.T) {
	r := Response{"typed": []any{map[string]any{"path": "/a:x", "flags": map[string]any{"new": false},
		"value": map[string]any{"canonical": "1", "type": "uint8", "union_member": nil}, "meta": []any{}}}}
	got, skipped := withoutSkipped(r, Yang{}.SkippedFields("data"))
	want := Response{"typed": []any{map[string]any{"path": "/a:x", "flags": map[string]any{"new": false},
		"value": map[string]any{"canonical": "1"}}}}
	if d := diffResponses(want, got); d != "" || len(skipped) != 3 || r["typed"].([]any)[0].(map[string]any)["meta"] == nil {
		t.Fatalf("%s %v %v", d, got, skipped)
	}
	got["typed"].([]any)[0].(map[string]any)["flags"] = map[string]any{"new": true}
	if diffResponses(want, got) == "" {
		t.Fatal("flags not compared")
	}
}
