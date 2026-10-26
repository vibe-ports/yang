// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// Re-run the storage facade's integration paths against live libyang, including
// deferred publication, registration-order replay, repeated keys, non-owning
// duplication views, payload transfer/copy, opaque removal and bulk disposal.
// Existing requests remain unchanged; this test never writes goldens.
func TestSiblingFacadeLiveOracle(t *testing.T) {
	manifest, err := LoadManifest("corpus/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{
		"ops/opidx-leader-replace-dup":                   false,
		"ops/opidx-leader-sort-dup-ctx":                  false,
		"ops/opidx-xml-deferred-sort-path":               false,
		"protocol-v2/hash-multikey-aab-merge-validate":   false,
		"protocol-v2/hash-repeated-key-after-table":      false,
		"any-core/merge-replace-tree":                    false,
		"any-core/dup-shallow-tree":                      false,
		"any-core/xpath-anydata-hash-child":              false,
		"seq/insert-between-parents":                     false,
		"seq/insert-from-tree":                           false,
		"protocol-v2/hash-table-dirty-run-created-merge": false,
		"seq/delete-opaque-first":                        false,
		"seq/trim-01":                                    false,
	}
	oracle := nativeRequestRunner(t)
	for _, fixture := range manifest.Fixtures {
		if _, ok := wanted[fixture.ID]; !ok {
			continue
		}
		wanted[fixture.ID] = true
		t.Run(fixture.ID, func(t *testing.T) {
			base := filepath.Join("corpus", fixture.Dir)
			want, err := oracle(base, fixture.Request)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (Yang{}).Run(Request{ID: fixture.ID, BaseDir: base, Params: fixture.Request})
			if err != nil {
				t.Fatal(err)
			}
			operation, ok := fixture.Request["op"].(string)
			if !ok {
				t.Fatal("missing request operation")
			}
			want, _ = withoutSkipped(want, Yang{}.SkippedFields(operation))
			got, _ = withoutSkipped(got, Yang{}.SkippedFields(operation))
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s\noracle: %v\nengine: %v", diff, want, got)
			}
		})
	}
	for id, found := range wanted {
		if !found {
			t.Errorf("missing fixture %s", id)
		}
	}
}

// Hand-written requests use the public pv2-edit corpus schema. No golden is
// created: native v5.8.6 linking is compared live, including a singleton origin,
// a deferred run whose leader changes, and a batch inserted before its leader.
func TestSiblingFacadePublicationFollowupOracle(t *testing.T) {
	cases := []struct{ name, request string }{
		{"singleton-origin", `{"op":"sequence","searchdirs":["schemas"],"modules":[{"name":"pv2-edit"}],"steps":[{"do":"parse","format":"json","parse_only":true,"data":"{\"pv2-edit:c\":{\"a\":\"x\"}}"},{"do":"insert","node":"/pv2-edit:c/a","parent":"/pv2-edit:c"},{"do":"dump"}]}`},
		{"deferred-leader", `{"op":"sequence","searchdirs":["schemas"],"modules":[{"name":"pv2-edit"}],"steps":[{"do":"parse","format":"json","parse_only":true,"data":"{\"pv2-edit:c\":{\"ll\":[\"d\",\"c\",\"b\",\"a\"]}}"},{"do":"edit","insert_term":{"module":"pv2-edit","parent":"/pv2-edit:c","name":"ll","value":"0"}},{"do":"dump"}]}`},
		{"batch-leader", `{"op":"sequence","searchdirs":["schemas"],"modules":[{"name":"pv2-edit"}],"steps":[{"do":"parse","format":"json","parse_only":true,"data":"{\"pv2-edit:c\":{\"ll\":[\"d\",\"c\"]}}"},{"do":"edit","format":"json","merge":"{\"pv2-edit:c\":{\"ll\":[\"b\",\"a\"]}}"},{"do":"dump"}]}`},
	}
	oracle := nativeRequestRunner(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var params map[string]any
			if err := json.Unmarshal([]byte(tc.request), &params); err != nil {
				t.Fatal(err)
			}
			base := filepath.Join("corpus", "protocol-v2")
			want, err := oracle(base, params)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (Yang{}).Run(Request{BaseDir: base, Params: params})
			if err != nil {
				t.Fatal(err)
			}
			if want.Verdict() != "valid" {
				t.Fatalf("native request did not succeed: %v", want)
			}
			want, _ = withoutSkipped(want, Yang{}.SkippedFields("sequence"))
			got, _ = withoutSkipped(got, Yang{}.SkippedFields("sequence"))
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s\noracle: %v\nengine: %v", diff, want, got)
			}
		})
	}
}
