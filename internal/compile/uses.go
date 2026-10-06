// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile_node.c (lys_compile_uses, lys_compile_grouping)
// and src/schema_compile.c (lys_compile, validation of unused groupings) (BSD-3-Clause, © CESNET).

package compile

import (
	"fmt"
	"slices"
	"strings"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// ifFeature is lys_eval_iffeatures for if-features written in pm (design 06 C4b evaluates them;
// until then every if-feature counts as enabled and nodes carrying one are ErrUnsupported in
// nodeGeneric).
func (w *nodeCtx) ifFeature(_ *pmod, _ []*parser.IfFeature) (bool, error) { return true, nil }

// addDisabled is ly_set_add(&ctx->unres->disabled, node) (design 06 C4b/C7).
func (w *nodeCtx) addDisabled(*schema.Node) {}

// parentOf is lysp_node.parent: the parsed node pn is written in (nil at the top level).
// pn must belong to w.pm or be a node created by the compile (refined copies, fake uses).
func (w *nodeCtx) parentOf(pn *parser.Node) *parser.Node {
	if !w.indexed[w.pm] {
		w.indexed[w.pm] = true
		var walk func(p *parser.Node, list []*parser.Node)
		walk = func(p *parser.Node, list []*parser.Node) {
			for _, n := range list {
				w.pparent[n] = p
				for _, l := range [][]*parser.Node{n.Children, n.Groupings, n.Actions, n.Notifications, n.Augments} {
					walk(n, l)
				}
				for _, io := range []*parser.Node{n.Input, n.Output} {
					if io != nil {
						walk(n, []*parser.Node{io})
					}
				}
			}
		}
		m := w.pm.Parsed
		for _, l := range [][]*parser.Node{m.Children, m.Groupings, m.Actions, m.Notifications, m.Augments} {
			walk(nil, l)
		}
	}
	return w.pparent[pn]
}

// scopeOf is the typedef scope of a node: its parsed ancestors, innermost first.
func (w *nodeCtx) scopeOf(pn *parser.Node) *scope {
	var chain []*parser.Node
	for p := w.parentOf(pn); p != nil; p = w.parentOf(p) {
		chain = append(chain, p)
	}
	var sc *scope
	for _, p := range slices.Backward(chain) {
		sc = &scope{node: p, up: sc}
	}
	return sc
}

// groupingNamed is match_grouping over the groupings of owner (a parsed node, or &Parsed.Node
// of a (sub)module), through a name index built on first use; the first of a name wins.
func (w *nodeCtx) groupingNamed(owner *parser.Node, name string) *parser.Node {
	idx := w.grpIdx[owner]
	if idx == nil {
		idx = map[string]*parser.Node{}
		for _, g := range owner.Groupings {
			w.c.work++
			if idx[g.Name] == nil {
				idx[g.Name] = g
			}
		}
		w.grpIdx[owner] = idx
	}
	w.c.work++
	return idx[name]
}

// findGrouping is lys_compile_uses_find_grouping.
func (w *nodeCtx) findGrouping(uses *parser.Node) (*parser.Node, *pmod, error) {
	prefix, name := "", uses.Name
	if i := strings.IndexByte(name, ':'); i >= 0 {
		prefix, name = name[:i], name[i+1:]
	}
	var grp *parser.Node
	var found, pm *pmod
	if prefix == "" || prefix == w.pm.Parsed.Prefix {
		// current module, search local groupings first
		pm = w.parsedOf(w.pm.mod)
		for p := w.parentOf(uses); found == nil && p != nil; p = w.parentOf(p) {
			if grp = w.groupingNamed(p, name); grp != nil {
				found = w.pm
			}
		}
	} else {
		mod, ok := resolvePrefix(w.pm, prefix)
		if !ok || mod == nil {
			return nil, nil, w.errf(ly.Reference, "Invalid prefix used for grouping \"%s\" reference.", uses.Name)
		}
		pm = w.parsedOf(mod)
	}
	if found == nil && pm != nil {
		// search in top-level groupings of the main module and all the submodules
		if grp = w.groupingNamed(&pm.Parsed.Node, name); grp != nil {
			found = pm
		} else {
			for _, inc := range pm.Includes {
				if inc.Sub == nil {
					continue
				}
				if grp = w.groupingNamed(&inc.Sub.Parsed.Node, name); grp != nil {
					found = &inc.Sub.pmod
					break
				}
			}
		}
	}
	if found == nil {
		return nil, nil, w.errf(ly.Semantics, "Grouping \"%s\" referenced by a uses statement not found.", uses.Name)
	}
	if w.opts&optGrouping == 0 {
		// remember that the grouping is instantiated to avoid its standalone validation
		w.c.usedGrp[grp] = true
	}
	return grp, found, nil
}

// parsedOf is lys_module.parsed of a compiled module: the main module's pmod.
func (w *nodeCtx) parsedOf(m *schema.Module) *pmod {
	if lm := w.tc.parsed[m]; lm != nil {
		return &lm.pmod
	}
	return nil
}

// statusInt is a compiled status as the statusOf flag value.
func statusInt(s schema.Status) int { return int(s) + 1 }

// uses is lys_compile_uses; inherited is the statusOf value inherited from a schema-only parent.
func (w *nodeCtx) uses(pn *parser.Node, parent *schema.Node, inherited int, childSet *[]*schema.Node) error {
	if w.c.nodes++; w.c.nodes > orDefault(w.c.opts.Budget.MaxNodes, DefaultMaxNodes) {
		return fmt.Errorf("%w: more than %d compiled schema nodes and uses", ErrBudget, orDefault(w.c.opts.Budget.MaxNodes, DefaultMaxNodes))
	}
	if w.depth++; w.depth > orDefault(w.c.opts.Budget.MaxDepth, DefaultMaxDepth) {
		return fmt.Errorf("%w: schema nodes nested deeper than %d", ErrBudget, orDefault(w.c.opts.Budget.MaxDepth, DefaultMaxDepth))
	}
	defer func() { w.depth-- }()
	grp, grpPm, err := w.findGrouping(pn)
	if err != nil {
		return err
	}
	// groupings must not reference themselves: the stack holds those being applied
	if w.groupings[grp] {
		return w.errf(ly.Reference, "Grouping \"%s\" references itself through a uses statement.", grp.Name)
	}
	w.groupings[grp] = true
	prev := w.opts
	defer func() {
		w.opts = prev
		delete(w.groupings, grp)
	}()
	// nodetype checks
	for _, ops := range [][]*parser.Node{grp.Actions, grp.Notifications} {
		if len(ops) > 0 && parent != nil && parent.Kind != schema.Container && parent.Kind != schema.List {
			return w.errf(ly.Reference, "Invalid child %s \"%s\" of uses parent %s \"%s\" node.",
				ops[0].Name, pkindStr(ops[0].Kind), parent.Name, lysNodetype2str(parent.Kind))
		}
	}
	if err := checkStatus(parsedStatus(pn.Status), w.pm, pn.Name, parsedStatus(grp.Status), grpPm, grp.Name); err != nil {
		return w.logVErr(err)
	}
	if err := w.precompileUsesAugmentsRefines(pn, parent); err != nil {
		return err
	}
	parentSt, parentName := 0, ""
	if parent != nil {
		parentSt, parentName = statusInt(parent.Status), parent.Name
	}
	st, err := w.status(statusOf(pn.Status), inherited, parentSt, parentName, "<uses>")
	if err != nil {
		return err
	}
	usesSt := statusInt(st)
	enabled, err := w.ifFeature(w.pm, pn.IfFeatures)
	if err != nil {
		return err
	}
	disabled := false
	if !enabled && w.opts&(optDisabled|optGrouping) == 0 {
		w.opts |= optDisabled
		disabled = true
	}
	var own []*schema.Node
	if childSet == nil {
		childSet = &own
	}
	for _, list := range [][]*parser.Node{grp.Children, grp.Actions, grp.Notifications} {
		if err := w.usesChildren(pn, usesSt, list, grpPm, parent, childSet, disabled); err != nil {
			return err
		}
	}
	// check that all augments and refines of this uses were applied
	if w.pendingOf[pn] == 0 {
		return w.usesExts(pn, grp)
	}
	for _, a := range w.usesAugs.items {
		if a.uses == pn {
			_ = w.errf(ly.Reference, "Augment target node \"%s\" in grouping \"%s\" was not found.", a.nid.str, grp.Name)
			err = eNotFound
		}
	}
	if err != nil {
		return err
	}
	for _, r := range w.usesRfns.items {
		if r.uses == pn {
			_ = w.errf(ly.Reference, "Refine(s) target node \"%s\" in grouping \"%s\" was not found.", r.nid.str, grp.Name)
			err = eNotFound
		}
	}
	if err != nil {
		return err
	}
	return w.usesExts(pn, grp)
}

// usesExts compiles the uses and grouping extension instances into the parent (design 06 C4b).
func (w *nodeCtx) usesExts(pn, grp *parser.Node) error {
	if len(pn.Exts) > 0 || len(grp.Exts) > 0 {
		return fmt.Errorf("%w: extension instances (design 06 C4b)", ErrUnsupported)
	}
	return nil
}

// logVErr logs a *vErr at the current path.
func (w *nodeCtx) logVErr(err error) error {
	ve := err.(*vErr) //nolint:errorlint // checkStatus returns *vErr only
	_ = w.errf(ve.Code, "%s", ve.Msg)
	return rc(ve.Err)
}

// usesChildren is lys_compile_uses_children.
func (w *nodeCtx) usesChildren(uses *parser.Node, inherited int, list []*parser.Node, grpPm *pmod,
	parent *schema.Node, childSet *[]*schema.Node, disabled bool) error {
	prevPm, prevOpts := w.pm, w.opts
	defer func() { w.pm, w.opts = prevPm, prevOpts }()
	var shared *schema.When
	for _, pn := range list {
		i := len(*childSet)
		// compile the nodes with their parsed (grouping) module
		w.pm = grpPm
		if err := w.node(pn, parent, inherited, childSet); err != nil {
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
		w.pm = prevPm
		// the uses is not in the compiled tree: pass its statements to the children
		for _, n := range (*childSet)[i:] {
			if uses.When != nil {
				if err := w.sharedWhen(uses.When, inherited, parent, schema.DataNode(parent), n, &shared); err != nil {
					return err
				}
			}
			if disabled {
				w.addDisabled(n)
			}
		}
		w.opts = prevOpts
	}
	return nil
}

// sharedWhen is lys_compile_when with when_c: one compiled when shared by every child of a uses
// or augment (the unres part is design 06 C7).
func (w *nodeCtx) sharedWhen(pw *parser.Restr, inherited int, parent, ctxNode, n *schema.Node, shared **schema.When) error {
	if *shared == nil {
		ns := nsCtx(w.pm)
		e, err := w.xpathCompile(pw.Arg, ns)
		if err != nil {
			return err
		}
		if len(pw.Exts) > 0 {
			return fmt.Errorf("%w: extension instances (design 06 C4b)", ErrUnsupported)
		}
		parentSt, parentName := 0, ""
		if parent != nil {
			parentSt, parentName = statusInt(parent.Status), parent.Name
		}
		st, err := w.status(0, inherited, parentSt, parentName, "when")
		if err != nil {
			return err
		}
		*shared = &schema.When{Src: pw.Arg, Ctx: ns, ContextNode: ctxNode, Status: st, Compiled: e}
	}
	n.Whens = append(n.Whens, *shared)
	return nil
}

// grouping is lys_compile_grouping: validate grp (declared in pnode, nil at the top level) on
// its own, in a fake container.
func (w *nodeCtx) grouping(pnode, grp *parser.Node) error {
	fakeUses := &parser.Node{Kind: "uses", Name: grp.Name, Status: grp.Status}
	w.pparent[fakeUses] = pnode
	fake := &schema.Node{Kind: schema.Container, Name: "fake", Module: w.cur}
	flagsOf := pnode
	if flagsOf == nil {
		flagsOf = &parser.Node{}
	}
	if err := w.nodeFlags(flagsOf, 0, fake); err != nil {
		return err
	}
	if pnode != nil {
		w.c.warn("Locally scoped grouping \"%s\" not used.", grp.Name)
	}
	w.path.set(w.groupingPathlog(pnode))
	w.path.update(nil, "{grouping}")
	w.path.update(nil, grp.Name)
	err := w.uses(fakeUses, fake, 0, nil)
	w.path.init(w.cur)
	return err
}

// groupingPathlog is lys_compile_grouping_pathlog for the parsed node a grouping is declared in.
func (w *nodeCtx) groupingPathlog(pn *parser.Node) string {
	path := ""
	for it := pn; it != nil; it = w.parentOf(it) {
		id := it.Name
		switch it.Kind {
		case "uses":
			id = "{uses='" + it.Name + "'}"
		case "grouping":
			id = "{grouping='" + it.Name + "'}"
		case "augment":
			id = "{augment='" + it.Name + "'}"
		}
		if w.parentOf(it) == nil {
			path = "/" + w.cur.Name + ":" + id + path
		} else {
			path = "/" + id + path
		}
	}
	if path == "" {
		return "/"
	}
	return path
}

// validateGroupings is the grouping part of lys_compile (SC:1817-1849): unused top-level
// groupings and unused groupings of top-level data nodes, of the module and its submodules.
func (w *nodeCtx) validateGroupings(m *Module) error {
	prev := w.opts
	defer func() { w.opts, w.pm = prev, &m.pmod }()
	w.opts |= optGrouping
	pms := []*pmod{&m.pmod}
	for _, inc := range m.Includes {
		if inc.Sub != nil {
			pms = append(pms, &inc.Sub.pmod)
		}
	}
	for _, pm := range pms {
		w.pm = pm
		for _, grp := range pm.Parsed.Groupings {
			if !w.c.usedGrp[grp] {
				if err := w.grouping(nil, grp); err != nil {
					return err
				}
			}
		}
		for _, pn := range pm.Parsed.Children {
			for _, grp := range pn.Groupings {
				if !w.c.usedGrp[grp] {
					if err := w.grouping(pn, grp); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
