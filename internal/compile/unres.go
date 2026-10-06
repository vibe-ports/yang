// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile.c (lys_compile_unres_depset,
// lys_compile_unres_leafref, lys_compile_leafref_circ_check, lys_compile_unres_dflt,
// lys_compile_unres_leaf_dlft, lys_compile_unres_llist_dflts, lys_compile_unres_disabled_bitenum,
// lys_type_leafref_next) and src/schema_compile_node.c (lysc_unres_*_add) (BSD-3-Clause, © CESNET).

package compile

import (
	"errors"
	"slices"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// unresSets are the sets of struct lys_depset_unres this port fills (design 06 §2.16); the
// disabled nodes are Context.disabled.
type unresSets struct {
	leafrefs, disabledLeafrefs []unresLref
	whens                      []unresWhen
	musts                      []unresMust
	bitenums                   []*schema.Node // leaves whose type has bits/enums (disabled_bitenums)
	dflts                      []*schema.Node // leaves and leaf-lists with a default, once each
}

// unresLref is struct lysc_unres_leafref: the leaf and the (sub)module its type is written in.
type unresLref struct {
	node  *schema.Node
	local *pmod
}

// --- lysc_unres_*_add, called by the node walk ---

// addTypeUnres is the unres part of lys_compile_node_type: the typedef default (dflt), the
// leafrefs and the bits/enumerations of n's type, whose type statement is written in pm.
func (w *nodeCtx) addTypeUnres(n *schema.Node, pm *pmod, dflt bool) {
	if dflt {
		w.addDflt(n)
	}
	if w.opts&optGrouping == 0 && leafrefNext(n.Type, 0) >= 0 { // lysc_unres_leafref_add
		l := unresLref{n, pm}
		if w.opts&optDisabled != 0 {
			w.c.ur.disabledLeafrefs = append(w.c.ur.disabledLeafrefs, l)
		} else {
			w.c.ur.leafrefs = append(w.c.ur.leafrefs, l)
		}
	}
	if w.opts&(optDisabled|optGrouping) == 0 && slices.ContainsFunc(members(n.Type), func(t *schema.Type) bool {
		return t.Base == schema.Bits || t.Base == schema.Enumeration
	}) {
		w.c.ur.bitenums = append(w.c.ur.bitenums, n) // lysc_unres_bitenum_add
	}
}

// addDflt is lysc_unres_leaf_dflt_add / lysc_unres_llist_dflts_add: a node is in the set once,
// a later default replaces the earlier one (here n.Default itself) at its place.
func (w *nodeCtx) addDflt(n *schema.Node) {
	if w.opts&(optDisabled|optGrouping) == 0 && !slices.Contains(w.c.ur.dflts, n) {
		w.c.ur.dflts = append(w.c.ur.dflts, n)
	}
}

// members are a type's union members, or the type itself.
func members(t *schema.Type) []*schema.Type {
	if t.Base == schema.Union {
		return t.Union
	}
	return []*schema.Type{t}
}

// leafrefNext is lys_type_leafref_next: the index of the next leafref of t from i on, -1 if none.
func leafrefNext(t *schema.Type, i int) int {
	for ms := members(t); i < len(ms); i++ {
		if ms[i].Base == schema.Leafref {
			return i
		}
	}
	return -1
}

func leafrefs(n *schema.Node) []*schema.Type {
	var out []*schema.Type
	ms := members(n.Type)
	for i := leafrefNext(n.Type, 0); i >= 0; i = leafrefNext(n.Type, i+1) {
		out = append(out, ms[i])
	}
	return out
}

// --- lys_compile_unres_depset ---

// unres is lys_compile_unres_depset for the sets of the dep set (design 06 P6): leafrefs
// (disabled ones LIFO, then two FIFO rounds), whens LIFO, musts LIFO, bits/enums LIFO, defaults
// LIFO, then the disabled nodes are removed and leafrefs re-checked against them. First the
// modules leafrefs name are implemented (unresImplement), which may return LY_ERECOMPILE.
// With LY_CTX_REF_IMPLEMENTED a when, must or default may implement more modules, whose new
// items are resolved by the resolve_all restart.
func (c *Context) unres() error {
	if c.unresHook != nil { // a test raising what unres can (LY_ERECOMPILE, errors)
		if err := c.unresHook(c); err != nil {
			return err
		}
	}
	ur := &c.ur
	defer func() { c.ur, c.disabled = unresSets{}, nil }() // lys_compile_unres_depset_erase
	// resolve_all: implementing a module (a leafref target, LY_CTX_REF_IMPLEMENTED) compiles it,
	// which adds to the sets; they are resolved again until nothing new comes
	processed := 0
	for {
		if err := c.unresImplement(); err != nil {
			return err
		}
		c.identitiesDisabled() // the identity values when/must and defaults are checked against
		for len(ur.disabledLeafrefs) > 0 {
			l := ur.disabledLeafrefs[len(ur.disabledLeafrefs)-1]
			for _, t := range leafrefs(l.node) {
				if err := c.unresLeafref(l.node, t, l.local); err != nil {
					return err
				}
			}
			ur.disabledLeafrefs = ur.disabledLeafrefs[:len(ur.disabledLeafrefs)-1]
		}
		for _, l := range ur.leafrefs[processed:] {
			for _, t := range leafrefs(l.node) {
				if err := c.unresLeafref(l.node, t, l.local); err != nil {
					return err
				}
			}
		}
		for _, l := range ur.leafrefs[processed:] { // store the first non-leafref type of the chain
			for _, t := range leafrefs(l.node) {
				rt := t.Realtype
				for rt.Base == schema.Leafref {
					rt = rt.Realtype
				}
				c.typeCache.release(t.Realtype)
				t.Realtype = rt
				c.typeCache.hold(rt)
			}
		}
		processed = len(ur.leafrefs)
		for len(ur.whens) > 0 {
			w := ur.whens[len(ur.whens)-1]
			if err := c.unresWhen(w.when, w.node); err != nil {
				return err
			}
			ur.whens = ur.whens[:len(ur.whens)-1]
		}
		for len(ur.musts) > 0 {
			if err := c.unresMusts(ur.musts[len(ur.musts)-1]); err != nil {
				return err
			}
			ur.musts = ur.musts[:len(ur.musts)-1]
		}
		for len(ur.bitenums) > 0 {
			n := ur.bitenums[len(ur.bitenums)-1]
			if err := c.unresBitenum(n); err != nil {
				return err
			}
			ur.bitenums = ur.bitenums[:len(ur.bitenums)-1]
		}
		for len(ur.dflts) > 0 {
			n := ur.dflts[len(ur.dflts)-1]
			if err := c.unresDflts(n); err != nil {
				return err
			}
			ur.dflts = ur.dflts[:len(ur.dflts)-1]
		}
		if processed == len(ur.leafrefs) && len(ur.disabledLeafrefs) == 0 && len(ur.whens) == 0 &&
			len(ur.musts) == 0 && len(ur.dflts) == 0 {
			break
		}
	}
	if err := c.removeDisabled(); err != nil {
		return err
	}
	for _, l := range ur.leafrefs { // the target must not have been disabled
		for _, t := range leafrefs(l.node) {
			if _, ok := c.compileLeafref(l.node, t); !ok {
				return c.logPath(ly.Reference, l.node.LogPath(),
					"Target of leafref \"%s\" cannot be referenced because it is disabled.", l.node.Name)
			}
		}
	}
	return nil
}

// compileLeafref is ly_path_compile_leafref for the leafref type t of node, its errors logged
// (the skipped deref() members first, D-0042/D-0043); ok false when it failed.
func (c *Context) compileLeafref(node *schema.Node, t *schema.Type) (types.Path, bool) {
	e, msg := lyxp.ParsePath(t.Path, lyxp.Opts{Begin: lyxp.BeginEither, Prefix: lyxp.PrefixOptional,
		Pred: lyxp.PredLeafref, Leafref: true, Extended: c.opts.LeafrefExtended})
	if msg != "" { // the parser checked the path already
		_ = c.logPath(ly.XPath, node.LogPath(), "%s", msg)
		return nil, false
	}
	t.PathExtended = c.opts.LeafrefExtended
	p, logged, err := types.CompileLeafref(node, e, t.Prefixes, node.InOutput(), t.PathExtended)
	if err != nil {
		logged = append(logged, err)
	}
	for _, l := range logged {
		path := ""
		if l.Node != nil {
			path = l.Node.LogPath()
		}
		_ = c.logPath(ly.XPath, path, "%s", l.Msg)
	}
	return p, err == nil
}

// unresLeafref is lys_compile_unres_leafref.
func (c *Context) unresLeafref(node *schema.Node, t *schema.Type, local *pmod) error {
	if t.Realtype != nil {
		return nil // already resolved (a shared union typedef with a leafref)
	}
	p, ok := c.compileLeafref(node, t)
	if !ok {
		return eValid
	}
	target := p[len(p)-1].Node
	if target.Kind != schema.Leaf && target.Kind != schema.LeafList {
		return c.logPath(ly.Reference, node.LogPath(), "Invalid leafref path \"%s\" - target node is %s instead of leaf or leaf-list.",
			t.Path, lysNodetype2str(target.Kind))
	}
	st := schema.Current // a foreign definition (deviation) is always current
	if node.Module == local.mod {
		st = node.Status
	}
	if err := checkStatus(st, local.mod, node.Name, target.Status, target.Module, target.Name); err != nil {
		return c.logPath(ly.Reference, node.LogPath(), "%s", err.Error())
	}
	if t.RequireInstance && node.Config && !target.Config && !noConfig(target) {
		return c.logPath(ly.Reference, node.LogPath(), "Invalid leafref path \"%s\" - target is supposed to represent "+
			"configuration data (as the leafref does), but it does not.", t.Path)
	}
	if circular(target.Type, t) {
		return c.logPath(ly.Reference, node.LogPath(), "Invalid leafref path \"%s\" - circular chain of leafrefs detected.", t.Path)
	}
	t.Realtype = target.Type
	t.PathCompiled = p
	c.typeCache.hold(t.Realtype)
	return nil
}

// noConfig reports a node without config flags (inside an operation or notification): libyang
// tests LYS_CONFIG_R there, which such a node does not have.
func noConfig(n *schema.Node) bool {
	for p := n; p != nil; p = p.Parent {
		switch p.Kind {
		case schema.RPC, schema.Action, schema.Notification, schema.Input, schema.Output:
			return true
		}
	}
	return false
}

// circular is lys_compile_leafref_circ_check: lref reachable from t through resolved realtypes
// and union members (iterative; an unresolved realtype ends a branch).
func circular(t, lref *schema.Type) bool {
	for work := []*schema.Type{t}; len(work) > 0; {
		t, work = work[len(work)-1], work[:len(work)-1]
		switch {
		case t == nil:
		case t == lref:
			return true
		case t.Base == schema.Leafref:
			work = append(work, t.Realtype)
		case t.Base == schema.Union:
			work = append(work, t.Union...)
		}
	}
	return false
}

// unresBitenum is lys_compile_unres_disabled_bitenum. The type object itself may be held by
// several leaves (typedef cache), which all see the removal, as in libyang; its item list is
// replaced by a new slice rather than edited, so a reader of the old slice is not disturbed.
// Published snapshots are deep copies (Snapshot) and never see either.
func (c *Context) unresBitenum(n *schema.Node) error {
	has := false
	for _, t := range members(n.Type) {
		switch t.Base {
		case schema.Enumeration:
			if slices.ContainsFunc(t.Enums, func(e *schema.Enum) bool { return e.Disabled }) {
				t.Enums = slices.DeleteFunc(slices.Clone(t.Enums), func(e *schema.Enum) bool { return e.Disabled })
			}
			has = has || len(t.Enums) > 0
		case schema.Bits:
			if slices.ContainsFunc(t.Bits, func(b *schema.Bit) bool { return b.Disabled }) {
				t.Bits = slices.DeleteFunc(slices.Clone(t.Bits), func(b *schema.Bit) bool { return b.Disabled })
			}
			has = has || len(t.Bits) > 0
		default:
			has = true
		}
	}
	if !has {
		return c.logPath(ly.Semantics, n.LogPath(), "Node \"%s\" without any (or all disabled) valid values.", n.Name)
	}
	return nil
}

// unresDflts is lys_compile_unres_leaf_dlft / lys_compile_unres_llist_dflts: each default is
// stored through the type (lys_compile_unres_dflt); a key or mandatory leaf drops its default.
func (c *Context) unresDflts(n *schema.Node) error {
	if n.Kind == schema.Leaf && (n.Mandatory || n.IsKey()) {
		n.Default = nil
		return nil
	}
	for _, d := range n.Default {
		// LY_EINCOMPLETE (a value that needs the data tree) is success
		var implErr error
		var diag *types.Diag
		if c.opts.RefImplemented { // LYPLG_TYPE_STORE_IMPLEMENT
			_, diag = types.StoreImplement(n.Type, d.Lex, types.FormatSchema, types.HintSchema, d.NS, n,
				func(m *schema.Module, importFeatures bool) error {
					err := c.implementRef(m, importFeatures)
					// identityref: lyplg_type_make_implemented's rc is the store's; an
					// instance-identifier makes it LY_EVALID (lyplg_type_lypath_new), except for
					// what the port cannot do (ErrUnsupported) or must stop (ErrBudget)
					if !importFeatures || errors.Is(err, ErrUnsupported) || errors.Is(err, ErrBudget) {
						implErr = err
					}
					return err
				})
		} else {
			_, diag = types.Store(n.Type, d.Lex, types.FormatSchema, types.HintSchema, d.NS, n)
		}
		if errors.Is(implErr, errRecompile) || errors.Is(implErr, ErrBudget) || errors.Is(implErr, ErrUnsupported) {
			return implErr // LY_ERECOMPILE, budget
		}
		if diag != nil {
			if diag.Msg == "" {
				return c.logPath(ly.Semantics, n.LogPath(), "Invalid default - value does not fit the type.")
			}
			return c.logPath(ly.Semantics, n.LogPath(), "Invalid default - value does not fit the type (%s).", diag.Msg)
		}
	}
	if n.Kind == schema.LeafList && n.Config {
		for u := range n.Default { // lexical, not canonical, comparison
			for v := range u {
				if n.Default[u].Lex == n.Default[v].Lex {
					return c.logPath(ly.Semantics, n.LogPath(), "Configuration leaf-list has multiple defaults of the same value \"%s\".",
						n.Default[u].Lex)
				}
			}
		}
	}
	return nil
}

// identitiesDisabled sets Identity.Disabled = lys_identity_iffeature_value with the current
// features, for every loaded module (libyang evaluates it when an identityref value is stored;
// the flag is what internal/types reads). An if-feature that does not compile leaves the
// identity enabled: P0 compiles only the if-features of features.
func (c *Context) identitiesDisabled() {
	for _, m := range c.Modules {
		if m.Schema == nil {
			continue
		}
		pms := []*pmod{&m.pmod}
		for _, inc := range m.Includes {
			pms = append(pms, &inc.Sub.pmod)
		}
		for _, pm := range pms {
			for _, p := range pm.Parsed.Identities {
				id := m.Schema.Identity(p.Name)
				if id == nil {
					continue
				}
				id.Disabled = false
				for _, iff := range p.IfFeatures {
					if iff.Err != nil {
						break
					}
					var fs []*feature
					for _, n := range iffNames(iff.AST) {
						if f := c.featureFind(pm, m, n, true); f != nil {
							fs = append(fs, f)
						}
					}
					if len(fs) == len(iffNames(iff.AST)) && !iffValue(iff.AST, fs) {
						id.Disabled = true
						break
					}
				}
			}
		}
	}
}
