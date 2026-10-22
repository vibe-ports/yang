// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

// mixedParentSet is the pv2-edit and a modules of the mixed-parent oracle requests
// (conformance/testdata/mixed-parent).
func mixedParentSet(t *testing.T, extra ...string) *schema.Set {
	t.Helper()
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS(filepath.Join(pv2Dir, "schemas")))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range append([]string{"pv2-edit", "a"}, extra...) {
		if _, diags, err := c.Load(name, "", nil); err != nil {
			t.Fatal(err, diags)
		}
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	return set
}

// mixedParentTree applies the edits, then inserts /a:foo beside the parent selected by beside.
func mixedParentTree(t *testing.T, set *schema.Set, beside string, steps ...editStep) *Tree {
	t.Helper()
	tr := newTree(set)
	for _, step := range steps {
		if rc, diags := step.apply(tr); rc != "LY_SUCCESS" {
			t.Fatalf("edit %v: %s %v", step, rc, diags)
		}
	}
	if _, err := tr.NewPath("/a:foo", "42", NewPathOptions{Parent: findNode(t, tr, beside)}); err != nil {
		t.Fatal(err)
	}
	return tr
}

func childNames(n *Node) []string {
	var names []string
	for c := range n.Children() {
		names = append(names, c.Schema().Name()+"="+c.Value())
	}
	return names
}

// R5-1 (#267): trimming the mixed siblings removes their first-instance records, so a later
// insertion anchors on live nodes (seq/mixed-parent-trim-then-insert).
func TestMixedParentTrimThenInsert(t *testing.T) {
	tr := mixedParentTree(t, mixedParentSet(t), "/pv2-edit:c/l[k='x']",
		editStep{set: "/pv2-edit:c/a", value: "x"}, editStep{set: "/pv2-edit:c/b", value: "1"},
		editStep{set: "/pv2-edit:c/l[k='x']"})
	if _, err := tr.TrimXPath("/pv2-edit:c/a | /pv2-edit:c/b", XPathOptions{}); err != nil {
		t.Fatal(err)
	}
	c := findNode(t, tr, "/pv2-edit:c")
	if _, err := tr.NewPath("np/d", "7", NewPathOptions{Parent: c}); err != nil {
		t.Fatal(err)
	}
	if got := childNames(c); !slices.Equal(got, []string{"a=x", "b=1", "np="}) {
		t.Fatalf("children = %v", got)
	}
}

// R5-2 (#267): a recursive copy appends the children in their order (lyd_dup_r,
// LYD_INSERT_NODE_LAST), including the root beside the nested parent
// (seq/mixed-parent-recursive-dup-order).
func TestMixedParentRecursiveDupOrder(t *testing.T) {
	tr := mixedParentTree(t, mixedParentSet(t), "/pv2-edit:c/l[k='x']",
		editStep{set: "/pv2-edit:c/a", value: "first"}, editStep{set: "/pv2-edit:c/l[k='x']"})
	c := findNode(t, tr, "/pv2-edit:c")
	want := []string{"foo=42", "a=first", "l="}
	if got := childNames(c); !slices.Equal(got, want) {
		t.Fatalf("children = %v", got)
	}
	d, _, err := tr.Dup(c, nil, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := childNames(d); !slices.Equal(got, want) {
		t.Fatalf("copy children = %v, want %v", got, want)
	}
}

// R5-3 (#267): after a removal, a positional lookup starts at the first-instance record of
// the hash table (LYD_LIST_FOR_INST from lyd_find_sibling_schema), not at the physically first
// instance (seq/mixed-parent-removal-positional-lookup).
func TestMixedParentRemovalPositionalLookup(t *testing.T) {
	tr := mixedParentTree(t, mixedParentSet(t), "/pv2-edit:c/sll[.='old']",
		editStep{set: "/pv2-edit:c/sll", value: "old"})
	for _, step := range []editStep{
		{set: "/pv2-edit:c/sll", value: "x"},
		{set: "/pv2-edit:c/sll", value: "y"},
		{del: "/pv2-edit:c/sll[.='x']"},
	} {
		if rc, diags := step.apply(tr); rc != "LY_SUCCESS" {
			t.Fatalf("edit %v: %s %v", step, rc, diags)
		}
	}
	if n, err := tr.Find("/pv2-edit:c/sll[1]"); err != nil || n == nil || n.Value() != "old" {
		t.Fatalf("sll[1] = %v, %v; want the hash leader old", n, err)
	}
}

func TestNestedParentAbsoluteRootMixedSiblings(t *testing.T) {
	set := mixedParentSet(t)
	tr := newTree(set)
	for _, step := range []editStep{
		{set: "/pv2-edit:c/a", value: "first"},
		{set: "/pv2-edit:c/l[k='x']"},
	} {
		if rc, diags := step.apply(tr); rc != "LY_SUCCESS" {
			t.Fatalf("initial edit: %s %v", rc, diags)
		}
	}
	l := findNode(t, tr, "/pv2-edit:c/l[k='x']")
	if _, err := tr.NewPath("/a:foo", "42", NewPathOptions{Parent: l}); err != nil {
		t.Fatal(err)
	}
	cNode := findNode(t, tr, "/pv2-edit:c")
	a := findNode(t, tr, "/pv2-edit:c/a")
	if got, err := tr.Find("/pv2-edit:c/a"); err != nil || got != a {
		t.Fatalf("Find existing child: %v, %v", got, err)
	}
	if got, err := tr.Find("/pv2-edit:c/l[k='x']"); err != nil || got != nil {
		t.Fatalf("schema-parent mismatch must hide keyed list: %v, %v", got, err)
	}
	if got, err := tr.NewPath("a", "changed", NewPathOptions{Parent: cNode, Update: true}); err != nil || got != a {
		t.Fatalf("Update existing child: %v, %v", got, err)
	}
	if _, err := tr.NewPath("b", "7", NewPathOptions{Parent: cNode}); err != nil {
		t.Fatal(err)
	}
	if got := slices.Collect(cNode.Children()); len(got) != 4 ||
		got[0].Schema().Name() != "b" || got[1].Schema().Name() != "foo" || got[2] != a || got[3] != l {
		t.Fatalf("insertion anchors: %v", typedDump(tr))
	}
	src := newTree(set)
	for _, step := range []editStep{
		{set: "/pv2-edit:c/a", value: "merged"},
		{set: "/pv2-edit:c/l[k='x']/v", value: "4"},
	} {
		if rc, diags := step.apply(src); rc != "LY_SUCCESS" {
			t.Fatalf("source edit: %s %v", rc, diags)
		}
	}
	if err := tr.Merge(src); err != nil {
		t.Fatal(err)
	}
	if got := findNode(t, tr, "/pv2-edit:c/a"); got != a || got.Value() != "merged" {
		t.Fatalf("Merge duplicated child: %v", typedDump(tr))
	}
}

func parseMixedParent(t *testing.T, set *schema.Set, text string, unknown UnknownPolicy) *Tree {
	t.Helper()
	tr, ds, err := parseWith(context.Background(), strings.NewReader(text), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true, Unknown: unknown}}, parseJSON, nil)
	if err != nil {
		t.Fatalf("parse %s: %v (%v)", text, err, ds)
	}
	return tr
}

// #267 review: a relative path under an anydata parent searches its value tree
// (ly_path_eval_partial starts at lyd_child_any) and updates the payload leaf
// (seq/new-path-parent-any-payload-update).
func TestNewPathParentAnyPayload(t *testing.T) {
	tr := parseMixedParent(t, mixedParentSet(t), `{"a:any":{"a:foo":1}}`, 0)
	anyNode := findNode(t, tr, "/a:any")
	n, err := tr.NewPath("a:foo", "2", NewPathOptions{Parent: anyNode, Update: true})
	if err != nil || n == nil || n.Value() != "2" || n.Parent() != anyNode {
		t.Fatalf("payload update: %v, %v", n, err)
	}
	if got := slices.Collect(tr.Top()); len(got) != 1 {
		t.Fatalf("top level = %v; want only the anydata", got)
	}
}

// #267 review: under an opaque parent, a root beside its schema child orders the siblings by
// lyd_insert_get_next_anchor's list walk (seq/mixed-parent-opaque-parent-root-walk).
func TestMixedParentOpaqueParentRootWalk(t *testing.T) {
	tr := parseMixedParent(t, mixedParentSet(t), `{"pv2-edit:c":"x"}`, Opaque)
	c := tr.top.first()
	for _, s := range []struct{ path, value string }{{"a", "v"}, {"b", "1"}} {
		if _, err := tr.NewPath(s.path, s.value, NewPathOptions{Parent: c, Update: true}); err != nil {
			t.Fatal(err)
		}
	}
	a := c.kids.list[0]
	if _, err := tr.NewPath("/a:foo", "42", NewPathOptions{Parent: a, Update: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.NewPath("np/d", "7", NewPathOptions{Parent: c, Update: true}); err != nil {
		t.Fatal(err)
	}
	if got := childNames(c); !slices.Equal(got, []string{"np=", "foo=42", "a=v", "b=1"}) {
		t.Fatalf("children = %v", got)
	}
}

// #267 review: an existing node without Update is lyd_new_path_create's LY_EEXIST with
// "Path \"...\" already exists." at that node (seq/new-path-parent-relative-exists-no-update).
func TestNewPathParentExists(t *testing.T) {
	tr := parseMixedParent(t, mixedParentSet(t), `{"pv2-edit:c":{"a":"x"}}`, 0)
	n, err := tr.NewPath("a", "y", NewPathOptions{Parent: findNode(t, tr, "/pv2-edit:c")})
	var ve *ValidationError
	if n != nil || !errors.As(err, &ve) || ve.RC() != "LY_EEXIST" || len(ve.Diags) != 1 ||
		ve.Diags[0].Msg != `Path "a" already exists.` || ve.Diags[0].Err != "LY_EVALID" ||
		ve.Diags[0].DataPath != "/pv2-edit:c/a" {
		t.Fatalf("existing child: %v, %#v", n, err)
	}
	if v := findNode(t, tr, "/pv2-edit:c/a").Value(); v != "x" {
		t.Fatalf("value changed to %q", v)
	}
}

// #267 review round 3: a recursive copy appends a root beside a schema child with monotone
// ranks; linking it still marks the copied siblings mixed, so a later insertion takes
// lyd_insert_get_next_anchor's list walk (seq/mixed-parent-recursive-dup-then-edit).
func TestMixedParentRecursiveDupThenEdit(t *testing.T) {
	set := mixedParentSet(t, "n")
	tr := newTree(set)
	for _, step := range []editStep{{set: "/pv2-edit:c/a", value: "x"}, {set: "/pv2-edit:c/l[k='x']"}} {
		if rc, diags := step.apply(tr); rc != "LY_SUCCESS" {
			t.Fatalf("edit %v: %s %v", step, rc, diags)
		}
	}
	// n:root has rank 0 among its module's top-level nodes: the copy's ranks are 0,0,3.
	if _, err := tr.NewPath("/n:root", "r", NewPathOptions{Parent: findNode(t, tr, "/pv2-edit:c/l[k='x']")}); err != nil {
		t.Fatal(err)
	}
	cp := newTree(set)
	c, _, err := cp.Dup(findNode(t, tr, "/pv2-edit:c"), nil, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := childNames(c); !slices.Equal(got, []string{"root=r", "a=x", "l="}) {
		t.Fatalf("copied children = %v", got)
	}
	if _, err := cp.NewPath("b", "1", NewPathOptions{Parent: c}); err != nil {
		t.Fatal(err)
	}
	if got := childNames(c); !slices.Equal(got, []string{"b=1", "root=r", "a=x", "l="}) {
		t.Fatalf("children = %v", got)
	}
}

// #267 review round 4: beside a parent in an anydata value tree, the position check counts that
// parent's siblings (LYD_LIST_FOR_INST from the parent), and a position past them is the
// LOGVAL with LY_EINVAL (seq/new-path-parent-any-payload-positional,
// seq/new-path-parent-positional-einval).
func TestNewPathParentPositions(t *testing.T) {
	set := mixedParentSet(t)
	tr := parseMixedParent(t, set, `{"a:any":{"a:foo":1,"a:ll2":["one"]}}`, 0)
	anyNode := findNode(t, tr, "/a:any")
	one := anyNode.any.children.list[1]
	n, err := tr.NewPath("/a:ll2[2]", "two", NewPathOptions{Parent: one})
	if err != nil || n == nil || n.Parent() != anyNode || n.Value() != "two" {
		t.Fatalf("payload position 2: %v, %v", n, err)
	}
	var ve *ValidationError
	if _, err := tr.NewPath("/a:ll2[4]", "four", NewPathOptions{Parent: one}); !errors.As(err, &ve) ||
		ve.RC() != "LY_EINVAL" || ve.Diags[0].Msg != `Cannot create "ll2" on position 4, only 2 instances exist.` {
		t.Fatalf("payload position 4: %v", err)
	}
	tr = parseMixedParent(t, set, `{"pv2-edit:c":{"a":"x"}}`, 0)
	if _, err := tr.NewPath("st[2]", "", NewPathOptions{Parent: findNode(t, tr, "/pv2-edit:c")}); !errors.As(err, &ve) ||
		ve.RC() != "LY_EINVAL" || len(ve.Diags) != 1 || ve.Diags[0].Err != "LY_EVALID" ||
		ve.Diags[0].Msg != `Cannot create "st" on position 2, no instances exist.` {
		t.Fatalf("relative position 2: %v", err)
	}
}

// #267 review round 5: a relative lyd_find_path from an anydata first node starts in its value
// tree (ly_path_eval_partial: lyd_child_any(ctx_node)), so a set parent selector reaches the
// payload leaf (seq/new-path-parent-relative-selector-any-payload).
func TestFindRelativeAnyPayload(t *testing.T) {
	tr := parseMixedParent(t, mixedParentSet(t), `{"a:any":{"a:foo":1}}`, 0)
	foo, err := tr.Find("a:foo")
	if err != nil || foo == nil || foo.Value() != "1" || foo.Parent() != findNode(t, tr, "/a:any") {
		t.Fatalf("relative payload lookup: %v, %v", foo, err)
	}
	if n, err := tr.NewPath("/a:any/a:foo", "2", NewPathOptions{Parent: foo, Update: true}); err != nil || n != foo || foo.Value() != "2" {
		t.Fatalf("update through the payload parent: %v, %v", n, err)
	}
}
