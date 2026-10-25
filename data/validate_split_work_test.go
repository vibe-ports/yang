// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// This test also compiles unchanged on origin/main: it measures the existing
// internal final-only entry point, with the same native-shaped retained flags.
// Input construction is excluded; duplicate-heavy and descending list keys must
// not make final validation rebuild indexes or scan earlier instances.
func TestSplitValidationWork(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1; namespace "urn:split-work"; prefix m;
  list entry {
    key k;
    leaf k { type string; }
    container flags { leaf d { type string; default "implicit"; } }
  }
}`)
	for _, order := range []string{"sorted", "reverse", "duplicates"} {
		t.Run(order, func(t *testing.T) {
			var previous int64
			for _, count := range []int{128, 256, 512} {
				var input strings.Builder
				input.WriteString(`{"m:entry":[`)
				for i := range count {
					if i > 0 {
						input.WriteByte(',')
					}
					key := i
					if order == "reverse" {
						key = count - i
					}
					if order == "duplicates" {
						key = 0
					}
					fmt.Fprintf(&input, `{"k":"%06d","flags":{"d":"implicit"}}`, key)
				}
				input.WriteString(`]}`)
				tr, ds, err := parseWith(context.Background(), strings.NewReader(input.String()), set,
					parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
				if err != nil {
					t.Fatal(ds, err)
				}
				var flags []*Node
				for _, entry := range tr.top.list {
					for _, n := range entry.kids.list {
						if n.Name() == "flags" {
							// A default-only diff changes the leaf flag directly and
							// leaves the ancestor explicit until final validation.
							n.kids.list[0].flags |= FlagDefault
							flags = append(flags, n)
						}
					}
				}
				if len(flags) != count {
					t.Fatalf("flags = %d, want %d", len(flags), count)
				}
				tr.work.visits = true
				tr.work.Store(0)
				tr.work.hashUsed = 0 // exclude parse work on the pre-journal implementation
				if ds, err = tr.validateModuleFinal(context.Background(), set.Implemented("m"), ValidateOptions{}, Budget{}); err != nil {
					t.Fatal(ds, err)
				}
				work, journalWork := tr.work.Load(), tr.work.hashUsed
				allocs := testing.AllocsPerRun(3, func() {
					for _, n := range flags {
						n.flags &^= FlagDefault
					}
					if ds, err := tr.validateModuleFinal(context.Background(), set.Implemented("m"), ValidateOptions{}, Budget{}); err != nil {
						t.Fatal(ds, err)
					}
				})
				t.Logf("n=%d visits=%d hash/journal=%d allocs=%.0f", count, work, journalWork, allocs)
				if work > int64(20*count+100) || previous > 0 && work > 3*previous {
					t.Fatalf("final validation is not linear: %d (previous %d)", work, previous)
				}
				if journalWork > uint64(4*count+100) { //nolint:gosec // count is one of the bounded positive test sizes above.
					t.Fatalf("journal copied unrelated state: %d", journalWork)
				}
				previous = work
			}
		})
	}
}

// The new NotFinal branch is also on the ordinary Validate/Parse hot path.
// Keep its default-false work comparable to main, including duplicate-heavy state data.
func TestSplitValidationDefaultWork(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1; namespace "urn:split-default-work"; prefix m;
  container r { leaf-list v { config false; type string; } }
}`)
	for _, order := range []string{"sorted", "reverse", "duplicates"} {
		t.Run(order, func(t *testing.T) {
			var previous int64
			for _, count := range []int{128, 256, 512} {
				var input strings.Builder
				input.WriteString(`{"m:r":{"v":[`)
				for i := range count {
					if i > 0 {
						input.WriteByte(',')
					}
					key := i
					if order == "reverse" {
						key = count - i
					}
					if order == "duplicates" {
						key = 0
					}
					fmt.Fprintf(&input, `"%06d"`, key)
				}
				input.WriteString(`]}}`)
				tr, ds, err := parseWith(context.Background(), strings.NewReader(input.String()), set,
					parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
				if err != nil {
					t.Fatal(ds, err)
				}
				tr.work.visits = true
				tr.work.Store(0)
				if ds, err := tr.Validate(context.Background(), ValidateOptions{Present: true}); err != nil {
					t.Fatal(ds, err)
				}
				work, hashWork := tr.work.Load(), tr.work.hashUsed
				allocs := testing.AllocsPerRun(3, func() {
					if ds, err := tr.Validate(context.Background(), ValidateOptions{Present: true}); err != nil {
						t.Fatal(ds, err)
					}
				})
				t.Logf("n=%d visits=%d hash/journal=%d allocs=%.0f", count, work, hashWork, allocs)
				if work > int64(30*count+100) || previous > 0 && work > 3*previous {
					t.Fatalf("nonlinear work: %d (previous %d)", work, previous)
				}
				previous = work
			}
		})
	}
}
