// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// This test uses only pre-facade APIs and runs unchanged on origin/main. The
// entire public constructor batch is measured, including preflight, optional
// enclosing journal, publication, duplicate registrations, traversal and print.
func TestSiblingFacadePublicWork(t *testing.T) {
	set := round13Set(t)
	for _, journaled := range []bool{false, true} {
		t.Run(fmt.Sprintf("journal=%t", journaled), func(t *testing.T) {
			var previous int64
			var previousBytes uint64
			for _, count := range []int{64, 256, 1024} {
				tr, op := round13RPC(t, set, `{}`)
				tr.work.visits, tr.work.shifts = true, true
				tr.work.Store(0)
				tr.work.hashUsed = 0
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				var j *hashTransaction
				if journaled {
					j = beginHashTransaction(tr)
				}
				var result error
				for i := count - 1; i >= 0; i-- {
					_, result = tr.NewTerm(op, "v", strconv.Itoa(i%17), NewOptions{})
					if result != nil {
						break
					}
				}
				j.end(&result)
				if result != nil {
					t.Fatal(result)
				}
				visited := 0
				for range op.All() {
					visited++
				}
				if visited != count+1 {
					t.Fatalf("walked %d nodes, want %d", visited, count+1)
				}
				if err := tr.PrintJSON(io.Discard, PrintOptions{Shrink: true}); err != nil {
					t.Fatal(err)
				}
				runtime.ReadMemStats(&after)
				work, allocated := tr.work.Load(), after.TotalAlloc-before.TotalAlloc
				t.Logf("n=%d visits/shifts=%d hash=%d bytes=%d", count, work, tr.work.hashUsed, allocated)
				if previous != 0 && (work > 18*previous || allocated > 6*previousBytes) {
					t.Fatal("work/allocation growth regressed")
				}
				previous, previousBytes = work, allocated
			}
		})
	}
}

// Parse publishes a descending deferred run; Merge publishes a replayed batch
// into an existing RB tree. Keep this test compatible with origin/main so both
// work and allocations can be compared without adapting either implementation.
func TestSiblingFacadePublicationWork(t *testing.T) {
	set := round13Set(t)
	for _, operation := range []string{"parse", "merge"} {
		t.Run(operation, func(t *testing.T) {
			var previous int64
			var previousBytes uint64
			for _, count := range []int{64, 256, 1024} {
				values := make([]string, count)
				for i := range values {
					values[i] = strconv.Itoa((count - 1 - i) % 17)
				}
				body := `{"v":[` + strings.Join(values, ",") + `]}`
				var dst, src *Tree
				var op *Node
				if operation == "merge" {
					dst, op = round13RPC(t, set, `{"v":[10000,10001]}`)
					src, _ = round13RPC(t, set, body)
					dst.work.visits, dst.work.shifts = true, true
					dst.work.Store(0)
					dst.work.hashUsed = 0
				}
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				if operation == "parse" {
					dst, op = round13RPC(t, set, body)
				} else if err := dst.Merge(src); err != nil {
					t.Fatal(err)
				}
				runtime.ReadMemStats(&after)
				work, allocated := dst.work.Load(), after.TotalAlloc-before.TotalAlloc
				visited := 0
				for range op.All() {
					visited++
				}
				want := count + 1
				if operation == "merge" {
					want += 2
				}
				if visited != want {
					t.Fatalf("walked %d nodes, want %d", visited, want)
				}
				t.Logf("n=%d visits/shifts=%d hash=%d bytes=%d", count, work, dst.work.hashUsed, allocated)
				// Native operation anchors and duplicate replay can be quadratic.
				// Match the pre-facade ceiling; compare exact counts on origin/main too.
				if previous != 0 && (work > 18*previous || allocated > 6*previousBytes) {
					t.Fatal("publication work/allocation growth regressed")
				}
				previous, previousBytes = work, allocated
			}
		})
	}
}
