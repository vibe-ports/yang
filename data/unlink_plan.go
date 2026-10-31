// SPDX-License-Identifier: BSD-3-Clause

package data

import "github.com/vibe-ports/yang/internal/schema"

// unlinkPlan reserves a single unlink before a relocation can remove hash records. It also
// avoids locating the node again after native lookup and recovery preparation. Bulk unlink uses
// its own virtual links and one charged compaction instead.
type unlinkPlan struct {
	node                     *Node
	sib                      *siblings
	gen                      uint64
	at                       int
	prev, next, tail, leader *Node
	// left is the physical leader of the run a removed separator joins to the next one, found
	// by a charged walk back from prev; nil when n separates no run or no run has an RB tree
	left  *Node
	order *dirtyOrder
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
	// the removal's freeLinks: a counterpart governed by a suspended state refuses it here,
	// before any write (design 13 §2.4 (5))
	if err := linkGuard(n, w); err != nil {
		return nil, err
	}
	at := sib.orderPosition(n) // the storage's rank walk, paid before it runs
	if w.hashErr != nil {
		return nil, w.hashErr
	}
	if n.schema == nil {
		at -= sib.SchemaLen()
	}
	if at < 0 {
		return nil, &opError{"LY_EINT", "Unlinked node in sibling storage."}
	}
	// n's node->prev and node->next: the document neighbours, whatever their kind (design 15);
	// a schema node's opaque successor is passed as none, as no RB metadata moves onto it
	h := sib.handle(n)
	prev, next := handleValue(sib.st.seq.Prev(h)), handleValue(sib.st.seq.Next(h))
	if n.schema != nil && next != nil && next.schema == nil {
		next = nil
	}
	var left *Node
	separates := n.schema != nil && prev != nil && next != nil && n.schema != prev.schema &&
		prev.schema == next.schema
	if n.schema == nil && sib.st.interleaved() {
		// an opaque node between two instances of one schema node separates a run too (design 15)
		pp, nn := sib.st.seq.Prev(h), sib.st.seq.Next(h)
		separates = pp != nil && nn != nil && pp.Value.schema != nil && pp.Value.schema == nn.Value.schema
	}
	if separates && len(sib.rbTree) != 0 {
		// removing a separator joins two runs: unlink marks the left run's tree unsorted
		l := sib.st.seq.Prev(h) // prev, walked back to its run's first member, a charged step each
		for {
			if err := w.chargeHash(1); err != nil {
				return nil, err
			}
			sib.visit()
			p := sib.st.seq.Prev(l)
			if p == nil || p.Value.schema != l.Value.schema {
				break
			}
			l = p
		}
		left = l.Value
	}
	if p := n.parent; p != nil && p.schema != nil && p.schema.Kind == schema.List &&
		(n.schema != nil && !n.isKey() && at == p.keyN ||
			n.schema == nil && sib.st.seq.FirstKind(false) == h) { // the first opaque child ends keyRun
		// detach exposes the keys n preceded (exposeListKeys), however many repeat: this walk
		// pays for itself and for that one, before any write
		for c := sib.st.seq.Next(h); c != nil && c.Value.isKey(); c = sib.st.seq.Next(c) {
			if err := w.chargeHash(2); err != nil {
				return nil, err
			}
			sib.visit()
		}
	}
	tail := sib.nativeTail()
	order, leader, err := sib.removalOrder(n, prev, next, tail, nil)
	if err != nil {
		return nil, err
	}
	amount := uint64(1)
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
	p := &unlinkPlan{node: n, sib: sib, gen: sib.gen, at: at, prev: prev, next: next, tail: tail, left: left, order: order, leader: leader}
	w.unlinkPlan = p
	return p, nil
}
