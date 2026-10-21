// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"reflect"
	"testing"
)

func TestTypedObserverFieldsCompared(t *testing.T) {
	for _, tc := range []struct {
		op   string
		want []string
	}{
		{"data", nil},
		{"diff", nil},
		{"sequence", nil},
	} {
		if got := (Yang{}).SkippedFields(tc.op); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s skips %v, want %v", tc.op, got, tc.want)
		}
	}
}
