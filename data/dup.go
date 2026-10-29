// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_dup, lyd_dup_r, lyd_dup_get_local_parent,
// lyd_dup_single, lyd_dup_single_to_ctx, lyd_dup_siblings, lyd_dup_siblings_to_ctx,
// lyd_dup_meta_single_to_ctx, lyd_dup_attr_single, lyd_find_schema_ctx, lyd_compare_schema_equal,
// lyd_compare_schema_parents_equal, lyd_compare_single_schema), src/tree_data_common.c
// (ly_dup_prefix_data) (BSD-3-Clause, © CESNET).

package data

import (
	"context"
	"fmt"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// dupAttr is lyd_dup_attr_single (tree_data.c:1921-1959): copy the XML value's
// namespace data for the target context (ly_dup_prefix_data, tree_data_common.c:1253-1308).
func dupAttr(a attr, target *schema.Set) attr {
	a.Prefixes = dupPrefixes(a.Prefixes, target)
	return a
}

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

// setOf is the schema set of the tree n is currently linked in, nil for a detached node. It is
// deliberately narrower than LYD_CTX; use ctxOf when libyang reads a node's retained context.
func setOf(n *Node) *schema.Set {
	for ; n != nil; n = n.parent {
		if n.payloadSet != nil {
			return n.payloadSet
		}
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
// same node type, name and module name (and so all the schema parents with cmpParents).
// Schema pointers retain their identity within a single compiled context. Ownerless test sets
// still retain module identity, which distinguishes their same-context nodes.
func compareSchemaEqual(a, b *schema.Node, cmpParents bool) bool {
	switch {
	case a == b:
		return true
	case a == nil || b == nil:
		return false
	}
	if a.Module == nil || b.Module == nil || a.Module == b.Module ||
		(a.Module.Owner != nil && a.Module.Owner == b.Module.Owner) {
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
	if sa, sb := ctxOf(a), ctxOf(b); sameSet(sa, sb) {
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

// dupR is lyd_dup_r: a copy of n (its subtree with dupRecursive, else the keys of a list)
// inserted into parent, or with top into the top level of t, by order; with neither it stays
// unlinked. A supplied parent determines the destination context.
func (t *Tree) dupR(n, parent *Node, top bool, order insertOrder, opts dupOpts) (*Node, error) {
	return t.dupWork(n, parent, top, order, opts, t.dupDest(parent), newAnyWork(context.Background(), Budget{}))
}

// dupRIn is dupR into the explicit destination context dest.
func (t *Tree) dupRIn(n, parent *Node, top bool, order insertOrder, opts dupOpts, dest *schema.Set) (*Node, error) {
	return t.dupWork(n, parent, top, order, opts, dest, newAnyWork(context.Background(), Budget{}))
}

// dupDest is the destination context of a duplicate: parent's (LYD_CTX, also for a
// detached parent) when supplied, otherwise the tree's.
func (t *Tree) dupDest(parent *Node) *schema.Set {
	if parent != nil && ctxOf(parent) != nil {
		return ctxOf(parent)
	}
	return t.context()
}

// dupFrame records a post-order insertion. Payload boundaries reset the options
// and target context just as lyd_any_copy_value's lyd_dup_siblings call does.
type dupFrame struct {
	tree             *Tree
	src, dst, parent *Node
	sourceSet        *schema.Set
	dest             *schema.Set // the target context of this copy
	top              bool
	order            insertOrder
	opts             dupOpts
	payload          bool // parent is a newly copied any-value forest, not the caller's parent
}

// dupWork replaces lyd_dup_r's recursion with an explicit stack, including across
// repeated schema roots in any payloads. Nothing is linked to the caller's tree
// until the entire requested subtree has been copied successfully.
func (t *Tree) dupWork(n, parent *Node, top bool, order insertOrder, opts dupOpts, dest *schema.Set, w *anyWork) (*Node, error) {
	stack := []dupFrame{{tree: t, src: n, parent: parent, sourceSet: ctxOf(n), dest: dest, top: top, order: order, opts: opts}}
	var root *Node
	// one tree per payload context: its sibling sets share one work counter, so
	// linking an outer node does not rebind the payloads below it again
	payloadTrees := map[*schema.Set]*Tree{}
	for len(stack) != 0 {
		if err := poll(w.ctx); err != nil {
			return nil, err
		}
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if f.dst != nil {
			if err := f.tree.dupLink(f); err != nil {
				return nil, err
			}
			if err := t.absorbHashWork(f.tree); err != nil {
				return nil, err
			}
			continue
		}
		d, err := f.tree.dupSingle(f.src, f.parent, f.sourceSet, f.dest, f.opts)
		if err != nil {
			return nil, err
		}
		if root == nil {
			root = d
		}
		f.dst = d
		stack = append(stack, f)
		children, childTree, childOpts, sourceSet, childDest := f.src.kids.nodes(), f.tree, f.opts, f.sourceSet, f.dest
		if isAny(d.schema) {
			d.any = &anyValue{}
			// lyd_dup_r resolves the schema and duplicates metadata before
			// lyd_any_copy_value checks its inputs (tree_data.c:2235-2324).
			if f.src.any != nil {
				if err := guardAnyCopyInput(f.src.any.children.first(), f.src.any.text); err != nil {
					return nil, err
				}
			}
			children = nil
			if av := f.src.any; av != nil {
				d.any.hints = av.hints
				children = av.children.nodes()
				if av.text != nil {
					s, _, _ := strings.Cut(*av.text, "\x00")
					d.any.text = &s
					if d.schema.Kind == schema.AnyData || av.hints&0x0fff == 0 {
						// lyd_any_copy_value passes log=0 for both any kinds
						// (tree_data_new.c:344): copy-time warnings stay silent.
						parsed, _, err := w.createAnyDatatree(f.dest, s)
						if anyParseFatal(err) {
							return nil, err
						}
						if err == nil && parsed.top.len() != 0 {
							// lyd_any_copy_value adopts freshly parsed nodes; copying
							// them again would change their flags to Default|New.
							if err := d.takeAnyChildren(f.tree, parsed.top.first()); err != nil {
								return nil, err
							}
							children, d.any.text = nil, nil
						}
					}
				}
			}
			childOpts = dupRecursive
			if len(children) != 0 {
				sourceSet = ctxOf(children[0])
				if sourceSet == nil {
					sourceSet = f.sourceSet
				}
				if childTree = payloadTrees[sourceSet]; childTree == nil {
					childTree = newTree(sourceSet)
					payloadTrees[sourceSet] = childTree
				}
				childDest = sourceSet
			}
		} else if f.opts&dupRecursive == 0 {
			keys := 0
			if d.schema != nil && d.schema.Kind == schema.List && !d.schema.Keyless() {
				for keys < len(children) && children[keys].isKey() {
					keys++
				}
			}
			children = children[:keys]
		}
		for i := len(children) - 1; i >= 0; i-- {
			// lyd_dup_r converts each child from LYD_CTX(child) (tree_data.c:2232-2235):
			// a node inserted from another context keeps its own.
			cs := ctxOf(children[i])
			if cs == nil {
				cs = sourceSet
			}
			stack = append(stack, dupFrame{tree: childTree, src: children[i], parent: d,
				sourceSet: cs, dest: childDest, order: insertLast, opts: childOpts, payload: isAny(d.schema)})
		}
	}
	return root, nil
}

// dupLink completes f.dst after its children were copied: lyd_dup_r's final hash
// (tree_data.c:2317: final leading keys, not the source hash), then the link.
func (t *Tree) dupLink(f dupFrame) error {
	d := f.dst
	if d.schema != nil && !d.isTerm() {
		if err := t.work.checkRefreshHash(d); err != nil {
			return err
		}
		refreshInstanceHash(&t.work, d)
		if isInner(d.schema) {
			npContDfltSet(d)
		}
	}
	if f.parent == nil && !f.top {
		return nil
	}
	// lyd_any_copy_value duplicates the forest before assigning its parent
	// pointers, so linking a payload must not clear the any node's own Default
	// flag through npContDfltDel.
	var flags Flags
	if f.payload && f.parent != nil {
		flags = f.parent.flags
	}
	var err error
	if f.order == insertLast && d.schema != nil {
		// lyd_dup_r uses LYD_INSERT_NODE_LAST for copied children, including
		// across contexts whose key/schema order differs. Bypass insertPos's
		// schema-order constraint (D-0058 applies to ordered parsing).
		sib := t.childrenOf(f.parent)
		// markUnsorted is visible: reserve the whole link before it.
		amount, boundErr := t.linkBound(f.parent, sib, d, sib.SchemaLen())
		if boundErr != nil {
			return boundErr
		}
		if err := t.reserveHash(amount); err != nil {
			return err
		}
		if sortedSupported(d) && sib.SchemaLen() > 0 &&
			sib.At(sib.SchemaLen()-1).schema == d.schema && sib.At(sib.SchemaLen()-1).inRB {
			leader := sib.At(sib.SchemaLen() - 1)
			for i := sib.SchemaLen() - 2; i >= 0 && sib.At(i).schema == d.schema; i-- {
				t.work.Add(1)
				leader = sib.At(i)
			}
			markUnsorted(sib, leader)
		}
		err = t.link(f.parent, sib, d, sib.SchemaLen())
	} else {
		err = t.insert(f.parent, d, f.order)
	}
	if f.payload && f.parent != nil {
		f.parent.flags = flags
	}
	return err
}

// absorbHashWork charges the hash work done in c, a payload copy's own-context
// tree, to t's operation quota.
// ponytail: c has its own quota, so one payload node may overshoot t's by at most
// that; share one counter across contexts if that ever matters.
func (t *Tree) absorbHashWork(c *Tree) error {
	if c == t {
		return nil
	}
	if c.work.hashErr != nil {
		t.work.hashErr = c.work.hashErr
		return c.work.hashErr
	}
	used := c.work.hashUsed
	c.work.hashUsed = 0
	return t.work.chargeHash(used)
}

// dupSingle copies scalar state; children are scheduled by dupWork. Schema and
// typed value objects are immutable, while opaque namespace maps/attributes are not.
func (t *Tree) dupSingle(n, parent *Node, sourceSet, dest *schema.Set, opts dupOpts) (*Node, error) {
	d := &Node{schema: n.schema, flags: n.flags&FlagDefault | FlagNew, payloadSet: dest}
	if opts&dupWithFlags != 0 {
		d.flags = n.flags
	}
	cross := !sameSet(sourceSet, dest)
	if cross {
		sn, err := findSchemaCtx(n.schema, dest, parent)
		if err != nil {
			return nil, err
		}
		d.schema = sn
	}
	if opts&dupNoMeta == 0 && n.schema != nil {
		// lyplg_type_dupl_lyds copies every metadata slot with a NULL root.
		// It never copies the source's populated topology.
		slots := len(n.carriedRB)
		if s := n.siblingsOf(); s != nil && s.rbGroups[n] != nil {
			slots += 1 + len(s.rbMore[n])
		}
		if n.detachedRB != nil {
			slots++
		}
		if err := t.work.chargeHash(3 * uint64(slots)); err != nil {
			return nil, err
		}
		for range slots {
			d.carriedRB = append(d.carriedRB, rbCarry{&rbGroup{}, &dirtyOrder{}})
		}
		for _, m := range n.meta {
			if dm := t.dupMeta(m, d, dest); dm != nil {
				d.meta = append(d.meta, dm)
			}
		}
	}
	switch {
	case d.schema == nil:
		o := *n.opaq
		o.set = dest // the target context
		o.Prefixes = dupPrefixes(o.Prefixes, dest)
		o.Attrs = nil
		if opts&dupNoMeta == 0 { // lyd_dup_attr_single
			for _, a := range n.opaq.Attrs {
				o.Attrs = append(o.Attrs, dupAttr(a, dest))
			}
		}
		d.opaq = &o
	case d.isTerm():
		// tree_data.c:2280-2295 retains the existing hash, even across types and
		// repeated duplicates. Only a later native value change replaces it.
		d.hash = n.hash
		d.value = n.value
		if cross {
			v, diag := types.StoreOnly(d.schema.Type, n.value.Canonical(), types.FormatCanon, types.HintData,
				types.ModuleNames{Set: dest}, d.schema)
			if diag != nil {
				if t.dupLog == nil {
					return nil, &opError{"LY_EVALID", diag.Msg}
				}
				return nil, t.dupLog.storeErr(d, d.schema, diag)
			}
			d.value = v
		}
	default:
		d.value = n.value
		if opts&dupRecursive != 0 && n.kids.ht != nil && !isAny(d.schema) {
			d.kids.ht = map[idxKey]*recordChain{}
		}
	}
	d.kids.work = kidsWork(d, &t.work) // copied children are linked with this same counter
	return d, nil
}

// dupMeta is lyd_dup_meta_single_to_ctx: a copy of m for its duplicate parent in dest; across
// contexts the annotation of the module with the same name and revision, and the
// canonical value stored with its type. nil when it cannot be stored there: libyang logs the
// errors ("…value duplication failed.") and the duplicate goes on without the metadata. A
// target without the annotation crashes libyang (NULL annotation); it is dropped the same way
// (D-0067).
func (t *Tree) dupMeta(m *meta, parent *Node, dest *schema.Set) *meta {
	mod := dest.Module(m.mod.Name, m.mod.Revision)
	if mod == m.mod {
		c := *m
		return &c
	}
	ant := annotation(mod, m.name)
	if ant == nil {
		return nil // D-0067
	}
	v, d := types.StoreOnly(ant.Type, m.value.Canonical(), types.FormatCanon, types.HintData,
		types.ModuleNames{Set: dest}, parent.schema)
	if d != nil {
		if t.dupLog != nil { // lyd_value_store's items at the duplicate, then LOGERR; the copy goes on
			_ = t.dupLog.storeErr(parent, parent.schema, d)
			_ = t.dupLog.logErr("LY_EINT", "Value duplication failed.")
		}
		return nil
	}
	return &meta{mod: mod, name: m.name, value: v}
}

// dupLocalParent is lyd_dup_get_local_parent: the parents of n duplicated (without their
// subtrees, opts dupWithFlags and dupNoMeta) up to the one equal to parent or the top, the
// highest duplicate inserted into parent; local is the duplicate parent n's copy goes under.
func (t *Tree) dupLocalParent(n, parent *Node, opts dupOpts, dest *schema.Set, w *anyWork) (top, local *Node, err error) {
	repeat := true
	for op := n.parent; repeat && op != nil; op = op.parent {
		var it *Node
		switch {
		case parent != nil && sameSet(ctxOf(parent), ctxOf(op)) && parent.schema == op.schema,
			parent != nil && !sameSet(ctxOf(parent), ctxOf(op)) && compareSchemaEqual(parent.schema, op.schema, false) &&
				compareSchemaParentsEqual(parent.schema, op.schema):
			it, repeat = parent, false // connect what we have into parent
		default:
			if it, err = t.dupWork(op, nil, false, insertDefault, opts, dest, w); err != nil {
				return nil, nil, err
			}
			if top != nil {
				if err := t.insert(it, top, insertDefault); err != nil {
					return nil, nil, err
				}
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
		if err := t.insert(parent, top, insertDefault); err != nil {
			return nil, nil, err
		}
	}
	return top, local, nil
}

// dupNodes is lyd_dup into the context of t: n (and with siblings the siblings after it) copied
// under parent, or the top level of t when there is none (with dupWithParents: under the
// duplicated parents); it returns the first copy. On an error the copies made so far are removed,
// not the parent's other children as libyang frees them (D-0065).
func (t *Tree) dupNodes(n, parent *Node, opts dupOpts, siblings bool) (*Node, error) {
	w := newAnyWork(context.Background(), Budget{})
	dest := t.dupDest(parent)
	local := parent
	var top *Node
	if opts&dupWithParents != 0 {
		var err error
		if top, local, err = t.dupLocalParent(n, parent, opts&(dupWithFlags|dupNoMeta), dest, w); err != nil {
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
				if uerr := unlink(d); uerr != nil {
					return nil, uerr // the sticky budget/journal failure outranks err
				}
			}
		}
		return nil, err
	}
	for _, orig := range nodes {
		var d *Node
		if orig.schema != nil && orig.schema.IsKey() {
			if local != nil {
				// the key must already exist in the parent; lyd_find_sibling_schema looks it up by
				// the source schema node, matching names in another context
				d = t.schemaLookup(&local.kids, orig.schema)
				if d == nil { // LY_CHECK_ERR_GOTO(rc, LOGINT(trg_ctx), error), tree_data.c:2453
					if t.dupLog != nil {
						_ = t.dupLog.logErr("LY_EINT", "Internal error (%s:%d).", "tree_data.c", 2453)
					}
					return fail(rcError("LY_ENOTFOUND"))
				}
			} else { // a single key
				var err error
				if d, err = t.dupWork(orig, nil, true, insertDefault, opts, dest, w); err != nil {
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
			if d, err = t.dupWork(orig, local, local == nil, order, opts, dest, w); err != nil {
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
	if sib == nil {
		return false
	}
	if d.schema == nil {
		return sib.opaqueAt(sib.OpaqueLen()-1) != d
	}
	return sib.At(sib.SchemaLen()-1) != d || sib.OpaqueLen() > 0
}

// dupTo is lyd_dup_single / lyd_dup_siblings (trg nil: the context of n) and their _to_ctx
// variants: the copy of n (and with siblings of its following siblings) under parent, or as
// top-level nodes of a new tree over the target set when parent is nil.
func dupTo(n *Node, trg *schema.Set, parent *Node, opts dupOpts, siblings bool) (*Node, error) {
	src := ctxOf(n)
	if src == nil {
		return nil, &opError{"LY_EINVAL", "Duplicating a node that is not in a tree."}
	}
	if trg == nil {
		if parent != nil && ctxOf(parent) != src {
			return nil, &opError{"LY_EINVAL", "Different \"node\" and \"parent\" contexts used in node duplication."}
		}
		trg = src
	} else if parent != nil && ctxOf(parent) != trg {
		return nil, &opError{"LY_EINVAL", "Different \"trg_ctx\" and \"parent\" contexts used in node duplication."}
	}
	t := newTree(trg)
	if parent != nil {
		root := parent
		for root.parent != nil {
			root = root.parent
		}
		if root.tree != nil && root.tree.set == trg {
			t = root.tree
		}
	}
	return t.dupNodes(n, parent, opts, siblings)
}

// DupOptions are the LYD_DUP_* options of Dup and DupSiblings.
type DupOptions struct {
	Recursive   bool // LYD_DUP_RECURSIVE: the whole subtree, not only the node (and a list's keys)
	NoMeta      bool // LYD_DUP_NO_META: without metadata
	WithParents bool // LYD_DUP_WITH_PARENTS: the node's parents too, up to parent or the top level
	WithFlags   bool // LYD_DUP_WITH_FLAGS: all the flags, not only Default (copies are New)
	// NoSort is LYD_DUP_NO_LYDS: copies of (leaf-)list instances are inserted after the instances
	// of their schema node (LYD_INSERT_NODE_LAST_BY_SCHEMA, then LAST for the following instances
	// of a run) instead of at their value-ordered position, so a copy into a populated run leaves
	// it unsorted. Later ordinary insertions rebuild the order index of the run. Source
	// compatibility: non-empty unkeyed DupOptions literals must become keyed.
	NoSort bool
}

func (o DupOptions) opts() dupOpts {
	var d dupOpts
	for _, f := range []struct {
		on bool
		o  dupOpts
	}{{o.Recursive, dupRecursive}, {o.NoMeta, dupNoMeta}, {o.WithParents, dupWithParents}, {o.WithFlags, dupWithFlags}, {o.NoSort, dupNoLyds}} {
		if f.on {
			d |= f.o
		}
	}
	return d
}

// Dup is lyd_dup_single: a copy of n, from any tree or retained after Unlink, under parent (a
// node of t or a detached node of its snapshot) or, with parent nil, as a top-level node of t
// (a fresh NewTree gives libyang's unlinked copy); with
// o.WithParents the copy of n under copies of its parents. When n's snapshot differs from the
// destination (parent's snapshot when supplied, otherwise t's), it is lyd_dup_single_to_ctx,
// the way to move data onto a newer snapshot: schema nodes are found by
// path and values stored again. The diagnostics are those logged also on success (a metadata
// value the target rejects: dropped, "Value duplication failed."); a terminal parent
// is refused (D-0123; opaque and any parents remain accepted), and so is a
// ordinary schema-backed parent that is not an instance of n's schema parent (D-0124), except for a list
// key, which libyang looks up in the parent instead (LY_ENOTFOUND there). A failure is a
// *ValidationError or a resource error and changes nothing. Anydata/anyxml payloads
// are always copied in full, retaining their source schema contexts; their copies
// keep metadata and only the Default flag (plus New), independently of o.
func (t *Tree) Dup(n, parent *Node, o DupOptions) (_ *Node, d []yang.Diagnostic, err error) {
	defer normalizeRefusal(t.context(), &err, &d) // the guard's refusal in its one public form, after the journal ended
	if t.work.inProgress() || heldNode(parent) {  // t, or a node argument, is held by an operation waiting on user code: refused before any counter is used
		d, err := refusedEntryDiags(t.context())
		return nil, d, err
	}
	return t.dupPublic(n, parent, o, false, "lyd_dup_single")
}

// DupSiblings is lyd_dup_siblings: as Dup, n and the siblings that follow it (not those before).
// Like Dup, it refuses a terminal parent (D-0123) or an ordinary schema parent that is not an
// instance of n's schema parent (D-0124).
func (t *Tree) DupSiblings(n, parent *Node, o DupOptions) (_ *Node, d []yang.Diagnostic, err error) {
	defer normalizeRefusal(t.context(), &err, &d) // the guard's refusal in its one public form, after the journal ended
	if t.work.inProgress() || heldNode(parent) {  // t, or a node argument, is held by an operation waiting on user code: refused before any counter is used
		d, err := refusedEntryDiags(t.context())
		return nil, d, err
	}
	return t.dupPublic(n, parent, o, true, "lyd_dup_siblings")
}

// isSchemaParent reports whether parent is an instance of n's schema parent (lysc_data_parent),
// n's schema node taken in dest as lyd_dup_single_to_ctx finds it; a node the target
// context lacks is left to the duplication's own error.
func (t *Tree) isSchemaParent(n, parent *Node, dest *schema.Set) bool {
	sn := n.schema
	if !sameSet(ctxOf(n), dest) {
		var err error
		if sn, err = findSchemaCtx(sn, dest, parent); err != nil {
			return true
		}
	}
	return sn.DataParent() == parent.schema
}

func (t *Tree) dupPublic(n, parent *Node, o DupOptions, siblings bool, fn string) (*Node, []yang.Diagnostic, error) {
	defer t.work.beginHashWork()()
	dest := t.dupDest(parent)
	lg := &logger{set: dest}
	switch {
	case n == nil:
		return nil, nil, lg.done(argErr("node", fn))
	case ctxOf(n) == nil:
		return nil, nil, lg.done(&opError{"LY_EINVAL", "Duplicating a node that is not in a tree."})
	case parent != nil && treeOf(parent) != nil && treeOf(parent) != t:
		return nil, nil, lg.done(argErr("parent (not in the tree)", fn))
	case parent != nil && ctxOf(parent) != ctxOf(n) && ctxOf(parent) != t.set:
		// A source in parent's context is lyd_dup_single, which compares only
		// those two (tree_data.c:2513); otherwise lyd_dup_single_to_ctx into
		// t's context requires parent there too (tree_data.c:2526).
		return nil, nil, lg.done(&opError{"LY_EINVAL", "Different \"trg_ctx\" and \"parent\" contexts used in node duplication."})
	case parent != nil && parent.schema != nil && !isInner(parent.schema) && !isAny(parent.schema) && !o.WithParents &&
		(n.schema == nil || !n.schema.IsKey()):
		// lyd_dup_get_local_parent rejects this parent with LYD_DUP_WITH_PARENTS. Without
		// that option libyang reaches lyd_dup_r and inserts under a term node, corrupting
		// the tree and crashing when the result is used (D-0123). A key is never inserted:
		// lyd_dup looks it up among lyd_child(parent), NULL for a term node, and fails with
		// LOGINT and LY_ENOTFOUND (tree_data.c:2449-2453), as dupNodes does. Duplicating
		// siblings from a key stops at that key, so no later sibling reaches lyd_dup_r.
		return nil, nil, lg.done(argErr("parent (not an inner node)", fn))
	case parent != nil && parent.schema != nil && !isAny(parent.schema) && n.schema != nil && !o.WithParents && !n.schema.IsKey() &&
		!t.isSchemaParent(n, parent, dest):
		// lyd_dup does not check that parent is an instance of n's schema parent either; it links
		// the copy where lyd_insert_get_next_anchor finds an anchor among unrelated schema
		// siblings, a tree whose later ordering and lookups depend on hash-table and rank
		// internals. Refused before changing the tree (D-0124). A key keeps libyang's lookup.
		return nil, nil, lg.done(&opError{"LY_EINVAL", fmt.Sprintf(
			"Duplicating \"%s\" under \"%s\", which is not an instance of its schema parent (D-0124).", n.Path(), parent.Path())})
	}
	// Journal before the preflights: a destination switching lookup mode (markMixed) changes in
	// insertPositionBound already. A refused preflight restores it (rollback).
	journal := beginHashTransaction(t)
	err := journal.captureDetachedParent(parent)
	if err == nil {
		err = t.preflightDupHash(n, parent, o, siblings)
	}
	if err == nil && parent != nil {
		err = t.preflightInsertPosition(t.childrenOf(parent), n, insertDefault)
	}
	if err != nil {
		if journal != nil {
			journal.rollback = true
		}
		journal.end(&err)
		return nil, nil, err
	}
	t.dupLog = lg
	d, err := t.dupNodes(n, parent, o.opts(), siblings)
	t.dupLog = nil
	if t.work.hashErr != nil {
		err = t.work.hashErr
	}
	err = lg.done(err)
	journal.end(&err)
	if err != nil {
		return nil, nil, err
	}
	return d, lg.diags, nil
}

// preflightDupHash reserves the copied sibling batch, including descendants
// whose list keys are rehashed and leaf-list values whose indexed canonical
// digest is recomputed. It runs before WithParents can link an ancestor.
func (t *Tree) preflightDupHash(n, parent *Node, o DupOptions, siblings bool) error {
	orig := []*Node{n}
	if siblings {
		if sib := n.siblingsOf(); sib != nil {
			all := sib.nodes()
			for i, c := range all {
				if c == n {
					orig = all[i:]
					break
				}
			}
		}
	}
	var amount uint64
	rootIndexed := false
	if parent != nil && parent.schema != nil {
		sib := t.childrenOf(parent)
		rootIndexed = sib.ht != nil || sib.SchemaLen()+len(orig) >= htMinItems
	}
	visit := func(child *Node, indexed bool) error {
		if indexed {
			amount = addHashBound(amount, indexWorkBound(child, nil, nil, &t.work))
		}
		if child.isKey() {
			amount = addHashBound(amount, types.HashLYBWorkBound(child.value))
		}
		if child.schema != nil && child.schema.Kind == schema.List && !child.schema.Keyless() {
			// lyd_dup_r hashes the completed list once more after copying its
			// children, independently of each key-link hash event.
			for _, key := range child.kids.SchemaAll() {
				if !key.isKey() {
					break
				}
				amount = addHashBound(amount, types.HashLYBWorkBound(key.value))
			}
		}
		return t.work.hashErr
	}
	for _, root := range orig {
		if o.Recursive {
			for child := range root.All() {
				indexed := rootIndexed
				if child != root {
					p := child.parent
					indexed = p != nil && p.schema != nil && (p.kids.ht != nil || p.kids.SchemaLen() >= htMinItems)
				}
				if err := visit(child, indexed); err != nil {
					return err
				}
			}
		} else {
			if err := visit(root, rootIndexed); err != nil {
				return err
			}
			if root.schema != nil && root.schema.Kind == schema.List {
				indexed := root.kids.ht != nil || len(root.schema.Keys) >= htMinItems
				for _, child := range root.kids.SchemaAll() {
					if !child.isKey() {
						break
					}
					if err := visit(child, indexed); err != nil {
						return err
					}
				}
			}
		}
	}
	if o.WithParents {
		for ancestor := n.parent; ancestor != nil; ancestor = ancestor.parent {
			if parent != nil && ancestor.schema == parent.schema && sameSet(setOf(ancestor), t.set) {
				break
			}
			if err := visit(ancestor, true); err != nil {
				return err
			}
			if ancestor.schema != nil && ancestor.schema.Kind == schema.List {
				for _, key := range ancestor.kids.SchemaAll() {
					if !key.isKey() {
						break
					}
					if err := visit(key, true); err != nil {
						return err
					}
				}
			}
		}
	}
	if parent != nil {
		sib := t.childrenOf(parent)
		if sib.ht == nil && sib.SchemaLen()+len(orig) >= htMinItems {
			for _, old := range sib.SchemaAll() {
				amount = addHashBound(amount, indexWorkBound(old, nil, nil, &t.work))
			}
		}
		if parent.schema != nil && parent.schema.Kind == schema.List {
			if ps := parent.siblingsOf(); ps != nil && ps.ht != nil && parent.keyN < len(parent.schema.Keys) {
				amount = addHashBound(amount, indexBatchInsertedBound(parent, orig, &t.work))
			}
		}
	}
	if t.work.hashErr != nil {
		return t.work.hashErr
	}
	if err := t.work.checkHashWork(amount); err != nil {
		t.work.hashErr = err
		return err
	}
	return nil
}

// kidsWork is the counter a copy's kids start with: an any node's live child storage is its
// payload, bound when the payload is adopted, so its unused kids keep no counter that would
// retain the copying tree.
func kidsWork(d *Node, w *workCounter) *workCounter {
	if d.schema != nil && isAny(d.schema) {
		return nil
	}
	return w
}
