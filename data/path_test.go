// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"reflect"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/schema"
)

// TestLydPath: lyd_path(LYD_PATH_STD) predicates and module prefixes (TD:2974, design 07 §1.10).
func TestLydPath(t *testing.T) {
	f := newFixture()
	aug := &schema.Node{Kind: schema.Leaf, Name: "ax", Module: f.a, Parent: f.c, Type: f.str, Config: true}
	f.c.Children = append(f.c.Children, aug)
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	l := f.list(t, tr, c, "eth0", "x")
	q := f.list(t, tr, c, "a'b", "")
	ll := f.term(t, f.ll, "3")
	tr.insert(c, ll, insertDefault)
	var sl []*Node
	for _, v := range []string{"1", "1"} {
		n := f.term(t, f.sl, v)
		tr.insert(c, n, insertDefault)
		sl = append(sl, n)
	}
	k2 := newInner(f.kl)
	tr.insert(c, newInner(f.kl), insertDefault)
	tr.insert(c, k2, insertDefault)
	kv := f.term(t, f.klv, "v")
	tr.insert(k2, kv, insertDefault)
	ax := f.term(t, aug, "a")
	tr.insert(c, ax, insertDefault)
	opJSON := newOpaque(opaque{Name: "o", ModuleNS: "a"})
	tr.insert(nil, opJSON, insertDefault)
	opUnknown := newOpaque(opaque{Name: "u", ModuleNS: "zz"})
	tr.insert(c, opUnknown, insertDefault)
	pending := newInner(f.l) // keys incomplete: not linked, its path starts at the list
	pv := f.term(t, f.lv, "v")
	tr.insert(pending, pv, insertDefault)
	for _, tc := range []struct {
		n    *Node
		want string
	}{
		{c, "/b:c"},
		{l.kids.list[1], "/b:c/l[k='eth0']/v"},
		{q, `/b:c/l[k="a'b"]`},
		{ll, "/b:c/ll[.='3']"},
		{sl[1], "/b:c/sl[2]"},
		{kv, "/b:c/kl[2]/v"},
		{ax, "/b:c/a:ax"},
		{opJSON, "/a:o"},
		{opUnknown, "/b:c/u"},
		{pv, "/b:l/v"},
	} {
		if got := lydPath(f.set, tc.n, false); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.want, got, tc.want)
		}
	}
	if got := lydPath(f.set, ll, true); got != "/b:c/ll" {
		t.Errorf("no last predicate: %q", got)
	}
}

// TestLocation: ly_vlog_build_path_line — a node's path, extended by the schema node being
// stored under it; the schema path without a node; the location path appended; the input line.
func TestLocation(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	line := 7
	l := &logger{set: f.set}
	l.pushInput(func() int { return line })
	l.locSet(f.z)
	_ = l.val(c, "", ly.Data, "bad %s", "z")
	l.locBack(1)
	_ = l.item(c, f.first, false, "LY_EVALID", ly.Data, "tag", "other module, not a child")
	_ = l.item(nil, f.lv, false, "LY_EVALID", ly.Data, "", "schema only")
	l.locSet(f.lk)
	l.pushPath("/extra")
	_ = l.val(nil, "", ly.Reference, "stack")
	_ = l.val(c, "", ly.Data, "data path and location path")
	l.popPath()
	l.locBack(1)
	aug := &schema.Node{Kind: schema.Leaf, Name: "ax", Module: f.a, Parent: f.c, Type: f.str}
	_ = l.item(c, aug, false, "LY_EVALID", ly.Data, "", "cross-module schema node")
	l.popInput()
	l.warn("w %d", 1)
	want := []yang.Diagnostic{
		{Err: "LY_EVALID", Code: "LYVE_DATA", DataPath: "/b:c/z", Line: 7, Msg: "bad z"},
		{Err: "LY_EVALID", Code: "LYVE_DATA", DataPath: "/b:c", AppTag: "tag", Line: 7, Msg: "other module, not a child"},
		{Err: "LY_EVALID", Code: "LYVE_DATA", SchemaPath: "/b:c/l/v", Line: 7, Msg: "schema only"},
		{Err: "LY_EVALID", Code: "LYVE_REFERENCE", SchemaPath: "/b:c/l/k/extra", Line: 7, Msg: "stack"},
		{Err: "LY_EVALID", Code: "LYVE_DATA", DataPath: "/b:c/extra", Line: 7, Msg: "data path and location path"},
		{Err: "LY_EVALID", Code: "LYVE_DATA", DataPath: "/b:c/a:ax", Line: 7, Msg: "cross-module schema node"},
		{Warning: true, Err: "LY_SUCCESS", Code: "LYVE_SUCCESS", Msg: "w 1"},
	}
	if !reflect.DeepEqual(l.diags, want) {
		t.Fatalf("got  %+v\nwant %+v", l.diags, want)
	}
	var ve *ValidationError
	if err := l.result(); !errors.As(err, &ve) || len(ve.Diags) != 7 || ve.Error() != "bad z" {
		t.Fatalf("result: %v", err)
	}
	w := &logger{set: f.set}
	w.warn("only a warning")
	if w.result() != nil {
		t.Fatal("warnings only must not fail")
	}
	// a key refusal of the tree operations is a LOGERR without location
	key := f.list(t, tr, c, "k", "").kids.list[0]
	e := &logger{set: f.set}
	var oe *opError
	if !errors.As(freeTree(key), &oe) {
		t.Fatal("key freed")
	}
	_ = e.logErr(oe.Err, "%s", oe.Msg)
	if d := e.diags[0]; d.Err != "LY_EINVAL" || d.Code != "LYVE_SUCCESS" || d.DataPath != "" ||
		d.Msg != `Cannot free a list key "k", free the list instance instead.` {
		t.Fatalf("%+v", d)
	}
}
