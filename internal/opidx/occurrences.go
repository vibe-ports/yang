// SPDX-License-Identifier: BSD-3-Clause

package opidx

// Occurrences indexes physical instances by a caller-supplied schema and
// equivalence key. Equality and key construction remain the caller's job.
// Unregister a handle before moving or removing it from the sequence, then
// register it again at its new position. Relabeling alone needs no update:
// labels change, but the relative order of existing handles does not.
type Occurrences[T any, S comparable, K comparable] struct {
	seq   *Seq[T]
	trees map[occKey[S, K]]*occTree[T]
	by    map[*Handle[T]]occKey[S, K]
	work  func(uint64)
}

type occKey[S comparable, K comparable] struct {
	schema S
	key    K
}

type occTree[T any] struct {
	root        *occNode[T]
	first, last *Handle[T]
	len         int
}

type occNode[T any] struct {
	handle      *Handle[T]
	left, right *occNode[T]
	height      int
}

// NewOccurrences creates an empty index over seq.
func NewOccurrences[T any, S comparable, K comparable](seq *Seq[T]) *Occurrences[T, S, K] {
	return &Occurrences[T, S, K]{
		seq: seq, trees: make(map[occKey[S, K]]*occTree[T]),
		by: make(map[*Handle[T]]occKey[S, K]),
	}
}

// SetWorkHook sets a synchronous counter for node visits and rotations.
func (o *Occurrences[T, S, K]) SetWorkHook(hook func(uint64)) { o.work = hook }

func (o *Occurrences[T, S, K]) charge() {
	if o.work != nil {
		o.work(1)
	}
}

// Insert registers one live sequence handle. It rejects duplicate registration
// and handles from another sequence.
func (o *Occurrences[T, S, K]) Insert(schema S, key K, h *Handle[T]) bool {
	if o.seq == nil || h == nil || h.owner != o.seq {
		return false
	}
	if _, present := o.by[h]; present {
		return false
	}
	k := occKey[S, K]{schema, key}
	t := o.trees[k]
	if t == nil {
		t = &occTree[T]{}
		o.trees[k] = t
	}
	t.root = o.insert(t.root, h)
	t.len++
	if t.first == nil || h.tag < t.first.tag {
		t.first = h
	}
	if t.last == nil || h.tag > t.last.tag {
		t.last = h
	}
	o.by[h] = k
	return true
}

// Remove unregisters a handle while it still belongs to the sequence.
func (o *Occurrences[T, S, K]) Remove(h *Handle[T]) bool {
	k, present := o.by[h]
	if !present || h.owner != o.seq {
		return false
	}
	t := o.trees[k]
	if t.first == h {
		t.first = o.Next(k.schema, k.key, h)
	}
	if t.last == h {
		t.last = o.previous(t.root, h.tag)
	}
	t.root = o.remove(t.root, h.tag)
	t.len--
	if t.len == 0 {
		delete(o.trees, k)
	}
	delete(o.by, h)
	return true
}

// Len returns the number of registered instances in one equivalence class.
func (o *Occurrences[T, S, K]) Len(schema S, key K) int {
	if t := o.trees[occKey[S, K]{schema, key}]; t != nil {
		return t.len
	}
	return 0
}

// First returns the earliest physical instance in a class.
func (o *Occurrences[T, S, K]) First(schema S, key K) *Handle[T] {
	if t := o.trees[occKey[S, K]{schema, key}]; t != nil {
		return t.first
	}
	return nil
}

// Last returns the latest physical instance in a class.
func (o *Occurrences[T, S, K]) Last(schema S, key K) *Handle[T] {
	if t := o.trees[occKey[S, K]{schema, key}]; t != nil {
		return t.last
	}
	return nil
}

// Next returns the first instance of (schema,key) physically after h. h may
// belong to a different equivalence class; nil denotes before the first node.
func (o *Occurrences[T, S, K]) Next(schema S, key K, h *Handle[T]) *Handle[T] {
	t := o.trees[occKey[S, K]{schema, key}]
	if t == nil {
		return nil
	}
	if h == nil {
		return t.first
	}
	if h.owner != o.seq {
		return nil
	}
	var next *Handle[T]
	for n := t.root; n != nil; {
		o.charge()
		if n.handle.tag > h.tag {
			next, n = n.handle, n.left
		} else {
			n = n.right
		}
	}
	return next
}

// LastWhere returns the last instance in physical order for which keep is true.
// keep must be monotone over the selected occurrences (true values followed by
// false values). This is useful when physical order is also a caller-defined
// sorted order, without exposing the occurrence tree representation.
func (o *Occurrences[T, S, K]) LastWhere(schema S, key K, keep func(T) bool) *Handle[T] {
	t := o.trees[occKey[S, K]{schema, key}]
	if t == nil || keep == nil {
		return nil
	}
	var found *Handle[T]
	for n := t.root; n != nil; {
		o.charge()
		if keep(n.handle.Value) {
			found, n = n.handle, n.right
		} else {
			n = n.left
		}
	}
	return found
}

func occHeight[T any](n *occNode[T]) int {
	if n == nil {
		return 0
	}
	return n.height
}

func occFix[T any](n *occNode[T]) {
	n.height = 1 + max(occHeight(n.left), occHeight(n.right))
}

func (o *Occurrences[T, S, K]) rotateLeft(n *occNode[T]) *occNode[T] {
	o.charge()
	x := n.right
	n.right, x.left = x.left, n
	occFix(n)
	occFix(x)
	return x
}

func (o *Occurrences[T, S, K]) rotateRight(n *occNode[T]) *occNode[T] {
	o.charge()
	x := n.left
	n.left, x.right = x.right, n
	occFix(n)
	occFix(x)
	return x
}

func (o *Occurrences[T, S, K]) balance(n *occNode[T]) *occNode[T] {
	occFix(n)
	if occHeight(n.left)-occHeight(n.right) > 1 {
		if occHeight(n.left.left) < occHeight(n.left.right) {
			n.left = o.rotateLeft(n.left)
		}
		return o.rotateRight(n)
	}
	if occHeight(n.right)-occHeight(n.left) > 1 {
		if occHeight(n.right.right) < occHeight(n.right.left) {
			n.right = o.rotateRight(n.right)
		}
		return o.rotateLeft(n)
	}
	return n
}

func (o *Occurrences[T, S, K]) insert(n *occNode[T], h *Handle[T]) *occNode[T] {
	if n == nil {
		return &occNode[T]{handle: h, height: 1}
	}
	o.charge()
	if h.tag < n.handle.tag {
		n.left = o.insert(n.left, h)
	} else {
		n.right = o.insert(n.right, h)
	}
	return o.balance(n)
}

func (o *Occurrences[T, S, K]) popMin(n *occNode[T]) (*occNode[T], *occNode[T]) {
	o.charge()
	if n.left == nil {
		return n.right, n
	}
	var smallest *occNode[T]
	n.left, smallest = o.popMin(n.left)
	return o.balance(n), smallest
}

func (o *Occurrences[T, S, K]) remove(n *occNode[T], tag uint64) *occNode[T] {
	o.charge()
	switch {
	case tag < n.handle.tag:
		n.left = o.remove(n.left, tag)
	case tag > n.handle.tag:
		n.right = o.remove(n.right, tag)
	default:
		if n.left == nil {
			return n.right
		}
		if n.right == nil {
			return n.left
		}
		var next *occNode[T]
		n.right, next = o.popMin(n.right)
		n.handle = next.handle
	}
	return o.balance(n)
}

func (o *Occurrences[T, S, K]) previous(n *occNode[T], tag uint64) *Handle[T] {
	var prev *Handle[T]
	for n != nil {
		o.charge()
		if n.handle.tag < tag {
			prev, n = n.handle, n.right
		} else {
			n = n.left
		}
	}
	return prev
}
