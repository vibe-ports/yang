// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"fmt"
	"strings"
	"testing"
)

func assertD0121Response(t *testing.T, got Response) {
	t.Helper()
	if got.Verdict() != "invalid" || !strings.Contains(fmt.Sprint(got), "D-0121") || !strings.Contains(fmt.Sprint(got), "LY_EINVAL") {
		t.Fatalf("expected the logged D-0121 class refusal, got %#v", got)
	}
}

// Existing native goldens remain immutable. Every newly excluded deterministic fixture must
// actually produce the logged class refusal, rather than an unrelated invalid result.
func TestStage4RecordedRefusals(t *testing.T) {
	m := load(t)
	count := 0
	for _, f := range m.Fixtures {
		if f.Assert == nil || f.Assert.Deviation == nil || *f.Assert.Deviation != "D-0121" {
			continue
		}
		count++
		t.Run(f.ID, func(t *testing.T) {
			got, err := (Yang{}).Run(Request{ID: f.ID, BaseDir: fixtureDir(m, f), Params: f.Request})
			if err != nil {
				t.Fatal(err)
			}
			assertD0121Response(t, got)
		})
	}
	if count < 26 {
		t.Fatalf("only %d recorded refusal cases", count)
	}
}
