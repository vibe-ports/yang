// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/types"
)

// Count width declaration visits as well as instances. Varying the dimensions
// together catches the former declarations*instances scan; varying each alone
// distinguishes schema preparation from repeated value hashing.
func TestStoredHashEnumDeclarationWork(t *testing.T) {
	measure := func(declarations, instances int) int64 {
		var source strings.Builder
		source.WriteString(`module m { yang-version 1.1; namespace urn:m; prefix m;
leaf-list s { config false; type enumeration {`)
		for i := range declarations {
			fmt.Fprintf(&source, "enum e%d;", i)
		}
		source.WriteString("} } }")
		set := opIndexSet(t, source.String())
		sn := opIndexModule(t, set).Top[0]
		if sn.Type.EnumLYBWidth == 0 {
			t.Fatal("compiled enum width was not prepared")
		}
		input := `{"m:s":[` + strings.Repeat(`"e0",`, instances-1) + `"e0"]}`
		tr, _, err := parseJSONString(set, input, Reject, false)
		if err != nil {
			t.Fatal(err)
		}
		if tr.top.SchemaLen() != instances {
			t.Fatalf("got %d instances, want %d", tr.top.SchemaLen(), instances)
		}
		work := int64(declarations) // one immutable declaration-width pass
		for _, node := range tr.top.SchemaAll() {
			_, _, ok, walked := types.HashLYBWork(schemaHash(sn), node.value)
			if !ok || walked > 4 {
				t.Fatalf("compiled enum hash did %d work; want at most four bytes", walked)
			}
			work += int64(walked) + 1 //nolint:gosec // the assertion above bounds walked
		}
		return work
	}
	const n = 128
	base := measure(n, n)
	for _, dims := range [][2]int{{4 * n, n}, {n, 4 * n}, {4 * n, 4 * n}} {
		if got := measure(dims[0], dims[1]); got > 6*base {
			t.Errorf("declarations=%d instances=%d: work %d > 6 * %d", dims[0], dims[1], got, base)
		}
	}
}
