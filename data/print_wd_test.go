// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// wdFixture is module wd (testdata/wd.yang-like: leaves and leaf-lists with defaults, NP and
// presence containers, a list with a defaulted leaf, config false data) plus the two modules the
// default attribute needs.
type wdFixture struct {
	set   *schema.Set
	nodes map[string]*schema.Node
}

func newWDFixture() *wdFixture {
	f := &wdFixture{set: &schema.Set{}, nodes: map[string]*schema.Node{}}
	wd := &schema.Module{Name: "wd", Namespace: "urn:vibe-ports:wd", Prefix: "wd", Implemented: true}
	nc := &schema.Module{Name: wdModule, Namespace: "urn:ietf:params:xml:ns:netconf:with-defaults:1.0", Prefix: "ncwd", Implemented: true}
	// libyang's internal module with the attribute definition
	df := &schema.Module{Name: "default", Namespace: "urn:ietf:params:xml:ns:netconf:default:1.0", Prefix: "dflt", Implemented: true}
	f.set.Modules = []*schema.Module{wd, nc, df}
	str, u8 := &schema.Type{Base: schema.String}, &schema.Type{Base: schema.Uint8}
	ns := schema.NSCtx{"": wd}
	add := func(p *schema.Node, k schema.Kind, name string, t *schema.Type, dflt ...string) *schema.Node {
		n := &schema.Node{Kind: k, Name: name, Module: wd, Parent: p, Type: t, Config: true}
		if p != nil && !p.Config {
			n.Config = false
		}
		for _, d := range dflt {
			n.Default = append(n.Default, schema.DefaultValue{Lex: d, NS: ns})
		}
		if p == nil {
			wd.Top = append(wd.Top, n)
		} else {
			p.Children = append(p.Children, n)
		}
		f.nodes[name] = n
		return n
	}
	c := add(nil, schema.Container, "c", nil)
	add(c, schema.Leaf, "a", str, "da")
	add(c, schema.Leaf, "b", u8, "5")
	add(c, schema.LeafList, "ll", str, "x", "y")
	add(c, schema.LeafList, "ll2", u8)
	add(c, schema.Leaf, "none", str)
	add(add(c, schema.Container, "np", nil), schema.Leaf, "d", str, "dd")
	np2 := add(c, schema.Container, "np2", nil)
	add(np2, schema.Leaf, "e", str, "ee")
	add(np2, schema.Leaf, "f", str)
	pres := add(c, schema.Container, "pres", nil)
	pres.Presence = true
	add(pres, schema.Leaf, "g", str, "gg")
	l := add(c, schema.List, "l", nil)
	l.Keys = []*schema.Node{add(l, schema.Leaf, "k", str)}
	add(l, schema.Leaf, "v", str, "vv")
	st := add(c, schema.Leaf, "st", str, "sd")
	st.Config = false
	stc := add(c, schema.Container, "stc", nil)
	stc.Config = false
	add(stc, schema.Leaf, "s2", str, "s2d").Config = false
	return f
}

// build is the tree libyang had for testdata/wd.xml (the nodes with the default flag are the
// implicit ones validation added), without the state data.
func (f *wdFixture) build(t *testing.T, state bool) *Tree {
	t.Helper()
	tr := newTree(f.set)
	ins := func(parent, n *Node, flags Flags) *Node {
		n.flags = flags
		tr.insert(parent, n, insertDefault)
		return n
	}
	term := func(name, lex string) *Node {
		s := f.nodes[name]
		v, d := types.Store(s.Type, lex, types.FormatJSON, types.JSONHints("string")|types.HintStringDatatypes, nil, s)
		if d != nil {
			t.Fatal(d.Msg)
		}
		return newTerm(s, v)
	}
	c := ins(nil, newInner(f.nodes["c"]), 0)
	ins(c, term("a", "da"), 0)
	ins(c, term("b", "5"), FlagDefault)
	for _, v := range []string{"x", "z"} {
		ins(c, term("ll", v), 0)
	}
	ins(c, term("ll2", "1"), 0)
	np := ins(c, newInner(f.nodes["np"]), FlagDefault)
	ins(np, term("d", "dd"), FlagDefault)
	np2 := ins(c, newInner(f.nodes["np2"]), 0)
	ins(np2, term("e", "ee"), FlagDefault)
	ins(np2, term("f", "ff"), 0)
	pres := ins(c, newInner(f.nodes["pres"]), 0)
	ins(pres, term("g", "gg"), FlagDefault)
	for _, k := range []struct{ k, v string }{{"one", ""}, {"two", "vv"}} {
		l := newInner(f.nodes["l"])
		ins(l, term("k", k.k), 0)
		if k.v == "" {
			ins(l, term("v", "vv"), FlagDefault)
		} else {
			ins(l, term("v", k.v), 0)
		}
		ins(c, l, 0)
	}
	if state {
		ins(c, term("st", "sd"), FlagDefault)
		stc := ins(c, newInner(f.nodes["stc"]), FlagDefault)
		ins(stc, term("s2", "s2d"), FlagDefault)
	}
	return tr
}

// TestPrintWithDefaults: every with-defaults mode against what libyang v5.8.6 yanglint printed
// (-d trim|all|all-tagged|implicit-tagged, none for explicit) for the same data; the files are
// its output.
func TestPrintWithDefaults(t *testing.T) {
	tr := newWDFixture().build(t, false)
	for name, wd := range map[string]WD{"explicit": WDExplicit, "trim": WDTrim, "all": WDAll,
		"all-tagged": WDAllTagged, "implicit-tagged": WDImplicitTagged} {
		for ext, print := range map[string]func(*bytes.Buffer) error{
			"json": func(b *bytes.Buffer) error { return tr.PrintJSON(b, PrintOptions{WithDefaults: wd}) },
			"xml":  func(b *bytes.Buffer) error { return tr.PrintXML(b, PrintOptions{WithDefaults: wd}) },
		} {
			want, err := os.ReadFile(filepath.Join("testdata", "wd-"+name+"."+ext)) //nolint:gosec // test data
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			if err := print(&got); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), want) {
				t.Errorf("%s %s:\n%s\nwant:\n%s", name, ext, got.String(), want)
			}
		}
	}
}

// TestPrintWithDefaultsState: explicit mode keeps the defaults of config false nodes (and an
// implicit container only if a node below prints), the other modes drop an implicit container
// without a printed descendant; derived from lyd_node_should_print (the oracle never creates
// state defaults for config data).
func TestPrintWithDefaultsState(t *testing.T) {
	f := newWDFixture()
	tr := f.build(t, true)
	c := tr.top.list[0]
	st := findKid(c, "st")
	stc := findKid(c, "stc")
	for _, tc := range []struct {
		wd          WD
		st, stc, np bool
	}{
		{WDExplicit, true, true, false}, {WDTrim, false, false, false}, {WDAll, true, true, true},
		{WDAllTagged, true, true, true}, {WDImplicitTagged, true, true, true},
	} {
		o := PrintOptions{WithDefaults: tc.wd}
		if got := o.shouldPrint(st); got != tc.st {
			t.Errorf("mode %d: st prints %v", tc.wd, got)
		}
		if got := o.shouldPrint(stc); got != tc.stc {
			t.Errorf("mode %d: stc prints %v", tc.wd, got)
		}
		if got := o.shouldPrint(findKid(c, "np")); got != tc.np {
			t.Errorf("mode %d: np prints %v", tc.wd, got)
		}
	}
}

func findKid(n *Node, name string) *Node {
	for c := range n.Children() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}
