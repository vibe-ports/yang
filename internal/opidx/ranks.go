// SPDX-License-Identifier: BSD-3-Clause

package opidx

// Ranks is a sparse AVL set of schema ranks. Use separate instances for input
// and output; storage depends on populated ranks, not schema width.
type Ranks struct {
	root  *rankNode
	len   int
	work  func(uint64)
	hooks Hooks
	stamp uint64
}

type rankNode struct {
	key         int
	left, right *rankNode
	height      int
	stamp       uint64
}

// SetWorkHook sets a synchronous counter for node visits and rotations.
func (r *Ranks) SetWorkHook(hook func(uint64)) { r.work = hook }

// Len returns the number of populated ranks.
func (r *Ranks) Len() int { return r.len }

// SetHooks connects r to its owner's work counter and journal; they replace the work hook.
func (r *Ranks) SetHooks(h Hooks) { r.hooks = h }

func (r *Ranks) charge() {
	if r.hooks != nil {
		r.hooks.Work(1)
	} else if r.work != nil {
		r.work(1)
	}
}

func (r *Ranks) journal() *Journal {
	if r.hooks == nil {
		return nil
	}
	return r.hooks.Journal()
}

func (r *Ranks) touchHeader() {
	if j := r.journal(); j != nil && j.first(&r.stamp) {
		j.image(Entry{obj: r, ptr: [6]any{r.root}, num: [3]uint64{uint64(r.len)}}, r.hooks) //nolint:gosec // restored bit for bit
	}
}

func (r *Ranks) undo(e *Entry) {
	r.root, _ = e.ptr[0].(*rankNode)
	r.len = int(e.num[0]) //nolint:gosec // the recorded int itself
}

func (r *Ranks) purge(*Entry) {}

func (r *Ranks) touch(n *rankNode) {
	if n == nil {
		return
	}
	if j := r.journal(); j != nil && j.first(&n.stamp) {
		j.image(Entry{obj: n, ptr: [6]any{n.left, n.right}, num: [3]uint64{uint64(n.key), uint64(n.height)}}, r.hooks) //nolint:gosec // restored bit for bit
	}
}

func (n *rankNode) undo(e *Entry) {
	n.left, _ = e.ptr[0].(*rankNode)
	n.right, _ = e.ptr[1].(*rankNode)
	n.key, n.height = int(e.num[0]), int(e.num[1]) //nolint:gosec // the recorded ints themselves
}

func (n *rankNode) purge(*Entry) {}

func rankHeight(n *rankNode) int {
	if n == nil {
		return 0
	}
	return n.height
}

func rankFix(n *rankNode) { n.height = 1 + max(rankHeight(n.left), rankHeight(n.right)) }

func (r *Ranks) rankLeft(n *rankNode) *rankNode {
	r.charge()
	x := n.right
	r.touch(n)
	r.touch(x)
	n.right, x.left = x.left, n
	rankFix(n)
	rankFix(x)
	return x
}

func (r *Ranks) rankRight(n *rankNode) *rankNode {
	r.charge()
	x := n.left
	r.touch(n)
	r.touch(x)
	n.left, x.right = x.right, n
	rankFix(n)
	rankFix(x)
	return x
}

func (r *Ranks) rankBalance(n *rankNode) *rankNode {
	rankFix(n)
	if rankHeight(n.left)-rankHeight(n.right) > 1 {
		if rankHeight(n.left.left) < rankHeight(n.left.right) {
			n.left = r.rankLeft(n.left)
		}
		return r.rankRight(n)
	}
	if rankHeight(n.right)-rankHeight(n.left) > 1 {
		if rankHeight(n.right.right) < rankHeight(n.right.left) {
			n.right = r.rankRight(n.right)
		}
		return r.rankLeft(n)
	}
	return n
}

// Insert returns false when rank was already present.
func (r *Ranks) Insert(rank int) bool {
	r.touchHeader()
	var added bool
	r.root, added = r.insert(r.root, rank)
	if added {
		r.len++
	}
	return added
}

func (r *Ranks) insert(n *rankNode, key int) (*rankNode, bool) {
	if n == nil {
		return &rankNode{key: key, height: 1}, true
	}
	r.charge()
	r.touch(n)
	var added bool
	switch {
	case key < n.key:
		n.left, added = r.insert(n.left, key)
	case key > n.key:
		n.right, added = r.insert(n.right, key)
	default:
		return n, false
	}
	if !added {
		return n, false
	}
	return r.rankBalance(n), true
}

// Remove returns false when rank was absent.
func (r *Ranks) Remove(rank int) bool {
	r.touchHeader()
	var removed bool
	r.root, removed = r.remove(r.root, rank)
	if removed {
		r.len--
	}
	return removed
}

func (r *Ranks) popRankMin(n *rankNode) (*rankNode, *rankNode) {
	r.charge()
	r.touch(n)
	if n.left == nil {
		return n.right, n
	}
	var smallest *rankNode
	n.left, smallest = r.popRankMin(n.left)
	return r.rankBalance(n), smallest
}

func (r *Ranks) remove(n *rankNode, key int) (*rankNode, bool) {
	if n == nil {
		return nil, false
	}
	r.charge()
	r.touch(n)
	var removed bool
	switch {
	case key < n.key:
		n.left, removed = r.remove(n.left, key)
	case key > n.key:
		n.right, removed = r.remove(n.right, key)
	default:
		if n.left == nil {
			return n.right, true
		}
		if n.right == nil {
			return n.left, true
		}
		var next *rankNode
		n.right, next = r.popRankMin(n.right)
		n.key = next.key
		removed = true
	}
	if !removed {
		return n, false
	}
	return r.rankBalance(n), true
}

// Contains reports whether rank is present.
func (r *Ranks) Contains(rank int) bool {
	for n := r.root; n != nil; {
		r.charge()
		switch {
		case rank < n.key:
			n = n.left
		case rank > n.key:
			n = n.right
		default:
			return true
		}
	}
	return false
}

// Predecessor returns the greatest rank strictly below rank.
func (r *Ranks) Predecessor(rank int) (int, bool) {
	var candidate *rankNode
	for n := r.root; n != nil; {
		r.charge()
		if n.key < rank {
			candidate, n = n, n.right
		} else {
			n = n.left
		}
	}
	if candidate == nil {
		return 0, false
	}
	return candidate.key, true
}

// Successor returns the least rank strictly above rank.
func (r *Ranks) Successor(rank int) (int, bool) {
	var candidate *rankNode
	for n := r.root; n != nil; {
		r.charge()
		if n.key > rank {
			candidate, n = n, n.left
		} else {
			n = n.right
		}
	}
	if candidate == nil {
		return 0, false
	}
	return candidate.key, true
}
