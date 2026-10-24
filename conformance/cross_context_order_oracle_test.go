// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestRetainedRBRound5Oracle compares the new-minimum metadata move and sampled merge-then-
// foreign-insert sequences directly with libyang v5.8.6. The seeded samples are the live-oracle
// counterpart of data/TestRetainedRBRandomMixedContextMutations.
func TestRetainedRBRound5Oracle(t *testing.T) {
	base := t.TempDir()
	const moduleName = "retained-rb-round5"
	if err := os.WriteFile(filepath.Join(base, moduleName+".yang"), []byte(`module retained-rb-round5 {
  yang-version 1.1; namespace "urn:vibe-ports:yang:retained-rb-round5"; prefix r;
  container c { leaf-list v { type uint32; } }
  leaf-list top { type uint32; }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := map[string]any{"searchdirs": []any{"."}, "modules": []any{map[string]any{"name": moduleName}}}
	parse := func(data string) any {
		return map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true, "data": data}
	}
	compare := func(id string, steps []any) {
		t.Helper()
		params := map[string]any{"op": "sequence", "searchdirs": []any{"."},
			"modules": []any{map[string]any{"name": moduleName}}, "steps": steps}
		got, err := (Yang{}).Run(Request{ID: id, BaseDir: base, Params: params})
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
	}
	t.Run("new-minimum", func(t *testing.T) {
		compare("ut-diff/retained-rb-new-minimum", []any{
			parse(`{"retained-rb-round5:c":{"v":[5]}}`),
			map[string]any{"do": "diff", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"retained-rb-round5:c":{"v":[1,7]}}`, "target": ctx},
		})
	})
	t.Run("foreign-scaling", func(t *testing.T) {
		var values strings.Builder
		values.WriteString(`{"retained-rb-round5:c":{"v":[`)
		for value := 1; value <= 64; value++ {
			if value != 1 {
				values.WriteByte(',')
			}
			fmt.Fprint(&values, value)
		}
		values.WriteString(`]}}`)
		compare("ut-diff/retained-rb-foreign-scaling", []any{
			parse(`{"retained-rb-round5:c":{"v":[0]}}`),
			map[string]any{"do": "diff", "format": "json", "data_type": "data", "parse_only": true,
				"data": values.String(), "target": ctx},
		})
	})
	rng := rand.New(rand.NewPCG(0x301, 5)) //nolint:gosec // deterministic oracle samples
	for sample := range 8 {
		created := 4 + rng.IntN(28)
		id := fmt.Sprintf("ut-diff/retained-rb-merge-foreign-%02d", sample)
		t.Run(fmt.Sprintf("merge-foreign-%02d", sample), func(t *testing.T) {
			compare(id, []any{
				map[string]any{"do": "diff", "format": "json", "data_type": "data", "parse_only": true,
					"data": fmt.Sprintf(`{"retained-rb-round5:top":[%d]}`, created), "target": ctx},
				parse(`{"retained-rb-round5:top":[1,3]}`),
				map[string]any{"do": "edit", "format": "json", "data_type": "data",
					"merge": `{"retained-rb-round5:top":[5,7,9,11]}`},
				map[string]any{"do": "diff_apply"},
				map[string]any{"do": "dump"},
			})
		})
	}
}

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
	store := func(name string) string {
		switch name {
		case "int8", "int16", "int32", "int64":
			return "int"
		case "uint8", "uint16", "uint32", "uint64":
			return "uint"
		case "leafref":
			return "string"
		default:
			return name
		}
	}
	if store(first) == store(second) {
		return true
	}
	scalar := func(name string) bool {
		switch name {
		case "int8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "uint64",
			"boolean", "decimal64", "string", "empty", "leafref":
			return true
		}
		return false
	}
	return scalar(first) && scalar(second)
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
// 19 batched live-oracle requests. The complementary unsafe cross-callback pairs are exhaustively
// required to return D-0121 by data/TestDiffCrossContextBuiltInTypeMatrix.
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

func compareCrossContextLive(t *testing.T, base string, params map[string]any) map[string]any {
	t.Helper()
	got, err := (Yang{}).Run(Request{ID: "ut-diff/" + t.Name(), BaseDir: base, Params: params})
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
	return want
}

// TestDiffApplyTopLevelInetRunOracle pins lyds_insert for an equivalent top-level run. The
// address comparator orders .2 before .10 even though their canonical strings order oppositely.
func TestDiffApplyTopLevelInetRunOracle(t *testing.T) {
	base := t.TempDir()
	const moduleName = "diff-cross-context-top-level-inet"
	module := `module ` + moduleName + ` { yang-version 1.1;
  namespace "urn:vibe-ports:yang:diff-cross-context-top-level-inet"; prefix m;
  import ietf-inet-types { prefix inet; }
  leaf-list address { type inet:ipv4-address-no-zone; }
}`
	inet, err := os.ReadFile(filepath.Join("..", "internal", "models", "libyang", "ietf-inet-types@2025-12-22.yang"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"a", "b"} {
		path := filepath.Join(base, dir)
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, moduleName+".yang"), []byte(module), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "ietf-inet-types@2025-12-22.yang"), inet, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	first := `<address xmlns="urn:vibe-ports:yang:diff-cross-context-top-level-inet">192.0.2.2</address>`
	second := first + `<address xmlns="urn:vibe-ports:yang:diff-cross-context-top-level-inet">192.0.2.10</address>`
	params := map[string]any{
		"op": "sequence", "searchdirs": []any{"a"},
		"modules": []any{map[string]any{"name": moduleName}},
		"steps": []any{
			map[string]any{"do": "parse", "format": "xml", "data_type": "data", "parse_only": true, "data": first},
			map[string]any{"do": "diff", "format": "xml", "data_type": "data", "parse_only": true,
				"data": second, "target": map[string]any{"searchdirs": []any{"b"}, "modules": []any{map[string]any{"name": moduleName}}}},
			map[string]any{"do": "diff_apply"},
			map[string]any{"do": "dump"},
		},
	}
	compareCrossContextLive(t, base, params)
}

// TestDiffDeepLineageOracle verifies that interned lineage identities preserve native equality;
// the larger Go regression separately enforces the memory and lookup-work bounds.
func TestDiffDeepLineageOracle(t *testing.T) {
	const depth = 64
	name := strings.Repeat("n", 256)
	leafPrefix := name[:len(name)-1]
	var module, xml strings.Builder
	module.WriteString(`module diff-cross-context-deep-lineage { namespace "urn:vibe-ports:yang:diff-cross-context-deep-lineage"; prefix m; `)
	for range depth {
		module.WriteString("container " + name + " {")
		xml.WriteByte('<')
		xml.WriteString(name)
		if xml.Len() == len(name)+1 {
			xml.WriteString(` xmlns="urn:vibe-ports:yang:diff-cross-context-deep-lineage"`)
		}
		xml.WriteByte('>')
	}
	for _, suffix := range []byte{'a', 'b', 'c', 'd'} {
		leaf := leafPrefix + string(suffix)
		module.WriteString("leaf " + leaf + " { type string; }")
		xml.WriteString("<" + leaf + ">x</" + leaf + ">")
	}
	for range depth {
		module.WriteByte('}')
		xml.WriteString("</" + name + ">")
	}
	module.WriteByte('}')

	base := t.TempDir()
	for _, dir := range []string{"a", "b"} {
		path := filepath.Join(base, dir)
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "diff-cross-context-deep-lineage.yang"), []byte(module.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	params := map[string]any{
		"op": "sequence", "searchdirs": []any{"a"},
		"modules": []any{map[string]any{"name": "diff-cross-context-deep-lineage"}},
		"steps": []any{
			map[string]any{"do": "parse", "format": "xml", "data_type": "data", "parse_only": true, "data": xml.String()},
			map[string]any{"do": "diff", "format": "xml", "data_type": "data", "parse_only": true,
				"data": xml.String(), "target": map[string]any{"searchdirs": []any{"b"}, "modules": []any{map[string]any{"name": "diff-cross-context-deep-lineage"}}}},
		},
	}
	compareCrossContextLive(t, base, params)
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
