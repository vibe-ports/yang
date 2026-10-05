// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/integer.c, src/plugins_types.c and
// src/ly_common.c (ly_parse_int, ly_parse_uint) (BSD-3-Clause, © CESNET).

package types

import (
	"math"
	"strconv"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
)

// intBounds are the value spaces of the integer types (RFC 7950 §9.2).
var intBounds = map[schema.BaseType][2]int64{
	schema.Int8:  {math.MinInt8, math.MaxInt8},
	schema.Int16: {math.MinInt16, math.MaxInt16},
	schema.Int32: {math.MinInt32, math.MaxInt32},
	schema.Int64: {math.MinInt64, math.MaxInt64},
}

var uintMax = map[schema.BaseType]uint64{
	schema.Uint8:  math.MaxUint8,
	schema.Uint16: math.MaxUint16,
	schema.Uint32: math.MaxUint32,
	schema.Uint64: math.MaxUint64,
}

// storeInt ports lyplg_type_store_int + lyplg_type_validate_value_int.
func storeInt(a *storeArgs) (Value, *Diag) {
	base, d := checkHints(a.h, a.lex, a.t.Base)
	if d != nil {
		return Value{}, d
	}
	bounds := intBounds[a.t.Base]
	n, d := parseInt(a.t.Base.String(), base, bounds[0], bounds[1], a.lex)
	if d != nil {
		return Value{}, d
	}
	v := Value{typ: a.t, i: n, canon: strconv.FormatInt(n, 10)}
	if a.f == FormatCanon {
		v.canon = a.lex
	}
	if !a.only && a.t.Range != nil {
		if d := checkRange(a.t.Base, a.t.Range, n, v.canon); d != nil {
			return Value{}, d
		}
	}
	return v, nil
}

// storeUint ports lyplg_type_store_uint + lyplg_type_validate_value_uint.
func storeUint(a *storeArgs) (Value, *Diag) {
	base, d := checkHints(a.h, a.lex, a.t.Base)
	if d != nil {
		return Value{}, d
	}
	n, d := parseUint(a.t.Base.String(), base, uintMax[a.t.Base], a.lex)
	if d != nil {
		return Value{}, d
	}
	v := Value{typ: a.t, u: n, canon: strconv.FormatUint(n, 10)}
	if a.f == FormatCanon {
		v.canon = a.lex
	}
	if !a.only && a.t.Range != nil {
		if d := checkRange(a.t.Base, a.t.Range, int64(n), v.canon); d != nil { //nolint:gosec // reinterpreted as uint64 by checkRange
			return Value{}, d
		}
	}
	return v, nil
}

// numPrefix strips leading C whitespace and cuts at the first NUL (the C code sees a C string).
func numPrefix(s string) string {
	s = trimCSpace(s)
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return s
}

// parseInt ports lyplg_type_parse_int / ly_parse_int.
func parseInt(datatype string, base int, minV, maxV int64, lex string) (int64, *Diag) {
	s := numPrefix(lex)
	if s == "" {
		return 0, errf("Invalid type %s empty value.", datatype)
	}
	mag, neg, end, ok := cStrtou(s, base)
	var n int64
	if ok { // strtoll's ERANGE
		switch {
		case neg && mag > 1<<63:
			ok = false
		case !neg && mag > math.MaxInt64:
			ok = false
		case neg && mag == 1<<63:
			n = math.MinInt64
		case neg:
			n = -int64(mag) //nolint:gosec // mag < 2^63 checked above
		default:
			n = int64(mag) //nolint:gosec // mag <= MaxInt64 checked above
		}
	}
	switch {
	case !ok || end == 0:
		return 0, errf("Invalid type %s value \"%s\".", datatype, s)
	case n < minV || n > maxV:
		return 0, errf("Value \"%s\" is out of type %s min/max bounds.", s, datatype)
	case !onlySpace(s[end:]):
		return 0, errf("Invalid type %s value \"%s\".", datatype, s)
	}
	return n, nil
}

// parseUint ports lyplg_type_parse_uint / ly_parse_uint.
func parseUint(datatype string, base int, maxV uint64, lex string) (uint64, *Diag) {
	s := numPrefix(lex)
	if s == "" {
		return 0, errf("Invalid type %s empty value.", datatype)
	}
	mag, neg, end, ok := cStrtou(s, base)
	n := mag
	if neg {
		n = -mag // strtoull negates in the unsigned type
	}
	switch {
	case !ok || end == 0:
		return 0, errf("Invalid type %s value \"%s\".", datatype, s)
	case n > maxV || (n != 0 && s[0] == '-'):
		return 0, errf("Value \"%s\" is out of type %s min/max bounds.", s, datatype)
	case !onlySpace(s[end:]):
		return 0, errf("Invalid type %s value \"%s\".", datatype, s)
	}
	return n, nil
}

func onlySpace(s string) bool {
	for i := 0; i < len(s); i++ {
		if !cIsSpace(s[i]) {
			return false
		}
	}
	return true
}

// cStrtou mirrors the parsing part of C strtoull/strtoll on s (no leading space, no NUL): an
// optional sign, then digits of base (0 = "0x" hex, "0" octal, else decimal). It returns the
// magnitude, the sign, the end of the number (0 = nothing parsed) and false on uint64 overflow.
func cStrtou(s string, base int) (mag uint64, neg bool, end int, ok bool) {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	hexPrefix := i+2 < len(s) && s[i] == '0' && (s[i+1] == 'x' || s[i+1] == 'X') && digitVal(s[i+2]) < 16
	switch {
	case (base == 0 || base == 16) && hexPrefix:
		base = 16
		i += 2
	case base == 0 && i < len(s) && s[i] == '0':
		base = 8
	case base == 0:
		base = 10
	}
	start := i
	ok = true
	for ; i < len(s); i++ {
		d := digitVal(s[i])
		if d >= base {
			break
		}
		if mag > (math.MaxUint64-uint64(d))/uint64(base) { //nolint:gosec // d, base are small
			ok = false
		}
		mag = mag*uint64(base) + uint64(d) //nolint:gosec // d is small
	}
	if i == start {
		return 0, false, 0, true
	}
	return mag, neg, i, ok
}

func digitVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10
	}
	return 99
}
