// SPDX-License-Identifier: BSD-3-Clause

package data

// deadLinks is the running when pass's view of the nodes it deleted (flagDead): their index part
// is unlinked at once (whenPass.del, unlinkIndex), their storage at the pass's end (one
// compaction per sibling set). libyang unlinks each node at once, so the pass's lookups read the
// live ends instead of the stored ones. Only the pass writes it (each deletion, through skip
// pointers with path compression: amortized near O(1) per deleted node); a lookup, a public
// read from user code while the pass waits on it included, only reads the ends. Document order
// does not change during the pass (it deletes, it inserts nothing).
type deadLinks struct {
	next, prev, prevSchema map[*Node]*Node // a dead node's live-side candidate in each direction
	indexed                map[*Node]bool  // deletions whose index part is unlinked already
	ends                   map[*siblings]*deadEnds
	steps                  int // uncompressed sibling steps taken (tests)
}

// deadEnds is a sibling set's first live sibling and last live schema sibling, kept by the pass
// for each set it deleted from.
type deadEnds struct{ first, lastSchema *Node }

// markDead opens w's view on the pass's first deletion.
func (w *workCounter) markDead() {
	if w.dead == nil {
		w.dead = &deadLinks{next: map[*Node]*Node{}, prev: map[*Node]*Node{}, prevSchema: map[*Node]*Node{},
			indexed: map[*Node]bool{}, ends: map[*siblings]*deadEnds{}}
	}
}

// live walks from c (nil: none) past dead nodes by step, through the skip pointers of m: the
// first live node in that direction, nil if none. The pass only (it writes the pointers).
func (d *deadLinks) live(c *Node, m map[*Node]*Node, step func(*Node) *Node) *Node {
	var path []*Node
	for c != nil && c.flags&flagDead != 0 {
		path = append(path, c)
		next, ok := m[c]
		if !ok {
			next = step(c)
			d.steps++
		}
		c = next
	}
	for _, p := range path {
		m[p] = c
	}
	return c
}

// liveAfter and liveBefore are the first live sibling at or after c, and at or before c, in
// document order: lyd_node.next and .prev once the pass's deletions are unlinked. The pass only.
func (s *siblings) liveAfter(c *Node) *Node {
	d := s.deadView()
	if d == nil {
		return c
	}
	return d.live(c, d.next, s.docNext)
}

func (s *siblings) liveBefore(c *Node) *Node {
	d := s.deadView()
	if d == nil {
		return c
	}
	return d.live(c, d.prev, func(n *Node) *Node { p, _ := s.docNeighbours(n); return p })
}

// endsAfter updates s's live ends after a deletion from s. The pass only.
func (d *deadLinks) endsAfter(s *siblings) {
	e := d.ends[s]
	if e == nil {
		e = &deadEnds{first: s.docFirst()}
		if s.st != nil {
			e.lastSchema = handleValue(s.st.seq.LastKind(true))
		}
		d.ends[s] = e
	}
	e.first = s.liveAfter(e.first)
	e.lastSchema = d.live(e.lastSchema, d.prevSchema, func(n *Node) *Node { return handleValue(s.st.seq.PrevKind(s.handle(n))) })
}

func (s *siblings) deadView() *deadLinks {
	if s.work == nil {
		return nil
	}
	return s.work.dead
}

// liveFirst is the first live sibling (libyang's first child after the pass's deletions). It
// only reads.
func (s *siblings) liveFirst() *Node {
	if d := s.deadView(); d != nil {
		if e := d.ends[s]; e != nil {
			return e.first
		}
	}
	return s.docFirst()
}

// liveLastSchema is the last live schema sibling, nil if none (interleaved sets: the kind
// thread). It only reads.
func (s *siblings) liveLastSchema() *Node {
	if d := s.deadView(); d != nil {
		if e := d.ends[s]; e != nil {
			return e.lastSchema
		}
	}
	if s.st == nil {
		return nil
	}
	return handleValue(s.st.seq.LastKind(true))
}
