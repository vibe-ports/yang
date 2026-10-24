// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"fmt"
	"path/filepath"
	"testing"
)

type issue247PendingPhaseCase struct {
	id, phase string
	step      map[string]any
}

func issue247PendingPrefix() []any {
	return []any{
		map[string]any{"do": "edit", "insert_inner": map[string]any{"module": "print-failure", "name": "c"}},
		map[string]any{"do": "edit", "insert_term": map[string]any{"module": "print-failure", "name": "s", "parent": "/print-failure:c", "value": "value"}},
		map[string]any{"do": "edit", "new_meta": map[string]any{"node": "/print-failure:c/s", "name": "print-failure:select", "value": "/missing:node"}},
		map[string]any{"do": "dump"},
	}
}

func issue247PendingDetachedPrefix() []any {
	return []any{
		map[string]any{"do": "edit", "insert_inner": map[string]any{"module": "print-failure", "name": "c"}},
		map[string]any{"do": "unlink", "node": "/print-failure:c"},
		map[string]any{"do": "edit", "insert_inner": map[string]any{"module": "print-failure", "name": "c"}},
		map[string]any{"do": "edit", "insert_term": map[string]any{"module": "print-failure", "name": "s", "parent": "/print-failure:c", "value": "value"}},
		map[string]any{"do": "edit", "new_meta": map[string]any{"node": "/print-failure:c/s", "name": "print-failure:select", "value": "/missing:node"}},
		map[string]any{"do": "dump"},
	}
}

func issue247PendingPhaseCases() []issue247PendingPhaseCase {
	datastore := func(do string) map[string]any {
		return map[string]any{"do": do, "format": "xml", "data_type": "data", "parse_only": true, "data": "<"}
	}
	reply := func(do string) map[string]any {
		return map[string]any{"do": do, "format": "xml", "data_type": "reply", "parse_only": true, "data": "<", "rpc": "<"}
	}
	return []issue247PendingPhaseCase{
		{"types/issue247-pending-parse-data", "parse", datastore("parse")},
		{"types/issue247-pending-parse-reply-rpc", "rpc", reply("parse")},
		{"types/issue247-pending-diff-data", "parse", datastore("diff")},
		{"types/issue247-pending-diff-reply-rpc", "rpc", reply("diff")},
		{"types/issue247-pending-diff-target-parse-error", "", map[string]any{
			"do": "diff", "format": "xml", "data_type": "data", "parse_only": true, "data": "<",
			"target": map[string]any{
				"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "print-failure"}},
			},
		}},
		{"types/issue247-pending-dup-data", "parse", map[string]any{"do": "dup", "node": "/x", "source": map[string]any{
			"format": "xml", "data_type": "data", "parse_only": true, "data": "<",
		}}},
		{"types/issue247-pending-dup-notif-netconf-rpc", "parse", map[string]any{"do": "dup", "node": "/x", "source": map[string]any{
			"format": "xml", "data_type": "notif-netconf", "parse_only": true, "data": "<", "rpc": "<",
		}}},
		{"types/issue247-pending-dup-reply-netconf-rpc", "rpc", map[string]any{"do": "dup", "node": "/x", "source": map[string]any{
			"format": "xml", "data_type": "reply-netconf", "parse_only": true, "data": "<", "rpc": "<",
		}}},
		{"types/issue247-pending-dup-rpc-rpc-error", "rpc", map[string]any{"do": "dup", "node": "/x", "source": map[string]any{
			"format": "xml", "data_type": "rpc", "parse_only": true, "data": "<", "rpc": "<",
		}}},
		{"types/issue247-pending-dup-retained", "edit", map[string]any{
			"do": "dup", "node": "/print-failure:c", "options": []any{"recursive"},
		}},
		{"types/issue247-pending-dup-detached-ignores-source", "edit", map[string]any{
			"do": "dup", "detached": true, "options": []any{"recursive"},
			"source": map[string]any{
				"format": "xml", "data_type": "reply-netconf", "parse_only": true, "data": "<", "rpc": "<",
			},
		}},
		{"types/issue247-pending-insert-data", "parse", datastore("insert")},
		{"types/issue247-pending-insert-target-data", "edit", map[string]any{
			"do": "insert", "format": "xml", "data_type": "data", "parse_only": true,
			"data": `<c xmlns="urn:vibe-ports:print-failure"/>`,
			"target": map[string]any{
				"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "print-failure"}},
			},
		}},
		{"types/issue247-pending-insert-target-parse-error", "", map[string]any{
			"do": "insert", "format": "xml", "data_type": "data", "parse_only": true, "data": "<",
			"target": map[string]any{
				"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "print-failure"}},
			},
		}},
		{"types/issue247-pending-edit-merge", "edit", map[string]any{
			"do": "edit", "format": "xml", "data_type": "data", "merge": "<",
		}},
		{"types/issue247-pending-diff-merge-data", "parse", map[string]any{
			"do": "diff_merge", "format": "xml", "data_type": "data", "data": "<",
		}},
	}
}

func issue247PendingRequest(tc issue247PendingPhaseCase) map[string]any {
	prefix := issue247PendingPrefix()
	if tc.id == "types/issue247-pending-dup-detached-ignores-source" {
		prefix = issue247PendingDetachedPrefix()
	}
	return map[string]any{
		"op": "sequence", "searchdirs": []any{"schemas"},
		"modules": []any{map[string]any{"name": "print-failure"}},
		"steps":   append(prefix, tc.step),
	}
}

func issue247PendingDiagnosticPhase(t *testing.T, r Response) (string, bool) {
	t.Helper()
	steps := r["steps"].([]any)
	last := len(steps) - 1
	if r.Verdict() != "invalid" || fmt.Sprint(r["failed_step"]) != fmt.Sprint(last) {
		t.Fatalf("pending diagnostic did not fail source step: %#v", r)
	}
	step := steps[last].(map[string]any)
	for _, raw := range step["diagnostics"].([]any) {
		d := raw.(map[string]any)
		if d["msg"] == `Failed to resolve prefix "missing".` {
			phase, _ := d["phase"].(string)
			return phase, true
		}
	}
	return "", false
}

// A dump leaves its XML printer error in the schema context. The next source-bearing step must
// assign that pending item the phase used by the native call that first collects the context.
func TestIssue247PendingSourcePhases(t *testing.T) {
	base := filepath.Join("corpus", "types")
	for _, tc := range issue247PendingPhaseCases() {
		t.Run(tc.id, func(t *testing.T) {
			got, err := (Yang{}).Run(Request{ID: tc.id, BaseDir: base, Params: issue247PendingRequest(tc)})
			if err != nil {
				t.Fatal(err)
			}
			phase, found := issue247PendingDiagnosticPhase(t, got)
			if found != (tc.phase != "") || phase != tc.phase {
				t.Fatalf("pending diagnostic phase = %q (found %t), want %q", phase, found, tc.phase)
			}
		})
	}
}

// parse_one's early "rpc" collect is selected only by rpc_src presence and the lyd_type. Keep the
// complete dparams_of data_type table here so adding an envelope type cannot silently inherit the
// wrong pending-log phase.
func TestIssue247ParseOnePendingPhaseMatrix(t *testing.T) {
	consumesRPC := map[string]bool{
		"rpc": true, "reply": true, "reply-netconf": true,
		"rpc-restconf": true, "reply-restconf": true,
	}
	for _, typ := range []string{
		"config", "data-operational", "data", "get", "getconfig", "edit",
		"rpc", "reply", "notif", "rpc-netconf", "notif-netconf", "reply-netconf",
		"rpc-restconf", "notif-restconf", "reply-restconf",
	} {
		p := map[string]any{"data_type": typ}
		if phase := parseOnePendingPhase(p, false, "parse"); phase != "parse" {
			t.Errorf("%s without rpc: phase = %q, want parse", typ, phase)
		}
		want := "parse"
		if consumesRPC[typ] {
			want = "rpc"
		}
		if phase := parseOnePendingPhase(p, true, "parse"); phase != want {
			t.Errorf("%s with rpc: phase = %q, want %s", typ, phase, want)
		}
	}
}
