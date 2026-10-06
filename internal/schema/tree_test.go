// SPDX-License-Identifier: BSD-3-Clause

package schema

import (
	"reflect"
	"testing"
)

// TestGetNext: lys_getnext's order (data children, actions, notifications) and options.
func TestGetNext(t *testing.T) {
	m, o := &Module{Name: "m", Implemented: true}, &Module{Name: "o", Implemented: true}
	node := func(k Kind, name string, mod *Module, parent *Node, kids ...*Node) *Node {
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
		return n
	}
	x := node(Leaf, "x", m, nil)
	ch := node(Choice, "ch", m, nil, node(Case, "a", m, nil, x))
	act := node(Action, "act", m, nil, node(Input, "input", m, nil, node(Leaf, "i", m, nil)),
		node(Output, "output", m, nil, node(Leaf, "o", m, nil)))
	c := node(Container, "c", m, nil, ch, node(Leaf, "y", o, nil), act, node(Notification, "n", m, nil))
	names := func(p *Node, opts GetNextOpt) []string {
		var s []string
		for n := range GetNext(p, nil, opts) {
			s = append(s, n.Name)
		}
		return s
	}
	for _, tc := range []struct {
		p    *Node
		opts GetNextOpt
		want []string
	}{
		{c, 0, []string{"x", "y", "act", "n"}},
		{c, GetNextWithChoice, []string{"ch", "y", "act", "n"}},
		{c, GetNextNoChoice, []string{"y", "act", "n"}},
		{ch, GetNextWithCase, []string{"a"}},
		{act, 0, []string{"i"}},
		{act, GetNextOutput, []string{"o"}},
	} {
		if got := names(tc.p, tc.opts); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %d: %v, want %v", tc.p.Name, tc.opts, got, tc.want)
		}
	}
	if FindChild(c, nil, m, "x", 0) != x || FindChild(c, nil, o, "x", 0) != nil || FindChild(c, nil, m, "x", GetNextNoChoice) != nil {
		t.Error("FindChild")
	}
	if got := x.LogPath(); got != "/m:c/ch/a/x" {
		t.Errorf("LogPath %q", got)
	}
	if got := c.Children[1].LogPath(); got != "/m:c/o:y" {
		t.Errorf("LogPath %q", got)
	}
	if x.DataParent() != c || DataNode(ch) != c || DataNode(x) != x {
		t.Error("DataParent/DataNode")
	}
}
