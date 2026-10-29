// SPDX-License-Identifier: BSD-3-Clause

package opidx

import "fmt"

// Kind reads (design 15): a sequence whose handles have two kinds (SetWeigh) is also read as two
// subsequences, the heavy handles in order and the light ones in order.

func kindCount[T any](n *Handle[T], heavy bool) int {
	if heavy {
		return nodeHeavy(n)
	}
	return nodeSize(n) - nodeHeavy(n)
}

// Heavy reports h's kind.
func (h *Handle[T]) Heavy() bool { return h.heavy }

// HeavyLen is the number of heavy handles.
func (s *Seq[T]) HeavyLen() int { return nodeHeavy(s.root) }

// PeekKind is the handle at rank among the handles of one kind (nil out of range), with the tree
// levels it descended.
func (s *Seq[T]) PeekKind(rank int, heavy bool) (*Handle[T], int) {
	if rank < 0 || rank >= kindCount(s.root, heavy) {
		return nil, 0
	}
	levels := 0
	for n := s.root; n != nil; {
		levels++
		left := kindCount(n.left, heavy)
		switch {
		case rank < left:
			n = n.left
		case n.heavy == heavy && rank == left:
			return n, levels
		default:
			rank -= left
			if n.heavy == heavy {
				rank--
			}
			n = n.right
		}
	}
	return nil, levels
}

// PosKind is h's rank among the handles of its kind.
func (s *Seq[T]) PosKind(h *Handle[T]) (int, bool) {
	if h == nil || h.owner != s {
		return 0, false
	}
	rank := kindCount(h.left, h.heavy)
	for n := h; n.parent != nil; n = n.parent {
		if p := n.parent; n == p.right {
			rank += kindCount(p.left, h.heavy)
			if p.heavy == h.heavy {
				rank++
			}
		}
	}
	return rank, true
}

// RankKind is PosKind charged as Rank is: a unit per level climbed.
func (s *Seq[T]) RankKind(h *Handle[T]) (int, bool) {
	if h == nil || h.owner != s {
		return 0, false
	}
	s.charge()
	rank := kindCount(h.left, h.heavy)
	for n := h; n.parent != nil; n = n.parent {
		s.charge()
		if p := n.parent; n == p.right {
			rank += kindCount(p.left, h.heavy)
			if p.heavy == h.heavy {
				rank++
			}
		}
	}
	return rank, true
}

// firstKind is the first handle of one kind in root's subtree, nil if none.
func firstKind[T any](root *Handle[T], heavy bool) *Handle[T] {
	for n := root; n != nil && kindCount(n, heavy) > 0; {
		switch {
		case kindCount(n.left, heavy) > 0:
			n = n.left
		case n.heavy == heavy:
			return n
		default:
			n = n.right
		}
	}
	return nil
}

// lastKind is the last handle of one kind in root's subtree, nil if none.
func lastKind[T any](root *Handle[T], heavy bool) *Handle[T] {
	for n := root; n != nil && kindCount(n, heavy) > 0; {
		switch {
		case kindCount(n.right, heavy) > 0:
			n = n.right
		case n.heavy == heavy:
			return n
		default:
			n = n.left
		}
	}
	return nil
}

// FirstKind is the first handle of one kind, nil if none.
func (s *Seq[T]) FirstKind(heavy bool) *Handle[T] { return firstKind(s.root, heavy) }

// LastKind is the last handle of one kind, nil if none.
func (s *Seq[T]) LastKind(heavy bool) *Handle[T] { return lastKind(s.root, heavy) }

// NextKind is the next handle of h's kind, nil if none: its kind thread once kind threads are
// on (O(1)), else the physical successor when it has that kind, else one climb and one descent,
// O(log n).
func (s *Seq[T]) NextKind(h *Handle[T]) *Handle[T] {
	if h == nil || h.owner != s {
		return nil
	}
	if s.kinds {
		return h.knext
	}
	return s.climbNext(h, h.heavy, true)
}

// PrevKind is the previous handle of h's kind, nil if none (NextKind's mirror).
func (s *Seq[T]) PrevKind(h *Handle[T]) *Handle[T] {
	if h == nil || h.owner != s {
		return nil
	}
	if s.kinds {
		return h.kprev
	}
	return s.climbPrev(h, h.heavy, true)
}

// PeekNextOfKind is the first handle of one kind after h (nil if none), uncharged, O(log n).
func (s *Seq[T]) PeekNextOfKind(h *Handle[T], heavy bool) *Handle[T] {
	if h == nil || h.owner != s {
		return nil
	}
	return s.climbNext(h, heavy, false)
}

// climbNext is the first handle of one kind after h: its successor when that has the kind, else
// the rest of h's right subtree and then the right subtrees of the ancestors h is left of,
// O(log n), a unit each step.
func (s *Seq[T]) climbNext(h *Handle[T], heavy, charge bool) *Handle[T] {
	if h.next == nil || h.next.heavy == heavy {
		return h.next
	}
	if charge {
		s.charge()
	}
	if n := firstKind(h.right, heavy); n != nil {
		return n
	}
	for n := h; n.parent != nil; n = n.parent {
		if charge {
			s.charge()
		}
		if p := n.parent; n == p.left {
			if p.heavy == heavy {
				return p
			}
			if r := firstKind(p.right, heavy); r != nil {
				return r
			}
		}
	}
	return nil
}

// climbPrev is climbNext's mirror: the last handle of one kind before h.
func (s *Seq[T]) climbPrev(h *Handle[T], heavy, charge bool) *Handle[T] {
	if h.prev == nil || h.prev.heavy == heavy {
		return h.prev
	}
	if charge {
		s.charge()
	}
	if n := lastKind(h.left, heavy); n != nil {
		return n
	}
	for n := h; n.parent != nil; n = n.parent {
		if charge {
			s.charge()
		}
		if p := n.parent; n == p.right {
			if p.heavy == heavy {
				return p
			}
			if l := lastKind(p.left, heavy); l != nil {
				return l
			}
		}
	}
	return nil
}

// Kind threads (design 15): once on, every handle links the previous and next handle of its kind,
// so a walk over one kind is O(1) a step however the kinds interleave. Turning them on is one
// pass; afterwards an edit relinks the handles it moves, O(log n) per moved range.

// KindThreads reports whether the kind threads are maintained.
func (s *Seq[T]) KindThreads() bool { return s.kinds }

// SetKindThreads turns the kind threads on (they stay on): one pass over the sequence, a unit
// and an image per handle.
func (s *Seq[T]) SetKindThreads() {
	if s.kinds {
		return
	}
	s.touchHeader()
	s.kinds = true
	s.threadKinds()
}

// KindThreadsBound bounds SetKindThreads.
func (s *Seq[T]) KindThreadsBound(journaled bool) uint64 {
	if s.kinds {
		return 0
	}
	n := uint64(s.length) //nolint:gosec // a length is nonnegative
	return saturatingAdd(n, images(n+1, journaled))
}

// threadKinds links every handle to its kind neighbours, in one pass.
func (s *Seq[T]) threadKinds() {
	var last [2]*Handle[T]
	for h := s.first; h != nil; h = h.next {
		s.charge()
		k := 0
		if h.heavy {
			k = 1
		}
		if h.kprev != last[k] || h.knext != nil {
			s.touch(h)
			h.kprev, h.knext = last[k], nil
		}
		if p := last[k]; p != nil && p.knext != h {
			s.touch(p)
			p.knext = h
		}
		last[k] = h
	}
}

// unlinkKind takes h out of its kind thread.
func (s *Seq[T]) unlinkKind(h *Handle[T]) {
	if !s.kinds {
		return
	}
	if p := h.kprev; p != nil {
		s.touch(p)
		p.knext = h.knext
	}
	if n := h.knext; n != nil {
		s.touch(n)
		n.kprev = h.kprev
	}
	if h.kprev != nil || h.knext != nil {
		s.touch(h)
		h.kprev, h.knext = nil, nil
	}
}

// relinkKinds links the physical range first..last, whose handles are in no kind thread, into
// the threads: per kind, the range's handles in order between the kind's last handle before the
// range (one climb) and its successor in the thread. O(range + log n).
func (s *Seq[T]) relinkKinds(first, last *Handle[T]) {
	for _, heavy := range [2]bool{false, true} {
		var head, tail *Handle[T]
		for h := first; ; h = h.next {
			s.charge()
			if h.heavy == heavy {
				if tail != nil {
					s.touch(tail)
					tail.knext = h
				}
				s.touch(h)
				h.kprev, h.knext = tail, nil
				if head == nil {
					head = h
				}
				tail = h
			}
			if h == last {
				break
			}
		}
		if head == nil {
			continue
		}
		var before, after *Handle[T]
		if first.prev != nil {
			before = s.climbPrev(first, heavy, true)
		}
		if before != nil {
			after = before.knext
		} else if last.next != nil {
			after = s.climbNext(last, heavy, true)
		}
		if before != nil {
			s.touch(before)
			before.knext = head
			head.kprev = before
		}
		if after != nil {
			s.touch(after)
			after.kprev = tail
			tail.knext = after
		}
	}
}

// kindLinkBound bounds relinkKinds over k handles and the unlinking of u: two climbs per kind
// and the range walked twice, every write an image.
func (s *Seq[T]) kindLinkBound(k, u int, journaled bool) uint64 {
	if !s.kinds {
		return 0
	}
	kk, uu := uint64(k), uint64(u)                   //nolint:gosec // counts are nonnegative
	climbs := 4 * (2*uint64(nodeHeight(s.root)) + 4) //nolint:gosec // a height is small and nonnegative
	units := saturatingAdd(climbs, 2*kk)
	return saturatingAdd(units, images(saturatingAdd(2*kk+4, 3*uu), journaled))
}

// checkKindThreads verifies the kind threads against the physical order (no links while off).
func (s *Seq[T]) checkKindThreads() error {
	var last [2]*Handle[T]
	for h := s.first; h != nil; h = h.next {
		if !s.kinds {
			if h.kprev != nil || h.knext != nil {
				return fmt.Errorf("kind links without kind threads")
			}
			continue
		}
		k := 0
		if h.heavy {
			k = 1
		}
		if h.kprev != last[k] || last[k] != nil && last[k].knext != h {
			return fmt.Errorf("kind thread is out of order")
		}
		last[k] = h
	}
	for _, h := range last {
		if h != nil && h.knext != nil {
			return fmt.Errorf("kind thread runs past its last handle")
		}
	}
	return nil
}
