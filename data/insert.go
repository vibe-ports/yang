// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_insert_node and its helpers, lyd_insert_before,
// lyd_unlink, lyd_unlink_tree), src/tree_data_sorted.c (lyds_insert ordering, lyds_additionally_create_rb_tree),
// src/tree_data_free.c (lyd_free_tree, lyd_free_siblings) and src/tree_data_common.c
// (lyd_np_cont_dflt_*) (BSD-3-Clause, © CESNET).

package data

import (
	"errors"
	"fmt"
	"iter"
	"math/bits"
	"slices"
	"strings"

	"github.com/vibe-ports/yang"
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

// compareSorted orders two instances like rb_compare_leaflists / rb_compare_lists. A list shape
// libyang cannot read (rbCompareLists refuses it) compares equal here; insertion goes through
// compareSortedCompatible, which reports it.
func compareSorted(a, b *Node) int {
	if a.schema.Kind == schema.LeafList {
		return types.Compare(a.value, b.value)
	}
	c, _ := rbCompareLists(nil, a, b)
	return c
}

func (t *Tree) compareSorted(a, b *Node) int {
	if a.schema.Kind == schema.LeafList {
		return types.Compare(a.value, b.value)
	}
	c, _ := rbCompareLists(&t.work, a, b)
	return c
}

// compareSortedCompatible is the RB comparison of a retained tree member a with b, which may come
// from another context: libyang keeps the run's comparator and calls it for b's value
// (tree_data_sorted.c:195-199,212-222). false: the closed D-0121 direct-sort profile or list
// layout is incompatible.
func (t *Tree) compareSortedCompatible(a, b *Node) (int, bool) {
	return sortedPairCompatible(&t.work, a, b)
}

// sortedPairCompatible is every RB comparison of a with b: insertion descent, native search
// (rbFind) and diff-tree insertion. A union never sorts across contexts: lyplg_type_sort_union
// dispatches on compiled member identity.
func sortedPairCompatible(work *workCounter, a, b *Node) (int, bool) {
	if a.schema == nil || b.schema == nil || a.schema.Kind != b.schema.Kind {
		return 0, false
	}
	switch a.schema.Kind {
	case schema.LeafList:
		if a.value.Type() == nil || b.value.Type() == nil {
			return 0, false
		}
		if ctxOf(a) != ctxOf(b) && (a.value.Type().Base == schema.Union || b.value.Type().Base == schema.Union) {
			return 0, false
		}
		return rbSortClb(nil, a.value, b.value) // RB descent already counts the leaf-list comparison
	case schema.List:
		return rbCompareLists(work, a, b)
	}
	return 0, false
}

// rbSortClb is rb_sort_clb (tree_data_sorted.c:195-200): val1's realtype sort callback on both
// values. CompareCompatible enforces the closed D-0121 profile before invoking that callback,
// for one realtype too (an empty time-period).
func rbSortClb(work *workCounter, v1, v2 types.Value) (int, bool) {
	if v1.Type() == nil || v2.Type() == nil {
		return 0, false
	}
	if work != nil {
		work.Add(1)
	}
	return types.CompareCompatible(v1, v2)
}

// rbCompareLists is rb_compare_lists (tree_data_sorted.c:234-270), line for line:
//
//	k1 = n1->child; k2 = n2->child;              the first children, whatever they are
//	cmp = rb_sort_clb(ctx, &k1->value, &k2->value); if (cmp) return cmp;
//	k1 = k1->next; k2 = k2->next;
//	while (k1 && k1->schema && KEY(k1) && k2 && k2->schema && KEY(k2)) { compare; next }
//	return cmp;
//
// Opaque children follow the schema children here, as libyang appends them, so a node's first
// child is its first schema child, else its first opaque one. libyang reads both first children
// as struct lyd_node_term: a missing, opaque or inner first child is no term (a NULL dereference
// or a reinterpreted node), refused like an indeterminate value pair. The loop stops at an
// opaque next sibling (no schema) or at the end, as the C does.
func rbCompareLists(work *workCounter, n1, n2 *Node) (int, bool) {
	if n1.schema != n2.schema && !listKeyProfile(work, n1, n2) {
		return 0, false
	}
	k1, k2 := firstChild(n1), firstChild(n2)
	if !isTermNode(k1) || !isTermNode(k2) {
		return 0, false
	}
	cmp, ok := rbSortClb(work, k1.value, k2.value)
	if !ok || cmp != 0 {
		return cmp, ok
	}
	for i := 1; i < n1.kids.SchemaLen() && i < n2.kids.SchemaLen(); i++ {
		k1, k2 = n1.kids.At(i), n2.kids.At(i)
		if !k1.isKey() || !k2.isKey() {
			break
		}
		if cmp, ok = rbSortClb(work, k1.value, k2.value); !ok || cmp != 0 {
			return cmp, ok
		}
	}
	return cmp, true
}

// listKeyProfile is the D-0121 list profile of two distinct compiled list schemas: the same
// ordered key names, both instances holding their full leading key prefix (rb_compare_lists
// would otherwise read a non-key first child), each key pair in the closed value profile. It
// invokes no value callback: a key that is never compared is never sorted.
func listKeyProfile(work *workCounter, n1, n2 *Node) bool {
	keys := n1.schema.Keys
	if len(keys) == 0 || len(keys) != len(n2.schema.Keys) || n1.kids.SchemaLen() < len(keys) || n2.kids.SchemaLen() < len(keys) {
		return false
	}
	if !sortKeyProfile(work, n1.schema, n2.schema) {
		return false
	}
	for i, key := range keys {
		a, b := n1.kids.At(i), n2.kids.At(i)
		if a.schema != key || b.schema != n2.schema.Keys[i] || a.value.Type() == nil || b.value.Type() == nil {
			return false
		}
		if ctxOf(n1) != ctxOf(n2) && (a.value.Type().Base == schema.Union || b.value.Type().Base == schema.Union) {
			return false
		}
		if work != nil {
			work.Add(1)
		}
		if !types.SortCompatible(a.value, b.value) {
			return false
		}
	}
	return true
}

// sortKeyProfile builds each ordered schema-key name profile once per operation. It charges
// before it allocates; a failed charge reports "incompatible" with w.hashErr set, which every
// caller tests before it builds a D-0121 refusal.
func sortKeyProfile(w *workCounter, a, b *schema.Node) bool {
	pair := [2]*schema.Node{a, b}
	if w != nil {
		if result, ok := w.sortKeys[pair]; ok {
			return result
		}
	}
	result := len(a.Keys) == len(b.Keys)
	for i := 0; result && i < len(a.Keys); i++ {
		if w != nil {
			if w.chargeHash(1+uint64(len(a.Keys[i].Name)+len(b.Keys[i].Name))) != nil {
				return false
			}
			w.Add(1)
		}
		result = a.Keys[i].Name == b.Keys[i].Name
	}
	if w != nil {
		if w.chargeHash(1) != nil {
			return false
		}
		if w.sortKeys == nil {
			w.sortKeys = make(map[[2]*schema.Node]bool)
		}
		w.sortKeys[pair] = result
	}
	return result
}

// firstChild is lyd_node_inner.child: the first schema child, else the first opaque one.
func firstChild(n *Node) *Node {
	switch {
	case n.kids.SchemaLen() != 0:
		return n.kids.At(0)
	case n.kids.OpaqueLen() != 0:
		return n.kids.opaqueAt(0)
	}
	return nil
}

func isTermNode(n *Node) bool {
	return n != nil && n.schema != nil && (n.schema.Kind == schema.Leaf || n.schema.Kind == schema.LeafList)
}

type reorderHash uint8

type rbGroup [1]byte

const (
	reorderExact reorderHash = 1 << iota
	reorderFirst
)

// reorderReplayCost bounds applyRunReorderHash's comparisons of run: at most two per instance,
// each of it with the running maximum or minimum, so each within the sum of both sides' own
// comparison bounds. Computing it is one pass, counted in it.
func reorderReplayCost(run siblingSpan) uint64 {
	var top, sum uint64
	for _, n := range run.All() {
		self := rbCompareCost(n, n)
		top = max(top, self)
		sum = addHashBound(sum, self)
	}
	count := uint64(run.Len()) //nolint:gosec // a borrowed range length is nonnegative.
	return addHashBound(count, mulHashBound(2, addHashBound(sum, mulHashBound(count, top))))
}

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
func (t *Tree) applyRunReorderHash(sib *siblings, before siblingSpan, mode reorderHash) {
	if before.Len() == 0 || sib.ht == nil {
		return
	}
	if !sib.firstRecordSchema(before.At(0).schema) {
		mode &^= reorderFirst // monotone ordinary siblings need no first-instance records
	}
	maximum, minimum := before.At(0), before.At(0)
	for _, n := range before.Slice(1, before.Len()).All() {
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
func (t *Tree) upper(l siblingSpan, lo int, after func(*Node) bool) int {
	return l.LowerBound(lo, l.Len(), func(n *Node) bool { t.work.Add(1); return after(n) })
}

// insertPos is where lyd_insert_node links the schema node n among sib.schemaSpan() (opaque nodes are
// kept apart, always last).
// Monotone siblings use binary searches. Copies may retain a different schema order, so their
// anchors follow lyd_insert_get_next_anchor and their runs are found by schema identity.
// insertLast still constrains LYD_PARSE_ORDERED input to schema order (D-0058).
func (t *Tree) insertPos(sib *siblings, n *Node, order insertOrder) int {
	if err := sib.touchLazy(); err != nil {
		return 0
	}
	l := sib.schemaSpan()
	if l.Len() == 0 {
		return 0
	}
	t.markMixed(sib, l.At(0).parent, n)
	// A narrow journal records only rbPos's sorted insertion into an ordered set of one context;
	// every other position search (and its caches) runs under a whole capture.
	narrow := order == insertDefault && !opChild(n.schema) && !sib.schemaUnordered && sib.order == nil &&
		len(sib.contexts) <= 1 && sameSet(ctxOf(l.At(0)), ctxOf(n)) && !t.mixedDiff
	if !narrow {
		if err := sib.touchWhole(); err != nil {
			return 0
		}
	}
	if order != insertLast {
		newCtx, firstCtx := ctxOf(n), ctxOf(l.At(0))
		mixedHere := firstCtx != nil && firstCtx != newCtx || len(sib.contexts) > 1
		foreignParent := l.At(0).parent != nil && ctxOf(l.At(0).parent) != nil && ctxOf(l.At(0).parent) != newCtx
		// Top-level nodes have no child anchor, but lyds_insert still finds an equivalent
		// system-ordered run and uses its retained RB comparator. Keep that typed path
		// independent of the child-only lyd_insert_get_next_anchor emulation below.
		if newCtx != nil && n.schema.DataParent() == nil && mixedHere &&
			order == insertDefault && sortedSupported(n) {
			if lo := t.sortedLeaderIndex(sib, n); lo >= 0 {
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
			if n.schema.DataParent() != nil && !sib.schemaUnordered && !sameSet(ctxOf(l.At(0)), ctxOf(n)) {
				// no leader, lyd_insert_node_ordby_schema: the linear anchor walk stops at the
				// foreign first sibling (as crossContextInsertPos's walk does for other children)
				return 0
			}
			return hi // no instance yet: by schema, no RB tree for a single instance
		}
		at, ok := t.rbPos(sib, n, lo, hi, t.compareSortedCompatible)
		if !ok && t.work.hashErr == nil {
			t.work.hashErr = sortedShapeError(n)
		}
		return at
	}
	// lyd_insert_node_ordby_schema; append fast path
	at := l.Len()
	t.work.Add(1)
	switch {
	case opChild(n.schema) && order == insertLast:
		// LYD_INSERT_NODE_LAST appends: an operation's children keep the order they are copied in
		// (lyd_dup), however the anchor split them
	case opChild(n.schema):
		at = t.opAnchor(sib, n)
	case sib.schemaUnordered:
		at = t.unorderedAnchor(sib, n)
	case t.after(l.At(at-1), n):
		at = t.upper(l, 0, afterN)
	}
	if order != insertDefault && sortedSupported(n) {
		var leader *Node
		switch {
		case at < l.Len() && l.At(at).schema == n.schema && (at == 0 || l.At(at-1).schema != n.schema):
			leader = l.At(at)
		case at > 0 && l.At(at-1).schema == n.schema && l.At(at-1).inRB:
			leader = l.At(at - 1)
			for i := at - 2; i >= 0 && l.At(i).schema == n.schema; i-- {
				t.work.Add(1)
				leader = l.At(i)
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
	l := sib.schemaSpan()
	foreign := force || ctxOf(l.At(0)) != nil && ctxOf(l.At(0)) != newCtx
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
		if lo := t.sortedLeaderIndex(sib, n); lo >= 0 {
			if at, compatible := t.crossContextSortedPos(sib, n, lo); compatible {
				return at, true
			}
		}
	}
	if order != insertDefault && sortedSupported(n) {
		if lo := t.sortedLeaderIndex(sib, n); lo >= 0 && sib.rbTree[l.At(lo)] {
			markUnsorted(sib, l.At(lo))
		}
	}

	var opts schema.GetNextOpt
	if n.schema.InOutput() {
		opts = schema.GetNextOutput
	}
	if ctxOf(l.At(0)) == newCtx && l.At(0).parent != nil && l.At(0).parent.schema != nil && sib.ht != nil {
		parent := l.At(0).parent.schema
		// lyd_find_sibling_schema is a hash lookup in libyang. The mixed sibling set being built
		// has no lineage index, so each probe indexes it once; the schema walk and the index are
		// charged to the enforced quota, keeping a create O(schema siblings + siblings).
		var index map[foreignSchemaIndexKey]int
		probe := func(seq iter.Seq[*schema.Node], after *schema.Node) (int, bool) {
			found := after == nil
			for sn := range seq {
				if t.work.chargeHash(1) != nil {
					return l.Len(), true // the sticky quota error fails the link
				}
				if !found {
					found = sn == after
					continue
				}
				if index == nil {
					index = make(map[foreignSchemaIndexKey]int) // grows after each charge
					for i, a := range l.All() {
						if t.work.chargeHash(1) != nil {
							return l.Len(), true
						}
						key, ok, err := t.foreignSchemaKey(a.schema, true)
						if err != nil {
							return t.probeFailed(err, l.Len())
						}
						if ok {
							if _, exists := index[key]; !exists {
								index[key] = i
							}
						}
					}
				}
				key, ok, err := t.foreignSchemaKey(sn, true)
				if err != nil {
					return t.probeFailed(err, l.Len())
				}
				if !ok {
					continue
				}
				i, hit := index[key]
				if !hit {
					continue
				}
				// the key is the table's lineage match; confirm it as schemaLookup does and
				// otherwise continue the same first-match scan from there
				for ; i < l.Len(); i++ {
					if t.work.chargeHash(1) != nil {
						return l.Len(), true
					}
					if schemaLookupMatch(l.At(i).schema, sn, true) {
						return i, true
					}
				}
			}
			return 0, false
		}
		if ctxOf(l.At(0).parent) != newCtx {
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
		return l.Len(), true
	}

	owner := ownerModule(newCtx, n)
	next, stop := iter.Pull(schema.GetNext(n.schema.DataParent(), n.schema.Module.Top, opts))
	defer stop()
	sn, ok := next()
	if !ok {
		return l.Len(), true
	}
	found := false
	for i, a := range l.All() {
		t.work.Add(1)
		if t.work.chargeHash(1) != nil {
			return l.Len(), true
		}
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
			if t.work.chargeHash(1) != nil {
				return l.Len(), true
			}
			sn, ok = next()
			if !ok {
				return l.Len(), true
			}
		}
		if found && a.schema != n.schema {
			return i, true
		}
	}
	return l.Len(), true
}

// probeFailed makes an anchor probe's budget failure the insertion's result: link reads the
// sticky error before publishing the node.
func (t *Tree) probeFailed(err error, at int) (int, bool) {
	if t.work.hashErr == nil {
		t.work.hashErr = err
	}
	return at, true
}

// crossContextSortedPos is lyds_insert after lyd_find_sibling_schema matched an equivalent run
// from another context. The first sorted insertion builds an RB tree only from the leader's
// pointer-identical physical run; later foreign instances join that same tree
// (libyang v5.8.6 src/tree_data_sorted.c:1013-1073,1213-1252).
func (t *Tree) crossContextSortedPos(sib *siblings, n *Node, lo int) (int, bool) {
	// Compatibility is checked only on rb_insert_node's own descent (tree_data_sorted.c:684-695):
	// the physical leader need not be on it.
	l := sib.schemaSpan()
	leader := l.At(lo)
	hi := lo + 1
	if !sib.rbTree[leader] {
		for hi < l.Len() && l.At(hi).schema == leader.schema {
			t.work.Add(1)
			hi++
		}
	}
	at, compatible := t.rbPos(sib, n, lo, hi, t.compareSortedCompatible)
	if !compatible && t.work.hashErr == nil {
		t.work.hashErr = crossContextStorageError(n)
	}
	return at, true
}

// sortedShapeError is a same-context refusal: comparing these instances makes libyang read a
// list's first child as a value although it is none (rb_compare_lists), or index before an empty
// time-period (lyplg_type_sort_time_period).
func sortedShapeError(n *Node) error {
	return &opError{"LY_EINVAL", fmt.Sprintf("Invalid arguments - ordering the system-ordered instances of %q makes libyang's sort callback read invalid storage (a list's first child that is no value, or an empty time-period) (D-0121) (lyd_insert_node()).", n.Name())}
}

func sortedInsertRefusal(sib *siblings, n *Node) error {
	if len(sib.contexts) <= 1 && sib.SchemaLen() > 0 && ctxOf(sib.At(0)) == ctxOf(n) {
		return sortedShapeError(n)
	}
	return crossContextStorageError(n)
}

func crossContextStorageError(n *Node) error {
	return &opError{"LY_EINVAL", fmt.Sprintf("Invalid arguments - the system-ordered instances of %q in two schema contexts have incompatible direct-sort profiles (D-0121) (lyd_insert_node()).", n.Name())}
}

// buildRBOrder retains rb_insert_node's exact topology. The physical slice remains the in-order
// view, but cross-context callbacks are asymmetric, so only a native root-to-leaf traversal can
// choose the same predecessor after rotations.
func (t *Tree) buildRBOrder(sib *siblings, group *rbGroup, insertionOrder []*Node) {
	if err := sib.touch(); err != nil {
		return
	}
	if t.work.chargeHash(1+uint64(len(insertionOrder))) != nil {
		return
	}
	order := &dirtyOrder{nodes: make(map[*Node]*dirtyOrderNode, len(insertionOrder)), work: &t.work}
	for _, n := range insertionOrder {
		if _, ok := t.rbAdd(order, n, t.compareSortedCompatible); !ok {
			return
		}
	}
	if sib.rbOrders == nil {
		sib.rbOrders = make(map[*rbGroup]*dirtyOrder)
	}
	sib.rbOrders[group] = order
}

// rbHeight bounds the height of a red-black tree of r nodes (2*log2(r+1)).
func rbHeight(r int) uint64 { return 2 * uint64(bits.Len(uint(r)+1)) } //nolint:gosec // a node count is nonnegative

// rbStructBound bounds rb_insert_node's work besides comparisons in a tree of r nodes: the
// size updates along the path, rb_insert_color's recolouring and rotations, and the node itself.
func rbStructBound(r int) uint64 { return 2*rbHeight(r) + 1 }

// rbCompareCost bounds one rb_compare_leaflists / rb_compare_lists call of a and b
// (tree_data_sorted.c:212-270): a unit per compared key pair plus both values' callback work.
func rbCompareCost(a, b *Node) uint64 {
	if a.schema == nil || b.schema == nil {
		return 1
	}
	if a.schema.Kind == schema.LeafList {
		return 1 + types.CompareCost(a.value, b.value)
	}
	cost := uint64(1)
	for i := range min(a.kids.SchemaLen(), b.kids.SchemaLen()) {
		ka, kb := a.kids.At(i), b.kids.At(i)
		if i > 0 && (!ka.isKey() || !kb.isKey()) {
			break
		}
		cost += 1 + types.CompareCost(ka.value, kb.value)
	}
	return cost
}

// chargeCompareCost charges rbCompareCost(a, b) while computing it, each key pair before the
// next is read, so a refused quota stops the scan of a long repeated-key prefix at once.
func chargeCompareCost(w *workCounter, a, b *Node) error {
	if a.schema == nil || b.schema == nil || a.schema.Kind == schema.LeafList {
		return w.chargeHash(rbCompareCost(a, b))
	}
	if err := w.chargeHash(1); err != nil {
		return err
	}
	for i := range min(a.kids.SchemaLen(), b.kids.SchemaLen()) {
		ka, kb := a.kids.At(i), b.kids.At(i)
		if i > 0 && (!ka.isKey() || !kb.isKey()) {
			break
		}
		// the same total as rbCompareCost, one pair at a time
		if err := w.chargeHash(1 + types.CompareCost(ka.value, kb.value)); err != nil {
			return err
		}
	}
	return nil
}

// chargedCompare charges each comparison to the operation's enforced quota before its result is
// used. A refused charge reports an incompatible comparison with t.work.hashErr set, so a
// descent stops before changing anything.
func (t *Tree) chargedCompare(compare func(*Node, *Node) (int, bool)) func(*Node, *Node) (int, bool) {
	return func(a, b *Node) (int, bool) {
		if chargeCompareCost(&t.work, a, b) != nil {
			return 0, false
		}
		return compare(a, b)
	}
}

// rbAdd inserts n into the detached or installed topology d (rb_insert): a charged descent, then
// the charged structural work, then the change. It reports n's predecessor; false means refused
// (t.work.hashErr is set when the quota refused it).
func (t *Tree) rbAdd(d *dirtyOrder, n *Node, compare func(*Node, *Node) (int, bool)) (*Node, bool) {
	if d.nodes[n] != nil {
		t.work.hashErr = rbUnlinkError(n)
		return nil, false
	}
	d.work = &t.work
	parent, left, pred, ok := d.search(n, t.chargedCompare(compare))
	if !ok || t.work.chargeHash(rbStructBound(len(d.nodes))) != nil {
		return nil, false
	}
	d.place(n, parent, left)
	return pred, true
}

// rbPos is lyds_insert of n into the sorted run sib.schemaSpan().Slice(lo, hi) (tree_data_sorted.c:1212-1253):
// a run without an RB tree first gets one from its leader and following instances
// (lyds_additionally_create_rb_tree, 1085-1104), then n goes through rb_insert and
// lyds_link_data_node links it right after its RB predecessor, or before the leader (1020-1039).
// Instances appended outside the tree (LAST, LAST_BY_SCHEMA) stay after the predecessor.
//
// All work is charged to the enforced quota before anything visible changes: the new tree is
// built detached, the descent is read-only, and the publication, hash replays and n's link are
// reserved before the run is reordered or the topology changes. It returns where link puts n;
// false means a refused comparison (t.work.hashErr is set when the quota refused it).
func (t *Tree) rbPos(sib *siblings, n *Node, lo, hi int, compare func(*Node, *Node) (int, bool)) (int, bool) {
	// lyds_insert frees only the incoming first metadata slot: detachedRB when present,
	// otherwise the first carried one. Both are dropped below, after every reservation, so a
	// refused insertion leaves the (possibly detached, unjournaled) node unchanged.
	if n.detachedRB == nil && len(n.carriedRB) > 0 && len(n.carriedRB[0].order.nodes) > 1 {
		t.work.hashErr = rbUnlinkError(n)
		return lo, false
	}
	l := sib.schemaSpan()
	leader := l.At(lo)
	create := !sib.rbTree[leader]
	group := sib.rbGroups[leader]
	var order *dirtyOrder
	if create {
		// the topology nodes lyds_create_node allocates, reserved before the allocation
		if t.work.chargeHash(uint64(hi-lo)+1) != nil { //nolint:gosec // a slice length is nonnegative
			return lo, false
		}
		if group == nil {
			group = &rbGroup{}
		}
		order = &dirtyOrder{nodes: make(map[*Node]*dirtyOrderNode, hi-lo+1)}
		for _, a := range l.Slice(lo, hi).All() {
			if _, ok := t.rbAdd(order, a, compare); !ok {
				return lo, false
			}
		}
	} else if order = sib.rbOrders[sib.rbGroups[leader]]; order == nil {
		t.work.hashErr = &opError{"LY_EINT", "Missing retained red-black topology for a system-ordered run."}
		return lo, true
	}
	if order.nodes[n] != nil {
		t.work.hashErr = rbUnlinkError(n)
		return lo, false
	}
	if j := sib.narrow(); j != nil && !create && order.work != &t.work {
		if _, whole := j.pending[order]; !whole && recordField(j, &order.work) != nil {
			return lo, false
		}
	}
	order.work = &t.work
	parent, left, pred, ok := order.search(n, t.chargedCompare(compare))
	if !ok || t.work.chargeHash(1+rbStructBound(len(order.nodes))) != nil {
		return lo, false
	}
	owner := l.At(0).parent
	at := lo
	if create {
		// publication of the sorted run, then the replays and n's link (insertPositionBound),
		// all before the run changes; n's slot follows from the published order
		if t.work.chargeHash(addHashBound(2*uint64(hi-lo), reorderReplayCost(l.Slice(lo, hi)))) != nil { //nolint:gosec // a slice length is nonnegative
			return lo, true
		}
		run := order.ordered()
		if pred != nil {
			at = lo + slices.Index(run, pred) + 1
		}
		amount, err := t.insertPositionBound(sib, n, insertDefault)
		if err != nil {
			return lo, true
		}
		link, err := t.linkCost(owner, sib, n, at)
		if err != nil {
			return lo, true
		}
		amount = addHashBound(amount, link)
		// the publication's index work (refreshRB per member, orderReorder) prepays inside
		// itself; reserve it with the rest, before the run changes
		amount = addHashBound(amount, sib.publicationIndexBound(l.Slice(lo, hi)))
		// the sibling-slice shift and copy of n's link, charged with the sorted insertion
		slots := linkSlots(sib, n, at)
		metaCost := addHashBound(sib.transferCost(leader, run[0]), sib.transferCost(run[0], n))
		if err := t.work.chargeHash(addHashBound(slots, addHashBound(metaCost, 8))); err != nil {
			return lo, true
		}
		if t.reserveHash(amount) != nil {
			return lo, true
		}
		if err := sib.touchWhole(); err != nil { // the first tree reorders the run
			return lo, false
		}
		if sib.rbGroups == nil {
			sib.rbGroups = map[*Node]*rbGroup{}
		}
		if sib.rbTree == nil {
			sib.rbTree = map[*Node]bool{}
		}
		if sib.rbOrders == nil {
			sib.rbOrders = map[*rbGroup]*dirtyOrder{}
		}
		sib.rbGroups[leader], sib.rbTree[leader], sib.rbOrders[group] = group, true, order
		t.applyRunReorderHash(sib, l.Slice(lo, hi), reorderExact|reorderFirst)
		if err := sib.replaceSchema(lo, hi, spanOf(run)); err != nil {
			return lo, false
		}
		l = sib.schemaSpan()
		sib.transferRBGroup(leader, l.At(lo), group)
		if sib.firstRun[leader.schema] == leader {
			sib.firstRun[leader.schema] = l.At(lo)
		}
		// lyds_create_metadata: the run's own new tree. An instance already in another retained
		// tree (a foreign leader's, joined before the children table gave it its own
		// first-instance record) keeps its node there too, as libyang's rb node does.
		for _, a := range l.Slice(lo, hi).All() {
			if err := a.touch(); err != nil {
				return lo, false
			}
			if old := a.rbGroup; a.inRB && old != nil && old != group && sib.rbOrders[old] != nil && sib.rbOrders[old].nodes[a] != nil {
				if sib.rbDual == nil {
					sib.rbDual, sib.rbDualCount = map[*Node][]*rbGroup{}, map[*rbGroup]int{}
				}
				sib.rbDual[a] = append(sib.rbDual[a], old)
				sib.rbDualCount[old]++
			}
			a.inRB, a.rbGroup = true, group
			if err := sib.refreshRB(a); err != nil { // the ordered index keys RB membership by tree
				return lo, false
			}
		}
		if sib.order != nil {
			if d := sib.order.entries[leader.schema]; d != nil && d.gaps == 0 {
				if err := sib.opRebuild(l.Slice(lo, hi)); err != nil { // only this run was reordered
					return lo, false
				}
			} else if err := sib.opBuild(t); err != nil { // retain other runs copied from a user-ordered schema
				return lo, false
			}
		}
		sib.rbOrders[group] = order
	}
	if !create {
		if pred != nil {
			i, err := t.chargedIndex(sib, pred)
			if err != nil {
				return lo, true
			}
			at = i + 1
		}
		if t.work.chargeHash(linkSlots(sib, n, at)) != nil {
			return lo, true
		}
		if pred == nil {
			if err := t.work.chargeHash(sib.transferCost(leader, n)); err != nil {
				return lo, false
			}
		}
		if amount, err := t.linkCost(owner, sib, n, at); err != nil || t.reserveHash(addHashBound(amount, 4)) != nil {
			return lo, true
		}
	}
	if err := sib.touchLazy(); err != nil {
		return lo, false
	}
	if err := n.touch(); err != nil {
		return lo, false
	}
	j := sib.narrow()
	if j != nil && j.recordPlace(order, n, parent) != nil {
		return lo, false
	}
	// lyds_insert frees the incoming first metadata slot (design 12, metadata slot order)
	if n.detachedRB == nil && len(n.carriedRB) > 0 {
		n.carriedRB = n.carriedRB[1:]
	}
	n.detachedRB, n.detachedGroup = nil, nil
	if err := sib.promoteRB(n, group, &t.work); err != nil {
		return lo, false
	}
	order.narrow = j // keep records what place changes
	order.place(n, parent, left)
	order.narrow = nil
	return at, true
}

// chargedIndex is the position of n in sib.schemaSpan(), scanned from both ends with the steps charged
// to the enforced quota.
func (t *Tree) chargedIndex(sib *siblings, n *Node) (int, error) {
	if sib.order != nil {
		// the ordered sequence's rank walk (a balanced tree): orderPosition's read credit reserves
		// it before it runs, rbHeight+1 units as before, not refunded (design 13 §4.4)
		at := sib.orderPosition(n)
		if err := sib.work.hashErr; err != nil {
			return -1, err
		}
		if at >= 0 {
			return at, nil
		}
	}
	// indexFromEnd's scan, each step charged before it reads its two slots
	l := sib.schemaSpan().nodes
	for i, j := 0, len(l)-1; i <= j; i, j = i+1, j-1 {
		if err := t.work.chargeHash(1); err != nil {
			return -1, err
		}
		t.work.Add(1)
		if l[j] == n {
			return j, nil
		}
		if l[i] == n {
			return i, nil
		}
	}
	return -1, nil
}

// linkCost is everything link reserves or charges for n at position at, so a caller that changes
// state first can reserve it in advance.
func (t *Tree) linkCost(parent *Node, sib *siblings, n *Node, at int) (uint64, error) {
	var amount uint64
	if n.schema != nil && !n.schema.IsKey() && parent != nil && parent.schema != nil &&
		parent.schema.Kind == schema.List && at < parent.lkeyN && parent.lkeyFrozen == nil {
		amount = uint64(parent.lkeyN) //nolint:gosec // a child count is nonnegative
	}
	bound, err := t.linkBound(parent, sib, n, at)
	return addHashBound(amount, bound), err
}

// mulHashBound multiplies two work bounds, saturating like addHashBound.
func mulHashBound(a, b uint64) uint64 {
	if hi, lo := bits.Mul64(a, b); hi == 0 {
		return lo
	}
	return ^uint64(0)
}

// linkSlots is the sibling-slice work link does for n at position at: the slots insertAt shifts
// (the shorter side) plus the copy of a reallocation it needs. rbPos charges it exactly before a
// sorted insertion changes anything.
func linkSlots(sib *siblings, n *Node, at int) uint64 {
	if n.schema == nil {
		return sib.opaqueInsertCost()
	}
	if at < 0 || at > sib.SchemaLen() {
		at = sib.SchemaLen()
	}
	return sib.insertCost(at)
}

// opRebuild moves the stable handles after one sibling run was sorted.
// Reordering the run keeps its boundaries, so gaps is unchanged.
func (s *siblings) opRebuild(run siblingSpan) error {
	s.gen++ // a physical reorder moves positions (lydCtx.position caches by gen)
	return s.orderReorder(run)
}

// runBounds is the run of n's schema node in sib.schemaSpan(), or the empty run where its first instance
// goes. It serves system-ordered runs only (insertPos): under an operation those are input data,
// placed by the anchor or after their RB predecessor and so contiguous by construction; output
// and notification lists are user-ordered (LYS_ORDBY_USER) and never get here, so an output run
// split by the kept input ([before, v1, v2, in, v3]) keeps its anchor order
// (TestParseOpDupKeepsOrder, ops/parse-json-reply-keep-order-split).
func (t *Tree) runBounds(sib *siblings, n *Node) (lo, hi int) {
	l := sib.schemaSpan()
	if !opChild(n.schema) && l.Len() > 0 && (len(sib.contexts) > 1 || !sameSet(ctxOf(l.At(0)), ctxOf(n))) {
		// lyd_find_sibling_schema can match a system-ordered run from another
		// context by schema name, before any schema-order lookup. Its instances
		// still take part in lyds_insert. With a children table the leader is the bucket's
		// first registered record (leaderFirstMixed).
		if lo = t.sortedLeaderIndex(sib, n); lo >= 0 {
			hi = lo
			for hi < l.Len() && schemaLookupMatch(l.At(hi).schema, n.schema, sib.ht != nil) {
				sib.visit()
				hi++
			}
			return lo, hi
		}
	}
	if opChild(n.schema) || sib.schemaUnordered {
		// Start at the native first-instance record. A copy from a user-ordered
		// schema can have several runs; lyds_insert uses only the selected one.
		leader := physicalStart(sib, n.schema, t.schemaLookupOperation(sib, n.schema))
		if leader == nil {
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
		if lo, hi, ok := sib.contiguousBounds(leader); ok {
			return lo, hi
		}
		// A copied user-ordered schema may have split runs. Native lyds_insert
		// selects one run and must walk it; charge those unavoidable visits.
		lo = sib.orderPosition(leader)
		if lo < 0 {
			if t.work.hashErr != nil {
				return 0, 0 // the read credit refused: no uncharged fallback scan
			}
			lo = l.indexFromEnd(&t.work, leader)
		}
		hi = lo
		for hi < l.Len() {
			sib.visit()
			if l.At(hi).schema != leader.schema {
				break
			}
			hi++
		}
		return lo, hi
	}
	lo = t.upper(l, 0, func(a *Node) bool { return t.sameOrAfter(a, n) })
	return lo, t.upper(l, lo, func(a *Node) bool { return t.after(a, n) })
}

// mixedSortedLookup reports whether lyd_find_sibling_schema may resolve n's run to a node of
// another context, as runBounds tests it: otherwise the leader is the run's own.
func mixedSortedLookup(sib *siblings, n *Node) bool {
	return !opChild(n.schema) && (len(sib.contexts) > 1 || !sameSet(ctxOf(sib.At(0)), ctxOf(n)))
}

// sortedLeaderIndex is the leader lyd_insert_node passes to lyds_insert, found by
// lyd_find_sibling_schema (tree_data.c:820-822) among siblings of several contexts: with a
// children table the bucket's first registered first-instance record (leaderFirstMixed), without
// one the first sibling matching by name. -1: none, or the probe's refused reservation
// (t.work.hashErr, which callers check first).
func (t *Tree) sortedLeaderIndex(sib *siblings, n *Node) int {
	if sib.SchemaLen() == 0 {
		return -1
	}
	if !mixedSortedLookup(sib, n) {
		// one context: the ordinary schema-order lookup, a binary search or a record lookup,
		// unless schemaIndex scans siblings of another context by name (foreignFor)
		bound := rbHeight(sib.SchemaLen()) + 1
		if sib.foreignFor(n.schema) {
			bound = uint64(sib.SchemaLen()) + 1 //nolint:gosec // a sibling count is nonnegative
		}
		if err := t.work.chargeHash(bound); err != nil {
			return -1
		}
		return t.schemaIndex(sib, n.schema)
	}
	// In the lookup mode insertPos switches to first (markMixed). The lookup is read-only: a
	// preflight probes that mode rather than entering it, so a refusal leaves sib as it was.
	if sib.ht == nil || !sib.schemaUnordered && !t.mixedSwitch(sib, sib.At(0).parent, n) {
		if sib.foreignFor(n.schema) && (t.work.lookupDepth == 0 || !t.foreignSchemaInput[sib]) {
			// schemaIndex's linear foreign-name scan, charged step by step: it stops at the
			// first match, so reserving every sibling per insertion made a run quadratic
			return t.foreignScan(sib, n.schema, true)
		}
		// the other lookups read up to every sibling: reserve that first
		if err := t.work.chargeHash(uint64(sib.SchemaLen()) + 1); err != nil { //nolint:gosec // a sibling count is nonnegative
			return -1
		}
		return t.schemaIndex(sib, n.schema)
	}
	// The record lookup reads every first-instance record: reserve it first. The position search
	// is charged step by step (chargedIndex), not reserved for every sibling: a reservation of the
	// whole set per insertion made a run of insertions quadratic.
	if err := t.work.chargeHash(uint64(len(sib.leaders)) + 1); err != nil {
		return -1
	}
	var leader *Node
	if sib.schemaUnordered {
		leader = sib.leaderFirstMixed(n.schema)
	} else {
		leader = sib.mixedLeaderProbe(n.schema, &t.work) // reserves its own sibling scan
	}
	if leader == nil {
		return -1
	}
	i, err := t.chargedIndex(sib, leader)
	if err != nil {
		return -1
	}
	return i
}

// switchMixed enters the lookup mode sortedLeaderIndex probes for n, as insertPos does first.
func (t *Tree) switchMixed(sib *siblings, n *Node) {
	if sib.SchemaLen() > 0 && mixedSortedLookup(sib, n) {
		t.markMixed(sib, sib.At(0).parent, n)
	}
}

// sortedLeader is the node sortedLeaderIndex selects (nil: none), with whether it carries an RB
// tree and whether that run has instances outside it: lyds_get_rb_tree of the native leader.
func (t *Tree) sortedLeader(sib *siblings, n *Node) (leader *Node, rb, unsorted bool) {
	if sib.SchemaLen() > 0 && !mixedSortedLookup(sib, n) {
		leader = sib.runLeader(n.schema) // one context: the run's own first-instance state
		return leader, sib.rbTree[leader], sib.unsorted[leader]
	}
	if i := t.sortedLeaderIndex(sib, n); i >= 0 {
		leader = sib.At(i)
		return leader, sib.rbTree[leader], sib.unsorted[leader]
	}
	return nil, false, false
}

// sortedRunBatchable reports whether Merge may batch n with the following copies (flushMerge):
// the native leader of n's run carries an RB tree with no instances outside it, and the leader
// stays the same for the whole batch. That holds only among siblings of one context; with
// several, an insertion can replace the first-instance record lyd_find_sibling_schema finds, so
// the next copy may select another tree, and each copy is inserted on its own.
func (t *Tree) sortedRunBatchable(sib *siblings, n *Node) bool {
	if sib.SchemaLen() == 0 || mixedSortedLookup(sib, n) {
		return false
	}
	_, rb, unsorted := t.sortedLeader(sib, n)
	return rb && !unsorted
}

// runEnd stops at the first different schema, even when another run follows later.
func (t *Tree) runEnd(sib *siblings, lo int) int {
	sn := sib.At(lo).schema
	if sib.order != nil {
		if d := sib.order.entries[sn]; d != nil && d.gaps == 0 {
			if _, hi, ok := sib.contiguousBounds(sib.At(lo)); ok {
				return hi
			}
		}
	}
	if !sib.schemaUnordered && !opChild(sn) {
		return t.upper(sib.schemaSpan(), lo, func(n *Node) bool { return n.schema != sn })
	}
	hi := lo
	for hi < sib.SchemaLen() {
		t.work.Add(1)
		if sib.At(hi).schema != sn {
			break
		}
		hi++
	}
	return hi
}

// retainedRunEnd is the physical end of a sorted run selected by lo. Equivalent schemas from
// distinct contexts share the same retained RB group even though their schema pointers differ.
func (t *Tree) retainedRunEnd(sib *siblings, lo int) int {
	if lo < 0 || lo >= sib.SchemaLen() {
		return lo
	}
	leader := sib.At(lo)
	group := leader.rbGroup
	if selected := sib.rbGroups[leader]; selected != nil {
		group = selected
	}
	hi := lo
	for hi < sib.SchemaLen() {
		n := sib.At(hi)
		if n.schema != leader.schema && (group == nil || !n.inRB || n.rbGroup != group) {
			break
		}
		t.work.Add(1)
		hi++
	}
	return hi
}

// markUnordered switches sib to the anchor and first-instance lookups of unordered siblings,
// before an insertion breaks schema order. Until then ordinary runs were contiguous, so their
// first-instance records are materialized first.
func (t *Tree) markUnordered(sib *siblings) {
	if sib.schemaUnordered {
		return
	}
	if err := t.touch(sib); err != nil {
		return
	}
	sib.schemaUnordered = true
	if sib.ht != nil {
		sib.leaderMaterialize(sib.logical())
	}
}

// climbOrder is lys_getnext(sn, sparent, NULL, 0) repeated (tree_schema.c:275-330) when sparent
// is not sn's schema parent: the schema siblings following sn at each level, climbing through
// its schema parents; past the top level the walk restarts at sparent's first child (children).
func climbOrder(sn, sparent *schema.Node, children []*schema.Node) []*schema.Node {
	var out []*schema.Node
	for cur := sn; ; {
		dp := cur.DataParent()
		var top []*schema.Node
		if dp == nil {
			top = cur.Module.Top
		}
		level := slices.Collect(schema.GetNext(dp, top, 0))
		if i := slices.Index(level, cur); i >= 0 {
			out = append(out, level[i+1:]...)
		}
		switch dp {
		case sparent:
			return out
		case nil:
			return append(out, children...)
		}
		cur = dp
	}
}

// markMixed marks sib unordered when n's schema parent is not lyd_node_schema(parent): a root
// that lyd_new_path_create put beside a nested parent, or its copy. Schema ranks of different
// parents do not compare, while lyd_insert_get_next_anchor walks such siblings like any other.
func (t *Tree) markMixed(sib *siblings, parent, n *Node) {
	if t.mixedSwitch(sib, parent, n) {
		t.markUnordered(sib)
	}
}

// mixedSwitch reports whether markMixed switches sib for n under parent.
func (t *Tree) mixedSwitch(sib *siblings, parent, n *Node) bool {
	if sib.schemaUnordered || parent == nil || n.schema == nil {
		return false
	}
	ps := parent.schema
	switch {
	case ps == nil:
		ps = nodeSchema(ctxOf(parent), parent)
	case ps.Kind == schema.AnyData || ps.Kind == schema.AnyXML:
		return false // a value tree's roots
	}
	// Siblings of another context: libyang's lookups follow the children table's registration
	// order (lyd_find_sibling_schema), which the first-instance records keep only once the
	// siblings are unordered; their runs were contiguous until now, so they are materialized.
	return !compareSchemaEqual(n.schema.DataParent(), ps, true) ||
		sib.SchemaLen() > 0 && !sameSet(ctxOf(sib.At(0)), ctxOf(n))
}

// switchJournal opens a journal of t, ended by the caller, when inserting n under parent switches
// the destination to the unordered lookup mode (markMixed: another context, or a parent of another
// schema) and no journal is running. The switch materializes first-instance records, in a
// preflight before any restorer is armed (insertPositionBound, preflightInsertAnchor); only a
// journal undoes it, so any failure of the insertion restores (rollback). A set switches once:
// the capture is paid once per set.
func (t *Tree) switchJournal(parent, n *Node) (*hashTransaction, error) {
	if t.work.journal != nil || n == nil || n.schema == nil {
		return nil, nil
	}
	sib := t.childrenOf(parent)
	if !t.mixedSwitch(sib, parent, n) && (sib.SchemaLen() == 0 || !mixedSortedLookup(sib, n) ||
		!t.mixedSwitch(sib, sib.At(0).parent, n)) {
		return nil, nil
	}
	j := beginHashTransaction(t)
	j.rollback = true
	return j, j.captureDetachedParent(parent)
}

// unorderedAnchor follows lyd_insert_get_next_anchor for ordinary siblings retained in source
// order. With a children table, choose the closest following schema with an instance; without
// one, walk the document and schema together. These differ on reordered children.
func (t *Tree) unorderedAnchor(sib *siblings, n *Node) int {
	dp := n.schema.DataParent()
	// The children_ht branch needs LYD_CTX(first_sibling) == LYD_CTX(new_node)
	// (tree_data.c:522-523); a foreign first sibling takes the linear walk.
	if sib.ht != nil && (sib.SchemaLen() == 0 || sameSet(ctxOf(sib.At(0)), ctxOf(n))) {
		order, from := t.opOrder(dp, false), t.schemaRank(n.schema)+1
		if sib.SchemaLen() > 0 && sib.At(0).parent != nil {
			if sp := sib.At(0).parent.schema; sp != nil && dp != sp {
				// A root beside a nested parent (lyd_new_path_create) or a node of another
				// context: lys_getnext(new_node->schema, sparent) does not start in sparent.
				order, from = climbOrder(n.schema, sp, t.opOrder(sp, false)), 0
			}
		}
		for r := from; r < len(order); r++ {
			t.work.Add(1)
			if leader := physicalStart(sib, order[r], t.schemaLookup(sib, order[r])); leader != nil {
				return sib.schemaSpan().indexFromEnd(&t.work, leader)
			}
		}
		return sib.SchemaLen()
	}
	var top []*schema.Node
	if dp == nil {
		top = n.schema.Module.Top
	}
	next, stop := iter.Pull(schema.GetNext(dp, top, 0))
	defer stop()
	sn, ok := next()
	if !ok {
		return sib.SchemaLen()
	}
	found := false
	started := dp != nil
	own := ownerModule(t.set, n)
	for i, a := range sib.SchemaAll() {
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
				return sib.SchemaLen()
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
	return sib.SchemaLen()
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

// opInst is the instances of sn in s.schemaSpan() for an operation or unordered copy. It only reads: the
// index is kept by the mutations alone (link, unlink, the RB sort), so concurrent readers of an
// unchanged tree share it safely.
func (s *siblings) opInst(sn *schema.Node) []*Node {
	if s.order == nil {
		// a sibling set never linked through link (sibsOf's singleton of a detached node):
		// no index, the few nodes are filtered
		var inst []*Node
		for _, a := range s.SchemaAll() {
			if s.work != nil {
				s.work.Add(1)
			}
			if a.schema == sn {
				inst = append(inst, a)
			}
		}
		return inst
	}
	return slices.Collect(s.allInstances(sn))
}

// schemaLookupOperation is lyd_find_sibling_schema for operation children and unordered copies. A (leaf-)list
// uses the schema-only first-instance chain while the table exists, including its observable
// absence. Other schema nodes use their ordinary schema-key bucket. Without a table libyang walks
// siblings and finds the first indexed instance.
func (t *Tree) schemaLookupOperation(s *siblings, sn *schema.Node) *Node {
	if s.ht == nil {
		return s.documentFirst(sn)
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
	if first := s.documentFirst(sn); first != nil {
		return first
	}
	return leader
}

// opRun is LYD_LIST_FOR_INST from the first instance: stop at the first other schema.
// The document-first accessor and physical-neighbor walk stay distinct from
// schemaLookupOperation, whose compatibility records have registration order.
func (t *Tree) opRun(s *siblings, sn *schema.Node) iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		leader := physicalStart(s, sn, t.schemaLookupOperation(s, sn))
		if leader == nil {
			return
		}
		if s.order != nil {
			for h := s.order.byNode[leader]; h != nil && h.Value.schema == sn; {
				next := s.order.seq.Next(h)
				if s.work != nil {
					s.work.Add(1)
				}
				if !yield(h.Value) {
					return
				}
				h = next
			}
			return
		}
		for _, n := range s.walkFrom(leader) {
			if n.schema != sn || !yield(n) {
				return
			}
		}
	}
}

// opNeighbours reads only the slots adjacent to the changed slot, before unlink or after link.
func (s *siblings) opNeighbours(at int) (left, right *schema.Node) {
	if at > 0 {
		left = s.At(at - 1).schema
	}
	if at+1 < s.SchemaLen() {
		right = s.At(at + 1).schema
	}
	return left, right
}

// opBuild (re)builds the instance index of an operation or unordered copy; mutations only.
func (s *siblings) opBuild(t *Tree) error {
	return s.orderBuild(t)
}

// opRemove drops n, being unlinked, from the instance index and returns whether its schema and
// any of its RB-tree members survive.
func (s *siblings) opRemove(n *Node, _ int) (survives, survivesInRB bool, err error) {
	if err := s.touchSticky(); err != nil {
		return false, false, err
	}
	if s.order == nil {
		return false, false, nil
	}
	survives, survivesInRB = s.orderRemove(n)
	return survives, survivesInRB, nil
}

// opOrderAdd records n, just placed at index at of s.schemaSpan(), in the ordered index.
func (s *siblings) opOrderAdd(t *Tree, n *Node, at int) error {
	if err := s.touchSticky(); err != nil {
		return err
	}
	return s.orderAddSchema(t, n, at)
}

// opAnchor is lyd_insert_get_next_anchor for an operation's child n: the index of the sibling n
// goes before, sib.SchemaLen() to append (before the opaque nodes).
//   - with the parent's children hash table: the first instance of the closest following schema
//     sibling in n's own input or output (lyd_find_sibling_schema: the instance index);
//   - without it (fewer than htMinItems children): the siblings walked against n's input or output
//     schema list, the anchor being the first sibling past n's schema node there; a sibling of the
//     other direction is never in that list, so an output node goes before the first input node
//     that does not precede it in the walk, and an input node before such an output node.
func (t *Tree) opAnchor(sib *siblings, n *Node) int {
	l := sib.schemaSpan()
	var opts schema.GetNextOpt
	if n.schema.InOutput() {
		opts = schema.GetNextOutput
	}
	order := t.opOrder(n.schema.DataParent(), opts == schema.GetNextOutput)
	if sib.ht != nil && sib.order != nil {
		// lyd_insert_get_next_anchor finds the closest following schema sibling that has an
		// instance. The occupied-rank summary skips absent schema siblings while preserving that
		// exact input/output-local search.
		d := 0
		if opts == schema.GetNextOutput {
			d = 1
		}
		next := func(r int) (int, bool) { // a class-A read: each step paid before it walks
			opened, err := sib.work.openRead(sib.order.ranks[d].ReadBound())
			if err != nil {
				return 0, false
			}
			r, ok := sib.order.ranks[d].Successor(r)
			sib.work.closeRead(opened)
			return r, ok
		}
		for r, ok := next(t.schemaRank(n.schema)); ok && r < len(order); r, ok = next(r) {
			leader := physicalStart(sib, order[r], t.schemaLookupOperation(sib, order[r]))
			if leader != nil {
				// The record, not the document-first instance, is the anchor. Its position is
				// its rank (schema nodes precede the opaque ones): a paid O(log n) read, not an
				// uncharged scan of the siblings (G10b).
				if at := sib.orderPosition(leader); at >= 0 || t.work.hashErr != nil {
					return max(at, 0) // a refused read credit is sticky: no fallback scan
				}
				return l.indexFromEnd(&t.work, leader)
			}
		}
		return l.Len()
	}
	own := ownerModule(ctxOf(n), n)
	k, found := 0, false
	for m, a := range l.All() {
		t.work.Add(1)
		if ownerModule(ctxOf(a), a) != own {
			return m // lyd_owner_module differs: the data of the next module
		}
		for !found {
			if k >= len(order) {
				return l.Len() // extension instance data: no anchor
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
	return l.Len()
}

// markRB records that the run of s has libyang's RB tree (created by its first sorted insertion
// into an existing run).
func markRB(sib *siblings, leader *Node) {
	if err := sib.touch(); err != nil {
		return
	}
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
	for i := sib.position(leader); i >= 0 && i < sib.SchemaLen() && sib.At(i).schema == leader.schema; i++ {
		if sib.work != nil {
			sib.work.Add(1)
		}
		if sib.At(i).inRB && sib.At(i).rbGroup == nil {
			if err := sib.At(i).touch(); err != nil {
				return
			}
			sib.At(i).rbGroup = group
		}
		if sib.refreshRB(sib.At(i)) != nil {
			return // sticky: the operation fails and its journal restores
		}
	}
}

// markUnsorted records that the run of s has instances outside its RB tree (appended by
// insertLast/insertLastBySchema after the tree was made).
func markUnsorted(sib *siblings, leader *Node) {
	if j := sib.narrow(); j != nil {
		if recordSlot(j, &sib.unsorted, leader) != nil {
			return
		}
	} else if err := sib.touch(); err != nil {
		return
	}
	if sib.unsorted == nil {
		sib.unsorted = map[*Node]bool{}
	}
	sib.unsorted[leader] = true
}

// insert is lyd_insert_node: link the unlinked node n under parent (nil: the top level of t).
// A list instance must have all its keys (libyang links it into its parent only then).
func (t *Tree) insert(parent, n *Node, order insertOrder) (err error) {
	journal, err := t.switchJournal(parent, n)
	if err == nil {
		err = t.insertNode(parent, n, order)
	}
	journal.end(&err)
	return err
}

func (t *Tree) insertNode(parent, n *Node, order insertOrder) (err error) {
	if n.parent != nil || n.tree != nil {
		panic("data: inserting a linked node") // native insertion requires an alone node
	}
	// Bind the still-unlinked subtree once. Repeating its charged planning after a sorted
	// publication wastes work and can otherwise consume the final link reservation.
	bound, err := bindSubtreeBound(n, &t.work)
	if err != nil {
		return err
	}
	if err := t.reserveHash(bound); err != nil {
		return err
	}
	old := childStorage(n).work
	if err := bindSubtreeWork(n, &t.work); err != nil {
		return err
	}
	defer func() {
		// A refused insertion without a journal gives the still-detached input its binding back.
		if err != nil && old != &t.work && t.work.journal == nil && n.parent == nil && n.tree == nil {
			unbindSubtreeWork(n, &t.work, old)
		}
	}()
	sib := t.childrenOf(parent)
	restoreDestination, err := t.bindDetachedDestination(parent, sib)
	if err != nil {
		return err
	}
	if restoreDestination != nil {
		defer func() {
			if err != nil && t.work.journal == nil {
				restoreDestination()
			}
		}()
	}
	if err := t.preflightInsertPosition(sib, n, order); err != nil {
		return err
	}
	at := -1 // opaque: appended to the opaque nodes
	if n.schema != nil {
		at = t.insertPos(sib, n, order)
	}
	return t.link(parent, sib, n, at)
}

// bindDetachedDestination binds a detached destination tree whole before insertPos: a sorted
// publication (rbPos) adds a graph to sib, which would raise link's binding bound past the
// reservation made before it. Without a journal, a refused insertion gives the tree its
// counter back through restore (as linkOrigin does for a detached origin).
func (t *Tree) bindDetachedDestination(parent *Node, sib *siblings) (restore func(), err error) {
	if sib.work == &t.work {
		return nil, nil
	}
	root, _, err := detachedRoot(parent, &t.work)
	if err != nil || root == nil {
		return nil, err
	}
	bound, err := bindSubtreeBound(root, &t.work)
	if err != nil {
		return nil, err
	}
	if err := t.reserveHash(bound); err != nil {
		return nil, err
	}
	was := childStorage(root).work
	restore = func() { unbindSubtreeWork(root, &t.work, was) }
	if err := bindSubtreeWork(root, &t.work); err != nil {
		if t.work.journal == nil {
			restore()
		}
		return nil, err
	}
	return restore, nil
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
// CompareCompatible accepts the closed D-0121 profile (R1-R3 included); an excluded pair is
// refused even when its native byte reinterpretation happens to be deterministic.
func (t *Tree) preflightSortedStorage(sib *siblings, n *Node, order insertOrder) error {
	// A refusal is sticky like the other insertion failures: batch callers read t.work.hashErr.
	err := t.sortedStorageRefusal(sib, n, order)
	if err != nil && t.work.hashErr == nil {
		t.work.hashErr = err
	}
	return err
}

func (t *Tree) sortedStorageRefusal(sib *siblings, n *Node, order insertOrder) error {
	return t.sortedStorageRefusalMode(sib, n, order, false)
}

func (t *Tree) sortedStorageRefusalMode(sib *siblings, n *Node, order insertOrder, local bool) error {
	if order != insertDefault || !sortedSupported(n) || sib.SchemaLen() == 0 {
		return nil
	}
	newCtx := ctxOf(n)
	foreign := ctxOf(sib.At(0)) != nil && ctxOf(sib.At(0)) != newCtx || len(sib.contexts) > 1
	if !foreign && sib.At(0).parent != nil && ctxOf(sib.At(0).parent) != nil &&
		ctxOf(sib.At(0).parent) != newCtx && t.mixedDiff {
		foreign = true
	}
	if !foreign && !local {
		return nil
	}
	lo := t.sortedLeaderIndex(sib, n) // the leader lyds_insert will use
	if lo < 0 {
		return nil
	}
	leader := sib.At(lo)
	if sib.rbTree[leader] {
		group := sib.rbGroups[leader]
		order := sib.rbOrders[group]
		if order == nil {
			return &opError{"LY_EINT", "Missing retained red-black topology for a system-ordered run."}
		}
		compatible, err := order.checkInsertCompatible(&t.work, n, t.compareSortedCompatible)
		if err != nil {
			return err
		}
		if !compatible {
			if t.work.hashErr != nil {
				return t.work.hashErr
			}
			return sortedInsertRefusal(sib, n)
		}
		return nil
	}
	// No tree yet: rb_insert_node descends the tree lyds_additionally_create_rb_tree is about to
	// build from the leader's run, so check exactly that path on a detached copy.
	detached := &dirtyOrder{work: &t.work}
	for i := lo; i < sib.SchemaLen() && sib.At(i).schema == leader.schema; i++ {
		if err := t.work.chargeHash(1); err != nil {
			return err
		}
		if _, ok := t.rbAdd(detached, sib.At(i), t.compareSortedCompatible); !ok {
			if t.work.hashErr != nil {
				return t.work.hashErr
			}
			return sortedInsertRefusal(sib, n)
		}
	}
	compatible, err := detached.checkInsertCompatible(&t.work, n, t.compareSortedCompatible)
	if err != nil {
		return err
	}
	if !compatible {
		if t.work.hashErr != nil {
			return t.work.hashErr
		}
		return sortedInsertRefusal(sib, n)
	}
	return nil
}

// insertPositionBound returns the native-record bound when insertPos replays
// registrations. Otherwise it also covers the following link (linkBound
// without a key, plus a displaced captured prefix), so a link that follows
// insertPos's visible changes cannot fail, its ordered-index edit included.
func (t *Tree) insertPositionBound(sib *siblings, n *Node, order insertOrder) (uint64, error) {
	if order != insertDefault || !sortedSupported(n) || sib.SchemaLen() == 0 {
		return 0, t.work.hashErr
	}
	if _, rb, _ := t.sortedLeader(sib, n); rb {
		return 0, t.work.hashErr
	}
	t.markMixed(sib, sib.At(0).parent, n)
	lo, hi := t.runBounds(sib, n)
	if t.work.hashErr != nil {
		return 0, t.work.hashErr
	}
	if lo == hi || sib.rbTree[sib.At(lo)] {
		return 0, nil
	}
	// insertPos now sorts the run and builds its RB state, replaying old
	// registrations of an indexed set, all before link. Reserve the link in the
	// same check (at any position: it is not known yet), including the child
	// table an unindexed set creates, so it cannot fail after those changes.
	var amount uint64
	if sib.ht != nil {
		for _, old := range sib.schemaSpan().Slice(lo+1, hi).All() {
			if !old.hashed {
				amount = addHashBound(amount, indexWorkBound(old, nil, nil, &t.work))
			}
		}
	}
	// link prepays its ordered-index edit (design 13 §4.2): at any position, and after the sort
	// may have built the index
	amount = addHashBound(amount, sib.orderAddBoundAny(n, sib.indexJournaled(&t.work)))
	parent := sib.At(0).parent
	if parent != nil && parent.schema != nil && (sib.ht != nil || sib.SchemaLen()+1 >= htMinItems) {
		amount = addHashBound(amount, indexWorkBound(n, nil, nil, &t.work))
		if sib.ht == nil {
			for _, old := range sib.SchemaAll() {
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
	t.markMixed(sib, parent, n)
	if opChild(n.schema) || sib.schemaUnordered {
		var entry *opEntry
		if sib.order != nil {
			entry = sib.order.entries[n.schema]
		}
		// The ordered index already maintains the instance count. Only the
		// singleton case needs its document-first node; materializing opInst here
		// made a sequence of detached Insert calls quadratic and allocated the
		// complete run on every call.
		if entry == nil || entry.count == 1 && sib.documentFirst(n.schema) == n {
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
	return t.link(anchor.parent, sib, n, sib.schemaSpan().Index(anchor))
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

// link puts the unlinked n at position at of sib.schemaSpan() (an opaque node: of sib.opaqueSpan(), appended when
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
	amount, err := t.linkApplyBound(parent, sib, n, at)
	if err != nil {
		return err
	}
	if n.detachedRB != nil {
		amount = addHashBound(amount, 3)
	}
	// The prepaid scope (design 13 §4.1-4.2): from here to the end of link no charge refuses.
	// It debits every charge of the suffix (linkApplyBound) and the index edit's point bound
	// now; journal captures stay fallible, and only exist where a journal restores them.
	amount = addHashBound(amount, sib.orderAddBound(n, at, sib.indexJournaled(&t.work)))
	nested, err := t.work.prepay(amount, false)
	if err != nil {
		return err
	}
	defer t.work.closeCredit(nested)
	if sib.work != &t.work || n.schema == nil || sib.narrow() == nil {
		if err := t.touch(sib); err != nil {
			return err
		}
	} else if err := sib.touchLazy(); err != nil {
		return err
	}
	if err := t.bindDestination(parent, sib); err != nil {
		return err
	}
	if n.detachedRB != nil {
		if err := t.work.chargeHash(3); err != nil {
			return err
		}
		if err := sib.touchWhole(); err != nil { // publication of a detached graph
			return err
		}
		if j := t.work.journal; j != nil {
			if err := j.captureNode(n); err != nil {
				return err
			}
			if err := j.captureOrder(n.detachedRB); err != nil {
				return err
			}
		}
		if sib.rbOrders == nil {
			sib.rbOrders = map[*rbGroup]*dirtyOrder{}
		}
		if sib.rbTree == nil {
			sib.rbTree = map[*Node]bool{}
		}
		if sib.rbGroups == nil {
			sib.rbGroups = map[*Node]*rbGroup{}
		}
		sib.rbOrders[n.detachedGroup] = n.detachedRB
		if len(n.carriedRB) == 0 {
			sib.rbTree[n], sib.rbGroups[n] = true, n.detachedGroup
		} else {
			// detachedRB is slot 0, ahead of the carried secondary slots (design 12, metadata
			// slot order): adoptCarried publishes all of them in that order.
			n.carriedRB = append([]rbCarry{{n.detachedGroup, n.detachedRB}}, n.carriedRB...)
		}
		n.inRB, n.rbGroup = true, n.detachedGroup
		n.detachedRB.work = &t.work
		n.detachedRB, n.detachedGroup = nil, nil
	}
	hadHT := sib.ht != nil
	switch {
	case n.schema == nil && (at < 0 || at >= sib.OpaqueLen()):
		at = sib.OpaqueLen()
		if err := sib.appendNode(n); err != nil {
			return err
		}
		if err := sib.orderAddOpaque(t, parent, n, at); err != nil {
			return err
		}
	case n.schema == nil:
		if _, err := sib.InsertAt(sib.SchemaLen()+at, n); err != nil {
			return err
		}
		if err := sib.orderAddOpaque(t, parent, n, at); err != nil {
			return err
		}
	default:
		first := sib.firstRun[n.schema] == nil || at < sib.SchemaLen() && sib.At(at) == sib.firstRun[n.schema]
		if j := sib.narrow(); j != nil && (sib.firstRun == nil || first) {
			if err := recordSlot(j, &sib.firstRun, n.schema); err != nil {
				return err
			}
		}
		if sib.firstRun == nil {
			sib.firstRun = make(map[*schema.Node]*Node)
		}
		if first {
			sib.firstRun[n.schema] = n
		}
		if at < sib.SchemaLen() && n.inRB && n.rbGroup != nil && sib.ownsGroup(sib.At(at), n.rbGroup) {
			sib.transferRBGroup(sib.At(at), n, n.rbGroup)
		}
		if !sib.schemaUnordered && !opChild(n.schema) &&
			(at > 0 && t.after(sib.At(at-1), n) || at < sib.SchemaLen() && t.after(n, sib.At(at))) {
			// LAST copies and whole-run moves may retain source schema order.
			t.markUnordered(sib)
		}
		t.markMixed(sib, parent, n) // a LAST copy appends without an anchor
		t.beforeKeyLink(parent, n, at)
		moved, err := sib.InsertAt(at, n)
		if err != nil {
			return err
		}
		if t.work.shifts {
			t.work.Add(int64(moved))
		}
		switch {
		case !opChild(n.schema) && !sib.schemaUnordered && sib.order == nil:
		case sib.order == nil:
			if err := sib.opBuild(t); err != nil { // the operation's first child (or the first since a bulk removal)
				return err
			}
		default:
			if err := sib.opOrderAdd(t, n, at); err != nil {
				return err
			}
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
	before := t.work.hashUsed
	amount, err := t.linkApplyBound(parent, sib, n, at)
	// link repeats this charged probe before applying its reserved work, and prepays the index
	// edit's point bound with it.
	amount = addHashBound(amount, sib.orderAddBound(n, at, sib.indexJournaled(&t.work)))
	return addHashBound(amount, t.work.hashUsed-before), err
}

func (t *Tree) linkApplyBound(parent *Node, sib *siblings, n *Node, at int) (uint64, error) {
	amount, err := rbAdoptionBound(sib, n, &t.work)
	if err != nil {
		return 0, err
	}
	bind, err := bindSubtreeBound(n, &t.work)
	if err != nil {
		return 0, err
	}
	amount = addHashBound(amount, bind)
	if sib.work != &t.work { // a set bound to t has its whole detached tree bound already
		root, steps, err := detachedRoot(parent, &t.work)
		if err != nil {
			return 0, err
		}
		// bindDestination repeats the ancestor walk
		amount = addHashBound(amount, steps)
		if root != nil {
			// bindDestination binds the whole detached destination tree
			if bind, err = bindSubtreeBound(root, &t.work); err != nil {
				return 0, err
			}
			amount = addHashBound(amount, bind)
		}
	}
	if n.schema != nil && n.schema.IsKey() && parent != nil {
		a, err := t.work.termHashBound(parent, n.schema, n.value, at)
		if err != nil {
			t.work.hashErr = err
			return 0, err
		}
		amount = addHashBound(amount, a)
	}
	if n.schema != nil && !n.schema.IsKey() && parent != nil && parent.schema != nil &&
		parent.schema.Kind == schema.List && (parent.keyN < len(parent.schema.Keys) || at < len(parent.schema.Keys)) {
		// keyLinked → reindexComparedParent: a non-key can enter the compared prefix (incomplete,
		// or made so by its position among the keys) and reindex the list in its own siblings
		// (a term the prepaid scope exposed)
		if ps := parent.siblingsOf(); ps != nil && ps.ht != nil {
			// the compared prefix after the link: the first len(Keys) positions with n at at
			amount = addHashBound(amount, indexInsertedBound(parent, n, at, &t.work))
		}
	}
	amount = addHashBound(amount, t.linkIndexBound(parent, sib, n, at))
	return amount - min(amount, prepaySkew), t.work.hashErr
}

// linkIndexBound bounds the destination discriminators of n at position at;
// immutable name components are cached and the bound itself spends no work.
func (t *Tree) linkIndexBound(parent *Node, sib *siblings, n *Node, at int) uint64 {
	var amount uint64
	if n.schema != nil && parent != nil && parent.schema != nil &&
		(sib.ht != nil || sib.SchemaLen()+1 >= htMinItems) {
		amount = indexWorkBound(n, nil, nil, &t.work)
		if sib.ht == nil {
			for _, old := range sib.SchemaAll() {
				amount = addHashBound(amount, indexWorkBound(old, nil, nil, &t.work))
			}
		}
	}
	if parent != nil && parent.schema != nil && parent.schema.Kind == schema.List &&
		(n.schema == nil || n.schema.IsKey() || parent.keyN < len(parent.schema.Keys)) {
		if ps := parent.siblingsOf(); ps != nil && ps.ht != nil {
			position := at
			if n.schema == nil {
				position = parent.kids.SchemaLen() + parent.kids.OpaqueLen()
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
	// A storage refusal of the destination's RB insertion comes before n leaves its origin.
	if err := t.preflightSortedStorage(sib, n, insertDefault); err != nil {
		return err
	}
	if n.siblingsOf() != sib {
		if err := t.sortedStorageRefusalMode(sib, n, insertDefault, true); err != nil {
			t.work.hashErr = err
			return err
		}
	}
	// One total: insertPos's replays, the source removal and the destination.
	amount, err := t.insertPositionBound(sib, n, insertDefault)
	if err != nil {
		return err
	}
	if n.schema != nil && parent != nil && parent.schema != nil &&
		(sib.ht != nil || sib.SchemaLen()+1 >= htMinItems) {
		amount = addHashBound(amount, indexWorkBound(n, nil, nil, &t.work))
		if sib.ht == nil {
			for _, old := range sib.SchemaAll() {
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
func (t *Tree) restoreLink(parent, n *Node, at int, allowance uint64) {
	// Rollback reattaches a node that already existed before the failed operation.
	// linkOrigin charged this finite recovery allowance before unlinking. Its use cannot spend
	// a failed operation's exhausted forward allowance or allocate unreserved rollback work.
	used, limit, err := t.work.hashUsed, t.work.hashLimit, t.work.hashErr
	t.work.hashUsed, t.work.hashLimit, t.work.hashErr = 0, allowance, nil
	if linkErr := t.link(parent, t.childrenOf(parent), n, at); linkErr != nil {
		panic("data: failed to restore a linked node")
	}
	t.work.hashUsed, t.work.hashLimit, t.work.hashErr = used, limit, err
}

// attach finishes linking n, already placed in sib's lists, under parent.
func (t *Tree) attach(parent *Node, sib *siblings, n *Node) {
	if err := n.touch(); err != nil {
		return
	}
	if n.parent != nil || n.tree != nil {
		panic("data: inserting a linked node") // internal invariant: callers unlink first
	}
	n.parent = parent
	if parent == nil {
		n.tree = t
	}
	if err := t.adoptCarried(sib, n); err != nil {
		t.work.hashErr = err
		return
	}
	if err := bindSubtreeWork(n, &t.work); err != nil {
		return
	}
	sib.addContext(n)
	if j := sib.narrow(); j != nil {
		if err := recordField(j, &sib.gen); err != nil {
			return
		}
	}
	sib.gen++
	sib.hashAdd(parent, n)
	if n.flags&FlagDefault == 0 {
		npContDfltDel(parent)
	}
	t.keyLinked(parent, n)
}

// detachedRoot is the root of the detached tree parent belongs to, nil for a linked parent (or
// none), with the ancestor steps it took. Each step is charged to w before it is taken: opaque
// nesting is not bounded by the schema depth.
func detachedRoot(parent *Node, w *workCounter) (root *Node, steps uint64, err error) {
	if parent == nil {
		return nil, 0, nil
	}
	root = parent
	for root.parent != nil {
		if err := w.chargeHash(1); err != nil {
			return nil, steps, err
		}
		if w.visits { // tests only
			w.Add(1)
		}
		root = root.parent
		steps++
	}
	if root.tree != nil {
		return nil, steps, nil
	}
	return root, steps, nil
}

// bindDestination binds sib, the children of parent, to t before a link publishes into it. A
// detached destination tree is bound whole (linkApplyBound reserves it, its ancestor walk
// included): every set below a set bound to a counter stays bound to it, which bindSubtreeWork's
// shortcut relies on.
func (t *Tree) bindDestination(parent *Node, sib *siblings) error {
	if sib.work == &t.work {
		return nil
	}
	root, _, err := detachedRoot(parent, &t.work)
	if err != nil {
		return err
	}
	if root != nil {
		if err := bindSubtreeWork(root, &t.work); err != nil {
			return err
		}
	}
	sib.work = &t.work
	return nil
}

// bindSubtreeWork binds the sibling sets of root's subtree, any payloads included, to work:
// the lazy journal finds its transaction through them (siblings.touch). Every binding covers a
// whole subtree, so a set already bound to work has its descendants bound too. A failed
// reservation stops before the set it was for changes its counter.
func bindSubtreeWork(root *Node, work *workCounter) error {
	if childStorage(root).work == work {
		return nil
	}
	if err := work.chargeHash(1); err != nil {
		return err
	}
	stack := []*Node{root}
	for len(stack) != 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		sib := childStorage(n)
		if sib.work == work {
			continue // bound with all its descendants already
		}
		if err := work.chargeHash(1 + uint64(sib.SchemaLen()+sib.OpaqueLen()+len(sib.rbOrders)+len(sib.pending))); err != nil { //nolint:gosec // sibling counts are nonnegative
			return err
		}
		if err := sib.rebind(work); err != nil {
			work.hashErr = err
			return err
		}
		sib.setWork(work)
		stack = sib.schemaSpan().AppendTo(stack)
		stack = sib.opaqueSpan().AppendTo(stack)
	}
	return nil
}

// unbindSubtreeWork undoes bindSubtreeWork(root, from) for a refused insertion with no journal.
// It revisits only the sets that binding charged for.
func unbindSubtreeWork(root *Node, from, to *workCounter) {
	stack := []*Node{root}
	for len(stack) != 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		sib := childStorage(n)
		if sib.work != from {
			continue
		}
		sib.setWork(to)
		stack = sib.schemaSpan().AppendTo(stack)
		stack = sib.opaqueSpan().AppendTo(stack)
	}
}

func childStorage(n *Node) *siblings {
	if n.any != nil {
		return &n.any.children
	}
	return &n.kids
}

// bindSubtreeBound reserves the binding and its journal entries before the link is visible.
// The probe's own traversal/stack slots are charged before allocating or visiting children.
func bindSubtreeBound(root *Node, work *workCounter) (uint64, error) {
	return subtreeBindingBound(root, work, false)
}

// force also budgets a future return from a temporary owner, whose bindings do not exist yet.
func subtreeBindingBound(root *Node, work *workCounter, force bool) (uint64, error) {
	if !force && childStorage(root).work == work {
		return 0, nil
	}
	if heldNode(root) {
		return 0, refuseIn(work.journal)
	}
	if err := work.chargeHash(1); err != nil {
		return 0, err
	}
	stack := []*Node{root}
	total := uint64(1)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		sib := childStorage(n)
		if !force && sib.work == work {
			continue
		}
		if heldSet(sib) { // a held binding below: refused before any set is rebound or lent
			return 0, refuseIn(work.journal)
		}
		children := uint64(sib.SchemaLen() + sib.OpaqueLen()) //nolint:gosec // sibling counts are nonnegative
		if err := work.chargeHash(1 + children); err != nil {
			return 0, err
		}
		total = addHashBound(total, 1+children+uint64(len(sib.rbOrders)+len(sib.pending)))
		if work.journal != nil {
			total = addHashBound(total, 2)
		}
		stack = sib.schemaSpan().AppendTo(stack)
		stack = sib.opaqueSpan().AppendTo(stack)
	}
	return total, nil
}

// preflightSplice reserves the destination's full batch of index digests before
// any member is attached.
func (t *Tree) preflightSplice(parent *Node, sib *siblings, ns []*Node) error {
	if t.work.hashErr != nil {
		return t.work.hashErr
	}
	if len(ns) == 0 {
		return nil
	}
	var amount uint64
	if parent != nil && parent.schema != nil {
		if sib.ht != nil || sib.SchemaLen()+len(ns) >= htMinItems {
			for _, n := range ns {
				amount = addHashBound(amount, indexWorkBound(n, nil, nil, &t.work))
			}
			if sib.ht == nil {
				for _, n := range sib.SchemaAll() {
					amount = addHashBound(amount, indexWorkBound(n, nil, nil, &t.work))
				}
			}
		}
		if ps := parent.siblingsOf(); ps != nil && ps.ht != nil && parent.schema.Kind == schema.List &&
			parent.keyN < len(parent.schema.Keys) {
			amount = addHashBound(amount, indexBatchInsertedBound(parent, ns, &t.work))
		}
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

// spliceNeedsOrder reports whether the post-splice sequence index must be
// rebuilt. The rebuild charges the ordinary work counter itself.
func spliceNeedsOrder(sib *siblings, ns []*Node) bool {
	if len(ns) == 0 || ns[0].schema == nil ||
		(sib.order == nil && !opChild(ns[0].schema) && !sib.schemaUnordered) {
		return false
	}
	return true
}

// rbReplay replays rb_insert of every node of replay, in order, into the detached topology d
// (lyd_insert_node per lyd_merge_sibling_r, lyds_merge_nodes2_front/_among, lyds_merge_nodes3's
// rb_insert_node), with the run's retained comparator, which also serves members from another
// context. Each comparison is charged its own cost before it runs and each insertion its actual
// rebalancing, so successive batches of one operation consume the quota cumulatively. A refusal
// leaves only d changed.
func (t *Tree) rbReplay(d *dirtyOrder, replay []*Node) error {
	compare := t.chargedCompare(t.compareSortedCompatible)
	for _, n := range replay {
		if err := t.work.chargeHash(1); err != nil { // lyds_create_node
			return err
		}
		if d.nodes[n] != nil {
			return rbUnlinkError(n)
		}
		d.work = &t.work
		parent, left, _, ok := d.search(n, compare)
		if !ok {
			if t.work.hashErr == nil {
				t.work.hashErr = crossContextStorageError(n)
			}
			return t.work.hashErr
		}
		before := t.work.Load()
		d.place(n, parent, left)
		// rb_insert_color's recolouring and rotations, as counted; amortized O(1) per insertion
		if err := t.work.chargeHash(uint64(t.work.Load()-before) + 1); err != nil { //nolint:gosec // the counter only grows
			return err
		}
	}
	return nil
}

// rbPublish links the unlinked nodes attach into the sorted run sib.schemaSpan().Slice(lo, hi) after rbReplay
// built its final topology d (installed under group). Every insertion lyds_link_data_node makes
// keeps the members in in-order, so the run is published once as d's in-order; the run has no
// members outside d. The nodes are then hashed in attach order (lyd_insert_hash), each against
// the siblings linked so far: the table libyang builds at the fourth child (tree_data_hash.c:
// 181-197) has their order, and a first-instance record follows the schema neighbours the node
// has at that moment (lyd_insert_hash_add, 139-160). Linking one by one would shift the list
// once per node (quadratic for a merged run).
func (t *Tree) rbPublish(parent *Node, sib *siblings, lo, hi int, group *rbGroup, d *dirtyOrder, attach []*Node) error {
	old := sib.schemaSpan()
	size := old.Len() - (hi - lo) + len(d.nodes)
	usize := uint64(size) //nolint:gosec // slice lengths are nonnegative
	pub := addHashBound(8*usize+8*uint64(len(d.nodes)), 8)
	sn := attach[0].schema
	canTable := sib.ht == nil && parent != nil && parent.schema != nil && !isAny(parent.schema)
	records := sib.firstRecordSchema(sn) && (sib.ht != nil || canTable)
	if canTable {
		pub = addHashBound(pub, mulHashBound(usize, uint64(min(htMinItems, len(attach)))))
	}
	if records {
		// ponytail: each record update scans to its linked neighbours, O(S) per node; only
		// operation children and copies outside schema order keep first-instance records
		pub = addHashBound(pub, mulHashBound(usize, uint64(len(attach))))
	}
	if err := t.work.chargeHash(pub); err != nil {
		return err
	}
	if err := t.preflightSplice(parent, sib, attach); err != nil {
		return err
	}
	// The run's members are its slots and the attached nodes; a node the tree shares with
	// another run (dual membership, rbPos) stays where that run has it.
	here := make(map[*Node]bool, hi-lo+len(attach))
	for _, n := range old.Slice(lo, hi).All() {
		here[n] = true
	}
	for _, n := range attach {
		here[n] = true
	}
	run := slices.DeleteFunc(d.ordered(), func(n *Node) bool { return !here[n] })
	metaCost := addHashBound(sib.transferCost(old.At(lo), run[0]), 8+4*uint64(len(sib.rbMore[old.At(lo)])))
	if err := t.work.chargeHash(metaCost); err != nil {
		return err
	}
	if err := t.touch(sib); err != nil {
		return err
	}
	if err := t.bindDestination(parent, sib); err != nil {
		return err
	}
	out := make([]*Node, 0, size)
	out = old.Slice(0, lo).AppendTo(out)
	out = append(out, run...)
	out = old.Slice(hi, old.Len()).AppendTo(out)
	oldLeader := old.At(lo)
	if err := sib.buildSchema(out); err != nil {
		return err
	}
	for _, n := range run {
		if err := n.touch(); err != nil {
			return err
		}
		if err := sib.promoteRB(n, group, &t.work); err != nil {
			return err
		}
	}
	if sib.rbOrders == nil {
		sib.rbOrders = make(map[*rbGroup]*dirtyOrder)
	}
	d.work = &t.work
	sib.rbOrders[group] = d
	if !sib.ownsGroup(oldLeader, group) {
		groups := append(sib.metaGroups(oldLeader), group)
		sib.setMetaGroups(oldLeader, groups)
		if len(groups) > 1 {
			markUnsorted(sib, oldLeader)
		}
	}

	if sib.firstRun == nil {
		sib.firstRun = make(map[*schema.Node]*Node)
	}
	if out[lo] != oldLeader {
		sib.transferRBGroup(oldLeader, out[lo], group)
		if sib.firstRun[sn] == oldLeader || sib.firstRun[sn] == nil {
			sib.firstRun[sn] = out[lo]
		}
	}
	if t.work.shifts {
		t.work.Add(int64(sib.SchemaLen()))
	}
	if spliceNeedsOrder(sib, attach) {
		if err := sib.opBuild(t); err != nil { // one counted pass, like the publication itself
			return err
		}
	}
	pending := make(map[*Node]bool, len(attach))
	for _, n := range attach {
		pending[n] = true
	}
	var at map[*Node]int
	if records {
		at = make(map[*Node]int, len(out))
		for i, x := range out {
			at[x] = i
		}
	}
	for _, n := range attach {
		tableBefore := sib.ht != nil
		delete(pending, n)
		if !tableBefore && canTable {
			prefix := make([]*Node, 0, htMinItems)
			for _, x := range out {
				if !pending[x] {
					prefix = append(prefix, x)
				}
			}
			if err := sib.exposeSchema(prefix); err != nil {
				return err
			}
		}
		t.attach(parent, sib, n)
		if err := sib.exposeSchema(out); err != nil {
			return err
		}
		if tableBefore && records {
			// the neighbours n has among the siblings linked so far
			var prev, next *Node
			for i := at[n] - 1; i >= 0; i-- {
				if !pending[out[i]] {
					prev = out[i]
					break
				}
			}
			for i := at[n] + 1; i < len(out); i++ {
				if !pending[out[i]] {
					next = out[i]
					break
				}
			}
			sib.leaderInsertBetween(n, prev, next)
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
				ls.leaderInsert(l, ls.schemaSpan().indexFromEnd(ls.work, l))
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
	return unlink(n)
}

// unlinkRetainingHistory is a relocation within the same sibling set. Native
// unlink removes one current-hash record; reinsertion can reveal an older one.
// Suspend the secondary index during the move, retaining all remaining native
// registrations for the following insertion (tree_data_new.c:1177-1194).
func unlinkRetainingHistory(n *Node) error {
	sib := n.siblingsOf()
	if sib == nil {
		return nil
	}
	if _, err := prepareUnlink(n); err != nil {
		sib.work.hashErr = err
		return err
	}
	sib.hashRemove(n)
	history := sib.history[n]
	err := unlink(n)
	if len(history) != 0 {
		sib.history[n] = history
	}
	return err
}

// afterRemove applies rb_remove_node's run-state rule after instances of sn have left s. The
// surviving flags may be computed while compacting a batch so unlinkAll does not rescan s once
// per affected schema node.
func (s *siblings) afterRemove(sn *schema.Node, survives bool) {
	if err := s.touch(); err != nil {
		return
	}
	if !survives {
		delete(s.firstRun, sn)
	}
}

// unlink is lyd_unlink (without the key check); n's own leafref links go
// (lyd_unlink_ignore_lyds). An error leaves n linked, or (a sticky budget error after the
// removal) fails the operation for its journal to restore; callers must not proceed with n.
func unlink(n *Node) error {
	sib := n.siblingsOf()
	if sib == nil {
		return nil
	}
	plan, err := prepareUnlink(n)
	if err != nil {
		sib.work.hashErr = err
		return err
	}
	defer func() { sib.work.unlinkPlan = nil }()
	// The prepaid scope of the removal (design 13 §4.1-4.2): the plan paid the sibling and RB
	// work above; the index removal and the parent list's reindex are paid here, and from here
	// to the end no charge refuses. Journal captures stay fallible.
	nested, err := sib.work.prepay(unlinkSuffixBound(sib, n), false)
	if err != nil {
		return err
	}
	defer sib.work.closeCredit(nested)
	if err := sib.touch(); err != nil {
		return err
	}
	if n.schema == nil {
		if err := freeLinks(n); err != nil {
			sib.work.hashErr = err
			return err
		}
		if i := plan.at; i >= 0 {
			sib.orderRemoveOpaque(n)
			moved, err := sib.DeleteAt(sib.SchemaLen() + i)
			if err != nil {
				return err
			}
			if sib.work.shifts {
				sib.work.Add(int64(moved))
			}
		}
		sib.runGone(n)
	} else if i := plan.at; i >= 0 {
		var prev, next *Node
		if i > 0 {
			prev = sib.At(i - 1)
		}
		if i+1 < sib.SchemaLen() {
			next = sib.At(i + 1)
		}
		if err := sib.removeRBNode(n, prev, next, sib.nativeTail(), nil); err != nil {
			sib.work.hashErr = err
			return err
		}
		if err := freeLinks(n); err != nil {
			sib.work.hashErr = err
			return err
		}
		if i > 0 && i+1 < sib.SchemaLen() && n.schema != sib.At(i-1).schema &&
			sib.At(i-1).schema == sib.At(i+1).schema {
			// Removing a separator joins physical runs, but their RB trees do not merge.
			left := i - 1
			for left > 0 && sib.At(left-1).schema == sib.At(left).schema {
				left--
			}
			if sib.rbTree[sib.At(left)] {
				markUnsorted(sib, sib.At(left))
			}
		}
		sameRetainedRun := func(next *Node) bool {
			return next != nil && (next.schema == n.schema ||
				n.inRB && n.rbGroup != nil && next.inRB && next.rbGroup == n.rbGroup)
		}
		if sib.firstRun[n.schema] == n {
			if i+1 < sib.SchemaLen() && sameRetainedRun(sib.At(i+1)) {
				sib.firstRun[n.schema] = sib.At(i + 1)
			} else {
				delete(sib.firstRun, n.schema)
			}
		}
		if n.isKey() {
			beforeKeyUnlink(n.parent, i, sib.work)
		}
		sib.leaderUnlink(n, i, true) // lyd_unlink_hash observes n while it is still linked
		survives := false
		if sib.order != nil {
			if survives, _, err = sib.opRemove(n, i); err != nil { // neighbours still surround n
				return err
			}
		}
		moved, err := sib.DeleteAt(i)
		if err != nil {
			return err
		}
		sib.dropContext(n)
		if sib.work.shifts {
			sib.work.Add(int64(moved))
		}
		if sib.order == nil {
			survives = sib.has(n.schema)
		}
		sib.afterRemove(n.schema, survives)
		// The last member of a tree can hand its leader state to a same-schema nonmember; an
		// emptied tree takes that state with it (O(1), unlike the bulk pass).
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
	return sib.work.hashErr
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
	work := n.kids.work
	if n.any != nil {
		work = n.any.children.work
	}
	if sib := n.siblingsOf(); sib != nil {
		work = sib.work
	}
	if work == nil {
		work = &workCounter{}
	}
	nodes, err := ownedNodes([]*Node{n}, work)
	if err != nil {
		return err
	}
	if err := reserveFreeLinks(nodes, work); err != nil {
		return err
	}
	if err := preflightUnlink(n); err != nil {
		return err
	}
	if err := unlink(n); err != nil {
		return err
	}
	cleaner := bulkLinkCleaner{work: work}
	for _, d := range nodes {
		cleaner.free(d)
		releaseOwnedRB(d)
	}
	n.detachedRB, n.detachedGroup = nil, nil
	return work.hashErr
}

// unlinkSuffixBound is what unlink's suffix charges beyond its plan: the ordered-index removal
// and the parent list's reindex after its compared prefix changes (preflightUnlink's check).
func unlinkSuffixBound(sib *siblings, n *Node) uint64 {
	amount := sib.orderRemoveBound(n, sib.indexJournaled(sib.work))
	parent := n.parent
	if parent == nil || parent.schema == nil || parent.schema.Kind != schema.List ||
		!n.isKey() && parent.keyN >= len(parent.schema.Keys) {
		return amount
	}
	if ps := parent.siblingsOf(); ps != nil && ps.ht != nil && ps.work != nil {
		amount = addHashBound(amount, indexRemovedBound(parent, n, ps.work))
	}
	return amount
}

// preflightUnlink reserves the only hash work a single detach can cause:
// reindexing the retained parent list after its compared prefix changes.
func preflightUnlink(n *Node) error {
	if err := chargeRBRemoval(n.siblingsOf(), n); err != nil {
		return err
	}
	if sib := n.siblingsOf(); sib != nil && sib.order != nil {
		// unlink prepays the index removal with the reindex below
		if err := sib.work.checkHashWork(unlinkSuffixBound(sib, n)); err != nil {
			sib.work.hashErr = err
			return err
		}
		return nil
	}
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

// chargeRBRemoval charges, before the unlink, the rb_remove work n's removal does in its own
// tree and in the earlier trees that still hold it (rbDual).
func chargeRBRemoval(sib *siblings, n *Node) error {
	if sib == nil || n == nil {
		return nil
	}
	_, err := prepareUnlink(n)
	return err
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
func (t *Tree) unlinkAllDefaults(ns, defaults []*Node) (result error) {
	for _, n := range ns {
		if err := unlinkCheck(n); err != nil {
			return err
		}
		// the batch's freeLinks: a counterpart governed by an operation waiting on user code
		// refuses the batch before its first removal (design 13 §2.4 (5))
		if err := linkGuard(n, &t.work); err != nil {
			return err
		}
	}
	if err := t.preflightUnlinkAll(ns); err != nil {
		return err
	}
	// Removal work is charged per node as the native sequence runs, so a budget failure can
	// come after earlier members left. Callers without a journal of their own (TrimXPath,
	// parser cleanup) get a batch-local one: a budget failure restores the whole batch, while
	// a D-0121 refusal publishes the completed prefix below. Outside a hash-work scope there is
	// no operation to restore (and opening one would reset its counter).
	if t.work.hashDepth != 0 {
		if j := beginHashTransaction(t); j != nil {
			defer j.end(&result)
		}
	}
	// One virtual link sequence per affected sibling set replays native unlink order and
	// intermediate metadata/hash-leader transitions. Physical storage is compacted once.
	type links struct {
		prev, next       map[*Node]*Node
		tail, opaqueTail *Node
	}
	sets := map[*siblings]*links{}
	for _, n := range ns {
		sib := n.siblingsOf()
		if sib == nil || sets[sib] != nil {
			continue
		}
		if err := t.work.chargeHash(8*(uint64(sib.SchemaLen())+uint64(sib.OpaqueLen())) + 1); err != nil { //nolint:gosec // sibling counts are nonnegative
			return err
		}
		if err := sib.touch(); err != nil {
			return err
		}
		l := &links{prev: map[*Node]*Node{}, next: map[*Node]*Node{}}
		if sib.SchemaLen() > 0 {
			l.tail = sib.At(sib.SchemaLen() - 1)
		}
		if sib.OpaqueLen() > 0 {
			l.opaqueTail = sib.opaqueAt(sib.OpaqueLen() - 1)
		}
		for _, nodes := range []siblingSpan{sib.schemaSpan(), sib.opaqueSpan()} {
			for i, c := range nodes.All() {
				if i > 0 {
					l.prev[c] = nodes.At(i - 1)
				}
				if i+1 < nodes.Len() {
					l.next[c] = nodes.At(i + 1)
				}
			}
		}
		sets[sib] = l
	}
	gone := map[*Node]bool{}
	parents := map[*Node]*Node{}
	// Even a refused native search publishes the already completed prefix. Budget errors
	// additionally restore the enclosing transaction at its public boundary.
	defer func() {
		for sib := range sets {
			if err := sib.deleteMatching(func(n *Node) bool { t.work.Add(1); return gone[n] },
				func(n *Node) bool { t.work.Add(1); return gone[n] }); err != nil {
				if result == nil {
					result = err
				}
				return
			}
			// Removing separators joins physical runs, never their independent retained trees.
			var leader *Node
			for _, n := range sib.SchemaAll() {
				t.work.Add(1)
				if leader == nil || leader.schema != n.schema {
					leader = n
				}
				if sib.rbTree[leader] && (!n.inRB || n.rbGroup != sib.rbGroups[leader]) {
					markUnsorted(sib, leader)
				}
			}
			if sib.order != nil {
				if err := sib.opBuild(t); err != nil {
					if result == nil {
						result = err
					}
					return
				}
			}
			sib.gen++
		}
		affectedLists := map[*Node]bool{}
		for n, parent := range parents {
			if parent != nil && !gone[parent] && parent.schema != nil && parent.schema.Kind == schema.List && gone[n] {
				affectedLists[parent] = true
			}
		}
		for parent := range affectedLists {
			oldKeyN := parent.keyN
			exposeListKeys(parent.kids.work, parent)
			if oldKeyN < len(parent.schema.Keys) || parent.keyN != oldKeyN {
				reindexList(parent)
			}
		}
		npContDfltSetAll(defaults)
		if result == nil {
			result = t.work.hashErr
		}
		if result != nil && !errors.Is(result, yang.ErrBudget) {
			// A refusal publishes the completed prefix, whose callers then return without their
			// subtree cleanup (lyd_free_tree's link part): finish it here. A budget failure
			// restores the batch instead.
			// The owned traversal and the reciprocal cleanup are reserved first (ownedNodes,
			// reserveFreeLinks, as discard does); a refused reservation turns the refusal into a
			// budget failure, which restores the batch.
			var roots []*Node
			for _, n := range ns {
				if gone[n] {
					roots = append(roots, n)
				}
			}
			nodes, err := ownedNodes(roots, &t.work)
			if err == nil {
				err = reserveFreeLinks(nodes, &t.work)
			}
			if err != nil {
				result = err
				return
			}
			cleaner := bulkLinkCleaner{work: &t.work}
			for _, d := range nodes {
				cleaner.free(d)
			}
		}
	}()
	for _, n := range ns {
		sib := n.siblingsOf()
		if sib == nil || gone[n] {
			continue
		}
		l := sets[sib]
		prev, next := l.prev[n], l.next[n]
		tail := l.tail
		if l.opaqueTail != nil {
			tail = l.opaqueTail
		}
		if err := sib.removeRBNode(n, prev, next, tail, func(c *Node) (*Node, *Node) { return l.prev[c], l.next[c] }); err != nil {
			return err
		}
		if err := freeLinks(n); err != nil {
			t.work.hashErr = err
			return err
		}
		if t.work.hashErr != nil {
			return t.work.hashErr
		}
		if n.schema != nil {
			sib.leaderUnlinkBetween(n, prev, next, true)
			sib.dropContext(n)
			if sib.firstRun[n.schema] == n {
				if next != nil && next.schema == n.schema {
					sib.firstRun[n.schema] = next
				} else {
					delete(sib.firstRun, n.schema)
				}
			}
		}
		sib.hashForget(n)
		sib.runGone(n)
		if prev != nil {
			l.next[prev] = next
		}
		if next != nil {
			l.prev[next] = prev
		}
		if l.tail == n {
			l.tail = prev
		}
		if l.opaqueTail == n {
			l.opaqueTail = prev
		}
		parents[n] = n.parent
		n.parent, n.tree, n.inRB, n.rbGroup = nil, nil, false, nil
		gone[n] = true
	}
	return t.work.hashErr
}

func (t *Tree) preflightUnlinkAll(ns []*Node) error {
	// The batch maps and compaction visit the removed nodes even when no
	// siblings survive to rebuild. Charge that source work before any mutation.
	if err := t.work.chargeHash(uint64(len(ns))); err != nil {
		return err
	}
	t.work.Add(int64(len(ns)))
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
			if err := p.touch(); err != nil {
				return
			}
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
		if err := p.touch(); err != nil {
			return
		}
		p.flags |= FlagDefault
	}
}

// npContDfltDel is lyd_np_cont_dflt_del: ancestors with an explicit descendant are not default.
func npContDfltDel(p *Node) {
	for ; p != nil && p.flags&FlagDefault != 0; p = p.parent {
		if err := p.touch(); err != nil {
			return
		}
		p.flags &^= FlagDefault
	}
}
