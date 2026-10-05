// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"errors"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// refLD is the exact reference (whole text as a rational; the previous implementation).
func refLD(t *testing.T, s string) (ld, bool) {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("bad number %q", s)
	}
	f := newF().SetRat(r)
	switch e := f.MantExp(nil); {
	case r.Sign() == 0:
	case e > ldMaxExp:
		return ldNaN, true
	case e < -16381:
		if !new(big.Rat).Mul(r, new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), 1-ldMinExp))).IsInt() {
			return ldNaN, true
		}
	}
	return ld{f: f}, false
}

// TestDenormalProbes: the integer conversion agrees with the exact rational one
// on the reviewer's decimal probes around the x87 denormal range.
func TestDenormalProbes(t *testing.T) {
	b, err := os.ReadFile("testdata/denormal-probes.txt")
	if err != nil {
		t.Fatal(err)
	}
	probes := strings.Fields(string(b))
	if len(probes) < 24 {
		t.Fatalf("only %d probes", len(probes))
	}
	for _, s := range probes {
		got, gotE := parseLD(s)
		want, wantE := refLD(t, s)
		if gotE != wantE || got.nan != want.nan || !got.nan && got.big().Cmp(want.big()) != 0 {
			t.Errorf("%.30s… (%d digits): got %v/%v, want %v/%v", s, len(s), got.big(), gotE, want.big(), wantE)
		}
	}
}

// TestManyLongParses: 10k parses of 20k-digit texts finish fast (same text:
// memoized) or hit the step budget (distinct texts: charged by length).
func TestManyLongParses(t *testing.T) {
	digits := strings.Repeat("1234567890", 2_000)
	for _, distinct := range []bool{false, true} {
		ls := make([]*tnode, 10_000)
		for i := range ls {
			v := "0." + digits
			if distinct {
				v = strconv.Itoa(i) + "." + digits
			}
			ls[i] = keyed(list("l", leaf("k", strconv.Itoa(i)), leaf("v", v)), "k")
		}
		tree := top(cont("pv2:c", ls...))
		for _, src := range []string{"count(/c/l[number(v) > 0])", "count(/c/l[k < number('0." + digits + "')])"} {
			start := time.Now()
			_, err := eval(src, EvalContext{Tree: tree})
			if d := time.Since(start); !errors.Is(err, ErrBudget) && d > time.Second {
				t.Errorf("distinct=%v %.40s: took %v (err %v)", distinct, src, d, err)
			}
		}
	}
}
