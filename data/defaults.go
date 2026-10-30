// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_new.c (lyd_new_implicit, lyd_new_implicit_r),
// src/validation.c (lyd_val_getnext_get) and src/tree_data_common.c (lys_getnext_data)
// (BSD-3-Clause, © CESNET).

package data

import (
	"errors"
	"iter"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
)

// diffOp is enum lyd_diff_op as the validation uses it.
type diffOp uint8

const (
	diffCreate diffOp = iota // LYD_DIFF_OP_CREATE
	diffDelete               // LYD_DIFF_OP_DELETE
)

// implOpts are the LYD_IMPLICIT_* options.
type implOpts uint8

const (
	implNoState    implOpts = 0x01 // LYD_IMPLICIT_NO_STATE
	implNoConfig   implOpts = 0x02 // LYD_IMPLICIT_NO_CONFIG
	implOutput     implOpts = 0x04 // LYD_IMPLICIT_OUTPUT
	implNoDefaults implOpts = 0x08 // LYD_IMPLICIT_NO_DEFAULTS
)

// valCtx is the state the new-node validation and the implicit nodes share: the work queues
// (nil when not collected), the validation options and the implicit diff.
type valCtx struct {
	t            *Tree
	probes       *Tree // private typed-probe construction and cumulative hash quota
	log          *logger
	xpathContext *schema.Set // explicit XPath context node; nil uses the first retained root
	opts         ValidateOptions
	nodeWhen     *nodeSet // node_when
	nodeTypes    *nodeSet // node_types
	metaTypes    *[]*meta // meta_types: metadata values that need the tree
	// output is LYD_INTOPT_REPLY: the output of an operation is validated (datastore data passes
	// false); op are the operation options of lyd_validate_op (zero for datastore data) and
	// ignoreWhen its LYXP_IGNORE_WHEN of the when and must evaluations after the subtree walk
	output     bool
	op         opOpts
	ignoreWhen bool
	// charge counts an implicit node against the parser's Budget.MaxNodes (U-0041); nil when
	// the caller does not count
	charge func() error
	// diff is lyd_val_diff_add: called with every node libyang adds to the implicit diff, in
	// libyang's order (the diff tree itself is design 07 D8b); nil when no diff is wanted.
	diff func(n *Node, op diffOp) error
	// getnext caches lyd_val_getnext_get per schema parent (and output).
	getnext map[getnextKey]getnextVal
	budget  xpathBudget  // MaxXPathSteps and cancellation (U-0042)
	top     []xpath.Node // the top-level view of the evaluations (topNodes)
	topGen  uint64
	// work counters for the tests: top-level view builds, when-pass position maps
	topBuilds, posBuilds int
	lrefs                map[lrefKey]*lrefTemplate // leafref target-path templates per type and node
	uniqDefs             map[*schema.Node]uniqDef  // unique leaves' default canonicals, per unique()
	// vars and to are the variables and the result cast of the evaluations of a query
	// (lyd_eval_xpath4); validation has none (to NodeSet casts nothing).
	vars []xpath.Var
	to   xpath.ResultType
	// modFirst is lyd_validate's *first2 when it is &first: the first top-level node of the
	// module being validated when that is not the tree's first node, else nil (see modInsert).
	modFirst *Node
	modCur   *schema.Module // the module lyd_validate validates now (lyd_del_move_root's mod)
	// module is the module of lyd_validate_module: only its data is validated (nil: every
	// module, lyd_validate_all)
	module *schema.Module
}

type getnextKey struct {
	sparent *schema.Node
	mod     *schema.Module
	output  bool
}

type getnextVal struct{ choices, snodes []*schema.Node }

// getnextOf is lyd_val_getnext_get: the choices and the other schema children of sparent (the
// top-level nodes of mod when nil), in lys_getnext order with choices not entered.
func (vc *valCtx) getnextOf(sparent *schema.Node, mod *schema.Module, output bool) (getnextVal, error) {
	k := getnextKey{sparent, mod, output}
	if sparent != nil {
		k.mod = nil
	}
	if v, ok := vc.getnext[k]; ok {
		return v, nil
	}
	opts := schema.GetNextWithChoice
	if output {
		opts |= schema.GetNextOutput
	}
	var top []*schema.Node
	if sparent == nil && mod != nil {
		top = mod.Top
	}
	var v getnextVal
	for sn := range schema.GetNext(sparent, top, opts) {
		if err := vc.checkContext(); err != nil {
			return getnextVal{}, err
		}
		if sn.Kind == schema.Choice {
			v.choices = append(v.choices, sn)
		} else {
			v.snodes = append(v.snodes, sn)
		}
	}
	if vc.getnext == nil {
		vc.getnext = map[getnextKey]getnextVal{}
	}
	vc.getnext[k] = v
	return v, nil
}

// stop is the test of LY_VAL_ERR_GOTO: go on after an error only when it is LY_EVALID under
// multi-error validation. The error's LY_ERR is that of the last error logged (warnings logged
// after it do not count).
func (vc *valCtx) stop(err error) bool {
	if err == nil {
		return false
	}
	if !errors.Is(err, errLogged) || !vc.opts.MultiError {
		return true // not a logged LY_EVALID (budget, cancellation, internal)
	}
	for i := len(vc.log.diags) - 1; i >= 0; i-- {
		if d := vc.log.diags[i]; failing(d) {
			return d.Err != "LY_EVALID"
		}
	}
	return true
}

// getnextData is lys_getnext_data iterated: the instances among sib of every schema node under
// parent (lys_getnext with no options: choices and cases entered), in schema order.
func (vc *valCtx) getnextData(sib *siblings, parent *schema.Node, yield func(*Node) bool) error {
	for sn := range schema.GetNext(parent, nil, 0) {
		if err := vc.checkContext(); err != nil {
			return err
		}
		// lys_getnext_data returns lyd_find_sibling_val's match itself, which may belong to
		// another context, then the following siblings of the same schema node pointer.
		for i, n := range vc.t.instanceRun(sib, sn) {
			if err := vc.checkContext(); err != nil {
				return err
			}
			if i > 0 && n.schema != sn {
				break
			}
			if !yield(n) {
				return nil
			}
		}
	}
	return nil
}

// firstData is lys_getnext_data(NULL, ...): the first instance of a schema node under parent.
func (vc *valCtx) firstData(sib *siblings, parent *schema.Node) (*Node, error) {
	var first *Node
	err := vc.getnextData(sib, parent, func(n *Node) bool { first = n; return false })
	return first, err
}

// configR is LYS_CONFIG_R: a state node. lys_compile_config sets no config flag at all on an rpc,
// action or notification and on everything inside it (LYS_COMPILE_NO_CONFIG is set before the
// operation node itself is compiled), so those are never state.
func configR(sn *schema.Node) bool {
	for p := sn; p != nil; p = p.Parent {
		switch p.Kind {
		case schema.RPC, schema.Action, schema.Notification:
			return false
		}
	}
	return !sn.Config
}

// newImplicit is lyd_new_implicit: the implicit children of parent (sparent: a case to fill, else
// parent's schema; the top level of mod when both are nil): defaults of choices (the default case
// when no case has data, else the existing case), then NP containers, leaf and leaf-list
// defaults that have no instance.
func (vc *valCtx) newImplicit(parent *Node, sparent *schema.Node, mod *schema.Module, o implOpts) error {
	if sparent == nil && parent != nil {
		sparent = parent.schema
	}
	sib := vc.t.childrenOf(parent)
	gn, err := vc.getnextOf(sparent, mod, o&implOutput != 0)
	if err != nil {
		return err
	}
	skip := func(sn *schema.Node) bool {
		return o&implNoState != 0 && configR(sn) || o&implNoConfig != 0 && sn.Config || sn.Status == schema.Obsolete
	}
	for _, ch := range gn.choices {
		if err := vc.checkContext(); err != nil {
			return err
		}
		if skip(ch) {
			continue
		}
		n, err := vc.firstData(sib, ch)
		if err != nil {
			return err
		}
		switch {
		case n == nil && ch.DefaultCase != nil:
			if err := vc.newImplicit(parent, ch.DefaultCase, nil, o); err != nil {
				return err
			}
		case n != nil:
			if err := vc.newImplicit(parent, n.schema.Parent, nil, o); err != nil {
				return err
			}
		}
	}
	for _, sn := range gn.snodes {
		if err := vc.checkContext(); err != nil {
			return err
		}
		if skip(sn) {
			continue
		}
		switch sn.Kind {
		case schema.Container:
			if !sn.Presence && vc.t.schemaLookup(sib, sn) == nil {
				n := newInner(sn, &vc.t.work)
				if vc.t.work.hashErr != nil {
					return vc.t.work.hashErr
				}
				if err := vc.addImplicit(parent, n); err != nil {
					return err
				}
			}
		case schema.Leaf, schema.LeafList:
			if o&implNoDefaults != 0 || len(sn.Default) == 0 || vc.t.schemaLookup(sib, sn) != nil {
				break
			}
			for _, d := range sn.Default { // a leaf has one, a leaf-list all of them
				if err := vc.checkContext(); err != nil {
					return err
				}
				v, diag := types.StoreDefault(sn, d)
				if diag != nil { // the default fit the type at compile; kept as libyang logs it
					return vc.log.item(parent, sn, false, "LY_EVALID", codeOf(diag.Code), diag.AppTag, diag.Msg)
				}
				n := newTerm(sn, v, &vc.t.work)
				if vc.t.work.hashErr != nil {
					return vc.t.work.hashErr
				}
				if err := vc.addImplicit(parent, n); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// addImplicit links an implicit node: Default (WhenTrue when it has a when, so a false when
// deletes it silently), the when queue and the diff.
func (vc *valCtx) addImplicit(parent, n *Node) error {
	if vc.charge != nil {
		if err := vc.charge(); err != nil {
			return err
		}
	}
	n.flags = FlagDefault
	if hasWhen(n.schema) {
		n.flags |= FlagWhenTrue
	}
	if parent == nil && vc.modFirst != nil {
		if err := vc.modInsert(n); err != nil {
			return err
		}
	} else {
		if err := vc.t.insert(parent, n, insertDefault); err != nil {
			return err
		}
	}
	if n.isTerm() && n.value.NeedsTree() && vc.nodeTypes != nil {
		vc.nodeTypes.add(n)
	}
	if hasWhen(n.schema) && vc.nodeWhen != nil {
		vc.nodeWhen.add(n)
	}
	if vc.diff != nil {
		return vc.diff(n, diffCreate)
	}
	return nil
}

// modInsert is lyd_insert_node of a top-level implicit node with first_sibling pointing at the
// module's first node, which is not the tree's first (lyd_validate passes &first then). Without
// an anchor, lyd_insert_node_last inserts after first->prev, which is the last node of the
// preceding module, not the last sibling: the node lands right before the module's first node,
// and first is not moved, so lyd_new_implicit_r does not recurse into it and lyd_validate_tree
// does not walk it (libyang v5.8.6 behaviour, fixtures types/print-prefixes-json and -xml).
// An anchor at first moves first to the node. Top-level opaque nodes after the data are an
// anchor (lyd_insert_get_next_anchor stops at them), so with any of them the node is inserted
// normally, before them (fixtures protocol-v2/implicit-top-anchor-last, -opaque).
// ponytail: Go's insertion position stands in for libyang's anchor search from first (they
// agree while the module's nodes are in schema order).
func (vc *valCtx) modInsert(n *Node) error {
	t, sib := vc.t, &vc.t.top
	if sib.st != nil && (sib.st.interleaved() || sib.schemaUnordered) {
		// out of schema order (an Ordered parse, copies): libyang's walks from first read the
		// physical order, opaque nodes or not
		return vc.modInsertInterleaved(n)
	}
	fi, err := vc.siblingIndex(sib.schemaSpan(), vc.modFirst)
	if err != nil {
		return err
	}
	at := t.insertPos(sib, n, insertDefault)
	moveFirst := at <= fi
	switch {
	case fi < 0:
		return t.link(nil, sib, n, at)
	case at == sib.SchemaLen() && sib.OpaqueLen() == 0:
		at = fi // no anchor: after first->prev
		moveFirst = false
	}
	if err := t.link(nil, sib, n, at); err != nil {
		return err
	}
	if moveFirst {
		vc.modFirst = n // inserted before first: lyd_insert_node_ordby_schema moves it
	}
	return nil
}

// modInsertInterleaved is modInsert in a top level out of schema order, interleaved or not
// (design 15): libyang's
// lyd_insert_node with first_sibling at the module's first node, in document order. A sorted
// instance with a leader takes the ordinary sorted insertion; otherwise
// lyd_insert_node_ordby_schema:
// lyd_insert_get_next_anchor walks from first past the data of preceding modules, then the
// module's data along its schema order, stopping at an opaque node or another module's node.
// Without an anchor, a trailing opaque block before first (first->prev) is the anchor; else the
// node goes after first->prev (the last sibling when first is the tree's first). first moves
// only when the node lands right before it.
func (vc *valCtx) modInsertInterleaved(n *Node) error {
	t, sib := vc.t, &vc.t.top
	if sortedSupported(n) && t.schemaLookup(sib, n.schema) != nil {
		// lyd_insert_node: a leader found by lyd_find_sibling_schema takes lyds_insert, the path
		// an ordinary insertion takes; lyds_insert moves first when n lands right before it
		if err := t.insert(nil, n, insertDefault); err != nil {
			return err
		}
		if sib.docNext(n) == vc.modFirst {
			vc.modFirst = n
		}
		return nil
	}
	first := vc.modFirst
	own := ownerModule(t.context(), n)
	mname := func(c *Node) string {
		if m := ownerModule(t.context(), c); m != nil {
			return m.Name
		}
		return ""
	}
	match := first
	for match != nil && match.schema != nil && mname(match) < own.Name {
		if err := vc.checkContext(); err != nil {
			return err
		}
		match = sib.docNext(match)
	}
	next, stop := iter.Pull(schema.GetNext(nil, n.schema.Module.Top, 0))
	defer stop()
	sn, ok := next()
	found := false
walk:
	for ; match != nil; match = sib.docNext(match) {
		if err := vc.checkContext(); err != nil {
			return err
		}
		if match.schema == nil || ownerModule(t.context(), match) != own {
			break
		}
		for !found {
			if !ok {
				match = nil // extension instance data: no anchor
				break walk
			}
			if sn == n.schema {
				found = true
				break
			}
			if match.schema == sn {
				break
			}
			sn, ok = next()
		}
		if found && match.schema != n.schema {
			break
		}
	}
	anchor := match
	prev := func(c *Node) *Node {
		if c == sib.docFirst() {
			return sib.docLast()
		}
		p, _ := sib.docNeighbours(c)
		return p
	}
	if anchor == nil {
		// cannot insert data node after opaque nodes: the opaque block before first
		if p := prev(first); p != nil && p.schema == nil {
			anchor = p
			for anchor != first && prev(anchor).schema == nil {
				anchor = prev(anchor)
			}
		}
	}
	var at int
	switch {
	case anchor != nil:
		sib.setPlace(placement{mode: placeBefore, node: anchor, target: n})
		at = sib.schemaBefore(anchor)
	case first == sib.docFirst():
		sib.setPlace(placement{mode: placeEnd, target: n})
		at = sib.SchemaLen()
	default:
		p := prev(first)
		sib.setPlace(placement{mode: placeAfter, node: p, target: n})
		at = sib.schemaBefore(p)
		if p.schema != nil {
			at++
		}
	}
	if err := t.link(nil, sib, n, at); err != nil {
		return err
	}
	if anchor == first {
		vc.modFirst = n // lyd_insert_before_node of the first sibling moves first
	}
	return nil
}

// delMoveRoots is lyd_del_move_root for lyd_validate's first (modFirst) before each top-level
// node of del is freed, in del's order, which libyang interleaves with the frees: first moves to
// the next sibling that is still linked, or back to the tree's first sibling (nil) when the node
// was the module's last. del is unlinked in one batch afterwards (one compaction), so the nodes
// before the one at hand count as freed already.
func (vc *valCtx) delMoveRoots(del []*Node) {
	if vc.modFirst == nil {
		return
	}
	var gone map[*Node]bool
	for i, n := range del {
		if vc.modFirst == n && n.parent == nil {
			if gone == nil {
				gone = make(map[*Node]bool, i)
				for _, d := range del[:i] {
					gone[d] = true
				}
			}
			vc.delMoveRoot(n, gone)
			if vc.modFirst == nil {
				return
			}
		}
		if gone != nil {
			gone[n] = true
		}
	}
}

// delMoveRoot is lyd_del_move_root for first == del, the nodes of gone already freed.
func (vc *valCtx) delMoveRoot(del *Node, gone map[*Node]bool) {
	mod := vc.modCur
	if ownerModule(vc.t.context(), del) != mod {
		mod = nil // no data of mod: simply the first top-level sibling
	}
	next := vc.t.top.docNext(del)
	for next != nil && gone[next] {
		next = vc.t.top.docNext(next)
	}
	if mod != nil && (next == nil || ownerModule(vc.t.context(), del) != ownerModule(vc.t.context(), next)) {
		vc.modFirst = nil // no more nodes of mod: the first top-level sibling
		return
	}
	vc.modFirst = next
}

// modStart is the document position of lyd_validate's *first2 in the top level: modFirst, else 0.
func (vc *valCtx) modStart() (int, error) {
	if vc.modFirst == nil {
		return 0, nil
	}
	i, err := vc.siblingIndex(vc.topList(), vc.modFirst) // a document position (topList)
	return max(0, i), err
}

func (vc *valCtx) siblingIndex(nodes siblingSpan, target *Node) (int, error) {
	for i, n := range nodes.All() {
		if err := vc.checkContext(); err != nil {
			return -1, err
		}
		if n == target {
			return i, nil
		}
	}
	return -1, nil
}

// newImplicitR is lyd_new_implicit_r: newImplicit, then recursively in every default container
// among the children (the top-level siblings when parent is nil).
func (vc *valCtx) newImplicitR(parent *Node, sparent *schema.Node, mod *schema.Module, o implOpts) error {
	if err := vc.newImplicit(parent, sparent, mod, o); err != nil {
		return err
	}
	// LY_LIST_FOR in document order: the children (lyd_child_no_keys: keys are not containers), or
	// at the top level from lyd_validate's first2
	kids := spanOf(vc.t.childrenOf(parent).docNodes())
	if parent == nil {
		kids = vc.topList()
		start, err := vc.modStart()
		if err != nil {
			return err
		}
		kids = kids.Slice(start, kids.Len())
	}
	for _, c := range kids.All() {
		if err := vc.checkContext(); err != nil {
			return err
		}
		if c.schema != nil && c.flags&FlagDefault != 0 && c.schema.Kind == schema.Container {
			if err := vc.newImplicitR(c, nil, mod, o); err != nil {
				return err
			}
		}
	}
	return nil
}
