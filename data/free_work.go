// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/lyds_tree.c (BSD-3-Clause, © CESNET).

package data

// ownedNodes collects a forest including any-value payloads. Each output slot and every
// expansion of the traversal stack is reserved before allocating. Cleanup can then reserve
// reciprocal-link work and journal all affected objects before detaching any root.
func ownedNodes(roots []*Node, work *workCounter) ([]*Node, error) {
	if err := work.chargeHash(uint64(len(roots)) + 1); err != nil {
		return nil, err
	}
	stack := make([]*Node, 0, len(roots))
	for i := len(roots) - 1; i >= 0; i-- {
		stack = append(stack, roots[i])
	}
	var nodes []*Node
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		sib := &n.kids
		if n.any != nil {
			sib = &n.any.children
		}
		if err := work.chargeHash(1 + uint64(sib.SchemaLen()) + uint64(sib.OpaqueLen())); err != nil { //nolint:gosec // sibling counts are nonnegative
			return nil, err
		}
		nodes = append(nodes, n)
		for i := sib.OpaqueLen() - 1; i >= 0; i-- {
			stack = append(stack, sib.opaqueAt(i))
		}
		for i := sib.SchemaLen() - 1; i >= 0; i-- {
			stack = append(stack, sib.At(i))
		}
	}
	return nodes, nil
}

func reserveFreeLinks(nodes []*Node, work *workCounter) error {
	// chargeBulkFreeLinks allocates the order map for all nodes before its incremental plan.
	if err := work.chargeHash(uint64(len(nodes))); err != nil {
		return err
	}
	if !chargeBulkFreeLinks(nodes, func(n int64) bool { return work.chargeHash(uint64(n)) == nil }, work) { //nolint:gosec // cleanup planner charges nonnegative counts
		return work.hashErr
	}
	for _, n := range nodes {
		if err := n.touch(); err != nil {
			return err
		}
		sib := &n.kids
		if n.any != nil {
			sib = &n.any.children
		}
		if len(sib.rbGroups)+len(sib.rbOrders)+len(sib.pending) > 0 {
			if err := sib.touch(); err != nil {
				return err
			}
		}
	}
	return nil
}

// releaseOwnedRB models the lyds_tree plugin free callback after the complete owned forest
// has been reserved and touched. There are no surviving members to rotate or search.
func releaseOwnedRB(n *Node) {
	n.inRB, n.rbGroup, n.detachedRB, n.detachedGroup, n.carriedRB = false, nil, nil, nil, nil
	sib := &n.kids
	if n.any != nil {
		sib = &n.any.children
	}
	sib.rbTree, sib.rbGroups, sib.rbMore, sib.rbOrders = nil, nil, nil, nil
	sib.rbDual, sib.rbDualCount, sib.pending, sib.unsorted = nil, nil, nil, nil
}
