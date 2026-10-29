// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"strings"
	"testing"
)

// An all-null metadata array after an already published RPC leaf-list walks the run by its
// threads (libyang's node->next), not by a positional descent per instance: its work over the
// same parse without metadata stays a constant per instance as the run grows.
func TestParseJSONMetaWalkAfterPublishedLeafList(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  import ietf-yang-metadata { prefix md; }
  md:annotation a { type string; }
  rpc r { input { leaf-list v { type uint32; } } }
}`)
	input := func(n int, meta bool) string {
		var b strings.Builder
		b.WriteString(`{"m:r":{"v":[`)
		for i := range n {
			if i != 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "%d", i)
		}
		b.WriteByte(']')
		if meta {
			b.WriteString(`,"@v":[`)
			for i := range n {
				if i != 0 {
					b.WriteByte(',')
				}
				b.WriteString("null")
			}
			b.WriteByte(']')
		}
		b.WriteString(`}}`)
		return b.String()
	}
	for _, n := range scaleSizes([]int{1024, 8192}, 1) {
		plain, _ := opIndexParseRPC(t, set, input(n, false))
		withMeta, _ := opIndexParseRPC(t, set, input(n, true))
		delta := withMeta.work.Load() - plain.work.Load()
		t.Logf("n=%d: metadata walk work %d", n, delta)
		if delta > 4*int64(n) {
			t.Errorf("n=%d: metadata walk work %d exceeds 4 per instance", n, delta)
		}
	}
}
