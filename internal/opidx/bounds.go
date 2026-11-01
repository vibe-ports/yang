// SPDX-License-Identifier: BSD-3-Clause

package opidx

import (
	"math"
	"math/bits"
)

// Pay charges planning work before it is done; false refuses it (the planner stops, and its bound
// saturates so the caller's prepay refuses). Nil plans unpaid. An interface, not a func: a
// pointer receiver converts without allocating.
type Pay interface{ Pay(units uint64) bool }

// PayFunc adapts a function to Pay.
type PayFunc func(units uint64) bool

// Pay calls f.
func (f PayFunc) Pay(units uint64) bool { return f(units) }

// Point bounds (#240 step 4, design 13 §7): what one mutation charges through its hooks, its
// work units plus two units per recorded image and three per map cell, for the actual AVL
// heights. They are O(height), except the relabel, which the read-only planner below counts
// exactly by walking the same handles labelRange (or rebuildTags) will; the planner charges its
// own walk through a Pay, before each visit. They are tight enough to prepay per
// edit and generous in the constants: a test build panics on any overdraft, so every sweep
// checks them.

// avlUnits is c units per level of an AVL of height h: measured worst cases are below 1.8 per
// level for a point edit's work and below 4.7 with its images (journaled), 12.2 and 18.6 for a
// move; the constants keep a margin over them.
func avlUnits(h int, journaled bool) uint64 {
	c := uint64(3)
	if journaled {
		c = 6
	}
	return c*uint64(h+2) + 4 //nolint:gosec // a height is small and nonnegative
}

func moveUnits(h int, journaled bool) uint64 {
	c := uint64(14)
	if journaled {
		c = 24
	}
	return c*uint64(h+2) + 4 //nolint:gosec // a height is small and nonnegative
}

// images is the units of n recorded images: none without a journal.
func images(n uint64, journaled bool) uint64 {
	if !journaled {
		return 0
	}
	return 2 * n
}

// SelectBound bounds one Select or Rank.
func (s *Seq[T]) SelectBound() uint64 { return uint64(nodeHeight(s.root)) + 2 } //nolint:gosec // a height is nonnegative

// InsertBound bounds InsertBefore(anchor, ·) (anchor nil: append), its relabel included;
// journaled: its images too.
func (s *Seq[T]) InsertBound(anchor *Handle[T], journaled bool, pay Pay) uint64 {
	return saturatingAdd(s.insertBound(anchor, journaled, pay), s.kindLinkBound(1, 0, journaled))
}

func (s *Seq[T]) insertBound(anchor *Handle[T], journaled bool, pay Pay) uint64 {
	if s.unlabeled {
		return avlUnits(nodeHeight(s.root)+1, journaled)
	}
	prev, next := s.last, (*Handle[T])(nil)
	if anchor != nil {
		prev, next = anchor.prev, anchor
	}
	length, scale := s.length+1, max(s.scale, 1)
	var charges, touched uint64
	if length > 2*scale {
		charges, touched = uint64(length), uint64(length) //nolint:gosec // a length is nonnegative
	} else {
		var ok bool
		if charges, touched, ok = s.planLabel(prev, next, nil, 1, scale, length, pay); !ok {
			return ^uint64(0)
		}
	}
	return saturatingAdd(avlUnits(nodeHeight(s.root)+1, journaled), saturatingAdd(charges, images(touched, journaled)))
}

// RemoveBound bounds one Remove, a scale-change rebuild included.
func (s *Seq[T]) RemoveBound(journaled bool) uint64 {
	bound := saturatingAdd(avlUnits(nodeHeight(s.root), journaled), s.kindLinkBound(0, 1, journaled))
	if rest := uint64(s.length - 1); !s.unlabeled && s.length > 1 && s.length-1 < s.scale/2 { //nolint:gosec // a length is nonnegative
		bound = saturatingAdd(bound, saturatingAdd(rest, images(rest, journaled)))
	}
	return bound
}

// MoveBound bounds MoveBefore(h, anchor), its relabel included.
func (s *Seq[T]) MoveBound(h, anchor *Handle[T], journaled bool, pay Pay) uint64 {
	if h == nil || h == anchor {
		return 0
	}
	var prev, next *Handle[T]
	if anchor == nil {
		prev = s.last
	} else {
		prev, next = anchor.prev, anchor
	}
	if prev == h {
		prev = h.prev
	}
	kinds := s.kindLinkBound(1, 1, journaled)
	if s.unlabeled {
		return saturatingAdd(moveUnits(nodeHeight(s.root), journaled), kinds)
	}
	charges, touched, ok := s.planLabel(prev, next, h, 1, max(s.scale, 1), s.length, pay)
	if !ok {
		return ^uint64(0)
	}
	return saturatingAdd(saturatingAdd(moveUnits(nodeHeight(s.root), journaled), kinds), saturatingAdd(charges, images(touched, journaled)))
}

// planLabel counts the charges and the touched handles of labelRange for count new handles
// placed between prev and next (either nil at an end), skip being absent from the order: the
// same window search, read-only.
func (s *Seq[T]) planLabel(prev, next, skip *Handle[T], count, scale, length int, pay Pay) (charges, touched uint64, ok bool) {
	step := func() bool { return pay == nil || pay.Pay(1) }
	before := func(x *Handle[T]) *Handle[T] {
		if x = x.prev; x == skip && x != nil {
			x = x.prev
		}
		return x
	}
	after := func(x *Handle[T]) *Handle[T] {
		if x = x.next; x == skip && x != nil {
			x = x.next
		}
		return x
	}
	lo, hi := uint64(0), tagUniverse
	if prev != nil {
		lo = prev.tag
	}
	if next != nil {
		hi = next.tag
	}
	if hi > lo && hi-lo > uint64(count) { //nolint:gosec // a count is nonnegative
		return uint64(count), uint64(count), true //nolint:gosec // a count is nonnegative
	}
	anchor := prev
	if anchor == nil {
		anchor = next
	}
	if anchor == nil {
		return uint64(length), uint64(length), true //nolint:gosec // a length is nonnegative
	}
	t := math.Pow(float64(tagUniverse)/float64(2*scale), 1.0/tagBits)
	scanned := 0
	for widthBits := uint(1); widthBits <= tagBits; widthBits++ {
		width := uint64(1) << widthBits
		base := anchor.tag & ^(width - 1)
		back := 0
		for x := prev; x != nil && x.tag >= base; x = before(x) {
			if !step() {
				return 0, 0, false
			}
			back++
		}
		fwd := 0
		for x := next; x != nil && x.tag < base+width; x = after(x) {
			if !step() {
				return 0, 0, false
			}
			fwd++
		}
		if scanned += 2*(back+fwd) + count; scanned > scanCap(length) {
			break
		}
		items := back + count + fwd
		charges = saturatingAdd(charges, uint64(back+fwd+items)) //nolint:gosec // counts are nonnegative
		if float64(items) > float64(width)/math.Pow(t, float64(widthBits)) && widthBits != tagBits {
			continue
		}
		if width/uint64(items+1) == 0 { //nolint:gosec // a count is nonnegative
			continue
		}
		return saturatingAdd(charges, uint64(items)), uint64(items), true //nolint:gosec // a count is nonnegative
	}
	return saturatingAdd(charges, uint64(length)), uint64(length), true //nolint:gosec // a length is nonnegative
}

func (o *Occurrences[T, S, K]) classHeight(schema S, key K) int {
	if t := o.trees[occKey[S, K]{schema, key}]; t != nil && t.root != nil {
		return t.root.height
	}
	return 0
}

// InsertBound bounds one Insert into the class (schema, key).
func (o *Occurrences[T, S, K]) InsertBound(schema S, key K, journaled bool) uint64 {
	return avlUnits(o.classHeight(schema, key)+1, journaled)
}

// RemoveBound bounds one Remove of h (a lookup when h is not registered).
func (o *Occurrences[T, S, K]) RemoveBound(h *Handle[T], journaled bool) uint64 {
	k := o.by[h]
	if k == nil {
		return 1
	}
	return avlUnits(o.classHeight(k.schema, k.key), journaled)
}

// UpdateBound bounds one Insert or Remove.
func (r *Ranks) UpdateBound(journaled bool) uint64 {
	h := 0
	if r.root != nil {
		h = r.root.height
	}
	return avlUnits(h+1, journaled)
}

// Peek is Select without charging: for computing a bound before the edit.
func (s *Seq[T]) Peek(rank int) *Handle[T] {
	if rank < 0 || rank >= s.length {
		return nil
	}
	for n := s.root; n != nil; {
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

// InsertBoundAny bounds one Insert into any class: an AVL over at most the sequence's handles.
func (o *Occurrences[T, S, K]) InsertBoundAny(journaled bool) uint64 {
	return avlUnits(2*bits.Len(uint(o.seq.length+1))+2, journaled)
}

// InsertBoundAny bounds InsertBefore at any position of a sequence of length handles, built or
// not yet (a preflight before the run it inserts into is reordered): the structural walk for the
// AVL height of length+1 and the capped relabel, scanCap plus a rebuild, with their images.
func InsertBoundAny(length int, journaled bool) uint64 {
	all := uint64(length + 1)                                                       //nolint:gosec // a length is nonnegative
	relabel := saturatingAdd(uint64(scanCap(length+1))+all, images(all, journaled)) //nolint:gosec // a length is nonnegative
	return saturatingAdd(avlUnits(2*bits.Len(uint(length+2))+2, journaled), relabel)
}

// UpdateBoundAny bounds one Insert or Remove of an occurrence class or a rank set with at most
// length members, or a Select in a sequence of length handles.
func UpdateBoundAny(length int, journaled bool) uint64 {
	return avlUnits(2*bits.Len(uint(length+2))+2, journaled)
}

// ReadBound bounds one Next or LastWhere in the class (schema, key): its walk.
func (o *Occurrences[T, S, K]) ReadBound(schema S, key K) uint64 {
	return uint64(o.classHeight(schema, key)) + 2 //nolint:gosec // a height is nonnegative
}

// ReadBound bounds one Contains, Predecessor or Successor: its walk.
func (r *Ranks) ReadBound() uint64 {
	if r.root == nil {
		return 1
	}
	return uint64(r.root.height) + 2 //nolint:gosec // a height is nonnegative
}

// ReplaceHandlesBound bounds ReplaceHandles of the k live handles that occupy one contiguous range
// of s, first and last its first and last in physical order: the ownership scans, the splits
// and joins, the subtree build and the run's relabel, planned exactly (its images included when
// journaled).
func (s *Seq[T]) ReplaceHandlesBound(first, last *Handle[T], k int, journaled bool, pay Pay) uint64 {
	if first == nil || last == nil || k < 2 {
		return 0
	}
	kk := uint64(k)
	bound := saturatingAdd(moveUnits(nodeHeight(s.root), journaled), saturatingAdd(4*kk, images(kk+2, journaled)))
	bound = saturatingAdd(bound, 3*s.SelectBound())
	if s.unlabeled {
		return bound
	}
	charges, touched, ok := s.planLabel(first.prev, last.next, nil, k, max(s.scale, 1), s.length, pay)
	if !ok {
		return ^uint64(0)
	}
	return saturatingAdd(bound, saturatingAdd(charges, images(touched, journaled)))
}
