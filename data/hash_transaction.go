// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"maps"
	"slices"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

// hashTransaction is the undo journal of one mutating operation: a late hash-budget error
// restores the tree and its indexes as they were. libyang keeps no snapshot (it touches only the
// matched nodes); the journal is lazy to match that cost: a sibling set (with its index records
// and the node structs in it) or a single node is copied the first time the operation is about to
// change it (siblings.touch, Node.touch), and every copy is charged to the tree's hash-work budget.
// Unchanged subtrees, however large, are never visited. New nodes become unreachable when their
// old sibling sets are restored; no failed operation returns a reference to them.
type hashTransaction struct {
	tree *Tree
	err  error  // a journal charge failed: the operation fails with it and rolls back
	done func() // ends the hash-work scope opened by beginHashTransaction
	rank map[*schema.Node]int
	// orders are an operation's input or output children (opOrder), schema-sized
	orders   map[orderKey][]*schema.Node
	nodes    map[*Node]Node
	siblings map[*siblings]siblings
	chains   map[*recordChain]recordChain
	records  map[*record]record
	op       map[*opEntry]opEntry
	pending  map[*dirtyOrder]dirtyOrder
	dirty    map[*dirtyOrderNode]dirtyOrderNode
	links    map[*leafrefLinks]leafrefLinks
	// owners are the link-record pointers of the counterparts of journaled nodes; a
	// counterpart may be in another tree (a moved subtree keeps its links), and freeLinks
	// drops an emptied record.
	owners map[*Node]*leafrefLinks
	metas  map[*meta]meta // lyd_change_meta edits a metadata instance in place
	opaq   map[*opaque]opaque
	// works are the work counters of the sibling sets of subtrees rebound to another tree
	// (bindSubtreeWork) and not journaled otherwise
	works map[*siblings]*workCounter
	// lent are the work counters of trees without a transaction of their own that a subtree of
	// this tree moved into (validateOp's temporary tree): changes there are journaled here
	lent []*workCounter
}

// beginHashTransaction starts journaling t for an operation that may change any part of it. It
// returns nil (whose methods do nothing) inside an operation that already journals t: the
// enclosing transaction records the changes and restores them, a budget error being sticky for
// the whole operation. The caller ends it with end.
func beginHashTransaction(t *Tree) *hashTransaction {
	if t.work.journal != nil {
		return nil
	}
	j := &hashTransaction{tree: t, done: t.work.beginHashWork(), rank: maps.Clone(t.rank),
		nodes: map[*Node]Node{}, siblings: map[*siblings]siblings{}, chains: map[*recordChain]recordChain{},
		records: map[*record]record{}, op: map[*opEntry]opEntry{},
		pending: map[*dirtyOrder]dirtyOrder{}, dirty: map[*dirtyOrderNode]dirtyOrderNode{},
		links: map[*leafrefLinks]leafrefLinks{}, owners: map[*Node]*leafrefLinks{},
		metas: map[*meta]meta{}, opaq: map[*opaque]opaque{}, works: map[*siblings]*workCounter{}}
	objects := uint64(len(t.rank)) + 1
	if t.orders != nil {
		j.orders = make(map[orderKey][]*schema.Node, len(t.orders))
		for key, value := range t.orders {
			j.orders[key] = slices.Clone(value)
			objects += uint64(len(value)) + 1
		}
	}
	t.work.journal = j
	j.charge(objects)
	return j
}

// end detaches the transaction from its tree. A failed journal charge becomes the operation's
// result, and a budget failure restores the tree. A nil result keeps the changes.
func (j *hashTransaction) end(result *error) {
	if j == nil {
		return
	}
	j.tree.work.journal = nil
	for _, w := range j.lent {
		if w.journal == j {
			w.journal = nil
		}
	}
	if result != nil {
		j.fail(result)
		if errors.Is(*result, yang.ErrBudget) {
			j.restore()
		}
	}
	j.done()
}

// fail makes a failed journal charge the operation's result.
func (j *hashTransaction) fail(result *error) {
	if j != nil && *result == nil && j.err != nil {
		*result = j.err
	}
}

// charge charges journaled objects to the tree's hash-work budget: the copy is work proportional
// to them, like the hashing it protects. The copy is already complete when the charge fails, so
// the operation can still be rolled back exactly.
func (j *hashTransaction) charge(objects uint64) {
	if err := j.tree.work.chargeHash(objects); err != nil && j.err == nil {
		j.err = err
	}
}

// journal returns the transaction of the tree s belongs to, if one is running.
func (s *siblings) journal() *hashTransaction {
	if s.work == nil {
		return nil
	}
	return s.work.journal
}

// touch journals s before it changes: its indexes and the structs of the nodes in it.
func (s *siblings) touch() {
	if j := s.journal(); j != nil {
		j.captureSiblings(s)
	}
}

// touch journals s, a sibling set of t, before it changes (also before its first link binds it
// to t).
func (t *Tree) touch(s *siblings) {
	if j := t.work.journal; j != nil {
		j.captureSiblings(s)
	}
}

// touch journals n before its own fields (value, flags, metadata, links, stored hash) change.
func (n *Node) touch() {
	j := n.kids.journal()
	if j == nil {
		if t := n.treeOf(); t != nil {
			j = t.work.journal
		}
	}
	if j == nil {
		return
	}
	if _, exists := j.nodes[n]; !exists {
		var objects uint64
		j.captureNode(n, &objects)
		j.charge(objects)
	}
}

// rebind records the work counter of s before bindSubtreeWork moves it to another tree.
func (s *siblings) rebind(work *workCounter) {
	if s.work == work {
		return
	}
	if from := s.journal(); from != nil && work.journal == nil {
		work.journal = from
		from.lent = append(from.lent, work)
	}
	for _, j := range [2]*hashTransaction{s.journal(), work.journal} {
		if j == nil {
			continue
		}
		if _, exists := j.siblings[s]; exists {
			continue
		}
		if _, exists := j.works[s]; !exists {
			j.works[s] = s.work
			j.charge(1)
		}
	}
}

func cloneDeque(d nodeDeque) nodeDeque {
	return nodeDeque{list: slices.Clone(d.list)}
}

func cloneRanks(in [2]rankBits) (out [2]rankBits) {
	for i := range in {
		out[i].bits = in[i].bits
		out[i].levels = make([][]uint64, len(in[i].levels))
		for j, level := range in[i].levels {
			out[i].levels[j] = slices.Clone(level)
		}
	}
	return out
}

// captureNode journals n itself, not its children: its sibling set is journaled on its own.
func (j *hashTransaction) captureNode(n *Node, objects *uint64) {
	if _, exists := j.nodes[n]; exists {
		return
	}
	saved := *n
	saved.meta = slices.Clone(n.meta)
	saved.lkeyFrozen = slices.Clone(n.lkeyFrozen)
	j.nodes[n] = saved
	*objects += 1 + uint64(len(n.meta))
	for _, m := range n.meta {
		j.metas[m] = *m
	}
	if n.links != nil {
		j.captureLinks(n.links, objects)
		for _, c := range n.links.leafrefs {
			j.captureOwner(c, objects)
		}
		for _, c := range n.links.targets {
			j.captureOwner(c, objects)
		}
	}
	if n.opaq != nil {
		op := *n.opaq
		op.Attrs = slices.Clone(op.Attrs)
		j.opaq[n.opaq] = op
	}
}

func (j *hashTransaction) captureLinks(l *leafrefLinks, objects *uint64) {
	if _, exists := j.links[l]; exists {
		return
	}
	j.links[l] = leafrefLinks{leafrefs: slices.Clone(l.leafrefs), targets: slices.Clone(l.targets)}
	*objects += 1 + uint64(len(l.leafrefs)+len(l.targets))
}

// captureOwner journals a link counterpart's record pointer and record, wherever it is linked:
// unlinking a journaled node removes it from its counterparts' records.
func (j *hashTransaction) captureOwner(c *Node, objects *uint64) {
	if _, exists := j.owners[c]; exists {
		return
	}
	j.owners[c] = c.links
	*objects++
	if c.links != nil {
		j.captureLinks(c.links, objects)
	}
}

// captureRecords journals the records reachable from r: its hash chain (prev/next) and its
// ordered index (left/right). A chain can be as long as the sibling set (duplicate values of a
// state leaf-list), so the walk keeps an explicit stack instead of recursing.
func (j *hashTransaction) captureRecords(r *record, objects *uint64) {
	stack := []*record{r}
	for len(stack) != 0 {
		r := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if r == nil {
			continue
		}
		if _, exists := j.records[r]; exists {
			continue
		}
		saved := *r
		saved.more = slices.Clone(r.more)
		j.records[r] = saved
		*objects++
		stack = append(stack, r.prev, r.next, r.left, r.right)
	}
}

func (j *hashTransaction) captureChain(c *recordChain, objects *uint64) {
	if c == nil {
		return
	}
	if _, exists := j.chains[c]; exists {
		return
	}
	j.chains[c] = *c
	*objects++
	j.captureRecords(c.first, objects)
	j.captureRecords(c.root, objects)
}

// captureDirty journals the nodes of a parser's dirty-run tree reachable from n, with an
// explicit stack like captureRecords.
func (j *hashTransaction) captureDirty(n *dirtyOrderNode, objects *uint64) {
	stack := []*dirtyOrderNode{n}
	for len(stack) != 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == nil {
			continue
		}
		if _, exists := j.dirty[n]; exists {
			continue
		}
		j.dirty[n] = *n
		*objects++
		stack = append(stack, n.left, n.right, n.parent)
	}
}

// captureSiblings journals s with its index records and the structs of the nodes in it (index
// changes update their positions, hashes and RB-tree state), not their children.
// ponytail: a sibling set is copied whole, O(its size) on the first change; a per-record journal
// would make a change in a huge sibling set O(1) if that ever matters.
func (j *hashTransaction) captureSiblings(s *siblings) {
	if _, exists := j.siblings[s]; exists {
		return
	}
	saved := *s
	saved.nodeDeque = cloneDeque(s.nodeDeque)
	saved.opq = slices.Clone(s.opq)
	saved.ht = maps.Clone(s.ht)
	saved.exactByNode = maps.Clone(s.exactByNode)
	saved.history = nil
	if s.history != nil {
		saved.history = make(map[*Node]map[uint32]*record, len(s.history))
		for n, byHash := range s.history {
			saved.history[n] = maps.Clone(byHash)
		}
	}
	saved.leaders = maps.Clone(s.leaders)
	saved.leaderByNode = maps.Clone(s.leaderByNode)
	saved.firstRun = maps.Clone(s.firstRun)
	saved.unsorted = maps.Clone(s.unsorted)
	saved.pending = maps.Clone(s.pending)
	saved.rbTree = maps.Clone(s.rbTree)
	saved.rbGroups = maps.Clone(s.rbGroups)
	saved.opIdx = maps.Clone(s.opIdx)
	saved.opRanks = cloneRanks(s.opRanks)
	j.siblings[s] = saved
	objects := 1 + uint64(len(s.list)+len(s.opq)+len(s.ht)+len(s.leaders)+len(s.firstRun)+
		len(s.exactByNode)+len(s.history)+len(s.leaderByNode)+len(s.unsorted)+len(s.rbTree)+len(s.rbGroups)+
		len(s.opIdx)+len(s.pending))
	for _, c := range s.ht {
		j.captureChain(c, &objects)
	}
	for _, c := range s.leaders {
		j.captureChain(c, &objects)
	}
	for _, r := range s.exactByNode {
		j.captureRecords(r, &objects)
	}
	for _, r := range s.leaderByNode {
		j.captureRecords(r, &objects)
	}
	for _, byHash := range s.history {
		for _, r := range byHash {
			j.captureRecords(r, &objects)
		}
	}
	for _, op := range s.opIdx {
		saved := *op
		saved.nodeDeque = cloneDeque(op.nodeDeque)
		j.op[op] = saved
		objects += uint64(len(op.list))
	}
	for _, p := range s.pending {
		saved := *p
		saved.nodes = maps.Clone(p.nodes)
		saved.leaders = slices.Clone(p.leaders)
		j.pending[p] = saved
		objects += uint64(len(p.nodes) + len(p.leaders))
		j.captureDirty(p.root, &objects)
		for _, n := range p.nodes {
			j.captureDirty(n, &objects)
		}
	}
	for _, n := range s.list {
		j.captureNode(n, &objects)
	}
	for _, n := range s.opq {
		j.captureNode(n, &objects)
	}
	j.charge(objects)
}

func (j *hashTransaction) restore() {
	for n, saved := range j.nodes {
		// a journaled sibling set is restored below; the others are as they were (the operation
		// did not change them)
		kids := n.kids
		*n = saved
		n.kids = kids
	}
	for c, l := range j.owners {
		c.links = l
	}
	for m, saved := range j.metas {
		*m = saved
	}
	for s, saved := range j.siblings {
		*s = saved
	}
	// after the sibling sets: a set rebound before its capture was captured with the new counter
	for s, work := range j.works {
		s.work = work
	}
	for c, saved := range j.chains {
		*c = saved
	}
	for r, saved := range j.records {
		*r = saved
	}
	for op, saved := range j.op {
		*op = saved
	}
	for p, saved := range j.pending {
		*p = saved
	}
	for n, saved := range j.dirty {
		*n = saved
	}
	for l, saved := range j.links {
		*l = saved
	}
	for op, saved := range j.opaq {
		*op = saved
	}
	j.tree.rank, j.tree.orders = j.rank, j.orders
}
