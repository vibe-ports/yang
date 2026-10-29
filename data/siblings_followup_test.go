// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"math/bits"
	"slices"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

// Over storage both facade searches are one bounded descent: the BinarySearchFunc result with a
// probe per level at most, all inside the range (design 14). A detached span keeps
// BinarySearchFunc's complete probe sequence, the final equality probe included.
func TestSiblingLowerBoundProbes(t *testing.T) {
	for _, size := range []int{0, 1, 2, 17, 64} {
		nodes := make([]*Node, size)
		positions := make(map[*Node]int, size)
		for i := range nodes {
			nodes[i] = &Node{schema: &schema.Node{}}
			positions[nodes[i]] = i
		}
		var sib siblings
		mustLink(t, testBuild(&sib, nodes...))
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
						// one storage descent: a probe per level at most, all inside the range
						if at != lo+offset || len(got) > 2*bits.Len(uint(size+1))+2 ||
							slices.ContainsFunc(got, func(p int) bool { return p < lo || p >= hi }) {
							t.Fatalf("size=%d [%d:%d] cut=%d: at=%d probes=%v; want at=%d", size, lo, hi, cut, at, got, lo+offset)
						}
					}
					var got []int // a detached span keeps BinarySearchFunc's probes
					at := spanOf(nodes).LowerBound(lo, hi, func(n *Node) bool { got = append(got, positions[n]); return positions[n] >= cut })
					if at != lo+offset || !slices.Equal(got, want) {
						t.Fatalf("detached size=%d [%d:%d] cut=%d: at=%d probes=%v; want at=%d probes=%v", size, lo, hi, cut, at, got, lo+offset, want)
					}
				}
			}
		}
	}
}

// A singleton's origin is its storage rank: a read credit of rbHeight(1)+1 units paid before
// the walk (design 13 §4.4, design 14), then one upper probe (a one-level descent). These fixed
// thresholds catch a smaller enforced charge at the origin lookup boundary.
func TestSiblingLinkOriginBudgetThreshold(t *testing.T) {
	need := rbHeight(1) + 1 + 1
	for _, limit := range []uint64{need - 1, need} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			f := newFixture()
			tr, parent := f.cont(t)
			node := f.term(t, f.z, "x")
			mustLink(t, tr.insert(parent, node, insertDefault))
			end := tr.work.beginHashWork() // a mutation path: the rank read is paid
			defer end()
			tr.work.hashUsed, tr.work.hashLimit = 0, limit
			tr.work.Store(0)
			restore, arm, err := tr.linkOrigin(node)
			if limit < need {
				if !errors.Is(err, yang.ErrBudget) || restore != nil || arm != nil {
					t.Fatalf("limit=%d: error=%v, restore=%t, arm=%t; want ErrBudget before mutation", limit, err, restore != nil, arm != nil)
				}
			} else if err != nil || restore == nil || arm == nil || tr.work.hashUsed != need {
				t.Fatalf("limit=%d: error=%v hash=%d; want success using %d units", limit, err, tr.work.hashUsed, need)
			}
			if node.parent != parent || node.treeOf() != tr {
				t.Fatal("origin probe changed ownership")
			}
		})
	}
}
