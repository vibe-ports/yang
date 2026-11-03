// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"runtime/debug"
	"strings"
	"testing"
)

// This file also runs unchanged on origin/main. Include long values, duplicate
// keys in adverse insertion order, both reservation and application of list
// comparisons, and the key-reading consumers. Measure allocations and counted
// work together so moving a walk between budgets cannot hide a regression.
func TestPhysicalKeyOrdinaryWork(t *testing.T) {
	// Keep fmt's reusable buffers alive across samples: pool eviction by a GC
	// otherwise adds a few unrelated allocations to the long-value workload.
	oldGC := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(oldGC)
	set := pv2Set(t, "pv2-sort-keys")
	for _, journaled := range []bool{false, true} {
		for _, count := range []int{64, 256} {
			t.Run(fmt.Sprintf("%t/%d", journaled, count), func(t *testing.T) {
				var visits int64
				var hash uint64
				allocs := testing.AllocsPerRun(3, func() {
					tr, ds, err := parseJSONString(set, `{"pv2-sort-keys:c":{"l":[{"a":"A","b":"B"},{"a":"A","b":"B"}]}}`, Reject, true)
					if err != nil {
						t.Fatal(err, ds)
					}
					a, b := tr.top.first().kids.At(0), tr.top.first().kids.At(1)
					tr.work.visits = true
					tr.work.Store(0)
					tr.work.hashUsed = 0
					var j *hashTransaction
					if journaled {
						j = beginNarrowTransaction(tr)
					}
					for i := count - 1; i >= 0; i-- {
						mustLink(t, tr.link(a, &a.kids, modelFreshTerm(t, a, "b", fmt.Sprintf("%04096d", i%17)), a.kids.SchemaLen()))
					}
					mustLink(t, chargeCompareCost(&tr.work, a, b))
					if cmp, ok := rbCompareLists(&tr.work, a, b); !ok || cmp != 0 {
						t.Fatal(cmp, ok)
					}
					mustLink(t, chargeCompareCost(&tr.work, b, a))
					if cmp, ok := rbCompareLists(&tr.work, b, a); !ok || cmp != 0 {
						t.Fatal(cmp, ok)
					}
					if listPredicate(a) == "" || compareInstanceKey(a, false, false) == "" {
						t.Fatal("missing keys")
					}
					j.end(&err)
					if err != nil {
						t.Fatal(err)
					}
					visits, hash = tr.work.Load(), tr.work.hashUsed
				})
				wantVisits, wantHash := int64(1109), uint64(263063)
				if count == 256 {
					wantVisits, wantHash = 4887, 1052697
				}
				if journaled {
					wantHash = 263601
					if count == 256 {
						wantHash = 1054771
					}
				}
				if visits != wantVisits || hash != wantHash {
					t.Fatalf("main work changed: visits=%d hash=%d", visits, hash)
				}
				t.Logf("visits=%d hash=%d allocations=%.0f", visits, hash, allocs)
			})
		}
	}
}

// Portable on origin/main: ordinary complete list prefixes keep their exact
// single/batch removal work and allocations, including duplicate-heavy input.
func TestPhysicalKeyOrdinaryUnlinkWork(t *testing.T) {
	oldGC := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(oldGC)
	set := pv2Set(t, "pv2-sorted")
	for _, count := range []int{64, 256} {
		var input strings.Builder
		input.WriteString(`{"pv2-sorted:c":{"l":[`)
		for i := count - 1; i >= 0; i-- {
			if i != count-1 {
				input.WriteByte(',')
			}
			fmt.Fprintf(&input, `{"k":"%04d","v":"v"}`, i%17)
		}
		input.WriteString(`]}}`)
		for _, batch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/batch=%t", count, batch), func(t *testing.T) {
				var visits int64
				var hash uint64
				allocs := testing.AllocsPerRun(3, func() {
					tr, ds, err := parseJSONString(set, input.String(), Reject, true)
					if err != nil {
						t.Fatal(err, ds)
					}
					var removed []*Node
					for _, list := range tr.top.first().kids.SchemaAll() {
						removed = append(removed, list.kids.At(1))
					}
					tr.work.visits = true
					tr.work.Store(0)
					tr.work.hashUsed = 0
					hash = 0
					if batch {
						mustLink(t, tr.unlinkAll(removed))
						hash = tr.work.hashUsed
					} else {
						for _, n := range removed {
							mustLink(t, n.Unlink())
							hash += tr.work.hashUsed
						}
					}
					visits = tr.work.Load()
				})
				wantVisits, wantHash := int64(704), uint64(704)
				if count == 256 {
					wantVisits, wantHash = 2816, 2816
				}
				if batch {
					wantVisits, wantHash = 576, 1344
					if count == 256 {
						wantVisits, wantHash = 2304, 5376
					}
				}
				if visits != wantVisits || hash != wantHash {
					t.Fatalf("main unlink work changed: visits=%d hash=%d", visits, hash)
				}
				t.Logf("visits=%d hash=%d allocations=%.0f", visits, hash, allocs)
			})
		}
	}
}
