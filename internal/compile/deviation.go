// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile_amend.c (lys_precompile_own_deviations,
// lys_precompile_own_deviation, the deviation loop of lys_compile_node_deviations_refines,
// lys_apply_deviation) and src/schema_compile.c (the deviation part of lys_compile_unres_mod)
// (BSD-3-Clause, © CESNET).

package compile

import (
	"errors"
	"fmt"
	"slices"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// devSet is struct lysc_deviation (ctx->devs): every deviation of one target node, with the
// (sub)module each is written in.
type devSet struct {
	nid          *nodeid
	devs         []*parser.Deviation
	pms          []*pmod // dev_pmods
	notSupported bool
}

// precompileOwnDeviations is lys_precompile_own_deviations: the deviations of the modules
// deviating the one being compiled (module, then its submodules, statement order) that target
// it, merged per target node. The loader checked their node-ids (precompileModAugments).
func (w *nodeCtx) precompileOwnDeviations(m *Module) error {
	for _, dm := range m.deviatedBy {
		pms := []*pmod{&dm.pmod}
		for _, inc := range dm.Includes {
			if inc.Sub != nil {
				pms = append(pms, &inc.Sub.pmod)
			}
		}
		for _, pm := range pms {
			for _, d := range pm.Parsed.Deviations {
				w.precompileOwnDeviation(d, pm)
			}
		}
	}
	// set not-supported flags for all the deviations
	for _, dev := range w.devs.items {
		u := slices.IndexFunc(dev.devs, func(d *parser.Deviation) bool {
			return slices.ContainsFunc(d.Deviates, func(dv *parser.Deviate) bool { return dv.Mod == "not-supported" })
		})
		if u >= 0 && len(dev.devs) > 1 {
			orig := w.path.cur
			w.path.cur = dev.pms[u].mod
			w.path.update(nil, "{deviation}")
			w.path.update(nil, dev.nid.str)
			err := w.errf(ly.Semantics, "Multiple deviations of \"%s\" with one of them being \"not-supported\".", dev.nid.str)
			w.path.pop()
			w.path.pop()
			w.path.cur = orig
			return err
		}
		dev.notSupported = u >= 0
	}
	return nil
}

// precompileOwnDeviation is lys_precompile_own_deviation for d written in pm.
func (w *nodeCtx) precompileOwnDeviation(d *parser.Deviation, pm *pmod) {
	e, msg := lyxp.Lex(d.Nodeid)
	if msg != "" || len(e.Toks) == 0 {
		return // reported when the deviating module was implemented
	}
	nid := precompileNodeid(e)
	if pm.resolve(nid.prefix[0]) != w.cur {
		return // deviation for another module
	}
	// try to find the node in already compiled deviations (only the same names can match)
	var dev *devSet
	never := func(string) bool { return false }
	for _, o := range w.devs.mergeCandidates(nid, never, &w.c.work) {
		if absNodeidMatch2(nid, pm, o.nid, o.pms[0]) {
			dev = o
			break
		}
	}
	if dev == nil {
		dev = &devSet{nid: nid}
		w.devs.add(dev, w.nidKey(nid, pm), nid)
	}
	dev.devs, dev.pms = append(dev.devs, d), append(dev.pms, pm)
}

// absNodeidMatch2 is lys_abs_schema_nodeid_match of a written in pa with b written in pb; the
// prefixes of both were checked when their modules were implemented.
func absNodeidMatch2(a *nodeid, pa *pmod, b *nodeid, pb *pmod) bool {
	if len(a.name) != len(b.name) {
		return false
	}
	for i := range a.name {
		if a.name[i] != b.name[i] || nodeidModule(pa, a.prefix[i]) != nodeidModule(pb, b.prefix[i]) {
			return false
		}
	}
	return true
}

// nodeDeviations is the deviation loop of lys_compile_node_deviations_refines: dev is the copy
// of pn the refines made (nil if none). It returns the copy with the deviations of pn applied
// (made here when needed), or notSupported for a node deviated as not-supported.
func (w *nodeCtx) nodeDeviations(pn *parser.Node, parent *schema.Node, dev *parser.Node) (
	_ *parser.Node, notSupported bool, _ error) {
	if len(w.devs.items) == 0 {
		return dev, false, nil
	}
	sc := w.devs.scan(w.devs.keys(pnodeName(pn), parent, &w.c.work), &w.c.work)
	for i := 0; ; {
		d, j, ok := sc.next(i)
		if !ok {
			return dev, false, nil
		}
		if !w.nodeidMatch(d.nid, d.pms[0], nil, parent, pn, w.cur) {
			i = j + 1
			continue
		}
		// all the deviations for one target node are in one set: applied, it is removed
		sc.remove(d)
		if d.notSupported {
			return dev, true, nil // no more deviations
		}
		if dev == nil {
			// first deviation on this node, create a copy first (lysp_dup_single with links)
			cp := *pn
			dev = &cp
			w.pparent[dev] = w.parentOf(pn)
		}
		t := &devTarget{n: dev, pm: w.pm}
		for u, dv := range d.devs {
			if err := w.applyDeviation(dv, d.pms[u], t); err != nil {
				return nil, false, err
			}
		}
		return dev, false, nil
	}
}

// devTarget is the parsed copy a deviation changes and the (sub)module its own text is written in.
type devTarget struct {
	n  *parser.Node
	pm *pmod
}

// applyDeviation is lys_apply_deviation: the deviates of d, written in pm, change the parsed
// copy t, logged at the path of the deviation in pm's module.
func (w *nodeCtx) applyDeviation(d *parser.Deviation, pm *pmod, t *devTarget) error {
	saved, prevPm := w.path, w.pm
	w.path.init(pm.mod)
	w.pm = pm
	w.path.update(nil, "{deviation}")
	w.path.update(nil, d.Nodeid)
	defer func() {
		// the path is restored (strcpy), the log location stays here
		loc := w.path.String()
		w.path, w.pm = saved, prevPm
		w.path.loc = loc
	}()
	for _, dv := range d.Deviates {
		var err error
		switch dv.Mod {
		case "add":
			err = w.deviateAdd(dv, t)
		case "delete", "replace":
			return fmt.Errorf("%w: deviate %s of \"%s\" (U-0020)", ErrUnsupported, dv.Mod, d.Nodeid)
		default:
			return w.errf(ly.Other, "Internal error (schema_compile_amend.c:1681).") // LOGINT
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// wrongNodetype is AMEND_WRONG_NODETYPE of a deviation.
func (w *nodeCtx) wrongNodetype(t *devTarget, op, prop string) error {
	return w.errf(ly.Reference, "Invalid deviation of %s node - it is not possible to %s \"%s\" property.",
		pkindStr(t.n.Kind), op, prop)
}

// cardinality is AMEND_CHECK_CARDINALITY of a deviation (at most one value).
func (w *nodeCtx) cardinality(t *devTarget, n int, prop string) error {
	if n > 1 {
		return w.errf(ly.Semantics, "Invalid deviation of %s with too many (%d) %s properties.", pkindStr(t.n.Kind), n, prop)
	}
	return nil
}

// --- the values a deviation changes, with the (sub)module each is written in ---

// devState is what the parsed copies of deviated nodes carry beyond parser.Node: per value, the
// (sub)module a default or unique is written in (lysp_qname.mod), and the exts array once a
// deviation changed it.
type devState struct {
	dflts map[*parser.Node][]*pmod
	uniqs map[*parser.Node][]*pmod
	exts  map[*parser.Node][]extIn
}

// dfltOrigin is the (sub)module default i of pn is written in.
func (w *nodeCtx) dfltOrigin(pn *parser.Node, i int) *pmod {
	if o := w.dev.dflts[pn]; o != nil {
		return o[i]
	}
	return w.origin(dfltKey{pn})
}

// uniqueOrigin is the (sub)module unique i of pn is written in.
func (w *nodeCtx) uniqueOrigin(pn *parser.Node, i int) *pmod {
	if o := w.dev.uniqs[pn]; o != nil {
		return o[i]
	}
	return w.pm
}

// dflts are the per-value origins of the defaults of t, made on first use.
func (w *nodeCtx) dflts(t *devTarget) []*pmod {
	if o, ok := w.dev.dflts[t.n]; ok {
		return o
	}
	pm := t.pm
	if o := w.from[dfltKey{t.n}]; o != nil {
		pm = o
	}
	o := make([]*pmod, len(t.n.Defaults))
	for i := range o {
		o[i] = pm
	}
	if w.dev.dflts == nil {
		w.dev.dflts = map[*parser.Node][]*pmod{}
	}
	w.dev.dflts[t.n] = o
	return o
}

// uniqs are the per-value origins of the uniques of t, made on first use.
func (w *nodeCtx) uniqs(t *devTarget) []*pmod {
	if o, ok := w.dev.uniqs[t.n]; ok {
		return o
	}
	o := make([]*pmod, len(t.n.Uniques))
	for i := range o {
		o[i] = t.pm
	}
	if w.dev.uniqs == nil {
		w.dev.uniqs = map[*parser.Node][]*pmod{}
	}
	w.dev.uniqs[t.n] = o
	return o
}

// extIn is one item of a parsed node's exts array: the instance, the statement owning it and
// the (sub)module it is written in; node is parent_stmt & LY_STMT_NODE_MASK (an instance of the
// node itself, not of one of its substatements).
type extIn struct {
	e, owner *parser.Stmt
	pm       *pmod
	node     bool
}

// exts is the exts array of t, made on first use: its own instances, then those its refines
// added (lys_apply_refine DUP_EXTS).
func (w *nodeCtx) exts(t *devTarget) []extIn {
	if l, ok := w.dev.exts[t.n]; ok {
		return l
	}
	var l []extIn
	if own, _ := w.c.ownedExts(t.n.Stmt); len(own) > 0 {
		for _, e := range own {
			l = append(l, extIn{e, t.n.Stmt, t.pm, slices.Contains(t.n.Stmt.Subs, e)})
		}
	}
	for _, rs := range w.rfnExts[t.n] {
		arr, _ := w.c.ownedExts(rs.stmt)
		for _, e := range arr {
			l = append(l, extIn{e, rs.stmt, rs.pm, true})
		}
	}
	if w.dev.exts == nil {
		w.dev.exts = map[*parser.Node][]extIn{}
	}
	w.dev.exts[t.n] = l
	return l
}

// compileExtList compiles the exts array of a deviated node into exts (COMPILE_EXTS_GOTO of
// lys_compile_node_).
func (w *nodeCtx) compileExtList(l []extIn, n *schema.Node, exts []*schema.ExtInstance) ([]*schema.ExtInstance, error) {
	prev := w.pm
	defer func() { w.pm = prev }()
	for _, x := range l {
		w.pm = x.pm
		owner := x.owner
		if x.node && !slices.Contains(owner.Subs, x.e) {
			owner = &parser.Stmt{Subs: []*parser.Stmt{x.e}} // logged as an instance of the node
		}
		inst, err := w.compileExt(x.e, owner, n)
		switch {
		case errors.Is(err, errNot):
		case err != nil:
			return exts, err
		default:
			exts = append(exts, inst)
		}
	}
	return exts, nil
}

// unresDeviations is the deviation part of lys_compile_unres_mod: every deviation left
// unapplied is logged, then the compile fails with LY_ENOTFOUND.
func (w *nodeCtx) unresDeviations() error {
	for _, dev := range w.devs.items {
		w.path.update(nil, "{deviation}")
		w.path.update(nil, dev.nid.str)
		_ = w.errf(ly.Reference, "Deviation(s) target node \"%s\" from module \"%s\" was not found.",
			dev.nid.str, dev.pms[0].Parsed.Name)
		w.path.pop()
		w.path.pop()
	}
	if len(w.devs.items) > 0 {
		return eNotFound
	}
	return nil
}
