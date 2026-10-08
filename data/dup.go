// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_dup, lyd_dup_r, lyd_dup_get_local_parent,
// lyd_dup_single, lyd_dup_single_to_ctx, lyd_dup_siblings, lyd_dup_siblings_to_ctx,
// lyd_dup_meta_single_to_ctx, lyd_find_schema_ctx, lyd_compare_schema_equal,
// lyd_compare_schema_parents_equal, lyd_compare_single_schema) (BSD-3-Clause, © CESNET).

package data

import (
	"fmt"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// dupOpts are the LYD_DUP_* options. LYD_DUP_NO_EXT and LYD_DUP_WITH_PRIV have nothing to act on
// (no extension data trees, no private pointers).
type dupOpts uint8

const (
	dupRecursive   dupOpts = 1 << iota // LYD_DUP_RECURSIVE: the whole subtree
	dupNoMeta                          // LYD_DUP_NO_META: no metadata
	dupWithParents                     // LYD_DUP_WITH_PARENTS: the parents too, up to parent or the top
	dupWithFlags                       // LYD_DUP_WITH_FLAGS: all the flags, not only the default flag (+ new)
	dupNoLyds                          // LYD_DUP_NO_LYDS: no value order for (leaf-)list instances
)

// setOf is LYD_CTX: the schema set of the tree n is in, nil for a node not in a tree.
func setOf(n *Node) *schema.Set {
	for ; n != nil; n = n.parent {
		if n.parent == nil && n.tree != nil {
			return n.tree.set
		}
	}
	return nil
}

// sameSet reports whether two contexts are the same, an unknown one (a node not in a tree, like a
// lookup probe) counting as the other's.
func sameSet(a, b *schema.Set) bool { return a == nil || b == nil || a == b }

// compareSchemaEqual is lyd_compare_schema_equal: the same schema node, or, across contexts, the
// same node type, name and module name (and so all the schema parents with cmpParents). Go schema
// nodes do not name their set, so different nodes are always compared by name: within one set
// two data nodes that agree on all of them up the parents are the same node.
func compareSchemaEqual(a, b *schema.Node, cmpParents bool) bool {
	switch {
	case a == b:
		return true
	case a == nil || b == nil:
		return false
	}
	for {
		if a.Kind != b.Kind || a.Name != b.Name || a.Module.Name != b.Module.Name {
			return false
		}
		a, b = a.Parent, b.Parent
		if !cmpParents || a == nil || b == nil {
			break
		}
	}
	return (a == nil) == (b == nil)
}

// compareSchemaParentsEqual is lyd_compare_schema_parents_equal: all the schema parents of a and
// b (nodes of different contexts) equal one by one.
func compareSchemaParentsEqual(a, b *schema.Node) bool {
	p, q := a.Parent, b.Parent
	for ; p != nil && q != nil; p, q = p.Parent, q.Parent {
		if !compareSchemaEqual(p, q, false) {
			return false
		}
	}
	return p == nil && q == nil
}

// compareSingleSchema is lyd_compare_single_schema: in one context the same schema node, with
// opaq (LYD_COMPARE_OPAQ) the same lyd_node_schema, so an opaque node matches the data node it
// stands for; across contexts an equal schema node with equal parents (unless parentsChecked).
func compareSingleSchema(a, b *Node, opaq, parentsChecked bool) bool {
	if !opaq && a.schema == b.schema {
		return true
	}
	if sa, sb := setOf(a), setOf(b); sameSet(sa, sb) {
		if sa == nil {
			sa = sb
		}
		if !opaq || sa == nil {
			return a.schema == b.schema
		}
		return nodeSchema(sa, a) == nodeSchema(sa, b)
	}
	return compareSchemaEqual(a.schema, b.schema, false) &&
		(parentsChecked || a.schema == nil || compareSchemaParentsEqual(a.schema, b.schema))
}

// findSchemaCtx is lyd_find_schema_ctx: the schema node of the set trg with the module and node
// names of sn and of its data parents (from the schema node of parent when it has one).
func findSchemaCtx(sn *schema.Node, trg *schema.Set, parent *Node) (*schema.Node, error) {
	if sn == nil {
		return nil, nil // opaque node
	}
	var srcParent, trgParent *schema.Node
	if sn.DataParent() != nil && parent != nil && parent.schema != nil {
		trgParent, srcParent = parent.schema, sn.DataParent()
	}
	var trgMod *schema.Module
	for {
		sp := sn
		for sp.DataParent() != srcParent {
			sp = sp.DataParent()
		}
		srcParent = sp
		if srcParent.DataParent() == nil {
			if trgMod = trg.Implemented(srcParent.Module.Name); trgMod == nil {
				return nil, &opError{"LY_ENOTFOUND", fmt.Sprintf("Module \"%s\" not present/implemented in the target context.",
					srcParent.Module.Name)}
			}
		}
		var top []*schema.Node
		if trgMod != nil {
			top = trgMod.Top
		}
		var tp *schema.Node
		for c := range schema.GetNext(trgParent, top, 0) {
			if c.Name == srcParent.Name && c.Module.Name == srcParent.Module.Name {
				tp = c
				break
			}
		}
		if tp == nil {
			return nil, &opError{"LY_ENOTFOUND", fmt.Sprintf("Schema node \"%s\" not found in the target context.", srcParent.LogPath())}
		}
		trgParent = tp
		if sn == srcParent {
			return trgParent, nil
		}
	}
}

// dupR is lyd_dup_r into the context of t: a copy of n (its subtree with dupRecursive, else the
// keys of a list) inserted into parent, or with top into the top level of t, by order; with
// neither it stays unlinked.
func (t *Tree) dupR(n, parent *Node, top bool, order insertOrder, opts dupOpts) (*Node, error) {
	d := &Node{flags: n.flags&FlagDefault | FlagNew}
	if opts&dupWithFlags != 0 {
		d.flags = n.flags
	}
	d.schema = n.schema
	cross := !sameSet(setOf(n), t.set)
	if cross {
		sn, err := findSchemaCtx(n.schema, t.set, parent)
		if err != nil {
			return nil, err
		}
		d.schema = sn
	}
	if opts&dupNoMeta == 0 && n.schema != nil {
		for _, m := range n.meta {
			if dm := t.dupMeta(m, d); dm != nil {
				d.meta = append(d.meta, dm)
			}
		}
	}
	switch {
	case d.schema == nil:
		o := *n.opaq
		o.Attrs = nil
		if opts&dupNoMeta == 0 { // lyd_dup_attr_single
			o.Attrs = append([]attr(nil), n.opaq.Attrs...)
		}
		d.opaq = &o
		if opts&dupRecursive != 0 {
			for _, c := range n.kids.nodes() {
				if _, err := t.dupR(c, d, false, insertLast, opts); err != nil {
					return nil, err
				}
			}
		}
	case d.schema.Kind == schema.Leaf || d.schema.Kind == schema.LeafList:
		d.value = n.value
		if cross { // the canonical value stored in the target context
			v, diag := types.StoreOnly(d.schema.Type, n.value.Canonical(), types.FormatCanon, types.HintData, nil, d.schema)
			if diag != nil {
				return nil, &opError{"LY_EVALID", diag.Msg}
			}
			d.value = v
		}
	default: // inner nodes; anydata/anyxml (no value of their own until M5, U-0043) as they are
		d.value = n.value
		for _, c := range n.kids.nodes() {
			if opts&dupRecursive == 0 && (d.schema.Kind != schema.List || d.schema.Keyless() || !c.isKey()) {
				break // always the keys of a list
			}
			if _, err := t.dupR(c, d, false, insertLast, opts); err != nil {
				return nil, err
			}
		}
		npContDfltSet(d)
	}
	if parent != nil || top {
		t.insert(parent, d, order)
	}
	return d, nil
}

// dupMeta is lyd_dup_meta_single_to_ctx: a copy of m for its duplicate parent in the context of
// t; across contexts the annotation of the module with the same name and revision, and the
// canonical value stored with its type. nil when it cannot be stored there: libyang logs the
// errors ("…value duplication failed.") and the duplicate goes on without the metadata. A
// target without the annotation crashes libyang (NULL annotation); it is dropped the same way
// (D-0067).
func (t *Tree) dupMeta(m *meta, parent *Node) *meta {
	mod := t.set.Module(m.mod.Name, m.mod.Revision)
	if mod == m.mod {
		c := *m
		return &c
	}
	ant := metaAnnotation(mod, m.name)
	if ant == nil {
		return nil // D-0067
	}
	v, d := types.StoreOnly(ant.Type, m.value.Canonical(), types.FormatCanon, types.HintData, nil, parent.schema)
	if d != nil {
		return nil
	}
	return &meta{mod: mod, name: m.name, value: v}
}

// dupLocalParent is lyd_dup_get_local_parent: the parents of n duplicated (without their
// subtrees, opts dupWithFlags and dupNoMeta) up to the one equal to parent or the top, the
// highest duplicate inserted into parent; local is the duplicate parent n's copy goes under.
func (t *Tree) dupLocalParent(n, parent *Node, opts dupOpts) (top, local *Node, err error) {
	repeat := true
	for op := n.parent; repeat && op != nil; op = op.parent {
		var it *Node
		switch {
		case parent != nil && sameSet(setOf(parent), setOf(op)) && parent.schema == op.schema,
			parent != nil && !sameSet(setOf(parent), setOf(op)) && compareSchemaEqual(parent.schema, op.schema, false) &&
				compareSchemaParentsEqual(parent.schema, op.schema):
			it, repeat = parent, false // connect what we have into parent
		default:
			if it, err = t.dupR(op, nil, false, insertDefault, opts); err != nil {
				return nil, nil, err
			}
			if top != nil {
				t.insert(it, top, insertDefault)
			}
			top = it
		}
		if local == nil {
			local = it
		}
	}
	if repeat && parent != nil {
		return nil, nil, &opError{"LY_EINVAL", fmt.Sprintf("None of the duplicated node \"%s\" schema parents match the provided parent \"%s\".",
			n.Name(), parent.Name())}
	}
	if top != nil {
		// into parent; without one the top of t holds the parents (libyang leaves them unlinked)
		t.insert(parent, top, insertDefault)
	}
	return top, local, nil
}

// dupNodes is lyd_dup into the context of t: n (and with siblings the siblings after it) copied
// under parent, or the top level of t when there is none (with dupWithParents: under the
// duplicated parents); it returns the first copy. On an error the copies made so far are removed,
// not the parent's other children as libyang frees them (D-0065).
func (t *Tree) dupNodes(n, parent *Node, opts dupOpts, siblings bool) (*Node, error) {
	local := parent
	var top *Node
	if opts&dupWithParents != 0 {
		var err error
		if top, local, err = t.dupLocalParent(n, parent, opts&(dupWithFlags|dupNoMeta)); err != nil {
			return nil, err
		}
	}
	// the siblings taken before the first copy: libyang follows orig->next and, copying into
	// their own parent, reaches its copies and never ends (D-0066)
	nodes := []*Node{n}
	if sib := n.siblingsOf(); sib != nil {
		all := sib.nodes()
		for i, c := range all {
			if c == n {
				nodes = all[i:]
				break
			}
		}
	}
	var first, firstLL *Node
	var made []*Node
	fail := func(err error) (*Node, error) {
		if top != nil {
			made = []*Node{top}
		}
		for _, d := range made {
			if d.parent != nil || d.tree != nil {
				unlink(d)
			}
		}
		return nil, err
	}
	for _, orig := range nodes {
		var d *Node
		if orig.schema != nil && orig.schema.IsKey() {
			if local != nil {
				// the key must already exist in the parent; libyang looks it up by the source
				// schema node, which another context's parent never has (LOGINT, LY_ENOTFOUND)
				for _, c := range local.kids.list {
					if c.schema == orig.schema {
						d = c
						break
					}
				}
				if d == nil {
					return fail(&opError{"LY_ENOTFOUND", "Internal error."})
				}
			} else { // a single key
				var err error
				if d, err = t.dupR(orig, nil, true, insertDefault, opts); err != nil {
					return fail(err)
				}
				made = append(made, d)
			}
		} else {
			order := insertDefault
			if opts&dupNoLyds != 0 {
				order = insertLastBySchema
			}
			switch {
			case firstLL != nil && orig.schema != firstLL.schema:
				firstLL = nil // all the (leaf-)list instances duplicated
			case firstLL != nil:
				order = insertLast // the next instances of the (leaf-)list keep their order
			case orig.schema != nil && (orig.schema.Kind == schema.List || orig.schema.Kind == schema.LeafList):
				firstLL = orig
			}
			var err error
			if d, err = t.dupR(orig, local, local == nil, order, opts); err != nil {
				return fail(err)
			}
			made = append(made, d)
			if firstLL != nil && hasNext(d) {
				firstLL = nil // inserted before other instances: their order must be found
			}
		}
		if first == nil {
			first = d
		}
		if !siblings {
			break
		}
	}
	return first, nil
}

// hasNext reports whether the linked node d has a next sibling (lyd_node.next).
func hasNext(d *Node) bool {
	sib := d.siblingsOf()
	if d.schema == nil {
		return sib.opq[len(sib.opq)-1] != d
	}
	return sib.list[len(sib.list)-1] != d || len(sib.opq) > 0
}

// dupTo is lyd_dup_single / lyd_dup_siblings (trg nil: the context of n) and their _to_ctx
// variants: the copy of n (and with siblings of its following siblings) under parent, or as
// top-level nodes of a new tree over the target set when parent is nil.
func dupTo(n *Node, trg *schema.Set, parent *Node, opts dupOpts, siblings bool) (*Node, error) {
	if setOf(n) == nil {
		return nil, &opError{"LY_EINVAL", "Duplicating a node that is not in a tree."} // libyang nodes always have a context
	}
	if trg == nil {
		if parent != nil && setOf(parent) != setOf(n) {
			return nil, &opError{"LY_EINVAL", "Different \"node\" and \"parent\" contexts used in node duplication."}
		}
		trg = setOf(n)
	} else if parent != nil && setOf(parent) != trg {
		return nil, &opError{"LY_EINVAL", "Different \"trg_ctx\" and \"parent\" contexts used in node duplication."}
	}
	t := newTree(trg)
	if parent != nil {
		root := parent
		for root.parent != nil {
			root = root.parent
		}
		if root.tree != nil {
			t = root.tree
		}
	}
	return t.dupNodes(n, parent, opts, siblings)
}
