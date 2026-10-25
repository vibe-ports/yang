// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"reflect"
	"testing"
)

// Live libyang v5.8.6 evidence for LYD_DUP_NO_LYDS: the oracle and the engine agree on the whole
// response (typed dumps and diagnostics) and on the leaf-list orders. No golden files are generated.
func TestDupNoSortLiveOracle(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	for _, tc := range dupNoSortCases {
		t.Run(tc.name, func(t *testing.T) {
			p := dupNoSortRequest(tc.steps)
			want, err := nativeRequestRunner(t)(base, p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (Yang{}).Run(Request{ID: "protocol-v2/dup-no-sort-" + tc.name, BaseDir: base, Params: p})
			if err != nil {
				t.Fatal(err)
			}
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s", diff)
			}
			if !reflect.DeepEqual(want.Diagnostics(), got.Diagnostics()) {
				t.Fatalf("diagnostics: oracle %#v, engine %#v", want.Diagnostics(), got.Diagnostics())
			}
			if orders := dupNoSortOrders(t, want); !reflect.DeepEqual(orders, tc.want) {
				t.Fatalf("oracle orders = %v, want %v", orders, tc.want)
			}
		})
	}
}
