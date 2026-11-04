// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strconv"
	"testing"
)

const ordinaryRunSchema = `module ordinary-run {yang-version 1.1; namespace "urn:ordinary-run"; prefix r;
 leaf-list ll {type uint32;} leaf separator {type string;}
 container c {leaf a {type string;}} }`

// This file runs unchanged on 46af50e6. Baselines are measured credit consumption,
// including refundable scopes, rather than estimates derived from the implementation.
func TestRunOrdinaryScanCredit(t *testing.T) {
	a, b := reviewDiffSet(t, ordinaryRunSchema), reviewDiffSet(t, ordinaryRunSchema)
	for _, count := range []int{64, 256} {
		for _, mode := range []string{"same", "foreign", "foreign-insert", "schema-separated"} {
			t.Run(fmt.Sprintf("%s/%d", mode, count), func(t *testing.T) {
				tr := newTree(a)
				sample, err := tr.NewTerm(nil, "ordinary-run:ll", "1", NewOptions{})
				if err != nil {
					t.Fatal(err)
				}
				mustLink(t, sample.Unlink())
				// Cold run, descending and duplicate-heavy, without an already retained RB tree.
				for i := 0; i < count; i++ {
					mustLink(t, tr.link(nil, &tr.top, retainedRBTerm(t, sample.schema, strconv.Itoa((count-i)%17)), tr.top.SchemaLen()))
				}
				if mode == "schema-separated" {
					sep, err := tr.NewTerm(nil, "ordinary-run:separator", "x", NewOptions{})
					if err != nil {
						t.Fatal(err)
					}
					if sep == nil {
						t.Fatal("missing separator")
					}
					mustLink(t, tr.link(nil, &tr.top, retainedRBTerm(t, sample.schema, "999"), tr.top.SchemaLen()))
					if !tr.top.schemaUnordered {
						t.Fatal("not schema-separated")
					}
					if _, _, ok := tr.top.contiguousBounds(tr.top.At(0)); ok {
						t.Fatal("fallback not reached")
					}
				}
				sourceSet := a
				if mode == "foreign" || mode == "foreign-insert" {
					sourceSet = b
				}
				source := newTree(sourceSet)
				n, err := source.NewTerm(nil, "ordinary-run:ll", "1000", NewOptions{})
				if err != nil {
					t.Fatal(err)
				}
				mustLink(t, n.Unlink())
				if tr.top.st.interleaved() || tr.top.rbTree[tr.top.At(0)] {
					t.Fatal("not ordinary cold layout")
				}
				w := &tr.work
				w.visits = true
				w.Store(0)
				var consumed uint64
				if mode == "foreign-insert" {
					w.hashUsed = 0
					mustLink(t, tr.Insert(nil, n))
					consumed = w.hashUsed
				} else {
					nested, err := w.prepay(1000000, false)
					if err != nil {
						t.Fatal(err)
					}
					before := w.credit
					lo, hi := tr.runBounds(&tr.top, n)
					consumed = before - w.credit
					w.closeCredit(nested)
					if lo != 0 || hi != count {
						t.Fatalf("bounds=%d,%d want=0,%d", lo, hi, count)
					}
				}
				visits := w.Load()
				if w.hashErr != nil {
					t.Fatal(w.hashErr)
				}
				want := map[string][2]uint64{
					"same": {0, 0}, "foreign": {1, 1},
					"foreign-insert": {9503, 50629}, "schema-separated": {6, 8},
				}[mode]
				wantVisits := map[string][2]int64{
					"same": {26, 34}, "foreign": {66, 258},
					"foreign-insert": {2423, 12733}, "schema-separated": {71, 265},
				}[mode]
				size := 0
				if count == 256 {
					size = 1
				}
				if consumed != want[size] || visits != wantVisits[size] {
					t.Errorf("main credit/visits=%d/%d, got=%d/%d", want[size], wantVisits[size], consumed, visits)
				}
				t.Logf("credit=%d visits=%d", consumed, visits)
			})
		}
	}
}

// Prepare independent identical trees outside the measured interval. Keep both returned
// closures alive so escape analysis cannot elide recovery state. Public Insert exercises
// arming as well. A whole transaction covers each journaled relocation.
func TestRunOrdinaryMoveBytes(t *testing.T) {
	set := reviewDiffSet(t, ordinaryRunSchema)
	for _, journaled := range []bool{false, true} {
		for _, public := range []bool{false, true} {
			t.Run(fmt.Sprintf("journal=%t/public=%t", journaled, public), func(t *testing.T) {
				gc := debug.SetGCPercent(-1)
				defer debug.SetGCPercent(gc)
				const count = 512
				trees := make([]*Tree, count)
				nodes := make([]*Node, count)
				journals := make([]*hashTransaction, count)
				type recovery struct {
					restore func()
					arm     func(*siblings) (func(), error)
				}
				keep := make([]recovery, count)
				for i := range trees {
					tr := newTree(set)
					c, err := tr.NewInner(nil, "ordinary-run:c", NewOptions{})
					if err != nil {
						t.Fatal(err)
					}
					n, err := tr.NewTerm(c, "a", "x", NewOptions{})
					if err != nil {
						t.Fatal(err)
					}
					trees[i], nodes[i] = tr, n
					if journaled {
						journals[i] = beginHashTransaction(tr)
					}
					tr.work.hashUsed = 0
				}
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				for i, tr := range trees {
					if public {
						if err := tr.Insert(nodes[i].parent, nodes[i]); err != nil {
							t.Fatal(err)
						}
					} else {
						restore, arm, err := tr.linkOrigin(nodes[i])
						if err != nil {
							t.Fatal(err)
						}
						keep[i] = recovery{restore, arm}
					}
				}
				runtime.ReadMemStats(&after)
				bytes := after.TotalAlloc - before.TotalAlloc
				credit := trees[0].work.hashUsed
				// Measured with this probe on 46af50e6, alongside the byte baselines.
				wantCredit := uint64(1)
				if public {
					wantCredit = 143
				}
				if journaled {
					wantCredit = 7
					if public {
						wantCredit = 37
					}
				}
				if credit != wantCredit {
					t.Errorf("move credit=%d, main=%d", credit, wantCredit)
				}
				for _, tr := range trees {
					if tr.work.hashUsed != credit {
						t.Fatal("inconsistent move credit")
					}
				}
				runtime.KeepAlive(keep)
				for _, j := range journals {
					var err error
					j.end(&err)
					if err != nil {
						t.Fatal(err)
					}
				}
				want := uint64(512)
				if public {
					want = 1032
				}
				if journaled {
					want = 6040
					if public {
						want = 10144
					}
				}
				if strconv.IntSize == 32 {
					want = 312
					if public {
						want = 624
					}
					if journaled {
						want = 3472
						if public {
							want = 5792
						}
					}
				} else if raceEnabled {
					want += 8
					if journaled {
						want += 8
					}
				}
				// Integer bytes/op uses the same metric as testing.Benchmark. Baselines
				// above were measured by this unchanged probe on 46af50e6.
				if bytes/count != want {
					t.Errorf("main bytes/move=%d, got total=%d (%d moves)", want, bytes, count)
				}
				t.Logf("credit=%d bytes=%d per-move=%d", credit, bytes, bytes/count)
			})
		}
	}
}
