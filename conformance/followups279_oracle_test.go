// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"testing"
)

// Live v5.8.6 oracle requests for the #261 audit follow-ups (#279, #280, #281, #282, #287).
// They have no generated goldens.
func TestFollowups279Requests(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	ctx := func(module string) map[string]any {
		return map[string]any{"searchdirs": []any{"schemas", "../ietf"},
			"modules":         []any{map[string]any{"name": module}},
			"context_options": []any{"no_yanglibrary"}}
	}
	const ns = `xmlns="urn:vibe-ports:yang:conformance:pv2-detached" xmlns:yang="urn:ietf:params:xml:ns:yang:1"`
	const dcNS = `xmlns="urn:vibe-ports:yang:conformance:pv2-dupcreate" xmlns:yang="urn:ietf:params:xml:ns:yang:1" ` +
		`xmlns:pdc="urn:vibe-ports:yang:conformance:pv2-dupcreate"`
	diffParse := func(data string) map[string]any {
		return map[string]any{"do": "diff_parse", "format": "xml", "data_type": "config", "data": data}
	}
	diffMerge := func(data string) map[string]any {
		return map[string]any{"do": "diff_merge", "format": "xml", "data_type": "config", "data": data}
	}
	dupinst := func(leaves string) []any {
		return []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"pv2-dupinst:c":{` + leaves + `"sl":[1,2]}}`},
			map[string]any{"do": "diff_parse", "format": "json", "data_type": "data",
				"data": `{"pv2-dupinst:c":{"@":{"yang:operation":"none"},"sl":[2,1,1,1],"@sl":[` +
					`{"yang:operation":"none"},{"yang:operation":"create","yang:position":1},` +
					`{"yang:operation":"none","yang:meta-create":"pv2-dupinst:note=x"},` +
					`{"yang:operation":"none","yang:meta-create":"pv2-dupinst:note=y"}]}}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "dump"},
		}
	}
	cases := []struct {
		id      string
		module  string
		variant string // ctx-a/ctx-b schema directories
		steps   []any
	}{
		{"detached-opaque-diff-leaflist", "pv2-detached", "", []any{
			map[string]any{"do": "parse", "format": "xml", "data_type": "config", "parse_only": true,
				"unknown": "opaque", "data": `<u xmlns="urn:unknown">x</u>`},
			map[string]any{"do": "unlink", "node": "@top"},
			map[string]any{"do": "diff", "detached": true, "single": true, "format": "xml", "data_type": "config",
				"data": `<ll ` + ns + `>a</ll>`},
		}},
		{"propagate-meta-list-missing-key", "pv2-detached", "", []any{
			diffParse(`<l ` + ns + ` yang:operation="create"><k>a</k></l>` +
				`<l ` + ns + ` yang:operation="create" yang:key="[k='a']"><k>b</k></l>`),
			diffMerge(`<l ` + ns + ` yang:operation="replace" yang:key="" yang:orig-key=""><k>a</k></l>`),
		}},
		{"propagate-meta-leaflist-missing-value", "pv2-detached", "", []any{
			diffParse(`<ll ` + ns + ` yang:operation="create">x</ll>` +
				`<ll ` + ns + ` yang:operation="create" yang:value="x">y</ll>`),
			diffMerge(`<ll ` + ns + ` yang:operation="replace" yang:value="" yang:orig-value="">x</ll>`),
		}},
		{"dup-key-with-parents-foreign", "pv2-edit", "", []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "config", "parse_only": true,
				"data": `{"pv2-edit:c":{"l":[{"k":"a"},{"k":"b"}]}}`},
			map[string]any{"do": "dup", "node": "/pv2-edit:c/l[k='b']/k", "options": []any{"with_parents"},
				"target": ctx("pv2-edit")},
			map[string]any{"do": "dump"},
		}},
		{"compare-foreign-leaflist-lyb", "pv2-ctxshape", "ctx-a", []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"pv2-ctxshape:ll":["1"]}`},
			map[string]any{"do": "compare", "format": "json", "data_type": "data", "parse_only": true,
				"data":   `{"pv2-ctxshape:ll":[1]}`,
				"target": map[string]any{"searchdirs": []any{"schemas/ctx-b"}, "modules": []any{map[string]any{"name": "pv2-ctxshape"}}}},
		}},
		{"apply-created-duplicate-table", "pv2-dupinst", "", dupinst(`"a":"x","b":"y","d":"z",`)},
		{"apply-created-duplicate-no-table", "pv2-dupinst", "", dupinst(``)},
		// The foreign tl makes merge_into's destination keep the request context while the source
		// c's child z keeps the target's (lyd_merge_siblings): c is a mixed-context level with a
		// children table. The lookup of a warms the duplicate cache before x is created.
		{"apply-created-duplicate-mixed-table", "pv2-dupcreate", "", []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "config", "parse_only": true,
				"data": `{"pv2-dupcreate:c":{"ll":["z"]}}`},
			map[string]any{"do": "insert", "format": "json", "data_type": "config", "target": ctx("pv2-dupcreate"),
				"data": `{"pv2-dupcreate:tl":["q"]}`},
			map[string]any{"do": "edit", "format": "json", "data_type": "config",
				"merge_into": `{"pv2-dupcreate:c":{"a":"1","b":"2","d":"3","ll":["a","x"]}}`},
			map[string]any{"do": "diff_parse", "format": "json", "data_type": "config",
				"data": `{"pv2-dupcreate:c":{"@":{"yang:operation":"none"},"ll":["a","x","x","x"],"@ll":[` +
					`{"yang:operation":"none"},{"yang:operation":"create"},` +
					`{"yang:operation":"none","yang:meta-create":"pv2-dupcreate:note=x"},` +
					`{"yang:operation":"none","yang:meta-create":"pv2-dupcreate:note=y"}]}}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "dump"},
		}},
		// A create, then a user-ordered replace moving that instance behind its equals, then two
		// first lookups: each set follows the live sibling order (xA, xB), not the order at the
		// create (xC, xA, xB). The leading tl keeps the retained tree's first node in place:
		// the diff_apply step reports the tree from the pointer lyd_diff_apply_all leaves.
		{"apply-created-duplicate-moved", "pv2-dupcreate", "", []any{
			map[string]any{"do": "parse", "format": "xml", "data_type": "data", "parse_only": true,
				"data": `<tl ` + dcNS + `>q</tl><sl ` + dcNS + `>z</sl><sl ` + dcNS + ` pdc:note="A">x</sl><sl ` + dcNS + ` pdc:note="B">x</sl>`},
			map[string]any{"do": "diff_parse", "format": "xml", "data_type": "data",
				"data": `<sl ` + dcNS + ` yang:operation="none">z</sl>` +
					`<sl ` + dcNS + ` yang:operation="create" yang:position="">x</sl>` +
					`<sl ` + dcNS + ` yang:operation="replace" yang:position="4" yang:orig-position="1">x</sl>` +
					`<sl ` + dcNS + ` yang:operation="none" yang:meta-create="pv2-dupcreate:note=p">x</sl>` +
					`<sl ` + dcNS + ` yang:operation="none" yang:meta-create="pv2-dupcreate:note=q">x</sl>`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "dump"},
		}},
		{"apply-created-duplicate-top-level", "pv2-dupcreate", "", []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "config", "parse_only": true,
				"data": `{"pv2-dupcreate:tl":["a","b"]}`},
			map[string]any{"do": "diff_parse", "format": "json", "data_type": "config",
				"data": `{"pv2-dupcreate:tl":["a","b","b","b"],"@pv2-dupcreate:tl":[` +
					`{"yang:operation":"none"},{"yang:operation":"create"},` +
					`{"yang:operation":"none","yang:meta-create":"pv2-dupcreate:note=x"},` +
					`{"yang:operation":"none","yang:meta-create":"pv2-dupcreate:note=y"}]}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "dump"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			p := map[string]any{"op": "sequence", "searchdirs": []any{"schemas", "../ietf"},
				"modules":         []any{map[string]any{"name": tc.module}},
				"context_options": []any{"no_yanglibrary"}, "steps": tc.steps}
			if tc.variant != "" {
				p["searchdirs"] = []any{"schemas/" + tc.variant}
				delete(p, "context_options")
			}
			got, err := (Yang{}).Run(Request{ID: tc.id, BaseDir: base, Params: p})
			if err != nil {
				t.Fatal(err)
			}
			want, err := nativeRequestRunner(t)(base, p)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("oracle verdict %s", want.Verdict())
			want, _ = withoutSkipped(want, Yang{}.SkippedFields("sequence"))
			got, _ = withoutSkipped(got, Yang{}.SkippedFields("sequence"))
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s (%s)", diff, round12FirstDifference(normalize(want), normalize(got), ""))
			}
		})
	}
}
