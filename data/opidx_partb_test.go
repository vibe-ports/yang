// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

var opidxPartBSizes = scaleSizes([]int{128, 256, 512, 1024}, 3)

func opidxPartBNLogN(t *testing.T, sizes []int, measure func(int) int64) {
	t.Helper()
	var previous float64
	for _, n := range sizes {
		work := float64(measure(n))
		normalized := work / (float64(n) * math.Log2(float64(n)+1))
		t.Logf("n=%d work=%.0f work/(n log2 n)=%.2f", n, work, normalized)
		if previous != 0 && normalized > 1.5*previous {
			t.Errorf("normalized work grew from %.2f to %.2f", previous, normalized)
		}
		previous = normalized
	}
}

func opidxPartBReply(t *testing.T, set *schema.Set, reply string) (*Tree, *Node) {
	t.Helper()
	tr, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"m:r":{}}`), set,
		FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	if _, _, diags, err = parseOp(context.Background(), strings.NewReader(reply), set,
		FormatJSON, opReply, op, Reject); err != nil {
		t.Fatal(err, diags)
	}
	return tr, op
}

func TestOpIndexMergeIdenticalReplyScaling(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 rpc r { output { leaf-list v { type uint32; } } } }`)
	opIndexScaling(t, opidxPartBSizes, func(n int) int64 {
		reply := `{"m:v":[` + opIndexValues(n, false) + `]}`
		dst, _ := opidxPartBReply(t, set, reply)
		src, _ := opidxPartBReply(t, set, reply)
		dst.work.Store(0)
		dst.work.visits, dst.work.shifts = true, true
		if err := dst.Merge(src); err != nil {
			t.Fatal(err)
		}
		return dst.work.Load()
	})
}

func TestOpIndexSplitValueChangeScaling(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 rpc r { input { leaf-list v { type uint32; ordered-by user; } }
 output { leaf split { type string; } } } }`)
	mod := opIndexModule(t, set)
	opIndexScaling(t, opidxPartBSizes, func(n int) int64 {
		tr, op := opIndexParseRPC(t, set, `{"m:r":{"v":[`+opIndexValues(n, false)+`]}}`)
		b := newBuilder{set: set, log: &logger{set: set}}
		if _, err := b.newTerm(op, mod, "split", "x", newValOptions{output: true}); err != nil {
			t.Fatal(err)
		}
		vs := schema.FindChild(op.schema, nil, mod, "v", 0)
		last := opIndexTerm(t, vs, n)
		mustLink(t, tr.insert(op, last, insertLast))
		if op.kids.order.entries[vs].gaps != 1 {
			t.Fatal("fixture did not split the value run")
		}
		nodes := op.kids.opInst(vs)
		tr.work.Store(0)
		tr.work.visits, tr.work.shifts = true, true
		for i, node := range nodes[:n] {
			if changed, err := node.SetValue(strconv.Itoa(2*n + i)); err != nil || !changed {
				t.Fatalf("SetValue(%d): changed=%t error=%v", i, changed, err)
			}
		}
		return tr.work.Load()
	})
}

func TestOpIndexXMLDistinctOpaqueNamesScaling(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m; rpc r; }`)
	opidxPartBNLogN(t, opidxPartBSizes, func(n int) int64 {
		var input strings.Builder
		input.WriteString(`<r xmlns="urn:m">`)
		for i := range n {
			fmt.Fprintf(&input, `<u%d/>`, i)
		}
		input.WriteString(`</r>`)
		var lc *lydCtx
		opts := parseOpts{ParseOptions: ParseOptions{Unknown: Opaque, ParseOnly: true}}
		tr, diags, err := parseWith(context.Background(), strings.NewReader(input.String()), set, opts, parseXML, func(c *lydCtx) {
			lc = c
			c.op = opOpts{rpc: true, action: true, noSiblings: true}
			c.rcOnly = true
			c.tree.work.visits, c.tree.work.shifts = true, true
		})
		if err != nil {
			t.Fatal(err, diags)
		}
		if lc.opNode == nil || len(lc.opNode.kids.opq) != n {
			t.Fatalf("opaque children = %d, want %d", len(lc.opNode.kids.opq), n)
		}
		return tr.work.Load()
	})
}

func TestOpIndexXMLOpaqueAnchorIgnoresSchemaPrefix(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 container c { leaf-list v { type uint32; ordered-by user; } } }`)
	opIndexScaling(t, []int{256, 512, 1024, 2048}, func(n int) int64 {
		var input strings.Builder
		input.WriteString(`<c xmlns="urn:m"><v>`)
		input.WriteString(strings.Repeat(`1</v><v>`, n-1))
		input.WriteString(`1</v>`)
		for range n {
			input.WriteString(`<unknown>x</unknown>`)
		}
		input.WriteString(`</c>`)
		var reached bool
		opts := parseOpts{ParseOptions: ParseOptions{Unknown: Opaque, ParseOnly: true}}
		tr, diags, err := parseWith(context.Background(), strings.NewReader(input.String()), set, opts, parseXML, func(c *lydCtx) {
			reached = true
			c.tree.work.visits, c.tree.work.shifts = true, true
		})
		if err != nil {
			t.Fatal(err, diags)
		}
		root := tr.top.first()
		if !reached || root == nil {
			t.Fatalf("parser reached=%t root=%v", reached, root)
		}
		if len(root.kids.list) != n || len(root.kids.opq) != n {
			t.Fatalf("reached=%t children=%d schema/%d opaque; want %d each", reached, len(root.kids.list), len(root.kids.opq), n)
		}
		return tr.work.Load()
	})
}

func TestOpIndexSparseRanksScaling(t *testing.T) {
	opIndexScaling(t, opidxPartBSizes, func(n int) int64 {
		var yang strings.Builder
		yang.WriteString(`module m { yang-version 1.1; namespace "urn:m"; prefix m; rpc r { output {`)
		for i := range n {
			fmt.Fprintf(&yang, " leaf v%d { type uint32; }", i)
		}
		yang.WriteString(` } } }`)
		set := opIndexSet(t, yang.String())
		mod := opIndexModule(t, set)
		tr, op := opidxPartBReply(t, set, `{}`)
		tr.work.Store(0)
		tr.work.visits, tr.work.shifts = true, true
		for _, rank := range []int{0, n / 2, n - 1} {
			sn := schema.FindChild(op.schema, nil, mod, fmt.Sprintf("v%d", rank), schema.GetNextOutput)
			mustLink(t, tr.insert(op, opIndexTerm(t, sn, rank), insertDefault))
		}
		if got := op.kids.order.ranks[1].Len(); got != 3 {
			t.Fatalf("populated output ranks = %d, want 3 for schema width %d", got, n)
		}
		return tr.work.Load()
	})
}

func TestOpIndexKeylessMergeScaling(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 rpc r { output { list l { leaf v { type uint32; } } } } }`)
	opIndexScaling(t, opidxPartBSizes, func(n int) int64 {
		var reply strings.Builder
		reply.WriteString(`{"m:l":[`)
		for i := range n {
			if i != 0 {
				reply.WriteByte(',')
			}
			fmt.Fprintf(&reply, `{"v":%d}`, i)
		}
		reply.WriteString(`]}`)
		dst, _ := opidxPartBReply(t, set, reply.String())
		src, _ := opidxPartBReply(t, set, reply.String())
		dst.work.Store(0)
		dst.work.visits, dst.work.shifts = true, true
		if err := dst.Merge(src); err != nil {
			t.Fatal(err)
		}
		return dst.work.Load()
	})
}
