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

// Uses only origin/main APIs: the identical duplicate-heavy workload runs on main and head.
// Counts include initialization, narrow-gap insertion, both move directions, bulk removal,
// reordering, retained/fresh republication and Keep, with and without an external journal.
func TestSequenceDormantKindsWork(t *testing.T) {
	for _, journaled := range []bool{false, true} {
		var units uint64
		workload := func() {
			var seq Seq[int]
			var j Journal
			hooks := &meterHooks{}
			if journaled {
				hooks.j = &j
			}
			seq.SetHooks(hooks)
			values := make([]int, 4096)
			if _, ok := seq.Splice(0, 0, values); !ok {
				t.Fatal("initial splice refused")
			}
			for range 128 {
				if seq.InsertBefore(seq.Peek(1), 0) == nil {
					t.Fatal("insert refused")
				}
			}
			if !seq.MoveBefore(seq.Last(), seq.First()) || !seq.MoveBefore(seq.First(), nil) {
				t.Fatal("move refused")
			}
			if !seq.Remove(seq.Peek(2)) {
				t.Fatal("remove refused")
			}
			if _, ok := seq.Splice(3, 32, values[:16]); !ok {
				t.Fatal("splice refused")
			}
			hs := make([]*Handle[int], 64)
			for i := range hs {
				hs[i] = seq.Peek(64 - i)
			}
			if !seq.ReplaceHandles(1, 65, hs) {
				t.Fatal("replace handles refused")
			}
			i := 0
			if _, ok := seq.Republish(1, 65, values[:68], func(int) *Handle[int] {
				i++
				if i <= len(hs) {
					return hs[i-1]
				}
				return nil
			}); !ok {
				t.Fatal("republication refused")
			}
			i = 0
			seq.Keep(func(int) bool { i++; return i%5 != 0 })
			units = hooks.units
		}
		allocations := testing.AllocsPerRun(1, workload)
		// Measured with this identical workload against origin/main.
		wantUnits := uint64(27780)
		if journaled {
			wantUnits = 36270
		}
		if units != wantUnits {
			t.Fatalf("dormant kind accounting changed: got %d, main %d", units, wantUnits)
		}
		t.Logf("journaled=%t work+entries=%d allocations=%.0f", journaled, units, allocations)
	}
}
