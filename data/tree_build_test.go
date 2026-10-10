// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// rcMsg is the return code and the messages of a *ValidationError, "" for nil.
func rcMsg(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	var ve *data.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("not a *ValidationError: %v", err)
	}
	s := ve.RC()
	for _, d := range ve.Diags {
		s += " | " + d.Msg
	}
	return s
}

// TestTreeBuild: NewTerm, NewInner, NewList, Insert and SetValue (design 07 §6.1.2) over the
// public API: names with and without a module, placement, the Default flag of non-presence
// containers, the key handling, the port's argument refusals and SetValue's results.
func TestTreeBuild(t *testing.T) {
	cx := fzContext(t)
	if _, err := cx.Load("fz2", "", nil); err != nil {
		t.Fatal(err)
	}
	s := cx.Schema()
	tr := data.NewTree(s)
	c, err := tr.NewInner(nil, "fz:c", data.NewOptions{})
	if err != nil || c.Flags()&data.FlagDefault == 0 {
		t.Fatalf("NewInner: %v %v", err, c)
	}
	sl, err := tr.NewTerm(c, "s", "x", data.NewOptions{})
	if err != nil || c.Flags()&data.FlagDefault != 0 || sl.Path() != "/fz:c/s" {
		t.Fatalf("NewTerm under c: %v, c default %v", err, c.Flags()&data.FlagDefault != 0)
	}
	l, err := tr.NewList(c, "l", []string{"k1", "extra"}, data.NewOptions{}) // extra values ignored
	if err != nil || l.Path() != "/fz:c/l[k='k1']" {
		t.Fatalf("NewList: %v %v", err, l)
	}
	if _, err := tr.NewTerm(nil, "fz2:t", "v", data.NewOptions{}); err != nil {
		t.Fatal(err)
	}
	var top []string
	for n := range tr.Top() {
		top = append(top, n.Path())
	}
	if strings.Join(top, " ") != "/fz:c /fz2:t" {
		t.Errorf("top level %v", top)
	}
	other := data.NewTree(s)
	o, _ := other.NewInner(nil, "fz:c", data.NewOptions{})
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"no module", func() error { _, err := tr.NewTerm(nil, "s", "x", data.NewOptions{}); return err }(),
			"LY_EINVAL | Invalid argument parent || module (_lyd_new_term())."},
		{"module not implemented", func() error { _, err := tr.NewInner(nil, "zz:c", data.NewOptions{}); return err }(),
			"LY_EINVAL | Invalid argument module (not implemented) (lyd_new_inner())."},
		{"parent of another tree", func() error { _, err := tr.NewTerm(o, "s", "x", data.NewOptions{}); return err }(),
			"LY_EINVAL | Invalid argument parent (not in the tree) (_lyd_new_term())."},
		{"not found", func() error { _, err := tr.NewTerm(c, "nope", "x", data.NewOptions{}); return err }(),
			`LY_ENOTFOUND | Term node "nope" not found.`},
		{"bad value", func() error { _, err := tr.NewTerm(c, "n", "x", data.NewOptions{}); return err }(),
			`LY_EVALID | Invalid type int32 value "x".`},
		{"nil keys", func() error { _, err := tr.NewList(c, "l", nil, data.NewOptions{}); return err }(),
			`LY_EINVAL | Missing list "l" keys.`},
		{"too few keys", func() error { _, err := tr.NewList(c, "l", []string{}, data.NewOptions{}); return err }(),
			"LY_EINVAL | Invalid argument keys (lyd_new_list())."},
		{"insert nil", tr.Insert(nil, nil), "LY_EINVAL | Invalid argument node (lyd_insert_sibling())."},
		{"insert under another tree's node", tr.Insert(o, sl), "LY_EINVAL | Invalid argument parent (not in the tree) (lyd_insert_child())."},
		{"insert from another snapshot", tr.Insert(nil, func() *data.Node {
			n, _ := data.NewTree(fzContext(t).Schema()).NewInner(nil, "fz:c", data.NewOptions{})
			return n
		}()), "LY_EINVAL | Invalid argument node (another schema snapshot) (lyd_insert_sibling())."},
		{"set value of a container", func() error { _, err := c.SetValue("x"); return err }(),
			"LY_EINVAL | Invalid argument term->schema->nodetype & (0x0004|0x0008) (lyd_change_term())."},
	} {
		if got := rcMsg(t, c.err); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
	// Insert moves a node from another tree
	ll, err := other.NewTerm(o, "ll", "7", data.NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Insert(c, ll); err != nil || ll.Path() != "/fz:c/ll[.='7']" || len(slices.Collect(o.Children())) != 0 {
		t.Fatalf("Insert: %v", err)
	}
	for _, c := range []struct {
		value   string
		changed bool
	}{{"y", true}, {"y", false}} {
		if changed, err := sl.SetValue(c.value); err != nil || changed != c.changed || sl.Value() != c.value {
			t.Errorf("SetValue %s: %v %v", c.value, changed, err)
		}
	}
}

// TestDupLinks: Dup and DupSiblings into another tree and under a parent, LinkLeafrefs and
// LeafrefLinks with and without yang.Options.LeafrefLinking (design 07 §6.1.3).
func TestDupLinks(t *testing.T) {
	in := `{"fz:c": {"s": "x", "l": [{"k": "a", "r": "x"}, {"k": "b"}]}, "fz2:t": "v"}`
	for _, linking := range []bool{false, true} {
		cx, _, err := yang.NewContext(yang.Options{NoYangLibrary: true, LeafrefLinking: linking}, fzModules)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range []string{"fz", "fz2"} {
			if _, err := cx.Load(m, "", nil); err != nil {
				t.Fatal(err)
			}
		}
		tr, _, err := data.Parse(context.Background(), strings.NewReader(in), data.FormatJSON, cx.Schema(),
			data.ParseOptions{ParseOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		diags, err := tr.LinkLeafrefs(context.Background())
		r, _ := tr.Find("/fz:c/l[k='a']/r")
		s, _ := tr.Find("/fz:c/s")
		lrefs, targets := s.LeafrefLinks()
		_, rt := r.LeafrefLinks()
		switch {
		case !linking && (rcMsg(t, err) != "LY_EDENIED" || lrefs != nil):
			t.Errorf("without linking: %q %v", rcMsg(t, err), lrefs)
		case linking && (err != nil || len(diags) != 0 || len(lrefs) != 1 || lrefs[0] != r || targets != nil ||
			len(rt) != 1 || rt[0] != s):
			t.Errorf("with linking: %v %v %v %v %v", err, diags, lrefs, targets, rt)
		}
		if _, err := data.NewTree(cx.Schema()).LinkLeafrefs(context.Background()); rcMsg(t, err) !=
			"LY_EINVAL | Invalid argument tree (lyd_leafref_link_node_tree())." {
			t.Errorf("empty tree: %s", rcMsg(t, err))
		}

		dst := data.NewTree(cx.Schema())
		c, _ := tr.Find("/fz:c")
		d, diags, err := dst.Dup(c, nil, data.DupOptions{Recursive: true})
		if err != nil || len(diags) != 0 || d.Path() != "/fz:c" || len(slices.Collect(d.Children())) != 3 {
			t.Fatalf("Dup: %v %v %v", err, diags, d)
		}
		if _, _, err := tr.Dup(c, d, data.DupOptions{}); rcMsg(t, err) != "LY_EINVAL | Invalid argument parent (not in the tree) (lyd_dup_single())." {
			t.Errorf("parent of another tree: %s", rcMsg(t, err))
		}
		sib := data.NewTree(cx.Schema())
		first, _, err := sib.DupSiblings(c, nil, data.DupOptions{}) // c and the siblings after it
		if top := slices.Collect(sib.Top()); err != nil || first.Path() != "/fz:c" || len(top) != 2 {
			t.Fatalf("DupSiblings: %v %d", err, len(top))
		}
	}
}

// TestDupMetaRejected: Dup into a snapshot whose annotation type rejects a metadata value
// (conformance dup-ctx/meta-incompatible): the call succeeds, returns the store's item and
// "Value duplication failed.", and the copy has no such metadata.
func TestDupMetaRejected(t *testing.T) {
	load := func(dir string) *yang.Schema {
		c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, os.DirFS("../conformance/corpus/dup-ctx/"+dir))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Load("dc", "", nil); err != nil {
			t.Fatal(err)
		}
		return c.Schema()
	}
	src, _, err := data.Parse(context.Background(), strings.NewReader(`{"dc:c": {"id": "dc:one", "@id": {"dc:tag": "dc:one"}}}`),
		data.FormatJSON, load("schemas-a"), data.ParseOptions{ParseOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := src.Find("/dc:c")
	dst := data.NewTree(load("schemas-c"))
	d, diags, err := dst.Dup(c, nil, data.DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range diags {
		got = append(got, x.Err+" "+x.Code+" "+x.Msg+" dp="+x.DataPath)
	}
	want := []string{`LY_EVALID LYVE_DATA Invalid type uint8 value "dc:one". dp=/dc:id`, "LY_EINT LYVE_SUCCESS Value duplication failed. dp="}
	if !slices.Equal(got, want) {
		t.Errorf("diagnostics:\n got %q\nwant %q", got, want)
	}
	id, _ := dst.Find("/dc:c/id")
	if d == nil || id == nil || len(slices.Collect(id.Meta())) != 0 {
		t.Errorf("copy %v, id %v with metadata", d, id)
	}
}

// TestInsertIntoItself: Insert refuses a destination inside the moved subtree (D-0112: libyang
// links the node under itself and later walks never end). Schema checks refuse it first for schema
// nodes; opaque nodes reach it: a node under itself, an ancestor under its descendant, and a whole
// top-level sibling list under one of its own nodes.
func TestInsertIntoItself(t *testing.T) {
	s := fzContext(t).Schema()
	tr, _, err := data.Parse(context.Background(), strings.NewReader(`{"fz:c": {"s": "x"}, "fz:zz": {"a": {"b": 1}}}`),
		data.FormatJSON, s, data.ParseOptions{ParseOnly: true, Unknown: data.Opaque})
	if err != nil {
		t.Fatal(err)
	}
	var zz *data.Node
	for n := range tr.Top() {
		if n.Name() == "zz" {
			zz = n
		}
	}
	a := slices.Collect(zz.Children())[0]
	b := slices.Collect(a.Children())[0]
	want := "LY_EINVAL | Invalid argument node (the destination is inside it) (lyd_insert_child())."
	first := slices.Collect(tr.Top())[0]
	for _, c := range []struct {
		name         string
		parent, node *data.Node
	}{{"itself", a, a}, {"ancestor under descendant", b, zz}, {"sibling list", b, first}} {
		if got := rcMsg(t, tr.Insert(c.parent, c.node)); got != want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, want)
		}
	}
	n := 0
	for top := range tr.Top() {
		for range top.All() { // the tree is intact: the walk ends
			n++
		}
	}
	if n != 5 {
		t.Errorf("tree changed: %d nodes", n)
	}
}
