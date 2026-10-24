// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"errors"
	"path/filepath"
	"testing"
)

func issue247OpaqueEnvelopeParentRequest() map[string]any {
	return map[string]any{
		"op": "sequence", "searchdirs": []any{"schemas"},
		"modules":         []any{map[string]any{"name": "pv2"}},
		"context_options": []any{"no_yanglibrary"},
		"steps": []any{map[string]any{
			"do": "dup", "node": "/missing",
			"source": map[string]any{
				"data_type": "reply-restconf", "format": "json", "parse_only": true,
				"unknown": "opaque", "rpc": `{"pv2:ping":42}`, "data": `{"pv2:output":{}}`,
			},
		}},
	}
}

// lyd_parse_op checks parent->schema before parsing a request-bound protocol envelope. The
// conformance adapter must not turn that native argument error into the YANG-reply U-0106 gap.
func TestIssue247OpaqueEnvelopeParentArgument(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	got, err := (Yang{}).Run(Request{ID: "protocol-v2/issue247-opaque-envelope-parent-argument", BaseDir: base,
		Params: issue247OpaqueEnvelopeParentRequest()})
	if err != nil {
		if errors.Is(err, ErrUnsupported) {
			t.Fatalf("opaque protocol request stopped at U-0106: %v", err)
		}
		t.Fatal(err)
	}
	if got.Verdict() != "invalid" {
		t.Fatalf("verdict = %q, want invalid: %#v", got.Verdict(), got)
	}
	diags := got.Diagnostics()
	if len(diags) != 1 || diags[0]["phase"] != "parse" || diags[0]["vecode_name"] != "LYVE_SUCCESS" ||
		diags[0]["msg"] != "Invalid argument parent->schema (lyd_parse_op())." {
		t.Fatalf("diagnostics = %#v, want the native parent->schema argument error", diags)
	}
}
