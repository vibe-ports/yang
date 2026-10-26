// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/types"
)

// Exercise Merge and Find's value-predicate path with distinct output schemas. Public Find
// compiles input paths only, so the find case uses evalPartial with an output path segment.
func TestOpRunLookupWork(t *testing.T) {
	for _, operation := range []string{"merge", "find", "instances"} {
		for _, order := range []string{"sorted", "reverse", "shuffled", "duplicates"} {
			t.Run(operation+"/"+order, func(t *testing.T) {
				opIndexScaling(t, []int{64, 128, 256, 512}, func(n int) int64 {
					var y, reply strings.Builder
					y.WriteString(`module m { yang-version 1.1; namespace "urn:m"; prefix m; rpc r { output {`)
					reply.WriteByte('{')
					for i := range n {
						fmt.Fprintf(&y, " leaf-list l%d { type uint32; }", i)
						if i > 0 {
							reply.WriteByte(',')
						}
						value := i
						if order == "duplicates" {
							value = 0
						}
						fmt.Fprintf(&reply, `"m:l%d":[%d]`, i, value)
					}
					y.WriteString(` } } }`)
					reply.WriteByte('}')
					set := opIndexSet(t, y.String())
					parse := func() (*Tree, *Node) {
						tr, op, d, err := parseOp(context.Background(), strings.NewReader(`{"m:r":{}}`), set, FormatJSON, opRPC, nil, Reject)
						if err != nil {
							t.Fatal(err, d)
						}
						if _, _, d, err = parseOp(context.Background(), strings.NewReader(reply.String()), set, FormatJSON, opReply, op, Reject); err != nil {
							t.Fatal(err, d)
						}
						return tr, op
					}
					tr, op := parse()
					src, srcOp := parse()
					indices := make([]int, n)
					for i := range indices {
						indices[i] = i
					}
					switch order {
					case "reverse":
						slices.Reverse(indices)
					case "shuffled":
						rand.New(rand.NewPCG(13, 27)).Shuffle( //nolint:gosec // deterministic test data
							n, func(i, j int) { indices[i], indices[j] = indices[j], indices[i] })
					}
					tr.work.Store(0)
					src.work.Store(0)
					tr.work.visits, tr.work.shifts = true, true
					src.work.visits, src.work.shifts = true, true
					lv := &mergeLevel{parent: op, cache: &dupCache{}}
					for _, i := range indices {
						target := srcOp.kids.At(i)
						switch operation {
						case "merge":
							if err := tr.mergeSibling(lv, target); err != nil {
								t.Fatal(err)
							}
						case "find":
							p := types.Path{{Node: op.schema}, {Node: target.schema, Preds: []types.PathPred{{Kind: types.PredLeafList, Value: target.value}}}}
							got, used, err := tr.evalPartial(&logger{set: set}, p)
							if err != nil || used != len(p) || got != op.kids.At(i) {
								t.Fatalf("find %d: %v, %d, %v", i, got, used, err)
							}
						case "instances":
							got := slices.Collect(tr.instances(&op.kids, target.schema))
							if len(got) != 1 || got[0] != op.kids.At(i) {
								t.Fatalf("instances %d: %v", i, got)
							}
						}
					}
					if err := tr.flushMerge(lv); err != nil {
						t.Fatal(err)
					}
					work := tr.work.Load() + src.work.Load()
					if operation == "merge" && op.kids.SchemaLen() != len(indices) {
						t.Fatalf("merge: %d instances, want %d", op.kids.SchemaLen(), len(indices))
					}
					return work
				})
			})
		}
	}
}

// The hash-table transition splits v around the kept input, as in the two split-run
// oracle fixtures. Both local updates and bulk rebuilds must preserve the first-run walk.
func TestOpRunGaps(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 rpc r { input { leaf in { type string; } } output {
 leaf before { type string; } leaf-list v { type uint32; } } } }`)
	tr, op, d, err := parseOp(context.Background(), strings.NewReader(`{"m:r":{"in":"keep"}}`), set, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, d)
	}
	if _, _, d, err = parseOp(context.Background(), strings.NewReader(`{"m:before":"b","m:v":[1,2,3]}`), set, FormatJSON, opReply, op, Reject); err != nil {
		t.Fatal(err, d)
	}
	if got := slices.Collect(op.kids.all()); len(got) != 5 || got[3].Name() != "in" {
		t.Fatalf("split order: %v", got)
	}
	sn := op.kids.At(1).schema
	check := func(gaps, run int) {
		t.Helper()
		if got := op.kids.order.entries[sn].gaps; got != gaps {
			t.Fatalf("gaps = %d, want %d", got, gaps)
		}
		if got := slices.Collect(tr.opRun(&op.kids, sn)); len(got) != run {
			t.Fatalf("first run has %d nodes, want %d", len(got), run)
		}
		if got := tr.findFirst(&op.kids, opIndexTerm(t, sn, 3)); (got != nil) != (run == 3) {
			t.Fatalf("value lookup for v3 = %v, run length %d", got, run)
		}
	}
	check(1, 2)
	in := op.kids.At(3)
	if err := unlinkTree(in); err != nil {
		t.Fatal(err)
	}
	check(0, 3)
	mustLink(t, tr.insertBefore(op.kids.At(3), in))
	check(1, 2)
	op.kids.opBuild(tr)
	check(1, 2)
	if err := tr.unlinkAll([]*Node{in}); err != nil {
		t.Fatal(err)
	}
	check(0, 3)
	// A singleton split run disappears and returns, updating its own entry too.
	v3 := op.kids.At(3)
	mustLink(t, tr.insertBefore(v3, in))
	if err := unlinkTree(v3); err != nil {
		t.Fatal(err)
	}
	if got := op.kids.order.entries[sn].gaps; got != 0 {
		t.Fatalf("removed singleton run: gaps %d", got)
	}
	mustLink(t, tr.insert(op, v3, insertLast))
	check(1, 2)
}
