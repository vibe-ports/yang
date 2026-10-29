// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// This test also runs unchanged on origin/main. Inputs cover a duplicate-heavy
// ApplyDiff walk and Merge inserts before an output anchor in adverse value order.
func TestAstra305Round13CountedWork(t *testing.T) {
	set := round13Set(t)
	for _, operation := range []string{"apply-duplicates", "merge-adverse", "merge-duplicates-adverse", "sort-split", "move-whole"} {
		t.Run(operation, func(t *testing.T) {
			var prevWork int64
			var prevBytes uint64
			for _, n := range scaleSizes([]int{128, 512, 2048}, 2) {
				values := make([]string, n)
				for i := range values {
					value := "b"
					if operation == "merge-adverse" {
						value = strconv.Itoa(n - i)
					}
					values[i] = `{"b":"` + value + `"}`
				}
				body := `{"l":[` + strings.Join(values, ",") + `]}`
				dstBody, srcBody := body, `{"@":{"yang:operation":"none"},"l":[`+strings.Join(values, ",")+`]}`
				if operation != "apply-duplicates" {
					dstBody, srcBody = round13MergeTarget, body
				}
				dst, op := round13RPC(t, set, dstBody)
				src, _ := round13RPC(t, set, srcBody)
				if operation != "apply-duplicates" {
					if _, _, ds, err := parseOp(context.Background(), strings.NewReader(round13Reply), set, FormatJSON, opReply, op, Reject); err != nil {
						t.Fatal(err, ds)
					}
				}
				if operation == "merge-duplicates-adverse" {
					// Root operation children never acquire a children table.
					// Keep the output anchor between equal input instances as
					// the digest deque grows throughout the measured merge.
					roots := func(tr *Tree) *Tree {
						out := newTree(set)
						for _, child := range append([]*Node(nil), tr.top.first().kids.schemaSpan().Collect()...) {
							if err := child.Unlink(); err != nil {
								t.Fatal(err)
							}
							if err := out.insert(nil, child, insertLast); err != nil {
								t.Fatal(err)
							}
						}
						return out
					}
					dst, src = roots(dst), roots(src)
				}
				if operation == "sort-split" {
					dst, op = round13SplitCopy(t, set, n)
				}
				if operation == "move-whole" {
					dst, op = round13RPC(t, set, `{}`)
					forest := newTree(set)
					if _, _, err := forest.DupSiblings(src.top.first().kids.At(0), nil, DupOptions{Recursive: true}); err != nil {
						t.Fatal(err)
					}
					src = forest
				}
				dst.work.Store(0)
				src.work.Store(0)
				dst.work.visits, src.work.visits = true, true
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				var err error
				switch operation {
				case "move-whole":
					err = dst.Insert(op, src.top.first())
				case "sort-split":
					_, err = dst.NewTerm(op, "v", "3", NewOptions{})
				case "apply-duplicates":
					err = dst.ApplyDiff(src, ApplyDiffOptions{})
				default:
					err = dst.Merge(src)
				}
				runtime.ReadMemStats(&after)
				if err != nil {
					t.Fatal(err)
				}
				work, allocated := dst.work.Load()+src.work.Load(), after.TotalAlloc-before.TotalAlloc
				t.Logf("n=%d visits/shifts=%d hash=%d bytes=%d", n, work, dst.work.hashUsed, allocated)
				if operation == "move-whole" {
					t.Logf("n=%d source-hash=%d", n, src.work.hashUsed)
				}
				growth := int64(7)
				if operation == "merge-duplicates-adverse" {
					// Native no-table root anchor walks remain quadratic on main
					// and head. The changed bucket insertion adds bounded deque
					// moves and logarithmic comparisons, not another full scan.
					growth = 18
				}
				if prevWork != 0 && (work > growth*prevWork || allocated > 6*prevBytes) {
					t.Fatalf("4x input exceeded expected growth: work %d -> %d, bytes %d -> %d", prevWork, work, prevBytes, allocated)
				}
				prevWork, prevBytes = work, allocated
			}
		})
	}
}
