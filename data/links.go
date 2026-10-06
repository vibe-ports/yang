// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_get_or_create_leafref_links_record,
// lyd_leafref_get_links, lyd_link_leafref_node, lyd_leafref_link_node_tree_type,
// lyd_leafref_link_node_tree) and src/tree_data_free.c (lyd_free_leafref_links_rec,
// lyd_free_leafref_nodes) (BSD-3-Clause, © CESNET).

package data

import (
	"errors"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
)

// leafrefLinks is struct lyd_leafref_links_rec: the leafref nodes pointing to a term node and,
// for a leafref, its targets. libyang keeps the records in a context hash table keyed by the
// node; the port keeps a node's record on the node (nil: none).
type leafrefLinks struct {
	leafrefs []*Node // leafref_nodes
	targets  []*Node // target_nodes
}

var (
	errLinksDenied   = &opError{"LY_EDENIED", ""}   // the context has no LY_CTX_LEAFREF_LINKING
	errLinksNotFound = &opError{"LY_ENOTFOUND", ""} // the node has no record
)

// linksOf is lyd_leafref_get_links: the record of n.
func linksOf(set *schema.Set, n *Node) (*leafrefLinks, error) {
	switch {
	case !set.LeafrefLinking:
		return nil, errLinksDenied
	case n.links == nil:
		return nil, errLinksNotFound
	}
	return n.links, nil
}

// linkLeafrefNode is lyd_link_leafref_node: lref points to target. Like libyang it stops early
// when lref is already among target's leafref nodes.
func linkLeafrefNode(target, lref *Node) {
	if target.links == nil {
		target.links = &leafrefLinks{}
	}
	for _, l := range target.links.leafrefs {
		if l == lref {
			return
		}
	}
	target.links.leafrefs = append(target.links.leafrefs, lref)
	if lref.links == nil {
		lref.links = &leafrefLinks{}
	}
	for _, t := range lref.links.targets {
		if t == target {
			return
		}
	}
	lref.links.targets = append(lref.links.targets, target)
}

// removeValue is LY_ARRAY_REMOVE_VALUE: the first n leaves the array, the last item taking its
// place.
func removeValue(a []*Node, n *Node) []*Node {
	for i, x := range a {
		if x == n {
			last := len(a) - 1
			a[i] = a[last]
			return a[:last]
		}
	}
	return a
}

// freeLinks is lyd_free_leafref_nodes with lyd_free_leafref_links_rec: n's record goes, n leaves
// the records of its leafref and target nodes, and a record left empty goes too. The arrays are
// walked by index with their length re-read, as LY_ARRAY_FOR does, since the recursion may
// remove from them.
func freeLinks(n *Node) {
	rec := n.links
	if rec == nil {
		return
	}
	for i := 0; i < len(rec.leafrefs); i++ {
		l := rec.leafrefs[i]
		if r2 := l.links; r2 != nil {
			r2.targets = removeValue(r2.targets, n)
			if len(r2.leafrefs) == 0 && len(r2.targets) == 0 {
				freeLinks(l)
			}
		}
	}
	rec.leafrefs = nil
	for i := 0; i < len(rec.targets); i++ {
		t := rec.targets[i]
		if r2 := t.links; r2 != nil {
			r2.leafrefs = removeValue(r2.leafrefs, n)
			if len(r2.leafrefs) == 0 && len(r2.targets) == 0 {
				freeLinks(t)
			}
		}
	}
	rec.targets = nil
	n.links = nil
}

// freeSubtreeLinks is the leafref part of lyd_free_subtree: every term node of the subtree of n
// loses its record (lyd_free_tree), where an unlink frees only n's own (lyd_unlink).
func freeSubtreeLinks(set *schema.Set, n *Node) {
	if !set.LeafrefLinking {
		return
	}
	for d := range n.All() {
		freeLinks(d)
	}
}

// linkLeafrefs is lyd_leafref_link_node_tree: every leafref value of the tree (union members
// included) is linked with the targets lyplg_type_resolve_leafref finds; a value without one is
// skipped.
func (t *Tree) linkLeafrefs(l *logger) error {
	if !t.set.LeafrefLinking {
		return errLinksDenied
	}
	vc := &valCtx{t: t, log: l}
	for top := range t.top.all() {
		for n := range top.All() {
			if !n.isTerm() {
				continue
			}
			if err := vc.linkType(n, n.value, n.schema.Type); err != nil {
				return err
			}
		}
	}
	return nil
}

// linkType is lyd_leafref_link_node_tree_type. libyang resolves every leafref member of a union
// with the value of the selected member (value.subvalue->value); compile flattens nested unions,
// which the recursion would visit in the same order.
func (vc *valCtx) linkType(n *Node, v types.Value, t *schema.Type) error {
	switch t.Base {
	case schema.Leafref:
		_, targets, err := vc.leafrefTargets(n, t, v)
		if err != nil {
			var xe *xpath.Error
			if errors.As(err, &xe) {
				_ = vc.xpathErr(err, n) // lyxp_eval logged it at cur_node; the link is skipped
				return nil
			}
			return err // budget, cancellation
		}
		for _, target := range targets {
			linkLeafrefNode(target, n)
		}
	case schema.Union:
		if u := v.Union(); u != nil {
			m, _ := u.Member()
			for _, mt := range t.Union {
				if err := vc.linkType(n, m, mt); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
