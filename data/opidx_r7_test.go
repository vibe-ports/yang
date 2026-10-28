// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"strings"
	"testing"
)

func TestOpIndexMetaArrayManyLists(t *testing.T) {
	sizes := scaleSizes([]int{256, 512, 1024, 2048}, 3)
	opIndexScaling(t, sizes, func(n int) int64 {
		var yang, input strings.Builder
		yang.WriteString(`module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  import ietf-yang-metadata { prefix md; }
  md:annotation a { type string; }
  rpc r { input {`)
		input.WriteString(`{"m:r":{`)
		for i := range n {
			fmt.Fprintf(&yang, " leaf-list l%d { type uint32; ordered-by user; }", i)
			if i != 0 {
				input.WriteByte(',')
			}
			fmt.Fprintf(&input, `"l%d":[1,2]`, i)
		}
		for i := range n {
			fmt.Fprintf(&input, `,"@l%d":[{"m:a":"x"},{"m:a":"y"}]`, i)
		}
		yang.WriteString(` } } }`)
		input.WriteString(`}}`)

		set := opIndexSet(t, yang.String())
		tree, op := opIndexParseRPC(t, set, input.String())
		work := tree.work.Load()

		counts := make(map[string]int, n)
		for child := range op.kids.all() {
			name := child.Name()
			at := counts[name]
			if at >= 2 {
				t.Fatalf("%s has more than 2 instances", name)
			}
			if len(child.meta) != 1 {
				t.Fatalf("%s[%d] has %d metadata instances, want 1", name, at, len(child.meta))
			}
			want := "x"
			if at == 1 {
				want = "y"
			}
			meta := child.meta[0]
			if got := meta.value.Canonical(); meta.mod.Name != "m" || meta.name != "a" || got != want {
				t.Fatalf("%s[%d] metadata = %s:%s=%q, want m:a=%q", name, at,
					meta.mod.Name, meta.name, got, want)
			}
			counts[name]++
		}
		if len(counts) != n {
			t.Fatalf("parsed %d leaf-list runs, want %d", len(counts), n)
		}
		for i := range n {
			if got := counts[fmt.Sprintf("l%d", i)]; got != 2 {
				t.Fatalf("l%d has %d instances, want 2", i, got)
			}
		}
		return work
	})
}
