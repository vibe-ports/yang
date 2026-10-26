// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

// Complete histories compare intermediate typed trees, diagnostics, JSON and XML directly
// with linux/amd64 libyang 5.8.6. No golden files are created.
func TestStage3MutationOracle(t *testing.T) {
	base := t.TempDir()
	const module = `module stage3 { yang-version 1.1; namespace "urn:stage3"; prefix s;
 leaf-list d { type uint32; default 2; default 8; }
 notification n;
 container c { leaf gate {type string;default "off";} leaf required {when "../gate = 'on'";type string;mandatory true;} leaf-list v { type uint32; } list l { key "a b";
 leaf a { type uint32; } leaf b { type string; } leaf p { type string; } } }
 list ops {key k;leaf k {type uint32;}action act {input {leaf x {type string;}}}}
 anydata payload;
 rpc r { output { leaf-list v { type uint32; } list l {key "a b";
 leaf a {type uint32;} leaf b {type string;} leaf p {type string;} } } }
 }`
	if err := os.WriteFile(filepath.Join(base, "stage3.yang"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	parse := func(data string) any {
		return map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true, "data": data}
	}
	edit := func(data string) any {
		return map[string]any{"do": "edit", "format": "json", "data_type": "data", "merge": data}
	}
	dump := map[string]any{"do": "dump"}
	run := func(t *testing.T, name string, steps []any) {
		t.Helper()
		oracleSequence(t, base, "stage3/"+name, map[string]any{
			"op": "sequence", "context_options": []any{"no_yanglibrary"}, "searchdirs": []any{"."}, "modules": []any{map[string]any{"name": "stage3"}}, "steps": steps})
	}
	t.Run("remove-root-minimum-two-child-singleton", func(t *testing.T) {
		run(t, "removals", []any{
			parse(`{"stage3:c":{"v":[7,3,11,1,5,9,13]}}`),
			map[string]any{"do": "unlink", "node": "/stage3:c/v[.='7']"}, dump,
			map[string]any{"do": "edit", "delete": "/stage3:c/v[.='1']"},
			map[string]any{"do": "change_term", "node": "/stage3:c/v[.='3']", "value": "8"}, dump,
			map[string]any{"do": "trim", "xpath": "/stage3:c/v[.='13']"},
			edit(`{"stage3:c":{"v":[2,10,12]}}`), dump})
	})
	t.Run("parser-resume-and-cleanup", func(t *testing.T) {
		run(t, "parser-resume", []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "rpc", "parse_only": true, "data": `{"stage3:r":{}}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "reply", "parse_only": true, "parent": "/stage3:r", "data": `{"stage3:v":[11,7,15,3,9,13,17]}`}, dump,
			map[string]any{"do": "parse", "format": "json", "data_type": "reply", "parse_only": true, "parent": "/stage3:r", "data": `{"stage3:v":[8,6,12,4,10]}`}, dump,
			map[string]any{"do": "parse", "format": "json", "data_type": "reply", "parse_only": true, "parent": "/stage3:r", "data": `{"stage3:l":[{"a":5,"b":"z"},{"a":3}]}`}, dump})
	})
	t.Run("multikey-value-reinsert-and-dup", func(t *testing.T) {
		run(t, "multikey", []any{
			parse(`{"stage3:c":{"l":[{"a":5,"b":"z"},{"a":1,"b":"a"},{"a":9,"b":"m"}]}}`),
			map[string]any{"do": "change_term", "node": "/stage3:c/l[a='5'][b='z']/b", "value": "b"},
			map[string]any{"do": "dup", "parent": "/stage3:c", "siblings": true, "options": []any{"recursive"}, "node": "/stage3:c/l[a='3'][b='n']", "source": map[string]any{"format": "json", "data_type": "data", "parse_only": true, "data": `{"stage3:c":{"l":[{"a":3,"b":"n"},{"a":7,"b":"q"}]}}`}}, dump})
	})
	t.Run("payload-copy-replace-and-free", func(t *testing.T) {
		run(t, "payload", []any{
			parse(`{"stage3:payload":{"stage3:c":{"v":[11,7,15,3,9]}}}`),
			edit(`{"stage3:payload":{"stage3:c":{"v":[13,5,17,1]}}}`), dump,
			map[string]any{"do": "dup", "node": "/stage3:payload", "options": []any{"recursive"}}, dump,
			map[string]any{"do": "edit", "delete": "/stage3:payload"}, dump})
	})
	t.Run("defaults-dummy-when-path-update", func(t *testing.T) {
		run(t, "defaults-dummy-path", []any{parse(`{"stage3:c":{"v":[7,3,11]}}`),
			map[string]any{"do": "edit", "set": map[string]any{"path": "/stage3:c/gate", "value": "off", "update": true}},
			map[string]any{"do": "validate", "data_type": "data", "module": "stage3"}, dump,
			map[string]any{"do": "edit", "set": map[string]any{"path": "/stage3:c/gate", "value": "still-off", "update": true}},
			map[string]any{"do": "validate", "data_type": "data", "module": "stage3"}, dump,
			map[string]any{"do": "edit", "insert_term": map[string]any{"module": "stage3", "name": "d", "value": "4"}},
			map[string]any{"do": "validate", "data_type": "data", "module": "stage3"}, dump})
	})
	for _, tc := range []struct{ name, format, typ, input string }{
		{"netconf-cleanup", "xml", "notif-netconf", `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2013-12-21T00:01:00Z</eventTime><n xmlns="urn:stage3"/></notification>`},
		{"restconf-cleanup", "json", "notif-restconf", `{"ietf-restconf:notification":{"eventTime":"2013-12-21T00:01:00Z","stage3:n":{}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run(t, tc.name, []any{map[string]any{"do": "parse", "format": tc.format, "data_type": tc.typ, "data": tc.input, "parse_only": true}, dump})
		})
	}
	t.Run("detached-singleton-metadata", func(t *testing.T) {
		run(t, "detached-singleton", []any{
			parse(`{"stage3:c":{"l":[{"a":5,"b":"z","p":"carry"},{"a":7,"b":"z","p":"other"}]}}`),
			map[string]any{"do": "edit", "delete": "/stage3:c/l[a='7'][b='z']"},
			map[string]any{"do": "unlink", "node": "/stage3:c/l[a='5'][b='z']"},
			parse(`{"stage3:c":{}}`),
			map[string]any{"do": "insert", "parent": "/stage3:c", "detached": true},
			map[string]any{"do": "dup", "parent": "/stage3:c", "node": "/stage3:c/l[a='5'][b='z']", "options": []any{"recursive", "no_lyds"}, "source": map[string]any{"format": "json", "data_type": "data", "parse_only": true, "data": `{"stage3:c":{"l":[{"a":5,"b":"z","p":"tail"}]}}`}},
			map[string]any{"do": "insert", "parent": "/stage3:c", "node": "/stage3:c/l[a='5'][b='z']", "format": "json", "data_type": "data", "parse_only": true, "data": `{"stage3:c":{"l":[{"a":5,"b":"z","p":"new"}]}}`}, dump})
	})
	t.Run("temporary-operation-topology", func(t *testing.T) {
		steps := []any{map[string]any{"do": "parse", "format": "xml", "data_type": "rpc", "parse_only": true, "data": `<ops xmlns="urn:stage3"><k>1</k><act><x>q</x></act></ops>`}}
		for _, k := range []string{"3", "5", "7", "9", "11", "13"} {
			steps = append(steps, map[string]any{"do": "edit", "insert_list": map[string]any{"module": "stage3", "name": "ops", "keys": []any{k}}})
		}
		steps = append(steps, map[string]any{"do": "validate", "data_type": "rpc"}, dump, map[string]any{"do": "edit", "insert_list": map[string]any{"module": "stage3", "name": "ops", "keys": []any{"2"}}}, dump)
		run(t, "temporary-operation", steps)
	})
	rng := rand.New(rand.NewPCG(0x301, 3)) //nolint:gosec // persisted sampled-history seed
	for seed := range 8 {
		t.Run(fmt.Sprintf("history-%d", seed), func(t *testing.T) {
			values := rng.Perm(16)
			input := `{"stage3:c":{"v":[`
			for i, v := range values {
				if i > 0 {
					input += ","
				}
				input += fmt.Sprint(v)
			}
			input += `]}}`
			steps := []any{parse(input)}
			for _, v := range values[:8] {
				steps = append(steps, map[string]any{"do": "edit", "delete": fmt.Sprintf("/stage3:c/v[.='%d']", v)}, dump)
			}
			steps = append(steps, edit(`{"stage3:c":{"v":[21,17,19]}}`), dump)
			run(t, fmt.Sprintf("seed-301-3-%d", seed), steps)
		})
	}
}
