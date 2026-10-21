// SPDX-License-Identifier: BSD-3-Clause

package opidx

// Ranks is a sparse AVL set of schema ranks. Use separate instances for input
// and output; storage depends on populated ranks, not schema width.
type Ranks struct {
	root *rankNode
	len  int
	work func(uint64)
}

type rankNode struct {
	key         int
	left, right *rankNode
	height      int
}

// SetWorkHook sets a synchronous counter for node visits and rotations.
func (r *Ranks) SetWorkHook(hook func(uint64)) { r.work = hook }

// Len returns the number of populated ranks.
func (r *Ranks) Len() int { return r.len }

func (r *Ranks) charge() {
	if r.work != nil {
		r.work(1)
	}
}

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
	n.right, x.left = x.left, n
	rankFix(n)
	rankFix(x)
	return x
}

func (r *Ranks) rankRight(n *rankNode) *rankNode {
	r.charge()
	x := n.left
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
	var removed bool
	r.root, removed = r.remove(r.root, rank)
	if removed {
		r.len--
	}
	return removed
}

func (r *Ranks) popRankMin(n *rankNode) (*rankNode, *rankNode) {
	r.charge()
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
