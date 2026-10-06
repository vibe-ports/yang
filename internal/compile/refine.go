// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile_amend.c (node-ids, uses refines and augments,
// lys_apply_refine) (BSD-3-Clause, © CESNET).

package compile

import (
	"slices"
	"strings"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// nodeid is struct lysc_nodeid: a schema node-id split into prefixes ("" = none) and names.
type nodeid struct {
	str          string
	prefix, name []string
}

// usesAug is struct lysc_augment of a uses augment (ctx->uses_augs).
type usesAug struct {
	nid     *nodeid
	pm      *pmod        // aug_pmod
	ctxNode *schema.Node // nodeid_ctx_node
	aug     *parser.Node // aug_p
	uses    *parser.Node // aug_p->parent
}

// usesRfn is struct lysc_refine (ctx->uses_rfns): every parsed refine with the same target,
// innermost uses last.
type usesRfn struct {
	nid     *nodeid
	nidPm   *pmod        // nodeid_pmod: cur_mod->parsed when collected
	ctxNode *schema.Node // nodeid_ctx_node
	uses    *parser.Node // uses_p
	rfns    []*parser.Node
	pms     []*pmod // the (sub)module each of rfns is written in (lysp_qname.mod of its values)
}

// nodeidModCheck is lys_nodeid_mod_check for a descendant schema node-id written in w.pm
// (nodeid requested): its syntax and the modules of its node tests, logged at the current path.
func (w *nodeCtx) nodeidModCheck(str string) (*nodeid, error) {
	const typ = "descendant-schema-nodeid"
	e, msg := lyxp.Lex(str)
	if msg != "" {
		_ = w.errf(ly.XPath, "%s", msg)
		return nil, w.errf(ly.SyntaxYang, "Invalid %s value \"%s\" - invalid syntax.", typ, str)
	}
	if e.Toks[0] != lyxp.TokNameTest {
		return nil, w.errf(ly.Reference, "Invalid %s value \"%s\" - name test expected instead of \"%s\".", typ, str, e.Text(0))
	}
	for i := 1; i < len(e.Toks); i += 2 {
		switch {
		case e.Toks[i] != lyxp.TokOperPath:
			return nil, w.errf(ly.Reference, "Invalid %s value \"%s\" - \"/\" expected instead of \"%s\".", typ, str, e.Text(i))
		case i+1 == len(e.Toks):
			return nil, w.errf(ly.Reference, "Invalid %s value \"%s\" - unexpected end of expression.", typ, e.Src)
		case e.Toks[i+1] != lyxp.TokNameTest:
			return nil, w.errf(ly.Reference, "Invalid %s value \"%s\" - name test expected instead of \"%s\".", typ, str, e.Text(i+1))
		}
	}
	// lys_precompile_nodeid
	nid := &nodeid{str: str}
	for i := 0; i < len(e.Toks); i += 2 {
		prefix, name, ok := strings.Cut(e.Text(i), ":")
		if !ok {
			prefix, name = "", prefix
		}
		nid.prefix, nid.name = append(nid.prefix, prefix), append(nid.name, name)
		if _, ok := w.nodeidMod(prefix, w.pm); !ok {
			return nil, eValid
		}
	}
	return nid, nil
}

// nodeidMod is lys_schema_node_get_module: the module of a node test written in pm; an unknown
// prefix is logged.
func (w *nodeCtx) nodeidMod(prefix string, pm *pmod) (*schema.Module, bool) {
	if prefix == "" || prefix == pm.Parsed.Prefix {
		return pm.mod, true
	}
	for u, im := range pm.Parsed.Imports {
		if im.Prefix == prefix && u < len(pm.Imports) && pm.Imports[u] != nil {
			return pm.Imports[u].mod, true
		}
	}
	_ = w.errf(ly.Reference, "Invalid schema-nodeid nametest - prefix \"%s\" not defined in module \"%s\".", prefix, pm.Parsed.Name)
	return nil, false
}

// absNodeidMatch is lys_abs_schema_nodeid_match with both node-ids written in pm.
func (w *nodeCtx) absNodeidMatch(a, b *nodeid, pm *pmod) bool {
	if len(a.name) != len(b.name) {
		return false
	}
	for i := range a.name {
		m1, _ := w.nodeidMod(a.prefix[i], pm)
		m2, _ := w.nodeidMod(b.prefix[i], pm)
		if m1 != m2 || a.name[i] != b.name[i] {
			return false
		}
	}
	return true
}

// precompileUsesAugmentsRefines is lys_precompile_uses_augments_refines.
func (w *nodeCtx) precompileUsesAugmentsRefines(uses *parser.Node, ctxNode *schema.Node) error {
	err := w.precompileUses(uses, ctxNode)
	if err != nil {
		w.path.pop()
		w.path.pop()
	}
	return err
}

func (w *nodeCtx) precompileUses(uses *parser.Node, ctxNode *schema.Node) error {
	for _, a := range uses.Augments {
		w.path.update(nil, "{augment}")
		w.path.update(nil, a.Name)
		nid, err := w.nodeidModCheck(a.Name)
		if err != nil {
			return err
		}
		w.usesAugs.add(&usesAug{nid: nid, pm: w.pm, ctxNode: ctxNode, aug: a, uses: uses}, w.nidKey(nid, w.parsedOf(w.cur)), nid)
		w.pendingOf[uses]++
		w.path.pop()
		w.path.pop()
	}
	for _, r := range uses.Refines {
		w.path.update(nil, "{refine}")
		w.path.update(nil, r.Name)
		nid, err := w.nodeidModCheck(r.Name)
		if err != nil {
			return err
		}
		// try to find the node in already compiled refines: every item of the same length is
		// compared, resolving its prefixes in w.pm (logged when undefined); the others cannot
		// match or log
		var rfn *usesRfn
		undefined := func(prefix string) bool { return prefixUndefined(w.pm, prefix) }
		for _, o := range w.usesRfns.mergeCandidates(nid, undefined, &w.c.work) {
			if w.absNodeidMatch(nid, o.nid, w.pm) {
				rfn = o
				break
			}
		}
		if rfn == nil {
			rfn = &usesRfn{nid: nid, nidPm: w.parsedOf(w.cur), ctxNode: ctxNode, uses: uses}
			w.usesRfns.add(rfn, w.nidKey(nid, rfn.nidPm), nid)
			w.pendingOf[uses]++
		}
		rfn.rfns, rfn.pms = append(rfn.rfns, r), append(rfn.pms, w.pm)
		w.path.pop()
		w.path.pop()
	}
	return nil
}

// nodeidMatch is lysp_schema_nodeid_match (no extension instances): nid written in pm against
// the parsed node pn to be compiled into module pnMod under parent, or, with pn nil, against
// parent itself.
func (w *nodeCtx) nodeidMatch(nid *nodeid, pm *pmod, ctxNode, parent *schema.Node, pn *parser.Node, pnMod *schema.Module) bool {
	i := len(nid.name) - 1
	mod, _ := w.nodeidMod(nid.prefix[i], pm)
	if pn != nil {
		if pnMod != mod || pnodeName(pn) != nid.name[i] {
			return false
		}
	} else {
		if parent.Module != mod || parent.Name != nid.name[i] {
			return false
		}
		parent = parent.Parent
	}
	for i > 0 {
		i--
		if parent == nil {
			return false // no more parents but path continues
		}
		mod, _ = w.nodeidMod(nid.prefix[i], pm)
		if parent.Module != mod || parent.Name != nid.name[i] {
			return false
		}
		parent = parent.Parent
	}
	return ctxNode == parent
}

// pnodeName is a parsed node's name: input and output are named after their keyword
// (parser_yang.c parse_inout).
func pnodeName(pn *parser.Node) string {
	if pn.Kind == "input" || pn.Kind == "output" {
		return pn.Kind
	}
	return pn.Name
}

// nidKey is the pending-set key of nid matched with the prefixes of pm: its names ("a/b"), or
// wildName when a prefix is not defined in pm (libyang logs it at every match attempt).
func (w *nodeCtx) nidKey(nid *nodeid, pm *pmod) string {
	if slices.ContainsFunc(nid.prefix, func(prefix string) bool { return prefixUndefined(pm, prefix) }) {
		return wildName
	}
	return strings.Join(nid.name, "/")
}

// prefixUndefined reports whether lys_schema_node_get_module fails (and logs) for prefix in pm.
func prefixUndefined(pm *pmod, prefix string) bool {
	return prefix != "" && prefix != pm.Parsed.Prefix &&
		!slices.ContainsFunc(pm.Parsed.Imports, func(im *parser.Import) bool { return im.Prefix == prefix })
}

// nodeRefines is the refine part of lys_compile_node_deviations_refines: a copy of pn with every
// matching refine applied, or nil when none matches (deviations: M2, U-0020).
func (w *nodeCtx) nodeRefines(pn *parser.Node, parent *schema.Node) (*parser.Node, error) {
	var dev *parser.Node
	sc := w.usesRfns.scan(w.usesRfns.keys(pnodeName(pn), parent, &w.c.work), &w.c.work)
	for i := 0; ; {
		rfn, j, ok := sc.next(i)
		if !ok {
			break
		}
		if !w.nodeidMatch(rfn.nid, rfn.nidPm, rfn.ctxNode, parent, pn, w.cur) {
			i = j + 1
			continue
		}
		if dev == nil {
			// first refine on this node, create a copy first (lysp_dup_single with links)
			cp := *pn
			dev = &cp
			w.pparent[dev] = w.parentOf(pn)
		}
		for u, r := range rfn.rfns {
			if err := w.applyRefine(r, rfn.pms[u], dev); err != nil {
				return nil, err
			}
		}
		// refine was applied, remove it; refines use relative paths so more may apply
		sc.remove(rfn)
		w.pendingOf[rfn.uses]--
		i = j
	}
	return dev, nil
}

// pkindStr is lys_nodetype2str of a parsed node.
func pkindStr(kind string) string {
	if kind == "uses" {
		return kind
	}
	if k, ok := kinds[kind]; ok {
		return lysNodetype2str(k)
	}
	return "unknown"
}

// dfltKey keys w.from for the default values of a refined node copy.
type dfltKey struct{ n *parser.Node }

// applyRefine is lys_apply_refine: rfn written in pm changes the parsed copy t. The values it
// adds keep pm as their module (lysp_qname.mod, lysp_restr.arg.mod) in w.from.
func (w *nodeCtx) applyRefine(rfn *parser.Node, pm *pmod, t *parser.Node) error {
	// libyang switches cur_mod/pmod to rfn->nodeid_pmod, the module being compiled: only the
	// log path could see it, and the special segments below do not print a module
	w.path.update(nil, "{refine}")
	w.path.update(nil, rfn.Name)
	err := w.refineNode(rfn, pm, t)
	w.path.pop()
	w.path.pop()
	return err
}

func (w *nodeCtx) refineNode(rfn *parser.Node, pm *pmod, t *parser.Node) error {
	wrong := func(op, prop string) error {
		return w.errf(ly.Reference, "Invalid refine of %s node - it is not possible to %s \"%s\" property.", pkindStr(t.Kind), op, prop)
	}
	cardinality := func(prop string) error {
		return w.errf(ly.Semantics, "Invalid refine of %s with too many (%d) %s properties.", pkindStr(t.Kind), len(rfn.Defaults), prop)
	}
	if len(rfn.Defaults) > 0 {
		switch t.Kind {
		case "leaf", "choice":
			if len(rfn.Defaults) > 1 {
				return cardinality("default")
			}
		case "leaf-list":
			if !pm.v11() {
				return w.errf(ly.Semantics, "Invalid refine of default in leaf-list - the default statement is allowed only in YANG 1.1 modules.")
			}
		default:
			return wrong("replace", "default")
		}
		t.Defaults = slices.Clone(rfn.Defaults)
		w.from[dfltKey{t}] = pm
	}
	if rfn.Description != "" {
		t.Description = rfn.Description
	}
	if rfn.Reference != "" {
		t.Reference = rfn.Reference
	}
	if rfn.Config != nil {
		if w.opts&optNoConfig != 0 {
			where := "a subtree ignoring config"
			switch {
			case w.opts&(optIsInput|optIsOutput) != 0:
				where = "RPC/action"
			case w.opts&optIsNotif != 0:
				where = "notification"
			}
			w.c.warn("Refining config inside %s has no effect (%s).", where, w.path.String())
		} else {
			t.Config = rfn.Config
		}
	}
	if rfn.Mandatory != nil {
		switch t.Kind {
		case "leaf", "choice", "anydata", "anyxml":
		default:
			return wrong("replace", "mandatory")
		}
		t.Mandatory = rfn.Mandatory
	}
	if rfn.Presence != nil {
		if t.Kind != "container" {
			return wrong("replace", "presence")
		}
		t.Presence = rfn.Presence
	}
	if len(rfn.Musts) > 0 {
		switch t.Kind {
		case "container", "list", "leaf", "leaf-list", "anydata", "anyxml":
		default:
			return wrong("add", "must")
		}
		t.Musts = append(slices.Clip(t.Musts), rfn.Musts...)
		for _, m := range rfn.Musts {
			w.from[m] = pm
		}
	}
	if rfn.MinElements != nil {
		if t.Kind != "leaf-list" && t.Kind != "list" {
			return wrong("replace", "min-elements")
		}
		t.MinElements = rfn.MinElements
	}
	if rfn.MaxElements != nil {
		if t.Kind != "leaf-list" && t.Kind != "list" {
			return wrong("replace", "max-elements")
		}
		t.MaxElements = rfn.MaxElements
	}
	if len(rfn.IfFeatures) > 0 {
		switch t.Kind {
		case "leaf", "leaf-list", "list", "container", "choice", "case", "anydata", "anyxml":
		default:
			return wrong("add", "if-feature")
		}
		t.IfFeatures = append(slices.Clip(t.IfFeatures), rfn.IfFeatures...)
		for _, f := range rfn.IfFeatures {
			w.from[f] = pm
		}
	}
	// extension instances (design 06 C4b compiles them)
	t.Exts = append(slices.Clip(t.Exts), rfn.Exts...)
	return nil
}

// origin is the (sub)module a must, if-feature or dfltKey of a refined node was written in
// (lysp_restr.arg.mod, lysp_qname.mod); anything else is written in w.pm.
func (w *nodeCtx) origin(key any) *pmod {
	if pm := w.from[key]; pm != nil {
		return pm
	}
	return w.pm
}
