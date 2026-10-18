// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"slices"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// TestSortRelocationOutsideOperation: building a missing RB tree relocates instances outside an
// operation too (lyds_additionally_create_rb_nodes), but only an operation's children keep
// first-instance records, so there is no first-record transition there.
func TestSortRelocationOutsideOperation(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  container c { leaf-list v { type uint32; } }
}`)
	mod := opIndexModule(t, set)
	c := mod.Child(nil, "c")
	sn := schema.FindChild(c, nil, mod, "v", 0)
	tr := newTree(set)
	cn := newInner(c)
	mustLink(t, tr.insert(nil, cn, insertDefault))
	for _, v := range []int{10, 2, 3, 4} {
		mustLink(t, tr.insert(cn, opIndexTerm(t, sn, v), insertLast))
	}
	if cn.kids.ht == nil {
		t.Fatal("no children table")
	}
	mustLink(t, tr.insert(cn, opIndexTerm(t, sn, 11), insertDefault))
	var got []string
	for _, n := range cn.kids.list {
		got = append(got, n.value.Canonical())
	}
	if want := []string{"2", "3", "4", "10", "11"}; !slices.Equal(got, want) {
		t.Errorf("run %v, want %v", got, want)
	}
}
