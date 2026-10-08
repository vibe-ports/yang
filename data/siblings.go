// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_find_sibling_first, lyd_find_sibling_schema,
// lyd_compare_single, lyd_compare_single_value, lyd_compare_siblings_) and src/tree_data_hash.c
// (lyd_hash, lyd_insert_hash, lyd_unlink_hash, lyd_hash_table_val_equal) (BSD-3-Clause,
// © CESNET).

package data

import (
	"iter"
	"slices"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// htMinItems is LYD_HT_MIN_ITEMS: an inner node gets its children hash table once it has this
// many schema children; it keeps it afterwards. The top level never has one.
const htMinItems = 4

// siblings is an ordered sibling list with libyang's children hash table (children_ht) as an
// index: buckets of instances per hash key, each bucket in insertion order like lyht_find
// returns colliding records. It is written only by insertions and removals; lookups read.
type siblings struct {
	list     []*Node // schema nodes, in order
	opq      []*Node // opaque nodes, after all schema nodes (libyang keeps them last)
	ht       map[idxKey][]*Node
	unsorted map[*schema.Node]bool // runs appended out of value order
	rbTree   map[*schema.Node]bool // runs that have libyang's RB tree (lyds)
	gen      uint64                // changes with every insertion and removal
	work     *int                  // the tree's work counter once a node was linked: all and indexOf count visits
}

// visit counts one sibling visit in the tree's work counter.
func (s *siblings) visit() {
	if s.work != nil {
		*s.work++
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
func (s *siblings) hashAdd(parent, n *Node) {
	if parent == nil || parent.schema == nil || n.schema == nil {
		return
	}
	if s.ht != nil {
		s.hashPut(n)
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
	if i := t.schemaIndex(s, sn); i >= 0 {
		return s.list[i]
	}
	return nil
}

// schemaIndex is the index of the first instance of sn in s, -1 if there is none.
func (t *Tree) schemaIndex(s *siblings, sn *schema.Node) int {
	if len(s.list) == 0 {
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
	return s != nil && (s.Kind == schema.List && s.Keyless() || s.Kind == schema.LeafList && !s.Config)
}

// findFirst is lyd_find_sibling_first. With a children hash table: the first record of the
// target's bucket that lyd_hash_table_val_equal accepts (instances by their keys or value,
// anything else by schema node alone), in table insertion order; dup-inst lists are scanned from
// their first instance comparing whole subtrees. Without a table: the first sibling equal to the
// target (lyd_compare_single, whole subtrees for dup-inst lists).
// ponytail: keyless lists compare whole subtrees per instance (O(n·subtree), libyang parity);
// parsers and validation never search them, Merge/diff must run under a step budget.
func (t *Tree) findFirst(s *siblings, target *Node) *Node {
	if s.len() == 0 {
		return nil
	}
	if len(s.list) > 0 && target.schema != nil &&
		!compareSchemaEqual(s.list[0].schema.DataParent(), target.schema.DataParent(), true) {
		return nil // schema mismatch
	}
	dup := isDupInstList(target.schema)
	if target.schema != nil && s.ht != nil {
		// the target's schema node in the siblings' context (libyang hashes module and node
		// names, so a target of another context finds its instances)
		sn := target.schema
		if first := s.list[0]; !sameSet(setOf(first), setOf(target)) {
			var err error
			if sn, err = findSchemaCtx(target.schema, setOf(first), first.parent); err != nil {
				return nil
			}
		}
		if dup {
			i := t.schemaIndex(s, sn)
			if i < 0 {
				return nil
			}
			for _, n := range s.list[i:] {
				if n.schema != sn {
					break
				}
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
		k.s = sn
		for _, n := range s.ht[k] {
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
	return compareSchemaEqual(a.schema, b.schema, true)
}

// hashEqual is the hash comparison of lyd_compare_single_data: lyd_hash covers the schema node
// and the LYB form of a leaf-list value or of the list keys, which types.Equal stands for; values
// of two contexts (types of different sets) by their canonical text, which lyd_hash hashes.
func hashEqual(a, b *Node) bool {
	eq := func(x, y types.Value) bool {
		if x.Type() != y.Type() {
			return x.Canonical() == y.Canonical()
		}
		return types.Equal(x, y)
	}
	switch {
	case a.schema == nil:
		return true // opaque nodes are not hashed
	case a.schema.Kind == schema.LeafList:
		return eq(a.value, b.value)
	case a.schema.Kind == schema.List && !a.schema.Keyless():
		for i := range a.schema.Keys {
			if i >= len(a.kids.list) || i >= len(b.kids.list) {
				return len(a.kids.list) == len(b.kids.list)
			}
			if !eq(a.kids.list[i].value, b.kids.list[i].value) {
				return false
			}
		}
	}
	return true
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
// is not ported). Nodes of two contexts compare by their schema nodes' names (compareSingleSchema).
func compareSingleChecked(t *Tree, a, b *Node, o CompareOptions, parentsChecked bool) bool {
	return compareSingleSchema(a, b, o.Opaque, parentsChecked) && compareSingleData(t, a, b, o)
}

// compareSingleData is lyd_compare_single_data (any nodes come with M5).
func compareSingleData(t *Tree, a, b *Node, o CompareOptions) bool {
	if !o.Opaque && !hashEqual(a, b) {
		return false
	}
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
