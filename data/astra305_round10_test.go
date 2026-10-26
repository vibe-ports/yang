// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// TestMergeKeylessUnderOpaqueChecksFirstSchemaParent pins
// lyd_find_sibling_first's initial data-parent guard. Dup can place schema data
// from unrelated schema parents under one opaque node; the first child decides
// whether the native lookup is attempted at all.
func TestMergeKeylessUnderOpaqueChecksFirstSchemaParent(t *testing.T) {
	set := &schema.Set{}
	b := &schemaBuilder{t: t, set: set}
	mod := b.module("mixed")
	a := b.add(mod, nil, schema.Container, "a", nil)
	x := b.add(mod, a, schema.Leaf, "x", &schema.Type{Base: schema.String})
	bParent := b.add(mod, nil, schema.Container, "b", nil)
	l := b.add(mod, bParent, schema.List, "l", nil)
	v := b.add(mod, l, schema.Leaf, "v", &schema.Type{Base: schema.String})

	staging := newTree(set)
	aNode := newInner(a, &staging.work)
	mustLink(t, staging.insert(nil, aNode, insertLast))
	xNode := b.term(x, "x")
	mustLink(t, staging.insert(aNode, xNode, insertLast))
	bNode := newInner(bParent, &staging.work)
	mustLink(t, staging.insert(nil, bNode, insertLast))
	lNode := newInner(l, &staging.work)
	mustLink(t, staging.insert(bNode, lNode, insertLast))
	mustLink(t, staging.insert(lNode, b.term(v, "same"), insertLast))

	makeOpaque := func(withX bool) *Tree {
		t.Helper()
		tr := newTree(set)
		parent := newOpaque(opaque{Name: "parent", ModuleNS: mod.Name, Format: types.FormatJSON})
		mustLink(t, tr.insert(nil, parent, insertLast))
		// Match the native probe's setup: the later x duplicate anchors
		// before the list, making a's schema parent the first-child guard.
		if _, _, err := tr.Dup(lNode, parent, DupOptions{Recursive: true}); err != nil {
			t.Fatal(err)
		}
		if withX {
			if _, _, err := tr.Dup(xNode, parent, DupOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		return tr
	}

	dst, src := makeOpaque(true), makeOpaque(false)
	parent := dst.top.first()
	if parent.schema != nil || parent.kids.ht != nil || parent.kids.SchemaLen() != 2 ||
		parent.kids.At(0).schema.DataParent() != a || parent.kids.At(1).schema.DataParent() != bParent {
		t.Fatal("fixture does not contain the required mixed-parent no-table children")
	}
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	var lists int
	for _, child := range parent.kids.SchemaAll() {
		if child.schema == l {
			lists++
		}
	}
	if lists != 2 {
		t.Fatalf("keyless list instances after merge = %d, want 2", lists)
	}
}
