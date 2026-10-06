// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_find_sibling_first, lyd_find_sibling_val,
// lyd_find_sibling_schema, lyd_compare_single, lyd_compare_siblings_) and src/tree_data_hash.c
// (the children hash table, as an index) (BSD-3-Clause, © CESNET).

package data

import (
	"iter"
	"slices"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// siblings is an ordered sibling list with a lazily built index (libyang's children_ht): the
// instances per (schema node, canonical key text), the text of a list's keys or of a leaf-list
// value. Canonical text alone is not equality across union members, so hits are confirmed with
// types.Equal.
type siblings struct {
	list []*Node
	idx  map[idxKey][]*Node // nil until the first lookup; instances in insertion order
}

type idxKey struct {
	s   *schema.Node
	key string
}

func (s *siblings) all() iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		for _, n := range s.list {
			if !yield(n) {
				return
			}
		}
	}
}

// keyOf is the index key of n (lyd_hash): schema node plus the canonical text of a leaf-list
// value or of a list's keys; ok false for a list whose keys are not all present yet.
func keyOf(n *Node) (k idxKey, ok bool) {
	k.s = n.schema
	switch {
	case n.schema == nil:
		return k, false
	case n.schema.Kind == schema.LeafList:
		k.key = n.value.Canonical()
	case n.schema.Kind == schema.List && !n.schema.Keyless():
		var b strings.Builder
		for i := range n.schema.Keys {
			if i >= len(n.kids.list) || n.kids.list[i].schema != n.schema.Keys[i] {
				return k, false // lyd_insert_has_keys
			}
			b.WriteString(n.kids.list[i].value.Canonical())
			b.WriteByte(0)
		}
		k.key = b.String()
	}
	return k, true
}

func (s *siblings) added(n *Node) {
	if s.idx == nil {
		return
	}
	if k, ok := keyOf(n); ok {
		s.idx[k] = append(s.idx[k], n)
	}
}

func (s *siblings) removed(n *Node) {
	if s.idx == nil {
		return
	}
	if k, ok := keyOf(n); ok {
		s.idx[k] = slices.DeleteFunc(s.idx[k], func(o *Node) bool { return o == n })
		if len(s.idx[k]) == 0 {
			delete(s.idx, k)
		}
	}
}

func (s *siblings) index() map[idxKey][]*Node {
	if s.idx == nil {
		s.idx = map[idxKey][]*Node{}
		for _, n := range s.list {
			if k, ok := keyOf(n); ok {
				s.idx[k] = append(s.idx[k], n)
			}
		}
	}
	return s.idx
}

// findSchema is lyd_find_sibling_schema: the first instance of the schema node.
func (t *Tree) findSchema(s *siblings, sn *schema.Node) *Node {
	probe := &Node{schema: sn}
	if len(s.list) > 0 {
		probe.parent = s.list[0].parent // top level or not decides the module order rule
	}
	i, _ := slices.BinarySearchFunc(s.list, probe, func(a, n *Node) int { return boolCmp(t.sameOrAfter(a, n)) })
	if i < len(s.list) && s.list[i].schema == sn {
		return s.list[i]
	}
	return nil
}

// isDupInstList is lysc_is_dup_inst_list: keyless lists and state leaf-lists may have equal
// instances.
func isDupInstList(s *schema.Node) bool {
	return s != nil && (s.Kind == schema.List && s.Keyless() || s.Kind == schema.LeafList && !s.Config)
}

// findFirst is lyd_find_sibling_first: the first sibling equal to target (keys or value; the
// whole subtree for keyless lists and state leaf-lists). Among several equal instances the first
// in sibling order is returned.
// VERIFY(dup/first-match): libyang's hash table returns the head of its collision chain when the
// parent has a children hash table; equal instances are errors (duplicates) except in the
// dup-inst lists, which libyang scans in order like here.
func (t *Tree) findFirst(s *siblings, target *Node) *Node {
	if target.schema == nil || isDupInstList(target.schema) {
		var start int
		if target.schema != nil {
			if f := t.findSchema(s, target.schema); f != nil {
				start = slices.Index(s.list, f)
			} else {
				return nil
			}
		}
		for _, n := range s.list[start:] {
			if target.schema != nil && n.schema != target.schema {
				break
			}
			if compareSingle(t, n, target, true) {
				return n
			}
		}
		return nil
	}
	k, ok := keyOf(target)
	if !ok {
		return t.findSchema(s, target.schema) // no keys: the first instance
	}
	var first *Node
	for _, n := range s.index()[k] {
		if compareSingle(t, n, target, false) && (first == nil || before(s, n, first)) {
			first = n
		}
	}
	return first
}

// before reports whether a precedes b in s.
func before(s *siblings, a, b *Node) bool {
	for _, n := range s.list {
		switch n {
		case a:
			return true
		case b:
			return false
		}
	}
	return false
}

// compareSingle is lyd_compare_single for nodes of one context: equal schema and data; full
// compares whole subtrees (LYD_COMPARE_FULL_RECURSION), else lists only by their keys.
func compareSingle(t *Tree, a, b *Node, full bool) bool {
	if a.schema != b.schema {
		return false
	}
	switch {
	case a.schema == nil:
		if a.opaq.Name != b.opaq.Name || a.opaq.ModuleNS != b.opaq.ModuleNS || a.opaq.Value != b.opaq.Value {
			return false
		}
		return !full || compareSiblings(t, &a.kids, &b.kids)
	case a.isTerm():
		return types.Equal(a.value, b.value)
	case full:
		return compareSiblings(t, &a.kids, &b.kids)
	case a.schema.Kind == schema.List:
		if a.schema.Keyless() {
			return true
		}
		for i := range a.schema.Keys {
			if i >= len(a.kids.list) || i >= len(b.kids.list) {
				return len(a.kids.list) == len(b.kids.list)
			}
			if !compareSingle(t, a.kids.list[i], b.kids.list[i], false) {
				return false
			}
		}
	}
	return true
}

// compareSiblings is lyd_compare_siblings_ with full recursion: pairwise in order, except that a
// system-ordered keyed list or config leaf-list instance is looked up among b's siblings.
func compareSiblings(t *Tree, a, b *siblings) bool {
	if len(a.list) != len(b.list) {
		return false
	}
	for i, n := range a.list {
		m := b.list[i]
		if n.schema != m.schema {
			return false
		}
		if sortedSupported(n) && !isDupInstList(n.schema) {
			if m = t.findFirst(b, n); m == nil {
				return false
			}
		}
		if !compareSingle(t, n, m, true) {
			return false
		}
	}
	return true
}
