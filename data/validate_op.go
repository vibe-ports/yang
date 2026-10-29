// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/validation.c (lyd_validate_op, _lyd_validate_op,
// lyd_val_op_merge_find) (BSD-3-Clause, © CESNET).

package data

import (
	"context"
	"errors"
	"fmt"

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
	// validation.c lyd_validate_op checks these arguments before reading or changing any data.
	if op == nil {
		return nil, argErr("op_tree", "lyd_validate_op")
	}
	// A *Tree dependency is always its top level, so !dep_tree->parent has no Go counterpart.
	if typ != opRPC && typ != opNotif && typ != opReply {
		return nil, argErr("(data_type == LYD_TYPE_RPC_YANG) || (data_type == LYD_TYPE_NOTIF_YANG) || (data_type == LYD_TYPE_REPLY_YANG)", "lyd_validate_op")
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
	if dep != nil && dep.top.SchemaLen() > 0 && dep.top.At(0) == op {
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
func validateOpNode(ctx context.Context, set *schema.Set, opNode *Node, dep *Tree, oo opOpts, b Budget) (_ []yang.Diagnostic, err error) {
	opTree := opNode.treeOf() // nil: a detached operation
	// A budget failure leaves the moved subtree for the one state over both trees to restore
	// (restore below). ValidateOp opens it; any other caller gets its own here.
	if opTree != nil {
		defer opTree.work.beginHashWork()()
	}
	if dep != nil {
		defer dep.work.beginHashWork()()
	}
	j, err := beginJointTransaction(opTree, dep)
	defer j.end(&err)
	if err != nil {
		return nil, err
	}
	t := dep
	if t == nil {
		t = newTree(set)
	}
	defer t.work.beginHashWork()()
	if opTree != nil && opTree != t {
		defer opTree.work.beginHashWork()()
	}
	sub, tparent, err := opMergeFind(opNode, dep)
	if err != nil {
		return nil, err
	}
	sourceSib, sourceParent, sourceOwner := sub.siblingsOf(), sub.parent, sub.treeOf()
	if sourceOwner == nil {
		sourceOwner = t
	}
	at := -1
	var recovery uint64
	var near placement
	if sourceSib != nil {
		list := sourceSib.schemaSpan()
		if sub.schema == nil {
			list = sourceSib.opaqueSpan()
		}
		var err error
		at, err = t.originIndex(sourceSib, list, sub)
		if err != nil {
			return nil, err
		}
		near = sourceSib.originPlace(sub)
		sourceSib.setPlace(near) // the bound of restoreLink's placed link
		cost, err := sourceOwner.linkBound(sourceParent, sourceSib, sub, at)
		sourceSib.clearPlace()
		if err != nil {
			return nil, err
		}
		binding, err := subtreeBindingBound(sub, &sourceOwner.work, true)
		if err != nil {
			return nil, err
		}
		cost = addHashBound(cost, binding)
		recovery = 4*cost + 64
		if err := t.work.chargeHash(recovery); err != nil {
			return nil, err
		}
	}
	// Reserve source removal and exact restoration before either forest changes.
	restoreOrigin, arm, err := t.linkOrigin(sub)
	if err != nil {
		return nil, err
	}
	if err := preflightUnlink(sub); err != nil {
		return nil, err
	}
	disarm, err := arm(nil)
	if err != nil {
		return nil, err
	}
	defer disarm()
	if sourceSib != nil && sub.treeOf() == nil {
		// A detached source has no journal of its own: the dependency's journal restores it.
		if err := t.work.journal.captureDetachedParent(sourceParent); err != nil {
			return nil, err
		}
	}
	if err := unlink(sub); err != nil {
		return nil, err
	}
	linkErr := t.insert(tparent, sub, insertDefault)
	// A move back that fails (a D-0121 removal refusal as well as a budget error) cannot leave
	// the operation in the dependency: both journals restore the trees on any error.
	abandon := func(err error) error {
		for _, owner := range []*Tree{t, opTree} {
			if owner != nil && owner.work.journal != nil {
				owner.work.journal.rollback = true
			}
		}
		return err
	}
	restore := func(failedMove bool) error {
		if t.work.hashErr != nil {
			return t.work.hashErr
		}
		if opTree != nil && opTree.work.hashErr != nil {
			return opTree.work.hashErr
		}
		for _, owner := range []*Tree{t, opTree} {
			if owner != nil && owner.work.journal != nil && owner.work.journal.err != nil {
				return owner.work.journal.err
			}
		}
		if !failedMove && sourceSib != nil {
			// Validation can create implicit children. Bind their complete resulting subtree
			// before unlinking from the temporary owner, while quota failure can roll back.
			bound, err := bindSubtreeBound(sub, &sourceOwner.work)
			if err != nil {
				return err
			}
			if err := sourceOwner.reserveHash(bound); err != nil {
				return err
			}
			if err := bindSubtreeWork(sub, &sourceOwner.work); err != nil {
				return err
			}
		}
		if sub.siblingsOf() != nil {
			// An exhausted operation rolls back both journals at the public boundary. Do not
			// attempt another charged mutation with its failed allowance.
			if t.work.hashErr != nil {
				return t.work.hashErr
			}
			if err := unlink(sub); err != nil {
				return err
			}
		}
		if failedMove {
			restoreOrigin()
		} else if sourceSib != nil {
			// _lyd_validate_op restores only positional links and hashes. The removed RB node
			// remains a nonmember; hash leadership must not move lyds metadata implicitly.
			sourceOwner.restoreLink(sourceParent, sub, at, recovery, near)
		}
		return nil
	}
	if linkErr != nil {
		if err := restore(true); err != nil {
			return nil, abandon(err)
		}
		return nil, linkErr
	}
	vc := &valCtx{t: t, log: &logger{set: t.context()}, nodeWhen: &nodeSet{}, nodeTypes: &nodeSet{}, metaTypes: &[]*meta{},
		budget: xpathBudget{ctx: ctx, max: b.MaxXPathSteps}, output: oo.reply, op: oo, charge: implicitCharge(ctx, b)}
	err = vc.validateOp(opNode)
	if restoreErr := restore(false); restoreErr != nil {
		return vc.log.diags, abandon(restoreErr)
	}
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
			return poll(ctx)
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
		for _, c := range slicesOf(&opNode.kids).All() {
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
func opMergeFind(opNode *Node, dep *Tree) (sub, parent *Node, err error) {
	var chain []*Node // the operation and its ancestors, top-level first
	for n := opNode; n != nil; n = n.parent {
		chain = append([]*Node{n}, chain...)
	}
	if dep == nil {
		return chain[0], nil, nil
	}
	sib := &dep.top
	for _, n := range chain[:len(chain)-1] {
		m, err := dep.findFirstChecked(sib, n)
		if err != nil {
			return nil, nil, err
		}
		if m == nil {
			return n, parent, nil
		}
		parent, sib = m, &m.kids
	}
	return opNode, parent, nil
}
