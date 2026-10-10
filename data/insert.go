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
		lo, hi := t.runBounds(sib, n)
		if lo == hi {
			return hi // no instance yet: by schema, no RB tree for a single instance
		}
		n.inRB = true
		if !sib.rbTree[n.schema] {
			// lyds_additionally_create_rb_tree: the run gets its RB tree from all its instances,
			// inserted in order, so it is sorted stably (rb_insert_node puts equal values after
			// the existing ones)
			slices.SortStableFunc(l[lo:hi], compareSorted)
			if sib.opIdx != nil {
				sib.opBuild(t) // reordered
			}
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
	switch {
	case opChild(n.schema):
		at = t.opAnchor(sib, n)
	case t.after(l[at-1], n):
		at = t.upper(l, 0, afterN)
	}
	if order != insertDefault && sortedSupported(n) && sib.rbTree[n.schema] {
		markUnsorted(sib, n.schema) // n is not in the run's RB tree
	}
	return at
}

// runBounds is the run of n's schema node in sib.list, or the empty run where its first instance
// goes.
func (t *Tree) runBounds(sib *siblings, n *Node) (lo, hi int) {
	l := sib.list
	if opChild(n.schema) {
		// a system-ordered run under an operation is input data: its instances are contiguous
		inst := sib.opInst(n.schema)
		if len(inst) == 0 {
			at := t.opAnchor(sib, n)
			return at, at
		}
		hi = t.indexFromEnd(l, inst[len(inst)-1]) + 1
		return hi - len(inst), hi
	}
	lo = t.upper(l, 0, func(a *Node) bool { return t.sameOrAfter(a, n) })
	return lo, t.upper(l, lo, func(a *Node) bool { return t.after(a, n) })
}

// opChild reports whether s is a child of an rpc or action's input or output. Such siblings are
// ordered by lyd_insert_get_next_anchor, which walks only the new node's own input or output
// schema list: input and output instances interleave, and the instances of one output node need
// not be contiguous (they are ordered by user, each placed by the anchor). They are found through
// the operation's instance index (siblings.opInst), as libyang finds them by hash.
func opChild(s *schema.Node) bool {
	if s == nil {
		return false
	}
	dp := s.DataParent()
	return dp != nil && (dp.Kind == schema.RPC || dp.Kind == schema.Action)
}

// indexFromEnd is the index of n in l, scanned from both ends at once, each step counted: the
// cost is the distance to the nearer end, which the slice insertion at that index moves anyway
// (insertAt shifts the shorter side), so it adds no order of growth.
func (t *Tree) indexFromEnd(l []*Node, n *Node) int {
	for i, j := 0, len(l)-1; i <= j; i, j = i+1, j-1 {
		t.work.Add(1)
		if l[j] == n {
			return j
		}
		if l[i] == n {
			return i
		}
	}
	return -1
}

// orderKey is an operation's input (out false) or output children.
type orderKey struct {
	op  *schema.Node
	out bool
}

// opOrder is lys_getnext over an operation's input or output, cached per tree.
func (t *Tree) opOrder(op *schema.Node, out bool) []*schema.Node {
	t.rankMu.Lock()
	defer t.rankMu.Unlock()
	k := orderKey{op, out}
	if o, ok := t.orders[k]; ok {
		return o
	}
	var opts schema.GetNextOpt
	if out {
		opts = schema.GetNextOutput
	}
	o := slices.Collect(schema.GetNext(op, nil, opts))
	if t.orders == nil {
		t.orders = map[orderKey][]*schema.Node{}
	}
	t.orders[k] = o
	return o
}

// opInst is the instances of the operation child sn in s.list, in list order. It only reads: the
// index is kept by the mutations alone (link, unlink, the RB sort), so concurrent readers of an
// unchanged tree share it safely.
func (s *siblings) opInst(sn *schema.Node) []*Node { return s.opIdx[sn] }

// opBuild (re)builds the instance index of an operation's children from s.list; mutations only.
func (s *siblings) opBuild(t *Tree) {
	s.opIdx = map[*schema.Node][]*Node{}
	s.opTop = [2]int{}
	for _, a := range s.list {
		t.work.Add(1)
		s.opIdx[a.schema] = append(s.opIdx[a.schema], a)
		s.opRaise(t, a.schema)
	}
}

// opRemove drops n, being unlinked, from the instance index.
func (s *siblings) opRemove(n *Node) {
	if s.opIdx == nil {
		return
	}
	if inst := slices.DeleteFunc(s.opIdx[n.schema], func(a *Node) bool { return a == n }); len(inst) > 0 {
		s.opIdx[n.schema] = inst
	} else {
		delete(s.opIdx, n.schema)
	}
}

// opRaise records that sn has instances: opTop is, per input (0) and output (1), one more than
// the greatest schema rank with instances, so that the anchor search stops there.
func (s *siblings) opRaise(t *Tree, sn *schema.Node) {
	d := 0
	if sn.InOutput() {
		d = 1
	}
	s.opTop[d] = max(s.opTop[d], t.schemaRank(sn)+1)
}

// opIdxAdd records n, just placed at index at of s.list, in the operation's instance index:
// appended when it follows the last instance of its schema node, at its place in a contiguous
// system-ordered run, else by a rescan of the list (an instance placed before another one of an
// output node, which the anchor does not do).
func (s *siblings) opIdxAdd(t *Tree, n *Node, at int) {
	inst := s.opIdx[n.schema]
	switch {
	case len(inst) == 0:
		s.opIdx[n.schema] = []*Node{n}
		s.opRaise(t, n.schema)
	case t.indexFromEnd(s.list, inst[len(inst)-1]) < at:
		s.opIdx[n.schema] = append(inst, n)
	case sortedSupported(n):
		s.opIdx[n.schema] = slices.Insert(inst, at-t.indexFromEnd(s.list, inst[0]), n)
	default:
		var all []*Node
		for _, a := range s.list {
			t.work.Add(1)
			if a.schema == n.schema {
				all = append(all, a)
			}
		}
		s.opIdx[n.schema] = all
	}
}

// opAnchor is lyd_insert_get_next_anchor for an operation's child n: the index of the sibling n
// goes before, len(sib.list) to append (before the opaque nodes).
//   - with the parent's children hash table: the first instance of the closest following schema
//     sibling in n's own input or output (lyd_find_sibling_schema: the instance index);
//   - without it (fewer than htMinItems children): the siblings walked against n's input or output
//     schema list, the anchor being the first sibling past n's schema node there; a sibling of the
//     other direction is never in that list, so an output node goes before the first input node
//     that does not precede it in the walk, and an input node before such an output node.
func (t *Tree) opAnchor(sib *siblings, n *Node) int {
	l := sib.list
	var opts schema.GetNextOpt
	if n.schema.InOutput() {
		opts = schema.GetNextOutput
	}
	order := t.opOrder(n.schema.DataParent(), opts == schema.GetNextOutput)
	if sib.ht != nil {
		// the closest following schema sibling with instances; none past the greatest rank with
		// instances (opTop), so input in schema order appends at once and input in reverse
		// order finds the next rank at once
		d := 0
		if opts == schema.GetNextOutput {
			d = 1
		}
		for r := t.schemaRank(n.schema) + 1; r < sib.opTop[d]; r++ {
			t.work.Add(1)
			if inst := sib.opIdx[order[r]]; len(inst) > 0 {
				return t.indexFromEnd(l, inst[0])
			}
		}
		return len(l)
	}
	own := ownerModule(t.set, n)
	k, found := 0, false
	for m, a := range l {
		t.work.Add(1)
		if ownerModule(t.set, a) != own {
			return m // lyd_owner_module differs: the data of the next module
		}
		for !found {
			if k >= len(order) {
				return len(l) // extension instance data: no anchor
			}
			if order[k] == n.schema {
				found = true
				break
			}
			if a.schema == order[k] {
				break
			}
			k++
		}
		if found && a.schema != n.schema {
			return m
		}
	}
	return len(l)
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
	if n.parent != nil || n.tree != nil {
		panic("data: inserting a linked node") // internal invariant: callers unlink first
	}
	sib.work = &t.work
	switch {
	case n.schema == nil && (at < 0 || at >= len(sib.opq)):
		sib.opq = append(sib.opq, n)
	case n.schema == nil:
		sib.opq = slices.Insert(sib.opq, at, n)
	case at == len(sib.list):
		sib.list = append(sib.list, n)
	default:
		// ponytail: insertion in the middle moves the tail (O(n) per insert, like design 02's
		// slice); the parsers append and sort runs lazily, a tree structure if API-built
		// reversed inputs ever matter.
		sib.list = slices.Insert(sib.list, at, n)
	}
	switch {
	case !opChild(n.schema):
	case sib.opIdx == nil:
		sib.opBuild(t) // the operation's first child (or the first since a bulk removal)
	default:
		sib.opIdxAdd(t, n, at)
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
		sib.opRemove(n)
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
		if sib.opIdx != nil {
			sib.opBuild(t)
		}
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
