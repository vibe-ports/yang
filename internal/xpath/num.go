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

// libyang computes in C long double; we use float64. ponytail: results differ
// only beyond float64 precision/range (|x| > 1.8e308, > 15 significant digits).

// cStrtod is cast_string_to_number: C strtold over the whole string, NaN unless
// it consumes everything (leading white space, sign, exponent, hex floats,
// inf/infinity and nan are accepted, as by strtold).
func cStrtod(s string) float64 {
	t := strings.TrimLeft(s, " \t\n\v\f\r")
	if strings.Contains(t, "_") { // Go accepts digit separators, C does not
		return math.NaN()
	}
	if l := strings.ToLower(strings.TrimLeft(t, "+-")); strings.HasPrefix(l, "0x") && !strings.Contains(l, "p") {
		t += "p0" // Go requires the binary exponent
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return math.NaN()
	}
	if (err != nil || math.Abs(f) < 0x1p-1022) && !inLongDouble(t) {
		return math.NaN() // strtold ERANGE
	}
	return f // in long double range but not in float64: ±Inf / 0 (D-0010)
}

// inLongDouble: s (valid, outside float64) is a normal x87 80-bit value, the
// canonical oracle host's long double; strtold sets ERANGE otherwise.
func inLongDouble(s string) bool {
	x, _, err := big.ParseFloat(s, 0, 64, big.ToNearestEven)
	if err != nil {
		return false
	}
	exp := x.MantExp(nil)
	return x.Sign() == 0 || exp > -16381 && exp <= 16384
}

// numToString is the NUMBER → STRING branch of lyxp_set_cast: integers in
// %lld, everything else in %03.1Lf (libyang: one decimal, 1.25 → "1.2").
func numToString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case f == 0:
		return "0"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f >= -(1<<63) && f < 1<<63 && f == math.Trunc(f):
		return strconv.FormatInt(int64(f), 10)
	}
	return x87(f).Text('f', 1)
}

// ctrunc is the C (long long) conversion. Out of range is UB in C; we pin the
// canonical oracle host (amd64, x87 fistp): NaN and out of range give the
// "integer indefinite" math.MinInt64 (D-0011).
func ctrunc(f float64) int64 {
	if math.IsNaN(f) || f >= 1<<63 || f < -(1<<63) {
		return math.MinInt64
	}
	return int64(f)
}

// x87 is the 80-bit long double nearest to f's shortest decimal form, i.e.
// what strtold made of the text f came from; printing it like printf("%Lf")
// does on amd64 reproduces libyang's rounding (string(0.15) = "0.2").
// ponytail: computed results differ from x87 arithmetic in the last bits (D-0010).
func x87(f float64) *big.Float {
	x, _, err := big.ParseFloat(strconv.FormatFloat(f, 'g', -1, 64), 10, 64, big.ToNearestEven)
	if err != nil {
		return new(big.Float).SetFloat64(f)
	}
	return x
}

// cfmt is printf("%0*Lf", width, f): zero-padded for numbers, space-padded inf/nan.
func cfmt(f float64, width int) string {
	var s string
	switch {
	case math.IsNaN(f):
		s = "nan"
	case math.IsInf(f, 0):
		s = "inf"
	default:
		s = x87(math.Abs(f)).Text('f', 6)
	}
	sign := ""
	if math.Signbit(f) && !math.IsNaN(f) {
		sign = "-"
	}
	if pad := width - len(sign) - len(s); pad > 0 {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return strings.Repeat(" ", pad) + sign + s
		}
		return sign + strings.Repeat("0", pad) + s
	}
	return sign + s
}

// numCmp is moveto_num_cmp: libyang compares the "%Lf" (6 decimals) renderings,
// so numbers equal to 6 decimal places compare equal and NaN sorts as text.
func numCmp(a, b float64) int {
	if a == 0 {
		a = 0 // avoid -0
	}
	if b == 0 {
		b = 0
	}
	neg := false
	switch {
	case a < 0 && b > 0:
		return -1
	case a < 0 && b < 0:
		neg, a, b = true, -a, -b
	case a > 0 && b < 0:
		return 1
	}
	var s1, s2 string
	if a > b {
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
