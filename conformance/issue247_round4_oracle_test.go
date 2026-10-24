// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestIssue247CrossContextInsertOrderLiveOracle(t *testing.T) {
	base := filepath.Join("corpus", "types")
	p := issue247CrossContextInsertRequest()
	want, err := nativeRequestRunner(t)(base, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (Yang{}).Run(Request{ID: "types/issue247-cross-context-insert-order", BaseDir: base, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	if diff := diffResponses(want, got); diff != "" {
		t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
	}
	if !reflect.DeepEqual(want.Diagnostics(), got.Diagnostics()) {
		t.Fatalf("full diagnostics mismatch\noracle: %#v\nengine: %#v", want.Diagnostics(), got.Diagnostics())
	}
}

// Every sequence operation that consumes a source is checked with three simultaneously live
// diagnostic streams: a retained-context printer error, a source parser warning, and an error
// from the source-bearing operation. DeepEqual makes both phase assignment and list order part of
// the assertion instead of merely checking that all messages eventually appear.
func TestIssue247SourceDiagnosticOrderMatrixLiveOracle(t *testing.T) {
	base := filepath.Join("corpus", "types")
	oracle := nativeRequestRunner(t)
	for _, tc := range issue247DiagnosticOrderCases() {
		t.Run(tc.id, func(t *testing.T) {
			p := issue247DiagnosticOrderRequest(tc)
			want, err := oracle(base, p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (Yang{}).Run(Request{ID: "types/issue247-diagnostic-order-" + tc.id, BaseDir: base, Params: p})
			if err != nil {
				t.Fatal(err)
			}
			wantDiags, gotDiags := want.Diagnostics(), got.Diagnostics()
			issue247AssertThreeDiagnosticStreams(t, wantDiags)
			if !reflect.DeepEqual(wantDiags, gotDiags) {
				t.Fatalf("full ordered diagnostics mismatch\noracle: %#v\nengine: %#v", wantDiags, gotDiags)
			}
		})
	}
}
