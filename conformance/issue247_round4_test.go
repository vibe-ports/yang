// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"path/filepath"
	"slices"
	"testing"
)

func issue247CrossContextInsertRequest() map[string]any {
	steps := issue247PendingPrefix()
	steps = append(steps, map[string]any{
		"do": "insert", "format": "xml", "data_type": "data", "parse_only": true,
		"unknown": "skip", "data": `<l xmlns="urn:rollback-key"><j>J</j><k>K</k></l>`,
		"target": map[string]any{
			"searchdirs": []any{"../ut-parser/rollback-key"},
			"modules":    []any{map[string]any{"name": "rollback-key"}},
		},
	})
	return map[string]any{
		"op": "sequence", "searchdirs": []any{"schemas"},
		"modules": []any{map[string]any{"name": "print-failure"}},
		"steps":   steps,
	}
}

func diagnosticPhaseMessages(r Response) []string {
	steps := r["steps"].([]any)
	diags := steps[len(steps)-1].(map[string]any)["diagnostics"].([]any)
	out := make([]string, 0, len(diags))
	for _, raw := range diags {
		d := raw.(map[string]any)
		out = append(out, d["phase"].(string)+": "+d["msg"].(string))
	}
	return out
}

// The target parser is collected before step_insert returns to the retained context. Its warning
// therefore precedes the retained printer error collected by the insertion step.
func TestIssue247CrossContextInsertDiagnosticOrder(t *testing.T) {
	base := filepath.Join("corpus", "types")
	got, err := (Yang{}).Run(Request{ID: "types/issue247-cross-context-insert-order", BaseDir: base,
		Params: issue247CrossContextInsertRequest()})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`parse: Invalid position of the key "k" in a list.`,
		`edit: Failed to resolve prefix "missing".`,
	}
	if actual := diagnosticPhaseMessages(got); !slices.Equal(actual, want) {
		t.Fatalf("diagnostic order = %#v, want %#v\nresponse: %#v", actual, want, got)
	}
}

type issue247DiagnosticOrderCase struct {
	id    string
	setup []any
	step  map[string]any
}

func issue247DiagnosticOrderCases() []issue247DiagnosticOrderCase {
	warning := `<l xmlns="urn:rollback-key"><j>J</j><k>K</k></l>`
	broken := warning + `<`
	return []issue247DiagnosticOrderCase{
		{id: "parse", step: map[string]any{"do": "parse", "format": "xml", "data_type": "data",
			"parse_only": true, "unknown": "skip", "data": broken}},
		{id: "diff", step: map[string]any{"do": "diff", "format": "xml", "data_type": "data",
			"parse_only": true, "unknown": "skip",
			"data":      warning + `<c xmlns="urn:vibe-ports:yang:conformance:dm2"><a>x</a></c>`,
			"data_node": "/dm2:c/a"}},
		{id: "dup", step: map[string]any{"do": "dup", "node": "/rollback-key:l[k='K'][j='J']", "source": map[string]any{
			"format": "xml", "data_type": "data", "parse_only": true, "unknown": "skip",
			"data": broken}}},
		{id: "insert", step: map[string]any{"do": "insert", "format": "xml", "data_type": "data",
			"parse_only": true, "unknown": "skip", "data": warning, "parent": "/print-failure:c",
			"target": map[string]any{"searchdirs": []any{"../ut-parser/rollback-key"},
				"modules": []any{map[string]any{"name": "rollback-key"}}}}},
		{id: "merge", step: map[string]any{"do": "edit", "format": "xml", "data_type": "data",
			"unknown": "skip", "merge": broken}},
		{id: "diff_parse", step: map[string]any{"do": "diff_parse", "format": "xml", "data_type": "data",
			"unknown": "skip", "data": broken}},
		{id: "diff_merge", setup: []any{map[string]any{"do": "diff_parse", "format": "xml", "data_type": "data",
			"unknown": "skip", "data": `<c xmlns="urn:vibe-ports:yang:conformance:dm2" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="create"><a>x</a></c>`}},
			step: map[string]any{"do": "diff_merge", "format": "xml", "data_type": "data", "unknown": "skip",
				"data": warning + `<c xmlns="urn:vibe-ports:yang:conformance:dm2" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="create"><a>y</a></c>`}},
	}
}

func issue247DiagnosticOrderRequest(tc issue247DiagnosticOrderCase) map[string]any {
	steps := append([]any{}, tc.setup...)
	steps = append(steps, issue247PendingPrefix()...)
	steps = append(steps, tc.step)
	return map[string]any{
		"op":         "sequence",
		"searchdirs": []any{"schemas", "../ut-parser/rollback-key", "../ut-diff/schemas"},
		"modules": []any{
			map[string]any{"name": "print-failure"},
			map[string]any{"name": "rollback-key"},
			map[string]any{"name": "dm2"},
		},
		"steps": steps,
	}
}

func issue247AssertThreeDiagnosticStreams(t *testing.T, diags []map[string]any) {
	t.Helper()
	pending, warning, operation := -1, -1, -1
	for i, d := range diags {
		switch d["msg"] {
		case `Failed to resolve prefix "missing".`:
			pending = i
		case `Invalid position of the key "k" in a list.`:
			warning = i
		default:
			if operation == -1 && d["level"] == "error" {
				operation = i
			}
		}
	}
	if pending == -1 || warning == -1 || operation <= pending || operation <= warning {
		t.Fatalf("diagnostics = %#v, want pending and parse warning before an operation error", diags)
	}
}

// Keep the matrix active in ordinary Go tests as well as in the live-oracle lane. The tagged
// test below compares every field of this ordered list with libyang; this test is the fast
// regression that proves all three streams are present in one step.
func TestIssue247SourceDiagnosticOrderMatrix(t *testing.T) {
	base := filepath.Join("corpus", "types")
	for _, tc := range issue247DiagnosticOrderCases() {
		t.Run(tc.id, func(t *testing.T) {
			p := issue247DiagnosticOrderRequest(tc)
			got, err := (Yang{}).Run(Request{ID: "types/issue247-diagnostic-order-" + tc.id, BaseDir: base, Params: p})
			if err != nil {
				t.Fatal(err)
			}
			issue247AssertThreeDiagnosticStreams(t, got.Diagnostics())
		})
	}
}
