// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

var opIndexSizes = func() []int {
	if raceEnabled {
		// Smaller runs retain the 2x and 4x work ratios under race instrumentation.
		return []int{256, 512, 1024}
	}
	return []int{2048, 4096, 8192, 16384}
}()

// scaleSizes keeps the first keep of the ascending sizes of a scaling test under the race
// detector. The tests compare counted work (and allocations) between sizes, which the detector's
// instrumentation does not change, only slows down ~10x; the remaining pairs still separate linear
// or n log n growth from quadratic growth, and the full sizes run in the non-race (386) suite.
func scaleSizes(sizes []int, keep int) []int {
	if raceEnabled {
		return sizes[:keep]
	}
	return sizes
}

func opIndexSet(t *testing.T, yang string) *schema.Set {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "m.yang"), []byte(yang), 0o600); err != nil {
		t.Fatal(err)
	}
	return opsSet(t, opsFixture{dir: dir, searchdirs: []string{"."}, modules: []struct {
		name, revision string
		features       []string
	}{{name: "m"}}})
}

func opIndexModule(t *testing.T, set *schema.Set) *schema.Module {
	t.Helper()
	for _, mod := range set.Modules {
		if mod.Name == "m" {
			return mod
		}
	}
	t.Fatal("module m not compiled")
	return nil
}

func opIndexScaling(t *testing.T, sizes []int, work func(int) int64) {
	t.Helper()
	works := make([]int64, len(sizes))
	for i, n := range sizes {
		works[i] = work(n)
	}
	t.Logf("sizes %v: work %v", sizes, works)
	for i := 1; i < len(sizes); i++ {
		if 2*works[i] > 5*works[i-1] {
			t.Errorf("work(%d)=%d exceeds 2.5 * work(%d)=%d", sizes[i], works[i], sizes[i-1], works[i-1])
		}
		if i > 1 && works[i] > 6*works[i-2] {
			t.Errorf("work(%d)=%d exceeds 6 * work(%d)=%d", sizes[i], works[i], sizes[i-2], works[i-2])
		}
	}
	// An 8x input permits the log factor of the order-statistic and occurrence
	// trees (8*log2(last)/log2(first), plus fixed setup), but still rejects n².
	if works[len(works)-1] > 14*works[0] {
		t.Errorf("work(%d)=%d exceeds 14 * work(%d)=%d", sizes[len(sizes)-1], works[len(works)-1], sizes[0], works[0])
	}
}

func opIndexValues(n int, descending bool) string {
	var b strings.Builder
	for i := range n {
		if i != 0 {
			b.WriteByte(',')
		}
		v := i
		if descending {
			v = n - 1 - i
		}
		b.WriteString(strconv.Itoa(v))
	}
	return b.String()
}

func opIndexTerm(t *testing.T, sn *schema.Node, value int) *Node {
	t.Helper()
	v, d := types.Store(sn.Type, strconv.Itoa(value), types.FormatJSON, types.JSONHints("number"), nil, sn)
	if d != nil {
		t.Fatal(d.Msg)
	}
	return newTerm(sn, v)
}

// opIndexParseRPC is parseOp's request setup with the test counters enabled before parsing.
func opIndexParseRPC(t *testing.T, set *schema.Set, input string) (*Tree, *Node) {
	t.Helper()
	var lc *lydCtx
	o := parseOpts{ParseOptions: ParseOptions{Unknown: Reject, ParseOnly: true}}
	tree, diags, err := parseWith(context.Background(), strings.NewReader(input), set, o, parseJSON, func(c *lydCtx) {
		lc = c
		c.op = opOpts{rpc: true, action: true, noSiblings: true}
		c.rcOnly = true
		c.tree.work.visits = true
		c.tree.work.shifts = true
	})
	if err != nil {
		t.Fatal(err, diags)
	}
	return tree, lc.opNode
}

func TestOpIndexDescendingInput(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r { input { leaf-list v { type uint32; } } }
}`)
	mod := opIndexModule(t, set)
	sn := schema.FindChild(mod.Child(nil, "r"), nil, mod, "v", 0)
	if sn == nil {
		t.Fatal("input leaf-list v not compiled")
	}

	for _, tc := range []struct {
		name       string
		descending bool
	}{
		{name: "descending-insert", descending: true},
		{name: "ascending-insert"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opIndexScaling(t, opIndexSizes, func(n int) int64 {
				tree, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"m:r":{}}`), set,
					FormatJSON, opRPC, nil, Reject)
				if err != nil {
					t.Fatal(err, diags)
				}
				tree.work.Store(0)
				tree.work.shifts = true
				for i := range n {
					v := i
					if tc.descending {
						v = n - 1 - i
					}
					mustLink(t, tree.insert(op, opIndexTerm(t, sn, v), insertDefault))
				}
				return tree.work.Load()
			})
		})
	}

	t.Run("descending-parse", func(t *testing.T) {
		opIndexScaling(t, opIndexSizes, func(n int) int64 {
			input := `{"m:r":{"v":[` + opIndexValues(n, true) + `]}}`
			tree, _, diags, err := parseOp(context.Background(), strings.NewReader(input), set,
				FormatJSON, opRPC, nil, Reject)
			if err != nil {
				t.Fatal(err, diags)
			}
			return tree.work.Load()
		})
	})
}

func TestOpIndexAbsentSiblings(t *testing.T) {
	// Under race, keep two sizes so the 2.5x work bound still rejects quadratic growth.
	sizes := scaleSizes([]int{256, 512, 1024, 2048}, 2)
	opIndexScaling(t, sizes, func(n int) int64 {
		var y strings.Builder
		y.WriteString(`module m { yang-version 1.1; namespace "urn:m"; prefix m; rpc r { output { leaf-list v { type uint32; }`)
		for i := range n {
			fmt.Fprintf(&y, " leaf a%d { type string; }", i)
		}
		y.WriteString(` leaf tail { type string; } } } }`)
		set := opIndexSet(t, y.String())
		tree, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"m:r":{}}`), set,
			FormatJSON, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		tree.work.Store(0)
		tree.work.visits = true
		tree.work.shifts = true
		reply := `{"m:tail":"t","m:v":[` + opIndexValues(n, false) + `]}`
		if _, _, diags, err = parseOp(context.Background(), strings.NewReader(reply), set,
			FormatJSON, opReply, op, Reject); err != nil {
			t.Fatal(err, diags)
		}
		return tree.work.Load()
	})
}

func TestOpIndexManySortedRuns(t *testing.T) {
	// Under race, keep two sizes so the 2.5x work bound still rejects quadratic growth.
	sizes := scaleSizes([]int{256, 512, 1024, 2048}, 2)
	opIndexScaling(t, sizes, func(n int) int64 {
		var y, input strings.Builder
		y.WriteString(`module m { yang-version 1.1; namespace "urn:m"; prefix m; rpc r { input {`)
		input.WriteString(`{"m:r":{`)
		for i := range n {
			fmt.Fprintf(&y, " leaf-list l%d { type uint32; }", i)
			if i != 0 {
				input.WriteByte(',')
			}
			fmt.Fprintf(&input, `"l%d":[1,0]`, i)
		}
		y.WriteString(` } } }`)
		input.WriteString(`}}`)
		set := opIndexSet(t, y.String())
		tree, _ := opIndexParseRPC(t, set, input.String())
		return tree.work.Load()
	})
}

func TestOpIndexPrependUserOrdered(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r { output {
    leaf-list u { type uint32; ordered-by user; }
    leaf w { type string; }
  } }
}`)
	opIndexScaling(t, opIndexSizes, func(n int) int64 {
		tree, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"m:r":{}}`), set,
			FormatJSON, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		if _, _, diags, err = parseOp(context.Background(), strings.NewReader(`{"m:u":[0]}`), set,
			FormatJSON, opReply, op, Reject); err != nil {
			t.Fatal(err, diags)
		}
		first := op.kids.At(0)
		sn := first.schema
		tree.work.Store(0)
		tree.work.shifts = true
		for i := range n {
			fresh := opIndexTerm(t, sn, i+1)
			mustLink(t, tree.insertBefore(first, fresh))
			first = fresh
		}
		return tree.work.Load()
	})
}

func TestOpIndexUnlinkClearsRetainedPointers(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r { input { leaf-list v { type uint32; } } }
}`)
	for _, tc := range []struct {
		name string
		at   int
	}{
		{name: "first", at: 0},
		{name: "middle", at: 2},
		{name: "last", at: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree, op, diags, err := parseOp(context.Background(), strings.NewReader(
				`{"m:r":{"v":[0,1,2,3,4]}}`), set, FormatJSON, opRPC, nil, Reject)
			if err != nil {
				t.Fatal(err, diags)
			}
			removed := op.kids.At(tc.at)
			if err := unlinkTree(removed); err != nil {
				t.Fatal(err)
			}
			assertDequeDoesNotRetain(t, "siblings", &op.kids.nodeDeque, removed)
			if op.kids.order.byNode[removed] != nil {
				t.Error("operation index retains removed node")
			}
			runtime.KeepAlive(tree)
		})
	}
}

func assertDequeDoesNotRetain(t *testing.T, name string, d *nodeDeque, removed *Node) {
	t.Helper()
	if d.retainsBase(removed) {
		t.Errorf("%s base retains removed node", name)
	}
	if d.retainsList(removed) {
		t.Errorf("%s list backing array retains removed node", name)
	}
}
