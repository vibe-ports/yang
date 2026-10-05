// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"errors"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"
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

// TestManyLongParses: 10k conversions of 20k-digit texts parse each distinct
// text once per evaluation (memo) and are charged to the budget (distinct
// texts: ErrBudget). Counts parses instead of timing them.
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
		for _, c := range []struct {
			src     string
			ok      func(float64) bool
			viaData bool // converts the 10k data values
		}{
			{"count(/c/l[number(v) > 0])", func(n float64) bool { return n == 10_000 }, true},
			{"count(/c/l[k < number('0." + digits + "')])", func(n float64) bool { return n == 1 }, false},
			{"sum(/c/l/v)", func(n float64) bool { return n > 1234 && n < 1235 }, true},
		} {
			e, err := Compile(c.src, jsonNS{"pv2": true})
			if err != nil {
				t.Fatal(err)
			}
			ec := EvalContext{Tree: tree}
			ev := newEvaluator(e, &ec)
			v, err := ev.eval(e.root, ev.start())
			if err == nil {
				err = ev.err
			}
			switch {
			case distinct && c.viaData:
				if !errors.Is(err, ErrBudget) {
					t.Errorf("distinct %.40s: want ErrBudget, got %v after %d parses", c.src, err, ev.parses)
				}
			case err != nil || ev.parses != 1 || !c.ok(v.f.float()):
				t.Errorf("distinct=%v %.40s: %v parses, result %v, err %v", distinct, c.src, ev.parses, v.f.float(), err)
			}
		}
	}
}
