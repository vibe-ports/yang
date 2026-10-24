// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"testing"
)

// Live libyang v5.8.6 evidence for dup.source rpc handling; no golden files are generated.
func TestIssue247DupSourceRPCLiveOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	base := filepath.Join("corpus", "protocol-v2")
	for _, tc := range issue247DupSourceRPCCases() {
		t.Run(tc.id, func(t *testing.T) {
			want, err := oracle(base, tc.params)
			if err != nil {
				t.Fatal(err)
			}
			got := issue247Run(t, base, tc)
			if tc.reqErr != "" {
				if want.Verdict() != "request-error" || want["request_error"] != tc.reqErr {
					t.Fatalf("oracle request error = %#v, want %q", want, tc.reqErr)
				}
				return
			}
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
			}
		})
	}
}

// Live evidence for the request-bound envelope branch of dup.source: each request-consuming
// envelope type is exercised with a valid request and both a valid and malformed main body.
func TestIssue247DupSourceRequestEnvelopeLiveOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	base := filepath.Join("corpus", "protocol-v2")
	for _, tc := range issue247DupSourceRequestEnvelopeCases() {
		t.Run(tc.id, func(t *testing.T) {
			want, err := oracle(base, tc.params)
			if err != nil {
				t.Fatal(err)
			}
			got := issue247Run(t, base, tc)
			if tc.reqErr != "" {
				if want.Verdict() != "request-error" || want["request_error"] != tc.reqErr {
					t.Fatalf("oracle request error = %#v, want %q", want, tc.reqErr)
				}
				return
			}
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
			}
		})
	}
}
