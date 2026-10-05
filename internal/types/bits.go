// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/bits.c (BSD-3-Clause, © CESNET).

package types

import (
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
)

// storeBits ports lyplg_type_store_bits (bits_str2bitmap, bits_bitmap2items, bits_items2canon).
func storeBits(a *storeArgs) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	var last uint32
	if n := len(a.t.Bits); n > 0 {
		last = a.t.Bits[n-1].Position
	}
	bmap := make([]byte, last/8+1)
	isSet := func(p uint32) bool { return bmap[p/8]&(1<<(p%8)) != 0 }
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
		if isSet(bit.Position) {
			return Value{}, errf("Duplicate bit \"%s\".", bit.Name)
		}
		bmap[bit.Position/8] |= 1 << (bit.Position % 8)
	}
	v := Value{typ: a.t, bmap: bmap}
	names := make([]string, 0, len(a.t.Bits))
	for _, b := range a.t.Bits { // in position order
		if isSet(b.Position) {
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
