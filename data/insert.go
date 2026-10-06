// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_insert_node and its helpers, lyd_insert_before,
// lyd_unlink), src/tree_data_sorted.c (lyds_insert ordering, lyds_additionally_create_rb_tree),
// src/tree_data_free.c (lyd_free_tree, lyd_free_siblings) and src/tree_data_common.c
// (lyd_np_cont_dflt_*) (BSD-3-Clause, © CESNET).

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
	// insertLast appends (LYD_PARSE_ORDERED input); a node that would break schema order is
	// placed by schema instead, and schema nodes stay before opaque ones (see insertPos).
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
	t.rankMu.Lock()
	defer t.rankMu.Unlock()
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

// upper returns the first index of l[lo:] for which after holds (l is monotone in it), counting
// the comparisons.
func (t *Tree) upper(l []*Node, lo int, after func(*Node) bool) int {
	i, _ := slices.BinarySearchFunc(l[lo:], struct{}{}, func(a *Node, _ struct{}) int {
		t.work++
		if after(a) {
			return 1
		}
		return -1
	})
	return lo + i
}

// insertPos is where lyd_insert_node links n among l.
// Siblings are monotone in (module name at the top level, schema rank) with opaque nodes last;
// every search relies on it, so insertLast never breaks it: libyang appends a schema node after
// an opaque tail too, which only LYD_PARSE_ORDERED input with unknown nodes reaches (not used by
// the M1 fixtures; VERIFY(order/ordered-opaque) if it is ever exported).
func (t *Tree) insertPos(sib *siblings, n *Node, order insertOrder) int {
	l := sib.list
	if n.schema == nil || len(l) == 0 {
		return len(l) // lyd_insert_node_last
	}
	afterN := func(a *Node) bool { return t.after(a, n) }
	tail := len(l) // start of the opaque tail
	for tail > 0 && l[tail-1].schema == nil {
		tail--
	}
	if order == insertDefault && sortedSupported(n) {
		lo := t.upper(l[:tail], 0, func(a *Node) bool { return t.sameOrAfter(a, n) })
		hi := t.upper(l[:tail], lo, afterN)
		if lo == hi {
			return hi // no instance yet: by schema
		}
		if sib.unsorted[n.schema] {
			// lyds_additionally_create_rb_tree: the run built without its RB tree is sorted now,
			// stably (rb_insert_node puts equal values after the existing ones)
			slices.SortStableFunc(l[lo:hi], compareSorted)
			delete(sib.unsorted, n.schema)
		}
		// after the equal values (rb_insert_node goes right on 0); append fast path
		t.work++
		if compareSorted(l[hi-1], n) <= 0 {
			return hi
		}
		return t.upper(l[:hi], lo, func(a *Node) bool { return compareSorted(a, n) > 0 })
	}
	// lyd_insert_node_ordby_schema; append fast path
	at := tail
	t.work++
	if tail > 0 && t.after(l[tail-1], n) {
		at = t.upper(l[:tail], 0, afterN)
	}
	if order != insertDefault && sortedSupported(n) && at > 0 && l[at-1].schema == n.schema && compareSorted(l[at-1], n) > 0 {
		markUnsorted(sib, n.schema)
	}
	return at
}

// markUnsorted records that the run of s no longer is in value order: it was appended without
// libyang's RB tree, which the next sorted insertion creates and so re-sorts the run.
func markUnsorted(sib *siblings, s *schema.Node) {
	if sib.unsorted == nil {
		sib.unsorted = map[*schema.Node]bool{}
	}
	sib.unsorted[s] = true
}

// insert is lyd_insert_node: link the unlinked node n under parent (nil: the top level of t).
// A list instance must have all its keys (libyang links it into its parent only then).
func (t *Tree) insert(parent, n *Node, order insertOrder) {
	sib := t.childrenOf(parent)
	t.link(parent, sib, n, t.insertPos(sib, n, order))
}

// insertBefore is lyd_insert_before for a user-ordered instance: n goes right before anchor,
// an instance of the same schema node.
func (t *Tree) insertBefore(anchor, n *Node) {
	sib := anchor.siblingsOf()
	t.link(anchor.parent, sib, n, slices.Index(sib.list, anchor))
}

func (t *Tree) childrenOf(parent *Node) *siblings {
	if parent == nil {
		return &t.top
	}
	return &parent.kids
}

// link puts the unlinked n at position at of sib (under parent) and updates the children index
// and the default flags of the NP-container ancestors.
func (t *Tree) link(parent *Node, sib *siblings, n *Node, at int) {
	if n.parent != nil || n.tree != nil {
		panic("data: inserting a linked node") // internal invariant: callers unlink first
	}
	if at == len(sib.list) {
		sib.list = append(sib.list, n)
	} else {
		// ponytail: insertion in the middle moves the tail (O(n) per insert, like design 02's
		// slice); the parsers append and sort runs lazily, a tree structure if API-built
		// reversed inputs ever matter.
		sib.list = slices.Insert(sib.list, at, n)
	}
	n.parent = parent
	if parent == nil {
		n.tree = t
	}
	sib.hashAdd(parent, n)
	if n.flags&FlagDefault == 0 {
		npContDfltDel(parent)
	}
	if n.isKey() {
		rehashParent(parent) // the list's keys changed: lyd_hash + lyd_insert_hash of the parent
	}
}

// rehashParent moves the list instance l to the bucket of its current keys in its parent's index.
func rehashParent(l *Node) {
	if ls := l.siblingsOf(); ls != nil && l.parent != nil {
		ls.hashRemove(l)
		ls.hashAdd(l.parent, l)
	}
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
	detach(sib, n)
}

// detach clears the links of n, already removed from sib.list.
func detach(sib *siblings, n *Node) {
	sib.hashRemove(n)
	parent := n.parent
	wasKey := n.isKey()
	n.parent, n.tree = nil, nil
	if parent != nil {
		npContDfltSet(parent) // the last non-default node may be gone
		if wasKey {
			rehashParent(parent)
		}
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

// unlinkAll unlinks every node of ns with one compaction per sibling list, for the bulk
// removals of validation (auto-deleted nodes) and lyd_free_siblings. A list key is refused
// before anything is unlinked.
func unlinkAll(ns []*Node) error {
	for _, n := range ns {
		if err := unlinkCheck(n); err != nil {
			return err
		}
	}
	gone := map[*Node]bool{}
	var sibs []*siblings
	for _, n := range ns {
		if sib := n.siblingsOf(); sib != nil && !gone[n] {
			gone[n] = true
			if !slices.Contains(sibs, sib) {
				sibs = append(sibs, sib)
			}
		}
	}
	for _, sib := range sibs {
		sib.list = slices.DeleteFunc(sib.list, func(n *Node) bool { return gone[n] })
	}
	for _, n := range ns {
		if gone[n] {
			delete(gone, n)
			detach(n.siblingsOf(), n)
		}
	}
	return nil
}

// isNPCont is lysc_is_np_cont.
func isNPCont(s *schema.Node) bool { return s != nil && s.Kind == schema.Container && !s.Presence }

// npContDfltSet is lyd_np_cont_dflt_set: an NP container whose children are all default becomes
// default, up the ancestors.
func npContDfltSet(p *Node) {
	for ; p != nil && p.flags&FlagDefault == 0 && isNPCont(p.schema); p = p.parent {
		for _, c := range p.kids.list {
			if c.flags&FlagDefault == 0 {
				return
			}
		}
		p.flags |= FlagDefault
	}
}

// npContDfltDel is lyd_np_cont_dflt_del: ancestors with an explicit descendant are not default.
func npContDfltDel(p *Node) {
	for ; p != nil && p.flags&FlagDefault != 0; p = p.parent {
		p.flags &^= FlagDefault
	}
}
