// SPDX-License-Identifier: BSD-3-Clause

package schema

import (
	"reflect"
	"strings"
	"testing"
)

// pathTree is m:c/l (list, keys k1 k2) with a choice and its case, an action with input/output,
// a notification, and a leaf of module o augmented into the case.
func pathTree() (*Module, map[string]*Node) {
	m, o := &Module{Name: "m", Implemented: true}, &Module{Name: "o", Implemented: true}
	nodes := map[string]*Node{}
	mk := func(k Kind, name string, mod *Module, parent *Node, kids ...*Node) *Node {
		n := &Node{Kind: k, Name: name, Module: mod, Parent: parent}
		for _, c := range kids {
			c.Parent = n
			switch c.Kind {
			case Action:
				n.Actions = append(n.Actions, c)
			case Notification:
				n.Notifs = append(n.Notifs, c)
			default:
				n.Children = append(n.Children, c)
			}
		}
		nodes[name] = n
		return n
	}
	k1, k2 := mk(Leaf, "k1", m, nil), mk(Leaf, "k2", m, nil)
	l := mk(List, "l", m, nil, k1, k2, mk(Choice, "ch", m, nil, mk(Case, "cs", m, nil, mk(Leaf, "x", m, nil),
		mk(Leaf, "ox", o, nil))),
		mk(Action, "act", m, nil, mk(Input, "input", m, nil, mk(Leaf, "i", m, nil)),
			mk(Output, "output", m, nil, mk(Leaf, "out", m, nil))),
		mk(Notification, "nt", m, nil, mk(Leaf, "nl", m, nil)))
	l.Keys = []*Node{k1, k2}
	c := mk(Container, "c", m, nil, l)
	m.Top = []*Node{c, mk(RPC, "r", m, nil)}
	return m, nodes
}

// TestPath: lysc_path_until for the three path types (tree_schema.c).
func TestPath(t *testing.T) {
	_, n := pathTree()
	for _, tc := range []struct {
		node string
		pt   PathType
		want string
	}{
		{"x", PathLog, "/m:c/l/ch/cs/x"},
		{"x", PathData, "/m:c/l/x"},
		{"x", PathDataPattern, "/m:c/l[k1='%s'][k2='%s']/x"},
		{"ox", PathLog, "/m:c/l/ch/cs/o:ox"},
		{"ox", PathData, "/m:c/l/o:ox"},
		{"i", PathLog, "/m:c/l/act/input/i"},
		{"i", PathData, "/m:c/l/act/i"},
		{"out", PathDataPattern, "/m:c/l[k1='%s'][k2='%s']/act/out"},
		{"l", PathDataPattern, "/m:c/l[k1='%s'][k2='%s']"},
		{"c", PathData, "/m:c"},
	} {
		if got := n[tc.node].Path(tc.pt); got != tc.want {
			t.Errorf("%s %d: %q, want %q", tc.node, tc.pt, got, tc.want)
		}
		if tc.pt == PathLog && n[tc.node].LogPath() != tc.want {
			t.Errorf("%s: LogPath %q", tc.node, n[tc.node].LogPath())
		}
	}
	// a data path of a choice alone: libyang prints "/" when every node was skipped
	ch := &Node{Kind: Choice, Name: "ch", Module: &Module{Name: "m"}}
	if got := ch.Path(PathData); got != "/" {
		t.Errorf("top-level choice data path %q", got)
	}
}

// TestDFS: lysc_module_dfs_full order: a node, its actions' and notifications' subtrees, then
// its children; top-level data before rpcs.
func TestDFS(t *testing.T) {
	m, n := pathTree()
	var got []string
	for x := range m.DFS() {
		got = append(got, x.Name)
	}
	want := "c l act input i output out nt nl k1 k2 ch cs x ox r"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v\nwant %s", got, want)
	}
	got = nil
	for x := range n["l"].DFS() { // stops early without panicking
		got = append(got, x.Name)
		if len(got) == 3 {
			break
		}
	}
	if !reflect.DeepEqual(got, []string{"l", "act", "input"}) {
		t.Fatalf("early stop: %v", got)
	}
}

// TestOwnerModule: lysc_owner_module is the module of the top-level ancestor.
func TestOwnerModule(t *testing.T) {
	_, n := pathTree()
	if got := n["ox"].OwnerModule(); got.Name != "m" {
		t.Errorf("ox owner %s", got.Name)
	}
	if (*Node)(nil).OwnerModule() != nil {
		t.Error("nil node")
	}
}
