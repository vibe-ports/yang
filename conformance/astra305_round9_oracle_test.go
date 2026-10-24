// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"testing"
)

// TestAstra305Round9SelectedRunMerge pins the native first-instance run used by
// lyd_find_sibling_first while lyd_merge_siblings adds duplicate keyless lists.
func TestAstra305Round9SelectedRunMerge(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	params := map[string]any{
		"op":              "sequence",
		"searchdirs":      []any{"astra305-round9"},
		"modules":         []any{map[string]any{"name": "pv2-astra305-r9"}},
		"context_options": []any{"no_yanglibrary"},
		"steps": []any{
			map[string]any{
				"do": "parse", "format": "xml", "data_type": "data", "parse_only": true,
				"parse_options": []any{"ordered"},
				"data": `<reply xmlns="urn:vibe-ports:yang:conformance:pv2-astra305-r9"><before>before</before>` +
					`<l><value>a</value></l><l><value>b</value></l><input>kept</input>` +
					`<l><value>c</value></l></reply>`,
			},
			map[string]any{
				"do": "edit", "format": "xml", "data_type": "data",
				"merge": `<reply xmlns="urn:vibe-ports:yang:conformance:pv2-astra305-r9">` +
					`<l><value>c</value></l><l><value>c</value></l></reply>`,
			},
			map[string]any{"do": "dump"},
		},
	}
	got, err := (Yang{}).Run(Request{ID: "merge-keyless-split-selected-run", BaseDir: base, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	want, err := nativeRequestRunner(t)(base, params)
	if err != nil {
		t.Fatal(err)
	}
	want, _ = withoutSkipped(want, Yang{}.SkippedFields("sequence"))
	got, _ = withoutSkipped(got, Yang{}.SkippedFields("sequence"))
	if diff := diffResponses(want, got); diff != "" {
		t.Fatalf("oracle mismatch: %s\noracle: %v\nengine: %v", diff, want, got)
	}
}
