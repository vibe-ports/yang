// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/decimal64.c and src/plugins_types.c
// (lyplg_type_parse_dec64) (BSD-3-Clause, © CESNET).

package types

import (
	"math"
	"strconv"
	"strings"
)

// storeDec64 ports lyplg_type_store_decimal64 + lyplg_type_validate_value_decimal64.
func storeDec64(a *storeArgs) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	n, d := parseDec64(a.t.FracDigits, a.lex)
	if d != nil {
		return Value{}, d
	}
	v := Value{typ: a.t, i: n, canon: dec64String(n, a.t.FracDigits)}
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

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// parseDec64 ports lyplg_type_parse_dec64: the value is rewritten without the decimal point,
// padded to fd fraction digits and parsed as an int64 (so the bounds message quotes that form).
func parseDec64(fd uint8, lex string) (int64, *Diag) {
	value := trimCSpace(lex)
	at := func(i int) byte { // the C code may read the terminating NUL
		if i < len(value) {
			return value[i]
		}
		return 0
	}
	if value == "" {
		return 0, errf("Invalid empty decimal64 value.")
	}
	if !isDigit(value[0]) && value[0] != '-' && value[0] != '+' {
		return 0, errf("Invalid %d. character of decimal64 value \"%s\".", 1, value)
	}
	n := 0
	if value[0] == '-' || value[0] == '+' {
		n++
	}
	for n < len(value) && isDigit(value[n]) {
		n++
	}
	fraction, trailingZeros := 0, 0
	if n >= len(value) || (value[n] == '.' && isDigit(at(n+1))) {
		if n < len(value) {
			fraction = n
			n++
			for n < len(value) && isDigit(value[n]) {
				if value[n] == '0' {
					trailingZeros++
				} else {
					trailingZeros = 0
				}
				n++
			}
			n -= trailingZeros
		}
	}
	if fraction != 0 && n-1-fraction > int(fd) {
		return 0, errf("Value \"%s\" of decimal64 type exceeds defined number (%d) of fraction digits.", value[:n], fd)
	}
	if n+trailingZeros < len(value) {
		u := n + trailingZeros
		for u < len(value) && cIsSpace(value[u]) {
			u++
		}
		if u != len(value) {
			return 0, errf("Invalid %d. character of decimal64 value \"%s\".", u+1, value)
		}
	}
	var digits string
	if fraction != 0 {
		frac := value[fraction+1 : n]
		digits = value[:fraction] + frac + strings.Repeat("0", int(fd)-len(frac))
	} else {
		digits = value[:n] + strings.Repeat("0", int(fd))
	}
	return parseInt("decimal64", 10, math.MinInt64, math.MaxInt64, digits)
}

// dec64String ports decimal64_num2str: at least one digit on each side of the point, no
// trailing zeros in the fraction.
func dec64String(n int64, fd uint8) string {
	if n == 0 {
		return "0.0"
	}
	mag := uint64(n) //nolint:gosec // two's complement magnitude below
	sign := ""
	if n < 0 {
		mag, sign = -mag, "-"
	}
	s := strconv.FormatUint(mag, 10)
	if len(s) <= int(fd) {
		s = strings.Repeat("0", int(fd)-len(s)+1) + s
	}
	ip, frac := s[:len(s)-int(fd)], strings.TrimRight(s[len(s)-int(fd):], "0")
	if frac == "" {
		frac = "0"
	}
	return sign + ip + "." + frac
}
