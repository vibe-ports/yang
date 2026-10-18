// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"reflect"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

func TestOpaqueHintNames(t *testing.T) {
	// All named LYD_VALHINT_* and LYD_NODEHINT_* bits, in hints_json order.
	n := newOpaque(opaque{Hints: 0x70ff, Format: types.FormatJSON})
	want := []string{"string", "decnum", "octnum", "hexnum", "num64", "boolean", "empty", "string_datatypes", "list", "leaflist", "container"}
	got, ok := n.OpaqueInfo()
	if !ok || !reflect.DeepEqual(got.Hints, want) {
		t.Fatalf("hints = %v, %v", got.Hints, ok)
	}
	n.opaq.Hints = 0x80000000 // unnamed bits are not emitted by the oracle
	got, _ = n.OpaqueInfo()
	if len(got.Hints) != 0 {
		t.Fatal(got.Hints)
	}
	if n.Attrs() != nil {
		t.Fatal("no attributes should return nil")
	}
}

func TestUnionMemberUnmatched(t *testing.T) {
	tp := &schema.Type{Base: schema.Union, Union: []*schema.Type{{Base: schema.String}}}
	v, d := types.Store(tp, "value", types.FormatJSON, types.HintString, nil, nil)
	if d != nil {
		t.Fatal(d)
	}
	n := newTerm(&schema.Node{Kind: schema.Leaf, Type: tp, Module: &schema.Module{Name: "m"}}, v)
	// This cannot normally happen after compilation; exercise the oracle's fallback
	// by removing the declared member after the value has stored its realtype.
	tp.Union = nil
	m, ok := n.UnionMember()
	if !ok || m.Index != -1 || m.Type.Base() != "string" || m.RealType.Base() != "string" {
		t.Fatalf("unmatched member = %+v, %v", m, ok)
	}
}
