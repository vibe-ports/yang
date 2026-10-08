// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_compare_single) (BSD-3-Clause, © CESNET).

package data

// CompareOptions are the lyd_compare_single options (LYD_COMPARE_*).
type CompareOptions struct {
	// FullRecursion is LYD_COMPARE_FULL_RECURSION: the whole subtrees must be equal, not only the
	// nodes themselves (a list instance by its keys, a container always).
	FullRecursion bool
	// Defaults is LYD_COMPARE_DEFAULTS: a default term is not equal to an explicit one with the
	// same value.
	Defaults bool
	// Opaque is LYD_COMPARE_OPAQ: an opaque node may equal the data node it stands for (same
	// schema node by module and name, same value text).
	Opaque bool
}

// Equal is lyd_compare_single: whether n and m are equal nodes (the same schema node, across
// schema snapshots an equal one; equal values, list keys and, with o.FullRecursion, subtrees).
// Two nil nodes are equal, a nil and a non-nil node are not.
func (n *Node) Equal(m *Node, o CompareOptions) bool {
	if n == nil || m == nil {
		return n == m
	}
	// a Tree of its own: it only caches schema ranks, so Equal writes nothing shared
	return compareSingleChecked(&Tree{}, n, m, o, false)
}
