// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func round12FirstDifference(a, b any, path string) string {
	if reflect.DeepEqual(a, b) {
		return ""
	}
	if x, ok := a.(map[string]any); ok {
		if y, ok := b.(map[string]any); ok {
			keys := make([]string, 0, len(x)+len(y))
			for k := range x {
				keys = append(keys, k)
			}
			for k := range y {
				if _, found := x[k]; !found {
					keys = append(keys, k)
				}
			}
			slices.Sort(keys)
			for _, k := range keys {
				if d := round12FirstDifference(x[k], y[k], path+"/"+k); d != "" {
					return d
				}
			}
		}
	}
	if x, ok := a.([]any); ok {
		if y, ok := b.([]any); ok {
			if len(x) != len(y) {
				return fmt.Sprintf("%s length: oracle %d, engine %d", path, len(x), len(y))
			}
			for i := range x {
				if d := round12FirstDifference(x[i], y[i], fmt.Sprintf("%s/%d", path, i)); d != "" {
					return d
				}
			}
		}
	}
	return fmt.Sprintf("%s: oracle %#v, engine %#v", path, a, b)
}

// These requests use the live v5.8.6 oracle. They have no generated goldens.
// The contexts in each request are compiled independently by lyoracle.
func TestAudit261Round12Requests(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	ctx := func(module string) map[string]any {
		return map[string]any{"searchdirs": []any{"schemas", "../ietf"},
			"modules":         []any{map[string]any{"name": module}},
			"context_options": []any{"no_yanglibrary"}}
	}
	parse := func(input string) any {
		return map[string]any{"do": "parse", "format": "json", "data_type": "config", "parse_only": true, "data": input}
	}
	const ns = `urn:vibe-ports:yang:conformance:pv2-detached`
	const yn = `urn:ietf:params:xml:ns:yang:1`
	cases := []struct {
		id     string
		module string
		steps  []any
	}{
		{"detached-leafref-default-diagnostics", "pv2-detached-default", []any{
			parse(`{"pv2-detached-default:c":{"t":["x"],"r":"x"}}`),
			map[string]any{"do": "unlink", "node": "/pv2-detached-default:c/r"},
			map[string]any{"do": "detached_dump", "with_defaults": "trim"},
		}},
		{"foreign-free-meta-collection", "pv2-mdapi", []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config", "target": ctx("pv2-detached"),
				"data": `{"pv2-detached:l":[{"k":"b"}]}`},
			parse(`{"pv2-mdapi:c":{"s":"x"}}`),
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "edit", "new_meta": map[string]any{"node": "/pv2-mdapi:c/s", "name": "yang:operation", "value": "none"}},
			map[string]any{"do": "edit", "free_meta": map[string]any{"node": "/pv2-mdapi:c/s", "name": "missing:x"}},
		}},
		{"foreign-change-term-collection", "pv2-mdapi", []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config", "target": ctx("pv2-detached"),
				"data": `{"pv2-detached:l":[{"k":"b"}]}`},
			parse(`{"pv2-mdapi:c":{"ll":[1]}}`),
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "change_term", "node": "/pv2-mdapi:c/ll[.='1']", "value": "bad"},
		}},
		{"foreign-diff-meta-collection", "pv2-detached", []any{
			parse(`{"pv2-detached:c":{"base":"a"}}`),
			map[string]any{"do": "diff_parse", "format": "xml", "data_type": "config",
				"data": `<ll xmlns="` + ns + `" xmlns:yang="` + yn + `" yang:operation="create" yang:value="">x</ll>`},
			map[string]any{"do": "dup", "node": "/pv2-detached:c", "options": []any{"recursive"}, "target": ctx("pv2-detached")},
			map[string]any{"do": "diff_merge", "format": "xml", "data_type": "config",
				"data": `<ll xmlns="` + ns + `" xmlns:yang="` + yn + `" yang:operation="replace" yang:value="y" yang:orig-value="">x</ll>`},
		}},
		{"foreign-leafref-target-disables-linking", "pv2-detached", []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config", "target": ctx("pv2-links"),
				"data": `{"pv2-links:c":{"l":[{"k":"a"}],"r":"a"}}`},
			parse(`{"pv2-detached:l":[{"k":"a"}]}`),
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "link"},
		}},
		{"foreign-position-anchor-stops-at-first-instance", "pv2-detached", []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config", "target": ctx("pv2-detached"),
				"data": `{"pv2-detached:l":[{"k":"b"}]}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"pv2-detached:sll":["a"]}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "diff_parse", "format": "xml", "data_type": "data",
				"data": `<sll xmlns="` + ns + `" xmlns:yang="` + yn + `" yang:operation="create" yang:position="1">z</sll>`},
			map[string]any{"do": "diff_apply"},
		}},
		{"foreign-positional-find-misses", "pv2-detached", []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config", "target": ctx("pv2-detached"),
				"data": `{"pv2-detached:l":[{"k":"b"}]}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"pv2-detached:sll":["a"]}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "edit", "delete": "/pv2-detached:sll[1]"},
		}},
		{"mixed-run-empty-anchor-selected-only", "pv2-detached", []any{
			parse(`{"pv2-detached:ll":["b"]}`),
			map[string]any{"do": "unlink", "node": "/pv2-detached:ll[.='b']"},
			map[string]any{"do": "dup", "detached": true, "target": ctx("pv2-detached")},
			map[string]any{"do": "change_term", "node": "/pv2-detached:ll[.='b']", "value": "a"},
			map[string]any{"do": "insert", "detached": true},
			map[string]any{"do": "diff_parse", "format": "xml", "data_type": "config",
				"data": `<ll xmlns="` + ns + `" xmlns:yang="` + yn + `" yang:operation="create" yang:value="">c</ll>`},
			map[string]any{"do": "diff_apply"},
		}},
		{"mixed-run-equal-duplicate-cache", "pv2-detached", []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"pv2-detached:sll":["x"]}`},
			map[string]any{"do": "unlink", "node": "/pv2-detached:sll[.='x']"},
			map[string]any{"do": "dup", "detached": true, "target": ctx("pv2-detached")},
			map[string]any{"do": "insert", "detached": true},
			map[string]any{"do": "diff_parse", "format": "xml", "data_type": "data",
				"data": `<sll xmlns="` + ns + `" xmlns:yang="` + yn + `" yang:operation="none">x</sll>` +
					`<sll xmlns="` + ns + `" xmlns:yang="` + yn + `" yang:operation="none">x</sll>`},
			map[string]any{"do": "diff_apply"},
		}},
		{"mixed-root-diff-retains-source-snapshots", "pv2-detached", []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config",
				"target": map[string]any{"searchdirs": []any{"schemas"},
					"modules": []any{map[string]any{"name": "pv2-detached", "features": []any{"extra"}}}},
				"data": `{"pv2-detached:c":{"extra":"x"}}`},
			parse(`{"aa:keep":"a"}`),
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "diff"},
		}},
		{"foreign-choice-schema-lookup", "pv2-ctxshape", []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config",
				"target": map[string]any{"searchdirs": []any{"schemas/ctx-b"},
					"modules": []any{map[string]any{"name": "aa"}, map[string]any{"name": "pv2-ctxshape"}}},
				"data": `{"aa:keep":"b"}`},
			parse(`{"pv2-ctxshape:x":"a"}`),
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "xpath", "xpath": "/pv2-ctxshape:x"},
		}},
		{"foreign-operation-context-identity", "pv2-ctxshape", []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config",
				"target": map[string]any{"searchdirs": []any{"schemas/ctx-b"},
					"modules": []any{map[string]any{"name": "aa"}, map[string]any{"name": "pv2-ctxshape"}}},
				"data": `{"aa:keep":"b"}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "rpc", "parse_only": true,
				"data": `{"pv2-ctxshape:r":{"x":"a"}}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "xpath", "node": "/pv2-ctxshape:r", "xpath": "/pv2-ctxshape:r"},
		}},
		{"foreign-revision-must", "pv2-ctxid", []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config",
				"target": map[string]any{"searchdirs": []any{"schemas/ctx-b"},
					"modules": []any{map[string]any{"name": "aa"}, map[string]any{"name": "pv2-ctxid"}}},
				"data": `{"pv2-ctxid:id":"pv2-ctxid:child"}`},
			parse(`{"aa:keep":"a"}`),
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "validate", "validate_options": []any{"present"}},
		}},
		{"failed-unlink-key-keeps-diff-register", "pv2-detached", []any{
			map[string]any{"do": "diff_parse", "format": "xml", "data_type": "config",
				"data": `<l xmlns="` + ns + `" xmlns:yang="` + yn + `" yang:operation="create"><k>a</k></l>`},
			map[string]any{"do": "unlink", "diff_node": "/pv2-detached:l[k='a']/k"},
		}},
		{"foreign-failed-unlink-key-retains-register", "pv2-detached", []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config",
				"target": ctx("pv2-detached"), "data": `{"pv2-detached:l":[{"k":"a"}]}`},
			map[string]any{"do": "unlink", "diff_node": "/pv2-detached:l[k='a']/k"},
		}},
		{"detached-opaque-diff-tree", "pv2-detached", []any{
			map[string]any{"do": "parse", "format": "xml", "data_type": "config", "parse_only": true,
				"unknown": "opaque", "data": `<u xmlns="urn:unknown">x</u>`},
			map[string]any{"do": "unlink", "node": "@top"},
			map[string]any{"do": "diff", "detached": true, "single": true, "format": "xml", "data_type": "config",
				"data": `<l xmlns="` + ns + `"><k>a</k></l>`},
		}},
		{"detached-opaque-diff-siblings", "pv2-detached", []any{
			map[string]any{"do": "parse", "format": "xml", "data_type": "config", "parse_only": true,
				"unknown": "opaque", "data": `<u xmlns="urn:unknown">x</u>`},
			map[string]any{"do": "unlink", "node": "@top"},
			map[string]any{"do": "diff", "detached": true, "format": "xml", "data_type": "config",
				"data": `<l xmlns="` + ns + `"><k>a</k></l>`},
		}},
		{"mixed-opaque-print-context", "pv2-ctxshape", []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config",
				"target": map[string]any{"searchdirs": []any{"schemas/ctx-b"},
					"modules": []any{map[string]any{"name": "aa"}, map[string]any{"name": "pv2-ctxshape"}}},
				"data": `{"aa:keep":"b"}`},
			map[string]any{"do": "parse", "format": "xml", "data_type": "config", "parse_only": true,
				"unknown": "opaque", "data": `<u xmlns="urn:vibe-ports:yang:conformance:pv2-ctxshape">x</u>`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "dump"},
		}},
		{"mixed-opaque-xml-print-context", "pv2-ctxshape", []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config",
				"target": map[string]any{"searchdirs": []any{"schemas/ctx-b"},
					"modules": []any{map[string]any{"name": "aa"}, map[string]any{"name": "pv2-ctxshape"}}},
				"data": `{"aa:keep":"b"}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "config", "parse_only": true,
				"unknown": "opaque", "data": `{"pv2-ctxshape:u":"x"}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "dump"},
		}},
		{"foreign-leaflist-hash-by-lyb", "pv2-ctxshape", []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config",
				"target": map[string]any{"searchdirs": []any{"schemas/ctx-b"},
					"modules": []any{map[string]any{"name": "aa"}, map[string]any{"name": "pv2-ctxshape"}}},
				"data": `{"aa:keep":"b"}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"pv2-ctxshape:ll":["1"]}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "xpath", "xpath": "/pv2-ctxshape:ll[.='1']"},
		}},
		{"foreign-leafref-owner-context", "pv2-ctxref", []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config",
				"target": map[string]any{"searchdirs": []any{"schemas/ctx-b"},
					"modules": []any{map[string]any{"name": "aa"}, map[string]any{"name": "pv2-ctxref"}}},
				"data": `{"aa:keep":"b"}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "config", "parse_only": true,
				"data": `{"pv2-ctxref:l":[{"k":"01"}],"pv2-ctxref:r":"01"}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "validate", "validate_options": []any{"present"}},
			map[string]any{"do": "xpath", "xpath": "deref(/pv2-ctxref:r)"},
			map[string]any{"do": "dump", "with_defaults": "trim"},
		}},
		{"foreign-list-key-hash-by-lyb", "pv2-ctxref", []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config",
				"target": map[string]any{"searchdirs": []any{"schemas/ctx-b"},
					"modules": []any{map[string]any{"name": "aa"}, map[string]any{"name": "pv2-ctxref"}}},
				"data": `{"aa:keep":"b"}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "config", "parse_only": true,
				"data": `{"pv2-ctxref:l":[{"k":"1"}]}`},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "xpath", "xpath": "/pv2-ctxref:l[k='1']"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			p := map[string]any{"op": "sequence", "searchdirs": []any{"schemas", "../ietf"},
				"modules":         []any{map[string]any{"name": tc.module}},
				"context_options": []any{"no_yanglibrary"}, "steps": tc.steps}
			if tc.id == "foreign-leafref-target-disables-linking" {
				p["modules"] = []any{map[string]any{"name": "pv2-detached"}, map[string]any{"name": "pv2-links"}}
				p["context_options"] = []any{"no_yanglibrary", "leafref_linking"}
			}
			if tc.id == "foreign-free-meta-collection" || tc.id == "foreign-change-term-collection" {
				steps := tc.steps
				diff := steps[0].(map[string]any)
				diff["target"] = map[string]any{"searchdirs": []any{"schemas", "../ietf"},
					"modules":         []any{map[string]any{"name": "pv2-detached"}, map[string]any{"name": "pv2-mdapi"}},
					"context_options": []any{"no_yanglibrary"}}
			}
			if tc.id == "foreign-choice-schema-lookup" || tc.id == "foreign-operation-context-identity" ||
				tc.id == "foreign-revision-must" || tc.id == "mixed-opaque-print-context" ||
				tc.id == "mixed-opaque-xml-print-context" ||
				tc.id == "foreign-leaflist-hash-by-lyb" || tc.id == "foreign-leafref-owner-context" ||
				tc.id == "foreign-list-key-hash-by-lyb" {
				p["searchdirs"] = []any{"schemas/ctx-a"}
				p["modules"] = []any{map[string]any{"name": "aa"}, map[string]any{"name": tc.module}}
			}
			if tc.id == "mixed-root-diff-retains-source-snapshots" {
				p["searchdirs"] = []any{"schemas/ctx-a", "schemas"}
				p["modules"] = []any{map[string]any{"name": "aa"}, map[string]any{"name": "pv2-detached"}}
			}
			got, err := (Yang{}).Run(Request{ID: tc.id, BaseDir: base, Params: p})
			if err != nil {
				t.Fatal(err)
			}
			want, err := nativeRequestRunner(t)(base, p)
			if err != nil {
				t.Fatal(err)
			}
			if tc.id == "foreign-leafref-owner-context" && want.Verdict() != "valid" {
				t.Fatalf("native leafref fixture did not resolve: %v", want)
			}
			want, _ = withoutSkipped(want, Yang{}.SkippedFields("sequence"))
			got, _ = withoutSkipped(got, Yang{}.SkippedFields("sequence"))
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s (%s)", diff, round12FirstDifference(normalize(want), normalize(got), ""))
			}
		})
	}
}
