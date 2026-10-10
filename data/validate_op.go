// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/validation.c (lyd_validate_op, _lyd_validate_op,
// lyd_val_op_merge_find) (BSD-3-Clause, © CESNET).

package data

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

// isOp reports whether sn is an rpc, action or notification.
func isOp(sn *schema.Node) bool {
	return sn != nil && (sn.Kind == schema.RPC || sn.Kind == schema.Action || sn.Kind == schema.Notification)
}

// validateOp is lyd_validate_op: the operation of type typ in op (the operation node itself or
// any node of its tree), validated against the dependency tree dep (the operational datastore,
// nil for none): its subtree is merged into dep for the time of the validation (where dep has the
// operation's parents; without dep the top node of the operation's tree stands alone, its
// top-level siblings unseen) and gets its implicit nodes (a reply: the output's), its when, value,
// must and schema checks. The operation's tree keeps the implicit nodes; dep is left as it was.
// As libyang compares the node pointers op_tree == dep_tree, dep is redundant (dropped) only when
// op itself is dep's first top-level node. set is the schema op belongs to (LYD_CTX(op)): op may
// be detached, a subtree of no tree.
func validateOp(ctx context.Context, set *schema.Set, op *Node, dep *Tree, typ opType, b Budget) ([]yang.Diagnostic, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var oo opOpts
	switch typ {
	case opRPC:
		oo.rpc, oo.action = true, true
	case opNotif:
		oo.notif = true
	case opReply:
		oo.reply = true
	}
	lg := &logger{set: set}
	opNode := op
	if !isOp(op.schema) {
		// find the operation/notification (LYD_TREE_DFS from the top of op's tree)
		top := op
		for top.parent != nil {
			top = top.parent
		}
		opNode = nil
		for n := range top.All() {
			if n.schema == nil {
				err := lg.opaqError(n)
				return lg.diags, lg.finish(err)
			}
			if (oo.rpc || oo.reply) && (n.schema.Kind == schema.RPC || n.schema.Kind == schema.Action) ||
				oo.notif && n.schema.Kind == schema.Notification {
				opNode = n
				break
			}
		}
	}
	if oo.rpc || oo.reply {
		if opNode == nil || opNode.schema.Kind != schema.RPC && opNode.schema.Kind != schema.Action {
			return lg.diags, lg.finish(lg.logErr("LY_EINVAL", "No RPC/action to validate found."))
		}
	} else if opNode == nil || opNode.schema.Kind != schema.Notification {
		return lg.diags, lg.finish(lg.logErr("LY_EINVAL", "No notification to validate found."))
	}
	if dep != nil && len(dep.top.list) > 0 && dep.top.list[0] == op {
		dep = nil // redundant dependency
	}
	return validateOpNode(ctx, set, opNode, dep, oo, b)
}

// finish is the error of a failed call that logged its reason.
func (l *logger) finish(err error) error {
	if errors.Is(err, errLogged) {
		return l.result()
	}
	return err
}

// validateOpNode is _lyd_validate_op with validate_subtree: opNode merged into dep (without dep,
// the top node of its tree moved into a tree of its own), validated, and moved back (a detached
// operation stays detached). Implicit nodes count against b.MaxNodes, as for Validate.
func validateOpNode(ctx context.Context, set *schema.Set, opNode *Node, dep *Tree, oo opOpts, b Budget) ([]yang.Diagnostic, error) {
	opTree := opNode.treeOf() // nil: a detached operation
	sub, tparent := opMergeFind(opNode, dep)
	t := dep
	if t == nil {
		t = newTree(set)
	}
	// move the operation's subtree into t, under its parent there
	sib := sub.siblingsOf()
	oparent := sub.parent
	at := -1 // the position among its siblings (an opaque node: among the opaque ones)
	switch {
	case sib == nil: // a detached operation
	case sub.schema != nil:
		at = slices.Index(sib.list, sub)
	default:
		at = slices.Index(sib.opq, sub)
	}
	unlink(sub)
	t.insert(tparent, sub, insertDefault)
	restore := func() {
		unlink(sub)
		switch {
		case opTree != nil:
			opTree.link(oparent, opTree.childrenOf(oparent), sub, at)
		case oparent != nil: // a subtree of a detached operation tree: back under its parent there
			t.link(oparent, &oparent.kids, sub, at)
		} // a detached top node stays detached
	}
	vc := &valCtx{t: t, log: &logger{set: t.set}, nodeWhen: &nodeSet{}, nodeTypes: &nodeSet{}, metaTypes: &[]*meta{},
		budget: xpathBudget{ctx: ctx, max: b.MaxXPathSteps}, output: oo.reply, op: oo, charge: implicitCharge(ctx, b)}
	err := vc.validateOp(opNode)
	restore()
	if err == nil || errors.Is(err, errLogged) {
		err = vc.log.result()
	}
	return vc.log.diags, err
}

// implicitCharge counts the implicit nodes of one validation against b.MaxNodes (U-0041) and
// checks ctx every 1k nodes, as Validate does.
func implicitCharge(ctx context.Context, b Budget) func() error {
	charged := 0
	return func() error {
		charged++
		if limit := orDefault(b.MaxNodes, DefaultMaxNodes); charged > limit {
			return fmt.Errorf("%w: more than %d implicit data nodes", yang.ErrBudget, limit)
		}
		if charged%1024 == 0 {
			return ctx.Err()
		}
		return nil
	}
}

// validateOp is the validation part of _lyd_validate_op with validate_subtree.
func (vc *valCtx) validateOp(opNode *Node) error {
	if vc.op.reply {
		// the output's implicit nodes, then the children (the operation itself is skipped)
		w, ty := vc.nodeWhen, vc.nodeTypes
		vc.nodeWhen, vc.nodeTypes = nil, nil
		err := vc.newImplicit(opNode, nil, nil, implOutput)
		vc.nodeWhen, vc.nodeTypes = w, ty
		if err != nil {
			return err
		}
		for _, c := range slicesOf(&opNode.kids) {
			if err := vc.validateTree(c); err != nil {
				return err
			}
		}
	} else if err := vc.validateTree(opNode); err != nil {
		return err
	}
	// incompletely validated values and when conditions on the full tree, unresolved whens of the
	// unvalidated dependency tree ignored
	vc.ignoreWhen = true
	if err := vc.unres(); err != nil {
		return err
	}
	vc.obsolete(opNode)
	if err := vc.validateMust(opNode); err != nil {
		return err
	}
	return vc.finalR(opNode, slicesOf(&opNode.kids), opNode.schema, nil)
}

// opMergeFind is lyd_val_op_merge_find: the operation's ancestor sub to move into dep (the
// highest one dep does not have) and its parent there (nil: the top level). Without dep, sub is
// the top of the operation's tree.
func opMergeFind(opNode *Node, dep *Tree) (sub, parent *Node) {
	var chain []*Node // the operation and its ancestors, top-level first
	for n := opNode; n != nil; n = n.parent {
		chain = append([]*Node{n}, chain...)
	}
	if dep == nil {
		return chain[0], nil
	}
	sib := &dep.top
	for _, n := range chain[:len(chain)-1] {
		m := dep.findFirst(sib, n)
		if m == nil {
			return n, parent
		}
		parent, sib = m, &m.kids
	}
	return opNode, parent
}
