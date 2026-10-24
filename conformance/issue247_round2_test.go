// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/data"
)

func issue247DupSourceRPCCases() []issue247Case {
	request := func(id, typ string, rpc any) issue247Case {
		return issue247Case{
			id: id,
			params: map[string]any{
				"op": "sequence", "searchdirs": []any{"schemas"},
				"modules":         []any{map[string]any{"name": "pv2"}},
				"context_options": []any{"no_yanglibrary"},
				"steps": []any{map[string]any{
					"do": "dup", "node": "/x",
					"source": map[string]any{
						"data_type": typ, "format": "xml", "parse_only": true,
						"data": "<", "rpc": rpc,
					},
				}},
			},
			verdict: "invalid",
		}
	}
	cases := []issue247Case{
		request("protocol-v2/issue247-dup-source-notif-ignores-rpc", "notif", "ignored"),
		request("protocol-v2/issue247-dup-source-rpc-netconf-ignores-rpc", "rpc-netconf", "ignored"),
		request("protocol-v2/issue247-dup-source-notif-netconf-ignores-rpc", "notif-netconf", "ignored"),
		request("protocol-v2/issue247-dup-source-notif-restconf-ignores-rpc", "notif-restconf", "ignored"),
	}
	syntax := request("protocol-v2/issue247-dup-source-ignored-rpc-syntax", "notif-netconf", 42.0)
	syntax.reqErr = `field "rpc" must be a string`
	return append(cases, syntax)
}

func issue247DupSourceRequestEnvelopeCases() []issue247Case {
	base := func(id, typ, format, rpc, input string, fails bool) issue247Case {
		c := issue247Case{id: id, params: map[string]any{
			"op": "sequence", "searchdirs": []any{"schemas"},
			"modules":         []any{map[string]any{"name": "pv2"}},
			"context_options": []any{"no_yanglibrary"},
			"steps": []any{map[string]any{"do": "dup", "node": "/missing", "source": map[string]any{
				"data_type": typ, "format": format, "parse_only": true, "rpc": rpc, "data": input,
			}}},
		}}
		if fails {
			c.verdict = "invalid"
		} else {
			c.reqErr = "dup node /missing not found in the source"
		}
		return c
	}
	xmlRPC := `<ping xmlns="urn:vibe-ports:yang:conformance:pv2"><n>1</n></ping>`
	jsonRPC := `{"pv2:ping":{"n":1}}`
	return []issue247Case{
		base("protocol-v2/issue247-dup-source-reply-netconf-request-envelope", "reply-netconf", "xml", xmlRPC,
			`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><rtt xmlns="urn:vibe-ports:yang:conformance:pv2">1</rtt></rpc-reply>`, false),
		base("protocol-v2/issue247-dup-source-reply-netconf-request-main-error", "reply-netconf", "xml", xmlRPC, `<`, true),
		base("protocol-v2/issue247-dup-source-rpc-restconf-request-envelope", "rpc-restconf", "json", jsonRPC,
			`{"pv2:input":{"n":2}}`, false),
		base("protocol-v2/issue247-dup-source-rpc-restconf-request-main-error", "rpc-restconf", "json", jsonRPC, `{`, true),
		base("protocol-v2/issue247-dup-source-reply-restconf-request-envelope", "reply-restconf", "json", jsonRPC,
			`{"pv2:output":{"rtt":1}}`, false),
		base("protocol-v2/issue247-dup-source-reply-restconf-request-main-error", "reply-restconf", "json", jsonRPC, `{`, true),
	}
}

// parse_one ignores a syntactically valid rpc source for notification data and the three
// envelope types that cannot use a request parent. The malformed envelope must therefore supply
// the result, while a non-string rpc field still fails input_of before parsing.
func TestIssue247DupSourceIgnoredRPC(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	for _, tc := range issue247DupSourceRPCCases() {
		t.Run(tc.id, func(t *testing.T) {
			got := issue247Run(t, base, tc)
			if tc.reqErr != "" {
				return
			}
			if got.Verdict() != tc.verdict {
				t.Fatalf("verdict = %q, want %q: %#v", got.Verdict(), tc.verdict, got)
			}
			diags := got.Diagnostics()
			if len(diags) == 0 || diags[0]["phase"] != "parse" {
				t.Fatalf("diagnostics = %#v, want an envelope parse diagnostic", diags)
			}
		})
	}
}

// A request-consuming dup source must parse the valid request first, then either return an
// envelope that can be duplicated or report the main body's parse failure. The sequence selector
// cannot address libyang's schema-less envelope root, so the request form observes successful
// parsing as the later selector request-error; the direct check duplicates that returned root.
func TestIssue247DupSourceRequestEnvelopes(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	for _, tc := range issue247DupSourceRequestEnvelopeCases() {
		t.Run(tc.id, func(t *testing.T) {
			got := issue247Run(t, base, tc)
			if tc.reqErr != "" {
				return
			}
			if got.Verdict() != "invalid" {
				t.Fatalf("verdict = %q, want invalid: %#v", got.Verdict(), got)
			}
			diags := got.Diagnostics()
			if len(diags) == 0 || diags[len(diags)-1]["phase"] != "parse" {
				t.Fatalf("diagnostics = %#v, want main-body parse failure", diags)
			}
		})
	}

	r := Request{ID: "protocol-v2/issue247-dup-source-request-envelope-direct", BaseDir: base,
		Params: map[string]any{"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
			"context_options": []any{"no_yanglibrary"}}, buildContext: buildSchemaContext}
	s, _, _, verdict, err := r.buildContext(r)
	if err != nil || verdict != "valid" {
		t.Fatalf("build context = %s, %v", verdict, err)
	}
	for _, tc := range issue247DupSourceRequestEnvelopeCases() {
		if strings.HasSuffix(tc.id, "main-error") {
			continue
		}
		source := tc.params["steps"].([]any)[0].(map[string]any)["source"].(map[string]any)
		tree, diags, err := parseOne(r, s, source, execStepParse, "data", "parse", nil)
		if err != nil || len(diags) != 0 || tree == nil || topNode(tree) == nil {
			t.Fatalf("%s parseOne = tree %v, diagnostics %#v, error %v", tc.id, tree, diags, err)
		}
		dst := data.NewTree(s)
		if _, diags, err := dst.Dup(topNode(tree), nil, data.DupOptions{Recursive: true}); err != nil || len(diags) != 0 {
			t.Fatalf("%s envelope duplication diagnostics %#v, error %v", tc.id, diags, err)
		}
	}
}
