// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"errors"
	"path/filepath"
	"testing"
)

// Live oracle fixtures keep request-error and parse-error ordering observable
// without creating golden files for requests that return no normal response.
func TestRound18EnvelopeOracleOrder(t *testing.T) {
	oracle := nativeRequestRunner(t)
	base := filepath.Join("corpus", "protocol-v2")
	for _, tc := range []struct {
		id, op, typ, input string
		add                map[string]any
		want               string
	}{
		{"protocol-v2/malformed-netconf-cur-module", "xpath", "notif-netconf", "<", map[string]any{"cur_module": "missing"}, "data-error"},
		{"protocol-v2/malformed-netconf-context", "xpath", "notif-netconf", "<", map[string]any{"context_path": "/pv2:c"}, "data-error"},
		{"protocol-v2/malformed-netconf-subtree", "data", "notif-netconf", "<", map[string]any{"print_subtree": "/pv2:c"}, "invalid"},
		{"protocol-v2/malformed-netconf-diff-data-node", "sequence", "notif-netconf", "<", map[string]any{"data_node": "/pv2:c"}, "invalid"},
		{"protocol-v2/malformed-restconf-cur-module", "xpath", "notif-restconf", "{", map[string]any{"cur_module": "missing"}, "data-error"},
		{"protocol-v2/malformed-restconf-subtree", "data", "notif-restconf", "{", map[string]any{"print_subtree": "/pv2:c"}, "invalid"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			p := envelopeRequest(tc.op, tc.typ, tc.input, tc.add)
			want, err := oracle(base, p)
			if err != nil {
				t.Fatal(err)
			}
			if want["verdict"] != tc.want || want["request_error"] != nil {
				t.Fatalf("oracle verdict = %v, request error = %v; want %s", want["verdict"], want["request_error"], tc.want)
			}
			_, err = (Yang{}).Run(Request{ID: tc.id, BaseDir: base, Params: p})
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("engine error = %v, want ErrUnsupported", err)
			}
		})
	}
}

func TestRound18NotificationSelectorOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	base := filepath.Join("corpus", "protocol-v2")
	input := `<alarm xmlns="urn:vibe-ports:yang:conformance:pv2"><sev>1</sev></alarm>`
	for _, tc := range []struct{ id, selector string }{
		{"protocol-v2/notif-context-self", "."},
		{"protocol-v2/notif-context-predicate", "/pv2:alarm[sev='1']"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			p := envelopeRequest("xpath", "notif", input, map[string]any{"context_path": tc.selector})
			p["xpath"] = "name(.)"
			want, err := oracle(base, p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (Yang{}).Run(Request{ID: tc.id, BaseDir: base, Params: p})
			if err != nil {
				t.Fatal(err)
			}
			for label, resp := range map[string]Response{"oracle": want, "engine": got} {
				if resp["verdict"] != "valid" || resp["result"].(map[string]any)["value"] != "pv2:alarm" {
					t.Fatalf("%s result = %v", label, resp)
				}
			}
		})
	}
}
