// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"testing"
)

// TestIssue247API4LiveOracle is live libyang v5.8.6 evidence for lyd_new_opaq / lyd_new_opaq2
// construction through Tree.NewOpaque (insert_opaq edits and compare roots), and for the
// context compare collects (issue247API4PendingCases). It generates no
// golden files.
func TestIssue247API4LiveOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	for base, cases := range map[string][]issue247Case{
		filepath.Join("corpus", "ut-new"): issue247API4Cases(),
		filepath.Join("corpus", "types"):  issue247API4PendingCases(),
	} {
		for _, tc := range cases {
			t.Run(tc.id, func(t *testing.T) {
				want, err := oracle(base, tc.params)
				if err != nil {
					t.Fatal(err)
				}
				got := issue247Run(t, base, tc)
				if diff := diffResponses(want, got); diff != "" {
					t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
				}
			})
		}
	}
}
