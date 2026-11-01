// SPDX-License-Identifier: BSD-3-Clause

package data

// deadLinks is the running when pass's view of the nodes it deleted (flagDead) and unlinks only
// at its end (one compaction per sibling set): libyang unlinks each at once, so the lookups
// read the live ends and neighbours instead. Skip pointers with path compression make every
// walk past dead nodes amortized near O(1) per deleted node, whatever the number of lookups;
// document order does not change during the pass (it deletes, it inserts nothing).
type deadLinks struct {
	next, prev, prevSchema map[*Node]*Node // a dead node's live-side candidate in each direction
	steps                  int             // uncompressed sibling steps taken (tests)
}

// markDead records n, just flagged dead, in w's view, opening it on the pass's first deletion.
func (w *workCounter) markDead() {
	if w.dead == nil {
		w.dead = &deadLinks{next: map[*Node]*Node{}, prev: map[*Node]*Node{}, prevSchema: map[*Node]*Node{}}
	}
}

// live walks from c (nil: none) past dead nodes by step, through the skip pointers of m: the
// first live node in that direction, nil if none.
func (s *siblings) live(c *Node, m map[*Node]*Node, step func(*Node) *Node) *Node {
	var path []*Node
	for c != nil && c.flags&flagDead != 0 {
		path = append(path, c)
		next, ok := m[c]
		if !ok {
			next = step(c)
			s.work.dead.steps++
		}
		c = next
	}
	for _, p := range path {
		m[p] = c
	}
	return c
}

func (s *siblings) deadView() *deadLinks {
	if s.work == nil {
		return nil
	}
	return s.work.dead
}

// liveAfter and liveBefore are the first live sibling at or after c, and at or before c, in
// document order: lyd_node.next and .prev once the pass's deletions are unlinked.
func (s *siblings) liveAfter(c *Node) *Node {
	d := s.deadView()
	if d == nil {
		return c
	}
	return s.live(c, d.next, s.docNext)
}

func (s *siblings) liveBefore(c *Node) *Node {
	d := s.deadView()
	if d == nil {
		return c
	}
	return s.live(c, d.prev, func(n *Node) *Node { p, _ := s.docNeighbours(n); return p })
}

// liveFirst is the first live sibling (libyang's first child after the pass's deletions).
func (s *siblings) liveFirst() *Node { return s.liveAfter(s.docFirst()) }

// liveLastSchema is the last live schema sibling, nil if none (interleaved sets: the kind
// thread).
func (s *siblings) liveLastSchema() *Node {
	if s.st == nil {
		return nil
	}
	c := handleValue(s.st.seq.LastKind(true))
	d := s.deadView()
	if d == nil {
		return c
	}
	return s.live(c, d.prevSchema, func(n *Node) *Node { return handleValue(s.st.seq.PrevKind(s.handle(n))) })
}
