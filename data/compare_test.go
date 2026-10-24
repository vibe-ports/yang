// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// TestEqualNil: lyd_compare_single(NULL, NULL) is LY_SUCCESS, one NULL node is LY_ENOT (the
// oracle's compare step always passes nodes; ut-compare/* and seq/compare-* cover the rest).
func TestEqualNil(t *testing.T) {
	tr := newTree(editSet(t))
	n, err := tr.NewPath("/pv2-edit:c/a", "x", NewPathOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var none *Node
	if !none.Equal(nil, CompareOptions{}) || n.Equal(nil, CompareOptions{}) || none.Equal(n, CompareOptions{}) ||
		!n.Equal(n, CompareOptions{FullRecursion: true, Defaults: true, Opaque: true}) {
		t.Error("nil handling")
	}
}

// Any payload roots can retain schema nodes from different parents. The lookup
// must check the paired RHS node's parent, as lyd_compare_siblings_ does.
func TestCompareMixedSchemaParentPayload(t *testing.T) {
	mod := &schema.Module{Name: "mixed", Namespace: "urn:mixed", Implemented: true}
	a := &schema.Node{Kind: schema.Container, Name: "a", Module: mod}
	b := &schema.Node{Kind: schema.Container, Name: "b", Module: mod}
	str := &schema.Type{Base: schema.String}
	first := &schema.Node{Kind: schema.LeafList, Name: "item", Module: mod, Parent: a, Type: str, Config: true}
	second := &schema.Node{Kind: schema.LeafList, Name: "item", Module: mod, Parent: b, Type: str, Config: true}
	v, diag := types.Store(str, "same", types.FormatJSON, types.HintString, nil, first)
	if diag != nil {
		t.Fatal(diag)
	}
	nodes := []*Node{newTerm(first, v), newTerm(second, v)}
	sibs := &siblings{nodeDeque: nodeDeque{list: nodes}} // like an any payload: no children hash table
	tr := newTree(&schema.Set{Modules: []*schema.Module{mod}})
	frames, ok, err := compareSiblingFrames(tr, &tr.work, nil, sibs, sibs, CompareOptions{})
	equal, compareErr := compareDataStack(tr, &tr.work, frames)
	if err != nil || compareErr != nil || !ok || !equal {
		t.Fatal("mixed-parent payload must compare equal to itself")
	}
	anySchema := &schema.Node{Kind: schema.AnyData, Name: "payload", Module: mod}
	mod.Top = append(mod.Top, anySchema)
	payload := &Node{schema: anySchema, any: &anyValue{}}
	mustLink(t, tr.insert(nil, payload, insertDefault))
	for _, n := range nodes {
		mustLink(t, tr.insert(payload, n, insertLast))
	}
	if !payload.Equal(payload, CompareOptions{}) {
		t.Fatal("mixed-parent any payload must compare equal to itself")
	}
}

// LYD_COMPARE_OPAQ lets the schema check accept an opaque counterpart of a
// system-ordered leaf-list. lyd_find_sibling_first must then find no instance.
func TestCompareOpaqueSystemLeafList(t *testing.T) {
	set := pv2Set(t, "pv2-fz")
	const prefix = `<c xmlns="urn:fz">`
	for _, tc := range []struct {
		name, first, second string
		hasHash             bool
	}{
		{"without children hash", `<ll>1</ll>`, `<ll>bad</ll>`, false},
		{"with children hash", `<ll>1</ll><ll>2</ll><ll>3</ll><ll>4</ll><ll>5</ll>`,
			`<ll>bad</ll><ll>2</ll><ll>3</ll><ll>4</ll><ll>5</ll>`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := parseOnlyXML(t, set, prefix+tc.first+`</c>`)
			second := parseOnlyXML(t, set, prefix+tc.second+`</c>`)
			a, b := first.top.first(), second.top.first()
			if a == nil || b == nil || len(a.kids.list) == 0 || a.kids.list[0].schema == nil || len(b.kids.opq) != 1 {
				t.Fatal("fixture must pair a typed leaf-list with an opaque counterpart")
			}
			if (b.kids.ht != nil) != tc.hasHash {
				t.Fatal("unexpected children hash state")
			}
			if a.Equal(b, CompareOptions{FullRecursion: true, Opaque: true}) {
				t.Fatal("typed value matched an opaque invalid value")
			}
			if _, err := Diff(first, second, DiffOptions{}); err != nil {
				t.Fatal("diff with opaque counterpart:", err)
			}
			if err := first.Merge(second); err != nil {
				t.Fatal("merge with opaque counterpart:", err)
			}
		})
	}
}
