// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile.c and src/tree_schema_free.c
// (BSD-3-Clause, © CESNET).

package compile

import (
	"slices"
	"strings"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// compileIdentities is lys_compile_identities (P0): the identities of m and
// its submodules (lys_identity_precompile), then the derived back-links of
// their bases, module first, then per submodule. Extension instances of
// identities are compiled with the node walk (design 06 C4b).
func (c *Context) compileIdentities(m *Module) error {
	pms := []*pmod{&m.pmod}
	for _, inc := range m.Includes {
		pms = append(pms, &inc.Sub.pmod)
	}
	for _, pm := range pms {
		for _, p := range pm.Parsed.Identities {
			m.Schema.Identities = append(m.Schema.Identities,
				&schema.Identity{Name: p.Name, Module: m.Schema, Status: parsedStatus(p.Status)})
		}
	}
	if err := c.identitiesDerived(m, &m.pmod, "/"+m.Name+":"); err != nil {
		return err
	}
	for _, inc := range m.Includes {
		if err := c.identitiesDerived(m, &inc.Sub.pmod, "/"+m.Name+":{submodule='"+inc.Sub.Name+"'}/"); err != nil {
			return err
		}
	}
	return nil
}

// identitiesDerived is lys_compile_identities_derived for the identities
// written in pm; prefix is the log path up to the {identity} segment.
func (c *Context) identitiesDerived(m *Module, pm *pmod, prefix string) error {
	for _, id := range m.Schema.Identities {
		i := slices.IndexFunc(pm.Parsed.Identities, func(p *parser.Node) bool { return p.Name == id.Name })
		if i < 0 || len(pm.Parsed.Identities[i].Bases) == 0 {
			continue
		}
		if err := c.identityBases(m, pm, pm.Parsed.Identities[i].Bases, id, prefix+"{identity='"+id.Name+"'}"); err != nil {
			return err
		}
	}
	return nil
}

// identityBases is lys_compile_identity_bases for an identity: link id into
// the derived list of each of its bases.
func (c *Context) identityBases(m *Module, pm *pmod, bases []string, id *schema.Identity, path string) error {
	if len(bases) > 1 && pm.Parsed.Version != "1.1" {
		return c.logPath(ly.SyntaxYang, path, "Multiple bases in identity are allowed only in YANG 1.1 modules.")
	}
	for _, b := range bases {
		mod, name := m, b
		if i := strings.IndexByte(b, ':'); i >= 0 {
			mod, name = c.prefixModule(pm, m, b[:i]), b[i+1:]
		}
		if mod == nil {
			return c.logPath(ly.SyntaxYang, path, "Invalid prefix used for base (%s) of identity \"%s\".", b, id.Name)
		}
		base := mod.Schema.Identity(name)
		switch {
		case base == nil:
			return c.logPath(ly.SyntaxYang, path, "Unable to find base (%s) of identity \"%s\".", b, id.Name)
		case base == id:
			return c.logPath(ly.Reference, path, "Identity \"%s\" is derived from itself.", id.Name)
		case reaches(base, id.Derived, func(d *schema.Identity) []*schema.Identity { return d.Derived }):
			return c.logPath(ly.Reference, path, "Identity \"%s\" is indirectly derived from itself.", base.Name)
		}
		base.Derived = append(base.Derived, id)
	}
	return nil
}

// unlinkDerived removes the identities of a module leaving the context from
// the derived lists of the remaining ones (lysc_ident_derived_unlink).
func unlinkDerived(mods []*Module, gone *Module) {
	for _, m := range mods {
		for _, id := range m.Schema.Identities {
			id.Derived = slices.DeleteFunc(id.Derived, func(d *schema.Identity) bool { return d.Module == gone.Schema })
		}
	}
}
