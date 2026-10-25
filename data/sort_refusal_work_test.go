// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// This test is self-contained so the exact workload can also run against origin/main.
// Compare enforced work on ascending, descending and duplicate-heavy mixed-context runs.
func TestStage4SortWork(t *testing.T) {
	makeSchema := func() (*schema.Set, *schema.Node) {
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fstest.MapFS{"sort-work.yang": {Data: []byte(`module sort-work { yang-version 1.1; namespace "urn:sort-work"; prefix s; leaf-list v { type uint32; } }`)}})
		if err != nil {
			t.Fatal(err)
		}
		if _, diags, err := c.Load("sort-work", "", nil); err != nil {
			t.Fatal(err, diags)
		}
		set := c.Snapshot()
		for _, m := range set.Modules {
			for _, sn := range m.Top {
				if sn.Name == "v" {
					return set, sn
				}
			}
		}
		t.Fatal("no v schema")
		return nil, nil
	}
	for _, duplicate := range []bool{false, true} {
		for _, descending := range []bool{false, true} {
			var previous uint64
			for _, n := range []int{128, 256} {
				a, sa := makeSchema()
				_, sb := makeSchema()
				tr := newTree(a)
				insert := func(sn *schema.Node, v int) {
					value, d := types.Store(sn.Type, fmt.Sprint(v), types.FormatJSON, types.JSONHints("number"), types.ModuleNames{Set: sn.Module.Owner}, sn)
					if d != nil {
						t.Fatal(d)
					}
					if err := tr.insert(nil, newTerm(sn, value), insertDefault); err != nil {
						t.Fatal(err)
					}
				}
				for i := range n {
					v := i
					if descending {
						v = n - i
					}
					if duplicate {
						v = 1
					}
					insert(sa, v)
				}
				tr.work.hashUsed = 0
				tr.work.Store(0)
				tr.work.visits, tr.work.shifts = true, true
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				for i := range n {
					v := n + i
					if descending {
						v = 2*n - i
					}
					if duplicate {
						v = 1
					}
					insert(sb, v)
				}
				runtime.ReadMemStats(&after)
				used := tr.work.hashUsed
				t.Logf("duplicates=%t descending=%t n=%d enforced=%d counted=%d allocated=%d", duplicate, descending, n, used, tr.work.Load(), after.TotalAlloc-before.TotalAlloc)
				if previous != 0 && used > 3*previous {
					t.Fatal("insertion work became quadratic")
				}
				previous = used
			}
		}
	}
}

// Autodelete checks the removal boundary before producing its diff, then performs the batch.
// The two native reachability proofs must remain linear, including their allocation work.
func TestStage4AutodelWork(t *testing.T) {
	var previous int64
	for _, n := range []int{1024, 2048} {
		f := newDfltFixture()
		tr := newTree(f.set)
		vc, _, _, typ := f.ctx(t, tr, false)
		top := newInner(f.top)
		if err := tr.insert(nil, top, insertDefault); err != nil {
			t.Fatal(err)
		}
		f.ll.Type = &schema.Type{Base: schema.Uint32}
		for i := range n {
			d := f.term(t, f.ll, fmt.Sprint(i), FlagDefault)
			if err := tr.insert(top, d, insertDefault); err != nil {
				t.Fatal(err)
			}
			typ.add(d)
		}
		if err := tr.insert(top, f.term(t, f.ll, fmt.Sprint(n), FlagNew), insertDefault); err != nil {
			t.Fatal(err)
		}
		tr.work.Store(0)
		tr.work.hashUsed = 0
		tr.work.visits = true
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		if err := vc.validateNew(top, nil, nil); err != nil {
			t.Fatal(err)
		}
		runtime.ReadMemStats(&after)
		counted := tr.work.Load()
		t.Logf("n=%d enforced=%d counted=%d allocated=%d", n, tr.work.hashUsed, counted, after.TotalAlloc-before.TotalAlloc)
		if previous != 0 && counted > 3*previous {
			t.Fatal("batch native-search preflight became superlinear")
		}
		previous = counted
	}
}

// Long ordered key profiles must be built once, even when the first key decides every descent.
func TestStage4KeySortWork(t *testing.T) {
	makeSchema := func() (*schema.Set, *schema.Node) {
		a, b := strings.Repeat("a", 256), strings.Repeat("b", 256)
		module := `module key-work { yang-version 1.1; namespace "urn:key-work"; prefix k; list l { key "` + a + ` ` + b + `"; leaf ` + a + ` { type uint32; } leaf ` + b + ` { type uint32; } } }`
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fstest.MapFS{"key-work.yang": {Data: []byte(module)}})
		if err != nil {
			t.Fatal(err)
		}
		if _, diags, err := c.Load("key-work", "", nil); err != nil {
			t.Fatal(err, diags)
		}
		set := c.Snapshot()
		for _, m := range set.Modules {
			for _, sn := range m.Top {
				if sn.Name == "l" {
					return set, sn
				}
			}
		}
		t.Fatal("no l schema")
		return nil, nil
	}
	var previous uint64
	for _, n := range []int{128, 256} {
		set, a := makeSchema()
		_, b := makeSchema()
		tr := newTree(set)
		insert := func(sn *schema.Node, i int) {
			l := newInner(sn)
			for j, key := range sn.Keys {
				lexical := "1"
				if j == 0 {
					lexical = fmt.Sprint(i)
				}
				v, d := types.Store(key.Type, lexical, types.FormatJSON, types.JSONHints("number"), types.ModuleNames{Set: key.Module.Owner}, key)
				if d != nil {
					t.Fatal(d)
				}
				if err := tr.insert(l, newTerm(key, v), insertDefault); err != nil {
					t.Fatal(err)
				}
			}
			if err := tr.insert(nil, l, insertDefault); err != nil {
				t.Fatal(err)
			}
		}
		for i := range n {
			insert(a, i)
		}
		tr.work.Store(0)
		tr.work.hashUsed = 0
		tr.work.visits, tr.work.shifts = true, true
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for i := range n {
			insert(b, n+i)
		}
		runtime.ReadMemStats(&after)
		used := tr.work.hashUsed
		t.Logf("n=%d enforced=%d counted=%d allocated=%d", n, used, tr.work.Load(), after.TotalAlloc-before.TotalAlloc)
		if previous != 0 && used > 3*previous {
			t.Fatal("key-profile work became quadratic")
		}
		previous = used
	}
}
