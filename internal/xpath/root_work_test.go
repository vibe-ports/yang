// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"context"
	"testing"
)

type firstIndexedNode struct {
	*tnode
	hit Node
}

func (n *firstIndexedNode) FirstChild(sn SchemaNode) (Node, bool) {
	if sn == n.hit.Schema() {
		return n.hit, true
	}
	return nil, true
}

// Root deletion skipping must not add per-lookup allocations to ordinary scans
// or to a child's existing FirstLookup fast path. In particular, allocating a
// range-function closure before dispatch would penalize even a handled lookup.
func TestRootLookupAllocations(t *testing.T) {
	hit := leaf("a:hit", "v")
	roots := top(leaf("a:gone", "g"), hit)
	child := leaf("hit", "v")
	base := cont("a:c", child)
	top(base)
	parent := &firstIndexedNode{tnode: base, hit: child}
	for _, name := range []string{"root", "live-root", "child-index"} {
		t.Run(name, func(t *testing.T) {
			ec := &EvalContext{Tree: roots}
			start := nodesV([]item{{t: itRoot}})
			want := Node(hit)
			switch name {
			case "live-root":
				ec.TreeNext = func(i int) int {
					if i < 0 {
						return 1
					}
					return len(roots)
				}
			case "child-index":
				start = nodesV([]item{{n: parent, t: itElem}})
				want = child
			}
			ev := &evaluator{ec: ec, ctx: context.Background()}
			allocs := testing.AllocsPerRun(1000, func() {
				ev.steps = 100
				if got := ev.firstInstance(start, want.Schema()); got != want || ev.err != nil {
					t.Fatalf("lookup: %v, %v", got, ev.err)
				}
			})
			if allocs != 0 {
				t.Fatalf("%s: %g allocations per lookup, want zero", name, allocs)
			}
		})
	}
}
