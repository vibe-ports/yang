// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"testing"
)

// TestMixedRootDupParentContext is a live oracle fixture. It needs no golden:
// diff apply puts a B root before an A container, then lyd_dup_single copies
// an A child and its metadata under that A container.
func TestMixedRootDupParentContext(t *testing.T) {
	const id = "protocol-v2/dup-mixed-root-parent-context"
	base := filepath.Join("corpus", "protocol-v2")
	params := map[string]any{
		"op": "sequence", "searchdirs": []any{"schemas", "../ietf"},
		"modules": []any{map[string]any{"name": "pv2-detached"}},
		"steps": []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config",
				"target": map[string]any{"searchdirs": []any{"schemas", "../ietf"},
					"modules": []any{map[string]any{"name": "pv2-detached"}}},
				"data": `{"pv2-detached:l":[{"k":"b"}]}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "config",
				"data": `{"pv2-detached:c":{"ll":["a"]}}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "edit", "new_meta": map[string]any{
				"node": "/pv2-detached:c/ll[.='a']", "name": "yang:operation", "value": "none"}},
			map[string]any{"do": "dup", "node": "/pv2-detached:c/ll[.='a']",
				"parent": "/pv2-detached:c"},
			map[string]any{"do": "dump", "with_defaults": "explicit"},
		},
	}
	got, err := (Yang{}).Run(Request{ID: id, BaseDir: base, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	if got["verdict"] != "valid" {
		t.Fatalf("engine verdict: %#v", got)
	}
	want, err := nativeRequestRunner(t)(base, params)
	if err != nil {
		t.Fatal(err)
	}
	want, _ = withoutSkipped(want, Yang{}.SkippedFields("sequence"))
	got, _ = withoutSkipped(got, Yang{}.SkippedFields("sequence"))
	if diff := diffResponses(want, got); diff != "" {
		t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
	}
}

// TestMixedRootMergeParentContext is a live oracle fixture for a diff register
// with a B root before an A parent. The retained tree is put back in A before
// parsing the final source diff, so source and parent both belong to A.
func TestMixedRootMergeParentContext(t *testing.T) {
	const id = "protocol-v2/diff-merge-mixed-root-parent-context"
	base := filepath.Join("corpus", "protocol-v2")
	params := map[string]any{
		"op": "sequence", "searchdirs": []any{"schemas", "../ietf"},
		"modules": []any{map[string]any{"name": "pv2-detached"}},
		"steps": []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config",
				"target": map[string]any{"searchdirs": []any{"schemas", "../ietf"},
					"modules": []any{map[string]any{"name": "pv2-detached"}}},
				"data": `{"pv2-detached:l":[{"k":"b"}]}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "config",
				"data": `{"pv2-detached:c":{"base":"old"}}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "diff", "format": "json", "data_type": "config",
				"data": `{}`},
			map[string]any{"do": "unlink", "node": "/pv2-detached:l[k='b']"},
			map[string]any{"do": "diff_merge", "format": "xml", "data_type": "config",
				"data":     `<c xmlns="urn:vibe-ports:yang:conformance:pv2-detached" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none"><base yang:operation="create">newer</base></c>`,
				"src_node": "/pv2-detached:c/base", "parent": "/pv2-detached:c"},
			map[string]any{"do": "dump", "with_defaults": "explicit"},
		},
	}
	got, err := (Yang{}).Run(Request{ID: id, BaseDir: base, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	if got["verdict"] != "valid" {
		t.Fatalf("engine verdict: %#v", got)
	}
	want, err := nativeRequestRunner(t)(base, params)
	if err != nil {
		t.Fatal(err)
	}
	want, _ = withoutSkipped(want, Yang{}.SkippedFields("sequence"))
	got, _ = withoutSkipped(got, Yang{}.SkippedFields("sequence"))
	if diff := diffResponses(want, got); diff != "" {
		t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
	}
}

// TestOrdinaryDupMixedContexts keeps the conformance adapter on lyd_dup_single:
// the ordinary C call rejects B source and A parent before schema-parent checks.
func TestOrdinaryDupMixedContexts(t *testing.T) {
	const id = "protocol-v2/dup-ordinary-mixed-context-refused"
	base := filepath.Join("corpus", "protocol-v2")
	params := map[string]any{
		"op": "sequence", "searchdirs": []any{"schemas", "../ietf"},
		"modules": []any{map[string]any{"name": "pv2-detached"}},
		"steps": []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config",
				"target": map[string]any{"searchdirs": []any{"schemas", "../ietf"},
					"modules": []any{map[string]any{"name": "pv2-detached"}}},
				"data": `{"pv2-detached:l":[{"k":"b"}]}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "config",
				"data": `{"pv2-detached:c":{}}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "dup", "node": "/pv2-detached:l[k='b']",
				"parent": "/pv2-detached:c"},
		},
	}
	got, err := (Yang{}).Run(Request{ID: id, BaseDir: base, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	if got["verdict"] != "invalid" {
		t.Fatalf("engine verdict: %#v", got)
	}
	want, err := nativeRequestRunner(t)(base, params)
	if err != nil {
		t.Fatal(err)
	}
	want, _ = withoutSkipped(want, Yang{}.SkippedFields("sequence"))
	got, _ = withoutSkipped(got, Yang{}.SkippedFields("sequence"))
	if diff := diffResponses(want, got); diff != "" {
		t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
	}
}
