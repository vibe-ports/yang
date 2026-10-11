// SPDX-License-Identifier: BSD-3-Clause
// Test vectors derived from libyang v5.8.6 src/hash_table.c (BSD-3-Clause, © CESNET).

package types

import (
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

func TestHashMulti32(t *testing.T) {
	for _, tc := range []struct {
		parts []string
		want  uint32
	}{
		{nil, 0},
		{[]string{"a"}, 0xca2e9442},
		{[]string{"hello"}, 0xc8fd181b},
		{[]string{"he", "llo"}, 0xc8fd181b},
	} {
		var got uint32
		for _, part := range tc.parts {
			got = HashMulti32(got, []byte(part))
		}
		got = FinishHash32(got)
		if got != tc.want {
			t.Fatalf("hash(%q) = %#x, want %#x", tc.parts, got, tc.want)
		}
	}
}

// TestHashMulti32SignedChar pins linux/amd64's signed-char evaluation of key_part[i] in
// libyang's lyht_hash_multi. The constants are the uint32 result after each C loop iteration.
func TestHashMulti32SignedChar(t *testing.T) {
	steps := []struct {
		b    byte
		want uint32
	}{
		{0x80, 0xfc02087e},
		{0xff, 0x0433738c},
		{0x41, 0xd14aad52},
	}

	var got uint32
	for i, step := range steps {
		got = HashMulti32(got, []byte{step.b})
		if got != step.want {
			t.Fatalf("after byte %d (%#02x), hash = %#08x, want signed-char %#08x", i, step.b, got, step.want)
		}
	}
	if got = FinishHash32(got); got != 0x0d9b63e0 {
		t.Fatalf("finished hash = %#08x, want signed-char %#08x", got, uint32(0x0d9b63e0))
	}
}

func TestHasSparseBits(t *testing.T) {
	bitsType := &schema.Type{Base: schema.Bits, Bits: []*schema.Bit{{Name: "high", Position: 4294967295}}}
	unionType := &schema.Type{Base: schema.Union, Union: []*schema.Type{bitsType, {Base: schema.String}}}
	store := func(typ *schema.Type, value string) Value {
		t.Helper()
		v, d := StoreOnly(typ, value, FormatXML, HintData, nil, nil)
		if d != nil {
			t.Fatal(d.Msg)
		}
		return v
	}
	if !HasSparseBits(store(bitsType, "high")) {
		t.Fatal("bits value was not recognized as sparse")
	}
	if !HasSparseBits(store(unionType, "high")) {
		t.Fatal("bits union member was not recognized as sparse")
	}
	if HasSparseBits(store(&schema.Type{Base: schema.String}, "high")) {
		t.Fatal("string value was recognized as sparse")
	}
}

// Empty payloads finalize at the lyd_hash call boundary (tree_data_hash.c:60-74;
// hash_table.c:58-61), not at recursive union member boundaries (union.c:660-666).
func TestHashLYBEmptyPayload(t *testing.T) {
	const seed = uint32(0x12345678)
	values := []Value{
		{typ: &schema.Type{Base: schema.String}},
		{typ: &schema.Type{Base: schema.Binary}},
		{typ: &schema.Type{Base: schema.Empty}},
		{typ: &schema.Type{Base: schema.Bits}},
		{typ: &schema.Type{Base: schema.Uint8}},
		{typ: &schema.Type{Base: schema.Enumeration, Enums: []*schema.Enum{{Name: "zero"}}}, enum: &schema.Enum{Name: "zero"}},
	}
	for _, v := range values {
		h, ok, walked := HashLYBWork(seed, v)
		if !ok || h != FinishHash32(seed) || walked != 0 {
			t.Fatalf("empty %v: hash=%#x ok=%v walked=%d", v.typ.Base, h, ok, walked)
		}
		if !EqualLYB(values[0], v) {
			t.Fatalf("empty %v payload differs from empty string", v.typ.Base)
		}
		u := Value{union: &UnionValue{member: v, index: 3}}
		if h, ok = HashLYB(seed, u); !ok || h != HashMulti32(seed, []byte{3}) {
			t.Fatalf("union of empty %v: hash=%#x ok=%v", v.typ.Base, h, ok)
		}
		nested := Value{union: &UnionValue{member: u, index: 2}}
		if h, ok = HashLYB(seed, nested); !ok || h != HashMulti32(seed, []byte{2, 3}) {
			t.Fatalf("nested union of empty %v: hash=%#x ok=%v", v.typ.Base, h, ok)
		}
		if !EqualLYB(u, Value{union: &UnionValue{member: values[0], index: 3}}) {
			t.Fatal("equal union payloads compare unequal")
		}
	}
	// An empty bits value with declared bits still has a nonempty zero bitmap.
	v := Value{typ: &schema.Type{Base: schema.Bits, Bits: []*schema.Bit{{Name: "bit"}}}}
	if h, ok := HashLYB(seed, v); !ok || h != HashMulti32(seed, []byte{0}) || EqualLYB(v, values[0]) {
		t.Fatalf("one-byte zero bitmap: hash=%#x ok=%v", h, ok)
	}
}

func TestHashSparseBitsDirect(t *testing.T) {
	// Leading, interior and trailing zero runs, including two bits in one byte.
	const last = 65539 * 8
	bits := []*schema.Bit{{Position: 8}, {Position: 15}, {Position: 65538 * 8}}
	r := lybRepresentation{bits: last, setBits: bits, sparseBits: true}
	bitmap := make([]byte, (last+7)/8)
	for _, bit := range bits {
		bitmap[bit.Position/8] |= 1 << (bit.Position % 8)
	}
	const seed = uint32(0xabcdef01)
	before := SparseHashBytes()
	h, walked := hashSparseBits(seed, r)
	if h != HashMulti32(seed, bitmap) || int64(walked) != SparseHashBytes()-before { //nolint:gosec // small test count
		t.Fatalf("sparse hash %#x, walked %d; direct %#x, counter %d", h, walked, HashMulti32(seed, bitmap), SparseHashBytes()-before)
	}
}
