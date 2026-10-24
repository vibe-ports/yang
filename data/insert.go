// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_insert_node and its helpers, lyd_insert_before,
// lyd_unlink, lyd_unlink_tree), src/tree_data_sorted.c (lyds_insert ordering, lyds_additionally_create_rb_tree),
// src/tree_data_free.c (lyd_free_tree, lyd_free_siblings) and src/tree_data_common.c
// (lyd_np_cont_dflt_*) (BSD-3-Clause, © CESNET).

package data

import (
	"fmt"
	"iter"
	"math/bits"
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

// loggedContextError retains the context selected by a native LOGERR(ctx, ...). The public Go
// error still contains the diagnostic; the conformance harness also needs its owner because the
// C oracle collects context logs separately.
type loggedContextError struct {
	error
	set *schema.Set
}

func (e *loggedContextError) Unwrap() error { return e.error }

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
// lyd_insert_get_next_anchor and lyd_insert_node_find_anchor): opaque nodes are always last;
// top-level schema nodes of different modules, at the top level or under an opaque parent, are
// ordered by strcmp of the module names.
func (t *Tree) after(a, n *Node) bool {
	switch {
	case a.schema == nil:
		return n.schema != nil
	case n.schema == nil:
		return false
	case a.schema.Module != n.schema.Module && a.schema.DataParent() == nil && n.schema.DataParent() == nil:
		// Top-level nodes of the same named module from different contexts have
		// distinct compiled module pointers. lyd_insert_get_next_anchor cannot
		// walk from n's schema to a's foreign schema, so a is the next anchor.
		// Different module names retain their lexicographic order.
		return strings.Compare(a.schema.Module.Name, n.schema.Module.Name) >= 0
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
	return compareKeyRuns(nil, a.kids.list, b.kids.list)
}

func (t *Tree) compareSorted(a, b *Node) int {
	if a.schema.Kind == schema.LeafList {
		return types.Compare(a.value, b.value)
	}
	return compareKeyRuns(&t.work, a.kids.list, b.kids.list)
}

// compareSortedCompatible compares equivalent sorted instances whose schema nodes came from
// distinct contexts. libyang retains the first run's RB comparator and calls it for the foreign
// value (tree_data_sorted.c:195-199,212-222).
func (t *Tree) compareSortedCompatible(a, b *Node) (int, bool) {
	if a.schema == nil || b.schema == nil || a.schema.Kind != b.schema.Kind {
		return 0, false
	}
	if a.schema.Kind == schema.LeafList {
		return types.CompareCompatible(a.value, b.value)
	}
	if a.schema.Kind != schema.List {
		return 0, false
	}
	for i := range min(len(a.kids.list), len(b.kids.list)) {
		t.work.Add(1)
		ka, kb := a.kids.list[i], b.kids.list[i]
		if !ka.isKey() || !kb.isKey() {
			break
		}
		c, compatible := types.CompareCompatible(ka.value, kb.value)
		if !compatible || c != 0 {
			return c, compatible
		}
	}
	return 0, true
}

// compareKeyRuns is rb_compare_lists (tree_data_sorted.c:235-270): compare every
// leading key pair, with equality when either run ends, including a proper prefix.
func compareKeyRuns(work *workCounter, a, b []*Node) int {
	for i := range min(len(a), len(b)) {
		if work != nil {
			work.Add(1)
		}
		ka, kb := a[i], b[i]
		if !ka.isKey() || !kb.isKey() {
			break
		}
		if c := types.Compare(ka.value, kb.value); c != 0 {
			return c
		}
	}
	return 0
}

type reorderHash uint8

type rbGroup [1]byte

const (
	reorderExact reorderHash = 1 << iota
	reorderFirst
)

// applyRunReorderHash replays the hash side effects of libyang linking the instances of one run
// in sibling order (before, the run before the port's stable sort):
//   - reorderExact: lyds_additionally_create_rb_nodes (tree_data_sorted.c:1052-1073) relocates
//     every instance that is not the running maximum (rb_insert_node puts equal values right, so
//     an instance equal to the maximum is the maximum) with lyd_unlink_ignore_lyds +
//     lyd_insert_hash: its exact-value record moves to the back of its bucket;
//   - reorderFirst: each instance below the running minimum is linked before the leader
//     (lyds_link_data_node, tree_data_sorted.c:1021-1040) and lyd_insert_hash_add
//     (tree_data_hash.c:128-163) removes the old first's first-instance record and inserts the
//     new one (refused when it already has one, D-0118). Every transition is replayed in order,
//     not only old first → final first: the intermediate removals are observable.
//
// The parser defers the sort: appendSorted applies reorderExact to the instances that
// existed when the run got its RB tree, and flushDirtyRun applies reorderFirst to the whole run
// in sibling order (those instances, then the parsed ones in parse order), which is the order
// lyds_insert linked them in. One counted comparison per instance and mode.
func (t *Tree) applyRunReorderHash(sib *siblings, before []*Node, mode reorderHash) {
	if len(before) == 0 || sib.ht == nil {
		return
	}
	if !sib.firstRecordSchema(before[0].schema) {
		mode &^= reorderFirst // monotone ordinary siblings need no first-instance records
	}
	maximum, minimum := before[0], before[0]
	for _, n := range before[1:] {
		if mode&reorderExact != 0 {
			t.work.Add(1)
			if t.compareSorted(maximum, n) <= 0 { // equal values extend the running maximum
				maximum = n
			} else {
				key, indexed := n.hkey, n.hashed
				sib.hashRemove(n)
				if indexed {
					sib.hashPutKey(n, key)
				} else {
					sib.hashPut(n)
				}
			}
		}
		if mode&reorderFirst != 0 {
			t.work.Add(1)
			if t.compareSorted(n, minimum) < 0 {
				sib.leaderReplace(n.schema, minimum, n)
				minimum = n
			}
		}
	}
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
// Monotone siblings use binary searches. Copies may retain a different schema order, so their
// anchors follow lyd_insert_get_next_anchor and their runs are found by schema identity.
// insertLast still constrains LYD_PARSE_ORDERED input to schema order (D-0058).
func (t *Tree) insertPos(sib *siblings, n *Node, order insertOrder) int {
	sib.touch()
	l := sib.list
	if len(l) == 0 {
		return 0
	}
	if order != insertLast {
		newCtx, firstCtx := ctxOf(n), ctxOf(l[0])
		mixedHere := firstCtx != nil && firstCtx != newCtx || len(sib.contexts) > 1
		foreignParent := l[0].parent != nil && ctxOf(l[0].parent) != nil && ctxOf(l[0].parent) != newCtx
		// Top-level nodes have no child anchor, but lyds_insert still finds an equivalent
		// system-ordered run and uses its retained RB comparator. Keep that typed path
		// independent of the child-only lyd_insert_get_next_anchor emulation below.
		if newCtx != nil && n.schema.DataParent() == nil && mixedHere &&
			order == insertDefault && sortedSupported(n) {
			if lo := t.schemaIndex(sib, n.schema); lo >= 0 {
				if at, compatible := t.crossContextSortedPos(sib, n, lo); compatible {
					return at
				}
			}
		}
		// A top-level insert has no schema parent whose children lys_getnext can restart from;
		// keep the ordinary module ordering there. The special same-child/different-parent state
		// occurs in a mixed-context diff before the destination child list itself becomes mixed.
		if newCtx != nil && n.schema.DataParent() != nil && (mixedHere || t.mixedDiff && foreignParent) {
			if at, ok := t.crossContextInsertPos(sib, n, newCtx, order, foreignParent); ok {
				return at
			}
		}
	}
	afterN := func(a *Node) bool { return t.after(a, n) }
	if order == insertDefault && sortedSupported(n) {
		lo, hi := t.runBounds(sib, n)
		if lo == hi {
			return hi // no instance yet: by schema, no RB tree for a single instance
		}
		n.inRB = true
		n.rbGroup = sib.rbGroups[l[lo]]
		if !sib.rbTree[l[lo]] {
			oldLeader := l[lo]
			insertionOrder := slices.Clone(l[lo:hi])
			// lyds_additionally_create_rb_tree: the run gets its RB tree from all its instances,
			// inserted in order, so it is sorted stably (rb_insert_node puts equal values after
			// the existing ones)
			t.applyRunReorderHash(sib, l[lo:hi], reorderExact|reorderFirst)
			slices.SortStableFunc(l[lo:hi], func(a, b *Node) int {
				t.work.Add(1)
				return t.compareSorted(a, b)
			})
			sib.transferRun(oldLeader, l[lo])
			if sib.firstRun[n.schema] == oldLeader {
				sib.firstRun[n.schema] = l[lo]
			}
			for _, a := range l[lo:hi] {
				a.inRB = true
			}
			if sib.opIdx != nil {
				if sib.opIdx[n.schema].gaps == 0 {
					sib.opRebuild(t, n.schema, l[lo:hi]) // only this run was reordered
				} else {
					sib.opBuild(t) // retain other runs copied from a user-ordered schema
				}
			}
			markRB(sib, l[lo])
			n.rbGroup = sib.rbGroups[l[lo]]
			t.buildRBOrder(sib, n.rbGroup, insertionOrder)
		} else if sib.unsorted[l[lo]] {
			// instances appended after the tree was made are not in it (only diff.c's
			// LAST_BY_SCHEMA does that): lyds_link_data_node puts n right after its RB
			// predecessor, the greatest tree member not above it, or before the leader
			at := lo
			for i := lo; i < hi; i++ {
				t.work.Add(1)
				if l[i].inRB && l[i].rbGroup == n.rbGroup && t.compareSorted(l[i], n) <= 0 {
					at = i + 1
				}
			}
			t.rbRecordInsert(sib, n)
			return at
		}
		// after the equal values (rb_insert_node goes right on 0); append fast path
		t.work.Add(1)
		if t.compareSorted(l[hi-1], n) <= 0 {
			t.rbRecordInsert(sib, n)
			return hi
		}
		at := t.upper(l[:hi], lo, func(a *Node) bool { return t.compareSorted(a, n) > 0 })
		t.rbRecordInsert(sib, n)
		return at
	}
	// lyd_insert_node_ordby_schema; append fast path
	at := len(l)
	t.work.Add(1)
	switch {
	case opChild(n.schema) && order == insertLast:
		// LYD_INSERT_NODE_LAST appends: an operation's children keep the order they are copied in
		// (lyd_dup), however the anchor split them
	case opChild(n.schema):
		at = t.opAnchor(sib, n)
	case sib.schemaUnordered:
		at = t.unorderedAnchor(sib, n)
	case t.after(l[at-1], n):
		at = t.upper(l, 0, afterN)
	}
	if order != insertDefault && sortedSupported(n) {
		var leader *Node
		switch {
		case at < len(l) && l[at].schema == n.schema && (at == 0 || l[at-1].schema != n.schema):
			leader = l[at]
		case at > 0 && l[at-1].schema == n.schema && l[at-1].inRB:
			leader = l[at-1]
			for i := at - 2; i >= 0 && l[i].schema == n.schema; i-- {
				t.work.Add(1)
				leader = l[i]
			}
		}
		if sib.rbTree[leader] {
			markUnsorted(sib, leader) // n is not in this run's RB tree
		}
	}
	return at
}

// crossContextInsertPos is lyd_insert_get_next_anchor for a child level retaining schemas from
// several contexts. With no children table libyang walks the new node's schema context and stops
// at the first foreign-context sibling. Once the table exists, tree_data.c:522-535 calls
// lys_getnext(new_node->schema, local_parent, ...). tree_schema.c:301-332 follows the supplied
// foreign node's next siblings first and only then restarts at the local parent's first child;
// lyd_find_sibling_schema makes the first matching instance the anchor.
func (t *Tree) crossContextInsertPos(sib *siblings, n *Node, newCtx *schema.Set, order insertOrder, force bool) (int, bool) {
	l := sib.list
	foreign := force || ctxOf(l[0]) != nil && ctxOf(l[0]) != newCtx
	if !foreign {
		// A nil context record can occur transiently for ownerless schemas. Only a known distinct
		// retained context selects libyang's cross-context anchor path.
		for retained, count := range sib.contexts {
			if count > 0 && retained != nil && retained != newCtx {
				foreign = true
				break
			}
		}
	}
	if !foreign {
		return 0, false
	}
	if order == insertDefault && sortedSupported(n) {
		if lo := t.schemaIndex(sib, n.schema); lo >= 0 {
			if at, compatible := t.crossContextSortedPos(sib, n, lo); compatible {
				return at, true
			}
		}
	}
	if order != insertDefault && sortedSupported(n) {
		if lo := t.schemaIndex(sib, n.schema); lo >= 0 && sib.rbTree[l[lo]] {
			markUnsorted(sib, l[lo])
		}
	}

	var opts schema.GetNextOpt
	if n.schema.InOutput() {
		opts = schema.GetNextOutput
	}
	if ctxOf(l[0]) == newCtx && l[0].parent != nil && l[0].parent.schema != nil && sib.ht != nil {
		parent := l[0].parent.schema
		probe := func(seq iter.Seq[*schema.Node], after *schema.Node) (int, bool) {
			found := after == nil
			for sn := range seq {
				if !found {
					found = sn == after
					continue
				}
				if leader := t.schemaLookup(sib, sn); leader != nil {
					return t.indexFromEnd(l, leader), true
				}
			}
			return 0, false
		}
		if ctxOf(l[0].parent) != newCtx {
			// lys_getnext(new_node->schema, local_parent, ...) first follows the foreign
			// schema's remaining siblings. Only after that list ends does it restart at the
			// local parent's first child (libyang v5.8.6 src/tree_schema.c:301-332 and
			// src/tree_data.c:522-535). An anchor
			// in the foreign suffix therefore wins over a same-named child in the restart.
			if at, ok := probe(schema.GetNext(n.schema.DataParent(), n.schema.Module.Top, opts), n.schema); ok {
				return at, true
			}
			if at, ok := probe(schema.GetNext(parent, parent.Module.Top, opts), nil); ok {
				return at, true
			}
		} else if at, ok := probe(schema.GetNext(parent, parent.Module.Top, opts), n.schema); ok {
			return at, true
		}
		return len(l), true
	}

	owner := ownerModule(newCtx, n)
	next, stop := iter.Pull(schema.GetNext(n.schema.DataParent(), n.schema.Module.Top, opts))
	defer stop()
	sn, ok := next()
	if !ok {
		return len(l), true
	}
	found := false
	for i, a := range l {
		t.work.Add(1)
		if ownerModule(ctxOf(a), a) != owner {
			return i, true
		}
		for !found {
			if sn == n.schema {
				found = true
				break
			}
			if sn == a.schema {
				break
			}
			sn, ok = next()
			if !ok {
				return len(l), true
			}
		}
		if found && a.schema != n.schema {
			return i, true
		}
	}
	return len(l), true
}

// crossContextSortedPos is lyds_insert after lyd_find_sibling_schema matched an equivalent run
// from another context. The first sorted insertion builds an RB tree only from the leader's
// pointer-identical physical run; later foreign instances join that same tree
// (libyang v5.8.6 src/tree_data_sorted.c:1013-1073,1213-1252).
func (t *Tree) crossContextSortedPos(sib *siblings, n *Node, lo int) (int, bool) {
	l := sib.list
	leader := l[lo]
	if _, compatible := t.compareSortedCompatible(leader, n); !compatible {
		t.work.hashErr = crossContextStorageError(n)
		return lo, true
	}
	if !sib.rbTree[leader] {
		hi := lo + 1
		for hi < len(l) && l[hi].schema == leader.schema {
			t.work.Add(1)
			hi++
		}
		oldLeader := leader
		insertionOrder := slices.Clone(l[lo:hi])
		t.applyRunReorderHash(sib, l[lo:hi], reorderExact|reorderFirst)
		slices.SortStableFunc(l[lo:hi], func(a, b *Node) int {
			t.work.Add(1)
			return t.compareSorted(a, b)
		})
		sib.transferRun(oldLeader, l[lo])
		if sib.firstRun[leader.schema] == oldLeader {
			sib.firstRun[leader.schema] = l[lo]
		}
		for _, a := range l[lo:hi] {
			a.inRB = true
		}
		if sib.opIdx != nil {
			if sib.opIdx[leader.schema].gaps == 0 {
				sib.opRebuild(t, leader.schema, l[lo:hi])
			} else {
				sib.opBuild(t)
			}
		}
		markRB(sib, l[lo])
		leader = l[lo]
		t.buildRBOrder(sib, sib.rbGroups[leader], insertionOrder)
	}
	group := sib.rbGroups[leader]
	if group == nil {
		group = leader.rbGroup
	}
	n.inRB = true
	n.rbGroup = group
	at, compatible := t.rbInsertPos(sib, n, lo, t.compareSortedCompatible)
	if !compatible {
		n.inRB, n.rbGroup = false, nil
		t.work.hashErr = crossContextStorageError(n)
		return lo, true
	}
	return at, true
}

func crossContextStorageError(n *Node) error {
	return &opError{"LY_EINVAL", fmt.Sprintf("Invalid arguments - the system-ordered instances of %q in two schema contexts require indeterminate libyang value storage, a diff of such data is not supported (D-0121) (lyd_diff()).", n.Name())}
}

// buildRBOrder retains rb_insert_node's exact topology. The physical slice remains the in-order
// view, but cross-context callbacks are asymmetric, so only a native root-to-leaf traversal can
// choose the same predecessor after rotations.
func (t *Tree) buildRBOrder(sib *siblings, group *rbGroup, insertionOrder []*Node) {
	sib.touch()
	order := &dirtyOrder{nodes: make(map[*Node]*dirtyOrderNode, len(insertionOrder)), work: &t.work}
	for _, n := range insertionOrder {
		_, _ = order.insertCompatible(n, func(a, b *Node) (int, bool) {
			return t.compareSorted(a, b), true
		})
	}
	if sib.rbOrders == nil {
		sib.rbOrders = make(map[*rbGroup]*dirtyOrder)
	}
	sib.rbOrders[group] = order
}

// rbInsertPos is rb_insert_node followed by lyds_link_data_node. A missing retained topology can
// only come from an older copied/moved run; rebuilding its symmetric in-order members preserves
// ordinary ordering while keeping all subsequent cross-context rotations exact.
func (t *Tree) rbInsertPos(sib *siblings, n *Node, lo int, compare func(*Node, *Node) (int, bool)) (int, bool) {
	pred, ok := t.rbInsert(sib, n, compare)
	if !ok {
		return lo, false
	}
	if pred == nil {
		return lo, true
	}
	return t.indexFromEnd(sib.list, pred) + 1, true
}

func (t *Tree) rbInsert(sib *siblings, n *Node, compare func(*Node, *Node) (int, bool)) (*Node, bool) {
	sib.touch()
	group := n.rbGroup
	order := sib.rbOrders[group]
	if order == nil {
		var members []*Node
		for _, candidate := range sib.list {
			if candidate.inRB && candidate.rbGroup == group {
				members = append(members, candidate)
			}
		}
		t.buildRBOrder(sib, group, members)
		order = sib.rbOrders[group]
	}
	order.work = &t.work
	return order.insertCompatible(n, compare)
}

// rbRecordInsert mirrors a normal symmetric insertion in the retained topology. Slice lookup is
// still the port's charged implementation for these nodes; recording the otherwise duplicate RB
// comparisons must not make its work accounting depend on this cross-context compatibility aid.
func (t *Tree) rbRecordInsert(sib *siblings, n *Node) {
	sib.touch()
	if n.rbGroup == nil {
		return
	}
	order := sib.rbOrders[n.rbGroup]
	if order == nil {
		var members []*Node
		for _, candidate := range sib.list {
			if candidate.inRB && candidate.rbGroup == n.rbGroup {
				members = append(members, candidate)
			}
		}
		t.buildRBOrder(sib, n.rbGroup, members)
		order = sib.rbOrders[n.rbGroup]
	}
	order.work = nil
	_, _ = order.insertCompatible(n, func(a, b *Node) (int, bool) {
		return compareSorted(a, b), true
	})
}

// opRebuild replaces one index entry after its contiguous sibling run was sorted.
// Reordering the run keeps its boundaries, so gaps is unchanged.
func (s *siblings) opRebuild(t *Tree, sn *schema.Node, run []*Node) {
	s.touch()
	d := s.opIdx[sn]
	d.nodeDeque = nodeDeque{list: make([]*Node, len(run))}
	s.gen++ // a physical reorder moves positions (lydCtx.position caches by gen)
	d.rb = 0
	for i, n := range run {
		t.work.Add(1)
		d.list[i] = n
		if n.inRB {
			d.rb++
		}
	}
	s.opIdx[sn] = d
}

// runBounds is the run of n's schema node in sib.list, or the empty run where its first instance
// goes. It serves system-ordered runs only (insertPos): under an operation those are input data,
// placed by the anchor or after their RB predecessor and so contiguous by construction; output
// and notification lists are user-ordered (LYS_ORDBY_USER) and never get here, so an output run
// split by the kept input ([before, v1, v2, in, v3]) keeps its anchor order
// (TestParseOpDupKeepsOrder, ops/parse-json-reply-keep-order-split).
func (t *Tree) runBounds(sib *siblings, n *Node) (lo, hi int) {
	l := sib.list
	if !opChild(n.schema) && len(l) > 0 && !sameSet(ctxOf(l[0]), ctxOf(n)) {
		// lyd_find_sibling_schema can match a system-ordered run from another
		// context by schema name, before any schema-order lookup. Its instances
		// still take part in lyds_insert.
		if lo = t.schemaIndex(sib, n.schema); lo >= 0 {
			hi = lo
			for hi < len(l) && schemaLookupMatch(l[hi].schema, n.schema, sib.ht != nil) {
				sib.visit()
				hi++
			}
			return lo, hi
		}
	}
	if opChild(n.schema) || sib.schemaUnordered {
		// Start at the native first-instance record. A copy from a user-ordered
		// schema can have several runs; lyds_insert uses only the selected one.
		inst := t.opRun(sib, n.schema)
		if len(inst) == 0 {
			var at int
			if opChild(n.schema) {
				at = t.opAnchor(sib, n)
			} else {
				at = t.unorderedAnchor(sib, n)
			}
			return at, at
		}
		// Position locates the insertion run; the nearer endpoint scan is bounded by
		// the insertion shift plus the run walked/sorted here.
		lo = t.runFrom(l, inst[0], inst[len(inst)-1], len(inst))
		return lo, lo + len(inst)
	}
	lo = t.upper(l, 0, func(a *Node) bool { return t.sameOrAfter(a, n) })
	return lo, t.upper(l, lo, func(a *Node) bool { return t.after(a, n) })
}

// runEnd stops at the first different schema, even when another run follows later.
func (t *Tree) runEnd(sib *siblings, lo int) int {
	sn := sib.list[lo].schema
	if d := sib.opIdx[sn]; d != nil && d.gaps == 0 {
		return t.indexFromEnd(sib.list, d.list[len(d.list)-1]) + 1
	}
	if !sib.schemaUnordered && !opChild(sn) {
		return t.upper(sib.list, lo, func(n *Node) bool { return n.schema != sn })
	}
	hi := lo
	for hi < len(sib.list) {
		t.work.Add(1)
		if sib.list[hi].schema != sn {
			break
		}
		hi++
	}
	return hi
}

// unorderedAnchor follows lyd_insert_get_next_anchor for ordinary siblings retained in source
// order. With a children table, choose the closest following schema with an instance; without
// one, walk the document and schema together. These differ on reordered children.
func (t *Tree) unorderedAnchor(sib *siblings, n *Node) int {
	dp := n.schema.DataParent()
	if sib.ht != nil {
		order := t.opOrder(dp, false)
		for r := t.schemaRank(n.schema) + 1; r < len(order); r++ {
			t.work.Add(1)
			if leader := physicalStart(sib, order[r], t.schemaLookup(sib, order[r])); leader != nil {
				return t.indexFromEnd(sib.list, leader)
			}
		}
		return len(sib.list)
	}
	var top []*schema.Node
	if dp == nil {
		top = n.schema.Module.Top
	}
	next, stop := iter.Pull(schema.GetNext(dp, top, 0))
	defer stop()
	sn, ok := next()
	if !ok {
		return len(sib.list)
	}
	found := false
	started := dp != nil
	own := ownerModule(t.set, n)
	for i, a := range sib.list {
		t.work.Add(1)
		am := ownerModule(t.set, a)
		if !started {
			if am.Name < own.Name {
				continue
			}
			started = true
		}
		if am != own {
			return i
		}
		for !found {
			t.work.Add(1)
			if !ok {
				return len(sib.list)
			}
			if sn == n.schema {
				found = true
				break
			}
			if sn == a.schema {
				break
			}
			sn, ok = next()
		}
		if found && a.schema != n.schema {
			return i
		}
	}
	return len(sib.list)
}

// runFrom returns the start of a contiguous run, finding whichever of its endpoints is nearer
// an end of l. Each pair of examined slots is counted once, like indexFromEnd.
func (t *Tree) runFrom(l []*Node, first, last *Node, length int) int {
	for i, j := 0, len(l)-1; i <= j; i, j = i+1, j-1 {
		t.work.Add(1)
		switch {
		case l[i] == first:
			return i
		case l[i] == last:
			return i - length + 1
		case l[j] == last:
			return j - length + 1
		case l[j] == first:
			return j
		}
	}
	return -1
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
	return indexFromEnd(&t.work, l, n)
}

func indexFromEnd(work *workCounter, l []*Node, n *Node) int {
	for i, j := 0, len(l)-1; i <= j; i, j = i+1, j-1 {
		work.Add(1)
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

// rankBits is a 64-ary summary tree. Level zero stores schema ranks; every higher level marks
// the nonempty words below it, so next skips absent schema siblings in logarithmic word visits.
type rankBits struct {
	levels [][]uint64
	bits   int
}

func (b *rankBits) reset(work *workCounter, n int) {
	b.levels, b.bits = nil, n
	for n > 0 {
		words := (n + 63) / 64
		work.Add(int64(words))
		b.levels = append(b.levels, make([]uint64, words))
		if words == 1 {
			return
		}
		n = words
	}
}

func (b *rankBits) set(work *workCounter, rank int) {
	if rank < 0 || rank >= b.bits {
		return
	}
	for _, words := range b.levels {
		work.Add(1)
		word, bit := rank/64, uint(rank%64)
		old := words[word]
		words[word] |= uint64(1) << bit
		if old != 0 {
			return
		}
		rank = word
	}
}

func (b *rankBits) clear(work *workCounter, rank int) {
	if rank < 0 || rank >= b.bits {
		return
	}
	for _, words := range b.levels {
		work.Add(1)
		word, bit := rank/64, uint(rank%64)
		words[word] &^= uint64(1) << bit
		if words[word] != 0 {
			return
		}
		rank = word
	}
}

func (b *rankBits) next(t *Tree, rank int) int {
	if len(b.levels) == 0 || rank >= b.bits-1 {
		return -1
	}
	return b.nextAt(t, 0, rank)
}

func (b *rankBits) nextAt(t *Tree, level, rank int) int {
	words := b.levels[level]
	start := rank + 1
	word := start / 64
	if word >= len(words) {
		return -1
	}
	t.work.Add(1)
	w := words[word] & (^uint64(0) << uint(start%64))
	if w != 0 {
		return word*64 + bits.TrailingZeros64(w)
	}
	if level+1 == len(b.levels) {
		return -1
	}
	word = b.nextAt(t, level+1, word)
	if word < 0 {
		return -1
	}
	t.work.Add(1)
	return word*64 + bits.TrailingZeros64(words[word])
}

// opOrder is lys_getnext over a schema parent, including operation input/output, cached per tree.
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

type opEntry struct {
	nodeDeque
	rank      int
	direction int
	gaps      int // extra runs in sibling order; zero means every instance is adjacent
	rb        int // instances in libyang's RB tree; exact through every index mutation
}

// opInst is the instances of sn in s.list for an operation or unordered copy. It only reads: the
// index is kept by the mutations alone (link, unlink, the RB sort), so concurrent readers of an
// unchanged tree share it safely.
func (s *siblings) opInst(sn *schema.Node) []*Node {
	if s.opIdx == nil {
		// a sibling set never linked through link (sibsOf's singleton of a detached node):
		// no index, the few nodes are filtered
		var inst []*Node
		for _, a := range s.list {
			if s.work != nil {
				s.work.Add(1)
			}
			if a.schema == sn {
				inst = append(inst, a)
			}
		}
		return inst
	}
	if d := s.opIdx[sn]; d != nil {
		return d.list
	}
	return nil
}

// schemaLookupOperation is lyd_find_sibling_schema for operation children and unordered copies. A (leaf-)list
// uses the schema-only first-instance chain while the table exists, including its observable
// absence. Other schema nodes use their ordinary schema-key bucket. Without a table libyang walks
// siblings and finds the first indexed instance.
func (t *Tree) schemaLookupOperation(s *siblings, sn *schema.Node) *Node {
	if s.ht == nil {
		if inst := s.opInst(sn); len(inst) > 0 {
			return inst[0]
		}
		return nil
	}
	if s.firstRecordSchema(sn) {
		return s.leaderFirst(sn)
	}
	var h uint32
	if t.work.hashDepth > 0 {
		h = schemaHashWork(sn, &t.work)
		if t.work.hashErr != nil {
			return nil
		}
	} else {
		h = schemaHash(sn)
	}
	if c := s.ht[idxKey{s: sn, hash: types.FinishHash32(h)}]; c != nil && c.first != nil {
		s.visit()
		return c.first.node
	}
	return nil
}

// physicalStart turns an operation child's first-instance record into a node to take a physical
// position from. While the parser defers its run (siblings.pending) the record follows the tree
// order, but the run's slots still hold link order: libyang's run is sorted in place, so its
// sorted first instance stands where the deferred run physically starts.
func physicalStart(s *siblings, sn *schema.Node, leader *Node) *Node {
	if leader == nil || s.pending[sn] == nil {
		return leader
	}
	if inst := s.opInst(sn); len(inst) > 0 {
		return inst[0]
	}
	return leader
}

// opRun is LYD_LIST_FOR_INST from the first instance: stop at the first other schema.
// Most entries are contiguous, so their index slice is already the run and needs no position.
// ponytail: anchor-created split entries arise only around the hash-table transition with
// fewer than four kept input nodes; those boundaries bound the position lookups on replies.
func (t *Tree) opRun(s *siblings, sn *schema.Node) []*Node {
	leader := physicalStart(s, sn, t.schemaLookupOperation(s, sn))
	if leader == nil {
		return nil
	}
	d := s.opIdx[sn]
	if d != nil && d.gaps == 0 {
		if d.list[0] == leader {
			return d.list
		}
		// A gap-free entry has the same order as the sibling run: the leader's run is the rest
		// of the entry (an interior leader only after a stale record, a counted scan).
		if i := indexFromEnd(s.work, d.list, leader); i >= 0 {
			return d.list[i:]
		}
	}
	i := s.position(leader)
	if i < 0 {
		return nil
	}
	j := i
	for j < len(s.list) && s.list[j].schema == sn {
		t.work.Add(1)
		j++
	}
	return s.list[i:j]
}

// opNeighbours reads only the slots adjacent to the changed slot, before unlink or after link.
func (s *siblings) opNeighbours(at int) (left, right *schema.Node) {
	if at > 0 {
		left = s.list[at-1].schema
	}
	if at+1 < len(s.list) {
		right = s.list[at+1].schema
	}
	return left, right
}

// opBuild (re)builds the instance index of an operation or unordered copy; mutations only.
func (s *siblings) opBuild(t *Tree) {
	s.touch()
	s.opIdx = map[*schema.Node]*opEntry{}
	s.opRanks = [2]rankBits{}
	if len(s.list) > 0 && opChild(s.list[0].schema) {
		s.opRanks[0].reset(&t.work, len(t.opOrder(s.list[0].schema.DataParent(), false)))
		s.opRanks[1].reset(&t.work, len(t.opOrder(s.list[0].schema.DataParent(), true)))
	}
	for i, a := range s.list {
		t.work.Add(1)
		d := s.opIdx[a.schema]
		if d == nil {
			d = s.opNewEntry(t, a.schema)
			s.opIdx[a.schema] = d
		} else if s.list[i-1].schema != a.schema {
			d.gaps++
		}
		d.list = append(d.list, a)
		if a.inRB {
			d.rb++
		}
	}
}

// opRemove drops n, being unlinked, from the instance index and returns whether its schema and
// any of its RB-tree members survive.
func (s *siblings) opRemove(n *Node, at int) (survives, survivesInRB bool) {
	s.touch()
	if s.opIdx == nil {
		return false, false
	}
	d := s.opIdx[n.schema]
	if d == nil {
		return false, false
	}
	left, right := s.opNeighbours(at)
	if left != nil && left == right && left != n.schema {
		s.opIdx[left].gaps-- // removing n joins the neighbouring runs
	}
	if len(d.list) > 1 && left != n.schema && right != n.schema {
		d.gaps-- // a singleton run disappears
	}
	// unlink already scans the sibling slice once on main, so this index scan and the shorter-side
	// compaction add no order of growth.
	i := indexFromEnd(s.work, d.list, n)
	if i < 0 {
		return len(d.list) > 0, d.rb > 0
	}
	moved := d.deleteAt(i)
	if s.work.shifts {
		s.work.Add(int64(moved))
	}
	if len(d.list) == 0 {
		delete(s.opIdx, n.schema)
		s.opRanks[d.direction].clear(s.work, d.rank)
		return false, false
	}
	if n.inRB {
		d.rb--
	}
	return true, d.rb > 0
}

// opNewEntry records the first instance of sn, including occupied ranks for operation children.
func (s *siblings) opNewEntry(t *Tree, sn *schema.Node) *opEntry {
	s.touch()
	if !opChild(sn) {
		return &opEntry{}
	}
	if len(s.opRanks[0].levels) == 0 && len(s.opRanks[1].levels) == 0 {
		op := sn.DataParent()
		s.opRanks[0].reset(&t.work, len(t.opOrder(op, false)))
		s.opRanks[1].reset(&t.work, len(t.opOrder(op, true)))
	}
	d := 0
	if sn.InOutput() {
		d = 1
	}
	rank := t.schemaRank(sn)
	s.opRanks[d].set(&t.work, rank)
	return &opEntry{rank: rank, direction: d}
}

// opIdxAdd records n, just placed at index at of s.list, in the operation's instance index.
// An adjacent instance determines the index position locally; otherwise n appends after the last
// instance or the split output run is rebuilt in sibling order.
func (s *siblings) opIdxAdd(t *Tree, n *Node, at int) {
	s.touch()
	left, right := s.opNeighbours(at)
	if left != nil && left == right && left != n.schema {
		s.opIdx[left].gaps++ // n splits the neighbouring run
	}
	d := s.opIdx[n.schema]
	if d == nil {
		d = s.opNewEntry(t, n.schema)
		d.list = []*Node{n}
		if n.inRB {
			d.rb = 1
		}
		s.opIdx[n.schema] = d
		return
	}
	if left != n.schema && right != n.schema {
		d.gaps++ // a new separate run of n
	}
	add := func(i int) {
		moved := d.insertAt(i, n)
		if n.inRB {
			d.rb++
		}
		if t.work.shifts {
			t.work.Add(int64(moved))
		}
	}
	// Neighbour positions cost no more than the index insertion shift plus one.
	// The nonadjacent case occurs at the bounded anchor-created split boundaries.
	switch {
	case at+1 < len(s.list) && s.list[at+1].schema == n.schema:
		add(t.indexFromEnd(d.list, s.list[at+1]))
	case at > 0 && s.list[at-1].schema == n.schema:
		add(t.indexFromEnd(d.list, s.list[at-1]) + 1)
	case t.indexFromEnd(s.list, d.list[len(d.list)-1]) < at:
		add(len(d.list))
	default:
		// ponytail: the full rebuild is reached once per anchor-created split boundary of an
		// output run; adjacent insertions on either side use the neighbour cases above.
		d = &opEntry{rank: d.rank, direction: d.direction, gaps: d.gaps}
		for _, a := range s.list {
			t.work.Add(1)
			if a.schema == n.schema {
				d.list = append(d.list, a)
				if a.inRB {
					d.rb++
				}
			}
		}
		s.opIdx[n.schema] = d
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
		// lyd_insert_get_next_anchor finds the closest following schema sibling that has an
		// instance. The occupied-rank summary skips absent schema siblings while preserving that
		// exact input/output-local search.
		d := 0
		if opts == schema.GetNextOutput {
			d = 1
		}
		for r := sib.opRanks[d].next(t, t.schemaRank(n.schema)); r >= 0 && r < len(order); r = sib.opRanks[d].next(t, r) {
			leader := physicalStart(sib, order[r], t.schemaLookupOperation(sib, order[r]))
			if leader != nil {
				// The record, not the document-first instance, is the anchor.
				return t.indexFromEnd(l, leader)
			}
		}
		return len(l)
	}
	own := ownerModule(ctxOf(n), n)
	k, found := 0, false
	for m, a := range l {
		t.work.Add(1)
		if ownerModule(ctxOf(a), a) != own {
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
func markRB(sib *siblings, leader *Node) {
	sib.touch()
	if sib.rbTree == nil {
		sib.rbTree = map[*Node]bool{}
	}
	sib.rbTree[leader] = true
	if sib.rbGroups == nil {
		sib.rbGroups = map[*Node]*rbGroup{}
	}
	group := sib.rbGroups[leader]
	if group == nil {
		group = leader.rbGroup
		if group == nil {
			group = &rbGroup{}
		}
		sib.rbGroups[leader] = group
	}
	// A newly created tree includes every indexed instance of this physical run.
	for i := sib.position(leader); i >= 0 && i < len(sib.list) && sib.list[i].schema == leader.schema; i++ {
		if sib.work != nil {
			sib.work.Add(1)
		}
		if sib.list[i].inRB && sib.list[i].rbGroup == nil {
			sib.list[i].rbGroup = group
		}
	}
}

// markUnsorted records that the run of s has instances outside its RB tree (appended by
// insertLast/insertLastBySchema after the tree was made).
func markUnsorted(sib *siblings, leader *Node) {
	sib.touch()
	if sib.unsorted == nil {
		sib.unsorted = map[*Node]bool{}
	}
	sib.unsorted[leader] = true
}

// insert is lyd_insert_node: link the unlinked node n under parent (nil: the top level of t).
// A list instance must have all its keys (libyang links it into its parent only then).
func (t *Tree) insert(parent, n *Node, order insertOrder) error {
	sib := t.childrenOf(parent)
	if err := t.preflightInsertPosition(sib, n, order); err != nil {
		return err
	}
	at := -1 // opaque: appended to the opaque nodes
	if n.schema != nil {
		at = t.insertPos(sib, n, order)
	}
	return t.link(parent, sib, n, at)
}

// preflightInsertPosition covers the old run's exact-record reinsertions that
// insertPos may perform when it first builds a sorted run. It observes the run
// before sorting it, so a failed hash charge cannot reorder visible siblings.
func (t *Tree) preflightInsertPosition(sib *siblings, n *Node, order insertOrder) error {
	if err := t.preflightSortedStorage(sib, n, order); err != nil {
		return err
	}
	amount, err := t.insertPositionBound(sib, n, order)
	if err != nil {
		return err
	}
	return t.reserveHash(amount)
}

// preflightSortedStorage prevents crossContextSortedPos from silently falling back to schema
// order when the retained native callback would read indeterminate or invalid foreign storage.
// Defined scalar and same-representation pairs are handled by CompareCompatible; the
// remaining native states are undefined and use the existing D-0121 cross-context refusal.
func (t *Tree) preflightSortedStorage(sib *siblings, n *Node, order insertOrder) error {
	if order != insertDefault || !sortedSupported(n) || len(sib.list) == 0 {
		return nil
	}
	newCtx := ctxOf(n)
	foreign := ctxOf(sib.list[0]) != nil && ctxOf(sib.list[0]) != newCtx || len(sib.contexts) > 1
	if !foreign && sib.list[0].parent != nil && ctxOf(sib.list[0].parent) != nil &&
		ctxOf(sib.list[0].parent) != newCtx && t.mixedDiff {
		foreign = true
	}
	if !foreign {
		return nil
	}
	lo := t.schemaIndex(sib, n.schema)
	if lo < 0 {
		return nil
	}
	leader := sib.list[lo]
	check := []*Node{leader}
	if sib.rbTree[leader] {
		group := sib.rbGroups[leader]
		check = check[:0]
		for _, candidate := range sib.list {
			if candidate.inRB && candidate.rbGroup == group {
				check = append(check, candidate)
			}
		}
	}
	for _, candidate := range check {
		if _, compatible := t.compareSortedCompatible(candidate, n); !compatible {
			return crossContextStorageError(n)
		}
	}
	return nil
}

// insertPositionBound is preflightInsertPosition's amount: zero when insertPos
// replays no registrations. Otherwise it also covers the following link
// (linkBound without a key, plus a displaced captured prefix), so a link that
// follows insertPos's visible changes cannot fail.
func (t *Tree) insertPositionBound(sib *siblings, n *Node, order insertOrder) (uint64, error) {
	if order != insertDefault || !sortedSupported(n) || len(sib.list) == 0 || sib.hasRB(n.schema) {
		return 0, t.work.hashErr
	}
	lo, hi := t.runBounds(sib, n)
	if t.work.hashErr != nil {
		return 0, t.work.hashErr
	}
	if lo == hi || sib.rbTree[sib.list[lo]] {
		return 0, nil
	}
	// insertPos now sorts the run and builds its RB state, replaying old
	// registrations of an indexed set, all before link. Reserve the link in the
	// same check (at any position: it is not known yet), including the child
	// table an unindexed set creates, so it cannot fail after those changes.
	var amount uint64
	if sib.ht != nil {
		for _, old := range sib.list[lo+1 : hi] {
			if !old.hashed {
				amount = addHashBound(amount, indexWorkBound(old, nil, nil, &t.work))
			}
		}
	}
	parent := sib.list[0].parent
	if parent != nil && parent.schema != nil && (sib.ht != nil || len(sib.list)+1 >= htMinItems) {
		amount = addHashBound(amount, indexWorkBound(n, nil, nil, &t.work))
		if sib.ht == nil {
			for _, old := range sib.list {
				amount = addHashBound(amount, indexWorkBound(old, nil, nil, &t.work))
			}
		}
	}
	if parent != nil && parent.schema != nil && parent.schema.Kind == schema.List {
		if ps := parent.siblingsOf(); ps != nil && ps.ht != nil {
			amount = addHashBound(amount, indexBatchInsertedBound(parent, []*Node{n}, &t.work))
			if parent.keyN < len(parent.schema.Keys) {
				// The sort can move any child into the incomplete compared prefix.
				amount = addHashBound(amount, indexAnyOrderBound(parent, n, &t.work))
			}
		}
		if parent.lkeyFrozen == nil {
			amount = addHashBound(amount, uint64(parent.lkeyN)) //nolint:gosec // a child count is nonnegative
		}
	}
	return amount, t.work.hashErr
}

// preflightInsertAnchor resolves the cold schema hashes that insertPos may
// need when n will start a run. Call before unlinking an existing node.
func (t *Tree) preflightInsertAnchor(parent, n *Node) error {
	if n.schema == nil {
		return t.work.hashErr
	}
	sib := t.childrenOf(parent)
	if opChild(n.schema) || sib.schemaUnordered {
		instances := sib.opInst(n.schema)
		if len(instances) == 0 || len(instances) == 1 && instances[0] == n {
			if opChild(n.schema) {
				t.opAnchor(sib, n)
			} else {
				t.unorderedAnchor(sib, n)
			}
		}
	}
	return t.work.hashErr
}

// insertBefore is lyd_insert_before for a user-ordered instance: n goes right before anchor,
// an instance of the same schema node.
func (t *Tree) insertBefore(anchor, n *Node) error {
	sib := anchor.siblingsOf()
	return t.link(anchor.parent, sib, n, slices.Index(sib.list, anchor))
}

func (t *Tree) childrenOf(parent *Node) *siblings {
	if parent == nil {
		return &t.top
	}
	if parent.any != nil {
		return &parent.any.children
	}
	return &parent.kids
}

// link puts the unlinked n at position at of sib.list (an opaque node: of sib.opq, appended when
// at is -1) under parent, and updates the children index and the default flags of the NP-container
// ancestors.
func (t *Tree) link(parent *Node, sib *siblings, n *Node, at int) error {
	// A schema lookup performed while finding at may already have exhausted the
	// operation quota. Do not publish this child after that failed preflight.
	if t.work.hashErr != nil {
		return t.work.hashErr
	}
	if n.schema != nil && !n.schema.IsKey() && parent != nil && parent.schema != nil &&
		parent.schema.Kind == schema.List && at < parent.lkeyN && parent.lkeyFrozen == nil {
		// Displacing a captured prefix copies its nodes before publishing the
		// non-key. Reserve that work while the insertion can still fail cleanly.
		if err := t.work.chargeHash(uint64(parent.lkeyN)); err != nil { //nolint:gosec // a child count is nonnegative
			return err
		}
	}
	amount, err := t.linkBound(parent, sib, n, at)
	if err != nil {
		return err
	}
	if err := t.reserveHash(amount); err != nil {
		return err
	}
	t.touch(sib)
	sib.work = &t.work
	hadHT := sib.ht != nil
	switch {
	case n.schema == nil && (at < 0 || at >= len(sib.opq)):
		sib.opq = append(sib.opq, n)
	case n.schema == nil:
		sib.opq = slices.Insert(sib.opq, at, n)
	default:
		if sib.firstRun == nil {
			sib.firstRun = make(map[*schema.Node]*Node)
		}
		if sib.firstRun[n.schema] == nil || at < len(sib.list) && sib.list[at] == sib.firstRun[n.schema] {
			sib.firstRun[n.schema] = n
		}
		if at < len(sib.list) && sib.list[at].schema == n.schema &&
			(at == 0 || sib.list[at-1].schema != n.schema) {
			sib.transferRun(sib.list[at], n)
		}
		if !sib.schemaUnordered && !opChild(n.schema) &&
			(at > 0 && t.after(sib.list[at-1], n) || at < len(sib.list) && t.after(n, sib.list[at])) {
			// LAST copies and whole-run moves may retain source schema order. Before the
			// first inversion, ordinary runs were contiguous; materialize their records
			// before this insertion can split them.
			sib.schemaUnordered = true
			if hadHT {
				sib.leaderBuild(sib.logical())
			}
		}
		t.beforeKeyLink(parent, n, at)
		moved := sib.insertAt(at, n)
		if t.work.shifts {
			t.work.Add(int64(moved))
		}
		switch {
		case !opChild(n.schema) && !sib.schemaUnordered:
		case sib.opIdx == nil:
			sib.opBuild(t) // the operation's first child (or the first since a bulk removal)
		default:
			sib.opIdxAdd(t, n, at)
		}
	}
	t.attach(parent, sib, n)
	if hadHT && n.schema != nil {
		sib.leaderInsert(n, at)
	}
	return t.work.hashErr
}

// linkBound is all the hash work link performs for n at position at: the key
// link with its list and ancestor rehash (termHashBound), and every destination
// discriminator, including a child table the link creates. Callers check it
// as one total; separate checks could each pass while their sum does not.
func (t *Tree) linkBound(parent *Node, sib *siblings, n *Node, at int) (uint64, error) {
	var amount uint64
	if n.schema != nil && n.schema.IsKey() && parent != nil {
		a, err := t.work.termHashBound(parent, n.schema, n.value, at)
		if err != nil {
			t.work.hashErr = err
			return 0, err
		}
		amount = a
	}
	amount = addHashBound(amount, t.linkIndexBound(parent, sib, n, at))
	return amount, t.work.hashErr
}

// linkIndexBound bounds the destination discriminators of n at position at;
// immutable name components are cached and the bound itself spends no work.
func (t *Tree) linkIndexBound(parent *Node, sib *siblings, n *Node, at int) uint64 {
	var amount uint64
	if n.schema != nil && parent != nil && parent.schema != nil &&
		(sib.ht != nil || len(sib.list)+1 >= htMinItems) {
		amount = indexWorkBound(n, nil, nil, &t.work)
		if sib.ht == nil {
			for _, old := range sib.list {
				amount = addHashBound(amount, indexWorkBound(old, nil, nil, &t.work))
			}
		}
	}
	if parent != nil && parent.schema != nil && parent.schema.Kind == schema.List &&
		(n.schema == nil || n.schema.IsKey() || parent.keyN < len(parent.schema.Keys)) {
		if ps := parent.siblingsOf(); ps != nil && ps.ht != nil {
			position := at
			if n.schema == nil {
				position = len(parent.kids.list) + len(parent.kids.opq)
			}
			amount = addHashBound(amount, indexInsertedBound(parent, n, position, &t.work))
		}
	}
	return amount
}

// reserveHash checks amount against the remaining operation quota; a failure is
// sticky for the operation.
func (t *Tree) reserveHash(amount uint64) error {
	if t.work.hashErr != nil {
		return t.work.hashErr
	}
	if err := t.work.checkHashWork(amount); err != nil {
		t.work.hashErr = err
		return err
	}
	return nil
}

// preflightMoveDestination uses only read-only bounds. insertPos can reorder a
// sorted destination run, so it must not run before the source is known safe.
func (t *Tree) preflightMoveDestination(parent, n *Node) error {
	sib := t.childrenOf(parent)
	// One total: insertPos's replays, the source removal and the destination.
	amount, err := t.insertPositionBound(sib, n, insertDefault)
	if err != nil {
		return err
	}
	if n.schema != nil && parent != nil && parent.schema != nil &&
		(sib.ht != nil || len(sib.list)+1 >= htMinItems) {
		amount = addHashBound(amount, indexWorkBound(n, nil, nil, &t.work))
		if sib.ht == nil {
			for _, old := range sib.list {
				amount = addHashBound(amount, indexWorkBound(old, nil, nil, &t.work))
			}
		}
	}
	if parent != nil && parent.schema != nil && parent.schema.Kind == schema.List && n.schema != nil &&
		(n.schema.IsKey() || parent.keyN < len(parent.schema.Keys)) {
		if ps := parent.siblingsOf(); ps != nil && ps.ht != nil {
			amount = addHashBound(amount, indexWorkBound(parent, nil, nil, &t.work))
			amount = addHashBound(amount, indexInsertedBound(parent, n, 0, &t.work))
		}
	}
	if parent != nil && parent.schema != nil && parent.schema.Kind == schema.List && n.schema != nil &&
		!n.schema.IsKey() && parent.lkeyFrozen == nil {
		// link may displace the captured prefix (its chargeHash); the position is not known yet
		amount = addHashBound(amount, uint64(parent.lkeyN)) //nolint:gosec // a child count is nonnegative
	}
	if old := n.parent; old != nil && old.schema != nil && old.schema.Kind == schema.List {
		if ps := old.siblingsOf(); ps != nil && ps.ht != nil {
			amount = addHashBound(amount, indexRemovedBound(old, n, &t.work))
		}
	}
	return t.reserveHash(amount)
}

// restoreLink puts a previously linked node back after a failed move or
// temporary validation. Structural cleanup must run despite a hash quota error.
func (t *Tree) restoreLink(parent, n *Node, at int) {
	// Rollback reattaches a node that already existed before the failed operation.
	// Its hash work cannot spend the failed operation's exhausted allowance.
	used, limit, err := t.work.hashUsed, t.work.hashLimit, t.work.hashErr
	t.work.hashUsed, t.work.hashLimit, t.work.hashErr = 0, ^uint64(0), nil
	if linkErr := t.link(parent, t.childrenOf(parent), n, at); linkErr != nil {
		panic("data: failed to restore a linked node")
	}
	t.work.hashUsed, t.work.hashLimit, t.work.hashErr = used, limit, err
}

// nodeDeque is a node slice with its backing allocation retained for front headroom.
type nodeDeque struct {
	list []*Node
	base []*Node
}

// insertAt puts n at position at and returns how many slots it shifted: the shorter side moves,
// the front into retained headroom, so appending and prepending are amortized O(1).
// ponytail: a single edit in the middle stays O(n/2) shifts (a 160k list: ~40µs); an ordered
// structure if API edits of huge sibling lists in random order ever matter.
func (d *nodeDeque) insertAt(at int, n *Node) int {
	l := d.list
	if at >= len(l)/2 || len(l) == 0 {
		c := cap(l)
		d.list = slices.Insert(l, at, n)
		if cap(d.list) != c {
			d.base = nil // reallocated: drop the old array
		}
		return len(l) - at
	}
	off := cap(d.base) - cap(l)
	if off <= 0 || off >= len(d.base) || &d.base[off] != &l[0] {
		// no headroom: copy the list between as much free space as it is long on either side
		buf := make([]*Node, 3*len(l)+1)
		off = len(l) + 1
		copy(buf[off:], l)
		d.base = buf
		l = buf[off : off+len(l)]
	}
	nl := d.base[off-1 : off+len(l)]
	copy(nl, l[:at])
	nl[at] = n
	d.list = nl
	return at
}

// deleteAt removes position at and returns the number of slots shifted, moving the shorter side.
func (d *nodeDeque) deleteAt(at int) int {
	l := d.list
	if at < len(l)-1-at {
		copy(l[1:at+1], l[:at])
		if len(d.base) == 0 {
			d.base = l[:cap(l)]
		}
		l[0] = nil
		d.list = l[1:]
		return at
	}
	copy(l[at:], l[at+1:])
	l[len(l)-1] = nil
	d.list = l[:len(l)-1]
	return len(l) - 1 - at
}

// insertAt applies the shared deque insertion to a sibling list. Batches (Merge, moved runs) are
// spliced in one pass instead (splice).
func (s *siblings) insertAt(at int, n *Node) int {
	s.touch()
	return s.nodeDeque.insertAt(at, n)
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
	bindSubtreeWork(n, &t.work)
	sib.addContext(n)
	sib.gen++
	sib.hashAdd(parent, n)
	if n.flags&FlagDefault == 0 {
		npContDfltDel(parent)
	}
	t.keyLinked(parent, n)
}

// bindSubtreeWork binds the sibling sets of root's subtree, any payloads included, to work:
// the lazy journal finds its transaction through them (siblings.touch). Every binding covers a
// whole subtree, so a set already bound to work has its descendants bound too.
func bindSubtreeWork(root *Node, work *workCounter) {
	stack := []*Node{root}
	for len(stack) != 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		sib := &n.kids
		if n.any != nil {
			sib = &n.any.children
		}
		if sib.work == work && n != root {
			continue // bound with all its descendants already
		}
		sib.rebind(work)
		sib.work = work
		stack = append(stack, sib.list...)
		stack = append(stack, sib.opq...)
	}
}

// preflightSplice reserves the destination's full batch of index digests before
// any member is attached.
func (t *Tree) preflightSplice(parent *Node, sib *siblings, ns []*Node) error {
	if t.work.hashErr != nil {
		return t.work.hashErr
	}
	if len(ns) == 0 || parent == nil || parent.schema == nil {
		return nil
	}
	var amount uint64
	if sib.ht != nil || len(sib.list)+len(ns) >= htMinItems {
		for _, n := range ns {
			amount = addHashBound(amount, indexWorkBound(n, nil, nil, &t.work))
		}
		if sib.ht == nil {
			for _, n := range sib.list {
				amount = addHashBound(amount, indexWorkBound(n, nil, nil, &t.work))
			}
		}
	}
	if ps := parent.siblingsOf(); ps != nil && ps.ht != nil && parent.schema.Kind == schema.List &&
		parent.keyN < len(parent.schema.Keys) {
		amount = addHashBound(amount, indexBatchInsertedBound(parent, ns, &t.work))
	}
	if t.work.hashErr != nil {
		return t.work.hashErr
	}
	if err := t.work.checkHashWork(amount); err != nil {
		t.work.hashErr = err
		return err
	}
	return nil
}

// splice links the unlinked nodes ns, ascending, into the sorted run lo..hi of sib.list in one
// pass: each after the equal values, or before them when first. Linking them one by one would
// shift the list once per node (quadratic for a merged run).
func (t *Tree) splice(parent *Node, sib *siblings, lo, hi int, ns []*Node, first bool) error {
	if err := t.preflightSplice(parent, sib, ns); err != nil {
		return err
	}
	t.touch(sib)
	sib.work = &t.work
	hadHT := sib.ht != nil
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
	out = append(out, old[j:]...)
	sib.list, sib.base = out, nil
	if len(ns) > 0 && ns[0].schema != nil {
		sn := ns[0].schema
		if sib.firstRun == nil {
			sib.firstRun = make(map[*schema.Node]*Node)
		}
		if lo < len(old) && out[lo] != old[lo] {
			sib.transferRun(old[lo], out[lo])
		}
		if sib.firstRun[sn] == nil {
			sib.firstRun[sn] = out[lo]
		} else if lo < len(old) && out[lo] != old[lo] {
			if sib.firstRun[sn] == old[lo] {
				sib.firstRun[sn] = out[lo]
			}
		}
	}
	if t.work.shifts {
		t.work.Add(int64(len(sib.list)))
	}
	if len(ns) > 0 && (opChild(ns[0].schema) || sib.schemaUnordered) {
		sib.opBuild(t) // one counted pass, like the splice itself
	}
	for _, n := range ns {
		t.attach(parent, sib, n)
	}
	if hadHT && len(ns) > 0 {
		// Sequential insertion can make only the first spliced node a run start. If it replaces
		// an existing run's first instance, remove that record before appending the new one. A
		// brand-new run has no successor record to remove (the following nodes are from this batch).
		if at := t.indexFromEnd(sib.list, ns[0]); at >= 0 {
			starts := at == 0 || sib.list[at-1].schema != ns[0].schema
			switch {
			case !starts || !sib.firstRecordSchema(ns[0].schema):
			case lo == hi:
				sib.leaderAppend(ns[0].schema, ns[0])
			default:
				sib.leaderReplace(ns[0].schema, old[lo], ns[0])
			}
		}
	}
	return t.work.hashErr
}

// registerParent is the successful key-insertion event in tree_data.c:837-842:
// insert the rehashed list without unlinking its earlier registrations.
func registerParent(l *Node) {
	if ls := l.siblingsOf(); ls != nil && l.parent != nil {
		if ls.ht == nil {
			ls.hashAdd(l.parent, l)
			return
		}
		ls.hashPut(l)
		if ls.firstRecordSchema(l.schema) {
			if prev, next, ok := ls.opRehashNeighbours(l); ok {
				ls.leaderInsertBetween(l, prev, next)
			} else {
				ls.leaderInsert(l, indexFromEnd(ls.work, ls.list, l))
			}
		}
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

// unlinkRetainingHistory is a relocation within the same sibling set. Native
// unlink removes one current-hash record; reinsertion can reveal an older one.
// Suspend the secondary index during the move, retaining all remaining native
// registrations for the following insertion (tree_data_new.c:1177-1194).
func unlinkRetainingHistory(n *Node) {
	sib := n.siblingsOf()
	if sib == nil {
		return
	}
	sib.hashRemove(n)
	history := sib.history[n]
	unlink(n)
	if len(history) != 0 {
		sib.history[n] = history
	}
}

// afterRemove applies rb_remove_node's run-state rule after instances of sn have left s. The
// surviving flags may be computed while compacting a batch so unlinkAll does not rescan s once
// per affected schema node.
func (s *siblings) afterRemove(sn *schema.Node, survives bool) {
	s.touch()
	if !survives {
		delete(s.firstRun, sn)
	}
}

// unlink is lyd_unlink (without the key check); n's own leafref links go
// (lyd_unlink_ignore_lyds).
func unlink(n *Node) {
	sib := n.siblingsOf()
	if sib == nil {
		return
	}
	sib.touch()
	freeLinks(n)
	if n.schema == nil {
		if i := indexFromEnd(sib.work, sib.opq, n); i >= 0 {
			d := nodeDeque{list: sib.opq}
			moved := d.deleteAt(i)
			sib.opq = d.list
			if sib.work.shifts {
				sib.work.Add(int64(moved))
			}
		}
	} else if i := indexFromEnd(sib.work, sib.list, n); i >= 0 {
		sib.removeRBNode(n)
		if i > 0 && i+1 < len(sib.list) && n.schema != sib.list[i-1].schema &&
			sib.list[i-1].schema == sib.list[i+1].schema {
			// Removing a separator joins physical runs, but their RB trees do not merge.
			left := i - 1
			for left > 0 && sib.list[left-1].schema == sib.list[left].schema {
				left--
			}
			if sib.rbTree[sib.list[left]] {
				markUnsorted(sib, sib.list[left])
			}
		}
		if sib.firstRun[n.schema] == n {
			if i+1 < len(sib.list) && sib.list[i+1].schema == n.schema {
				sib.firstRun[n.schema] = sib.list[i+1]
			} else {
				delete(sib.firstRun, n.schema)
			}
		}
		if (i == 0 || sib.list[i-1].schema != n.schema) && i+1 < len(sib.list) &&
			sib.list[i+1].schema == n.schema {
			sib.transferRun(n, sib.list[i+1])
		}
		if n.isKey() {
			beforeKeyUnlink(n.parent, i, sib.work)
		}
		sib.leaderUnlink(n, i, true) // lyd_unlink_hash observes n while it is still linked
		survives := false
		if sib.opIdx != nil {
			survives, _ = sib.opRemove(n, i) // neighbours still surround n
		}
		moved := sib.deleteAt(i)
		sib.dropContext(n)
		if sib.work.shifts {
			sib.work.Add(int64(moved))
		}
		if sib.opIdx == nil {
			survives = sib.has(n.schema)
		}
		sib.afterRemove(n.schema, survives)
		if n.inRB {
			sib.clearEmptyRBGroup(n.rbGroup)
		}
		sib.runGone(n)
	}
	sib.hashForget(n)
	sib.gen++
	// ponytail: repeatedly removing the middle of a large sibling list still moves O(n) slots
	// per removal; the full indexed-sequence redesign is issue #240.
	npContDfltSet(detach(n)) // the last non-default node may be gone
}

// detach clears the links of n, already removed from its siblings and their index, and returns
// its former parent.
func detach(n *Node) *Node {
	parent := n.parent
	wasKey := n.isKey()
	n.parent, n.tree, n.inRB, n.rbGroup = nil, nil, false, nil
	if wasKey {
		n.keySlot = 0
		// Internal key removal has no parent hash/registration event in
		// lyd_unlink_ignore_lyds (tree_data.c:1187-1239).
	}
	if parent != nil && parent.schema != nil && parent.schema.Kind == schema.List {
		oldKeyN := parent.keyN
		if !wasKey {
			exposeListKeys(parent.kids.work, parent)
		}
		if wasKey || oldKeyN < len(parent.schema.Keys) || parent.keyN != oldKeyN {
			reindexList(parent)
		}
	}
	return parent
}

// freeTree is lyd_free_tree: a list key is refused, nothing is freed but the leafref links of
// the subtree (lyd_free_subtree).
func freeTree(n *Node) error {
	if n.isKey() {
		return &opError{"LY_EINVAL", fmt.Sprintf("Cannot free a list key \"%s\", free the list instance instead.", n.Name())}
	}
	if err := preflightUnlink(n); err != nil {
		return err
	}
	unlink(n)
	for d := range n.allAny() {
		freeLinks(d)
	}
	return nil
}

// preflightUnlink reserves the only hash work a single detach can cause:
// reindexing the retained parent list after its compared prefix changes.
func preflightUnlink(n *Node) error {
	parent := n.parent
	if parent == nil || parent.schema == nil || parent.schema.Kind != schema.List {
		return nil
	}
	if !n.isKey() && parent.keyN >= len(parent.schema.Keys) {
		return nil
	}
	sib := parent.siblingsOf()
	if sib == nil || sib.ht == nil || sib.work == nil {
		return nil
	}
	amount := indexRemovedBound(parent, n, sib.work)
	if sib.work.hashErr != nil {
		return sib.work.hashErr
	}
	if err := sib.work.checkHashWork(amount); err != nil {
		sib.work.hashErr = err
		return err
	}
	return nil
}

// unlinkAll unlinks every node of ns with one compaction per sibling list, for the bulk
// removals of validation (auto-deleted nodes) and lyd_free_siblings. A list key is refused
// before anything is unlinked.
func (t *Tree) unlinkAll(ns []*Node) error {
	return t.unlinkAllDefaults(ns, npContDfltAffected(ns))
}

// unlinkAllDefaults is unlinkAll with the non-presence-container ancestors whose default flags
// may change already collected. TrimXPath supplies them from its read-only preflight so their
// later scans can be charged before any mutation.
func (t *Tree) unlinkAllDefaults(ns, defaults []*Node) error {
	for _, n := range ns {
		if err := unlinkCheck(n); err != nil {
			return err
		}
	}
	if err := t.preflightUnlinkAll(ns); err != nil {
		return err
	}
	// The preparation above has reserved every retained-list discriminator.
	removed := make(map[*Node]bool, len(ns))
	for _, n := range ns {
		removed[n] = true
	}
	gone := map[*Node]bool{}
	sibs := map[*siblings]bool{}                         // touched sibling sets
	removedRuns := map[*siblings]map[*schema.Node]bool{} // affected schemas
	removedGroups := map[*siblings]map[*rbGroup]bool{}
	groups := map[*siblings][]*Node{} // removals in ns order, grouped by sibling set
	var order []*siblings
	for _, n := range ns {
		sib := n.siblingsOf()
		if sib == nil || gone[n] {
			continue
		}
		gone[n] = true
		sib.touch()
		freeLinks(n) // lyd_unlink_ignore_lyds
		if !sibs[sib] {
			sibs[sib] = true
			order = append(order, sib)
		}
		if sib.ht != nil && len(sib.list) != 0 && (sib.schemaUnordered || opChild(sib.list[0].schema)) {
			t.work.Add(1)
			groups[sib] = append(groups[sib], n)
		}
		if n.schema != nil {
			sib.removeRBNode(n)
			sib.dropContext(n)
			if removedRuns[sib] == nil {
				removedRuns[sib] = map[*schema.Node]bool{}
			}
			removedRuns[sib][n.schema] = true
			if n.inRB && n.rbGroup != nil {
				if removedGroups[sib] == nil {
					removedGroups[sib] = map[*rbGroup]bool{}
				}
				removedGroups[sib][n.rbGroup] = true
			}
		}
		sib.hashForget(n)
	}
	isGone := func(n *Node) bool { t.work.Add(1); return gone[n] }
	// First-instance records are changed as if each node were unlinked in ns order. The sibling
	// order is represented once as a mutable linked list, avoiding a rescan for every removal.
	for _, sib := range order {
		if sib.ht == nil || len(sib.list) == 0 || !sib.schemaUnordered && !opChild(sib.list[0].schema) {
			continue
		}
		pos := make(map[*Node]int, len(sib.list))
		prev, next := make([]int, len(sib.list)), make([]int, len(sib.list))
		for i, n := range sib.list {
			t.work.Add(1)
			pos[n], prev[i], next[i] = i, i-1, i+1
		}
		next[len(next)-1] = -1
		active := make([]bool, len(sib.list))
		for i := range active {
			active[i] = true
		}
		for _, n := range groups[sib] {
			t.work.Add(1)
			i, ok := pos[n]
			if !ok || !active[i] {
				continue
			}
			pi, ni := prev[i], next[i]
			var pn, nn *Node
			if pi >= 0 {
				pn = sib.list[pi]
			}
			if ni >= 0 {
				nn = sib.list[ni]
			}
			sib.leaderUnlinkBetween(n, pn, nn, true)
			active[i] = false
			if pi >= 0 {
				next[pi] = ni
			}
			if ni >= 0 {
				prev[ni] = pi
			}
		}
	}
	for _, sib := range order {
		sib.gen++
		// Transfer each physical run's state to its first survivor while the
		// compaction already visits every node. firstRun names only one run.
		var oldLeader *Node
		pending := false
		sib.list = slices.DeleteFunc(sib.list, func(n *Node) bool {
			t.work.Add(1)
			if oldLeader == nil || n.schema != oldLeader.schema {
				oldLeader, pending = n, gone[n]
			} else if pending && !gone[n] {
				sib.transferRun(oldLeader, n)
				pending = false
			}
			return gone[n]
		})
		// Compaction may have removed several separators at once. A joined run
		// retains each old tree; only members of the selected tree can be searched.
		for i := 0; i < len(sib.list); {
			j := i + 1
			for j < len(sib.list) && sib.list[j].schema == sib.list[i].schema {
				j++
			}
			leader := sib.list[i]
			if sib.rbTree[leader] {
				group := sib.rbGroups[leader]
				for _, member := range sib.list[i:j] {
					if !member.inRB || member.rbGroup != group {
						markUnsorted(sib, leader)
						break
					}
				}
			}
			i = j
		}
		if sib.opIdx != nil {
			sib.opBuild(t)
		}
		sib.opq = slices.DeleteFunc(sib.opq, isGone)
	}
	for _, sib := range order {
		present := map[*schema.Node]bool{}
		for _, n := range sib.list {
			t.work.Add(1)
			if !present[n.schema] {
				sib.firstRun[n.schema] = n
			}
			present[n.schema] = true
		}
		for s := range removedRuns[sib] {
			sib.afterRemove(s, present[s])
		}
		for group := range removedGroups[sib] {
			sib.clearEmptyRBGroup(group)
		}
	}
	affectedLists := map[*Node]bool{}
	var affectedListOrder []*Node
	for _, n := range ns {
		if gone[n] {
			if sib := n.siblingsOf(); sib != nil {
				sib.runGone(n)
			}
			delete(gone, n)
			parent := n.parent
			n.parent, n.tree, n.inRB, n.rbGroup = nil, nil, false, nil
			if parent != nil && !removed[parent] && parent.schema != nil && parent.schema.Kind == schema.List {
				if !affectedLists[parent] {
					affectedLists[parent] = true
					affectedListOrder = append(affectedListOrder, parent)
				}
			}
		}
	}
	for _, parent := range affectedListOrder {
		oldKeyN := parent.keyN
		exposeListKeys(parent.kids.work, parent)
		if oldKeyN < len(parent.schema.Keys) || parent.keyN != oldKeyN {
			reindexList(parent)
		}
	}
	npContDfltSetAll(defaults)
	return nil
}

func (t *Tree) preflightUnlinkAll(ns []*Node) error {
	// Every retained list is reindexed after the batch compaction. Reserve
	// each call before freeLinks or the sibling/index maps can change.
	removed := make(map[*Node]bool, len(ns))
	for _, n := range ns {
		removed[n] = true
	}
	var reindexWork uint64
	parents := make(map[*Node]bool)
	for _, n := range ns {
		parent := n.parent
		if parent == nil || removed[parent] || parent.schema == nil || parent.schema.Kind != schema.List {
			continue
		}
		if !n.isKey() && parent.keyN >= len(parent.schema.Keys) {
			continue
		}
		if sib := parent.siblingsOf(); sib != nil && sib.ht != nil && !parents[parent] {
			parents[parent] = true
			reindexWork = addHashBound(reindexWork, indexAfterRemovedBound(parent, removed, &t.work))
			if t.work.hashErr != nil {
				return t.work.hashErr
			}
		}
	}
	if err := t.work.checkHashWork(reindexWork); err != nil {
		t.work.hashErr = err
		return err
	}
	return nil
}

// npContDfltAffected returns the retained non-presence-container ancestors whose default flags
// may change when ns are removed, deepest first. Each ancestor is returned once so a wide batch
// cannot repeatedly scan a shared parent's growing prefix.
func npContDfltAffected(ns []*Node) []*Node {
	gone := make(map[*Node]bool, len(ns))
	for _, n := range ns {
		gone[n] = true
	}
	seen := map[*Node]bool{}
	var affected []*Node
	for _, n := range ns {
		p := n.parent
		for p != nil && gone[p] {
			p = p.parent
		}
		for p != nil && p.flags&FlagDefault == 0 && isNPCont(p.schema) {
			if seen[p] {
				break
			}
			seen[p] = true
			affected = append(affected, p)
			p = p.parent
		}
	}

	// Bucket by tree depth instead of sorting with repeated ancestor walks. The memoized depth
	// calculation visits every ancestor chain only once.
	depths := map[*Node]int{}
	maxDepth := 0
	for _, n := range affected {
		path := []*Node{}
		p := n
		base := 0
		for p != nil {
			if d, ok := depths[p]; ok {
				base = d
				break
			}
			path = append(path, p)
			p = p.parent
		}
		for i := len(path) - 1; i >= 0; i-- {
			base++
			depths[path[i]] = base
		}
		if depths[n] > maxDepth {
			maxDepth = depths[n]
		}
	}
	buckets := make([][]*Node, maxDepth+1)
	for _, n := range affected {
		buckets[depths[n]] = append(buckets[depths[n]], n)
	}
	affected = affected[:0]
	for depth := maxDepth; depth > 0; depth-- {
		affected = append(affected, buckets[depth]...)
	}
	return affected
}

// npContDfltSetAll recomputes each affected container once. Children precede parents, so a
// parent observes the final default flags of all of its affected children.
func npContDfltSetAll(affected []*Node) {
	for _, p := range affected {
		if p.flags&FlagDefault != 0 {
			continue
		}
		allDefault := true
		for c := range p.kids.all() {
			if c.flags&FlagDefault == 0 {
				allDefault = false
				break
			}
		}
		if allDefault {
			p.touch()
			p.flags |= FlagDefault
		}
	}
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
		p.touch()
		p.flags |= FlagDefault
	}
}

// npContDfltDel is lyd_np_cont_dflt_del: ancestors with an explicit descendant are not default.
func npContDfltDel(p *Node) {
	for ; p != nil && p.flags&FlagDefault != 0; p = p.parent {
		p.touch()
		p.flags &^= FlagDefault
	}
}
