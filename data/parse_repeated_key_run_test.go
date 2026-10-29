// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestParseRepeatedKeyRunScaling checks that one list instance with a repeated key does not take
// the rest of its run out of the deferred ordered insertion. libyang keeps that instance in its
// RB tree (tree_data_sorted.c:669-716) and inserts every later instance in O(log n); an eager
// slice insertion per instance would move O(n) siblings and operation-index entries each time.
// Work counts comparisons, tree and sibling visits and slice shifts; allocated bytes are measured
// too. The final order must equal a slow reference insertion sort of the input key tuples.
func TestParseRepeatedKeyRunScaling(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r { input { list l { key k; leaf k { type int32; } } } }
  container c { list l { key k; leaf k { type int32; } } }
}`)
	shapes := map[string]func(n int) []int{
		"even-odd": func(n int) []int {
			var ks []int
			for k := 2; k <= n; k += 2 {
				ks = append(ks, k)
			}
			for k := 1; k <= n; k += 2 {
				ks = append(ks, k)
			}
			return ks
		},
		"shuffled": func(n int) []int {
			ks := make([]int, n)
			for i := range ks {
				ks[i] = (i*7919)%n + 1 // n is a power of two times 125: 7919 is coprime
			}
			return ks
		},
		"descending": func(n int) []int {
			ks := make([]int, n)
			for i := range ks {
				ks[i] = n - i
			}
			return ks
		},
	}
	input := func(top string, ks []int) string {
		var b strings.Builder
		fmt.Fprintf(&b, `{"m:%s":{"l":[{"k":0,"k":0}`, top)
		for _, k := range ks {
			fmt.Fprintf(&b, `,{"k":%d}`, k)
		}
		b.WriteString(`]}}`)
		return b.String()
	}
	type measurement struct {
		work  int64
		bytes uint64
		order []string
	}
	parse := func(t *testing.T, operation bool, in string) measurement {
		t.Helper()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		var parent *Node
		var tree *Tree
		if operation {
			tree, parent = opIndexParseRPC(t, set, in)
		} else {
			tr, diags, err := parseWith(context.Background(), strings.NewReader(in), set,
				parseOpts{ParseOptions: ParseOptions{Unknown: Reject, ParseOnly: true}}, parseJSON,
				func(c *lydCtx) { c.tree.work.visits = true })
			if err != nil {
				t.Fatal(err, diags)
			}
			tree = tr
			for n := range tree.Top() {
				parent = n
			}
		}
		runtime.ReadMemStats(&after)
		m := measurement{work: tree.work.Load(), bytes: after.TotalAlloc - before.TotalAlloc}
		for _, l := range parent.kids.SchemaAll() {
			var keys []string
			for _, k := range l.kids.SchemaAll() {
				keys = append(keys, k.value.Canonical())
			}
			m.order = append(m.order, strings.Join(keys, ","))
		}
		return m
	}
	// reference is an insertion sort of the key tuples, equal values after the earlier ones, with
	// rb_compare_lists' rule that a tuple equals its prefix.
	reference := func(ks []int) []string {
		tuples := [][]int{{0, 0}}
		for _, k := range ks {
			tuples = append(tuples, []int{k})
		}
		less := func(a, b []int) bool {
			for i := range min(len(a), len(b)) {
				if a[i] != b[i] {
					return a[i] < b[i]
				}
			}
			return false
		}
		var out [][]int
		for _, tu := range tuples {
			at := len(out)
			for at > 0 && less(tu, out[at-1]) {
				at--
			}
			out = slices.Insert(out, at, tu)
		}
		s := make([]string, len(out))
		for i, tu := range out {
			parts := make([]string, len(tu))
			for j, v := range tu {
				parts[j] = strconv.Itoa(v)
			}
			s[i] = strings.Join(parts, ",")
		}
		return s
	}
	for _, operation := range []bool{true, false} {
		top := map[bool]string{true: "r", false: "c"}[operation]
		for _, name := range []string{"even-odd", "shuffled", "descending"} {
			t.Run(fmt.Sprintf("%s/%s", top, name), func(t *testing.T) {
				for _, n := range scaleSizes([]int{500, 2000, 8000}, 1) {
					one := parse(t, operation, input(top, shapes[name](n)))
					four := parse(t, operation, input(top, shapes[name](4*n)))
					if n == 500 {
						if want := reference(shapes[name](n)); !slices.Equal(one.order, want) {
							t.Fatalf("order %v, want %v", one.order[:8], want[:8])
						}
					}
					if four.work > 6*one.work || four.bytes > 6*one.bytes {
						t.Fatalf("N=%d: work %d -> %d, allocated bytes %d -> %d: not O(n log n)",
							n, one.work, four.work, one.bytes, four.bytes)
					}
				}
			})
		}
	}
}
