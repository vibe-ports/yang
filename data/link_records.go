// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"github.com/vibe-ports/yang/internal/schema"
	"maps"
)

// linkRecordOrigin preserves the registrations affected by one unlink/relink, without copying
// an entire sibling table or duplicate bucket. Ordinary chains restore their saved neighbours
// directly; an already materialized ordered index reinserts in O(log bucket-size) work.
func linkRecordOrigin(s *siblings, n, next *Node, w *workCounter) (func(), uint64, error) {
	if s.ht == nil || n.schema == nil {
		return func() {}, 0, nil
	}
	history := s.history[n]
	active := s.exactByNode[n]
	var activeValue record
	var chain *recordChain
	if active != nil {
		activeValue = *active
		chain = active.chain
	}
	avl := chain != nil && chain.root != nil
	height := 0
	if avl {
		height = recordHeight(chain.root)
	}
	bound := 4*uint64(len(history)) + 64 + 32*uint64(height)
	if err := w.chargeHash(bound); err != nil {
		return nil, 0, err
	}
	oldHistory := maps.Clone(history)
	values := make(map[*record]record, len(history))
	for _, r := range history {
		values[r] = *r
	}
	// more's elements are immutable registration IDs. This local operation only reslices the
	// head or appends after the old end, so retaining the old slice header is sufficient.
	key, hashed := n.hkey, n.hashed
	exactOrder, leaderSeq := s.exactOrder, s.leaderSeq
	first := s.firstRun[n.schema]
	leader := s.leaders[n.schema]
	var leaderValue recordChain
	if leader != nil {
		leaderValue = *leader
	}
	leaderNodes := map[*Node]*record{n: s.leaderByNode[n]}
	if next != nil && next.schema == n.schema {
		leaderNodes[next] = s.leaderByNode[next]
	}
	leaderValues := map[*record]record{}
	keep := func(r *record) {
		if r != nil {
			leaderValues[r] = *r
		}
	}
	if leader != nil {
		keep(leader.first)
		keep(leader.last)
	}
	for _, r := range leaderNodes {
		if r != nil {
			keep(r)
			keep(r.prev)
			keep(r.next)
		}
	}
	return func() {
		if err := s.touch(); err != nil {
			return
		}
		if err := n.touch(); err != nil {
			return
		}
		s.unindexRecord(n)
		delete(s.history, n)
		for r, saved := range values {
			*r = record{node: saved.node, order: saved.order, seq: saved.seq, more: saved.more}
		}
		if oldHistory != nil {
			if s.history == nil {
				s.history = map[*Node]map[uint32]*record{}
			}
			s.history[n] = oldHistory
		}
		if hashed && active != nil && chain != nil {
			if avl {
				chain.root, _ = recordOrderInsert(chain.root, active, s.work)
			}
			active.chain, active.prev, active.next = chain, activeValue.prev, activeValue.next
			if active.prev == nil {
				chain.first = active
			} else {
				active.prev.next = active
			}
			if active.next == nil {
				chain.last = active
			} else {
				active.next.prev = active
			}
			chain.count++
			s.ht[key] = chain
			if s.exactByNode == nil {
				s.exactByNode = map[*Node]*record{}
			}
			s.exactByNode[n] = active
		}
		n.hkey, n.hashed = key, hashed
		s.exactOrder, s.leaderSeq = exactOrder, leaderSeq
		if leader == nil {
			delete(s.leaders, n.schema)
		} else {
			if s.leaders == nil {
				s.leaders = map[*schema.Node]*recordChain{}
			}
			s.leaders[n.schema] = leader
			*leader = leaderValue
		}
		for r, saved := range leaderValues {
			*r = saved
		}
		for node, r := range leaderNodes {
			if r == nil {
				delete(s.leaderByNode, node)
			} else {
				if s.leaderByNode == nil {
					s.leaderByNode = map[*Node]*record{}
				}
				s.leaderByNode[node] = r
			}
		}
		if first == nil {
			delete(s.firstRun, n.schema)
		} else {
			s.firstRun[n.schema] = first
		}
	}, bound, nil
}
