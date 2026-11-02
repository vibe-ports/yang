// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/lyds_tree.c (BSD-3-Clause, © CESNET).

package data

// ownedNodes collects a forest including any-value payloads. Each output slot and every
// expansion of the traversal stack is reserved before allocating. Cleanup can then reserve
// reciprocal-link work and journal all affected objects before detaching any root.
func ownedNodes(roots []*Node, work *workCounter) ([]*Node, error) {
	return ownedNodesChecked(roots, work, nil)
}

// ownedNodesChecked also polls validation cancellation, including for opaque payload
// nodes that ordinary validation never visits. Every stack and output slot is paid
// before it is appended; the returned plan is reused by the non-refusing cleanup.
func ownedNodesChecked(roots []*Node, work *workCounter, check func() error) ([]*Node, error) {
	if check != nil {
		if err := check(); err != nil {
			return nil, err
		}
	}
	if err := work.chargeHash(uint64(len(roots)) + 1); err != nil {
		return nil, err
	}
	stack := make([]*Node, 0, len(roots))
	for i := len(roots) - 1; i >= 0; i-- {
		stack = append(stack, roots[i])
	}
	var nodes []*Node
	for len(stack) > 0 {
		if check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
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
		for _, c := range sib.opaqueSpan().Reverse() {
			if check != nil {
				if err := check(); err != nil {
					return nil, err
				}
			}
			stack = append(stack, c)
		}
		for _, c := range sib.schemaSpan().Reverse() {
			if check != nil {
				if err := check(); err != nil {
					return nil, err
				}
			}
			stack = append(stack, c)
		}
	}
	return nodes, nil
}

func reserveFreeLinks(nodes []*Node, work *workCounter) error {
	return reserveFreeLinksChecked(nodes, work, nil)
}

func reserveFreeLinksChecked(nodes []*Node, work *workCounter, check func() error) error {
	// chargeBulkFreeLinks allocates the order map for all nodes before its incremental plan.
	if err := work.chargeHash(uint64(len(nodes))); err != nil {
		return err
	}
	var refusal error
	if !chargeBulkFreeLinks(nodes, func(n int64) bool {
		if check != nil {
			refusal = check()
			if refusal != nil {
				return false
			}
		}
		refusal = work.chargeHash(uint64(n)) //nolint:gosec // cleanup planner charges nonnegative counts
		return refusal == nil
	}, work) {
		return refusal
	}
	for _, n := range nodes {
		if check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		if err := linkGuardChecked(n, work, check); err != nil { // the cleaner's link edits (design 13 §2.4 (5))
			return err
		}
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
