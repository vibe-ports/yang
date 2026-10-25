// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"testing"
)

// TestIssue247API1LiveOracle is live libyang v5.8.6 evidence for canonical/store-only term and
// list construction, canonical changes, and lyd_new_list2's option, predicate, lookup and parent
// branches. It generates no golden files.
func TestIssue247API1LiveOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	base := filepath.Join("corpus", "ut-new")
	for _, tc := range issue247API1Cases() {
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
