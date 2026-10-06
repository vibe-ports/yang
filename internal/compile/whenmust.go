// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile.c (lys_compile_unres_when,
// lys_compile_unres_when_cyclic, lys_compile_unres_must, the when/must part of
// lys_compile_unres_depset_implement and lys_compile_expr_implement) and src/xpath.c
// (lyxp_set_scnode_merge, lyxp_set_scnode_insert_node) (BSD-3-Clause, © CESNET).

package compile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
)

// unresWhen is struct lysc_unres_when; unresMust is struct lysc_unres_must (local: the (sub)module
// each must of node is written in).
type unresWhen struct {
	when *schema.When
	node *schema.Node
}

type unresMust struct {
	node  *schema.Node
	local []*pmod
}

// addWhen is lysc_unres_when_add: whens in groupings and disabled data are never checked.
func (w *nodeCtx) addWhen(wh *schema.When, n *schema.Node) {
	if w.opts&(optGrouping|optDisabled) == 0 {
		w.c.ur.whens = append(w.c.ur.whens, unresWhen{wh, n})
	}
}

// addMusts is lysc_unres_must_add for the musts of n, all written in w.pm.
func (w *nodeCtx) addMusts(n *schema.Node) {
	if w.opts&(optGrouping|optDisabled) != 0 || len(n.Musts) == 0 {
		return
	}
	m := unresMust{node: n}
	for range n.Musts {
		m.local = append(m.local, w.pm)
	}
	w.c.ur.musts = append(w.c.ur.musts, m)
}

// unresImplement is lys_compile_unres_depset_implement: the modules named by leafref paths
// (disabled ones included: their target must exist) are implemented and compiled; a when or must
// naming a module that is not implemented is not checked, with a warning (LY_CTX_REF_IMPLEMENTED
// is unsupported). Compiling a module adds to the sets, so this runs until they are stable.
// Implementing may need the dep set compiled again (LY_ERECOMPILE).
func (c *Context) unresImplement() error {
	ur := &c.ur
	di, li, wi, mi := 0, 0, 0, 0
	for {
		for ; di < len(ur.disabledLeafrefs); di++ {
			if err := c.implementLeafrefs(ur.disabledLeafrefs[di].node); err != nil {
				return err
			}
		}
		for ; li < len(ur.leafrefs); li++ {
			if err := c.implementLeafrefs(ur.leafrefs[li].node); err != nil {
				return err
			}
		}
		for wi < len(ur.whens) {
			w := ur.whens[wi]
			if m := exprModules(w.when.Src, w.when.Ctx, true); m != nil {
				c.warn("When condition \"%s\" check skipped because referenced module \"%s\" is not implemented.", w.when.Src, m[0].Name)
				ur.whens[wi] = ur.whens[len(ur.whens)-1] // ly_set_rm_index
				ur.whens = ur.whens[:len(ur.whens)-1]
				continue
			}
			wi++
		}
		for mi < len(ur.musts) {
			skip := false
			for _, must := range ur.musts[mi].node.Musts {
				if m := exprModules(must.Src, must.Ctx, true); m != nil {
					c.warn("Must condition \"%s\" check skipped because referenced module \"%s\" is not implemented.", must.Src, m[0].Name)
					skip = true
				}
			}
			if skip {
				ur.musts[mi] = ur.musts[len(ur.musts)-1]
				ur.musts = ur.musts[:len(ur.musts)-1]
				continue
			}
			mi++
		}
		if di == len(ur.disabledLeafrefs) && li == len(ur.leafrefs) && wi == len(ur.whens) {
			return nil
		}
	}
}

// implementLeafrefs is lys_compile_expr_implement(implement 1) for the leafref paths of n.
func (c *Context) implementLeafrefs(n *schema.Node) error {
	for _, t := range leafrefs(n) {
		for _, sm := range exprModules(t.Path, t.Prefixes, false) {
			var m *Module
			for _, lm := range c.Modules {
				if lm.Schema == sm {
					m = lm
				}
			}
			if m == nil {
				continue
			}
			if !m.Implemented {
				if err := c.implement(m, c.importFeatures()); err != nil {
					return err
				}
			}
			if !m.compiled {
				if err := c.compile(m); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// exprModules are the modules bound to the prefixes of the name tests and literals of src
// (lys_compile_expr_implement); with firstMissing only the first one that is not implemented,
// nil if there is none.
func exprModules(src string, ns schema.NSCtx, firstMissing bool) []*schema.Module {
	e, msg := lyxp.Lex(src)
	if msg != "" {
		return nil
	}
	var out []*schema.Module
	for i, tk := range e.Toks {
		if tk != lyxp.TokNameTest && tk != lyxp.TokLiteral {
			continue
		}
		tok := e.Src[e.Pos[i] : e.Pos[i]+e.Len[i]]
		j := strings.IndexByte(tok, ':')
		m := ns[tok[:max(j, 0)]]
		switch {
		case j <= 0 || m == nil: // no prefix, or an unknown one: not cared about now
		case !firstMissing:
			out = append(out, m)
		case !m.Implemented:
			return []*schema.Module{m}
		}
	}
	return out
}

// isOutput is LYS_IS_OUTPUT: a node inside an output, the output node itself excluded.
func isOutput(n *schema.Node) bool { return n.Kind != schema.Output && n.InOutput() }

// atomize is lyxp_atomize over the compiled schema with LYXP_SCNODE_SCHEMA; an XPath error is
// logged at n (libyang's LOGVAL from inside the walk). Warnings go to the diagnostics.
func (c *Context) atomize(e any, ctxNode, n *schema.Node, output bool) ([]xpath.Atom, error) {
	ex, ok := e.(*xpath.Expr)
	if !ok {
		return nil, fmt.Errorf("compile: condition of %s not compiled", n.LogPath())
	}
	atoms, err := ex.Atomize(xpath.AtomizeContext{Node: wrap(ctxNode), SchemaRules: true, Output: output,
		Schema: schemaInfo{c}, Warn: func(msg string) { c.warn("%s", msg) }, Steps: &c.xpathSteps})
	var xe *xpath.Error
	switch {
	case errors.Is(err, xpath.ErrBudget):
		return nil, fmt.Errorf("%w: the when/must checks of one Load need more than %d XPath steps (at %s)", ErrBudget,
			orDefault(c.opts.Budget.MaxXPathSteps, xpath.DefaultMaxSteps), n.LogPath())
	case errors.As(err, &xe):
		code := ly.XPath
		for k := ly.Success; k <= ly.Other; k++ {
			if k.String() == xe.VECode {
				code = k
			}
		}
		_ = c.logPath(code, n.LogPath(), "%s", xe.Msg)
		return nil, eValid
	}
	return atoms, err
}

// unresWhen is lys_compile_unres_when.
func (c *Context) unresWhen(wh *schema.When, n *schema.Node) error {
	set, err := c.atomize(wh.Compiled, wh.ContextNode, n, isOutput(n))
	if err != nil {
		if errors.Is(err, eValid) {
			return c.logPath(ly.Semantics, n.LogPath(), "Invalid when condition \"%s\".", wh.Src)
		}
		return err
	}
	for _, a := range set {
		if a.Node == nil || a.Use == xpath.AtomStartUsed {
			continue // roots, the context node not actually traversed
		}
		s := unwrap(a.Node)
		if checkStatus(wh.Status, n.Module, n.Name, s.Status, s.Module, s.Name) != nil {
			c.warn("When condition \"%s\" may be referencing %s node \"%s\".", wh.Src, statusWord(s), s.Name)
		}
		switch {
		case s.DataParent() == n:
			return c.logPath(ly.Semantics, n.LogPath(), "When condition is accessing its own conditional node children.")
		case s == n && a.Use == xpath.AtomVal:
			return c.logPath(ly.Semantics, n.LogPath(), "When condition is accessing its own conditional node value.")
		}
	}
	set = dependent(set, wh, n)
	return c.whenCyclic(set)
}

// statusWord is the "%s" of the status warnings: libyang compares all of the node's flags with
// LYS_STATUS_OBSLT, so only a node without any other flag (an rpc, action or notification) is
// called obsolete, every other one deprecated.
func statusWord(s *schema.Node) string {
	if s.Status == schema.Obsolete && (s.Kind == schema.RPC || s.Kind == schema.Action || s.Kind == schema.Notification) {
		return "obsolete"
	}
	return "deprecated"
}

// dependent: when the when is not the node's own (it comes from a uses, augment or a choice/case
// parent), the node depends on it instead of the context node (SC:528-539).
func dependent(set []xpath.Atom, wh *schema.When, n *schema.Node) []xpath.Atom {
	if wh.ContextNode == n {
		return set
	}
	if set[0].Use == xpath.AtomStartUsed {
		set[0].Node = wrap(n) // replace the non-traversed context node
		return set
	}
	return scnodeInsert(set, wrap(n))
}

// scnodeInsert is lyxp_set_scnode_insert_node for an element: whatever use the caller asks for,
// the node is (re)marked as in context.
func scnodeInsert(set []xpath.Atom, n xpath.SchemaNode) []xpath.Atom {
	for i := range set {
		if set[i].Node == n {
			set[i].Use = xpath.AtomCtx
			return set
		}
	}
	return append(set, xpath.Atom{Node: n, Use: xpath.AtomCtx})
}

// scnodeMerge is lyxp_set_scnode_merge: nodes of b missing from a are appended; a node in both
// takes b's use when a's is START_USED, or when a's is NODE and b's VAL.
func scnodeMerge(a, b []xpath.Atom) []xpath.Atom {
	orig := len(a)
	for _, x := range b {
		j := 0
		for j < orig && a[j].Node != x.Node {
			j++
		}
		switch {
		case j == orig:
			a = append(a, x)
		case a[j].Use == xpath.AtomStartUsed, a[j].Use == xpath.AtomNode && x.Use == xpath.AtomVal:
			a[j].Use = x.Use
		}
	}
	return a
}

// whenCyclic is lys_compile_unres_when_cyclic, iterative over the growing set: the whens of every
// node the condition reaches (and of their choice/case parents) are atomized, without
// LYXP_SCNODE_OUTPUT, and a node reached back that is the starting node is a cycle.
func (c *Context) whenCyclic(set []xpath.Atom) error {
	for i := range set {
		if set[i].Use != xpath.AtomStartUsed {
			set[i].Use = xpath.AtomCtx // check its when, skip the context node (just checked)
		}
	}
	for i := 0; i < len(set); i++ {
		if set[i].Use != xpath.AtomCtx {
			continue
		}
		if set[i].Node == nil || len(unwrap(set[i].Node).Whens) == 0 {
			set[i].Use = xpath.AtomNode
			continue
		}
		node := unwrap(set[i].Node)
		for {
			for _, wh := range node.Whens {
				tmp, err := c.atomize(wh.Compiled, wh.ContextNode, node, false)
				if err != nil {
					if errors.Is(err, eValid) {
						return c.logPath(ly.Semantics, node.LogPath(), "Invalid when condition \"%s\".", wh.Src)
					}
					return err
				}
				for j := range tmp {
					if tmp[j].Node == nil {
						tmp[j].Use = xpath.AtomNode // roots: no when, nothing to check
						continue
					}
					for _, x := range set {
						if x.Node == tmp[j].Node && x.Use == xpath.AtomStartUsed {
							return c.logPath(ly.Semantics, node.LogPath(), "When condition cyclic dependency on the node \"%s\".",
								unwrap(tmp[j].Node).Name)
						}
					}
					tmp[j].Use = xpath.AtomCtx // needs to be checked; in both sets it is ignored
				}
				tmp = dependent(tmp, wh, node)
				set = scnodeMerge(set, tmp)
			}
			if node = node.Parent; node == nil || node.Kind != schema.Case && node.Kind != schema.Choice {
				break
			}
		}
		set[i].Use = xpath.AtomNode
	}
	return nil
}

// unresMusts is lys_compile_unres_must.
func (c *Context) unresMusts(m unresMust) error {
	n := m.node
	for u, must := range n.Musts {
		set, err := c.atomize(must.Compiled, n, n, isOutput(n))
		if err != nil {
			if errors.Is(err, eValid) {
				return c.logPath(ly.Semantics, n.LogPath(), "Invalid must condition \"%s\".", must.Src)
			}
			return err
		}
		st := schema.Current // a foreign definition (deviation, refine) is always current
		if m.local[u].mod == n.Module {
			st = n.Status
		}
		for _, a := range set {
			if a.Node == nil {
				continue
			}
			s := unwrap(a.Node)
			if checkStatus(st, m.local[u].mod, n.Name, s.Status, s.Module, s.Name) != nil {
				c.warn("Must condition \"%s\" may be referencing %s node \"%s\".", must.Src, statusWord(s), s.Name)
				break
			}
		}
	}
	return nil
}

// --- the compiled schema as xpath.SchemaNode / xpath.SchemaInfo ---

// snode adapts a compiled node; equal nodes give equal values.
type snode struct{ n *schema.Node }

func wrap(n *schema.Node) xpath.SchemaNode {
	if n == nil {
		return nil
	}
	return snode{n}
}

func unwrap(s xpath.SchemaNode) *schema.Node { return s.(snode).n }

var xpathKinds = map[schema.Kind]xpath.Kind{
	schema.Container: xpath.KindContainer, schema.List: xpath.KindList, schema.Leaf: xpath.KindLeaf,
	schema.LeafList: xpath.KindLeafList, schema.AnyData: xpath.KindAnydata, schema.AnyXML: xpath.KindAnyxml,
	schema.RPC: xpath.KindRPC, schema.Action: xpath.KindAction, schema.Notification: xpath.KindNotif,
	schema.Choice: xpath.KindChoice, schema.Case: xpath.KindCase, schema.Input: xpath.KindInput,
	schema.Output: xpath.KindOutput,
}

func (s snode) Kind() xpath.Kind  { return xpathKinds[s.n.Kind] }
func (s snode) Name() string      { return s.n.Name }
func (s snode) Module() string    { return s.n.Module.Name }
func (s snode) Namespace() string { return s.n.Module.Namespace }
func (s snode) Path() string      { return s.n.LogPath() }

// Config is false only for LYS_CONFIG_R: nodes inside operations have no config flag.
func (s snode) Config() bool { return s.n.Config || noConfig(s.n) }

func (s snode) Keys() []string {
	var ks []string
	for _, k := range s.n.Keys {
		ks = append(ks, k.Name)
	}
	return ks
}

func (s snode) Child(module, name string) xpath.SchemaNode {
	find := func(p *schema.Node) *schema.Node {
		for c := range schema.GetNext(p, nil, 0) {
			if c.Name == name && c.Module.Name == module {
				return c
			}
		}
		return nil
	}
	if s.n.Kind == schema.RPC || s.n.Kind == schema.Action {
		in, out := find(s.n.Children[0]), find(s.n.Children[1])
		if in != nil && out != nil {
			return nil
		}
		if in != nil {
			return wrap(in)
		}
		return wrap(out)
	}
	return wrap(find(s.n))
}

// Canonical is not needed over the schema (Atomize compares no values).
func (s snode) Canonical(string) (string, bool) { return "", false }

// Type is the leaf's type, nil for other nodes.
func (s snode) Type() xpath.SchemaType {
	if s.n.Type == nil {
		return nil
	}
	return stype{s.n.Type}
}

// CheckValue stores lexical with the prefixes of the when/must being checked (compile.CheckValue).
func (s snode) CheckValue(lexical string, pc xpath.NamespaceCtx) (string, bool) {
	var ns schema.NSCtx
	if x, ok := pc.(xpathNS); ok {
		ns = x.ctx
	}
	return CheckValue(s.n, lexical, ns)
}

// stype adapts a compiled type (xpath.BaseType has schema.BaseType's values).
type stype struct{ t *schema.Type }

func (t stype) Base() xpath.BaseType { return xpath.BaseType(t.t.Base) }

func (t stype) Union() []xpath.SchemaType {
	if t.t.Base != schema.Union {
		return nil
	}
	out := make([]xpath.SchemaType, len(t.t.Union))
	for i, m := range t.t.Union {
		out[i] = stype{m}
	}
	return out
}

func (t stype) Realtype() xpath.SchemaType {
	if t.t.Realtype == nil {
		return nil
	}
	return stype{t.t.Realtype}
}

func (s snode) Parent() xpath.SchemaNode          { return wrap(s.n.Parent) }
func (s snode) Children() []xpath.SchemaNode      { return wrapAll(s.n.Children) }
func (s snode) Actions() []xpath.SchemaNode       { return wrapAll(s.n.Actions) }
func (s snode) Notifications() []xpath.SchemaNode { return wrapAll(s.n.Notifs) }
func wrapAll(ns []*schema.Node) []xpath.SchemaNode {
	out := make([]xpath.SchemaNode, len(ns))
	for i, n := range ns {
		out[i] = snode{n}
	}
	return out
}

// LeafrefTarget is the resolved target of a leaf whose own type is a leafref.
func (s snode) LeafrefTarget() xpath.SchemaNode {
	if t := s.n.Type; t != nil && t.Base == schema.Leafref {
		if p, ok := t.PathCompiled.(types.Path); ok && len(p) > 0 {
			return wrap(p[len(p)-1].Node)
		}
	}
	return nil
}

// schemaInfo is the context-wide part: identities and the implemented modules.
type schemaInfo struct{ c *Context }

func (si schemaInfo) mod(name string) *schema.Module {
	for _, lm := range si.c.Modules {
		if lm.mod != nil && lm.Name == name && lm.Implemented {
			return lm.mod
		}
	}
	return nil
}

func (si schemaInfo) ident(id xpath.Ident) *schema.Identity {
	for _, lm := range si.c.Modules {
		if lm.mod != nil && lm.Name == id.Module {
			if x := lm.mod.Identity(id.Name); x != nil {
				return x
			}
		}
	}
	return nil
}

func (si schemaInfo) HasIdentity(id xpath.Ident) bool { return si.ident(id) != nil }

func (si schemaInfo) IsDerived(base, id xpath.Ident) bool {
	b, x := si.ident(base), si.ident(id)
	if b == nil || x == nil {
		return false
	}
	seen := map[*schema.Identity]bool{}
	for work := b.Derived; len(work) > 0; {
		d := work[len(work)-1]
		work = work[:len(work)-1]
		if d == x {
			return true
		}
		if !seen[d] {
			seen[d] = true
			work = append(work, d.Derived...)
		}
	}
	return false
}

func (si schemaInfo) TopLevel(module, name string) []xpath.SchemaNode {
	var out []xpath.SchemaNode
	for _, lm := range si.c.Modules {
		if lm.mod == nil || !lm.Implemented || module != "" && lm.Name != module {
			continue
		}
		for n := range schema.GetNext(nil, lm.mod.Top, 0) {
			if n.Name == name && n.Kind != schema.RPC && n.Kind != schema.Notification {
				out = append(out, snode{n})
			}
		}
	}
	return out
}

func (si schemaInfo) Modules() []string {
	var out []string
	for _, lm := range si.c.Modules {
		if lm.mod != nil && lm.Implemented {
			out = append(out, lm.Name)
		}
	}
	return out
}

func (si schemaInfo) ModuleNodes(module string) (data, rpcs, notifs []xpath.SchemaNode) {
	m := si.mod(module)
	if m == nil {
		return nil, nil, nil
	}
	for _, n := range m.Top {
		switch n.Kind {
		case schema.RPC:
			rpcs = append(rpcs, snode{n})
		case schema.Notification:
			notifs = append(notifs, snode{n})
		default:
			data = append(data, snode{n})
		}
	}
	return data, rpcs, notifs
}
