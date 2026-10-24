// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type issue247Case struct {
	id      string
	params  map[string]any
	verdict string
	reqErr  string
}

func issue247EnvelopeCases() []issue247Case {
	base := func(op, typ, format, input string) map[string]any {
		return map[string]any{
			"op": op, "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
			"context_options": []any{"no_yanglibrary"}, "data_type": typ, "format": format,
			"parse_only": true, "data": input,
		}
	}
	netconfNotif := `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-01-01T00:00:00Z</eventTime><alarm xmlns="urn:vibe-ports:yang:conformance:pv2"><sev>1</sev></alarm></notification>`
	restconfNotif := `{"ietf-restconf:notification":{"eventTime":"2026-01-01T00:00:00Z","pv2:alarm":{"sev":1}}}`
	rpcNetconf := `<rpc xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><ping xmlns="urn:vibe-ports:yang:conformance:pv2"><n>1</n></ping></rpc>`
	replyNetconf := `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><ok/></rpc-reply>`
	rpcRestconf := `{"pv2:input":{"n":1}}`
	replyRestconf := `{"pv2:output":{"rtt":1}}`

	nc := base("data", "notif-netconf", "xml", netconfNotif)
	nc["print_subtree"] = "/notification"
	rc := base("data", "notif-restconf", "json", restconfNotif)
	rc["print_subtree"] = "/notification"

	sequence := func(typ, format, input string) map[string]any {
		p := base("sequence", typ, format, input)
		p["steps"] = []any{map[string]any{"do": "parse", "data_type": typ, "format": format,
			"parse_only": true, "data": input}, map[string]any{"do": "dump"}}
		delete(p, "data_type")
		delete(p, "format")
		delete(p, "parse_only")
		delete(p, "data")
		return p
	}
	xpath := func(typ, format, input string) map[string]any {
		p := base("xpath", typ, format, input)
		p["xpath"] = "true()"
		return p
	}

	return []issue247Case{
		{"protocol-v2/issue247-notification-netconf-sequence", sequence("notif-netconf", "xml", netconfNotif), "valid", ""},
		{"protocol-v2/issue247-notification-restconf-sequence", sequence("notif-restconf", "json", restconfNotif), "valid", ""},
		{"protocol-v2/issue247-notification-netconf-subtree", nc, "request-error", `print_subtree "/notification" not found`},
		{"protocol-v2/issue247-notification-restconf-subtree", rc, "request-error", `print_subtree "/notification" not found`},
		{"protocol-v2/issue247-rpc-netconf-sequence", sequence("rpc-netconf", "xml", rpcNetconf), "valid", ""},
		{"protocol-v2/issue247-reply-netconf-xpath", xpath("reply-netconf", "xml", replyNetconf), "data-error", ""},
		{"protocol-v2/issue247-rpc-restconf-xpath", xpath("rpc-restconf", "json", rpcRestconf), "data-error", ""},
		{"protocol-v2/issue247-reply-restconf-xpath", xpath("reply-restconf", "json", replyRestconf), "data-error", ""},
	}
}

func issue247Run(t *testing.T, base string, tc issue247Case) Response {
	t.Helper()
	got, err := (Yang{}).Run(Request{ID: tc.id, BaseDir: base, Params: tc.params})
	if tc.reqErr != "" {
		if err == nil || strings.TrimSuffix(err.Error(), " (a request-error)") != tc.reqErr {
			t.Fatalf("request error = %v, want %q", err, tc.reqErr)
		}
		return Response{"verdict": "request-error", "request_error": tc.reqErr}
	}
	if err != nil {
		if errors.Is(err, ErrUnsupported) {
			t.Fatalf("envelope request remains unsupported: %v", err)
		}
		t.Fatal(err)
	}
	return got
}

// Every NETCONF/RESTCONF message type reaches the same ParseOp path as lyoracle's parse_one.
// These requests used to stop at the conformance adapter with U-0110.
func TestIssue247EnvelopeRequests(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	for _, tc := range issue247EnvelopeCases() {
		t.Run(tc.id, func(t *testing.T) {
			got := issue247Run(t, base, tc)
			if got.Verdict() != tc.verdict {
				t.Fatalf("verdict = %q, want %q: %#v", got.Verdict(), tc.verdict, got)
			}
		})
	}
}
