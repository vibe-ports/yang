// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"strings"
	"testing"
)

// A metadata array after a leaf-list walks the instances by their threads (libyang's
// node->next), not by a positional descent per instance, for an already published RPC
// leaf-list and an ordinary container's: its work over the same parse without metadata stays
// a constant per instance as the run grows.
func TestParseJSONMetaWalkThreads(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  import ietf-yang-metadata { prefix md; }
  md:annotation a { type string; }
  rpc r { input {
    leaf-list v { type uint32; }
    container c { leaf-list v { type uint32; } }
  } }
}`)
	array := func(n int, item func(int) string) string {
		var b strings.Builder
		b.WriteByte('[')
		for i := range n {
			if i != 0 {
				b.WriteByte(',')
			}
			b.WriteString(item(i))
		}
		b.WriteByte(']')
		return b.String()
	}
	values := func(n int) string { return array(n, func(i int) string { return fmt.Sprint(i) }) }
	nulls := func(n int) string { return array(n, func(int) string { return "null" }) }
	cases := []struct {
		name  string
		input func(n int, meta bool) string
	}{
		{"rpc-after", func(n int, meta bool) string {
			s := `{"m:r":{"v":` + values(n)
			if meta {
				s += `,"@v":` + nulls(n)
			}
			return s + `}}`
		}},
		{"container-after", func(n int, meta bool) string {
			s := `{"m:r":{"c":{"v":` + values(n)
			if meta {
				s += `,"@v":` + nulls(n)
			}
			return s + `}}}`
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, n := range scaleSizes([]int{1024, 8192}, 1) {
				plain, _ := opIndexParseRPC(t, set, tc.input(n, false))
				withMeta, _ := opIndexParseRPC(t, set, tc.input(n, true))
				delta := withMeta.work.Load() - plain.work.Load()
				t.Logf("n=%d: metadata walk work %d", n, delta)
				if delta > 4*int64(n) {
					t.Errorf("n=%d: metadata walk work %d exceeds 4 per instance", n, delta)
				}
			}
		})
	}
}
