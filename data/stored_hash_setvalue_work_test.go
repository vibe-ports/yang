// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"testing"

	"github.com/vibe-ports/yang"
)

// Changing the maximum of a system-ordered leaf-list removes and reinserts its
// tail instance without shifting any siblings. The old linkOrigin walk back to
// the first instance made N such edits of an N-instance run quadratic.
func TestStoredHashMaxLeafListSetValueWork(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
		leaf-list v { type uint32; } }`)
	measure := func(n int) (int64, uint64) {
		tr := newTree(set)
		for i := range n {
			if _, err := tr.NewTerm(nil, "m:v", fmt.Sprint(i), NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		tail := tr.top.At(n - 1)
		tr.work.Store(0)
		tr.work.visits, tr.work.shifts = true, true
		var charged uint64
		for i := range n {
			if changed, err := tail.SetValue(fmt.Sprint(n + i)); err != nil || !changed {
				t.Fatalf("SetValue %d: changed=%t error=%v", i, changed, err)
			}
			charged += tr.work.hashUsed
			if tr.top.At(n-1) != tail {
				t.Fatalf("SetValue %d moved maximum away from tail", i)
			}
		}
		return tr.work.Load(), charged
	}
	baseWork, baseHash := measure(128)
	largeWork, largeHash := measure(512)
	if largeWork > 6*baseWork || largeHash > 6*baseHash {
		t.Fatalf("4N maximum edits: counted work %d -> %d, charged hash work %d -> %d",
			baseWork, largeWork, baseHash, largeHash)
	}
	tr := newTree(set)
	for i := range 32 {
		if _, err := tr.NewTerm(nil, "m:v", fmt.Sprint(i), NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	tail := tr.top.At(31)
	tr.work.hashLimit = 1
	if changed, err := tail.SetValue("32"); changed || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("budget: changed=%t error=%v", changed, err)
	}
	if tail.Value() != "31" || tr.top.At(31) != tail {
		t.Fatal("budget refusal changed the ordered run")
	}
	tr.work.hashLimit = 2
	end := tr.work.beginHashWork()
	_, _, err := tr.linkOrigin(tr.top.At(16))
	end()
	if !errors.Is(err, yang.ErrBudget) || tr.work.hashUsed != 2 || tr.top.At(16).Value() != "16" {
		t.Fatalf("middle-position lookup: error=%v charged=%d", err, tr.work.hashUsed)
	}
}

// Removing an interior member of a single sorted run does not join two trees.
// Ascending inserts must retain the append path after that removal.
func TestStoredHashMiddleRemovalAppendWork(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
		leaf-list v { type uint32; } }`)
	measure := func(n int) int64 {
		t.Helper()
		tr := newTree(set)
		for i := range n {
			if _, err := tr.NewTerm(nil, "m:v", fmt.Sprint(i), NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if err := tr.top.At(n / 2).Remove(); err != nil {
			t.Fatal(err)
		}
		tr.work.Store(0)
		tr.work.visits, tr.work.shifts = true, true
		for i := range n {
			if _, err := tr.NewTerm(nil, "m:v", fmt.Sprint(n+i), NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		if tr.top.unsorted[tr.top.At(0)] || tr.top.At(tr.top.SchemaLen()-1).Value() != fmt.Sprint(2*n-1) {
			t.Fatal("middle removal lost the sorted append path")
		}
		return tr.work.Load()
	}
	base, large := measure(128), measure(512)
	if large > 6*base {
		t.Fatalf("4N ascending appends after middle removal: counted work %d -> %d", base, large)
	}
}
