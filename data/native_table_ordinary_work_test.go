// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"math"
	"runtime"
	"strconv"
	"testing"
)

// This file also runs unchanged on the design's ecb6fd1 baseline. The private A
// harness must add no ordinary work or allocations, even on adverse insertion
// order and duplicate-heavy data. Allocation assertions use the minimum of five
// independent samples. There is no production ownership allowance in G1.
func TestNativeOrdinaryWorkBaseline(t *testing.T) {
	baselines := map[int][2][4]uint64{
		0:   {{1, 0, 16, 2248}, {1, 11, 36, 12384}},
		1:   {{8, 17, 37, 4152}, {8, 42, 58, 18256}},
		2:   {{38, 155, 65, 6392}, {38, 187, 86, 20496}},
		3:   {{53, 208, 77, 7504}, {53, 247, 98, 21624}},
		64:  {{4504, 8099, 1147, 116176}, {4504, 8649, 1186, 345128}},
		256: {{26608, 42844, 4207, 444112}, {26608, 44994, 4256, 1425512}},
	}
	set := reviewDiffSet(t, `module native-work {yang-version 1.1;namespace urn:native-work;prefix n;
 container c {leaf-list ll {type uint32;}}}`)
	for _, count := range []int{0, 1, 2, 3, 64, 256} {
		for _, journaled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%t", count, journaled), func(t *testing.T) {
				var visits int64
				var charges uint64
				run := func() {
					tr := newTree(set)
					tr.work.visits = true
					done := tr.work.beginHashWork()
					var j *hashTransaction
					if journaled {
						j = beginHashTransaction(tr)
					}
					p, err := tr.NewInner(nil, "native-work:c", NewOptions{})
					if err != nil {
						t.Fatal(err)
					}
					nodes := make([]*Node, count)
					for i := count - 1; i >= 0; i-- {
						nodes[i], err = tr.NewTerm(p, "ll", strconv.Itoa(i%17), NewOptions{})
						if err != nil {
							t.Fatal(err)
						}
					}
					for i := 0; i < count; i += 3 {
						if err = nodes[i].Unlink(); err != nil {
							t.Fatal(err)
						}
					}
					j.end(&err)
					if err != nil {
						t.Fatal(err)
					}
					visits, charges = tr.work.Load(), tr.work.hashUsed
					done()
					runtime.KeepAlive(tr)
				}
				run() // warm schema-only caches equally on both revisions
				allocations := math.Inf(1)
				bytes := ^uint64(0)
				for range 5 {
					allocations = min(allocations, testing.AllocsPerRun(1, run))
					var before, after runtime.MemStats
					runtime.ReadMemStats(&before)
					run()
					runtime.ReadMemStats(&after)
					bytes = min(bytes, after.TotalAlloc-before.TotalAlloc)
				}
				t.Logf("visits=%d charges=%d allocations=%.0f bytes=%d", visits, charges, allocations, bytes)
				index := 0
				if journaled {
					index = 1
				}
				want := baselines[count][index]
				if visits != int64(want[0]) || charges != want[1] { //nolint:gosec // fixed small measured constants
					t.Fatalf("ordinary work changed: visits=%d charges=%d want=%v", visits, charges, want)
				}
				// Allocator sizes differ on 386 and race instrumentation adds its
				// own allocations. The canonical non-race amd64 comparison is exact.
				if strconv.IntSize == 64 && !raceEnabled && (allocations > float64(want[2]) || bytes > want[3]) {
					t.Fatalf("ordinary allocation cost grew: allocs=%.0f bytes=%d ceilings=%v", allocations, bytes, want)
				}
			})
		}
	}
}
