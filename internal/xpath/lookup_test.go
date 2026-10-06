// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"fmt"
	"testing"
)

// lookupNode is a container that finds its children through an index (ChildLookup).
type lookupNode struct {
	*tnode
	calls int
}

func (l *lookupNode) LookupChild(sn SchemaNode, vals []string) ([]Node, bool) {
	l.calls++
	var out []Node
	for _, c := range l.kids { // the test index: a map would do, the step count is what matters
		if c.Schema() != sn {
			continue
		}
		if vals == nil || c.Value() != nil && c.Value().String() == vals[0] {
			out = append(out, c)
		}
	}
	return out, true
}

// TestChildLookup: a step with a known schema node and literal hashed predicates uses
// LookupChild (one step per context node instead of one per child, and the hashed predicate is
// not evaluated again); a non-literal value or a Node without the interface scans as before;
// all give the same nodes.
func TestChildLookup(t *testing.T) {
	const n = 2000
	var kids []*tnode
	for i := range n {
		kids = append(kids, leafl("ll", fmt.Sprint(i)))
	}
	kids = append(kids, leaf("a", "x"))
	c := cont("a:c", kids...)
	tree := top(c)
	lc := &lookupNode{tnode: c}
	for _, tc := range []struct {
		src    string
		want   string
		lookup bool
	}{
		{"/a:c/ll[.='1999']", "1999", true},
		{"/a:c/ll[. = '7']", "7", true},
		{"/a:c/a", "x", true},
		{"/a:c/ll[.=concat('19','99')]", "1999", false},
	} {
		plain, err := eval(tc.src, EvalContext{Tree: tree})
		if err != nil || len(plain.Nodes) != 1 || plain.Nodes[0].Value().String() != tc.want {
			t.Fatalf("%s scan: %v %v", tc.src, plain.Nodes, err)
		}
		lc.calls = 0
		got, err := eval(tc.src, EvalContext{Tree: []Node{lc}})
		if err != nil || len(got.Nodes) != 1 || got.Nodes[0] != plain.Nodes[0] {
			t.Fatalf("%s lookup: %v %v", tc.src, got.Nodes, err)
		}
		if (lc.calls > 0) != tc.lookup {
			t.Fatalf("%s: %d lookups", tc.src, lc.calls)
		}
		if tc.lookup && (got.Steps > 20 || plain.Steps < n) {
			t.Fatalf("%s: %d steps with lookup, %d scanning", tc.src, got.Steps, plain.Steps)
		}
	}
}

// rootLookup finds top-level nodes for EvalContext.TreeLookup.
type rootLookup struct {
	tree  []Node
	calls int
}

func (r *rootLookup) LookupChild(sn SchemaNode, _ []string) ([]Node, bool) {
	r.calls++
	for _, n := range r.tree {
		if n.Schema() == sn {
			return []Node{n}, true
		}
	}
	return nil, true
}

// TestTreeLookup: a child step from the document root (`../x` of a top-level node, a sibling
// test in a must or when) uses EvalContext.TreeLookup when given, and scans Tree otherwise.
func TestTreeLookup(t *testing.T) {
	const n = 2000
	var roots []*tnode
	for i := range n {
		roots = append(roots, leafl("a:ll", fmt.Sprint(i)))
	}
	roots = append(roots, leaf("a:x", "v"))
	tree := top(roots...)
	ctxNode := tree[0]
	src := "../x = 'v'"
	sch := tinfo{tree: tree}
	plain, err := eval(src, EvalContext{Tree: tree, Node: ctxNode, Schema: sch})
	if err != nil || !plain.Bool || plain.Steps < n {
		t.Fatalf("scan: %v %v, %d steps", plain, err, plain.Steps)
	}
	rl := &rootLookup{tree: tree}
	got, err := eval(src, EvalContext{Tree: tree, Node: ctxNode, Schema: sch, TreeLookup: rl})
	if err != nil || !got.Bool || rl.calls != 1 || got.Steps > 20 {
		t.Fatalf("lookup: %v %v, %d calls, %d steps", got, err, rl.calls, got.Steps)
	}
}
