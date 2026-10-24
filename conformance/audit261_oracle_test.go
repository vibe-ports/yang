// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"os"
	"strings"
	"testing"
)

// These named oracle reproducers avoid generated goldens. Each runs the native
// sequence and the Go adapter against independently built contexts.
func TestAudit261Reproducers(t *testing.T) {
	source, sourceErr := os.Stat("oracle/lyoracle.c")
	binary, binaryErr := os.Stat(OracleBinary("oracle"))
	if sourceErr == nil && binaryErr == nil && source.ModTime().After(binary.ModTime()) {
		t.Skip("oracle binary predates the adapter changes; rebuild in the dev container")
	}
	oracle, base := requestOracle(t)
	a := map[string]any{"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2-detached"}},
		"context_options": []any{"no_yanglibrary"}}
	b := map[string]any{"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2-detached", "features": []any{"extra"}}},
		"context_options": []any{"no_yanglibrary"}}
	mdCtx := map[string]any{"searchdirs": []any{"schemas", "../ietf"}, "modules": []any{map[string]any{"name": "pv2-mdapi"}},
		"context_options": []any{"no_yanglibrary"}}
	metaPrefixModule := []any{map[string]any{"name": "pv2-meta-prefix"}}
	editTarget := map[string]any{"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2-edit"}},
		"context_options": []any{"no_yanglibrary"}}
	step := func(kv map[string]any) any { return kv }
	parse := func(input string) any {
		return step(map[string]any{"do": "parse", "format": "json", "data_type": "config", "parse_only": true, "data": input})
	}
	prefix := func() []any {
		return []any{
			parse(`{"pv2-detached:c":{"base":"x"}}`),
			step(map[string]any{"do": "dup", "node": "/pv2-detached:c", "options": []any{"recursive"}, "target": b}),
			step(map[string]any{"do": "edit", "set": map[string]any{"path": "/pv2-detached:c/extra", "value": "y"}}),
			step(map[string]any{"do": "unlink", "node": "/pv2-detached:c"}),
		}
	}
	withoutExtra := func() []any {
		return []any{
			parse(`{"pv2-detached:c":{"base":"x"}}`),
			step(map[string]any{"do": "dup", "node": "/pv2-detached:c", "options": []any{"recursive"}, "target": b}),
			step(map[string]any{"do": "unlink", "node": "/pv2-detached:c"}),
		}
	}
	detachedEdit := func() []any {
		return []any{
			parse(`{"pv2-edit:c":{}}`),
			step(map[string]any{"do": "dup", "node": "/pv2-edit:c", "options": []any{"recursive"}, "target": editTarget}),
			step(map[string]any{"do": "unlink", "node": "/pv2-edit:c"}),
		}
	}
	plus := func(xs []any, tail ...any) []any { return append(xs, tail...) }
	cases := []struct {
		id          string
		steps       []any
		modules     []any
		searchdirs  []any
		contextOpts []any
	}{
		{id: "unlink-before-parse", steps: []any{step(map[string]any{"do": "unlink", "node": "@top"})}},
		{id: "unlink-empty-tree", steps: []any{parse(`{}`), step(map[string]any{"do": "unlink", "node": "@top"})}},
		{id: "reverse-opaque-create", steps: []any{
			step(map[string]any{"do": "diff_parse", "format": "xml", "data_type": "config", "unknown": "opaque", "data": `<unknown xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="create">v</unknown>`}),
			step(map[string]any{"do": "diff_reverse"}),
		}, modules: mdCtx["modules"].([]any)},
		{id: "reverse-opaque-delete", steps: []any{
			step(map[string]any{"do": "diff_parse", "format": "xml", "data_type": "config", "unknown": "opaque", "data": `<unknown xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="delete">v</unknown>`}),
			step(map[string]any{"do": "diff_reverse"}),
		}, modules: mdCtx["modules"].([]any)},
		{id: "diff-opaque-change", steps: []any{
			step(map[string]any{"do": "parse", "format": "xml", "data_type": "config", "unknown": "opaque", "parse_only": true, "data": `<unknown xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi">v</unknown>`}),
			step(map[string]any{"do": "diff", "format": "xml", "data_type": "config", "unknown": "opaque", "parse_only": true, "data": `<unknown xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi">w</unknown>`}),
		}, modules: mdCtx["modules"].([]any)},
		{id: "merge-opaque-into-empty", steps: []any{
			step(map[string]any{"do": "diff_merge", "format": "xml", "data_type": "config", "unknown": "opaque", "data": `<unknown xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="create">v</unknown>`}),
		}, modules: mdCtx["modules"].([]any)},
		{id: "new-term-detached-parent", steps: []any{
			parse(`{"pv2-edit:c":{}}`), step(map[string]any{"do": "unlink", "node": "/pv2-edit:c"}),
			step(map[string]any{"do": "edit", "insert_term": map[string]any{"detached_parent": true, "name": "a", "value": "x"}}),
			step(map[string]any{"do": "detached_dump"}),
		}, modules: []any{map[string]any{"name": "pv2-edit"}}},
		{id: "new-inner-detached-parent", steps: []any{
			parse(`{"pv2-edit:c":{}}`), step(map[string]any{"do": "unlink", "node": "/pv2-edit:c"}),
			step(map[string]any{"do": "edit", "insert_inner": map[string]any{"detached_parent": true, "name": "np"}}),
			step(map[string]any{"do": "detached_dump"}),
		}, modules: []any{map[string]any{"name": "pv2-edit"}}},
		{id: "new-list-detached-parent", steps: []any{
			parse(`{"pv2-edit:c":{}}`), step(map[string]any{"do": "unlink", "node": "/pv2-edit:c"}),
			step(map[string]any{"do": "edit", "insert_list": map[string]any{"detached_parent": true, "name": "l", "keys": []any{"a"}}}),
			step(map[string]any{"do": "detached_dump"}),
		}, modules: []any{map[string]any{"name": "pv2-edit"}}},
		// lyd_new_term/inner/list3 resolve a NULL module from the detached parent's
		// schema (libyang v5.8.6 src/tree_data_new.c), including after tree becomes NULL.
		{id: "new-term-target-detached-parent", steps: plus(detachedEdit(),
			step(map[string]any{"do": "edit", "insert_term": map[string]any{"detached_parent": true, "name": "a", "value": "x"}}),
			step(map[string]any{"do": "detached_dump"})), modules: editTarget["modules"].([]any)},
		{id: "new-inner-target-detached-parent", steps: plus(detachedEdit(),
			step(map[string]any{"do": "edit", "insert_inner": map[string]any{"detached_parent": true, "name": "np"}}),
			step(map[string]any{"do": "detached_dump"})), modules: editTarget["modules"].([]any)},
		{id: "new-list-target-detached-parent", steps: plus(detachedEdit(),
			step(map[string]any{"do": "edit", "insert_list": map[string]any{"detached_parent": true, "name": "l", "keys": []any{"a"}}}),
			step(map[string]any{"do": "detached_dump"})), modules: editTarget["modules"].([]any)},
		{id: "new-term-target-detached-explicit-module", steps: plus(detachedEdit(),
			step(map[string]any{"do": "edit", "insert_term": map[string]any{"detached_parent": true, "module": "pv2-edit", "name": "a", "value": "x"}})),
			modules: editTarget["modules"].([]any)},
		// lyd_diff_is_redundant_meta checks the annotation name prefix on the
		// node and its keys, regardless of the annotation's module (src/diff.c).
		{id: "diff-merge-custom-meta-prefix-container", steps: []any{
			step(map[string]any{"do": "diff_merge", "format": "xml", "data_type": "config", "data": `<c xmlns="urn:vibe-ports:yang:conformance:pv2-meta-prefix" xmlns:yang="urn:ietf:params:xml:ns:yang:1" xmlns:pm="urn:vibe-ports:yang:conformance:pv2-meta-prefix" yang:operation="none" pm:meta-note="x"/>`}),
		}, modules: metaPrefixModule, searchdirs: []any{"schemas", "../ietf"}},
		{id: "diff-merge-custom-meta-prefix-key", steps: []any{
			step(map[string]any{"do": "diff_merge", "format": "xml", "data_type": "config", "data": `<l xmlns="urn:vibe-ports:yang:conformance:pv2-meta-prefix" xmlns:yang="urn:ietf:params:xml:ns:yang:1" xmlns:pm="urn:vibe-ports:yang:conformance:pv2-meta-prefix" yang:operation="none"><k pm:meta-note="x">a</k></l>`}),
		}, modules: metaPrefixModule, searchdirs: []any{"schemas", "../ietf"}},
		{id: "new-term-missing-detached-parent", steps: []any{
			step(map[string]any{"do": "edit", "insert_term": map[string]any{"detached_parent": true, "name": "a", "value": "x"}}),
		}, modules: []any{map[string]any{"name": "pv2-edit"}}},
		{id: "new-list2-detached-selector-rejected", steps: []any{
			step(map[string]any{"do": "edit", "insert_list2": map[string]any{"detached_parent": true, "name": "l", "keys": "[k='a']"}}),
		}, modules: []any{map[string]any{"name": "pv2-edit"}}},
		{id: "unlink-target-dup-missing-parent", steps: plus(prefix(), step(map[string]any{"do": "dup", "detached": true, "parent": "/missing"}))},
		{id: "unlink-opaque-path-xml", steps: []any{
			step(map[string]any{"do": "parse", "format": "xml", "data_type": "config", "parse_only": true, "unknown": "opaque", "data": `<unknown xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi"><child>v</child></unknown>`}),
			step(map[string]any{"do": "unlink", "node": "@top"}), step(map[string]any{"do": "detached_dump"}), step(map[string]any{"do": "detached_dump", "child": true}),
		}, modules: mdCtx["modules"].([]any)},
		{id: "unlink-opaque-path-json", steps: []any{
			step(map[string]any{"do": "parse", "format": "json", "data_type": "config", "parse_only": true, "unknown": "opaque", "data": `{"pv2-mdapi:unknown":{"child":"v"}}`}),
			step(map[string]any{"do": "unlink", "node": "@top"}), step(map[string]any{"do": "detached_dump"}), step(map[string]any{"do": "detached_dump", "child": true}),
		}, modules: mdCtx["modules"].([]any)},
		{id: "detached-defaults-present-trim", steps: []any{
			parse(`{"pv2-detached-default:c":{"t":["x"],"r":"x","iid":"/pv2-detached-default:c/t[.='x']","u":"x"}}`),
			step(map[string]any{"do": "unlink", "node": "/pv2-detached-default:c"}), step(map[string]any{"do": "detached_dump", "with_defaults": "trim"}),
		}, modules: []any{map[string]any{"name": "pv2-detached-default"}}},
		{id: "detached-defaults-present-tagged", steps: []any{
			parse(`{"pv2-detached-default:c":{"t":["x"],"r":"x","iid":"/pv2-detached-default:c/t[.='x']","u":"x"}}`),
			step(map[string]any{"do": "unlink", "node": "/pv2-detached-default:c"}), step(map[string]any{"do": "detached_dump", "with_defaults": "all-tagged"}),
		}, modules: []any{map[string]any{"name": "pv2-detached-default"}}},
		{id: "detached-defaults-absent-trim", steps: []any{
			parse(`{"pv2-detached-default:c":{"r":"x","iid":"/pv2-detached-default:c/t[.='x']","u":"x"}}`),
			step(map[string]any{"do": "unlink", "node": "/pv2-detached-default:c"}), step(map[string]any{"do": "detached_dump", "with_defaults": "trim"}),
		}, modules: []any{map[string]any{"name": "pv2-detached-default"}}},
		{id: "detached-defaults-absent-tagged", steps: []any{
			parse(`{"pv2-detached-default:c":{"r":"x","iid":"/pv2-detached-default:c/t[.='x']","u":"x"}}`),
			step(map[string]any{"do": "unlink", "node": "/pv2-detached-default:c"}), step(map[string]any{"do": "detached_dump", "with_defaults": "all-tagged"}),
		}, modules: []any{map[string]any{"name": "pv2-detached-default"}}},
		{id: "unlink-target-dup-retains-context", steps: plus(prefix(), step(map[string]any{"do": "dup", "detached": true, "options": []any{"recursive"}}))},
		{id: "unlink-target-empty-parse-reinsert", steps: plus(prefix(), parse(`{}`), step(map[string]any{"do": "insert", "detached": true}))},
		{id: "unlink-target-empty-delete-reinsert", steps: plus(prefix(), parse(`{"pv2-detached:ll":["a"]}`), step(map[string]any{"do": "edit", "delete": "/pv2-detached:ll[.='a']"}), step(map[string]any{"do": "insert", "detached": true}))},
		{id: "unlink-target-dup-incompatible-parent", steps: plus(prefix(), parse(`{"pv2-detached:c":{}}`), step(map[string]any{"do": "dup", "detached": true, "parent": "/pv2-detached:c"}))},
		{id: "unlink-target-insert-incompatible-parent", steps: plus(prefix(), parse(`{"pv2-detached:c":{}}`), step(map[string]any{"do": "insert", "detached": true, "parent": "/pv2-detached:c"}))},
		{id: "diff-detached-cross-context-identical", steps: plus(withoutExtra(), step(map[string]any{"do": "diff", "detached": true, "single": true, "format": "json", "data_type": "config", "parse_only": true, "data": `{"pv2-detached:c":{"base":"x"}}`}))},
		{id: "diff-detached-target-merge-empty", steps: plus(prefix(), step(map[string]any{"do": "diff", "detached": true, "single": true, "merge": true}))},
		{id: "diff-detached-target-reverse-apply-empty", steps: plus(prefix(), step(map[string]any{"do": "diff", "detached": true, "single": true}), step(map[string]any{"do": "diff_reverse"}), step(map[string]any{"do": "diff_apply"}))},
		// libyang v5.8.6 src/diff.c:1793-1811 compares the module pointer passed by
		// lyoracle's original request context with the diff node's owner module.
		{id: "diff-detached-target-reverse-apply-empty-filtered", steps: plus(prefix(), step(map[string]any{"do": "diff", "detached": true, "single": true}), step(map[string]any{"do": "diff_reverse"}), step(map[string]any{"do": "diff_apply", "module": "pv2-detached"}))},
		{id: "diff-step-invalid-detached-boolean", steps: []any{step(map[string]any{"do": "diff", "detached": "bad"})}},
		{id: "dup-empty-detached-register", steps: []any{step(map[string]any{"do": "dup", "detached": true})}},
		{id: "insert-empty-detached-register", steps: []any{step(map[string]any{"do": "insert", "detached": true})}},
		{id: "insert-consumed-detached-register", steps: plus(withoutExtra(), step(map[string]any{"do": "insert", "detached": true}), step(map[string]any{"do": "insert", "detached": true}))},
		{id: "dup-consumed-detached-register", steps: plus(withoutExtra(), step(map[string]any{"do": "insert", "detached": true}), step(map[string]any{"do": "dup", "detached": true}))},
		{id: "diff-detached-nested-list-delete", steps: []any{parse(`{"pv2-detached:c":{"l":[{"k":"a"}]}}`), step(map[string]any{"do": "unlink", "node": "/pv2-detached:c/l[k='a']"}), step(map[string]any{"do": "diff", "detached": true, "single": true})}},
		{id: "diff-detached-nested-leaflist-delete", steps: []any{parse(`{"pv2-detached:c":{"ll":["a"]}}`), step(map[string]any{"do": "unlink", "node": "/pv2-detached:c/ll[.='a']"}), step(map[string]any{"do": "diff", "detached": true, "single": true})}},
		{id: "diff-detached-nested-list-unchanged", steps: []any{parse(`{"pv2-detached:c":{"l":[{"k":"a"}]}}`), step(map[string]any{"do": "unlink", "node": "/pv2-detached:c/l[k='a']"}), step(map[string]any{"do": "diff", "detached": true, "single": true, "format": "json", "data_type": "config", "parse_only": true, "data": `{"pv2-detached:c":{"l":[{"k":"a"}]}}`, "data_node": "/pv2-detached:c/l[k='a']"})}},
		{id: "diff-detached-nested-list-create", steps: []any{step(map[string]any{"do": "diff", "single": true, "format": "json", "data_type": "config", "parse_only": true, "data": `{"pv2-detached:c":{"l":[{"k":"a"}]}}`, "data_node": "/pv2-detached:c/l[k='a']"})}},
		{id: "diff-detached-nested-leaflist-create", steps: []any{step(map[string]any{"do": "diff", "single": true, "format": "json", "data_type": "config", "parse_only": true, "data": `{"pv2-detached:c":{"ll":["a"]}}`, "data_node": "/pv2-detached:c/ll[.='a']"})}},
		{id: "diff-apply-latest-leaflist-anchor", steps: []any{parse(`{}`), step(map[string]any{"do": "diff", "format": "json", "data_type": "config", "parse_only": true, "data": `{"pv2-detached:ll":["a"]}`}), step(map[string]any{"do": "diff_apply"})}, modules: []any{map[string]any{"name": "pv2-detached"}, map[string]any{"name": "pv2-yang-loader"}}},
		{id: "diff-apply-cross-context-container-leaf", steps: []any{parse(`{"pv2-detached:c":{"base":"x"}}`), step(map[string]any{"do": "dup", "node": "/pv2-detached:c", "options": []any{"recursive"}, "target": b}), step(map[string]any{"do": "diff", "format": "json", "data_type": "config", "parse_only": true, "data": `{"pv2-detached:c":{"base":"y"}}`}), step(map[string]any{"do": "dup", "node": "/pv2-detached:c", "options": []any{"recursive"}, "target": a}), step(map[string]any{"do": "diff_apply"})}},
		{id: "diff-apply-latest-list-anchor", steps: []any{parse(`{}`), step(map[string]any{"do": "diff", "format": "json", "data_type": "config", "parse_only": true, "data": `{"pv2-detached:l":[{"k":"a"}]}`}), step(map[string]any{"do": "diff_apply"})}, modules: []any{map[string]any{"name": "pv2-detached"}, map[string]any{"name": "pv2-yang-loader"}}},
		{id: "diff-apply-latest-state-anchor", steps: []any{parse(`{}`), step(map[string]any{"do": "diff", "format": "json", "data_type": "data", "parse_only": true, "data": `{"pv2-detached:sll":["a"]}`}), step(map[string]any{"do": "diff_apply"})}, modules: []any{map[string]any{"name": "pv2-detached"}, map[string]any{"name": "pv2-yang-loader"}}},
		{id: "diff-merge-latest-delete-create-anchor", steps: []any{
			parse(`{"pv2-detached:ll":["a"]}`),
			step(map[string]any{"do": "diff", "format": "json", "data_type": "config", "parse_only": true, "data": `{}`}),
			step(map[string]any{"do": "diff_merge", "format": "xml", "data_type": "config", "data": `<ll xmlns="urn:vibe-ports:yang:conformance:pv2-detached" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="create" yang:value="">a</ll>`}),
		}, modules: []any{map[string]any{"name": "pv2-detached"}, map[string]any{"name": "pv2-yang-loader"}}},
		{id: "diff-merge-latest-replace-delete", steps: []any{
			parse(`{"pv2-detached:c":{"base":"x"}}`),
			step(map[string]any{"do": "diff", "format": "json", "data_type": "config", "parse_only": true, "data": `{"pv2-detached:c":{"base":"y"}}`}),
			step(map[string]any{"do": "diff_merge", "format": "xml", "data_type": "config", "data": `<c xmlns="urn:vibe-ports:yang:conformance:pv2-detached" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none"><base yang:operation="delete">y</base></c>`}),
		}, modules: []any{map[string]any{"name": "pv2-detached"}, map[string]any{"name": "pv2-yang-loader"}}},
		// libyang v5.8.6 src/diff.c:2060-2063,2099-2102 selects the latest
		// yang revision, so the older orig-value annotation is not found.
		{id: "diff-merge-latest-replace-replace", steps: []any{
			parse(`{"pv2-detached:c":{"base":"x"}}`),
			step(map[string]any{"do": "diff", "format": "json", "data_type": "config", "parse_only": true, "data": `{"pv2-detached:c":{"base":"y"}}`}),
			step(map[string]any{"do": "diff_merge", "format": "xml", "data_type": "config", "data": `<c xmlns="urn:vibe-ports:yang:conformance:pv2-detached" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none"><base yang:operation="replace" yang:orig-value="y">z</base></c>`}),
		}, modules: []any{map[string]any{"name": "pv2-detached"}, map[string]any{"name": "pv2-yang-loader"}}},
		// src/diff.c:2009-2034,2060-2086 uses one latest module identity
		// for propagation and duplication of user-ordered anchors.
		{id: "diff-merge-latest-replace-anchor", steps: []any{
			step(map[string]any{"do": "diff_parse", "format": "xml", "data_type": "config", "data": `<ll xmlns="urn:vibe-ports:yang:conformance:pv2-detached" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="replace" yang:orig-value="" yang:value="a">b</ll>`}),
			step(map[string]any{"do": "diff_merge", "format": "xml", "data_type": "config", "data": `<ll xmlns="urn:vibe-ports:yang:conformance:pv2-detached" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="replace" yang:orig-value="a" yang:value="">b</ll>`}),
		}, modules: []any{map[string]any{"name": "pv2-detached"}, map[string]any{"name": "pv2-yang-loader"}}},
		{id: "diff-merge-detached-parent-foreign-source", steps: []any{
			parse(`{"pv2-detached:c":{}}`),
			step(map[string]any{"do": "unlink", "node": "/pv2-detached:c"}),
			step(map[string]any{"do": "dup", "detached": true, "options": []any{"recursive"}, "target": b}),
			step(map[string]any{"do": "diff_merge", "format": "xml", "data_type": "config", "detached_parent": true, "src_node": "/pv2-detached:c/base", "data": `<c xmlns="urn:vibe-ports:yang:conformance:pv2-detached" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none"><base yang:operation="create">x</base></c>`}),
		}},
		{id: "diff-opaque-delete-own-operation", steps: []any{
			step(map[string]any{"do": "parse", "format": "xml", "data_type": "config", "parse_only": true, "unknown": "opaque", "data": `<unknown xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi">v</unknown>`}),
			step(map[string]any{"do": "diff_parse", "format": "xml", "data_type": "config", "unknown": "opaque", "data": `<unknown xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="delete">v</unknown>`}),
			step(map[string]any{"do": "diff_apply"}),
		}, modules: mdCtx["modules"].([]any)},
		// libyang v5.8.6 src/diff.c:2315-2318,2365-2368,2697-2700
		// reports a normal merge conflict for a second opaque delete.
		{id: "diff-opaque-delete-delete-conflict", steps: []any{
			step(map[string]any{"do": "diff_parse", "format": "xml", "data_type": "config", "unknown": "opaque", "data": `<unknown xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="delete">v</unknown>`}),
			step(map[string]any{"do": "diff_merge", "format": "xml", "data_type": "config", "unknown": "opaque", "data": `<unknown xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="delete">v</unknown>`}),
		}, modules: mdCtx["modules"].([]any)},
		{id: "diff-opaque-child-delete-overrides-none", steps: []any{
			step(map[string]any{"do": "parse", "format": "xml", "data_type": "config", "parse_only": true, "unknown": "opaque", "data": `<c xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi"><unknown>v</unknown></c>`}),
			step(map[string]any{"do": "diff_parse", "format": "xml", "data_type": "config", "unknown": "opaque", "data": `<c xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none"><unknown yang:operation="delete">v</unknown></c>`}),
			step(map[string]any{"do": "diff_apply"}),
		}, modules: mdCtx["modules"].([]any)},
		{id: "diff-opaque-delete-own-operation-json", steps: []any{
			step(map[string]any{"do": "parse", "format": "json", "data_type": "config", "parse_only": true, "unknown": "opaque", "data": `{"pv2-mdapi:unknown":"v"}`}),
			step(map[string]any{"do": "diff_parse", "format": "json", "data_type": "config", "unknown": "opaque", "data": `{"pv2-mdapi:unknown":"v","@pv2-mdapi:unknown":{"yang:operation":"delete"}}`}),
			step(map[string]any{"do": "diff_apply"}),
		}, modules: mdCtx["modules"].([]any)},
		{id: "diff-apply-meta-cross-context", steps: []any{
			parse(`{"pv2-mdapi:c":{"s":"x"}}`),
			step(map[string]any{"do": "dup", "node": "/pv2-mdapi:c", "options": []any{"recursive"}, "target": mdCtx}),
			step(map[string]any{"do": "diff", "format": "json", "data_type": "config", "parse_only": true, "options": []any{"meta"}, "data": `{"pv2-mdapi:c":{"s":"x","@s":{"pv2-mdapi:num":1}}}`}),
			step(map[string]any{"do": "dup", "node": "/pv2-mdapi:c", "options": []any{"recursive"}, "target": mdCtx}),
			step(map[string]any{"do": "diff_apply"}),
		}, modules: mdCtx["modules"].([]any)},
		{id: "diff-apply-meta-delete-invalid-module", steps: []any{
			parse(`{"pv2-mdapi:c":{"s":"x","@s":{"pv2-mdapi:num":1}}}`),
			step(map[string]any{"do": "diff_parse", "format": "xml", "data_type": "config", "data": `<c xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none"><s yang:operation="none" yang:meta-delete="missing:note=x">x</s></c>`}),
			step(map[string]any{"do": "diff_apply"}),
		}, modules: mdCtx["modules"].([]any)},
		{id: "diff-apply-meta-delete-invalid-name", steps: []any{
			parse(`{"pv2-mdapi:c":{"s":"x","@s":{"pv2-mdapi:num":1}}}`),
			step(map[string]any{"do": "diff_parse", "format": "xml", "data_type": "config", "data": `<c xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none"><s yang:operation="none" yang:meta-delete="pv2-mdapi:a b=x">x</s></c>`}),
			step(map[string]any{"do": "diff_apply"}),
		}, modules: mdCtx["modules"].([]any)},
		{id: "diff-apply-meta-replace-invalid-module", steps: []any{
			parse(`{"pv2-mdapi:c":{"s":"x","@s":{"pv2-mdapi:num":1}}}`),
			step(map[string]any{"do": "diff_parse", "format": "xml", "data_type": "config", "data": `<c xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none"><s yang:operation="none" yang:meta-replace="missing:note=x" yang:meta-orig="missing:note=old">x</s></c>`}),
			step(map[string]any{"do": "diff_apply"}),
		}, modules: mdCtx["modules"].([]any)},
		{id: "diff-apply-meta-replace-invalid-name", steps: []any{
			parse(`{"pv2-mdapi:c":{"s":"x","@s":{"pv2-mdapi:num":1}}}`),
			step(map[string]any{"do": "diff_parse", "format": "xml", "data_type": "config", "data": `<c xmlns="urn:vibe-ports:yang:conformance:pv2-mdapi" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none"><s yang:operation="none" yang:meta-replace="pv2-mdapi:a b=x" yang:meta-orig="pv2-mdapi:a b=old">x</s></c>`}),
			step(map[string]any{"do": "diff_apply"}),
		}, modules: mdCtx["modules"].([]any)},
		{id: "bulk-dispose-links-retained", steps: []any{
			step(map[string]any{"do": "parse", "format": "json", "data_type": "config", "data_file": "data/links.json"}),
			step(map[string]any{"do": "link"}),
			step(map[string]any{"do": "unlink", "node": "/pv2-links:c/l[k='a']"}),
			parse(`{}`),
			step(map[string]any{"do": "links", "detached": true}),
		}, modules: []any{map[string]any{"name": "pv2-links"}}, contextOpts: []any{"no_yanglibrary", "leafref_linking"}},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			p := map[string]any{"op": "sequence", "searchdirs": a["searchdirs"], "modules": a["modules"],
				"context_options": a["context_options"], "steps": tc.steps}
			if tc.modules != nil {
				p["modules"] = tc.modules
			}
			if tc.searchdirs != nil {
				p["searchdirs"] = tc.searchdirs
			}
			if tc.contextOpts != nil {
				p["context_options"] = tc.contextOpts
			}
			want, nativeErr := inventoryNativeResponse(oracle, base, p)
			if nativeErr != nil {
				t.Fatal(nativeErr)
			}
			// LOGINT embeds libyang's build directory. Its basename and line are
			// stable; the absolute build prefix is not part of the data verdict.
			if strings.HasPrefix(tc.id, "diff-apply-meta-") && strings.Contains(tc.id, "invalid") {
				for _, step := range list(want["steps"]) {
					for _, d := range list(step.(map[string]any)["diagnostics"]) {
						item := d.(map[string]any)
						if msg, ok := item["msg"].(string); ok && strings.HasPrefix(msg, "Internal error (") {
							if idx := strings.LastIndex(msg, "/diff.c:"); idx >= 0 {
								item["msg"] = "Internal error (" + msg[idx+1:]
							}
						}
					}
				}
			}
			got, err := inventoryRun(Request{BaseDir: base, Params: p})
			category, class := inventoryClassification(want, got, err, "")
			if category != "MATCH" {
				t.Fatalf("%s/%s: native=%v Go=%v err=%v", category, class, want, got, err)
			}
		})
	}
}
