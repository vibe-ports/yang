// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// exactLD rounds the whole decimal text exactly (reference for the truncation).
func exactLD(t *testing.T, s string) *big.Float {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("bad number %q", s)
	}
	return newF().SetRat(r)
}

// TestParseTruncation: cutting the mantissa to sigDigits with a sticky digit
// rounds like the exact conversion, also next to x87 ties.
func TestParseTruncation(t *testing.T) {
	tie := "1.0000000000000000000542101086242752217003726400434970855712890625" // 1 + 2^-64
	cases := []string{
		tie + strings.Repeat("0", sigDigits) + "1",
		tie[:len(tie)-1] + "4" + strings.Repeat("9", sigDigits+10),
		tie + strings.Repeat("0", sigDigits+50),
	}
	for k := range 20 { // pseudo-random digits beyond the cut
		var b strings.Builder
		b.WriteString("0.")
		for i := range sigDigits + 1 + k*25 {
			b.WriteByte("0123456789"[(i*31+k*17+i/7*3+i/97)%10])
		}
		cases = append(cases, b.String())
	}
	for _, s := range cases {
		got, erange := parseLD(s)
		if want := exactLD(t, s); erange || got.big().Cmp(want) != 0 {
			t.Errorf("%.40s…: got %v, want %v", s, got.big(), want)
		}
	}
}

// TestHugeNumbers: a 4M-digit literal or data value parses in linear time,
// and long %Lf renderings are charged to the step budget.
func TestHugeNumbers(t *testing.T) {
	start := time.Now()
	for _, s := range []string{"1" + strings.Repeat("0", 4_000_000), "0." + strings.Repeat("0", 4_000_000) + "1",
		"1." + strings.Repeat("3", 4_000_000), strings.Repeat("7", 4_000_000) + "e-3999990"} {
		cStrtod(s)
	}
	if d := time.Since(start); d > 30*time.Second { // sanity bound only (CI runners under -race)
		t.Errorf("4M-digit numbers took %v", d)
	}
	tree := bigTree(10_000)
	start = time.Now()
	if r, err := eval("count(/c/l[k = number('1e-4000')])", EvalContext{Tree: tree}); err != nil || r.Num != 1 { // k = "0" equals 1e-4000 to 6 decimals, as libyang
		t.Errorf("tiny: %v %v", r.Num, err)
	}
	if d := time.Since(start); d > 30*time.Second { // sanity bound only (CI runners under -race)
		t.Errorf("tiny comparisons took %v", d)
	}
	_, err := eval("count(/c/l[k = number('1e4000')])", EvalContext{Tree: tree, MaxSteps: 100_000})
	if !errors.Is(err, ErrBudget) {
		t.Errorf("huge comparisons not charged: %v", err)
	}
}
