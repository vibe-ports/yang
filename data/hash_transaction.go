// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"maps"
	"slices"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/opidx"
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
	orders map[orderKey][]*schema.Node
	// The whole captures (#240 step 4, design 13 §2.2): each map holds, per object, the serial
	// of the level that made its newest live capture (wholeAt); the images live in img, in
	// capture order, and whole lists their kinds in that order. A level captures an object iff
	// its serial is below the level's own: a descendant's committed capture stays valid, a
	// rolled-back one restores the previous serial (prevAt).
	nodes    map[*Node]uint32
	siblings map[*siblings]uint32
	chains   map[*recordChain]uint32
	records  map[*record]uint32
	pending  map[*dirtyOrder]uint32
	dirty    map[*dirtyOrderNode]uint32
	links    map[*leafrefLinks]uint32
	// owners are the link-record pointers of the counterparts of journaled nodes; a
	// counterpart may be in another tree (a moved subtree keeps its links), and freeLinks
	// drops an emptied record.
	owners map[*Node]uint32
	metas  map[*meta]uint32 // lyd_change_meta edits a metadata instance in place
	opaq   map[*opaque]uint32
	// works are the work counters of the sibling sets of subtrees rebound to another tree
	// (bindSubtreeWork) and not journaled otherwise
	works map[*siblings]uint32
	img   wholeImages
	whole []wholeKind
	// serial is the innermost open level's (1: the transaction itself); levels are the open
	// savepoints, innermost last (design 13 §2.1)
	serial, issued uint32
	levels         []level
	// lent are the work counters of trees without a transaction of their own that a subtree of
	// this tree moved into (validateOp's temporary tree): changes there are journaled here
	lent []*workCounter
	// detached are the detached roots captureDetachedParent journaled whole: a later boundary of
	// the same level (Insert, then linkOrigin) does not walk them again
	detached map[*Node]uint32
	// narrow is NewPath's journal (design 12, "NewPath mutation journal"): a narrow-aware
	// primitive records each slot, field or struct it changes as an entry of undo instead of
	// copying its sibling set whole; every other primitive still captures whole (touch).
	narrow bool
	fresh  map[*Node]bool // the nodes constructed under the narrow journal: never captured
	undo   []func()       // restored in reverse after the whole captures
	// placed are the topology nodes an RB insertion may change, the bulk of a narrow journal's
	// entries: kept by value (restored in reverse, after undo) instead of one closure each
	placed []placedNode
	// index records every write of the ordered indexes (childOrder) of the sets captured
	// whole, entry by entry (#240 step 4): restored last, in reverse; its tombstones are purged
	// when the operation keeps its changes. bound are the index bindings that report to it.
	index opidx.Journal
	bound []*orderBinding
	// suspendDepth counts the user-code sites the operation is waiting on (design 13 §2.4);
	// refusing marks the detached state a begin on a suspended tree returns: it is not installed,
	// opens no hash-work scope, and its end only reports the refusal
	suspendDepth int
	refusing     bool
	// unbound is the counter enroll binds counterless detached sets to (unboundSets) until end
	unbound     *workCounter
	unboundSets []*siblings
}

// refusingState is what a begin returns inside a suspended state: the operation is refused
// before any write, and the suspended state and its later rollback are untouched.
func refusingState(t *Tree) *hashTransaction {
	return &hashTransaction{tree: t, err: errInProgress, refusing: true}
}

type placedNode struct {
	x   *dirtyOrderNode
	old dirtyOrderNode
}

type wholeKind uint8

const (
	wholeNode wholeKind = iota
	wholeSiblings
	wholeChain
	wholeRecord
	wholePending
	wholeDirty
	wholeLinks
	wholeOwner
	wholeMeta
	wholeOpaque
	wholeWorks
	wholeDetached
)

// wholeImg is one whole capture: the object, its image and the serial it replaced.
type wholeImg[K comparable, V any] struct {
	k    K
	v    V
	prev uint32
}

type wholeImages struct {
	nodes    []wholeImg[*Node, Node]
	siblings []wholeImg[*siblings, siblings]
	chains   []wholeImg[*recordChain, recordChain]
	records  []wholeImg[*record, record]
	pending  []wholeImg[*dirtyOrder, dirtyOrder]
	dirty    []wholeImg[*dirtyOrderNode, dirtyOrderNode]
	links    []wholeImg[*leafrefLinks, leafrefLinks]
	owners   []wholeImg[*Node, *leafrefLinks]
	metas    []wholeImg[*meta, meta]
	opaq     []wholeImg[*opaque, opaque]
	works    []wholeImg[*siblings, *workCounter]
	detached []wholeImg[*Node, struct{}]
}

// level is an open savepoint: the serial to return to and the log lengths at its opening.
type level struct {
	parent                     uint32
	whole, undo, placed, index int
}

// addWhole appends a whole capture of k at the current level.
func addWhole[K comparable, V any](j *hashTransaction, at map[K]uint32, imgs *[]wholeImg[K, V], kind wholeKind, k K, v V) {
	*imgs = append(*imgs, wholeImg[K, V]{k, v, at[k]})
	at[k] = j.serial
	j.whole = append(j.whole, kind)
}

// popWhole drops the newest capture of its kind and returns it; the object's serial returns to
// the one it replaced. A first capture inserted the key, and its replay deletes it: no image of
// the object remains in any tail, and an assignment (even of a present key) to a full small Go
// map grows it. Any other capture assigned a present key on the forward path, which grew such
// a map then, so its replay assignment allocates nothing.
func popWhole[K comparable, V any](at map[K]uint32, imgs *[]wholeImg[K, V]) (K, V) {
	l := *imgs
	e := l[len(l)-1]
	if e.prev == 0 {
		delete(at, e.k)
	} else {
		at[e.k] = e.prev
	}
	l[len(l)-1] = wholeImg[K, V]{}
	*imgs = l[:len(l)-1]
	return e.k, e.v
}

// captured reports whether the current level holds a capture of k.
func captured[K comparable](j *hashTransaction, at map[K]uint32, k K) bool { return at[k] >= j.serial }

// savepoint opens a level (design 13 §2.1): a later rollbackLevel restores exactly what the
// level changed, commitLevel keeps it in the enclosing level. O(1), charged one unit.
func (j *hashTransaction) savepoint() (int, error) {
	if err := j.charge(1); err != nil {
		return 0, err
	}
	j.levels = append(j.levels, level{parent: j.serial, whole: len(j.whole), undo: len(j.undo),
		placed: len(j.placed), index: j.index.Mark()})
	j.issued++
	j.serial = j.issued
	return len(j.levels), nil
}

func (j *hashTransaction) closeLevel(sp int) level {
	if sp != len(j.levels) || sp == 0 {
		panic("data: savepoints complete innermost first")
	}
	l := j.levels[sp-1]
	j.levels = j.levels[:sp-1]
	j.serial = l.parent
	return l
}

// commitLevel keeps the level's changes: its tail becomes part of the enclosing level's.
func (j *hashTransaction) commitLevel(sp int) { j.closeLevel(sp) }

// rollbackLevel restores what the level changed and drops its entries.
func (j *hashTransaction) rollbackLevel(sp int) {
	l := j.levels[sp-1]
	j.rollbackTo(l)
	j.closeLevel(sp)
}

// endLevel commits the level on success and rolls it back on any failure.
func (j *hashTransaction) endLevel(sp int, result error) {
	if result != nil {
		j.rollbackLevel(sp)
	} else {
		j.commitLevel(sp)
	}
}

// beginHashTransaction starts journaling t for an operation that may change any part of it. It
// returns nil (whose methods do nothing) inside an operation that already journals t: the
// enclosing transaction records the changes and restores them, a budget error being sticky for
// the whole operation. The caller ends it with end.
func beginHashTransaction(t *Tree) *hashTransaction {
	if t.work.journal != nil {
		if t.work.journal.suspendedState() {
			return refusingState(t)
		}
		return nil
	}
	j := &hashTransaction{tree: t, done: t.work.beginHashWork(), rank: t.rank, orders: t.orders, set: t.set,
		serial: 1, issued: 1}
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

// beginJointTransaction opens one recovery state for an operation over several trees (design
// 13 §3.2, ValidateOp): on the first tree, the others' counters lent to it as moveNodes does.
// The caller ends the returned transaction (nil inside an enclosing state, which then lends the
// other trees); a tree whose state waits on user code refuses.
func beginJointTransaction(trees ...*Tree) (*hashTransaction, error) {
	var j *hashTransaction
	var running *hashTransaction
	for _, t := range trees {
		if t == nil {
			continue
		}
		if running == nil {
			j = beginHashTransaction(t)
			if j != nil && j.refusing {
				return j, j.err
			}
			running = t.work.journal
			continue
		}
		switch w := &t.work; {
		case w.journal == running:
		case w.journal.suspendedState():
			return j, errInProgress
		case w.journal == nil:
			if err := running.charge(1); err != nil {
				return j, err
			}
			w.journal = running
			running.lent = append(running.lent, w)
		}
	}
	return j, nil
}

// beginNarrowTransaction starts NewPath's journal (nil inside an enclosing transaction, as
// beginHashTransaction). The schema-derived caches t.rank and t.orders keep their identity: an
// entry they gain is the same for any later call, so nothing is copied up front.
func beginNarrowTransaction(t *Tree) *hashTransaction {
	if t.work.journal != nil {
		if t.work.journal.suspendedState() {
			return refusingState(t)
		}
		return nil
	}
	j := t.work.spare
	if j == nil {
		j = &hashTransaction{}
	}
	t.work.spare = nil
	*j = hashTransaction{tree: t, done: t.work.beginHashWork(), rank: t.rank, orders: t.orders, set: t.set,
		narrow: true, undo: j.undo[:0], placed: j.placed[:0], fresh: j.fresh, index: j.index, bound: j.bound[:0],
		serial: 1, issued: 1}
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
	clear(j.bound)
	*j = hashTransaction{undo: j.undo[:0], placed: j.placed[:0], fresh: j.fresh, index: j.index, bound: j.bound[:0]}
	t.work.spare = j
}

// maps allocates the whole-capture maps, which a narrow journal leaves nil (reads of a nil map
// see no capture) until its first whole capture.
func (j *hashTransaction) maps() {
	if j.nodes != nil {
		return
	}
	j.nodes, j.siblings, j.chains, j.records = map[*Node]uint32{}, map[*siblings]uint32{}, map[*recordChain]uint32{}, map[*record]uint32{}
	j.pending, j.dirty = map[*dirtyOrder]uint32{}, map[*dirtyOrderNode]uint32{}
	j.links, j.owners = map[*leafrefLinks]uint32{}, map[*Node]uint32{}
	j.metas, j.opaq, j.works = map[*meta]uint32{}, map[*opaque]uint32{}, map[*siblings]uint32{}
}

// narrow returns the narrow journal that records the changes of s entry by entry: nil without
// a journal, in a whole-set journal, or while s has a whole capture in some tail (an entry after
// a whole capture would restore over it; see rollbackTo). A rolled-back first capture leaves no
// image of s, so s gets entries again.
func (s *siblings) narrow() *hashTransaction {
	j := s.journal()
	if j == nil || !j.narrow || j.suspendedState() {
		return nil // a suspended state's objects refuse at touch
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
	if j.refusing {
		if result != nil && (*result == nil || errors.Is(*result, errInProgress)) {
			*result = j.err
		}
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
		// a guard refusal restores too: it may follow the operation's earlier writes
		if errors.Is(*result, yang.ErrBudget) || errors.Is(*result, errInProgress) || j.rollback && *result != nil {
			j.restore()
		}
	}
	j.release()
	j.index.Commit() // after a restore the log is empty; otherwise its tombstones go
	for _, b := range j.bound {
		if b.state == j {
			b.state = nil
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
	if j.refusing {
		return j.err
	}
	if err := j.tree.work.chargeLimited(objects); err != nil {
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
		if j.suspendedState() {
			return errInProgress
		}
		return j.captureSiblings(s)
	}
	return nil
}

// touchSticky is touch for an index edit that cannot return its refusal everywhere it is called:
// the refusal also becomes the counter's sticky error, so the operation fails and its journal
// restores (design 13 §4.5, swallowed index errors).
func (s *siblings) touchSticky() error {
	err := s.touch()
	if err != nil && s.work != nil && s.work.hashErr == nil {
		s.work.hashErr = err
	}
	return err
}

// touch journals s, a sibling set of t, before it changes (also before its first link binds it
// to t).
func (t *Tree) touch(s *siblings) error {
	if j := t.work.journal; j != nil {
		if j.suspendedState() || s.journal().suspendedState() {
			return errInProgress
		}
		return j.captureSiblings(s)
	}
	if s.journal().suspendedState() {
		return errInProgress
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
	if j.suspendedState() {
		return errInProgress
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
	if from.suspendedState() || t.work.journal.suspendedState() {
		return errInProgress // a lend into, or from, an operation waiting on user code
	}
	lend := func(j *hashTransaction, w *workCounter) error {
		if w.journal != nil {
			if w.journal.suspendedState() {
				return errInProgress
			}
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
	if s.journal().suspendedState() || work.journal.suspendedState() {
		return errInProgress
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
		if captured(j, j.siblings, s) {
			continue
		}
		if !captured(j, j.works, s) {
			if err := j.charge(1); err != nil {
				return err
			}
			addWhole(j, j.works, &j.img.works, wholeWorks, s, s.work)
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
	if root.tree != nil || captured(j, j.detached, root) {
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
		if captured(j, j.siblings, s) {
			// journaled already (a lazy touch): its children are still walked, so charge them
			if err := j.charge(1 + uint64(s.SchemaLen()+s.OpaqueLen())); err != nil { //nolint:gosec // sibling counts are nonnegative
				return err
			}
		} else if err := j.captureSiblings(s); err != nil {
			return err
		}
		// enrollment (design 13 §2.4): user code that reaches the captured detached tree meets
		// this state, suspended, instead of writing under its rollback
		if err := j.enroll(s); err != nil {
			return err
		}
		stack = s.schemaSpan().AppendTo(stack)
		stack = s.opaqueSpan().AppendTo(stack)
	}
	if j.detached == nil {
		j.detached = map[*Node]uint32{}
	}
	addWhole(j, j.detached, &j.img.detached, wholeDetached, root, struct{}{})
	return nil
}

// captureNode journals n itself, not its children: its sibling set is journaled on its own.
func (j *hashTransaction) captureNode(n *Node) error {
	j.maps()
	if captured(j, j.nodes, n) {
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
	addWhole(j, j.nodes, &j.img.nodes, wholeNode, n, saved)
	for _, m := range n.meta {
		addWhole(j, j.metas, &j.img.metas, wholeMeta, m, *m)
	}
	if n.opaq != nil {
		op := *n.opaq
		op.Attrs = slices.Clone(op.Attrs)
		addWhole(j, j.opaq, &j.img.opaq, wholeOpaque, n.opaq, op)
	}
	for _, c := range n.carriedRB {
		if err := j.captureOrder(c.order); err != nil {
			return err
		}
	}
	if n.links != nil {
		for _, side := range [2][]*Node{n.links.leafrefs, n.links.targets} {
			for _, c := range side {
				if governing(c).suspendedState() {
					return j.refuse() // freeLinks, the bulk cleaner or a link edit would reach it
				}
			}
		}
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
	if captured(j, j.links, l) {
		return nil
	}
	if err := j.charge(1 + uint64(len(l.leafrefs)+len(l.targets))); err != nil {
		return err
	}
	addWhole(j, j.links, &j.img.links, wholeLinks, l, leafrefLinks{leafrefs: slices.Clone(l.leafrefs), targets: slices.Clone(l.targets)})
	return nil
}

// captureOwner includes counterparts in another tree: freeLinks can empty their record.
func (j *hashTransaction) captureOwner(c *Node) error {
	j.maps()
	if captured(j, j.owners, c) {
		return nil
	}
	g := governing(c)
	if g.suspendedState() {
		return j.refuse()
	}
	if err := j.charge(1); err != nil {
		return err
	}
	if g == nil {
		// enrollment (design 13 §2.4 (4)): c's tree joins this state until end, so a callback
		// that later reaches it meets this state, suspended, even after the link is gone
		if err := j.enroll(childStorage(c)); err != nil {
			return err
		}
	}
	addWhole(j, j.owners, &j.img.owners, wholeOwner, c, c.links)
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
		if captured(j, j.records, r) {
			continue
		}
		if err := j.charge(5 + uint64(len(r.more))); err != nil {
			return err
		}
		saved := *r
		saved.more = slices.Clone(r.more)
		addWhole(j, j.records, &j.img.records, wholeRecord, r, saved)
		stack = append(stack, r.prev, r.next, r.left, r.right)
	}
	return nil
}

func (j *hashTransaction) captureChain(c *recordChain) error {
	j.maps()
	if c == nil {
		return nil
	}
	if captured(j, j.chains, c) {
		return nil
	}
	if err := j.charge(1); err != nil {
		return err
	}
	addWhole(j, j.chains, &j.img.chains, wholeChain, c, *c)
	if err := j.captureRecords(c.first); err != nil {
		return err
	}
	return j.captureRecords(c.root)
}

// captureOrder saves the authoritative graph in place. Both parser pending references and
// retained metadata keep their identity across rollback; there is no independently cloned tree.
func (j *hashTransaction) captureOrder(d *dirtyOrder) error {
	j.maps()
	if captured(j, j.pending, d) {
		return nil
	}
	if err := j.charge(1 + 2*uint64(len(d.nodes)) + uint64(len(d.leaders))); err != nil {
		return err
	}
	saved := *d
	saved.undo = nil // an armed move undo belongs to this operation, never the retained graph
	saved.nodes = maps.Clone(d.nodes)
	saved.leaders = slices.Clone(d.leaders)
	addWhole(j, j.pending, &j.img.pending, wholePending, d, saved)
	for _, n := range d.nodes {
		addWhole(j, j.dirty, &j.img.dirty, wholeDirty, n, *n)
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
	if captured(j, j.siblings, s) {
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
	addWhole(j, j.siblings, &j.img.siblings, wholeSiblings, s, saved)
	if s.order != nil && s.order.bind.state == nil {
		// the index is not copied: from now on every write of it is an entry of j.index
		s.order.bind.state = j
		j.bound = append(j.bound, s.order.bind)
	}
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

// restore returns the tree to its state when the transaction began: every level rolls back.
func (j *hashTransaction) restore() {
	j.rollbackTo(level{})
	j.levels = j.levels[:0]
	j.serial = 1
	j.tree.rank, j.tree.orders, j.tree.set = j.rank, j.orders, j.set
}

// rollbackTo replays every capture and entry made since l opened, newest first: the whole
// captures, then #318's entries, then the index entries. An object captured whole after its
// first entries returns to that capture first, and its entries undo the rest; no entry follows
// its own object's whole capture in any tail (siblings.narrow).
func (j *hashTransaction) rollbackTo(l level) {
	for i := len(j.whole) - 1; i >= l.whole; i-- {
		switch j.whole[i] {
		case wholeNode:
			n, saved := popWhole(j.nodes, &j.img.nodes)
			// a journaled sibling set is restored on its own
			n.restoreFields(saved)
		case wholeSiblings:
			s, saved := popWhole(j.siblings, &j.img.siblings)
			s.restoreStorage(saved)
		case wholeChain:
			c, saved := popWhole(j.chains, &j.img.chains)
			*c = saved
		case wholeRecord:
			r, saved := popWhole(j.records, &j.img.records)
			*r = saved
		case wholePending:
			p, saved := popWhole(j.pending, &j.img.pending)
			*p = saved
		case wholeDirty:
			n, saved := popWhole(j.dirty, &j.img.dirty)
			*n = saved
		case wholeLinks:
			l, saved := popWhole(j.links, &j.img.links)
			*l = saved
		case wholeOwner:
			c, saved := popWhole(j.owners, &j.img.owners)
			c.links = saved
		case wholeMeta:
			m, saved := popWhole(j.metas, &j.img.metas)
			*m = saved
		case wholeOpaque:
			op, saved := popWhole(j.opaq, &j.img.opaq)
			*op = saved
		case wholeWorks:
			// newest first: a topology captured after rebinding saved the temporary counter,
			// and this older entry gives the set and its topologies their counter back
			s, work := popWhole(j.works, &j.img.works)
			s.setWork(work)
		case wholeDetached:
			popWhole(j.detached, &j.img.detached)
		}
	}
	clear(j.whole[l.whole:])
	j.whole = j.whole[:l.whole]
	for i := len(j.undo) - 1; i >= l.undo; i-- {
		j.undo[i]()
	}
	clear(j.undo[l.undo:])
	j.undo = j.undo[:l.undo]
	for i := len(j.placed) - 1; i >= l.placed; i-- {
		*j.placed[i].x = j.placed[i].old
	}
	clear(j.placed[l.placed:])
	j.placed = j.placed[:l.placed]
	j.index.Rollback(l.index)
}

// capturer returns the running state that has captured s whole (orderBuild's new index reports
// to it), nil when none has.
func capturer(t *Tree, s *siblings) *hashTransaction {
	for _, j := range [2]*hashTransaction{s.journal(), t.work.journal} {
		if j == nil {
			continue
		}
		if _, whole := j.siblings[s]; whole {
			return j
		}
	}
	return nil
}

// refuse makes the guard's refusal the operation's: it fails with it and restores whatever it
// wrote before reaching the suspended state's object.
func (j *hashTransaction) refuse() error {
	if j.err == nil {
		j.err = errInProgress
	}
	j.rollback = true
	return errInProgress
}

// governingCounter is the counter whose state would govern c (Node.touch's resolution).
func governingCounter(c *Node) *workCounter {
	if w := childStorage(c).work; w != nil {
		return w
	}
	if t := c.treeOf(); t != nil {
		return &t.work
	}
	return nil
}

// enroll makes this state govern s until it ends (design 13 §2.4): s's counter is lent to it,
// or, for a set without a counter (an unbound detached set), s is bound to the state's
// temporary counter, recorded as a works entry (a rollback restores it) and unbound at end.
func (j *hashTransaction) enroll(s *siblings) error {
	switch w := s.work; {
	case w != nil && w.journal != nil:
		return nil // governed already: this state, or another one (refused when suspended)
	case w != nil:
		if err := j.charge(1); err != nil {
			return err
		}
		w.journal = j
		j.lent = append(j.lent, w)
		return nil
	}
	if err := j.charge(1); err != nil {
		return err
	}
	if j.unbound == nil {
		j.unbound = &workCounter{journal: j}
		j.lent = append(j.lent, j.unbound)
	}
	j.maps()
	if !captured(j, j.works, s) && !captured(j, j.siblings, s) {
		addWhole(j, j.works, &j.img.works, wholeWorks, s, s.work)
	}
	s.setWork(j.unbound)
	j.unboundSets = append(j.unboundSets, s)
	return nil
}

// release unbinds the sets enroll bound that still use the temporary counter.
func (j *hashTransaction) release() {
	for _, s := range j.unboundSets {
		if s.work == j.unbound {
			s.setWork(nil)
		}
	}
	clear(j.unboundSets)
	j.unboundSets = j.unboundSets[:0]
}
