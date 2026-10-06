// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"reflect"
	"slices"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// srcList builds a libyang sibling list as the top level of a new tree: ll values inserted in
// order (sorted with an RB tree), or appended by schema when unsorted (no RB tree).
func (f *fixture) srcList(t *testing.T, unsorted bool, vals ...string) (*Tree, []*Node) {
	t.Helper()
	src := newTree(f.set)
	order := insertDefault
	if unsorted {
		order = insertLastBySchema
	}
	var ns []*Node
	for _, v := range vals {
		n := f.term(t, f.ll, v)
		src.insert(nil, n, order)
		ns = append(ns, n)
	}
	return src, ns
}

func (f *fixture) cont(t *testing.T) (*Tree, *Node) {
	t.Helper()
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	return tr, c
}

// TestInsertCheckSchema: lyd_insert_check_schema messages; opaque nodes and unknown places pass.
func TestInsertCheckSchema(t *testing.T) {
	f := newFixture()
	cases := []struct {
		err  string
		fail bool
		args [3]*schema.Node
	}{
		{"", false, [3]*schema.Node{f.c, nil, f.ll}},
		{"", false, [3]*schema.Node{nil, f.z, f.ll}},
		{"", false, [3]*schema.Node{nil, f.top, f.first}},
		{"", false, [3]*schema.Node{nil, nil, f.ll}},
		{"Cannot insert, parent of \"top\" is not \"c\".", true, [3]*schema.Node{f.c, nil, f.top}},
		{"Cannot insert, parent of \"k\" is not \"c\".", true, [3]*schema.Node{nil, f.z, f.lk}},
		{"Cannot insert, node \"ll\" is not top-level.", true, [3]*schema.Node{nil, f.top, f.ll}},
	}
	for _, tc := range cases {
		err := insertCheckSchema(tc.args[0], tc.args[1], tc.args[2])
		if (err != nil) != tc.fail || err != nil && err.Error() != tc.err {
			t.Errorf("%v: %v", tc.args, err)
		}
	}
}

// TestInsertChild: lyd_insert_child of a lone node, the argument and key checks, and of a
// sibling list into an empty parent (lyd_move_nodes_at_once: order and RB tree kept).
func TestInsertChild(t *testing.T) {
	f := newFixture()
	tr, c := f.cont(t)
	if err := tr.insertChild(c, f.term(t, f.z, "z")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		parent, n *Node
		err       string
	}{
		{nil, c, "Invalid argument parent (lyd_insert_child())."},
		{c, nil, "Invalid argument node (lyd_insert_child())."},
		{c.kids.list[0], c, "Invalid argument !parent->schema || (parent->schema->nodetype & LYD_NODE_INNER) (lyd_insert_child())."},
		{c, f.term(t, f.top, "t"), "Cannot insert, parent of \"top\" is not \"c\"."},
	} {
		if err := tr.insertChild(tc.parent, tc.n); err == nil || err.Error() != tc.err {
			t.Errorf("%s: %v", tc.err, err)
		}
	}
	l := f.list(t, tr, c, "eth0", "")
	if err := tr.insertChild(l, l.kids.list[0]); err == nil || err.Error() != "Cannot unlink a list key \"k\", unlink the list instance instead." {
		t.Errorf("key: %v", err)
	}

	// at once into an empty parent
	tr2, c2 := f.cont(t)
	src, _ := f.srcList(t, false, "5", "2")
	src.insert(nil, f.term(t, f.z, "z"), insertDefault)
	if err := tr2.insertChild(c2, src.top.first()); err != nil {
		t.Fatal(err)
	}
	tr2.insert(c2, f.term(t, f.ll, "3"), insertDefault)
	if got := names(c2.Children()); !reflect.DeepEqual(got, []string{"ll=2", "ll=3", "ll=5", "z=z"}) || src.top.len() != 0 {
		t.Fatalf("at once: %v, %d left", got, src.top.len())
	}
	if !c2.kids.rbTree[f.ll] {
		t.Fatal("RB tree not moved")
	}
}

// TestMergeSorted: lyds_merge, one case per RB-tree combination, ties included.
func TestMergeSorted(t *testing.T) {
	f := newFixture()
	move := func(t *testing.T, dstVals []string, dstUnsorted bool, src *Tree) (*Tree, *Node, []*Node) {
		t.Helper()
		tr, c := f.cont(t)
		var dst []*Node
		order := insertDefault
		if dstUnsorted {
			order = insertLastBySchema
		}
		for _, v := range dstVals {
			n := f.term(t, f.ll, v)
			tr.insert(c, n, order)
			dst = append(dst, n)
		}
		tr.insert(c, f.term(t, f.z, "z"), insertDefault)
		if err := tr.insertChild(c, src.top.first()); err != nil {
			t.Fatal(err)
		}
		return tr, c, dst
	}

	// neither has an RB tree: the destination run is sorted first, then the source values join
	// one by one in data order (lyds_merge_nodes1 after lyds_additionally_create_rb_tree)
	src, _ := f.srcList(t, true, "6", "1", "4")
	_, c, dst := move(t, []string{"4", "2"}, true, src)
	if got := names(c.Children()); !reflect.DeepEqual(got, []string{"ll=1", "ll=2", "ll=4", "ll=4", "ll=6", "z=z"}) {
		t.Fatalf("no trees: %v", got)
	}
	if c.kids.list[2] != dst[0] || !c.kids.rbTree[f.ll] {
		t.Fatal("no trees: the source 4 must follow the destination 4")
	}

	// only the source has one (lyds_merge_nodes2): libyang 5.8.6 crashes on this input (D-0062);
	// the expected order is the stable merge _front/_among/_back are written to produce, the
	// destination instance after the equal source value
	src, sn := f.srcList(t, false, "5", "4", "3")
	_, c, dst = move(t, []string{"4"}, false, src)
	if got := names(c.Children()); !reflect.DeepEqual(got, []string{"ll=3", "ll=4", "ll=4", "ll=5", "z=z"}) {
		t.Fatalf("source tree: %v", got)
	}
	if c.kids.list[1] != sn[1] || c.kids.list[2] != dst[0] || !c.kids.rbTree[f.ll] {
		t.Fatal("source tree: the source 4 must come first")
	}

	// both have one: source values join one by one, after equal destination values
	src, sn = f.srcList(t, false, "6", "4", "1")
	_, c, dst = move(t, []string{"4", "2"}, false, src)
	if got := names(c.Children()); !reflect.DeepEqual(got, []string{"ll=1", "ll=2", "ll=4", "ll=4", "ll=6", "z=z"}) {
		t.Fatalf("both trees: %v", got)
	}
	if c.kids.list[2] != dst[0] || c.kids.list[3] != sn[1] {
		t.Fatal("both trees: the destination 4 must come first")
	}

	// the destination run has instances outside its tree: each source value goes right after its
	// RB predecessor (lyds_link_data_node), the unsorted 1 stays last
	tr, c := f.cont(t)
	for _, v := range []string{"5", "3"} {
		tr.insert(c, f.term(t, f.ll, v), insertDefault)
	}
	tr.insert(c, f.term(t, f.ll, "1"), insertLastBySchema)
	src, _ = f.srcList(t, false, "4", "6")
	if err := tr.insertChild(c, src.top.first()); err != nil {
		t.Fatal(err)
	}
	if got := names(c.Children()); !reflect.DeepEqual(got, []string{"ll=3", "ll=4", "ll=5", "ll=6", "ll=1"}) {
		t.Fatalf("unsorted destination: %v", got)
	}
}

// TestMoveBySchema: lyd_move_nodes_ordby_schema — runs without a destination instance go to their
// schema place in source order (an RB tree moves with them), other runs after the existing
// instances, opaque nodes last.
func TestMoveBySchema(t *testing.T) {
	f := newFixture()
	tr, c := f.cont(t)
	tr.insert(c, f.term(t, f.ul, "1"), insertDefault)
	tr.insert(c, f.term(t, f.z, "z"), insertDefault)
	tr.insert(c, newOpaque(opaque{Name: "op1"}), insertDefault)
	src := newTree(f.set)
	for _, n := range []*Node{f.term(t, f.ul, "9"), f.term(t, f.ul, "0"), f.term(t, f.ll, "7"), f.term(t, f.ll, "2"),
		newOpaque(opaque{Name: "op2"})} {
		src.insert(nil, n, insertDefault)
	}
	if err := tr.insertChild(c, src.top.first()); err != nil {
		t.Fatal(err)
	}
	want := []string{"ll=2", "ll=7", "ul=1", "ul=9", "ul=0", "z=z", "op1", "op2"}
	if got := names(c.Children()); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if !c.kids.rbTree[f.ll] || src.top.len() != 0 {
		t.Fatal("RB tree not moved with its run, or source not emptied")
	}
}

// TestInsertSibling: lyd_insert_sibling of a lone node and of a sibling list at the top level
// (module order), argument and schema checks.
func TestInsertSibling(t *testing.T) {
	f := newFixture()
	tr, c := f.cont(t)
	if err := tr.insertSibling(c, f.term(t, f.top, "t")); err != nil {
		t.Fatal(err)
	}
	src := newTree(f.set)
	src.insert(nil, f.term(t, f.first, "f"), insertDefault)
	src.insert(nil, newOpaque(opaque{Name: "zz", ModuleNS: "zz"}), insertDefault)
	if err := newTree(f.set).insertSibling(c, src.top.first()); err != nil {
		t.Fatal(err)
	}
	if got := names(tr.Top()); !reflect.DeepEqual(got, []string{"first=f", "c", "top=t", "zz"}) {
		t.Fatalf("top: %v", got)
	}
	z := f.term(t, f.z, "z")
	if err := tr.insertSibling(nil, z); err != nil || !slices.Contains(tr.top.list, z) {
		t.Fatalf("nil sibling: %v", err) // libyang checks nothing without a sibling
	}
	for _, tc := range []struct {
		sibling, n *Node
		err        string
	}{
		{c, nil, "Invalid argument node (lyd_insert_sibling())."},
		{c, c, "Invalid argument sibling != node (lyd_insert_sibling())."},
		{c, f.term(t, f.ll, "1"), "Cannot insert, node \"ll\" is not top-level."},
		{newInner(f.c), f.term(t, f.top, "t"), "Sibling is not linked (no parent, no tree)."},
	} {
		if err := tr.insertSibling(tc.sibling, tc.n); err == nil || err.Error() != tc.err {
			t.Errorf("%s: %v", tc.err, err)
		}
	}
}

// TestInsertAfter: lyd_insert_after for user-ordered instances, its checks, and the opaque
// placements Go keeps apart.
func TestInsertAfter(t *testing.T) {
	f := newFixture()
	tr, c := f.cont(t)
	var ul []*Node
	for _, v := range []string{"1", "2", "3"} {
		n := f.term(t, f.ul, v)
		tr.insert(c, n, insertDefault)
		ul = append(ul, n)
	}
	op := newOpaque(opaque{Name: "op"})
	tr.insert(c, op, insertDefault)
	if err := tr.insertAfter(ul[0], ul[2]); err != nil {
		t.Fatal(err)
	}
	if got := names(c.Children()); !reflect.DeepEqual(got, []string{"ul=1", "ul=3", "ul=2", "op"}) {
		t.Fatalf("%v", got)
	}
	n4 := f.term(t, f.ul, "4")
	// after an opaque sibling: after the last instance of its run, schema order kept
	tr.insert(c, f.term(t, f.z, "z"), insertDefault)
	if err := tr.insertAfter(op, n4); err != nil || c.kids.list[3] != n4 {
		t.Fatalf("after opaque: %v %v", err, names(c.Children()))
	}
	n5 := f.term(t, f.ul, "5")
	tr.insert(c, n5, insertDefault)
	if c.kids.list[4] != n5 {
		t.Fatalf("schema order broken: %v", names(c.Children()))
	}
	op2 := newOpaque(opaque{Name: "op2"})
	if err := tr.insertAfter(ul[0], op2); err != nil || c.kids.opq[0] != op2 {
		t.Fatalf("opaque after schema: %v", err)
	}
	ll := f.term(t, f.ll, "1")
	tr.insert(c, ll, insertDefault)
	for _, tc := range []struct {
		sibling, n *Node
		err        string
	}{
		{nil, ul[0], "Invalid argument sibling (lyd_insert_after())."},
		{ul[0], nil, "Invalid argument node (lyd_insert_after())."},
		{ul[0], ul[0], "Invalid argument sibling != node (lyd_insert_after())."},
		{ul[0], f.term(t, f.ll, "5"), "Can be used only for user-ordered nodes."},
		{ul[0], f.term(t, f.sl, "5"), "Cannot insert after a different schema node instance."},
		{ul[0], f.term(t, f.top, "t"), "Cannot insert, parent of \"top\" is not \"c\"."},
		{f.term(t, f.ul, "9"), f.term(t, f.ul, "5"), "Sibling is not linked (no parent, no tree)."},
	} {
		if err := tr.insertAfter(tc.sibling, tc.n); err == nil || err.Error() != tc.err {
			t.Errorf("%s: %v", tc.err, err)
		}
	}
}
