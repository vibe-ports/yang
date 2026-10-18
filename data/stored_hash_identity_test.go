// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// identityref.c:326-353 regenerates the LYB spelling from the resolved identity.
// tree_data.c:2317 rehashes duplicated lists even when their keys retain the source
// canonical text. In particular, a string "child" must no longer hash like the copy.
func TestStoredHashIdentityDup(t *testing.T) {
	makeSet := func(typ string) *schema.Set {
		return opIndexSet(t, fmt.Sprintf(`module m {
  yang-version 1.1; namespace "urn:stored-hash-identity"; prefix m;
  identity base; identity child { base base; }
  container r { list l { key k; leaf k { type %s } } }
}`, typ))
	}
	srcSet, dstSet := makeSet("string;"), makeSet("identityref { base base; }")
	for _, repeated := range []bool{false, true} {
		t.Run(fmt.Sprintf("repeated=%t", repeated), func(t *testing.T) {
			input := `{"m:r":{"l":[{"k":"child"}]}}`
			freshInput := `{"m:r":{"l":[{"k":"m:child"}]}}`
			if repeated {
				input = `{"m:r":{"l":[{"k":"m:child","k":"child"}]}}`
				freshInput = `{"m:r":{"l":[{"k":"m:child","k":"m:child"}]}}`
			}
			src, _, err := parseJSONString(srcSet, input, Reject, true)
			if err != nil {
				t.Fatal(err)
			}
			dst := newTree(dstSet)
			if _, _, err := dst.Dup(src.top.list[0], nil, DupOptions{Recursive: true}); err != nil {
				t.Fatal(err)
			}
			source, copied := src.top.list[0].kids.list[0], dst.top.list[0].kids.list[0]
			if source.Equal(copied, CompareOptions{}) || copied.Equal(source, CompareOptions{}) {
				t.Fatal("identityref copy still compares equal to the string source")
			}
			if got := copied.kids.list[len(copied.kids.list)-1].Value(); got != "child" {
				t.Fatalf("LYB print replaced the canonical cache: %q", got)
			}
			fresh, _, err := parseJSONString(dstSet, freshInput, Reject, true)
			if err != nil {
				t.Fatal(err)
			}
			if got := copied.Equal(fresh.top.list[0].kids.list[0], CompareOptions{}); got != repeated {
				t.Fatalf("fresh identityref comparison = %t, want %t", got, repeated)
			}
		})
	}
}
