// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_sorted.c (rb_find, rb_prev, rb_next) and
// src/tree_data.c (lyd_find_sibling_schema, lyd_compare_schema_equal) (BSD-3-Clause, © CESNET).

package data

import (
	"fmt"

	"github.com/vibe-ports/yang/internal/schema"
)

// retainedRemovalRefusal checks the D-0121 lifetime boundary before a primitive detaches n.
// Native lyd_unlink selects a tree by physical predecessor/schema lookup, not cached membership.
// rb_find then searches current values, which need not still satisfy the retained topology.
// This is a refusal check, not a substitute removal from some other tree.
func retainedRemovalRefusal(s *siblings, n *Node) error {
	return retainedRemovalChecked(s, n, false)
}

func retainedRemovalChecked(s *siblings, n *Node, ordered bool) error {
	if s == nil || n == nil || !n.inRB || n.rbGroup == nil {
		return nil
	}
	refuse := func(reason string) error {
		return &opError{"LY_EINVAL", fmt.Sprintf("Invalid arguments - unlinking %q %s and would retain a detached member (D-0121) (lyd_unlink()).", n.Name(), reason)}
	}
	if len(s.rbDual[n]) != 0 {
		return refuse("leaves references in another retained tree")
	}
	own := s.rbOrders[n.rbGroup]
	if own == nil || own.nodes[n] == nil {
		return nil // unindexed LAST/LAST_BY_SCHEMA tail, or a whole-tree transfer
	}
	w := s.work
	if err := w.chargeHash(1); err != nil {
		return err
	}
	leader := s.runLeader(n.schema)
	// The ordinary, single-context run has no separators. Other sibling sets must inspect
	// the physical predecessor before choosing the schema lookup branch of lyd_unlink.
	if len(s.contexts) > 1 || s.schemaUnordered || opChild(n.schema) {
		at := -1
		if s.order != nil {
			if err := w.chargeHash(rbHeight(len(s.list))); err != nil {
				return err
			}
			at = s.orderPosition(n)
		} else {
			for i := len(s.list) - 1; i >= 0; i-- {
				if err := w.chargeHash(1); err != nil {
					return err
				}
				w.Add(1)
				if s.list[i] == n {
					at = i
					break
				}
			}
		}
		if at < 0 {
			return refuse("has no linked position")
		}
		if at == 0 || s.list[at-1].schema != n.schema {
			leader = n
		} else if len(s.contexts) > 1 {
			var err error
			leader, err = retainedLookupLeader(w, s, n.schema)
			if err != nil {
				return err
			}
		}
		if leader == n {
			prev := (at + len(s.list) - 1) % len(s.list)
			alone := len(s.list) == 1 || s.list[prev].schema != n.schema && (at+1 == len(s.list) || s.list[at+1].schema != n.schema)
			if alone && len(own.nodes) > 1 {
				return refuse("takes metadata still referencing other linked members")
			}
		}
	}
	selected := s.rbOrders[s.rbGroups[leader]]
	if selected != own {
		return refuse("selects a tree other than its retained owner")
	}
	if ordered {
		return nil
	} // the batch proved native search reachability in one linear pass
	found, err := retainedFind(w, selected, n)
	if err != nil {
		return err
	}
	if !found {
		return refuse("is not found by the native value search")
	}
	return nil
}

// retainedFind follows rb_find's root shortcut, descent and equal-value predecessor/successor
// searches. Reserve each comparison and traversal step before reading it; equal runs can be
// linear, just as in native rb_find. It never mutates topology or membership.
func retainedFind(w *workCounter, d *dirtyOrder, target *Node) (bool, error) {
	if d.root == nil {
		return false, nil
	}
	if d.root.node == target {
		return true, nil
	}
	compare := func(n *dirtyOrderNode) (int, error) {
		if err := w.chargeHash(1 + rbCompareCost(n.node, target)); err != nil {
			return 0, err
		}
		w.Add(1)
		order, ok := sortedPairCompatible(w, n.node, target)
		if !ok {
			if w.hashErr != nil {
				return 0, w.hashErr
			}
			return 0, &opError{"LY_EINVAL", fmt.Sprintf("Invalid arguments - searching for %q requires incompatible direct-sort profiles (D-0121) (lyd_unlink()).", target.Name())}
		}
		return order, nil
	}
	// rb_prev/rb_next with a charge for every climbed or descended edge.
	step := func(n *dirtyOrderNode, next bool) (*dirtyOrderNode, error) {
		child, opposite := func(n *dirtyOrderNode) *dirtyOrderNode { return n.left }, func(n *dirtyOrderNode) *dirtyOrderNode { return n.right }
		if next {
			child, opposite = opposite, child
		}
		if c := child(n); c != nil {
			n = c
			for opposite(n) != nil {
				if err := w.chargeHash(1); err != nil {
					return nil, err
				}
				w.Add(1)
				n = opposite(n)
			}
			return n, nil
		}
		for n.parent != nil && n == child(n.parent) {
			if err := w.chargeHash(1); err != nil {
				return nil, err
			}
			w.Add(1)
			n = n.parent
		}
		return n.parent, nil
	}
	for cur := d.root; cur != nil; {
		c, err := compare(cur)
		if err != nil {
			return false, err
		}
		switch {
		case c > 0:
			cur = cur.left
		case c < 0:
			cur = cur.right
		case cur.node == target:
			return true, nil
		default:
			for _, next := range []bool{false, true} {
				for n, err := step(cur, next); n != nil || err != nil; n, err = step(n, next) {
					if err != nil {
						return false, err
					}
					c, err := compare(n)
					if err != nil {
						return false, err
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

// retainedBatchRefusal allows release of a wholly discarded forest. Partial removal must not
// leave native references behind. Count membership once per batch, including dual membership;
// never scan every retained tree for every removed node.
func retainedBatchRefusal(w *workCounter, ns []*Node) error {
	counts := map[*dirtyOrder]int{}
	seen := map[*Node]bool{}
	for _, n := range ns {
		if seen[n] {
			continue
		}
		s := n.siblingsOf()
		if s == nil {
			continue
		}
		if err := w.chargeHash(3 + 2*uint64(len(s.rbDual[n]))); err != nil {
			return err
		}
		seen[n] = true
		if d := s.rbOrders[n.rbGroup]; d != nil && d.nodes[n] != nil {
			counts[d]++
		}
		for _, g := range s.rbDual[n] {
			if d := s.rbOrders[g]; d != nil && d.nodes[n] != nil {
				counts[d]++
			}
		}
	}
	ordered := map[*dirtyOrder]bool{}
	checked := map[*dirtyOrder]bool{}
	for _, n := range ns {
		s := n.siblingsOf()
		if s == nil {
			continue
		}
		if err := w.chargeHash(1 + uint64(len(s.rbDual[n]))); err != nil {
			return err
		}
		retained := false
		if d := s.rbOrders[n.rbGroup]; d != nil {
			retained = counts[d] < len(d.nodes)
		}
		for _, g := range s.rbDual[n] {
			if d := s.rbOrders[g]; d != nil && counts[d] < len(d.nodes) {
				retained = true
			}
		}
		if retained {
			d := s.rbOrders[n.rbGroup]
			// A large homogeneous batch can prove reachability once. This avoids N independent
			// root descents for default deletion; small batches never scan a whole retained run.
			if d != nil && !checked[d] && len(s.contexts) <= 1 && counts[d] > len(d.nodes)/2 {
				if err := w.chargeHash(2); err != nil {
					return err
				}
				checked[d] = true
				var err error
				ordered[d], err = retainedOrdered(w, d)
				if err != nil {
					return err
				}
			}
			if err := retainedRemovalChecked(s, n, ordered[d]); err != nil {
				return err
			}
		}
	}
	return nil
}

// retainedOrdered proves the sufficient native rb_find precondition for a homogeneous tree.
// Its in-order values are monotone under one compatible comparator. Charge each visited node
// and comparison before reading it. A failed proof falls back to the exact native search.
func retainedOrdered(w *workCounter, d *dirtyOrder) (bool, error) {
	var previous *Node
	var walk func(*dirtyOrderNode) (bool, error)
	walk = func(cur *dirtyOrderNode) (bool, error) {
		if cur == nil {
			return true, nil
		}
		if err := w.chargeHash(1); err != nil {
			return false, err
		}
		w.Add(1)
		if cur.node.schema != d.root.node.schema {
			return false, nil
		}
		if ok, err := walk(cur.left); err != nil || !ok {
			return ok, err
		}
		if previous != nil {
			if err := w.chargeHash(rbCompareCost(previous, cur.node)); err != nil {
				return false, err
			}
			order, compatible := sortedPairCompatible(w, previous, cur.node)
			if w.hashErr != nil {
				return false, w.hashErr
			}
			if !compatible || order > 0 {
				return false, nil
			}
		}
		previous = cur.node
		return walk(cur.right)
	}
	return walk(d.root)
}

// sortKeyProfile builds each ordered schema-key profile once per operation. Layout and stored
// value checks still run for each compared instance, since parser edits can change that prefix.
func sortKeyProfile(w *workCounter, a, b *schema.Node) bool {
	pair := [2]*schema.Node{a, b}
	if w != nil {
		if result, ok := w.sortKeys[pair]; ok {
			return result
		}
	}
	result := len(a.Keys) == len(b.Keys)
	for i := 0; result && i < len(a.Keys); i++ {
		if w != nil {
			if w.chargeHash(1+uint64(len(a.Keys[i].Name)+len(b.Keys[i].Name))) != nil {
				return false
			}
			w.Add(1)
		}
		result = a.Keys[i].Name == b.Keys[i].Name
	}
	if w != nil {
		if w.chargeHash(1) != nil {
			return false
		}
		if w.sortKeys == nil {
			w.sortKeys = make(map[[2]*schema.Node]bool)
		}
		w.sortKeys[pair] = result
	}
	return result
}

// retainedLookupLeader is the mixed-context schema lookup used by lyd_unlink: physical first
// match without children_ht, earliest registered first-instance record with children_ht.
// Charge every inspected name/ancestor before comparing it; do not use cached membership.
func retainedLookupLeader(w *workCounter, s *siblings, sn *schema.Node) (*Node, error) {
	matches := func(a, b *schema.Node, parents bool) (bool, error) {
		if a == b {
			return true, nil
		}
		if a == nil || b == nil || a.Module == nil || b.Module == nil || (a.Module == b.Module || a.Module.Owner != nil && a.Module.Owner == b.Module.Owner) {
			return false, nil
		}
		for {
			if err := w.chargeHash(1 + uint64(len(a.Name)+len(b.Name)+len(a.Module.Name)+len(b.Module.Name))); err != nil {
				return false, err
			}
			w.Add(1)
			if a.Name != b.Name || a.Module.Name != b.Module.Name || parents && a.Kind != b.Kind {
				return false, nil
			}
			if !parents {
				return true, nil
			}
			a, b = a.Parent, b.Parent
			if a == nil || b == nil {
				return a == b, nil
			}
		}
	}
	if s.ht == nil {
		for _, n := range s.list {
			if err := w.chargeHash(1); err != nil {
				return nil, err
			}
			w.Add(1)
			if ok, err := matches(n.schema, sn, false); err != nil || ok {
				return n, err
			}
		}
		return nil, nil
	}
	var best *record
	for key, chain := range s.leaders {
		if err := w.chargeHash(1); err != nil {
			return nil, err
		}
		w.Add(1)
		if chain.first == nil || best != nil && best.seq < chain.first.seq {
			continue
		}
		if ok, err := matches(key, sn, true); err != nil {
			return nil, err
		} else if ok {
			best = chain.first
		}
	}
	if best == nil {
		return nil, nil
	}
	return best.node, nil
}
