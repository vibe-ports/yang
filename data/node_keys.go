// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_common.c (lyd_child_no_keys) (BSD-3-Clause, © CESNET).

package data

import "iter"

// ChildrenNoKeys is lyd_child_no_keys: the children of n after the keys of a list instance (the
// keys come first), all children of other nodes.
func (n *Node) ChildrenNoKeys() iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		keys := true
		for c := range n.kids.all() {
			if keys = keys && c.isKey(); keys {
				continue
			}
			if !yield(c) {
				return
			}
		}
	}
}
