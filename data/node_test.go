// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// A hand-built compiled schema (design 06 §4 shapes):
//
//	module b { container c { leaf-list ll; (system, uint8) list l { key k; leaf k; leaf v; }
//	           choice ch { case x { leaf x; } } leaf-list ul (user); leaf-list sl (state);
//	           list kl (keyless, state) { leaf v; } leaf z; } leaf top; }
//	module a { leaf first; }
type fixture struct {
	set                            *schema.Set
	a, b                           *schema.Module
	c, ll, l, lk, lv, x, ul, sl    *schema.Node
	kl, klv, z, top, first, choice *schema.Node
	u8, str                        *schema.Type
}

func newFixture() *fixture {
	f := &fixture{set: &schema.Set{}, u8: &schema.Type{Base: schema.Uint8}, str: &schema.Type{Base: schema.String}}
	f.b = &schema.Module{Name: "b", Implemented: true}
	f.a = &schema.Module{Name: "a", Implemented: true}
	f.set.Modules = []*schema.Module{f.b, f.a}
	add := func(m *schema.Module, p *schema.Node, k schema.Kind, name string, t *schema.Type) *schema.Node {
		n := &schema.Node{Kind: k, Name: name, Module: m, Parent: p, Type: t, Config: true}
		if p == nil {
			m.Top = append(m.Top, n)
		} else {
			p.Children = append(p.Children, n)
		}
		return n
	}
	f.c = add(f.b, nil, schema.Container, "c", nil)
	f.ll = add(f.b, f.c, schema.LeafList, "ll", f.u8)
	f.l = add(f.b, f.c, schema.List, "l", nil)
	f.lk = add(f.b, f.l, schema.Leaf, "k", f.str)
	f.lv = add(f.b, f.l, schema.Leaf, "v", f.str)
	f.l.Keys = []*schema.Node{f.lk}
	f.choice = add(f.b, f.c, schema.Choice, "ch", nil)
	cs := add(f.b, f.choice, schema.Case, "x", nil)
	f.x = add(f.b, cs, schema.Leaf, "x", f.str)
	f.ul = add(f.b, f.c, schema.LeafList, "ul", f.u8)
	f.ul.UserOrdered = true
	f.sl = add(f.b, f.c, schema.LeafList, "sl", f.u8)
	f.sl.Config, f.sl.UserOrdered = false, true
	f.kl = add(f.b, f.c, schema.List, "kl", nil)
	f.kl.Config, f.kl.UserOrdered = false, true
	f.klv = add(f.b, f.kl, schema.Leaf, "v", f.str)
	f.klv.Config = false
	f.z = add(f.b, f.c, schema.Leaf, "z", f.str)
	f.top = add(f.b, nil, schema.Leaf, "top", f.str)
	f.first = add(f.a, nil, schema.Leaf, "first", f.str)
	return f
}

func (f *fixture) term(t *testing.T, s *schema.Node, lex string) *Node {
	t.Helper()
	v, d := types.Store(s.Type, lex, types.FormatJSON, types.JSONHints("string")|types.HintStringDatatypes, nil, s)
	if d != nil {
		t.Fatal(d.Msg)
	}
	return newTerm(s, v)
}

// list returns a linked list instance with key k (and value v when not empty) under parent.
func (f *fixture) list(t *testing.T, tr *Tree, parent *Node, k, v string) *Node {
	t.Helper()
	l := newInner(f.l)
	if v != "" {
		tr.insert(l, f.term(t, f.lv, v), insertDefault)
	}
	tr.insert(l, f.term(t, f.lk, k), insertDefault)
	tr.insert(parent, l, insertDefault)
	return l
}

func names(it func(func(*Node) bool)) []string {
	var s []string
	for n := range it {
		s = append(s, n.Name()+valueOf(n))
	}
	return s
}

func valueOf(n *Node) string {
	switch {
	case n.isTerm():
		return "=" + n.value.Canonical()
	case n.schema != nil && n.schema.Kind == schema.List && len(n.kids.list) > 0 && n.kids.list[0].isKey():
		return "[" + n.kids.list[0].value.Canonical() + "]"
	}
	return ""
}

// TestInsertOrder: schema order with keys first, value-sorted system-ordered instances (equal
// values after the existing ones), user-ordered and state instances in insertion order, opaque
// nodes last, top level grouped by module name (TD:503-865, TDS rb_insert_node).
func TestInsertOrder(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	tr.insert(nil, f.term(t, f.top, "t"), insertDefault)
	tr.insert(nil, newOpaque(opaque{Name: "zz", ModuleNS: "zz"}), insertDefault)
	tr.insert(nil, f.term(t, f.first, "f"), insertDefault)
	if got, want := names(tr.Top()), []string{"first=f", "c", "top=t", "zz"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("top: %v, want %v", got, want)
	}
	tr.insert(c, f.term(t, f.z, "z"), insertDefault)
	tr.insert(c, newOpaque(opaque{Name: "op"}), insertDefault)
	for _, v := range []string{"7", "3", "9", "3"} {
		tr.insert(c, f.term(t, f.ul, v), insertDefault)
		tr.insert(c, f.term(t, f.sl, v), insertDefault)
		tr.insert(c, f.term(t, f.ll, v), insertDefault)
	}
	f.list(t, tr, c, "eth1", "")
	f.list(t, tr, c, "eth0", "x")
	tr.insert(c, f.term(t, f.x, "x"), insertDefault)
	want := []string{"ll=3", "ll=3", "ll=7", "ll=9", "l[eth0]", "l[eth1]", "x=x", "ul=7", "ul=3", "ul=9", "ul=3",
		"sl=7", "sl=3", "sl=9", "sl=3", "z=z", "op"}
	if got := names(c.Children()); !reflect.DeepEqual(got, want) {
		t.Fatalf("children:\n got  %v\n want %v", got, want)
	}
	// the key is moved first even when inserted after another child
	if got := names(tr.findSchema(&c.kids, f.l).Children()); !reflect.DeepEqual(got, []string{"k=eth0", "v=x"}) {
		t.Fatalf("list children %v", got)
	}
	// equal values: the new instance goes after the existing ones
	ll3 := c.kids.list[:2]
	n3 := f.term(t, f.ll, "3")
	tr.insert(c, n3, insertDefault)
	if c.kids.list[2] != n3 || c.kids.list[0] != ll3[0] {
		t.Fatal("equal value not inserted after the existing ones")
	}
	// insertLast appends, insertLastBySchema goes after its schema node's instances unsorted
	n1 := f.term(t, f.ll, "1")
	tr.insert(c, n1, insertLastBySchema)
	if i := slices.Index(c.kids.list, n1); c.kids.list[i-1].value.Canonical() != "9" {
		t.Fatalf("last by schema at %d", i)
	}
}

// TestInsertReversedLinear: inserting n system-ordered values in reverse needs O(log n)
// comparisons each (no quadratic search; design 07 §4).
func TestInsertReversedLinear(t *testing.T) {
	f := newFixture()
	f.ll.Type = &schema.Type{Base: schema.Uint16}
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	const n = 20000
	for i := n; i > 0; i-- {
		tr.insert(c, f.term(t, f.ll, fmt.Sprint(i%65536)), insertDefault)
	}
	for i, k := range c.kids.list {
		if k.value.Canonical() != fmt.Sprint(i+1) {
			t.Fatalf("position %d holds %s", i, k.value.Canonical())
		}
	}
}

// TestNPContainerDefault: an explicit child clears Default up the NP-container ancestors;
// unlinking the last explicit child sets it again (lyd_np_cont_dflt_del/set).
func TestNPContainerDefault(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	c.flags = Default
	tr.insert(nil, c, insertDefault)
	d := f.term(t, f.z, "d")
	d.flags = Default
	tr.insert(c, d, insertDefault)
	if c.flags&Default == 0 {
		t.Fatal("default child cleared Default")
	}
	e := f.term(t, f.x, "e")
	tr.insert(c, e, insertDefault)
	if c.flags&Default != 0 {
		t.Fatal("explicit child kept Default")
	}
	if err := unlinkTree(e); err != nil {
		t.Fatal(err)
	}
	if c.flags&Default == 0 || e.parent != nil {
		t.Fatal("unlink: Default not set back")
	}
}

// TestKeyRefusal: a linked key cannot be unlinked or freed (lyd_unlink_check, lyd_free_tree).
func TestKeyRefusal(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	l := f.list(t, tr, c, "k1", "v")
	key := l.kids.list[0]
	var oe *opError
	if err := unlinkTree(key); !errors.As(err, &oe) || oe.Err != "LY_EINVAL" ||
		oe.Msg != `Cannot unlink a list key "k", unlink the list instance instead.` {
		t.Fatalf("unlink: %v", err)
	}
	if err := freeTree(key); !errors.As(err, &oe) || oe.Msg != `Cannot free a list key "k", free the list instance instead.` {
		t.Fatalf("free: %v", err)
	}
	if key.parent != l || len(l.kids.list) != 2 {
		t.Fatal("key was unlinked")
	}
	if err := freeTree(l); err != nil || len(c.kids.list) != 0 {
		t.Fatalf("free list: %v", err)
	}
}

// TestFindSibling: lookups by schema, by value and by keys through the index, confirmed by
// value equality; dup-inst lists compare whole subtrees in order.
func TestFindSibling(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	for _, v := range []string{"5", "2"} {
		tr.insert(c, f.term(t, f.ll, v), insertDefault)
	}
	l1 := f.list(t, tr, c, "a", "")
	if got := tr.findFirst(&c.kids, f.term(t, f.ll, "5")); got == nil || got.value.Canonical() != "5" {
		t.Fatalf("leaf-list 5: %v", got)
	}
	if tr.findFirst(&c.kids, f.term(t, f.ll, "6")) != nil {
		t.Fatal("found absent value")
	}
	// the index is built now: later inserts and unlinks keep it current
	l2 := f.list(t, tr, c, "b", "")
	probe := newInner(f.l)
	probe.kids.list = []*Node{f.term(t, f.lk, "b")}
	probe.kids.list[0].parent = probe
	if got := tr.findFirst(&c.kids, probe); got != l2 {
		t.Fatalf("list b: %v", got)
	}
	unlink(l2)
	if tr.findFirst(&c.kids, probe) != nil {
		t.Fatal("unlinked instance still indexed")
	}
	if tr.findSchema(&c.kids, f.l) != l1 || tr.findSchema(&c.kids, f.z) != nil {
		t.Fatal("findSchema")
	}
	// state leaf-list: equal instances allowed, the first in order is found
	s1, s2 := f.term(t, f.sl, "4"), f.term(t, f.sl, "4")
	tr.insert(c, s1, insertDefault)
	tr.insert(c, s2, insertDefault)
	if tr.findFirst(&c.kids, f.term(t, f.sl, "4")) != s1 {
		t.Fatal("dup-inst first")
	}
	// keyless list: whole subtree compared
	k1, k2 := newInner(f.kl), newInner(f.kl)
	tr.insert(k1, f.term(t, f.klv, "p"), insertDefault)
	tr.insert(k2, f.term(t, f.klv, "q"), insertDefault)
	tr.insert(c, k1, insertDefault)
	tr.insert(c, k2, insertDefault)
	q := newInner(f.kl)
	tr.insert(q, f.term(t, f.klv, "q"), insertDefault)
	if tr.findFirst(&c.kids, q) != k2 {
		t.Fatal("keyless list by subtree")
	}
}

// TestCompareSorted: a system-ordered list compares its instances by lookup, not by position
// (lyd_compare_siblings_).
func TestCompareSorted(t *testing.T) {
	f := newFixture()
	t1, t2 := newTree(f.set), newTree(f.set)
	c1, c2 := newInner(f.c), newInner(f.c)
	t1.insert(nil, c1, insertDefault)
	t2.insert(nil, c2, insertDefault)
	f.list(t, t1, c1, "a", "1")
	f.list(t, t1, c1, "b", "2")
	f.list(t, t2, c2, "b", "2")
	f.list(t, t2, c2, "a", "1")
	if !compareSingle(t1, c1, c2, true) {
		t.Fatal("equal trees differ")
	}
	c2.kids.list[1].kids.list[1].value = f.term(t, f.lv, "3").value
	if compareSingle(t1, c1, c2, true) {
		t.Fatal("different trees equal")
	}
}

// TestAllPreOrder: All walks a subtree in pre-order.
func TestAllPreOrder(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	f.list(t, tr, c, "a", "1")
	tr.insert(c, f.term(t, f.z, "z"), insertDefault)
	if got := names(c.All()); !reflect.DeepEqual(got, []string{"c", "l[a]", "k=a", "v=1", "z=z"}) {
		t.Fatalf("%v", got)
	}
}
