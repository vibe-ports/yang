// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"path/filepath"
	"testing"
)

func TestEngineEmptyTreeSequencesOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	for name, p := range engineEmptyTreeRequests() {
		t.Run(name, func(t *testing.T) {
			base := filepath.Join("corpus", "protocol-v2")
			want, err := oracle(base, p)
			if err != nil {
				t.Fatal(err)
			}
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
