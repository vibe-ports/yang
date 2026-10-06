// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile.c, src/schema_compile_amend.c,
// src/tree_schema.c and src/context.c (BSD-3-Clause, © CESNET).

package compile

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// errRecompile is LY_ERECOMPILE: a dep set must be compiled again.
var errRecompile = errors.New("LY_ERECOMPILE")

// setImplemented is _lys_set_implemented.
func (c *Context) setImplemented(m *Module, features []string) error {
	if m.Implemented {
		changed, err := c.setFeatures(m, features)
		if err == nil && changed {
			m.toCompile = true
			c.compiling = append(c.compiling, m)
		}
		return err
	}
	// recompilation always takes place later
	if err := c.implement(m, features); err != nil && !errors.Is(err, errRecompile) {
		return err
	}
	if c.opts.AllImplemented {
		for i := 0; i < len(c.creating); i++ {
			if mi := c.creating[i]; !mi.Implemented {
				if err := c.implement(mi, c.importFeatures()); err != nil && !errors.Is(err, errRecompile) {
					return err
				}
			}
		}
	}
	return nil
}

// importFeatures are the features of implicitly implemented modules.
func (c *Context) importFeatures() []string {
	if c.opts.EnableImportFeatures {
		return []string{"*"}
	}
	return nil
}

// implement is lys_implement.
func (c *Context) implement(m *Module, features []string) error {
	if o := c.implemented(m.Name); o != nil {
		return c.logErr(eDenied, "Module \"%s@%s\" is already implemented in revision \"%s\".",
			m.Name, orNone(m.Revision), orNone(o.Revision))
	}
	if _, err := c.setFeatures(m, features); err != nil {
		return err
	}
	m.Implemented, m.Schema.Implemented, m.toCompile = true, true, true
	c.compiling = append(c.compiling, m)
	c.implementing = append(c.implementing, m)
	if err := c.precompileAugmentsDeviations(m); err != nil {
		return err
	}
	return hasCompiledImport(m)
}

// hasCompiledImport is lys_has_compiled_import_r.
func hasCompiledImport(m *Module) error {
	for _, im := range m.Imports {
		if !im.Implemented {
			continue
		}
		if !im.toCompile {
			im.toCompile = true
			return errRecompile
		}
		if err := hasCompiledImport(im); err != nil {
			return err
		}
	}
	return nil
}

// precompileAugmentsDeviations is lys_precompile_augments_deviations: the
// modules targeted by top-level augments of m (and its submodules) become
// implemented, or are recompiled when already compiled. A module with
// deviations is not supported yet (U-0020).
func (c *Context) precompileAugmentsDeviations(m *Module) error {
	var set []*Module
	if err := c.precompileModAugments(m, &m.pmod, m.Name, &set); err != nil {
		return err
	}
	for _, inc := range m.Includes {
		if err := c.precompileModAugments(m, &inc.Sub.pmod, inc.Sub.Name, &set); err != nil {
			return err
		}
	}
	var ret error
	for _, t := range set {
		switch {
		case t == m: // applied normally later
		case !t.Implemented:
			if err := c.implement(t, c.importFeatures()); errors.Is(err, errRecompile) {
				ret = err
			} else if err != nil {
				return err
			}
		case t.compiled:
			t.toCompile = true
			ret = errRecompile
		}
	}
	return ret
}

// precompileModAugments is lys_precompile_mod_augments_deviations for the
// (sub)module pm (named name) of m.
func (c *Context) precompileModAugments(m *Module, pm *pmod, name string, set *[]*Module) error {
	for _, aug := range pm.Parsed.Augments {
		var mods []*Module
		path := "/" + m.Name + ":{augment='" + aug.Name + "'}"
		t, err := c.nodeidModCheck(m, pm, name, aug.Name, path, &mods)
		if err != nil {
			return err
		}
		added := !slices.Contains(t.augmentedBy, m)
		if added {
			t.augmentedBy = append(t.augmentedBy, m)
		}
		if added || slices.ContainsFunc(mods, func(x *Module) bool { return !x.Implemented }) {
			for _, x := range mods { // ly_set_merge without duplicates
				if !slices.Contains(*set, x) {
					*set = append(*set, x)
				}
			}
		}
	}
	if len(pm.Parsed.Deviations) > 0 {
		return fmt.Errorf("%w: deviations of module %q are applied in M2 (U-0020)", ErrUnsupported, m.Name)
	}
	// augments in extension instances (augment-structure) never get here: U-0023
	return nil
}

// nodeidModCheck is lys_nodeid_mod_check for an absolute schema node-id
// written in pm (named name): its syntax, and the modules of its node tests,
// added to mods without duplicates. It returns the first one.
func (c *Context) nodeidModCheck(main *Module, pm *pmod, name, nodeid, path string, mods *[]*Module) (*Module, error) {
	const typ = "absolute-schema-nodeid"
	e, msg := lyxp.Lex(nodeid)
	if msg != "" {
		_ = c.logPath(ly.XPath, path, "%s", msg)
		return nil, c.logPath(ly.SyntaxYang, path, "Invalid %s value \"%s\" - invalid syntax.", typ, nodeid)
	}
	for i := 0; i < len(e.Toks); i += 2 {
		switch {
		case e.Toks[i] != lyxp.TokOperPath:
			return nil, c.logPath(ly.Reference, path, "Invalid %s value \"%s\" - \"/\" expected instead of \"%s\".", typ, nodeid, e.Text(i))
		case i+1 == len(e.Toks):
			return nil, c.logPath(ly.Reference, path, "Invalid %s value \"%s\" - unexpected end of expression.", typ, e.Src)
		case e.Toks[i+1] != lyxp.TokNameTest:
			return nil, c.logPath(ly.Reference, path, "Invalid %s value \"%s\" - name test expected instead of \"%s\".", typ, nodeid, e.Text(i+1))
		}
	}
	var first *Module
	for i := 1; i < len(e.Toks); i += 2 {
		mod := main
		if prefix, _, ok := strings.Cut(e.Text(i), ":"); ok {
			if mod = c.prefixModule(pm, mod, prefix); mod == nil { // lys_schema_node_get_module
				return nil, c.logPath(ly.Reference, path, "Invalid schema-nodeid nametest - prefix \"%s\" not defined in module \"%s\".",
					prefix, name)
			}
		}
		if first == nil {
			first = mod
		}
		if !slices.Contains(*mods, mod) {
			*mods = append(*mods, mod)
		}
	}
	return first, nil
}

// --- dependency sets (tree_schema.c) ---

// singleDepSet is LYS_IS_SINGLE_DEP_SET.
func singleDepSet(m *Module) bool {
	return len(m.Parsed.Features) == 0 && (!hasCompiled(m) || m.compiled && !hasRecompiled(m))
}

// hasRecompiled is lys_has_recompiled (LYSP_HAS_RECOMPILED per (sub)module).
func hasRecompiled(m *Module) bool {
	return m.anyPmod(func(pm *pmod) bool {
		p := pm.Parsed
		return len(p.Children) > 0 || len(p.Actions) > 0 || len(p.Notifications) > 0 || len(p.Exts) > 0
	})
}

// hasCompiled is lys_has_compiled (LYSP_HAS_COMPILED per (sub)module).
func hasCompiled(m *Module) bool {
	return hasRecompiled(m) || m.anyPmod(func(pm *pmod) bool {
		return len(pm.Parsed.Augments) > 0 || len(pm.Parsed.Deviations) > 0
	})
}

// hasDepMods is lys_has_dep_mods.
func hasDepMods(m *Module) bool {
	return len(m.Parsed.Features) > 0 || m.anyPmod(func(pm *pmod) bool {
		return len(pm.Parsed.Groupings) > 0 || len(pm.Parsed.Augments) > 0
	})
}

func (m *Module) anyPmod(f func(*pmod) bool) bool {
	if f(&m.pmod) {
		return true
	}
	for _, inc := range m.Includes {
		if inc.Sub != nil && f(&inc.Sub.pmod) { // nil: a failed module's include
			return true
		}
	}
	return false
}

// swapRemove is ly_set_rm_index: the last item takes the removed one's place.
func swapRemove[T any](s []T, i int) []T {
	s[i] = s[len(s)-1]
	return s[:len(s)-1]
}

// depSets is lys_unres_dep_sets_create with compile_set NULL (ly_ctx_compile).
func (c *Context) depSets() [][]*Module {
	ctxSet := slices.Clone(c.Modules)
	var sets [][]*Module
	for i := 0; i < len(ctxSet); {
		if singleDepSet(ctxSet[i]) {
			sets = append(sets, []*Module{ctxSet[i]})
			ctxSet = swapRemove(ctxSet, i)
		} else {
			i++
		}
	}
	for len(ctxSet) > 0 {
		var set, aux []*Module
		c.depSetMod(ctxSet[0], &ctxSet, &set, &aux)
		if slices.ContainsFunc(set, func(m *Module) bool { return m.toCompile }) {
			for _, m := range set {
				if m.Implemented {
					m.toCompile = true
				}
			}
		}
		sets = append(sets, set)
	}
	return sets
}

// depSetMod is lys_unres_dep_sets_create_mod_r.
func (c *Context) depSetMod(m *Module, ctxSet, set, aux *[]*Module) {
	if singleDepSet(m) {
		if !hasDepMods(m) || slices.Contains(*aux, m) {
			return
		}
		*aux = append(*aux, m)
	} else {
		i := slices.Index(*ctxSet, m)
		if i < 0 {
			return
		}
		*ctxSet = swapRemove(*ctxSet, i)
		*set = append(*set, m)
	}
	for _, im := range m.Imports {
		c.depSetMod(im, ctxSet, set, aux)
	}
	for _, inc := range m.Includes {
		for _, im := range inc.Sub.Imports {
			if !singleDepSet(im) || hasDepMods(im) {
				c.depSetMod(im, ctxSet, set, aux)
			}
		}
	}
	for _, o := range c.Modules { // modules and submodules importing m
		if slices.Contains(o.Imports, m) || slices.ContainsFunc(o.Includes, func(inc *Include) bool {
			return slices.Contains(inc.Sub.Imports, m)
		}) {
			c.depSetMod(o, ctxSet, set, aux)
		}
	}
}

// compileCtx is ly_ctx_compile: dep sets, then every set compiled.
func (c *Context) compileCtx() error {
	c.sets = c.depSets()
	return c.compileDepSetAll()
}

// compileDepSetAll is lys_compile_depset_all.
func (c *Context) compileDepSetAll() error {
	for _, set := range c.sets {
		for _, m := range set { // lys_compile_depset_check_features
			if m.toCompile {
				if err := c.checkFeatures(m); err != nil {
					return err
				}
			}
		}
		if err := c.compileDepSet(set); err != nil {
			return err
		}
	}
	return nil
}

// compileDepSet is lys_compile_depset_r: compile the flagged modules of the
// set, resolve its unres; a module implemented by unres either restarts the
// whole set (LY_ERECOMPILE) or is compiled alone and unres resolved again.
func (c *Context) compileDepSet(set []*Module) error {
	for {
		for _, m := range set {
			if m.toCompile {
				c.free(m)
				if err := c.compile(m); err != nil {
					return err
				}
			}
		}
		err := c.unres()
		for err == nil {
			i := slices.IndexFunc(set, func(m *Module) bool { return m.toCompile && !m.compiled })
			if i < 0 {
				break
			}
			if err = c.compile(set[i]); err == nil {
				err = c.unres()
			}
		}
		switch {
		case errors.Is(err, errRecompile):
			continue
		case err != nil:
			return err
		}
		for _, m := range set {
			m.toCompile = false
		}
		return nil
	}
}

// unres is lys_compile_unres_depset; the unres sets come with the node walk
// (design 06 C7), until then only a test hook raises anything.
func (c *Context) unres() error {
	if c.unresHook != nil {
		return c.unresHook(c)
	}
	return nil
}

// compile is lys_compile: the features of the module, then the node walk
// (P3, design 06 C4a); P2 own augments (C6), module extension instances,
// P4 and P5 (C4b) follow with their PRs. A failed compile leaves no
// compiled module.
func (c *Context) compile(m *Module) error {
	for _, f := range m.features {
		m.Schema.Features = append(m.Schema.Features, &schema.Feature{Name: f.p.Name, Module: m.Schema,
			Enabled: f.enabled, Status: parsedStatus(f.p.Status)})
	}
	if !c.nodeWalk {
		m.compiled = true
		return nil
	}
	if err := c.compileNodes(m, m.Schema); err != nil {
		c.free(m)
		return err
	}
	m.compiled = true
	return nil
}

// free is lysc_module_free: the types its leaves and leaf-lists hold are
// released (lysc_node_free → lysc_type_free).
func (c *Context) free(m *Module) {
	for work := slices.Clone(m.Schema.Top); len(work) > 0; {
		n := work[len(work)-1]
		work = append(work[:len(work)-1], n.Children...)
		work = append(work, n.Actions...)
		work = append(work, n.Notifs...)
		if n.Type != nil && (n.Kind == schema.Leaf || n.Kind == schema.LeafList) {
			c.typeCache.release(n.Type)
		}
	}
	m.Schema.Features, m.Schema.Top, m.Schema.Exts = nil, nil, nil
	m.compiled = false
}

// revert is lys_unres_glob_revert: the modules implemented by the failed
// operation become import-only again, the modules it created leave the
// context, and the previous state is compiled again without logging.
// Feature changes of modules implemented before stay, as in libyang.
func (c *Context) revert() {
	for _, m := range c.implementing {
		m.Implemented, m.Schema.Implemented = false, false
		for _, o := range c.Modules { // lys_precompile_augments_deviations_revert
			if i := slices.Index(o.augmentedBy, m); i >= 0 {
				o.augmentedBy = slices.Delete(o.augmentedBy, i, i+1)
			}
		}
		c.free(m)
		m.toCompile = false
	}
	for _, m := range c.creating {
		if i := slices.Index(c.Modules, m); i >= 0 {
			c.Modules = swapRemove(c.Modules, i)
		}
		for j, set := range c.sets {
			if i := slices.Index(set, m); i >= 0 {
				c.sets[j] = swapRemove(set, i)
				break
			}
		}
		unlinkDerived(c.Modules, m)
		c.dropTypedefs(m)
	}
	if len(c.implementing) > 0 {
		n := len(c.diags)
		err := c.compileDepSetAll()
		c.diags = c.diags[:n]
		if err != nil {
			_ = c.logErr(rc("LY_EINT"), "Internal error (tree_schema.c:1340).")
		}
	}
}

// dropTypedefs releases the cached types of the typedefs of a module leaving
// the context (lys_module_free frees its parsed typedefs and their
// compiled types).
func (c *Context) dropTypedefs(m *Module) {
	var work []*parser.Node
	m.anyPmod(func(pm *pmod) bool { work = append(work, &pm.Parsed.Node); return false })
	for len(work) > 0 {
		n := work[len(work)-1]
		work = work[:len(work)-1]
		for _, td := range n.Typedefs {
			if t, ok := c.typeCache.compiled[td]; ok {
				delete(c.typeCache.compiled, td)
				c.typeCache.release(t)
			}
		}
		work = append(work, n.Children...)
		work = append(work, n.Groupings...)
		work = append(work, n.Actions...)
		work = append(work, n.Notifications...)
		work = append(work, n.Augments...)
		if n.Input != nil {
			work = append(work, n.Input)
		}
		if n.Output != nil {
			work = append(work, n.Output)
		}
	}
}
