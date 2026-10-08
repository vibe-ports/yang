// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/bits.c (BSD-3-Clause, © CESNET).

package types

import (
	"math"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
)

// storeBits ports lyplg_type_store_bits (bits_str2bitmap, bits_bitmap2items, bits_items2canon).
func storeBits(a *storeArgs) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	set := map[uint32]bool{} // the bitmap, kept sparse: positions go up to 4294967295
	for i := 0; i < len(a.lex); {
		for i < len(a.lex) && cIsSpace(a.lex[i]) {
			i++
		}
		if i == len(a.lex) {
			break
		}
		start := i
		for i < len(a.lex) && !cIsSpace(a.lex[i]) {
			i++
		}
		name := a.lex[start:i]
		var bit *schema.Bit
		for _, b := range a.t.Bits {
			if b.Name == name {
				bit = b
				break
			}
		}
		if bit == nil {
			return Value{}, errf("Invalid bit \"%s\".", name)
		}
		if set[bit.Position] {
			return Value{}, errf("Duplicate bit \"%s\".", bit.Name)
		}
		set[bit.Position] = true
	}
	v := Value{typ: a.t}
	names := make([]string, 0, len(a.t.Bits))
	for _, b := range a.t.Bits { // in position order
		if set[b.Position] {
			v.bits = append(v.bits, b)
			names = append(names, b.Name)
		}
	}
	v.canon = strings.Join(names, " ")
	if a.f == FormatCanon {
		v.canon = a.lex
	}
	return v, nil
}

// compareBits ports lyplg_type_sort_bits, the memcmp of two little-endian bitmaps (byte i holds
// positions 8i..8i+7, bit p%8 of it), over the set bits in position order: the first byte that
// differs decides, a byte with no set bit being 0.
func compareBits(a, b []*schema.Bit) int {
	for i, j := 0, 0; i < len(a) || j < len(b); {
		var ba, bb byte
		idx := uint32(math.MaxUint32 / 8)
		if i < len(a) {
			idx = a[i].Position / 8
		}
		if j < len(b) {
			idx = min(idx, b[j].Position/8)
		}
		for ; i < len(a) && a[i].Position/8 == idx; i++ {
			ba |= 1 << (a[i].Position % 8)
		}
		for ; j < len(b) && b[j].Position/8 == idx; j++ {
			bb |= 1 << (b[j].Position % 8)
		}
		if ba != bb {
			return cmp3(ba < bb, true)
		}
	}
	return 0
}
