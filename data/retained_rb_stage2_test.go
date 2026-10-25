// SPDX-License-Identifier: BSD-3-Clause AND ISC AND BSD-2-Clause
// Ported from libyang v5.8.6 src/tree_data_sorted.c (rb_insert_node, rb_insert_color,
// rb_rotate_left, rb_rotate_right, rb_iter_traversal) for the test reference refRB
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
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// refRB is an independent test reference for libyang's red-black tree: rb_insert_node with
// rb_insert_color and the rotations (tree_data_sorted.c:373-478,668-715), and rb_iter_traversal
// (294-320). It never calls the port's dirtyOrder, so a shape it predicts is the native one.
type refRB struct {
	root *refRBNode
}

type refRBNode struct {
	n                   *Node
	left, right, parent *refRBNode
	red                 bool
}

func refCompare(a, b *Node) int {
	if a.schema.Kind == schema.LeafList {
		return types.Compare(a.value, b.value)
	}
	c, _ := rbCompareLists(nil, a, b)
	return c
}

func (r *refRB) rotateLeft(x *refRBNode) {
	y := x.right
	x.right = y.left
	if y.left != nil {
		y.left.parent = x
	}
	y.parent = x.parent
	switch {
	case x.parent == nil:
		r.root = y
	case x == x.parent.left:
		x.parent.left = y
	default:
		x.parent.right = y
	}
	y.left, x.parent = x, y
}

func (r *refRB) rotateRight(x *refRBNode) {
	y := x.left
	x.left = y.right
	if y.right != nil {
		y.right.parent = x
	}
	y.parent = x.parent
	switch {
	case x.parent == nil:
		r.root = y
	case x == x.parent.left:
		x.parent.left = y
	default:
		x.parent.right = y
	}
	y.right, x.parent = x, y
}

func (r *refRB) insert(n *Node) {
	x := &refRBNode{n: n, red: true}
	var parent *refRBNode
	comp := 0
	for tmp := r.root; tmp != nil; {
		parent = tmp
		if comp = refCompare(tmp.n, n); comp > 0 {
			tmp = tmp.left
		} else {
			tmp = tmp.right
		}
	}
	x.parent = parent
	switch {
	case parent == nil:
		r.root = x
	case comp > 0:
		parent.left = x
	default:
		parent.right = x
	}
	for p := x.parent; p != nil && p.red; p = x.parent {
		g := p.parent
		if p == g.left {
			if u := g.right; u != nil && u.red {
				u.red, p.red, g.red = false, false, true
				x = g
				continue
			}
			if p.right == x {
				r.rotateLeft(p)
				p, x = x, p
			}
			p.red, g.red = false, true
			r.rotateRight(g)
		} else {
			if u := g.left; u != nil && u.red {
				u.red, p.red, g.red = false, false, true
				x = g
				continue
			}
			if p.left == x {
				r.rotateRight(p)
				p, x = x, p
			}
			p.red, g.red = false, true
			r.rotateLeft(g)
		}
	}
	r.root.red = false
}

// postOrder is rb_iter_traversal's visiting order, computed recursively.
func (r *refRB) postOrder() []*Node {
	var out []*Node
	var walk func(*refRBNode)
	walk = func(x *refRBNode) {
		if x == nil {
			return
		}
		walk(x.left)
		walk(x.right)
		out = append(out, x.n)
	}
	walk(r.root)
	return out
}

// checkRBShape asserts that the retained topology of the run led by leader has exactly the
// reference's nodes, links and colours.
func checkRBShape(t *testing.T, sib *siblings, leader *Node, ref *refRB) {
	t.Helper()
	group := sib.rbGroups[leader]
	d := sib.rbOrders[group]
	if ref.root == nil {
		if sib.rbTree[leader] {
			t.Fatalf("run of %s has a tree, the reference has none", leader.Value())
		}
		return
	}
	if d == nil {
		t.Fatalf("run of %s has no retained topology, the reference has one", leader.Value())
	}
	count := 0
	var walk func(*dirtyOrderNode, *refRBNode, string)
	walk = func(x *dirtyOrderNode, y *refRBNode, path string) {
		if x == nil || y == nil {
			if (x == nil) != (y == nil) {
				t.Fatalf("shape differs at %s: port %v, reference %v", path, x != nil, y != nil)
			}
			return
		}
		count++
		if x.node != y.n || x.red != y.red {
			t.Fatalf("shape differs at %s: port %s red=%t, reference %s red=%t", path,
				x.node.Value(), x.red, y.n.Value(), y.red)
		}
		walk(x.left, y.left, path+"L")
		walk(x.right, y.right, path+"R")
	}
	walk(d.root, ref.root, "root")
	if count != len(d.nodes) {
		t.Fatalf("retained topology maps %d nodes, %d reachable", len(d.nodes), count)
	}
}

func retainedRBTop(t *testing.T, set *schema.Set) *schema.Node {
	t.Helper()
	for _, module := range set.Modules {
		for _, sn := range module.Top {
			if sn.Name == "top" {
				return sn
			}
		}
	}
	t.Fatal("top leaf-list schema not compiled")
	return nil
}

func retainedRBTerm(t *testing.T, sn *schema.Node, value string) *Node {
	t.Helper()
	v, diag := types.Store(sn.Type, value, types.FormatJSON, types.JSONHints("string")|types.HintStringDatatypes,
		types.ModuleNames{Set: sn.Module.Owner}, sn)
	if diag != nil {
		t.Fatal(diag)
	}
	return newTerm(sn, v)
}

// refModel follows the native history of one top-level run: a tree exists once a second
// instance is inserted by value (lyds_additionally_create_rb_tree from the existing instance).
type refModel struct {
	ref     refRB
	pending []*Node // instances linked before the tree exists, in physical order
	tree    bool
}

func (m *refModel) insert(n *Node) {
	if !m.tree {
		if len(m.pending) == 0 {
			m.pending = append(m.pending, n)
			return
		}
		for _, p := range m.pending {
			m.ref.insert(p)
		}
		m.pending, m.tree = nil, true
	}
	m.ref.insert(n)
}

// TestRetainedRBShapeFollowsInsertionHistory: insertions, single and batched Merge, and foreign
// ApplyDiff creates all go through rb_insert of the retained tree, so its exact shape follows the
// native insertion history (contract §2 "shape and colours follow native insertion history").
func TestRetainedRBShapeFollowsInsertionHistory(t *testing.T) {
	a, b := retainedRBSets(t)
	schemaA, schemaB := retainedRBTop(t, a), retainedRBTop(t, b)
	tr := newTree(a)
	model := &refModel{}
	present := map[int]bool{}
	rng := rand.New(rand.NewPCG(0x301, 2)) //nolint:gosec // deterministic mutation sequence
	fresh := func() int {
		for {
			if v := rng.IntN(1 << 20); !present[v] {
				present[v] = true
				return v
			}
		}
	}
	steps := 2048
	if raceEnabled {
		steps = 384 // every step checks the whole tree: quadratic under the race detector
	}
	for step := range steps {
		switch rng.IntN(4) {
		case 0, 1:
			sn := schemaA
			if rng.IntN(2) != 0 {
				sn = schemaB
			}
			n := retainedRBTerm(t, sn, fmt.Sprint(fresh()))
			if err := tr.insert(nil, n, insertDefault); err != nil {
				t.Fatalf("step %d insert: %v", step, err)
			}
			model.insert(n)
		case 2:
			values := make([]int, 1+rng.IntN(5))
			for i := range values {
				values[i] = fresh()
			}
			// lyd_merge requires the target's context (that of its first node)
			src := parseOnlyXML(t, tr.context(), retainedRBXML("top", values...))
			if err := tr.Merge(src); err != nil {
				t.Fatalf("step %d merge: %v", step, err)
			}
			// lyd_merge_sibling_r duplicates each source instance in source order and inserts it
			var added []*Node
			for _, n := range tr.top.list {
				if !slices.ContainsFunc(model.pending, func(p *Node) bool { return p == n }) &&
					(model.ref.root == nil || !refContains(&model.ref, n)) {
					added = append(added, n)
				}
			}
			slices.SortFunc(added, refCompare) // source order is the source's sorted order
			for _, n := range added {
				model.insert(n)
			}
		case 3:
			v := fresh()
			src := parseOnlyXML(t, b, retainedRBXML("top", v))
			diff, err := Diff(nil, src, DiffOptions{})
			if err != nil {
				t.Fatalf("step %d foreign diff: %v", step, err)
			}
			if err := tr.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
				t.Fatalf("step %d foreign apply: %v", step, err)
			}
			for _, n := range tr.top.list {
				if n.Value() == fmt.Sprint(v) {
					model.insert(n)
				}
			}
		}
		checkTreeRetainedRB(t, tr)
		if len(tr.top.list) > 0 {
			checkRBShape(t, &tr.top, tr.top.list[0], &model.ref)
		}
	}
}

func refContains(r *refRB, n *Node) bool {
	for x := r.root; x != nil; {
		if x.n == n {
			return true
		}
		if refCompare(x.n, n) > 0 {
			x = x.left
		} else {
			x = x.right
		}
	}
	return false
}

// TestMergeNodes3RBIterOrder retires D-0061: lyds_merge_nodes3 inserts the source tree's
// members in rb_iter_traversal order. The libyang 5.8.6 C probes moved 7 and 11 equal values
// into a run with a tree and observed s0 s2 s4 s6 s5 s3 s1 and s0 s2 s1 s4 s6 s8 s10 s9 s7 s5 s3.
func TestMergeNodes3RBIterOrder(t *testing.T) {
	f := newFixture()
	for _, tc := range []struct {
		count int
		want  []int
	}{
		{7, []int{0, 2, 4, 6, 5, 3, 1}},
		{11, []int{0, 2, 1, 4, 6, 8, 10, 9, 7, 5, 3}},
	} {
		t.Run(fmt.Sprint(tc.count), func(t *testing.T) {
			tr, c := f.cont(t)
			dst := &refRB{}
			for _, v := range []string{"2", "9"} {
				n := f.term(t, f.ll, v)
				mustLink(t, tr.insert(c, n, insertDefault))
				dst.insert(n)
			}
			vals := make([]string, tc.count)
			for i := range vals {
				vals[i] = "5"
			}
			src, sn := f.srcList(t, false, vals...)
			srcRef := &refRB{}
			for _, n := range sn {
				srcRef.insert(n)
			}
			for _, n := range srcRef.postOrder() {
				dst.insert(n)
			}
			if err := tr.insertChild(c, src.top.first()); err != nil {
				t.Fatal(err)
			}
			got := c.kids.list[1 : 1+tc.count]
			for i, want := range tc.want {
				if got[i] != sn[want] {
					idx := make([]int, len(got))
					for j, n := range got {
						idx[j] = slices.Index(sn, n)
					}
					t.Fatalf("equal source values in %v, want %v", idx, tc.want)
				}
			}
			checkRBShape(t, &c.kids, c.kids.list[0], dst)
			checkTreeRetainedRB(t, tr)
		})
	}
}

// TestMergeNodes2HashOrder: lyds_merge_nodes2_front hashes the destination leader's tree
// predecessors backwards, each before the current first instance, and the parent's children
// table is created at the fourth child from the siblings linked so far (tree_data_hash.c:181-197).
// Equal values register b, c, d, a.
func TestMergeNodes2HashOrder(t *testing.T) {
	f := newFixture()
	tr, c := f.cont(t)
	two := f.term(t, f.ll, "2")
	mustLink(t, tr.insert(c, two, insertDefault))
	src, sn := f.srcList(t, false, "1", "1", "1", "1", "3")
	ref := &refRB{}
	for _, n := range sn {
		ref.insert(n)
	}
	ref.insert(two)
	if err := tr.insertChild(c, src.top.first()); err != nil {
		t.Fatal(err)
	}
	want := []*Node{sn[0], sn[1], sn[2], sn[3], two, sn[4]}
	if !slices.Equal(c.kids.list, want) {
		t.Fatalf("nodes2 order = %v", names(c.Children()))
	}
	chain := c.kids.ht[sn[0].hkey]
	if chain == nil {
		t.Fatal("no children table")
	}
	var got []*Node
	for r := chain.first; r != nil; r = r.next {
		got = append(got, r.node)
	}
	if wantHash := []*Node{sn[1], sn[2], sn[3], sn[0]}; !slices.Equal(got, wantHash) {
		t.Fatalf("equal-value registrations = %v, want b c d a", got)
	}
	checkRBShape(t, &c.kids, c.kids.list[0], ref)
	checkTreeRetainedRB(t, tr)
}

// TestWholeRunTransferThenForeignInsert covers every source/destination tree-presence
// combination of lyd_move_nodes, then a foreign-context insertion through the moved tree.
func TestWholeRunTransferThenForeignInsert(t *testing.T) {
	a, b := retainedRBSets(t)
	schemaA, schemaB := retainedRBTop(t, a), retainedRBTop(t, b)
	for _, srcVals := range [][]string{{"10"}, {"10", "30"}, {"20", "10"}, {"20", "10", "30"}} {
		for _, dstVals := range [][]string{nil, {"25"}, {"25", "5"}, {"25", "5", "15"}} {
			t.Run(fmt.Sprintf("src%v-dst%v", srcVals, dstVals), func(t *testing.T) {
				dst, src := newTree(a), newTree(a)
				dstModel, srcModel := &refModel{}, &refModel{}
				var dstNodes []*Node
				for _, v := range dstVals {
					n := retainedRBTerm(t, schemaA, v)
					mustLink(t, dst.insert(nil, n, insertDefault))
					dstModel.insert(n)
					dstNodes = append(dstNodes, n)
				}
				for _, v := range srcVals {
					n := retainedRBTerm(t, schemaA, v)
					mustLink(t, src.insert(nil, n, insertDefault))
					srcModel.insert(n)
				}
				srcPhysical := slices.Clone(src.top.list)
				if err := dst.insertSibling(dst.top.first(), src.top.first()); err != nil {
					t.Fatal(err)
				}
				// the expected history (lyds_merge, or lyd_move_nodes_at_once with no destination)
				result := &refModel{}
				switch {
				case len(dstNodes) == 0:
					result = srcModel
				case !dstModel.tree && !srcModel.tree:
					result = dstModel
					for _, n := range srcPhysical {
						result.insert(n)
					}
				case dstModel.tree && !srcModel.tree:
					result = dstModel
					for _, n := range srcPhysical {
						result.insert(n)
					}
				case !dstModel.tree:
					result = srcModel
					for _, n := range dstModel.pending {
						result.insert(n)
					}
				default:
					result = dstModel
					for _, n := range srcModel.ref.postOrder() {
						result.insert(n)
					}
				}
				checkTreeRetainedRB(t, dst)
				checkRBShape(t, &dst.top, dst.top.list[0], &result.ref)
				if len(src.top.rbOrders) != 0 || len(src.top.rbTree) != 0 {
					t.Fatal("the source keeps retained RB state after the move")
				}
				foreign := retainedRBTerm(t, schemaB, "17")
				mustLink(t, dst.insert(nil, foreign, insertDefault))
				result.insert(foreign)
				checkTreeRetainedRB(t, dst)
				checkRBShape(t, &dst.top, dst.top.list[0], &result.ref)
			})
		}
	}
}

// TestLastBySchemaTailOutsideTree: LAST_BY_SCHEMA appends outside the run's RB tree
// (lyd_insert_node_ordby_schema); a later sorted insertion links after its RB predecessor,
// ahead of the tail, and the tree's shape ignores the tail.
func TestLastBySchemaTailOutsideTree(t *testing.T) {
	f := newFixture()
	tr, c := f.cont(t)
	ref := &refRB{}
	for _, v := range []string{"5", "3"} {
		n := f.term(t, f.ll, v)
		mustLink(t, tr.insert(c, n, insertDefault))
		ref.insert(n)
	}
	tail := f.term(t, f.ll, "1")
	mustLink(t, tr.insert(c, tail, insertLastBySchema))
	d := c.kids.rbOrders[c.kids.rbGroups[c.kids.list[0]]]
	if tail.inRB || d.nodes[tail] != nil || !c.kids.hasUnsorted(f.ll) {
		t.Fatal("LAST_BY_SCHEMA instance recorded in the RB tree")
	}
	four := f.term(t, f.ll, "4")
	mustLink(t, tr.insert(c, four, insertDefault))
	ref.insert(four)
	if got := names(c.Children()); !slices.Equal(got, []string{"ll=3", "ll=4", "ll=5", "ll=1"}) {
		t.Fatalf("after the tail: %v", got)
	}
	checkRBShape(t, &c.kids, c.kids.list[0], ref)
	checkTreeRetainedRB(t, tr)
}

const retainedRBBitsModuleA = `module retained-rb-round5 { yang-version 1.1;
  namespace "urn:vibe-ports:yang:retained-rb-round5"; prefix r;
  container c { leaf-list v { type uint32; } }
  leaf-list top { type bits { bit a { position 0; } bit b { position 1; } bit c { position 2; } } }
}`

const retainedRBBitsModuleB = `module retained-rb-round5 { yang-version 1.1;
  namespace "urn:vibe-ports:yang:retained-rb-round5"; prefix r;
  container c { leaf-list v { type uint32; } }
  leaf-list top { type bits { bit a { position 0; } bit b { position 1; } bit c { position 2; }
    bit w { position 8; } } }
}`

// TestFailedForeignMoveRestoresRBTopology: a move refused by D-0121 must leave the source run's
// retained tree exactly as it was, so a later foreign insertion still lands in order.
func TestFailedForeignMoveRestoresRBTopology(t *testing.T) {
	a, c := retainedRBSets(t)
	bits := reviewDiffSet(t, retainedRBBitsModuleA)
	schemaA, schemaC, schemaBits := retainedRBTop(t, a), retainedRBTop(t, c), retainedRBTop(t, bits)
	src := newTree(a)
	ref := &refModel{}
	var nodes []*Node
	for _, v := range []string{"1", "3"} {
		n := retainedRBTerm(t, schemaA, v)
		mustLink(t, src.insert(nil, n, insertDefault))
		ref.insert(n)
		nodes = append(nodes, n)
	}
	dst := newTree(bits)
	for _, v := range []string{"a", "b"} {
		mustLink(t, dst.insert(nil, retainedRBTerm(t, schemaBits, v), insertDefault))
	}
	if err := dst.Insert(nil, nodes[1]); err == nil || !strings.Contains(err.Error(), "D-0121") {
		t.Fatalf("foreign move = %v, want the D-0121 refusal", err)
	}
	if nodes[1].tree != src {
		t.Fatal("the refused node left its origin")
	}
	checkTreeRetainedRB(t, src)
	checkRBShape(t, &src.top, src.top.list[0], &ref.ref)
	four := retainedRBTerm(t, schemaC, "4")
	mustLink(t, src.insert(nil, four, insertDefault))
	ref.insert(four)
	if got := retainedRBValues(&src.top); !slices.Equal(got, []string{"1", "3", "4"}) {
		t.Fatalf("after the refused move: %v", got)
	}
	checkTreeRetainedRB(t, src)
	checkRBShape(t, &src.top, src.top.list[0], &ref.ref)
}

// TestForeignLeaderNotCompared: rb_insert_node compares only the nodes on its descent. A foreign
// minimum with a wider bitmap becomes the physical leader but not the tree root, so a narrower
// local insertion never compares it.
func TestForeignLeaderNotCompared(t *testing.T) {
	a, b := reviewDiffSet(t, retainedRBBitsModuleA), reviewDiffSet(t, retainedRBBitsModuleB)
	schemaA, schemaB := retainedRBTop(t, a), retainedRBTop(t, b)
	tr := newTree(a)
	for _, v := range []string{"a", "b"} {
		mustLink(t, tr.insert(nil, retainedRBTerm(t, schemaA, v), insertDefault))
	}
	mustLink(t, tr.insert(nil, retainedRBTerm(t, schemaB, ""), insertDefault))
	if tr.top.list[0].schema != schemaB {
		t.Fatalf("foreign empty value is not the leader: %v", retainedRBValues(&tr.top))
	}
	if err := tr.insert(nil, retainedRBTerm(t, schemaA, "c"), insertDefault); err != nil {
		t.Fatalf("local insertion past a wider foreign leader: %v", err)
	}
	if got := retainedRBValues(&tr.top); !slices.Equal(got, []string{"", "a", "b", "c"}) {
		t.Fatalf("order = %v", got)
	}
	checkTreeRetainedRB(t, tr)
}

// TestMoveMixedContextSourceTree: one source tree holds instances of equivalent schemas from two
// contexts; lyds_merge_nodes3 traverses and moves all its members at once.
func TestMoveMixedContextSourceTree(t *testing.T) {
	a, b := retainedRBSets(t)
	schemaA, schemaB := retainedRBTop(t, a), retainedRBTop(t, b)
	src, dst := newTree(a), newTree(a)
	srcRef, dstRef := &refModel{}, &refModel{}
	for _, v := range []string{"5", "7"} {
		n := retainedRBTerm(t, schemaA, v)
		mustLink(t, dst.insert(nil, n, insertDefault))
		dstRef.insert(n)
	}
	for _, x := range []struct {
		sn *schema.Node
		v  string
	}{{schemaA, "1"}, {schemaA, "3"}, {schemaB, "2"}} {
		n := retainedRBTerm(t, x.sn, x.v)
		mustLink(t, src.insert(nil, n, insertDefault))
		srcRef.insert(n)
	}
	if err := dst.insertSibling(dst.top.first(), src.top.first()); err != nil {
		t.Fatal(err)
	}
	for _, n := range srcRef.ref.postOrder() {
		dstRef.insert(n)
	}
	if got := retainedRBValues(&dst.top); !slices.Equal(got, []string{"1", "2", "3", "5", "7"}) {
		t.Fatalf("moved mixed tree = %v", got)
	}
	if src.top.len() != 0 {
		t.Fatalf("source keeps %d nodes", src.top.len())
	}
	checkTreeRetainedRB(t, dst)
	checkRBShape(t, &dst.top, dst.top.list[0], &dstRef.ref)
}

// TestMergeSelectsMatchedLeaderTree: after a foreign new minimum leads the destination run, the
// run's tree is read from that leader (lyds_get_rb_tree), so moving a source tree is nodes3.
func TestMergeSelectsMatchedLeaderTree(t *testing.T) {
	a, b := retainedRBSets(t)
	schemaA, schemaB := retainedRBTop(t, a), retainedRBTop(t, b)
	src, dst := newTree(a), newTree(a)
	srcRef, dstRef := &refModel{}, &refModel{}
	for _, x := range []struct {
		sn *schema.Node
		v  string
	}{{schemaA, "1"}, {schemaA, "3"}, {schemaB, "0"}} {
		n := retainedRBTerm(t, x.sn, x.v)
		mustLink(t, dst.insert(nil, n, insertDefault))
		dstRef.insert(n)
	}
	for _, v := range []string{"2", "4"} {
		n := retainedRBTerm(t, schemaA, v)
		mustLink(t, src.insert(nil, n, insertDefault))
		srcRef.insert(n)
	}
	if err := dst.insertSibling(dst.top.first(), src.top.first()); err != nil {
		t.Fatal(err)
	}
	for _, n := range srcRef.ref.postOrder() {
		dstRef.insert(n)
	}
	if got := retainedRBValues(&dst.top); !slices.Equal(got, []string{"0", "1", "2", "3", "4"}) {
		t.Fatalf("merged = %v", got)
	}
	checkTreeRetainedRB(t, dst)
	checkRBShape(t, &dst.top, dst.top.list[0], &dstRef.ref)
}

// TestRemovalUndoRestoresExactTopology: a failed move puts back the exact tree rb_remove changed,
// for every member of trees built from ascending runs plus interleaved values (the history
// 10..150, 45, 55, 42 rotates four levels below the removal path).
func TestRemovalUndoRestoresExactTopology(t *testing.T) {
	f := newFixture()
	f.ll.Type = &schema.Type{Base: schema.Uint16}
	type shape struct {
		n                   *Node
		left, right, parent *dirtyOrderNode
		size                int
		red, active         bool
	}
	histories := [][]int{{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120, 130, 140, 150, 45, 55, 42}}
	rng := rand.New(rand.NewPCG(0x301, 7)) //nolint:gosec // deterministic histories
	for range 32 {
		h := make([]int, 1+rng.IntN(40))
		for i := range h {
			h[i] = rng.IntN(1000)
		}
		histories = append(histories, h)
	}
	for _, history := range histories {
		var nodes []*Node
		build := func() *dirtyOrder {
			d := &dirtyOrder{}
			for _, n := range nodes {
				_, _ = d.insertCompatible(n, func(a, b *Node) (int, bool) { return compareSorted(a, b), true })
			}
			return d
		}
		for _, v := range history {
			nodes = append(nodes, f.term(t, f.ll, fmt.Sprint(v)))
		}
		for _, victim := range nodes {
			d := build()
			before := map[*dirtyOrderNode]shape{}
			for _, x := range d.nodes {
				before[x] = shape{x.node, x.left, x.right, x.parent, x.size, x.red, x.active}
			}
			root := d.root
			var work workCounter
			bound := d.removalBound()
			restore := d.armRemovalUndo(victim, &work)
			d.remove(victim)
			if work.hashUsed > bound {
				t.Fatalf("removal journaled %d nodes, bound %d", work.hashUsed, bound)
			}
			restore()
			if d.root != root || len(d.nodes) != len(before) {
				t.Fatalf("history %v, removing %s: root or membership not restored", history, victim.Value())
			}
			for x, want := range before {
				if got := (shape{x.node, x.left, x.right, x.parent, x.size, x.red, x.active}); got != want {
					t.Fatalf("history %v, removing %s: node %s not restored", history, victim.Value(), x.node.Value())
				}
			}
		}
	}
}
