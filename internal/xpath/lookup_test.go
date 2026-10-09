// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"fmt"
	"slices"
	"strconv"
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
		{"/a:c/ll[.='1999']", "1999", false}, // a leaf-list predicate is never hashed
		{"/a:c/ll[. = '7']", "7", false},
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

// wnode is a parallel tree over tnodes; lnode adds LookupChild, so a tree can mix nodes with
// and without the interface.
type wnode struct {
	t      *tnode
	parent Node
	kids   []Node
	calls  *int
}

type lnode struct{ *wnode }

func (w *wnode) Parent() Node       { return w.parent }
func (w *wnode) Children() []Node   { return w.kids }
func (w *wnode) Name() string       { return w.t.Name() }
func (w *wnode) Module() string     { return w.t.Module() }
func (w *wnode) Schema() SchemaNode { return w.t.Schema() }
func (w *wnode) Value() Value       { return w.t.Value() }
func (w *wnode) When() WhenState    { return w.t.When() }

func (w *wnode) OpaqueSchema() (SchemaNode, bool) { return w.t.OpaqueSchema() }

func unwrap(n Node) *tnode {
	if l, ok := n.(lnode); ok {
		return l.t
	}
	return n.(*wnode).t
}

func wrap(t *tnode, parent Node, look func(*tnode) bool, calls *int) Node {
	w := &wnode{t: t, parent: parent, calls: calls}
	var self Node = w
	if look(t) {
		self = lnode{w}
	}
	for _, c := range t.kids {
		w.kids = append(w.kids, wrap(c.(*tnode), self, look, calls))
	}
	return self
}

// LookupChild implements the contract by scanning (the step count is not what these tests check).
func (l lnode) LookupChild(sn SchemaNode, vals []string) ([]Node, bool) {
	*l.calls++
	var out []Node
	exists := false
	for _, c := range l.kids {
		if c.Schema() != sn {
			continue
		}
		exists = true
		ok := true
		switch keys := sn.Keys(); {
		case vals == nil:
		case keys == nil:
			ok = c.Value().String() == vals[0]
		default:
			for i, k := range keys {
				var kv string
				for _, kc := range c.Children() {
					if kc.Name() == k {
						kv = kc.Value().String()
					}
				}
				ok = ok && kv == vals[i]
			}
		}
		if ok {
			out = append(out, c)
		}
	}
	if !exists {
		for _, c := range l.kids {
			if c.Schema() == nil && c.Name() == sn.Name() {
				return []Node{c}, true
			}
		}
	}
	return out, true
}

// TestChildLookupContract: lookups through LookupChild select the same nodes as the scan for
// multi-key lists, canonized literals, leaf-lists and steps whose predicates are not consumed
// (no lookup), and mixed context sets (a context node without the interface scans, the others
// still look up).
func TestChildLookupContract(t *testing.T) {
	stripZeros := func(v string) (string, bool) {
		n, err := strconv.Atoi(v)
		return strconv.Itoa(n), err == nil
	}
	inst := func(k, j string, kids ...*tnode) *tnode {
		return keyed(list("l", append([]*tnode{leaf("k", k), leaf("j", j)}, kids...)...), "k", "j")
	}
	opq := leafl("ll", "2")
	tree := top(cont("a:c",
		inst("a", "1", leaf("v", "x")), inst("a", "2", leaf("v", "y")),
		inst("b", "1", leafl("ll", "1")), inst("b", "2", opq),
		leafl("n", "1"), leafl("n", "2")))
	c := tree[0].(*tnode)
	opq.sch = nil
	c.sch.kids[[2]string{"a", "n"}].canon = stripZeros
	c.sch.kids[[2]string{"a", "l"}].kids[[2]string{"a", "j"}].canon = stripZeros
	first := c.kids[0].(*tnode)
	for _, tc := range []struct {
		src     string
		want    []string // values of the selected nodes
		mixed   bool     // the first list instance has no LookupChild
		noCalls bool
	}{
		{"/a:c/l[k='a'][j='2']/v", []string{"y"}, false, false},
		{"/a:c/l[k='a'][j='01']/v", []string{"x"}, false, false}, // canonized literal
		{"/a:c/n[.='002']", []string{"2"}, false, true},          // leaf-lists are never hashed
		{"/a:c/l/ll[.='1']", []string{"1"}, false, true},         // opaque ll=2 filtered out
		{"/a:c/l/ll[.='2']", []string{"2"}, false, true},         // the opaque ll is an ll (lyd_node_schema)
		{"/a:c/l[k='a'][j='1']/v[.='zz']", nil, false, false},    // a leaf: nothing consumed
		{"/a:c/l/v", []string{"x", "y"}, true, false},            // per context node: the others still look up
		{"/a:c/l/ll[.='1']", []string{"1"}, true, true},
	} {
		plain, err := eval(tc.src, EvalContext{Tree: tree})
		if err != nil {
			t.Fatal(tc.src, err)
		}
		calls := 0
		look := func(n *tnode) bool { return (!tc.mixed || n != first) && n.Schema() != nil }
		got, err := eval(tc.src, EvalContext{Tree: []Node{wrap(c, nil, look, &calls)}})
		if err != nil {
			t.Fatal(tc.src, err)
		}
		var pv, gv []string
		for i, n := range plain.Nodes {
			pv = append(pv, n.Value().String())
			if i >= len(got.Nodes) || unwrap(got.Nodes[i]) != n.(*tnode) {
				t.Fatalf("%s: lookup %v, scan %v", tc.src, got.Nodes, plain.Nodes)
			}
		}
		for _, n := range got.Nodes {
			gv = append(gv, n.Value().String())
		}
		if !slices.Equal(pv, tc.want) || !slices.Equal(gv, tc.want) || (calls == 0) != tc.noCalls {
			t.Fatalf("%s: scan %v, lookup %v (%d calls), want %v", tc.src, pv, gv, calls, tc.want)
		}
	}
}
