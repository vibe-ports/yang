// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"reflect"
	"slices"
	"sync"
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
func (f *fixture) makeList(t *testing.T, tr *Tree, parent *Node, k, v string) *Node {
	t.Helper()
	l := newInner(f.l)
	if v != "" {
		mustLink(t, tr.insert(l, f.term(t, f.lv, v), insertDefault))
	}
	mustLink(t, tr.insert(l, f.term(t, f.lk, k), insertDefault))
	mustLink(t, tr.insert(parent, l, insertDefault))
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
	case n.schema != nil && n.schema.Kind == schema.List && n.kids.SchemaLen() > 0 && n.kids.At(0).isKey():
		return "[" + n.kids.At(0).value.Canonical() + "]"
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
	mustLink(t, tr.insert(nil, c, insertDefault))
	mustLink(t, tr.insert(nil, f.term(t, f.top, "t"), insertDefault))
	mustLink(t, tr.insert(nil, newOpaque(opaque{Name: "zz", ModuleNS: "zz"}), insertDefault))
	mustLink(t, tr.insert(nil, f.term(t, f.first, "f"), insertDefault))
	if got, want := names(tr.Top()), []string{"first=f", "c", "top=t", "zz"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("top: %v, want %v", got, want)
	}
	mustLink(t, tr.insert(c, f.term(t, f.z, "z"), insertDefault))
	mustLink(t, tr.insert(c, newOpaque(opaque{Name: "op"}), insertDefault))
	for _, v := range []string{"7", "3", "9", "3"} {
		mustLink(t, tr.insert(c, f.term(t, f.ul, v), insertDefault))
		mustLink(t, tr.insert(c, f.term(t, f.sl, v), insertDefault))
		mustLink(t, tr.insert(c, f.term(t, f.ll, v), insertDefault))
	}
	f.makeList(t, tr, c, "eth1", "")
	f.makeList(t, tr, c, "eth0", "x")
	mustLink(t, tr.insert(c, f.term(t, f.x, "x"), insertDefault))
	want := []string{"ll=3", "ll=3", "ll=7", "ll=9", "l[eth0]", "l[eth1]", "x=x", "ul=7", "ul=3", "ul=9", "ul=3",
		"sl=7", "sl=3", "sl=9", "sl=3", "z=z", "op"}
	if got := names(c.Children()); !reflect.DeepEqual(got, want) {
		t.Fatalf("children:\n got  %v\n want %v", got, want)
	}
	// the key is moved first even when inserted after another child
	if got := names(tr.schemaLookup(&c.kids, f.l).Children()); !reflect.DeepEqual(got, []string{"k=eth0", "v=x"}) {
		t.Fatalf("list children %v", got)
	}
	// equal values: the new instance goes after the existing ones
	ll3 := slices.Clone(c.kids.schemaSpan().Collect()[:2])
	n3 := f.term(t, f.ll, "3")
	mustLink(t, tr.insert(c, n3, insertDefault))
	if c.kids.At(2) != n3 || c.kids.At(0) != ll3[0] {
		t.Fatal("equal value not inserted after the existing ones")
	}
	// insertLast appends, insertLastBySchema goes after its schema node's instances unsorted
	n1 := f.term(t, f.ll, "1")
	mustLink(t, tr.insert(c, n1, insertLastBySchema))
	if i := slices.Index(c.kids.schemaSpan().Collect(), n1); c.kids.At(i-1).value.Canonical() != "9" {
		t.Fatalf("last by schema at %d", i)
	}
}

// TestInsertWork: system-ordered values inserted in reverse cost O(log n) comparisons each, in
// order O(1) beyond the run lookup (design 07 §4); counted, not timed.
func TestInsertWork(t *testing.T) {
	f := newFixture()
	f.ll.Type = &schema.Type{Base: schema.Uint16}
	n := raceScale(20000, 4)
	for _, reversed := range []bool{true, false} {
		tr := newTree(f.set)
		c := newInner(f.c)
		mustLink(t, tr.insert(nil, c, insertDefault))
		tr.work.Store(0)
		tr.work.visits = true
		for i := range n {
			v := i + 1
			if reversed {
				v = n - i
			}
			mustLink(t, tr.insert(c, f.term(t, f.ll, fmt.Sprint(v)), insertDefault))
		}
		for i, k := range c.kids.SchemaAll() {
			if k.value.Canonical() != fmt.Sprint(i+1) {
				t.Fatalf("position %d holds %s", i, k.value.Canonical())
			}
		}
		// rb_insert_node descends the retained tree (height up to 2*log2 n), then recolours; the
		// storage's AVL insertion and the run's bound searches descend the storage (design 14)
		if limit := n * (9*bits.Len(uint(n)) + 4); int(tr.work.Load()) > limit {
			t.Fatalf("reversed %v: %d comparisons, limit %d", reversed, int(tr.work.Load()), limit)
		}
	}
}

// TestResortRun: a run appended out of value order (no RB tree in libyang) is sorted by the next
// sorted insertion (lyds_additionally_create_rb_tree): [9,1,5,7] + 6 = 1,5,6,7,9.
func TestResortRun(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	mustLink(t, tr.insert(nil, c, insertDefault))
	for _, v := range []string{"9", "1", "5", "7"} {
		mustLink(t, tr.insert(c, f.term(t, f.ll, v), insertLast))
	}
	if got := names(c.Children()); !reflect.DeepEqual(got, []string{"ll=9", "ll=1", "ll=5", "ll=7"}) {
		t.Fatalf("appended: %v", got)
	}
	mustLink(t, tr.insert(c, f.term(t, f.ll, "6"), insertDefault))
	if got := names(c.Children()); !reflect.DeepEqual(got, []string{"ll=1", "ll=5", "ll=6", "ll=7", "ll=9"}) {
		t.Fatalf("re-sorted: %v", got)
	}
}

// TestOpaqueTail: insertLast (LYD_INSERT_NODE_LAST) appends after the last sibling whatever it
// is: input order is kept and a schema node follows the opaque one (design 15).
func TestOpaqueTail(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	mustLink(t, tr.insert(nil, c, insertDefault))
	mustLink(t, tr.insert(c, f.term(t, f.z, "z"), insertLast))
	mustLink(t, tr.insert(c, newOpaque(opaque{Name: "op"}), insertLast))
	mustLink(t, tr.insert(c, f.term(t, f.x, "x"), insertLast))
	mustLink(t, tr.insert(c, f.term(t, f.ll, "1"), insertLast))
	if got := names(c.Children()); !reflect.DeepEqual(got, []string{"z=z", "op", "x=x", "ll=1"}) {
		t.Fatalf("%v", got)
	}
	if !panics(func() { mustLink(t, tr.insert(c, c.kids.At(0), insertDefault)) }) {
		t.Fatal("inserting a linked node must panic")
	}
}

func panics(f func()) (p bool) {
	defer func() { p = recover() != nil }()
	f()
	return false
}

// TestUnlinkAll: one compaction per sibling list; the index and default flags follow.
func TestUnlinkAll(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	c.flags = FlagDefault
	mustLink(t, tr.insert(nil, c, insertDefault))
	var odd []*Node
	for i := range 1000 {
		n := f.term(t, f.ll, fmt.Sprint(i%256))
		mustLink(t, tr.insert(c, n, insertDefault))
		if i%2 == 1 {
			odd = append(odd, n)
		}
	}
	d := f.term(t, f.z, "d")
	d.flags = FlagDefault
	mustLink(t, tr.insert(c, d, insertDefault))
	if err := tr.unlinkAll(odd); err != nil {
		t.Fatal(err)
	}
	if c.kids.SchemaLen() != 501 || odd[0].parent != nil {
		t.Fatalf("%d children left", c.kids.SchemaLen())
	}
	if err := tr.unlinkAll(slices.Clone(c.kids.schemaSpan().Collect()[:500])); err != nil {
		t.Fatal(err)
	}
	if c.kids.SchemaLen() != 1 || len(c.kids.ht) != 1 || c.flags&FlagDefault == 0 {
		t.Fatalf("left %v, index %d, flags %x", names(c.Children()), len(c.kids.ht), c.flags)
	}
	key := f.makeList(t, tr, c, "k", "").kids.At(0)
	if err := tr.unlinkAll([]*Node{d, key}); err == nil || d.parent == nil {
		t.Fatal("a key in the batch must refuse the whole batch")
	}
}

// TestNPContainerDefault: an explicit child clears Default up the NP-container ancestors;
// unlinking the last explicit child sets it again (lyd_np_cont_dflt_del/set).
func TestNPContainerDefault(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	c.flags = FlagDefault
	mustLink(t, tr.insert(nil, c, insertDefault))
	d := f.term(t, f.z, "d")
	d.flags = FlagDefault
	mustLink(t, tr.insert(c, d, insertDefault))
	if c.flags&FlagDefault == 0 {
		t.Fatal("default child cleared Default")
	}
	e := f.term(t, f.x, "e")
	mustLink(t, tr.insert(c, e, insertDefault))
	if c.flags&FlagDefault != 0 {
		t.Fatal("explicit child kept Default")
	}
	if err := unlinkTree(e); err != nil {
		t.Fatal(err)
	}
	if c.flags&FlagDefault == 0 || e.parent != nil {
		t.Fatal("unlink: Default not set back")
	}
}

// TestKeyRefusal: a linked key cannot be unlinked or freed (lyd_unlink_check, lyd_free_tree).
func TestKeyRefusal(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	mustLink(t, tr.insert(nil, c, insertDefault))
	l := f.makeList(t, tr, c, "k1", "v")
	key := l.kids.At(0)
	var oe *opError
	var ve *ValidationError
	if err := key.Unlink(); !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" ||
		len(ve.Diags) != 1 || ve.Diags[0].Msg != `Cannot unlink a list key "k", unlink the list instance instead.` {
		t.Fatalf("public unlink: %v", err)
	}
	if err := unlinkTree(key); !errors.As(err, &oe) || oe.Err != "LY_EINVAL" ||
		oe.Msg != `Cannot unlink a list key "k", unlink the list instance instead.` {
		t.Fatalf("unlink: %v", err)
	}
	if err := freeTree(key); !errors.As(err, &oe) || oe.Msg != `Cannot free a list key "k", free the list instance instead.` {
		t.Fatalf("free: %v", err)
	}
	if key.parent != l || l.kids.SchemaLen() != 2 {
		t.Fatal("key was unlinked")
	}
	if err := freeTree(l); err != nil || c.kids.SchemaLen() != 0 {
		t.Fatalf("free list: %v", err)
	}
}

// TestFindSibling: lookups by schema, by value and by keys through the index, confirmed by
// value equality; dup-inst lists compare whole subtrees in order.
func TestFindSibling(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	mustLink(t, tr.insert(nil, c, insertDefault))
	for _, v := range []string{"5", "2"} {
		mustLink(t, tr.insert(c, f.term(t, f.ll, v), insertDefault))
	}
	l1 := f.makeList(t, tr, c, "a", "")
	if got := tr.findFirst(&c.kids, f.term(t, f.ll, "5")); got == nil || got.value.Canonical() != "5" {
		t.Fatalf("leaf-list 5: %v", got)
	}
	if tr.findFirst(&c.kids, f.term(t, f.ll, "6")) != nil {
		t.Fatal("found absent value")
	}
	// the index is built now: later inserts and unlinks keep it current
	l2 := f.makeList(t, tr, c, "b", "")
	probe := newInner(f.l)
	mustLink(t, tr.insert(probe, f.term(t, f.lk, "b"), insertDefault))
	if got := tr.findFirst(&c.kids, probe); got != l2 {
		t.Fatalf("list b: %v", got)
	}
	_ = unlink(l2)
	if tr.findFirst(&c.kids, probe) != nil {
		t.Fatal("unlinked instance still indexed")
	}
	if tr.schemaLookup(&c.kids, f.l) != l1 || tr.schemaLookup(&c.kids, f.z) != nil {
		t.Fatal("schemaLookup")
	}
	// state leaf-list: equal instances allowed, the first in order is found
	s1, s2 := f.term(t, f.sl, "4"), f.term(t, f.sl, "4")
	mustLink(t, tr.insert(c, s1, insertDefault))
	mustLink(t, tr.insert(c, s2, insertDefault))
	if tr.findFirst(&c.kids, f.term(t, f.sl, "4")) != s1 {
		t.Fatal("dup-inst first")
	}
	// keyless list: whole subtree compared
	k1, k2 := newInner(f.kl), newInner(f.kl)
	mustLink(t, tr.insert(k1, f.term(t, f.klv, "p"), insertDefault))
	mustLink(t, tr.insert(k2, f.term(t, f.klv, "q"), insertDefault))
	mustLink(t, tr.insert(c, k1, insertDefault))
	mustLink(t, tr.insert(c, k2, insertDefault))
	q := newInner(f.kl)
	mustLink(t, tr.insert(q, f.term(t, f.klv, "q"), insertDefault))
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
	mustLink(t, t1.insert(nil, c1, insertDefault))
	mustLink(t, t2.insert(nil, c2, insertDefault))
	f.makeList(t, t1, c1, "a", "1")
	f.makeList(t, t1, c1, "b", "2")
	f.makeList(t, t2, c2, "b", "2")
	f.makeList(t, t2, c2, "a", "1")
	if !compareSingle(t1, c1, c2, true) {
		t.Fatal("equal trees differ")
	}
	c2.kids.At(1).kids.At(1).value = f.term(t, f.lv, "3").value
	if compareSingle(t1, c1, c2, true) {
		t.Fatal("different trees equal")
	}
}

// TestAllPreOrder: All walks a subtree in pre-order.
func TestAllPreOrder(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	mustLink(t, tr.insert(nil, c, insertDefault))
	f.makeList(t, tr, c, "a", "1")
	mustLink(t, tr.insert(c, f.term(t, f.z, "z"), insertDefault))
	if got := names(c.All()); !reflect.DeepEqual(got, []string{"c", "l[a]", "k=a", "v=1", "z=z"}) {
		t.Fatalf("%v", got)
	}
}

// TestFindFirstHashOrder: with a children hash table (4+ schema children) libyang's lookup
// returns the first record of the bucket in insertion order; without one, the first sibling.
// User-ordered [1,2,3,4] with a new 3 inserted before 1: the old 3 with the table, the new one
// without (VERIFY(dup/first-match)).
func TestFindFirstHashOrder(t *testing.T) {
	f := newFixture()
	for _, withHT := range []bool{true, false} {
		tr := newTree(f.set)
		c := newInner(f.c)
		mustLink(t, tr.insert(nil, c, insertDefault))
		vals := []string{"1", "2", "3", "4"}
		if !withHT {
			vals = vals[:2]
			vals[1] = "3"
		}
		var old *Node
		for _, v := range vals {
			n := f.term(t, f.ul, v)
			mustLink(t, tr.insert(c, n, insertDefault))
			if v == "3" {
				old = n
			}
		}
		fresh := f.term(t, f.ul, "3")
		mustLink(t, tr.insertBefore(c.kids.At(0), fresh))
		want := old
		if !withHT {
			want = fresh
		}
		if (c.kids.ht != nil) != withHT || tr.findFirst(&c.kids, f.term(t, f.ul, "3")) != want {
			t.Fatalf("hash table %v: wrong instance", withHT)
		}
	}
}

// TestCompareLiteral: lyd_compare_single as libyang has it. A plain leaf compares canonical
// text (a union "1" stored as int equals "1" stored as string, VERIFY(cmp/union-leaf-text)); a
// leaf-list also compares the hashed value, so those differ; opaque nodes compare values only
// (VERIFY(cmp/opaque-value-only)); with a hash table a leaf target matches by schema node.
func TestCompareLiteral(t *testing.T) {
	f := newFixture()
	u := &schema.Type{Base: schema.Union, Union: []*schema.Type{{Base: schema.Int8}, {Base: schema.String}}}
	store := func(s *schema.Node, kind string) *Node {
		v, d := types.Store(u, "1", types.FormatJSON, types.JSONHints(kind), nil, s)
		if d != nil {
			t.Fatal(d.Msg)
		}
		return newTerm(s, v)
	}
	f.z.Type, f.ll.Type = u, u
	tr := newTree(f.set)
	if !compareSingle(tr, store(f.z, "number"), store(f.z, "string"), false) {
		t.Error("plain leaf: canonical text must decide")
	}
	if compareSingle(tr, store(f.ll, "number"), store(f.ll, "string"), false) {
		t.Error("leaf-list: the hashed value must differ")
	}
	if !compareSingle(tr, newOpaque(opaque{Name: "a", Value: "v"}), newOpaque(opaque{Name: "b", Value: "v"}), true) {
		t.Error("opaque: value only")
	}
	c := newInner(f.c)
	mustLink(t, tr.insert(nil, c, insertDefault))
	for _, v := range []string{"1", "2", "3"} {
		mustLink(t, tr.insert(c, f.term(t, f.sl, v), insertDefault))
	}
	z := f.term(t, f.z, "zz")
	f.z.Type = f.str
	mustLink(t, tr.insert(c, z, insertDefault))
	if c.kids.ht == nil || tr.findFirst(&c.kids, f.term(t, f.z, "other")) != z {
		t.Error("hash table: a leaf matches by schema node")
	}
	// a keyed list without its keys is never found
	if tr.findFirst(&c.kids, newInner(f.l)) != nil {
		t.Error("list target without keys")
	}
}

// TestUnionKeyHashIdentity checks that keyed-list hashing retains the selected union member even
// though the canonical-text bucket key is the same for JSON 1 and "1".
func TestUnionKeyHashIdentity(t *testing.T) {
	f := newFixture()
	f.lk.Type = &schema.Type{Base: schema.Union, Union: []*schema.Type{
		{Base: schema.Int8},
		{Base: schema.String},
	}}
	tr := newTree(f.set)
	c := newInner(f.c)
	mustLink(t, tr.insert(nil, c, insertDefault))
	for _, v := range []string{"2", "3", "4"} {
		mustLink(t, tr.insert(c, f.term(t, f.ll, v), insertDefault))
	}
	list := func(kind string) *Node {
		l := newInner(f.l)
		v, d := types.Store(f.lk.Type, "1", types.FormatJSON, types.JSONHints(kind), nil, f.lk)
		if d != nil {
			t.Fatal(d.Msg)
		}
		mustLink(t, tr.insert(l, newTerm(f.lk, v), insertDefault))
		return l
	}
	number := list("number")
	mustLink(t, tr.insert(c, number, insertDefault))
	stringProbe := list("string")
	if c.kids.ht == nil {
		t.Fatal("children hash table was not built")
	}
	if hashEqual(number, stringProbe) {
		t.Fatal(`union key JSON 1 and "1" have equal hashes`)
	}
	if got := tr.findFirst(&c.kids, stringProbe); got != nil {
		t.Fatalf(`union key JSON "1" matched JSON 1: %v`, got)
	}
	numberProbe := list("number")
	if !hashEqual(number, numberProbe) {
		t.Fatal("equal union key members have different hashes")
	}
	if got := tr.findFirst(&c.kids, numberProbe); got != number {
		t.Fatalf("equal union key member lookup = %v, want %v", got, number)
	}
}

// Internal key removal updates only the positional discriminator; libyang keeps
// the stored hash and registration (tree_data.c:1187-1239).
func TestKeyUnlinkReindex(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	mustLink(t, tr.insert(nil, c, insertDefault))
	var l *Node
	for _, k := range []string{"a", "b", "c", "d"} {
		l = f.makeList(t, tr, c, k, "")
	}
	probe := newInner(f.l)
	mustLink(t, tr.insert(probe, f.term(t, f.lk, "d"), insertDefault))
	if tr.findFirst(&c.kids, probe) != l {
		t.Fatal("list d not found")
	}
	_ = unlink(l.kids.At(0))
	if tr.findFirst(&c.kids, probe) != nil {
		t.Fatal("list found by a removed key")
	}
}

// TestConcurrentReads: lookups and comparisons only read the tree (go test -race).
func TestConcurrentReads(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	mustLink(t, tr.insert(nil, c, insertDefault))
	for i := range 50 {
		mustLink(t, tr.insert(c, f.term(t, f.ll, fmt.Sprint(i)), insertDefault))
	}
	f.makeList(t, tr, c, "a", "1")
	probe := f.term(t, f.ll, "7")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				if tr.findFirst(&c.kids, probe) == nil || tr.schemaLookup(&c.kids, f.z) != nil || !compareSingle(tr, c, c, true) {
					t.Error("lookup failed")
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestBulkWork: removing many instances that share a bucket (equal state leaf-list values) costs
// linear work, and alternating opaque/schema inserts cost the storage's O(log n) insertion each
// (counted, not timed; design 14).
func TestBulkWork(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	tr.work.visits = true
	c := newInner(f.c)
	mustLink(t, tr.insert(nil, c, insertDefault))
	const n = 20000
	var all []*Node
	for range n {
		s := f.term(t, f.sl, "1")
		mustLink(t, tr.insert(c, s, insertDefault))
		all = append(all, s)
		mustLink(t, tr.insert(c, newOpaque(opaque{Name: "o"}), insertDefault))
	}
	// each link inserts into the storage and reads its position's neighbours by rank: a few
	// O(log n) descents, counted as sibling visits
	if limit := 2 * n * (4*bits.Len(uint(2*n)) + 4); int(tr.work.Load()) > limit || c.kids.OpaqueLen() != n {
		t.Fatalf("alternating inserts: %d comparisons", int(tr.work.Load()))
	}
	tr.work.Store(0)
	tr.work.visits = true
	if err := tr.unlinkAll(all); err != nil {
		t.Fatal(err)
	}
	// Include the preflight batch scan now charged to the source hash budget, and the storage's
	// one compaction (Keep): a scan of all 2n siblings and the rebuild of the n survivors.
	if int(tr.work.Load()) > 9*n || c.kids.SchemaLen() != 0 || len(c.kids.ht) != 0 {
		t.Fatalf("bulk unlink: %d steps, %d left, %d buckets", int(tr.work.Load()), c.kids.SchemaLen(), len(c.kids.ht))
	}
}

// TestUnlinkAllWorkScaling checks one-pass batch removal for insertion orders that exercise
// sorted runs and large duplicate buckets. Quadrupling N must not approach quadratic growth.
func TestUnlinkAllWorkScaling(t *testing.T) {
	modes := []struct {
		name  string
		value func(int, int) int
	}{
		{"sorted", func(i, _ int) int { return i }},
		{"reverse", func(i, n int) int { return n - i }},
		{"shuffled", func(i, n int) int { return i * 73 % n }},
		{"duplicate-heavy", func(i, _ int) int { return i * 73 % 8 }},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			work := func(count int) int64 {
				t.Helper()
				f := newFixture()
				f.ll.Type = &schema.Type{Base: schema.Uint32}
				tr := newTree(f.set)
				c := newInner(f.c)
				mustLink(t, tr.insert(nil, c, insertDefault))
				all := make([]*Node, 0, count)
				for i := range count {
					n := f.term(t, f.ll, fmt.Sprint(mode.value(i, count)))
					mustLink(t, tr.insert(c, n, insertDefault))
					all = append(all, n)
				}
				tr.work.Store(0)
				tr.work.visits = true
				if err := tr.unlinkAll(all); err != nil {
					t.Fatal(err)
				}
				if c.kids.len() != 0 {
					t.Fatalf("%d siblings remain", c.kids.len())
				}
				return tr.work.Load()
			}

			const n = 64
			wN, w4N := work(n), work(4*n)
			if w4N > 6*wN {
				t.Fatalf("work(4N) = %d, want <= 6 * work(N) = %d", w4N, 6*wN)
			}
		})
	}
}

// TestRBTreeRunEmptied checks that removing the last indexed members resets a run even when
// appended members remain, so the next sorted insertion rebuilds and re-sorts it.
func TestRBTreeRunEmptied(t *testing.T) {
	// issue #101/#208: 3,5 are indexed, 9 and 1 are appended outside the tree (LYD_DUP_NO_LYDS),
	// then a bulk trim removes indexed nodes and 4 is inserted sorted. The native orders are the
	// goldens of seq/trim-lyds-emptied-run and seq/trim-lyds-partial-run, which the conformance
	// engine runs (their ordered parse is ParseOptions.Ordered, dup no_lyds DupOptions.NoSort): when the last indexed instance
	// goes, the next sorted insertion rebuilds and re-sorts the run; while one survives, 4 goes
	// into the remaining index and the appended instances stay where they are.
	for _, tc := range []struct {
		keep  string
		empty bool
		want  []string
	}{
		{"/b:c/ll[. = 9 or . = 1]", true, []string{"ll=1", "ll=4", "ll=9"}},                   // seq/trim-lyds-emptied-run
		{"/b:c/ll[. = 5 or . = 9 or . = 1]", false, []string{"ll=4", "ll=5", "ll=9", "ll=1"}}, // seq/trim-lyds-partial-run
	} {
		f := newFixture()
		tr := newTree(f.set)
		c := newInner(f.c)
		mustLink(t, tr.insert(nil, c, insertDefault))
		for _, v := range []string{"3", "5"} {
			mustLink(t, tr.insert(c, f.term(t, f.ll, v), insertDefault))
		}
		if !c.kids.hasRB(f.ll) || !c.kids.At(0).inRB || !c.kids.At(1).inRB {
			t.Fatal("sorted instances did not build the run index")
		}
		for _, v := range []string{"9", "1"} {
			mustLink(t, tr.insert(c, f.term(t, f.ll, v), insertLastBySchema))
		}
		if !c.kids.hasUnsorted(f.ll) || c.kids.At(2).inRB || c.kids.At(3).inRB {
			t.Fatal("appended instances unexpectedly joined the run index")
		}
		if diags, err := tr.TrimXPath(tc.keep, XPathOptions{}); err != nil {
			t.Fatal(err, diags)
		}
		if tc.empty && (c.kids.hasRB(f.ll) || c.kids.hasUnsorted(f.ll)) {
			t.Fatal("empty run index state survived bulk removal")
		}
		mustLink(t, tr.insert(c, f.term(t, f.ll, "4"), insertDefault))
		if got := names(c.Children()); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: %v, want %v", tc.keep, got, tc.want)
		}
	}
}

// TestRBTreeBulkUnlinkMatchesSequential checks that unlinkAll applies the same run-state rule as
// unlink after each node, both before and after the next sorted insertion.
func TestRBTreeBulkUnlinkMatchesSequential(t *testing.T) {
	makeRun := func() (*fixture, *Tree, *Node, []*Node) {
		f := newFixture()
		tr := newTree(f.set)
		c := newInner(f.c)
		mustLink(t, tr.insert(nil, c, insertDefault))
		var indexed []*Node
		for _, v := range []string{"3", "5"} {
			n := f.term(t, f.ll, v)
			mustLink(t, tr.insert(c, n, insertDefault))
			indexed = append(indexed, n)
		}
		for _, v := range []string{"9", "1"} {
			mustLink(t, tr.insert(c, f.term(t, f.ll, v), insertLastBySchema))
		}
		return f, tr, c, indexed
	}

	bulkFixture, bulk, bulkParent, bulkIndexed := makeRun()
	sequentialFixture, sequential, sequentialParent, sequentialIndexed := makeRun()
	if err := bulk.unlinkAll(bulkIndexed); err != nil {
		t.Fatal(err)
	}
	for _, n := range sequentialIndexed {
		_ = unlink(n)
	}
	if got, want := names(bulkParent.Children()), names(sequentialParent.Children()); !reflect.DeepEqual(got, want) || !reflect.DeepEqual(got, []string{"ll=9", "ll=1"}) {
		t.Fatalf("after removal: bulk %v, sequential %v", got, want)
	}

	mustLink(t, bulk.insert(bulkParent, bulkFixture.term(t, bulkFixture.ll, "4"), insertDefault))
	mustLink(t, sequential.insert(sequentialParent, sequentialFixture.term(t, sequentialFixture.ll, "4"), insertDefault))
	if got, want := names(bulkParent.Children()), names(sequentialParent.Children()); !reflect.DeepEqual(got, want) || !reflect.DeepEqual(got, []string{"ll=1", "ll=4", "ll=9"}) {
		t.Fatalf("after sorted insert: bulk %v, sequential %v", got, want)
	}
}

// TestRBTreeRun: instances appended to a run after its RB tree exists stay outside it; the next
// sorted insertion goes right after its RB predecessor (lyds_link_data_node), the run is not
// re-sorted: [3,4,5] sorted, 1 appended by schema, then 6 → 3,4,5,6,1.
func TestRBTreeRun(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	mustLink(t, tr.insert(nil, c, insertDefault))
	for _, v := range []string{"4", "3", "5"} {
		mustLink(t, tr.insert(c, f.term(t, f.ll, v), insertDefault))
	}
	mustLink(t, tr.insert(c, f.term(t, f.ll, "1"), insertLastBySchema))
	mustLink(t, tr.insert(c, f.term(t, f.ll, "6"), insertDefault))
	mustLink(t, tr.insert(c, f.term(t, f.ll, "0"), insertDefault))
	if got := names(c.Children()); !reflect.DeepEqual(got, []string{"ll=0", "ll=3", "ll=4", "ll=5", "ll=6", "ll=1"}) {
		t.Fatalf("%v", got)
	}
}

// TestParserSortedRuns adapts the parser insertion check to main's deferred operation
// runs. Both kinds share the same stable order; appends outside the RB tree stay eager.
func TestParserSortedRuns(t *testing.T) {
	for _, operation := range []bool{false, true} {
		t.Run(fmt.Sprintf("operation=%v", operation), func(t *testing.T) {
			f := newFixture()
			parentSchema, sn := f.c, f.ll
			if operation {
				rpc := &schema.Node{Kind: schema.RPC, Name: "r", Module: f.a}
				input := &schema.Node{Kind: schema.Input, Name: "input", Module: f.a, Parent: rpc}
				sn = &schema.Node{Kind: schema.LeafList, Name: "v", Module: f.a, Parent: input, Type: f.ll.Type}
				rpc.Children, input.Children = []*schema.Node{input}, []*schema.Node{sn}
				parentSchema = rpc
			}
			tr := newTree(f.set)
			parent := newInner(parentSchema)
			mustLink(t, tr.insert(nil, parent, insertDefault))
			lc := &lydCtx{ctx: context.Background(), tree: tr}
			for _, v := range []string{"1", "3"} {
				mustLink(t, lc.nodeInsert(parent, nil, f.term(t, sn, v)))
			}
			if len(lc.dirtyOrd) != 0 {
				t.Fatal("ascending run has pending work")
			}
			equal := f.term(t, sn, "2")
			mustLink(t, lc.nodeInsert(parent, nil, equal))
			mustLink(t, lc.nodeInsert(parent, nil, f.term(t, sn, "2")))
			if len(lc.dirtyOrd) != 1 {
				t.Fatal("shuffled run was not deferred")
			}
			if err := lc.flushDirtyAll(); err != nil {
				t.Fatal(err)
			}
			if !slices.IsSortedFunc(parent.kids.schemaSpan().Collect(), compareSorted) || parent.kids.At(1) != equal {
				t.Fatal("run did not retain stable value order")
			}
			outside := f.term(t, sn, "0")
			mustLink(t, tr.insert(parent, outside, insertLastBySchema))
			mustLink(t, lc.nodeInsert(parent, nil, f.term(t, sn, "4")))
			if len(lc.dirtyOrd) != 0 || outside.inRB || parent.kids.At(parent.kids.SchemaLen()-1) != outside {
				t.Fatal("parser reordered an instance outside the RB tree")
			}
		})
	}
}
