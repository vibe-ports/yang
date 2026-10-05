// SPDX-License-Identifier: BSD-3-Clause

package xpath

import "testing"

// lazyNode is a node of a generated tree: fan-out 10, depth 7 (11 111 110
// nodes); children exist only once asked for.
type lazyNode struct {
	parent *lazyNode
	depth  int
	kids   []Node
}

var lazySchema = &tschema{kind: KindContainer, mod: "m", name: "n", config: true}

func (n *lazyNode) Parent() Node {
	if n.parent == nil {
		return nil
	}
	return n.parent
}

func (n *lazyNode) Children() []Node {
	if n.kids == nil && n.depth < 7 {
		n.kids = lazyLevel(n, n.depth+1)
	}
	return n.kids
}

func (n *lazyNode) Name() string       { return "n" }
func (n *lazyNode) Module() string     { return "m" }
func (n *lazyNode) Schema() SchemaNode { return lazySchema }
func (n *lazyNode) Value() Value       { return nil }
func (n *lazyNode) When() WhenState    { return WhenTrue }

func lazyLevel(parent *lazyNode, depth int) []Node {
	out := make([]Node, 10)
	for i := range out {
		out[i] = &lazyNode{parent: parent, depth: depth}
	}
	return out
}

// TestOrderIsLocal: sorting a two-node union on a 10M-node tree numbers only
// the sibling lists on the two ancestor chains, far below any budget.
func TestOrderIsLocal(t *testing.T) {
	tree := lazyLevel(nil, 1)
	r, err := eval("/n[2]/n[3]/n[4]/n[5]/n[6]/n[7]/n[8] | /n[1]/n[9]/n[1]/n[1]/n[1]/n[1]/n[1]",
		EvalContext{Tree: tree, MaxSteps: 10_000})
	if err != nil || len(r.Nodes) != 2 {
		t.Fatalf("got %d nodes, %v", len(r.Nodes), err)
	}
	top := r.Nodes[0]
	for top.Parent() != nil {
		top = top.Parent()
	}
	if top != tree[0] {
		t.Fatal("not in document order")
	}
}

// TestActionStringValue (D-0012): libyang fails internally on an action in a
// string-value; we dump it like a container.
func TestActionStringValue(t *testing.T) {
	tree := top(cont("pv2:c", keyed(list("l", leaf("k", "a"), mk(KindAction, "reset", nil, leaf("why", "x"))), "k")))
	r, err := eval("string(.)", EvalContext{Tree: tree, Node: tree[0]})
	if want := "\n\n    a\n\n      x\n"; err != nil || r.Str != want {
		t.Fatalf("got %q, %v; want %q", r.Str, err, want)
	}
}
