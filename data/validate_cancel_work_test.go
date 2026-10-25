// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// No XPath, unique collisions, or implicit nodes can provide incidental polling.
const cancelValidationModel = `module m {
  yang-version 1.1; namespace "urn:validation-cancel"; prefix m;
  leaf-list top { config false; type string; }
  container r {
    leaf-list v { config false; type string; max-elements 100000; }
  }
}`

func cancelValidationInput(count int, nested bool) string {
	return cancelValidationOrderedInput(count, nested, "sorted")
}

func cancelValidationOrderedInput(count int, nested bool, order string) string {
	var input strings.Builder
	if nested {
		input.WriteString(`{"m:r":{"v":[`)
	} else {
		input.WriteString(`{"m:top":[`)
	}
	for i := range count {
		if i > 0 {
			input.WriteByte(',')
		}
		key := i
		if order == "reverse" {
			key = count - 1 - i
		}
		if order == "duplicates" {
			key = 0
		}
		fmt.Fprintf(&input, `"%06d"`, key)
	}
	input.WriteString(`]}`)
	if nested {
		input.WriteByte('}')
	}
	return input.String()
}

func cancelValidationTree(t *testing.T, set *schema.Set, count int, nested bool) *Tree {
	t.Helper()
	tr, ds, err := parseWith(context.Background(), strings.NewReader(cancelValidationInput(count, nested)), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
	if err != nil {
		t.Fatal(ds, err)
	}
	return tr
}

// Compiles unchanged on origin/main. Construction is excluded, and duplicate
// state values plus descending order exercise the retained-tree hot paths.
func TestValidationCancellationWork(t *testing.T) {
	set := opIndexSet(t, cancelValidationModel)
	for _, nested := range []bool{false, true} {
		for _, order := range []string{"sorted", "reverse", "duplicates"} {
			for _, final := range []bool{false, true} {
				t.Run(fmt.Sprintf("nested=%t/%s/final=%t", nested, order, final), func(t *testing.T) {
					var previous int64
					for _, count := range []int{128, 256, 512} {
						tr, ds, err := parseWith(context.Background(), strings.NewReader(cancelValidationOrderedInput(count, nested, order)), set,
							parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
						if err != nil {
							t.Fatal(ds, err)
						}
						tr.work.visits = true
						tr.work.Store(0)
						tr.work.hashUsed = 0 // exclude construction on origin/main's final-only path
						ctx := &cancelAtContext{Context: context.Background()}
						run := func() {
							var err error
							if final {
								_, err = tr.validateModuleFinal(ctx, set.Implemented("m"), ValidateOptions{}, Budget{})
							} else {
								_, err = tr.validateModule(ctx, set.Implemented("m"), ValidateOptions{}, Budget{}, nil)
							}
							if err != nil {
								t.Fatal(err)
							}
						}
						run()
						work, hashWork, polls := tr.work.Load(), tr.work.hashUsed, ctx.calls
						allocs := testing.AllocsPerRun(3, run)
						t.Logf("n=%d visits=%d hash/journal=%d polls=%d allocs=%.0f", count, work, hashWork, polls, allocs)
						if work > int64(30*count+100) || previous > 0 && work > 3*previous || polls > 20*count+100 {
							t.Fatalf("nonlinear validation: visits=%d polls=%d previous=%d", work, polls, previous)
						}
						previous = work
					}
				})
			}
		}
	}
}
