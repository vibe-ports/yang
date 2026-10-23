// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import "testing"

func TestSequenceReorderedSelectorOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	for _, value := range []string{"z", "0"} {
		t.Run(value, func(t *testing.T) {
			base, p := reorderedSelectorRequest(t, value)
			want, err := oracle(base, p)
			if err != nil {
				t.Fatal(err)
			}
			checkReorderedSelector(t, want)
			got, err := (Yang{}).Run(Request{BaseDir: base, Params: p})
			if err != nil {
				t.Fatal(err)
			}
			if diff := diffResponses(want, got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestSequenceOpaqueParentSelectorOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	for _, kind := range []string{"insert_term", "insert_inner", "insert_list"} {
		t.Run(kind, func(t *testing.T) {
			base, p := opaqueParentSelectorRequest(t, kind, 2)
			want, err := oracle(base, p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (Yang{}).Run(Request{BaseDir: base, Params: p})
			if err != nil {
				t.Fatal(err)
			}
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("%s: native = %v; Go = %v", diff, want, got)
			}
		})
	}
}
