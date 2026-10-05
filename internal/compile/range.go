// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile_node.c (lys_compile_type_range and its
// range_part_* helpers) and src/ly_common.c (ly_parse_int, ly_parse_uint) (BSD-3-Clause, © CESNET).

package compile

import (
	"math"
	"strconv"
	"strings"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// rpart is a range part as libyang's lysc_range_part union: unsigned bounds are stored as their
// two's-complement bit pattern, and libyang compares them both ways.
type rpart struct{ lo, hi int64 }

// unsignedBase reports whether ranges of b are unsigned (binary, uint*, string length).
func unsignedBase(b schema.BaseType) bool { return b <= schema.String }

func toParts(r *schema.Range, b schema.BaseType) []rpart {
	if r == nil {
		return nil
	}
	ps := make([]rpart, len(r.Parts))
	for i, p := range r.Parts {
		if unsignedBase(b) {
			ps[i] = rpart{int64(p.MinU), int64(p.MaxU)} //nolint:gosec // bit pattern, as the C union
		} else {
			ps[i] = rpart{p.Min, p.Max}
		}
	}
	return ps
}

func fromParts(ps []rpart, b schema.BaseType) []schema.RangePart {
	out := make([]schema.RangePart, len(ps))
	for i, p := range ps {
		if unsignedBase(b) {
			out[i] = schema.RangePart{MinU: uint64(p.lo), MaxU: uint64(p.hi)} //nolint:gosec // bit pattern
		} else {
			out[i] = schema.RangePart{Min: p.lo, Max: p.hi}
		}
	}
	return out
}

// libyang return codes of the range helpers.
const (
	rcValid  = "LY_EVALID"
	rcInval  = "LY_EINVAL"
	rcExist  = "LY_EEXIST"
	rcDenied = "LY_EDENIED"
)

// cIsSpace is C isspace in the "C" locale.
func cIsSpace(c byte) bool { return c == ' ' || (c >= '\t' && c <= '\r') }

func cIsDigit(c byte) bool { return c >= '0' && c <= '9' }

// rangeErr is a failed range part with libyang's return code; the message is logged by the
// caller (range_part_minmax).
type rangeErr struct {
	rc  string
	msg *vErr // already logged message (decimal64 fraction digits), nil otherwise
}

// rangeValueSyntax ports range_part_check_value_syntax: the length of the number at the start of
// v and its integer text (decimal64 scaled by 10^fd).
func rangeValueSyntax(b schema.BaseType, fd uint8, v string) (n int, valcopy string, err *rangeErr) {
	at := func(i int) byte {
		if i < len(v) {
			return v[i]
		}
		return 0
	}
	if !cIsDigit(at(0)) && at(0) != '-' && at(0) != '+' {
		return 0, "", &rangeErr{rc: rcValid}
	}
	if at(0) == '-' || at(0) == '+' {
		n++
	}
	for cIsDigit(at(n)) {
		n++
	}
	fraction := 0
	if b == schema.Dec64 && at(n) == '.' && cIsDigit(at(n+1)) {
		fraction = n
		n++
		for cIsDigit(at(n)) {
			n++
		}
	} else if b != schema.Dec64 {
		return n, v[:n], nil
	}
	fdi := int(fd)
	if fraction != 0 && n-1-fraction > fdi {
		return 0, "", &rangeErr{rc: rcInval, msg: &vErr{Err: rcValid, Code: ly.SyntaxYang, Msg: "Range boundary \"" + v[:n] +
			"\" of decimal64 type exceeds defined number (" + strconv.Itoa(fdi) + ") of fraction digits."}}
	}
	if fraction != 0 {
		return n, v[:fraction] + v[fraction+1:n] + strings.Repeat("0", fdi-(n-1-fraction)), nil
	}
	return n, v[:n] + strings.Repeat("0", fdi), nil
}

// parseRangeInt ports ly_parse_int over the decimal text s ([+-]digits): "" when it fits
// [minV, maxV], else LY_EVALID (no number, strtoll overflow) or LY_EDENIED (out of bounds).
func parseRangeInt(s string, minV, maxV int64) (int64, string) {
	v, err := strconv.ParseInt(s, 10, 64)
	switch {
	case err != nil:
		return 0, rcValid
	case v < minV || v > maxV:
		return 0, rcDenied
	}
	return v, ""
}

// parseRangeUint ports ly_parse_uint (strtoull: a minus sign negates in the unsigned type).
func parseRangeUint(s string, maxV uint64) (uint64, string) {
	neg := strings.HasPrefix(s, "-")
	u, err := strconv.ParseUint(strings.TrimLeft(s, "+-"), 10, 64)
	if err != nil || len(s)-len(strings.TrimLeft(s, "+-")) > 1 {
		return 0, rcValid
	}
	if neg {
		u = -u
	}
	if u > maxV || (u != 0 && neg) {
		return 0, rcDenied
	}
	return u, ""
}

// typeLimits are the bounds of the built-in types that take a range or length.
func typeLimits(b schema.BaseType) (minV, maxV int64, umax uint64) {
	switch b {
	case schema.Int8:
		return math.MinInt8, math.MaxInt8, 0
	case schema.Int16:
		return math.MinInt16, math.MaxInt16, 0
	case schema.Int32:
		return math.MinInt32, math.MaxInt32, 0
	case schema.Int64, schema.Dec64:
		return math.MinInt64, math.MaxInt64, 0
	case schema.Uint8:
		return 0, 0, math.MaxUint8
	case schema.Uint16:
		return 0, 0, math.MaxUint16
	case schema.Uint32:
		return 0, 0, math.MaxUint32
	}
	return 0, 0, math.MaxUint64 // uint64, string, binary
}

// ascending ports range_part_check_ascendancy: min bounds must be strictly above the previous
// value, max bounds not below it.
func ascending(unsigned, isMax bool, v, prev int64) bool {
	switch {
	case unsigned && isMax:
		return uint64(prev) <= uint64(v) //nolint:gosec // C union
	case unsigned:
		return uint64(prev) < uint64(v) //nolint:gosec // C union
	case isMax:
		return prev <= v
	}
	return prev < v
}

// skipSpace drops leading C isspace characters.
func skipSpace(s string) string {
	for s != "" && cIsSpace(s[0]) {
		s = s[1:]
	}
	return s
}

// rangeBound ports range_part_minmax: one bound of a part, parsed from *value (advanced past the
// number on success) or, for min/max, taken from base or the type limits.
func rangeBound(part *rpart, isMax bool, prev int64, b schema.BaseType, first, length bool, fd uint8,
	base []rpart, value *string) error {
	var valcopy string
	var rc string
	n := 0
	hasCopy := false
	if value != nil {
		var e *rangeErr
		n, valcopy, e = rangeValueSyntax(b, fd, *value)
		if e != nil {
			if e.msg != nil {
				return &vErr{Err: e.rc, Code: e.msg.Code, Msg: e.msg.Msg}
			}
			rc = e.rc
		} else {
			hasCopy = true
		}
	}
	set := func(v int64) {
		if isMax {
			part.hi = v
		} else {
			part.lo = v
		}
	}
	cur := func() int64 {
		if isMax {
			return part.hi
		}
		return part.lo
	}
	switch {
	case rc != "":
	case !hasCopy && base != nil:
		if isMax {
			part.hi = base[len(base)-1].hi
		} else {
			part.lo = base[0].lo
		}
		if !first && !ascending(b <= schema.String, isMax, cur(), prev) {
			rc = rcExist
		}
	default:
		minV, maxV, umax := typeLimits(b)
		unsigned := unsignedBase(b)
		switch {
		case hasCopy && unsigned:
			var u uint64
			u, rc = parseRangeUint(valcopy, umax)
			set(int64(u)) //nolint:gosec // bit pattern, as the C union
		case hasCopy:
			var v int64
			v, rc = parseRangeInt(valcopy, minV, maxV)
			set(v)
		case unsigned && isMax:
			set(int64(umax)) //nolint:gosec // bit pattern, as the C union
		case unsigned:
			set(0)
		case isMax:
			set(maxV)
		default:
			set(minV)
		}
		if rc == "" && !first && !ascending(unsigned, isMax, cur(), prev) {
			rc = rcExist
		}
	}
	what := "range"
	if length {
		what = "length"
	}
	shown := valcopy
	if !hasCopy && value != nil {
		shown = *value
	}
	switch rc {
	case rcDenied:
		return &vErr{Err: rc, Code: ly.SyntaxYang, Msg: "Invalid " + what + " restriction - value \"" + shown + "\" does not fit the type limitations."}
	case rcValid:
		return &vErr{Err: rc, Code: ly.SyntaxYang, Msg: "Invalid " + what + " restriction - invalid value \"" + shown + "\"."}
	case rcExist:
		switch {
		case hasCopy && b != schema.Dec64:
		case value != nil:
			shown = *value
		case isMax:
			shown = "max"
		default:
			shown = "min"
		}
		return &vErr{Err: rc, Code: ly.SyntaxYang, Msg: "Invalid " + what + " restriction - values are not in ascending order (" + shown + ")."}
	}
	if value != nil {
		*value = (*value)[n:]
	}
	return nil
}

// compileRange ports lys_compile_type_range: parse a range or length expression of base type b,
// check it is equally or more limiting than base (the inherited restriction, nil for none) and
// take the error-message/app-tag of r.
func compileRange(r *parser.Restr, b schema.BaseType, length bool, fd uint8, baseRange *schema.Range) (*schema.Range, error) {
	what := "range"
	if length {
		what = "length"
	}
	syntax := func(format string, a ...any) error { return verr(ly.SyntaxYang, format, a...) }
	base := toParts(baseRange, b)
	var parts []rpart
	partsDone := 0
	rangeExpected := false
	expr := r.Arg
	for {
		switch {
		case expr != "" && cIsSpace(expr[0]):
			expr = expr[1:]
			continue
		case expr == "":
			if rangeExpected {
				return nil, syntax("Invalid %s restriction - unexpected end of the expression after \"..\" (%s).", what, r.Arg)
			}
			if len(parts) == 0 || partsDone == len(parts) {
				return nil, syntax("Invalid %s restriction - unexpected end of the expression (%s).", what, r.Arg)
			}
			partsDone++
		case strings.HasPrefix(expr, "min"):
			if len(parts) != 0 {
				return nil, syntax("Invalid %s restriction - unexpected data before min keyword (%s).", what,
					r.Arg[:len(r.Arg)-len(expr)])
			}
			expr = expr[3:]
			parts = append(parts, rpart{})
			p := &parts[len(parts)-1]
			if err := rangeBound(p, false, 0, b, true, length, fd, base, nil); err != nil {
				return nil, err
			}
			p.hi = p.lo
			continue
		case expr[0] == '|':
			if len(parts) == 0 || rangeExpected {
				return nil, syntax("Invalid %s restriction - unexpected beginning of the expression (%s).", what, expr)
			}
			expr = expr[1:]
			partsDone++
			continue
		case strings.HasPrefix(expr, ".."):
			expr = skipSpace(expr[2:])
			if len(parts) == 0 || len(parts) == partsDone {
				return nil, syntax("Invalid %s restriction - unexpected \"..\" without a lower bound.", what)
			}
			rangeExpected = true
			continue
		case cIsDigit(expr[0]) || expr[0] == '-' || expr[0] == '+':
			if rangeExpected {
				p := &parts[len(parts)-1]
				if err := rangeBound(p, true, p.lo, b, false, length, fd, nil, &expr); err != nil {
					return nil, err
				}
				rangeExpected = false
			} else {
				var prev int64
				if partsDone != 0 {
					prev = parts[len(parts)-1].hi
				}
				parts = append(parts, rpart{})
				p := &parts[len(parts)-1]
				if err := rangeBound(p, false, prev, b, partsDone == 0, length, fd, nil, &expr); err != nil {
					return nil, err
				}
				p.hi = p.lo
			}
			continue
		case strings.HasPrefix(expr, "max"):
			expr = skipSpace(expr[3:])
			if expr != "" {
				return nil, syntax("Invalid %s restriction - unexpected data after max keyword (%s).", what, expr)
			}
			if rangeExpected {
				p := &parts[len(parts)-1]
				if err := rangeBound(p, true, p.lo, b, false, length, fd, base, nil); err != nil {
					return nil, err
				}
				rangeExpected = false
			} else {
				var prev int64
				if partsDone != 0 {
					prev = parts[len(parts)-1].hi
				}
				parts = append(parts, rpart{})
				p := &parts[len(parts)-1]
				if err := rangeBound(p, true, prev, b, partsDone == 0, length, fd, base, nil); err != nil {
					return nil, err
				}
				p.lo = p.hi
			}
			continue
		default:
			return nil, syntax("Invalid %s restriction - unexpected data (%s).", what, expr)
		}
		break
	}
	if base != nil && !moreLimiting(parts[:partsDone], base, unsignedBase(b)) {
		return nil, syntax("Invalid %s restriction - the derived restriction (%s) is not equally or more limiting.", what, r.Arg)
	}
	return &schema.Range{Parts: fromParts(parts, b), Msg: r.ErrorMessage, AppTag: r.ErrorAppTag}, nil
}

// moreLimiting is the base check of lys_compile_type_range: every part must lie in the base.
func moreLimiting(parts, base []rpart, uns bool) bool {
	lt := func(a, b int64) bool {
		if uns {
			return uint64(a) < uint64(b) //nolint:gosec // C union
		}
		return a < b
	}
	u, v := 0, 0
	for u < len(parts) && v < len(base) {
		p, bp := parts[u], base[v]
		if lt(p.lo, bp.lo) {
			return false
		}
		switch {
		case bp.lo == bp.hi: // base has a single value
			if bp.lo == p.lo {
				if p.lo != p.hi {
					return false
				}
				u++
			}
			v++
		case p.lo == p.hi: // current is a single value
			if lt(bp.hi, p.hi) {
				v++ // current is behind the base part
			} else {
				u++
			}
		case lt(bp.hi, p.hi):
			if !lt(bp.hi, p.lo) {
				return false // starts within the base part but ends behind it
			}
			v++
		default:
			u++
		}
	}
	return u == len(parts)
}
