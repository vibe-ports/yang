// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

func storedHashWidthSet(t *testing.T, typ string) *schema.Set {
	t.Helper()
	return opIndexSet(t, fmt.Sprintf(`module m {
  yang-version 1.1; namespace "urn:stored-hash-width"; prefix m;
  typedef value { type %s }
  container r {
    leaf-list s { config false; type value; }
    list l { key k; leaf k { type value; } }
  }
}`, typ))
}

// Terminal hashes survive cross-context duplication (tree_data.c:2280-2295),
// even when an unused declaration changes their LYB width. A keyed list's hash
// is recomputed from the destination values (tree_data.c:2317).
func TestStoredHashDeclarationWidthDup(t *testing.T) {
	for _, tc := range []struct {
		name, source, target string
	}{
		{
			"enum",
			`enumeration { enum keep { value 1; } enum alternate { value 2; } }`,
			`enumeration { enum keep { value 1; } enum alternate { value 2; } enum wide { value 256; } }`,
		},
		{
			"bits",
			`bits { bit keep { position 0; } bit alternate { position 1; } }`,
			`bits { bit keep { position 0; } bit alternate { position 1; } bit wide { position 8; } }`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srcSet, dstSet := storedHashWidthSet(t, tc.source), storedHashWidthSet(t, tc.target)
			const input = `{"m:r":{"s":["keep"],"l":[{"k":"keep"}]}}`
			src, _, err := parseJSONString(srcSet, input, Reject, false)
			if err != nil {
				t.Fatal(err)
			}
			fresh, _, err := parseJSONString(dstSet, input, Reject, false)
			if err != nil {
				t.Fatal(err)
			}
			wantLeaf := findNode(t, fresh, "/m:r/s[1]")
			if findNode(t, src, "/m:r/s[1]").Equal(wantLeaf, CompareOptions{}) {
				t.Fatal("cross-context comparison ignored the declaration-dependent width")
			}
			dst := newTree(dstSet)
			if _, _, err := dst.Dup(src.top.At(0), nil, DupOptions{Recursive: true}); err != nil {
				t.Fatal(err)
			}
			leaf := findNode(t, dst, "/m:r/s[1]")
			if leaf.Value() != wantLeaf.Value() || leaf.Equal(wantLeaf, CompareOptions{}) {
				t.Fatal("terminal duplication must preserve the source hash and canonical value")
			}
			if !findNode(t, dst, "/m:r/l[k='keep']").Equal(findNode(t, fresh, "/m:r/l[k='keep']"), CompareOptions{FullRecursion: true}) {
				t.Fatal("keyed-list duplication must rehash the destination values")
			}
			for i, value := range []string{"keep", "alternate", "keep"} {
				v, diag := types.Store(leaf.schema.Type, value, types.FormatJSON, types.HintData, nil, leaf.schema)
				if diag != nil {
					t.Fatal(diag.Msg)
				}
				changed, _ := dst.changeTermVal(leaf, v, false)
				if changed != (i > 0) {
					t.Fatalf("assignment %d changed = %t", i, changed)
				}
				if got, want := leaf.Equal(wantLeaf, CompareOptions{}), i == 2; got != want {
					t.Errorf("assignment %d comparison = %t, want %t", i, got, want)
				}
			}
		})
	}
}
