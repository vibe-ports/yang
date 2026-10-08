// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types.c (BSD-3-Clause, © CESNET).

package types

import (
	"bytes"
	"slices"
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
	bin   []byte
	ident *schema.Identity
	path  Path
	union *UnionValue
	ext   any // type-specific storage of an ietf-* plugin (ipValue, dateTime)

	needsTree bool // leafref/instance-identifier with require-instance: ValidateTree pending
}

// leaf is the value that holds the data: the selected member for unions (recursively).
func (v Value) leaf() Value {
	for v.union != nil {
		v = v.union.member
	}
	return v
}

// Type returns the type that stored the value (libyang realtype).
func (v Value) Type() *schema.Type { return v.typ }

// Canonical returns the canonical lexical form.
func (v Value) Canonical() string { return v.canon }

// String returns the canonical lexical form.
func (v Value) String() string { return v.canon }

// Int returns an int8..int64 value or the boolean as 0/1.
func (v Value) Int() int64 { return v.leaf().i }

// Uint returns a uint8..uint64 value.
func (v Value) Uint() uint64 { return v.leaf().u }

// Dec64 returns a decimal64 value as an integer scaled by 10^FracDigits of its type.
func (v Value) Dec64() int64 { return v.leaf().i }

// Bool returns a boolean value.
func (v Value) Bool() bool { return v.leaf().i != 0 }

// Enum returns an enumeration value's item.
func (v Value) Enum() *schema.Enum { return v.leaf().enum }

// Bits returns the set bits of a bits value in position order.
func (v Value) Bits() []*schema.Bit { return slices.Clone(v.leaf().bits) }

// Bytes returns a binary value's decoded bytes.
func (v Value) Bytes() []byte { return slices.Clone(v.leaf().bin) }

// Ident returns an identityref value's identity.
func (v Value) Ident() *schema.Identity { return v.leaf().ident }

// Path returns an instance-identifier value's compiled target path.
func (v Value) Path() Path { return v.leaf().path.clone() }

// Union returns the union details of a union value, nil otherwise. Typed getters of a union
// value already answer for the selected member.
func (v Value) Union() *UnionValue { return v.union }

// NeedsTree reports whether the value still needs ValidateTree (libyang LY_EINCOMPLETE):
// a require-instance leafref or instance-identifier, also as the selected union member.
func (v Value) NeedsTree() bool { return v.needsTree }

// Equal reports whether two values of the same type are equal (libyang compare callbacks).
// Values stored by different types are never equal: libyang's callers check the realtype first
// (lyd_compare_single, tree_data.c:1736; lyplg_type_compare_int, integer.c:217;
// lyplg_type_compare_union, union.c:578; lyplg_type_resolve_leafref, plugins_types.c:1039), so
// only lyplg_type_compare_simple on its own would compare canonical strings across types.
func Equal(a, b Value) bool {
	if a.typ != b.typ || a.typ == nil {
		return a.typ == b.typ
	}
	if p := pluginFor(a.typ); p != nil {
		if p.equal == nil {
			return a.canon == b.canon
		}
		return p.equal(a, b)
	}
	switch a.typ.Base {
	case schema.Int8, schema.Int16, schema.Int32, schema.Int64, schema.Dec64, schema.Bool:
		return a.i == b.i
	case schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64:
		return a.u == b.u
	case schema.Bits:
		return compareBits(a.bits, b.bits) == 0 // lyplg_type_compare_bits: memcmp of the bitmaps
	case schema.Binary:
		return bytes.Equal(a.bin, b.bin)
	case schema.IdentityRef:
		return a.ident == b.ident
	case schema.Union: // lyplg_type_compare_union
		return Equal(a.union.member, b.union.member)
	}
	return a.canon == b.canon // lyplg_type_compare_simple
}

// Compare orders two values of the same type like libyang's sort callbacks (user-ordered lists
// excluded): negative, zero or positive. Values of different base types order by canonical
// string; a union orders its members as lyplg_type_sort_union (union.c).
func Compare(a, b Value) int {
	if a.typ == nil || b.typ == nil || a.typ.Base != b.typ.Base {
		return strings.Compare(a.canon, b.canon)
	}
	if p := pluginFor(a.typ); p != nil && a.typ == b.typ {
		if p.compare == nil {
			return strings.Compare(a.canon, b.canon)
		}
		return p.compare(a, b)
	}
	switch a.typ.Base {
	case schema.Int8, schema.Int16, schema.Int32, schema.Int64, schema.Dec64, schema.Bool:
		return cmp3(a.i < b.i, a.i > b.i)
	case schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64:
		return cmp3(a.u < b.u, a.u > b.u)
	case schema.Enumeration:
		return cmp3(a.enum.Value < b.enum.Value, a.enum.Value > b.enum.Value)
	case schema.Bits:
		return compareBits(a.bits, b.bits) // lyplg_type_sort_bits
	case schema.Binary:
		if len(a.bin) != len(b.bin) {
			return cmp3(len(a.bin) < len(b.bin), true)
		}
		return bytes.Compare(a.bin, b.bin)
	case schema.IdentityRef:
		return strings.Compare(a.ident.Name, b.ident.Name) // lyplg_type_sort_identityref
	case schema.Union:
		return compareUnion(a, b)
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
