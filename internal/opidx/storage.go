// SPDX-License-Identifier: BSD-3-Clause

package opidx

import "math/bits"

// Storage access (#240 step 5, design 14): a Seq is the authoritative sibling storage. Reads by
// position and identity are uncounted (they replace slice indexing); edits charge through the
// hooks as before.

// Pos is Rank without charging.
func (s *Seq[T]) Pos(h *Handle[T]) (int, bool) {
	if h == nil || h.owner != s {
		return 0, false
	}
	rank := nodeSize(h.left)
	for h.parent != nil {
		if h == h.parent.right {
			rank += 1 + nodeSize(h.parent.left)
		}
		h = h.parent
	}
	return rank, true
}

// Labeled reports whether s maintains its order labels.
func (s *Seq[T]) Labeled() bool { return !s.unlabeled }

// SetLabeled turns label maintenance on or off. Turning it on labels every handle in one pass.
// Like the build of the index that needs the labels, it reports no work and pays for no entry:
// it records only the header (unpaid), since before it the labels were not maintained and a
// rollback that restores the flag needs none of them.
func (s *Seq[T]) SetLabeled(on bool) {
	if on != s.unlabeled {
		return
	}
	if j := s.journal(); j != nil && j.first(&s.stamp) {
		j.add(Entry{obj: s, ptr: [6]any{s.root, s.first, s.last}, num: [3]uint64{uint64(s.length), uint64(s.scale)}, had: s.unlabeled}, 0, nil) //nolint:gosec // restored bit for bit
	}
	s.unlabeled = !on
	if !on || s.length == 0 {
		return
	}
	s.scale = s.length
	step := tagUniverse / 4 * 3 / uint64(s.length+1) //nolint:gosec // a length is nonnegative
	i := uint64(1)
	for h := s.first; h != nil; h = h.next {
		h.tag = i * step
		i++
	}
}

// BoundIn returns the first rank in [lo, hi) whose value satisfies pred, monotone over the range
// (false values, then true ones), or hi if none does. It is one descent: O(log n) structural
// visits, each reported to visit (nil: none), and pred runs only on values inside the range,
// once per visited level at most.
func (s *Seq[T]) BoundIn(lo, hi int, pred func(T) bool, visit func()) int {
	lo, hi = max(lo, 0), min(hi, s.length)
	answer, base := hi, 0
	for n := s.root; n != nil && lo < hi; {
		if visit != nil {
			visit()
		}
		rank := base + nodeSize(n.left)
		switch {
		case rank < lo:
			base, n = rank+1, n.right
		case rank >= hi:
			n = n.left
		case pred(n.Value):
			answer, n = rank, n.left
		default:
			base, n = rank+1, n.right
		}
	}
	return answer
}

// Owns reports whether h is a live handle of s.
func (s *Seq[T]) Owns(h *Handle[T]) bool { return h != nil && h.owner == s }

// Republish replaces [lo, hi) with values, in their order. handleOf returns the live handle of a
// value already in [lo, hi), which keeps its identity, or nil for a value that gets a new handle;
// every handle of the range must be returned for exactly one value. It returns the handles in
// order. An invalid request is rejected before any write.
func (s *Seq[T]) Republish(lo, hi int, values []T, handleOf func(T) *Handle[T]) ([]*Handle[T], bool) {
	if lo < 0 || hi < lo || hi > s.length || len(values) < hi-lo {
		return nil, false
	}
	allowed := make(map[*Handle[T]]bool, hi-lo)
	for h, remaining := s.Peek(lo), hi-lo; remaining > 0; h, remaining = h.next, remaining-1 {
		s.charge()
		allowed[h] = false
	}
	handles := make([]*Handle[T], len(values))
	fresh := 0
	for i, v := range values {
		s.charge()
		h := handleOf(v)
		if h == nil {
			fresh++
			continue
		}
		if seen, ok := allowed[h]; !ok || seen {
			return nil, false
		}
		allowed[h] = true
		handles[i] = h
	}
	if len(values)-fresh != hi-lo {
		return nil, false
	}
	if len(values) == 0 {
		return handles, true
	}
	for i, v := range values {
		if handles[i] == nil {
			h := &Handle[T]{Value: v, owner: s, height: 1, size: 1}
			s.trackCreated(h)
			s.created(h)
			handles[i] = h
		}
	}
	var prev, next *Handle[T]
	if lo > 0 {
		prev = s.Select(lo - 1)
	}
	if hi < s.length {
		next = s.Select(hi)
	}
	s.touchHeader()
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
	s.length += fresh
	s.adjustScale()
	s.labelRange(handles[0], handles[len(handles)-1], len(handles))
	return handles, true
}

// RepublishBound bounds Republish of [lo, hi) with fresh new values: the ownership scans, the
// splits and joins, the build, a scale change and the run's relabel, planned exactly (images
// included when journaled).
func (s *Seq[T]) RepublishBound(lo, hi, fresh int, journaled bool, pay Pay) uint64 {
	k := hi - lo + fresh
	if k == 0 {
		return 0
	}
	var prev, next *Handle[T]
	if lo > 0 {
		prev = s.Peek(lo - 1)
	}
	if hi < s.length {
		next = s.Peek(hi)
	}
	length := s.length + fresh
	kk := uint64(k) //nolint:gosec // a count is nonnegative
	bound := saturatingAdd(moveUnits(nodeHeight(s.root)+1, journaled), saturatingAdd(5*kk, images(2*kk+2, journaled)))
	bound = saturatingAdd(bound, 3*(s.SelectBound()+1))
	if s.unlabeled {
		return bound
	}
	charges, touched, ok := s.planLabel(prev, next, nil, k, max(s.scale, 1), length, pay)
	if !ok {
		return ^uint64(0)
	}
	if s.scale == 0 || length > 2*s.scale || length < s.scale/2 {
		// the rebuild, then the run's relabel inside the even gaps it leaves
		all := uint64(length) + kk //nolint:gosec // a length is nonnegative
		bound = saturatingAdd(bound, saturatingAdd(all, images(all, journaled)))
	}
	return saturatingAdd(bound, saturatingAdd(charges, images(touched, journaled)))
}

// Keep removes, in one pass, every handle whose value keep rejects (keep is called once per
// handle, in order) and returns the removed handles, detached. The survivors keep their
// identities, order and labels; the tree is rebuilt balanced from them. O(n) for n handles,
// however many go.
func (s *Seq[T]) Keep(keep func(T) bool) []*Handle[T] {
	var kept, gone []*Handle[T]
	for h := s.first; h != nil; h = h.next {
		s.charge()
		if keep(h.Value) {
			kept = append(kept, h)
		} else {
			gone = append(gone, h)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	s.touchHeader()
	for _, h := range gone {
		s.detach(h)
	}
	s.root = s.build(kept, 0, len(kept), nil)
	for i, h := range kept {
		h.prev = valueAtHandle(kept, i-1, nil)
		h.next = valueAtHandle(kept, i+1, nil)
	}
	s.first, s.last = valueAtHandle(kept, 0, nil), valueAtHandle(kept, len(kept)-1, nil)
	s.length = len(kept)
	s.adjustScale()
	return gone
}

// KeepBound bounds Keep: the scan, the detaches, the build and a shrinking scale's relabel, with
// every handle's image and the header's.
func (s *Seq[T]) KeepBound(journaled bool) uint64 {
	n := uint64(s.length) //nolint:gosec // a length is nonnegative
	bound := saturatingAdd(2*n, images(n+1, journaled))
	if !s.unlabeled {
		bound = saturatingAdd(bound, saturatingAdd(n, images(n, journaled)))
	}
	return bound
}

// RepublishBoundAny bounds Republish of the k handles of one range, none fresh, in a sequence of
// at most length handles, labeled or not: position-free, for a reservation made before the
// sequence reaches its final size (a deferred publication). The range's labels already fit
// between its neighbours, so its relabel is the k handles themselves.
func RepublishBoundAny(length, k int, journaled bool) uint64 {
	h := 2*bits.Len(uint(length+2)) + 2
	kk := uint64(k) //nolint:gosec // a count is nonnegative
	bound := saturatingAdd(moveUnits(h+1, journaled), saturatingAdd(6*kk, images(3*kk+2, journaled)))
	return saturatingAdd(bound, 3*(uint64(h)+3)) //nolint:gosec // a height is small and nonnegative
}

// Slot is a value's own reference to its handle in the sequence that holds it. Its writes are
// journaled in the hooks' journal (two units each, an image), so a rollback restores it with
// the sequence.
type Slot[T any] struct{ h *Handle[T] }

// Get returns the handle, nil when none was set.
func (p *Slot[T]) Get() *Handle[T] { return p.h }

// Set stores h, journaled in hooks' journal; without one it is the plain assignment.
func (p *Slot[T]) Set(hooks Hooks, h *Handle[T]) {
	if j := journalOf(hooks); j != nil {
		j.image(Entry{obj: p, ptr: [6]any{p.h}}, hooks)
	}
	p.h = h
}

func (p *Slot[T]) undo(e *Entry) { p.h, _ = e.ptr[0].(*Handle[T]) }
func (p *Slot[T]) purge(*Entry)  {}
