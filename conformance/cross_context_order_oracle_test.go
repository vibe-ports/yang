// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestDiffReviewOracleCases keeps the ordering, indexed-miss, and small retained-delta request
// shapes live. Their libyang implementation behavior has no hand-written golden here.
func TestDiffReviewOracleCases(t *testing.T) {
	base := filepath.Join("corpus", "ut-diff")
	tests := []struct {
		name, module, firstDir, secondDir, first, second, order string
		foreign                                                 bool
	}{
		{
			name: "mixed-width-rb-rotation", module: "diff-cross-context-int-width-rb-order",
			firstDir: "schemas/cross-context-int-width-rb-order-a", secondDir: "schemas/cross-context-int-width-rb-order-b",
			first:  `<top xmlns="urn:vibe-ports:yang:diff-cross-context-int-width-rb-order"><v>1</v></top>`,
			second: `<top xmlns="urn:vibe-ports:yang:diff-cross-context-int-width-rb-order"><v>-2</v><v>-1</v><v>0</v></top>`,
			order:  ">1</v>|>-2</v>|>-1</v>|>0</v>", foreign: true,
		},
		{
			name: "integer-callback-zeroed-string", module: "diff-cross-context-int-string-order",
			firstDir: "schemas/cross-context-int-string-order-a", secondDir: "schemas/cross-context-int-string-order-b",
			first:  `<top xmlns="urn:vibe-ports:yang:diff-cross-context-int-string-order"><v>-1</v></top>`,
			second: `<top xmlns="urn:vibe-ports:yang:diff-cross-context-int-string-order"><v>2</v></top>`,
			order:  ">-1</v>|>2</v>", foreign: true,
		},
		{
			name: "integer-callback-boolean-payload", module: "diff-cross-context-int-boolean-order",
			firstDir: "schemas/cross-context-int-boolean-order-a", secondDir: "schemas/cross-context-int-boolean-order-b",
			first:  `<top xmlns="urn:vibe-ports:yang:diff-cross-context-int-boolean-order"><v>-1</v></top>`,
			second: `<top xmlns="urn:vibe-ports:yang:diff-cross-context-int-boolean-order"><v>true</v></top>`,
			order:  ">-1</v>|>true</v>", foreign: true,
		},
		{
			name: "disjoint-index-misses", module: "diff-cross-context-foreign-creates-toplevel",
			firstDir: "schemas/cross-context-foreign-creates-toplevel-a", secondDir: "schemas/cross-context-foreign-creates-toplevel-b",
			first:   `<a xmlns="urn:vibe-ports:yang:diff-cross-context-foreign-creates-toplevel">A</a><b xmlns="urn:vibe-ports:yang:diff-cross-context-foreign-creates-toplevel">B</b>`,
			second:  `<c xmlns="urn:vibe-ports:yang:diff-cross-context-foreign-creates-toplevel">C</c><d xmlns="urn:vibe-ports:yang:diff-cross-context-foreign-creates-toplevel">D</d>`,
			foreign: true,
		},
		{
			name: "same-context-delta", module: "defaults", firstDir: "schemas",
			first:  `<df xmlns="urn:libyang:tests:defaults"><foo>1</foo></df>`,
			second: `<df xmlns="urn:libyang:tests:defaults"><foo>2</foo></df>`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{
				"op": "sequence", "searchdirs": []any{tc.firstDir},
				"modules": []any{map[string]any{"name": tc.module}},
				"steps": []any{
					map[string]any{"do": "parse", "format": "xml", "data_type": "data", "parse_only": true, "data": tc.first},
					map[string]any{"do": "diff", "format": "xml", "data_type": "data", "parse_only": true,
						"data": tc.second},
				},
			}
			if tc.foreign {
				steps := params["steps"].([]any)
				steps[1].(map[string]any)["target"] = map[string]any{"searchdirs": []any{tc.secondDir},
					"modules": []any{map[string]any{"name": tc.module}}}
			}
			got, err := (Yang{}).Run(Request{ID: "ut-diff/" + tc.name, BaseDir: base, Params: params})
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
				t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
			}
			if tc.order != "" {
				steps := want["steps"].([]any)
				diff := steps[1].(map[string]any)["diff"].(map[string]any)["xml"].(string)
				at := 0
				for _, value := range strings.Split(tc.order, "|") {
					next := strings.Index(diff[at:], value)
					if next < 0 {
						t.Fatalf("oracle diff order does not contain %q after byte %d:\n%s", value, at, diff)
					}
					at += next + len(value)
				}
			}
		})
	}
}

type oracleBuiltInCase struct {
	name, declaration, first, second string
}

var oracleBuiltIns = []oracleBuiltInCase{
	{"binary", "binary", "AQ==", "Ag=="},
	{"uint8", "uint8", "1", "2"}, {"uint16", "uint16", "1", "2"},
	{"uint32", "uint32", "1", "2"}, {"uint64", "uint64", "1", "2"},
	{"string", "string", "a", "b"},
	{"bits", "bits { bit a { position 0; } bit b { position 1; } }", "a", "b"},
	{"boolean", "boolean", "false", "true"},
	{"decimal64", "decimal64 { fraction-digits 1; }", "1.0", "2.0"},
	{"empty", "empty", "", ""},
	{"enumeration", "enumeration { enum a { value 1; } enum b { value 2; } }", "a", "b"},
	{"identityref", "identityref { base root; }", "id-a", "id-b"},
	{"instance-identifier", "instance-identifier { require-instance false; }", "/m:top/m:anchor", "/m:top/m:other"},
	{"leafref", "leafref { path /top/anchor; require-instance false; }", "a", "b"},
	{"union", "union { type int8; type string; }", "-1", "2"},
	{"int8", "int8", "-1", "2"}, {"int16", "int16", "-1", "2"},
	{"int32", "int32", "-1", "2"}, {"int64", "int64", "-1", "2"},
}

func oracleBuiltInSupported(first, second string) bool {
	switch first {
	case "string", "empty", "instance-identifier", "leafref":
		return true
	case "int8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "uint64", "boolean", "decimal64":
		switch second {
		case "int8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "uint64", "boolean", "decimal64", "string", "empty", "leafref":
			return true
		}
	}
	return first == second
}

func oracleTypeStatement(declaration string) string {
	if strings.Contains(declaration, "{") {
		return "type " + declaration
	}
	return "type " + declaration + ";"
}

func oracleMatrixModule(first oracleBuiltInCase, seconds []oracleBuiltInCase, source bool) string {
	var b strings.Builder
	b.WriteString(`module diff-cross-context-built-in-matrix { yang-version 1.1;
  namespace "urn:vibe-ports:yang:diff-cross-context-built-in-matrix"; prefix m;
  identity root; identity id-a { base root; } identity id-b { base root; }
  container top { presence "retain"; leaf anchor { type string; } leaf other { type string; }
`)
	for _, second := range seconds {
		typ := second
		if source {
			typ = first
		}
		b.WriteString("leaf-list v_" + strings.ReplaceAll(second.name, "-", "_") + " { " + oracleTypeStatement(typ.declaration) + " }\n")
	}
	b.WriteString("} }")
	return b.String()
}

func oracleMatrixXML(first oracleBuiltInCase, seconds []oracleBuiltInCase, source bool) string {
	var b strings.Builder
	b.WriteString(`<top xmlns="urn:vibe-ports:yang:diff-cross-context-built-in-matrix" xmlns:m="urn:vibe-ports:yang:diff-cross-context-built-in-matrix"><anchor>a</anchor><other>b</other>`)
	for _, second := range seconds {
		value := second.second
		if source {
			value = first.first
		}
		name := "v_" + strings.ReplaceAll(second.name, "-", "_")
		if value == "" {
			b.WriteString("<" + name + "/>")
		} else {
			b.WriteString("<" + name + ">" + value + "</" + name + ">")
		}
	}
	b.WriteString("</top>")
	return b.String()
}

// TestDiffBuiltInCallbackMatrixOracle compares every defined built-in callback/storage pair in
// 19 batched live-oracle requests. The complementary unsafe pairs are exhaustively required to
// return D-0121 by data/TestDiffCrossContextBuiltInTypeMatrix.
func TestDiffBuiltInCallbackMatrixOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	base := t.TempDir()
	for _, first := range oracleBuiltIns {
		var seconds []oracleBuiltInCase
		for _, second := range oracleBuiltIns {
			if oracleBuiltInSupported(first.name, second.name) {
				seconds = append(seconds, second)
			}
		}
		t.Run(first.name, func(t *testing.T) {
			firstDir, secondDir := first.name+"-a", first.name+"-b"
			for _, dir := range []string{firstDir, secondDir} {
				if err := os.Mkdir(filepath.Join(base, dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			moduleFile := "diff-cross-context-built-in-matrix.yang"
			if err := os.WriteFile(filepath.Join(base, firstDir, moduleFile), []byte(oracleMatrixModule(first, seconds, true)), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(base, secondDir, moduleFile), []byte(oracleMatrixModule(first, seconds, false)), 0o644); err != nil {
				t.Fatal(err)
			}
			params := map[string]any{
				"op": "sequence", "searchdirs": []any{firstDir},
				"modules": []any{map[string]any{"name": "diff-cross-context-built-in-matrix"}},
				"steps": []any{
					map[string]any{"do": "parse", "format": "xml", "data_type": "data", "parse_only": true,
						"data": oracleMatrixXML(first, seconds, true)},
					map[string]any{"do": "diff", "format": "xml", "data_type": "data", "parse_only": true,
						"data": oracleMatrixXML(first, seconds, false), "target": map[string]any{
							"searchdirs": []any{secondDir}, "modules": []any{map[string]any{"name": "diff-cross-context-built-in-matrix"}}}},
				},
			}
			got, err := (Yang{}).Run(Request{ID: "ut-diff/built-in-matrix-" + first.name, BaseDir: base, Params: params})
			if err != nil {
				t.Fatal(err)
			}
			want, err := oracle(base, params)
			if err != nil {
				t.Fatal(err)
			}
			want, _ = withoutSkipped(want, Yang{}.SkippedFields("sequence"))
			got, _ = withoutSkipped(got, Yang{}.SkippedFields("sequence"))
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
			}
		})
	}
}

// TestRetainedRBUnlinkOracle exercises repeated lyd_change_term unlink/reinsert operations and a
// final lyds_unlink while another member keeps the RB group alive. Heap retention is asserted by
// the Go weak-pointer regression; this live request pins the corresponding observable behavior.
func TestRetainedRBUnlinkOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	base := t.TempDir()
	const moduleName = "retained-rb-unlink"
	if err := os.WriteFile(filepath.Join(base, moduleName+".yang"), []byte(`module retained-rb-unlink {
  yang-version 1.1; namespace "urn:vibe-ports:yang:retained-rb-unlink"; prefix r;
  leaf-list v { type uint32; }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	steps := []any{map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
		"data": `{"retained-rb-unlink:v":[0,1]}`}}
	current := 1
	for next := 2; next < 34; next++ {
		steps = append(steps, map[string]any{"do": "change_term", "node": "/retained-rb-unlink:v[.='" + strconv.Itoa(current) + "']", "value": strconv.Itoa(next)})
		current = next
	}
	steps = append(steps,
		map[string]any{"do": "unlink", "node": "/retained-rb-unlink:v[.='" + strconv.Itoa(current) + "']"},
		map[string]any{"do": "dump", "with_defaults": "explicit"})
	params := map[string]any{"op": "sequence", "searchdirs": []any{"."},
		"modules": []any{map[string]any{"name": moduleName}}, "steps": steps}
	got, err := (Yang{}).Run(Request{ID: "ut-diff/retained-rb-unlink", BaseDir: base, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	want, err := oracle(base, params)
	if err != nil {
		t.Fatal(err)
	}
	want, _ = withoutSkipped(want, Yang{}.SkippedFields("sequence"))
	got, _ = withoutSkipped(got, Yang{}.SkippedFields("sequence"))
	if diff := diffResponses(want, got); diff != "" {
		t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
	}
}
