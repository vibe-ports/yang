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
		t.work.Add(1)
		if after(a) {
			return 1
		}
		return -1
	})
	return lo + i
}

// insertPos is where lyd_insert_node links the schema node n among sib.list (opaque nodes are
// kept apart, always last).
// Schema nodes are monotone in (module name at the top level, schema rank); every search relies
// on it, so insertLast never breaks it: libyang appends after the last sibling whatever it is,
// which only LYD_PARSE_ORDERED input that is not in schema order, or that has unknown nodes,
// reaches (D-0058).
func (t *Tree) insertPos(sib *siblings, n *Node, order insertOrder) int {
	l := sib.list
	if len(l) == 0 {
		return 0
	}
	afterN := func(a *Node) bool { return t.after(a, n) }
	if order == insertDefault && sortedSupported(n) {
		lo := t.upper(l, 0, func(a *Node) bool { return t.sameOrAfter(a, n) })
		hi := t.upper(l, lo, afterN)
		if lo == hi {
			return hi // no instance yet: by schema, no RB tree for a single instance
		}
		n.inRB = true
		if !sib.rbTree[n.schema] {
			// lyds_additionally_create_rb_tree: the run gets its RB tree from all its instances,
			// inserted in order, so it is sorted stably (rb_insert_node puts equal values after
			// the existing ones)
			slices.SortStableFunc(l[lo:hi], compareSorted)
			for _, a := range l[lo:hi] {
				a.inRB = true
			}
			markRB(sib, n.schema)
		} else if sib.unsorted[n.schema] {
			// instances appended after the tree was made are not in it (only diff.c's
			// LAST_BY_SCHEMA does that): lyds_link_data_node puts n right after its RB
			// predecessor, the greatest tree member not above it, or before the leader
			at := lo
			for i := lo; i < hi; i++ {
				t.work.Add(1)
				if l[i].inRB && compareSorted(l[i], n) <= 0 {
					at = i + 1
				}
			}
			return at
		}
		// after the equal values (rb_insert_node goes right on 0); append fast path
		t.work.Add(1)
		if compareSorted(l[hi-1], n) <= 0 {
			return hi
		}
		return t.upper(l[:hi], lo, func(a *Node) bool { return compareSorted(a, n) > 0 })
	}
	// lyd_insert_node_ordby_schema; append fast path
	at := len(l)
	t.work.Add(1)
	if t.after(l[at-1], n) {
		at = t.upper(l, 0, afterN)
	}
	if order != insertDefault && sortedSupported(n) && sib.rbTree[n.schema] {
		markUnsorted(sib, n.schema) // n is not in the run's RB tree
	}
	return at
}

// markRB records that the run of s has libyang's RB tree (created by its first sorted insertion
// into an existing run).
func markRB(sib *siblings, s *schema.Node) {
	if sib.rbTree == nil {
		sib.rbTree = map[*schema.Node]bool{}
	}
	sib.rbTree[s] = true
}

// markUnsorted records that the run of s has instances outside its RB tree (appended by
// insertLast/insertLastBySchema after the tree was made).
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
	at := -1 // opaque: appended to the opaque nodes
	if n.schema != nil {
		at = t.insertPos(sib, n, order)
	}
	t.link(parent, sib, n, at)
}

// insertLazy is insert for the parsers: a system-ordered instance joining a run with libyang's
// order (nothing appended outside its RB tree) goes to the end of the run, and the run is
// stably sorted once, before the parser or anyone else reads it (sortLazy). libyang's
// incremental RB insert puts every instance after its equal values, which is the stable sort of
// the insertion order, so the order is the same; the parser then costs O(n log n), not one shift
// of the run per instance.
func (t *Tree) insertLazy(parent, n *Node) {
	sib := t.childrenOf(parent)
	s := n.schema
	if s == nil || !sortedSupported(n) || sib.unsorted[s] || inOpNode(s) {
		t.insert(parent, n, insertDefault)
		return
	}
	lo := t.upper(sib.list, 0, func(a *Node) bool { return t.sameOrAfter(a, n) })
	hi := t.upper(sib.list, lo, func(a *Node) bool { return t.after(a, n) })
	if lo == hi {
		t.insert(parent, n, insertDefault) // the first instance
		return
	}
	if !sib.lazy[s] && compareSorted(sib.list[hi-1], n) <= 0 {
		// a clean run that n extends in order stays clean: the eager append (no sort pending, so
		// a read before the next insertion costs nothing)
		t.work.Add(1)
		t.insert(parent, n, insertDefault)
		return
	}
	if sib.lazy == nil {
		sib.lazy = map[*schema.Node]bool{}
	}
	if len(sib.lazy) == 0 {
		t.lazy = append(t.lazy, sib)
	}
	sib.lazy[s] = true
	t.link(parent, sib, n, hi)
}

// inOpNode reports whether s is a child of an rpc, action or notification node. Those children
// take the eager insert: an operation's input and output siblings are not monotone in schema
// order (lyd_insert_get_next_anchor walks only the new node's own input or output), so their
// place is the eager path's anchor, not the end of a run found by binary search.
func inOpNode(s *schema.Node) bool {
	dp := s.DataParent()
	return dp != nil && (dp.Kind == schema.RPC || dp.Kind == schema.Action || dp.Kind == schema.Notification)
}

// sortLazy sorts the runs of sib that insertLazy appended to, as libyang's RB insert orders them:
// a run of two or more instances gets its RB tree.
func (t *Tree) sortLazy(sib *siblings) {
	for s := range sib.lazy {
		lo := t.schemaIndex(sib, s)
		if lo < 0 {
			continue // freed after an error
		}
		hi := lo
		for hi < len(sib.list) && sib.list[hi].schema == s {
			hi++
		}
		slices.SortStableFunc(sib.list[lo:hi], func(a, b *Node) int {
			t.work.Add(1)
			return compareSorted(a, b)
		})
		if hi-lo > 1 {
			for _, a := range sib.list[lo:hi] {
				a.inRB = true
			}
			markRB(sib, s)
		}
		sib.gen++
	}
	clear(sib.lazy)
}

// sortAllLazy sorts every run the parser left unsorted.
func (t *Tree) sortAllLazy() {
	for _, sib := range t.lazy {
		t.sortLazy(sib)
	}
	t.lazy = nil
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

// link puts the unlinked n at position at of sib.list (an opaque node: of sib.opq, appended when
// at is -1) under parent, and updates the children index and the default flags of the NP-container
// ancestors.
func (t *Tree) link(parent *Node, sib *siblings, n *Node, at int) {
	sib.work = &t.work
	switch {
	case n.schema == nil && (at < 0 || at >= len(sib.opq)):
		sib.opq = append(sib.opq, n)
	case n.schema == nil:
		sib.opq = slices.Insert(sib.opq, at, n)
	default:
		moved := sib.insertAt(at, n)
		if t.work.shifts {
			t.work.Add(int64(moved))
		}
	}
	t.attach(parent, sib, n)
}

// insertAt puts n at position at of s.list and returns how many slots it shifted: the shorter
// side moves, the front into headroom kept before the list (s.base), so appending and prepending
// are amortized O(1) and an edit in the middle shifts at most half the list. Batches (Merge,
// moved runs) are spliced in one pass instead (splice).
// ponytail: a single edit in the middle stays O(n/2) shifts (a 160k list: ~40µs); an ordered
// structure if API edits of huge sibling lists in random order ever matter.
func (s *siblings) insertAt(at int, n *Node) int {
	l := s.list
	if at >= len(l)/2 || len(l) == 0 {
		c := cap(l)
		s.list = slices.Insert(l, at, n)
		if cap(s.list) != c {
			s.base = nil // reallocated: drop the old array
		}
		return len(l) - at
	}
	off := cap(s.base) - cap(l)
	if off <= 0 || off >= len(s.base) || &s.base[off] != &l[0] {
		// no headroom: copy the list between as much free space as it is long on either side
		buf := make([]*Node, 3*len(l)+1)
		off = len(l) + 1
		copy(buf[off:], l)
		s.base = buf
		l = buf[off : off+len(l)]
	}
	nl := s.base[off-1 : off+len(l)]
	copy(nl, l[:at])
	nl[at] = n
	s.list = nl
	return at
}

// attach finishes linking n, already placed in sib's lists, under parent.
func (t *Tree) attach(parent *Node, sib *siblings, n *Node) {
	if n.parent != nil || n.tree != nil {
		panic("data: inserting a linked node") // internal invariant: callers unlink first
	}
	n.parent = parent
	if parent == nil {
		n.tree = t
	}
	sib.gen++
	sib.hashAdd(parent, n)
	if n.flags&FlagDefault == 0 {
		npContDfltDel(parent)
	}
	if n.isKey() {
		rehashParent(parent) // the list's keys changed: lyd_hash + lyd_insert_hash of the parent
	}
}

// splice links the unlinked nodes ns, ascending, into the sorted run lo..hi of sib.list in one
// pass: each after the equal values, or before them when first. Linking them one by one would
// shift the list once per node (quadratic for a merged run).
func (t *Tree) splice(parent *Node, sib *siblings, lo, hi int, ns []*Node, first bool) {
	sib.work = &t.work
	old := sib.list
	out := make([]*Node, 0, len(old)+len(ns))
	out = append(out, old[:lo]...)
	j := lo
	for _, n := range ns {
		for j < hi && (first && t.less(old[j], n) || !first && !t.less(n, old[j])) {
			out = append(out, old[j])
			j++
		}
		out = append(out, n)
	}
	sib.list, sib.base = append(out, old[j:]...), nil
	if t.work.shifts {
		t.work.Add(int64(len(sib.list)))
	}
	for _, n := range ns {
		t.attach(parent, sib, n)
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

// unlink is lyd_unlink (without the key check); n's own leafref links go
// (lyd_unlink_ignore_lyds).
func unlink(n *Node) {
	sib := n.siblingsOf()
	if sib == nil {
		return
	}
	freeLinks(n)
	if n.schema == nil {
		if i := slices.Index(sib.opq, n); i >= 0 {
			sib.opq = slices.Delete(sib.opq, i, i+1)
		}
	} else if i := slices.Index(sib.list, n); i >= 0 {
		sib.list = slices.Delete(sib.list, i, i+1)
	}
	sib.hashRemove(n)
	sib.gen++
	if n.schema != nil && !sib.has(n.schema) {
		sib.runGone(n.schema)
	} else if n.inRB && !slices.ContainsFunc(sib.list, func(x *Node) bool { return x.schema == n.schema && x.inRB }) {
		// the last RB-tree member left while instances appended outside the tree remain:
		// rb_remove_node empties the tree, so the next sorted insertion builds it again from the
		// whole run and re-sorts it (lyds_additionally_create_rb_tree)
		sib.runGone(n.schema)
	}
	npContDfltSet(detach(n)) // the last non-default node may be gone
}

// detach clears the links of n, already removed from its siblings and their index, and returns
// its former parent.
func detach(n *Node) *Node {
	parent := n.parent
	wasKey := n.isKey()
	n.parent, n.tree, n.inRB = nil, nil, false
	if wasKey {
		rehashParent(parent)
	}
	return parent
}

// freeTree is lyd_free_tree: a list key is refused, nothing is freed but the leafref links of
// the subtree (lyd_free_subtree).
func freeTree(n *Node) error {
	if n.isKey() {
		return &opError{"LY_EINVAL", fmt.Sprintf("Cannot free a list key \"%s\", free the list instance instead.", n.Name())}
	}
	unlink(n)
	for d := range n.All() {
		freeLinks(d)
	}
	return nil
}

// unlinkAll unlinks every node of ns with one compaction per sibling list, for the bulk
// removals of validation (auto-deleted nodes) and lyd_free_siblings. A list key is refused
// before anything is unlinked.
func (t *Tree) unlinkAll(ns []*Node) error {
	for _, n := range ns {
		if err := unlinkCheck(n); err != nil {
			return err
		}
	}
	gone := map[*Node]bool{}
	sibs := map[*siblings]map[idxKey]bool{} // touched lists and their touched buckets
	var order []*siblings
	for _, n := range ns {
		sib := n.siblingsOf()
		if sib == nil || gone[n] {
			continue
		}
		gone[n] = true
		freeLinks(n) // lyd_unlink_ignore_lyds
		if sibs[sib] == nil {
			sibs[sib] = map[idxKey]bool{}
			order = append(order, sib)
		}
		if n.hashed {
			sibs[sib][n.hkey] = true
			n.hashed = false
		}
	}
	isGone := func(n *Node) bool { t.work.Add(1); return gone[n] }
	for _, sib := range order {
		sib.gen++
		sib.list = slices.DeleteFunc(sib.list, isGone)
		sib.opq = slices.DeleteFunc(sib.opq, isGone)
		for k := range sibs[sib] { // each bucket compacted once
			if b := slices.DeleteFunc(sib.ht[k], isGone); len(b) > 0 {
				sib.ht[k] = b
			} else {
				delete(sib.ht, k)
			}
		}
	}
	for _, sib := range order {
		present := map[*schema.Node]bool{}
		for _, n := range sib.list {
			present[n.schema] = true
		}
		for s := range sib.rbTree {
			if !present[s] {
				sib.runGone(s)
			}
		}
		for s := range sib.unsorted {
			if !present[s] {
				sib.runGone(s)
			}
		}
	}
	var parents []*Node
	seen := map[*Node]bool{}
	for _, n := range ns {
		if gone[n] {
			delete(gone, n)
			if p := detach(n); p != nil && !seen[p] {
				seen[p] = true
				parents = append(parents, p)
			}
		}
	}
	for _, p := range parents { // once per parent, not per removed child
		npContDfltSet(p)
	}
	return nil
}

// isNPCont is lysc_is_np_cont.
func isNPCont(s *schema.Node) bool { return s != nil && s.Kind == schema.Container && !s.Presence }

// npContDfltSet is lyd_np_cont_dflt_set: an NP container whose children are all default becomes
// default, up the ancestors.
func npContDfltSet(p *Node) {
	for ; p != nil && p.flags&FlagDefault == 0 && isNPCont(p.schema); p = p.parent {
		for c := range p.kids.all() {
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
