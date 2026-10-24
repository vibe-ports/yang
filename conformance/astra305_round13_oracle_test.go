// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAstra305Round13KeylessCaches(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	parse := func(lists string) map[string]any {
		return map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
			"parse_options": []any{"when_true"}, "data": `{"pv2-astra305-r13:state":{"l":` + lists + `}}`}
	}
	for _, tc := range []struct {
		id, before, after string
		mutation          map[string]any
	}{
		{"set-value", `[{"b":"a"},{"b":"b"}]`, `[{"b":"b"},{"b":"b"}]`,
			map[string]any{"do": "change_term", "node": "/pv2-astra305-r13:state/l[1]/b", "value": "b"}},
		{"new-path-update", `[{"b":"a"},{"b":"b"}]`, `[{"b":"b"},{"b":"b"}]`,
			map[string]any{"do": "edit", "set": map[string]any{"path": "/pv2-astra305-r13:state/l[1]/b", "value": "b"}}},
		{"insert", `[{"b":"b"},{"b":"b","extra":"x"}]`, `[{"b":"b","extra":"x"},{"b":"b","extra":"x"}]`,
			map[string]any{"do": "insert", "format": "json", "data_type": "data", "parse_only": true,
				"data": `{"pv2-astra305-r13:state":{"l":[{"extra":"x"}]}}`,
				"node": "/pv2-astra305-r13:state/l[1]/extra", "parent": "/pv2-astra305-r13:state/l[1]"}},
		{"remove", `[{"b":"b","extra":"x"},{"b":"b"}]`, `[{"b":"b"},{"b":"b"}]`,
			map[string]any{"do": "edit", "delete": "/pv2-astra305-r13:state/l[1]/extra"}},
		{"validation-defaults", `[{"b":"defaults"},{"b":"defaults","d":"z"}]`, `[{"b":"defaults","d":"z"},{"b":"defaults","d":"z"}]`,
			map[string]any{"do": "validate", "data_type": "data"}},
		{"validation-autodelete", `[{"b":"b","gone":"x"},{"b":"b"}]`, `[{"b":"b"},{"b":"b"}]`,
			map[string]any{"do": "validate", "data_type": "data"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			params := map[string]any{"op": "sequence", "searchdirs": []any{"astra305-round13"},
				"modules":         []any{map[string]any{"name": "pv2-astra305-r13"}},
				"context_options": []any{"no_yanglibrary"}, "steps": []any{parse(tc.before), map[string]any{"do": "edit", "merge_self": true}, tc.mutation,
					map[string]any{"do": "edit", "format": "json", "data_type": "data", "merge": `{"pv2-astra305-r13:state":{"l":` + tc.after + `}}`},
					map[string]any{"do": "dump"}}}
			want, err := nativeRequestRunner(t)(base, params)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (Yang{}).Run(Request{ID: tc.id, BaseDir: base, Params: params})
			if err != nil {
				t.Fatal(err)
			}
			want, _ = withoutSkipped(want, Yang{}.SkippedFields("sequence"))
			got, _ = withoutSkipped(got, Yang{}.SkippedFields("sequence"))
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s\noracle: %v\nengine: %v", diff, want, got)
			}
			if steps, ok := got["steps"].([]any); !ok || len(steps) != 5 || got["verdict"] != "valid" {
				t.Fatalf("sequence did not reach all steps successfully: %v", got)
			}
		})
	}
}

// Rebuilding a split copied run must count only registered RB members. The
// summary is derived state; compare native physical order after sorting and
// after removing the complete first RB group and sorting the remaining run.
func TestAstra305Round13SplitRBRebuild(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	params := map[string]any{"op": "sequence", "searchdirs": []any{"astra305-round13-user"},
		"modules": []any{map[string]any{"name": "pv2-astra305-r13"}}, "context_options": []any{"no_yanglibrary"},
		"steps": []any{
			map[string]any{"do": "parse", "format": "json", "data_type": "rpc", "parse_only": true,
				"data": `{"pv2-astra305-r13:r":{"a":"a","b":"b","c":"c","v":[5]}}`},
			map[string]any{"do": "parse", "format": "json", "data_type": "reply", "parse_only": true,
				"parent": "/pv2-astra305-r13:r", "data": `{"pv2-astra305-r13:out":"x"}`},
			map[string]any{"do": "edit", "insert_term": map[string]any{"parent": "/pv2-astra305-r13:r", "name": "v", "value": "1"}},
			map[string]any{"do": "dup", "node": "/pv2-astra305-r13:r", "options": []any{"recursive"},
				"target": map[string]any{"searchdirs": []any{"astra305-round13"}, "modules": []any{map[string]any{"name": "pv2-astra305-r13"}}, "context_options": []any{"no_yanglibrary"}}},
			map[string]any{"do": "edit", "insert_term": map[string]any{"parent": "/pv2-astra305-r13:r", "name": "v", "value": "3"}},
			map[string]any{"do": "dump"},
			map[string]any{"do": "trim", "xpath": "/pv2-astra305-r13:r/*[. != '3']"},
			map[string]any{"do": "trim", "xpath": "/pv2-astra305-r13:r/*[. != '5']"},
			map[string]any{"do": "edit", "insert_term": map[string]any{"parent": "/pv2-astra305-r13:r", "name": "v", "value": "2"}},
			map[string]any{"do": "dump"},
		}}
	want, err := nativeRequestRunner(t)(base, params)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (Yang{}).Run(Request{ID: "split-rb-rebuild", BaseDir: base, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	want, _ = withoutSkipped(want, Yang{}.SkippedFields("sequence"))
	got, _ = withoutSkipped(got, Yang{}.SkippedFields("sequence"))
	if diff := diffResponses(want, got); diff != "" {
		t.Fatalf("oracle mismatch: %s\noracle: %v\nengine: %v", diff, want, got)
	}
	if got["verdict"] != "valid" {
		t.Fatalf("sequence failed before final rebuild: %v", got)
	}
	steps := got["steps"].([]any)
	for _, checkpoint := range []struct {
		step  int
		shape string
	}{
		{3, "a:a,b:b,c:c,v:5,out:x,v:1"},
		{4, "a:a,b:b,c:c,v:3,v:5,out:x,v:1"},
	} {
		var shape []string
		for _, raw := range steps[checkpoint.step].(map[string]any)["typed"].([]any)[1:] {
			n := raw.(map[string]any)
			path := n["schema"].(string)
			name := path[strings.LastIndex(path, "/")+1:]
			shape = append(shape, name+":"+n["value"].(map[string]any)["canonical"].(string))
		}
		if got := strings.Join(shape, ","); got != checkpoint.shape {
			t.Fatalf("step %d shape=%s, want %s", checkpoint.step, got, checkpoint.shape)
		}
	}
}
