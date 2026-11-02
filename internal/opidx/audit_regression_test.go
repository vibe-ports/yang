// SPDX-License-Identifier: BSD-3-Clause

package opidx

import (
	"fmt"
	"strconv"
	"testing"
)

// Check the point bound itself, including the Select performed for an empty range.
func TestSequenceReplaceHandlesSmallBound(t *testing.T) {
	for _, journaled := range []bool{false, true} {
		for _, labeled := range []bool{false, true} {
			for _, threaded := range []bool{false, true} {
				for _, length := range []int{0, 1, 2, 17, 128} {
					for lo := 0; lo <= length; lo++ {
						for k := 0; k <= min(1, length-lo); k++ {
							t.Run(fmt.Sprintf("journal=%t/labels=%t/threads=%t/n=%d/lo=%d/k=%d", journaled, labeled, threaded, length, lo, k), func(t *testing.T) {
								var seq Seq[int]
								seq.SetLabeled(labeled)
								seq.SetWeigh(func(int) bool { return true })
								hs, ok := seq.Splice(0, 0, make([]int, length))
								if !ok {
									t.Fatal("initial splice refused")
								}
								if threaded {
									seq.SetKindThreads()
								}
								var j Journal
								hooks := &meterHooks{}
								if journaled {
									hooks.j = &j
								}
								seq.SetHooks(hooks)
								var first, last *Handle[int]
								if k != 0 {
									first, last = hs[lo], hs[lo+k-1]
								}
								bound := seq.ReplaceHandlesBound(first, last, k, journaled, nil)
								if !seq.ReplaceHandles(lo, lo+k, hs[lo:lo+k]) {
									t.Fatal("replacement refused")
								}
								if hooks.units > bound {
									t.Fatalf("used=%d bound=%d", hooks.units, bound)
								}
								if err := seq.CheckInvariants(); err != nil {
									t.Fatal(err)
								}
							})
						}
					}
				}
			}
		}
	}
}

func TestSequenceKindThreadsCheckpointBound(t *testing.T) {
	for _, journaled := range []bool{false, true} {
		for _, depth := range []int{0, 1, 3, 16} {
			for _, length := range []int{0, 1, 2, 17, 128} {
				t.Run(fmt.Sprintf("journal=%t/depth=%d/n=%d", journaled, depth, length), func(t *testing.T) {
					var seq Seq[int]
					seq.SetWeigh(func(int) bool { return true })
					hs, ok := seq.Splice(0, 0, make([]int, length))
					if !ok {
						t.Fatal("initial splice refused")
					}
					tokens := make([]*Checkpoint[int], depth)
					for i := range tokens {
						tokens[i] = seq.Checkpoint()
					}
					var j Journal
					hooks := &meterHooks{}
					if journaled {
						hooks.j = &j
					}
					seq.SetHooks(hooks)
					bound := seq.KindThreadsBound(journaled)
					seq.SetKindThreads()
					if hooks.units > bound {
						t.Fatalf("used=%d bound=%d", hooks.units, bound)
					}
					if seq.KindThreadsBound(journaled) != 0 {
						t.Fatal("enabled threads need no work")
					}
					for i := len(tokens) - 1; i >= 0; i-- {
						if !seq.Rollback(tokens[i]) {
							t.Fatal("rollback refused")
						}
						if seq.KindThreads() {
							t.Fatal("rollback retained threads")
						}
						for _, h := range hs {
							if h.kprev != nil || h.knext != nil {
								t.Fatal("rollback retained kind links")
							}
						}
						if err := seq.CheckInvariants(); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}

// Uses only APIs available on origin/main; the same workload measures its work and allocations.
// All values are duplicates. Checkpoint history includes fresh captures, reused outer images,
// inner commit, inner rollback and handles created under an outer checkpoint.
func dormantCheckpointMeasurement[T any](t *testing.T, depth int, commit bool) (float64, uint64) {
	t.Helper()
	var seq Seq[T]
	if _, ok := seq.Splice(0, 0, make([]T, 4)); !ok {
		t.Fatal("initial splice refused")
	}
	var work uint64
	seq.SetWorkHook(func(n uint64) { work += n })
	run := func() {
		work = 0
		outer := seq.Checkpoint()
		if depth > 1 {
			if !seq.ReplaceHandles(0, 1, []*Handle[T]{seq.First()}) {
				t.Fatal("singleton refused")
			}
			if seq.InsertBefore(seq.First(), *new(T)) == nil {
				t.Fatal("insert refused")
			}
		}
		var inner *Checkpoint[T]
		if depth > 1 {
			inner = seq.Checkpoint()
		}
		seq.Keep(func(T) bool { return false })
		if inner != nil {
			if commit {
				if !seq.Commit(inner) {
					t.Fatal("inner commit refused")
				}
			} else if !seq.Rollback(inner) {
				t.Fatal("inner rollback refused")
			}
		}
		if !seq.Rollback(outer) {
			t.Fatal("outer rollback refused")
		}
	}
	allocs := testing.AllocsPerRun(10, run)
	if err := seq.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	return allocs, work
}

func TestSequenceDormantCheckpointAllocations(t *testing.T) {
	for _, depth := range []int{1, 2} {
		for _, commit := range []bool{false, true} {
			t.Run(fmt.Sprintf("depth=%d/commit=%t", depth, commit), func(t *testing.T) {
				baseline, baselineWork := dormantCheckpointMeasurement[int](t, depth, commit)
				// Measured by running this identical test on origin/main. Fixed allocation
				// overhead varies with the Go version; payload sizes share the int control.
				wantWork := uint64(13)
				if depth == 2 {
					wantWork = 59
					if commit {
						wantWork = 54
					}
				}
				if baselineWork != wantWork {
					t.Fatalf("work=%d; main work=%d", baselineWork, wantWork)
				}
				check := func(name string, allocs float64, work uint64, indirect bool) {
					t.Helper()
					// Main stores values larger than 128 bytes indirectly. Four old handles are captured
					// at each checkpoint; the fifth handle is new under the outer checkpoint.
					want := baseline
					if indirect {
						want += float64(4*depth + (depth - 1))
					}
					if allocs != want || work != baselineWork {
						t.Errorf("%s allocations=%.0f want=%.0f work=%d want=%d", name, allocs, want, work, baselineWork)
					}
					t.Logf("%s allocations=%.0f work=%d", name, allocs, work)
				}
				a, w := dormantCheckpointMeasurement[[0]uint64](t, depth, commit)
				check("[0]uint64", a, w, false)
				a, w = dormantCheckpointMeasurement[[3]uint64](t, depth, commit)
				check("[3]uint64", a, w, false)
				a, w = dormantCheckpointMeasurement[[6]uint64](t, depth, commit)
				check("[6]uint64", a, w, false)
				a, w = dormantCheckpointMeasurement[[7]uint64](t, depth, commit)
				check("[7]uint64", a, w, strconv.IntSize == 64)
			})
		}
	}
}

// The bound must saturate instead of wrapping when nested captures or journal images overflow.
func TestSequenceKindThreadsBoundSaturates(t *testing.T) {
	var seq Seq[int]
	seq.length = int(^uint(0) >> 1)
	for _, depth := range []int{0, 1, 2, 3} {
		seq.checkpoints = make([]*seqCheckpoint[int], depth)
		for _, journaled := range []bool{false, true} {
			want := ^uint64(0)
			if strconv.IntSize == 32 || (depth == 0 && !journaled) || (depth == 1 && !journaled) {
				want = uint64(seq.length) * uint64(depth+1) //nolint:gosec // positive maximum int and nonnegative depth
				if journaled {
					want += 2 * (uint64(seq.length) + 1) //nolint:gosec // positive maximum int
				}
			}
			if got := seq.KindThreadsBound(journaled); got != want {
				t.Fatalf("depth=%d journaled=%t bound=%d want=%d", depth, journaled, got, want)
			}
		}
	}
}

// Exact handle images must survive captures both before and after threads are enabled.
func TestSequenceCheckpointKindImages(t *testing.T) {
	for _, weighed := range []bool{false, true} {
		for _, threaded := range []bool{false, true} {
			for _, journaled := range []bool{false, true} {
				t.Run(fmt.Sprintf("weighed=%t/threaded=%t/journal=%t", weighed, threaded, journaled), func(t *testing.T) {
					var seq Seq[int]
					if weighed {
						seq.SetWeigh(func(v int) bool { return v%2 == 0 })
					}
					hs, ok := seq.Splice(0, 0, []int{0, 1, 2, 3, 4})
					if !ok {
						t.Fatal("initial splice refused")
					}
					if threaded {
						seq.SetKindThreads()
					}
					before := make([]Handle[int], len(hs))
					for i, h := range hs {
						before[i] = *h
					}
					var j Journal
					if journaled {
						seq.SetHooks(&meterHooks{j: &j})
					}
					mark := j.Mark()
					outer := seq.Checkpoint()
					seq.SetKindThreads()
					if !seq.Remove(hs[2]) {
						t.Fatal("remove refused")
					}
					inner := seq.Checkpoint()
					seq.Keep(func(int) bool { return false })
					if !seq.Commit(inner) || !seq.Rollback(outer) {
						t.Fatal("checkpoint completion refused")
					}
					// A concurrent external journal stamps handles before checkpoint capture.
					for i, h := range hs {
						if journaled {
							before[i].stamp = h.stamp
						}
						if *h != before[i] {
							t.Fatalf("checkpoint image %d not restored", i)
						}
					}
					if journaled {
						j.Rollback(mark)
					}
					for i, h := range hs {
						before[i].stamp = h.stamp
						if *h != before[i] {
							t.Fatalf("journal image %d not restored", i)
						}
					}
					if seq.KindThreads() != threaded {
						t.Fatal("thread flag not restored")
					}
					if err := seq.CheckInvariants(); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

// Audit the other bulk point bounds changed by kind threading at k == 0 and k == 1,
// including external neighbours. These bounds retain their external-journal contract.
func TestSequenceKindSmallRepublishBounds(t *testing.T) {
	for _, journaled := range []bool{false, true} {
		for _, labeled := range []bool{false, true} {
			for _, threaded := range []bool{false, true} {
				for _, length := range []int{0, 1, 2, 17} {
					for lo := 0; lo <= length; lo++ {
						for k := 0; k <= min(1, length-lo); k++ {
							var seq Seq[int]
							seq.SetLabeled(labeled)
							seq.SetWeigh(func(v int) bool { return v%2 == 0 })
							values := make([]int, length)
							for i := range values {
								values[i] = i
							}
							hs, ok := seq.Splice(0, 0, values)
							if !ok {
								t.Fatal("initial splice refused")
							}
							if threaded {
								seq.SetKindThreads()
							}
							var j Journal
							hooks := &meterHooks{}
							if journaled {
								hooks.j = &j
							}
							seq.SetHooks(hooks)
							for _, anyPosition := range []bool{false, true} {
								j.Commit()
								bound := seq.RepublishBound(lo, lo+k, 0, journaled, nil)
								if anyPosition {
									bound = RepublishBoundAny(length, k, journaled, threaded)
								}
								hooks.units = 0
								if _, ok := seq.Republish(lo, lo+k, values[lo:lo+k], func(v int) *Handle[int] { return hs[v] }); !ok {
									t.Fatal("republication refused")
								}
								if hooks.units > bound {
									t.Fatalf("journal=%t/labels=%t/threads=%t/n=%d/lo=%d/k=%d/any=%t: used=%d bound=%d", journaled, labeled, threaded, length, lo, k, anyPosition, hooks.units, bound)
								}
								if err := seq.CheckInvariants(); err != nil {
									t.Fatal(err)
								}
							}
						}
					}
				}
			}
		}
	}
}
