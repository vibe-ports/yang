// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"strings"
	"testing"
)

// TestAllDescDuplicates: '//' NameTest (moveto_node_alldesc_child) keeps libyang's duplicates.
// A matching node that is also another item of the child set is added, not descended into, and
// added again by that item's own walk; nothing is sorted or removed (the Release oracle compiles
// the set_sort assert out). The cases are also in testdata (oracle-pv2.jsonl, amd64 oracle).
func TestAllDescDuplicates(t *testing.T) {
	for _, tc := range []struct{ x, want string }{
		{"(l[1]/following::* | .)//k", "/pv2:c/l[k='a']/k /pv2:c/l[k='b']/k /pv2:c/l[k='b']/k /pv2:c/l[k='c']/k /pv2:c/l[k='c']/k"},
		{"(l | .)//k", "/pv2:c/l[k='a']/k /pv2:c/l[k='b']/k /pv2:c/l[k='c']/k /pv2:c/l[k='a']/k /pv2:c/l[k='b']/k /pv2:c/l[k='c']/k"},
		{"l//k", "/pv2:c/l[k='a']/k /pv2:c/l[k='b']/k /pv2:c/l[k='c']/k"},
	} {
		tree := pv2Tree()
		r, err := eval(tc.x, EvalContext{Tree: tree, Node: tree[0], IgnoreWhen: true, Schema: tinfo{tree: tree}})
		var ps []string
		for _, n := range r.Nodes {
			ps = append(ps, path(n))
		}
		if got := strings.Join(ps, " "); err != nil || got != tc.want {
			t.Errorf("%s: %s %v, want %s", tc.x, got, err, tc.want)
		}
	}
}
