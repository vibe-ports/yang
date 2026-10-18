// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

// The fourth child builds the table while the descending run is still deferred.
// Every parsed instance must remain reachable by its stored hash after publication.
func TestParseDeferredRunHashThreshold(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parse func() (*Tree, error)
		paths []string
	}{
		{"json", func() (*Tree, error) {
			tr, _, err := parseJSONString(pjSchema(), `{"pj:c":{"ll":[4,3,2,1]}}`, Reject, false)
			return tr, err
		}, []string{"/pj:c/ll[.='1']", "/pj:c/ll[.='2']", "/pj:c/ll[.='3']", "/pj:c/ll[.='4']"}},
		{"xml", func() (*Tree, error) {
			tr, _, err := parseXMLString(pjSchema(), `<c xmlns="urn:pj"><ll>4</ll><ll>3</ll><ll>2</ll><ll>1</ll></c>`, Reject, false)
			return tr, err
		}, []string{"/pj:c/ll[.='1']", "/pj:c/ll[.='2']", "/pj:c/ll[.='3']", "/pj:c/ll[.='4']"}},
		{"json-list", func() (*Tree, error) {
			tr, _, err := parseJSONString(pjSchema(), `{"pj:c":{"l":[{"k":"d"},{"k":"c"},{"k":"b"},{"k":"a"}]}}`, Reject, false)
			return tr, err
		}, []string{"/pj:c/l[k='a']", "/pj:c/l[k='b']", "/pj:c/l[k='c']", "/pj:c/l[k='d']"}},
		{"xml-list", func() (*Tree, error) {
			tr, _, err := parseXMLString(pjSchema(), `<c xmlns="urn:pj"><l><k>d</k></l><l><k>c</k></l><l><k>b</k></l><l><k>a</k></l></c>`, Reject, false)
			return tr, err
		}, []string{"/pj:c/l[k='a']", "/pj:c/l[k='b']", "/pj:c/l[k='c']", "/pj:c/l[k='d']"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := tc.parse()
			if err != nil {
				t.Fatal(err)
			}
			parent, err := tr.Find("/pj:c")
			if err != nil || parent == nil || parent.kids.ht == nil || len(parent.kids.list) != 4 {
				t.Fatalf("parent %v, error %v", parent, err)
			}
			for _, path := range tc.paths {
				n, err := tr.Find(path)
				if err != nil || n == nil {
					t.Fatalf("%s: node %v, error %v", path, n, err)
				}
			}
		})
	}
}

// A regular link or a merge splice can attach a node beside a deferred parser
// run without adding it to that run's RB tree. Table construction still reads it.
func TestPendingOrderIncludesOtherLinks(t *testing.T) {
	for _, splice := range []bool{false, true} {
		name := "link"
		if splice {
			name = "splice"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			tr := newTree(f.set)
			parent := newInner(f.c)
			mustLink(t, tr.insert(nil, parent, insertDefault))
			for _, value := range []string{"4", "3", "2"} {
				mustLink(t, tr.insert(parent, f.term(t, f.ll, value), insertLast))
			}
			sib := &parent.kids
			ord, _ := newDirtyOrder(&tr.work, sib.list, tr.compareSorted, true)
			sib.pending = map[*schema.Node]*dirtyOrder{f.ll: ord}
			n := f.term(t, f.ll, "1")
			if splice {
				mustLink(t, tr.splice(parent, sib, 0, len(sib.list), []*Node{n}, true))
			} else {
				mustLink(t, tr.insert(parent, n, insertLast))
			}
			if sib.ht == nil {
				t.Fatal("fourth child did not build a table")
			}
			for _, value := range []string{"1", "2", "3", "4"} {
				got, err := tr.Find("/b:c/ll[.='" + value + "']")
				if err != nil || got == nil {
					t.Fatalf("ll=%s: node %v, error %v", value, got, err)
				}
			}
			k := dirtyRun{sib, f.ll}
			lc := &lydCtx{tree: tr, dirtyOrd: map[dirtyRun]*dirtyOrder{k: ord}}
			lc.flushDirtyRun(k, 0)
			seen := map[*Node]bool{}
			for _, child := range sib.list {
				if seen[child] {
					t.Fatal("publishing the deferred run duplicated a child")
				}
				seen[child] = true
			}
			if len(seen) != 4 {
				t.Fatalf("publishing the deferred run kept %d children", len(seen))
			}
		})
	}
}

func TestAppendSortedBudgetPreflightKeepsRun(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	parent := newInner(f.c)
	mustLink(t, tr.insert(nil, parent, insertDefault))
	mustLink(t, tr.insert(parent, f.term(t, f.ll, "4"), insertDefault))
	n := f.term(t, f.ll, "3")
	tr.work.hashErr = yang.ErrBudget
	lc := &lydCtx{tree: tr}
	inserted, err := lc.appendSorted(parent, n)
	if inserted || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("inserted %t, error %v", inserted, err)
	}
	if len(parent.kids.list) != 1 || n.parent != nil || n.inRB || parent.kids.hasRB(f.ll) ||
		len(parent.kids.pending) != 0 || len(lc.runOrd) != 0 {
		t.Fatal("failed preflight changed the sibling run")
	}
}
