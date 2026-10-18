// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/vibe-ports/yang/internal/types"
)

// lyb_union_print replaces the member, not the outer canonical cache. Terminal
// duplication copies the hash; inner duplication and changed keys rehash it.
func TestStoredHashUnionMemberPrint(t *testing.T) {
	set := pv2Set(t, "pv2-hash-reselect")
	src, _, err := parseXMLString(set, `<r xmlns="urn:pv2-hash-reselect"><l><k>01</k><edit>a</edit></l><s>01</s><plain>01</plain></r>`, Reject, false)
	if err != nil {
		t.Fatal(err)
	}
	if diags, err := src.Validate(context.Background(), ValidateOptions{}); err != nil {
		t.Fatal(err, diags)
	}
	printTree := func(tr *Tree, want string) {
		t.Helper()
		var out strings.Builder
		if err := tr.PrintJSON(&out, PrintOptions{Shrink: true, WithDefaults: WDAll}); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(out.String()); got != want {
			t.Fatalf("JSON = %s, want %s", got, want)
		}
	}
	const validated = `{"pv2-hash-reselect:r":{"l":[{"k":"01","edit":"a"}],"s":["01"],"plain":"01"}}`
	printTree(src, validated)
	root := findNode(t, src, "/pv2-hash-reselect:r")
	key := findNode(t, src, "/pv2-hash-reselect:r/l[1]/k")
	original := key.value.Union()
	// Concurrent lookups, predicates, compares and duplication must leave their
	// shared source unchanged even though a duplicate reselects its key member.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 10 {
				_, _ = types.HashLYB(0, key.value)
				_, _ = (xs{key.parent.schema, set}).PrepareInstance([]string{"01", "a"}, nil)
				if !key.Equal(key, CompareOptions{}) {
					t.Error("self comparison differs")
				}
				if _, err := src.Find("/pv2-hash-reselect:r/l[1]/k"); err != nil {
					t.Error(err)
				}
				dst := newTree(set)
				if _, _, err := dst.Dup(root, nil, DupOptions{Recursive: true}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if key.value.Union() != original {
		t.Fatal("read-only use replaced the source union member")
	}
	printTree(src, validated)
	dst := newTree(set)
	if _, _, err := dst.Dup(root, nil, DupOptions{Recursive: true}); err != nil {
		t.Fatal(err)
	}
	printTree(dst, `{"pv2-hash-reselect:r":{"l":[{"k":1,"edit":"a"}],"s":["01"],"plain":"01"}}`)
	if got := findNode(t, dst, "/pv2-hash-reselect:r/l[1]/k").Value(); got != "01" {
		t.Fatalf("rehash replaced the cached canonical value: %q", got)
	}
	edit := findNode(t, src, "/pv2-hash-reselect:r/l[1]/edit")
	b := newBuilder{set: set, log: &logger{set: set}}
	if err := b.changeTerm(edit, "b", false); err != nil {
		t.Fatal(err)
	}
	printTree(src, `{"pv2-hash-reselect:r":{"l":[{"k":1,"edit":"b"}],"s":["01"],"plain":"01"}}`)
}
