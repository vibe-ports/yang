// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

func opidxR9OperationRun(t *testing.T, source, target *schema.Set, values []int) (*Tree, *Node, *schema.Node, []*Node, []*Node) {
	t.Helper()
	mod1 := source.Implemented("opidxu")
	_, src, diags, err := parseOp(context.Background(), strings.NewReader(
		`{"opidxu:l":{}}`), source, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	// Keep the output child first so opidxR8DupOperation exercises its native
	// recursive-copy refusal before building the LAST-linked copy by hand.
	opidxR8AddTerm(t, source, mod1, src, "o", "o", true)
	rpc1 := mod1.Child(nil, "l")
	ss1 := schema.FindChild(rpc1, nil, mod1, "s", 0)
	if ss1 == nil {
		t.Fatal("source s schema not compiled")
	}
	srcTree := treeOf(src)
	for _, value := range values {
		srcTree.insert(src, opIndexTerm(t, ss1, value), insertLast)
	}
	sourceNodes := opidxSchemaNodes(src, ss1)

	op := opidxR8DupOperation(t, src, target)
	tr := treeOf(op)
	mod2 := target.Implemented("opidxu")
	ss2 := schema.FindChild(mod2.Child(nil, "l"), nil, mod2, "s", 0)
	if ss2 == nil {
		t.Fatal("target s schema not compiled")
	}
	copyNodes := opidxSchemaNodes(op, ss2)
	if len(sourceNodes) != len(values) || len(copyNodes) != len(values) {
		t.Fatalf("source/copy instances = %d/%d, want %d", len(sourceNodes), len(copyNodes), len(values))
	}
	if op.kids.rbTree[ss2] {
		t.Fatal("LAST-linked copy unexpectedly has an RB tree")
	}
	return tr, op, ss2, sourceNodes, copyNodes
}

func opidxR9ContainerRun(t *testing.T, source, target *schema.Set, values []int) (*Tree, *Node, *schema.Node, []*Node, []*Node) {
	t.Helper()
	mod1 := opIndexModule(t, source)
	cs1 := mod1.Child(nil, "c")
	vs1 := schema.FindChild(cs1, nil, mod1, "v", 0)
	if cs1 == nil || vs1 == nil {
		t.Fatal("source container schema not compiled")
	}
	srcTree := newTree(source)
	src := newInner(cs1)
	srcTree.insert(nil, src, insertDefault)
	for _, value := range values {
		srcTree.insert(src, opIndexTerm(t, vs1, value), insertLast)
	}
	sourceNodes := opidxSchemaNodes(src, vs1)

	copyRoot, err := dupTo(src, target, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	tr := treeOf(copyRoot)
	mod2 := opIndexModule(t, target)
	vs2 := schema.FindChild(mod2.Child(nil, "c"), nil, mod2, "v", 0)
	if vs2 == nil {
		t.Fatal("target container schema not compiled")
	}
	for _, node := range sourceNodes {
		value, err := strconv.Atoi(node.value.Canonical())
		if err != nil {
			t.Fatal(err)
		}
		tr.insert(copyRoot, opIndexTerm(t, vs2, value), insertLast)
	}
	copyNodes := opidxSchemaNodes(copyRoot, vs2)
	if len(copyNodes) != len(values) {
		t.Fatalf("copy instances = %d, want %d", len(copyNodes), len(values))
	}
	if copyRoot.kids.rbTree[vs2] {
		t.Fatal("LAST-linked copy unexpectedly has an RB tree")
	}
	return tr, copyRoot, vs2, sourceNodes, copyNodes
}

// opidxR9RelocationBuckets models lyds_additionally_create_rb_nodes: the
// current maximum stays linked (equal values extend it), while every smaller
// instance is removed from its exact-value bucket and appended again.
func opidxR9RelocationBuckets(t *testing.T, sourceNodes, copyNodes []*Node) map[string][]*Node {
	t.Helper()
	if len(sourceNodes) != len(copyNodes) {
		t.Fatalf("source/copy instance mismatch: %d/%d", len(sourceNodes), len(copyNodes))
	}
	buckets := make(map[string][]*Node)
	for i, source := range sourceNodes {
		key := source.value.Canonical()
		buckets[key] = append(buckets[key], copyNodes[i])
	}
	maximum := -1
	for i, source := range sourceNodes {
		value, err := strconv.Atoi(source.value.Canonical())
		if err != nil {
			t.Fatal(err)
		}
		if value >= maximum {
			maximum = value
			continue
		}
		key := source.value.Canonical()
		bucket := buckets[key]
		at := slices.Index(bucket, copyNodes[i])
		if at < 0 {
			t.Fatalf("source instance %d missing from modeled bucket %s", i, key)
		}
		bucket = append(bucket[:at], bucket[at+1:]...)
		buckets[key] = append(bucket, copyNodes[i])
	}
	return buckets
}

func opidxR9AssertSorted(t *testing.T, parent *Node, sn *schema.Node, count int) {
	t.Helper()
	nodes := opidxSchemaNodes(parent, sn)
	if len(nodes) != count {
		t.Fatalf("sorted run has %d instances, want %d", len(nodes), count)
	}
	previous := -1
	for i, node := range nodes {
		value, err := strconv.Atoi(node.value.Canonical())
		if err != nil {
			t.Fatal(err)
		}
		if value < previous {
			t.Fatalf("sorted run decreases at %d: %d after %d", i, value, previous)
		}
		previous = value
	}
}

func TestOpIndexSortRelocationDuplicateHeavy(t *testing.T) {
	_, source, target := opidxR8Sets(t)
	opIndexScaling(t, []int{512, 1024, 2048, 4096}, func(n int) int64 {
		values := make([]int, 0, 2*n+1)
		for range n {
			values = append(values, 1)
		}
		values = append(values, 5)
		for range n {
			values = append(values, 1)
		}

		tr, op, ss, sourceNodes, copied := opidxR9OperationRun(t, source, target, values)
		wantOnes := make([]*Node, 0, 2*n)
		for i, sourceNode := range sourceNodes {
			if sourceNode.value.Canonical() == "1" {
				wantOnes = append(wantOnes, copied[i])
			}
		}
		if len(wantOnes) != 2*n {
			t.Fatalf("value-1 source instances = %d, want %d", len(wantOnes), 2*n)
		}

		tr.work.Store(0)
		tr.work.visits = true
		tr.work.shifts = true
		tr.insert(op, opIndexTerm(t, ss, 6), insertDefault)
		work := tr.work.Load()

		opidxR9AssertSorted(t, op, ss, 2*n+2)
		if got := op.kids.bucket(idxKey{s: ss, key: "1"}); !slices.Equal(got, wantOnes) {
			t.Errorf("n=%d exact-value bucket order differs: got %p, want leading then relocated trailing %p", n, got, wantOnes)
		}
		opidxCheckRecords(t, &op.kids)
		return work
	})
}

func TestOpIndexSortRelocationBucketEquivalence(t *testing.T) {
	_, operationSource, operationTarget := opidxR8Sets(t)
	const containerSource = `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  container c { leaf-list v { type uint32; ordered-by user; } }
}`
	const containerTarget = `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  container c { leaf-list v { type uint32; } }
}`
	plainSource, plainTarget := opidxR6SetPair(t, containerSource, containerTarget)

	type seed struct{ a, b uint64 }
	for _, sd := range []seed{{1, 2}, {0x241, 9}, {0xfeed, 0xbeef}} {
		rng := rand.New(rand.NewPCG(sd.a, sd.b)) //nolint:gosec // deterministic test data
		values := make([]int, 768)
		for i := range values {
			values[i] = rng.IntN(6)
		}
		// Guarantee both equal maxima and relocations independently of the RNG.
		copy(values[:8], []int{1, 1, 5, 1, 3, 5, 0, 1})

		for _, tc := range []struct {
			name  string
			build func(*testing.T, []int) (*Tree, *Node, *schema.Node, []*Node, []*Node)
		}{
			{
				name: "operation",
				build: func(t *testing.T, values []int) (*Tree, *Node, *schema.Node, []*Node, []*Node) {
					return opidxR9OperationRun(t, operationSource, operationTarget, values)
				},
			},
			{
				name: "container",
				build: func(t *testing.T, values []int) (*Tree, *Node, *schema.Node, []*Node, []*Node) {
					return opidxR9ContainerRun(t, plainSource, plainTarget, values)
				},
			},
		} {
			t.Run(fmt.Sprintf("%x-%x/%s", sd.a, sd.b, tc.name), func(t *testing.T) {
				tr, parent, sn, sourceNodes, copied := tc.build(t, values)
				want := opidxR9RelocationBuckets(t, sourceNodes, copied)
				tr.insert(parent, opIndexTerm(t, sn, 6), insertDefault)

				opidxR9AssertSorted(t, parent, sn, len(values)+1)
				for value := 0; value < 6; value++ {
					key := strconv.Itoa(value)
					if got := parent.kids.bucket(idxKey{s: sn, key: key}); !slices.Equal(got, want[key]) {
						t.Errorf("value %s bucket order differs: got %p, want %p", key, got, want[key])
					}
				}
				opidxCheckRecords(t, &parent.kids)
			})
		}
	}
}

func TestOpIndexBucketRemoveScrambled(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r {
    input { leaf keep { type string; } }
    output { leaf-list dup { type uint32; } }
  }
}`)
	opIndexScaling(t, []int{512, 1024, 2048, 4096}, func(n int) int64 {
		tr, op, sn := opidxRemovalTree(t, set, "duplicates", n)
		if sn == nil {
			t.Fatal("duplicate leaf-list schema not compiled")
		}
		remaining := opidxSchemaNodes(op, sn)
		if len(remaining) != n {
			t.Fatalf("instances = %d, want %d", len(remaining), n)
		}
		if got := len(op.kids.bucket(idxKey{s: sn, key: "1"})); got != n {
			t.Fatalf("exact-value bucket records = %d, want %d", got, n)
		}
		// Put the exact records into a permutation unrelated to sibling order. Removing
		// sibling endpoints below is then O(1) in the ordered node deques while exercising
		// removals throughout the exact bucket. With a slice-backed bucket, its two-ended
		// searches and shifts still make this loop quadratic.
		removalOrder := make([]*Node, 0, len(remaining))
		for lo, hi := 0, len(remaining)-1; lo <= hi; {
			if (hi-lo+1)%2 == 0 {
				removalOrder = append(removalOrder, remaining[hi])
				hi--
			} else {
				removalOrder = append(removalOrder, remaining[lo])
				lo++
			}
		}
		bucketOrder := make([]*Node, 0, len(remaining))
		for i := len(removalOrder) - 1; i >= 0; i-- {
			at := (len(bucketOrder) + 1) / 2
			bucketOrder = slices.Insert(bucketOrder, at, removalOrder[i])
		}
		for _, node := range bucketOrder {
			op.kids.hashRemove(node)
			op.kids.hashPut(node)
		}

		tr.work.Store(0)
		tr.work.visits = true
		tr.work.shifts = true
		for len(remaining) != 0 {
			at := 0
			if len(remaining)%2 == 0 {
				at = len(remaining) - 1
			}
			node := remaining[at]
			remaining = append(remaining[:at], remaining[at+1:]...)
			if err := freeTree(node); err != nil {
				t.Fatal(err)
			}
		}
		work := tr.work.Load()

		if got := opidxSchemaNodes(op, sn); len(got) != 0 {
			t.Errorf("%d instances remain", len(got))
		}
		if got := op.kids.bucket(idxKey{s: sn, key: "1"}); len(got) != 0 {
			t.Errorf("%d exact-value records remain", len(got))
		}
		if got := tr.schemaLookup(&op.kids, sn); got != nil {
			t.Errorf("empty first-instance lookup = %s", opidxLabel(got))
		}
		opidxCheckRecords(t, &op.kids)
		return work
	})
}
