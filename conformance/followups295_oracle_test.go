// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestFollowup295LiveOracle compares the complete parser and envelope responses without adding
// goldens: verdicts, diagnostics (including the anydata text truncation), trees and typed views.
func TestFollowup295LiveOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	base := filepath.Join("corpus", "ut-parser")
	for _, tc := range followup295Cases() {
		t.Run(tc.id, func(t *testing.T) {
			want, err := oracle(base, tc.params)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (Yang{}).Run(Request{ID: tc.id, BaseDir: base, Params: tc.params})
			if err != nil {
				t.Fatal(err)
			}
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
			}
			if !reflect.DeepEqual(want.Diagnostics(), got.Diagnostics()) {
				t.Fatalf("full diagnostics mismatch\noracle: %#v\nengine: %#v", want.Diagnostics(), got.Diagnostics())
			}
		})
	}
}
