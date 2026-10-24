// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
)

func TestKeylessAddedPreservesPhysicalAndRegistrationOrder(t *testing.T) {
	set := round13Set(t)
	dst, op := round13RPC(t, set, round13MergeTarget)
	if _, _, ds, err := parseOp(context.Background(), strings.NewReader(round13Reply), set, FormatJSON, opReply, op, Reject); err != nil {
		t.Fatal(err, ds)
	}
	old := op.kids.list[1]
	cache := &dupCache{}
	if _, err := dst.keylessFirst(cache, &op.kids, old); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.dupInstNext(old, cache); err != nil {
		t.Fatal(err)
	}
	consumed := cache.inst[old]
	for range 4 {
		_, sourceOp := round13RPC(t, set, round13MergeTarget)
		n := sourceOp.kids.list[0]
		if err := n.Unlink(); err != nil {
			t.Fatal(err)
		}
		if err := dst.insert(op, n, insertDefault); err != nil {
			t.Fatal(err)
		}
		cache.added(n, &dst.work)
		if err := workError(&dst.work); err != nil {
			t.Fatal(err)
		}
		if cache.inst[old] != consumed || consumed.used != 1 {
			t.Fatal("consumed native set was reset")
		}
		if err := opidxCheckKeylessCache(dst, cache); err != nil {
			t.Fatal(err)
		}
		// Repopulate any table that was invalidated by the native branch change.
		if _, err := dst.keylessFirst(cache, &op.kids, n); err != nil {
			t.Fatal(err)
		}
		if _, err := dst.run(cache, n); err != nil {
			t.Fatal(err)
		}
		if err := opidxCheckKeylessCache(dst, cache); err != nil {
			t.Fatal(err)
		}
	}
	if op.kids.ht == nil {
		t.Fatal("did not cross children-table creation")
	}
}

func TestAstra305Round13HashBudgetRollback(t *testing.T) {
	set := round13Set(t)
	for _, operation := range []string{"apply", "merge"} {
		t.Run(operation, func(t *testing.T) {
			build := func() (*Tree, func() error) {
				target, source := round13ApplyTarget, round13ApplySource
				if operation == "merge" {
					target, source = round13MergeTarget, round13MergeSource
				}
				dst, op := round13RPC(t, set, target)
				src, _ := round13RPC(t, set, source)
				if operation == "merge" {
					if _, _, ds, err := parseOp(context.Background(), strings.NewReader(round13Reply), set, FormatJSON, opReply, op, Reject); err != nil {
						t.Fatal(err, ds)
					}
					return dst, func() error { return dst.Merge(src) }
				}
				return dst, func() error { return dst.ApplyDiff(src, ApplyDiffOptions{}) }
			}
			full, apply := build()
			var points []uint64
			full.work.hashTrace = &points
			if err := apply(); err != nil {
				t.Fatal(err)
			}
			seen := map[uint64]bool{}
			refused := 0
			for _, at := range points {
				if at <= 1 || seen[at] {
					continue
				}
				seen[at] = true
				dst, apply := build()
				before := snapshotHashBudgetTree(dst)
				dst.work.hashLimit = at - 1
				err := apply()
				if err == nil && dst.work.hashErr == nil && dst.work.hashUsed < at {
					// A speculative preflight may exceed the eventual total, and
					// immutable schema caches can reduce later operations' work.
					continue
				}
				if !errors.Is(err, yang.ErrBudget) {
					t.Fatalf("limit %d: %v", at-1, err)
				}
				refused++
				if !reflect.DeepEqual(before, snapshotHashBudgetTree(dst)) {
					t.Fatalf("limit %d: tree changed on budget refusal", at-1)
				}
				opidxAssertTreeInvariant(t, dst)
			}
			if refused == 0 {
				t.Fatal("no budget refusals reached")
			}
			t.Logf("checked %d budget refusals", refused)
		})
	}
}

func TestSplitRBRebuildCountsRegisteredMembers(t *testing.T) {
	tr, op := round13SplitCopy(t, round13Set(t), 1)
	if _, err := tr.NewTerm(op, "v", "3", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	opidxAssertTreeInvariant(t, tr)
	var values []*Node
	for _, n := range op.kids.list {
		if n.Name() == "v" {
			values = append(values, n)
		}
	}
	if len(values) != 3 || values[2].inRB || op.kids.order.entries[values[0].schema].rb != 2 {
		t.Fatal("rebuild did not retain exactly the first physical run's two RB members")
	}
}
