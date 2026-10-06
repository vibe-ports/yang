// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile_amend.c (lys_compile_augment,
// lys_compile_node_augments) (BSD-3-Clause, © CESNET).

package compile

import (
	"fmt"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// augments is lys_compile_node_augments: apply the uses augments targeting node, restarting the
// scan after each (an applied augment may add targets of others).
func (w *nodeCtx) augments(node *schema.Node) error {
	prevPm := w.pm
	defer func() { w.pm = prevPm }()
	keys := w.usesAugs.keys(node.Name, node.Parent, &w.c.work)
	sc := w.usesAugs.scan(keys, &w.c.work)
	for i := 0; ; {
		aug, j, ok := sc.next(i)
		if !ok {
			break
		}
		// node-ids use the prefixes of cur_mod->parsed
		if !w.nodeidMatch(aug.nid, w.parsedOf(w.cur), aug.ctxNode, node, nil, nil) {
			i = j + 1
			continue
		}
		// use the path and modules from the augment
		w.path.update(nil, "{augment}")
		w.path.update(nil, aug.aug.Name)
		w.pm = aug.pm
		err := w.compileAugment(aug.aug, node)
		w.path.pop()
		w.path.pop()
		if err != nil {
			return err
		}
		// applied: remove it (ly_set_rm) and start again; the augment may have added items
		w.usesAugs.remove(aug)
		w.pendingOf[aug.uses]--
		sc = w.usesAugs.scan(keys, &w.c.work)
		i = 0
	}
	return nil
}

// compileAugment is lys_compile_augment: connect the augment's children into target.
func (w *nodeCtx) compileAugment(aug *parser.Node, target *schema.Node) error {
	prev := w.opts
	defer func() { w.opts = prev }()
	ops := target.Kind == schema.Container || target.Kind == schema.List // lysc_node_actions_p, lysc_node_notifs_p
	if len(aug.Actions) > 0 && !ops {
		return w.errf(ly.Reference, "Invalid augment of %s node which is not allowed to contain RPC/action node \"%s\".",
			lysNodetype2str(target.Kind), aug.Actions[0].Name)
	}
	if len(aug.Notifications) > 0 && !ops {
		return w.errf(ly.Reference, "Invalid augment of %s node which is not allowed to contain notification node \"%s\".",
			lysNodetype2str(target.Kind), aug.Notifications[0].Name)
	}
	enabled, err := w.ifFeature(w.pm, aug.IfFeatures)
	if err != nil {
		return err
	}
	disabled := false
	if !enabled && w.opts&(optDisabled|optGrouping) == 0 {
		w.opts |= optDisabled
		disabled = true
	}
	for _, list := range [][]*parser.Node{aug.Children, aug.Actions, aug.Notifications} {
		if err := w.augmentChildren(aug, list, target, disabled); err != nil {
			return err
		}
	}
	if len(aug.Exts) > 0 {
		// augment extension instances go to the target
		return fmt.Errorf("%w: extension instances (design 06 C4b)", ErrUnsupported)
	}
	return nil
}

// augmentChildren is lys_compile_augment_children.
func (w *nodeCtx) augmentChildren(aug *parser.Node, list []*parser.Node, target *schema.Node, disabled bool) error {
	prev := w.opts
	defer func() { w.opts = prev }()
	// mandatory nodes are allowed only in a conditional augment, in new cases of a choice, or
	// in the augmented module itself
	allowMand := aug.When != nil || target.Kind == schema.Choice || w.cur == target.Module
	inherited := statusOf(aug.Status)
	var shared *schema.When
	for _, pn := range list {
		// can the child be connected to the target (e.g. a case cannot be inserted into a container)?
		ops := target.Kind == schema.Container || target.Kind == schema.List
		if pn.Kind == "case" && target.Kind != schema.Choice ||
			(pn.Kind == "rpc" || pn.Kind == "action" || pn.Kind == "notification") && !ops ||
			pn.Kind == "uses" && target.Kind == schema.Choice {
			return w.errf(ly.Reference, "Invalid augment of %s node which is not allowed to contain %s node \"%s\".",
				lysNodetype2str(target.Kind), pkindStr(pn.Kind), pn.Name)
		}
		var set []*schema.Node
		var err error
		switch target.Kind {
		case schema.Choice:
			err = w.choiceChild(pn, target, &set)
		case schema.Input:
			w.opts |= optRPCInput
			err = w.node(pn, target, inherited, &set)
		case schema.Output:
			w.opts |= optRPCOutput
			err = w.node(pn, target, inherited, &set)
		default:
			err = w.node(pn, target, inherited, &set)
		}
		if err != nil {
			return err
		}
		// eval if-features again for the rest of this node processing
		enabled, err := w.ifFeature(w.pm, pn.IfFeatures)
		if err != nil {
			return err
		}
		if !enabled && w.opts&(optDisabled|optGrouping) == 0 {
			w.opts |= optDisabled
		}
		// the augment is not in the compiled tree: pass its statements to the children
		for _, n := range set {
			if !allowMand && w.fl[n]&flConfigW != 0 && n.Mandatory {
				// (libyang unsets the flag on the node and its parents first; the compile fails anyway)
				return w.errf(ly.Semantics, "Invalid augment adding mandatory node \"%s\" without making it conditional via when statement.", n.Name)
			}
			if aug.When != nil {
				if err := w.sharedWhen(aug.When, inherited, target, schema.DataNode(target), n, &shared); err != nil {
					return err
				}
			}
			if disabled {
				w.addDisabled(n)
			}
		}
		w.opts = prev
	}
	return nil
}
