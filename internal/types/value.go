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

// sortCallback identifies the plugin .sort callback, not its type record. Several distinct
// realtypes share a callback: all signed widths share lyplg_type_sort_int, all unsigned widths
// share lyplg_type_sort_uint, and the simple lexical types share lyplg_type_sort_simple
// (libyang v5.8.6 src/plugins_types/integer.c:603-708, string.c:164, empty.c:109 and
// instanceid.c:335). Leafrefs already store their target realtype.
func sortCallback(t *schema.Type) string {
	if t == nil {
		return ""
	}
	if p := pluginFor(t); p != nil {
		if p.compare == nil {
			return "simple"
		}
		return p.id
	}
	switch t.Base {
	case schema.Int8, schema.Int16, schema.Int32, schema.Int64:
		return "int"
	case schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64:
		return "uint"
	case schema.String, schema.Empty, schema.InstanceID:
		return "simple"
	default:
		return t.Base.String()
	}
}

// CompareCompatible invokes the first value's callback for values compiled in distinct schema
// contexts. lyds_compare_single() does not check the second realtype before passing its storage
// to that callback (libyang v5.8.6 src/tree_data_sorted.c:195-199,212-222). Values stored by the
// same callback retain their ordinary comparison. Across distinct store callbacks, only built-in
// scalar union storage is interpreted; plugin and pointer/fixed-buffer storage is refused.
func CompareCompatible(a, b Value) (order int, compatible bool) {
	if a.typ == nil || b.typ == nil {
		return 0, false
	}
	if storeCallback(a.typ) != storeCallback(b.typ) &&
		(!plainBuiltInScalarStorage(a.typ) || !plainBuiltInScalarStorage(b.typ)) {
		return 0, false
	}
	switch sortCallback(a.typ) {
	case "simple":
		return strings.Compare(a.canon, b.canon), true
	case "int":
		x, okX := integerSortStorage(a, a)
		y, okY := integerSortStorage(a, b)
		if !okX || !okY {
			return 0, false
		}
		return cmp3(x < y, x > y), true
	case "uint":
		x, okX := unsignedSortStorage(a, a)
		y, okY := unsignedSortStorage(a, b)
		if !okX || !okY {
			return 0, false
		}
		return cmp3(x < y, x > y), true
	}
	switch a.typ.Base {
	case schema.Bool:
		x, okX := scalarUnionStorage(a)
		y, okY := scalarUnionStorage(b)
		if !okX || !okY {
			return 0, false
		}
		x8, y8 := int8(x), int8(y) //nolint:gosec // the C callback reads the union's signed low byte
		return cmp3(x8 < y8, x8 > y8), true
	case schema.Dec64:
		x, okX := scalarUnionStorage(a)
		y, okY := scalarUnionStorage(b)
		if !okX || !okY {
			return 0, false
		}
		x64, y64 := int64(x), int64(y) //nolint:gosec // the C callback reads the union's int64 field
		return cmp3(x64 < y64, x64 > y64), true
	case schema.Bits:
		if b.typ.Base != schema.Bits || len(a.typ.Bits) == 0 || len(b.typ.Bits) == 0 {
			return 0, false
		}
		// lyplg_type_sort_bits uses val1's declaration width for memcmp. Reading that
		// many bytes from a shorter foreign bitmap is undefined; otherwise compare the
		// same full-byte prefix, including val2 bits above val1's last declared bit.
		firstBytes := uint64(a.typ.Bits[len(a.typ.Bits)-1].Position)/8 + 1
		secondBytes := uint64(b.typ.Bits[len(b.typ.Bits)-1].Position)/8 + 1
		if firstBytes > secondBytes {
			return 0, false
		}
		return compareBitsPrefix(a.bits, b.bits, firstBytes), true
	}
	if !SortCompatible(a, b) {
		return 0, false
	}
	if p := pluginFor(a.typ); p != nil {
		return p.compare(a, b), true
	}
	return Compare(a, b), true
}

// storeCallback identifies the callback that populated struct lyd_value. Leafrefs have already
// selected their target realtype before a Value is returned. Plugin IDs identify the shared
// handler records registered for standard-module typedefs.
func storeCallback(t *schema.Type) string {
	if t == nil {
		return ""
	}
	if p := pluginFor(t); p != nil {
		return "plugin:" + p.id
	}
	switch t.Base {
	case schema.Int8, schema.Int16, schema.Int32, schema.Int64:
		return "int"
	case schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64:
		return "uint"
	default:
		return "builtin:" + t.Base.String()
	}
}

// plainBuiltInScalarStorage is the set whose union bytes can be proved without modelling a
// plugin's fixed_mem or dyn_mem. Enumeration is deliberately absent: its enum_item is a pointer,
// but same-callback enumeration comparisons remain supported above.
func plainBuiltInScalarStorage(t *schema.Type) bool {
	if t == nil || pluginFor(t) != nil {
		return false
	}
	switch t.Base {
	case schema.Int8, schema.Int16, schema.Int32, schema.Int64,
		schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64,
		schema.Bool, schema.Dec64, schema.String, schema.Empty:
		return true
	default:
		return false
	}
}

// SortCompatible reports whether two realtypes use the same libyang sort callback.
func SortCompatible(a, b Value) bool {
	return a.typ != nil && b.typ != nil && sortCallback(a.typ) == sortCallback(b.typ)
}

// scalarUnionStorage returns the first eight bytes of struct lyd_value's union when deterministic
// on the canonical linux/amd64 oracle. Every store callback first zeroes the structure. Narrow
// scalars then write only their low bytes; string and empty leave the whole union zero. Pointer-
// backed and type-specific fixed-memory values are refused.
func scalarUnionStorage(v Value) (uint64, bool) {
	if v.typ == nil || pluginFor(v.typ) != nil {
		return 0, false
	}
	switch v.typ.Base {
	case schema.Int8, schema.Int16, schema.Int32, schema.Int64,
		schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64:
		return integerLYB(v), true
	case schema.Bool:
		return uint64(v.i), true //nolint:gosec // stored boolean is exactly 0 or 1
	case schema.Dec64:
		return uint64(v.i), true //nolint:gosec // C union contains the two's-complement int64
	case schema.String, schema.Empty:
		return 0, true
	default:
		return 0, false
	}
}

// integerSortStorage reads zero-initialized C union payloads using the first value's field width.
// A narrower second value is not sign-extended before a wider field reads it
// (libyang v5.8.6 src/plugins_types/integer.c:61-62,99-117,249-294; linux/amd64 oracle).
func integerSortStorage(first, v Value) (int64, bool) {
	u, ok := scalarUnionStorage(v)
	if !ok {
		return 0, false
	}
	switch first.typ.Base {
	case schema.Int8:
		return int64(int8(u)), true //nolint:gosec // the C callback reads this union field width
	case schema.Int16:
		return int64(int16(u)), true //nolint:gosec // the C callback reads this union field width
	case schema.Int32:
		return int64(int32(u)), true //nolint:gosec // the C callback reads this union field width
	default:
		return int64(u), true //nolint:gosec // the C callback reads this union field width
	}
}

// unsignedSortStorage selects the first value's C union field width
// (libyang v5.8.6 src/plugins_types/integer.c:472-517).
func unsignedSortStorage(first, v Value) (uint64, bool) {
	u, ok := scalarUnionStorage(v)
	if !ok {
		return 0, false
	}
	switch first.typ.Base {
	case schema.Uint8:
		return uint64(uint8(u)), true //nolint:gosec // the C callback reads this union field width
	case schema.Uint16:
		return uint64(uint16(u)), true //nolint:gosec // the C callback reads this union field width
	case schema.Uint32:
		return uint64(uint32(u)), true //nolint:gosec // the C callback reads this union field width
	default:
		return u, true
	}
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
