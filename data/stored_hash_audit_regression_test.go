// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"slices"
	"testing"
)

// A removed separator can join two physical runs without joining their RB
// trees. Merge consumes only the selected source tree as a sorted sequence.
func TestMergeDistinctSourceRBGroups(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  leaf-list s { type uint8; } }`)
	src, dst := newTree(set), newTree(set)
	byValue := map[string]*Node{}
	for _, value := range []string{"20", "40", "5", "10"} {
		n, err := src.NewTerm(nil, "m:s", value, NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		byValue[value] = n
	}
	mustLink(t, src.top.buildSchema([]*Node{byValue["20"], byValue["40"], byValue["5"], byValue["10"]}))
	src.top.firstRun[src.top.At(0).schema] = src.top.At(0)
	groupA, groupB := &rbGroup{}, &rbGroup{}
	for i, n := range src.top.SchemaAll() {
		n.inRB = true
		if i < 2 {
			n.rbGroup = groupA
		} else {
			n.rbGroup = groupB
		}
	}
	src.top.rbTree = map[*Node]bool{src.top.At(0): true}
	src.top.rbGroups = map[*Node]*rbGroup{src.top.At(0): groupA}
	// the selected tree's retained topology, built in its native insertion order
	selected := &dirtyOrder{work: &src.work}
	for _, n := range src.top.schemaSpan().Collect()[:2] {
		_, _ = selected.insertCompatible(n, func(a, b *Node) (int, bool) { return compareSorted(a, b), true })
	}
	src.top.rbOrders = map[*rbGroup]*dirtyOrder{groupA: selected}
	for _, value := range []string{"15", "30"} {
		if _, err := dst.NewTerm(nil, "m:s", value, NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := dst.Insert(nil, src.top.first()); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range dst.top.SchemaAll() {
		got = append(got, n.Value())
	}
	if want := []string{"5", "10", "15", "20", "30", "40"}; !slices.Equal(got, want) {
		t.Fatalf("merged order %v, want %v", got, want)
	}
}

func TestBatchRemovalComparedPrefixLinear(t *testing.T) {
	srcSet := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { list l { key a; leaf a { type string; } } } }`)
	dstSet := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { list l { key "a b"; leaf a { type string; } leaf b { type string; }
    leaf-list x { type string; } leaf c { type string; } } } }`)
	src, _, err := parseJSONString(srcSet, `{"m:r":{"l":[{"a":"a"}]}}`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	tr := newTree(dstSet)
	root, err := tr.NewInner(nil, "m:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var list *Node
	for range 4 {
		list, _, err = tr.Dup(src.top.first().kids.At(0), root, DupOptions{Recursive: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	const count = 10_000
	removed := make([]*Node, 0, count)
	for i := range count {
		n, err := tr.NewTerm(list, "x", fmt.Sprintf("%05d", i), NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		removed = append(removed, n)
	}
	if _, err := tr.NewTerm(list, "c", "tail", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	if root.kids.ht == nil || !list.lkeyOK {
		t.Fatal("fixture needs an indexed copied incomplete list")
	}
	tr.work.Store(0)
	if err := tr.unlinkAll(removed); err != nil {
		t.Fatal(err)
	}
	if got := tr.work.Load(); got > 2_000_000 {
		t.Fatalf("batch removal inspected %d positions for %d children", got, count)
	}
	if list.kids.SchemaLen() != 2 || list.kids.At(1).Name() != "c" {
		t.Fatal("batch removal did not expose retained child")
	}
	if want, ok := hashOf(list); !ok || list.hkey != want {
		t.Fatalf("retained list discriminator %v, want %v", list.hkey, want)
	}
}

func TestRemoveLastMemberClearsOnlyItsRBGroup(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf-list s { type uint8; } leaf sep { type string; }
    leaf a { type string; } leaf b { type string; } leaf c { type string; } } }`)
	tr := newTree(set)
	root, err := tr.NewInner(nil, "m:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var members []*Node
	for _, item := range []struct{ name, value string }{
		{"s", "20"}, {"s", "40"}, {"sep", "x"}, {"s", "60"}, {"s", "80"},
		{"a", "a"}, {"b", "b"}, {"c", "c"},
	} {
		n := modelFreshTerm(t, root, item.name, item.value)
		if err := tr.link(root, &root.kids, n, root.kids.SchemaLen()); err != nil {
			t.Fatal(err)
		}
		if item.name == "s" {
			members = append(members, n)
		}
	}
	if root.kids.ht == nil {
		t.Fatal("fixture needs a children table")
	}
	a, b := &rbGroup{}, &rbGroup{}
	for i, n := range members {
		n.inRB = true
		if i < 2 {
			n.rbGroup = a
		} else {
			n.rbGroup = b
		}
	}
	root.kids.rbTree = map[*Node]bool{members[0]: true, members[2]: true}
	root.kids.rbGroups = map[*Node]*rbGroup{members[0]: a, members[2]: b}
	for _, n := range members[2:] {
		if err := n.Remove(); err != nil {
			t.Fatal(err)
		}
	}
	for _, group := range root.kids.rbGroups {
		if group == b {
			t.Fatal("empty RB group remained live")
		}
	}
	if root.kids.rbGroups[members[0]] != a {
		t.Fatal("surviving RB group was cleared")
	}
}
