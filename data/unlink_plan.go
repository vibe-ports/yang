// SPDX-License-Identifier: BSD-3-Clause

package data

// unlinkPlan reserves a single unlink before a relocation can remove hash records. It also
// avoids rescanning the slice after native lookup and recovery preparation. Bulk unlink uses
// its own virtual links and one charged compaction instead.
type unlinkPlan struct {
	node                     *Node
	sib                      *siblings
	gen                      uint64
	at                       int
	prev, next, tail, leader *Node
	order                    *dirtyOrder
}

func prepareUnlink(n *Node) (*unlinkPlan, error) {
	sib := n.siblingsOf()
	if sib == nil {
		return nil, nil
	}
	w := sib.work
	if w == nil {
		return nil, &opError{"LY_EINT", "Unbound sibling storage during unlink."}
	}
	if w.hashErr != nil {
		return nil, w.hashErr
	}
	if p := w.unlinkPlan; p != nil && p.node == n && p.sib == sib && p.gen == sib.gen {
		return p, nil
	}
	nodes := sib.schemaSpan()
	if n.schema == nil {
		nodes = sib.opaqueSpan()
	}
	at := -1
	for i, j := 0, nodes.Len()-1; i <= j; i, j = i+1, j-1 {
		if err := w.chargeHash(1); err != nil {
			return nil, err
		}
		w.Add(1)
		if nodes.At(j) == n {
			at = j
			break
		}
		if nodes.At(i) == n {
			at = i
			break
		}
	}
	if at < 0 {
		return nil, &opError{"LY_EINT", "Unlinked node in sibling storage."}
	}
	var prev, next *Node
	if at > 0 {
		prev = nodes.At(at - 1)
	}
	if at+1 < nodes.Len() {
		next = nodes.At(at + 1)
	}
	tail := sib.nativeTail()
	order, leader, err := sib.removalOrder(n, prev, next, tail, nil)
	if err != nil {
		return nil, err
	}
	amount := uint64(min(at, nodes.Len()-at-1)) + 1 //nolint:gosec // a valid slice position and its shift count are nonnegative
	if sib.rbGroups[n] != nil {
		// Metadata surviving unlink travels on n. Pay its queue copies before any
		// physical/hash mutation, including a NULL-root item made by duplication.
		amount = addHashBound(amount, 4*(uint64(len(sib.rbMore[n]))+1))
	}
	if order != nil {
		if order.nodes[n] != nil {
			amount = addHashBound(amount, order.removalBound())
		}
		amount = addHashBound(amount, sib.transferCost(leader, next))
		if leader != n {
			amount = addHashBound(amount, 4*(uint64(len(sib.rbMore[leader]))+1)) // compactMeta(leader)
		}
	}
	if err := w.chargeHash(amount); err != nil {
		return nil, err
	}
	p := &unlinkPlan{node: n, sib: sib, gen: sib.gen, at: at, prev: prev, next: next, tail: tail, order: order, leader: leader}
	w.unlinkPlan = p
	return p, nil
}
