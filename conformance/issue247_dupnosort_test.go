// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// dupNoSortCases are sequence requests over DupOptions.NoSort (LYD_DUP_NO_LYDS) into a bucket's
// leaf-list run. The source run is parsed in sorted order [1,9], so the sorted copy and the
// appended copy differ. want is the order of dst's leaf-list after each dump step.
var dupNoSortCases = []struct {
	name  string
	steps []any
	want  [][]string
}{
	{"siblings-populated", []any{
		dupNoSortSetup,
		map[string]any{"do": "dup", "node": "/pv2-lyds:bucket[name='src']/ll[.='1']", "parent": "/pv2-lyds:bucket[name='dst']", "siblings": true, "options": []any{"no_lyds"}},
		map[string]any{"do": "dump"},
		dupNoSortInsert("4"), map[string]any{"do": "dump"},
	}, [][]string{{"3", "5", "1", "9"}, {"3", "4", "5", "1", "9"}}},
	{"single-populated", []any{
		dupNoSortSetup,
		map[string]any{"do": "dup", "node": "/pv2-lyds:bucket[name='src']/ll[.='9']", "parent": "/pv2-lyds:bucket[name='dst']", "options": []any{"no_lyds"}},
		map[string]any{"do": "dump"},
	}, [][]string{{"3", "5", "9"}}},
	{"siblings-sorted-default", []any{
		dupNoSortSetup,
		map[string]any{"do": "dup", "node": "/pv2-lyds:bucket[name='src']/ll[.='1']", "parent": "/pv2-lyds:bucket[name='dst']", "siblings": true},
		map[string]any{"do": "dump"},
	}, [][]string{{"1", "3", "5", "9"}}},
	{"siblings-trim-insert", []any{
		dupNoSortSetup,
		map[string]any{"do": "dup", "node": "/pv2-lyds:bucket[name='src']/ll[.='1']", "parent": "/pv2-lyds:bucket[name='dst']", "siblings": true, "options": []any{"no_lyds"}},
		map[string]any{"do": "trim", "xpath": "/pv2-lyds:bucket[name='dst']/ll[.='1' or .='9']"},
		map[string]any{"do": "dump"},
		dupNoSortInsert("4"), map[string]any{"do": "dump"},
	}, [][]string{{"1", "9"}, {"1", "4", "9"}}},
	{"siblings-trim-partial-insert", []any{
		dupNoSortSetup,
		map[string]any{"do": "dup", "node": "/pv2-lyds:bucket[name='src']/ll[.='1']", "parent": "/pv2-lyds:bucket[name='dst']", "siblings": true, "options": []any{"no_lyds"}},
		map[string]any{"do": "trim", "xpath": "/pv2-lyds:bucket[name='dst']/ll[.='5' or .='1' or .='9']"},
		map[string]any{"do": "dump"},
		dupNoSortInsert("4"), map[string]any{"do": "dump"},
	}, [][]string{{"5", "1", "9"}, {"4", "5", "1", "9"}}},
}

var dupNoSortSetup = map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
	"data": `{"pv2-lyds:bucket":[{"name":"dst","ll":[3,5]},{"name":"src","ll":[9,1]}]}`}

func dupNoSortInsert(v string) map[string]any {
	return map[string]any{"do": "insert", "node": "/pv2-lyds:bucket[name='n" + v + "']/ll[.='" + v + "']",
		"parent": "/pv2-lyds:bucket[name='dst']", "format": "json", "data_type": "data", "parse_only": true,
		"data": `{"pv2-lyds:bucket":[{"name":"n` + v + `","ll":[` + v + `]}]}`}
}

func dupNoSortRequest(steps []any) map[string]any {
	return map[string]any{"op": "sequence", "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2-lyds"}},
		"context_options": []any{"no_yanglibrary"}, "steps": steps}
}

// dupNoSortOrders is the leaf-list order of dst after each dump step.
func dupNoSortOrders(t *testing.T, resp Response) [][]string {
	t.Helper()
	var out [][]string
	for _, s := range resp["steps"].([]any) {
		st := s.(map[string]any)
		if st["do"] != "dump" {
			continue
		}
		var vals []string
		for _, n := range st["typed"].([]any) {
			p := n.(map[string]any)["path"].(string)
			if strings.HasPrefix(p, "/pv2-lyds:bucket[name='dst']/ll[") {
				vals = append(vals, strings.TrimSuffix(strings.TrimPrefix(p, "/pv2-lyds:bucket[name='dst']/ll[.='"), "']"))
			}
		}
		out = append(out, vals)
	}
	return out
}

// The engine runs dup option no_lyds and copies the instances
// unsorted; without DupOptions.NoSort the leaf-list would stay sorted.
func TestDupNoSortEngine(t *testing.T) {
	for _, tc := range dupNoSortCases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: dupNoSortRequest(tc.steps)})
			if errors.Is(err, ErrUnsupported) || err != nil {
				t.Fatal(err)
			}
			if got := dupNoSortOrders(t, resp); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("orders = %v, want %v", got, tc.want)
			}
		})
	}
}
