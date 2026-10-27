// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"slices"
	"testing"
)

func TestCrossContextHashAnchorAncestry(t *testing.T) {
	model := func(branch string) string {
		return `module anchor {yang-version 1.1; namespace "urn:anchor"; prefix a;
 container top {leaf a {type string;} leaf b {type string;}
 choice pick {case ` + branch + ` {leaf c {type string;}}}
 leaf d {type string;} leaf e {type string;}}}`
	}
	for _, branch := range []string{"left", "right"} {
		a, b := reviewDiffSet(t, model("left")), reviewDiffSet(t, model(branch))
		sourceA := parseOnlyXML(t, a, `<top xmlns="urn:anchor"><a>a</a><b>b</b><d>d</d><e>e</e></top>`)
		sourceB := parseOnlyXML(t, b, `<top xmlns="urn:anchor"><c>c</c></top>`)
		tr := newTree(a)
		tr.mixedDiff = true
		parent := newInner(topOf(sourceA).schema)
		if err := tr.insert(nil, parent, insertDefault); err != nil {
			t.Fatal(err)
		}
		children := &topOf(sourceA).kids
		// Keep each source context, as diffDup does for mixed-context diffs.
		for _, n := range []*Node{children.At(0), topOf(sourceB).kids.At(0), children.At(2), children.At(3)} {
			if _, err := tr.dupRIn(n, parent, false, insertLast, 0, ctxOf(n)); err != nil {
				t.Fatal(err)
			}
		}
		if parent.kids.ht == nil {
			t.Fatal("expected children hash table")
		}
		if _, err := tr.dupRIn(children.At(1), parent, false, insertDefault, 0, ctxOf(children.At(1))); err != nil {
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
