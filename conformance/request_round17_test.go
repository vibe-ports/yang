// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func envelopeRequest(op, typ, input string, add map[string]any) map[string]any {
	p := map[string]any{"op": op, "searchdirs": []any{"schemas"},
		"modules": []any{map[string]any{"name": "pv2"}}, "context_options": []any{"no_yanglibrary"}}
	fields := map[string]any{"data_type": typ, "format": "xml", "parse_only": true, "data": input}
	if typ == "notif-restconf" {
		fields["format"] = "json"
	}
	if op == "sequence" {
		st := map[string]any{"do": "diff"}
		for k, v := range fields {
			st[k] = v
		}
		for k, v := range add {
			st[k] = v
		}
		p["steps"] = []any{st}
		return p
	}
	for k, v := range fields {
		p[k] = v
	}
	for k, v := range add {
		p[k] = v
	}
	if op == "xpath" {
		p["xpath"] = "/pv2:alarm"
	}
	return p
}

// lyoracle resolves these fields after parse_one. The engine must parse the notification
// envelope before it reports the selector, module or XPath-option result.
func TestEnvelopePostParseChecks(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	inputs := map[string]string{
		"notif-netconf":  `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-01-01T00:00:00Z</eventTime><alarm xmlns="urn:vibe-ports:yang:conformance:pv2"><sev>1</sev></alarm></notification>`,
		"notif-restconf": `{"ietf-restconf:notification":{"eventTime":"2026-01-01T00:00:00Z","pv2:alarm":{"sev":1}}}`,
	}
	for typ, input := range inputs {
		for _, tc := range []struct {
			name, op string
			add      map[string]any
		}{
			{"subtree", "data", map[string]any{"print_subtree": "/pv2:c"}},
			{"context-miss", "xpath", map[string]any{"context_path": "/pv2:c"}},
			{"context-self", "xpath", map[string]any{"context_path": "."}},
			{"context-predicate", "xpath", map[string]any{"context_path": "/notification[eventTime='2026-01-01T00:00:00Z']"}},
			{"cur-module", "xpath", map[string]any{"cur_module": "missing"}},
			{"namespace-type", "xpath", map[string]any{"namespaces": "bad"}},
			{"namespace-uri", "xpath", map[string]any{"xpath_format": "xml", "namespaces": []any{map[string]any{"prefix": "p"}}}},
			{"xpath-format", "xpath", map[string]any{"xpath_format": "bogus"}},
			{"diff-data-node", "sequence", map[string]any{"data_node": "/pv2:c"}},
		} {
			t.Run(typ+"/"+tc.name, func(t *testing.T) {
				_, err := (Yang{}).Run(Request{BaseDir: base, Params: envelopeRequest(tc.op, typ, input, tc.add)})
				if errors.Is(err, ErrUnsupported) {
					t.Fatalf("envelope request remains unsupported: %v", err)
				}
			})
		}
	}
}

func TestMalformedEnvelopePrecedesPostParseChecks(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	for _, tc := range []struct {
		name, op, verdict string
		add               map[string]any
	}{
		{"subtree", "data", "invalid", map[string]any{"print_subtree": "/pv2:c"}},
		{"cur-module", "xpath", "data-error", map[string]any{"cur_module": "missing"}},
		{"context", "xpath", "data-error", map[string]any{"context_path": "/pv2:c"}},
		{"diff-data-node", "sequence", "invalid", map[string]any{"data_node": "/pv2:c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (Yang{}).Run(Request{BaseDir: base, Params: envelopeRequest(tc.op, "notif-netconf", "<", tc.add)})
			if err != nil {
				t.Fatal(err)
			}
			if got.Verdict() != tc.verdict {
				t.Fatalf("verdict = %q, want %q: %#v", got.Verdict(), tc.verdict, got)
			}
		})
	}
}

func TestNotificationSelfAndPredicateSelectors(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	input := `<alarm xmlns="urn:vibe-ports:yang:conformance:pv2"><sev>1</sev></alarm>`
	for _, path := range []string{".", "/pv2:alarm[sev='1']"} {
		t.Run(path, func(t *testing.T) {
			p := envelopeRequest("xpath", "notif", input, map[string]any{"context_path": path})
			p["xpath"] = "name(.)"
			r, err := (Yang{}).Run(Request{BaseDir: base, Params: p})
			if err != nil || r["verdict"] != "valid" {
				t.Fatalf("selector %q: response %v, error %v", path, r, err)
			}
			if got := r["result"].(map[string]any)["value"]; got != "pv2:alarm" {
				t.Fatalf("selector %q: result %v", path, got)
			}
		})
	}
}

func TestRPCRequestSelectorsBeforeUnsupported(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	for key, path := range map[string]string{"rpc_parent": "/pv2:ping/rtt", "print_subtree": "/pv2:c"} {
		t.Run(key, func(t *testing.T) {
			p := map[string]any{"op": "data", "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
				"data_type": "rpc", "format": "json", "parse_only": true, "keep_input": true,
				"rpc": inventoryRPC, "data": `{"n":1}`, key: path}
			_, err := (Yang{}).Run(Request{BaseDir: base, Params: p})
			if key == "print_subtree" {
				if !errors.Is(err, ErrUnsupported) {
					t.Fatalf("got %v, want ErrUnsupported", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), key+" ") || errors.Is(err, ErrUnsupported) {
				t.Fatalf("got %v, want %s request error", err, key)
			}
		})
	}
}

func TestAnyDirectAbsentModulePrecedesTarget(t *testing.T) {
	p := map[string]any{"op": "sequence", "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
		"steps": []any{map[string]any{"do": "any_direct", "case": "equal_repeated_collisions"}}}
	r, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: p})
	if err != nil || r["verdict"] != "invalid" || r["rc"].(map[string]any)["name"] != "LY_EINVAL" {
		t.Fatalf("got %v, %v", r, err)
	}
}
