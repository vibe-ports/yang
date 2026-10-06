// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_new.c (lyd_new_implicit, lyd_new_implicit_r),
// src/validation.c (lyd_val_getnext_get) and src/tree_data_common.c (lys_getnext_data)
// (BSD-3-Clause, © CESNET).

package data

import (
	"errors"
	"slices"

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
	t         *Tree
	log       *logger
	opts      ValidateOptions
	nodeWhen  *nodeSet // node_when
	nodeTypes *nodeSet // node_types
	metaTypes *[]*meta // meta_types: metadata values that need the tree
	// output is LYD_INTOPT_REPLY: the output of an operation is validated (operations are M4;
	// datastore data passes false)
	output bool
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
}

type getnextKey struct {
	sparent *schema.Node
	mod     *schema.Module
	output  bool
}

type getnextVal struct{ choices, snodes []*schema.Node }

// getnextOf is lyd_val_getnext_get: the choices and the other schema children of sparent (the
// top-level nodes of mod when nil), in lys_getnext order with choices not entered.
func (vc *valCtx) getnextOf(sparent *schema.Node, mod *schema.Module, output bool) getnextVal {
	k := getnextKey{sparent, mod, output}
	if sparent != nil {
		k.mod = nil
	}
	if v, ok := vc.getnext[k]; ok {
		return v
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
	return v
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
		if d := vc.log.diags[i]; !d.Warning {
			return d.Err != "LY_EVALID"
		}
	}
	return true
}

// getnextData is lys_getnext_data iterated: the instances among sib of every schema node under
// parent (lys_getnext with no options: choices and cases entered), in schema order.
func (vc *valCtx) getnextData(sib *siblings, parent *schema.Node, yield func(*Node) bool) {
	for sn := range schema.GetNext(parent, nil, 0) {
		i := vc.t.schemaIndex(sib, sn)
		if i < 0 {
			continue
		}
		for _, n := range sib.list[i:] {
			if n.schema != sn {
				break
			}
			if !yield(n) {
				return
			}
		}
	}
}

// firstData is lys_getnext_data(NULL, ...): the first instance of a schema node under parent.
func (vc *valCtx) firstData(sib *siblings, parent *schema.Node) (first *Node) {
	vc.getnextData(sib, parent, func(n *Node) bool { first = n; return false })
	return first
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
	gn := vc.getnextOf(sparent, mod, o&implOutput != 0)
	skip := func(sn *schema.Node) bool {
		return o&implNoState != 0 && configR(sn) || o&implNoConfig != 0 && sn.Config || sn.Status == schema.Obsolete
	}
	for _, ch := range gn.choices {
		if skip(ch) {
			continue
		}
		n := vc.firstData(sib, ch)
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
		if skip(sn) {
			continue
		}
		switch sn.Kind {
		case schema.Container:
			if !sn.Presence && vc.t.findSchema(sib, sn) == nil {
				if err := vc.addImplicit(parent, newInner(sn)); err != nil {
					return err
				}
			}
		case schema.Leaf, schema.LeafList:
			if o&implNoDefaults != 0 || len(sn.Default) == 0 || vc.t.findSchema(sib, sn) != nil {
				break
			}
			for _, d := range sn.Default { // a leaf has one, a leaf-list all of them
				v, diag := types.StoreDefault(sn, d)
				if diag != nil { // the default fit the type at compile; kept as libyang logs it
					return vc.log.item(parent, sn, false, "LY_EVALID", codeOf(diag.Code), diag.AppTag, diag.Msg)
				}
				if err := vc.addImplicit(parent, newTerm(sn, v)); err != nil {
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
	if n.isTerm() && n.value.NeedsTree() && vc.nodeTypes != nil {
		vc.nodeTypes.add(n) // only once charged: a budget error leaves nothing queued
	}
	n.flags = FlagDefault
	if hasWhen(n.schema) {
		n.flags |= FlagWhenTrue
	}
	if parent == nil && vc.modFirst != nil {
		vc.modInsert(n)
	} else {
		vc.t.insert(parent, n, insertDefault)
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
func (vc *valCtx) modInsert(n *Node) {
	t, sib := vc.t, &vc.t.top
	fi := slices.Index(sib.list, vc.modFirst)
	at := t.insertPos(sib, n, insertDefault)
	switch {
	case fi < 0:
		t.link(nil, sib, n, at)
		return
	case at == len(sib.list) && len(sib.opq) == 0:
		at = fi // no anchor: after first->prev
	case at <= fi:
		vc.modFirst = n // inserted before first: lyd_insert_node_ordby_schema moves it
	}
	t.link(nil, sib, n, at)
}

// modStart is the index of lyd_validate's *first2 in the top level: modFirst, else 0.
func (vc *valCtx) modStart() int {
	if vc.modFirst == nil {
		return 0
	}
	return max(0, slices.Index(vc.t.top.list, vc.modFirst))
}

// newImplicitR is lyd_new_implicit_r: newImplicit, then recursively in every default container
// among the children (the top-level siblings when parent is nil).
func (vc *valCtx) newImplicitR(parent *Node, sparent *schema.Node, mod *schema.Module, o implOpts) error {
	if err := vc.newImplicit(parent, sparent, mod, o); err != nil {
		return err
	}
	kids := vc.t.childrenOf(parent).list
	if parent == nil {
		kids = kids[vc.modStart():] // LY_LIST_FOR(*first): from lyd_validate's first2
	}
	for _, c := range kids { // lyd_child_no_keys: keys are not containers
		if c.flags&FlagDefault != 0 && c.schema.Kind == schema.Container {
			if err := vc.newImplicitR(c, nil, mod, o); err != nil {
				return err
			}
		}
	}
	return nil
}
