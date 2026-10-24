// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"testing"
)

// Live libyang v5.8.6 evidence for every distinct pending-diagnostic collection path used by a
// sequence source. No golden files are generated.
func TestIssue247PendingSourcePhasesLiveOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	base := filepath.Join("corpus", "types")
	for _, tc := range issue247PendingPhaseCases() {
		t.Run(tc.id, func(t *testing.T) {
			p := issue247PendingRequest(tc)
			want, err := oracle(base, p)
			if err != nil {
				t.Fatal(err)
			}
			phase, found := issue247PendingDiagnosticPhase(t, want)
			if found != (tc.phase != "") || phase != tc.phase {
				t.Fatalf("oracle pending diagnostic phase = %q (found %t), want %q", phase, found, tc.phase)
			}
			got, err := (Yang{}).Run(Request{ID: tc.id, BaseDir: base, Params: p})
			if err != nil {
				t.Fatal(err)
			}
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
			}
		})
	}
}
