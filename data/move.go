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
	if src := sibList(n); src != nil {
		t.moveNodes(parent, src)
		return nil
	}
	if err := unlinkTree(n); err != nil {
		return err
	}
	t.insert(parent, n, insertDefault)
	return nil
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
	if src := sibList(n); src != nil && src != dt {
		dt.moveNodes(parent, src)
		return nil
	}
	if err := unlinkTree(n); err != nil {
		return err
	}
	dt.insert(parent, n, insertDefault)
	return nil
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
	unlink(n)
	dt := t
	if sibling.parent == nil {
		dt = sibling.tree
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
	dt.link(sibling.parent, sib, n, at)
	return nil
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
		rb, uns = src.top.rbTree[s], src.top.unsorted[s]
	}
	inRB := make([]bool, len(run))
	for i, n := range run {
		inRB[i] = n.inRB
	}
	_ = src.unlinkAll(run) // top-level nodes are never keys under a parent: lyd_unlink_check passes
	for i, n := range run {
		n.inRB = inRB[i]
	}
	return rb, uns
}

// give records the tree state of a run moved whole into dst (lyds_tree metadata moves with its
// leader).
func give(dst *siblings, s *schema.Node, rb, uns bool) {
	if rb {
		markRB(dst, s)
	}
	if uns {
		markUnsorted(dst, s)
	}
}

// moveNodes is lyd_move_nodes: every top-level node of src joins the children of parent (the top
// level of t when parent is nil).
func (t *Tree) moveNodes(parent *Node, src *Tree) {
	dst := t.childrenOf(parent)
	rs := runs(slices.Collect(src.top.all()))
	if dst.len() == 0 {
		// lyd_move_nodes_at_once: the list keeps its order, its runs their RB trees
		for _, run := range rs {
			rb, uns := take(src, run)
			for _, n := range run {
				at := -1
				if n.schema != nil {
					at = len(dst.list)
				}
				t.link(parent, dst, n, at)
			}
			if run[0].schema != nil {
				give(dst, run[0].schema, rb, uns)
			}
		}
		return
	}
	// lyd_move_nodes_by_schema
	for _, run := range rs {
		s := run[0].schema
		if sortedSupported(run[0]) && t.findSchema(dst, s) != nil {
			t.merge(parent, dst, src, run)
			continue
		}
		// lyd_move_nodes_ordby_schema: before the anchor (the first sibling after the run by
		// schema, the first opaque one at the latest), the run in its order
		rb, uns := take(src, run)
		at := -1
		if s != nil {
			at = t.insertPos(dst, run[0], insertLastBySchema)
		}
		for i, n := range run {
			if at < 0 {
				t.link(parent, dst, n, -1)
			} else {
				t.link(parent, dst, n, at+i)
			}
		}
		if s != nil {
			give(dst, s, rb, uns)
		}
	}
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
func (t *Tree) merge(parent *Node, dst *siblings, src *Tree, run []*Node) {
	s := run[0].schema
	srcRB := src.top.rbTree[s]
	var members, rest []*Node
	for _, n := range run {
		if srcRB && n.inRB {
			members = append(members, n)
		} else {
			rest = append(rest, n)
		}
	}
	take(src, run)
	if len(members) > 0 && !dst.rbTree[s] {
		// lyds_merge_nodes2_front/_among/_back: a stable merge, source first on equal values
		lo := t.schemaIndex(dst, s)
		hi := lo
		for hi < len(dst.list) && dst.list[hi].schema == s {
			dst.list[hi].inRB = true
			hi++
		}
		d := slices.Clone(dst.list[lo:hi])
		at, j := lo, 0
		for _, n := range members {
			for j < len(d) && t.less(d[j], n) {
				j++
				at++
			}
			t.link(parent, dst, n, at)
			at++
		}
		markRB(dst, s)
		members = nil
	}
	for _, n := range append(members, rest...) {
		n.inRB = false
		t.insert(parent, n, insertDefault)
	}
}

// less reports whether a orders strictly before b by value, counting the comparison.
func (t *Tree) less(a, b *Node) bool {
	t.work++
	return compareSorted(a, b) < 0
}
