// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"iter"
	"slices"
)

// The facade changes physical storage only. Callers retain native linking,
// generations, secondary indexes, and their existing work reservation/charging.
// Iteration is uncounted; siblings.all keeps the original visit hooks. Point
// edits return the same shift counts; mutators touch before writing. Restore
// uses previously reserved undo state and cannot fail.
//
// Slices remain authoritative in this stage. Borrowed ranges are invalidated by
// structural mutation; collect only when the caller needs a stable snapshot.

// insertCost is the work insertAt does at position at: the shifted slots and, when it must
// reallocate, the copied ones.
func (d *nodeDeque) insertCost(at int) uint64 {
	l := d.list
	if at >= len(l)/2 || len(l) == 0 {
		if len(l) == cap(l) {
			return uint64(2*len(l)-at) + 1 //nolint:gosec // slice lengths are nonnegative
		}
		return uint64(len(l)-at) + 1 //nolint:gosec // slice lengths are nonnegative
	}
	off := cap(d.base) - cap(l)
	if off <= 0 || off >= len(d.base) || &d.base[off] != &l[0] {
		return uint64(at) + 3*uint64(len(l)) + 1 //nolint:gosec // slice lengths are nonnegative
	}
	return uint64(at) + 1 //nolint:gosec // a position is nonnegative
}

// nodeDeque is a node slice with its backing allocation retained for front headroom.
type nodeDeque struct {
	list []*Node
	base []*Node
}

// insertAt puts n at position at and returns how many slots it shifted: the shorter side moves,
// the front into retained headroom, so appending and prepending are amortized O(1).
// ponytail: a single edit in the middle stays O(n/2) shifts (a 160k list: ~40µs); an ordered
// structure if API edits of huge sibling lists in random order ever matter.
func (d *nodeDeque) insertAt(at int, n *Node) int {
	l := d.list
	if at >= len(l)/2 || len(l) == 0 {
		c := cap(l)
		d.list = slices.Insert(l, at, n)
		if cap(d.list) != c {
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
	d.list = nl
	return at
}

// deleteAt removes position at and returns the number of slots shifted, moving the shorter side.
func (d *nodeDeque) deleteAt(at int) int {
	l := d.list
	if at < len(l)-1-at {
		copy(l[1:at+1], l[:at])
		if len(d.base) == 0 {
			d.base = l[:cap(l)]
		}
		l[0] = nil
		d.list = l[1:]
		return at
	}
	copy(l[at:], l[at+1:])
	l[len(l)-1] = nil
	d.list = l[:len(l)-1]
	return len(l) - 1 - at
}

// siblingSpan is a borrowed physical range. Like the old slice header it keeps
// its length and backing storage: insertion, removal, publication, reset or
// transfer invalidates it. Collect makes an independent snapshot when a walk
// must survive mutation. Neither a span nor a view links or reparents nodes.
// Storage access lives here so a later representation change has one boundary.
type siblingSpan struct{ nodes []*Node }

func spanOf(nodes []*Node) siblingSpan             { return siblingSpan{nodes: nodes} }
func (r siblingSpan) Len() int                     { return len(r.nodes) }
func (r siblingSpan) At(i int) *Node               { return r.nodes[i] }
func (r siblingSpan) Slice(lo, hi int) siblingSpan { return spanOf(r.nodes[lo:hi]) }
func (r siblingSpan) Collect() []*Node             { return slices.Clone(r.nodes) }
func (r siblingSpan) All() iter.Seq2[int, *Node] {
	return func(yield func(int, *Node) bool) {
		for i, n := range r.nodes {
			if !yield(i, n) {
				return
			}
		}
	}
}

// LowerBound searches a monotone bounded range, returning its first true index.
// Preserve BinarySearchFunc's final equality probe as well as its descent:
// callers may charge each predicate invocation to an enforced work budget.
func (r siblingSpan) LowerBound(lo, hi int, pred func(*Node) bool) int {
	end := hi
	for lo < hi {
		mid := lo + (hi-lo)/2
		if pred(r.nodes[mid]) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	if lo < end {
		pred(r.nodes[lo])
	}
	return lo
}
func (r siblingSpan) Index(n *Node) int { return slices.Index(r.nodes, n) }
func (r siblingSpan) indexFromEnd(work *workCounter, n *Node) int {
	// Retain the nearer-end scan and its exact visit accounting.
	return indexFromEnd(work, r.nodes, n)
}

func (s *siblings) Len() int       { return len(s.list) + len(s.opq) }
func (s *siblings) SchemaLen() int { return len(s.list) }
func (s *siblings) OpaqueLen() int { return len(s.opq) }
func (s *siblings) At(i int) *Node {
	if i < len(s.list) {
		return s.list[i]
	}
	return s.opq[i-len(s.list)]
}
func (s *siblings) opaqueAt(i int) *Node             { return s.opq[i] }
func (s *siblings) schemaSpan() siblingSpan          { return spanOf(s.list) }
func (s *siblings) opaqueSpan() siblingSpan          { return spanOf(s.opq) }
func (s *siblings) SchemaAll() iter.Seq2[int, *Node] { return s.schemaSpan().All() }
func (s *siblings) opaqueAll() iter.Seq2[int, *Node] { return s.opaqueSpan().All() }
func (s *siblings) All() iter.Seq2[int, *Node]       { return s.Range(0, s.Len()) }
func (s *siblings) Range(lo, hi int) iter.Seq2[int, *Node] {
	// Capture headers before iteration, as ranging the previous slices did.
	a, b := s.list, s.opq
	return func(yield func(int, *Node) bool) {
		for i := lo; i < hi; i++ {
			var n *Node
			if i < len(a) {
				n = a[i]
			} else {
				n = b[i-len(a)]
			}
			if !yield(i-lo, n) {
				return
			}
		}
	}
}
func (s *siblings) CollectRange(lo, hi int) []*Node {
	if hi <= s.SchemaLen() {
		return slices.Clone(s.list[lo:hi])
	}
	if lo >= s.SchemaLen() {
		return slices.Clone(s.opq[lo-s.SchemaLen() : hi-s.SchemaLen()])
	}
	return slices.Concat(s.list[lo:], s.opq[:hi-s.SchemaLen()])
}

// Index is uncounted, matching slices.Index. Budgeted scans use the span's
// indexFromEnd; ordered-index lookup remains the responsibility of its callers.
func (s *siblings) Index(n *Node) int {
	if i := slices.Index(s.list, n); i >= 0 {
		return i
	}
	if i := slices.Index(s.opq, n); i >= 0 {
		return len(s.list) + i
	}
	return -1
}
func (s *siblings) LowerBound(lo, hi int, pred func(*Node) bool) int {
	end := hi
	for lo < hi {
		mid := lo + (hi-lo)/2
		if pred(s.At(mid)) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	if lo < end {
		pred(s.At(lo))
	}
	return lo
}

// Build publishes a borrowed set of physical nodes without linking them or
// changing compatibility indexes. The caller owns the input arrays afterwards
// and must maintain the schema/opaque partition and secondary index contract.
func (s *siblings) Build(schema, opaque []*Node) {
	s.touch()
	s.list, s.base, s.opq = schema, nil, opaque
}
func (s *siblings) buildSchema(nodes []*Node) { s.touch(); s.list, s.base = nodes, nil }

// exposeSchema temporarily publishes an attach-replay prefix while retaining
// the existing deque backing allocation, exactly as the original publication.
func (s *siblings) exposeSchema(nodes []*Node) { s.touch(); s.list = nodes }
func (s *siblings) appendNode(n *Node) {
	s.touch()
	if n.schema == nil {
		s.opq = append(s.opq, n)
	} else {
		s.list = append(s.list, n)
	}
}
func (s *siblings) InsertAt(at int, n *Node) int {
	s.touch()
	if n.schema != nil {
		return s.insertAt(at, n)
	}
	at -= len(s.list)
	old := len(s.opq)
	if at < 0 || at >= old {
		s.opq = append(s.opq, n)
		return 0
	}
	s.opq = slices.Insert(s.opq, at, n)
	return old - at
}
func (s *siblings) DeleteAt(at int) int {
	s.touch()
	if at < len(s.list) {
		return s.deleteAt(at)
	}
	d := nodeDeque{list: s.opq}
	moves := d.deleteAt(at - len(s.list))
	s.opq = d.list
	return moves
}

// Splice replaces physical slots; publication and compatibility replay stay with
// the caller. It makes no ownership/index changes beyond the physical storage.
func (s *siblings) Splice(lo, hi int, nodes []*Node) {
	s.touch()
	boundary := len(s.list)
	split := 0
	for split < len(nodes) && nodes[split].schema != nil {
		split++
	}
	if lo < boundary || split > 0 {
		s.list = slices.Replace(s.list, min(lo, boundary), min(hi, boundary), nodes[:split]...)
		s.base = nil
	}
	if hi > boundary || split < len(nodes) {
		s.opq = slices.Replace(s.opq, max(0, lo-boundary), max(0, hi-boundary), nodes[split:]...)
	}
}
func (s *siblings) deleteMatching(schema, opaque func(*Node) bool) {
	s.touch()
	if schema != nil {
		s.list = slices.DeleteFunc(s.list, schema)
	}
	if opaque != nil {
		s.opq = slices.DeleteFunc(s.opq, opaque)
	}
}
func (s *siblings) opaqueInsertCost() uint64 {
	if len(s.opq) == cap(s.opq) {
		return uint64(len(s.opq)) + 1
	}
	return 1
}
func (s *siblings) cloneStorage(src *siblings) {
	s.nodeDeque = nodeDeque{list: slices.Clone(src.list)}
	s.opq = slices.Clone(src.opq)
}

// transferFrom moves the entire container, including native history and work
// binding; no node is relinked. The caller rebinds its new owner afterwards.
func (s *siblings) transferFrom(src *siblings) { s.touch(); src.touch(); *s, *src = *src, siblings{} }
func (s *siblings) reset()                     { s.touch(); *s = siblings{} }

// cacheDeque is unrelated duplication-cache storage. Keeping it distinct makes
// the storage-access lint independent of receiver names or type-checker imports.
type cacheDeque struct{ nodes, base []*Node }

func (d *cacheDeque) insertAt(at int, n *Node) int {
	q := nodeDeque{list: d.nodes, base: d.base}
	m := q.insertAt(at, n)
	d.nodes, d.base = q.list, q.base
	return m
}
func (d *cacheDeque) deleteAt(at int) int {
	q := nodeDeque{list: d.nodes, base: d.base}
	m := q.deleteAt(at)
	d.nodes, d.base = q.list, q.base
	return m
}

func (r siblingSpan) AppendTo(dst []*Node) []*Node { return append(dst, r.nodes...) }

// replaceSchema overwrites an equal-sized run while retaining deque headroom.
// Publication invalidates borrowed ranges; callers must reacquire them.
func (s *siblings) replaceSchema(lo, hi int, nodes siblingSpan) {
	s.touch()
	copy(s.list[lo:hi], nodes.nodes)
}

func (d *cacheDeque) schemaSpan() siblingSpan { return spanOf(d.nodes) }
func (d *nodeDeque) retainsBase(n *Node) bool { return slices.Contains(d.base[:cap(d.base)], n) }
func (d *nodeDeque) retainsList(n *Node) bool { return slices.Contains(d.list[:cap(d.list)], n) }

// Reverse cursors keep full/subtree walks linear after a representation switch.
func (r siblingSpan) Reverse() iter.Seq2[int, *Node] {
	return func(yield func(int, *Node) bool) {
		for i := len(r.nodes) - 1; i >= 0; i-- {
			if !yield(i, r.nodes[i]) {
				return
			}
		}
	}
}
func (s *siblings) reverseAll() iter.Seq2[int, *Node] {
	a, b := s.schemaSpan(), s.opaqueSpan()
	return func(yield func(int, *Node) bool) {
		for i, n := range b.Reverse() {
			if !yield(a.Len()+i, n) {
				return
			}
		}
		for i, n := range a.Reverse() {
			if !yield(i, n) {
				return
			}
		}
	}
}
func (s *siblings) snapshotStorage() siblings { saved := *s; saved.cloneStorage(s); return saved }

// restoreStorage uses already reserved undo state and cannot fail or journal.
func (s *siblings) restoreStorage(saved siblings) { *s = saved }

// siblingCursor reads a borrowed forest incrementally, without materialising a
// wide child set or repeatedly selecting by rank. Mutation invalidates it.
type siblingCursor struct {
	schema, opaque siblingSpan
	next           int
	owner          *siblings
}

func (s *siblings) cursor() siblingCursor {
	return siblingCursor{schema: s.schemaSpan(), opaque: s.opaqueSpan(), owner: s}
}
func (c *siblingCursor) done() bool { return c.next == c.schema.Len()+c.opaque.Len() }
func (c *siblingCursor) take() *Node {
	i := c.next
	c.next++
	if i < c.schema.Len() {
		return c.schema.At(i)
	}
	return c.opaque.At(i - c.schema.Len())
}

func (c *siblingCursor) visit() { c.owner.visit() }

// restoreFields retains the separately journaled sibling container of a node.
func (n *Node) restoreFields(saved Node) { kids := n.kids; *n = saved; n.kids = kids }
