// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types.c (BSD-3-Clause, © CESNET).

package types

import (
	"bytes"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
)

// Value is a stored value (libyang struct lyd_value). The zero Value is "no value".
type Value struct {
	typ   *schema.Type // realtype: the type that stored it (a leafref stores as its target type)
	canon string
	i     int64  // int8..int64, decimal64 scaled by 10^fraction-digits, boolean 0/1
	u     uint64 // uint8..uint64
	enum  *schema.Enum
	bits  []*schema.Bit // set bits in position order
	bmap  []byte        // bits bitmap, byte i holds positions 8i..8i+7 (libyang little-endian layout)
	bin   []byte
}

// Type returns the type that stored the value (libyang realtype).
func (v Value) Type() *schema.Type { return v.typ }

// Canonical returns the canonical lexical form.
func (v Value) Canonical() string { return v.canon }

// String returns the canonical lexical form.
func (v Value) String() string { return v.canon }

// Int returns an int8..int64 value or the boolean as 0/1.
func (v Value) Int() int64 { return v.i }

// Uint returns a uint8..uint64 value.
func (v Value) Uint() uint64 { return v.u }

// Dec64 returns a decimal64 value as an integer scaled by 10^FracDigits of its type.
func (v Value) Dec64() int64 { return v.i }

// Bool returns a boolean value.
func (v Value) Bool() bool { return v.i != 0 }

// Enum returns an enumeration value's item.
func (v Value) Enum() *schema.Enum { return v.enum }

// Bits returns the set bits of a bits value in position order.
func (v Value) Bits() []*schema.Bit { return v.bits }

// Bytes returns a binary value's decoded bytes.
func (v Value) Bytes() []byte { return v.bin }

// Equal reports whether two values of the same type are equal (libyang compare callbacks).
// Values stored by different types are never equal.
func Equal(a, b Value) bool {
	if a.typ != b.typ || a.typ == nil {
		return a.typ == b.typ
	}
	switch a.typ.Base {
	case schema.Int8, schema.Int16, schema.Int32, schema.Int64, schema.Dec64, schema.Bool:
		return a.i == b.i
	case schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64:
		return a.u == b.u
	case schema.Bits:
		return bytes.Equal(a.bmap, b.bmap)
	case schema.Binary:
		return bytes.Equal(a.bin, b.bin)
	}
	return a.canon == b.canon // lyplg_type_compare_simple
}

// Compare orders two values of the same type like libyang's sort callbacks (user-ordered lists
// excluded): negative, zero or positive.
func Compare(a, b Value) int {
	if a.typ == nil || b.typ == nil || a.typ.Base != b.typ.Base {
		return strings.Compare(a.canon, b.canon)
	}
	switch a.typ.Base {
	case schema.Int8, schema.Int16, schema.Int32, schema.Int64, schema.Dec64, schema.Bool:
		return cmp3(a.i < b.i, a.i > b.i)
	case schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64:
		return cmp3(a.u < b.u, a.u > b.u)
	case schema.Enumeration:
		return cmp3(a.enum.Value < b.enum.Value, a.enum.Value > b.enum.Value)
	case schema.Bits:
		return bytes.Compare(a.bmap, b.bmap) // lyplg_type_sort_bits: memcmp of the bitmaps
	case schema.Binary:
		if len(a.bin) != len(b.bin) {
			return cmp3(len(a.bin) < len(b.bin), true)
		}
		return bytes.Compare(a.bin, b.bin)
	}
	return strings.Compare(a.canon, b.canon) // lyplg_type_sort_simple
}

func cmp3(less, greater bool) int {
	switch {
	case less:
		return -1
	case greater:
		return 1
	}
	return 0
}
