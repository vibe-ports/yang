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
	err  error // a journal charge failed: the operation fails with it and rolls back
	// rollback makes any failure restore, not only a budget one: an operation that could not
	// undo its own temporary change (validateOpNode's refused cleanup) sets it
	rollback bool
	done     func() // ends the hash-work scope opened by beginHashTransaction
	rank     map[*schema.Node]int
	set      *schema.Set // the tree's snapshot: a diff create may retain another context's root
	// orders are an operation's input or output children (opOrder), schema-sized
	orders   map[orderKey][]*schema.Node
	nodes    map[*Node]Node
	siblings map[*siblings]siblings
	chains   map[*recordChain]recordChain
	records  map[*record]record
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
	// detached are the detached roots captureDetachedParent journaled whole: a later boundary of
	// the same operation (Insert, then linkOrigin) does not walk them again
	detached map[*Node]bool
	// narrow is NewPath's journal (design 12, "NewPath mutation journal"): a narrow-aware
	// primitive records each slot, field or struct it changes as an entry of undo instead of
	// copying its sibling set whole; every other primitive still captures whole (touch).
	narrow bool
	fresh  map[*Node]bool // the nodes constructed under the narrow journal: never captured
	undo   []func()       // restored in reverse after the whole captures
	// placed are the topology nodes an RB insertion may change, the bulk of a narrow journal's
	// entries: kept by value (restored in reverse, after undo) instead of one closure each
	placed []placedNode
}

type placedNode struct {
	x   *dirtyOrderNode
	old dirtyOrderNode
}

// beginHashTransaction starts journaling t for an operation that may change any part of it. It
// returns nil (whose methods do nothing) inside an operation that already journals t: the
// enclosing transaction records the changes and restores them, a budget error being sticky for
// the whole operation. The caller ends it with end.
func beginHashTransaction(t *Tree) *hashTransaction {
	if t.work.journal != nil {
		return nil
	}
	j := &hashTransaction{tree: t, done: t.work.beginHashWork(), rank: t.rank, orders: t.orders, set: t.set}
	j.maps()
	t.work.journal = j
	objects := uint64(len(t.rank)) + 1
	if j.charge(uint64(len(t.orders))) != nil {
		return j
	}
	for _, value := range t.orders {
		objects = addHashBound(objects, uint64(len(value))+1)
	}
	if j.charge(objects) != nil {
		return j
	}
	j.rank = maps.Clone(t.rank)
	if t.orders != nil {
		j.orders = make(map[orderKey][]*schema.Node, len(t.orders))
		for key, value := range t.orders {
			j.orders[key] = slices.Clone(value)
		}
	}
	return j
}

// beginNarrowTransaction starts NewPath's journal (nil inside an enclosing transaction, as
// beginHashTransaction). The schema-derived caches t.rank and t.orders keep their identity: an
// entry they gain is the same for any later call, so nothing is copied up front.
func beginNarrowTransaction(t *Tree) *hashTransaction {
	if t.work.journal != nil {
		return nil
	}
	j := t.work.spare
	if j == nil {
		j = &hashTransaction{}
	}
	t.work.spare = nil
	*j = hashTransaction{tree: t, done: t.work.beginHashWork(), rank: t.rank, orders: t.orders, set: t.set,
		narrow: true, undo: j.undo[:0], placed: j.placed[:0], fresh: j.fresh}
	t.work.journal = j
	return j
}

// spare keeps a finished narrow journal that captured nothing whole, its entries cleared, for the
// tree's next NewPath. ponytail: up to 1024 entries; a larger journal is left to the collector.
func (j *hashTransaction) spare() {
	if !j.narrow || j.nodes != nil || cap(j.undo) > 1024 || cap(j.placed) > 1024 || len(j.fresh) > 64 {
		return
	}
	clear(j.undo)
	clear(j.placed)
	t := j.tree
	clear(j.fresh)
	*j = hashTransaction{undo: j.undo[:0], placed: j.placed[:0], fresh: j.fresh}
	t.work.spare = j
}

// maps allocates the whole-capture maps, which a narrow journal leaves nil (reads of a nil map
// see no capture) until its first whole capture.
func (j *hashTransaction) maps() {
	if j.nodes != nil {
		return
	}
	j.nodes, j.siblings, j.chains, j.records = map[*Node]Node{}, map[*siblings]siblings{}, map[*recordChain]recordChain{}, map[*record]record{}
	j.pending, j.dirty = map[*dirtyOrder]dirtyOrder{}, map[*dirtyOrderNode]dirtyOrderNode{}
	j.links, j.owners = map[*leafrefLinks]leafrefLinks{}, map[*Node]*leafrefLinks{}
	j.metas, j.opaq, j.works = map[*meta]meta{}, map[*opaque]opaque{}, map[*siblings]*workCounter{}
}

// narrow returns the narrow journal that records the changes of s entry by entry: nil without
// a journal, in a whole-set journal, or once s is captured whole (an entry after a whole capture
// would restore over it; see restore).
func (s *siblings) narrow() *hashTransaction {
	j := s.journal()
	if j == nil || !j.narrow {
		return nil
	}
	if _, whole := j.siblings[s]; whole {
		return nil
	}
	return j
}

// touchLazy is touch for a narrow-aware primitive: under a narrow journal it captures nothing,
// and the primitive records its own writes (or calls touch before a branch it does not record).
func (s *siblings) touchLazy() error {
	if j := s.narrow(); j != nil {
		return j.err
	}
	return s.touch()
}

// touchWhole captures s whole before a write a narrow-aware primitive does not record. Outside
// a narrow journal the primitive's touchLazy has already captured s, as before.
func (s *siblings) touchWhole() error {
	if s.narrow() != nil {
		return s.touch()
	}
	return nil
}

// record prepays an entry, its capture and its restoration, before the old state is read.
func (j *hashTransaction) record(units uint64) error {
	if j.err != nil {
		return j.err
	}
	return j.charge(2 * units)
}

// recordField journals *p. The entry addresses the live field, so a later whole capture of its
// owner, restored first, does not detach it.
func recordField[T any](j *hashTransaction, p *T) error {
	if err := j.record(1); err != nil {
		return err
	}
	old := *p
	j.undo = append(j.undo, func() { *p = old })
	return nil
}

// recordSlot journals the slot k of the map field *p (or the field itself while it is nil).
func recordSlot[K comparable, V any](j *hashTransaction, p *map[K]V, k K) error {
	if err := j.record(1); err != nil {
		return err
	}
	if *p == nil {
		j.undo = append(j.undo, func() { *p = nil })
		return nil
	}
	old, had := (*p)[k]
	j.undo = append(j.undo, func() {
		switch {
		case had:
			(*p)[k] = old
		case *p != nil:
			delete(*p, k)
		}
	})
	return nil
}

// recordChainEntry journals the fields of chain c (nil: new or none).
func (j *hashTransaction) recordChainEntry(c *recordChain) error {
	if c == nil {
		return nil
	}
	if _, whole := j.chains[c]; whole {
		return j.err
	}
	if err := j.record(1); err != nil {
		return err
	}
	old := *c
	j.undo = append(j.undo, func() { *c = old })
	return nil
}

// recordRecord journals one record, its later registrations included.
func (j *hashTransaction) recordRecord(r *record) error {
	if r == nil {
		return nil
	}
	if _, whole := j.records[r]; whole {
		return j.err
	}
	if err := j.record(1 + uint64(len(r.more))); err != nil {
		return err
	}
	old := *r
	old.more = slices.Clone(r.more)
	j.undo = append(j.undo, func() { *r = old })
	return nil
}

// recordRecordPath journals the records an AVL insertion of order into root can change: those on
// its search path, which balance and its rotations rewrite, and both children of each.
func (j *hashTransaction) recordRecordPath(root *record, order uint64) error {
	for r := root; r != nil; {
		for _, x := range [3]*record{r, r.left, r.right} {
			if err := j.recordRecord(x); err != nil {
				return err
			}
		}
		if order < r.order {
			r = r.left
		} else {
			r = r.right
		}
	}
	return nil
}

// recordPlace journals d's root, nodes slot and armed undo before d.place(n, parent, …), and
// prepays the topology nodes place then records itself (dirtyOrder.keep, d.narrow set by the
// caller around place): each ancestor of the leaf position (its size), and the nodes
// rb_insert_color recolours (at most three per step up) and its two rotations move (at most four
// each). A graph captured whole needs nothing.
func (j *hashTransaction) recordPlace(d *dirtyOrder, n *Node, parent *dirtyOrderNode) error {
	if _, whole := j.pending[d]; whole {
		return j.err
	}
	if err := recordField(j, &d.root); err != nil {
		return err
	}
	if d.undo != nil { // place drops an armed removal undo
		if err := recordField(j, &d.undo); err != nil {
			return err
		}
	}
	if err := recordSlot(j, &d.nodes, n); err != nil {
		return err
	}
	depth := 0
	for a := parent; a != nil; a = a.parent {
		depth++
	}
	bound := 4*depth + 12
	if err := j.record(uint64(bound)); err != nil {
		return err
	}
	j.placed = slices.Grow(j.placed, depth+8) // the usual count: a size per ancestor, one recolouring
	return nil
}

// end detaches the transaction from its tree. A failed journal charge becomes the operation's
// result, and a budget failure (any failure with rollback set) restores the tree. A nil result
// keeps the changes.
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
		if errors.Is(*result, yang.ErrBudget) || j.rollback && *result != nil {
			j.restore()
		}
	}
	j.done()
	j.spare()
}

// fail makes a failed journal charge the operation's result.
func (j *hashTransaction) fail(result *error) {
	if j == nil {
		return
	}
	if errors.Is(j.tree.work.hashErr, yang.ErrBudget) {
		*result = j.tree.work.hashErr
	} else if j.err != nil && (*result == nil || errors.Is(j.err, yang.ErrBudget)) {
		*result = j.err
	}
}

// charge charges journaled objects to the tree's hash-work budget: the copy is work proportional
// to them, like the hashing it protects. Reserve before copying; touch refuses the mutation
// when reservation fails. Earlier completed captures remain available for rollback.
func (j *hashTransaction) charge(objects uint64) error {
	if err := j.tree.work.chargeHash(objects); err != nil {
		if j.err == nil {
			j.err = err
		}
		return err
	}
	return nil
}

// journal returns the transaction of the tree s belongs to, if one is running.
func (s *siblings) journal() *hashTransaction {
	if s.work == nil {
		return nil
	}
	return s.work.journal
}

// touch journals s before it changes: its indexes and the structs of the nodes in it.
func (s *siblings) touch() error {
	if j := s.journal(); j != nil {
		return j.captureSiblings(s)
	}
	return nil
}

// touch journals s, a sibling set of t, before it changes (also before its first link binds it
// to t).
func (t *Tree) touch(s *siblings) error {
	if j := t.work.journal; j != nil {
		return j.captureSiblings(s)
	}
	return nil
}

// touch journals n before its own fields (value, flags, metadata, links, stored hash) change.
func (n *Node) touch() error {
	j := childStorage(n).journal()
	if j == nil {
		if t := n.treeOf(); t != nil {
			j = t.work.journal
		}
	}
	if j == nil {
		return nil
	}
	if j.fresh[n] {
		return j.err // NewPath's own new nodes: a refusal leaves no reference to them
	}
	return j.captureNode(n)
}

// created registers a node constructed under a narrow journal (newTerm, newInner): NewPath
// created it, so its fields need no capture.
func (w *workCounter) created(n *Node) {
	if w == nil || w.journal == nil || !w.journal.narrow {
		return
	}
	j := w.journal
	if j.fresh == nil {
		j.fresh = map[*Node]bool{}
	}
	j.fresh[n] = true
}

// joinLinkJournal associates both sides before either a linked or detached source changes.
// A detached subtree can still belong to an outer transaction through its work binding.
func joinLinkJournal(t *Tree, n *Node) error {
	from := childStorage(n).journal()
	source := n.treeOf()
	if from == nil && source != nil {
		from = source.work.journal
	}
	lend := func(j *hashTransaction, w *workCounter) error {
		if w.journal != nil {
			return nil
		}
		if err := j.charge(1); err != nil {
			return err
		}
		w.journal = j
		j.lent = append(j.lent, w)
		return nil
	}
	if t.work.journal == nil && from != nil {
		if err := lend(from, &t.work); err != nil {
			return err
		}
	}
	if j := t.work.journal; j != nil {
		if source != nil {
			if err := lend(j, &source.work); err != nil {
				return err
			}
		} else if err := j.captureDetachedParent(n.parent); err != nil {
			// a detached source tree changes on unlink and recovery: journal it whole
			return err
		}
		return j.captureNode(n)
	}
	return nil
}

// setWork binds s and its retained RB topologies to work: a topology must not keep the tree it
// was built in alive, or charge it, after its subtree moved to another tree.
func (s *siblings) setWork(work *workCounter) {
	s.work = work
	for _, d := range s.rbOrders {
		d.work = work
	}
	for _, d := range s.pending {
		d.work = work
	}
}

// rebind records the work counter of s before bindSubtreeWork moves it to another tree.
func (s *siblings) rebind(work *workCounter) error {
	if s.work == work {
		return nil
	}
	if from := s.journal(); from != nil && work.journal == nil {
		if err := from.charge(1); err != nil {
			return err
		}
		work.journal = from
		from.lent = append(from.lent, work)
	}
	for _, j := range [2]*hashTransaction{s.journal(), work.journal} {
		if j == nil {
			continue
		}
		j.maps()
		if _, exists := j.siblings[s]; exists {
			continue
		}
		if _, exists := j.works[s]; !exists {
			if err := j.charge(1); err != nil {
				return err
			}
			j.works[s] = s.work
		}
	}
	return nil
}

// captureDetachedParent journals the whole detached tree parent belongs to, at its operation's
// boundary and before the first mutation: every node and sibling set reachable from its root,
// any payloads included. Their counters have no journal, so a lazy touch there records nothing;
// an operation may reach any of them (the chain's default flags, a list parent's hash, a matched
// descendant a diff merge replaces). captureSiblings charges each set and its nodes before the
// walk continues into them. A linked parent is journaled lazily by its tree's transaction
// instead. Design 12 states the invariant: every node a mutation can reach is under the active
// journal before it changes.
func (j *hashTransaction) captureDetachedParent(parent *Node) error {
	if j == nil || parent == nil {
		return nil
	}
	if j.err != nil {
		return j.err
	}
	root := parent
	for root.parent != nil {
		if err := j.charge(1); err != nil {
			return err
		}
		if j.tree.work.visits { // tests only
			j.tree.work.Add(1)
		}
		root = root.parent
	}
	if root.tree != nil || j.detached[root] {
		return nil
	}
	if err := j.charge(1); err != nil { // the record below
		return err
	}
	if err := j.captureNode(root); err != nil {
		return err
	}
	stack := []*Node{root}
	for len(stack) != 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		s := childStorage(n)
		if _, captured := j.siblings[s]; captured {
			// journaled already (a lazy touch): its children are still walked, so charge them
			if err := j.charge(1 + uint64(s.SchemaLen()+s.OpaqueLen())); err != nil { //nolint:gosec // sibling counts are nonnegative
				return err
			}
		} else if err := j.captureSiblings(s); err != nil {
			return err
		}
		stack = s.schemaSpan().AppendTo(stack)
		stack = s.opaqueSpan().AppendTo(stack)
	}
	if j.detached == nil {
		j.detached = map[*Node]bool{}
	}
	j.detached[root] = true
	return nil
}

// captureNode journals n itself, not its children: its sibling set is journaled on its own.
func (j *hashTransaction) captureNode(n *Node) error {
	j.maps()
	if _, exists := j.nodes[n]; exists {
		return j.err
	}
	objects := 1 + uint64(2*len(n.meta)+len(n.lkeyFrozen)+len(n.carriedRB))
	if n.opaq != nil {
		objects += 1 + uint64(len(n.opaq.Attrs))
	}
	if err := j.charge(objects); err != nil {
		return err
	}
	saved := *n
	saved.meta = slices.Clone(n.meta)
	saved.lkeyFrozen = slices.Clone(n.lkeyFrozen)
	saved.carriedRB = slices.Clone(n.carriedRB)
	j.nodes[n] = saved
	for _, m := range n.meta {
		j.metas[m] = *m
	}
	if n.opaq != nil {
		op := *n.opaq
		op.Attrs = slices.Clone(op.Attrs)
		j.opaq[n.opaq] = op
	}
	for _, c := range n.carriedRB {
		if err := j.captureOrder(c.order); err != nil {
			return err
		}
	}
	if n.links != nil {
		if err := j.captureLinks(n.links); err != nil {
			return err
		}
		for _, c := range n.links.leafrefs {
			if err := j.captureOwner(c); err != nil {
				return err
			}
		}
		for _, c := range n.links.targets {
			if err := j.captureOwner(c); err != nil {
				return err
			}
		}
	}
	return nil
}

func (j *hashTransaction) captureLinks(l *leafrefLinks) error {
	j.maps()
	if _, exists := j.links[l]; exists {
		return nil
	}
	if err := j.charge(1 + uint64(len(l.leafrefs)+len(l.targets))); err != nil {
		return err
	}
	j.links[l] = leafrefLinks{leafrefs: slices.Clone(l.leafrefs), targets: slices.Clone(l.targets)}
	return nil
}

// captureOwner includes counterparts in another tree: freeLinks can empty their record.
func (j *hashTransaction) captureOwner(c *Node) error {
	j.maps()
	if _, exists := j.owners[c]; exists {
		return nil
	}
	if err := j.charge(1); err != nil {
		return err
	}
	j.owners[c] = c.links
	if c.links != nil {
		return j.captureLinks(c.links)
	}
	return nil
}

// captureRecords walks each record once. Reserve each record and its outgoing stack slots
// before allocating; a duplicate-heavy chain cannot allocate beyond the remaining quota.
func (j *hashTransaction) captureRecords(r *record) error {
	j.maps()
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
		if err := j.charge(5 + uint64(len(r.more))); err != nil {
			return err
		}
		saved := *r
		saved.more = slices.Clone(r.more)
		j.records[r] = saved
		stack = append(stack, r.prev, r.next, r.left, r.right)
	}
	return nil
}

func (j *hashTransaction) captureChain(c *recordChain) error {
	j.maps()
	if c == nil {
		return nil
	}
	if _, exists := j.chains[c]; exists {
		return nil
	}
	if err := j.charge(1); err != nil {
		return err
	}
	j.chains[c] = *c
	if err := j.captureRecords(c.first); err != nil {
		return err
	}
	return j.captureRecords(c.root)
}

// captureOrder saves the authoritative graph in place. Both parser pending references and
// retained metadata keep their identity across rollback; there is no independently cloned tree.
func (j *hashTransaction) captureOrder(d *dirtyOrder) error {
	j.maps()
	if _, exists := j.pending[d]; exists {
		return nil
	}
	if err := j.charge(1 + 2*uint64(len(d.nodes)) + uint64(len(d.leaders))); err != nil {
		return err
	}
	saved := *d
	saved.undo = nil // an armed move undo belongs to this operation, never the retained graph
	saved.nodes = maps.Clone(d.nodes)
	saved.leaders = slices.Clone(d.leaders)
	j.pending[d] = saved
	for _, n := range d.nodes {
		j.dirty[n] = *n
	}
	return nil
}

// captureSiblings journals s with its index records and the structs of the nodes in it (index
// changes update their positions, hashes and RB-tree state), not their children.
// ponytail: a sibling set is copied whole, O(its size) on the first change; a per-record journal
// would make a change in a huge sibling set O(1) if that ever matters.
// captureHook observes every whole sibling-set capture (tests only; nil otherwise).
var captureHook func(*siblings)

func (j *hashTransaction) captureSiblings(s *siblings) error {
	j.maps()
	if _, exists := j.siblings[s]; exists {
		return j.err
	}
	if captureHook != nil {
		captureHook(s)
	}
	// Counting nested map/slice sizes is itself work; reserve that scan before it starts.
	if err := j.charge(uint64(len(s.history) + len(s.rbDual) + len(s.rbMore))); err != nil {
		return err
	}
	//nolint:gosec // all terms are existing nonnegative collection sizes
	objects := 1 + uint64(s.SchemaLen()+s.OpaqueLen()+len(s.ht)+len(s.leaders)+len(s.firstRun)+
		len(s.exactByNode)+len(s.history)+len(s.leaderByNode)+len(s.unsorted)+len(s.rbTree)+len(s.rbGroups)+
		len(s.rbOrders)+len(s.pending)+len(s.contexts)+len(s.rbDual)+len(s.rbDualCount)+len(s.rbMore))
	for _, byHash := range s.history {
		objects = addHashBound(objects, uint64(len(byHash)))
	}
	for _, groups := range s.rbMore {
		objects = addHashBound(objects, uint64(len(groups)))
	}
	for _, groups := range s.rbDual {
		objects = addHashBound(objects, uint64(len(groups)))
	}
	if s.order != nil {
		objects = addHashBound(objects, 6+6*uint64(len(s.order.byNode))+2*uint64(len(s.order.entries)))
	}
	if err := j.charge(objects); err != nil {
		return err
	}
	saved := s.snapshotStorage()
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
	saved.order = cloneChildOrder(s)
	saved.rbDualCount = maps.Clone(s.rbDualCount)
	if s.rbDual != nil {
		saved.rbDual = make(map[*Node][]*rbGroup, len(s.rbDual))
		for n, groups := range s.rbDual {
			saved.rbDual[n] = slices.Clone(groups)
		}
	}
	if s.rbMore != nil {
		saved.rbMore = make(map[*Node][]*rbGroup, len(s.rbMore))
		for n, groups := range s.rbMore {
			saved.rbMore[n] = slices.Clone(groups)
		}
	}
	saved.rbOrders = maps.Clone(s.rbOrders)
	saved.contexts = maps.Clone(s.contexts)
	j.siblings[s] = saved
	for _, order := range s.rbOrders {
		if err := j.captureOrder(order); err != nil {
			return err
		}
	}
	for _, order := range s.pending {
		if err := j.captureOrder(order); err != nil {
			return err
		}
	}
	for _, c := range s.ht {
		if err := j.captureChain(c); err != nil {
			return err
		}
	}
	for _, c := range s.leaders {
		if err := j.captureChain(c); err != nil {
			return err
		}
	}
	for _, r := range s.exactByNode {
		if err := j.captureRecords(r); err != nil {
			return err
		}
	}
	for _, r := range s.leaderByNode {
		if err := j.captureRecords(r); err != nil {
			return err
		}
	}
	for _, byHash := range s.history {
		for _, r := range byHash {
			if err := j.captureRecords(r); err != nil {
				return err
			}
		}
	}
	for _, n := range s.All() {
		if err := j.captureNode(n); err != nil {
			return err
		}
	}
	return nil
}

func (j *hashTransaction) restore() {
	for n, saved := range j.nodes {
		// a journaled sibling set is restored below; the others are as they were (the operation
		// did not change them)
		n.restoreFields(saved)
	}
	for c, l := range j.owners {
		c.links = l
	}
	for m, saved := range j.metas {
		*m = saved
	}
	for s, saved := range j.siblings {
		s.restoreStorage(saved)
	}
	for c, saved := range j.chains {
		*c = saved
	}
	for r, saved := range j.records {
		*r = saved
	}
	for p, saved := range j.pending {
		*p = saved
	}
	for n, saved := range j.dirty {
		*n = saved
	}
	// After graph fields too: a topology captured after rebinding saved the temporary counter.
	for s, work := range j.works {
		s.setWork(work)
	}
	for s := range j.siblings {
		s.setOrderHooks()
	}
	for l, saved := range j.links {
		*l = saved
	}
	for op, saved := range j.opaq {
		*op = saved
	}
	j.tree.rank, j.tree.orders, j.tree.set = j.rank, j.orders, j.set
	// Entries last, newest first: an object captured whole after its first entries returns to
	// that capture above, and its entries undo the rest. None follows its own whole capture.
	for i := len(j.undo) - 1; i >= 0; i-- {
		j.undo[i]()
	}
	for i := len(j.placed) - 1; i >= 0; i-- {
		*j.placed[i].x = j.placed[i].old
	}
}
