// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// linksTree is the oracle fixture data protocol-v2/data/links.json over a hand-built copy of
// pv2-links (module lk here), validated (the types queue) with LY_CTX_LEAFREF_LINKING.
func linksTree(t *testing.T, linking bool) (*Tree, *Node) {
	t.Helper()
	set := &schema.Set{LeafrefLinking: linking}
	m := &schema.Module{Name: "lk", Implemented: true}
	set.Modules = []*schema.Module{m}
	str, u8 := &schema.Type{Base: schema.String}, &schema.Type{Base: schema.Uint8}
	add := func(p *schema.Node, k schema.Kind, name string, ty *schema.Type) *schema.Node {
		n := &schema.Node{Kind: k, Name: name, Module: m, Parent: p, Type: ty, Config: true}
		if p == nil {
			m.Top = append(m.Top, n)
		} else {
			p.Children = append(p.Children, n)
		}
		return n
	}
	lref := func(path string, ri bool) *schema.Type {
		return &schema.Type{Base: schema.Leafref, Path: path, Prefixes: schema.NSCtx{"": m}, RequireInstance: ri, Realtype: str}
	}
	c := add(nil, schema.Container, "c", nil)
	l := add(c, schema.List, "l", nil)
	k := add(l, schema.Leaf, "k", str)
	l.Keys = []*schema.Node{k}
	tl := add(c, schema.LeafList, "t", str)
	r := add(c, schema.Leaf, "r", lref("../l/k", true))
	rl := add(c, schema.LeafList, "rl", lref("../t", true))
	nr := add(c, schema.Leaf, "nr", lref("../l/k", false))
	nx := add(c, schema.Leaf, "nx", lref("../l/k", false))
	u := add(c, schema.Leaf, "u", &schema.Type{Base: schema.Union, Union: []*schema.Type{u8, lref("../l/k", true)}})
	un := add(c, schema.Leaf, "un", &schema.Type{Base: schema.Union, Union: []*schema.Type{u8, lref("../l/k", true)}})
	v := add(c, schema.Leaf, "v", lref("../r", true))

	tr := newTree(set)
	vc := &valCtx{t: tr, log: &logger{set: set}, nodeWhen: &nodeSet{}, nodeTypes: &nodeSet{}}
	cn := newInner(c)
	tr.insert(nil, cn, insertDefault)
	term := func(parent *Node, sn *schema.Node, lex, hint string) {
		val, d := types.Store(sn.Type, lex, types.FormatJSON, types.JSONHints(hint), types.ModuleNames{Set: set}, sn)
		if d != nil {
			t.Fatal(d.Msg)
		}
		n := newTerm(sn, val)
		tr.insert(parent, n, insertDefault)
		if val.NeedsTree() {
			vc.nodeTypes.add(n)
		}
	}
	for _, key := range []string{"a", "b", "5"} {
		li := newInner(l)
		term(li, k, key, "string")
		tr.insert(cn, li, insertDefault)
	}
	term(cn, tl, "x", "string")
	term(cn, tl, "y", "string")
	term(cn, r, "a", "string")
	term(cn, rl, "y", "string")
	term(cn, rl, "x", "string")
	term(cn, nr, "a", "string")
	term(cn, nx, "zz", "string")
	term(cn, u, "b", "string")
	term(cn, un, "5", "number")
	term(cn, v, "a", "string")
	if err := vc.unres(); err != nil {
		t.Fatal(err, vc.log.diags)
	}
	return tr, cn
}

// dumpLinks is the oracle's links step: the record of every term node, in DFS order.
func dumpLinks(t *testing.T, tr *Tree) []string {
	t.Helper()
	paths := func(ns []*Node) []string {
		out := []string{}
		for _, n := range ns {
			out = append(out, lydPath(tr.set, n, false))
		}
		return out
	}
	var out []string
	for top := range tr.top.all() {
		for n := range top.All() {
			if !n.isTerm() {
				continue
			}
			rec, err := linksOf(tr.set, n)
			if err != nil {
				continue
			}
			out = append(out, fmt.Sprintf("%s L%v T%v", lydPath(tr.set, n, false), paths(rec.leafrefs), paths(rec.targets)))
		}
	}
	return out
}

func find(t *testing.T, tr *Tree, path string) *Node {
	t.Helper()
	for top := range tr.top.all() {
		for n := range top.All() {
			if lydPath(tr.set, n, false) == path {
				return n
			}
		}
	}
	t.Fatalf("no node %s", path)
	return nil
}

// TestLeafrefLinks replays the oracle fixtures protocol-v2/links*: validation links resolved
// require-instance leafrefs (union members too), lyd_leafref_link_node_tree adds the others and
// is idempotent, freeing a target or a leafref cleans both directions, and the flag is required.
func TestLeafrefLinks(t *testing.T) {
	validated := []string{
		"/lk:c/l[k='a']/k L[/lk:c/r] T[]",
		"/lk:c/l[k='b']/k L[/lk:c/u] T[]",
		"/lk:c/t[.='x'] L[/lk:c/rl[.='x']] T[]",
		"/lk:c/t[.='y'] L[/lk:c/rl[.='y']] T[]",
		"/lk:c/r L[/lk:c/v] T[/lk:c/l[k='a']/k]",
		"/lk:c/rl[.='x'] L[] T[/lk:c/t[.='x']]",
		"/lk:c/rl[.='y'] L[] T[/lk:c/t[.='y']]",
		"/lk:c/u L[] T[/lk:c/l[k='b']/k]",
		"/lk:c/v L[] T[/lk:c/r]",
	}
	linked := []string{
		"/lk:c/l[k='a']/k L[/lk:c/r /lk:c/nr] T[]",
		"/lk:c/l[k='b']/k L[/lk:c/u] T[]",
		"/lk:c/t[.='x'] L[/lk:c/rl[.='x']] T[]",
		"/lk:c/t[.='y'] L[/lk:c/rl[.='y']] T[]",
		"/lk:c/r L[/lk:c/v] T[/lk:c/l[k='a']/k]",
		"/lk:c/rl[.='x'] L[] T[/lk:c/t[.='x']]",
		"/lk:c/rl[.='y'] L[] T[/lk:c/t[.='y']]",
		"/lk:c/nr L[] T[/lk:c/l[k='a']/k]",
		"/lk:c/u L[] T[/lk:c/l[k='b']/k]",
		"/lk:c/v L[] T[/lk:c/r]",
	}
	tr, _ := linksTree(t, true)
	if got := dumpLinks(t, tr); !reflect.DeepEqual(got, validated) {
		t.Fatalf("after validation:\n%v", got)
	}
	for i := 0; i < 2; i++ {
		if err := tr.linkLeafrefs(context.Background(), &logger{set: tr.set}); err != nil {
			t.Fatal(err)
		}
		if got := dumpLinks(t, tr); !reflect.DeepEqual(got, linked) {
			t.Fatalf("link %d:\n%v", i, got)
		}
	}

	// links-free-target: lyd_free_tree of l[k='a']
	tr, _ = linksTree(t, true)
	_ = tr.linkLeafrefs(context.Background(), &logger{set: tr.set})
	if err := freeTree(find(t, tr, "/lk:c/l[k='a']")); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/lk:c/l[k='b']/k L[/lk:c/u] T[]",
		"/lk:c/t[.='x'] L[/lk:c/rl[.='x']] T[]",
		"/lk:c/t[.='y'] L[/lk:c/rl[.='y']] T[]",
		"/lk:c/r L[/lk:c/v] T[]",
		"/lk:c/rl[.='x'] L[] T[/lk:c/t[.='x']]",
		"/lk:c/rl[.='y'] L[] T[/lk:c/t[.='y']]",
		"/lk:c/u L[] T[/lk:c/l[k='b']/k]",
		"/lk:c/v L[] T[/lk:c/r]",
	}
	if got := dumpLinks(t, tr); !reflect.DeepEqual(got, want) {
		t.Fatalf("free target:\n%v", got)
	}

	// links-free-leafref: lyd_free_tree of r, a leafref that is also v's target
	tr, _ = linksTree(t, true)
	_ = tr.linkLeafrefs(context.Background(), &logger{set: tr.set})
	if err := freeTree(find(t, tr, "/lk:c/r")); err != nil {
		t.Fatal(err)
	}
	want = []string{
		"/lk:c/l[k='a']/k L[/lk:c/nr] T[]",
		"/lk:c/l[k='b']/k L[/lk:c/u] T[]",
		"/lk:c/t[.='x'] L[/lk:c/rl[.='x']] T[]",
		"/lk:c/t[.='y'] L[/lk:c/rl[.='y']] T[]",
		"/lk:c/rl[.='x'] L[] T[/lk:c/t[.='x']]",
		"/lk:c/rl[.='y'] L[] T[/lk:c/t[.='y']]",
		"/lk:c/nr L[] T[/lk:c/l[k='a']/k]",
		"/lk:c/u L[] T[/lk:c/l[k='b']/k]",
	}
	if got := dumpLinks(t, tr); !reflect.DeepEqual(got, want) {
		t.Fatalf("free leafref:\n%v", got)
	}

	// links-change-value: lyd_new_path(UPDATE) of u to "a" (lyd_change_term_val) drops u's links
	tr, _ = linksTree(t, true)
	_ = tr.linkLeafrefs(context.Background(), &logger{set: tr.set})
	if _, err := tr.NewPath("/lk:c/u", "a", NewPathOptions{Update: true}); err != nil {
		t.Fatal(err)
	}
	want = []string{
		"/lk:c/l[k='a']/k L[/lk:c/r /lk:c/nr] T[]",
		"/lk:c/t[.='x'] L[/lk:c/rl[.='x']] T[]",
		"/lk:c/t[.='y'] L[/lk:c/rl[.='y']] T[]",
		"/lk:c/r L[/lk:c/v] T[/lk:c/l[k='a']/k]",
		"/lk:c/rl[.='x'] L[] T[/lk:c/t[.='x']]",
		"/lk:c/rl[.='y'] L[] T[/lk:c/t[.='y']]",
		"/lk:c/nr L[] T[/lk:c/l[k='a']/k]",
		"/lk:c/v L[] T[/lk:c/r]",
	}
	if got := dumpLinks(t, tr); !reflect.DeepEqual(got, want) {
		t.Fatalf("change value:\n%v", got)
	}

	// links-denied: no flag, no records, LY_EDENIED
	tr, _ = linksTree(t, false)
	if err := tr.linkLeafrefs(context.Background(), &logger{set: tr.set}); !errors.Is(err, errLinksDenied) || len(dumpLinks(t, tr)) != 0 {
		t.Fatalf("denied: %v %v", err, dumpLinks(t, tr))
	}
}

// TestFreeLinksOrder: LY_ARRAY_REMOVE_VALUE moves the last item into the removed slot.
func TestFreeLinksOrder(t *testing.T) {
	a, b, c, target := &Node{}, &Node{}, &Node{}, &Node{}
	for _, l := range []*Node{a, b, c} {
		linkLeafrefNode(target, l)
	}
	freeLinks(a)
	if target.links == nil || !reflect.DeepEqual(target.links.leafrefs, []*Node{c, b}) || a.links != nil {
		t.Fatal("swap removal")
	}
	freeLinks(b)
	freeLinks(c)
	if target.links != nil {
		t.Fatal("an emptied record must go")
	}
}
