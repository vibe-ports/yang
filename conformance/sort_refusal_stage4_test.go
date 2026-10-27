// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func assertD0121Response(t *testing.T, got Response) {
	t.Helper()
	if got.Verdict() != "invalid" || !strings.Contains(fmt.Sprint(got), "D-0121") || !strings.Contains(fmt.Sprint(got), "LY_EINVAL") {
		t.Fatalf("expected the logged D-0121 refusal, got %#v", got)
	}
}

// stage4Refused are the classified fixtures whose native golden is release-build undefined
// behavior (D-0121 §2.3 P1): the golden stays as evidence, the port refuses.
var stage4Refused = []string{"ut-diff/sweep/sweep-06", "ut-diff/sweep/sweep-37"}

// stage4Agreeing are the other 25 classified fixtures: the port must match their golden. Eight
// rely on the canonical-ABI pairs R1-R3 (int-string, int-width, uint-width, ipv4-string and
// sweeps 02, 26, 29, 35).
var stage4Agreeing = []string{
	"ut-diff/cross-context-bits-max-position-equal", "ut-diff/cross-context-bits-max-position",
	"ut-diff/cross-context-hash-signed-char", "ut-diff/cross-context-int-string-order",
	"ut-diff/cross-context-int-width-order", "ut-diff/cross-context-uint-width-order",
	"ut-diff/cross-context-ipv4-string", "ut-diff/cross-context-keyed-loses-key",
	"ut-diff/cross-context-keyless-gains-key", "ut-diff/cross-context-keyless",
	"ut-diff/cross-context-mixed-order", "ut-diff/cross-context-userord-delete-before",
	"ut-diff/cross-context",
	"ut-diff/sweep/sweep-02", "ut-diff/sweep/sweep-07", "ut-diff/sweep/sweep-08",
	"ut-diff/sweep/sweep-11", "ut-diff/sweep/sweep-15", "ut-diff/sweep/sweep-18",
	"ut-diff/sweep/sweep-26", "ut-diff/sweep/sweep-27", "ut-diff/sweep/sweep-29",
	"ut-diff/sweep/sweep-31", "ut-diff/sweep/sweep-34", "ut-diff/sweep/sweep-35",
}

// TestStage4RecordedRefusals pins the 25/2 classification of #301 stage 4: exactly sweeps 06
// and 37 carry the D-0121 assert and produce the logged refusal; the other 25 carry none (and
// TestYangEngineSchema compares them with their goldens).
func TestStage4RecordedRefusals(t *testing.T) {
	m := load(t)
	var asserted []string
	byID := map[string]Fixture{}
	for _, f := range m.Fixtures {
		byID[f.ID] = f
		if f.Assert != nil && f.Assert.Deviation != nil && *f.Assert.Deviation == "D-0121" {
			asserted = append(asserted, f.ID)
		}
	}
	slices.Sort(asserted)
	if !slices.Equal(asserted, stage4Refused) {
		t.Fatalf("D-0121 asserted fixtures = %v, want %v", asserted, stage4Refused)
	}
	if len(stage4Agreeing) != 25 {
		t.Fatalf("%d agreeing fixtures, want 25", len(stage4Agreeing))
	}
	for _, id := range stage4Agreeing {
		if _, ok := byID[id]; !ok {
			t.Errorf("classified fixture %s is missing", id)
		}
	}
	for _, id := range stage4Refused {
		t.Run(id, func(t *testing.T) {
			f := byID[id]
			got, err := (Yang{}).Run(Request{ID: f.ID, BaseDir: fixtureDir(m, f), Params: f.Request})
			if err != nil {
				t.Fatal(err)
			}
			assertD0121Response(t, got)
			if s := fmt.Sprint(got); !strings.Contains(s, "first instance missing") || strings.Contains(s, "two schema contexts") {
				t.Fatalf("refusal names the wrong precondition: %v", s)
			}
		})
	}
}
