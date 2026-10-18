// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_insert_check_schema, lyd_insert_child,
// lyd_insert_sibling, lyd_insert_after, lyd_move_nodes, lyd_move_nodes_at_once,
// lyd_move_nodes_by_schema, lyd_move_nodes_ordby_schema) and src/tree_data_sorted.c (lyds_merge,
// lyds_merge_nodes1, lyds_merge_nodes2, lyds_merge_nodes2_front, lyds_merge_nodes2_among,
// lyds_merge_nodes2_back, lyds_merge_nodes3) (BSD-3-Clause, © CESNET).

package data

import (
	"fmt"
	"slices"

	"github.com/vibe-ports/yang/internal/schema"
)

// argErr is LOGARG: an invalid argument of the public function fn.
func argErr(arg, fn string) error {
	return &opError{"LY_EINVAL", fmt.Sprintf("Invalid argument %s (%s()).", arg, fn)}
}

// insertCheckSchema is lyd_insert_check_schema: the data node of schema node sn may be inserted
// under a data node of schema parent, or next to one of schema sibling. Opaque nodes go anywhere,
// and so does everything when neither is known (an opaque parent or sibling).
func insertCheckSchema(parent, sibling, sn *schema.Node) error {
	if sn == nil || parent == nil && sibling == nil {
		return nil
	}
	if parent == nil {
		parent = sibling.DataParent()
	}
	par2 := sn.DataParent()
	switch {
	case parent != nil && par2 != parent:
		return &opError{"LY_EINVAL", fmt.Sprintf("Cannot insert, parent of \"%s\" is not \"%s\".", sn.Name, parent.Name)}
	case parent == nil && par2 != nil:
		return &opError{"LY_EINVAL", fmt.Sprintf("Cannot insert, node \"%s\" is not top-level.", sn.Name)}
	}
	return nil
}

// isInner is LYD_NODE_INNER.
func isInner(s *schema.Node) bool {
	switch s.Kind {
	case schema.Container, schema.List, schema.RPC, schema.Action, schema.Notification:
		return true
	}
	return false
}

// first is the first sibling, opaque nodes last.
func (s *siblings) first() *Node {
	if len(s.list) > 0 {
		return s.list[0]
	}
	if len(s.opq) > 0 {
		return s.opq[0]
	}
	return nil
}

// sibList is the tree whose top level n starts when that top level has more nodes: the Go form
// of a libyang sibling list of unlinked nodes, which lyd_insert_child and lyd_insert_sibling move
// as a whole (lyd_move_nodes) when given its first node. nil: n moves alone.
func sibList(n *Node) *Tree {
	if n.parent != nil || n.tree == nil || n.tree.top.len() < 2 || n.tree.top.first() != n {
		return nil
	}
	return n.tree
}

// Insert is lyd_insert_child: n becomes a child of parent, a node of t; with parent nil it is
// lyd_insert_sibling with t's first top-level node, so n becomes a top-level node of t. n leaves
// the tree it is in; when n is the first top-level node of another tree, all that tree's
// top-level nodes move (libyang moves a whole sibling list). Refused as an LY_EINVAL
// *ValidationError: a list key, a node of another schema snapshot, a parent not in t, and what
// lyd_insert_check_schema refuses (n's schema node not a child of parent's, or not top-level).
// Oracle coverage of moves (between parents, from another tree, whole sibling lists, refusals):
// the sequence step insert, seq/insert-*; Go tests TestTreeBuild, TestInsertChild,
// TestInsertSibling and TestInsertIntoItself.
func (t *Tree) Insert(parent, n *Node) error {
	defer t.work.beginHashWork()()
	fn := "lyd_insert_child"
	if parent == nil {
		fn = "lyd_insert_sibling"
	}
	lg := &logger{set: t.set}
	switch {
	case n == nil:
		return lg.done(argErr("node", fn))
	case parent != nil && treeOf(parent) != t:
		return lg.done(argErr("parent (not in the tree)", fn))
	case !sameSet(setOf(n), t.set) || setOf(n) == nil:
		return lg.done(argErr("node (another schema snapshot)", fn)) // a Go tree has one snapshot
	}
	if source := n.treeOf(); source != nil && source != t {
		defer source.work.beginHashWork()()
	}
	if parent != nil {
		return lg.done(t.insertChild(parent, n))
	}
	return lg.done(t.insertSibling(t.top.first(), n))
}

// intoItself refuses a destination at or inside what moves (D-0112): n's subtree, or every
// top-level subtree of n's tree when n starts it (sibList). libyang links the node into its own
// subtree and every later walk of the tree never ends.
func intoItself(dest, n *Node, fn string) error {
	src := sibList(n)
	for d := dest; d != nil; d = d.parent {
		if d == n || src != nil && d.parent == nil && d.tree == src {
			return argErr("node (the destination is inside it)", fn)
		}
	}
	return nil
}

// insertChild is lyd_insert_child: n becomes a child of parent, or every top-level node of n's
// tree when n starts it (sibList).
func (t *Tree) insertChild(parent, n *Node) error {
	switch {
	case parent == nil:
		return argErr("parent", "lyd_insert_child")
	case n == nil:
		return argErr("node", "lyd_insert_child")
	case parent.schema != nil && !isInner(parent.schema):
		return argErr("!parent->schema || (parent->schema->nodetype & LYD_NODE_INNER)", "lyd_insert_child")
	}
	if err := insertCheckSchema(parent.schema, nil, n.schema); err != nil {
		return err
	}
	if err := intoItself(parent, n, "lyd_insert_child"); err != nil {
		return err
	}
	if n.schema != nil && n.schema.IsKey() {
		if err := t.work.checkTermHash(parent, n.schema, n.value); err != nil {
			return err
		}
	}
	if src := sibList(n); src != nil {
		return t.moveNodes(parent, src)
	}
	if err := t.preflightInsertAnchor(parent, n); err != nil {
		return err
	}
	restore, err := t.linkOrigin(n)
	if err != nil {
		return err
	}
	if err := preflightUnlink(n); err != nil {
		return err
	}
	if err := t.preflightMoveDestination(parent, n); err != nil {
		return err
	}
	destination := t.childrenOf(parent)
	destinationGen := destination.gen
	if n.siblingsOf() == t.childrenOf(parent) {
		if err := unlinkCheck(n); err != nil {
			return err
		}
		unlinkRetainingHistory(n)
	} else if err := unlinkTree(n); err != nil {
		return err
	}
	err = t.insert(parent, n, insertDefault)
	if err != nil {
		restore()
		destination.gen = destinationGen
	}
	return err
}

// insertSibling is lyd_insert_sibling: n, or every top-level node of n's tree when n starts it
// (sibList), joins the siblings of sibling; with sibling nil, the top level of t. A parentless
// sibling must be a top-level node of a tree (Go has no free sibling lists).
func (t *Tree) insertSibling(sibling, n *Node) error {
	switch {
	case n == nil:
		return argErr("node", "lyd_insert_sibling")
	case sibling == n:
		return argErr("sibling != node", "lyd_insert_sibling")
	}
	var parent *Node
	dt := t
	if sibling != nil {
		if err := insertCheckSchema(nil, sibling.schema, n.schema); err != nil {
			return err
		}
		if parent = sibling.parent; parent == nil {
			if dt = sibling.tree; dt == nil {
				return &opError{"LY_EINVAL", "Sibling is not linked (no parent, no tree)."}
			}
		}
	}
	if sibling != nil {
		if err := intoItself(sibling, n, "lyd_insert_sibling"); err != nil {
			return err
		}
	}
	if n.schema != nil && n.schema.IsKey() {
		if err := dt.work.checkTermHash(parent, n.schema, n.value); err != nil {
			return err
		}
	}
	if src := sibList(n); src != nil && src != dt {
		return dt.moveNodes(parent, src)
	}
	if err := dt.preflightInsertAnchor(parent, n); err != nil {
		return err
	}
	restore, err := dt.linkOrigin(n)
	if err != nil {
		return err
	}
	if err := preflightUnlink(n); err != nil {
		return err
	}
	if err := dt.preflightMoveDestination(parent, n); err != nil {
		return err
	}
	destination := dt.childrenOf(parent)
	destinationGen := destination.gen
	if n.siblingsOf() == dt.childrenOf(parent) {
		if err := unlinkCheck(n); err != nil {
			return err
		}
		unlinkRetainingHistory(n)
	} else if err := unlinkTree(n); err != nil {
		return err
	}
	err = dt.insert(parent, n, insertDefault)
	if err != nil {
		restore()
		destination.gen = destinationGen
	}
	return err
}

// insertAfter is lyd_insert_after: the user-ordered instance n goes right after sibling, an
// instance of the same schema node. Go keeps schema nodes in schema order and before opaque
// ones, so a schema node after an opaque sibling goes after the last instance of its own run
// (insertLastBySchema) and an opaque node after a schema sibling becomes the first opaque one
// (libyang links them where asked; D-0058).
func (t *Tree) insertAfter(sibling, n *Node) error {
	switch {
	case sibling == nil:
		return argErr("sibling", "lyd_insert_after")
	case n == nil:
		return argErr("node", "lyd_insert_after")
	case sibling == n:
		return argErr("sibling != node", "lyd_insert_after")
	}
	if err := insertCheckSchema(nil, sibling.schema, n.schema); err != nil {
		return err
	}
	if n.schema != nil && (n.schema.Kind != schema.List && n.schema.Kind != schema.LeafList || !n.schema.UserOrdered) {
		return &opError{"LY_EINVAL", "Can be used only for user-ordered nodes."}
	}
	if n.schema != nil && sibling.schema != nil && n.schema != sibling.schema {
		return &opError{"LY_EINVAL", "Cannot insert after a different schema node instance."}
	}
	sib := sibling.siblingsOf()
	if sib == nil {
		return &opError{"LY_EINVAL", "Sibling is not linked (no parent, no tree)."}
	}
	dt := t
	if sibling.parent == nil {
		dt = sibling.tree
	}
	if dt.work.hashErr != nil {
		return dt.work.hashErr
	}
	if err := dt.preflightInsertAnchor(sibling.parent, n); err != nil {
		return err
	}
	restore, err := dt.linkOrigin(n)
	if err != nil {
		return err
	}
	if err := preflightUnlink(n); err != nil {
		return err
	}
	if err := dt.preflightMoveDestination(sibling.parent, n); err != nil {
		return err
	}
	destinationGen := sib.gen
	if n.siblingsOf() == sib {
		unlinkRetainingHistory(n)
	} else {
		unlink(n)
	}
	at := 0
	switch {
	case n.schema != nil && sibling.schema == nil:
		at = dt.insertPos(sib, n, insertLastBySchema)
	case n.schema != nil:
		at = slices.Index(sib.list, sibling) + 1
	case sibling.schema == nil:
		at = slices.Index(sib.opq, sibling) + 1
	}
	err = dt.link(sibling.parent, sib, n, at)
	if err != nil {
		restore()
		sib.gen = destinationGen
	}
	return err
}

// linkOrigin records a linked node's position before an insertion may unlink it.
// A failed hash lookup or preflight must not strand that node detached.
func (t *Tree) linkOrigin(n *Node) (func(), error) {
	sib := n.siblingsOf()
	if sib == nil {
		return func() {}, nil
	}
	owner, parent := n.treeOf(), n.parent
	gen := sib.gen
	if owner == nil {
		owner = t // a subtree under a detached parent
	}
	inRB := n.inRB
	at := -1
	var leader *Node
	if n.schema != nil {
		var err error
		at, err = t.originIndex(sib, sib.list, n)
		if err != nil {
			return nil, err
		}
		if !sib.schemaUnordered && !opChild(n.schema) {
			// Ordinary schema runs are contiguous and monotone. Their first slot
			// is a binary lookup even when n is the last of many instances.
			before := t.work.Load()
			first := t.upper(sib.list, 0, func(a *Node) bool { return t.sameOrAfter(a, n) })
			if err := t.work.chargeHash(uint64(t.work.Load() - before)); err != nil { //nolint:gosec // lookup only increments the counter
				return nil, err
			}
			leader = sib.list[first]
		} else {
			// A copied or operation sibling set may have split runs. Limit
			// this walk to the physical run and charge each inspected slot.
			leader = n
			for i := at - 1; i >= 0; i-- {
				if err := t.work.chargeHash(1); err != nil {
					return nil, err
				}
				sib.work.Add(1)
				if sib.list[i].schema != n.schema {
					break
				}
				leader = sib.list[i]
			}
		}
	} else {
		var err error
		at, err = t.originIndex(sib, sib.opq, n)
		if err != nil {
			return nil, err
		}
	}
	rb, uns, group := sib.rbTree[leader], sib.unsorted[leader], n.rbGroup
	return func() {
		if n.siblingsOf() == nil {
			n.touch()
			n.inRB = inRB
			n.rbGroup = group
			owner.restoreLink(parent, n, at)
			sib.gen = gen
			if rb {
				markRB(sib, leader)
			} else {
				delete(sib.rbTree, leader)
			}
			if uns {
				markUnsorted(sib, leader)
			} else {
				delete(sib.unsorted, leader)
			}
		}
	}, nil
}

// originIndex locates a linked node from both ends. Each step is charged before
// touching the sibling slots, so a middle-position lookup stops at the quota.
func (t *Tree) originIndex(sib *siblings, nodes []*Node, n *Node) (int, error) {
	for i, j := 0, len(nodes)-1; i <= j; i, j = i+1, j-1 {
		if err := t.work.chargeHash(1); err != nil {
			return -1, err
		}
		sib.work.Add(1)
		if nodes[j] == n {
			return j, nil
		}
		if nodes[i] == n {
			return i, nil
		}
	}
	return -1, nil
}

// runs splits nodes into runs of consecutive instances of one schema node (opaque nodes: of
// schema nil).
func runs(nodes []*Node) [][]*Node {
	var rs [][]*Node
	for i := 0; i < len(nodes); {
		j := i + 1
		for j < len(nodes) && nodes[j].schema == nodes[i].schema {
			j++
		}
		rs = append(rs, nodes[i:j])
		i = j
	}
	return rs
}

// take unlinks the run from the top level of src like lyd_unlink_ignore_lyds: the nodes keep
// their RB-tree membership, and the run's tree state is returned to be given to its new place.
func take(src *Tree, run []*Node) (rb, uns bool) {
	if s := run[0].schema; s != nil {
		rb, uns = src.top.rbTree[run[0]], src.top.unsorted[run[0]]
	}
	inRB := make([]bool, len(run))
	groups := make([]*rbGroup, len(run))
	for i, n := range run {
		inRB[i] = n.inRB
		groups[i] = n.rbGroup
	}
	_ = src.unlinkAll(run) // top-level nodes are never keys under a parent: lyd_unlink_check passes
	for i, n := range run {
		n.touch()
		n.inRB = inRB[i]
		n.rbGroup = groups[i]
	}
	return rb, uns
}

// give records the tree state of a run moved whole into dst (lyds_tree metadata moves with its
// leader).
func give(dst *siblings, leader *Node, rb, uns bool) {
	if rb {
		markRB(dst, leader)
	}
	if uns {
		markUnsorted(dst, leader)
	}
}

// moveNodes is lyd_move_nodes: every top-level node of src joins the children of parent (the top
// level of t when parent is nil).
func (t *Tree) moveNodes(parent *Node, src *Tree) error {
	dst := t.childrenOf(parent)
	dstGen, srcGen := dst.gen, src.top.gen
	original := slices.Collect(src.top.all())
	if err := t.preflightSplice(parent, dst, original); err != nil {
		return err
	}
	for _, n := range original {
		if err := t.preflightInsertAnchor(parent, n); err != nil {
			return err
		}
	}
	rs := runs(original)
	for _, run := range rs {
		if err := t.preflightInsertPosition(dst, run[0], insertDefault); err != nil {
			return err
		}
	}
	inRB := make([]bool, len(original))
	groups := make([]*rbGroup, len(original))
	type runState struct {
		rb, uns bool
	}
	states := make(map[*Node]runState, len(rs))
	for i, n := range original {
		t.work.Add(1)
		inRB[i] = n.inRB
		groups[i] = n.rbGroup
		if n.schema != nil && (i == 0 || original[i-1].schema != n.schema) {
			states[n] = runState{src.top.rbTree[n], src.top.unsorted[n]}
		}
	}
	defer func() {
		if t.work.hashErr == nil {
			return
		}
		// A later run may fail its hash-backed anchor lookup. Put every
		// original top-level node back, including runs already transferred.
		for _, n := range original {
			unlink(n)
		}
		for i, n := range original {
			n.touch()
			n.inRB = inRB[i]
			n.rbGroup = groups[i]
			at := -1
			if n.schema != nil {
				at = len(src.top.list)
			}
			src.restoreLink(nil, n, at)
		}
		for leader, state := range states {
			if state.rb {
				markRB(&src.top, leader)
			} else {
				delete(src.top.rbTree, leader)
			}
			if state.uns {
				markUnsorted(&src.top, leader)
			} else {
				delete(src.top.unsorted, leader)
			}
		}
		dst.gen, src.top.gen = dstGen, srcGen
	}()
	if dst.len() == 0 {
		// lyd_move_nodes_at_once: the list keeps its order, its runs their RB trees
		for _, run := range rs {
			rb, uns := take(src, run)
			for _, n := range run {
				at := -1
				if n.schema != nil {
					at = len(dst.list)
				}
				if err := t.link(parent, dst, n, at); err != nil {
					return err
				}
			}
			if run[0].schema != nil {
				give(dst, run[0], rb, uns)
			}
		}
		return nil
	}
	// lyd_move_nodes_by_schema
	for _, run := range rs {
		s := run[0].schema
		if sortedSupported(run[0]) && t.schemaLookup(dst, s) != nil {
			if err := t.merge(parent, dst, src, run); err != nil {
				return err
			}
			continue
		}
		if t.work.hashErr != nil {
			return t.work.hashErr
		}
		// lyd_move_nodes_ordby_schema: before the anchor (the first sibling after the run by
		// schema, the first opaque one at the latest), the run in its order
		at := -1
		if s != nil {
			at = t.insertPos(dst, run[0], insertLastBySchema)
			if t.work.hashErr != nil {
				return t.work.hashErr
			}
		}
		rb, uns := take(src, run)
		for i, n := range run {
			if at < 0 {
				if err := t.link(parent, dst, n, -1); err != nil {
					return err
				}
			} else {
				if err := t.link(parent, dst, n, at+i); err != nil {
					return err
				}
			}
		}
		if s != nil {
			give(dst, run[0], rb, uns)
		}
	}
	return nil
}

// merge is lyds_merge: the run of a system-ordered list or leaf-list from src joins its
// instances in dst.
//   - the source run has no RB tree (lyds_merge_nodes1, after lyds_additionally_create_rb_tree
//     when neither has one): its instances are inserted one by one in data order, each after the
//     equal values;
//   - only the source run has one (lyds_merge_nodes2): the destination instances join the source
//     tree, so equal values keep the source instances first. libyang 5.8.6 crashes on most such
//     inputs (D-0062); the port does the stable merge the C is written to do. A destination run
//     without a tree is assumed sorted, as the C assumes: one with several unsorted instances
//     (appended by schema) is merged as if sorted and then counts as sorted;
//   - both have one (lyds_merge_nodes3): the source tree members are inserted one by one. libyang
//     visits them in rb_iter order, which decides the order of equal values only; the port has no
//     tree shape and uses data order (D-0061).
//
// Source instances outside the source tree come last, one by one (lyds_merge_nodes1 from next_p).
func (t *Tree) merge(parent *Node, dst *siblings, src *Tree, run []*Node) error {
	if err := t.preflightSplice(parent, dst, run); err != nil {
		return err
	}
	s := run[0].schema
	srcRB := src.top.rbTree[run[0]]
	selected := src.top.rbGroups[run[0]]
	var members, rest []*Node
	for _, n := range run {
		if srcRB && n.inRB && n.rbGroup == selected {
			members = append(members, n)
		} else {
			rest = append(rest, n)
		}
	}
	take(src, run)
	if len(members) > 0 && !dst.hasRB(s) {
		// lyds_merge_nodes2_front/_among/_back: a stable merge, source first on equal values
		// Splice needs positions; its sibling copy already bounds this scan.
		group := members[0].rbGroup
		for _, n := range members {
			n.touch()
			n.rbGroup = group
		}
		lo := t.schemaIndex(dst, s)
		hi := lo
		for hi < len(dst.list) && dst.list[hi].schema == s {
			dst.list[hi].touch()
			dst.list[hi].inRB = true
			dst.list[hi].rbGroup = group
			hi++
		}
		if err := t.splice(parent, dst, lo, hi, members, true); err != nil {
			return err
		}
		markRB(dst, dst.runLeader(s))
		members = nil
	}
	monotone := true
	for i := 1; i < len(members); i++ {
		if t.less(members[i], members[i-1]) {
			monotone = false
			break
		}
	}
	if len(members) > 1 && monotone && !dst.hasUnsorted(s) {
		// lyds_merge_nodes3 into a sorted run: one pass, each after the equal values
		// Splice needs positions; its sibling copy already bounds this scan.
		group := dst.rbGroups[dst.runLeader(s)]
		for _, n := range members {
			n.touch()
			n.rbGroup = group
		}
		lo := t.schemaIndex(dst, s)
		hi := lo
		for hi < len(dst.list) && dst.list[hi].schema == s {
			hi++
		}
		if err := t.splice(parent, dst, lo, hi, members, false); err != nil {
			return err
		}
		members = nil
	}
	for _, n := range append(members, rest...) {
		n.touch()
		n.inRB = false
		if err := t.insert(parent, n, insertDefault); err != nil {
			return err
		}
	}
	return nil
}

// less reports whether a orders strictly before b by value, counting the comparison.
func (t *Tree) less(a, b *Node) bool {
	t.work.Add(1)
	return t.compareSorted(a, b) < 0
}
