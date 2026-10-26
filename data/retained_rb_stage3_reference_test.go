// SPDX-License-Identifier: BSD-3-Clause AND ISC AND BSD-2-Clause
// Ported from libyang v5.8.6 src/tree_data_sorted.c (rb_remove, rb_remove_color,
// rb_rotate_left, rb_rotate_right) for the independent removal reference refRB.erase
// (BSD-3-Clause, © CESNET).
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

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// erase is an independent parent-link formulation of native rb_remove. Selection is by node
// identity, independently of the production membership map. The production rb_find descent is
// tested separately with unreachable parser-mutated keys and duplicate-heavy histories.
func (r *refRB) erase(target *Node) {
	var find func(*refRBNode) *refRBNode
	find = func(n *refRBNode) *refRBNode {
		if n == nil || n.n == target {
			return n
		}
		if x := find(n.left); x != nil {
			return x
		}
		return find(n.right)
	}
	z := find(r.root)
	if z == nil {
		panic("reference target absent")
	}
	transplant := func(old, next *refRBNode) {
		switch {
		case old.parent == nil:
			r.root = next
		case old.parent.left == old:
			old.parent.left = next
		default:
			old.parent.right = next
		}
		if next != nil {
			next.parent = old.parent
		}
	}
	y := z
	red := y.red
	var x, parent *refRBNode
	switch {
	case z.left == nil:
		x, parent = z.right, z.parent
		transplant(z, z.right)
	case z.right == nil:
		x, parent = z.left, z.parent
		transplant(z, z.left)
	default:
		y = z.right
		for y.left != nil {
			y = y.left
		}
		red, x = y.red, y.right
		if y.parent == z {
			parent = y
		} else {
			parent = y.parent
			transplant(y, x)
			y.right = z.right
			y.right.parent = y
		}
		transplant(z, y)
		y.left = z.left
		y.left.parent = y
		y.red = z.red
	}
	isRed := func(n *refRBNode) bool { return n != nil && n.red }
	for !red && x != r.root && !isRed(x) && parent != nil {
		if x == parent.left {
			sibling := parent.right
			if isRed(sibling) {
				sibling.red = false
				parent.red = true
				r.rotateLeft(parent)
				sibling = parent.right
			}
			if !isRed(sibling.left) && !isRed(sibling.right) {
				sibling.red = true
				x = parent
				parent = x.parent
				continue
			}
			if !isRed(sibling.right) {
				sibling.left.red = false
				sibling.red = true
				r.rotateRight(sibling)
				sibling = parent.right
			}
			sibling.red = parent.red
			parent.red = false
			sibling.right.red = false
			r.rotateLeft(parent)
			x = r.root
		} else {
			sibling := parent.left
			if isRed(sibling) {
				sibling.red = false
				parent.red = true
				r.rotateRight(parent)
				sibling = parent.left
			}
			if !isRed(sibling.left) && !isRed(sibling.right) {
				sibling.red = true
				x = parent
				parent = x.parent
				continue
			}
			if !isRed(sibling.left) {
				sibling.right.red = false
				sibling.red = true
				r.rotateLeft(sibling)
				sibling = parent.left
			}
			sibling.red = parent.red
			parent.red = false
			sibling.left.red = false
			r.rotateRight(parent)
			x = r.root
		}
	}
	if !red && x != nil {
		x.red = false
	}
}

func TestStage3NativeRemovalReference(t *testing.T) {
	a, _ := retainedRBSets(t)
	sn := retainedRBTop(t, a)
	for seed := range 4 {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			tr := newTree(a)
			ref := &refRB{}
			rng := rand.New(rand.NewPCG(0x301, uint64(seed))) //nolint:gosec // persisted native-history seeds
			for step := range 1024 {
				if tr.top.SchemaLen() < 3 || rng.IntN(2) == 0 {
					n := retainedRBTerm(t, sn, fmt.Sprint(rng.IntN(64)))
					if err := tr.insert(nil, n, insertDefault); err != nil {
						t.Fatalf("step %d insert: %v", step, err)
					}
					ref.insert(n)
				} else {
					n := tr.top.At(rng.IntN(tr.top.SchemaLen()))
					if err := n.Unlink(); err != nil {
						t.Fatalf("step %d unlink: %v", step, err)
					}
					ref.erase(n)
				}
				if tr.top.SchemaLen() > 1 {
					checkRBShape(t, &tr.top, tr.top.first(), ref)
				}
				checkTreeRetainedRB(t, tr)
			}
		})
	}
}
