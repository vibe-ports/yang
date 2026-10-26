// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/vibe-ports/yang"
)

// Both facade searches replace BinarySearchFunc. Preserve its complete probe
// sequence, including the final equality probe, for bounded and empty searches.
func TestSiblingLowerBoundProbes(t *testing.T) {
	for _, size := range []int{0, 1, 2, 17, 64} {
		nodes := make([]*Node, size)
		positions := make(map[*Node]int, size)
		for i := range nodes {
			nodes[i] = &Node{}
			positions[nodes[i]] = i
		}
		var sib siblings
		mustLink(t, sib.Build(nodes, nil))
		for lo := 0; lo <= size; lo++ {
			for hi := lo; hi <= size; hi++ {
				for cut := lo; cut <= hi; cut++ {
					var want []int
					offset, _ := slices.BinarySearchFunc(nodes[lo:hi], cut, func(n *Node, target int) int {
						want = append(want, positions[n])
						if positions[n] >= target {
							return 1
						}
						return -1
					})
					for _, search := range []func(int, int, func(*Node) bool) int{sib.LowerBound, sib.schemaSpan().LowerBound} {
						var got []int
						at := search(lo, hi, func(n *Node) bool { got = append(got, positions[n]); return positions[n] >= cut })
						if at != lo+offset || !slices.Equal(got, want) {
							t.Fatalf("size=%d [%d:%d] cut=%d: at=%d probes=%v; want at=%d probes=%v", size, lo, hi, cut, at, got, lo+offset, want)
						}
					}
				}
			}
		}
	}
}

// A singleton needs one origin slot probe and two upper probes. These fixed
// thresholds catch a smaller enforced charge at the origin lookup boundary.
func TestSiblingLinkOriginBudgetThreshold(t *testing.T) {
	for _, limit := range []uint64{2, 3} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			f := newFixture()
			tr, parent := f.cont(t)
			node := f.term(t, f.z, "x")
			mustLink(t, tr.insert(parent, node, insertDefault))
			tr.work.hashUsed, tr.work.hashLimit = 0, limit
			tr.work.Store(0)
			restore, arm, err := tr.linkOrigin(node)
			if limit == 2 {
				if !errors.Is(err, yang.ErrBudget) || restore != nil || arm != nil {
					t.Fatalf("limit=2: error=%v, restore=%t, arm=%t; want ErrBudget before mutation", err, restore != nil, arm != nil)
				}
			} else if err != nil || restore == nil || arm == nil || tr.work.hashUsed != 3 {
				t.Fatalf("limit=3: error=%v hash=%d; want success using 3 units", err, tr.work.hashUsed)
			}
			if node.parent != parent || node.treeOf() != tr {
				t.Fatal("origin probe changed ownership")
			}
			if got := tr.work.Load(); got != 3 {
				t.Fatalf("origin work=%d; want 3", got)
			}
		})
	}
}
