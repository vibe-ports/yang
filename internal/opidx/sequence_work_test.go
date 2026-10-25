// SPDX-License-Identifier: BSD-3-Clause

package opidx

import (
	"math"
	"testing"
)

// Uses the API shared with origin/main so the identical counted workload can
// run on both revisions. Every value is identical and every insertion narrows
// the same gap; successful order comparisons must still distinguish handles.
func TestSequenceCountedDuplicateGapWork(t *testing.T) {
	for _, initial := range []int{512, 4096, 16384} {
		var seq Seq[int]
		for range initial {
			if seq.InsertBefore(nil, 0) == nil {
				t.Fatal("initial insertion failed")
			}
		}
		var work uint64
		seq.SetWorkHook(func(amount uint64) { work += amount })
		const operations = 256
		for range operations {
			anchor := seq.Select(1)
			h := seq.InsertBefore(anchor, 0)
			if h == nil {
				t.Fatal("gap insertion failed")
			}
			if order, ok := seq.Compare(h, anchor); !ok || order != -1 {
				t.Fatal("inserted handle does not precede anchor")
			}
			if order, ok := seq.Compare(anchor, h); !ok || order != 1 {
				t.Fatal("anchor does not follow inserted handle")
			}
		}
		limit := uint64(80 * operations * math.Log2(float64(seq.Len())+1))
		if work > limit {
			t.Fatalf("initial=%d duplicate gap work=%d exceeds %d", initial, work, limit)
		}
		t.Logf("initial=%d operations=%d duplicate gap work=%d", initial, operations, work)
	}
}
