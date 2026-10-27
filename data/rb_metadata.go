// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_sorted.c (BSD-3-Clause, © CESNET).
// Also ports metadata transport from src/tree_data.c (BSD-3-Clause, © CESNET).

package data

import "slices"

// rbAdoptionBound includes the empty metadata that unlink will leave on a linked source.
// It is reserved with the link, before physical publication can make adoption fail late.
func rbAdoptionBound(s *siblings, n *Node, w *workCounter) (uint64, error) {
	carried := uint64(len(n.carriedRB))
	head := n.detachedRB != nil && carried > 0 // link hands slot 0 to adoptCarried first
	slots := carried
	if head {
		slots++
	}
	if from := n.siblingsOf(); from != nil && from.rbGroups[n] != nil {
		slots += 1 + uint64(len(from.rbMore[n]))
		if from.rbTree[n] {
			slots-- // selected populated metadata moves to the successor or detachedRB
		}
	}
	if slots == 0 {
		return 0, nil
	}
	// adoptCarried reserves its scan of the queue (slots + 1) before reading it
	bound := 4*slots + 4*uint64(len(s.rbMore[n])) + 9
	if w.forestMove || carried == 0 {
		return bound, nil
	}
	// The carried queue is scanned below: charge that traversal before reading it.
	if err := w.chargeHash(carried + 1); err != nil {
		return 0, err
	}
	add := func(order *dirtyOrder) error {
		if w.visits {
			w.Add(1)
		}
		if err := w.chargeHash(uint64(len(order.nodes))); err != nil {
			return err
		}
		bound = addHashBound(bound, 2*uint64(len(order.nodes)))
		for member := range order.nodes {
			bound = addHashBound(bound, uint64(len(s.rbDual[member]))+1)
		}
		return nil
	}
	if head {
		if err := add(n.detachedRB); err != nil {
			return 0, err
		}
	}
	for _, c := range n.carriedRB {
		if err := add(c.order); err != nil {
			return 0, err
		}
	}
	return bound, nil
}

// rbMetadataOrigin saves only the two affected metadata owners, proportional to their queues.
// Single-move recovery must restore items cleared by lyds_insert, including NULL-root items.
func rbMetadataOrigin(s *siblings, n, next *Node, w *workCounter) (func(), error) {
	count := len(n.carriedRB)
	for _, owner := range []*Node{n, next} {
		if owner != nil && s.rbGroups[owner] != nil {
			count += 1 + len(s.rbMore[owner])
		}
	}
	if count == 0 && n.detachedRB == nil {
		return func() {}, nil
	}
	if err := w.chargeHash(8*uint64(count) + 8); err != nil {
		return nil, err
	}
	type savedOwner struct {
		node     *Node
		groups   []*rbGroup
		orders   []*dirtyOrder
		unsorted bool
	}
	var owners []savedOwner
	for _, owner := range []*Node{n, next} {
		if owner == nil {
			continue
		}
		groups := s.metaGroups(owner)
		saved := savedOwner{node: owner, groups: groups, unsorted: s.unsorted[owner]}
		for _, g := range groups {
			saved.orders = append(saved.orders, s.rbOrders[g])
		}
		owners = append(owners, saved)
	}
	carried := slices.Clone(n.carriedRB)
	detached, detachedGroup := n.detachedRB, n.detachedGroup
	return func() {
		if err := s.touch(); err != nil {
			return
		}
		if err := n.touch(); err != nil {
			return
		}
		if s.rbOrders == nil {
			s.rbOrders = map[*rbGroup]*dirtyOrder{}
		}
		for _, saved := range owners {
			for i, g := range saved.groups {
				if d := saved.orders[i]; d != nil {
					d.work = s.work
					s.rbOrders[g] = d
				}
			}
			s.setMetaGroups(saved.node, saved.groups)
			if saved.unsorted {
				markUnsorted(s, saved.node)
			} else {
				delete(s.unsorted, saved.node)
			}
		}
		n.carriedRB, n.detachedRB, n.detachedGroup = carried, detached, detachedGroup
	}, nil
}

// rbCarry is metadata attached to a node while a whole forest is in transit. These are the
// additional native trees, not the selected tree consumed/transferred by lyds_merge.
type rbCarry struct {
	group *rbGroup
	order *dirtyOrder
}

type forestMembership struct {
	node   *Node
	groups []*rbGroup
}

func captureForestMemberships(s *siblings, nodes []*Node, w *workCounter) ([]forestMembership, error) {
	if err := w.chargeHash(2*uint64(len(nodes)) + 1); err != nil {
		return nil, err
	}
	var size uint64
	for _, n := range nodes {
		size = addHashBound(size, uint64(len(s.rbDual[n]))+1)
	}
	if err := w.chargeHash(size); err != nil {
		return nil, err
	}
	out := make([]forestMembership, 0, len(nodes))
	for _, n := range nodes {
		w.Add(1)
		groups := make([]*rbGroup, 0, len(s.rbDual[n])+1)
		if n.inRB && s.rbOrders[n.rbGroup] != nil {
			groups = append(groups, n.rbGroup)
		}
		groups = append(groups, s.rbDual[n]...)
		out = append(out, forestMembership{n, groups})
	}
	return out, nil
}

// Whole moves preserve all source memberships, even when a graph's metadata arrives after
// one of its members. Finalization uses a temporary set, so overlapping histories cost O(S)
// for S retained memberships instead of repeatedly scanning growing per-node slices.
func finishForestMemberships(s *siblings, entries []forestMembership, w *workCounter) error {
	for _, entry := range entries {
		w.Add(1)
		n := entry.node
		old := s.rbDual[n]
		if err := w.chargeHash(4*uint64(len(old)+len(entry.groups)) + 1); err != nil {
			return err
		}
		if len(old)+len(entry.groups) == 0 {
			continue
		}
		seen := make(map[*rbGroup]bool, len(old)+len(entry.groups))
		groups := make([]*rbGroup, 0, len(old)+len(entry.groups))
		for _, g := range old {
			s.rbDualCount[g]--
		}
		add := func(g *rbGroup) {
			if seen[g] || n.inRB && n.rbGroup == g {
				return
			}
			d := s.rbOrders[g]
			if d == nil || d.nodes[n] == nil {
				return
			}
			seen[g] = true
			groups = append(groups, g)
			if s.rbDualCount == nil {
				s.rbDualCount = map[*rbGroup]int{}
			}
			s.rbDualCount[g]++
		}
		for _, g := range old {
			add(g)
		}
		for _, g := range entry.groups {
			add(g)
		}
		if len(groups) > 0 {
			if s.rbDual == nil {
				s.rbDual = map[*Node][]*rbGroup{}
			}
			s.rbDual[n] = groups
		} else {
			delete(s.rbDual, n)
		}
	}
	return w.hashErr
}

// promoteRB changes the cached primary membership. The previous primary is not a dual
// membership; newly built/transferred destination groups are independent of the old cache.
func (s *siblings) promoteRB(n *Node, group *rbGroup, w *workCounter) error {
	if old := n.rbGroup; n.inRB && old != nil && old != group {
		if d := s.rbOrders[old]; d != nil && d.nodes[n] != nil {
			if err := w.chargeHash(4); err != nil {
				return err
			}
			if s.rbDual == nil {
				s.rbDual = map[*Node][]*rbGroup{}
			}
			if s.rbDualCount == nil {
				s.rbDualCount = map[*rbGroup]int{}
			}
			s.rbDual[n] = append(s.rbDual[n], old)
			s.rbDualCount[old]++
		}
	}
	n.inRB, n.rbGroup = true, group
	return nil
}

func (s *siblings) metaGroups(n *Node) []*rbGroup {
	g := s.rbGroups[n]
	if g == nil {
		return nil
	}
	return append([]*rbGroup{g}, s.rbMore[n]...)
}

func (s *siblings) setMetaGroups(n *Node, groups []*rbGroup) {
	if len(groups) == 0 {
		delete(s.rbGroups, n)
		delete(s.rbTree, n)
		delete(s.rbMore, n)
		delete(s.unsorted, n)
		return
	}
	if s.rbGroups == nil {
		s.rbGroups = map[*Node]*rbGroup{}
	}
	if s.rbTree == nil {
		s.rbTree = map[*Node]bool{}
	}
	s.rbGroups[n] = groups[0]
	if d := s.rbOrders[groups[0]]; d != nil && d.root != nil {
		s.rbTree[n] = true
	} else {
		delete(s.rbTree, n)
	}
	if len(groups) > 1 {
		if s.rbMore == nil {
			s.rbMore = map[*Node][]*rbGroup{}
		}
		s.rbMore[n] = groups[1:]
	} else {
		delete(s.rbMore, n)
	}
}

// compactMeta refreshes selection after rb_remove_node empties a tree. Native keeps the
// metadata (with a NULL root); selecting a later metadata item would change future dispatch.
func (s *siblings) compactMeta(n *Node) {
	groups := s.metaGroups(n)
	live := false
	for _, g := range groups {
		d := s.rbOrders[g]
		live = live || d != nil && d.root != nil
	}
	s.setMetaGroups(n, groups)
	if !live {
		delete(s.unsorted, n)
	} else if len(groups) > 1 {
		markUnsorted(s, n)
	}
}

func (s *siblings) ownsGroup(n *Node, g *rbGroup) bool {
	return s.rbGroups[n] == g || slices.Contains(s.rbMore[n], g)
}

func (s *siblings) transferCost(old, next *Node) uint64 {
	if old == nil || next == nil || old == next {
		return 0
	}
	return 8 + 16*uint64(len(s.rbMore[old])+len(s.rbMore[next]))
}

// adoptCarried prepends source metadata: it existed on the node before a destination leader's
// metadata could be appended by lyds_link_data_node. Every secondary graph remains owned.
func (t *Tree) adoptCarried(s *siblings, n *Node) error {
	if len(n.carriedRB) == 0 {
		return nil
	}
	if err := s.touch(); err != nil {
		return err
	}
	// the bound below scans the carried queue: reserve that scan before reading it
	if err := t.work.chargeHash(uint64(len(n.carriedRB)) + 1); err != nil {
		return err
	}
	amount := 2*uint64(len(n.carriedRB)) + 4*uint64(len(s.rbMore[n])) + 8
	for _, c := range n.carriedRB {
		if t.work.visits { // tests only
			t.work.Add(1)
		}
		if !t.work.forestMove {
			amount = addHashBound(amount, 2*uint64(len(c.order.nodes))+1)
		}
	}
	if err := t.work.chargeHash(amount); err != nil {
		return err
	}
	groups := make([]*rbGroup, 0, len(n.carriedRB)+len(s.rbMore[n])+1)
	if s.rbOrders == nil {
		s.rbOrders = map[*rbGroup]*dirtyOrder{}
	}
	for _, c := range n.carriedRB {
		groups = append(groups, c.group)
		s.rbOrders[c.group] = c.order
		c.order.work = &t.work
		if t.work.forestMove {
			continue
		}
		for member := range c.order.nodes {
			if member.inRB && member.rbGroup == c.group {
				continue
			}
			if err := t.work.chargeHash(uint64(len(s.rbDual[member])) + 1); err != nil {
				return err
			}
			if slices.Contains(s.rbDual[member], c.group) {
				continue
			}
			if s.rbDual == nil {
				s.rbDual = map[*Node][]*rbGroup{}
			}
			if s.rbDualCount == nil {
				s.rbDualCount = map[*rbGroup]int{}
			}
			s.rbDual[member] = append(s.rbDual[member], c.group)
			s.rbDualCount[c.group]++
		}
	}
	groups = append(groups, s.metaGroups(n)...)
	s.setMetaGroups(n, groups)
	if len(groups) > 1 {
		markUnsorted(s, n)
	}
	n.carriedRB = nil
	return nil
}
