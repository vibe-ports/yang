// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/time_period.c (BSD-3-Clause, © CESNET).

package types

import "math"

// plugins_time_period: stored as a string (lyplg_type_store_string validates the restrictions,
// the record has no validate_value), compared as strings, sorted by compareTimePeriod.
func init() {
	plugins[pluginKey{"libnetconf2-netconf-server", "", "time-period"}] = &plugin{
		id: "time-period", store: storeString, compare: compareTimePeriod}
}

// compareTimePeriod ports lyplg_type_sort_time_period: descending, months before weeks before
// days before hours (the unit is the last character), then by the number (strtol).
func compareTimePeriod(a, b Value) int {
	s1, s2 := a.leaf().canon, b.leaf().canon
	var u1, u2 byte
	if s1 != "" {
		u1 = s1[len(s1)-1]
	}
	if s2 != "" {
		u2 = s2[len(s2)-1]
	}
	switch {
	case u1 == u2:
		v1, v2 := strtol(s1), strtol(s2)
		return cmp3(v1 > v2, v1 < v2)
	case u1 == 'm', u1 == 'w' && u2 != 'm', u1 == 'd' && u2 == 'h':
		return -1
	}
	return 1
}

// strtol is C strtol(s, NULL, 10) for a 64-bit long: leading white space, an optional sign,
// then decimal digits, saturating at the bounds; 0 without digits.
func strtol(s string) int64 {
	i := 0
	for i < len(s) && cIsSpace(s[i]) {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	const limit = math.MaxInt64 + 1
	var v uint64
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		v = min(v, limit/10+1)*10 + uint64(s[i]-'0') // stays far below 2^64
		v = min(v, limit)
	}
	switch {
	case neg && v == limit:
		return math.MinInt64
	case neg:
		return -int64(v)
	case v == limit:
		return math.MaxInt64
	}
	return int64(v)
}
