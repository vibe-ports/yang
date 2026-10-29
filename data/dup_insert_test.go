// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"slices"
	"strconv"
	"testing"
)

// Source-backed expectations: lyd_dup_r retains child order; lyd_insert_node finds the
// existing sorted run or uses lyd_insert_get_next_anchor's table/manual branch.
func TestDupCrossSchemaInsert(t *testing.T) {
	const head = `module m { yang-version 1.1; namespace "urn:m"; prefix m; container r { `
	const x = `leaf-list x { type uint8; } `
	const y = `leaf y { type string; } `
	const z = `leaf z { type string; } `
	const between = `leaf between { type string; } `
	const l = `list l { key k; leaf k { type uint8; } } `
	const u = `leaf-list u { type uint8; ordered-by user; } `
	srcSet := opIndexSet(t, head+x+l+u+z+y+between+`}}`)
	dstSet := opIndexSet(t, head+y+between+z+x+l+u+`}}`)
	for _, tc := range []struct {
		name, input, add, value string
		want                    []string
	}{
		{"leaf-list", `"x":[2],"y":"y"`, "x", "1", []string{"x=1", "x=2", "y=y"}},
		{"leaf-list-table", `"x":[2,4],"z":"z","y":"y"`, "x", "3", []string{"x=2", "x=3", "x=4", "z=z", "y=y"}},
		{"list", `"l":[{"k":2}],"y":"y"`, "l", "1", []string{"l=1", "l=2", "y=y"}},
		{"anchor", `"x":[2],"z":"z","y":"y"`, "between", "b", []string{"between=b", "x=2", "z=z", "y=y"}},
		{"anchor-table", `"x":[2,4],"z":"z","y":"y"`, "between", "b", []string{"x=2", "x=4", "between=b", "z=z", "y=y"}},
		{"user-list", `"u":[2],"y":"y"`, "u", "1", []string{"u=2", "u=1", "y=y"}},
		{"user-list-table", `"u":[2,4],"z":"z","y":"y"`, "u", "1", []string{"u=2", "u=4", "z=z", "y=y", "u=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, _, err := parseJSONString(srcSet, `{"m:r":{`+tc.input+`}}`, Reject, true)
			if err != nil {
				t.Fatal(err)
			}
			dst := newTree(dstSet)
			root, _, err := dst.Dup(src.top.first(), nil, DupOptions{Recursive: true})
			if err != nil {
				t.Fatal(err)
			}
			if tc.add == "l" {
				_, err = dst.NewList(root, "l", []string{tc.value}, NewOptions{})
			} else {
				_, err = dst.NewTerm(root, tc.add, tc.value, NewOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, n := range root.kids.SchemaAll() {
				v := n.value.Canonical()
				if n.Name() == "l" {
					v = n.kids.At(0).value.Canonical()
				}
				got = append(got, n.Name()+"="+v)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("children = %v, want %v", got, tc.want)
			}
			for _, n := range root.kids.SchemaAll() {
				if got, err := dst.Find(n.Path()); err != nil || got != n {
					t.Errorf("Find(%s) = %v, %v", n.Path(), got, err)
				}
			}
			if tc.name == "user-list-table" {
				checkDupSplitLeaders(t, dst, root)
			}
		})
	}
}

// Repeated insertions at a copied run's end must not rescan all preceding instances.
func TestDupCrossSchemaInsertWork(t *testing.T) {
	const head = `module m { yang-version 1.1; namespace "urn:m"; prefix m; container r { `
	const x = `leaf-list x { type uint32; } `
	const y = `leaf y { type string; } `
	srcSet, dstSet := opIndexSet(t, head+x+y+`}}`), opIndexSet(t, head+y+x+`}}`)
	src, _, err := parseJSONString(srcSet, `{"m:r":{"x":[0],"y":"y"}}`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	opIndexScaling(t, scaleSizes([]int{512, 1024, 2048, 4096}, 3), func(count int) int64 {
		tr := newTree(dstSet)
		root, _, err := tr.Dup(src.top.first(), nil, DupOptions{Recursive: true})
		if err != nil {
			t.Fatal(err)
		}
		tr.work.visits = true
		tr.work.Store(0)
		for i := 1; i <= count; i++ {
			if _, err := tr.NewTerm(root, "x", strconv.Itoa(i), NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if root.kids.SchemaLen() != count+2 || root.kids.At(count).value.Canonical() != strconv.Itoa(count) ||
			root.kids.At(count+1).Name() != "y" {
			t.Fatal("insertion split the copied run")
		}
		return tr.work.Load()
	})
}

func checkDupSplitLeaders(t *testing.T, tr *Tree, root *Node) {
	t.Helper()
	first := findNode(t, tr, "/m:r/u[.='2']")
	last := findNode(t, tr, "/m:r/u[.='1']")
	checkLeader := func(want *Node) {
		t.Helper()
		if got := tr.schemaLookup(&root.kids, last.schema); got != want {
			t.Fatalf("first schema record = %v, want %v", got, want)
		}
		// l is the closest schema predecessor of u. A new l uses u's first record
		// as its insertion anchor, making the lookup observable in printed data.
		n, err := tr.NewList(root, "l", []string{"9"}, NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if at := slices.Index(root.kids.schemaSpan().Collect(), n); at+1 >= root.kids.SchemaLen() || root.kids.At(at+1) != want {
			t.Fatalf("new list did not use first-instance anchor %v", want)
		}
		if err := n.Remove(); err != nil {
			t.Fatal(err)
		}
	}
	if got := tr.nextAnchor(first); got != nil {
		t.Fatalf("last schema's hash anchor = %v, want none", got)
	}
	if hi := tr.runEnd(&root.kids, 0); hi != 2 {
		t.Fatalf("split run end = %d, want 2", hi)
	}
	if err := first.Remove(); err != nil {
		t.Fatal(err)
	}
	checkLeader(last)
	if got := tr.documentFirst(&root.kids, last.schema); got.value.Canonical() != "4" {
		t.Fatalf("document first after unlink = %v, want 4", got)
	}
	if _, err := last.SetValue("3"); err != nil {
		t.Fatal(err)
	}
	checkLeader(findNode(t, tr, "/m:r/u[.='4']"))
	if _, err := tr.TrimXPath("/m:r/u[.='3']", XPathOptions{}); err != nil {
		t.Fatal(err)
	}
	checkLeader(last)
}
