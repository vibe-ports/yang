// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"math"
	"slices"
	"strings"
	"testing"
)

func TestTimePeriodSort(t *testing.T) {
	in := strings.Fields("3h 2d 12h 1m 4w 7d 12m 1w 24h 9m")
	slices.SortStableFunc(in, func(a, b string) int {
		return compareTimePeriod(Value{canon: a}, Value{canon: b})
	})
	if got := strings.Join(in, " "); got != "12m 9m 1m 4w 1w 7d 2d 24h 12h 3h" {
		t.Errorf("order %s", got)
	}
	for _, c := range []struct {
		s    string
		want int64
	}{{"", 0}, {"\t-12x", -12}, {"99999999999999999999h", math.MaxInt64}, {"-99999999999999999999", math.MinInt64}, {"+7", 7}} {
		if got := strtol(c.s); got != c.want {
			t.Errorf("strtol(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}
