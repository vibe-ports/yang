// SPDX-License-Identifier: BSD-3-Clause

// Package opidx provides operation-local ordered indexes. Its structures own no
// data nodes and charge work through optional, caller-owned hooks.
package opidx

import "math"

const tagUniverse uint64 = 1 << 63

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
	tag         uint64
}

// Seq maintains physical order, positions, and constant-time order labels.
// Insertions have amortized O(log n) structural and relabel work; remove,
// rank, and select take O(log n). First, Last, Next, Prev, and Compare are O(1).
type Seq[T any] struct {
	root, first, last *Handle[T]
	length            int
	scale             int
	work              func(uint64)
}

// SetWorkHook sets a synchronous callback for node visits, rotations, and
// relabels. It must not mutate this sequence. Nil disables counting.
func (s *Seq[T]) SetWorkHook(hook func(uint64)) { s.work = hook }

func (s *Seq[T]) charge() {
	if s.work != nil {
		s.work(1)
	}
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
	switch {
	case a.tag < b.tag:
		return -1, true
	case a.tag > b.tag:
		return 1, true
	default:
		return 0, true
	}
}

// Rank returns a zero-based position. The bool is false for a foreign or
// removed handle.
func (s *Seq[T]) Rank(h *Handle[T]) (int, bool) {
	if h == nil || h.owner != s {
		return 0, false
	}
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
	for n := s.root; n != nil; {
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

// InsertBefore inserts value before anchor. A nil anchor appends. A foreign or
// removed anchor is rejected without changing the sequence.
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
	h := &Handle[T]{Value: value, owner: s, height: 1, size: 1, prev: prev, next: next}
	if prev == nil {
		s.first = h
	} else {
		prev.next = h
	}
	if next == nil {
		s.last = h
	} else {
		next.prev = h
	}
	s.root = s.insertAt(s.root, rank, h)
	s.root.parent = nil
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
	s.root = s.removeAt(s.root, rank)
	if s.root != nil {
		s.root.parent = nil
	}
	if h.prev == nil {
		s.first = h.next
	} else {
		h.prev.next = h.next
	}
	if h.next == nil {
		s.last = h.prev
	} else {
		h.next.prev = h.prev
	}
	h.left, h.right, h.parent, h.prev, h.next, h.owner = nil, nil, nil, nil, nil, nil
	s.length--
	if s.length == 0 {
		s.scale = 0
	} else if s.length < s.scale/2 {
		s.scale = s.length
		s.rebuildTags()
	}
	return true
}

// MoveBefore relocates a live handle before anchor while preserving its
// identity. A nil anchor moves it to the end. Relative order of all other
// handles is unchanged. Callers must unregister h from occurrence indexes
// before moving it and register it again afterwards.
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
	if from == to || from+1 == to {
		return true
	}
	s.root = s.removeAt(s.root, from)
	if s.root != nil {
		s.root.parent = nil
	}
	if h.prev == nil {
		s.first = h.next
	} else {
		h.prev.next = h.next
	}
	if h.next == nil {
		s.last = h.prev
	} else {
		h.next.prev = h.prev
	}
	h.left, h.right, h.parent, h.prev, h.next = nil, nil, nil, nil, nil
	h.height, h.size = 1, 1
	s.length--
	if from < to {
		to--
	}
	var prev, next *Handle[T]
	if to == s.length {
		prev = s.last
	} else {
		next = s.Select(to)
		prev = next.prev
	}
	h.prev, h.next = prev, next
	if prev == nil {
		s.first = h
	} else {
		prev.next = h
	}
	if next == nil {
		s.last = h
	} else {
		next.prev = h
	}
	s.root = s.insertAt(s.root, to, h)
	s.root.parent = nil
	s.length++
	s.label(h)
	return true
}

func nodeSize[T any](n *Handle[T]) int {
	if n == nil {
		return 0
	}
	return n.size
}

func nodeHeight[T any](n *Handle[T]) int {
	if n == nil {
		return 0
	}
	return n.height
}

func fixNode[T any](n *Handle[T]) {
	n.size = 1 + nodeSize(n.left) + nodeSize(n.right)
	n.height = 1 + max(nodeHeight(n.left), nodeHeight(n.right))
	if n.left != nil {
		n.left.parent = n
	}
	if n.right != nil {
		n.right.parent = n
	}
}

func (s *Seq[T]) rotateLeft(n *Handle[T]) *Handle[T] {
	s.charge()
	r := n.right
	n.right = r.left
	r.left = n
	r.parent = n.parent
	fixNode(n)
	fixNode(r)
	return r
}

func (s *Seq[T]) rotateRight(n *Handle[T]) *Handle[T] {
	s.charge()
	l := n.left
	n.left = l.right
	l.right = n
	l.parent = n.parent
	fixNode(n)
	fixNode(l)
	return l
}

func (s *Seq[T]) balance(n *Handle[T]) *Handle[T] {
	fixNode(n)
	if nodeHeight(n.left)-nodeHeight(n.right) > 1 {
		if nodeHeight(n.left.left) < nodeHeight(n.left.right) {
			n.left = s.rotateLeft(n.left)
		}
		return s.rotateRight(n)
	}
	if nodeHeight(n.right)-nodeHeight(n.left) > 1 {
		if nodeHeight(n.right.right) < nodeHeight(n.right.left) {
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
	if rank <= nodeSize(n.left) {
		n.left = s.insertAt(n.left, rank, h)
	} else {
		n.right = s.insertAt(n.right, rank-nodeSize(n.left)-1, h)
	}
	return s.balance(n)
}

func (s *Seq[T]) removeAt(n *Handle[T], rank int) *Handle[T] {
	s.charge()
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
	if n.left == nil {
		return n.right, n
	}
	var smallest *Handle[T]
	n.left, smallest = s.popMin(n.left)
	return s.balance(n), smallest
}

func (s *Seq[T]) join(left, right *Handle[T]) *Handle[T] {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	s.charge()
	if nodeHeight(left) > nodeHeight(right)+1 {
		left.right = s.join(left.right, right)
		return s.balance(left)
	}
	if nodeHeight(right) > nodeHeight(left)+1 {
		right.left = s.join(left, right.left)
		return s.balance(right)
	}
	var root *Handle[T]
	right, root = s.popMin(right)
	root.left, root.right = left, right
	return s.balance(root)
}

// The tag-range relabeling rule follows Bender et al., section 3. The root
// range has 2^63 slots. Rebuilding when size crosses a factor of two updates
// the density threshold and costs O(1) amortized per intervening mutation.
func (s *Seq[T]) rebuildTags() {
	step := tagUniverse / uint64(s.length+1) //nolint:gosec // a sequence length is nonnegative and bounded by addressable memory
	i := uint64(1)
	for h := s.first; h != nil; h = h.next {
		s.charge()
		h.tag = i * step
		i++
	}
}

func (s *Seq[T]) label(h *Handle[T]) {
	lo, hi := uint64(0), tagUniverse
	if h.prev != nil {
		lo = h.prev.tag
	}
	if h.next != nil {
		hi = h.next.tag
	}
	if hi-lo > 1 {
		h.tag = lo + (hi-lo)/2
		return
	}
	anchor := h.prev
	if anchor == nil {
		anchor = h.next
	}
	// Threshold at the root is at least 2*scale / tagUniverse. For any
	// size within [scale/2, 2*scale], a non-overflowing range exists.
	t := math.Pow(float64(tagUniverse)/float64(2*s.scale), 1.0/63)
	for bits := uint(1); bits <= 63; bits++ {
		width := uint64(1) << bits
		base := anchor.tag & ^(width - 1)
		// The range encloses the predecessor (or the successor for a
		// prepend). The other neighbor can be in the next range.
		start := h
		for start.prev != nil && start.prev.tag >= base {
			s.charge()
			start = start.prev
		}
		end := h
		for end.next != nil && end.next.tag < base+width {
			s.charge()
			end = end.next
		}
		count := 0
		for p := start; ; p = p.next {
			s.charge()
			count++
			if p == end {
				break
			}
		}
		if float64(count) > float64(width)/math.Pow(t, float64(bits)) && bits != 63 {
			continue
		}
		step := width / uint64(count+1)
		if step == 0 {
			continue
		}
		index := uint64(1)
		for p := start; ; p = p.next {
			s.charge()
			p.tag = base + index*step
			index++
			if p == end {
				break
			}
		}
		return
	}
	// The root always has room for an in-memory Go slice of handles. Keep a
	// deterministic recovery path if a future platform changes that premise.
	s.rebuildTags()
}
