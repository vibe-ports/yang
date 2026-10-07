// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"net/netip"
	"testing"
)

// TestNtop6 pins the IPv6 text form libyang's canonical ipv6-address values depend on: zero-run
// compression (RFC 5952 §4.2) and the dotted-quad tail of IPv4-mapped and -compatible addresses.
func TestNtop6(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"::", "::"},
		{"::1", "::1"},
		{"1::", "1::"},
		{"fe80::", "fe80::"},
		{"1:2:3:4:5:6:7:8", "1:2:3:4:5:6:7:8"},
		{"2001:0DB8:0000:0000:0000:FF00:0042:8329", "2001:db8::ff00:42:8329"}, // lower case, no leading zeros
		{"1:0:2:3:4:5:6:7", "1:0:2:3:4:5:6:7"},                                // a single zero group stays
		{"1:0:0:1:0:0:0:1", "1:0:0:1::1"},                                     // the longest run
		{"1:0:0:2:0:0:3:4", "1::2:0:0:3:4"},                                   // the first of equal runs
		{"0:0:1:0:0:0:0:0", "0:0:1::"},
		{"0:0:1:0:0:2:0:0", "::1:0:0:2:0:0"},
		{"::ffff:192.0.2.1", "::ffff:192.0.2.1"}, // IPv4-mapped
		{"::ffff:0:1", "::ffff:0.0.0.1"},
		{"::ffff:0:0", "::ffff:0.0.0.0"},
		{"::192.0.2.1", "::192.0.2.1"}, // IPv4-compatible
		{"::0.1.0.0", "::0.1.0.0"},
		{"::ab:cd", "::0.171.0.205"},
		{"::0.0.255.255", "::ffff"}, // bits 96-111 zero: plain hex
		{"::1:ffff:1.2.3.4", "::1:ffff:102:304"},
		{"0:0:0:0:ffff:0:102:304", "::ffff:0:102:304"},
		{"::fffe:1.2.3.4", "::fffe:102:304"},
		{"1::ffff:1.2.3.4", "1::ffff:102:304"},
	} {
		a := netip.MustParseAddr(c.in).As16()
		if got := ntop6(a[:]); got != c.want {
			t.Errorf("ntop6(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCStrptime pins the strptime "%Y-%m-%d" / "%H:%M:%S" subset libyang's date and time types
// rely on: leading white space per field, up to the field width of digits (fewer when the next
// digit would exceed the maximum), and the range check.
func TestCStrptime(t *testing.T) {
	ymd := [3][3]int{{4, 0, 9999}, {2, 1, 12}, {2, 1, 31}}
	hms := [3][3]int{{2, 0, 23}, {2, 0, 59}, {2, 0, 61}}
	for _, c := range []struct {
		in     string
		sep    byte
		fields [3][3]int
		vals   [3]int
		n      int
		ok     bool
	}{
		{"2024-01-02", '-', ymd, [3]int{2024, 1, 2}, 10, true},
		{"2024-12-31T00:00:00Z", '-', ymd, [3]int{2024, 12, 31}, 10, true},
		{"0000-01-01", '-', ymd, [3]int{0, 1, 1}, 10, true},
		{"2024-1-2", '-', ymd, [3]int{2024, 1, 2}, 8, true},
		{"7-1-2", '-', ymd, [3]int{7, 1, 2}, 5, true},
		{" 2024-\t01-\n02", '-', ymd, [3]int{2024, 1, 2}, 13, true},
		{"2024-1-41", '-', ymd, [3]int{2024, 1, 4}, 8, true}, // 41 > 31: one digit taken
		{"2024-1-3", '-', ymd, [3]int{2024, 1, 3}, 8, true},
		{"20245-01-01", '-', ymd, [3]int{2024}, 4, false}, // 4-digit width, then '5' is no separator
		{"2024-13-01", '-', ymd, [3]int{2024}, 7, false},
		{"2024-00-01", '-', ymd, [3]int{2024}, 7, false},
		{"2024-01-00", '-', ymd, [3]int{2024, 1}, 10, false},
		{"2024-1-32", '-', ymd, [3]int{2024, 1}, 9, false},
		{"2024-01", '-', ymd, [3]int{2024, 1}, 7, false},
		{"2024--01-01", '-', ymd, [3]int{2024}, 5, false},
		{"+2024-01-01", '-', ymd, [3]int{}, 0, false},
		{"", '-', ymd, [3]int{}, 0, false},
		{"12:34:56", ':', hms, [3]int{12, 34, 56}, 8, true},
		{"23:59:60", ':', hms, [3]int{23, 59, 60}, 8, true},
		{"23:59:61", ':', hms, [3]int{23, 59, 61}, 8, true},
		{"9:5:3", ':', hms, [3]int{9, 5, 3}, 5, true},
		{"23:59:7.5", ':', hms, [3]int{23, 59, 7}, 7, true},
		{"23:59:62", ':', hms, [3]int{23, 59}, 8, false},
		{"24:00:00", ':', hms, [3]int{}, 2, false},
		{"3:61:00", ':', hms, [3]int{3, 6}, 3, false}, // 61 > 59: "6", then '1' is no separator
		{"12:34", ':', hms, [3]int{12, 34}, 5, false},
	} {
		vals, n, ok := cStrptime(c.in, c.sep, c.fields)
		if vals != c.vals || n != c.n || ok != c.ok {
			t.Errorf("cStrptime(%q) = %v, %d, %v; want %v, %d, %v", c.in, vals, n, ok, c.vals, c.n, c.ok)
		}
	}
}
