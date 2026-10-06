// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile_amend.c (lys_compile_augment,
// lys_compile_node_augments) (BSD-3-Clause, © CESNET).

package compile

import (
	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// topAug is struct lysc_augment of a top-level augment (ctx->augs).
type topAug struct {
	nid *nodeid
	pm  *pmod // aug_pmod
	aug *parser.Node
}

// precompileOwnAugments is lys_precompile_own_augments: the top-level augments of the modules
// augmenting the one being compiled (module, then its submodules, statement order) that target
// it. The loader checked their node-ids (lys_nodeid_mod_check, design 06 C1b); augments in
// extension instances are U-0023.
func (w *nodeCtx) precompileOwnAugments(m *Module) {
	for _, am := range m.augmentedBy {
		pms := []*pmod{&am.pmod}
		for _, inc := range am.Includes {
			if inc.Sub != nil {
				pms = append(pms, &inc.Sub.pmod)
			}
		}
		for _, pm := range pms {
			for _, a := range pm.Parsed.Augments {
				e, msg := lyxp.Lex(a.Name)
				if msg != "" || len(e.Toks) == 0 { // defensive: Lex returns a message for empty/blank input
					continue // reported when the augmenting module was implemented
				}
				nid := precompileNodeid(e)
				if pm.resolve(nid.prefix[0]) != w.cur {
					continue // augment for another module
				}
				w.augs.add(&topAug{nid: nid, pm: pm, aug: a}, w.nidKey(nid, pm), nid)
			}
		}
	}
}

// augments is lys_compile_node_augments: apply the uses augments, then the top-level augments
// targeting node, restarting each scan after an application (an applied augment may add
// targets of others).
func (w *nodeCtx) augments(node *schema.Node) error {
	prevPm, prevCur := w.pm, w.cur
	defer func() { w.pm, w.cur, w.tc.cur = prevPm, prevCur, prevCur }()
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
	keys = w.augs.keys(node.Name, node.Parent, &w.c.work)
	tsc := w.augs.scan(keys, &w.c.work)
	for i := 0; ; {
		aug, j, ok := tsc.next(i)
		if !ok {
			break
		}
		if !w.nodeidMatch(aug.nid, aug.pm, nil, node, nil, nil) {
			i = j + 1
			continue
		}
		// use the path (from the root) and the modules of the augment
		saved := w.path
		w.cur, w.tc.cur, w.pm = aug.pm.mod, aug.pm.mod, aug.pm
		w.path.init(w.cur)
		w.path.update(nil, "{augment}")
		w.path.update(nil, aug.aug.Name)
		err := w.compileAugment(aug.aug, node)
		w.path = saved
		w.cur, w.tc.cur = prevCur, prevCur
		if err != nil {
			return err
		}
		w.augs.remove(aug)
		tsc = w.augs.scan(keys, &w.c.work)
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
	enabled, err := w.ifFeature(aug.IfFeatures)
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
	// augment extension instances go to the target
	var err2 error
	target.Exts, err2 = w.compileExts(aug.Stmt, target, target.Exts)
	return err2
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
		enabled, err := w.ifFeature(pn.IfFeatures)
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
