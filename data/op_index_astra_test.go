// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

func TestOpIndexKeylessDigestKeepsLookupEquivalentSubtrees(t *testing.T) {
	module := `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 rpc r { input { list outer { list inner { key "id"; leaf id { type uint32; } } } } } }`
	sets := []*schema.Set{opIndexSet(t, module), opIndexSet(t, module)}
	for _, set := range sets {
		for _, mod := range set.Modules {
			mod.Owner = set
		}
	}
	parse := func(set *schema.Set, input string) (*Tree, *Node) {
		tr, op, diags, err := parseOp(context.Background(), strings.NewReader(input), set,
			FormatJSON, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		return tr, op
	}
	for _, foreign := range []bool{false, true} {
		name := "same-context"
		if foreign {
			name = "foreign-context-child"
		}
		t.Run(name, func(t *testing.T) {
			dstInput := `{"m:r":{"outer":[{"inner":[{"id":1},{"id":1}]}]}}`
			srcInput := `{"m:r":{"outer":[{"inner":[{"id":1},{"id":2}]}]}}`
			if foreign {
				dstInput, srcInput = `{"m:r":{"outer":[{}]}}`, `{"m:r":{"outer":[{}]}}`
			}
			dst, op := parse(sets[0], dstInput)
			src, srcOp := parse(sets[0], srcInput)
			if foreign {
				foreignTree, foreignOp := parse(sets[1], srcInput)
				outer := foreignOp.kids.list[0]
				unlink(outer)
				unlink(srcOp.kids.list[0])
				if err := src.link(srcOp, &srcOp.kids, outer, 0); err != nil {
					t.Fatal(err)
				}
				_ = foreignTree
			}
			cache := &dupCache{}
			if candidate := dst.keylessFirst(cache, &op.kids, srcOp.kids.list[0]); candidate == nil {
				t.Fatal("foreign keyless first-match candidate not found")
			}
			if err := opidxCheckKeylessCache(dst, cache); err != nil {
				t.Fatal(err)
			}
			if err := dst.Merge(src); err != nil {
				t.Fatal(err)
			}
			var outer int
			for n := range op.kids.all() {
				if n.Name() == "outer" {
					outer++
				}
			}
			if outer != 1 {
				t.Fatalf("outer instances after merge = %d, want 1", outer)
			}
		})
	}
}

func TestOpIndexForeignLeafListDelete(t *testing.T) {
	for _, tc := range []struct {
		name, schema, before, after string
	}{
		{
			name:   "without-table",
			schema: `leaf-list v { type uint32; }`,
			before: `{"m:r":{"v":[1]}}`, after: `{"m:r":{}}`,
		},
		{
			name: "with-table",
			schema: `leaf a { type string; } leaf b { type string; } leaf c { type string; }
 leaf-list v { type uint32; }`,
			before: `{"m:r":{"a":"a","b":"b","c":"c","v":[1]}}`,
			after:  `{"m:r":{"a":"a","b":"b","c":"c"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			module := `module m { yang-version 1.1; namespace "urn:m"; prefix m; rpc r { input {` + tc.schema + `} } }`
			a, b := opIndexSet(t, module), opIndexSet(t, module)
			for _, set := range []*schema.Set{a, b} {
				for _, mod := range set.Modules {
					mod.Owner = set
				}
			}
			parse := func(set *schema.Set, input string) (*Tree, *Node) {
				tr, op, diags, err := parseOp(context.Background(), strings.NewReader(input), set,
					FormatJSON, opRPC, nil, Reject)
				if err != nil {
					t.Fatal(err, diags)
				}
				return tr, op
			}
			target, _ := parse(a, tc.before)
			_, oldOp := parse(b, tc.before)
			_, newOp := parse(b, tc.after)
			diff, err := DiffTree(oldOp, newOp, DiffOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := target.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
				t.Fatalf("ApplyDiff foreign delete: %v", err)
			}
			if n, err := target.Find("/m:r/v[.='1']"); err != nil || n != nil {
				t.Fatalf("deleted leaf-list = %v, %v", n, err)
			}
		})
	}
}

func TestOpIndexKeylessDuplicateSetUsesRegistrationOrder(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 rpc r { input { list l { leaf v { type string; } } leaf split { type string; } } } }`)
	for _, split := range []bool{false, true} {
		name := "reordered"
		if split {
			name = "split-run"
		}
		t.Run(name, func(t *testing.T) {
			tr, op := opIndexParseRPC(t, set,
				`{"m:r":{"l":[{"v":"x"},{"v":"x"},{"v":"x"},{"v":"x"}],"split":"s"}}`)
			var lists []*Node
			var separator *Node
			for _, n := range op.kids.list {
				if n.Name() == "l" {
					lists = append(lists, n)
				} else {
					separator = n
				}
			}
			if len(lists) != 4 || separator == nil || op.kids.ht == nil {
				t.Fatal("fixture needs four keyless instances, a separator, and a children table")
			}
			op.kids.touch()
			if split {
				op.kids.list = []*Node{lists[0], lists[1], separator, lists[3], lists[2]}
			} else {
				op.kids.list = []*Node{lists[0], lists[3], lists[1], lists[2], separator}
			}
			op.kids.orderBuild(tr)
			cache := &dupCache{}
			var got []*Node
			for range lists {
				got = append(got, tr.dupInstNext(lists[0], cache))
			}
			if !slices.Equal(got, lists) {
				t.Fatalf("duplicate registration order = %p, want %p", got, lists)
			}
		})
	}
}

func TestOpIndexParsedRBMembersDriveLaterInsertion(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 rpc r { input { leaf-list v { type uint32; } } } }`)
	dst, op := opIndexParseRPC(t, set, `{"m:r":{"v":[1,3]}}`)
	_, source := opIndexParseRPC(t, set, `{"m:r":{"v":[5,7]}}`)
	if _, _, err := dst.DupSiblings(source.kids.list[0], op, DupOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.NewTerm(op, "v", "2", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range op.kids.list {
		got = append(got, n.Value())
	}
	if want := []string{"1", "2", "3", "5", "7"}; !slices.Equal(got, want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
}

func TestOpIndexAscendingNewTermAllocationScaling(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 rpc r { input { leaf-list v { type uint32; } } } }`)
	measure := func(n int) (int64, uint64) {
		tr, op := opIndexParseRPC(t, set, `{"m:r":{}}`)
		tr.work.Store(0)
		tr.work.visits, tr.work.shifts = true, true
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for i := range n {
			if _, err := tr.NewTerm(op, "v", strconv.Itoa(i), NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		runtime.ReadMemStats(&after)
		return tr.work.Load(), after.TotalAlloc - before.TotalAlloc
	}
	w1, a1 := measure(512)
	w2, a2 := measure(1024)
	t.Logf("n=512 work=%d alloc=%d; n=1024 work=%d alloc=%d", w1, a1, w2, a2)
	if w2 > 3*w1 {
		t.Fatalf("counted work doubled input by more than 3x: %d -> %d", w1, w2)
	}
	if a2 > 3*a1 {
		t.Fatalf("allocated bytes doubled input by more than 3x: %d -> %d", a1, a2)
	}
}
