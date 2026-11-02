// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"github.com/vibe-ports/yang/internal/opidx"
)

// siblingPlacement is one resolved link result. The caller keeps it locally and passes it to
// both reservation and application, without changing the destination or retaining the source
// in it. A storage mutation invalidates the result. at uses link's partition coordinates;
// docAt is the physical position simulated by the key preflight. See design 17.
type siblingPlacement struct {
	anchor    *opidx.Handle[*Node]
	at, docAt int
}

// linkPlacement resolves legacy placement: schema nodes at a schema rank, opaque nodes at an
// opaque rank (-1 appends). Ordinary layouts need no additional rank walk or allocation.
func (s *siblings) linkPlacement(n *Node, at int) siblingPlacement {
	p := siblingPlacement{at: at, anchor: s.storeAnchor(n, at)}
	switch {
	case s.st.interleaved():
		p.docAt = s.Len()
		if p.anchor != nil {
			p.docAt, _ = s.st.seq.Pos(p.anchor)
		}
	case n.schema != nil:
		p.docAt = min(max(at, 0), s.SchemaLen())
	case at < 0 || at >= s.OpaqueLen():
		p.docAt = s.Len()
	default:
		p.docAt = s.SchemaLen() + at
	}
	return p
}

// physicalPlacement resolves an explicit physical anchor (nil appends). Only package tests
// select this placement; public producers keep linkPlacement's schema-then-opaque layout.
func (s *siblings) physicalPlacement(n, before *Node) siblingPlacement {
	p := siblingPlacement{anchor: s.handle(before), docAt: s.Len()}
	if p.anchor == nil {
		p.at = s.SchemaLen()
		if n.schema == nil {
			p.at = s.OpaqueLen()
		}
		return p
	}
	p.docAt, _ = s.st.seq.Pos(p.anchor)
	k, _ := s.st.seq.PosKind(p.anchor)
	if (n.schema != nil) == p.anchor.Heavy() {
		p.at = k
	} else {
		p.at = p.docAt - k
	}
	return p
}

func (s *siblings) linkAnchor(p siblingPlacement) *opidx.Handle[*Node] { return p.anchor }
func (s *siblings) linkDocPos(p siblingPlacement) int                  { return p.docAt }

// docPrev is the physical predecessor (nil at the beginning, without libyang's cyclic wrap).
func (s *siblings) docPrev(n *Node) *Node {
	if s.st == nil {
		return nil
	}
	return handleValue(s.st.seq.Prev(s.handle(n)))
}

// linkMixes tests the prospective layout using the same resolved anchor as the bound and
// insertion. Preflights run before the insertion can publish the sticky interleaving flag.
func (s *siblings) linkMixes(n *Node, p siblingPlacement) bool {
	return s.st != nil && s.st.mixes(s.linkAnchor(p), n)
}
