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

// TestStage3NewPathJournalOracle: multi-segment lyd_new_path histories whose last segment's
// value is rejected after the first creation (lyd_new_path frees what it created), then a
// further creation, compared step by step with linux/amd64 libyang 5.8.6: wide union keys,
// inet keys, 4 KiB keys, a run's first RB tree, a new run leader, and a root list put beside a
// nested parent (mixed marking) into an indexed and an unindexed destination. No golden files
// are created.
func TestStage3NewPathJournalOracle(t *testing.T) {
	base := t.TempDir()
	var u strings.Builder
	for i := range 1023 {
		fmt.Fprintf(&u, "type enumeration {enum e%d;} ", i)
	}
	module := `module stage3-npj { yang-version 1.1; namespace "urn:stage3-npj"; prefix s;
 import ietf-inet-types { prefix inet; }
 typedef wide { type union { ` + u.String() + `type string; } }
 container c {
  list u { key k; leaf k { type wide; } leaf v { type uint8; } }
  list p { key k; leaf k { type inet:ipv4-address; } leaf v { type uint8; } }
  list s { key k; leaf k { type string; } leaf v { type uint8; } }
  leaf-list w { type string; }
  container d { leaf y { type string; } }
 }
 list r { key k; leaf k { type string; } leaf v { type uint8; } } }`
	if err := os.WriteFile(filepath.Join(base, "stage3-npj.yang"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	long := func(c string) string { return strings.Repeat(c, 4096) }
	parse := func(data string) any {
		return map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true, "data": data}
	}
	set := func(path, value, parent string) any {
		x := map[string]any{"path": path, "value": value}
		if parent != "" {
			x["parent"] = parent
		}
		return map[string]any{"do": "edit", "set": x}
	}
	dump := map[string]any{"do": "dump"}
	var ws []string
	for i := range 12 {
		ws = append(ws, fmt.Sprintf(`"w%02d"`, i))
	}
	for _, tc := range []struct{ name, data, parent, path, then string }{
		{"union", `{"stage3-npj:c":{"u":[{"k":"1"},{"k":"2"}]}}`, "", "/stage3-npj:c/u[k='3']/v", "/stage3-npj:c/u[k='0']/v"},
		{"plugin", `{"stage3-npj:c":{"p":[{"k":"192.0.2.1"},{"k":"192.0.2.2"}]}}`, "", "/stage3-npj:c/p[k='192.0.2.3']/v", "/stage3-npj:c/p[k='192.0.2.0']/v"},
		{"long", `{"stage3-npj:c":{"s":[{"k":"` + long("a") + `"},{"k":"` + long("b") + `"}]}}`, "",
			"/stage3-npj:c/s[k='" + long("z") + "']/v", "/stage3-npj:c/s[k='" + long("0") + "']/v"},
		{"first-tree", `{"stage3-npj:c":{"s":[{"k":"m"}]}}`, "", "/stage3-npj:c/s[k='a']/v", "/stage3-npj:c/s[k='b']/v"},
		{"new-leader", `{"stage3-npj:c":{"s":[{"k":"m"},{"k":"n"},{"k":"o"}]}}`, "", "/stage3-npj:c/s[k='a']/v", "/stage3-npj:c/s[k='0']/v"},
		{"mixed-indexed", `{"stage3-npj:c":{"w":[` + strings.Join(ws, ",") + `],"d":{"y":"1"}}}`, "/stage3-npj:c/d",
			"/stage3-npj:r[k='x']/v", "/stage3-npj:r[k='y']/v"},
		{"mixed-unindexed", `{"stage3-npj:c":{"d":{"y":"1"}}}`, "/stage3-npj:c/d", "/stage3-npj:r[k='x']/v", "/stage3-npj:r[k='y']/v"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oracleSequence(t, base, "stage3/newpath-journal-"+tc.name, map[string]any{
				"op": "sequence", "context_options": []any{"no_yanglibrary"}, "searchdirs": []any{"."},
				"modules": []any{map[string]any{"name": "stage3-npj"}},
				"steps": []any{parse(tc.data), set(tc.path, "300", tc.parent), dump, set(tc.then, "1", tc.parent), dump,
					set(tc.path, "30", tc.parent), dump}})
		})
	}
}
