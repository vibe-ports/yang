// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"iter"
	"math/bits"
	"slices"

	"github.com/vibe-ports/yang/internal/opidx"
)

// Sibling storage (#240 step 5, design 14). One order-statistic sequence per set is the only
// physical storage, in document order. Positions are logical: the schema children occupy
// [0, SchemaLen()) and the opaque ones follow them. Physically they coincide until a schema node
// follows an opaque one (LYD_INSERT_NODE_LAST, design 15): the set is then interleaved, and
// positions, spans and neighbours translate through the sequence's kind counts.
// A linked node finds its handle through its slot. The ordered index (childOrder) registers the
// same handles and owns no sequence.
//
// Reads by position or identity are uncounted, as slice indexing was; counted lookups on a
// mutation path go through orderPosition. Every write is captured first (touch) or recorded by
// a narrow journal, and pays its bound before it writes: its own prepay, or the credit of the
// scope it runs in (link, unlink), whose bound includes it. Spans and iterators borrow the live
// sequence: a storage write invalidates them (test builds panic on a stale read).

// siblingStore is the storage of a set, reached through a pointer so that moving or capturing a
// siblings value never copies the sequence (its handles keep their owner).
type siblingStore struct {
	seq     opidx.Seq[*Node]
	nschema int // schema nodes: the logical prefix, physically the heavy handles
	// mixed is nonzero once a schema node follows an opaque one physically (an interleaved
	// set, design 15); journaled, never cleared. The sequence's kind threads are on with it.
	mixed int
	// place is how the next link places its node physically, when not by its logical position
	// (design 15); transient, set right before the link and cleared by it, never journaled
	place placement
	// bind is the one hook target of the sequence and of the ordered index over it
	bind orderBinding
	// view marks a non-owning view (sibsOf, printing, a payload copy's source): its handles are
	// not its nodes' slots, which stay their own sets'
	view bool
	// ver changes at every write that moves positions (an insertion or removal): borrowed spans
	// and iterators check it in test builds. moves changes at a republication, which keeps every
	// position and only reorders a run: an iterator walking it reseeks by position, as indexing a
	// slice that was overwritten in place did.
	ver, moves uint64
}

// checkSpans makes a read through a span or iterator whose store changed since it was taken
// panic (test builds).
var checkSpans bool

// store returns the storage of s, allocated with its first child.
func (s *siblings) store() *siblingStore {
	if s.st == nil {
		st := &siblingStore{}
		st.bind.owner = s
		st.seq.SetLabeled(false) // an ordered index turns the labels on (orderBuild)
		st.seq.SetWeigh(isSchemaNode)
		st.seq.SetHooks(&st.bind)
		s.st = st
	}
	return s.st
}

// bindStore makes j journal every later write of s's storage and index, allocating the store
// (a capture's image of s keeps the nil pointer, so a rollback drops a new store).
func (s *siblings) bindStore(j *hashTransaction) {
	st := s.store()
	if st.bind.state == nil {
		st.bind.state = j
		j.bound = append(j.bound, &st.bind)
	}
}

// placement is a link's physical placement (design 15): placeDefault follows the logical
// position (storeAnchor); placeEnd appends after the last sibling (LYD_INSERT_NODE_LAST);
// placeAfter and placeBefore put the node right after or before node. It applies to the link of
// target only, so a placement whose link did not happen places nothing else.
type placement struct {
	mode         placeMode
	node, target *Node
}

type placeMode uint8

const (
	placeDefault placeMode = iota
	placeEnd
	placeAfter
	placeBefore
)

// setPlace sets the physical placement of s's next link (no store: nothing to place among).
func (s *siblings) setPlace(p placement) {
	if s.st != nil {
		s.st.place = p
	}
}

// clearPlace ends a link's placement.
func (s *siblings) clearPlace() { s.setPlace(placement{}) }

// linkAnchor is the handle link puts n physically before (nil: appended) for position at:
// storeAnchor, or the placement set for this link.
func (s *siblings) linkAnchor(n *Node, at int) *opidx.Handle[*Node] {
	if s.st == nil {
		return nil
	}
	switch p := s.st.place; {
	case p.target != n:
	case p.mode == placeEnd:
		return nil
	case p.mode == placeAfter:
		if h := s.handle(p.node); h != nil {
			return s.st.seq.Next(h)
		}
	case p.mode == placeBefore:
		if h := s.handle(p.node); h != nil {
			return h
		}
	}
	if n.schema == nil {
		at -= s.SchemaLen()
	}
	return s.storeAnchor(n, at)
}

// originPlace is the placement that puts the linked n back between its document neighbours after
// a temporary removal (restoreLink); the logical default unless s is interleaved.
func (s *siblings) originPlace(n *Node) placement {
	if s.st == nil || !s.st.interleaved() {
		return placement{}
	}
	prev, next := s.docNeighbours(n)
	switch {
	case prev != nil:
		return placement{mode: placeAfter, node: prev, target: n}
	case next != nil:
		return placement{mode: placeBefore, node: next, target: n}
	}
	return placement{}
}

// linkMixes reports whether link's insertion of n at at (link's convention: an opaque n's at is
// among the opaque nodes, -1 appended) makes s interleaved.
func (s *siblings) linkMixes(n *Node, at int) bool {
	if s.st == nil {
		return false
	}
	if n.schema == nil && at >= 0 {
		at += s.SchemaLen()
	}
	return s.st.mixes(s.linkAnchor(n, at), n)
}

// mixes reports whether inserting n before anchor makes st interleaved: in a set that was not, a
// schema node after an opaque one, or an opaque node before a schema one.
func (st *siblingStore) mixes(anchor *opidx.Handle[*Node], n *Node) bool {
	if st.interleaved() {
		return false
	}
	if n.schema == nil {
		return anchor != nil && anchor.Value.schema != nil
	}
	if st.seq.Len() == st.nschema {
		return false
	}
	if anchor == nil {
		return true // the last sibling is opaque
	}
	p := st.seq.Prev(anchor)
	return p != nil && p.Value.schema == nil
}

// isSchemaNode is the storage's kind: schema nodes are heavy, opaque ones light.
func isSchemaNode(n *Node) bool { return n.schema != nil }

// interleaved reports whether a schema node follows an opaque one in st (design 15).
func (st *siblingStore) interleaved() bool { return st.mixed != 0 }

// peek is the handle at logical position i, nil out of range; uncounted.
func (st *siblingStore) peek(i int) *opidx.Handle[*Node] {
	if !st.interleaved() {
		return st.seq.Peek(i)
	}
	h, _ := st.peekLevels(i)
	return h
}

func (st *siblingStore) peekLevels(i int) (*opidx.Handle[*Node], int) {
	if i < st.nschema {
		return st.seq.PeekKind(i, true)
	}
	return st.seq.PeekKind(i-st.nschema, false)
}

// pos is h's logical position.
func (st *siblingStore) pos(h *opidx.Handle[*Node]) (int, bool) {
	if !st.interleaved() {
		return st.seq.Pos(h)
	}
	at, ok := st.seq.PosKind(h)
	if ok && !h.Heavy() {
		at += st.nschema
	}
	return at, ok
}

// nextL and prevL are h's logical neighbours: in an interleaved set the next handle of its kind,
// the first opaque one after the last schema one (O(1) when physically adjacent, else O(log n)).
func (st *siblingStore) nextL(h *opidx.Handle[*Node]) *opidx.Handle[*Node] {
	if !st.interleaved() || h == nil {
		return st.seq.Next(h)
	}
	if n := st.seq.NextKind(h); n != nil || !h.Heavy() {
		return n
	}
	return st.seq.FirstKind(false)
}

func (st *siblingStore) prevL(h *opidx.Handle[*Node]) *opidx.Handle[*Node] {
	if !st.interleaved() || h == nil {
		return st.seq.Prev(h)
	}
	if p := st.seq.PrevKind(h); p != nil || h.Heavy() {
		return p
	}
	return st.seq.LastKind(true)
}

func handleValue(h *opidx.Handle[*Node]) *Node {
	if h == nil {
		return nil
	}
	return h.Value
}

// storeJournaled reports whether s's storage writes are recorded now.
func (s *siblings) storeJournaled() bool { return s.st != nil && s.st.bind.state != nil }

// slotUnits is what one slot write and the schema count's cell report.
func slotUnits(journaled bool) uint64 {
	if !journaled {
		return 0
	}
	return 2 + 2
}

// storeCredit is the prepaid bound of one storage write (payStore).
type storeCredit struct {
	w      *workCounter
	nested bool
}

// payStore prepays bound for one storage write on s's counter: its own scope, or inside an open
// scope a draw on that scope's credit (whose bound includes it); without a counter it pays
// nothing. As for the ordered index (design 13 §4.1), the write is charged its actual units and
// the rest of the bound is refunded at close.
func (s *siblings) payStore(bound uint64) (storeCredit, error) {
	w := s.work
	if w == nil {
		return storeCredit{}, nil
	}
	nested, err := w.prepay(bound, false)
	if err != nil {
		return storeCredit{}, err
	}
	return storeCredit{w: w, nested: nested}, nil
}

func (c storeCredit) close() {
	if c.w != nil {
		c.w.closeCredit(c.nested)
	}
}

func (s *siblings) Len() int {
	if s.st == nil {
		return 0
	}
	return s.st.seq.Len()
}

func (s *siblings) SchemaLen() int {
	if s.st == nil {
		return 0
	}
	return s.st.nschema
}

func (s *siblings) OpaqueLen() int { return s.Len() - s.SchemaLen() }

// At is the node at position i: a walk along the threads from the nearer end when i is closer
// to it than the tree's height (a list's keys, a neighbour of an end), else one descent; its
// steps or levels are counted as sibling visits. A sequential walk uses an iterator (All, Range,
// from, back) instead.
func (s *siblings) At(i int) *Node { return s.st.at(i) }

// at is the node at logical position i (At); atPhys at physical position i.
func (st *siblingStore) at(i int) *Node {
	if !st.interleaved() {
		return st.atPhys(i)
	}
	h, steps := st.peekLevels(i)
	st.countVisits(steps)
	return h.Value
}

// interleave marks st interleaved and turns its kind threads on (charged by the caller's bound:
// interleaveBound).
func (st *siblingStore) interleave() {
	opidx.AddInt(&st.bind, &st.mixed, 1)
	st.seq.SetKindThreads()
}

// interleaveBound bounds interleave.
func (st *siblingStore) interleaveBound(journaled bool) uint64 {
	return addHashBound(mixedUnits(journaled), st.seq.KindThreadsBound(journaled))
}

func (st *siblingStore) countVisits(steps int) {
	if o := st.bind.owner; o != nil && o.work != nil && o.work.visits && steps != 0 { // tests only
		o.work.Add(int64(steps))
	}
}

func (st *siblingStore) atPhys(i int) *Node {
	n := st.seq.Len()
	var h *opidx.Handle[*Node]
	steps := 0
	switch height := bits.Len(uint(n)); {
	case i < height:
		for h = st.seq.First(); steps < i; steps++ {
			h = st.seq.Next(h)
		}
	case n-1-i < height:
		for h = st.seq.Last(); steps < n-1-i; steps++ {
			h = st.seq.Prev(h)
		}
	default:
		h, steps = st.seq.PeekLevels(i)
	}
	st.countVisits(steps)
	return h.Value
}
func (s *siblings) opaqueAt(i int) *Node {
	return s.At(s.SchemaLen() + i)
}

func (s *siblings) span(lo, hi int) siblingSpan {
	if s.st == nil {
		return siblingSpan{}
	}
	return siblingSpan{st: s.st, lo: lo, hi: hi, ver: s.st.ver}
}
func (s *siblings) schemaSpan() siblingSpan          { return s.span(0, s.SchemaLen()) }
func (s *siblings) opaqueSpan() siblingSpan          { return s.span(s.SchemaLen(), s.Len()) }
func (s *siblings) SchemaAll() iter.Seq2[int, *Node] { return s.schemaSpan().All() }
func (s *siblings) opaqueAll() iter.Seq2[int, *Node] { return s.opaqueSpan().All() }

// docSpan is the whole set in document order (libyang's sibling list): physical positions.
func (s *siblings) docSpan() siblingSpan {
	if s.st == nil {
		return siblingSpan{}
	}
	return siblingSpan{st: s.st, lo: 0, hi: s.Len(), ver: s.st.ver, doc: true}
}

// All walks the set in document order; docNodes collects it.
func (s *siblings) All() iter.Seq2[int, *Node] { return s.docSpan().All() }
func (s *siblings) docNodes() []*Node          { return s.docSpan().Collect() }

// opaqTail is the trailing opaque block in document order (where lyd_find_sibling_opaq_next
// starts: the opaque nodes after the last schema node); the opaque nodes unless interleaved.
func (s *siblings) opaqTail() siblingSpan {
	if s.st == nil || !s.st.interleaved() {
		return s.opaqueSpan()
	}
	lo := 0
	if last := s.st.seq.LastKind(true); last != nil {
		at, _ := s.st.seq.Pos(last)
		lo = at + 1
	}
	return s.docSpan().Slice(lo, s.Len())
}

// opaqNextSpan is what lyd_find_sibling_opaq_next walks from the first sibling: every sibling
// when that one is opaque, else the trailing opaque block (tree_data.c:3392-3412; it compares
// LYD_NAME, so a schema node's name too).
func (s *siblings) opaqNextSpan() siblingSpan {
	if first := s.docFirst(); first != nil && first.schema == nil && s.st.interleaved() {
		return s.docSpan()
	}
	return s.opaqTail()
}

// docFirst and docLast are libyang's first child and its prev: the ends in document order.
func (s *siblings) docFirst() *Node {
	if s.st == nil {
		return nil
	}
	return handleValue(s.st.seq.First())
}

func (s *siblings) docLast() *Node {
	if s.st == nil {
		return nil
	}
	return handleValue(s.st.seq.Last())
}

// docNext is the linked n's next sibling in document order (lyd_node.next), nil at the end.
func (s *siblings) docNext(n *Node) *Node {
	if s.st == nil {
		return nil
	}
	return handleValue(s.st.seq.Next(s.handle(n)))
}
func (s *siblings) Range(lo, hi int) iter.Seq2[int, *Node] {
	return s.span(lo, hi).All()
}
func (s *siblings) CollectRange(lo, hi int) []*Node { return s.span(lo, hi).Collect() }

// handle is n's handle in s, nil when n is not in s.
func (s *siblings) handle(n *Node) *opidx.Handle[*Node] {
	if s.st == nil || n == nil {
		return nil
	}
	if h := n.slot.Get(); s.st.seq.Owns(h) {
		return h
	}
	if s.st.view {
		for h := s.st.seq.First(); h != nil; h = s.st.seq.Next(h) {
			if h.Value == n {
				return h
			}
		}
	}
	return nil
}

// Index is the position of n in s, -1 if absent. It is uncounted, as slice indexing was.
func (s *siblings) Index(n *Node) int {
	h := s.handle(n)
	if h == nil {
		return -1
	}
	at, _ := s.st.pos(h)
	return at
}

func (s *siblings) LowerBound(lo, hi int, pred func(*Node) bool) int {
	return s.span(0, s.Len()).LowerBound(lo, hi, pred)
}

// view publishes nodes (in document order) as a non-owning view of a fresh set: no node is
// linked, re-slotted or reparented, and no work is bound.
func (s *siblings) view(nodes ...*Node) {
	st := &siblingStore{view: true}
	st.seq.SetLabeled(false)
	st.seq.SetWeigh(isSchemaNode)
	st.seq.Splice(0, 0, nodes)
	opaque := false
	for _, n := range nodes {
		switch {
		case n.schema == nil:
			opaque = true
		case opaque: // a schema node after an opaque one: interleaved
			st.mixed = 1
			st.nschema++
		default:
			st.nschema++
		}
	}
	if st.interleaved() {
		st.seq.SetKindThreads()
	}
	s.st = st
}

// insert places n physically before anchor (nil: appended), the anchor storeAnchor or the
// physical end (insertLast) gives. A schema node appended after an opaque one makes the set
// interleaved.
func (s *siblings) insert(anchor *opidx.Handle[*Node], n *Node) error {
	st := s.store()
	journaled := s.storeJournaled()
	mixes := st.mixes(anchor, n)
	bound := addHashBound(st.seq.InsertBound(anchor, journaled, s.work.planPay()), slotUnits(journaled))
	if mixes {
		bound = addHashBound(bound, st.interleaveBound(journaled))
	}
	paid, err := s.payStore(bound)
	if err != nil {
		return err
	}
	defer paid.close()
	st.ver++
	h := st.seq.InsertBefore(anchor, n)
	n.slot.Set(&st.bind, h)
	if n.schema != nil {
		opidx.AddInt(&st.bind, &st.nschema, 1)
	}
	if mixes {
		st.interleave()
	}
	return nil
}

// mixedUnits is what the interleaved counter's first write reports.
func mixedUnits(journaled bool) uint64 {
	if !journaled {
		return 0
	}
	return 2
}

// insertBound bounds link's insertion of n at position at (an opaque n: among the opaque nodes,
// appended when out of range), placed as linkAnchor places it.
func (s *siblings) insertBound(n *Node, at int, journaled bool) uint64 {
	if s.st == nil {
		var empty opidx.Seq[*Node] // the store insert creates
		empty.SetLabeled(false)
		return empty.InsertBound(nil, journaled, nil) + slotUnits(journaled)
	}
	if n.schema == nil && at >= 0 {
		at += s.SchemaLen()
	}
	anchor := s.linkAnchor(n, at)
	bound := addHashBound(s.st.seq.InsertBound(anchor, journaled, s.work.planPay()), slotUnits(journaled))
	if s.st.mixes(anchor, n) {
		bound = addHashBound(bound, s.st.interleaveBound(journaled))
	}
	return bound
}

// storeAnchor is the handle n goes physically before when link puts it at at (nil: appended),
// libyang's anchors (design 15): a schema node before schema node at, or after the last schema
// node (lyd_insert_node_ordby_schema keeps data nodes before a trailing opaque block); an opaque
// node right after opaque node at-1 (lyd_insert_after of the XML parser's opaque anchor), first
// among them at 0, appended when out of range (lyd_insert_node_last).
func (s *siblings) storeAnchor(n *Node, at int) *opidx.Handle[*Node] {
	st := s.st
	if st == nil {
		return nil
	}
	if n.schema != nil {
		return st.schemaAnchor(at)
	}
	if at < 0 || at >= s.OpaqueLen() {
		return nil
	}
	if at == 0 || !st.interleaved() {
		return st.peek(st.nschema + at)
	}
	return st.seq.Next(st.peek(st.nschema + at - 1))
}

// appendNode puts n last among its kind (an opaque node: appended), or as linkAnchor places it.
func (s *siblings) appendNode(n *Node) error {
	if err := s.touch(); err != nil {
		return err
	}
	return s.insert(s.linkAnchor(n, s.Len()), n)
}

// InsertAt puts n at position at of its partition's positions in s (an opaque n: at counts all
// siblings, appended past the end). Under a narrow journal a schema node's insertion is recorded
// in the store's journal instead of a whole capture.
func (s *siblings) InsertAt(at int, n *Node) error {
	if j := s.narrow(); j != nil && n.schema != nil {
		if s.st == nil {
			if err := recordField(j, &s.st); err != nil {
				return err
			}
		}
		s.bindStore(j)
	} else if err := s.touch(); err != nil {
		return err
	}
	return s.insert(s.linkAnchor(n, at), n)
}

// removeBound bounds DeleteAt.
func (s *siblings) removeBound(journaled bool) uint64 {
	if s.st == nil {
		return 0
	}
	return addHashBound(s.st.seq.RemoveBound(journaled), slotUnits(journaled))
}

func (s *siblings) DeleteAt(at int) error {
	if err := s.touch(); err != nil {
		return err
	}
	return s.remove(s.st.peek(at))
}

func (s *siblings) remove(h *opidx.Handle[*Node]) error {
	st := s.st
	paid, err := s.payStore(s.removeBound(s.storeJournaled()))
	if err != nil {
		return err
	}
	defer paid.close()
	st.ver++
	n := h.Value
	st.seq.Remove(h)
	n.slot.Set(&st.bind, nil)
	if n.schema != nil {
		opidx.AddInt(&st.bind, &st.nschema, -1)
	}
	return nil
}

// deleteMatching removes the schema nodes schemaMatch matches and the opaque ones opaqueMatch
// matches (nil: none), calling each predicate once per node of its partition, in order: one
// O(n) pass over the storage (Keep), whatever it removes.
func (s *siblings) deleteMatching(schemaMatch, opaqueMatch func(*Node) bool) error {
	if err := s.touch(); err != nil {
		return err
	}
	st := s.st
	if st == nil {
		return nil
	}
	journaled := s.storeJournaled()
	paid, err := s.payStore(addHashBound(st.seq.KeepBound(journaled),
		mulHashBound(uint64(st.seq.Len()), slotUnits(journaled)))) //nolint:gosec // a length is nonnegative
	if err != nil {
		return err
	}
	defer paid.close()
	gone := st.seq.Keep(func(n *Node) bool {
		match := schemaMatch
		if n.schema == nil {
			match = opaqueMatch
		}
		return match == nil || !match(n)
	})
	if len(gone) == 0 {
		return nil
	}
	st.ver++
	schemaGone := 0
	for _, h := range gone {
		h.Value.slot.Set(&st.bind, nil)
		if h.Value.schema != nil {
			schemaGone++
		}
	}
	opidx.AddInt(&st.bind, &st.nschema, -schemaGone)
	return nil
}

// transferFrom moves the entire container, including native history and work binding; no node
// is relinked. The caller rebinds its new owner afterwards.
func (s *siblings) transferFrom(src *siblings) error {
	if err := s.touch(); err != nil {
		return err
	}
	if err := src.touch(); err != nil {
		return err
	}
	if st := src.st; st != nil && st.bind.state != nil {
		// the binding moves with the store: one entry restores its owner
		if err := recordField(st.bind.state, &st.bind.owner); err != nil {
			return err
		}
	}
	*s, *src = *src, siblings{}
	if s.st != nil {
		s.st.bind.owner = s // a pointer write: the hooks stay installed
	}
	return nil
}

// reset discards the container. Its nodes give their slots back first (journaled, paid): a
// discarded root a caller keeps must not keep its former siblings alive through the storage.
func (s *siblings) reset() error {
	if err := s.touch(); err != nil {
		return err
	}
	if st := s.st; st != nil && st.seq.Len() != 0 && !st.view {
		paid, err := s.payStore(mulHashBound(uint64(st.seq.Len()), slotUnits(s.storeJournaled()))) //nolint:gosec // a length is nonnegative
		if err != nil {
			return err
		}
		for h := st.seq.First(); h != nil; h = st.seq.Next(h) {
			s.visit()
			h.Value.slot.Set(&st.bind, nil)
		}
		paid.close()
	}
	*s = siblings{}
	return nil
}

// replaceSchema republishes the run [lo, hi) in the order of nodes, which hold exactly its
// members: their handles keep their identities, the index unregisters them first and registers
// them again (their relative order changes). It pays its bound before the first write.
func (s *siblings) replaceSchema(lo, hi int, nodes siblingSpan) error {
	if err := s.touch(); err != nil {
		return err
	}
	if hi-lo < 2 {
		return nil
	}
	st := s.st
	journaled := s.storeJournaled()
	bound := s.republishBound(lo, hi, 0, journaled, s.work.planPay())
	if s.order != nil {
		for _, n := range nodes.All() {
			bound = addHashBound(bound, s.orderRegisterBound(n, journaled))
		}
	}
	paid, err := s.payStore(bound)
	if err != nil {
		return err
	}
	defer paid.close()
	ordered := nodes.Collect()
	if s.order != nil {
		for _, n := range ordered {
			s.orderUnregister(n)
		}
	}
	st.moves++
	plo, phi := st.extent(lo, hi)
	if _, ok := st.seq.Republish(plo, phi, st.extentValues(plo, phi, ordered), s.handle); !ok {
		panic("data: a republished run does not hold exactly its range")
	}
	if s.order != nil {
		for _, n := range ordered {
			s.orderRegister(n, n.slot.Get())
		}
	}
	return nil
}

// publishRun replaces the run [lo, hi) with nodes, which hold its members and new nodes to link
// (rbPublish): members keep their handles, new nodes get slots. A present index is dropped first
// and its caller builds it again (every position after lo moves).
func (s *siblings) publishRun(lo, hi int, nodes []*Node) error {
	if err := s.touch(); err != nil {
		return err
	}
	st := s.store()
	fresh := len(nodes) - (hi - lo)
	journaled := s.storeJournaled()
	bound := addHashBound(s.republishBound(lo, hi, fresh, journaled, s.work.planPay()),
		mulHashBound(uint64(fresh), slotUnits(journaled))) //nolint:gosec // a count is nonnegative
	paid, err := s.payStore(bound)
	if err != nil {
		return err
	}
	defer paid.close()
	s.order = nil
	st.ver++
	plo, phi := st.extent(lo, hi)
	values := st.extentValues(plo, phi, nodes)
	hs, ok := st.seq.Republish(plo, phi, values, s.handle)
	if !ok {
		panic("data: a published run does not hold its range")
	}
	for i, n := range values {
		if n.slot.Get() != hs[i] {
			n.slot.Set(&st.bind, hs[i])
			opidx.AddInt(&st.bind, &st.nschema, 1)
		}
	}
	return nil
}

// schemaAnchor is storeAnchor's handle for a schema node at at.
func (st *siblingStore) schemaAnchor(at int) *opidx.Handle[*Node] {
	at = min(max(at, 0), st.nschema)
	if at < st.nschema || !st.interleaved() {
		return st.peek(at)
	}
	if last := st.seq.LastKind(true); last != nil {
		return st.seq.Next(last)
	}
	return st.seq.First()
}

// runPiece is hi cut at the first opaque sibling after schema node lo: in an interleaved set an
// opaque node ends a run as another schema node does (LYD_LIST_FOR_INST, lyds; design 15). One
// O(log n) point read.
func (s *siblings) runPiece(lo, hi int) int {
	if lo >= hi || s.st == nil || !s.st.interleaved() {
		return hi
	}
	h := s.st.peek(lo)
	o := s.st.seq.PeekNextOfKind(h, false)
	if o == nil {
		return hi
	}
	from, _ := s.st.seq.Pos(h)
	to, _ := s.st.seq.Pos(o)
	return min(hi, lo+to-from) // the handles between h and o are schema nodes
}

// runPieceStart is the first logical position of the run piece that holds schema node x: past
// the last opaque sibling before it in an interleaved set (runPiece's mirror), 0 otherwise.
func (s *siblings) runPieceStart(x int) int {
	if x <= 0 || s.st == nil || !s.st.interleaved() {
		return 0
	}
	h := s.st.peek(x)
	o := s.st.seq.PeekPrevOfKind(h, false)
	if o == nil {
		return 0
	}
	from, _ := s.st.seq.Pos(o)
	to, _ := s.st.seq.Pos(h)
	return x - (to - from - 1) // the handles between o and h are schema nodes
}

// extent is the physical range [plo, phi) that the logical schema range [lo, hi) spans: the same
// range unless s is interleaved, where it includes the opaque nodes between its members (an
// empty range: where a schema node at lo goes).
func (st *siblingStore) extent(lo, hi int) (int, int) {
	if st == nil || !st.interleaved() {
		return lo, hi
	}
	if lo >= hi {
		anchor := st.schemaAnchor(lo)
		if anchor == nil {
			return st.seq.Len(), st.seq.Len()
		}
		at, _ := st.seq.Pos(anchor)
		return at, at
	}
	a, _ := st.seq.Pos(st.peek(lo))
	b, _ := st.seq.Pos(st.peek(hi - 1))
	return a, b + 1
}

// extentValues is the republication of the extent [plo, phi) with the run's members in the
// order of run: an opaque node inside the extent keeps its slot, the schema slots take run's
// nodes in order, and the nodes past them (publishRun's new ones) follow the extent (design 15).
func (st *siblingStore) extentValues(plo, phi int, run []*Node) []*Node {
	if !st.interleaved() {
		return run
	}
	values := make([]*Node, 0, phi-plo+len(run))
	j := 0
	for h, k := st.seq.Peek(plo), plo; k < phi; h, k = st.seq.Next(h), k+1 {
		if h.Value.schema == nil {
			values = append(values, h.Value)
			continue
		}
		values = append(values, run[j])
		j++
	}
	return append(values, run[j:]...)
}

// republishBound bounds republishing the logical schema range [lo, hi) with fresh new nodes:
// the sequence's bound over its physical extent, and the extent's assembly.
func (s *siblings) republishBound(lo, hi, fresh int, journaled bool, pay opidx.Pay) uint64 {
	plo, phi := s.st.extent(lo, hi)
	bound := s.st.seq.RepublishBound(plo, phi, fresh, journaled, pay)
	if s.st.interleaved() {
		bound = addHashBound(bound, uint64(phi-plo)) //nolint:gosec // a range length is nonnegative
	}
	return bound
}

// extentGap is the number of opaque nodes inside the physical extent of [lo, hi).
func (s *siblings) extentGap(lo, hi int) int {
	plo, phi := s.st.extent(lo, hi)
	return phi - plo - (hi - lo)
}

// cacheDeque is unrelated duplication-cache storage: a node slice with its backing allocation
// retained for front headroom.
type cacheDeque struct{ nodes, base []*Node }

// insertAt puts n at position at and returns how many slots it shifted: the shorter side moves,
// the front into retained headroom, so appending and prepending are amortized O(1).
func (d *cacheDeque) insertAt(at int, n *Node) int {
	l := d.nodes
	if at >= len(l)/2 || len(l) == 0 {
		c := cap(l)
		d.nodes = slices.Insert(l, at, n)
		if cap(d.nodes) != c {
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
	d.nodes = nl
	return at
}

// deleteAt removes position at and returns the number of slots shifted, moving the shorter side.
func (d *cacheDeque) deleteAt(at int) int {
	l := d.nodes
	if at < len(l)-1-at {
		copy(l[1:at+1], l[:at])
		if len(d.base) == 0 {
			d.base = l[:cap(l)]
		}
		l[0] = nil
		d.nodes = l[1:]
		return at
	}
	copy(l[at:], l[at+1:])
	l[len(l)-1] = nil
	d.nodes = l[:len(l)-1]
	return len(l) - 1 - at
}

func (d *cacheDeque) schemaSpan() siblingSpan { return spanOf(d.nodes) }

// siblingSpan is a borrowed range: of a store (a live view, invalidated by the store's next
// write) or of a detached slice (logical(), collected runs). Neither links or reparents nodes.
type siblingSpan struct {
	nodes  []*Node
	st     *siblingStore
	lo, hi int
	ver    uint64
	// doc: positions are physical (document order, docSpan), not logical
	doc bool
}

// physical reports whether r's positions are physical ones: a document-order span, or any span
// of a set that is not interleaved (where both coincide).
func (r siblingSpan) physical() bool { return r.doc || !r.st.interleaved() }

func (r siblingSpan) peek(i int) *opidx.Handle[*Node] {
	if r.physical() {
		return r.st.seq.Peek(i)
	}
	return r.st.peek(i)
}

func (r siblingSpan) next(h *opidx.Handle[*Node]) *opidx.Handle[*Node] {
	if r.physical() {
		return r.st.seq.Next(h)
	}
	return r.st.nextL(h)
}

func (r siblingSpan) prev(h *opidx.Handle[*Node]) *opidx.Handle[*Node] {
	if r.physical() {
		return r.st.seq.Prev(h)
	}
	return r.st.prevL(h)
}

func spanOf(nodes []*Node) siblingSpan { return siblingSpan{nodes: nodes} }

func (r siblingSpan) check() {
	if checkSpans && r.st != nil && r.st.ver != r.ver {
		panic("data: a borrowed sibling span read after its storage changed")
	}
}

// reborrow is the span again after writes its borrower knows to have left every position as it
// was (a temporary node linked and unlinked again).
func (r siblingSpan) reborrow() siblingSpan {
	if r.st != nil {
		r.ver = r.st.ver
	}
	return r
}

func (r siblingSpan) Len() int {
	if r.st == nil {
		return len(r.nodes)
	}
	return r.hi - r.lo
}

func (r siblingSpan) At(i int) *Node {
	if r.st == nil {
		return r.nodes[i]
	}
	r.check()
	if i < 0 || i >= r.hi-r.lo {
		panic("data: sibling span index out of range")
	}
	if r.physical() {
		return r.st.atPhys(r.lo + i)
	}
	return r.st.at(r.lo + i)
}

func (r siblingSpan) Slice(lo, hi int) siblingSpan {
	if r.st == nil {
		return spanOf(r.nodes[lo:hi])
	}
	if lo < 0 || hi < lo || hi > r.hi-r.lo {
		panic("data: sibling span slice out of range")
	}
	return siblingSpan{st: r.st, lo: r.lo + lo, hi: r.lo + hi, ver: r.ver, doc: r.doc}
}

func (r siblingSpan) Collect() []*Node { return r.AppendTo(make([]*Node, 0, r.Len())) }

func (r siblingSpan) AppendTo(dst []*Node) []*Node {
	for _, n := range r.All() {
		dst = append(dst, n)
	}
	return dst
}

func (r siblingSpan) All() iter.Seq2[int, *Node] {
	return func(yield func(int, *Node) bool) {
		if r.st == nil {
			for i, n := range r.nodes {
				if !yield(i, n) {
					return
				}
			}
			return
		}
		st := r.st
		h, moves := r.peek(r.lo), st.moves
		for i := 0; i < r.hi-r.lo; i++ {
			r.check()
			if st.moves != moves {
				h, moves = r.peek(r.lo+i), st.moves
			}
			if !yield(i, h.Value) {
				return
			}
			h = r.next(h)
		}
	}
}

// spanCursor reads a span forward one node at a time (next), for a walk that cannot be a range
// loop (two spans in step); like an iterator it is invalidated by a write that moves positions.
type spanCursor struct {
	r siblingSpan
	h *opidx.Handle[*Node]
	i int
}

func (r siblingSpan) cursor() spanCursor {
	c := spanCursor{r: r}
	if r.st != nil && r.Len() > 0 {
		c.h = r.peek(r.lo)
	}
	return c
}

// next is the next node, nil past the end.
func (c *spanCursor) next() *Node {
	if c.i >= c.r.Len() {
		return nil
	}
	c.i++
	if c.r.st == nil {
		return c.r.nodes[c.i-1]
	}
	c.r.check()
	n := c.h.Value
	c.h = c.r.next(c.h)
	return n
}

// Reverse walks the span backwards, keeping full and subtree walks linear.
func (r siblingSpan) Reverse() iter.Seq2[int, *Node] {
	return func(yield func(int, *Node) bool) {
		if r.st == nil {
			for i := len(r.nodes) - 1; i >= 0; i-- {
				if !yield(i, r.nodes[i]) {
					return
				}
			}
			return
		}
		st := r.st
		h, moves := r.peek(r.hi-1), st.moves
		for i := r.hi - r.lo - 1; i >= 0; i-- {
			r.check()
			if st.moves != moves {
				h, moves = r.peek(r.lo+i), st.moves
			}
			if !yield(i, h.Value) {
				return
			}
			h = r.prev(h)
		}
	}
}

// LowerBound returns the first index of the monotone bounded range [lo, hi) for which pred
// holds, hi if none. Over storage it is one sequence descent (design 14): O(log n) structural
// visits, counted as sibling visits, and at most one predicate call per level, which callers
// count or charge themselves. A detached span keeps BinarySearchFunc's probes. Over logical
// positions of an interleaved set it is a binary search of kind-rank probes (design 15):
// O(log² n) visits, counted, and one predicate call per probe.
func (r siblingSpan) LowerBound(lo, hi int, pred func(*Node) bool) int {
	if r.st != nil && !r.physical() {
		r.check()
		for lo < hi {
			mid := lo + (hi-lo)/2
			if pred(r.At(mid)) {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
		return lo
	}
	if r.st != nil {
		r.check()
		var visit func()
		if owner := r.st.bind.owner; owner != nil {
			visit = owner.visit
		}
		return r.st.seq.BoundIn(r.lo+lo, r.lo+hi, pred, visit) - r.lo
	}
	end := hi
	for lo < hi {
		mid := lo + (hi-lo)/2
		if pred(r.At(mid)) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	if lo < end {
		pred(r.At(lo))
	}
	return lo
}

// Index is the position of n in the span, -1 if absent; uncounted.
func (r siblingSpan) Index(n *Node) int {
	if r.st == nil {
		return slices.Index(r.nodes, n)
	}
	r.check()
	owner := siblings{st: r.st}
	h := owner.handle(n)
	if h == nil {
		return -1
	}
	var at int
	if r.physical() {
		at, _ = r.st.seq.Pos(h)
	} else {
		at, _ = r.st.pos(h)
	}
	if at >= r.lo && at < r.hi {
		return at - r.lo
	}
	return -1
}

func (s *siblings) reverseAll() iter.Seq2[int, *Node] { return s.docSpan().Reverse() }

// snapshotStorage is the whole-capture image of s: the store pointer, not its content (the
// store's own journal restores that).
func (s *siblings) snapshotStorage() siblings { return *s }

// restoreStorage uses already reserved undo state and cannot fail or journal.
func (s *siblings) restoreStorage(saved siblings) { *s = saved }

// siblingCursor reads a borrowed forest incrementally, without materialising a wide child set
// or repeatedly selecting by rank. Mutation invalidates it.
type siblingCursor struct {
	h     *opidx.Handle[*Node]
	owner *siblings
}

func (s *siblings) cursor() siblingCursor {
	c := siblingCursor{owner: s}
	if s.st != nil {
		c.h = s.st.seq.First()
	}
	return c
}
func (c *siblingCursor) done() bool { return c.h == nil }
func (c *siblingCursor) take() *Node {
	n := c.h.Value
	c.h = c.owner.st.seq.Next(c.h)
	return n
}

func (c *siblingCursor) visit() { c.owner.visit() }

// restoreFields retains the separately journaled sibling container of a node.
func (n *Node) restoreFields(saved Node) { kids := n.kids; *n = saved; n.kids = kids }
