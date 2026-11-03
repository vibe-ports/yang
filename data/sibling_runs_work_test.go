// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"strconv"
	"testing"
)

// Runs unchanged on origin/main. Include construction, duplicate-heavy descending RB
// insertion, single unlink and batch unlink, with and without an enclosing journal.
func TestRunOrdinaryWork(t *testing.T) {
	set := reviewDiffSet(t, `module run-work {yang-version 1.1;namespace "urn:run-work";prefix r;
 container c { leaf-list ll {type uint32;} }}`)
	for _, journaled := range []bool{false, true} {
		for _, count := range []int{64, 256} {
			t.Run(fmt.Sprintf("%t/%d", journaled, count), func(t *testing.T) {
				var visits int64
				var hashes uint64
				var observed *Tree
				allocations := testing.AllocsPerRun(3, func() {
					tr := newTree(set)
					c, err := tr.NewInner(nil, "run-work:c", NewOptions{})
					if err != nil {
						t.Fatal(err)
					}
					tr.work.visits = true
					tr.work.Store(0)
					tr.work.hashUsed = 0
					var j *hashTransaction
					if journaled {
						j = beginHashTransaction(tr)
					}
					nodes := make([]*Node, count)
					for i := count - 1; i >= 0; i-- {
						nodes[i], err = tr.NewTerm(c, "ll", strconv.Itoa(i%17), NewOptions{})
						if err != nil {
							t.Fatal(err)
						}
					}
					for i := 0; i < count; i += 3 {
						if err = nodes[i].Unlink(); err != nil {
							t.Fatal(err)
						}
					}
					if err = tr.unlinkAll([]*Node{nodes[1], nodes[2]}); err != nil {
						t.Fatal(err)
					}
					j.end(&err)
					if err != nil {
						t.Fatal(err)
					}
					visits, hashes = tr.work.Load(), tr.work.hashUsed
					observed = tr
				})
				checkTreeRetainedRB(t, observed)
				wantVisits, wantHash := int64(4691), uint64(918)
				if count == 256 {
					wantVisits, wantHash = 27322, 2390
				}
				if journaled {
					wantHash = 9392
					if count == 256 {
						wantHash = 47151
					}
				}
				// Ordinary accounting must match main exactly, including conservative debits.
				if visits != wantVisits || hashes != wantHash {
					t.Fatalf("main work changed: visits=%d hash=%d", visits, hashes)
				}
				wantAllocs := float64(1175)
				if count == 256 {
					wantAllocs = 4245
				}
				if journaled {
					wantAllocs = 1213
					if count == 256 {
						wantAllocs = 4293
					}
				}
				// Map growth can vary by a few allocations. These are the origin/main probe
				// totals, with the same duplicate-heavy descending input and journal modes.
				// Race instrumentation adds four allocations in each probe mode.
				if raceEnabled {
					wantAllocs += 4
				}
				if allocations > wantAllocs+4 {
					t.Fatalf("main allocations grew: %.2f, baseline %.2f", allocations, wantAllocs)
				}
				t.Logf("visits=%d hash=%d allocations=%.2f", visits, hashes, allocations)
			})
		}
	}
}
