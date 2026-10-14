// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_find_sibling_first, lyd_find_sibling_schema,
// lyd_compare_single, lyd_compare_single_value, lyd_compare_siblings_), src/tree_data_hash.c
// (lyd_hash, lyd_insert_hash, lyd_unlink_hash, lyd_hash_table_val_equal), and
// src/plugins_types/integer.c (lyplg_type_print_u_int), and src/plugins_types/union.c
// (lyplg_type_print_union) (BSD-3-Clause, © CESNET).

package data

import (
	"iter"
	"slices"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
)

// htMinItems is LYD_HT_MIN_ITEMS: an inner node gets its children hash table once it has this
// many schema children; it keeps it afterwards. The top level never has one.
const htMinItems = 4

// siblings is an ordered sibling list with libyang's children hash table (children_ht) as an
// index: buckets of instances per hash key, each bucket in insertion order like lyht_find
// returns colliding records. It is written only by insertions and removals; lookups read.
type siblings struct {
	nodeDeque         // schema nodes, in order, with front headroom for insertAt
	opq       []*Node // opaque nodes, after all schema nodes (libyang keeps them last)
	ht        map[idxKey][]*Node
	unsorted  map[*schema.Node]bool     // runs appended out of value order
	rbTree    map[*schema.Node]bool     // runs that have libyang's RB tree (lyds)
	opIdx     map[*schema.Node]*opEntry // an operation's children by schema node (opInst), nil: not built
	opRanks   [2]rankBits               // occupied input/output schema ranks, for the next anchor
	gen       uint64                    // changes with every insertion and removal
	work      *workCounter              // the tree's work counter once a node was linked: all and indexOf count visits
}

// visit counts one sibling visit in the tree's work counter.
func (s *siblings) visit() {
	if s.work != nil && s.work.visits { // tests only
		s.work.Add(1)
	}
}

// indexOf is the position of n in l, one of s's lists, -1 if absent. It scans from the end: the
// XML parser's opaque anchor is the last opaque sibling of its name, usually the last one.
// ponytail: O(distance from the end); a node→index map if anchors far from the end matter.
func (s *siblings) indexOf(l []*Node, n *Node) int {
	for i := len(l) - 1; i >= 0; i-- {
		s.visit()
		if l[i] == n {
			return i
		}
	}
	return -1
}

// has reports whether an instance of s is in the list.
func (s *siblings) has(sn *schema.Node) bool {
	for _, n := range s.list {
		if n.schema == sn {
			return true
		}
	}
	return false
}

// runGone forgets the run state of s once its last instance left (the RB tree goes with it).
func (s *siblings) runGone(sn *schema.Node) {
	delete(s.rbTree, sn)
	delete(s.unsorted, sn)
}

// len is the number of siblings, opaque ones included.
func (s *siblings) len() int { return len(s.list) + len(s.opq) }

// idxKey stands for lyd_hash: the schema node, plus the canonical text of a leaf-list value or
// of a list's keys. libyang hashes the LYB form of the values; buckets of equal canonical text
// are filtered by compareSingle, which checks value equality like the hash comparison does.
type idxKey struct {
	s   *schema.Node
	key string
}

func (s *siblings) all() iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		for _, n := range s.list {
			s.visit()
			if !yield(n) {
				return
			}
		}
		for _, n := range s.opq {
			s.visit()
			if !yield(n) {
				return
			}
		}
	}
}

// hashOf is lyd_hash: ok false for an opaque node and for a list whose keys are not all
// present (libyang hashes and links a list only then).
func hashOf(n *Node) (k idxKey, ok bool) {
	k.s = n.schema
	switch {
	case n.schema == nil:
		return k, false
	case n.schema.Kind == schema.LeafList:
		k.key = n.value.Canonical()
	case n.schema.Kind == schema.List && !n.schema.Keyless():
		var b strings.Builder
		for i, sk := range n.schema.Keys {
			if i >= len(n.kids.list) || n.kids.list[i].schema != sk {
				return k, false
			}
			b.WriteString(n.kids.list[i].value.Canonical())
			b.WriteByte(0)
		}
		k.key = b.String()
	}
	return k, true
}

// hashAdd is lyd_insert_hash: create the parent's table once it has htMinItems schema children
// (from all children, in order), else add n to the existing one.
//
// Whether a parent has the table is state of its own, observable through the order of an rpc's
// or action's children (lyd_insert_get_next_anchor, tree_data.c:522: with the table the anchor
// search, without it the walk), so it is kept in siblings.ht and never derived from the child
// count. libyang v5.8.6 lifecycle:
//   - created by lyd_insert_hash (tree_data_hash.c:181-197) when, after an insertion, the parent
//     (an inner schema node, not anydata/anyxml) has at least LYD_HT_MIN_ITEMS schema children;
//   - never freed while the parent lives: lyd_unlink_hash (tree_data_hash.c:207-245) only
//     removes entries, so a table outlives a shrink below the threshold (lyd_free_tree frees it
//     with its node only, tree_data_free.c:236);
//   - copied by lyd_dup_r with LYD_DUP_RECURSIVE (tree_data.c:2300-2305), empty, before the
//     children are copied into it (dupR), so a copy of a shrunk parent has the table; a
//     non-recursive copy (keys only) gets it only by reaching the threshold; lyd_merge copies
//     source subtrees with lyd_dup_single recursive and so the same way;
//   - a node moved to another parent keeps its own children (and their table).
//
// Every reader checks ht != nil: opAnchor, findFirst, the XPath and validation lookups.
// Fixtures ops/ht-* (create, none, shrink, grow after shrink, dup of a shrunk, of a small and
// non-recursive copy, each followed by an output insertion) pin it.
func (s *siblings) hashAdd(parent, n *Node) {
	if parent == nil || parent.schema == nil || n.schema == nil {
		return
	}
	if s.ht != nil {
		if !n.hashed { // a splice publishes its batch first: the table built for one has the rest
			s.hashPut(n)
		}
		return
	}
	if len(s.list) < htMinItems {
		return
	}
	s.ht = map[idxKey][]*Node{}
	for _, c := range s.list {
		s.hashPut(c)
	}
}

func (s *siblings) hashPut(n *Node) {
	if k, ok := hashOf(n); ok {
		n.hkey, n.hashed = k, true
		s.ht[k] = append(s.ht[k], n)
	}
}

// hashRemove is lyd_unlink_hash.
func (s *siblings) hashRemove(n *Node) {
	if !n.hashed || s.ht == nil {
		return
	}
	b := slices.DeleteFunc(s.ht[n.hkey], func(o *Node) bool { return o == n })
	if len(b) == 0 {
		delete(s.ht, n.hkey)
	} else {
		s.ht[n.hkey] = b
	}
	n.hashed = false
}

// findSchema is lyd_find_sibling_schema: the first instance of the schema node.
func (t *Tree) findSchema(s *siblings, sn *schema.Node) *Node {
	if opChild(sn) {
		if inst := s.opInst(sn); len(inst) > 0 {
			return inst[0]
		}
		return nil
	}
	if i := t.schemaIndex(s, sn); i >= 0 {
		return s.list[i]
	}
	return nil
}

// schemaIndex is the index of the first instance of sn in s, -1 if there is none.
// Operation callers must need a position for a mutation or metadata walk; first-instance
// and run-only lookups use findSchema and opRun without a position scan.
func (t *Tree) schemaIndex(s *siblings, sn *schema.Node) int {
	if len(s.list) == 0 {
		return -1
	}
	if opChild(sn) {
		if inst := s.opInst(sn); len(inst) > 0 {
			return t.opFirstFrom(s.list, inst)
		}
		return -1
	}
	probe := &Node{schema: sn, parent: s.list[0].parent} // top level or not decides the module order
	i, _ := slices.BinarySearchFunc(s.list, probe, func(a, n *Node) int {
		if t.sameOrAfter(a, n) {
			return 1
		}
		return -1
	})
	if i < len(s.list) && s.list[i].schema == sn {
		return i
	}
	return -1
}

// isDupInstList is lysc_is_dup_inst_list: keyless lists and state leaf-lists may have equal
// instances.
func isDupInstList(s *schema.Node) bool {
	return s != nil && s.IsDupInstList()
}

// findFirst is lyd_find_sibling_first. A target from another context keeps its original values and
// subtree; only its schema is mapped to the siblings' context for indexed lookup. With a children
// hash table: the first record of the target's bucket that lyd_hash_table_val_equal accepts
// (instances by their keys or value, anything else by schema node alone), in table insertion
// order; dup-inst lists are scanned from their first instance comparing whole subtrees. Without a
// table: the first sibling equal to the target (lyd_compare_single, whole subtrees for dup-inst
// lists).
// ponytail: keyless lists compare whole subtrees per instance (O(n·subtree), libyang parity);
// parsers and validation never search them, Merge/diff must run under a step budget.
func (t *Tree) findFirst(s *siblings, target *Node) *Node {
	if s.len() == 0 {
		return nil
	}
	var first *Node
	if len(s.list) > 0 {
		first = s.list[0]
	} else {
		first = s.opq[0]
	}
	cross := !sameSet(setOf(first), setOf(target))
	lookupSchema := target.schema
	if cross && target.schema != nil {
		var err error
		lookupSchema, err = findSchemaCtx(target.schema, setOf(first), first.parent)
		if err != nil {
			return nil
		}
	}
	if len(s.list) > 0 && target.schema != nil {
		a, b := s.list[0].schema.DataParent(), target.schema.DataParent()
		if (!cross && a != b) || (cross && !diffSchemaEqual(a, b)) {
			return nil // schema mismatch
		}
	}
	dup := isDupInstList(lookupSchema)
	if target.schema != nil && s.ht != nil {
		if len(s.list) == 0 {
			// a table kept after its schema children left (lyd_unlink_hash never frees it) or
			// copied by a recursive dup: lyht_find misses, opaque nodes are not in it
			// (tree_data.c:3210-3237)
			return nil
		}
		if dup {
			for n := range t.instances(s, lookupSchema) {
				s.visit()
				if compareSingle(t, n, target, true) {
					return n
				}
			}
			return nil
		}
		k, ok := hashOf(target)
		if !ok {
			return nil
		}
		k.s = lookupSchema
		for _, n := range s.ht[k] {
			s.visit()
			if htValEqual(t, n, target) {
				return n
			}
		}
		return nil
	}
	for n := range s.all() {
		if compareSingle(t, n, target, dup) {
			return n
		}
	}
	return nil
}

// htValEqual is lyd_hash_table_val_equal: lists and leaf-lists by their instance, other nodes
// by schema node only (equal schema nodes and parents across contexts).
func htValEqual(t *Tree, a, b *Node) bool {
	if a.schema.Kind == schema.List || a.schema.Kind == schema.LeafList {
		return compareSingle(t, a, b, false)
	}
	return compareSingleSchema(a, b, false, false)
}

// canonicalStoredValue is lyd_get_value for a stored schema value. In particular, it retains
// module names in identityrefs and instance-identifiers; local JSON spelling is not comparable
// across contexts.
func canonicalStoredValue(n *Node) string {
	if n.schema == nil {
		return n.Value()
	}
	return n.value.Canonical()
}

// hashEqual is the 32-bit node-hash comparison of lyd_compare_single_data. Equal hashes are only
// a gate: compareSingleData still compares canonical values explicitly because collisions are
// permitted by libyang.
func hashEqual(t *Tree, a, b *Node) bool {
	if lydHashInputsEqual(t, a, b) {
		return true
	}
	return differingHashInputsEqual(t, a, b)
}

func differingHashInputsEqual(t *Tree, a, b *Node) bool {
	ah, aok := lydHash(t, a)
	bh, bok := lydHash(t, b)
	return aok && bok && ah == bh
}

// compareSingle is lyd_compare_single; full is LYD_COMPARE_FULL_RECURSION (Node.Equal for all
// the options).
func compareSingle(t *Tree, a, b *Node, full bool) bool {
	return compareSingleChecked(t, a, b, CompareOptions{FullRecursion: full}, false)
}

// compareSingleChecked is lyd_compare_single_schema with its parental_schemas_checked, then
// lyd_compare_single_data. Values compare by canonical text (lyd_compare_single_value: a plain
// leaf's union "1" as int and as string are equal, VERIFY(cmp/union-leaf-text)); opaque nodes by
// value only, not by name (VERIFY(cmp/opaque-value-only); the XML prefix rewrite of opaque values
// is not ported). Nodes of two contexts compare by complete schema ancestry (compareSingleSchema).
func compareSingleChecked(t *Tree, a, b *Node, o CompareOptions, parentsChecked bool) bool {
	return compareSingleSchema(a, b, o.Opaque, parentsChecked) && compareSingleData(t, a, b, o)
}

// compareSingleData is lyd_compare_single_data (any nodes come with M5).
func compareSingleData(t *Tree, a, b *Node, o CompareOptions) bool {
	deferSparseHash := false
	if !o.Opaque && !lydHashInputsEqual(t, a, b) {
		deferSparseHash = lydHashHasSparseBits(t, a) || lydHashHasSparseBits(t, b)
		if !deferSparseHash && !differingHashInputsEqual(t, a, b) {
			return false
		}
	}

	equal := func() bool {
		if a.schema == nil || b.schema == nil {
			if !o.Opaque && (a.schema == nil) != (b.schema == nil) {
				return false
			}
			// compare values only if there are any to compare
			if a.schema == nil && b.schema == nil || a.isTerm() || b.isTerm() {
				if a.Value() != b.Value() {
					return false
				}
			}
			return !o.FullRecursion || compareSiblings(t, &a.kids, &b.kids, o)
		}
		switch a.schema.Kind {
		case schema.Leaf, schema.LeafList:
			if o.Defaults && a.flags&FlagDefault != b.flags&FlagDefault {
				return false
			}
			if a.schema != b.schema {
				return canonicalStoredValue(a) == canonicalStoredValue(b)
			}
			return a.value.Canonical() == b.value.Canonical()
		case schema.List:
			if o.FullRecursion {
				return compareSiblings(t, &a.kids, &b.kids, o)
			}
			if a.schema.Keyless() {
				return true
			}
			for i := range a.schema.Keys {
				if i >= len(a.kids.list) || i >= len(b.kids.list) {
					return len(a.kids.list) == len(b.kids.list)
				}
				if !compareSingleChecked(t, a.kids.list[i], b.kids.list[i], o, true) {
					return false
				}
			}
			return true
		}
		// container, rpc, action, notification: an implicit container equals one with explicit
		// descendants
		return !o.FullRecursion || compareSiblings(t, &a.kids, &b.kids, o)
	}()
	if !equal {
		return false
	}
	if deferSparseHash {
		// bits.c:328 allocates the whole bitmap and tree_data_hash.c:61-74 hashes it once when
		// libyang inserts a node. This residual walk is only for canonically equal values whose
		// LYB payloads differ (for example, one bit name at different positions in two contexts);
		// the port generates the bytes without allocating the bitmap.
		return differingHashInputsEqual(t, a, b)
	}
	return true
}

// compareSiblings is lyd_compare_siblings_: pairwise in order, except that a system-ordered
// keyed list or config leaf-list instance is looked up among b's siblings.
func compareSiblings(t *Tree, a, b *siblings, o CompareOptions) bool {
	al, bl := slices.Concat(a.list, a.opq), slices.Concat(b.list, b.opq)
	i := 0
	for ; i < len(al) && i < len(bl); i++ {
		n, m := al[i], bl[i]
		if !compareSingleSchema(n, m, o.Opaque, true) {
			return false
		}
		if sortedSupported(n) && !isDupInstList(n.schema) {
			if m = t.findFirst(b, n); m == nil {
				return false
			}
		}
		o.FullRecursion = true
		if !compareSingleData(t, n, m, o) {
			return false
		}
	}
	return i == len(al) && i == len(bl)
}
