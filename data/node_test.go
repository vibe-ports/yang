// SPDX-License-Identifier: BSD-3-Clause

package data

import (
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

// TestInsertWork: system-ordered values inserted in reverse cost O(log n) comparisons each, in
// order O(1) beyond the run lookup (design 07 §4); counted, not timed.
func TestInsertWork(t *testing.T) {
	f := newFixture()
	f.ll.Type = &schema.Type{Base: schema.Uint16}
	const n = 20000
	for _, reversed := range []bool{true, false} {
		tr := newTree(f.set)
		c := newInner(f.c)
		tr.insert(nil, c, insertDefault)
		tr.work = 0
		for i := range n {
			v := i + 1
			if reversed {
				v = n - i
			}
			tr.insert(c, f.term(t, f.ll, fmt.Sprint(v)), insertDefault)
		}
		for i, k := range c.kids.list {
			if k.value.Canonical() != fmt.Sprint(i+1) {
				t.Fatalf("position %d holds %s", i, k.value.Canonical())
			}
		}
		if limit := n * (3*bits.Len(n) + 4); tr.work > limit {
			t.Fatalf("reversed %v: %d comparisons, limit %d", reversed, tr.work, limit)
		}
	}
}

// TestResortRun: a run appended out of value order (no RB tree in libyang) is sorted by the next
// sorted insertion (lyds_additionally_create_rb_tree): [9,1,5,7] + 6 = 1,5,6,7,9.
func TestResortRun(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	for _, v := range []string{"9", "1", "5", "7"} {
		tr.insert(c, f.term(t, f.ll, v), insertLast)
	}
	if got := names(c.Children()); !reflect.DeepEqual(got, []string{"ll=9", "ll=1", "ll=5", "ll=7"}) {
		t.Fatalf("appended: %v", got)
	}
	tr.insert(c, f.term(t, f.ll, "6"), insertDefault)
	if got := names(c.Children()); !reflect.DeepEqual(got, []string{"ll=1", "ll=5", "ll=6", "ll=7", "ll=9"}) {
		t.Fatalf("re-sorted: %v", got)
	}
}

// TestOpaqueTail: insertLast keeps schema nodes before the opaque tail, and a node that would
// break schema order is placed by schema, so siblings stay monotone for the binary searches.
func TestOpaqueTail(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	tr.insert(c, f.term(t, f.z, "z"), insertLast)
	tr.insert(c, newOpaque(opaque{Name: "op"}), insertLast)
	tr.insert(c, f.term(t, f.x, "x"), insertLast)
	tr.insert(c, f.term(t, f.ll, "1"), insertLast)
	if got := names(c.Children()); !reflect.DeepEqual(got, []string{"ll=1", "x=x", "z=z", "op"}) {
		t.Fatalf("%v", got)
	}
	if !panics(func() { tr.insert(c, c.kids.list[0], insertDefault) }) {
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
	tr.insert(nil, c, insertDefault)
	var odd []*Node
	for i := range 1000 {
		n := f.term(t, f.ll, fmt.Sprint(i%256))
		tr.insert(c, n, insertDefault)
		if i%2 == 1 {
			odd = append(odd, n)
		}
	}
	d := f.term(t, f.z, "d")
	d.flags = FlagDefault
	tr.insert(c, d, insertDefault)
	if err := tr.unlinkAll(odd); err != nil {
		t.Fatal(err)
	}
	if len(c.kids.list) != 501 || odd[0].parent != nil {
		t.Fatalf("%d children left", len(c.kids.list))
	}
	if err := tr.unlinkAll(slices.Clone(c.kids.list[:500])); err != nil {
		t.Fatal(err)
	}
	if len(c.kids.list) != 1 || len(c.kids.ht) != 1 || c.flags&FlagDefault == 0 {
		t.Fatalf("left %v, index %d, flags %x", names(c.Children()), len(c.kids.ht), c.flags)
	}
	key := f.list(t, tr, c, "k", "").kids.list[0]
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
	tr.insert(nil, c, insertDefault)
	d := f.term(t, f.z, "d")
	d.flags = FlagDefault
	tr.insert(c, d, insertDefault)
	if c.flags&FlagDefault == 0 {
		t.Fatal("default child cleared Default")
	}
	e := f.term(t, f.x, "e")
	tr.insert(c, e, insertDefault)
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

// TestFindFirstHashOrder: with a children hash table (4+ schema children) libyang's lookup
// returns the first record of the bucket in insertion order; without one, the first sibling.
// User-ordered [1,2,3,4] with a new 3 inserted before 1: the old 3 with the table, the new one
// without (VERIFY(dup/first-match)).
func TestFindFirstHashOrder(t *testing.T) {
	f := newFixture()
	for _, withHT := range []bool{true, false} {
		tr := newTree(f.set)
		c := newInner(f.c)
		tr.insert(nil, c, insertDefault)
		vals := []string{"1", "2", "3", "4"}
		if !withHT {
			vals = vals[:2]
			vals[1] = "3"
		}
		var old *Node
		for _, v := range vals {
			n := f.term(t, f.ul, v)
			tr.insert(c, n, insertDefault)
			if v == "3" {
				old = n
			}
		}
		fresh := f.term(t, f.ul, "3")
		tr.insertBefore(c.kids.list[0], fresh)
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
	tr.insert(nil, c, insertDefault)
	for _, v := range []string{"1", "2", "3"} {
		tr.insert(c, f.term(t, f.sl, v), insertDefault)
	}
	z := f.term(t, f.z, "zz")
	f.z.Type = f.str
	tr.insert(c, z, insertDefault)
	if c.kids.ht == nil || tr.findFirst(&c.kids, f.term(t, f.z, "other")) != z {
		t.Error("hash table: a leaf matches by schema node")
	}
	// a keyed list without its keys is never found
	if tr.findFirst(&c.kids, newInner(f.l)) != nil {
		t.Error("list target without keys")
	}
}

// TestKeyUnlinkRehash: removing a key (internally) moves the list out of its old bucket in the
// parent's table (lyd_unlink_hash leaves libyang's stale; the index must not serve it).
func TestKeyUnlinkRehash(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	var l *Node
	for _, k := range []string{"a", "b", "c", "d"} {
		l = f.list(t, tr, c, k, "")
	}
	probe := newInner(f.l)
	probe.kids.list = []*Node{f.term(t, f.lk, "d")}
	if tr.findFirst(&c.kids, probe) != l {
		t.Fatal("list d not found")
	}
	unlink(l.kids.list[0])
	if tr.findFirst(&c.kids, probe) != nil {
		t.Fatal("list found by a removed key")
	}
}

// TestConcurrentReads: lookups and comparisons only read the tree (go test -race).
func TestConcurrentReads(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	for i := range 50 {
		tr.insert(c, f.term(t, f.ll, fmt.Sprint(i)), insertDefault)
	}
	f.list(t, tr, c, "a", "1")
	probe := f.term(t, f.ll, "7")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				if tr.findFirst(&c.kids, probe) == nil || tr.findSchema(&c.kids, f.z) != nil || !compareSingle(tr, c, c, true) {
					t.Error("lookup failed")
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestBulkWork: removing many instances that share a bucket (equal state leaf-list values) and
// alternating opaque/schema inserts cost linear work (counted, not timed).
func TestBulkWork(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	const n = 20000
	var all []*Node
	for range n {
		s := f.term(t, f.sl, "1")
		tr.insert(c, s, insertDefault)
		all = append(all, s)
		tr.insert(c, newOpaque(opaque{Name: "o"}), insertDefault)
	}
	if tr.work > 4*n || len(c.kids.opq) != n {
		t.Fatalf("alternating inserts: %d comparisons", tr.work)
	}
	tr.work = 0
	if err := tr.unlinkAll(all); err != nil {
		t.Fatal(err)
	}
	if tr.work > 4*n || len(c.kids.list) != 0 || len(c.kids.ht) != 0 {
		t.Fatalf("bulk unlink: %d steps, %d left, %d buckets", tr.work, len(c.kids.list), len(c.kids.ht))
	}
}

// TestRBTreeRun: instances appended to a run after its RB tree exists stay outside it; the next
// sorted insertion goes right after its RB predecessor (lyds_link_data_node), the run is not
// re-sorted: [3,4,5] sorted, 1 appended by schema, then 6 → 3,4,5,6,1.
func TestRBTreeRun(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	for _, v := range []string{"4", "3", "5"} {
		tr.insert(c, f.term(t, f.ll, v), insertDefault)
	}
	tr.insert(c, f.term(t, f.ll, "1"), insertLastBySchema)
	tr.insert(c, f.term(t, f.ll, "6"), insertDefault)
	tr.insert(c, f.term(t, f.ll, "0"), insertDefault)
	if got := names(c.Children()); !reflect.DeepEqual(got, []string{"ll=0", "ll=3", "ll=4", "ll=5", "ll=6", "ll=1"}) {
		t.Fatalf("%v", got)
	}
}
