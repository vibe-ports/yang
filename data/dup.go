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

// setOf is LYD_CTX: the schema set of the tree n is in, nil for a node not in a tree.
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
	return t.dupWork(n, parent, top, order, opts, newAnyWork(context.Background(), Budget{}))
}

// dupFrame records a post-order insertion. Payload boundaries reset the options
// and target context just as lyd_any_copy_value's lyd_dup_siblings call does.
type dupFrame struct {
	tree             *Tree
	src, dst, parent *Node
	sourceSet        *schema.Set
	top              bool
	order            insertOrder
	opts             dupOpts
	payload          bool // parent is a newly copied any-value forest, not the caller's parent
}

// dupWork replaces lyd_dup_r's recursion with an explicit stack, including across
// repeated schema roots in any payloads. Nothing is linked to the caller's tree
// until the entire requested subtree has been copied successfully.
func (t *Tree) dupWork(n, parent *Node, top bool, order insertOrder, opts dupOpts, w *anyWork) (*Node, error) {
	stack := []dupFrame{{tree: t, src: n, parent: parent, sourceSet: setOf(n), top: top, order: order, opts: opts}}
	var root *Node
	for len(stack) != 0 {
		if err := w.ctx.Err(); err != nil {
			return nil, err
		}
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if f.dst != nil {
			if f.dst.schema != nil && isInner(f.dst.schema) {
				npContDfltSet(f.dst)
			}
			if f.parent != nil || f.top {
				// lyd_any_copy_value duplicates the forest before assigning its
				// parent pointers, so linking a payload must not clear the any
				// node's own Default flag through npContDfltDel.
				var flags Flags
				if f.payload && f.parent != nil {
					flags = f.parent.flags
				}
				f.tree.insert(f.parent, f.dst, f.order)
				if f.payload && f.parent != nil {
					f.parent.flags = flags
				}
			}
			continue
		}
		d, err := f.tree.dupSingle(f.src, f.parent, f.sourceSet, f.opts)
		if err != nil {
			return nil, err
		}
		if root == nil {
			root = d
		}
		f.dst = d
		stack = append(stack, f)
		children, childTree, childOpts, sourceSet := f.src.kids.nodes(), f.tree, f.opts, f.sourceSet
		if isAny(d.schema) {
			d.any = &anyValue{}
			// lyd_dup_r resolves the schema and duplicates metadata before
			// lyd_any_copy_value checks its inputs (tree_data.c:2235-2324).
			if f.src.any != nil {
				if err := guardAnyCopyInput(f.src.any.children.first(), f.src.any.text,
					"trg/child/value", "lyd_any_copy_value"); err != nil {
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
						parsed, _, err := w.createAnyDatatree(f.tree.set, s)
						if anyParseFatal(err) {
							return nil, err
						}
						if err == nil && parsed.top.len() != 0 {
							// lyd_any_copy_value adopts freshly parsed nodes; copying
							// them again would change their flags to Default|New.
							d.takeAnyChildren(f.tree, parsed.top.first())
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
				childTree = newTree(sourceSet)
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
			cs := sourceSet
			if children[i].payloadSet != nil {
				cs = children[i].payloadSet
			}
			stack = append(stack, dupFrame{tree: childTree, src: children[i], parent: d,
				sourceSet: cs, order: insertLast, opts: childOpts, payload: isAny(d.schema)})
		}
	}
	return root, nil
}

// dupSingle copies scalar state; children are scheduled by dupWork. Schema and
// typed value objects are immutable, while opaque namespace maps/attributes are not.
func (t *Tree) dupSingle(n, parent *Node, sourceSet *schema.Set, opts dupOpts) (*Node, error) {
	d := &Node{schema: n.schema, flags: n.flags&FlagDefault | FlagNew, payloadSet: t.set}
	if opts&dupWithFlags != 0 {
		d.flags = n.flags
	}
	cross := !sameSet(sourceSet, t.set)
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
		o.set = t.set // the target context
		o.Prefixes = dupPrefixes(o.Prefixes, t.set)
		o.Attrs = nil
		if opts&dupNoMeta == 0 { // lyd_dup_attr_single
			for _, a := range n.opaq.Attrs {
				o.Attrs = append(o.Attrs, dupAttr(a, t.set))
			}
		}
		d.opaq = &o
	case d.isTerm():
		d.value = n.value
		if cross {
			v, diag := types.StoreOnly(d.schema.Type, n.value.Canonical(), types.FormatCanon, types.HintData,
				types.ModuleNames{Set: t.set}, d.schema)
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
	ant := annotation(mod, m.name)
	if ant == nil {
		return nil // D-0067
	}
	v, d := types.StoreOnly(ant.Type, m.value.Canonical(), types.FormatCanon, types.HintData,
		types.ModuleNames{Set: t.set}, parent.schema)
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
func (t *Tree) dupLocalParent(n, parent *Node, opts dupOpts, w *anyWork) (top, local *Node, err error) {
	repeat := true
	for op := n.parent; repeat && op != nil; op = op.parent {
		var it *Node
		switch {
		case parent != nil && sameSet(setOf(parent), setOf(op)) && parent.schema == op.schema,
			parent != nil && !sameSet(setOf(parent), setOf(op)) && compareSchemaEqual(parent.schema, op.schema, false) &&
				compareSchemaParentsEqual(parent.schema, op.schema):
			it, repeat = parent, false // connect what we have into parent
		default:
			if it, err = t.dupWork(op, nil, false, insertDefault, opts, w); err != nil {
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
	w := newAnyWork(context.Background(), Budget{})
	local := parent
	var top *Node
	if opts&dupWithParents != 0 {
		var err error
		if top, local, err = t.dupLocalParent(n, parent, opts&(dupWithFlags|dupNoMeta), w); err != nil {
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
				if d == nil { // LY_CHECK_ERR_GOTO(rc, LOGINT(trg_ctx), error), tree_data.c:2453
					if t.dupLog != nil {
						_ = t.dupLog.logErr("LY_EINT", "Internal error (%s:%d).", "tree_data.c", 2453)
					}
					return fail(rcError("LY_ENOTFOUND"))
				}
			} else { // a single key
				var err error
				if d, err = t.dupWork(orig, nil, true, insertDefault, opts, w); err != nil {
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
			if d, err = t.dupWork(orig, local, local == nil, order, opts, w); err != nil {
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
}

func (o DupOptions) opts() dupOpts {
	var d dupOpts
	for _, f := range []struct {
		on bool
		o  dupOpts
	}{{o.Recursive, dupRecursive}, {o.NoMeta, dupNoMeta}, {o.WithParents, dupWithParents}, {o.WithFlags, dupWithFlags}} {
		if f.on {
			d |= f.o
		}
	}
	return d
}

// Dup is lyd_dup_single: a copy of n, from any tree, under parent (a node of t) or, with parent
// nil, as a top-level node of t (a fresh NewTree gives libyang's unlinked copy); with
// o.WithParents the copy of n under copies of its parents. When n's snapshot is not t's it is
// lyd_dup_single_to_ctx, the way to move data onto a newer snapshot: schema nodes are found by
// path and values stored again. The diagnostics are those logged also on success (a metadata
// value the target rejects: dropped, "Value duplication failed."); a terminal parent
// is refused (D-0123; opaque and any parents remain accepted), and so is a
// ordinary schema-backed parent that is not an instance of n's schema parent (D-0124), except for a list
// key, which libyang looks up in the parent instead (LY_ENOTFOUND there). A failure is a
// *ValidationError or a resource error and changes nothing. Anydata/anyxml payloads
// are always copied in full, retaining their source schema contexts; their copies
// keep metadata and only the Default flag (plus New), independently of o.
func (t *Tree) Dup(n, parent *Node, o DupOptions) (*Node, []yang.Diagnostic, error) {
	return t.dupPublic(n, parent, o, false, "lyd_dup_single")
}

// DupSiblings is lyd_dup_siblings: as Dup, n and the siblings that follow it (not those before).
// Like Dup, it refuses a terminal parent (D-0123) or an ordinary schema parent that is not an
// instance of n's schema parent (D-0124).
func (t *Tree) DupSiblings(n, parent *Node, o DupOptions) (*Node, []yang.Diagnostic, error) {
	return t.dupPublic(n, parent, o, true, "lyd_dup_siblings")
}

// isSchemaParent reports whether parent is an instance of n's schema parent (lysc_data_parent),
// n's schema node taken in t's context as lyd_dup_single_to_ctx finds it; a node the target
// context lacks is left to the duplication's own error.
func (t *Tree) isSchemaParent(n, parent *Node) bool {
	sn := n.schema
	if !sameSet(setOf(n), t.set) {
		var err error
		if sn, err = findSchemaCtx(sn, t.set, parent); err != nil {
			return true
		}
	}
	return sn.DataParent() == parent.schema
}

func (t *Tree) dupPublic(n, parent *Node, o DupOptions, siblings bool, fn string) (*Node, []yang.Diagnostic, error) {
	lg := &logger{set: t.set}
	switch {
	case n == nil:
		return nil, nil, lg.done(argErr("node", fn))
	case setOf(n) == nil:
		return nil, nil, lg.done(&opError{"LY_EINVAL", "Duplicating a node that is not in a tree."})
	case parent != nil && treeOf(parent) != t:
		return nil, nil, lg.done(argErr("parent (not in the tree)", fn))
	case parent != nil && ctxOf(parent) != t.set:
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
		!t.isSchemaParent(n, parent):
		// lyd_dup does not check that parent is an instance of n's schema parent either; it links
		// the copy where lyd_insert_get_next_anchor finds an anchor among unrelated schema
		// siblings, a tree whose later ordering and lookups depend on hash-table and rank
		// internals. Refused before changing the tree (D-0124). A key keeps libyang's lookup.
		return nil, nil, lg.done(&opError{"LY_EINVAL", fmt.Sprintf(
			"Duplicating \"%s\" under \"%s\", which is not an instance of its schema parent (D-0124).", n.Path(), parent.Path())})
	}
	t.dupLog = lg
	d, err := t.dupNodes(n, parent, o.opts(), siblings)
	t.dupLog = nil
	if err = lg.done(err); err != nil {
		return nil, nil, err
	}
	return d, lg.diags, nil
}
