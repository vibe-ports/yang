// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_sorted.c (BSD-3-Clause, © CESNET).
// Also ports unlink selection from src/tree_data.c (BSD-3-Clause, © CESNET).

package data

import (
	"fmt"
	"github.com/vibe-ports/yang/internal/schema"
)

// rbAdjacent is rb_prev/rb_next. Every visited topology link is charged before it is read.
func (w *workCounter) rbAdjacent(x *dirtyOrderNode, next bool) (*dirtyOrderNode, error) {
	child := func(n *dirtyOrderNode) *dirtyOrderNode {
		if next {
			return n.right
		}
		return n.left
	}
	if err := w.chargeHash(1); err != nil {
		return nil, err
	}
	if n := child(x); n != nil {
		for {
			if err := w.chargeHash(1); err != nil {
				return nil, err
			}
			var inner *dirtyOrderNode
			if next {
				inner = n.left
			} else {
				inner = n.right
			}
			if inner == nil {
				return n, nil
			}
			n = inner
		}
	}
	for x.parent != nil {
		if err := w.chargeHash(1); err != nil {
			return nil, err
		}
		p := x.parent
		if child(p) != x {
			return p, nil
		}
		x = p
	}
	return nil, nil
}

// rbFind follows native comparison order, including the equal-value predecessor/successor
// search. Looking up d.nodes[target] would incorrectly find a parser-mutated key that native
// search can no longer reach.
func (w *workCounter) rbFind(d *dirtyOrder, target *Node) (bool, error) {
	if d == nil || d.root == nil {
		return false, nil
	}
	if d.root.node == target {
		return true, nil
	}
	compare := func(a, b *Node) (int, bool) {
		if chargeCompareCost(w, a, b) != nil {
			return 0, false
		}
		if a.schema.Kind != b.schema.Kind {
			return 0, false
		}
		if a.schema.Kind == schema.LeafList {
			return rbSortClb(w, a.value, b.value)
		}
		return rbCompareLists(w, a, b)
	}
	for x := d.root; x != nil; {
		c, ok := compare(x.node, target)
		if !ok {
			if w.hashErr != nil {
				return false, w.hashErr
			}
			return false, rbUnlinkError(target)
		}
		switch {
		case c > 0:
			x = x.left
		case c < 0:
			x = x.right
		case x.node == target:
			return true, nil
		default:
			for _, next := range []bool{false, true} {
				for n := x; ; {
					var err error
					n, err = w.rbAdjacent(n, next)
					if err != nil {
						return false, err
					}
					if n == nil {
						break
					}
					c, ok = compare(n.node, target)
					if !ok {
						if w.hashErr != nil {
							return false, w.hashErr
						}
						return false, rbUnlinkError(target)
					}
					if c != 0 {
						break
					}
					if n.node == target {
						return true, nil
					}
				}
			}
			return false, nil
		}
	}
	return false, nil
}

func rbUnlinkError(n *Node) error {
	return &opError{"LY_EINVAL", fmt.Sprintf("Cannot unlink system-ordered node %q: native tree selection/search would retain a detached member (D-0121) (lyd_unlink()).", n.Name())}
}

// removalOrder selects lyd_unlink's leader, rather than a cached membership group. prev is the
// current physical predecessor (nil at the head). Bulk removals supply their intermediate links.
func (s *siblings) removalOrder(n, prev, next, tail *Node, around func(*Node) (*Node, *Node)) (*dirtyOrder, *Node, error) {
	w := s.work
	if w == nil {
		w = &workCounter{}
	}
	if !sortedSupported(n) {
		// Native lyd_unlink skips lyds_unlink for this node (a user-ordered or keyless instance
		// can still be a member of a tree built by an equivalent foreign system-ordered schema):
		// any retained tree holding it or owned by it would keep a detached reference (D-0121).
		if err := w.chargeHash(uint64(len(s.rbMore[n]))); err != nil {
			return nil, nil, err
		}
		if len(s.rbDual[n]) != 0 || n.inRB && s.rbOrders[n.rbGroup] != nil {
			return nil, nil, rbUnlinkError(n)
		}
		if d := s.rbOrders[s.rbGroups[n]]; d != nil && d.root != nil {
			return nil, nil, rbUnlinkError(n)
		}
		for _, g := range s.rbMore[n] {
			if d := s.rbOrders[g]; d != nil && d.root != nil {
				return nil, nil, rbUnlinkError(n)
			}
		}
		return nil, nil, nil
	}
	leader := n
	if prev != nil && prev.schema == n.schema {
		leader = s.runLeader(n.schema)
		if len(s.contexts) > 1 {
			if s.ht != nil {
				if err := w.chargeHash(uint64(len(s.leaders))); err != nil {
					return nil, nil, err
				}
				leader = s.leaderFirstMixed(n.schema)
			} else {
				leader = nil
				for _, c := range s.SchemaAll() {
					if err := w.chargeHash(1); err != nil {
						return nil, nil, err
					}
					if c.siblingsOf() == s && (c.schema == n.schema || ctxOf(c) != ctxOf(n) && compareSchemaEqual(c.schema, n.schema, true)) {
						leader = c
						break
					}
				}
			}
		}
	}
	if err := w.chargeHash(uint64(len(s.rbMore[n]))); err != nil {
		return nil, nil, err
	}
	for _, g := range s.rbMore[n] {
		if d := s.rbOrders[g]; d != nil && len(d.nodes) > 0 {
			return nil, nil, rbUnlinkError(n)
		}
	}
	group := s.rbGroups[leader]
	d := s.rbOrders[group]
	if own := s.rbGroups[n]; own != nil && own != group {
		if owned := s.rbOrders[own]; owned != nil && owned.root != nil {
			return nil, nil, rbUnlinkError(n)
		}
	}
	leaderAlone := false
	if leader != nil && d != nil {
		before, after := prev, next
		if leader != n {
			if around != nil {
				before, after = around(leader)
			} else {
				at := -1
				for i, j := 0, s.SchemaLen()-1; i <= j; i, j = i+1, j-1 {
					if err := w.chargeHash(1); err != nil {
						return nil, nil, err
					}
					if s.At(i) == leader {
						at = i
						break
					}
					if s.At(j) == leader {
						at = j
						break
					}
				}
				if at > 0 {
					before = s.At(at - 1)
				} else {
					before = nil
				}
				if at >= 0 && at+1 < s.SchemaLen() {
					after = s.At(at + 1)
				} else {
					after = nil
				}
			}
		}
		if before == nil {
			before = tail
		}
		leaderAlone = before == leader || (before == nil || before.schema != leader.schema) && (after == nil || after.schema != leader.schema)
		if leaderAlone && (leader != n && d.nodes[n] != nil || leader == n && len(d.nodes) > 0 && (len(d.nodes) != 1 || d.nodes[n] == nil)) {
			return nil, nil, rbUnlinkError(n)
		}
	}
	// The closed compatibility profile refuses native dangling-reference transitions. Never
	// silently remove from a different retained tree (including dual membership).
	if len(s.rbDual[n]) != 0 || n.inRB && n.rbGroup != group && s.rbOrders[n.rbGroup] != nil {
		return nil, nil, rbUnlinkError(n)
	}
	if leaderAlone {
		if leader == n {
			return d, leader, nil
		}
		return nil, leader, nil
	}
	found, err := w.rbFind(d, n)
	if err != nil {
		return nil, nil, err
	}
	if !found && d != nil && d.nodes[n] != nil {
		return nil, nil, rbUnlinkError(n)
	}
	if !found {
		return d, leader, nil // lyds_unlink moves metadata before rb_find, even for a nonmember
	}
	return d, leader, nil
}

func (s *siblings) nativeTail() *Node {
	if s.OpaqueLen() > 0 {
		return s.opaqueAt(s.OpaqueLen() - 1)
	}
	if s.SchemaLen() > 0 {
		return s.At(s.SchemaLen() - 1)
	}
	return nil
}
