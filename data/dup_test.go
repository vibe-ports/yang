// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// dupFixture is a pv2-edit tree: c/a, two list instances (one with v), two leaf-list values and
// the NP container np with d.
func dupFixture(t *testing.T, set *schema.Set) *Tree {
	t.Helper()
	tr := newTree(set)
	for _, s := range []editStep{{set: "/pv2-edit:c/a", value: "x"}, {set: "/pv2-edit:c/l[k='a']/v", value: "1"},
		{set: "/pv2-edit:c/l[k='b']"}, {set: "/pv2-edit:c/ll", value: "b"}, {set: "/pv2-edit:c/ll", value: "a"},
		{set: "/pv2-edit:c/np/d", value: "7"}} {
		if rc, d := s.apply(tr); rc != "LY_SUCCESS" {
			t.Fatal(rc, d)
		}
	}
	return tr
}

func findNode(t *testing.T, tr *Tree, path string) *Node {
	t.Helper()
	n, err := tr.Find(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return n
}

// treeOf is the tree a duplicate (or its duplicated parents) went to.
func treeOf(n *Node) *Tree {
	for n.parent != nil {
		n = n.parent
	}
	return n.tree
}

// TestDupSiblings: lyd_dup_siblings / lyd_dup_single in one context, with and without
// LYD_DUP_RECURSIVE (a list keeps its keys), the flags (LYD_DUP_WITH_FLAGS or default + new) and
// LYD_DUP_NO_META.
func TestDupSiblings(t *testing.T) {
	set := editSet(t)
	tr := dupFixture(t, set)
	c := findNode(t, tr, "/pv2-edit:c")
	a := findNode(t, tr, "/pv2-edit:c/a")
	a.flags &^= FlagNew

	d, err := dupTo(a, nil, nil, dupRecursive, true) // a and the siblings after it, to the top level
	if err != nil {
		t.Fatal(err)
	}
	got := typedDump(treeOf(d))
	want := []string{"/pv2-edit:a new = x", "/pv2-edit:np new", "/pv2-edit:np/d new = 7", "/pv2-edit:l[k='a'] new",
		"/pv2-edit:l[k='a']/k new = a", "/pv2-edit:l[k='a']/v new = 1", "/pv2-edit:l[k='b'] new", "/pv2-edit:l[k='b']/k new = b",
		"/pv2-edit:ll[.='a'] new = a", "/pv2-edit:ll[.='b'] new = b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("siblings:\n got  %q\n want %q", got, want)
	}

	// into their own parent: each sibling once (D-0066; libyang loops over its own copies)
	ll := findNode(t, tr, "/pv2-edit:c/ll[.='a']")
	if _, err := dupTo(ll, nil, c, dupRecursive, true); err != nil {
		t.Fatal(err)
	}
	if got := typedDump(tr); !slices.Equal(got[len(got)-4:], []string{"/pv2-edit:c/ll[.='a'] new = a",
		"/pv2-edit:c/ll[.='a'] new = a", "/pv2-edit:c/ll[.='b'] new = b", "/pv2-edit:c/ll[.='b'] new = b"}) {
		t.Errorf("into their own parent: %q", got)
	}

	l := findNode(t, tr, "/pv2-edit:c/l[k='a']")
	d, err = dupTo(l, nil, nil, 0, false) // not recursive: the list keeps its key
	if err != nil {
		t.Fatal(err)
	}
	if got := typedDump(treeOf(d)); !slices.Equal(got, []string{"/pv2-edit:l[k='a'] new", "/pv2-edit:l[k='a']/k new = a"}) {
		t.Errorf("single: %q", got)
	}

	c.flags |= FlagWhenTrue
	c.meta = []*meta{{mod: set.Modules[len(set.Modules)-1], name: "m"}}
	for _, o := range []struct {
		opts  dupOpts
		flags Flags
		metas int
	}{{0, FlagNew | FlagDefault, 1}, {dupWithFlags, c.flags | FlagDefault, 1}, {dupNoMeta, FlagNew | FlagDefault, 0}} { // an empty NP container is default
		d, err := dupTo(c, nil, nil, o.opts, false)
		if err != nil {
			t.Fatal(err)
		}
		if d.flags != o.flags || len(d.meta) != o.metas || len(d.kids.nodes()) != 0 {
			t.Errorf("opts %b: flags %b meta %d kids %d", o.opts, d.flags, len(d.meta), len(d.kids.nodes()))
		}
	}
}

// TestDupWithParents: LYD_DUP_WITH_PARENTS builds the parents (a list with its keys) to the top,
// or connects them into a given parent; a parent none of the schema parents matches is refused.
func TestDupWithParents(t *testing.T) {
	set := editSet(t)
	tr := dupFixture(t, set)
	v := findNode(t, tr, "/pv2-edit:c/l[k='a']/v")

	d, err := dupTo(v, nil, nil, dupWithParents, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/pv2-edit:c new", "/pv2-edit:c/l[k='a'] new", "/pv2-edit:c/l[k='a']/k new = a", "/pv2-edit:c/l[k='a']/v new = 1"}
	if got := typedDump(treeOf(d)); !reflect.DeepEqual(got, want) {
		t.Errorf("to the top:\n got  %q\n want %q", got, want)
	}

	other := newTree(set)
	if _, err := other.NewPath("/pv2-edit:c/a", "y", NewPathOptions{}); err != nil {
		t.Fatal(err)
	}
	c2 := findNode(t, other, "/pv2-edit:c")
	if _, err := dupTo(v, nil, c2, dupWithParents, false); err != nil {
		t.Fatal(err)
	}
	if got := typedDump(other); !slices.Contains(got, "/pv2-edit:c/l[k='a']/v new = 1") || !slices.Contains(got, "/pv2-edit:c/a new = y") {
		t.Errorf("into a parent: %q", got)
	}

	a2 := findNode(t, other, "/pv2-edit:c/a")
	_, err = dupTo(v, nil, a2, dupWithParents, false)
	if err == nil || err.Error() != `None of the duplicated node "v" schema parents match the provided parent "a".` {
		t.Errorf("parent mismatch: %v", err)
	}
	// a key copied with its parents is the key of the duplicated list
	k := findNode(t, tr, "/pv2-edit:c/l[k='b']/k")
	if d, err := dupTo(k, nil, nil, dupWithParents, false); err != nil || d.parent == nil || d.parent.parent == nil {
		t.Errorf("key with parents: %v %v", d, err)
	}
}

// TestDupContexts: duplicates and comparisons across two sets compiled from the same modules
// (lyd_dup_siblings_to_ctx, lyd_find_schema_ctx, lyd_compare_single_schema across contexts) and
// libyang's errors.
func TestDupContexts(t *testing.T) {
	set1, set2 := editSet(t), editSet(t)
	tr := dupFixture(t, set1)
	c := findNode(t, tr, "/pv2-edit:c")

	d, err := dupTo(c, set2, nil, dupRecursive|dupWithFlags, false)
	if err != nil {
		t.Fatal(err)
	}
	for n := range d.All() {
		if n.schema == nil || !slices.Contains(set2.Modules, n.schema.Module) {
			t.Fatalf("%s is not of the target set", n.Name())
		}
	}
	if !reflect.DeepEqual(typedDump(treeOf(d)), typedDump(tr)) {
		t.Errorf("copy:\n got  %q\n want %q", typedDump(treeOf(d)), typedDump(tr))
	}
	if !compareSingle(tr, c, d, true) || !compareSingle(treeOf(d), d, c, true) {
		t.Error("equal trees of two contexts compare unequal")
	}
	// a leaf-list ordered differently is found by value across contexts
	ll := findNode(t, treeOf(d), "/pv2-edit:c/ll[.='a']")
	if m := tr.findFirst(&c.kids, ll); m == nil || m.value.Canonical() != "a" {
		t.Errorf("findFirst across contexts: %v", m)
	}
	if _, err := treeOf(d).NewPath("/pv2-edit:c/a", "z", NewPathOptions{Update: true}); err != nil {
		t.Fatal(err)
	}
	if compareSingle(tr, c, d, true) {
		t.Error("different trees of two contexts compare equal")
	}

	// into a parent of the target set; the context of the parent must be the target
	c2 := findNode(t, treeOf(d), "/pv2-edit:c")
	v := findNode(t, tr, "/pv2-edit:c/l[k='b']")
	if _, err := dupTo(v, set2, c2, dupRecursive, false); err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct {
		trg    *schema.Set
		parent *Node
		want   string
	}{
		{nil, c2, `Different "node" and "parent" contexts used in node duplication.`},
		{set1, c2, `Different "trg_ctx" and "parent" contexts used in node duplication.`},
		{&schema.Set{}, nil, `Module "pv2-edit" not present/implemented in the target context.`},
	} {
		if _, err := dupTo(v, e.trg, e.parent, 0, false); err == nil || err.Error() != e.want {
			t.Errorf("got %v, want %q", err, e.want)
		}
	}
	// a key with its parents: libyang looks the key up in the duplicated list by the source
	// schema node, which fails across contexts (LOGINT, LY_ENOTFOUND)
	var oe *opError
	if _, err := dupTo(findNode(t, tr, "/pv2-edit:c/l[k='b']/k"), set2, nil, dupWithParents, false); !errors.As(err, &oe) ||
		oe.Err != "LY_ENOTFOUND" {
		t.Errorf("key with parents across contexts: %v", err)
	}
	// a schema node the target lacks
	trimmed := editSet(t)
	tc := trimmed.Implemented("pv2-edit").Top[0]
	tc.Children = slices.DeleteFunc(tc.Children, func(n *schema.Node) bool { return n.Name == "np" })
	np := findNode(t, tr, "/pv2-edit:c/np")
	if _, err := dupTo(np, trimmed, nil, dupWithParents, false); err == nil ||
		err.Error() != `Schema node "/pv2-edit:c/np" not found in the target context.` {
		t.Errorf("missing node: %v", err)
	}
}

// TestDupGoldens replays the protocol-v2 seq/dup-* fixtures: the six edits of dupFixture, then
// the dup step (lyd_dup_single / lyd_dup_siblings into a parent, or replacing the tree) against
// the oracle's diagnostics and tree.
func TestDupGoldens(t *testing.T) {
	set := editSet(t)
	type dupStep struct {
		node, parent string
		opts         dupOpts
		siblings     bool
	}
	for name, s := range map[string]dupStep{
		"seq-dup-siblings":          {"/pv2-edit:c/a", "", dupRecursive, true},
		"seq-dup-single-keys":       {"/pv2-edit:c/l[k='a']", "", 0, false},
		"seq-dup-with-parents":      {"/pv2-edit:c/l[k='a']/v", "", dupWithParents, false},
		"seq-dup-with-parents-key":  {"/pv2-edit:c/l[k='b']/k", "", dupWithParents, false},
		"seq-dup-with-parents-into": {"/pv2-edit:c/l[k='a']/v", "/pv2-edit:c", dupWithParents, false},
		"seq-dup-parent-mismatch":   {"/pv2-edit:c/l[k='a']/v", "/pv2-edit:c/np", dupWithParents, false},
		"seq-dup-leaflist":          {"/pv2-edit:c/ll[.='a']", "", dupRecursive, true},
		"seq-dup-no-lyds":           {"/pv2-edit:c/ll[.='a']", "", dupRecursive | dupNoLyds, true},
	} {
		t.Run(name, func(t *testing.T) {
			want := loadSteps(t, name)
			tr := dupFixture(t, set)
			w := want[len(want)-1]
			var parent *Node
			if s.parent != "" {
				parent = findNode(t, tr, s.parent)
			}
			d, err := dupTo(findNode(t, tr, s.node), nil, parent, s.opts, s.siblings)
			if w.RC.Name != "LY_SUCCESS" {
				if len(w.Diagnostics) == 0 || err == nil || err.Error() != w.Diagnostics[0].Msg {
					t.Fatalf("got %v, golden %s %q", err, w.RC.Name, w.diags())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if parent == nil {
				tr = treeOf(d)
			}
			if got := typedDump(tr); !reflect.DeepEqual(got, w.dump()) {
				t.Errorf("tree:\n got  %q\n want %q", got, w.dump())
			}
		})
	}
}

// TestDupFailure: the probed D-0065 case. The target c holds z (here the leaf-list ll), the
// siblings a and b are copied into it and b has no schema node in the target context: libyang
// returns LY_ENOTFOUND (rc 5) and frees all of c's children, z included; the port removes only
// the copy of a.
func TestDupFailure(t *testing.T) {
	set1, set2 := editSet(t), editSet(t)
	c2 := set2.Implemented("pv2-edit").Top[0]
	c2.Children = slices.DeleteFunc(c2.Children, func(n *schema.Node) bool { return n.Name == "b" })
	src := newTree(set1)
	for _, s := range []editStep{{set: "/pv2-edit:c/a", value: "x"}, {set: "/pv2-edit:c/b", value: "1"}} {
		if rc, d := s.apply(src); rc != "LY_SUCCESS" {
			t.Fatal(rc, d)
		}
	}
	dst := newTree(set2)
	if _, err := dst.NewPath("/pv2-edit:c/ll", "z", NewPathOptions{}); err != nil {
		t.Fatal(err)
	}
	before := typedDump(dst)
	_, err := dupTo(findNode(t, src, "/pv2-edit:c/a"), set2, findNode(t, dst, "/pv2-edit:c"), dupRecursive, true)
	var oe *opError
	if !errors.As(err, &oe) || oe.Err != "LY_ENOTFOUND" || oe.Msg != `Schema node "/pv2-edit:c/b" not found in the target context.` {
		t.Fatalf("got %v", err)
	}
	if got := typedDump(dst); !reflect.DeepEqual(got, before) {
		t.Errorf("target changed:\n got  %q\n want %q", got, before)
	}
}

// TestDupMetaContexts: metadata copied into another context: stored with the target's
// annotation, dropped when the target's type rejects the value (libyang logs and goes on) or the
// target has no such annotation (libyang crashes, D-0067), copied within one context.
func TestDupMetaContexts(t *testing.T) {
	set1 := pjSchema()
	tr, diags, err := parseJSONString(set1, `{"pj:c": {"s": "a", "@s": {"pj:ann": "abc"}}}`, Reject, true)
	if err != nil {
		t.Fatal(err, diags)
	}
	s := findNode(t, tr, "/pj:c/s")
	same, other, none := pjSchema(), pjSchema(), pjSchema()
	other.Implemented("pj").Exts[0].Type = &schema.Type{Base: schema.Uint8} // ann: "abc" does not fit
	none.Implemented("pj").Exts = nil
	for _, c := range []struct {
		name string
		trg  *schema.Set
		want int
	}{{"own context", nil, 1}, {"same annotation", same, 1}, {"value rejected", other, 0}, {"no annotation (D-0067)", none, 0}} {
		d, err := dupTo(s, c.trg, nil, 0, false)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(d.meta) != c.want || c.want == 1 && (d.meta[0].value.Canonical() != "abc" || d.meta[0] == s.meta[0]) {
			t.Errorf("%s: %d metadata", c.name, len(d.meta))
		}
	}
}
