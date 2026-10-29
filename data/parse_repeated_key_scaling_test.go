// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

// TestParseRepeatedKeyScaling checks that repeated JSON key members do not repeatedly scan and
// copy all the key children already parsed for the same list instance. Work includes comparisons,
// sibling visits, and slice shifts, as in TestParseJSONLinear. TotalAlloc excludes construction of
// the input and schema set.
func TestParseRepeatedKeyScaling(t *testing.T) {
	type measurement struct {
		work     int64
		hashWork uint64
		bytes    uint64
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
				l.tree.work.visits = true
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
		return measurement{work: lc.tree.work.Load(), hashWork: lc.tree.work.hashUsed,
			bytes: after.TotalAlloc - before.TotalAlloc}
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
			for _, n := range scaleSizes([]int{500, 2000, 8000}, 1) {
				one := measure(t, tc.set, tc.input(n), tc.wantValid)
				four := measure(t, tc.set, tc.input(4*n), tc.wantValid)
				minWork := uint64(n) //nolint:gosec // the measured sizes above are all positive
				if tc.name == "first-of-two" && (one.hashWork < minWork || four.hashWork > 6*one.hashWork) {
					t.Fatalf("N=%d: positional-prefix visits charged %d -> %d; want bounded linear work",
						n, one.hashWork, four.hashWork)
				}
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

func TestParseLongFirstKeyRepeatedLastKeyWork(t *testing.T) {
	long := "a" + strings.Repeat("x", 1<<16)
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
	  container c { leaf x { type string; } leaf y { type string; }
	    leaf z { type string; } list l { key "`+long+` b";
	      leaf `+long+` { type string; } leaf b { type string; } } } }`)
	input := func(count int) string {
		return `{"m:c":{"x":"x","y":"y","z":"z","l":[{"` + long + `":"a",` +
			strings.Repeat(`"b":"q",`, count-1) + `"b":"q"}]}}`
	}
	measure := func(count int, limit uint64) (uint64, int64, *Tree, error) {
		var parsed *Tree
		_, _, err := parseWith(context.Background(), strings.NewReader(input(count)), set,
			parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, func(lc *lydCtx) {
				parsed = lc.tree
				parsed.work.hashLimit = limit
				parsed.work.visits = true
			})
		return parsed.work.hashUsed, parsed.work.Load(), parsed, err
	}
	one, countedOne, _, err := measure(64, defaultMaxHashWork)
	if err != nil {
		t.Fatal(err)
	}
	four, countedFour, _, err := measure(256, defaultMaxHashWork)
	if err != nil {
		t.Fatal(err)
	}
	if one < uint64(len(long)) || four > 6*one ||
		four > one && four-one > 192*128 || countedFour > 6*countedOne {
		t.Fatalf("64 to 256 repeated last keys: charged %d -> %d, counted %d -> %d",
			one, four, countedOne, countedFour)
	}
	base, _, _, err := measure(1, defaultMaxHashWork)
	if err != nil {
		t.Fatal(err)
	}
	limit := base + (four-base)/2
	used, counted, _, err := measure(256, limit)
	if !errors.Is(err, yang.ErrBudget) || used > limit || float64(counted) > 32*float64(limit) {
		t.Fatalf("budget error=%v charged=%d counted=%d limit=%d", err, used, counted, limit)
	}
	if used <= base || counted < 64 {
		t.Fatalf("budget refused before exercising repeated keys: charged=%d counted=%d", used, counted)
	}
	_, _, tr, err := measure(1, defaultMaxHashWork)
	if err != nil {
		t.Fatal(err)
	}
	container := tr.top.first()
	list := container.kids.At(3)
	if container.kids.ht == nil || list.kids.SchemaLen() != 2 {
		t.Fatal("linked list did not build the children table")
	}
	oldHash, oldKey := list.hash, list.hkey
	tr.work.hashLimit = 32 // the native key hash fits, the secondary discriminator does not
	if changed, err := list.kids.At(1).SetValue("r"); changed || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("last key change=%t error=%v; want ErrBudget", changed, err)
	}
	if list.kids.At(1).Value() != "q" || list.hash != oldHash || list.hkey != oldKey {
		t.Fatal("failed last key change published a value or changed the list index")
	}
	tr.work.hashLimit = 1
	if added, err := tr.NewTerm(list, "b", "q", NewOptions{}); added != nil || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("repeated key node=%v error=%v; want ErrBudget", added, err)
	}
	if list.kids.SchemaLen() != 2 || list.hash != oldHash || list.hkey != oldKey {
		t.Fatal("failed repeated key insertion published a key or changed the list index")
	}
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
				l.tree.work.visits = true
			})
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatal(err, diags)
		}
		return lc.tree.work.Load(), after.TotalAlloc - before.TotalAlloc
	}
	for _, parseOnly := range []bool{true, false} {
		t.Run(fmt.Sprintf("parse-only=%v", parseOnly), func(t *testing.T) {
			for _, n := range scaleSizes([]int{500, 2000, 8000}, 1) {
				w1, b1 := measure(t, input(n), parseOnly)
				w4, b4 := measure(t, input(4*n), parseOnly)
				if w4 > 6*w1 || b4 > 6*b1 {
					t.Fatalf("N=%d: work %d -> %d, allocated bytes %d -> %d: not O(n log n)", n, w1, w4, b1, b4)
				}
			}
		})
	}
}
