// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
)

// agreeFloor is the number of fixtures that agree (with or without skipped fields) on main; a
// change that lowers it is a regression.
const agreeFloor = 488

// nodeWalk reports whether Load compiles schema nodes (design 06 C6 lifts the gate).
func nodeWalk(t *testing.T) bool {
	t.Helper()
	ctx, _, err := yang.NewContext(yang.Options{}, fstest.MapFS{"p.yang": {Data: []byte(
		"module p { namespace urn:p; prefix p; leaf l { type string; } }")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.Load("p", "", nil); err != nil {
		t.Fatal(err)
	}
	for range ctx.Schema().Implemented("p").Top() {
		return true
	}
	return false
}

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
	if n := counts[Agree] + counts[AgreeSkipped]; n < agreeFloor {
		t.Errorf("%d fixtures agree, fewer than %d", n, agreeFloor)
	}
}

// TestYangEngineTargets: the m1 schema dumps agree with the oracle (compiled printout skipped).
// It skips while the node walk is gated (until design 06 C6).
func TestYangEngineTargets(t *testing.T) {
	if !nodeWalk(t) {
		t.Skip("the node walk is gated until design 06 C6")
	}
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
