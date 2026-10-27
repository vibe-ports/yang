// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The accepted D-0121 boundary is tested with independent contexts, both operand directions,
// stored leafref realtypes, equal bitmap widths with different declarations, matching plugins,
// and the canonical-ABI pairs R1-R3 at their extrema, truncation boundaries, zero address and
// zones, including multi-value runs whose rotations bring each representation's callback to the
// root. Values separated by "," form one run. No request invokes a native crashing
// representation. The goldens of R1-R3 are linux/amd64 output (D-0121 §2.2a).
func TestStage4SortNeighboursOracle(t *testing.T) {
	cases := []struct{ name, a, b, av, bv string }{
		{"signed", "int16;", "int16;", "-2", "3"},
		{"unsigned", "uint16;", "uint16;", "1", "3"},
		{"decimal", "decimal64 { fraction-digits 2; }", "decimal64 { fraction-digits 2; }", "1.25", "2.50"},
		{"bits-byte-width", "bits { bit a { position 0; } }", "bits { bit b { position 7; } }", "a", "b"},
		{"enum", "enumeration { enum a { value 1; } enum b { value 2; } }", "enumeration { enum a { value 1; } enum b { value 2; } }", "a", "b"},
		{"leafref", "uint8;", "leafref { path /anchor; require-instance false; }", "1", "2"},
		{"inet", "inet:ipv4-address;", "inet:ipv4-address;", "192.0.2.2", "192.0.2.10"},
		{"plugin-shared-handler", "yang:hex-string;", "yang:phys-address;", "aa", "bb"},
		{"plugin-timestamp", "yang:date-and-time;", "yang:date-and-time;", "2024-01-01T00:00:00Z", "2024-01-02T00:00:00Z"},
		{"plugin-date", "yang:date;", "yang:date;", "2024-01-01Z", "2024-01-02Z"},
		{"plugin-date-no-zone", "yang:date-no-zone;", "yang:date-no-zone;", "2024-01-01", "2024-01-02"},
		{"R1-truncation", "int8;", "int16;", "-1,0,1", "256,-1,255,257,-256"},
		{"R1-extremes", "int64;", "int8;", "-9223372036854775808,9223372036854775807", "127,-128,-1"},
		{"R1-unsigned", "uint8;", "uint32;", "255,0", "256,65535,4294967295,1"},
		{"R1-rotation", "int16;", "int8;", "-2", "-2,-1,1,0"},
		{"R2-signed", "int8;", "string;", "-1,0,1", "2,-1,a,"},
		{"R2-unsigned", "uint64;", "string;", "0,18446744073709551615", "0,z"},
		{"R3", "inet:ipv4-address;", "string;", "1.2.3.4,0.0.0.0", "1.2.3.4,a"},
		{"R3-zones", "inet:ipv4-address;", "string;", "0.0.0.0%eth0,1.2.3.4%z", ",b"},
	}
	for _, tc := range cases {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse=%t", tc.name, reverse), func(t *testing.T) {
				base := t.TempDir()
				types := []string{tc.a, tc.b}
				values := []string{tc.av, tc.bv}
				if reverse {
					types[0], types[1] = types[1], types[0]
					values[0], values[1] = values[1], values[0]
				}
				for i, dir := range []string{"a", "b"} {
					if err := os.Mkdir(filepath.Join(base, dir), 0o755); err != nil {
						t.Fatal(err)
					}
					module := `module sort-neighbour { yang-version 1.1; namespace "urn:sort-neighbour"; prefix s;
 import ietf-inet-types { prefix inet; } import ietf-yang-types { prefix yang; }
 leaf anchor { type uint8; } container c { leaf-list v { type ` + types[i] + ` } } }`
					if err := os.WriteFile(filepath.Join(base, dir, "sort-neighbour.yang"), []byte(module), 0o644); err != nil {
						t.Fatal(err)
					}
					for _, model := range []string{"ietf-inet-types@2025-12-22.yang", "ietf-yang-types@2025-12-22.yang"} {
						raw, err := os.ReadFile(filepath.Join("..", "internal", "models", "libyang", model))
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(base, dir, model), raw, 0o644); err != nil {
							t.Fatal(err)
						}
					}
				}
				module := []any{map[string]any{"name": "sort-neighbour"}}
				wrap := func(vs string) string {
					out := `<c xmlns="urn:sort-neighbour">`
					for _, v := range strings.Split(vs, ",") {
						out += `<v>` + v + `</v>`
					}
					return out + `</c>`
				}
				oracleSequence(t, base, "ut-diff/stage4-neighbour-"+tc.name, map[string]any{"op": "sequence", "searchdirs": []any{"a"}, "modules": module, "steps": []any{
					map[string]any{"do": "parse", "format": "xml", "data_type": "data", "parse_only": true, "data": wrap(values[0])},
					map[string]any{"do": "diff", "format": "xml", "data_type": "data", "parse_only": true, "data": wrap(values[1]), "target": map[string]any{"searchdirs": []any{"b"}, "modules": module}},
				}})
			})
		}
	}
	// Same-context union dispatch and user-order operations remain native-defined. A foreign
	// user-order run is supported when it is populated on only one side.
	for _, typ := range []string{"union { type int8; type string; }", "string; ordered-by user;"} {
		for _, foreign := range []bool{false, true} {
			for _, emptyFirst := range []bool{false, true} {
				if foreign && typ == "union { type int8; type string; }" {
					continue
				}
				base := t.TempDir()
				module := `module sort-neighbour { yang-version 1.1; namespace "urn:sort-neighbour"; prefix s; leaf-list v { type ` + typ + ` } }`
				if err := os.WriteFile(filepath.Join(base, "sort-neighbour.yang"), []byte(module), 0o644); err != nil {
					t.Fatal(err)
				}
				first, second := `<v xmlns="urn:sort-neighbour">1</v>`, `<v xmlns="urn:sort-neighbour">2</v>`
				if foreign {
					if emptyFirst {
						first = ""
					} else {
						second = ""
					}
				}
				diff := map[string]any{"do": "diff", "format": "xml", "data_type": "data", "parse_only": true, "data": second}
				if foreign {
					diff["target"] = map[string]any{"searchdirs": []any{"."}, "modules": []any{map[string]any{"name": "sort-neighbour"}}}
				}
				oracleSequence(t, base, "ut-diff/stage4-userord-neighbour", map[string]any{"op": "sequence", "searchdirs": []any{"."}, "modules": []any{map[string]any{"name": "sort-neighbour"}}, "steps": []any{
					map[string]any{"do": "parse", "format": "xml", "data_type": "data", "parse_only": true, "data": first}, diff,
				}})
			}
		}
	}
	// Native rb_find's duplicate predecessor/successor search and ordinary removals.
	base := t.TempDir()
	module := `module sort-neighbour { yang-version 1.1; namespace "urn:sort-neighbour"; prefix s;
 list l { config false; key k; leaf k { type uint8; } leaf p { type uint8; } } }`
	if err := os.WriteFile(filepath.Join(base, "sort-neighbour.yang"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	oracleSequence(t, base, "seq/stage4-equal-removal-neighbour", map[string]any{"op": "sequence", "searchdirs": []any{"."}, "modules": []any{map[string]any{"name": "sort-neighbour"}}, "steps": []any{
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true, "data": `{"sort-neighbour:l":[{"k":1,"p":1},{"k":1,"p":2},{"k":1,"p":3},{"k":1,"p":4},{"k":1,"p":5}]}`},
		map[string]any{"do": "edit", "delete": "/sort-neighbour:l[p='4']"}, map[string]any{"do": "dump"},
	}})
}

// A later key's profile is checked, but its value callback is not invoked once the first key
// decides the ordering. Sorting the empty time-period itself would be outside D-0121.
func TestStage4UnusedKeyOracle(t *testing.T) {
	base := t.TempDir()
	const module = `module refusal-unused-key { yang-version 1.1; namespace "urn:refusal-unused-key"; prefix r;
 import libnetconf2-netconf-server { prefix ln2; }
 container c { list l { key "a b"; leaf a { type uint8; } leaf b { type ln2:time-period; } } } }`
	const period = `module libnetconf2-netconf-server { yang-version 1.1; namespace "urn:cesnet:libnetconf2-netconf-server"; prefix ln2;
 revision 2026-04-17; typedef time-period { type string; } }`
	for _, dir := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{"refusal-unused-key.yang": module, "libnetconf2-netconf-server@2026-04-17.yang": period} {
			if err := os.WriteFile(filepath.Join(base, dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, values := range [][2]string{{"1", "2"}, {"2", "1"}} {
		wrap := func(v string) string { return `<c xmlns="urn:refusal-unused-key"><l><a>` + v + `</a><b/></l></c>` }
		modules := []any{map[string]any{"name": "refusal-unused-key"}}
		oracleSequence(t, base, "ut-diff/stage4-unused-key", map[string]any{"op": "sequence", "searchdirs": []any{"a"}, "modules": modules, "steps": []any{
			map[string]any{"do": "parse", "format": "xml", "data_type": "data", "parse_only": true, "data": wrap(values[0])},
			map[string]any{"do": "diff", "format": "xml", "data_type": "data", "parse_only": true, "data": wrap(values[1]), "target": map[string]any{"searchdirs": []any{"b"}, "modules": modules}},
		}})
	}
}

// TestStage4UserordPreconditionsOracle: distinct compiled schema identities are no refusal
// reason (D-0121 §2.3). The in-range cross-context histories of data/TestStage4UserordPreconditions
// diff like libyang, including the stale second-pass entry of {a,b}/{b}.
func TestStage4UserordPreconditionsOracle(t *testing.T) {
	for _, list := range []bool{false, true} {
		base := t.TempDir()
		stmt := `leaf-list v { ordered-by user; type string; }`
		if list {
			stmt = `list v { key k; ordered-by user; leaf k { type string; } }`
		}
		module := `module uo-pre { yang-version 1.1; namespace "urn:uo-pre"; prefix u; ` + stmt + ` }`
		if err := os.WriteFile(filepath.Join(base, "uo-pre.yang"), []byte(module), 0o644); err != nil {
			t.Fatal(err)
		}
		doc := func(vs []string) string {
			out := ""
			for _, v := range vs {
				if list {
					v = `<k>` + v + `</k>`
				}
				out += `<v xmlns="urn:uo-pre">` + v + `</v>`
			}
			return out
		}
		modules := []any{map[string]any{"name": "uo-pre"}}
		for _, pair := range [][2][]string{{{"a"}, {"a"}}, {{"a"}, {"b"}}, {{"a", "b"}, {"b"}}, {{"a"}, {"a", "b"}}} {
			t.Run(fmt.Sprintf("list=%t/%v", list, pair), func(t *testing.T) {
				oracleSequence(t, base, "ut-diff/stage4-userord-preconditions", map[string]any{"op": "sequence", "searchdirs": []any{"."}, "modules": modules, "steps": []any{
					map[string]any{"do": "parse", "format": "xml", "data_type": "data", "parse_only": true, "data": doc(pair[0])},
					map[string]any{"do": "diff", "format": "xml", "data_type": "data", "parse_only": true, "data": doc(pair[1]),
						"target": map[string]any{"searchdirs": []any{"."}, "modules": modules}},
				}})
			})
		}
	}
}
