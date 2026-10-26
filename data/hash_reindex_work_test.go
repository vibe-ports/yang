// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/types"
)

const hashReindexSchema = `module m {
  yang-version 1.1; namespace "urn:hash-reindex"; prefix m;
  container r {
    leaf target { type int32; }
    leaf-list v { config false; type union {
      type leafref { path "../target"; } type string;
    } }
  }
}`

// Validate changes the union's member from leafref(int32) to string without
// changing its native hash or registration order. The second batch must enter
// the populated canonical buckets without rescanning their existing records.
func TestHashReindexTwoBatchWork(t *testing.T) {
	set := opIndexSet(t, hashReindexSchema)
	for _, order := range []string{"sorted", "reverse", "shuffled", "duplicates"} {
		t.Run(order, func(t *testing.T) {
			opIndexScaling(t, scaleSizes([]int{512, 1024, 2048, 4096}, 3), func(n int) int64 {
				tr := newTree(set)
				root, err := tr.NewInner(nil, "m:r", NewOptions{})
				if err != nil {
					t.Fatal(err)
				}
				indices := make([]int, n)
				for i := range indices {
					indices[i] = i + 1
				}
				switch order {
				case "reverse":
					slices.Reverse(indices)
				case "shuffled":
					rand.New(rand.NewPCG(17, 31)).Shuffle( //nolint:gosec // deterministic test permutation
						n, func(i, j int) { indices[i], indices[j] = indices[j], indices[i] })
				case "duplicates":
					for i := range indices {
						indices[i] = 1
					}
				}
				tr.work.visits, tr.work.shifts = true, true
				for batch := range 2 {
					tr.work.Store(0)
					for _, v := range indices {
						if _, err := tr.NewTerm(root, "v", fmt.Sprintf("0%d", v), NewOptions{}); err != nil {
							t.Fatal(err)
						}
					}
					if diags, err := tr.Validate(context.Background(), ValidateOptions{Present: true}); err != nil {
						t.Fatalf("batch %d validation: %v, %v", batch, err, diags)
					}
				}
				work := tr.work.Load()
				if root.kids.SchemaLen() != 2*n || root.kids.ht == nil {
					t.Fatalf("second batch has %d nodes, hash table %t", root.kids.SchemaLen(), root.kids.ht != nil)
				}
				for i, node := range root.kids.SchemaAll() {
					if want := fmt.Sprintf("0%d", indices[i%n]); node.value.Canonical() != want {
						t.Fatalf("instance %d: canonical %q, want %q", i, node.value.Canonical(), want)
					}
				}
				checkRecordOrder(t, &root.kids)
				return work
			})
		})
	}
}

// The XPath budget can abort before or partway through the second batch. The
// completed canonical moves remain visible and indexed, and a retry can finish.
func TestHashReindexBudgetCleanup(t *testing.T) {
	set := opIndexSet(t, hashReindexSchema)
	for _, limit := range []int64{1, 64} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			tr := newTree(set)
			root, err := tr.NewInner(nil, "m:r", NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			const count = 32
			var nodes []*Node
			for batch := range 2 {
				for range count {
					node, err := tr.NewTerm(root, "v", "01", NewOptions{})
					if err != nil {
						t.Fatal(err)
					}
					nodes = append(nodes, node)
				}
				if batch == 0 {
					if _, err := tr.Validate(context.Background(), ValidateOptions{Present: true}); err != nil {
						t.Fatal(err)
					}
				}
			}
			orders := make(map[*Node]uint64, len(nodes))
			hashes := make(map[*Node]uint32, len(nodes))
			for _, node := range nodes {
				orders[node], hashes[node] = root.kids.exactByNode[node].order, node.hash
			}
			diags, err := tr.validateAll(context.Background(), ValidateOptions{Present: true}, Budget{MaxXPathSteps: limit}, nil)
			if !errors.Is(err, yang.ErrBudget) || len(diags) != 0 {
				t.Fatalf("budget error = %v, diagnostics %v", err, diags)
			}
			if !slices.Equal(root.kids.schemaSpan().Collect(), nodes) {
				t.Fatal("budget failure changed sibling membership or order")
			}
			moved := 0
			for i, node := range nodes {
				if root.kids.exactByNode[node].order != orders[node] || node.hash != hashes[node] {
					t.Fatal("budget failure changed registration order or native hash")
				}
				if i >= count && node.value.Canonical() == "01" {
					moved++
				}
			}
			if limit == 1 && moved != 0 || limit > 1 && (moved == 0 || moved == count) {
				t.Fatalf("budget %d reindexed %d of %d new records", limit, moved, count)
			}
			checkRecordOrder(t, &root.kids)
			opidxCheckRecords(t, &root.kids)
			if _, err := tr.Validate(context.Background(), ValidateOptions{Present: true}); err != nil {
				t.Fatal("retry:", err)
			}
			for _, node := range nodes {
				if node.value.Canonical() != "01" {
					t.Fatal("retry left an unresolved union member")
				}
			}
			if err := tr.unlinkAll(nodes); err != nil {
				t.Fatal(err)
			}
			checkRecordOrder(t, &root.kids)
			opidxCheckRecords(t, &root.kids)
			if len(root.kids.exactByNode) != 0 || len(root.kids.ht) != 0 {
				t.Fatal("cleanup retained records")
			}
		})
	}
}

// Canonical-only moves can arrive in any permutation, not only increasing
// registration order. Every intermediate bucket must remain ready for lookup.
func TestHashReindexRecordOrder(t *testing.T) {
	set := storedHashSet(t, "string")
	for _, order := range []string{"sorted", "reverse", "shuffled"} {
		t.Run(order, func(t *testing.T) {
			tr := newTree(set)
			root, err := tr.NewInner(nil, "m:r", NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			const count = 128
			nodes := make([]*Node, count)
			for i := range nodes {
				nodes[i], err = tr.NewTerm(root, "s", "old", NewOptions{})
				if err != nil {
					t.Fatal(err)
				}
			}
			// Seed a destination using ordinary registrations, which must remain
			// unindexed until a canonical-only move needs ordered insertion.
			seed, diag := types.Store(nodes[0].schema.Type, "next", types.FormatJSON, types.HintData, nil, nodes[0].schema)
			if diag != nil {
				t.Fatal(diag.Msg)
			}
			for _, node := range nodes[:count/2] {
				root.kids.hashRemove(node)
				node.value = seed
				root.kids.hashPut(node)
			}
			for _, chain := range root.kids.ht {
				if chain.root != nil {
					t.Fatal("ordinary registration built an unnecessary order index")
				}
			}
			checkRecordOrder(t, &root.kids)
			indices := make([]int, count)
			for i := range indices {
				indices[i] = i
			}
			switch order {
			case "reverse":
				slices.Reverse(indices)
			case "shuffled":
				rand.New(rand.NewPCG(29, 47)).Shuffle( //nolint:gosec // deterministic test permutation
					count, func(i, j int) { indices[i], indices[j] = indices[j], indices[i] })
			}
			originalOrders := make(map[*Node]uint64, count)
			for _, node := range nodes {
				originalOrders[node] = root.kids.exactByNode[node].order
			}
			for _, value := range []string{"next", "old", "final"} {
				for _, i := range indices {
					node := nodes[i]
					v, diag := types.Store(node.schema.Type, value, types.FormatJSON, types.HintData, nil, node.schema)
					if diag != nil {
						t.Fatal(diag.Msg)
					}
					moved := node.value.Canonical() != value
					node.value = v
					root.kids.hashReindex(node)
					if root.kids.exactByNode[node].order != originalOrders[node] {
						t.Fatal("canonical move changed registration order")
					}
					if moved && root.kids.exactByNode[node].chain.root == nil {
						t.Fatal("canonical move did not index its destination bucket")
					}
					checkRecordOrder(t, &root.kids)
				}
			}
			for _, i := range indices {
				root.kids.hashRemove(nodes[i])
				checkRecordOrder(t, &root.kids)
			}
			if len(root.kids.ht) != 0 || len(root.kids.exactByNode) != 0 {
				t.Fatal("removing every record retained index state")
			}
		})
	}
}

func checkRecordOrder(t *testing.T, sib *siblings) {
	t.Helper()
	seen := map[*record]bool{}
	for key, chain := range sib.ht {
		var ordered []*record
		var walk func(*record) int
		walk = func(r *record) int {
			if r == nil {
				return 0
			}
			if seen[r] {
				t.Fatal("cycle or repeated record in ordered index")
			}
			seen[r] = true
			left := walk(r.left)
			ordered = append(ordered, r)
			right := walk(r.right)
			if r.height != 1+max(left, right) || left-right > 1 || right-left > 1 {
				t.Fatalf("unbalanced record %d: height %d, children %d and %d", r.order, r.height, left, right)
			}
			return r.height
		}
		if chain.root != nil {
			walk(chain.root)
		} else {
			for r := chain.first; r != nil; r = r.next {
				if seen[r] {
					t.Fatal("cycle or repeated record in unindexed chain")
				}
				seen[r] = true
				if r.left != nil || r.right != nil || r.height != 0 {
					t.Fatal("unindexed chain retained order index state")
				}
				ordered = append(ordered, r)
			}
		}
		if len(ordered) != chain.count {
			t.Fatalf("ordered index has %d records, chain has %d", len(ordered), chain.count)
		}
		var prev *record
		next := chain.first
		for _, r := range ordered {
			if r != next || r.prev != prev || r.chain != chain || sib.exactByNode[r.node] != r || r.node.hkey != key {
				t.Fatalf("record %d differs between ordered and linked indexes", r.order)
			}
			if prev != nil && prev.order >= r.order {
				t.Fatalf("registration order %d followed by %d", prev.order, r.order)
			}
			prev, next = r, r.next
		}
		if next != nil || prev != chain.last {
			t.Fatal("chain endpoints differ from ordered index")
		}
	}
	if len(seen) != len(sib.exactByNode) {
		t.Fatalf("ordered indexes have %d records, node index has %d", len(seen), len(sib.exactByNode))
	}
}
