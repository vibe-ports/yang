// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"slices"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

func TestDiffCrossContextInsertionScan(t *testing.T) {
	const model = `module scan { yang-version 1.1; namespace "urn:scan"; prefix s;
 leaf a { type string; } leaf b { type string; }
 leaf c { type string; } leaf d { type string; }
 container top {
 leaf a { type string; } leaf b { type string; }
 leaf c { type string; } leaf d { type string; }
 } }`
	for _, nested := range []bool{false, true} {
		a, b := inlineDiffSet(t, model), inlineDiffSet(t, model)
		before := `<a xmlns="urn:scan">a</a><b xmlns="urn:scan">b</b>`
		after := `<c xmlns="urn:scan">c</c><d xmlns="urn:scan">d</d>`
		if nested {
			before = `<top xmlns="urn:scan">` + before + `</top>`
			after = `<top xmlns="urn:scan">` + after + `</top>`
		}
		first, second := parseOnlyXML(t, a, before), parseOnlyXML(t, b, after)
		methods := []struct {
			name string
			diff func() (*Tree, error)
		}{
			{"Diff", func() (*Tree, error) { return Diff(first, second, DiffOptions{}) }},
			{"DiffSiblings", func() (*Tree, error) { return DiffSiblings(topOf(first), topOf(second), DiffOptions{}) }},
		}
		if nested {
			methods = append(methods, struct {
				name string
				diff func() (*Tree, error)
			}{"DiffTree", func() (*Tree, error) { return DiffTree(topOf(first), topOf(second), DiffOptions{}) }})
		}
		for _, method := range methods {
			diff, err := method.diff()
			if err != nil {
				t.Fatal(err)
			}
			if !diff.mixedDiff {
				t.Fatal("cross-context diff lacks insertion scan flag")
			}
			nodes := diff.Top()
			if nested {
				nodes = topOf(diff).Children()
			}
			// libyang v5.8.6 src/tree_data.c:556-587: the second B node passes
			// the first B node, then stops at A's owner-module pointer.
			if got, want := names(nodes), []string{"c=c", "d=d", "a=a", "b=b"}; !slices.Equal(got, want) {
				t.Fatalf("%s nested=%v: %v, want %v", method.name, nested, got, want)
			}
			rev, err := diff.ReverseDiff()
			if err != nil {
				t.Fatal(err)
			}
			if !rev.mixedDiff {
				t.Fatal("reverse lost insertion scan flag")
			}
			var checkContext func(*Node)
			checkContext = func(n *Node) {
				if setOf(n) != rev.set {
					t.Fatal("reverse did not remap a node into the first diff node's context")
				}
				for c := range n.Children() {
					checkContext(c)
				}
			}
			for n := range rev.Top() {
				checkContext(n)
			}

			copy := newTree(diff.set)
			if _, err := copy.dupNodes(topOf(diff), nil, dupRecursive|dupKeepContext, true); err != nil {
				t.Fatal(err)
			}
			if !copy.mixedDiff {
				t.Fatal("context-retaining duplicate lost insertion scan flag")
			}
		}
	}
}

func TestReverseDiffCrossContextOnlyLeaf(t *testing.T) {
	a := inlineDiffSet(t, `module rev {namespace "urn:rev"; prefix r; leaf a {type string;}}`)
	b := inlineDiffSet(t, `module rev {namespace "urn:rev"; prefix r; leaf a {type string;} leaf b {type string;}}`)
	first := parseOnlyXML(t, a, `<a xmlns="urn:rev">same</a>`)
	second := parseOnlyXML(t, b, `<a xmlns="urn:rev">same</a><b xmlns="urn:rev">new</b>`)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rev, err := diff.ReverseDiff()
	if err != nil {
		t.Fatal(err)
	}
	// src/diff.c:3489-3496 uses the diff node's context; b is absent in A.
	if rev.set != b || setOf(topOf(rev)) != b || topOf(rev).schema != topOf(diff).schema {
		t.Fatal("reverse did not retain B context")
	}
	if op, _ := diffGetOp(topOf(rev)); op != diffDelete {
		t.Fatalf("reverse operation=%v", op)
	}
}

func TestDiffCrossContextIntegerSort(t *testing.T) {
	for _, tc := range []struct {
		oldType, newType, old, new string
		want                       []string
	}{
		{"int8", "int16", "2", "10", []string{"v=2", "v=10"}},
		{"int8", "int16", "2", "256", []string{"v=256", "v=2"}},
		{"int16", "int8", "2", "-1", []string{"v=2", "v=-1"}},
	} {
		model := func(typ string) *schema.Set {
			return inlineDiffSet(t, `module widths {yang-version 1.1; namespace "urn:widths"; prefix w; container top { leaf-list v {type `+typ+`;} }}`)
		}
		first := parseOnlyXML(t, model(tc.oldType), `<top xmlns="urn:widths"><v>`+tc.old+`</v></top>`)
		second := parseOnlyXML(t, model(tc.newType), `<top xmlns="urn:widths"><v>`+tc.new+`</v></top>`)
		diff, err := Diff(first, second, DiffOptions{})
		if err != nil {
			t.Fatal(err)
		}
		// integer.c:249-294 reads both payloads at the existing value's width.
		if got := names(topOf(diff).Children()); !slices.Equal(got, tc.want) {
			t.Fatalf("%s %s -> %s %s: %v, want %v", tc.oldType, tc.old, tc.newType, tc.new, got, tc.want)
		}
	}
}

func TestCrossContextHashAnchorAncestry(t *testing.T) {
	model := func(branch string) string {
		return `module anchor {yang-version 1.1; namespace "urn:anchor"; prefix a;
 container top {leaf a {type string;} leaf b {type string;}
 choice pick {case ` + branch + ` {leaf c {type string;}}}
 leaf d {type string;} leaf e {type string;}}}`
	}
	for _, branch := range []string{"left", "right"} {
		a, b := inlineDiffSet(t, model("left")), inlineDiffSet(t, model(branch))
		sourceA := parseOnlyXML(t, a, `<top xmlns="urn:anchor"><a>a</a><b>b</b><d>d</d><e>e</e></top>`)
		sourceB := parseOnlyXML(t, b, `<top xmlns="urn:anchor"><c>c</c></top>`)
		tr := newTree(a)
		tr.mixedDiff = true
		parent := newInner(topOf(sourceA).schema)
		tr.insert(nil, parent, insertDefault)
		children := topOf(sourceA).kids.list
		for _, n := range []*Node{children[0], topOf(sourceB).kids.list[0], children[2], children[3]} {
			if _, err := tr.dupR(n, parent, false, insertLast, dupKeepContext); err != nil {
				t.Fatal(err)
			}
		}
		if parent.kids.ht == nil {
			t.Fatal("expected children hash table")
		}
		if _, err := tr.dupR(children[1], parent, false, insertDefault, dupKeepContext); err != nil {
			t.Fatal(err)
		}
		want := []string{"a=a", "b=b", "c=c", "d=d", "e=e"}
		if branch == "right" {
			want = []string{"a=a", "c=c", "b=b", "d=d", "e=e"}
		}
		// tree_data.c:522-539 probes following schemas; tree_data_common.c:1088
		// compares parents, so c from a different case cannot be the hash anchor.
		if got := names(parent.Children()); !slices.Equal(got, want) {
			t.Fatalf("case %s: %v, want %v", branch, got, want)
		}
	}
}
