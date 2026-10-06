// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// fakeTree answers every tree-time lookup with found.
type fakeTree bool

func (t fakeTree) LeafrefTarget(*schema.Type, types.Value) (bool, error) { return bool(t), nil }
func (t fakeTree) InstanceExists(types.Path) bool                        { return bool(t) }

// TestValueValidate: lyd_value_validate / lyd_value_validate3 — store, LY_EINCOMPLETE without a
// context node, resolution with one, union realtype, logging at the context node and schema node.
func TestValueValidate(t *testing.T) {
	f := newFixture()
	lref := &schema.Type{Base: schema.Leafref, Path: "/b:c/ll", Realtype: f.u8, RequireInstance: true}
	ref := &schema.Node{Kind: schema.Leaf, Name: "ref", Module: f.b, Parent: f.c, Type: lref}
	f.c.Children = append(f.c.Children, ref)
	uni := &schema.Type{Base: schema.Union, Union: []*schema.Type{f.u8, f.str}}
	un := &schema.Node{Kind: schema.Leaf, Name: "un", Module: f.b, Parent: f.c, Type: uni}
	f.c.Children = append(f.c.Children, un)
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)

	cases := []struct {
		name     string
		sn       *schema.Node
		val      string
		ctx      *Node
		tree     types.Tree
		realtype *schema.Type
		canon    string
		incompl  bool
		diag     string
	}{
		{"valid", f.ll, "007", nil, nil, f.u8, "7", false, ""},
		{"invalid", f.ll, "x", nil, nil, nil, "", false, `LY_EVALID LYVE_DATA /b:c/ll: Invalid type uint8 value "x".`},
		{"incomplete", ref, "7", nil, nil, f.u8, "7", true, ""},
		{"resolved", ref, "7", c, fakeTree(true), f.u8, "7", false, ""},
		{"no target", ref, "7", c, fakeTree(false), nil, "", false,
			`LY_EVALID LYVE_DATA /b:c/ref: Invalid leafref value "7" - no target instance "/b:c/ll" with the same value.`},
		{"union member", un, "8", nil, nil, f.u8, "8", false, ""},
		{"union string", un, "x", nil, nil, f.str, "x", false, ""},
	}
	for _, tc := range cases {
		l := &logger{set: f.set}
		rt, canon, inc, err := l.validateValue(tc.sn, tc.val, tc.ctx, tc.tree)
		if rt != tc.realtype || canon != tc.canon || inc != tc.incompl || (err != nil) != (tc.diag != "") {
			t.Errorf("%s: %v %q %v %v", tc.name, rt, canon, inc, err)
		}
		if tc.diag != "" && (len(l.diags) != 1 || codes(l.diags)[0] != tc.diag) {
			t.Errorf("%s: %v", tc.name, codes(l.diags))
		}
	}

	// log = 0: the error is returned, nothing is logged
	l := &logger{set: f.set}
	if _, _, _, err := l.valueValidate3(f.ll, "x", types.FormatJSON, nil, types.HintData, nil, nil, false); err == nil ||
		len(l.diags) != 0 {
		t.Errorf("no log: %v %v", err, codes(l.diags))
	}
}

// TestValueCompare: lyd_value_compare — equal canonical values, a different value, a rejected
// value logged at the node.
func TestValueCompare(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	v, d := types.Store(f.u8, "7", types.FormatJSON, types.HintData, nil, f.ll)
	if d != nil {
		t.Fatal(d)
	}
	n := newTerm(f.ll, v)
	tr.insert(c, n, insertDefault)
	l := &logger{set: f.set}
	for val, want := range map[string]bool{"7": true, "+07": true, "8": false} {
		if eq, err := l.valueCompare(n, val); eq != want || err != nil {
			t.Errorf("%s: %v %v", val, eq, err)
		}
	}
	if _, err := l.valueCompare(n, "256"); err == nil || len(l.diags) != 1 ||
		codes(l.diags)[0] != `LY_EVALID LYVE_DATA /b:c/ll[.='7']: Value "256" is out of type uint8 min/max bounds.` {
		t.Errorf("rejected: %v %v", err, codes(l.diags))
	}
}
