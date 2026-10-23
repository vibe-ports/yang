// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

func anyFixture() (*Tree, *schema.Node, *schema.Node) {
	m := &schema.Module{Name: "anycore", Namespace: "urn:anycore", Implemented: true}
	a := &schema.Node{Name: "a", Kind: schema.AnyData, Module: m}
	x := &schema.Node{Name: "x", Kind: schema.AnyXML, Module: m}
	m.Top = []*schema.Node{a, x}
	return newTree(&schema.Set{Modules: []*schema.Module{m}}), a, x
}

func anyText(t *testing.T, tr *Tree, sn *schema.Node, text string, hints types.Hints) *Node {
	t.Helper()
	n, _, err := newAnyWork(context.Background(), Budget{}).createAny(tr, sn, nil, &text, hints, true, false)
	if err != nil {
		t.Fatal(err)
	}
	mustLink(t, tr.insert(nil, n, insertDefault))
	return n
}

// Constructor inputs imported from libyang v5.8.6 tests/utests/data/test_new.c:184-186,
// 414-515 (BSD-3-Clause). Assertions inspect the payload before printer integration.
func TestAnyCreate(t *testing.T) {
	for _, tc := range []struct {
		value string
		count int
	}{
		{`{"node":"val"}`, 1}, {`<node>val</node>`, 1},
		{`<elem>val</elem>`, 1}, {`<a/><b/><c/>`, 3},
		{`{"a":[null],"b":[null],"c":[null]}`, 3},
	} {
		t.Run(tc.value, func(t *testing.T) {
			tr, a, _ := anyFixture()
			n, _, err := newAnyWork(context.Background(), Budget{}).createAny(tr, a, nil, &tc.value, 0, false, true)
			if err != nil {
				t.Fatal(err)
			}
			if n.any.text != nil || n.any.children.len() != tc.count {
				t.Fatalf("payload = %#v", n.any)
			}
			for c := range n.ChildrenAny() {
				if c.Parent() != n {
					t.Fatal("payload parent")
				}
			}
			if err := n.any.children.first().Remove(); err != nil {
				t.Fatal(err)
			}
			if n.any.children.len() != tc.count-1 {
				t.Fatal("removal from parsed payload")
			}
		})
	}
	tr, a, x := anyFixture()
	text := "not a tree"
	w := newAnyWork(context.Background(), Budget{})
	if _, _, err := w.createAny(tr, a, nil, &text, 0, false, true); err == nil {
		t.Fatal("anydata accepted text")
	}
	n, _, err := w.createAny(tr, x, nil, &text, 0, false, true)
	if err != nil || n.any.text == nil || *n.any.text != text {
		t.Fatalf("anyxml fallback: %v", err)
	}
	markup := "<a/><b/><c/>"
	n, _, err = w.createAny(tr, x, nil, &markup, types.HintString, false, true)
	if err != nil || n.any.text == nil {
		t.Fatalf("hint suppression: %v", err)
	}
	if err := n.copyAnyValue(w, nil, &markup, 0); err != nil {
		t.Fatal(err)
	}
	if n.any.text != nil || n.any.children.len() != 3 {
		t.Fatal("copy did not reparse text")
	}
}

func TestAnyEmptyAndCompare(t *testing.T) {
	tr, _, x := anyFixture()
	n := anyText(t, tr, x, "55", types.HintDecNum)
	m := anyText(t, tr, x, "55", types.HintString)
	if !n.Equal(m, CompareOptions{}) {
		t.Fatal("hints affect equality")
	}
	w := newAnyWork(context.Background(), Budget{})
	if err := n.copyAnyValue(w, nil, nil, hintContainer); err != nil {
		t.Fatal(err)
	}
	if n.any.hints != types.HintDecNum || n.any.text != nil {
		t.Fatal("empty copy changed hints")
	}
	// D-0129: both operand orders, absent versus text (including explicit empty text)
	// and absent versus tree; never run the crashing C comparison.
	for _, text := range []string{"55", "", "<node/>"} {
		if err := m.copyAnyValue(w, nil, &text, 0); err != nil {
			t.Fatal(err)
		}
		if n.Equal(m, CompareOptions{}) || m.Equal(n, CompareOptions{}) {
			t.Fatalf("empty equals %q", text)
		}
	}
	if err := m.copyAnyValue(w, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if !n.Equal(m, CompareOptions{}) {
		t.Fatal("empty hints affect equality")
	}
}

func TestAnyDupOwnership(t *testing.T) {
	tr, _, x := anyFixture()
	n := anyText(t, tr, x, `<node xmlns:p="urn:p" p:a="p:v">p:v<child/></node>`, types.HintString)
	// Use well-formed element-only content, retaining namespace state in attributes.
	text := `<node xmlns:p="urn:p" p:a="p:v"><child>p:v</child></node>`
	if err := n.copyAnyValue(newAnyWork(context.Background(), Budget{}), nil, &text, 0); err != nil {
		t.Fatal(err)
	}
	c := n.any.children.opq[0]
	c.flags = FlagWhenTrue
	n.flags = FlagWhenTrue | FlagDefault
	n.meta = []*meta{{mod: x.Module, name: "outer"}}
	d, _, err := tr.Dup(n, nil, DupOptions{NoMeta: true, WithFlags: true})
	if err != nil {
		t.Fatal(err)
	}
	dc := d.any.children.opq[0]
	if d.flags != FlagWhenTrue|FlagDefault || len(d.meta) != 0 || dc.flags != FlagNew || len(dc.opaq.Attrs) != 1 {
		t.Fatal("payload inherited outer duplication options")
	}
	if dc == c || dc.parent != d || dc.kids.opq[0] == c.kids.opq[0] || d.any.children.ht != nil {
		t.Fatal("payload copy ownership")
	}
	dc.opaq.Attrs[0].Value = "changed"
	dc.opaq.Attrs[0].Prefixes.(types.XMLNamespaces).NS["p"] = "urn:changed"
	dc.kids.opq[0].opaq.Prefixes.(types.XMLNamespaces).NS["p"] = "urn:changed"
	if c.opaq.Attrs[0].Value != "p:v" || c.opaq.Attrs[0].Prefixes.(types.XMLNamespaces).NS["p"] != "urn:p" || c.kids.opq[0].opaq.Prefixes.(types.XMLNamespaces).NS["p"] != "urn:p" {
		t.Fatal("mutable namespace/attributes shared")
	}
	if len(slices.Collect(n.Children())) != 0 || len(slices.Collect(n.ChildrenNoKeys())) != 0 || len(slices.Collect(n.All())) != 1 {
		t.Fatal("ordinary traversal includes payload")
	}
	if err := dc.Remove(); err != nil {
		t.Fatal(err)
	}
	if d.any.children.len() != 0 || dc.Parent() != nil || n.any.children.len() != 1 {
		t.Fatal("payload removal")
	}
	old := c
	if err := n.copyAnyValue(newAnyWork(context.Background(), Budget{}), nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if old.Parent() != nil {
		t.Fatal("old payload parent retained")
	}
}

func TestAnyCrossContextAndDeep(t *testing.T) {
	tr, a, _ := anyFixture()
	root := &Node{schema: a, any: &anyValue{}}
	root.refreshHash(nil) // lyd_create_any's lyd_hash
	mustLink(t, tr.insert(nil, root, insertDefault))
	last := root
	const depth = 12000
	for range depth {
		n := &Node{schema: a, any: &anyValue{}, payloadSet: tr.set}
		n.refreshHash(nil)
		mustLink(t, tr.insert(last, n, insertLast))
		last = n
	}
	value := "end"
	last.any.text, last.any.hints = &value, types.HintString
	dst, da, _ := anyFixture()
	d, _, err := dst.Dup(root, nil, DupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d.schema != da || setOf(d.any.children.list[0]) != tr.set || d.any.children.list[0].schema != a {
		t.Fatal("payload contexts rebound")
	}
	if !root.Equal(d, CompareOptions{}) {
		t.Fatal("deep equal")
	}
	for range depth {
		d = d.any.children.list[0]
	}
	changed := "changed"
	d.any.text = &changed
	if root.Equal(dst.top.list[0], CompareOptions{}) || *last.any.text != "end" {
		t.Fatal("deep comparison or isolation")
	}
}

func TestAnyMerge(t *testing.T) {
	tr, _, x := anyFixture()
	n := anyText(t, tr, x, "55", types.HintDecNum)
	src := newTree(tr.set)
	m := anyText(t, src, x, "55", types.HintString)
	n.flags, m.flags = FlagWhenTrue, FlagDefault
	if err := tr.Merge(src); err != nil {
		t.Fatal(err)
	}
	if n.any.hints != types.HintDecNum || n.flags != FlagWhenTrue {
		t.Fatal("equal merge changed destination")
	}
	text := "<node/>"
	if err := m.copyAnyValue(newAnyWork(context.Background(), Budget{}), nil, &text, 0); err != nil {
		t.Fatal(err)
	}
	if err := tr.Merge(src); err != nil {
		t.Fatal(err)
	}
	if n.any.text != nil || n.any.children.len() != 1 || n.flags != FlagDefault|FlagNew || n.any.children.opq[0] == m.any.children.opq[0] {
		t.Fatal("unequal merge must replace")
	}
}

func TestAnyResourceFailures(t *testing.T) {
	tr, _, x := anyFixture()
	n := anyText(t, tr, x, "old", types.HintString)
	text := "<node/>"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.copyAnyValue(newAnyWork(ctx, Budget{}), nil, &text, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel swallowed: %v", err)
	}
	if err := n.copyAnyValue(newAnyWork(context.Background(), Budget{MaxBytes: 3}), nil, &text, 0); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("budget swallowed: %v", err)
	}
	deep := strings.Repeat("<node>", 501) + strings.Repeat("</node>", 501)
	if err := n.copyAnyValue(newAnyWork(context.Background(), Budget{}), nil, &deep, 0); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("nesting failure swallowed: %v", err)
	}
	// A text any can start reparsing during Dup and Merge, even in the same context.
	n.any = &anyValue{text: &deep}
	if _, _, err := newTree(tr.set).Dup(n, nil, DupOptions{}); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("dup failure: %v", err)
	}
	if err := newTree(tr.set).Merge(tr); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("merge failure: %v", err)
	}
}

func TestAnySchemaPayload(t *testing.T) {
	tr, a, _ := anyFixture()
	b := &schemaBuilder{t: t, set: tr.set}
	c := b.add(a.Module, nil, schema.Container, "c", nil)
	c.Presence = true
	sn := b.add(a.Module, c, schema.Leaf, "v", &schema.Type{Base: schema.String})
	sn.Mandatory = true
	payload := newTree(tr.set)
	container := newInner(c)
	mustLink(t, payload.insert(nil, container, insertDefault))
	leaf := b.term(sn, "kept")
	mustLink(t, payload.insert(container, leaf, insertDefault))
	leaf.flags = FlagDefault | FlagWhenTrue
	leaf.meta = []*meta{{mod: a.Module, name: "note", value: leaf.value}}
	w := newAnyWork(context.Background(), Budget{})
	n, _, err := w.createAny(tr, a, container, nil, hintContainer, false, false)
	if err != nil {
		t.Fatal(err)
	}
	mustLink(t, tr.insert(nil, n, insertDefault))
	if payload.top.len() != 1 || n.any.children.list[0] == container {
		t.Fatal("copy consumed its input")
	}
	d, _, err := tr.Dup(n, nil, DupOptions{NoMeta: true, WithFlags: true})
	if err != nil {
		t.Fatal(err)
	}
	dl := d.any.children.list[0].kids.list[0]
	if dl.flags != FlagDefault|FlagNew || len(dl.meta) != 1 || dl.meta[0] == leaf.meta[0] {
		t.Fatal("payload metadata or flags lost/shared")
	}
	if !d.Equal(n, CompareOptions{}) {
		t.Fatal("shallow comparison omitted payload")
	}
	changed := b.term(sn, "changed")
	dl.value = changed.value
	if d.Equal(n, CompareOptions{}) {
		t.Fatal("payload container descendants not compared")
	}
	if err := d.Remove(); err != nil {
		t.Fatal(err)
	}
	owned, _, err := w.createAny(tr, a, container, nil, 0, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if payload.top.len() != 0 || owned.any.children.list[0] != container || container.Parent() != owned {
		t.Fatal("ownership transfer")
	}
	if err := container.Remove(); err != nil {
		t.Fatal(err)
	}
	if owned.any.children.len() != 0 {
		t.Fatal("removal from transferred payload")
	}
	// Mandatory payload descendants are not revisited by ordinary validation.
	if err := n.any.children.list[0].kids.list[0].Remove(); err != nil {
		t.Fatal(err)
	}
	if diags, err := tr.validateAll(context.Background(), ValidateOptions{Present: true}, Budget{}, nil); err != nil {
		t.Fatalf("validation entered payload: %v %v", err, diags)
	}
}

func TestAnyEmptyConstructorTransitions(t *testing.T) {
	for _, use := range []bool{false, true} {
		tr, a, x := anyFixture()
		w := newAnyWork(context.Background(), Budget{})
		for _, sn := range []*schema.Node{a, x} {
			text := "{}"
			n, _, err := w.createAny(tr, sn, nil, &text, 0, use, true)
			if err != nil {
				t.Fatal(err)
			}
			if (n.any.text == nil) != use {
				t.Fatalf("empty parsed forest, transfer=%v: %#v", use, n.any)
			}
			if err := n.copyAnyValue(w, nil, &text, 0); err != nil {
				t.Fatal(err)
			}
			if n.any.text == nil || *n.any.text != text {
				t.Fatal("empty forest copy must retain input text")
			}
			n, _, err = w.createAny(tr, sn, nil, nil, hintContainer, use, false)
			if err != nil {
				t.Fatal(err)
			}
			want := types.Hints(0)
			if use {
				want = hintContainer
			}
			if n.any.hints != want {
				t.Fatal("empty constructor hints")
			}
		}
	}
}

func TestAnyAggregateParseBudget(t *testing.T) {
	tr, a, x := anyFixture()
	root := &Node{schema: a, any: &anyValue{}}
	mustLink(t, tr.insert(nil, root, insertDefault))
	for range 2 {
		text := "<node/>"
		mustLink(t, tr.insert(root, &Node{schema: x, any: &anyValue{text: &text}, payloadSet: tr.set}, insertLast))
	}
	for _, budget := range []Budget{{MaxBytes: 10}, {MaxNodes: 1}} {
		_, err := tr.dupWork(root, nil, false, insertDefault, 0, tr.dupDest(nil), newAnyWork(context.Background(), budget))
		if !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("aggregate parse work escaped budget: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tr.dupWork(root, nil, false, insertDefault, 0, tr.dupDest(nil), newAnyWork(ctx, Budget{})); !errors.Is(err, context.Canceled) {
		t.Fatalf("tree copy cancel: %v", err)
	}
}

func TestAnyOpaqueXMLComparison(t *testing.T) {
	tr, _, x := anyFixture()
	for _, tc := range []struct {
		left, right string
		equal       bool
	}{
		{`<n xmlns:p="urn:anycore">p:v</n>`, `<n xmlns:q="urn:anycore">q:v</n>`, true},
		{`<n xmlns:p="urn:unknown">p:v</n>`, `<n xmlns:p="urn:unknown">p:v</n>`, false},
		{`<n>literal</n>`, `<n>literal</n>`, true},
		{`<n>left</n>`, `<n>right</n>`, false},
		{"<n>\xf0\x81\x80\x80A</n>", "<n>\xf0\x81\x80\x80B</n>", false},
	} {
		w := newAnyWork(context.Background(), Budget{})
		n, _, err := w.createAny(tr, x, nil, &tc.left, 0, true, true)
		if err != nil {
			t.Fatal(err)
		}
		m, _, err := w.createAny(tr, x, nil, &tc.right, 0, true, true)
		if err != nil {
			t.Fatal(err)
		}
		if n.Equal(m, CompareOptions{}) != tc.equal {
			t.Fatalf("XML value comparison: %s / %s", tc.left, tc.right)
		}
	}
	invalid := opaque{Value: "\x01A", Format: types.FormatXML}
	if compareXMLValue(&invalid, &opaque{Value: "\x01B", Format: types.FormatXML}) ||
		compareXMLValue(&invalid, &opaque{Value: invalid.Value, Format: types.FormatXML}) {
		t.Fatal("XML value scan errors must compare unequal")
	}
}

func TestAnyOpaqueContextCopy(t *testing.T) {
	src, _, x := anyFixture()
	dst, _, _ := anyFixture()
	src.set.Modules[0].Revision = "2020-01-01"
	dst.set.Modules[0].Revision = "2021-01-01"
	makeOpaque := func(tr *Tree) *Node {
		pc := types.XMLNamespaces{Set: tr.set, NS: map[string]string{"p": "urn:anycore"}}
		n := newOpaque(opaque{Name: "n", Value: "p:v", Format: types.FormatXML, Prefixes: pc, set: tr.set,
			Attrs: []attr{{Name: "a", Value: "p:v", Format: types.FormatXML, Prefixes: pc}}})
		mustLink(t, tr.insert(nil, n, insertDefault))
		return n
	}
	orig, want := makeOpaque(src), makeOpaque(dst)
	d, _, err := dst.Dup(orig, nil, DupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Equal(want, CompareOptions{}) || d.opaq.Attrs[0].Prefixes.(types.XMLNamespaces).Set != dst.set {
		t.Fatal("ordinary opaque duplication kept source prefix context")
	}
	w := newAnyWork(context.Background(), Budget{})
	n, _, err := w.createAny(src, x, orig, nil, 0, true, false)
	if err != nil {
		t.Fatal(err)
	}
	mustLink(t, src.insert(nil, n, insertDefault))
	d, _, err = dst.Dup(n, nil, DupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	payload := d.any.children.opq[0]
	if !payload.Equal(orig, CompareOptions{}) || payload.Equal(want, CompareOptions{}) ||
		payload.opaq.Prefixes.(types.XMLNamespaces).Set != src.set || payload.opaq.Attrs[0].Prefixes.(types.XMLNamespaces).Set != src.set {
		t.Fatal("any payload prefix contexts rebound")
	}
}

func TestAnyPayloadLinkCleanup(t *testing.T) {
	for _, action := range []string{"clear", "remove", "subtree", "trim"} {
		t.Run(action, func(t *testing.T) {
			outer, a, _ := anyFixture()
			payload, _, _ := anyFixture()
			payload.set.LeafrefLinking = true
			b := &schemaBuilder{t: t, set: payload.set}
			sn := b.add(payload.set.Modules[0], nil, schema.Leaf, "v", &schema.Type{Base: schema.String})
			leaf, outside := b.term(sn, "v"), b.term(sn, "v")
			mustLink(t, payload.insert(nil, leaf, insertDefault))
			w := newAnyWork(context.Background(), Budget{})
			n, _, err := w.createAny(outer, a, leaf, nil, 0, true, false)
			if err != nil {
				t.Fatal(err)
			}
			mustLink(t, outer.insert(nil, n, insertDefault))
			linkLeafrefNode(outside, leaf)
			switch action {
			case "clear":
				err = n.copyAnyValue(w, nil, nil, 0)
			case "remove":
				err = n.Remove()
			case "subtree":
				freeSubtreeLinks(outer.set, n)
			case "trim":
				_, err = outer.TrimXPath("/*[false()]", XPathOptions{})
			}
			if err != nil || outside.links != nil || leaf.links != nil {
				t.Fatalf("payload links survived %s: %v", action, err)
			}
		})
	}
}

func TestAnyObservers(t *testing.T) {
	tr, a, x := anyFixture()
	if (*Node)(nil).AnyValueType() != AnyValueNone || len(slices.Collect((*Node)(nil).ChildrenAny())) != 0 {
		t.Fatal("nil node")
	}
	m := a.Module
	c := &schema.Node{Name: "c", Kind: schema.Container, Module: m}
	l := &schema.Node{Name: "l", Kind: schema.Leaf, Module: m, Parent: c}
	cn, ln := &Node{schema: c}, &Node{schema: l}
	mustLink(t, tr.insert(nil, cn, insertDefault))
	mustLink(t, tr.insert(cn, ln, insertDefault))
	if cn.AnyValueType() != AnyValueNone || ln.AnyValueType() != AnyValueNone {
		t.Fatal("container/leaf not AnyValueNone")
	}
	if got := slices.Collect(cn.ChildrenAny()); len(got) != 1 || got[0] != ln {
		t.Fatalf("container ChildrenAny = %v", got)
	}

	value := `<a/><b/>`
	ad, _, err := newAnyWork(context.Background(), Budget{}).createAny(tr, a, nil, &value, 0, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if ad.AnyValueType() != AnyValueDataTree {
		t.Fatalf("anydata tree = %v", ad.AnyValueType())
	}
	if len(slices.Collect(ad.Children())) != 0 {
		t.Fatal("Children includes payload")
	}
	got := slices.Collect(ad.ChildrenAny())
	if len(got) != 2 || got[0].Name() != "a" || got[1].Name() != "b" || got[0].Parent() != ad {
		t.Fatalf("anydata ChildrenAny = %v", got)
	}

	if n := anyText(t, tr, x, "plain", types.HintString); n.AnyValueType() != AnyValueText {
		t.Fatalf("anyxml text = %v", n.AnyValueType())
	}
	empty := anyText(t, tr, x, "", types.HintString)
	if s, ok, err := empty.AnyValueString(FormatJSON); empty.AnyValueType() != AnyValueText || s != "" || !ok || err != nil {
		t.Fatalf("explicit empty = %v %q %t %v", empty.AnyValueType(), s, ok, err)
	}
	absent := &Node{schema: x, any: &anyValue{}}
	if _, ok, err := absent.AnyValueString(FormatJSON); absent.AnyValueType() != AnyValueNone || ok || err != nil {
		t.Fatalf("absent = %v %t %v", absent.AnyValueType(), ok, err)
	}
	if len(slices.Collect(absent.ChildrenAny())) != 0 {
		t.Fatal("absent ChildrenAny")
	}
}
