// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_find_sibling_first, lyd_find_sibling_schema,
// lyd_compare_single, lyd_compare_single_value, lyd_compare_siblings_) and src/tree_data_hash.c
// (lyd_hash, lyd_insert_hash, lyd_insert_hash_add, lyd_unlink_hash, lyd_hash_table_val_equal),
// src/hash_table.c (lyht_insert, lyht_remove, lyht_find) (BSD-3-Clause, © CESNET).

package data

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
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
// colliding records. Mutations maintain the index; lookups only read it.
//
// List invariant: every active instance's bucket describes its CURRENT first
// len(schema.Keys) children, positionally (schema, stored child hash and canonical
// value, including an early end), as tree_data.c:1758-1785 compares them. Its own
// stored hash instead follows native HISTORY (tree_data_hash.c:45-81), and its
// record order follows native registration, never canonical bucket order. History
// groups retain all recorded hashes and orders; the secondary index contains the
// earliest record whose recorded hash equals the node's current stored hash.
// In particular a failed lyd_insert_has_keys (tree_data.c:684-702,836-842), an
// internal key unlink (1187-1239), or union validation changes only the canonical
// discriminator: hashReindex preserves hash, exact order and first-instance
// records. Successful key insertion, a changed key value, and duplication follow
// their native hash/registration events (836-842; tree_data_new.c:1129-1197;
// tree_data.c:2280-2330). All child links go through keyLinked; unlink updates the
// discriminator after detach, and value validation uses hashReindex directly.
type siblings struct {
	nodeDeque           // schema nodes, in order, with front headroom for insertAt
	opq         []*Node // opaque nodes, after all schema nodes (libyang keeps them last)
	ht          map[idxKey]*recordChain
	exactByNode map[*Node]*record
	// history retains unchecked native insertions, grouped by recorded hash.
	// Only the oldest registration for the node's CURRENT hash can win a lookup;
	// the others stay dormant until removal or a later return to that hash.
	history    map[*Node]map[uint32]*record
	exactOrder uint64 // native insertion order, retained by secondary-index-only moves
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
	// Schema-ordered ordinary children have contiguous runs, so no record is kept. Operation
	// children and copies outside schema order may have split runs, and the records
	// exist only while ht does; without a table lookup starts at the first sibling instance.
	leaders      map[*schema.Node]*recordChain
	leaderByNode map[*Node]*record
	firstRun     map[*schema.Node]*Node       // lookup hint only; sorting state stays on the leader
	unsorted     map[*Node]bool               // run leaders with instances outside their RB tree
	pending      map[*schema.Node]*dirtyOrder // the parser's dirty runs: the sibling order is their tree's
	rbTree       map[*Node]bool               // libyang's lyds metadata lives on each run leader
	rbGroups     map[*Node]*rbGroup           // tree identity follows metadata when its leader moves
	rbMore       map[*Node][]*rbGroup         // ordered additional metadata; rbGroups selects the first
	rbOrders     map[*rbGroup]*dirtyOrder     // retained native RB shape for asymmetric cross-context comparisons
	order        *childOrder                  // ordered sequence and occurrence indexes; compatibility records stay above
	gen          uint64                       // changes with every insertion and removal
	work         *workCounter                 // the tree's work counter once a node was linked: all and indexOf count visits
	// rbDual lists the earlier trees that still hold a node besides its own (rbPos: a run that
	// starts a new tree keeps its instances' nodes in a foreign leader's tree, as libyang's rb
	// nodes do); rbDualCount counts such foreign entries per tree. Both make removal O(1) trees.
	rbDual      map[*Node][]*rbGroup
	rbDualCount map[*rbGroup]int

	schemaUnordered bool                // a copy or move retained a different schema order
	contexts        map[*schema.Set]int // linked schema nodes by retained context; lookups only read its size
	leaderSeq       uint64              // the last first-instance registration (record.seq)
}

type recordChain struct {
	first *record
	last  *record
	count int
	// Exact chains build an ordered index at their first canonical-only move.
	// Ordinary registration and removal stay O(1). After validation first moves
	// a record into a bucket, AVL height bounds its subsequent ordered updates.
	root *record
}

type record struct {
	node        *Node
	chain       *recordChain
	prev, next  *record
	order       uint64
	seq         uint64 // first-instance records: registration order across all schema nodes
	left, right *record
	height      int
	more        []uint64 // later registrations of this node with the same recorded hash
}

func recordHeight(r *record) int {
	if r == nil {
		return 0
	}
	return r.height
}

func (r *record) setHeight() {
	r.height = 1 + max(recordHeight(r.left), recordHeight(r.right))
}

func (r *record) rotateLeft(work *workCounter) *record {
	if work != nil {
		work.Add(1)
	}
	next := r.right
	r.right, next.left = next.left, r
	r.setHeight()
	next.setHeight()
	return next
}

func (r *record) rotateRight(work *workCounter) *record {
	if work != nil {
		work.Add(1)
	}
	next := r.left
	r.left, next.right = next.right, r
	r.setHeight()
	next.setHeight()
	return next
}

func (r *record) balance(work *workCounter) *record {
	if work != nil {
		work.Add(1)
	}
	r.setHeight()
	switch delta := recordHeight(r.left) - recordHeight(r.right); {
	case delta > 1:
		if recordHeight(r.left.left) < recordHeight(r.left.right) {
			r.left = r.left.rotateLeft(work)
		}
		return r.rotateRight(work)
	case delta < -1:
		if recordHeight(r.right.right) < recordHeight(r.right.left) {
			r.right = r.right.rotateRight(work)
		}
		return r.rotateLeft(work)
	default:
		return r
	}
}

// recordOrderBuild consumes an already ordered chain in linear work, only when
// a mutation first needs arbitrary-order insertion. Reads never build indexes.
func recordOrderBuild(next **record, count int, work *workCounter) *record {
	if count == 0 {
		return nil
	}
	if work != nil {
		work.Add(1)
	}
	left := recordOrderBuild(next, count/2, work)
	r := *next
	*next = r.next
	r.left = left
	r.right = recordOrderBuild(next, count-count/2-1, work)
	r.setHeight()
	return r
}

// recordOrderInsert returns the balanced root and the new record's successor.
// It counts every search/comparison and balancing step, including rotations.
func recordOrderInsert(root, r *record, work *workCounter) (*record, *record) {
	if work != nil {
		work.Add(1)
	}
	if root == nil {
		r.height = 1
		return r, nil
	}
	var before *record
	if r.order < root.order {
		root.left, before = recordOrderInsert(root.left, r, work)
		if before == nil {
			before = root
		}
	} else {
		root.right, before = recordOrderInsert(root.right, r, work)
	}
	return root.balance(work), before
}

func recordOrderRemove(root *record, order uint64, work *workCounter) *record {
	if work != nil {
		work.Add(1)
	}
	switch {
	case order < root.order:
		root.left = recordOrderRemove(root.left, order, work)
	case order > root.order:
		root.right = recordOrderRemove(root.right, order, work)
	case root.left == nil:
		return root.right
	case root.right == nil:
		return root.left
	default:
		// The linked successor is also the right subtree's minimum, so finding
		// the replacement requires no additional traversal.
		next := root.next
		right := recordOrderRemove(root.right, next.order, work)
		next.left, next.right = root.left, right
		root = next
	}
	return root.balance(work)
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

// addContext and dropContext maintain the context mix as schema siblings change. Charging the
// update makes the context bookkeeping visible to the counted scaling tests.
func (s *siblings) addContext(n *Node) {
	if n.schema == nil {
		return
	}
	if err := s.touch(); err != nil {
		return
	}
	s.visit()
	if s.contexts == nil {
		s.contexts = make(map[*schema.Set]int)
	}
	s.contexts[ctxOf(n)]++
}

func (s *siblings) dropContext(n *Node) {
	if n.schema == nil {
		return
	}
	if err := s.touch(); err != nil {
		return
	}
	s.visit()
	set := ctxOf(n)
	if s.contexts[set] == 1 {
		delete(s.contexts, set)
	} else {
		s.contexts[set]--
	}
}

// indexOf is the position of n in l, one of s's lists, -1 if absent. It scans from the end: the
// XML parser's opaque anchor is the last opaque sibling of its name, usually the last one.
// ponytail: O(distance from the end); a node→index map if anchors far from the end matter.
func (s *siblings) indexOf(l siblingSpan, n *Node) int {
	for i := l.Len() - 1; i >= 0; i-- {
		s.visit()
		if l.At(i) == n {
			return i
		}
	}
	return -1
}

// position caches one interior sibling position until the next insertion or removal. The
// generation in the key makes mutation invalidate it without any maintenance work.
func (s *siblings) position(n *Node) int {
	if at := s.orderPosition(n); at >= 0 {
		return at
	}
	// No cache here: lookups run on read-only paths that concurrent readers share (doc.go), so
	// they must not write sibling state. The parser keeps its own cache (lydCtx.position).
	return s.schemaSpan().indexFromEnd(s.work, n)
}

// has reports whether an instance of s is in the list.
func (s *siblings) has(sn *schema.Node) bool {
	for _, n := range s.SchemaAll() {
		s.visit()
		if n.schema == sn {
			return true
		}
	}
	return false
}

// runLeader returns the selected first-instance record, or the first physical
// instance before a children hash table exists.
func (s *siblings) runLeader(sn *schema.Node) *Node {
	if n := s.leaderFirst(sn); n != nil {
		return n
	}
	if n := s.firstRun[sn]; n != nil {
		return n
	}
	if n := s.documentFirst(sn); n != nil {
		return n
	}
	return nil
}

func (s *siblings) hasRB(sn *schema.Node) bool       { return s.rbTree[s.runLeader(sn)] }
func (s *siblings) hasUnsorted(sn *schema.Node) bool { return s.unsorted[s.runLeader(sn)] }

func (s *siblings) transferRun(old, next *Node) { s.transferRBGroup(old, next, s.rbGroups[old]) }

func (s *siblings) transferRBGroup(old, next *Node, group *rbGroup) {
	if err := s.touch(); err != nil {
		return
	}
	if group == nil || old == nil || next == nil || old == next {
		return
	}
	oldGroups := s.metaGroups(old)
	if !slices.Contains(oldGroups, group) {
		return
	}
	nextGroups := s.metaGroups(next)
	wasUnsorted := s.unsorted[old]
	oldGroups = slices.DeleteFunc(oldGroups, func(g *rbGroup) bool { return g == group })
	s.setMetaGroups(old, oldGroups)
	if !slices.Contains(nextGroups, group) {
		nextGroups = append(nextGroups, group)
	}
	s.setMetaGroups(next, nextGroups)
	if len(oldGroups) > 0 {
		markUnsorted(s, old)
	}
	if len(nextGroups) > 1 || wasUnsorted {
		markUnsorted(s, next)
	}
	if len(oldGroups) == 0 {
		delete(s.unsorted, old)
	}
}

// runGone forgets state carried by a removed run leader.
func (s *siblings) runGone(n *Node) {
	if err := s.touch(); err != nil {
		return
	}
	if s.rbGroups[n] != nil {
		p := s.work.unlinkPlan
		if p == nil || p.node != n || p.sib != s || p.gen != s.gen {
			if err := s.work.chargeHash(4 * (uint64(len(s.rbMore[n])) + 1)); err != nil {
				return
			}
		}
		if err := n.touch(); err != nil {
			return
		}
		for _, g := range s.metaGroups(n) {
			if d := s.rbOrders[g]; d != nil && d.root == nil {
				n.carriedRB = append(n.carriedRB, rbCarry{g, d})
				d.work = nil
				delete(s.rbOrders, g)
			}
		}
	}
	delete(s.rbTree, n)
	delete(s.rbGroups, n)
	delete(s.rbMore, n)
	delete(s.unsorted, n)
}

// rb_remove_node empties only the selected tree. Adjacent runs of the same
// schema can have different trees after a separator is removed.
func (s *siblings) clearEmptyRBGroup(group *rbGroup) bool {
	if err := s.touch(); err != nil {
		return false
	}
	if group == nil {
		return false
	}
	// The tree's own members are its topology nodes less those it holds for other runs. Its
	// leader was its last member: a single unlink drops it with runGone, a bulk one clears the
	// leaders of all emptied trees in one pass (clearEmptyLeaders).
	if order := s.rbOrders[group]; order != nil && len(order.nodes) > s.rbDualCount[group] {
		return false
	}
	// Empty graphs retained by an effective metadata selection slot are compacted by removal.
	if d := s.rbOrders[group]; d != nil && d.root != nil {
		return false
	}
	delete(s.rbDualCount, group)
	return true
}

// removeRBNode is lyds_unlink's rb_remove_node. The sibling set is touched before the retained
// topology changes so #257's transaction can restore both its group map and every RB node.
func (s *siblings) removeRBNode(n, prev, next, tail *Node, around func(*Node) (*Node, *Node)) error {
	var order *dirtyOrder
	var leader *Node
	if p := s.work.unlinkPlan; around == nil && p != nil && p.node == n && p.sib == s && p.gen == s.gen {
		order, leader = p.order, p.leader
	} else {
		var err error
		order, leader, err = s.removalOrder(n, prev, next, tail, around)
		if err != nil {
			return err
		}
		if order != nil && s.work != nil {
			cost := s.transferCost(leader, next)
			if order.nodes[n] != nil {
				cost = addHashBound(cost, order.removalBound())
			}
			if leader != n {
				cost = addHashBound(cost, 4*(uint64(len(s.rbMore[leader]))+1)) // compactMeta(leader)
			}
			if err := s.work.chargeHash(cost); err != nil {
				return err
			}
		}
	}
	if order == nil {
		return nil
	}
	if err := s.touch(); err != nil {
		return err
	}
	before := prev
	if before == nil {
		before = tail
	}
	alone := before == n || (before == nil || before.schema != n.schema) && (next == nil || next.schema != n.schema)
	if leader == n && alone && len(order.nodes) == 1 {
		if err := n.touch(); err != nil {
			return err
		}
		// lyds_unlink leaves an alone leader's metadata attached. Its next sorted insertion
		// clears it (lyds_insert:1225-1232); a direct whole-run link transfers it intact.
		n.detachedRB, n.detachedGroup = order, n.rbGroup
		order.work = nil
		delete(s.rbOrders, n.rbGroup)
		s.runGone(n)
		return nil
	}
	if leader == n && alone {
		return nil
	}
	if leader == n && next != nil {
		s.transferRun(n, next)
	}
	order.work = s.work
	order.remove(n)
	if len(order.nodes) == 0 {
		owner := leader
		if leader == n && next != nil {
			owner = next
		}
		s.compactMeta(owner)
	}

	return nil
}

// len is the number of siblings, opaque ones included.
func (s *siblings) len() int { return s.Len() }

// idxKey is a secondary index by schema and a digest of delimited canonical values. Native
// equality also checks Node.hash. Keeping this discriminator avoids unbounded work
// when many distinct key tuples concatenate to identical LYB bytes (#236 case 4).
type idxKey struct {
	s    *schema.Node
	key  string
	hash uint32
}

func (s *siblings) all() iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		for _, n := range s.SchemaAll() {
			s.visit()
			if !yield(n) {
				return
			}
		}
		for _, n := range s.opaqueAll() {
			s.visit()
			if !yield(n) {
				return
			}
		}
	}
}

// hashOf reads the stored hash and the live positional discriminator. It does not
// run lyd_hash. A list with no native hash event yet cannot be registered.
func hashOf(n *Node) (idxKey, bool) { return hashOfWork(n, nil) }

func indexedKey(n *Node, work *workCounter) (idxKey, bool) {
	if n.hashed {
		return n.hkey, true
	}
	return hashOfWork(n, work)
}

func hashOfWork(n *Node, work *workCounter) (k idxKey, ok bool) {
	k.s = n.schema
	k.hash = n.hash
	switch {
	case n.schema == nil:
		return k, false
	case n.schema.Kind == schema.LeafList:
		k.key = indexDigest(n.value.Canonical(), work)
		if work != nil && work.hashErr != nil {
			return k, false
		}
	case n.schema.Kind == schema.List && !n.schema.Keyless():
		if !n.lkeyOK {
			return k, false
		}
		h := sha256.New()
		for i := range n.schema.Keys {
			if work != nil {
				work.Add(1)
			}
			child := positionalChild(n, i)
			if child == nil {
				break // early end is part of positional equality, not lost registration
			}
			writeKeyPosition(h, child.schema, child.hash, child.Value(), work)
			if work != nil && work.hashErr != nil {
				return k, false
			}
		}
		k.key = string(h.Sum(nil))
	}
	return k, true
}

// Fixed-size map keys make an indexed miss independent of predicate length.
// Callers still verify native equality, so digest collisions cannot select a node.
func indexDigest(value string, work *workCounter) string {
	if work != nil && work.chargeHash(2*uint64(len(value))) != nil {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return string(digest[:])
}

// indexWorkBound warms immutable name components and bounds one subsequent
// discriminator computation without touching the node or its index record.
func indexWorkBound(n, changed *Node, value *types.Value, work *workCounter) uint64 {
	if n.schema == nil {
		return 0
	}
	if n.schema.Kind == schema.LeafList {
		if value != nil && n == changed {
			return 2 * uint64(len(value.Canonical()))
		}
		return 2 * uint64(len(n.value.Canonical()))
	}
	if n.schema.Kind != schema.List || n.schema.Keyless() || !n.lkeyOK {
		return 0
	}
	var amount uint64
	for i := range n.schema.Keys {
		child := positionalChild(n, i)
		if child == nil {
			break
		}
		length := len(child.Value())
		if child == changed && value != nil {
			length = len(value.Canonical())
		}
		if child.schema != nil {
			indexNameDigest(child.schema, work)
			amount = addHashBound(amount, 32)
		}
		amount = addHashBound(amount, 16+2*uint64(length))
	}
	return amount
}

func indexInsertedBound(list, inserted *Node, at int, work *workCounter) uint64 {
	if list == nil || list.schema == nil || list.schema.Kind != schema.List || list.schema.Keyless() {
		return 0
	}
	var amount uint64
	for i := range list.schema.Keys {
		var child *Node
		switch {
		case i == at:
			child = inserted
		case i < at:
			child = positionalChild(list, i)
		default:
			child = positionalChild(list, i-1)
		}
		if child == nil {
			break
		}
		if child.schema != nil {
			indexNameDigest(child.schema, work)
			amount = addHashBound(amount, 32)
		}
		amount = addHashBound(amount, 16+2*uint64(len(child.Value())))
	}
	return amount
}

// indexAnyOrderBound bounds list's discriminator with inserted among its
// children, whatever order they take: the len(Keys) largest compared
// contributions of all children. A sort of a run can move any child into an
// incomplete list's positional prefix (positionalChild).
func indexAnyOrderBound(list, inserted *Node, work *workCounter) uint64 {
	if list == nil || list.schema == nil || list.schema.Kind != schema.List || list.schema.Keyless() || !list.lkeyOK {
		return 0
	}
	contribution := func(c *Node) uint64 {
		if work != nil {
			work.Add(1)
		}
		amount := addHashBound(16, 2*uint64(len(c.Value())))
		if c.schema != nil {
			indexNameDigest(c.schema, work) // cached per schema node: warmed before the sort
			amount = addHashBound(amount, 32)
		}
		return amount
	}
	all := []uint64{contribution(inserted)}
	for _, c := range list.kids.SchemaAll() {
		all = append(all, contribution(c))
	}
	for _, c := range list.kids.opaqueAll() {
		all = append(all, contribution(c))
	}
	slices.SortFunc(all, func(a, b uint64) int { return cmp.Compare(b, a) })
	var amount uint64
	for _, a := range all[:min(len(all), len(list.schema.Keys))] {
		amount = addHashBound(amount, a)
	}
	return amount
}

// indexBatchInsertedBound bounds every intermediate compared prefix while a
// batch enters an incomplete list. A later insertion may compare an earlier
// inserted value, so summing independent single-insertion bounds is too small.
func indexBatchInsertedBound(list *Node, inserted []*Node, work *workCounter) uint64 {
	if len(inserted) == 0 {
		return 0
	}
	bound := indexWorkBound(list, nil, nil, work)
	for _, child := range inserted {
		if child.schema != nil {
			indexNameDigest(child.schema, work)
			bound = addHashBound(bound, 32)
		}
		bound = addHashBound(bound, 16+2*uint64(len(child.Value())))
	}
	var total uint64
	for range inserted {
		total = addHashBound(total, bound)
	}
	return total
}

// indexRemovedBound bounds the discriminator after a child leaves a list.
// In particular, removing a non-key from a copied incomplete prefix can expose
// a different, much larger child to the parent's live comparison index.
func indexRemovedBound(list, removed *Node, work *workCounter) uint64 {
	return indexAfterRemovedBound(list, map[*Node]bool{removed: true}, work)
}

func indexAfterRemovedBound(list *Node, removed map[*Node]bool, work *workCounter) uint64 {
	if list == nil || list.schema == nil || list.schema.Kind != schema.List || list.schema.Keyless() || !list.lkeyOK {
		return 0
	}
	var amount uint64
	position := 0
	for i := 0; i < len(list.schema.Keys); i++ {
		for {
			if work != nil {
				work.Add(1)
			}
			if !removed[positionalChild(list, position)] {
				break
			}
			position++
		}
		if work != nil {
			work.Add(1)
		}
		child := positionalChild(list, position)
		if child == nil {
			break
		}
		position++
		if child.schema != nil {
			indexNameDigest(child.schema, work)
			amount = addHashBound(amount, 32)
		}
		amount = addHashBound(amount, 16+2*uint64(len(child.Value())))
	}
	return amount
}

// positionalChild follows lyd_child/next, including an opaque child exposed by
// internal key removal. Both the discriminator and comparison must see it.
func positionalChild(n *Node, i int) *Node {
	if i < n.kids.SchemaLen() {
		return n.kids.At(i)
	}
	i -= n.kids.SchemaLen()
	if i < n.kids.OpaqueLen() {
		return n.kids.opaqueAt(i)
	}
	return nil
}

// writeKeyPosition encodes the schema and data gates of the positional comparison
// (tree_data.c:1775-1778). Names, not pointers, permit probes from another context.
func writeKeyPosition(h hash.Hash, sn *schema.Node, stored uint32, value string, work *workCounter) {
	var fixed [16]byte
	if sn != nil {
		name := indexNameDigest(sn, work)
		if work != nil && work.hashErr != nil {
			return
		}
		if work != nil && work.chargeHash(uint64(len(name)+len(fixed))+2*uint64(len(value))) != nil {
			return
		}
		_, _ = h.Write(name[:])
	} else if work != nil && work.chargeHash(uint64(len(fixed))+2*uint64(len(value))) != nil {
		return
	}
	if sn != nil {
		binary.LittleEndian.PutUint32(fixed[:4], uint32(sn.Kind))
	}
	binary.LittleEndian.PutUint32(fixed[4:], stored)
	binary.LittleEndian.PutUint64(fixed[8:], uint64(len(value)))
	_, _ = h.Write(fixed[:])
	_, _ = h.Write([]byte(value))
}

func indexNameDigest(sn *schema.Node, work *workCounter) [32]byte {
	compute := func() ([32]byte, error) {
		if work != nil {
			if err := work.chargeHash(2 * (uint64(len(sn.Module.Name)) + uint64(len(sn.Name)) + 1)); err != nil {
				return [32]byte{}, err
			}
		}
		h := sha256.New()
		_, _ = h.Write([]byte(sn.Module.Name))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(sn.Name))
		var digest [32]byte
		copy(digest[:], h.Sum(nil))
		return digest, nil
	}
	if sn.Module.Owner != nil {
		digest, err := sn.Module.Owner.CachedIndexName(sn, compute)
		if work != nil && err != nil {
			work.hashErr = err
		}
		return digest
	}
	if work != nil {
		if digest, ok := work.indexNames[sn]; ok {
			return digest
		}
	}
	digest, err := compute()
	if work != nil {
		if err != nil {
			work.hashErr = err
		} else {
			if work.indexNames == nil {
				work.indexNames = map[*schema.Node][32]byte{}
			}
			work.indexNames[sn] = digest
		}
	}
	return digest
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
	if err := s.touch(); err != nil {
		return
	}
	if parent == nil || parent.schema == nil || isAny(parent.schema) || n.schema == nil {
		return
	}
	if s.ht != nil {
		if !n.hashed { // a splice publishes its batch first: the table built for one has the rest
			s.hashPut(n)
		}
		return
	}
	if s.SchemaLen() < htMinItems {
		return
	}
	s.ht = map[idxKey]*recordChain{}
	s.exactByNode = map[*Node]*record{}
	// lyd_insert_hash builds the table from the children in sibling order (tree_data_hash.c:
	// 181-197), which for a run the parser still defers is its tree order: libyang has linked
	// each instance at its sorted place.
	order := s.logical()
	for _, c := range order.All() {
		s.hashPut(c)
	}
	s.leaderBuild(order)
}

// logical is the sibling order libyang has: each member of a dirty run occupies a physical
// slot in its tree order. A node linked by another insertion path while the parser defers that
// run is not in its RB tree; keep it in its physical slot so table construction sees it too.
func (s *siblings) logical() siblingSpan {
	if len(s.pending) == 0 {
		return s.schemaSpan()
	}
	out := make([]*Node, 0, s.SchemaLen())
	type run struct {
		ordered []*Node
		next    int
	}
	runs := map[*schema.Node]*run{}
	for _, n := range s.SchemaAll() {
		if s.work != nil {
			s.work.Add(1)
		}
		ord := s.pending[n.schema]
		if ord == nil || ord.nodes[n] == nil || !ord.nodes[n].active {
			out = append(out, n)
			continue
		}
		r := runs[n.schema]
		if r == nil {
			r = &run{ordered: ord.ordered()}
			runs[n.schema] = r
		}
		out = append(out, r.ordered[r.next])
		r.next++
	}
	return spanOf(out)
}

// hashPut is the unchecked exact insertion in tree_data_hash.c:134. Repeated
// key insertion must retain even registrations whose recorded hash is now stale.
func (s *siblings) hashPut(n *Node) {
	if err := s.touch(); err != nil {
		return
	}
	k, ok := hashOfWork(n, s.work)
	if !ok {
		return
	}
	s.hashPutKey(n, k)
}

// hashPutKey reuses a live discriminator across a registration-only reorder.
func (s *siblings) hashPutKey(n *Node, k idxKey) {
	if err := s.touch(); err != nil {
		return
	}
	if s.work != nil {
		s.work.Add(1)
	}
	if s.history == nil {
		s.history = map[*Node]map[uint32]*record{}
	}
	byHash := s.history[n]
	if byHash == nil {
		byHash = map[uint32]*record{}
		s.history[n] = byHash
	}
	s.exactOrder++
	r := byHash[n.hash]
	if r == nil {
		r = &record{node: n, order: s.exactOrder}
		byHash[n.hash] = r
	} else {
		r.more = append(r.more, s.exactOrder)
	}
	s.activateRecord(n, k, r)
}

// activateRecord indexes the oldest applicable registration. Grouping later
// identical registrations avoids revisiting an ever-growing history on each key
// insertion while preserving the order revealed by successive native removals.
func (s *siblings) activateRecord(n *Node, k idxKey, r *record) {
	if err := s.touch(); err != nil {
		return
	}
	if n.hashed && n.hkey == k && s.exactByNode[n] == r {
		return
	}
	s.unindexRecord(n)
	if r == nil {
		return
	}
	if s.exactByNode == nil {
		s.exactByNode = map[*Node]*record{}
	}
	c := s.ht[k]
	if c == nil {
		c = &recordChain{}
		s.ht[k] = c
	}
	var before *record
	if c.last == nil || c.last.order < r.order {
		if c.root != nil {
			c.root, _ = recordOrderInsert(c.root, r, s.work)
		}
	} else {
		if c.root == nil {
			next := c.first
			c.root = recordOrderBuild(&next, c.count, s.work)
		}
		c.root, before = recordOrderInsert(c.root, r, s.work)
	}
	r.chain, r.next = c, before
	if before == nil {
		r.prev = c.last
		c.last = r
	} else {
		r.prev = before.prev
		before.prev = r
	}
	if r.prev == nil {
		c.first = r
	} else {
		r.prev.next = r
	}
	c.count++
	s.exactByNode[n] = r
	n.hkey, n.hashed = k, true
}

// hashReindex changes only the live comparison discriminator, without adding or
// deleting any native registration. Old recorded hashes remain in history.
func (s *siblings) hashReindex(n *Node) {
	if err := s.touch(); err != nil {
		return
	}
	if s.ht == nil || s.history[n] == nil {
		return
	}
	k, ok := hashOfWork(n, s.work)
	if !ok {
		return
	}
	if n.hashed && k == n.hkey {
		return
	}
	s.activateRecord(n, k, s.history[n][n.hash])
	if c := s.ht[k]; c != nil && c.root == nil {
		next := c.first
		c.root = recordOrderBuild(&next, c.count, s.work)
	}
}

// unindexRecord removes a secondary-index entry, preserving native history.
func (s *siblings) unindexRecord(n *Node) {
	if err := s.touch(); err != nil {
		return
	}
	if !n.hashed || s.ht == nil {
		return
	}
	if s.work != nil {
		s.work.Add(1)
	}
	c, r := s.ht[n.hkey], s.exactByNode[n]
	if c != nil && c.root != nil && r != nil && r.chain == c {
		c.root = recordOrderRemove(c.root, r.order, s.work)
		r.left, r.right, r.height = nil, nil, 0
	}
	if !recordDrop(c, s.exactByNode, n) {
		return
	}
	if c.first == nil {
		delete(s.ht, n.hkey)
	}
	n.hashed = false
}

// hashRemove is lyd_unlink_hash: remove ONE registration at the node's current
// stored hash (tree_data_hash.c:219), never a registration under an earlier hash.
func (s *siblings) hashRemove(n *Node) {
	if err := s.touch(); err != nil {
		return
	}
	byHash := s.history[n]
	r := byHash[n.hash]
	if r == nil {
		return
	}
	k := n.hkey
	s.unindexRecord(n)
	if len(r.more) == 0 {
		delete(byHash, n.hash)
		if len(byHash) == 0 {
			delete(s.history, n)
		}
		return
	}
	r.order, r.more = r.more[0], r.more[1:]
	s.activateRecord(n, k, r)
}

// hashForget drops references when a node leaves this sibling set. As with
// first-instance records (D-0117), dangling native records are not retained.
func (s *siblings) hashForget(n *Node) {
	if err := s.touch(); err != nil {
		return
	}
	s.unindexRecord(n)
	delete(s.history, n)
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

func (s *siblings) firstRecordSchema(sn *schema.Node) bool {
	return sn != nil && (sn.Kind == schema.List || sn.Kind == schema.LeafList) && (s.schemaUnordered || opChild(sn))
}

// leaderMaterialize registers the ordinary run starts in sibling order when the siblings first
// leave schema order: until then their runs were contiguous, so libyang's records are the run
// starts. Operation children already have records, which need not follow document order
// (D-0117); they are kept.
func (s *siblings) leaderMaterialize(order siblingSpan) {
	if err := s.touch(); err != nil {
		return
	}
	seen := map[*schema.Node]bool{}
	var prev *schema.Node
	for _, n := range order.All() {
		if s.work != nil {
			s.work.Add(1)
		}
		if !opChild(n.schema) && s.firstRecordSchema(n.schema) && n.schema != prev {
			if !seen[n.schema] {
				s.firstRun[n.schema] = n
				seen[n.schema] = true
			}
			s.leaderAppend(n.schema, n)
		}
		prev = n.schema
	}
}

// leaderBuild is lyd_insert_hash_add's empty_ht branch: every run start is registered in sibling
// order. It runs when the table is created or ordinary siblings first leave schema order.
func (s *siblings) leaderBuild(order siblingSpan) {
	if err := s.touch(); err != nil {
		return
	}
	s.leaders = map[*schema.Node]*recordChain{}
	s.leaderByNode = map[*Node]*record{}
	seen := map[*schema.Node]bool{}
	var prev *schema.Node
	for _, n := range order.All() {
		if s.work != nil {
			s.work.Add(1)
		}
		if s.firstRecordSchema(n.schema) && n.schema != prev {
			if !seen[n.schema] {
				s.firstRun[n.schema] = n
				seen[n.schema] = true
			}
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
	if err := s.touch(); err != nil {
		return false
	}
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
	if !recordAppend(c, s.leaderByNode, n) {
		return false
	}
	s.leaderSeq++
	c.last.seq = s.leaderSeq
	return true
}

// leaderFirstMixed is lyd_find_sibling_schema's children_ht lookup in siblings of several
// contexts: the bucket holds the first instances of every schema node with sn's module and
// node names, in registration order, and lyd_hash_table_schema_val_equal accepts the first one
// whose schema compares equal with its parents (tree_data_common.c:1069-1112).
func (s *siblings) leaderFirstMixed(sn *schema.Node) *Node {
	var best *record
	for k, c := range s.leaders {
		s.visit()
		if c.first != nil && (best == nil || c.first.seq < best.seq) && compareSchemaEqual(sn, k, true) {
			best = c.first
		}
	}
	if best == nil {
		return nil
	}
	return best.node
}

func (s *siblings) leaderDrop(sn *schema.Node, n *Node) bool {
	if err := s.touch(); err != nil {
		return false
	}
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
	linked := make(map[*Node]bool, s.SchemaLen())
	for _, n := range s.SchemaAll() {
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
	for _, n := range s.SchemaAll() {
		_, hashable := hashOf(n)
		if n.hashed != exactNodes[n] || s.ht != nil && hashable != n.hashed {
			return fmt.Errorf("linked node %p hashable=%v hashed=%v exact-record=%v", n, hashable, n.hashed, exactNodes[n])
		}
	}
	return nil
}

// leaderInsert applies lyd_insert_hash_add to n already at position at.
func (s *siblings) leaderInsert(n *Node, at int) {
	if err := s.touch(); err != nil {
		return
	}
	if s.ht == nil || !s.firstRecordSchema(n.schema) || at < 0 || at >= s.SchemaLen() ||
		at > 0 && s.At(at-1).schema == n.schema {
		return
	}
	if at+1 < s.SchemaLen() && s.At(at+1).schema == n.schema {
		if !s.leaderReplace(n.schema, s.At(at+1), n) {
			return
		}
		return
	}
	s.leaderAppend(n.schema, n)
}

// leaderInsertBetween is leaderInsert with already-known neighbours.
func (s *siblings) leaderInsertBetween(n, prev, next *Node) {
	if err := s.touch(); err != nil {
		return
	}
	if s.ht == nil || !s.firstRecordSchema(n.schema) || prev != nil && prev.schema == n.schema {
		return
	}
	if next != nil && next.schema == n.schema {
		s.leaderReplace(n.schema, next, n)
		return
	}
	s.leaderAppend(n.schema, n)
}

// opRehashNeighbours gets the physical neighbours relevant to first-record
// rehashing in O(1), including around a split operation run.
func (s *siblings) opRehashNeighbours(n *Node) (prev, next *Node, ok bool) {
	if ord := s.pending[n.schema]; ord != nil {
		// a run the parser defers: its tree order is the sibling order libyang has
		if first := ord.first(); first != n {
			return first, nil, true
		}
		return nil, ord.successor(n), true
	}
	if s.order == nil {
		return nil, nil, false
	}
	h := s.order.byNode[n]
	if h == nil {
		return nil, nil, false
	}
	if before := s.order.seq.Prev(h); before != nil && before.Value.schema == n.schema {
		return before.Value, nil, true
	}
	if after := s.order.seq.Next(h); after != nil && after.Value.schema == n.schema {
		next = after.Value
	}
	return nil, next, true
}

// leaderReplace is lyd_insert_hash_add replacing the old first-instance record when a linked
// node becomes the run start. A missing old record leaves the new record uninserted; libyang also
// logs an internal error, which the port deliberately does not surface as a data verdict (D-0118).
func (s *siblings) leaderReplace(sn *schema.Node, old, n *Node) bool {
	if err := s.touch(); err != nil {
		return false
	}
	if !s.leaderDrop(sn, old) {
		return false
	}
	return s.leaderAppend(sn, n)
}

// leaderUnlink applies lyd_unlink_hash while n is still at position at. removing is a real
// unlink (the node leaves its siblings); a value rehash (lyd_change_node_value: lyd_unlink_hash
// then lyd_insert_hash on the same linked node, tree_data_new.c:1177-1194) passes false.
func (s *siblings) leaderUnlink(n *Node, at int, removing bool) {
	if err := s.touch(); err != nil {
		return
	}
	if s.ht == nil || !s.firstRecordSchema(n.schema) || at < 0 || at >= s.SchemaLen() {
		return
	}
	var prev, next *Node
	if at > 0 {
		prev = s.At(at - 1)
	}
	if at+1 < s.SchemaLen() {
		next = s.At(at + 1)
	}
	s.leaderUnlinkBetween(n, prev, next, removing)
}

// leaderUnlinkBetween is lyd_unlink_hash's first-instance part (tree_data_hash.c:226-243): only a
// run start loses its record, and its same-schema successor gets one. A node that kept a record
// after it stopped starting a run keeps it through a rehash, as libyang does; when the node is
// removed the port drops that record, which libyang leaves dangling (D-0117).
func (s *siblings) leaderUnlinkBetween(n, prev, next *Node, removing bool) {
	if err := s.touch(); err != nil {
		return
	}
	if s.ht == nil || !s.firstRecordSchema(n.schema) {
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
//	xpathnode.go FirstChild                 eval_name_test_try_compile_predicate_append (xpath.c:7963) hash
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
// first sibling instance when runs can be split.
func (t *Tree) schemaLookup(s *siblings, sn *schema.Node) *Node {
	mixed := s.mixedContexts()
	if (opChild(sn) || s.schemaUnordered) && !mixed &&
		(s.SchemaLen() == 0 || sn.Module.Owner == nil || ctxOf(s.At(0)) == sn.Module.Owner) {
		return t.schemaLookupOperation(s, sn)
	}
	if i := t.schemaIndex(s, sn); i >= 0 {
		return s.At(i)
	}
	return nil
}

// A diff create can leave sibling roots compiled in different contexts. The
// hash and schema-order indexes use Go schema pointers, while libyang's
// sibling lookup also compares schema names across contexts.
func (s *siblings) mixedContexts() bool {
	s.visit() // one counted check per lookup, independent of sibling count
	return len(s.contexts) > 1
}

// documentFirst is the first instance in sibling order. Operation children use their mutation-
// maintained document-order index, as do copies outside schema order.
func (t *Tree) documentFirst(s *siblings, sn *schema.Node) *Node {
	if opChild(sn) || s.schemaUnordered {
		return s.documentFirst(sn)
	}
	if i := t.schemaIndex(s, sn); i >= 0 {
		return s.At(i)
	}
	return nil
}

// schemaIndex is the index of the first instance of sn in s, -1 if there is none.
// Operation callers must need a position for a mutation or metadata walk; first-instance
// and run-only lookups use schemaLookup and opRun without a position scan. Unordered copies
// likewise use their first-instance records when the children table exists.
func (t *Tree) schemaIndex(s *siblings, sn *schema.Node) int {
	if s.SchemaLen() == 0 {
		return -1
	}
	// The linear branch of lyd_find_sibling_schema compares only module and
	// node names for foreign contexts. Its table callback compares schema lineage.
	if s.foreignFor(sn) {
		if t.work.lookupDepth != 0 && t.foreignSchemaInput[s] {
			return t.foreignSchemaIndex(s, sn, s.ht != nil)
		}
		for i, n := range s.SchemaAll() {
			s.visit()
			if schemaLookupMatch(n.schema, sn, s.ht != nil) {
				return i
			}
		}
		return -1
	}
	if opChild(sn) || s.schemaUnordered {
		if leader := physicalStart(s, sn, t.schemaLookupOperation(s, sn)); leader != nil {
			return s.orderPosition(leader)
		}
		return -1
	}
	probe := &Node{schema: sn, parent: s.At(0).parent} // top level or not decides the module order
	i := s.LowerBound(0, s.SchemaLen(), func(a *Node) bool { return t.sameOrAfter(a, probe) })
	if i < s.SchemaLen() && s.At(i).schema == sn {
		return i
	}
	return -1
}

type foreignSchemaCacheKey struct {
	s     *siblings
	table bool
}

type schemaLineageID uint32

type schemaLineageComponent struct {
	parent schemaLineageID
	module string
	name   string
	kind   schema.Kind
}

type foreignSchemaIndexKey struct {
	module  string
	name    string
	lineage schemaLineageID
}

// foreignSchemaIndex is the diff-time equivalent of lyd_find_sibling_schema's cross-context
// comparisons. The C lookup compares module/name only without children_ht and complete schema
// lineage with it (libyang v5.8.6 src/tree_data_common.c:1101-1137). Building this index once per
// sibling set replaces the two repeated scans in diff.c:1252-1254 and 1339-1341.
func (t *Tree) foreignSchemaIndex(s *siblings, sn *schema.Node, table bool) int {
	if t.foreignSchema == nil {
		t.foreignSchema = make(map[foreignSchemaCacheKey]map[foreignSchemaIndexKey][]int)
	}
	cacheKey := foreignSchemaCacheKey{s: s, table: table}
	index := t.foreignSchema[cacheKey]
	if index == nil {
		// no capacity hint: the index grows only after each sibling's charge succeeded
		index = make(map[foreignSchemaIndexKey][]int)
		for i, n := range s.SchemaAll() {
			s.visit()
			if err := t.work.chargeLookup(1); err != nil {
				return -1
			}
			key, ok, err := t.foreignSchemaKey(n.schema, table)
			if err != nil || !ok { // a lookup budget error is returned at the diff boundary
				return -1
			}
			index[key] = append(index[key], i)
		}
		t.foreignSchema[cacheKey] = index
	}
	if err := t.work.chargeLookup(1); err != nil {
		return -1
	}
	key, valid, err := t.foreignSchemaKey(sn, table)
	if err != nil || !valid {
		return -1
	}
	// The key is the foreign match; a candidate of sn's own context must be sn itself
	// (lyd_find_sibling_schema compares schema pointers there), so check each in order.
	for _, i := range index[key] {
		if err := t.work.chargeLookup(1); err != nil {
			return -1
		}
		if schemaLookupMatch(s.At(i).schema, sn, table) {
			return i
		}
	}
	return -1
}

type foreignInstanceKey struct {
	module, name string
	hash         uint32
}

// foreignInstances is the candidate sequence of the linear branches of lyd_find_sibling_first
// that compare every sibling with a target of another context (tree_data.c:3239-3251). Equal
// instances have equal module and node names (compareSchemaEqual) and equal stored hashes
// (hashEqual gates lyd_compare_single_data), so while a cross-context diff runs over its
// immutable inputs the siblings are indexed by those once, charged to the lookup quota, and the
// target's bucket keeps their order: the same first match, without a scan per instance. Other
// callers keep the full sibling walk.
func (t *Tree) foreignInstances(s *siblings, target *Node) (iter.Seq[*Node], error) {
	if t.work.lookupDepth == 0 || !t.foreignSchemaInput[s] || target.schema == nil || target.schema.Module == nil {
		return s.all(), nil
	}
	if t.foreignInstance == nil {
		t.foreignInstance = make(map[*siblings]map[foreignInstanceKey][]*Node)
	}
	index := t.foreignInstance[s]
	if index == nil {
		// no capacity hint: the index grows only after each sibling's charge succeeded
		index = make(map[foreignInstanceKey][]*Node)
		for _, n := range s.SchemaAll() {
			s.visit()
			if err := t.work.chargeLookup(1); err != nil {
				return nil, err
			}
			if n.schema != nil && n.schema.Module != nil {
				k := foreignInstanceKey{n.schema.Module.Name, n.schema.Name, n.hash}
				index[k] = append(index[k], n)
			}
		}
		t.foreignInstance[s] = index
	}
	if err := t.work.chargeLookup(1); err != nil {
		return nil, err
	}
	bucket := index[foreignInstanceKey{target.schema.Module.Name, target.schema.Name, target.hash}]
	return func(yield func(*Node) bool) {
		for _, n := range bucket {
			if t.work.chargeLookup(1) != nil || !yield(n) {
				return // a refused charge is the diff's sticky lookup error
			}
		}
	}, nil
}

// foreignTableSchema is the schema node of s's instances that a foreign sn matches by schema
// lineage, the comparison of lyd_hash_table_val_equal; nil when s has none. A running
// cross-context diff uses its charged lineage index, other callers a charged sibling scan.
func (t *Tree) foreignTableSchema(s *siblings, sn *schema.Node, work *workCounter) (*schema.Node, error) {
	if t.work.lookupDepth != 0 && t.foreignSchemaInput[s] {
		if i := t.foreignSchemaIndex(s, sn, true); i >= 0 {
			return s.At(i).schema, nil
		}
		return nil, t.work.lookupErr
	}
	for _, n := range s.SchemaAll() {
		s.visit()
		if err := chargeComparison(work); err != nil {
			return nil, err
		}
		if n.schema == sn || compareSchemaEqual(n.schema, sn, true) {
			return n.schema, nil
		}
	}
	return nil, nil
}

// foreignInstancesErr is the lookup quota refusal that ended a foreignInstances walk.
func (t *Tree) foreignInstancesErr() error {
	if t.work.lookupDepth == 0 {
		return nil
	}
	return t.work.lookupErr
}

// foreignSchemaKey is the lineage key lyd_compare_schema_equal matches by. Building it is charged
// to the running diff's lookup quota, or outside a diff to the mutation's hash quota: both are
// reset at their operation boundary and their failure is returned, never a silently missing key.
func (t *Tree) foreignSchemaKey(sn *schema.Node, table bool) (foreignSchemaIndexKey, bool, error) {
	if sn == nil || sn.Module == nil {
		return foreignSchemaIndexKey{}, false, nil
	}
	if !table {
		return foreignSchemaIndexKey{module: sn.Module.Name, name: sn.Name}, true, nil
	}
	charge := t.work.chargeHash
	if t.work.lookupDepth != 0 {
		charge = t.work.chargeLookup
	}
	if t.schemaLineage == nil {
		t.schemaLineage = make(map[*schema.Node]schemaLineageID)
		t.lineageComponents = make(map[schemaLineageComponent]schemaLineageID)
	}
	if id, ok := t.schemaLineage[sn]; ok {
		return foreignSchemaIndexKey{lineage: id}, true, nil
	}
	var missing []*schema.Node
	for n := sn; n != nil; n = n.Parent {
		if _, ok := t.schemaLineage[n]; ok {
			break
		}
		missing = append(missing, n)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		n := missing[i]
		if n.Module == nil {
			return foreignSchemaIndexKey{}, false, nil
		}
		if err := charge(1); err != nil {
			return foreignSchemaIndexKey{}, false, err
		}
		component := schemaLineageComponent{
			parent: t.schemaLineage[n.Parent], module: n.Module.Name, name: n.Name, kind: n.Kind,
		}
		id, ok := t.lineageComponents[component]
		if !ok {
			t.nextLineageID++
			id = t.nextLineageID
			t.lineageComponents[component] = id
		}
		t.schemaLineage[n] = id
	}
	return foreignSchemaIndexKey{lineage: t.schemaLineage[sn]}, true, nil
}

// foreignFor reports whether a lookup of sn in s compares schema names, as
// lyd_find_sibling_schema does for siblings of another context than sn.
func (s *siblings) foreignFor(sn *schema.Node) bool {
	return s.SchemaLen() > 0 && (s.mixedContexts() ||
		sn.Module.Owner != nil && ctxOf(s.At(0)) != nil && ctxOf(s.At(0)) != sn.Module.Owner)
}

func schemaLookupMatch(got, want *schema.Node, table bool) bool {
	if got == want {
		return true
	}
	if got == nil || want == nil || got.Module == nil || want.Module == nil ||
		got.Module.Owner == want.Module.Owner {
		return false
	}
	if table {
		return compareSchemaEqual(got, want, true)
	}
	return got.Module.Name == want.Module.Name && got.Name == want.Name
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
	n, _ := t.findFirstWork(s, target, nil)
	return n
}

func (t *Tree) findFirstChecked(s *siblings, target *Node) (*Node, error) {
	if t.work.hashDepth == 0 {
		var readWork workCounter
		return t.findFirstWork(s, target, &readWork)
	}
	return t.findFirstWork(s, target, &t.work)
}

func (t *Tree) findFirstWork(s *siblings, target *Node, work *workCounter) (*Node, error) {
	if s.len() == 0 {
		return nil, nil
	}
	if s.SchemaLen() > 0 && s.At(0).schema != nil && target.schema != nil &&
		!compareSchemaEqual(s.At(0).schema.DataParent(), target.schema.DataParent(), true) {
		return nil, nil // schema mismatch
	}
	dup := isDupInstList(target.schema)
	if target.schema != nil && s.mixedContexts() {
		candidates, err := t.foreignInstances(s, target)
		if err != nil {
			return nil, err
		}
		for n := range candidates {
			first, second := n, target
			if s.ht != nil {
				first, second = target, n
			}
			equal, err := compareSingleWork(t, work, first, second, dup)
			if err != nil || equal {
				return n, err
			}
		}
		return nil, t.foreignInstancesErr()
	}
	sn := target.schema
	if sn != nil && s.SchemaLen() != 0 {
		if first := s.At(0); !sameSet(ctxOf(first), ctxOf(target)) {
			// The no-table branch of lyd_find_sibling_first compares the live
			// siblings directly. Mapping first would lose operation output nodes
			// because lyd_find_schema_ctx walks lys_getnext with input options.
			if s.ht == nil {
				candidates, err := t.foreignInstances(s, target)
				if err != nil {
					return nil, err
				}
				for n := range candidates {
					equal, err := compareSingleWork(t, work, n, target, dup)
					if err != nil || equal {
						return n, err
					}
				}
				return nil, t.foreignInstancesErr()
			}
			// With the table, lyd_hash_table_val_equal matches the target's schema by lineage
			// (lyd_compare_schema_equal), which also finds operation output nodes.
			var err error
			if sn, err = t.foreignTableSchema(s, target.schema, work); sn == nil || err != nil {
				return nil, err
			}
		}
	}
	if target.schema != nil && s.ht != nil {
		if s.SchemaLen() == 0 {
			// a table kept after its schema children left (lyd_unlink_hash never frees it) or
			// copied by a recursive dup: lyht_find misses, opaque nodes are not in it
			// (tree_data.c:3210-3237)
			return nil, nil
		}
		// the target's schema node in the siblings' context (libyang hashes module and node
		// names, so a target of another context finds its instances)
		if dup {
			if target.schema.Kind == schema.LeafList && s.order != nil &&
				(opChild(sn) || s.schemaUnordered) {
				entry := s.order.entries[sn]
				leader := physicalStart(s, sn, t.schemaLookupOperation(s, sn))
				if entry != nil && entry.gaps == 0 {
					if candidate := s.exactAtOrAfter(sn, target, leader); candidate != nil {
						equal, err := compareSingleWork(t, work, target, candidate, true)
						if err != nil || equal {
							return candidate, err
						}
					}
					return nil, nil
				}
			}
			for n := range t.instances(s, sn) {
				s.visit()
				equal, err := compareSingleWork(t, work, target, n, true)
				if err != nil || equal {
					return n, err
				}
			}
			return nil, nil
		}
		k, ok := indexedKey(target, work)
		if !ok {
			return nil, workError(work)
		}
		k.s = sn
		for n := range s.bucketNodes(k) {
			equal, err := htValEqual(t, work, target, n)
			if err != nil || equal {
				return n, err
			}
		}
		return nil, nil
	}
	if dup && target.schema.Kind == schema.LeafList && s.order != nil {
		if candidate := s.exactFirst(sn, target); candidate != nil {
			equal, err := compareSingleWork(t, work, candidate, target, true)
			if err != nil || equal {
				return candidate, err
			}
		}
		return nil, nil
	}
	for n := range s.all() {
		equal, err := compareSingleWork(t, work, n, target, dup)
		if err != nil || equal {
			return n, err
		}
	}
	return nil, nil
}

// htValEqual is lyd_hash_table_val_equal: lists and leaf-lists by their instance, other nodes
// by schema node only (equal schema nodes and parents across contexts).
func htValEqual(t *Tree, work *workCounter, a, b *Node) (bool, error) {
	if a.schema.Kind == schema.List || a.schema.Kind == schema.LeafList {
		return compareSingleWork(t, work, a, b, false)
	}
	return compareSchemaEqual(a.schema, b.schema, true), nil
}

// hashEqual is the stored-hash gate of lyd_compare_single_data (tree_data.c:1714-1716).
// Canonical value and declared-key comparisons follow separately. In particular, a
// cross-type terminal duplicate retains its source hash even when its value changes type.
func hashEqual(a, b *Node) bool { return a.hash == b.hash }

// hashValueEqual is the value part of lyd_hash (its LYB bytes, not the canonical text) for two
// values that may come from different contexts.
func hashValueEqual(x, y types.Value) bool {
	if x.Type() == y.Type() {
		return types.Equal(x, y)
	}
	xh, xok := types.HashLYB(0, x)
	yh, yok := types.HashLYB(0, y)
	if xok && yok {
		return xh == yh
	}
	// A type-specific plugin without an LYB representation here can only be
	// safely compared across snapshots when its base type also agrees.
	return x.Type() != nil && y.Type() != nil && x.Type().Base == y.Type().Base &&
		x.Canonical() == y.Canonical()
}

// compareSingle is lyd_compare_single; full is LYD_COMPARE_FULL_RECURSION (Node.Equal for all
// the options).
func compareSingle(t *Tree, a, b *Node, full bool) bool {
	equal, _ := compareSingleWork(t, nil, a, b, full)
	return equal
}

func compareSingleErr(t *Tree, a, b *Node, full bool) (bool, error) {
	if t.work.hashDepth != 0 {
		return compareSingleWork(t, &t.work, a, b, full)
	}
	// Read-only comparisons must not mutate the queried tree: concurrent readers
	// share it. Their comparison still has an enforced private cumulative budget.
	var work workCounter
	return compareSingleWork(t, &work, a, b, full)
}

func compareSingleWork(t *Tree, work *workCounter, a, b *Node, full bool) (bool, error) {
	return compareSingleCheckedWork(t, work, a, b, CompareOptions{FullRecursion: full}, false)
}

// compareSingleCheckedWork implements lyd_compare_single_schema followed by the
// iterative lyd_compare_single_data/lyd_compare_siblings_ walk. Any payloads
// are always compared as sibling forests, irrespective of the outer
// FullRecursion option. With a non-nil work counter, every candidate comparison
// and recursively visited node consumes the operation's cumulative stored-work
// budget, so digest collisions cannot turn merge or diff into unbounded work.
func compareSingleCheckedWork(t *Tree, work *workCounter, a, b *Node, o CompareOptions, parentsChecked bool) (bool, error) {
	if err := chargeComparison(work); err != nil {
		return false, err
	}
	if !compareSingleSchema(a, b, o.Opaque, parentsChecked) {
		return false, nil
	}
	return compareDataStack(t, work, []compareFrame{{a: a, b: b, opts: o}})
}

func chargeComparison(work *workCounter) error {
	if work == nil {
		return nil
	}
	return work.chargeHash(1)
}

func workError(work *workCounter) error {
	if work == nil {
		return nil
	}
	return work.hashErr
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
		for _, k := range n.kids.SchemaAll() {
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

func (x *compareIndex) first(t *Tree, work *workCounter, s *siblings, current, target *Node) (*Node, error) {
	if current.schema != nil && target.schema != nil &&
		!compareSchemaEqual(current.schema.DataParent(), target.schema.DataParent(), true) {
		return nil, nil // keep lyd_find_sibling_first's initial schema-parent check
	}
	if n, ok := x.matched[target]; ok {
		return n, nil
	}
	canonical := compareInstanceKey(target, false, false)
	if !x.mixed[canonical] && x.types[canonical] == compareInstanceKey(target, true, false) {
		// One declared type per value position narrows the search to the
		// selected union member, while typed equality resolves collisions.
		key := compareInstanceKey(target, true, true)
		for _, n := range x.exact[key] {
			s.visit()
			equal, err := compareSingleWork(t, work, n, target, false)
			if err != nil {
				return nil, err
			}
			if equal {
				x.matched[target] = n
				return n, nil
			}
		}
		// Stored hashes decide equality, not the selected member: a duplicate
		// keeps its hash while its union member may be reselected
		// (hash-union-key-member-dup-merge). Scan the canonical bucket on a miss.
	}
	for _, n := range x.buckets[canonical] {
		s.visit()
		equal, err := compareSingleWork(t, work, n, target, false)
		if err != nil {
			return nil, err
		}
		if equal {
			x.matched[target] = n
			return n, nil
		}
	}
	x.matched[target] = nil
	return nil, nil
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
func compareSiblingFrames(t *Tree, work *workCounter, stack []compareFrame, a, b *siblings, o CompareOptions) ([]compareFrame, bool, error) {
	if a.len() != b.len() {
		return stack, false, nil
	}
	al, bl := a.nodes(), b.nodes()
	var index *compareIndex
	for i, n := range al {
		m := bl[i]
		if err := chargeComparison(work); err != nil {
			return stack, false, err
		}
		if !compareSingleSchema(n, m, o.Opaque, true) {
			return stack, false, nil
		}
		if compareNeedsLookup(n) {
			if b.ht == nil {
				if index == nil {
					index = newCompareIndex(bl)
				}
				var err error
				m, err = index.first(t, work, b, m, n)
				if err != nil {
					return stack, false, err
				}
			} else {
				var err error
				m, err = t.findFirstWork(b, n, work)
				if err != nil {
					return stack, false, err
				}
			}
			if m == nil {
				return stack, false, nil
			}
		}
		o.FullRecursion = true
		stack = append(stack, compareFrame{n, m, o})
	}
	return stack, true, nil
}

func compareDataStack(t *Tree, work *workCounter, stack []compareFrame) (bool, error) {
	for len(stack) != 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		a, b, o := f.a, f.b, f.opts
		if err := chargeComparison(work); err != nil {
			return false, err
		}
		if !o.Opaque && !hashEqual(a, b) {
			return false, nil
		}
		var ac, bc *siblings
		switch {
		case a.schema == nil || b.schema == nil:
			if !o.Opaque && (a.schema == nil) != (b.schema == nil) {
				return false, nil
			}
			if a.schema == nil && b.schema == nil || a.isTerm() || b.isTerm() {
				if !compareNodeValue(a, b) {
					return false, nil
				}
			}
			if o.FullRecursion {
				ac, bc = &a.kids, &b.kids
			}
		case a.isTerm():
			if o.Defaults && a.flags&FlagDefault != b.flags&FlagDefault {
				return false, nil
			}
			if a.value.Canonical() != b.value.Canonical() {
				return false, nil
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
				return false, nil
			}
			if ach {
				ac, bc = &a.any.children, &b.any.children
			} else if av.text == nil || bv.text == nil || *av.text != *bv.text {
				return false, nil
			}
		case a.schema.Kind == schema.List && !o.FullRecursion:
			if a.schema.Keyless() {
				continue
			}
			for i := range a.schema.Keys {
				x, y := positionalChild(a, i), positionalChild(b, i)
				if x == nil || y == nil {
					if x != y {
						return false, nil
					}
					break
				}
				if err := chargeComparison(work); err != nil {
					return false, err
				}
				if !compareSingleSchema(x, y, o.Opaque, true) {
					return false, nil
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
			var err error
			if stack, ok, err = compareSiblingFrames(t, work, stack, ac, bc, o); err != nil || !ok {
				return false, err
			}
		}
	}
	return true, nil
}

// mixedLeaderProbe is leaderFirstMixed(sn) as markUnordered's leaderMaterialize would leave it,
// read-only: existing records precede every record it appends (leaderSeq grows), and it
// appends each ordinary list or leaf-list run start in logical order.
func (s *siblings) mixedLeaderProbe(sn *schema.Node) *Node {
	if leader := s.leaderFirstMixed(sn); leader != nil {
		return leader
	}
	var prev *schema.Node
	for _, n := range s.logical().All() {
		s.visit()
		if n.schema != nil && n.schema != prev && !opChild(n.schema) &&
			(n.schema.Kind == schema.List || n.schema.Kind == schema.LeafList) &&
			s.leaderByNode[n] == nil && compareSchemaEqual(sn, n.schema, true) {
			return n
		}
		prev = n.schema
	}
	return nil
}
