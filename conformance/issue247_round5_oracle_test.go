// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"reflect"
	"testing"
)

// Live libyang v5.8.6 evidence that an opaque request reaches the protocol envelope call and its
// parent->schema argument check. No golden files are generated.
func TestIssue247OpaqueEnvelopeParentArgumentLiveOracle(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	p := issue247OpaqueEnvelopeParentRequest()
	want, err := nativeRequestRunner(t)(base, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (Yang{}).Run(Request{ID: "protocol-v2/issue247-opaque-envelope-parent-argument", BaseDir: base, Params: p})
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
