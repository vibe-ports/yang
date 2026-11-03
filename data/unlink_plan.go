// SPDX-License-Identifier: BSD-3-Clause

package data

import "github.com/vibe-ports/yang/internal/schema"

// unlinkPlan reserves a single unlink before a relocation can remove hash records. It also
// avoids locating the node again after native lookup and recovery preparation. Bulk unlink uses
// its own virtual links and one charged compaction instead.
type unlinkPlan struct {
	node               *Node
	sib                *siblings
	gen                uint64
	at, docAt          int
	prev, next, leader *Node
	// left is the extended piece's first member; additional retained owners are cold.
	// The unused cached-tail slot holds their optional pointer, keeping the ordinary
	// plan and schema-separator allocation paths unchanged.
	left     *Node
	moreLeft *unlinkOwners
	order    *dirtyOrder
}

// unlinkOwners is allocated only when a join has interior retained owners.
type unlinkOwners struct {
	nodes []*Node
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
	// n's node->prev and node->next: the document neighbours, whatever their kind.
	// lyds_move_meta also accepts an opaque successor (tree_data_sorted.c:1344).
	h := sib.handle(n)
	prev, next := handleValue(sib.st.seq.Prev(h)), handleValue(sib.st.seq.Next(h))
	var left *Node
	var moreLeft []*Node
	separates := n.schema != nil && prev != nil && prev.schema != nil && next != nil && n.schema != prev.schema &&
		prev.schema == next.schema
	if n.schema == nil && sib.st.interleaved() {
		// an opaque node between two instances of one schema node separates a run too (design 15)
		pp, nn := sib.st.seq.Prev(h), sib.st.seq.Next(h)
		separates = pp != nil && nn != nil && pp.Value.schema != nil && pp.Value.schema == nn.Value.schema
	}
	if separates && len(sib.rbTree) != 0 {
		// Every retained owner in the left piece now has a larger physical suffix.
		l := sib.st.seq.Prev(h) // prev, walked back to its run's first member, a charged step each
		for {
			if err := w.chargeHash(1); err != nil {
				return nil, err
			}
			sib.visit()
			p := sib.st.seq.Prev(l)
			if p == nil || p.Value.schema != l.Value.schema {
				left = l.Value
				break
			}
			if sib.rbTree[l.Value] {
				moreLeft = append(moreLeft, l.Value)
			}
			l = p
		}
	}
	docAt := at
	if n.schema == nil {
		docAt += sib.SchemaLen()
	}
	if p := n.parent; p != nil && p.schema != nil && p.schema.Kind == schema.List {
		if sib.st.interleaved() {
			opened, err := w.openRead(rbHeight(sib.Len()) + 1)
			if err != nil {
				return nil, err
			}
			docAt, _ = sib.st.seq.Rank(h)
			w.closeRead(opened)
		}
		if !n.isKey() && docAt == p.keyN {
			// Removing the physical prefix separator (including an opaque child)
			// exposes this run. Pay for this walk and exposeListKeys before writes.
			for c := sib.st.seq.Next(h); c != nil && c.Value.isKey(); c = sib.st.seq.Next(c) {
				if err := w.chargeHash(2); err != nil {
					return nil, err
				}
				sib.visit()
			}
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
		groups := uint64(len(sib.rbMore[n])) + 1
		// The selected item leaves n before runGone when the native branch transfers it.
		if order != nil && leader == n && next != nil && !unlinkAlone(n, prev, next, tail) {
			groups--
		}
		amount = addHashBound(amount, 4*groups)
	}
	if order != nil {
		amount = addHashBound(amount, sib.removalWorkCost(n, leader, prev, next, tail, order))
	}
	if err := w.chargeHash(amount); err != nil {
		return nil, err
	}
	p := &unlinkPlan{node: n, sib: sib, gen: sib.gen, at: at, docAt: docAt, prev: prev, next: next, left: left, order: order, leader: leader}
	if len(moreLeft) != 0 {
		p.moreLeft = &unlinkOwners{nodes: moreLeft}
	}
	w.unlinkPlan = p
	return p, nil
}

// markJoinedRun preserves the ordinary single-owner path without allocating an owner slice.
// Interior owners from earlier joins share the same charged preparation and journal capture.
func (p *unlinkPlan) markJoinedRun() {
	if p.left != nil && p.sib.rbTree[p.left] {
		markUnsorted(p.sib, p.left)
	}
	if p.moreLeft == nil {
		return
	}
	for _, owner := range p.moreLeft.nodes {
		if err := p.sib.work.chargeHash(1); err != nil {
			return
		}
		p.sib.visit()
		markUnsorted(p.sib, owner)
	}
}
