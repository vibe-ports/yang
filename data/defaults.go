// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_new.c (lyd_new_implicit, lyd_new_implicit_r),
// src/validation.c (lyd_val_getnext_get) and src/tree_data_common.c (lys_getnext_data)
// (BSD-3-Clause, © CESNET).

package data

import (
	"errors"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
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

// configR is LYS_CONFIG_R: a state node (nodes of operations have no config flag).
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
				n := newTerm(sn, v)
				if v.NeedsTree() && vc.nodeTypes != nil {
					vc.nodeTypes.add(n)
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
	vc.t.insert(parent, n, insertDefault)
	if hasWhen(n.schema) && vc.nodeWhen != nil {
		vc.nodeWhen.add(n)
	}
	if vc.diff != nil {
		return vc.diff(n, diffCreate)
	}
	return nil
}

// newImplicitR is lyd_new_implicit_r: newImplicit, then recursively in every default container
// among the children (the top-level siblings when parent is nil).
func (vc *valCtx) newImplicitR(parent *Node, sparent *schema.Node, mod *schema.Module, o implOpts) error {
	if err := vc.newImplicit(parent, sparent, mod, o); err != nil {
		return err
	}
	for _, c := range vc.t.childrenOf(parent).list { // lyd_child_no_keys: keys are not containers
		if c.flags&FlagDefault != 0 && c.schema.Kind == schema.Container {
			if err := vc.newImplicitR(c, nil, mod, o); err != nil {
				return err
			}
		}
	}
	return nil
}
