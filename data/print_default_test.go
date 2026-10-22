// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// protocol-v2/print-detached-absolute-leafref covers the temporary printer
// tree, whose root has no sibling set during an absolute XPath lookup.
func TestPrintDetachedAbsoluteLeafrefDefault(t *testing.T) {
	set := round12Set(t, false, "pv2-absleaf-default")
	wd := &schema.Module{Name: wdModule, Owner: set, Namespace: "urn:ietf:params:xml:ns:netconf:with-defaults:1.0", Prefix: "wd", Implemented: true}
	set.Modules = append(set.Modules, wd)
	tr := round12Tree(t, set, `{"pv2-absleaf-default:c":{"t":"x","r":"x"}}`)
	c := tr.top.first()
	if err := c.Unlink(); err != nil {
		t.Fatal(err)
	}
	for _, wd := range []WD{WDTrim, WDAllTagged} {
		var b bytes.Buffer
		if err := c.PrintJSON(&b, PrintOptions{WithDefaults: wd}); err != nil {
			t.Fatalf("mode %d: %v", wd, err)
		}
		if wd == WDTrim && bytes.Contains(b.Bytes(), []byte(`"r"`)) {
			t.Fatalf("default leafref was not trimmed: %s", b.String())
		}
		if wd == WDAllTagged && !bytes.Contains(b.Bytes(), []byte(`ietf-netconf-with-defaults:default`)) {
			t.Fatalf("default leafref was not tagged: %s", b.String())
		}
	}
}

// dfltLeafrefTree is pdl (print/schemas/pdl.yang) with leaf-list t = vals and r = rv, built with
// LY_CTX_LEAFREF_LINKING and linked (LinkLeafrefs).
func dfltLeafrefTree(t *testing.T, rv string, vals ...string) (*Tree, *Node) {
	t.Helper()
	set := &schema.Set{LeafrefLinking: true}
	m := &schema.Module{Name: "pdl", Owner: set, Implemented: true}
	wd := &schema.Module{Name: wdModule, Owner: set, Namespace: "urn:ietf:params:xml:ns:netconf:with-defaults:1.0", Prefix: "wd", Implemented: true}
	set.Modules = []*schema.Module{m, wd}
	str := &schema.Type{Base: schema.String}
	c := &schema.Node{Kind: schema.Container, Name: "c", Module: m, Config: true}
	m.Top = []*schema.Node{c}
	tl := &schema.Node{Kind: schema.LeafList, Name: "t", Module: m, Parent: c, Type: str, Config: true}
	r := &schema.Node{Kind: schema.Leaf, Name: "r", Module: m, Parent: c, Config: true, Type: &schema.Type{
		Base: schema.Leafref, Path: "../t", Prefixes: schema.NSCtx{"": m}, RequireInstance: true, Realtype: str}}
	r.Default = []schema.DefaultValue{{Lex: "x", NS: schema.NSCtx{"": m}}}
	c.Children = []*schema.Node{tl, r}
	tr := newTree(set)
	cn := newInner(c)
	mustLink(t, tr.insert(nil, cn, insertDefault))
	term := func(sn *schema.Node, lex string) *Node {
		v, d := types.Store(sn.Type, lex, types.FormatJSON, types.JSONHints("string"), types.ModuleNames{Set: set}, sn)
		if d != nil {
			t.Fatal(d.Msg)
		}
		n := newTerm(sn, v)
		mustLink(t, tr.insert(cn, n, insertDefault))
		return n
	}
	for _, v := range vals {
		term(tl, v)
	}
	rn := term(r, rv)
	if _, err := tr.LinkLeafrefs(context.Background()); err != nil {
		t.Fatal(err)
	}
	return tr, rn
}

func linkNames(n *Node) (leafrefs, targets []string) {
	l, tg := n.LeafrefLinks()
	for _, x := range l {
		leafrefs = append(leafrefs, x.Path())
	}
	for _, x := range tg {
		targets = append(targets, x.Path()+"="+x.value.Canonical())
	}
	return leafrefs, targets
}

// TestPrintDefaultKeepsLinks: deciding whether r is a default validates the default "x" in the
// tree (lysc_value_cmp), which must not link r to the target x (libyang does under
// LY_CTX_LEAFREF_LINKING, leafref.c:94-107; D-0128): r = "y" stays linked to y only.
func TestPrintDefaultKeepsLinks(t *testing.T) {
	tr, r := dfltLeafrefTree(t, "y", "x", "y")
	_, before := linkNames(r)
	if !slices.Equal(before, []string{"/pdl:c/t[.='y']=y"}) {
		t.Fatalf("links before printing: %v", before)
	}
	for _, wd := range []WD{WDTrim, WDAllTagged} {
		var b bytes.Buffer
		if err := tr.PrintJSON(&b, PrintOptions{WithDefaults: wd}); err != nil {
			t.Fatal(err)
		}
		if _, after := linkNames(r); !slices.Equal(after, before) {
			t.Fatalf("mode %d: links after printing %v, before %v", wd, after, before)
		}
	}
}

// TestPrintDefaultConcurrent: printing is a read (data/doc.go), also when the default check
// evaluates leafref targets; run with -race.
func TestPrintDefaultConcurrent(t *testing.T) {
	tr, r := dfltLeafrefTree(t, "y", "x", "y")  // checking default x would link r to x
	ref, _ := dfltLeafrefTree(t, "y", "x", "y") // the expected output, printed outside the race
	var want bytes.Buffer
	if err := ref.PrintJSON(&want, PrintOptions{WithDefaults: WDTrim}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				var b bytes.Buffer
				if err := tr.PrintJSON(&b, PrintOptions{WithDefaults: WDTrim}); err != nil || !bytes.Equal(b.Bytes(), want.Bytes()) {
					t.Errorf("print: %v %q", err, b.String())
					return
				}
				_, _ = r.LeafrefLinks()
			}
		}()
	}
	wg.Wait()
}

// TestPrintDefaultBudget: an XPath budget overrun in the default check fails the print with
// yang.ErrBudget and writes nothing, instead of printing r as if it were not a default.
func TestPrintDefaultBudget(t *testing.T) {
	tr, _ := dfltLeafrefTree(t, "x", "x", "y")
	o := PrintOptions{WithDefaults: WDTrim,
		dflt: &defaultCheck{vc: &valCtx{t: tr, log: &logger{set: tr.set}, budget: xpathBudget{max: 1}}}}
	var b bytes.Buffer
	if err := printData(&b, tr.set, o, slices.Collect(tr.top.all()), 0, true, printJSON); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("err = %v, want yang.ErrBudget", err)
	}
	if b.Len() != 0 {
		t.Fatalf("wrote %q", b.String())
	}
	b.Reset()
	if err := tr.PrintJSON(&b, PrintOptions{WithDefaults: WDTrim}); err != nil || bytes.Contains(b.Bytes(), []byte(`"r"`)) {
		t.Fatalf("default budget: %v %q", err, b.String()) // r = "x" with target x is a default: trimmed
	}
}

// Detached default checks use the retained subtree as the value-validation root.
func TestPrintDetachedLeafrefDefault(t *testing.T) {
	for _, tc := range []struct {
		target  bool
		trimmed bool
	}{{true, true}, {false, false}} {
		var vals []string
		if tc.target {
			vals = []string{"x"}
		}
		tr, r := dfltLeafrefTree(t, "x", vals...)
		c := tr.top.first()
		if err := c.Unlink(); err != nil {
			t.Fatal(err)
		}
		for _, wd := range []WD{WDTrim, WDAllTagged} {
			var b bytes.Buffer
			if err := c.PrintJSON(&b, PrintOptions{WithDefaults: wd}); err != nil {
				t.Fatal(err)
			}
			if got := bytes.Contains(b.Bytes(), []byte(`"r"`)); wd == WDTrim && got == tc.trimmed {
				t.Fatalf("target=%v trim=%v: %s", tc.target, got, b.String())
			}
			if wd == WDAllTagged && tc.target && !bytes.Contains(b.Bytes(), []byte(`ietf-netconf-with-defaults:default`)) {
				t.Fatalf("missing default tag: %s", b.String())
			}
		}
		if refs, _ := linkNames(r); len(refs) != 0 {
			t.Fatalf("printing changed links: %v", refs)
		}
	}
}

// TestPrintDetachedLeafDefaultRootError: the default check of a detached non-top-level leafref
// logs lyxp_eval's LY_EINVAL root error before the plugin's LY_EVALID (lysc_value_cmp, xpath.c:9954).
func TestPrintDetachedLeafDefaultRootError(t *testing.T) {
	_, r := dfltLeafrefTree(t, "x", "x")
	if err := r.Unlink(); err != nil {
		t.Fatal(err)
	}
	o := PrintOptions{WithDefaults: WDTrim, dflt: &defaultCheck{}}
	var b bytes.Buffer
	if err := r.PrintJSON(&b, o); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range o.dflt.diags {
		got = append(got, d.Err)
	}
	if !slices.Equal(got, []string{"LY_EINVAL", "LY_EVALID"}) ||
		o.dflt.diags[0].Msg != `Data node "r" has no parent but is not instance of a top-level schema node.` {
		t.Fatalf("diagnostics: %+v", o.dflt.diags)
	}
}
