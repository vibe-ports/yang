// SPDX-License-Identifier: BSD-3-Clause
// Test cases ported from libyang v5.8.6 tests/utests/schema/test_tree_schema_compile.c
// (test_type_range, test_type_length, test_type_dec64) (BSD-3-Clause, © CESNET).

package compile

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// fmtParts renders r as "lo..hi | v" with unsigned bounds for unsigned bases.
func fmtParts(r *schema.Range, b schema.BaseType) string {
	var s []string
	for _, p := range r.Parts {
		lo, hi := fmt.Sprint(p.Min), fmt.Sprint(p.Max)
		if unsignedBase(b) {
			lo, hi = fmt.Sprint(p.MinU), fmt.Sprint(p.MaxU)
		}
		if lo == hi {
			s = append(s, lo)
		} else {
			s = append(s, lo+".."+hi)
		}
	}
	return strings.Join(s, " | ")
}

func TestCompileRange(t *testing.T) {
	for _, tc := range []struct {
		b          schema.BaseType
		fd         uint8
		base, expr string // base "" = derived from the built-in
		want       string
	}{
		{schema.Int16, 0, "", "min..10|max", "-32768..10 | 32767"},
		{schema.Int32, 0, "", "min..10|max", "-2147483648..10 | 2147483647"},
		{schema.Int64, 0, "", "min..10|max", "-9223372036854775808..10 | 9223372036854775807"},
		{schema.Uint8, 0, "", "min..10|max", "0..10 | 255"},
		{schema.Uint16, 0, "", "min..10|max", "0..10 | 65535"},
		{schema.Uint32, 0, "", "min..10|max", "0..10 | 4294967295"},
		{schema.Uint64, 0, "", "min..10|max", "0..10 | 18446744073709551615"},
		{schema.Binary, 0, "", "min", "0"},
		{schema.Binary, 0, "", "max", "18446744073709551615"},
		{schema.Binary, 0, "", "min..max", "0..18446744073709551615"},
		{schema.Binary, 0, "", "5", "5"},
		{schema.Binary, 0, "", "1..10|20..30", "1..10 | 20..30"},
		{schema.Binary, 0, "", "16 | 32", "16 | 32"},
		{schema.Binary, 0, "10", "10", "10"},
		{schema.Binary, 0, "10..100", "50", "50"},
		{schema.Binary, 0, "10..100", "10..30|60..100", "10..30 | 60..100"},
		{schema.Binary, 0, "10..100", "min..max", "10..100"},
		{schema.Int8, 0, "1 | 5..10", "1 | 6..7", "1 | 6..7"},
		{schema.Int8, 0, "1 | 5..10", "6", "6"},
		{schema.Dec64, 2, "", "min..-1.5 | 0 .. 3.1| max", "-9223372036854775808..-150 | 0..310 | 9223372036854775807"},
		{schema.Dec64, 2, "", "+1.1 .. 2", "110..200"},
	} {
		var base *schema.Range
		if tc.base != "" {
			var err error
			if base, err = compileRange(&parser.Restr{Arg: tc.base}, tc.b, false, tc.fd, nil); err != nil {
				t.Fatalf("base %s: %v", tc.base, err)
			}
		}
		r, err := compileRange(&parser.Restr{Arg: tc.expr, ErrorMessage: "m", ErrorAppTag: "a"}, tc.b, false, tc.fd, base)
		if err != nil {
			t.Errorf("%s %q: %v", tc.b, tc.expr, err)
			continue
		}
		if got := fmtParts(r, tc.b); got != tc.want || r.Msg != "m" || r.AppTag != "a" {
			t.Errorf("%s %q: %s, want %s", tc.b, tc.expr, got, tc.want)
		}
	}
}

func TestCompileRangeErrors(t *testing.T) {
	for _, tc := range []struct {
		b                    schema.BaseType
		fd                   uint8
		length               bool
		base, expr, rc, want string
	}{
		{schema.Binary, 0, true, "", "-10", "LY_EDENIED", `Invalid length restriction - value "-10" does not fit the type limitations.`},
		{schema.Binary, 0, true, "", "18446744073709551616", "LY_EVALID", `Invalid length restriction - invalid value "18446744073709551616".`},
		{schema.Binary, 0, true, "", "max .. 10", "LY_EVALID", "Invalid length restriction - unexpected data after max keyword (.. 10)."},
		{schema.Binary, 0, true, "", "50..10", "LY_EEXIST", "Invalid length restriction - values are not in ascending order (10)."},
		{schema.Binary, 0, true, "", "50 | 10", "LY_EEXIST", "Invalid length restriction - values are not in ascending order (10)."},
		{schema.Binary, 0, true, "", "x", "LY_EVALID", "Invalid length restriction - unexpected data (x)."},
		{schema.Binary, 0, true, "", "50 | min", "LY_EVALID", "Invalid length restriction - unexpected data before min keyword (50 | )."},
		{schema.Binary, 0, true, "", "| 50", "LY_EVALID", "Invalid length restriction - unexpected beginning of the expression (| 50)."},
		{schema.Binary, 0, true, "", "10 ..", "LY_EVALID", `Invalid length restriction - unexpected end of the expression after ".." (10 ..).`},
		{schema.Binary, 0, true, "", ".. 10", "LY_EVALID", `Invalid length restriction - unexpected ".." without a lower bound.`},
		{schema.Binary, 0, true, "", "10 |", "LY_EVALID", "Invalid length restriction - unexpected end of the expression (10 |)."},
		{schema.Binary, 0, true, "", "10..20 | 15..30", "LY_EEXIST", "Invalid length restriction - values are not in ascending order (15)."},
		{schema.Binary, 0, true, "10", "11", "LY_EVALID", "Invalid length restriction - the derived restriction (11) is not equally or more limiting."},
		{schema.Binary, 0, true, "10..100", "1..11", "LY_EVALID", "Invalid length restriction - the derived restriction (1..11) is not equally or more limiting."},
		{schema.Binary, 0, true, "10..100", "20..110", "LY_EVALID", "Invalid length restriction - the derived restriction (20..110) is not equally or more limiting."},
		{schema.Binary, 0, true, "10..100", "20..30|110..120", "LY_EVALID", "Invalid length restriction - the derived restriction (20..30|110..120) is not equally or more limiting."},
		{schema.Binary, 0, true, "10..11", "15", "LY_EVALID", "Invalid length restriction - the derived restriction (15) is not equally or more limiting."},
		{schema.Binary, 0, true, "10..20|30..40", "15..35", "LY_EVALID", "Invalid length restriction - the derived restriction (15..35) is not equally or more limiting."},
		{schema.Binary, 0, true, "10", "10..35", "LY_EVALID", "Invalid length restriction - the derived restriction (10..35) is not equally or more limiting."},
		{schema.Uint16, 0, false, "", "1..70000", "LY_EDENIED", `Invalid range restriction - value "70000" does not fit the type limitations.`},
		{schema.Int8, 0, false, "", "min..10 | 5..max", "LY_EEXIST", "Invalid range restriction - values are not in ascending order (5)."},
		{schema.Int8, 0, false, "", "1 | max | 2", "LY_EVALID", "Invalid range restriction - unexpected data after max keyword (| 2)."},
		{schema.Dec64, 2, false, "", "3.142..4", "LY_EINVAL", `Range boundary "3.142" of decimal64 type exceeds defined number (2) of fraction digits.`},
		{schema.Dec64, 2, false, "", "1.1..3.14 | 1.2", "LY_EEXIST", "Invalid range restriction - values are not in ascending order (1.2)."},
		{schema.Dec64, 18, false, "", "-10000000000000000000..0", "LY_EVALID",
			`Invalid range restriction - invalid value "-10000000000000000000000000000000000000".`},
		{schema.Dec64, 18, false, "", "10000000000000000000", "LY_EVALID",
			`Invalid range restriction - invalid value "10000000000000000000000000000000000000".`},
	} {
		var base *schema.Range
		if tc.base != "" {
			var err error
			if base, err = compileRange(&parser.Restr{Arg: tc.base}, tc.b, tc.length, tc.fd, nil); err != nil {
				t.Fatalf("base %s: %v", tc.base, err)
			}
		}
		_, err := compileRange(&parser.Restr{Arg: tc.expr}, tc.b, tc.length, tc.fd, base)
		var ve *vErr
		if !errors.As(err, &ve) || ve.Err != tc.rc || ve.Msg != tc.want {
			t.Errorf("%q:\n got  %v\n want %s %s", tc.expr, err, tc.rc, tc.want)
		}
	}
}
