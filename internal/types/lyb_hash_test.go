// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

func TestHashLYBLongUnionSelectionCached(t *testing.T) {
	typ := &schema.Type{Base: schema.Union, Union: []*schema.Type{
		{Base: schema.Uint32}, {Base: schema.String},
	}}
	v, d := StoreOnly(typ, strings.Repeat("0", 1<<20)+"1", FormatXML, HintData, nil, nil)
	if d != nil {
		t.Fatal(d.Msg)
	}
	if v.union.noTree == nil {
		t.Fatal("stored union has no immutable no-tree selection")
	}
	if allocations := testing.AllocsPerRun(100, func() { _ = HashLYBWorkBound(v) }); allocations != 0 {
		t.Fatalf("hash preflight reparsed the long lexical union: %.1f allocations", allocations)
	}
}

// union.c:633-666 reselects from original input with no data tree. A validated
// string member must not supply index 1 when the original also fits leafref 0.
func TestHashLYBUnionReselection(t *testing.T) {
	text := &schema.Type{Base: schema.String}
	typ := &schema.Type{Base: schema.Union, Union: []*schema.Type{
		{Base: schema.Leafref, Realtype: text, RequireInstance: true}, text,
	}}
	v, d := StoreOnly(typ, "missing", FormatJSON, HintData, nil, nil)
	if d != nil {
		t.Fatal(d.Msg)
	}
	member, d := StoreOnly(text, "missing", FormatJSON, HintData, nil, nil)
	if d != nil {
		t.Fatal(d.Msg)
	}
	u := *v.union
	u.member, u.index = member, 1 // successful tree validation selected the fallback
	v.union = &u
	const seed = uint32(0x12345678)
	want := HashMulti32(seed, []byte("\x00missing"))
	if got, ok := HashLYB(seed, v); !ok || got != want {
		t.Fatalf("hash = %#x, %v; want leafref member %#x", got, ok, want)
	}
	if _, index := v.union.Member(); index != 1 {
		t.Fatal("hashing modified the stored value")
	}
	// The index is still present for an empty member; it prevents finalizing an
	// empty payload at this inner boundary (hash_table.c:58-61).
	v, d = StoreOnly(typ, "", FormatJSON, HintData, nil, nil)
	if d != nil {
		t.Fatal(d.Msg)
	}
	if got, ok := HashLYB(seed, v); !ok || got != HashMulti32(seed, []byte{0}) {
		t.Fatalf("empty union hash = %#x, %v", got, ok)
	}
}

// Mutation callers install the reselected member; probes discard it. The outer
// canonical cache is retained by lyb_union_print even when JSON changes type.
func TestHashLYBUnionReturnedMember(t *testing.T) {
	text := &schema.Type{Base: schema.String}
	typ := &schema.Type{Base: schema.Union, Union: []*schema.Type{
		{Base: schema.Leafref, Realtype: &schema.Type{Base: schema.Int32}, RequireInstance: true}, text,
	}}
	v, diag := StoreOnly(typ, "01", FormatXML, HintData, nil, nil)
	if diag != nil {
		t.Fatal(diag.Msg)
	}
	member, diag := StoreOnly(text, "01", FormatXML, HintData, nil, nil)
	if diag != nil {
		t.Fatal(diag.Msg)
	}
	u := *v.union
	u.member, u.index = member, 1
	v.union, v.canon, v.needsTree = &u, "01", false
	for _, nested := range []bool{false, true} {
		input := v
		if nested {
			outer := &schema.Type{Base: schema.Union, Union: []*schema.Type{typ}}
			input = Value{typ: outer, canon: "01", union: &UnionValue{
				member: v, orig: "01", f: FormatXML, h: HintData,
			}}
		}
		_, selected, ok, _ := HashLYBWork(0, input)
		if !ok || selected.leaf().Type().Base != schema.Int32 || selected.Int() != 1 {
			t.Fatalf("nested=%t: reselected member = %#v, ok=%t", nested, selected.leaf(), ok)
		}
		if selected.Canonical() != "01" || selected.NeedsTree() {
			t.Fatalf("nested=%t: hash replaced canonical cache or validation state", nested)
		}
		if input.leaf().Type().Base != schema.String || input.Canonical() != "01" {
			t.Fatalf("nested=%t: hash modified its immutable input", nested)
		}
	}
}

func TestHashLYBSparseBitmap(t *testing.T) {
	typ := &schema.Type{Base: schema.Bits, Bits: []*schema.Bit{
		{Name: "low", Position: 9}, {Name: "high", Position: 65537*8 + 7},
	}}
	v, d := StoreOnly(typ, "low high", FormatJSON, HintData, nil, nil)
	if d != nil {
		t.Fatal(d.Msg)
	}
	bitmap := make([]byte, 65538)
	bitmap[1], bitmap[65537] = 2, 128
	const seed = uint32(0x12345678)
	if got, ok := HashLYB(seed, v); !ok || got != HashMulti32(seed, bitmap) {
		t.Fatalf("sparse hash = %#x, %v; want %#x", got, ok, HashMulti32(seed, bitmap))
	}
}
