// SPDX-License-Identifier: BSD-3-Clause

package opidx

// Occurrences indexes physical instances by a caller-supplied schema and
// equivalence key. Equality and key construction remain the caller's job.
// Unregister a handle before moving or removing it from the sequence, then
// register it again at its new position. Relabeling alone needs no update:
// labels change, but the relative order of existing handles does not.
//
// Under a Journal (Hooks) a removal stores nil tombstones in trees and by instead of deleting:
// every lookup treats a nil cell as absent, and no count is taken from the maps' sizes.
type Occurrences[T any, S comparable, K comparable] struct {
	seq   *Seq[T]
	trees map[occKey[S, K]]*occTree[T, S, K]
	by    map[*Handle[T]]*occKey[S, K] // the key of the handle's class: &trees[key].key
	work  func(uint64)
	hooks Hooks
}

type occKey[S comparable, K comparable] struct {
	schema S
	key    K
}

type occTree[T any, S comparable, K comparable] struct {
	root        *occNode[T]
	first, last *Handle[T]
	len         int
	key         occKey[S, K]
	stamp       uint64
}

type occNode[T any] struct {
	handle      *Handle[T]
	left, right *occNode[T]
	height      int
	stamp       uint64
}

// NewOccurrences creates an empty index over seq.
func NewOccurrences[T any, S comparable, K comparable](seq *Seq[T]) *Occurrences[T, S, K] {
	return &Occurrences[T, S, K]{
		seq: seq, trees: make(map[occKey[S, K]]*occTree[T, S, K]),
		by: make(map[*Handle[T]]*occKey[S, K]),
	}
}

// SetWorkHook sets a synchronous counter for node visits and rotations.
func (o *Occurrences[T, S, K]) SetWorkHook(hook func(uint64)) { o.work = hook }

// SetHooks connects o to its owner's work counter and journal; they replace the work hook.
func (o *Occurrences[T, S, K]) SetHooks(h Hooks) { o.hooks = h }

func (o *Occurrences[T, S, K]) charge() {
	if o.hooks != nil {
		o.hooks.Work(1)
	} else if o.work != nil {
		o.work(1)
	}
}

func (o *Occurrences[T, S, K]) journal() *Journal {
	if o.hooks == nil {
		return nil
	}
	return o.hooks.Journal()
}

// The map-cell entries: a trees cell keeps a tree carrying its key (old or new value), a by
// cell its handle.
const (
	occTreesCell uint64 = iota
	occByCell
)

func (o *Occurrences[T, S, K]) undo(e *Entry) {
	if e.num[0] == occTreesCell {
		carrier, _ := e.key.(*occTree[T, S, K])
		if e.had {
			o.trees[carrier.key], _ = e.ptr[0].(*occTree[T, S, K])
		} else {
			delete(o.trees, carrier.key)
		}
		return
	}
	h, _ := e.key.(*Handle[T])
	if e.had {
		o.by[h], _ = e.ptr[0].(*occKey[S, K])
	} else {
		delete(o.by, h)
	}
}

func (o *Occurrences[T, S, K]) purge(e *Entry) {
	if e.num[0] == occTreesCell {
		carrier, _ := e.key.(*occTree[T, S, K])
		if t, ok := o.trees[carrier.key]; ok && t == nil {
			delete(o.trees, carrier.key)
		}
		return
	}
	h, _ := e.key.(*Handle[T])
	if k, ok := o.by[h]; ok && k == nil {
		delete(o.by, h)
	}
}

// setTree stores trees[t.key] = v (nil: removes it) with t carrying the key.
func (o *Occurrences[T, S, K]) setTree(j *Journal, t, v *occTree[T, S, K]) {
	if j == nil {
		if v == nil {
			delete(o.trees, t.key)
		} else {
			o.trees[t.key] = v
		}
		return
	}
	old, had := o.trees[t.key]
	j.cell(Entry{obj: o, key: t, had: had, ptr: [6]any{old}, num: [3]uint64{occTreesCell}})
	o.trees[t.key] = v
}

func (o *Occurrences[T, S, K]) setBy(j *Journal, h *Handle[T], k *occKey[S, K]) {
	if j == nil {
		if k == nil {
			delete(o.by, h)
		} else {
			o.by[h] = k
		}
		return
	}
	old, had := o.by[h]
	j.cell(Entry{obj: o, key: h, had: had, ptr: [6]any{old}, num: [3]uint64{occByCell}})
	o.by[h] = k
}

func (t *occTree[T, S, K]) undo(e *Entry) {
	t.root, _ = e.ptr[0].(*occNode[T])
	t.first, _ = e.ptr[1].(*Handle[T])
	t.last, _ = e.ptr[2].(*Handle[T])
	t.len = int(e.num[0]) //nolint:gosec // the recorded int itself
}

func (t *occTree[T, S, K]) purge(*Entry) {}

func (t *occTree[T, S, K]) record(j *Journal) {
	if j != nil && j.first(&t.stamp) {
		j.image(Entry{obj: t, ptr: [6]any{t.root, t.first, t.last}, num: [3]uint64{uint64(t.len)}}) //nolint:gosec // restored bit for bit
	}
}

func (n *occNode[T]) undo(e *Entry) {
	n.handle, _ = e.ptr[0].(*Handle[T])
	n.left, _ = e.ptr[1].(*occNode[T])
	n.right, _ = e.ptr[2].(*occNode[T])
	n.height = int(e.num[0]) //nolint:gosec // the recorded int itself
}

func (n *occNode[T]) purge(*Entry) {}

func (o *Occurrences[T, S, K]) touch(n *occNode[T]) {
	if n == nil {
		return
	}
	if j := o.journal(); j != nil && j.first(&n.stamp) {
		j.image(Entry{obj: n, ptr: [6]any{n.handle, n.left, n.right}, num: [3]uint64{uint64(n.height)}}) //nolint:gosec // restored bit for bit
	}
}

// Insert registers one live sequence handle. It rejects duplicate registration
// and handles from another sequence.
func (o *Occurrences[T, S, K]) Insert(schema S, key K, h *Handle[T]) bool {
	if o.seq == nil || h == nil || h.owner != o.seq {
		return false
	}
	if o.by[h] != nil {
		return false
	}
	j := o.journal()
	k := occKey[S, K]{schema, key}
	t := o.trees[k]
	if t == nil {
		t = &occTree[T, S, K]{key: k}
		o.setTree(j, t, t)
	} else {
		t.record(j)
	}
	t.root = o.insert(t.root, h)
	t.len++
	if t.first == nil || h.tag < t.first.tag {
		t.first = h
	}
	if t.last == nil || h.tag > t.last.tag {
		t.last = h
	}
	o.setBy(j, h, &t.key)
	return true
}

// Remove unregisters a handle while it still belongs to the sequence.
func (o *Occurrences[T, S, K]) Remove(h *Handle[T]) bool {
	kp := o.by[h]
	if kp == nil || h.owner != o.seq {
		return false
	}
	k := *kp
	t := o.trees[k]
	j := o.journal()
	t.record(j)
	if t.first == h {
		t.first = o.Next(k.schema, k.key, h)
	}
	if t.last == h {
		t.last = o.previous(t.root, h.tag)
	}
	t.root = o.remove(t.root, h.tag)
	t.len--
	if t.len == 0 {
		o.setTree(j, t, nil)
	}
	o.setBy(j, h, nil)
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
	o.touch(n)
	o.touch(x)
	n.right, x.left = x.left, n
	occFix(n)
	occFix(x)
	return x
}

func (o *Occurrences[T, S, K]) rotateRight(n *occNode[T]) *occNode[T] {
	o.charge()
	x := n.left
	o.touch(n)
	o.touch(x)
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
	o.touch(n)
	if h.tag < n.handle.tag {
		n.left = o.insert(n.left, h)
	} else {
		n.right = o.insert(n.right, h)
	}
	return o.balance(n)
}

func (o *Occurrences[T, S, K]) popMin(n *occNode[T]) (*occNode[T], *occNode[T]) {
	o.charge()
	o.touch(n)
	if n.left == nil {
		return n.right, n
	}
	var smallest *occNode[T]
	n.left, smallest = o.popMin(n.left)
	return o.balance(n), smallest
}

func (o *Occurrences[T, S, K]) remove(n *occNode[T], tag uint64) *occNode[T] {
	o.charge()
	o.touch(n)
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
