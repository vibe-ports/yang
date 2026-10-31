// SPDX-License-Identifier: BSD-3-Clause

// Package opidx provides operation-local ordered indexes. Its structures own no
// data nodes and charge work through optional, caller-owned hooks.
package opidx

import (
	"fmt"
	"math"
	"math/bits"
)

const (
	tagBits            = 63
	tagUniverse uint64 = 1 << tagBits
)

// Handle is a stable identity in a Seq. A removed handle cannot be inserted
// again; callers create a fresh handle for a later occurrence.
type Handle[T any] struct {
	Value       T
	left, right *Handle[T]
	parent      *Handle[T]
	prev, next  *Handle[T]
	owner       *Seq[T]
	height      int
	size        int
	// heavy is the handle's kind (the sequence's weigh function, fixed at creation); hsize counts
	// the heavy handles of its subtree, maintained with size (design 15)
	heavy bool
	hsize int
	// kprev, knext thread the handles of one kind, maintained once kind threads are on
	kprev, knext *Handle[T]
	tag          uint64
	stamp        uint64 // the journal epoch of its last recorded image
}

type seqHeader[T any] struct {
	root, first, last *Handle[T]
	length            int
	scale             int
	unlabeled         bool
	kinds             bool
}

type seqCheckpoint[T any] struct {
	id         uint64
	header     seqHeader[T]
	before     map[*Handle[T]]Handle[T]
	created    []*Handle[T]
	createdSet map[*Handle[T]]struct{}
}

// Checkpoint identifies a mutation journal position. Checkpoints are completed
// in last-in, first-out order.
type Checkpoint[T any] struct {
	seq *Seq[T]
	id  uint64
}

// Seq maintains physical order, positions, and constant-time order labels.
// Insertions have amortized O(log n) structural and relabel work; remove,
// rank, and select take O(log n). First, Last, Next, Prev, and Compare are O(1).
// A Seq must not be copied after first use.
type Seq[T any] struct {
	root, first, last *Handle[T]
	length            int
	scale             int
	// unlabeled: the order labels are not maintained (Compare falls back to ranks); a sequence
	// without an ordered index over it does not pay for them (#240 step 5)
	unlabeled bool
	// kinds: the kind threads (kprev, knext) are maintained (SetKindThreads)
	kinds bool
	// weigh is the kind of a new handle's value (nil: every handle is light)
	weigh        func(T) bool
	work         func(uint64)
	hooks        Hooks
	stamp        uint64 // the journal epoch of the last recorded header
	checkpointID uint64
	checkpoints  []*seqCheckpoint[T]
}

// SetWorkHook sets a synchronous callback for node visits, rotations, splits,
// joins, relabels, and journal before-images. It must not mutate this sequence.
// Nil disables counting.
func (s *Seq[T]) SetWorkHook(hook func(uint64)) { s.work = hook }

// SetWeigh sets the kind of every handle created later: heavy when weigh reports true. Handles
// keep their kind; set it before the first insertion.
func (s *Seq[T]) SetWeigh(weigh func(T) bool) { s.weigh = weigh }

// newHandle is a fresh handle of value, not yet in the tree.
func (s *Seq[T]) newHandle(value T) *Handle[T] {
	h := &Handle[T]{Value: value, owner: s, height: 1, size: 1}
	if s.weigh != nil && s.weigh(value) {
		h.heavy, h.hsize = true, 1
	}
	return h
}

// SetHooks connects s to its owner's work counter and journal; they replace the work hook.
func (s *Seq[T]) SetHooks(h Hooks) { s.hooks = h }

func (s *Seq[T]) charge() {
	if s.hooks != nil {
		s.hooks.Work(1)
	} else if s.work != nil {
		s.work(1)
	}
}

func (s *Seq[T]) journal() *Journal {
	if s.hooks == nil {
		return nil
	}
	return s.hooks.Journal()
}

// touchHeader records the header before a mutation that may change it.
func (s *Seq[T]) touchHeader() {
	if j := s.journal(); j != nil && j.first(&s.stamp) {
		var kinds uint64
		if s.kinds {
			kinds = 1
		}
		j.image(Entry{obj: s, ptr: [6]any{s.root, s.first, s.last}, num: [3]uint64{uint64(s.length), uint64(s.scale), kinds}, had: s.unlabeled}, s.hooks) //nolint:gosec // restored bit for bit
	}
}

func (s *Seq[T]) undo(e *Entry) {
	s.root, _ = e.ptr[0].(*Handle[T])
	s.first, _ = e.ptr[1].(*Handle[T])
	s.last, _ = e.ptr[2].(*Handle[T])
	s.length, s.scale = int(e.num[0]), int(e.num[1]) //nolint:gosec // the recorded ints themselves
	s.unlabeled = e.had
	s.kinds = e.num[2] != 0
}

func (s *Seq[T]) purge(*Entry) {}

func (h *Handle[T]) undo(e *Entry) {
	h.left, _ = e.ptr[0].(*Handle[T])
	h.right, _ = e.ptr[1].(*Handle[T])
	h.parent, _ = e.ptr[2].(*Handle[T])
	h.prev, _ = e.ptr[3].(*Handle[T])
	h.next, _ = e.ptr[4].(*Handle[T])
	h.owner, _ = e.ptr[5].(*Seq[T])
	h.height, h.hsize = int(e.num[0]&0xff), int(e.num[0]>>8)
	h.size, h.tag = int(e.num[1]), e.num[2] //nolint:gosec // the recorded int itself
	h.kprev, h.knext = nil, nil
	if l, ok := e.key.(kindLinks[T]); ok {
		h.kprev, h.knext = l.prev, l.next
	}
}

// kindLinks is a handle image's kind threads (absent: both nil, no allocation).
type kindLinks[T any] struct{ prev, next *Handle[T] }

func (h *Handle[T]) purge(*Entry) {}

func handleImage[T any](h *Handle[T]) Entry {
	e := Entry{obj: h, ptr: [6]any{h.left, h.right, h.parent, h.prev, h.next, h.owner},
		num: [3]uint64{uint64(h.height) | uint64(h.hsize)<<8, uint64(h.size), h.tag}} //nolint:gosec // restored bit for bit; a height is below 256
	if h.kprev != nil || h.knext != nil {
		e.key = kindLinks[T]{h.kprev, h.knext}
	}
	return e
}

// created records a handle allocated under a journal as detached: rollback leaves it as a
// removed handle, so a reference that survives the rollback cannot reach the sequence.
func (s *Seq[T]) created(h *Handle[T]) {
	if j := s.journal(); j != nil && j.first(&h.stamp) {
		j.image(Entry{obj: h, ptr: [6]any{(*Handle[T])(nil), (*Handle[T])(nil), (*Handle[T])(nil),
			(*Handle[T])(nil), (*Handle[T])(nil), (*Seq[T])(nil)}}, s.hooks)
	}
}

func (s *Seq[T]) header() seqHeader[T] {
	return seqHeader[T]{s.root, s.first, s.last, s.length, s.scale, s.unlabeled, s.kinds}
}

func (s *Seq[T]) restoreHeader(h seqHeader[T]) {
	s.root, s.first, s.last, s.length, s.scale, s.unlabeled, s.kinds = h.root, h.first, h.last, h.length, h.scale, h.unlabeled, h.kinds
}

// touch records h before its first mutation after every active checkpoint.
func (s *Seq[T]) touch(h *Handle[T]) {
	if h == nil {
		return
	}
	if j := s.journal(); j != nil && j.first(&h.stamp) {
		j.image(handleImage(h), s.hooks)
	}
	for _, checkpoint := range s.checkpoints {
		if _, ok := checkpoint.before[h]; ok {
			continue
		}
		if _, created := checkpoint.createdSet[h]; created {
			continue
		}
		checkpoint.before[h] = *h
		s.charge()
	}
}

func (s *Seq[T]) trackCreated(h *Handle[T]) {
	for _, checkpoint := range s.checkpoints {
		checkpoint.created = append(checkpoint.created, h)
		checkpoint.createdSet[h] = struct{}{}
		s.charge()
	}
}

// Checkpoint starts a mutation journal. Creating one takes constant time.
func (s *Seq[T]) Checkpoint() *Checkpoint[T] {
	s.checkpointID++
	checkpoint := &seqCheckpoint[T]{
		id:         s.checkpointID,
		header:     s.header(),
		before:     make(map[*Handle[T]]Handle[T]),
		createdSet: make(map[*Handle[T]]struct{}),
	}
	s.checkpoints = append(s.checkpoints, checkpoint)
	s.charge() // the sequence-header before-image
	return &Checkpoint[T]{seq: s, id: checkpoint.id}
}

func (s *Seq[T]) topCheckpoint(checkpoint *Checkpoint[T]) (*seqCheckpoint[T], bool) {
	if checkpoint == nil || checkpoint.seq != s || len(s.checkpoints) == 0 {
		return nil, false
	}
	top := s.checkpoints[len(s.checkpoints)-1]
	return top, top.id == checkpoint.id
}

// Commit keeps the changes after checkpoint. Nested checkpoints must be
// completed in last-in, first-out order.
func (s *Seq[T]) Commit(checkpoint *Checkpoint[T]) bool {
	if _, ok := s.topCheckpoint(checkpoint); !ok {
		return false
	}
	last := len(s.checkpoints) - 1
	s.checkpoints[last] = nil
	s.checkpoints = s.checkpoints[:last]
	checkpoint.seq = nil
	return true
}

// Rollback restores the exact sequence state at checkpoint. Handles allocated
// after it are detached. Nested checkpoints must be completed in LIFO order.
func (s *Seq[T]) Rollback(checkpoint *Checkpoint[T]) bool {
	top, ok := s.topCheckpoint(checkpoint)
	if !ok {
		return false
	}
	for h, before := range top.before {
		*h = before
		s.charge()
	}
	for _, h := range top.created {
		h.left, h.right, h.parent, h.prev, h.next, h.owner = nil, nil, nil, nil, nil, nil
		h.height, h.size, h.hsize, h.tag = 0, 0, 0, 0
		h.kprev, h.knext = nil, nil
		s.charge()
	}
	s.restoreHeader(top.header)
	last := len(s.checkpoints) - 1
	s.checkpoints[last] = nil
	s.checkpoints = s.checkpoints[:last]
	checkpoint.seq = nil
	return true
}

// Len returns the number of live handles.
func (s *Seq[T]) Len() int { return s.length }

// First returns the first handle, or nil for an empty sequence.
func (s *Seq[T]) First() *Handle[T] { return s.first }

// Last returns the last handle, or nil for an empty sequence.
func (s *Seq[T]) Last() *Handle[T] { return s.last }

// Next returns h's physical successor, or nil for an invalid or last handle.
func (s *Seq[T]) Next(h *Handle[T]) *Handle[T] {
	if h == nil || h.owner != s {
		return nil
	}
	return h.next
}

// Prev returns h's physical predecessor, or nil for an invalid or first handle.
func (s *Seq[T]) Prev(h *Handle[T]) *Handle[T] {
	if h == nil || h.owner != s {
		return nil
	}
	return h.prev
}

// Compare returns -1, 0, or 1 in physical order. The bool is false for a
// removed handle, a handle from another sequence, or nil.
func (s *Seq[T]) Compare(a, b *Handle[T]) (int, bool) {
	if a == nil || b == nil || a.owner != s || b.owner != s {
		return 0, false
	}
	if s.unlabeled {
		x, _ := s.Pos(a)
		y, _ := s.Pos(b)
		switch {
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		default:
			return 0, true
		}
	}
	switch {
	case a.tag < b.tag:
		return -1, true
	case a.tag > b.tag:
		return 1, true
	default:
		return 0, true
	}
}

// Rank returns a zero-based position. The bool is false for a foreign or removed handle.
func (s *Seq[T]) Rank(h *Handle[T]) (int, bool) {
	if h == nil || h.owner != s {
		return 0, false
	}
	s.charge()
	rank := nodeSize(h.left)
	for h.parent != nil {
		s.charge()
		if h == h.parent.right {
			rank += 1 + nodeSize(h.parent.left)
		}
		h = h.parent
	}
	return rank, true
}

// Select returns the handle at zero-based rank, or nil out of range.
func (s *Seq[T]) Select(rank int) *Handle[T] {
	if rank < 0 || rank >= s.length {
		return nil
	}
	return s.selectNode(s.root, rank)
}

func (s *Seq[T]) selectNode(root *Handle[T], rank int) *Handle[T] {
	for n := root; n != nil; {
		s.charge()
		left := nodeSize(n.left)
		switch {
		case rank < left:
			n = n.left
		case rank > left:
			rank -= left + 1
			n = n.right
		default:
			return n
		}
	}
	return nil
}

// LowerBound returns the first rank at or after lo for which pred is true, or
// Len if none exists. pred must be monotone (false values followed by true values).
func (s *Seq[T]) LowerBound(lo int, pred func(T) bool) int { return s.bound(lo, pred) }

// UpperBound is the upper-bound counterpart of LowerBound. Callers supply the
// strict predicate (for example value > key instead of value >= key).
func (s *Seq[T]) UpperBound(lo int, pred func(T) bool) int { return s.bound(lo, pred) }

func (s *Seq[T]) bound(lo int, pred func(T) bool) int {
	if lo < 0 {
		lo = 0
	}
	if lo >= s.length || pred == nil {
		return s.length
	}
	answer, base := s.length, 0
	for n := s.root; n != nil; {
		s.charge()
		rank := base + nodeSize(n.left)
		switch {
		case rank < lo:
			base, n = rank+1, n.right
		case pred(n.Value):
			answer, n = rank, n.left
		default:
			base, n = rank+1, n.right
		}
	}
	return answer
}

// InsertBefore inserts value before anchor. A nil anchor appends.
func (s *Seq[T]) InsertBefore(anchor *Handle[T], value T) *Handle[T] {
	if anchor != nil && anchor.owner != s {
		return nil
	}
	var rank int
	var prev, next *Handle[T]
	if anchor == nil {
		rank, prev = s.length, s.last
	} else {
		rank, _ = s.Rank(anchor)
		prev, next = anchor.prev, anchor
	}
	return s.insert(rank, prev, next, value)
}

// InsertAfter inserts value after anchor. A nil anchor prepends.
func (s *Seq[T]) InsertAfter(anchor *Handle[T], value T) *Handle[T] {
	if anchor != nil && anchor.owner != s {
		return nil
	}
	var rank int
	var prev, next *Handle[T]
	if anchor == nil {
		next = s.first
	} else {
		rank, _ = s.Rank(anchor)
		rank++
		prev, next = anchor, anchor.next
	}
	return s.insert(rank, prev, next, value)
}

func (s *Seq[T]) insert(rank int, prev, next *Handle[T], value T) *Handle[T] {
	h := s.newHandle(value)
	h.prev, h.next = prev, next
	s.trackCreated(h)
	s.created(h)
	s.touchHeader()
	if prev == nil {
		s.first = h
	} else {
		s.touch(prev)
		prev.next = h
	}
	if next == nil {
		s.last = h
	} else {
		s.touch(next)
		next.prev = h
	}
	s.root = s.insertAt(s.root, rank, h)
	s.setParent(s.root, nil)
	if s.kinds {
		s.relinkKinds(h, h)
	}
	s.length++
	if s.scale == 0 {
		s.scale = 1
	}
	if s.length > 2*s.scale {
		s.scale = s.length
		s.rebuildTags()
	} else {
		s.label(h)
	}
	return h
}

// Remove detaches h and returns whether it belonged to the sequence.
func (s *Seq[T]) Remove(h *Handle[T]) bool {
	rank, ok := s.Rank(h)
	if !ok {
		return false
	}
	s.touchHeader()
	s.root = s.removeAt(s.root, rank)
	s.setParent(s.root, nil)
	s.unlink(h)
	s.unlinkKind(h)
	s.detach(h)
	s.length--
	if s.length == 0 {
		s.scale = 0
	} else if s.length < s.scale/2 {
		s.scale = s.length
		s.rebuildTags()
	}
	return true
}

// MoveBefore relocates a live handle before anchor while preserving its identity.
func (s *Seq[T]) MoveBefore(h, anchor *Handle[T]) bool {
	if h == nil || h.owner != s || anchor != nil && anchor.owner != s {
		return false
	}
	if h == anchor {
		return true
	}
	from, _ := s.Rank(h)
	to := s.length
	if anchor != nil {
		to, _ = s.Rank(anchor)
	}
	return s.MoveRange(from, from+1, to)
}

// Splice removes delete handles at lo and inserts values in their place. It
// returns handles for inserted values. Invalid ranges are rejected before mutation.
func (s *Seq[T]) Splice(lo, deleteCount int, values []T) ([]*Handle[T], bool) {
	if lo < 0 || deleteCount < 0 || lo > s.length || deleteCount > s.length-lo {
		return nil, false
	}
	inserted := make([]*Handle[T], len(values))
	for i, value := range values {
		inserted[i] = s.newHandle(value)
		s.trackCreated(inserted[i])
		s.created(inserted[i])
	}
	s.touchHeader()
	s.spliceHandles(lo, deleteCount, inserted)
	return inserted, true
}

// ReplaceRange replaces [lo, hi) with values.
func (s *Seq[T]) ReplaceRange(lo, hi int, values []T) ([]*Handle[T], bool) {
	if hi < lo {
		return nil, false
	}
	return s.Splice(lo, hi-lo, values)
}

// ReplaceHandles republishes [lo, hi) in the order given by handles without
// changing their identities. Every handle must occur exactly once in that
// range. It is the bulk primitive for stable-sort and retained-order replay.
func (s *Seq[T]) ReplaceHandles(lo, hi int, handles []*Handle[T]) bool {
	if lo < 0 || hi < lo || hi > s.length || len(handles) != hi-lo {
		return false
	}
	allowed := make(map[*Handle[T]]struct{}, len(handles))
	for h, remaining := s.Select(lo), len(handles); remaining > 0; h, remaining = h.next, remaining-1 {
		s.charge()
		allowed[h] = struct{}{}
	}
	seen := make(map[*Handle[T]]struct{}, len(handles))
	for _, h := range handles {
		s.charge()
		if h == nil || h.owner != s {
			return false
		}
		if _, duplicate := seen[h]; duplicate {
			return false
		}
		if _, ok := allowed[h]; !ok {
			return false
		}
		seen[h] = struct{}{}
	}
	if len(handles) == 0 {
		return true
	}
	var prev, next *Handle[T]
	if lo > 0 {
		prev = s.Select(lo - 1)
	}
	if hi < s.length {
		next = s.Select(hi)
	}
	s.touchHeader()
	for _, h := range handles {
		s.unlinkKind(h)
	}
	left, tail := s.split(s.root, lo)
	_, right := s.split(tail, hi-lo)
	middle := s.build(handles, 0, len(handles), nil)
	s.root = s.join(s.join(left, middle), right)
	s.setParent(s.root, nil)
	for i, h := range handles {
		s.touch(h)
		h.prev = valueAtHandle(handles, i-1, prev)
		h.next = valueAtHandle(handles, i+1, next)
	}
	if prev == nil {
		s.first = handles[0]
	} else {
		s.touch(prev)
		prev.next = handles[0]
	}
	if next == nil {
		s.last = handles[len(handles)-1]
	} else {
		s.touch(next)
		next.prev = handles[len(handles)-1]
	}
	if s.kinds {
		s.relinkKinds(handles[0], handles[len(handles)-1])
	}
	s.labelRange(handles[0], handles[len(handles)-1], len(handles))
	return true
}

func (s *Seq[T]) spliceHandles(lo, deleteCount int, inserted []*Handle[T]) {
	var prev, next *Handle[T]
	if lo > 0 {
		prev = s.Select(lo - 1)
	}
	if lo+deleteCount < s.length {
		next = s.Select(lo + deleteCount)
	}
	left, tail := s.split(s.root, lo)
	removed, right := s.split(tail, deleteCount)
	removedHandles := make([]*Handle[T], 0, deleteCount)
	s.collect(removed, &removedHandles)
	middle := s.build(inserted, 0, len(inserted), nil)
	s.root = s.join(s.join(left, middle), right)
	s.setParent(s.root, nil)
	if prev != nil {
		s.touch(prev)
		prev.next = valueAtHandle(inserted, 0, next)
	} else {
		s.first = valueAtHandle(inserted, 0, next)
	}
	if next != nil {
		s.touch(next)
		next.prev = valueAtHandle(inserted, len(inserted)-1, prev)
	} else {
		s.last = valueAtHandle(inserted, len(inserted)-1, prev)
	}
	for i, h := range inserted {
		s.touch(h)
		h.prev = valueAtHandle(inserted, i-1, prev)
		h.next = valueAtHandle(inserted, i+1, next)
	}
	for _, h := range removedHandles {
		s.unlinkKind(h)
		s.detach(h)
	}
	if s.kinds && len(inserted) != 0 {
		s.relinkKinds(inserted[0], inserted[len(inserted)-1])
	}
	s.length += len(inserted) - deleteCount
	s.adjustScale()
	if len(inserted) != 0 && s.length != 0 {
		s.labelRange(inserted[0], inserted[len(inserted)-1], len(inserted))
	}
}

// MoveRange moves [lo, hi) before the position before in the original sequence.
func (s *Seq[T]) MoveRange(lo, hi, before int) bool {
	if lo < 0 || hi < lo || hi > s.length || before < 0 || before > s.length {
		return false
	}
	if lo == hi || before >= lo && before <= hi {
		return true
	}
	count := hi - lo
	s.touchHeader()
	left, tail := s.split(s.root, lo)
	run, right := s.split(tail, count)
	runFirst, runLast := s.minimum(run), s.maximum(run)
	if s.kinds {
		for h := runFirst; ; h = h.next {
			s.unlinkKind(h)
			if h == runLast {
				break
			}
		}
	}
	oldPrev, oldNext := runFirst.prev, runLast.next
	remainder := s.join(left, right)
	s.setParent(remainder, nil)
	destination := before
	if before > hi {
		destination -= count
	}
	a, b := s.split(remainder, destination)
	prev, next := s.maximum(a), s.minimum(b)
	s.root = s.join(s.join(a, run), b)
	s.setParent(s.root, nil)
	if oldPrev != nil {
		s.touch(oldPrev)
		oldPrev.next = oldNext
	} else {
		s.first = oldNext
	}
	if oldNext != nil {
		s.touch(oldNext)
		oldNext.prev = oldPrev
	} else {
		s.last = oldPrev
	}
	s.touch(runFirst)
	s.touch(runLast)
	runFirst.prev, runLast.next = prev, next
	if prev == nil {
		s.first = runFirst
	} else {
		s.touch(prev)
		prev.next = runFirst
	}
	if next == nil {
		s.last = runLast
	} else {
		s.touch(next)
		next.prev = runLast
	}
	if s.kinds {
		s.relinkKinds(runFirst, runLast)
	}
	s.labelRange(runFirst, runLast, count)
	return true
}

func valueAtHandle[T any](handles []*Handle[T], at int, fallback *Handle[T]) *Handle[T] {
	if at < 0 || at >= len(handles) {
		return fallback
	}
	return handles[at]
}

func nodeSize[T any](n *Handle[T]) int {
	if n == nil {
		return 0
	}
	return n.size
}

func nodeHeavy[T any](n *Handle[T]) int {
	if n == nil {
		return 0
	}
	return n.hsize
}

func selfHeavy[T any](n *Handle[T]) int {
	if n.heavy {
		return 1
	}
	return 0
}

func nodeHeight[T any](n *Handle[T]) int {
	if n == nil {
		return 0
	}
	return n.height
}

func (s *Seq[T]) setParent(n, parent *Handle[T]) {
	if n != nil && n.parent != parent {
		s.touch(n)
		n.parent = parent
	}
}

func (s *Seq[T]) fixNode(n *Handle[T]) {
	s.touch(n)
	n.size = 1 + nodeSize(n.left) + nodeSize(n.right)
	n.hsize = selfHeavy(n) + nodeHeavy(n.left) + nodeHeavy(n.right)
	n.height = 1 + max(nodeHeight(n.left), nodeHeight(n.right))
	s.setParent(n.left, n)
	s.setParent(n.right, n)
}

func (s *Seq[T]) rotateLeft(n *Handle[T]) *Handle[T] {
	s.charge()
	r := n.right
	s.touch(n)
	s.touch(r)
	n.right = r.left
	r.left = n
	r.parent = n.parent
	s.fixNode(n)
	s.fixNode(r)
	return r
}

func (s *Seq[T]) rotateRight(n *Handle[T]) *Handle[T] {
	s.charge()
	l := n.left
	s.touch(n)
	s.touch(l)
	n.left = l.right
	l.right = n
	l.parent = n.parent
	s.fixNode(n)
	s.fixNode(l)
	return l
}

func (s *Seq[T]) balance(n *Handle[T]) *Handle[T] {
	s.fixNode(n)
	if nodeHeight(n.left)-nodeHeight(n.right) > 1 {
		if nodeHeight(n.left.left) < nodeHeight(n.left.right) {
			s.touch(n)
			n.left = s.rotateLeft(n.left)
		}
		return s.rotateRight(n)
	}
	if nodeHeight(n.right)-nodeHeight(n.left) > 1 {
		if nodeHeight(n.right.right) < nodeHeight(n.right.left) {
			s.touch(n)
			n.right = s.rotateRight(n.right)
		}
		return s.rotateLeft(n)
	}
	return n
}

func (s *Seq[T]) insertAt(n *Handle[T], rank int, h *Handle[T]) *Handle[T] {
	if n == nil {
		return h
	}
	s.charge()
	s.touch(n)
	if rank <= nodeSize(n.left) {
		n.left = s.insertAt(n.left, rank, h)
	} else {
		n.right = s.insertAt(n.right, rank-nodeSize(n.left)-1, h)
	}
	return s.balance(n)
}

func (s *Seq[T]) removeAt(n *Handle[T], rank int) *Handle[T] {
	s.charge()
	s.touch(n)
	left := nodeSize(n.left)
	switch {
	case rank < left:
		n.left = s.removeAt(n.left, rank)
	case rank > left:
		n.right = s.removeAt(n.right, rank-left-1)
	default:
		return s.join(n.left, n.right)
	}
	return s.balance(n)
}

func (s *Seq[T]) popMin(n *Handle[T]) (*Handle[T], *Handle[T]) {
	s.charge()
	s.touch(n)
	if n.left == nil {
		return n.right, n
	}
	var smallest *Handle[T]
	n.left, smallest = s.popMin(n.left)
	return s.balance(n), smallest
}

func (s *Seq[T]) join(left, right *Handle[T]) *Handle[T] {
	s.charge()
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	if nodeHeight(left) > nodeHeight(right)+1 {
		s.touch(left)
		left.right = s.join(left.right, right)
		return s.balance(left)
	}
	if nodeHeight(right) > nodeHeight(left)+1 {
		s.touch(right)
		right.left = s.join(left, right.left)
		return s.balance(right)
	}
	var root *Handle[T]
	right, root = s.popMin(right)
	s.touch(root)
	root.left, root.right = left, right
	return s.balance(root)
}

func (s *Seq[T]) joinRoot(left, root, right *Handle[T]) *Handle[T] {
	s.charge()
	if nodeHeight(left) > nodeHeight(right)+1 {
		s.touch(left)
		left.right = s.joinRoot(left.right, root, right)
		return s.balance(left)
	}
	if nodeHeight(right) > nodeHeight(left)+1 {
		s.touch(right)
		right.left = s.joinRoot(left, root, right.left)
		return s.balance(right)
	}
	s.touch(root)
	root.left, root.right = left, right
	s.fixNode(root)
	return root
}

func (s *Seq[T]) split(root *Handle[T], rank int) (*Handle[T], *Handle[T]) {
	s.charge()
	if root == nil {
		return nil, nil
	}
	leftSize := nodeSize(root.left)
	if rank <= leftSize {
		left, middle := s.split(root.left, rank)
		right := root.right
		s.touch(root)
		root.left, root.right, root.parent = nil, nil, nil
		root.height, root.size, root.hsize = 1, 1, selfHeavy(root)
		result := s.joinRoot(middle, root, right)
		s.setParent(left, nil)
		s.setParent(result, nil)
		return left, result
	}
	middle, right := s.split(root.right, rank-leftSize-1)
	left := root.left
	s.touch(root)
	root.left, root.right, root.parent = nil, nil, nil
	root.height, root.size, root.hsize = 1, 1, selfHeavy(root)
	result := s.joinRoot(left, root, middle)
	s.setParent(result, nil)
	s.setParent(right, nil)
	return result, right
}

func (s *Seq[T]) build(handles []*Handle[T], lo, hi int, parent *Handle[T]) *Handle[T] {
	if lo >= hi {
		return nil
	}
	mid := lo + (hi-lo)/2
	h := handles[mid]
	s.charge()
	s.touch(h)
	h.parent = parent
	h.left = s.build(handles, lo, mid, h)
	h.right = s.build(handles, mid+1, hi, h)
	h.size = hi - lo
	h.hsize = selfHeavy(h) + nodeHeavy(h.left) + nodeHeavy(h.right)
	h.height = 1 + max(nodeHeight(h.left), nodeHeight(h.right))
	return h
}

func (s *Seq[T]) collect(root *Handle[T], out *[]*Handle[T]) {
	if root == nil {
		return
	}
	s.collect(root.left, out)
	s.charge()
	*out = append(*out, root)
	s.collect(root.right, out)
}

func (s *Seq[T]) minimum(root *Handle[T]) *Handle[T] {
	for root != nil {
		s.charge()
		if root.left == nil {
			return root
		}
		root = root.left
	}
	return nil
}

func (s *Seq[T]) maximum(root *Handle[T]) *Handle[T] {
	for root != nil {
		s.charge()
		if root.right == nil {
			return root
		}
		root = root.right
	}
	return nil
}

func (s *Seq[T]) unlink(h *Handle[T]) {
	if h.prev == nil {
		s.first = h.next
	} else {
		s.touch(h.prev)
		h.prev.next = h.next
	}
	if h.next == nil {
		s.last = h.prev
	} else {
		s.touch(h.next)
		h.next.prev = h.prev
	}
}

func (s *Seq[T]) detach(h *Handle[T]) {
	s.touch(h)
	h.left, h.right, h.parent, h.prev, h.next, h.owner = nil, nil, nil, nil, nil, nil
	h.height, h.size, h.hsize, h.tag = 0, 0, 0, 0
	h.kprev, h.knext = nil, nil
}

func (s *Seq[T]) adjustScale() {
	switch {
	case s.length == 0:
		s.scale = 0
	case s.scale == 0 || s.length > 2*s.scale || s.length < s.scale/2:
		s.scale = s.length
		s.rebuildTags()
	}
}

// The tag-range relabeling rule follows Bender et al., section 3.
// rebuildTags spreads the labels over the first three quarters of the universe: the last quarter
// takes up to 2*scale appends at endStride before the next relabel, instead of halving one
// step's gap at every append (#240 step 4: the relabel is now charged work).
func (s *Seq[T]) rebuildTags() {
	if s.unlabeled {
		return
	}
	step := tagUniverse / 4 * 3 / uint64(s.length+1) //nolint:gosec
	i := uint64(1)
	for h := s.first; h != nil; h = h.next {
		s.charge()
		s.touch(h)
		h.tag = i * step
		i++
	}
}

// endStride is the label distance of an append.
func (s *Seq[T]) endStride() uint64 { return tagUniverse / 4 / uint64(2*s.scale+2) } //nolint:gosec // a scale is nonnegative

func (s *Seq[T]) labelRange(first, last *Handle[T], count int) {
	if s.unlabeled {
		return
	}
	lo, hi := uint64(0), tagUniverse
	if first.prev != nil {
		lo = first.prev.tag
	}
	if last.next != nil {
		hi = last.next.tag
	}
	if hi > lo && hi-lo > uint64(count) { //nolint:gosec
		step := (hi - lo) / uint64(count+1) //nolint:gosec
		if last.next == nil && first.prev != nil {
			// an append keeps the rest of the end gap for the next ones
			step = max(min(step, s.endStride()), 1)
		}
		i := uint64(1)
		for h := first; ; h = h.next {
			s.charge()
			s.touch(h)
			h.tag = lo + i*step
			i++
			if h == last {
				return
			}
		}
	}

	// The moved or inserted run may carry labels from another part of the
	// sequence, so expand from its physical neighbors instead of inspecting
	// labels inside the run. This is the range form of label's density search.
	anchor := first.prev
	if anchor == nil {
		anchor = last.next
	}
	t := math.Pow(float64(tagUniverse)/float64(2*s.scale), 1.0/tagBits)
	scanned := 0
	for widthBits := uint(1); widthBits <= tagBits; widthBits++ {
		width := uint64(1) << widthBits
		base := anchor.tag & ^(width - 1)
		start := first
		back, fwd := 0, 0
		for start.prev != nil && start.prev.tag >= base {
			back++
			start = start.prev
		}
		end := last
		for end.next != nil && end.next.tag < base+width {
			fwd++
			end = end.next
		}
		if scanned += 2*(back+fwd) + count; scanned > scanCap(s.length) {
			break // a search that costs more than relabelling everything does that instead
		}
		for range back + fwd {
			s.charge()
		}
		items := 0
		for h := start; ; h = h.next {
			s.charge()
			items++
			if h == end {
				break
			}
		}
		if float64(items) > float64(width)/math.Pow(t, float64(widthBits)) && widthBits != tagBits {
			continue
		}
		step := width / uint64(items+1)
		if step == 0 {
			continue
		}
		index := uint64(1)
		for h := start; ; h = h.next {
			s.charge()
			s.touch(h)
			h.tag = base + index*step
			index++
			if h == end {
				return
			}
		}
	}
	s.rebuildTags()
}

// scanCap bounds labelRange's window search: past it, rebuildTags (length more) is cheaper.
func scanCap(length int) int { return 32*length + 64 }

func (s *Seq[T]) label(h *Handle[T]) {
	s.labelRange(h, h, 1)
}

// CheckInvariants validates balance, sizes, parents, threads, labels,
// ownership, and identical tree in-order and threaded traversal orders.
func (s *Seq[T]) CheckInvariants() error {
	if s.length == 0 {
		if s.root != nil || s.first != nil || s.last != nil || s.scale != 0 {
			return fmt.Errorf("empty sequence retains structural state")
		}
		return nil
	}
	if s.scale <= 0 {
		return fmt.Errorf("nonempty sequence has invalid label scale")
	}
	if s.root == nil || s.first == nil || s.last == nil {
		return fmt.Errorf("nonempty sequence has a nil root or endpoint")
	}
	if s.root.parent != nil {
		return fmt.Errorf("root has a parent")
	}
	tree := make(map[*Handle[T]]struct{}, s.length)
	threadNext := s.first
	var walk func(*Handle[T], *Handle[T]) (int, int, error)
	walk = func(n, parent *Handle[T]) (int, int, error) {
		if n == nil {
			return 0, 0, nil
		}
		if _, duplicate := tree[n]; duplicate {
			return 0, 0, fmt.Errorf("tree contains a duplicate handle")
		}
		tree[n] = struct{}{}
		if n.owner != s || n.parent != parent {
			return 0, 0, fmt.Errorf("tree handle has invalid owner or parent")
		}
		ls, lh, err := walk(n.left, n)
		if err != nil {
			return 0, 0, err
		}
		if threadNext != n {
			return 0, 0, fmt.Errorf("tree/thread order differs")
		}
		threadNext = threadNext.next
		rs, rh, err := walk(n.right, n)
		if err != nil {
			return 0, 0, err
		}
		if n.size != ls+rs+1 {
			return 0, 0, fmt.Errorf("subtree size is %d, want %d", n.size, ls+rs+1)
		}
		if want := selfHeavy(n) + nodeHeavy(n.left) + nodeHeavy(n.right); n.hsize != want {
			return 0, 0, fmt.Errorf("subtree heavy count is %d, want %d", n.hsize, want)
		}
		height := 1 + max(lh, rh)
		if n.height != height || absInt(lh-rh) > 1 {
			return 0, 0, fmt.Errorf("AVL height or balance is invalid")
		}
		return n.size, height, nil
	}
	size, _, err := walk(s.root, nil)
	if err != nil {
		return err
	}
	if size != s.length || len(tree) != s.length {
		return fmt.Errorf("tree has %d handles, length is %d", size, s.length)
	}
	thread := make(map[*Handle[T]]struct{}, s.length)
	var previous *Handle[T]
	count := 0
	for h := s.first; h != nil; h = h.next {
		if _, duplicate := thread[h]; duplicate {
			return fmt.Errorf("thread contains a cycle or duplicate")
		}
		thread[h] = struct{}{}
		if _, ok := tree[h]; !ok {
			return fmt.Errorf("thread handle is absent from tree")
		}
		if h.owner != s || h.prev != previous {
			return fmt.Errorf("thread handle has invalid owner or predecessor")
		}
		if !s.unlabeled && previous != nil && previous.tag >= h.tag {
			return fmt.Errorf("order labels are not strictly increasing")
		}
		previous = h
		count++
	}
	if previous != s.last || count != s.length || s.first.prev != nil || s.last.next != nil {
		return fmt.Errorf("thread endpoints or length are invalid")
	}
	if len(thread) != len(tree) {
		return fmt.Errorf("tree/thread handle sets differ")
	}
	return s.checkKindThreads()
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func saturatingAdd(a, b uint64) uint64 {
	if ^uint64(0)-a < b {
		return ^uint64(0)
	}
	return a + b
}
func saturatingMul(a, b uint64) uint64 {
	if a != 0 && b > ^uint64(0)/a {
		return ^uint64(0)
	}
	return a * b
}

func (s *Seq[T]) structuralWorkBound(items int) uint64 {
	if items < 0 {
		return ^uint64(0)
	}
	total := saturatingAdd(uint64(s.length), uint64(items)) //nolint:gosec // lengths are nonnegative
	total = saturatingAdd(total, 1)
	depth := uint64(bits.Len64(total) + 1) //nolint:gosec // bits.Len64 is at most 64
	factor := saturatingMul(64, saturatingAdd(uint64(len(s.checkpoints)), 1))
	structural := saturatingMul(factor, saturatingAdd(total, depth))
	// A mutation calls labelRange at most once, on at most total handles.
	// At each of tagBits widths, expanding both ends visits at most N-count
	// handles and counting the enclosed run visits at most N: at most 2*N
	// charges per width, even when consecutive widths scan the same handles.
	// Allow N more for the final relabel (or fallback rebuild), and N for a
	// scale-change rebuild before labelRange. Journal before-images are already
	// covered by structural: each handle is saved at most once per checkpoint.
	relabel := saturatingMul(2*tagBits+2, total)
	return saturatingAdd(structural, relabel)
}

// LowerBoundWorkBound conservatively bounds one LowerBound or UpperBound.
func (s *Seq[T]) LowerBoundWorkBound() uint64 {
	length := saturatingAdd(uint64(s.length), 1) //nolint:gosec // length is nonnegative
	return 2*uint64(bits.Len64(length)) + 2      //nolint:gosec // bits.Len64 is in [0, 64]
}

// UpperBoundWorkBound conservatively bounds one UpperBound.
func (s *Seq[T]) UpperBoundWorkBound() uint64 { return s.LowerBoundWorkBound() }

// InsertWorkBound conservatively bounds one point insertion, including a possible label rebuild.
func (s *Seq[T]) InsertWorkBound() uint64 { return s.structuralWorkBound(1) }

// RemoveWorkBound conservatively bounds one point removal.
func (s *Seq[T]) RemoveWorkBound() uint64 { return s.structuralWorkBound(0) }

// MoveWorkBound conservatively bounds moving count existing handles.
func (s *Seq[T]) MoveWorkBound(count int) uint64 { return s.structuralWorkBound(count) }

// SpliceWorkBound conservatively bounds a splice before it mutates the tree.
func (s *Seq[T]) SpliceWorkBound(deleteCount, insertCount int) uint64 {
	if deleteCount < 0 || insertCount < 0 || deleteCount > s.length {
		return ^uint64(0)
	}
	return s.structuralWorkBound(insertCount + deleteCount)
}

// ReplaceRangeWorkBound conservatively bounds replacing delete handles with insert values.
func (s *Seq[T]) ReplaceRangeWorkBound(deleteCount, insertCount int) uint64 {
	return s.SpliceWorkBound(deleteCount, insertCount)
}

// ReplaceHandlesWorkBound conservatively bounds republishing count handles.
func (s *Seq[T]) ReplaceHandlesWorkBound(count int) uint64 { return s.structuralWorkBound(count) }

// CheckpointWorkBound bounds creation of a checkpoint and its header before-image.
func (s *Seq[T]) CheckpointWorkBound() uint64 { return 1 }

// CommitWorkBound bounds committing the most recent checkpoint.
func (s *Seq[T]) CommitWorkBound() uint64 { return 0 }

// RollbackWorkBound bounds restoring checkpoint. An invalid or non-top token
// performs no work and has a zero bound.
func (s *Seq[T]) RollbackWorkBound(checkpoint *Checkpoint[T]) uint64 {
	top, ok := s.topCheckpoint(checkpoint)
	if !ok {
		return 0
	}
	return saturatingAdd(uint64(len(top.before)), uint64(len(top.created)))
}
