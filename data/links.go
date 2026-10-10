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

// bulkLinkCleaner is freeLinks with lazy indexes for the large counterpart arrays a batch may
// repeatedly shrink. It is used only by TrimXPath; single-node callers keep freeLinks' smaller
// linear scans. remove still swaps in the last item, exactly like removeValue.
type bulkLinkCleaner struct {
	indexes map[*leafrefLinks]*bulkLinkIndexes
	work    *workCounter
}

type bulkLinkIndexes struct {
	leafrefs bulkLinkIndex
	targets  bulkLinkIndex
}

type bulkLinkIndex struct {
	positions map[*Node]int
	hits      uint8
}

const bulkLinkIndexMin = 8

func (c *bulkLinkCleaner) visit() {
	if c.work != nil && c.work.visits { // tests only
		c.work.Add(1)
	}
}

func (c *bulkLinkCleaner) remove(rec *leafrefLinks, leafrefs bool, n *Node) {
	a := rec.targets
	if leafrefs {
		a = rec.leafrefs
	}
	var index map[*Node]int
	if x := c.indexes[rec]; x != nil {
		if leafrefs {
			index = x.leafrefs.positions
		} else {
			index = x.targets.positions
		}
	}
	if index == nil && len(a) > bulkLinkIndexMin {
		if c.indexes == nil {
			c.indexes = map[*leafrefLinks]*bulkLinkIndexes{}
		}
		x := c.indexes[rec]
		if x == nil {
			x = &bulkLinkIndexes{}
			c.indexes[rec] = x
		}
		side := &x.targets
		if leafrefs {
			side = &x.leafrefs
		}
		side.hits++
		// A first removal is commonly isolated and removeValue usually finds it near the
		// front. Build the whole-array index only when this side is hit again in the batch.
		if side.hits >= 2 {
			index = make(map[*Node]int, len(a))
			for i, x := range a {
				c.visit()
				index[x] = i
			}
			side.positions = index
		}
	}
	if index != nil {
		c.visit()
		i, ok := index[n]
		if !ok {
			return
		}
		last := len(a) - 1
		delete(index, n)
		if i != last {
			a[i] = a[last]
			index[a[i]] = i
		}
		a = a[:last]
	} else {
		for i, x := range a {
			c.visit()
			if x == n {
				last := len(a) - 1
				a[i] = a[last]
				a = a[:last]
				break
			}
		}
	}
	if leafrefs {
		rec.leafrefs = a
	} else {
		rec.targets = a
	}
}

type bulkLinkChargeKey struct {
	rec      *leafrefLinks
	leafrefs bool
}

// chargeBulkFreeLinks plans the counterpart deletions made by freeing nodes, charging each link
// once and each record-side index once. It only reads records, so TrimXPath can reject the whole
// operation before any links or tree nodes have been changed.
func chargeBulkFreeLinks(nodes []*Node, charge func(int64) bool) bool {
	order := make(map[*Node]int, len(nodes))
	for i, n := range nodes {
		order[n] = i
	}
	deletions := map[bulkLinkChargeKey]int64{}
	lengths := map[bulkLinkChargeKey]int{}
	plan := func(key bulkLinkChargeKey, length int) {
		if key.rec == nil {
			return
		}
		deletions[key]++
		lengths[key] = length
	}
	for i, n := range nodes {
		rec := n.links
		if rec == nil {
			continue
		}
		for _, l := range rec.leafrefs {
			if j, ok := order[l]; ok && j < i {
				continue // the earlier endpoint plans this link
			}
			if r2 := l.links; r2 != nil {
				plan(bulkLinkChargeKey{rec: r2}, len(r2.targets))
			}
		}
		for _, t := range rec.targets {
			if j, ok := order[t]; ok && j < i {
				continue // the earlier endpoint plans this link
			}
			if r2 := t.links; r2 != nil {
				plan(bulkLinkChargeKey{rec: r2, leafrefs: true}, len(r2.leafrefs))
			}
		}
	}
	for key, count := range deletions {
		if !charge(count) { // one planned counterpart deletion per link
			return false
		}
		// bulkLinkCleaner builds an index on the second hit only while more than the small
		// linear-scan threshold remains. Charge the original array once as a safe bound.
		if count >= 2 && lengths[key] > bulkLinkIndexMin+1 && !charge(int64(lengths[key])) {
			return false
		}
	}
	return true
}

func (c *bulkLinkCleaner) free(n *Node) {
	rec := n.links
	if rec == nil {
		return
	}
	for i := 0; i < len(rec.leafrefs); i++ {
		c.visit()
		l := rec.leafrefs[i]
		if r2 := l.links; r2 != nil {
			c.remove(r2, false, n)
			if len(r2.leafrefs) == 0 && len(r2.targets) == 0 {
				c.free(l)
			}
		}
	}
	rec.leafrefs = nil
	for i := 0; i < len(rec.targets); i++ {
		c.visit()
		t := rec.targets[i]
		if r2 := t.links; r2 != nil {
			c.remove(r2, true, n)
			if len(r2.leafrefs) == 0 && len(r2.targets) == 0 {
				c.free(t)
			}
		}
	}
	rec.targets = nil
	n.links = nil
	delete(c.indexes, rec)
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
		// targets is nil when the target was compiled away by if-feature; libyang would read it
		// unchecked here, but never gets there: Load rejects such a module ("Target of leafref
		// ... is disabled")
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
