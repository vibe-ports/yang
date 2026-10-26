// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

func round8CopiedRPCChildren(t *testing.T, set *schema.Set) (*Tree, *Tree, *Node) {
	t.Helper()
	_, parsed := opIndexParseRPC(t, set, `{"m:r":{"l":[{"v":"a"},{"v":"b"}]}}`)
	src := newTree(set)
	first, _, err := src.DupSiblings(parsed.kids.At(0), nil, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	dst, dstOp := opIndexParseRPC(t, set, `{"m:r":{}}`)
	if first != src.top.first() || src.top.len() != 2 || src.top.order == nil {
		t.Fatal("fixture needs two copied operation children with an ordered top-level index")
	}
	return src, dst, dstOp
}

func TestInsertWholeCopiedRPCSiblingsSourceBudget(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input { list l { leaf v { type string; } } } } }`)
	src, dst, parent := round8CopiedRPCChildren(t, set)
	srcBefore, dstBefore := snapshotHashBudgetTree(src), snapshotHashBudgetTree(dst)
	src.work.hashLimit = 1
	if err := dst.Insert(parent, src.top.first()); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("Insert with exhausted source budget = %v, want ErrBudget", err)
	}
	if after := snapshotHashBudgetTree(src); !reflect.DeepEqual(after, srcBefore) {
		t.Fatalf("failed Insert changed source\nbefore=%+v\nafter=%+v", srcBefore, after)
	}
	if after := snapshotHashBudgetTree(dst); !reflect.DeepEqual(after, dstBefore) {
		t.Fatalf("failed Insert changed destination\nbefore=%+v\nafter=%+v", dstBefore, after)
	}
	opidxAssertTreeInvariant(t, src)
	opidxAssertTreeInvariant(t, dst)
}

func round8KeylessMerge(t *testing.T, set *schema.Set, count int) (*Tree, *Node, *Tree) {
	t.Helper()
	dst, op := opIndexParseRPC(t, set, `{"m:r":{}}`)
	var input strings.Builder
	input.WriteString(`{"m:r":{"l":[`)
	for i := range count {
		if i != 0 {
			input.WriteByte(',')
		}
		input.WriteString(`{"v":"`)
		input.WriteString(strconv.Itoa(i))
		input.WriteString(`"}`)
	}
	input.WriteString(`]}}`)
	src, _ := opIndexParseRPC(t, set, input.String())
	return dst, op, src
}

func TestMergeDistinctKeylessRPCListsWithinDefaultBudget(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input { list l { leaf v { type string; } } } } }`)
	dst, op, src := round8KeylessMerge(t, set, 4096)
	dst.work.Store(0)
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	if got := len(opidxSchemaNodes(op, src.top.first().kids.At(0).schema)); got != 4096 {
		t.Fatalf("merged keyless instances = %d, want 4096", got)
	}
	opidxAssertTreeInvariant(t, dst)
}

func TestMergeDistinctKeylessRPCListsScaling(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input { list l { leaf v { type string; } } } } }`)
	measure := func(count int) int64 {
		dst, _, src := round8KeylessMerge(t, set, count)
		dst.work.Store(0)
		if err := dst.Merge(src); err != nil {
			t.Fatal(err)
		}
		return dst.work.Load()
	}
	small, large := measure(512), measure(1024)
	t.Logf("keyless merge work: 512=%d, 1024=%d", small, large)
	if small == 0 || large > 3*small {
		t.Fatalf("doubling disjoint keyless input changed work from %d to %d", small, large)
	}
}

func TestMutationHashLimitFaultInjection(t *testing.T) {
	opSet := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input { list l { leaf v { type string; } } } } }`)
	dataSet := storedHashSet(t, "string")
	type faultCase struct {
		build func() ([]*Tree, func() error)
	}
	cases := map[string]faultCase{
		"Insert": {build: func() ([]*Tree, func() error) {
			src, dst, parent := round8CopiedRPCChildren(t, opSet)
			return []*Tree{src, dst}, func() error { return dst.Insert(parent, src.top.first()) }
		}},
		"Merge": {build: func() ([]*Tree, func() error) {
			dst, _, src := round8KeylessMerge(t, opSet, 4)
			return []*Tree{dst}, func() error { return dst.Merge(src) }
		}},
		"ApplyDiff": {build: func() ([]*Tree, func() error) {
			before, _, err := parseJSONString(dataSet,
				`{"m:r":{"a":"a","b":"b","c":"c","s":["old"]}}`, Reject, true)
			if err != nil {
				t.Fatal(err)
			}
			after, _, err := parseJSONString(dataSet,
				`{"m:r":{"a":"a","b":"b","c":"c","s":["old","new"]}}`, Reject, true)
			if err != nil {
				t.Fatal(err)
			}
			diff, err := Diff(before, after, DiffOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return []*Tree{before}, func() error { return before.ApplyDiff(diff, ApplyDiffOptions{}) }
		}},
	}
	for _, name := range []string{"Insert", "Merge", "ApplyDiff"} {
		t.Run(name, func(t *testing.T) {
			full, apply := cases[name].build()
			if err := apply(); err != nil {
				t.Fatal("unbounded mutation:", err)
			}
			need := make([]uint64, len(full))
			want := make([][]string, len(full))
			for i, tr := range full {
				need[i], want[i] = tr.work.hashUsed, typedDump(tr)
			}
			for limitedTree, operationNeed := range need {
				for limit := uint64(1); limit <= operationNeed; limit++ {
					trial, mutate := cases[name].build()
					before := make([]hashBudgetSnapshot, len(trial))
					for i, tr := range trial {
						before[i] = snapshotHashBudgetTree(tr)
					}
					trial[limitedTree].work.hashLimit = limit
					err := mutate()
					if errors.Is(err, yang.ErrBudget) {
						for i, tr := range trial {
							if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(after, before[i]) {
								t.Fatalf("limit %d changed tree %d", limit, i)
							}
							opidxAssertTreeInvariant(t, tr)
						}
						continue
					}
					if err != nil {
						t.Fatalf("limit %d: %v", limit, err)
					}
					for i, tr := range trial {
						if got := typedDump(tr); !slices.Equal(got, want[i]) {
							t.Fatalf("limit %d successful tree %d differs from unbounded result", limit, i)
						}
						opidxAssertTreeInvariant(t, tr)
					}
				}
			}
		})
	}
}
