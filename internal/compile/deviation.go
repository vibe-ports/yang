// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile_amend.c (lys_precompile_own_deviations,
// lys_precompile_own_deviation, the deviation loop of lys_compile_node_deviations_refines,
// lys_apply_deviation) and src/schema_compile.c (the deviation part of lys_compile_unres_mod)
// (BSD-3-Clause, © CESNET).

package compile

import (
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
		for u, dv := range d.devs {
			if err := w.applyDeviation(dv, d.pms[u], dev); err != nil {
				return nil, false, err
			}
		}
		return dev, false, nil
	}
}

// applyDeviation is lys_apply_deviation: the deviates of d, written in pm, change the parsed
// copy, logged at the path of the deviation in pm's module.
func (w *nodeCtx) applyDeviation(d *parser.Deviation, pm *pmod, _ *parser.Node) error {
	saved, prevPm := w.path, w.pm
	defer func() { w.path, w.pm = saved, prevPm }()
	w.path.init(pm.mod)
	w.pm = pm
	w.path.update(nil, "{deviation}")
	w.path.update(nil, d.Nodeid)
	for _, dv := range d.Deviates {
		switch dv.Mod {
		case "add", "delete", "replace":
			return fmt.Errorf("%w: deviate %s of \"%s\" (U-0020)", ErrUnsupported, dv.Mod, d.Nodeid)
		default:
			return w.errf(ly.Other, "Internal error (schema_compile_amend.c:1681).") // LOGINT
		}
	}
	return nil
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
