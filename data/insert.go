// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_insert_node and its helpers, lyd_unlink),
// src/tree_data_sorted.c (lyds_insert ordering), src/tree_data_free.c (lyd_free_tree) and
// src/tree_data_common.c (lyd_np_cont_dflt_*) (BSD-3-Clause, © CESNET).

package data

import (
	"fmt"
	"slices"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// insertOrder is lyd_insert_node's order argument (LYD_INSERT_NODE_*).
type insertOrder uint8

const (
	// insertDefault keeps schema order and sorts system-ordered lists and leaf-lists by value.
	insertDefault insertOrder = iota
	// insertLast appends; the caller guarantees the result is in order (LYD_PARSE_ORDERED input).
	insertLast
	// insertLastBySchema keeps schema order, the node after the instances of its schema node.
	insertLastBySchema
)

// opError is an error libyang logs with LOGERR (no validation code): Err is the LY_ERR name.
// The diagnostics of design 07 D1b turn it into a yang.Diagnostic.
type opError struct {
	Err string
	Msg string
}

func (e *opError) Error() string { return e.Msg }

// schemaRank is the position of s among its schema siblings in lys_getnext order: the data
// children of its data parent (choices and cases entered, the input or output of an operation),
// or the top-level nodes of its module.
func (t *Tree) schemaRank(s *schema.Node) int {
	if r, ok := t.rank[s]; ok {
		return r
	}
	if t.rank == nil {
		t.rank = map[*schema.Node]int{}
	}
	dp := s.DataParent()
	var top []*schema.Node
	if dp == nil {
		top = s.Module.Top
	}
	var opts schema.GetNextOpt
	if s.InOutput() {
		opts = schema.GetNextOutput
	}
	i := 0
	for c := range schema.GetNext(dp, top, opts) {
		t.rank[c] = i
		i++
	}
	if _, ok := t.rank[s]; !ok {
		t.rank[s] = i // not reachable through lys_getnext (extension data): after all others
	}
	return t.rank[s]
}

// after reports whether sibling a goes after n in schema order (the anchor test of
// lyd_insert_get_next_anchor and lyd_insert_node_find_anchor): opaque nodes are always last; at
// the top level the data of modules are ordered by strcmp of the module names.
func (t *Tree) after(a, n *Node) bool {
	switch {
	case a.schema == nil:
		return n.schema != nil
	case n.schema == nil:
		return false
	case a.parent == nil && n.parent == nil && a.schema.Module != n.schema.Module:
		return strings.Compare(a.schema.Module.Name, n.schema.Module.Name) > 0
	}
	return t.schemaRank(a.schema) > t.schemaRank(n.schema)
}

// sameOrAfter reports whether a is an instance of n's schema node or goes after it.
func (t *Tree) sameOrAfter(a, n *Node) bool {
	return a.schema != nil && a.schema == n.schema || t.after(a, n)
}

// sortedSupported is lyds_is_supported: system-ordered leaf-lists and keyed lists keep their
// instances sorted by value.
func sortedSupported(n *Node) bool {
	s := n.schema
	return s != nil && !s.UserOrdered && (s.Kind == schema.LeafList || s.Kind == schema.List && !s.Keyless())
}

// compareSorted orders two instances like rb_compare_leaflists / rb_compare_lists: by value, a
// list key by key in key order.
func compareSorted(a, b *Node) int {
	if a.schema.Kind == schema.LeafList {
		return types.Compare(a.value, b.value)
	}
	for i := 0; i < len(a.kids.list) && i < len(b.kids.list); i++ {
		ka, kb := a.kids.list[i], b.kids.list[i]
		if !ka.isKey() || !kb.isKey() {
			break
		}
		if c := types.Compare(ka.value, kb.value); c != 0 {
			return c
		}
	}
	return 0
}

// insert is lyd_insert_node: link the unlinked node n under parent (nil: the top level of t).
// A list instance must have all its keys (libyang links it into its parent only then).
func (t *Tree) insert(parent, n *Node, order insertOrder) {
	sib := &t.top
	if parent != nil {
		sib = &parent.kids
	}
	l := sib.list
	at := len(l)
	switch {
	case order == insertLast || n.schema == nil:
		// lyd_insert_node_last
	case order == insertDefault && sortedSupported(n):
		lo, _ := slices.BinarySearchFunc(l, n, func(a, n *Node) int { return boolCmp(t.sameOrAfter(a, n)) })
		hi, _ := slices.BinarySearchFunc(l[lo:], n, func(a, n *Node) int { return boolCmp(t.after(a, n)) })
		hi += lo
		if lo < hi { // instances exist: lyds_insert, after the equal ones (rb_insert_node goes right on 0)
			at, _ = slices.BinarySearchFunc(l[lo:hi], n, func(a, n *Node) int { return boolCmp(compareSorted(a, n) > 0) })
			at += lo
			break
		}
		at = hi
	default:
		// lyd_insert_node_ordby_schema: before the first sibling after n's schema node, data
		// before opaque nodes
		at, _ = slices.BinarySearchFunc(l, n, func(a, n *Node) int { return boolCmp(t.after(a, n)) })
	}
	sib.list = slices.Insert(l, at, n)
	n.parent = parent
	if parent == nil {
		n.tree = t
	}
	sib.added(n)
	if n.flags&Default == 0 {
		npContDfltDel(parent)
	}
	if n.isKey() {
		// the list's index key changed (lyd_insert_hash of the parent once all keys exist)
		if ls := parent.siblingsOf(); ls != nil {
			ls.idx = nil
		}
	}
}

// boolCmp turns "a goes after the searched node" into a comparison for an upper-bound search.
func boolCmp(after bool) int {
	if after {
		return 1
	}
	return -1
}

// unlinkCheck is lyd_unlink_check.
func unlinkCheck(n *Node) error {
	if n.isKey() {
		return &opError{"LY_EINVAL", fmt.Sprintf("Cannot unlink a list key \"%s\", unlink the list instance instead.", n.Name())}
	}
	return nil
}

// unlinkTree is lyd_unlink_tree: n with its subtree leaves its siblings.
func unlinkTree(n *Node) error {
	if err := unlinkCheck(n); err != nil {
		return err
	}
	unlink(n)
	return nil
}

// unlink is lyd_unlink (without the key check).
func unlink(n *Node) {
	sib := n.siblingsOf()
	if sib == nil {
		return
	}
	if i := slices.Index(sib.list, n); i >= 0 {
		sib.list = slices.Delete(sib.list, i, i+1)
	}
	sib.removed(n)
	parent := n.parent
	n.parent, n.tree = nil, nil
	if parent != nil {
		npContDfltSet(parent) // the last non-default node may be gone
	}
}

// freeTree is lyd_free_tree: a list key is refused, nothing is freed.
func freeTree(n *Node) error {
	if n.isKey() {
		return &opError{"LY_EINVAL", fmt.Sprintf("Cannot free a list key \"%s\", free the list instance instead.", n.Name())}
	}
	unlink(n)
	return nil
}

// isNPCont is lysc_is_np_cont.
func isNPCont(s *schema.Node) bool { return s != nil && s.Kind == schema.Container && !s.Presence }

// npContDfltSet is lyd_np_cont_dflt_set: an NP container whose children are all default becomes
// default, up the ancestors.
func npContDfltSet(p *Node) {
	for ; p != nil && p.flags&Default == 0 && isNPCont(p.schema); p = p.parent {
		for _, c := range p.kids.list {
			if c.flags&Default == 0 {
				return
			}
		}
		p.flags |= Default
	}
}

// npContDfltDel is lyd_np_cont_dflt_del: ancestors with an explicit descendant are not default.
func npContDfltDel(p *Node) {
	for ; p != nil && p.flags&Default != 0; p = p.parent {
		p.flags &^= Default
	}
}
