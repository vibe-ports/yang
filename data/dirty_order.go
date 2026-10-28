// SPDX-License-Identifier: BSD-3-Clause AND ISC AND BSD-2-Clause
// Ported from libyang v5.8.6 src/tree_data_sorted.c (rb_rotate_left, rb_rotate_right,
// rb_insert_color, rb_insert_node) (BSD-3-Clause, © CESNET).
//
// RB tree routines derived from OpenBSD subr_tree.c 1.9 via libyang. libyang's file carries
// "SPDX-License-Identifier: ISC AND BSD-2-Clause" and these copyright lines:
//   Copyright 2002 Niels Provos <provos@citi.umich.edu>
//   Copyright (c) 2016 David Gwynne <dlg@openbsd.org>
// The permission notices below are verbatim from that OpenBSD file at revision 1.9:
//
// /*	$OpenBSD: subr_tree.c,v 1.9 2017/06/08 03:30:52 dlg Exp $ */
//
// /*
//  * Copyright 2002 Niels Provos <provos@citi.umich.edu>
//  * All rights reserved.
//  *
//  * Redistribution and use in source and binary forms, with or without
//  * modification, are permitted provided that the following conditions
//  * are met:
//  * 1. Redistributions of source code must retain the above copyright
//  *    notice, this list of conditions and the following disclaimer.
//  * 2. Redistributions in binary form must reproduce the above copyright
//  *    notice, this list of conditions and the following disclaimer in the
//  *    documentation and/or other materials provided with the distribution.
//  *
//  * THIS SOFTWARE IS PROVIDED BY THE AUTHOR ``AS IS'' AND ANY EXPRESS OR
//  * IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES
//  * OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED.
//  * IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY DIRECT, INDIRECT,
//  * INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT
//  * NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
//  * DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
//  * THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
//  * (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF
//  * THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
//  */
//
// /*
//  * Copyright (c) 2016 David Gwynne <dlg@openbsd.org>
//  *
//  * Permission to use, copy, modify, and distribute this software for any
//  * purpose with or without fee is hereby granted, provided that the above
//  * copyright notice and this permission notice appear in all copies.
//  *
//  * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
//  * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
//  * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
//  * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
//  * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
//  * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
//  * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
//  */

package data

// dirtyOrder is a system-ordered run's authoritative retained RB tree (lyds), shared by parser
// publication and ordinary mutations: the same red-black
// algorithm and comparison direction as rb_insert_node, rb_insert_color, and rb_remove
// (tree_data_sorted.c:430-667,669-716), fed in the same order, so it has libyang's shape. The
// shape matters once a repeated key has changed an instance after it was linked: libyang leaves
// that instance where it is and later insertions compare its current keys on their way down
// (rb_compare_lists, tree_data_sorted.c:235-270). The physical sibling and operation-index
// slices hold link order while the run is dirty and in-order otherwise.
type dirtyOrder struct {
	root    *dirtyOrderNode
	nodes   map[*Node]*dirtyOrderNode
	work    *workCounter
	compare func(*Node, *Node) int
	// leaders are the first-instance changes (lyds_link_data_node without a predecessor,
	// tree_data_sorted.c:1021-1040) of the latest insertions, until applyLeaders applies them.
	leaders [][2]*Node
	// undo records, while armed for one removal (armRemovalUndo), every topology node before
	// remove changes it.
	undo *removalUndo
	// narrow is NewPath's journal while rbPos places one node: keep records every topology node
	// place changes into it, within the bound recordPlace prepaid.
	narrow *hashTransaction
}

type removalUndo struct {
	target *Node
	active bool // remove(target) is running: only its writes are journaled
	saved  map[*dirtyOrderNode]dirtyOrderNode
	work   *workCounter
}

// keep records x before remove changes it, each saved node charged within the armed reservation,
// or before place changes it into a narrow journal.
func (d *dirtyOrder) keep(x *dirtyOrderNode) {
	if j := d.narrow; j != nil && x != nil {
		if _, whole := j.dirty[x]; !whole {
			j.placed = append(j.placed, placedNode{x, *x})
		}
	}
	u := d.undo
	if u == nil || !u.active || x == nil {
		return
	}
	if _, ok := u.saved[x]; ok {
		return
	}
	u.saved[x] = *x
	u.work.Add(1) // the caller prepaid removalBound before arming this journal
}

type dirtyOrderNode struct {
	node                *Node
	left, right, parent *dirtyOrderNode
	size                int
	red, active         bool
}

// newDirtyOrder starts the tree of a run. create inserts the nodes one by one as
// lyds_additionally_create_rb_tree does (the first is the leader); otherwise the run already
// had a tree before this parse, whose shape is not kept, and the nodes go in in their order.
// It reports whether the given order was not sorted.
func newDirtyOrder(work *workCounter, nodes siblingSpan, compare func(*Node, *Node) int, create bool) (*dirtyOrder, bool) {
	d := &dirtyOrder{nodes: make(map[*Node]*dirtyOrderNode, nodes.Len()), work: work, compare: compare}
	moved := false
	for _, n := range nodes.All() {
		if isMax, _ := d.insertNode(n, !create); !isMax {
			moved = true
		}
	}
	return d, moved
}

// cloneDirtyOrder copies an RB topology for the mutation journal. The data nodes are immutable
// keys here; only the topology nodes and their links are changed by later insertions.
func cloneDirtyOrder(d *dirtyOrder) *dirtyOrder {
	if d == nil {
		return nil
	}
	clone := &dirtyOrder{nodes: make(map[*Node]*dirtyOrderNode, len(d.nodes)), work: d.work, compare: d.compare,
		leaders: append([][2]*Node(nil), d.leaders...)}
	byOld := make(map[*dirtyOrderNode]*dirtyOrderNode, len(d.nodes))
	stack := []*dirtyOrderNode{d.root}
	for len(stack) != 0 {
		old := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if old == nil || byOld[old] != nil {
			continue
		}
		byOld[old] = &dirtyOrderNode{node: old.node, size: old.size, red: old.red, active: old.active}
		stack = append(stack, old.left, old.right)
	}
	for old, copied := range byOld {
		copied.left, copied.right, copied.parent = byOld[old.left], byOld[old.right], byOld[old.parent]
		clone.nodes[copied.node] = copied
	}
	clone.root = byOld[d.root]
	return clone
}

func dirtySize(n *dirtyOrderNode) int {
	if n == nil {
		return 0
	}
	return n.size
}

func dirtyFix(n *dirtyOrderNode) {
	n.size = dirtySize(n.left) + dirtySize(n.right)
	if n.active {
		n.size++
	}
}

func (d *dirtyOrder) replaceChild(parent, old, x *dirtyOrderNode) {
	d.keep(parent)
	switch {
	case parent == nil:
		d.root = x
	case parent.left == old:
		parent.left = x
	default:
		parent.right = x
	}
}

// rotateLeft is rb_rotate_left (tree_data_sorted.c:374-400).
func (d *dirtyOrder) rotateLeft(n *dirtyOrderNode) {
	r := n.right
	d.keep(n)
	d.keep(r)
	d.keep(r.left)
	n.right = r.left
	if r.left != nil {
		r.left.parent = n
	}
	r.parent = n.parent
	d.replaceChild(n.parent, n, r)
	r.left, n.parent = n, r
	dirtyFix(n)
	dirtyFix(r)
}

// rotateRight is rb_rotate_right (tree_data_sorted.c:402-428).
func (d *dirtyOrder) rotateRight(n *dirtyOrderNode) {
	l := n.left
	d.keep(n)
	d.keep(l)
	d.keep(l.right)
	n.left = l.right
	if l.right != nil {
		l.right.parent = n
	}
	l.parent = n.parent
	d.replaceChild(n.parent, n, l)
	l.right, n.parent = n, l
	dirtyFix(n)
	dirtyFix(l)
}

// insertColor is rb_insert_color (tree_data_sorted.c:430-479).
func (d *dirtyOrder) insertColor(x *dirtyOrderNode) {
	for p := x.parent; p != nil && p.red; p = x.parent {
		if d.work != nil {
			d.work.Add(1)
		}
		g := p.parent
		if p == g.left {
			if u := g.right; u != nil && u.red {
				d.keep(u)
				d.keep(p)
				d.keep(g)
				u.red, p.red, g.red = false, false, true
				x = g
				continue
			}
			if p.right == x {
				d.rotateLeft(p)
				p, x = x, p
			}
			d.keep(p)
			d.keep(g)
			p.red, g.red = false, true
			d.rotateRight(g)
		} else {
			if u := g.left; u != nil && u.red {
				d.keep(u)
				d.keep(p)
				d.keep(g)
				u.red, p.red, g.red = false, false, true
				x = g
				continue
			}
			if p.left == x {
				d.rotateRight(p)
				p, x = x, p
			}
			d.keep(p)
			d.keep(g)
			p.red, g.red = false, true
			d.rotateLeft(g)
		}
	}
	d.keep(d.root)
	d.root.red = false
}

func (d *dirtyOrder) insertNode(n *Node, right bool) (isMax, leftmost bool) {
	x := &dirtyOrderNode{node: n, size: 1, red: true, active: true}
	d.nodes[n] = x
	isMax, leftmost = true, true
	var parent *dirtyOrderNode
	left := false
	for cur := d.root; cur != nil; {
		d.work.Add(1) // one unit for each visited tree node
		parent = cur
		cur.size++
		if left = !right && d.compare(cur.node, n) > 0; left {
			cur, isMax = cur.left, false
		} else {
			cur, leftmost = cur.right, false
		}
	}
	x.parent = parent
	switch {
	case parent == nil:
		d.root = x
		leftmost = false // the run's first instance is its leader already
	case left:
		parent.left = x
	default:
		parent.right = x
	}
	if leftmost {
		d.leaders = append(d.leaders, [2]*Node{d.successor(n), n})
	}
	d.insertColor(x)
	return isMax, leftmost
}

// insertCompatible is rb_insert_node with a comparison that can refuse a C storage access the Go
// port cannot model deterministically. It returns the inserted node's RB predecessor, which is
// where lyds_link_data_node links the data node (tree_data_sorted.c:669-716,1021-1039).
// No topology is changed when a comparison is refused.
func (d *dirtyOrder) insertCompatible(n *Node, compare func(*Node, *Node) (int, bool)) (*Node, bool) {
	parent, left, pred, ok := d.search(n, compare)
	if !ok {
		return nil, false
	}
	d.place(n, parent, left)
	return pred, true
}

// search is rb_insert_node's read-only descent (tree_data_sorted.c:684-695): equal values go
// right. It returns the leaf position and the new node's in-order predecessor, the last node the
// descent left to the right (lyds_link_data_node's rb_prev, 1026). A refused comparison changes
// nothing.
func (d *dirtyOrder) search(n *Node, compare func(*Node, *Node) (int, bool)) (parent *dirtyOrderNode, left bool, pred *Node, ok bool) {
	var lastRight *dirtyOrderNode
	for cur := d.root; cur != nil; {
		if d.work != nil {
			d.work.Add(1)
		}
		parent = cur
		order, compatible := compare(cur.node, n)
		if !compatible {
			return nil, false, nil, false
		}
		if left = order > 0; left {
			cur = cur.left
		} else {
			lastRight, cur = cur, cur.right
		}
	}
	if lastRight != nil {
		pred = lastRight.node
	}
	return parent, left, pred, true
}

// place links n at the leaf position search found and rebalances (rb_set, rb_insert_color,
// tree_data_sorted.c:697-708).
func (d *dirtyOrder) place(n *Node, parent *dirtyOrderNode, left bool) {
	d.undo = nil // a removal journal armed by a move that stopped before its removal is stale
	for cur := parent; cur != nil; cur = cur.parent {
		d.keep(cur)
		cur.size++
	}
	x := &dirtyOrderNode{node: n, size: 1, red: true, active: true, parent: parent}
	if d.nodes == nil {
		d.nodes = make(map[*Node]*dirtyOrderNode)
	}
	d.nodes[n] = x
	switch {
	case parent == nil:
		d.root = x
	case left:
		parent.left = x
	default:
		parent.right = x
	}
	d.insertColor(x)
}

// postOrder is the sequence rb_iter_begin/rb_iter_next visit (rb_iter_traversal,
// tree_data_sorted.c:294-320): each node after its left and then its right subtree. libyang's
// walk dismantles the tree as it goes; the caller drops this one afterwards.
func (d *dirtyOrder) postOrder() []*Node {
	out := make([]*Node, 0, len(d.nodes))
	var stack []*dirtyOrderNode
	var last *dirtyOrderNode
	for cur := d.root; cur != nil || len(stack) != 0; {
		if d.work != nil {
			d.work.Add(1)
		}
		if cur != nil {
			stack, cur = append(stack, cur), cur.left
			continue
		}
		top := stack[len(stack)-1]
		if top.right != nil && last != top.right {
			cur = top.right
			continue
		}
		out = append(out, top.node)
		last, stack = top, stack[:len(stack)-1]
	}
	return out
}

// checkInsertCompatible walks the exact rb_insert_node search path without changing the tree.
// Each visited node is charged its comparison's bound (rbCompareCost) to the mutation's enforced
// work budget before the callback runs. It is the failure-safe preflight for a foreign-context
// insertion.
func (d *dirtyOrder) checkInsertCompatible(work *workCounter, n *Node,
	compare func(*Node, *Node) (int, bool),
) (bool, error) {
	for cur := d.root; cur != nil; {
		// the callback's bound is charged before it runs, as chargedCompare does
		if err := work.chargeHash(1); err != nil {
			return false, err
		}
		if err := chargeCompareCost(work, cur.node, n); err != nil {
			return false, err
		}
		order, ok := compare(cur.node, n)
		if !ok {
			return false, nil
		}
		if order > 0 {
			cur = cur.left
		} else {
			cur = cur.right
		}
	}
	return true, nil
}

func dirtyRed(n *dirtyOrderNode) bool { return n != nil && n.red }

func (d *dirtyOrder) fixSizes(n *dirtyOrderNode) {
	for ; n != nil; n = n.parent {
		d.keep(n)
		dirtyFix(n)
	}
}

// removeColor is rb_remove_color (tree_data_sorted.c:481-572).
func (d *dirtyOrder) removeColor(parent, n *dirtyOrderNode) {
	for !dirtyRed(n) && n != d.root && parent != nil {
		if d.work != nil {
			d.work.Add(1)
		}
		if parent.left == n {
			sibling := parent.right
			if dirtyRed(sibling) {
				d.keep(sibling)
				d.keep(parent)
				sibling.red, parent.red = false, true
				d.rotateLeft(parent)
				sibling = parent.right
			}
			if sibling == nil {
				n, parent = parent, parent.parent
				continue
			}
			if !dirtyRed(sibling.left) && !dirtyRed(sibling.right) {
				d.keep(sibling)
				sibling.red = true
				n, parent = parent, parent.parent
				continue
			}
			if !dirtyRed(sibling.right) {
				if sibling.left != nil {
					d.keep(sibling.left)
					sibling.left.red = false
				}
				d.keep(sibling)
				sibling.red = true
				d.rotateRight(sibling)
				sibling = parent.right
			}
			d.keep(sibling)
			d.keep(parent)
			sibling.red, parent.red = parent.red, false
			if sibling.right != nil {
				d.keep(sibling.right)
				sibling.right.red = false
			}
			d.rotateLeft(parent)
			n = d.root
			break
		}

		sibling := parent.left
		if dirtyRed(sibling) {
			d.keep(sibling)
			d.keep(parent)
			sibling.red, parent.red = false, true
			d.rotateRight(parent)
			sibling = parent.left
		}
		if sibling == nil {
			n, parent = parent, parent.parent
			continue
		}
		if !dirtyRed(sibling.left) && !dirtyRed(sibling.right) {
			d.keep(sibling)
			sibling.red = true
			n, parent = parent, parent.parent
			continue
		}
		if !dirtyRed(sibling.left) {
			if sibling.right != nil {
				d.keep(sibling.right)
				sibling.right.red = false
			}
			d.keep(sibling)
			sibling.red = true
			d.rotateLeft(sibling)
			sibling = parent.left
		}
		d.keep(sibling)
		d.keep(parent)
		sibling.red, parent.red = parent.red, false
		if sibling.left != nil {
			d.keep(sibling.left)
			sibling.left.red = false
		}
		d.rotateRight(parent)
		n = d.root
		break
	}
	if n != nil {
		d.keep(n)
		n.red = false
	}
}

// removalBound bounds the topology nodes one remove can change: the size path to the root (h),
// per rb_remove_color step the recoloured sibling and parent (2h), the removed slot and its
// successor with their links (6), and the final case's rotations and recolouring (at most 16).
// The bound is generous (8h+32); keep journals each node exactly once.
func (d *dirtyOrder) removalBound() uint64 { return 8*rbHeight(len(d.nodes)) + 32 }

// armRemovalUndo makes the next remove of n journal every node it changes, so a failed move can
// put back the exact topology. The caller has reserved removalBound; nothing is copied before
// remove runs, and nothing but that removal is journaled: an insertion or another removal
// first drops an arm a move left behind when it stopped before unlinking. It returns the
// restore function. The reservation is charged, not merely checked, before the arm allocates.
func (d *dirtyOrder) armRemovalUndo(n *Node, work *workCounter) func() {
	old := d.nodes[n]
	if old == nil {
		return func() {}
	}
	u := &removalUndo{target: n, saved: map[*dirtyOrderNode]dirtyOrderNode{}, work: work}
	root := d.root
	d.undo = u
	return func() {
		if d.undo == u {
			d.undo = nil
		}
		for x, v := range u.saved {
			*x = v
		}
		d.root = root
		if d.nodes == nil {
			d.nodes = make(map[*Node]*dirtyOrderNode)
		}
		d.nodes[n] = old
	}
}

// remove is rb_remove_node followed by rb_remove (tree_data_sorted.c:582-667,954-982).
// It structurally removes the topology node so the retained tree neither changes shape nor
// keeps a detached data node alive after lyds_unlink.
func (d *dirtyOrder) remove(n *Node) {
	old := d.nodes[n]
	if old == nil || !old.active {
		return
	}
	if d.undo != nil && d.undo.target != n {
		d.undo = nil // armed for another node: this removal is not the move's
	}
	if d.undo != nil {
		d.undo.active = true
	}
	defer func() { d.undo = nil }()
	d.keep(old)
	d.keep(old.left)
	d.keep(old.right)
	d.keep(old.parent)
	delete(d.nodes, n)

	removed := old
	var child, parent *dirtyOrderNode
	red := old.red
	switch {
	case old.left == nil:
		child, parent = old.right, old.parent
		d.replaceChild(parent, old, child)
		if child != nil {
			child.parent = parent
		}
		d.fixSizes(parent)
	case old.right == nil:
		child, parent = old.left, old.parent
		d.replaceChild(parent, old, child)
		child.parent = parent
		d.fixSizes(parent)
	default:
		replacement := old.right
		for replacement.left != nil {
			if d.work != nil {
				d.work.Add(1)
			}
			replacement = replacement.left
		}
		d.keep(replacement)
		d.keep(replacement.right)
		child, parent, red = replacement.right, replacement.parent, replacement.red
		if child != nil {
			child.parent = parent
		}
		d.replaceChild(parent, replacement, child)
		if replacement.parent == old {
			parent = replacement
		}

		replacement.parent, replacement.left, replacement.right, replacement.red =
			old.parent, old.left, old.right, old.red
		d.replaceChild(old.parent, old, replacement)
		replacement.left.parent = replacement
		if replacement.right != nil {
			replacement.right.parent = replacement
		}
		d.fixSizes(parent)
	}
	if !red {
		d.removeColor(parent, child)
	}
	removed.left, removed.right, removed.parent = nil, nil, nil
	removed.size, removed.red, removed.active = 0, false, false
}

func (d *dirtyOrder) rawNext(n *dirtyOrderNode) *dirtyOrderNode {
	if n.right != nil {
		n = n.right
		d.work.Add(1)
		for n.left != nil {
			n = n.left
			d.work.Add(1)
		}
		return n
	}
	for n.parent != nil && n == n.parent.right {
		n = n.parent
		d.work.Add(1)
	}
	if n.parent != nil {
		d.work.Add(1)
	}
	return n.parent
}

func (d *dirtyOrder) firstNode() *dirtyOrderNode {
	n := d.root
	for n != nil {
		d.work.Add(1)
		if dirtySize(n.left) > 0 {
			n = n.left
			continue
		}
		if n.active {
			return n
		}
		n = d.rawNext(n)
	}
	return nil
}

func (d *dirtyOrder) first() *Node {
	if n := d.firstNode(); n != nil {
		return n.node
	}
	return nil
}

func (d *dirtyOrder) successor(n *Node) *Node {
	start := d.nodes[n]
	if start == nil {
		return nil
	}
	for x := d.rawNext(start); x != nil; x = d.rawNext(x) {
		if x.active {
			return x.node
		}
	}
	return nil
}

// rank is the node's one-based position in sorted order.
func (d *dirtyOrder) rank(n *Node) (int, bool) {
	x := d.nodes[n]
	if x == nil || !x.active {
		return 0, false
	}
	d.work.Add(1)
	rank := dirtySize(x.left) + 1
	for x.parent != nil {
		d.work.Add(1)
		if x == x.parent.right {
			rank += dirtySize(x.parent.left)
			if x.parent.active {
				rank++
			}
		}
		x = x.parent
	}
	return rank, true
}

func (d *dirtyOrder) ordered() []*Node {
	out := make([]*Node, 0, dirtySize(d.root))
	for n := d.firstNode(); n != nil; n = d.rawNext(n) {
		if n.active {
			d.work.Add(1) // publication movement
			out = append(out, n.node)
		}
	}
	return out
}
