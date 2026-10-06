// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/set.c (ly_set_add, ly_set_rm_index, ly_set_contains)
// (BSD-3-Clause, © CESNET).

package data

// nodeSet is struct ly_set of data nodes as the validation queues use it. The removal kind
// matters: libyang drains node_types with ly_set_rm_index, which moves the last item into the
// hole, so removals change the order later items are visited in. A node is in a queue at most
// once (the parser and the implicit nodes add each node once), so a position index makes
// contains O(1); it is kept up to date by the removals.
type nodeSet struct {
	items []*Node
	pos   map[*Node]int
}

// add is ly_set_add with list = 1 (appended).
func (s *nodeSet) add(n *Node) {
	if s.pos != nil {
		s.pos[n] = len(s.items)
	}
	s.items = append(s.items, n)
}

// rmIndex is ly_set_rm_index: the last item takes the place of the removed one.
func (s *nodeSet) rmIndex(i int) {
	last := len(s.items) - 1
	if s.pos != nil {
		delete(s.pos, s.items[i])
		if i != last {
			s.pos[s.items[last]] = i
		}
	}
	s.items[i] = s.items[last]
	s.items[last] = nil
	s.items = s.items[:last]
}

// contains is ly_set_contains: the index of n, or -1.
func (s *nodeSet) contains(n *Node) int {
	if s.pos == nil {
		s.pos = make(map[*Node]int, len(s.items))
		for i, m := range s.items {
			if _, dup := s.pos[m]; !dup {
				s.pos[m] = i
			}
		}
	}
	if i, ok := s.pos[n]; ok {
		return i
	}
	return -1
}

func (s *nodeSet) len() int { return len(s.items) }
