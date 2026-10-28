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
	if s.SchemaLen() > 0 {
		return s.At(0)
	}
	if s.OpaqueLen() > 0 {
		return s.opaqueAt(0)
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

// Insert is lyd_insert_child: n becomes a child of parent, a node of t or a detached node of its
// snapshot; with parent nil it is
// lyd_insert_sibling with t's first top-level node, so n becomes a top-level node of t. n leaves
// the tree it is in (or may already be detached); when n is the first top-level node of another
// tree, all that tree's top-level nodes move (libyang moves a whole sibling list). Refused as an
// LY_EINVAL *ValidationError: a list key, a parent not in t, and what
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
	set := t.context()
	if parent != nil {
		set = ctxOf(parent)
	}
	lg := &logger{set: set}
	switch {
	case n == nil:
		return lg.done(argErr("node", fn))
	case parent != nil && treeOf(parent) != nil && treeOf(parent) != t:
		return lg.done(argErr("parent (not in the tree)", fn))
	case ctxOf(n) == nil:
		return lg.done(argErr("node (without a schema snapshot)", fn))
	}
	if source := n.treeOf(); source != nil && source != t {
		defer source.work.beginHashWork()()
	} else if s := n.siblingsOf(); source == nil && s != nil && s.work != nil && s.work != &t.work {
		// a detached parent's counter is the origin's: this operation is its scope too, so a
		// refusal there does not stay sticky for the next one
		defer s.work.beginHashWork()()
	}
	// A destination switching lookup mode is journaled from its preflights on (switchJournal).
	journal, err := t.switchJournal(parent, n)
	if err == nil {
		err = joinLinkJournal(t, n)
	}
	if err == nil && parent != nil {
		err = t.insertChild(parent, n)
	} else if err == nil {
		err = t.insertSibling(t.top.first(), n)
	}
	journal.end(&err)
	return lg.done(err)
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
	restore, arm, err := t.linkOrigin(n)
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
	if err := unlinkCheck(n); err != nil {
		return err
	}
	disarm, err := arm()
	if err != nil {
		return err
	}
	defer disarm()
	unlinkSource := unlink
	if n.siblingsOf() == t.childrenOf(parent) {
		unlinkSource = unlinkRetainingHistory
	}
	if err := unlinkSource(n); err != nil {
		if n.parent == nil && n.tree == nil {
			restore() // a sticky error after the removal (unlink's contract): no journal here
		}
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
	restore, arm, err := dt.linkOrigin(n)
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
	if err := unlinkCheck(n); err != nil {
		return err
	}
	disarm, err := arm()
	if err != nil {
		return err
	}
	defer disarm()
	unlinkSource := unlink
	if n.siblingsOf() == dt.childrenOf(parent) {
		unlinkSource = unlinkRetainingHistory
	}
	if err := unlinkSource(n); err != nil {
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
	restore, arm, err := dt.linkOrigin(n)
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
	disarm, err := arm()
	if err != nil {
		return err
	}
	defer disarm()
	unlinkSource := unlink
	if n.siblingsOf() == sib {
		unlinkSource = unlinkRetainingHistory
	}
	if err := unlinkSource(n); err != nil {
		return err
	}
	at := 0
	switch {
	case n.schema != nil && sibling.schema == nil:
		at = dt.insertPos(sib, n, insertLastBySchema)
	case n.schema != nil:
		at = sib.schemaSpan().Index(sibling) + 1
	case sibling.schema == nil:
		at = sib.opaqueSpan().Index(sibling) + 1
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
//
// arm, called right before the unlink, returns disarm: the caller defers it so no removal undo
// outlives the operation (a refused capture before the removal, or a transfer that never calls
// dirtyOrder.remove, would otherwise leave it armed and retaining the destination).
func (t *Tree) linkOrigin(n *Node) (restore func(), arm func() (disarm func(), err error), err error) {
	if err := joinLinkJournal(t, n); err != nil {
		return nil, nil, err
	}
	sib := n.siblingsOf()
	if sib == nil {
		return func() {}, func() (func(), error) { return func() {}, nil }, nil
	}
	owner, parent := n.treeOf(), n.parent
	gen := sib.gen
	detachedOrigin := owner == nil
	originWork, subtreeWork := sib.work, childStorage(n).work
	var originRoot *Node
	if owner == nil {
		// recovery rebinds the origin tree: its ancestor walk is charged here, before any change
		if originRoot, _, err = detachedRoot(parent, &t.work); err != nil {
			return nil, nil, err
		}
		owner = t // a subtree under a detached parent
	}
	inRB := n.inRB
	at := -1
	var leader *Node
	if n.schema != nil {
		var err error
		at, err = t.originIndex(sib, sib.schemaSpan(), n)
		if err != nil {
			return nil, nil, err
		}
		if !sib.schemaUnordered && !opChild(n.schema) {
			// Ordinary schema runs are contiguous and monotone. Their first slot
			// is a binary lookup even when n is the last of many instances.
			before := t.work.Load()
			first := t.upper(sib.schemaSpan(), 0, func(a *Node) bool { return t.sameOrAfter(a, n) })
			if err := t.work.chargeHash(uint64(t.work.Load() - before)); err != nil { //nolint:gosec // lookup only increments the counter
				return nil, nil, err
			}
			leader = sib.At(first)
		} else {
			// A copied or operation sibling set may have split runs. Limit
			// this walk to the physical run and charge each inspected slot.
			leader = n
			for i := at - 1; i >= 0; i-- {
				if err := t.work.chargeHash(1); err != nil {
					return nil, nil, err
				}
				sib.work.Add(1)
				if sib.At(i).schema != n.schema {
					break
				}
				leader = sib.At(i)
			}
		}
	} else {
		var err error
		at, err = t.originIndex(sib, sib.opaqueSpan(), n)
		if err != nil {
			return nil, nil, err
		}
	}
	rb, uns, group := sib.rbTree[leader], sib.unsorted[leader], n.rbGroup
	// lyds_unlink removes n from its tree, so a failed insertion must put back that exact
	// topology. arm, called right before the unlink once every preflight passed, reserves the
	// O(log R) journal of that removal and arms it; repeated moves stay linear.
	undo := func() {}
	metaUndo := func() {}
	recordUndo := func() {}
	linksUndo := func() {}
	var recovery uint64
	disarm := func() {} // a refusal after arming drops the armed removal undos, restoring nothing
	armOrigin := func() error {
		plan, err := prepareUnlink(n)
		if err != nil {
			return err
		}
		var next *Node
		if plan != nil && plan.leader == n {
			next = plan.next
		}
		var recordRecovery uint64
		var recordNext *Node
		if plan != nil {
			recordNext = plan.next
		}
		recordUndo, recordRecovery, err = linkRecordOrigin(sib, n, recordNext, &t.work)
		if err != nil {
			return err
		}
		metaUndo, err = rbMetadataOrigin(sib, n, next, &t.work)
		if err != nil {
			return err
		}
		if linksUndo, err = linkOriginLinks(n, &t.work); err != nil {
			return err
		}
		cost, err := owner.linkBound(parent, sib, n, at)
		if err != nil {
			return err
		}
		binding, err := subtreeBindingBound(n, &owner.work, true)
		if err != nil {
			return err
		}
		cost = addHashBound(cost, binding)
		recovery = addHashBound(addHashBound(4*cost+64, 2*linkSlots(sib, n, at)), sib.transferCost(next, n))
		recovery = addHashBound(recovery, recordRecovery)
		if err := t.work.chargeHash(recovery); err != nil {
			return err
		}
		current := sib.rbOrders[group]
		if !inRB || group == nil {
			return nil
		}
		// Reserve every cached removal undo before allocating. Public native-selection preflight
		// refuses unsafe dual detachment; the arm is also used by internal recovery probes.
		if err := t.work.chargeHash(2 * uint64(len(sib.rbDual[n]))); err != nil {
			return err
		}
		dual := slices.Clone(sib.rbDual[n])
		var orders []*dirtyOrder
		bound := uint64(0)
		if current != nil {
			bound = current.removalBound()
		}
		for _, g := range dual {
			if d := sib.rbOrders[g]; d != nil && d.nodes[n] != nil {
				orders = append(orders, d)
				bound = addHashBound(bound, d.removalBound())
			}
		}
		if current == nil && len(dual) == 0 {
			return nil
		}
		if err := t.work.chargeHash(bound); err != nil {
			return err
		}
		var restores []func()
		armed := orders
		if current != nil {
			armed = append([]*dirtyOrder{current}, orders...)
		}
		mine := make([]*removalUndo, len(armed))
		for i, d := range armed {
			restores = append(restores, d.armRemovalUndo(n, &t.work))
			mine[i] = d.undo
		}
		disarm = func() {
			for i, d := range armed {
				if d.undo == mine[i] { // its own arm, not consumed by the removal (which clears it)
					d.undo = nil
				}
			}
		}
		undo = func() {
			if err := sib.touch(); err != nil {
				return
			}
			for _, r := range restores {
				r()
			}
			if sib.rbOrders == nil {
				sib.rbOrders = make(map[*rbGroup]*dirtyOrder)
			}
			if current != nil {
				current.work = sib.work
				sib.rbOrders[group] = current
			}
			for _, d := range orders {
				d.work = sib.work
			}
			if len(dual) != 0 {
				if sib.rbDual == nil {
					sib.rbDual, sib.rbDualCount = map[*Node][]*rbGroup{}, map[*rbGroup]int{}
				}
				if sib.rbDualCount == nil {
					sib.rbDualCount = map[*rbGroup]int{}
				}
				sib.rbDual[n] = dual
				for _, g := range dual {
					sib.rbDualCount[g]++
				}
			}
		}
		return nil
	}
	arm = func() (func(), error) {
		if err := armOrigin(); err != nil {
			return nil, err
		}
		// The recovery reservation spends the same quota: recheck the removal's parent-list
		// reindex (preflightUnlink, its plan cached) so unlink cannot fail after the removal.
		if err := preflightUnlink(n); err != nil {
			disarm() // nothing was removed: no undo stays armed (or keeps the destination alive)
			return nil, err
		}
		return disarm, nil
	}
	restore = func() {
		if n.siblingsOf() == nil {
			if err := n.touch(); err != nil {
				return
			}
			n.inRB = inRB
			n.rbGroup = group
			owner.restoreLink(parent, n, at, recovery)
			if detachedOrigin && t.work.journal == nil {
				// restoreLink bound the detached origin tree to t (bindDestination); give it
				// its own counters back
				unbindSubtreeWork(n, &t.work, subtreeWork)
				unbindSubtreeWork(originRoot, &t.work, originWork)
			}
			sib.gen = gen
			undo()
			metaUndo()
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
			recordUndo()
			linksUndo() // unlink's freeLinks: both endpoints of every reciprocal link
		}
	}
	return restore, arm, nil
}

// originIndex locates a linked node from both ends. Each step is charged before
// touching the sibling slots, so a middle-position lookup stops at the quota.
func (t *Tree) originIndex(sib *siblings, nodes siblingSpan, n *Node) (int, error) {
	for i, j := 0, nodes.Len()-1; i <= j; i, j = i+1, j-1 {
		if err := t.work.chargeHash(1); err != nil {
			return -1, err
		}
		sib.work.Add(1)
		if nodes.At(j) == n {
			return j, nil
		}
		if nodes.At(i) == n {
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

// runSlots bounds the sibling-slice work of linking run one by one from position at (-1:
// appended): each link shifts the shorter side, plus every reallocation copy. Charged before the
// run is linked.
func runSlots(dst *siblings, run []*Node, at int) uint64 {
	size, m := uint64(dst.len()), uint64(len(run)) //nolint:gosec // lengths are nonnegative
	shift := uint64(0)
	if at >= 0 && at < dst.SchemaLen() {
		shift = uint64(min(at+len(run), dst.SchemaLen()-at)) //nolint:gosec // positions are nonnegative
	}
	// a front insertion without headroom copies into 3*len+1 slots (nodeDeque.insertAt); the
	// headroom it leaves makes later reallocations geometric, so all copies stay within 6*(len+m)
	return addHashBound(mulHashBound(m, shift+1), 6*(size+m)+2)
}

// treeRuns joins each run to the following runs whose instances belong to its leader's RB tree.
// One tree can hold equivalent instances from several contexts (distinct schema nodes); they are
// contiguous, and lyds_merge_nodes3's traversal or a whole-run move carries them all at once
// (tree_data_sorted.c:1673-1680). The runs are subslices of one slice, so joining extends them.
func treeRuns(top *siblings, rs [][]*Node) [][]*Node {
	var out [][]*Node
	for _, run := range rs {
		if k := len(out); k > 0 {
			lead := out[k-1][0]
			if g := top.rbGroups[lead]; top.rbTree[lead] && g != nil && run[0].inRB && run[0].rbGroup == g {
				out[k-1] = out[k-1][:len(out[k-1])+len(run)]
				continue
			}
		}
		out = append(out, run)
	}
	return out
}

// takeTree detaches the retained tree of run from src before the run is unlinked, like
// lyd_unlink_ignore_lyds: the nodes keep their RB-tree membership, and the tree state is
// returned to be given to the run's new place.
func takeTree(src *Tree, run []*Node) movedRunState {
	var st movedRunState
	if run[0].schema == nil {
		return st
	}
	st.rb, st.uns = src.top.rbTree[run[0]], src.top.unsorted[run[0]]
	if !st.rb {
		return st
	}
	st.group = src.top.rbGroups[run[0]]
	st.order = src.top.rbOrders[st.group]
	if err := src.top.touch(); err != nil {
		return st
	}
	delete(src.top.rbOrders, st.group)

	return st
}

type movedRunState struct {
	rb, uns bool
	group   *rbGroup
	order   *dirtyOrder // the retained topology that leaves with the run
}

// give records the tree state of a run moved whole into dst (lyds_tree metadata moves with its
// leader).
func (t *Tree) give(dst *siblings, leader *Node, st movedRunState) error {
	if st.rb && st.order != nil {
		if err := leader.touch(); err != nil {
			return err
		}
		if err := t.work.chargeHash(2); err != nil {
			return err
		}
		leader.carriedRB = []rbCarry{{st.group, st.order}}
		if err := t.adoptCarried(dst, leader); err != nil {
			return err
		}
	}
	if st.uns {
		markUnsorted(dst, leader)
	}
	return t.work.hashErr
}

// moveNodes is lyd_move_nodes: every top-level node of src joins the children of parent (the top
// level of t when parent is nil).
func (t *Tree) moveNodes(parent *Node, src *Tree) (result error) {
	if t.work.hashErr != nil {
		return t.work.hashErr
	}
	journal := beginHashTransaction(t)
	defer journal.end(&result)
	j := t.work.journal
	defer src.work.beginHashWork()()
	if src.work.journal == nil {
		if err := j.charge(1); err != nil {
			return err
		}
		src.work.journal = j
		j.lent = append(j.lent, &src.work)
	}
	dst := t.childrenOf(parent)
	oldForestMove := t.work.forestMove
	t.work.forestMove = true
	defer func() { t.work.forestMove = oldForestMove }()
	if err := j.captureSiblings(&src.top); err != nil {
		return err
	}
	if err := j.captureSiblings(dst); err != nil {
		return err
	}
	if err := j.captureDetachedParent(parent); err != nil {
		return err
	}
	// A refused whole-run move restores both forests, including hash registrations and work
	// ownership. One journal replaces the topology-only snapshots and recovery relinking.
	defer func() {
		if result != nil {
			j.restore()
		}
	}()
	if err := t.work.chargeHash(8*(uint64(src.top.SchemaLen())+uint64(src.top.OpaqueLen())) + 1); err != nil { //nolint:gosec // sibling counts are nonnegative
		return err
	}
	original := slices.Collect(src.top.all())
	memberships, err := captureForestMemberships(&src.top, original, &t.work)
	if err != nil {
		return err
	}
	if err := t.preflightSplice(parent, dst, original); err != nil {
		return err
	}
	for _, n := range original {
		if err := t.preflightInsertAnchor(parent, n); err != nil {
			return err
		}
	}
	rs := treeRuns(&src.top, runs(original))
	for _, run := range rs {
		g := src.top.rbGroups[run[0]]
		if src.top.rbDualCount[g] == 0 {
			continue
		}
		d := src.top.rbOrders[g]
		if err := t.work.chargeHash(uint64(len(run)) + uint64(len(d.nodes)) + 1); err != nil {
			return err
		}
		members := make(map[*Node]bool, len(run))
		for _, n := range run {
			members[n] = true
		}
		for n := range d.nodes {
			if !members[n] {
				return rbUnlinkError(run[0])
			}
		}
	}
	for _, run := range rs {
		if err := t.preflightInsertPosition(dst, run[0], insertDefault); err != nil {
			return err
		}
	}
	inRB := make([]bool, len(original))
	groups := make([]*rbGroup, len(original))
	for i, n := range original {
		inRB[i], groups[i] = n.inRB, n.rbGroup
	}
	// Removing a top-level operation run rebuilds its ordered index and can exhaust the
	// source tree's hash-work budget. Unlink the complete source batch before publishing any
	// destination change, so a refusal leaves both trees untouched and no later link sees a
	// still-linked node. Each run's retained tree leaves with it.
	taken := make(map[*Node]movedRunState, len(rs))
	for _, run := range rs {
		taken[run[0]] = takeTree(src, run)
	}
	// lyd_unlink_ignore_lyds: every selected run tree is already taken. Other metadata
	// travels on its original owner, including a secondary tree below a native positional
	// operation restore. No live topology is rebalanced or shared with the emptied source.
	for _, n := range original {
		cost := 4*uint64(len(src.top.rbMore[n])) + 4
		if err := t.work.chargeHash(cost); err != nil {
			return err
		}
		if err := n.touch(); err != nil {
			return err
		}
		for _, g := range src.top.metaGroups(n) {
			if d := src.top.rbOrders[g]; d != nil {
				n.carriedRB = append(n.carriedRB, rbCarry{g, d})
			}
		}
	}
	if err := src.top.touch(); err != nil {
		return err
	}
	src.top.rbOrders, src.top.rbDual, src.top.rbDualCount, src.top.rbMore = nil, nil, nil, nil
	if err := src.unlinkAll(original); err != nil {
		for _, st := range taken {
			if st.order != nil {
				if src.top.rbOrders == nil {
					src.top.rbOrders = make(map[*rbGroup]*dirtyOrder)
				}
				src.top.rbOrders[st.group] = st.order
			}
		}
		return err
	}
	for i, n := range original {
		if err := n.touch(); err != nil {
			return err
		}
		n.inRB = inRB[i]
		n.rbGroup = groups[i]
	}
	if dst.len() == 0 {
		// lyd_move_nodes_at_once: the list keeps its order, its runs their RB trees
		for _, run := range rs {
			if err := t.work.chargeHash(runSlots(dst, run, -1)); err != nil {
				return err
			}
			for _, n := range run {
				at := -1
				if n.schema != nil {
					at = dst.SchemaLen()
				}
				if err := t.link(parent, dst, n, at); err != nil {
					return err
				}
			}
			if run[0].schema != nil {
				st := taken[run[0]]
				if err := t.give(dst, run[0], st); err != nil {
					return err
				}
			}
		}
		return finishForestMemberships(dst, memberships, &t.work)
	}
	// lyd_move_nodes_by_schema
	for _, run := range rs {
		s := run[0].schema
		if sortedSupported(run[0]) && t.schemaLookup(dst, s) != nil {
			if err := t.merge(parent, dst, run, taken[run[0]]); err != nil {
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
		if err := t.work.chargeHash(runSlots(dst, run, at)); err != nil {
			return err
		}
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
			st := taken[run[0]]
			if err := t.give(dst, run[0], st); err != nil {
				return err
			}
		}
	}
	return finishForestMemberships(dst, memberships, &t.work)
}

// merge is lyds_merge (tree_data_sorted.c:1692-1728): the run of a system-ordered list or
// leaf-list from src joins its instances in dst.
//   - the source run has no RB tree (lyds_merge_nodes1, after lyds_additionally_create_rb_tree
//     when neither has one): its instances are inserted one by one in data order, each after the
//     equal values;
//   - only the source run has one (lyds_merge_nodes2): the destination instances join the source
//     tree in data order, and that tree becomes the destination's. libyang 5.8.6 crashes on most
//     such inputs (D-0062); the port keeps the source tree and inserts the destination members
//     into it, as the C is written to do, so the run is that tree's in-order (equal values keep
//     the source instances first). The moved instances are hashed in _front/_among/_back order:
//     the destination leader's tree predecessors backwards, then the rest forwards;
//   - both have one (lyds_merge_nodes3): the source tree is released and its members are
//     inserted into the destination tree in rb_iter order (rb_iter_traversal, a post-order),
//     which decides the order of equal values.
//
// Source instances outside the source tree come last, one by one (lyds_merge_nodes1 from next_p).
func (t *Tree) merge(parent *Node, dst *siblings, run []*Node, state movedRunState) error {
	if err := t.preflightSplice(parent, dst, run); err != nil {
		return err
	}
	srcRB, group, srcOrder := state.rb, state.group, state.order
	if srcRB && srcOrder == nil {
		t.work.hashErr = &opError{"LY_EINT", "Missing retained red-black topology for a system-ordered run."}
		return t.work.hashErr
	}
	var members, rest []*Node
	if srcRB {
		// the traversal and its slice, charged before they are made
		if err := t.work.chargeHash(uint64(len(srcOrder.nodes)) + 1); err != nil {
			return err
		}
		members = srcOrder.postOrder() // lyds_merge_nodes3's visiting order
	}
	for _, n := range run {
		if !srcRB || srcOrder.nodes[n] == nil {
			rest = append(rest, n)
		}
	}
	// lyds_get_rb_tree of the matched destination leader selects nodes1, 2 or 3 (1701-1725)
	t.switchMixed(dst, run[0])
	lo := t.sortedLeaderIndex(dst, run[0]) // the leader lyd_move_nodes_by_schema passes lyds_merge
	if lo < 0 && t.work.hashErr != nil {
		return t.work.hashErr // the leader probe's reservation was refused
	}
	if lo < 0 {
		t.work.hashErr = &opError{"LY_EINT", "Missing destination run of a merged system-ordered run."}
		return t.work.hashErr
	}
	leader := dst.At(lo)
	// lyds_merge releases only the source leader's first, empty metadata. Other
	// metadata travels with its owner through lyd_unlink_ignore_lyds.
	if !srcRB && len(run[0].carriedRB) > 0 && run[0].carriedRB[0].order.root == nil {
		if err := run[0].touch(); err != nil {
			return err
		}
		run[0].carriedRB = run[0].carriedRB[1:]
	}
	dstGroup := dst.rbGroups[leader]
	dstOrder := dst.rbOrders[dstGroup]
	switch {
	case srcRB && !dst.rbTree[leader]:
		// lyds_merge_nodes2: the destination run joins the source tree in data order
		if group == nil {
			group = &rbGroup{}
		}
		hi := t.retainedRunEnd(dst, lo)
		dstRun := dst.schemaSpan().Slice(lo, hi).Collect()
		if err := t.rbReplay(srcOrder, dstRun); err != nil {
			return err
		}
		// the in-order publication, the membership set and the attach order, charged first
		if err := t.work.chargeHash(3*uint64(len(srcOrder.nodes)) + 1); err != nil {
			return err
		}
		final := srcOrder.ordered()
		leaderAt := slices.Index(final, dstRun[0])
		inDst := make(map[*Node]bool, len(dstRun))
		for _, n := range dstRun {
			inDst[n] = true
		}
		attach := make([]*Node, 0, len(final)-len(dstRun))
		for i := leaderAt - 1; i >= 0; i-- { // _front: each becomes the first instance in turn
			if !inDst[final[i]] {
				attach = append(attach, final[i])
			}
		}
		for _, n := range final[leaderAt+1:] { // _among and _back
			if !inDst[n] {
				attach = append(attach, n)
			}
		}
		if err := t.rbPublish(parent, dst, lo, hi, group, srcOrder, attach); err != nil {
			return err
		}
		dstGroup, dstOrder = group, srcOrder
		leader = dst.At(lo)
		members = nil
	case srcRB && !dst.unsorted[leader]:
		// lyds_merge_nodes3 into a run without LAST instances: replay on a copy, publish once
		hi := t.retainedRunEnd(dst, lo)
		group := dst.rbGroups[leader]
		current := dst.rbOrders[group]
		if current == nil {
			t.work.hashErr = &opError{"LY_EINT", "Missing retained red-black topology for a system-ordered run."}
			return t.work.hashErr
		}
		if err := t.work.chargeHash(uint64(len(current.nodes)) + 1); err != nil {
			return err
		}
		order := cloneDirtyOrder(current)
		if err := t.rbReplay(order, members); err != nil {
			return err
		}
		if err := t.rbPublish(parent, dst, lo, hi, group, order, members); err != nil {
			return err
		}
		members = nil
		dstGroup, dstOrder = group, order
		leader = dst.At(lo)
	}
	// lyds_merge_nodes3 next to LAST instances (each after its RB predecessor), and nodes1
	for _, n := range append(members, rest...) {
		if err := n.touch(); err != nil {
			return err
		}
		if dstOrder == nil || dstOrder.root == nil {
			// Build the destination once. Subsequent nodes1/3 iterations retain the
			// passed root_meta even if an arriving node owns an earlier metadata item.
			// nodes1 does not perform lyds_insert's incoming-metadata cleanup.
			carried := n.carriedRB
			n.carriedRB = nil
			err := t.insert(parent, n, insertDefault)
			n.carriedRB = carried
			if err != nil {
				return err
			}
			if err := t.adoptCarried(dst, n); err != nil {
				return err
			}
			dstGroup, dstOrder = n.rbGroup, dst.rbOrders[n.rbGroup]
			leader = dst.At(lo)
			continue
		}
		pred, ok := t.rbAdd(dstOrder, n, t.compareSortedCompatible)
		if !ok {
			if t.work.hashErr != nil {
				return t.work.hashErr
			}
			return sortedInsertRefusal(dst, n)
		}
		anchor := leader
		if pred != nil {
			anchor = pred
		}
		at, err := t.chargedIndex(dst, anchor)
		if err != nil {
			return err
		}
		if pred != nil {
			at++
		} else {
			leader = n
		}
		if err := t.work.chargeHash(addHashBound(linkSlots(dst, n, at), dst.transferCost(anchor, n))); err != nil {
			return err
		}
		n.inRB, n.rbGroup = true, dstGroup
		if err := t.link(parent, dst, n, at); err != nil {
			return err
		}
	}
	return nil
}
