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

// CrossContextChange is lyd_change_term_val with a value stored in another context, as
// lyd_merge_sibling_r passes it (tree_data.c:2696-2701): the compare callback of the destination's
// realtype, then, for a different value, the duplicate callback of the destination's schema type
// (lyd_change_node_value). It is ported only for leaf types with identical definitions in the two
// contexts (defWalk); for any other pair libyang compares through the destination's callback
// over the source's storage, which ranges from defined but layout-dependent to undefined, and the
// result is CrossDefinition (D-0133).
//
// Two contexts share no dictionary strings and their compiled types, identities and union members
// are distinct pointers until a duplicate keeps the source's (lyplg_type_dup_union keeps the
// member realtype, so a value merged back compares its member again). lyplg_type_compare_simple
// (canonical pointers, plugins_types.c:170-181), lyplg_type_compare_int (realtypes) and
// lyplg_type_compare_identityref (identities) compare pointers, the zoned inet addresses their
// zone dictionary pointers; lyplg_type_dup_simple's lydict_dup finds no foreign string
// (dict.c:341-365): LY_ENOTFOUND, dupOK false. The other duplicate callbacks insert their strings.
// A leafref duplicates with its source realtype's callback (lyplg_type_dup_leafref), a union with
// its source member's (lyplg_type_dup_union).
//
// defs memoizes the definition comparison across the calls of one operation (one Merge), so a
// typedef shared by many leaves is compared once; nil compares afresh. charge, if not nil, is
// charged for the work of every type pair compared for the first time; its error is returned as is.
func CrossContextChange(defs *DefWalk, dstType, srcType *schema.Type, dst, src Value, charge func(uint64) error) (
	equal, dupOK bool, undefined CrossUndefined, err error) {
	if dst.typ == nil || src.typ == nil {
		return false, false, CrossDefinition, nil
	}
	if defs == nil {
		defs = &DefWalk{}
	}
	defs.charge = charge
	if same := defs.same(dstType, srcType); defs.err != nil || !same {
		return false, false, CrossDefinition, defs.err
	}
	if equal = crossCompare(dst, src); equal {
		return true, false, 0, nil
	}
	dupType := dst.typ
	if dstType.Base == schema.Leafref || dstType.Base == schema.Union {
		dupType = src.leaf().typ
	}
	if dupOK = dupInserts(dupType); dupOK && pathPredicates(src.leaf()) {
		return false, true, CrossPredicate, nil
	}
	return false, dupOK, 0, nil
}

// pathPredicates reports whether v is an instance-identifier or node-instance-identifier whose
// path has a key or leaf-list predicate. ly_path_dup_predicates copies their stored canonical
// values with lydict_dup and ignores its LY_ENOTFOUND (path.c:944), so a copy into another
// context keeps a NULL value, which the next print dereferences.
func pathPredicates(v Value) bool {
	p := v.path
	if n, ok := v.ext.(*nodeInstanceID); ok {
		p = n.path
	}
	for _, seg := range p {
		for _, pr := range seg.Preds {
			if pr.Kind == PredKey || pr.Kind == PredLeafList {
				return true
			}
		}
	}
	return false
}

// RebindContext is the context half of the duplicate callback that copies v into a value of the
// context set, as lyd_change_node_value does for a value of another context. lyplg_type_dup_xpath10
// (xpath1.0, instance-identifier-keys) sets the destination context (dup_val->ctx = ctx), so the
// module-name prefixes of a JSON or canonical value and the namespaces of an XML value (prefix - URI
// pairs) resolve there from now on; schema prefix data is copied with its module pointers
// (lyplg_type_prefix_data_dup). lyplg_type_dup_union
// copies its prefix data the same way and duplicates the member with the member's callback.
// instance-identifier and node-instance-identifier copy their compiled path (ly_path_dup), so its
// schema nodes stay the source context's, as here; identityref duplicates with
// lyplg_type_dup_simple, which fails across contexts before any copy.
func (v Value) RebindContext(set *schema.Set) Value {
	if k, ok := v.ext.(*keysValue); ok {
		rebound := *k
		rebound.pc = rebindPrefixes(k.pc, set)
		v.ext = &rebound
	}
	if v.union != nil {
		u := *v.union
		u.member = u.member.RebindContext(set)
		u.pc = rebindPrefixes(u.pc, set)
		v.union = &u
	}
	return v
}

// rebindPrefixes is prefix data resolved in the context set: module names (JSON) and XML
// namespaces are looked up there, schema prefix data keeps its module pointers.
func rebindPrefixes(pc PrefixCtx, set *schema.Set) PrefixCtx {
	switch p := pc.(type) {
	case ModuleNames:
		return ModuleNames{Set: set}
	case XMLNamespaces:
		p.Set = set
		return p
	}
	return pc
}

// CrossUndefined is why CrossContextChange does not follow libyang (D-0133), 0 when it does.
type CrossUndefined uint8

const (
	// CrossDefinition means the leaf type definitions differ between the two contexts.
	CrossDefinition CrossUndefined = iota + 1
	// CrossPredicate means the different value is an instance-identifier or
	// node-instance-identifier path with key or leaf-list predicates (pathPredicates).
	CrossPredicate
)

// crossCompare is the destination realtype's compare callback across contexts, for identical
// definitions.
func crossCompare(dst, src Value) bool {
	if p := pluginFor(dst.typ); p != nil {
		switch p.id {
		case "hex-string", "time-period", "xpath1.0", "node-instance-identifier", "instance-identifier-keys":
			return false // lyplg_type_compare_simple
		case "ipv4-address", "ipv6-address": // the zones are distinct dictionary pointers
			x, y := dst.ext.(*ipValue), src.ext.(*ipValue)
			return bytes.Equal(x.addr, y.addr) && !x.hasZ && !y.hasZ
		case "ipv4-address-no-zone", "ipv6-address-no-zone":
			return bytes.Equal(dst.ext.(*ipValue).addr, src.ext.(*ipValue).addr)
		}
		return p.equal(dst, src) // prefixes, date-and-time, date, time: the stored values
	}
	switch dst.typ.Base {
	case schema.Int8, schema.Int16, schema.Int32, schema.Int64:
		return dst.typ == src.typ && dst.i == src.i // lyplg_type_compare_int checks the realtypes
	case schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64:
		return dst.u == src.u
	case schema.Dec64, schema.Bool:
		return dst.i == src.i
	case schema.Bits:
		return slices.EqualFunc(dst.bits, src.bits, func(a, b *schema.Bit) bool { return a.Position == b.Position })
	case schema.Binary:
		return bytes.Equal(dst.bin, src.bin)
	case schema.IdentityRef:
		return dst.ident == src.ident
	case schema.Union:
		// lyplg_type_compare_union: the member realtypes, then the member's own callback
		return dst.union != nil && src.union != nil && dst.union.member.typ == src.union.member.typ &&
			crossCompare(dst.union.member, src.union.member)
	}
	return false // string, enumeration, empty, instance-identifier: lyplg_type_compare_simple
}

// DefWalk compares two compiled leaf types, normally of two contexts, for structural identity:
// the same base, type handler and its name-keyed storage variant, restrictions, enum and bit
// assignments, fraction digits, identity bases, union members (recursively), leafref target
// realtype and require-instance. Union members and leafref realtypes are shared subgraphs, so
// every compared pair is memoized for the walk's lifetime: it is linear in the two type graphs,
// and each new pair is charged for everything its comparison scans (defCost) before it is
// scanned. A pair under comparison counts as identical when it is reached again (a cycle); that
// cannot make the result true wrongly, as every pair is a conjunct of its parent's.
type DefWalk struct {
	seen   map[[2]*schema.Type]bool
	charge func(uint64) error
	err    error
}

func (d *DefWalk) same(a, b *schema.Type) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil || d.err != nil {
		return false
	}
	key := [2]*schema.Type{a, b}
	if r, ok := d.seen[key]; ok {
		return r
	}
	if d.charge != nil {
		if err := d.charge(defCost(a) + defCost(b)); err != nil {
			d.err = err
			return false
		}
	}
	if d.seen == nil {
		d.seen = map[[2]*schema.Type]bool{}
	}
	d.seen[key] = true
	r := a.Base == b.Base && a.FracDigits == b.FracDigits &&
		a.RequireInstance == b.RequireInstance && Plugin(a) == Plugin(b) && storageVariant(a) == storageVariant(b) &&
		sameRange(a.Range, b.Range) && sameRange(a.Length, b.Length) &&
		slices.EqualFunc(a.Patterns, b.Patterns, func(x, y *schema.Pattern) bool {
			return x.Expr == y.Expr && x.Invert == y.Invert
		}) &&
		slices.EqualFunc(a.Enums, b.Enums, func(x, y *schema.Enum) bool { return x.Name == y.Name && x.Value == y.Value }) &&
		slices.EqualFunc(a.Bits, b.Bits, func(x, y *schema.Bit) bool { return x.Name == y.Name && x.Position == y.Position }) &&
		slices.EqualFunc(a.Bases, b.Bases, func(x, y *schema.Identity) bool {
			return x.Name == y.Name && x.Module.Name == y.Module.Name
		}) &&
		slices.EqualFunc(a.Union, b.Union, d.same) &&
		(a.Base != schema.Leafref || d.same(a.Realtype, b.Realtype))
	d.seen[key] = r && d.err == nil
	return d.seen[key]
}

// storageVariant is the part of a type's name its handler keys its storage on: date.c and time.c
// store the zoned form only for a type named "date" or "time" (strcmp(type->name, ...)), the
// prefix handlers zero host bits only for "ipv4-prefix" and "ipv6-prefix". A typedef derived from
// one of them with the same restrictions keeps the handler but not that name.
func storageVariant(t *schema.Type) string {
	if p := pluginFor(t); p != nil {
		switch p.id {
		case "date", "time", "ipv4-address-prefix", "ipv6-address-prefix":
			if t.Typedef == "date" || t.Typedef == "time" || t.Typedef == "ipv4-prefix" || t.Typedef == "ipv6-prefix" {
				return t.Typedef
			}
			return "-"
		}
	}
	return ""
}

// defCost is the work DefWalk.same does for one side of a new pair: a unit per scanned range and
// length interval, union member, pattern, enum, bit and base, plus their compared strings, and
// the derivation chain Plugin and storageVariant walk.
func defCost(t *schema.Type) uint64 {
	n := 1 + uint64(len(t.Union))
	if t.Range != nil {
		n += uint64(len(t.Range.Parts))
	}
	if t.Length != nil {
		n += uint64(len(t.Length.Parts))
	}
	for _, p := range t.Patterns {
		n += 1 + uint64(len(p.Expr))
	}
	for _, e := range t.Enums {
		n += 1 + uint64(len(e.Name))
	}
	for _, b := range t.Bits {
		n += 1 + uint64(len(b.Name))
	}
	for _, i := range t.Bases {
		n += 1 + uint64(len(i.Name)) + uint64(len(i.Module.Name))
	}
	for c := t; c != nil; c = c.From {
		n += 2
	}
	return n
}

func sameRange(a, b *schema.Range) bool {
	if a == nil || b == nil {
		return a == b
	}
	return slices.Equal(a.Parts, b.Parts)
}

// dupInserts reports whether t's duplicate callback inserts its strings (not lyplg_type_dup_simple).
func dupInserts(t *schema.Type) bool {
	if p := pluginFor(t); p != nil {
		switch p.id {
		case "hex-string", "time-period", "ipv4-address-no-zone":
			return false
		}
		return true
	}
	switch t.Base {
	case schema.Binary, schema.Bits, schema.InstanceID, schema.Union:
		return true
	}
	return false
}

// CompareCost bounds the work the first value's sort callback does comparing a with b, from what
// it reads: the shorter canonical string for the canonical sorts (and for values of different
// bases, which Compare orders canonically), the shorter binary, both set-bit lists, a plugin's
// fixed struct plus the shorter canonical text (zone, fractions), a union's member scan plus
// its member's comparison; a fixed-size scalar or an enumeration costs one unit.
func CompareCost(a, b Value) uint64 {
	short := uint64(min(len(a.canon), len(b.canon)))
	if a.typ == nil || b.typ == nil || a.typ.Base != b.typ.Base {
		return 1 + short
	}
	switch sortCallback(a.typ) {
	case "simple", "time-period":
		return 1 + short
	}
	if a.union != nil {
		n := 1 + uint64(len(a.typ.Union))
		if b.union != nil && a.union.member.typ == b.union.member.typ {
			n += CompareCost(a.union.member, b.union.member)
		}
		return n
	}
	if a.ext != nil {
		return 17 + short // an inet address, a timestamp; zone and fraction text
	}
	switch a.typ.Base {
	case schema.Binary:
		return 1 + uint64(min(len(a.bin), len(b.bin)))
	case schema.Bits:
		return 1 + uint64(len(a.bits)+len(b.bits))
	case schema.IdentityRef:
		return 1 + short
	}
	return 1
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
// to that callback (libyang v5.8.6 src/tree_data_sorted.c:195-199,212-222), so what is defined
// depends on what that sort callback reads of the second value (src/plugins_types):
//   - only the canonical string (lyd_value_get_canonical): lyplg_type_sort_simple (string,
//     empty, instance-identifier, hex-string, xpath1.0, node-instance-identifier,
//     instance-identifier-keys) and lyplg_type_sort_time_period. Defined for any second value;
//     time-period indexes before an empty string, so that one pair is refused;
//   - a specific storage layout: the integer, boolean and decimal64 union fields (deterministic
//     for built-in scalar storage, which is zeroed first), and bits (bitmap of val1's width).
//     Defined when the second value's storage provides that layout;
//   - a plugin's own fixed-memory struct (LYD_VALUE_GET): binary, date, time, date-and-time and
//     the inet addresses. Defined for the same store callback, and against a second value whose
//     storage is all zero (string, empty, time-period, hex-string: their store callbacks zero the value
//     first), which the callback reads as the zero struct;
//   - a pointer it dereferences: enumeration, identityref and union. Defined only for the same
//     store callback; any other second value is indeterminate and refused (D-0121).
func CompareCompatible(a, b Value) (order int, compatible bool) {
	if a.typ == nil || b.typ == nil {
		return 0, false
	}
	switch sortCallback(a.typ) {
	case "simple":
		return strings.Compare(a.canon, b.canon), true
	case "time-period":
		if a.canon == "" || b.canon == "" {
			return 0, false
		}
		return compareTimePeriod(a, b), true
	}
	if storeCallback(a.typ) != storeCallback(b.typ) && zeroStorage(b) {
		if z, ok := zeroStructValue(a); ok {
			if p := pluginFor(a.typ); p != nil {
				return p.compare(a, z), true
			}
			return Compare(a, z), true
		}
	}
	if storeCallback(a.typ) != storeCallback(b.typ) &&
		(!plainBuiltInScalarStorage(a.typ) || !plainBuiltInScalarStorage(b.typ)) {
		return 0, false
	}
	switch sortCallback(a.typ) {
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
	base, builtIn := storageBase(t)
	if !builtIn {
		// date-and-time's two revisions have distinct store callbacks writing one
		// lyd_value_date_and_time representation, sorted by one callback: one class
		return "plugin:" + pluginFor(t).id
	}
	switch base {
	case schema.Int8, schema.Int16, schema.Int32, schema.Int64:
		return "int"
	case schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64:
		return "uint"
	default:
		return "builtin:" + base.String()
	}
}

// zeroStorage reports a value whose whole lyd_value union is zero: the string and empty store
// callbacks and the canonical-only plugin ones (plugin.canonOnly) write only the canonical form,
// whatever the callback's identity.
func zeroStorage(v Value) bool {
	return zeroStorageType(v.typ)
}

func zeroStorageType(t *schema.Type) bool {
	if t == nil {
		return false
	}
	if p := pluginFor(t); p != nil {
		return p.canonOnly
	}
	return t.Base == schema.String || t.Base == schema.Empty
}

// zeroStructValue is what a's sort callback reads from all-zero storage through LYD_VALUE_GET:
// its fixed-memory struct zeroed (a zero address without zone, the epoch without fractions, an
// empty binary). false: a's callback dereferences a pointer there instead.
func zeroStructValue(a Value) (Value, bool) {
	switch x := a.ext.(type) {
	case *ipValue:
		return Value{typ: a.typ, ext: &ipValue{addr: make([]byte, len(x.addr))}}, true
	case *dateVal:
		return Value{typ: a.typ, ext: &dateVal{}}, true
	case *timeVal:
		return Value{typ: a.typ, ext: &timeVal{}}, true
	case *dateTime:
		return Value{typ: a.typ, ext: &dateTime{}}, true
	}
	if base, builtIn := storageBase(a.typ); builtIn && base == schema.Binary && a.union == nil {
		return Value{typ: a.typ}, true // lyd_value_binary {NULL, 0}: sizes compare first
	}
	return Value{}, false
}

// storageBase is the built-in type whose store callback writes t's values: t's own base, or
// string for a plugin registered with lyplg_type_store_string. false: a plugin's own storage.
func storageBase(t *schema.Type) (schema.BaseType, bool) {
	p := pluginFor(t)
	switch {
	case p == nil:
		return t.Base, true
	case p.stringStore:
		return schema.String, true
	default:
		return 0, false
	}
}

// plainBuiltInScalarStorage is the set whose union bytes can be proved without modelling a
// plugin's fixed_mem or dyn_mem. Enumeration is deliberately absent: its enum_item is a pointer,
// but same-callback enumeration comparisons remain supported above.
func plainBuiltInScalarStorage(t *schema.Type) bool {
	if t == nil {
		return false
	}
	if zeroStorageType(t) {
		return true // an all-zero union: string, empty or a canonical-only plugin
	}
	base, builtIn := storageBase(t)
	if !builtIn {
		return false
	}
	switch base {
	case schema.Int8, schema.Int16, schema.Int32, schema.Int64,
		schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64,
		schema.Bool, schema.Dec64:
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
	if v.typ == nil {
		return 0, false
	}
	if zeroStorageType(v.typ) {
		return 0, true
	}
	base, builtIn := storageBase(v.typ)
	if !builtIn {
		return 0, false
	}
	switch base {
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
