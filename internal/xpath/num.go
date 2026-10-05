// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (BSD-3-Clause, © CESNET).

package xpath

import (
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// ld is a C long double of the canonical oracle host (amd64): x87 extended,
// 64-bit mantissa, 15-bit exponent. libyang keeps every XPath number in one,
// so numbers are parsed, computed and printed in that precision; only Result
// narrows to float64, as lyoracle does. The zero value is +0.
type ld struct {
	nan bool
	f   *big.Float // nil = +0; never mutated once built
}

const (
	ldPrec   = 64
	ldMaxExp = 16384  // |x| < 2^16384 (big.Float exponent: x = m·2^exp, 0.5 ≤ m < 1)
	ldMinExp = -16444 // smallest denormal 2^-16445
)

var ldNaN = ld{nan: true}

func newF() *big.Float { return new(big.Float).SetPrec(ldPrec).SetMode(big.ToNearestEven) }

// ldRound rounds f to the x87 format: overflow → ±Inf, underflow → ±0.
// ponytail: denormals keep 64 bits of precision instead of fewer.
func ldRound(f *big.Float) ld {
	if f.IsInf() || f.Sign() == 0 {
		return ld{f: f}
	}
	switch exp := f.MantExp(nil); {
	case exp > ldMaxExp:
		return ld{f: newF().SetInf(f.Signbit())}
	case exp < ldMinExp:
		z := newF()
		if f.Signbit() {
			z.Neg(z)
		}
		return ld{f: z}
	}
	return ld{f: f}
}

func ldInt(i int64) ld { return ld{f: newF().SetInt64(i)} }

func ldFloat(f float64) ld {
	if math.IsNaN(f) {
		return ldNaN
	}
	return ld{f: newF().SetFloat64(f)}
}

func (x ld) big() *big.Float {
	if x.f == nil {
		return newF()
	}
	return x.f
}

func (x ld) isNaN() bool  { return x.nan }
func (x ld) isInf() bool  { return !x.nan && x.f != nil && x.f.IsInf() }
func (x ld) isZero() bool { return !x.nan && (x.f == nil || x.f.Sign() == 0) }
func (x ld) sign() int {
	if x.nan || x.f == nil {
		return 0
	}
	return x.f.Sign()
}

// float narrows to double, as lyoracle prints results.
func (x ld) float() float64 {
	if x.nan {
		return math.NaN()
	}
	f, _ := x.big().Float64()
	return f
}

// cmp is the C comparison; ok=false when either is NaN (every relation false).
func (x ld) cmp(y ld) (c int, ok bool) {
	if x.nan || y.nan {
		return 0, false
	}
	return x.big().Cmp(y.big()), true
}

func (x ld) eq(y ld) bool { c, ok := x.cmp(y); return ok && c == 0 }

func ldNeg(x ld) ld {
	if x.nan {
		return x
	}
	return ld{f: newF().Neg(x.big())}
}

// ldOp is moveto_op_math in long double.
func ldOp(op string, x, y ld) ld {
	if x.nan || y.nan {
		return ldNaN
	}
	a, b := x.big(), y.big()
	switch op {
	case "+", "-":
		if op == "-" {
			b = newF().Neg(b)
		}
		if a.IsInf() && b.IsInf() && a.Signbit() != b.Signbit() {
			return ldNaN
		}
		return ldRound(newF().Add(a, b))
	case "*":
		if a.IsInf() && b.Sign() == 0 || b.IsInf() && a.Sign() == 0 {
			return ldNaN
		}
		return ldRound(newF().Mul(a, b))
	case "div":
		if a.Sign() == 0 && b.Sign() == 0 || a.IsInf() && b.IsInf() {
			return ldNaN
		}
		return ldRound(newF().Quo(a, b))
	}
	return ldMod(a, b)
}

// ldMod is fmodl: exact, with the sign of x.
func ldMod(a, b *big.Float) ld {
	switch {
	case a.IsInf() || b.Sign() == 0:
		return ldNaN
	case b.IsInf() || a.Sign() == 0:
		return ld{f: a}
	}
	ma, mb := new(big.Int), new(big.Int)
	ea := a.MantExp(nil) - ldPrec
	eb := b.MantExp(nil) - ldPrec
	new(big.Float).SetMantExp(new(big.Float).Abs(a), ldPrec-a.MantExp(nil)).Int(ma) // |a| = ma·2^ea
	new(big.Float).SetMantExp(new(big.Float).Abs(b), ldPrec-b.MantExp(nil)).Int(mb)
	e := min(ea, eb)
	ma.Lsh(ma, uint(ea-e))
	mb.Lsh(mb, uint(eb-e))
	r := newF().SetInt(ma.Rem(ma, mb))
	r.SetMantExp(r, e)
	if a.Signbit() {
		r.Neg(r)
	}
	return ld{f: r}
}

// parseLD is strtold on a whole (pre-checked) number text; erange as errno.
func parseLD(s string) (x ld, erange bool) {
	l := strings.ToLower(strings.TrimLeft(s, "+-"))
	neg := strings.HasPrefix(s, "-")
	switch l {
	case "inf", "infinity":
		return ld{f: newF().SetInf(neg)}, false
	case "nan":
		return ldNaN, false
	}
	if strings.HasPrefix(l, "0x") && !strings.Contains(l, "p") {
		s += "p0"
	}
	f, _, err := big.ParseFloat(s, 0, ldPrec, big.ToNearestEven)
	if err != nil {
		return ldNaN, true
	}
	if f.Sign() != 0 { // strtold: ERANGE beyond the range and for denormal results
		if exp := f.MantExp(nil); exp > ldMaxExp || exp < -16381 {
			return ldNaN, true
		}
	}
	return ld{f: f}, false
}

// parseNumberToken is eval_number: the Number token in long double.
func parseNumberToken(s string) ld {
	x, _ := parseLD(s)
	return x
}

// cStrtod is cast_string_to_number: C strtold over the whole string, NaN unless
// it consumes everything (leading white space, sign, exponent, hex floats,
// inf/infinity and nan are accepted, as by strtold) or on ERANGE.
func cStrtod(s string) ld {
	t := strings.TrimLeft(s, " \t\n\v\f\r")
	if strings.Contains(t, "_") { // Go accepts digit separators, C does not
		return ldNaN
	}
	g := t
	if l := strings.ToLower(strings.TrimLeft(t, "+-")); strings.HasPrefix(l, "0x") && !strings.Contains(l, "p") {
		g += "p0"
	}
	if _, err := strconv.ParseFloat(g, 64); err != nil && !errors.Is(err, strconv.ErrRange) {
		return ldNaN // not a complete C number (strconv checks the syntax)
	}
	x, erange := parseLD(t)
	if erange {
		return ldNaN
	}
	return x
}

// ctrunc is the C (long long) conversion. Out of range is UB in C; we pin the
// canonical oracle host (amd64, x87 fistp): NaN and out of range give the
// "integer indefinite" math.MinInt64 (D-0011).
func ctrunc(x ld) int64 {
	if !x.fitsInt64() {
		return math.MinInt64
	}
	i, _ := x.big().Int64() // truncates toward zero
	return i
}

func (x ld) fitsInt64() bool {
	return !x.nan && !x.isInf() && x.big().Cmp(big.NewFloat(-(1<<63))) >= 0 && x.big().Cmp(big.NewFloat(1<<63)) < 0
}

// isIntVal is (long long)x == x.
func (x ld) isIntVal() bool {
	return x.fitsInt64() && x.big().IsInt()
}

// numToString is the NUMBER → STRING branch of lyxp_set_cast: integers in
// %lld, everything else in %03.1Lf (libyang: one decimal, 1.25 → "1.2").
func numToString(x ld) string {
	switch {
	case x.nan:
		return "NaN"
	case x.isZero():
		return "0"
	case x.isInf() && x.sign() > 0:
		return "Infinity"
	case x.isInf():
		return "-Infinity"
	case x.isIntVal():
		return strconv.FormatInt(ctrunc(x), 10)
	}
	return x.big().Text('f', 1)
}

// cfmt is printf("%0*Lf", width, x): zero-padded for numbers, space-padded inf/nan.
func cfmt(x ld, width int) string {
	var s string
	switch {
	case x.nan:
		s = "nan"
	case x.isInf():
		s = "inf"
	default:
		s = new(big.Float).Abs(x.big()).Text('f', 6)
	}
	sign := ""
	if !x.nan && x.big().Signbit() {
		sign = "-"
	}
	if pad := width - len(sign) - len(s); pad > 0 {
		if x.nan || x.isInf() {
			return strings.Repeat(" ", pad) + sign + s
		}
		return sign + strings.Repeat("0", pad) + s
	}
	return sign + s
}

// numCmp is moveto_num_cmp: libyang compares the "%Lf" (6 decimals) renderings,
// so numbers equal to 6 decimal places compare equal and NaN sorts as text.
func numCmp(a, b ld) int {
	if a.isZero() {
		a = ld{} // avoid -0
	}
	if b.isZero() {
		b = ld{}
	}
	neg := false
	switch sa, sb := a.sign(), b.sign(); {
	case sa < 0 && sb > 0:
		return -1
	case sa < 0 && sb < 0:
		neg, a, b = true, ldNeg(a), ldNeg(b)
	case sa > 0 && sb < 0:
		return 1
	}
	var s1, s2 string
	if c, ok := a.cmp(b); ok && c > 0 {
		s1 = cfmt(a, 0)
		s2 = cfmt(b, len(s1))
	} else {
		s2 = cfmt(b, 0)
		s1 = cfmt(a, len(s2))
	}
	c := strings.Compare(s1, s2)
	if neg {
		c = -c
	}
	return c
}
