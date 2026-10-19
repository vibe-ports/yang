// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_find_sibling_first, lyd_find_sibling_schema,
// lyd_compare_single, lyd_compare_single_value, lyd_compare_siblings_) and src/tree_data_hash.c
// (lyd_hash, lyd_insert_hash, lyd_insert_hash_add, lyd_unlink_hash, lyd_hash_table_val_equal),
// src/hash_table.c (lyht_insert, lyht_remove, lyht_find) (BSD-3-Clause, © CESNET).

package data

import (
	"fmt"
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
// index: chains of instances per hash key, each chain in insertion order like lyht_find returns
// colliding records. It is written only by insertions and removals; lookups read.
type siblings struct {
	nodeDeque           // schema nodes, in order, with front headroom for insertAt
	opq         []*Node // opaque nodes, after all schema nodes (libyang keeps them last)
	ht          map[idxKey]*recordChain
	exactByNode map[*Node]*record
	// leaders are libyang's first-instance records. lyd_insert_hash_add
	// (tree_data_hash.c:128-163) stores every list or leaf-list instance that starts a run under
	// the schema-only hash, and lyd_find_sibling_schema (tree_data_common.c:1101-1115) returns the
	// first record in hash-chain insertion order; LYD_LIST_FOR_INST (tree_data.h:495) then walks
	// forward only to the first sibling of another schema node. lyht_insert appends records,
	// lyht_remove unlinks one, and lyht_find returns the first (hash_table.c).
	//
	// Creation records all run starts in sibling order. A later insertion that starts a run
	// removes its same-schema successor's record (and aborts without adding its own if that record
	// is absent), then appends its own. Unlink removes a run-start record and appends its
	// same-schema successor unless that successor already has a record. A record otherwise stays
	// put when its node ceases or starts being a run start. Value rehash is unlink then insert on
	// the linked node: a run start's record moves to the back, any other record stays where it is.
	// Building a missing sorted index relocates every non-running-maximum instance, moving its
	// exact record to the back and replacing the first-instance record whenever the run leader
	// changes. Missing/repeated first-instance transitions leave the records in libyang's final
	// state, but their internal LOGINT is not a data verdict (D-0118). Recursive duplication starts
	// with an empty table and inserts copies with LYD_INSERT_NODE_LAST, restoring sibling order.
	// Removed nodes are always dropped; libyang keeps the record of a removed node that no longer
	// started a run (D-0117).
	//
	// Outside operation children runs are contiguous and schemaIndex is their first instance, so
	// no record is kept. Operation children may be split around retained input, and the records
	// exist only while ht does; without a table lookup starts at the first sibling instance.
	leaders      map[*schema.Node]*recordChain
	leaderByNode map[*Node]*record
	unsorted     map[*schema.Node]bool        // runs appended out of value order
	pending      map[*schema.Node]*dirtyOrder // the parser's dirty runs: the sibling order is their tree's
	rbTree       map[*schema.Node]bool        // runs that have libyang's RB tree (lyds)
	opIdx        map[*schema.Node]*opEntry    // an operation's children by schema node (opInst), nil: not built
	opRanks      [2]rankBits                  // occupied input/output schema ranks, for the next anchor
	gen          uint64                       // changes with every insertion and removal
	work         *workCounter                 // the tree's work counter once a node was linked: all and indexOf count visits
}

type recordChain struct {
	first *record
	last  *record
	count int
}

type record struct {
	node       *Node
	chain      *recordChain
	prev, next *record
}

func recordAppend(c *recordChain, byNode map[*Node]*record, n *Node) bool {
	if byNode[n] != nil {
		return false
	}
	r := &record{node: n, chain: c, prev: c.last}
	if c.last == nil {
		c.first = r
	} else {
		c.last.next = r
	}
	c.last = r
	c.count++
	byNode[n] = r
	return true
}

func recordDrop(c *recordChain, byNode map[*Node]*record, n *Node) bool {
	r := byNode[n]
	if c == nil || r == nil || r.chain != c {
		return false
	}
	if r.prev == nil {
		c.first = r.next
	} else {
		r.prev.next = r.next
	}
	if r.next == nil {
		c.last = r.prev
	} else {
		r.next.prev = r.prev
	}
	c.count--
	delete(byNode, n)
	r.chain, r.prev, r.next = nil, nil, nil
	return true
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

// position caches one interior sibling position until the next insertion or removal. The
// generation in the key makes mutation invalidate it without any maintenance work.
func (s *siblings) position(n *Node) int {
	// No cache here: lookups run on read-only paths that concurrent readers share (doc.go), so
	// they must not write sibling state. The parser keeps its own cache (lydCtx.position).
	return indexFromEnd(s.work, s.list, n)
}

// has reports whether an instance of s is in the list.
func (s *siblings) has(sn *schema.Node) bool {
	for _, n := range s.list {
		s.visit()
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
	if parent == nil || parent.schema == nil || isAny(parent.schema) || n.schema == nil {
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
	s.ht = map[idxKey]*recordChain{}
	s.exactByNode = map[*Node]*record{}
	// lyd_insert_hash builds the table from the children in sibling order (tree_data_hash.c:
	// 181-197), which for a run the parser still defers is its tree order: libyang has linked
	// each instance at its sorted place.
	order := s.logical()
	for _, c := range order {
		s.hashPut(c)
	}
	s.leaderBuild(order)
}

// logical is the sibling order libyang has: s.list with each dirty run in its tree order, at
// the place of its first instance.
func (s *siblings) logical() []*Node {
	if len(s.pending) == 0 {
		return s.list
	}
	out := make([]*Node, 0, len(s.list))
	done := map[*schema.Node]bool{}
	for _, n := range s.list {
		if s.work != nil {
			s.work.Add(1)
		}
		ord := s.pending[n.schema]
		switch {
		case ord == nil:
			out = append(out, n)
		case !done[n.schema]:
			done[n.schema] = true
			out = append(out, ord.ordered()...)
		}
	}
	return out
}

func (s *siblings) hashPut(n *Node) {
	if k, ok := hashOf(n); ok {
		if s.work != nil {
			s.work.Add(1)
		}
		if s.exactByNode == nil {
			s.exactByNode = map[*Node]*record{}
		}
		if s.exactByNode[n] != nil {
			return
		}
		c := s.ht[k]
		if c == nil {
			c = &recordChain{}
			s.ht[k] = c
		}
		if recordAppend(c, s.exactByNode, n) {
			n.hkey, n.hashed = k, true
		}
	}
}

// hashRemove is lyd_unlink_hash.
func (s *siblings) hashRemove(n *Node) {
	if !n.hashed || s.ht == nil {
		return
	}
	if s.work != nil {
		s.work.Add(1)
	}
	c := s.ht[n.hkey]
	if !recordDrop(c, s.exactByNode, n) {
		return
	}
	if c.first == nil {
		delete(s.ht, n.hkey)
	}
	n.hashed = false
}

// bucket is a snapshot of an exact-value record chain in hash insertion order. It is for tests;
// production readers either use bucketNodes or inspect the first record directly.
func (s *siblings) bucket(k idxKey) []*Node {
	c := s.ht[k]
	if c == nil {
		return nil
	}
	out := make([]*Node, 0, c.count)
	for r := c.first; r != nil; r = r.next {
		out = append(out, r.node)
	}
	return out
}

func (s *siblings) bucketNodes(k idxKey) iter.Seq[*Node] {
	c := s.ht[k]
	return func(yield func(*Node) bool) {
		if c == nil {
			return
		}
		for r := c.first; r != nil; r = r.next {
			s.visit()
			if !yield(r.node) {
				return
			}
		}
	}
}

func firstRecordSchema(sn *schema.Node) bool {
	return sn != nil && (sn.Kind == schema.List || sn.Kind == schema.LeafList) && opChild(sn)
}

// leaderBuild is lyd_insert_hash_add's empty_ht branch: every run start is registered in sibling
// order. It is called only when the children table is created, never for a copied empty table.
func (s *siblings) leaderBuild(order []*Node) {
	s.leaders = map[*schema.Node]*recordChain{}
	s.leaderByNode = map[*Node]*record{}
	var prev *schema.Node
	for _, n := range order {
		if s.work != nil {
			s.work.Add(1)
		}
		if firstRecordSchema(n.schema) && n.schema != prev {
			s.leaderAppend(n.schema, n)
		}
		prev = n.schema
	}
}

func (s *siblings) leaderHas(sn *schema.Node, n *Node) bool {
	if s.work != nil {
		s.work.Add(1)
	}
	r := s.leaderByNode[n]
	return r != nil && r.chain == s.leaders[sn]
}

func (s *siblings) leaderAppend(sn *schema.Node, n *Node) bool {
	if s.work != nil {
		s.work.Add(1)
	}
	if s.leaders == nil {
		s.leaders = map[*schema.Node]*recordChain{}
	}
	if s.leaderByNode == nil {
		s.leaderByNode = map[*Node]*record{}
	}
	if s.leaderByNode[n] != nil {
		return false
	}
	c := s.leaders[sn]
	if c == nil {
		c = &recordChain{}
		s.leaders[sn] = c
	}
	return recordAppend(c, s.leaderByNode, n)
}

func (s *siblings) leaderDrop(sn *schema.Node, n *Node) bool {
	if s.work != nil {
		s.work.Add(1)
	}
	c := s.leaders[sn]
	if !recordDrop(c, s.leaderByNode, n) {
		return false
	}
	if c.first == nil {
		delete(s.leaders, sn)
	}
	return true
}

func (s *siblings) leaderFirst(sn *schema.Node) *Node {
	if s.work != nil {
		s.work.Add(1)
	}
	if c := s.leaders[sn]; c != nil && c.first != nil {
		return c.first.node
	}
	return nil
}

// records returns the first-instance record chain in hash insertion order. It is a snapshot for
// tests; production lookups use leaderFirst and do not materialize the chain.
func (s *siblings) records(sn *schema.Node) []*Node {
	c := s.leaders[sn]
	if c == nil {
		return nil
	}
	out := make([]*Node, 0)
	for r := c.first; r != nil; r = r.next {
		out = append(out, r.node)
	}
	return out
}

// checkRecords verifies the exact-value and first-instance record chain and node index
// invariants. It is for package tests only; production paths maintain the invariants without
// walking all siblings.
func (s *siblings) checkRecords() error {
	linked := make(map[*Node]bool, len(s.list))
	for _, n := range s.list {
		linked[n] = true
	}

	seenNodes := make(map[*Node]bool, len(s.leaderByNode))
	seenRecords := make(map[*record]bool, len(s.leaderByNode))
	seenChains := make(map[*recordChain]*schema.Node, len(s.leaders))
	for sn, c := range s.leaders {
		if c == nil {
			return fmt.Errorf("first-instance record chain for %v is nil", sn)
		}
		if other, ok := seenChains[c]; ok {
			return fmt.Errorf("first-instance record chain is shared by %v and %v", other, sn)
		}
		seenChains[c] = sn
		if c.first == nil || c.last == nil {
			return fmt.Errorf("empty or half-empty first-instance record chain for %v", sn)
		}
		if c.first.prev != nil || c.last.next != nil {
			return fmt.Errorf("inconsistent first/last links in first-instance record chain for %v", sn)
		}
		var prev *record
		count := 0
		for r := c.first; r != nil; r = r.next {
			count++
			if seenRecords[r] {
				return fmt.Errorf("cycle or shared entry in first-instance record chain for %v", sn)
			}
			seenRecords[r] = true
			if r.chain != c || r.prev != prev {
				return fmt.Errorf("inconsistent entry links in first-instance record chain for %v", sn)
			}
			if r.node == nil || r.node.schema != sn {
				return fmt.Errorf("wrong node in first-instance record chain for %v", sn)
			}
			if seenNodes[r.node] {
				return fmt.Errorf("duplicate first-instance record for node %p", r.node)
			}
			seenNodes[r.node] = true
			if !linked[r.node] || r.node.siblingsOf() != s {
				return fmt.Errorf("first-instance record for detached node %p", r.node)
			}
			if s.leaderByNode[r.node] != r {
				return fmt.Errorf("first-instance node index disagrees for node %p", r.node)
			}
			prev = r
		}
		if prev != c.last {
			return fmt.Errorf("last link disagrees in first-instance record chain for %v", sn)
		}
		if count != c.count {
			return fmt.Errorf("first-instance record chain for %v counts %d entries, want %d", sn, count, c.count)
		}
	}
	if len(seenRecords) != len(s.leaderByNode) {
		return fmt.Errorf("first-instance chain has %d entries, node index has %d", len(seenRecords), len(s.leaderByNode))
	}
	for n, r := range s.leaderByNode {
		if n == nil || r == nil || r.node != n || !seenRecords[r] {
			return fmt.Errorf("invalid first-instance node index entry %p", n)
		}
		if r.chain == nil || s.leaders[n.schema] != r.chain {
			return fmt.Errorf("first-instance node index has wrong chain for node %p", n)
		}
		if !linked[n] || n.siblingsOf() != s {
			return fmt.Errorf("first-instance node index contains detached node %p", n)
		}
	}

	exactNodes := make(map[*Node]bool, len(s.exactByNode))
	exactRecords := make(map[*record]bool, len(s.exactByNode))
	exactChains := make(map[*recordChain]idxKey, len(s.ht))
	for k, c := range s.ht {
		if c == nil {
			return fmt.Errorf("exact record chain for %v is nil", k)
		}
		if other, ok := exactChains[c]; ok {
			return fmt.Errorf("exact record chain is shared by %v and %v", other, k)
		}
		exactChains[c] = k
		if c.first == nil || c.last == nil {
			return fmt.Errorf("empty or half-empty exact record chain for %v", k)
		}
		if c.first.prev != nil || c.last.next != nil {
			return fmt.Errorf("inconsistent first/last links in exact record chain for %v", k)
		}
		var prev *record
		count := 0
		for r := c.first; r != nil; r = r.next {
			count++
			if exactRecords[r] {
				return fmt.Errorf("cycle or shared entry in exact record chain for %v", k)
			}
			exactRecords[r] = true
			if r.chain != c || r.prev != prev {
				return fmt.Errorf("inconsistent entry links in exact record chain for %v", k)
			}
			if r.node == nil || !r.node.hashed || r.node.hkey != k {
				return fmt.Errorf("wrong node in exact record chain for %v", k)
			}
			if exactNodes[r.node] {
				return fmt.Errorf("duplicate exact record for node %p", r.node)
			}
			exactNodes[r.node] = true
			if !linked[r.node] || r.node.siblingsOf() != s {
				return fmt.Errorf("exact record for detached node %p", r.node)
			}
			if s.exactByNode[r.node] != r {
				return fmt.Errorf("exact node index disagrees for node %p", r.node)
			}
			prev = r
		}
		if prev != c.last {
			return fmt.Errorf("last link disagrees in exact record chain for %v", k)
		}
		if count != c.count {
			return fmt.Errorf("exact record chain for %v counts %d entries, want %d", k, count, c.count)
		}
	}
	if len(exactRecords) != len(s.exactByNode) {
		return fmt.Errorf("exact chains have %d entries, node index has %d", len(exactRecords), len(s.exactByNode))
	}
	for n, r := range s.exactByNode {
		if n == nil || r == nil || r.node != n || !exactRecords[r] {
			return fmt.Errorf("invalid exact node index entry %p", n)
		}
		if r.chain == nil || s.ht[n.hkey] != r.chain || !n.hashed {
			return fmt.Errorf("exact node index has wrong chain for node %p", n)
		}
		if !linked[n] || n.siblingsOf() != s {
			return fmt.Errorf("exact node index contains detached node %p", n)
		}
	}
	for _, n := range s.list {
		_, hashable := hashOf(n)
		if n.hashed != exactNodes[n] || s.ht != nil && hashable != n.hashed {
			return fmt.Errorf("linked node %p hashable=%v hashed=%v exact-record=%v", n, hashable, n.hashed, exactNodes[n])
		}
	}
	return nil
}

// leaderInsert applies lyd_insert_hash_add to n already at position at.
func (s *siblings) leaderInsert(n *Node, at int) {
	if s.ht == nil || !firstRecordSchema(n.schema) || at < 0 || at >= len(s.list) ||
		at > 0 && s.list[at-1].schema == n.schema {
		return
	}
	if at+1 < len(s.list) && s.list[at+1].schema == n.schema {
		if !s.leaderReplace(n.schema, s.list[at+1], n) {
			return
		}
		return
	}
	s.leaderAppend(n.schema, n)
}

// leaderInsertBetween is leaderInsert with already-known neighbours.
func (s *siblings) leaderInsertBetween(n, prev, next *Node) {
	if s.ht == nil || !firstRecordSchema(n.schema) || prev != nil && prev.schema == n.schema {
		return
	}
	if next != nil && next.schema == n.schema {
		s.leaderReplace(n.schema, next, n)
		return
	}
	s.leaderAppend(n.schema, n)
}

// opRehashNeighbours gets the neighbours relevant to first-record rehashing in O(1) for a
// gap-free operation entry. Split entries retain the counted position scan below (#240).
func (s *siblings) opRehashNeighbours(n *Node) (prev, next *Node, ok bool) {
	if ord := s.pending[n.schema]; ord != nil {
		// a run the parser defers: its tree order is the sibling order libyang has
		if first := ord.first(); first != n {
			return first, nil, true
		}
		return nil, ord.successor(n), true
	}
	d := s.opIdx[n.schema]
	if d == nil || d.gaps != 0 || len(d.list) == 0 {
		return nil, nil, false
	}
	if d.list[0] != n {
		return d.list[0], nil, true // any same-schema predecessor proves n is not a run start
	}
	if len(d.list) > 1 {
		next = d.list[1]
	}
	return nil, next, true
}

// leaderReplace is lyd_insert_hash_add replacing the old first-instance record when a linked
// node becomes the run start. A missing old record leaves the new record uninserted; libyang also
// logs an internal error, which the port deliberately does not surface as a data verdict (D-0118).
func (s *siblings) leaderReplace(sn *schema.Node, old, n *Node) bool {
	if !s.leaderDrop(sn, old) {
		return false
	}
	return s.leaderAppend(sn, n)
}

// leaderUnlink applies lyd_unlink_hash while n is still at position at. removing is a real
// unlink (the node leaves its siblings); a value rehash (lyd_change_node_value: lyd_unlink_hash
// then lyd_insert_hash on the same linked node, tree_data_new.c:1177-1194) passes false.
func (s *siblings) leaderUnlink(n *Node, at int, removing bool) {
	if s.ht == nil || !firstRecordSchema(n.schema) || at < 0 || at >= len(s.list) {
		return
	}
	var prev, next *Node
	if at > 0 {
		prev = s.list[at-1]
	}
	if at+1 < len(s.list) {
		next = s.list[at+1]
	}
	s.leaderUnlinkBetween(n, prev, next, removing)
}

// leaderUnlinkBetween is lyd_unlink_hash's first-instance part (tree_data_hash.c:226-243): only a
// run start loses its record, and its same-schema successor gets one. A node that kept a record
// after it stopped starting a run keeps it through a rehash, as libyang does; when the node is
// removed the port drops that record, which libyang leaves dangling (D-0117).
func (s *siblings) leaderUnlinkBetween(n, prev, next *Node, removing bool) {
	if s.ht == nil || !firstRecordSchema(n.schema) {
		return
	}
	if prev != nil && prev.schema == n.schema {
		if removing {
			s.leaderDrop(n.schema, n) // D-0117
		}
		return
	}
	if !s.leaderDrop(n.schema, n) {
		return
	}
	if next == nil || next.schema != n.schema {
		return
	}
	if s.leaderHas(n.schema, next) {
		return
	}
	s.leaderAppend(n.schema, next)
}

// The production-caller audit below is intentionally kept beside the two accessors. Tests only
// assert these choices. "Hash" starts at lyd_find_sibling_schema/lyd_find_sibling_val's first
// record (tree_data_common.c:1013-1077), including LYD_LIST_FOR_INST (tree_data.h:482-485);
// "document" is a LY_LIST_FOR sibling walk.
//
// Accessor / Go caller                     libyang v5.8.6 function                 order
// schemaLookup:
//
//	move.go:293 moveNodes                   lyd_move_nodes_by_schema (tree_data.c:909,917) hash
//	diff.go:667 diffMergeDelete             lyd_diff_merge_delete                  hash
//	xpathnode.go:84 FirstChild              moveto_node_hash_child                 hash
//	edit.go:149 evalPartial                 ly_path_eval_partial                   hash
//	edit.go:307 newPath                     lyd_new_path_                          hash
//	edit.go:625 mergeSibling                lyd_merge_sibling_r                    hash
//	unres.go:578 pathEval                   ly_path_eval_partial                   hash
//	diff_gen.go:259 findMatch               lyd_diff_find_match                    hash
//	validate.go:426 mandatory               lyd_validate_mandatory                 hash
//	validate.go:516 uniqFindLeaf            lyd_val_uniq_find_leaf (validation.c:1220,1238) hash
//	defaults.go:202,208 newImplicit         lyd_new_implicit                       hash
//	diff_apply.go:209 diffInsert            lyd_diff_insert                        hash
//
// documentFirst:
//
//	parse_json.go:852 parseAttribute        lydjson_parse_attribute (parser_json.c:1057,1075) document
//
// schemaIndex (a position, preserving the lookup order for operation children):
//
//	edit.go:187 instances                   LYD_LIST_FOR_INST                      hash
//	edit.go:692 flushMerge                  lyd_merge_sibling_r                    hash
//	move.go:347,360 merge                   lyds_merge                             hash
//	parse_json.go:427 metadataFinish        lydjson_metadata_finish (parser_json.c:539,568) document
//	parse_json.go:854 parseAttribute        lydjson_meta_attr (parser_json.c:683,728) document
//	unres.go:589 pathEval                   ly_path_eval_partial                   hash
//	siblings.go:483,498 accessors           lyd_find_sibling_schema / LY_LIST_FOR  hash / document
//
// opRun:
//
//	edit.go:186 instances                   LYD_LIST_FOR_INST (tree_data.h:482)     hash
//	unres.go:585 pathEval                   ly_path_eval_partial                   hash
//
// instances:
//
//	defaults.go:140 getnextData             lys_getnext_data                       hash
//	siblings.go:551 findFirst               lyd_find_sibling_first                 hash
//	edit.go:152 evalPartial                 ly_path_eval_partial                   hash
//	edit.go:341 checkPosition               lyd_new_path_                          hash
//	edit.go:525 run                         lyd_dup_inst_next                      hash
//	diff_apply.go:227 diffInsert            lyd_diff_insert                        hash
//	validate.go:462 minmax                  lyd_validate_minmax                    hash
//	validate_new.go:160,166,176 autodel*    lyd_validate_autodel_*                hash
//	diff_gen.go:287 userordGet              lyd_diff_userord_get                   hash
//
// schemaLookupOperation (the former opLeader helper):
//
//	siblings.go:481 schemaLookup            lyd_find_sibling_schema                hash
//	siblings.go:512 schemaIndex             lyd_find_sibling_schema                hash
//	insert.go:506 opRun                     LYD_LIST_FOR_INST                      hash
//	insert.go:700 opAnchor                  lyd_insert_get_next_anchor (tree_data.c:468,493) hash
//
// schemaLookup is lyd_find_sibling_schema: its first-instance hash record, which need not be the
// first sibling instance for an operation child.
func (t *Tree) schemaLookup(s *siblings, sn *schema.Node) *Node {
	if opChild(sn) {
		return t.schemaLookupOperation(s, sn)
	}
	if i := t.schemaIndex(s, sn); i >= 0 {
		return s.list[i]
	}
	return nil
}

// documentFirst is the first instance in sibling order. Operation children use their mutation-
// maintained document-order index; ordinary schema runs start at schemaIndex.
func (t *Tree) documentFirst(s *siblings, sn *schema.Node) *Node {
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
// and run-only lookups use schemaLookup and opRun without a position scan.
func (t *Tree) schemaIndex(s *siblings, sn *schema.Node) int {
	if len(s.list) == 0 {
		return -1
	}
	if opChild(sn) {
		if leader := physicalStart(s, sn, t.schemaLookupOperation(s, sn)); leader != nil {
			return t.indexFromEnd(s.list, leader)
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
	if len(s.list) > 0 && s.list[0].schema != nil && target.schema != nil &&
		!compareSchemaEqual(s.list[0].schema.DataParent(), target.schema.DataParent(), true) {
		return nil // schema mismatch
	}
	dup := isDupInstList(target.schema)
	if target.schema != nil && s.ht != nil {
		if len(s.list) == 0 {
			// a table kept after its schema children left (lyd_unlink_hash never frees it) or
			// copied by a recursive dup: lyht_find misses, opaque nodes are not in it
			// (tree_data.c:3210-3237)
			return nil
		}
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
			for n := range t.instances(s, sn) {
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
		k.s = sn
		for n := range s.bucketNodes(k) {
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

// compareSingleChecked is lyd_compare_single_schema followed by the iterative
// lyd_compare_single_data/lyd_compare_siblings_ walk. Any payloads are always
// compared as sibling forests, irrespective of the outer FullRecursion option.
func compareSingleChecked(t *Tree, a, b *Node, o CompareOptions, parentsChecked bool) bool {
	if !compareSingleSchema(a, b, o.Opaque, parentsChecked) {
		return false
	}
	return compareDataStack(t, []compareFrame{{a: a, b: b, opts: o}})
}

type compareFrame struct {
	a, b *Node
	opts CompareOptions
}

// compareIndex is private to one sibling comparison. Payload roots have no
// persistent children_ht: index their instance values without changing that
// observable state. Keep sibling order and reuse the first match (never consume
// it), including when equal instances are repeated.
type compareIndex struct {
	buckets map[string][]*Node
	matched map[*Node]*Node
	exact   map[string][]*Node
	types   map[string]string
	mixed   map[string]bool
}

// compareInstanceKey uses lengths, not delimiters that may occur in values. The
// typed key also distinguishes union members with the same canonical spelling.
func compareInstanceKey(n *Node, typed, members bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d:%s", n.schema.Kind, n.schema.LogPath())
	value := func(v types.Value) {
		fmt.Fprintf(&b, "/%d:%s", len(v.Canonical()), v.Canonical())
		if typed {
			for {
				fmt.Fprintf(&b, ":%p", v.Type())
				if !members || v.Union() == nil {
					break
				}
				v, _ = v.Union().Member()
			}
		}
	}
	if n.schema.Kind == schema.LeafList {
		value(n.value)
	} else {
		for _, k := range n.kids.list {
			if !k.isKey() {
				break
			}
			value(k.value)
		}
	}
	return b.String()
}

func newCompareIndex(nodes []*Node) *compareIndex {
	x := &compareIndex{buckets: map[string][]*Node{}, matched: map[*Node]*Node{},
		exact: map[string][]*Node{}, types: map[string]string{}, mixed: map[string]bool{}}
	for _, n := range nodes {
		// The LHS decides whether lyd_compare_siblings_ looks up an instance.
		// lyd_find_sibling_first then searches every RHS instance, including one
		// whose schema is user-ordered in another context.
		if n.schema == nil || n.schema.Kind != schema.LeafList &&
			(n.schema.Kind != schema.List || n.schema.Keyless()) {
			continue
		}
		// Canonical text and type can still hide distinct stored values (for
		// example, dates with different timestamps). Keep every candidate.
		typed := compareInstanceKey(n, true, true)
		x.exact[typed] = append(x.exact[typed], n)
		key := compareInstanceKey(n, false, false)
		topTypes := compareInstanceKey(n, true, false)
		if old, ok := x.types[key]; ok && old != topTypes {
			x.mixed[key] = true
		} else {
			x.types[key] = topTypes
		}
		x.buckets[key] = append(x.buckets[key], n)
	}
	return x
}

func (x *compareIndex) first(t *Tree, s *siblings, current, target *Node) *Node {
	if current.schema != nil && target.schema != nil &&
		!compareSchemaEqual(current.schema.DataParent(), target.schema.DataParent(), true) {
		return nil // keep lyd_find_sibling_first's initial schema-parent check
	}
	if n, ok := x.matched[target]; ok {
		return n
	}
	canonical := compareInstanceKey(target, false, false)
	if !x.mixed[canonical] && x.types[canonical] == compareInstanceKey(target, true, false) {
		// One declared type per value position narrows the search to the
		// selected union member, while typed equality resolves collisions.
		key := compareInstanceKey(target, true, true)
		for _, n := range x.exact[key] {
			s.visit()
			if compareSingle(t, n, target, false) {
				x.matched[target] = n
				return n
			}
		}
		x.matched[target] = nil
		return nil
	}
	for _, n := range x.buckets[canonical] {
		s.visit()
		if compareSingle(t, n, target, false) {
			x.matched[target] = n
			return n
		}
	}
	x.matched[target] = nil
	return nil
}

// compareNeedsLookup is lyd_compare_siblings_' LHS-only selection. Unlike
// lyds_is_supported, it excludes state leaf-lists, which compare in sibling order.
func compareNeedsLookup(n *Node) bool {
	if n.schema == nil || n.schema.UserOrdered {
		return false
	}
	s := n.schema
	return s.Kind == schema.List && !s.Keyless() || s.Kind == schema.LeafList && s.Config
}

// compareSiblingFrames is lyd_compare_siblings_: pairwise schema comparison,
// system-ordered instance lookup, then full recursive data comparison. The work
// is queued instead of calling recursively across unbounded payload boundaries.
func compareSiblingFrames(t *Tree, stack []compareFrame, a, b *siblings, o CompareOptions) ([]compareFrame, bool) {
	if a.len() != b.len() {
		return stack, false
	}
	al, bl := a.nodes(), b.nodes()
	var index *compareIndex
	for i, n := range al {
		m := bl[i]
		if !compareSingleSchema(n, m, o.Opaque, true) {
			return stack, false
		}
		if compareNeedsLookup(n) {
			if b.ht == nil {
				if index == nil {
					index = newCompareIndex(bl)
				}
				m = index.first(t, b, m, n)
			} else {
				m = t.findFirst(b, n)
			}
			if m == nil {
				return stack, false
			}
		}
		o.FullRecursion = true
		stack = append(stack, compareFrame{n, m, o})
	}
	return stack, true
}

func compareDataStack(t *Tree, stack []compareFrame) bool {
	for len(stack) != 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		a, b, o := f.a, f.b, f.opts
		if !o.Opaque && !hashEqual(a, b) {
			return false
		}
		var ac, bc *siblings
		switch {
		case a.schema == nil || b.schema == nil:
			if !o.Opaque && (a.schema == nil) != (b.schema == nil) {
				return false
			}
			if a.schema == nil && b.schema == nil || a.isTerm() || b.isTerm() {
				if !compareNodeValue(a, b) {
					return false
				}
			}
			if o.FullRecursion {
				ac, bc = &a.kids, &b.kids
			}
		case a.isTerm():
			if o.Defaults && a.flags&FlagDefault != b.flags&FlagDefault {
				return false
			}
			if a.value.Canonical() != b.value.Canonical() {
				return false
			}
		case isAny(a.schema):
			// libyang checks the first operand's child and text independently.
			// A mixed first operand differs from a tree-only second operand;
			// a tree-only first operand compares only their children. D-0129:
			// an empty first operand must not dereference absent text.
			av, bv := a.any, b.any
			if av == nil {
				av = &anyValue{}
			}
			if bv == nil {
				bv = &anyValue{}
			}
			ach, bch := av.children.len() != 0, bv.children.len() != 0
			if !ach && av.text == nil && !bch && bv.text == nil {
				break
			}
			if ach && !bch || av.text != nil && bv.text == nil {
				return false
			}
			if ach {
				ac, bc = &a.any.children, &b.any.children
			} else if av.text == nil || bv.text == nil || *av.text != *bv.text {
				return false
			}
		case a.schema.Kind == schema.List && !o.FullRecursion:
			if a.schema.Keyless() {
				continue
			}
			for i := range a.schema.Keys {
				if i >= len(a.kids.list) || i >= len(b.kids.list) {
					if len(a.kids.list) != len(b.kids.list) {
						return false
					}
					break
				}
				x, y := a.kids.list[i], b.kids.list[i]
				if !compareSingleSchema(x, y, o.Opaque, true) {
					return false
				}
				stack = append(stack, compareFrame{x, y, o})
			}
		default:
			if o.FullRecursion {
				ac, bc = &a.kids, &b.kids
			}
		}
		if ac != nil {
			var ok bool
			if stack, ok = compareSiblingFrames(t, stack, ac, bc, o); !ok {
				return false
			}
		}
	}
	return true
}
