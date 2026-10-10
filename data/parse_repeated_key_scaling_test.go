// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// TestParseRepeatedKeyScaling checks that repeated JSON key members do not repeatedly scan and
// copy all the key children already parsed for the same list instance. Work includes comparisons,
// sibling visits, and slice shifts, as in TestParseJSONLinear. TotalAlloc excludes construction of
// the input and schema set.
func TestParseRepeatedKeyScaling(t *testing.T) {
	type measurement struct {
		work  int64
		bytes uint64
	}
	measure := func(t *testing.T, set *schema.Set, in string, wantValid bool) measurement {
		t.Helper()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		var lc *lydCtx
		tr, diags, err := parseWith(context.Background(), strings.NewReader(in), set,
			parseOpts{ParseOptions: ParseOptions{Unknown: Reject, ParseOnly: true}}, parseJSON,
			func(l *lydCtx) {
				lc = l
				l.tree.work.visits, l.tree.work.shifts = true, true
			})
		runtime.ReadMemStats(&after)
		runtime.KeepAlive(tr)
		runtime.KeepAlive(lc)
		if wantValid && err != nil {
			t.Fatalf("parse: %v (%v)", err, diags)
		}
		if !wantValid && err == nil {
			t.Fatal("parse succeeded, want the repeated first key to displace the second key")
		}
		if lc == nil {
			t.Fatal("parser setup was not called")
		}
		return measurement{work: lc.tree.work.Load(), bytes: after.TotalAlloc - before.TotalAlloc}
	}

	repeatMember := func(name, value string, n int) string {
		return strings.Repeat(fmt.Sprintf(`%q:%q,`, name, value), n-1) + fmt.Sprintf(`%q:%q`, name, value)
	}
	single := func(n int) string {
		return `{"pv2-sorted:c":{"l":[{` + repeatMember("k", "a", n) + `}]}}`
	}
	firstOfTwo := func(n int) string {
		return `{"pv2-sort-keys:c":{"l":[{` + repeatMember("a", "q", n) + `,"b":"x"}]}}`
	}
	lastOfTwo := func(n int) string {
		return `{"pv2-sort-keys:c":{"l":[{"a":"q",` + repeatMember("b", "x", n) + `}]}}`
	}

	cases := []struct {
		name      string
		set       *schema.Set
		input     func(int) string
		wantValid bool
	}{
		{"single-key", pv2Set(t, "pv2-sorted"), single, true},
		{"first-of-two", pv2Set(t, "pv2-sort-keys"), firstOfTwo, false},
		{"last-of-two", pv2Set(t, "pv2-sort-keys"), lastOfTwo, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, n := range []int{500, 2000, 8000} {
				one := measure(t, tc.set, tc.input(n), tc.wantValid)
				four := measure(t, tc.set, tc.input(4*n), tc.wantValid)
				if four.work > 6*one.work || four.bytes > 6*one.bytes {
					t.Fatalf("N=%d: work %d -> %d, allocated bytes %d -> %d: not linear",
						n, one.work, four.work, one.bytes, four.bytes)
				}
			}
		})
	}

	t.Run("many-instances", func(t *testing.T) {
		set := pv2Set(t, "pv2-sorted")
		input := func(m int) string {
			var b strings.Builder
			b.WriteString(`{"pv2-sorted:c":{"l":[`)
			for i := range m {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(`{`)
				b.WriteString(repeatMember("k", fmt.Sprintf("v%03d", i), 50))
				b.WriteByte('}')
			}
			b.WriteString(`]}}`)
			return b.String()
		}
		fifty := measure(t, set, input(50), true)
		twoHundred := measure(t, set, input(200), true)
		if twoHundred.work > 6*fifty.work || twoHundred.bytes > 6*fifty.bytes {
			t.Fatalf("M=50 -> 200, 50 repeated keys each: work %d -> %d, allocated bytes %d -> %d: not linear",
				fifty.work, twoHundred.work, fifty.bytes, twoHundred.bytes)
		}
	})
}

// TestKeyConcatCollisionScaling: instances of a four-string-key list whose keys are distinct
// splits of one string (a run of 'a') share their key concatenation, so a hash over the
// concatenated key bytes would put them all in one bucket. Parse-only and multi-error validation
// (the duplicate-instance check) must still do O(n log n) work and allocations.
func TestKeyConcatCollisionScaling(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  container c {
    list l {
      key "a b c d";
      leaf a { type string; }
      leaf b { type string; }
      leaf c { type string; }
      leaf d { type string; }
    }
  }
}`)
	input := func(n int) string {
		var b strings.Builder
		b.WriteString(`{"m:c":{"l":[`)
		count := 0
		const total = 64
	out:
		for p1 := 1; p1 < total; p1++ {
			for p2 := 1; p1+p2 < total; p2++ {
				for p3 := 1; p1+p2+p3 < total; p3++ {
					if count == n {
						break out
					}
					if count > 0 {
						b.WriteByte(',')
					}
					fmt.Fprintf(&b, `{"a":"%s","b":"%s","c":"%s","d":"%s"}`, strings.Repeat("a", p1),
						strings.Repeat("a", p2), strings.Repeat("a", p3), strings.Repeat("a", total-p1-p2-p3))
					count++
				}
			}
		}
		b.WriteString(`]}}`)
		return b.String()
	}
	measure := func(t *testing.T, in string, parseOnly bool) (int64, uint64) {
		t.Helper()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		var lc *lydCtx
		_, diags, err := parseWith(context.Background(), strings.NewReader(in), set,
			parseOpts{ParseOptions: ParseOptions{Unknown: Reject, ParseOnly: parseOnly,
				Validate: ValidateOptions{MultiError: true, Present: true}}}, parseJSON,
			func(l *lydCtx) {
				lc = l
				l.tree.work.visits, l.tree.work.shifts = true, true
			})
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatal(err, diags)
		}
		return lc.tree.work.Load(), after.TotalAlloc - before.TotalAlloc
	}
	for _, parseOnly := range []bool{true, false} {
		t.Run(fmt.Sprintf("parse-only=%v", parseOnly), func(t *testing.T) {
			for _, n := range []int{500, 2000, 8000} {
				w1, b1 := measure(t, input(n), parseOnly)
				w4, b4 := measure(t, input(4*n), parseOnly)
				if w4 > 6*w1 || b4 > 6*b1 {
					t.Fatalf("N=%d: work %d -> %d, allocated bytes %d -> %d: not O(n log n)", n, w1, w4, b1, b4)
				}
			}
		})
	}
}
