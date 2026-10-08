// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_schema_common.c (lysp_type_find), src/tree_schema.c
// (ly_schema_resolve_prefix) and src/schema_compile.c (lysc_check_status) (BSD-3-Clause, © CESNET).

package compile

import (
	"strings"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

func (m *pmod) v11() bool { return m.Parsed.Version == "1.1" }

// resolve ports ly_schema_resolve_prefix: the (sub)module's own prefix, then its imports, to
// their compiled modules. prefix "" is the module itself.
func (m *pmod) resolve(prefix string) *schema.Module {
	if prefix == "" || prefix == m.Parsed.Prefix {
		return m.mod
	}
	for u, im := range m.Parsed.Imports {
		if im.Prefix == prefix && u < len(m.Imports) && m.Imports[u] != nil {
			return m.Imports[u].mod
		}
	}
	return nil
}

// scope is the chain of parsed nodes enclosing a type statement, innermost first (libyang walks
// lysp_node.parent); nil is the module top level.
type scope struct {
	node *parser.Node
	up   *scope
}

// tpdfItem is one typedef of a chain (libyang struct lys_type_item): the typedef, the scope it
// was found in (nil = top level) and the (sub)module defining it, which is its type's pmod.
type tpdfItem struct {
	tpdf *parser.Node
	node *scope
	pm   *pmod
}

// typeCtx is the part of libyang's struct lysc_ctx that type compilation reads and writes.
type typeCtx struct {
	cur    *schema.Module             // ctx->cur_mod: the module being compiled
	pmod   *pmod                      // ctx->pmod: the (sub)module whose text is being compiled
	parsed map[*schema.Module]*Module // lys_module.parsed: the loaded module of every compiled one
	cache  *typeCache
	chain  []*tpdfItem // ctx->tpdf_chain: typedefs being compiled, for nested union cycles
	// iff evaluates the if-features of an enum or bit written in pm (lys_eval_iffeatures);
	// nil means no if-feature may occur.
	iff    func(pm *pmod, ifs []*parser.IfFeature) (bool, error)
	budget Budget
	types  int // compiled types plus union member slots in this Load (Budget.MaxTypes)
	// extTpdfs are the typedefs of the extension instance being compiled (ctx->ext, through
	// lyplg_ext_parsed_get_storage)
	extTpdfs []*parser.Node
}

// countTypes charges n type objects or member slots against Budget.MaxTypes.
func (c *typeCtx) countTypes(n int) error {
	limit := c.budget.MaxTypes
	if limit <= 0 {
		limit = DefaultMaxTypes
	}
	if n > limit-c.types {
		return budgetErr("more than %d compiled types and union members", limit)
	}
	c.types += n
	return nil
}

// builtinBase maps the YANG built-in type names to their base (lysp_type_str2builtin; dup.go's
// builtinTypes is the same name set).
var builtinBase = func() map[string]schema.BaseType {
	m := map[string]schema.BaseType{}
	for b := schema.Binary; b <= schema.Int64; b++ {
		m[b.String()] = b
	}
	return m
}()

// findType ports lysp_type_find: a built-in type (unprefixed names only), or a typedef in the
// enclosing scopes and the extension instance being compiled (own module only), the main
// module's top level, then its submodules. ok is
// false when nothing is found; base is Unknown for a typedef.
func (c *typeCtx) findType(id string, start *scope, pm *pmod) (base schema.BaseType, it *tpdfItem, ok bool) {
	local, name := pm, id
	if i := strings.IndexByte(id, ':'); i >= 0 {
		name = id[i+1:]
		local = nil
		if m := c.parsed[pm.resolve(id[:i])]; m != nil {
			local = &m.pmod
		}
	} else if b := builtinBase[name]; b != schema.Unknown {
		return b, nil, true
	}
	if local == nil {
		return schema.Unknown, nil, false
	}
	if local == pm {
		for s := start; s != nil; s = s.up {
			if t := typedefNamed(s.node.Typedefs, name); t != nil {
				return schema.Unknown, &tpdfItem{t, s, pm}, true
			}
		}
		// search typedefs directly in the extension
		if t := typedefNamed(c.extTpdfs, name); t != nil {
			return schema.Unknown, &tpdfItem{t, nil, pm}, true
		}
	}
	// go to the main module if in a submodule
	main := c.parsed[local.mod]
	if main == nil {
		return schema.Unknown, nil, false
	}
	if t := typedefNamed(main.Parsed.Typedefs, name); t != nil {
		return schema.Unknown, &tpdfItem{t, nil, &main.pmod}, true
	}
	for _, inc := range main.Includes {
		if inc.Sub == nil {
			continue
		}
		if t := typedefNamed(inc.Sub.Parsed.Typedefs, name); t != nil {
			return schema.Unknown, &tpdfItem{t, nil, &inc.Sub.pmod}, true
		}
	}
	return schema.Unknown, nil, false
}

func typedefNamed(tpdfs []*parser.Node, name string) *parser.Node {
	for _, t := range tpdfs {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// parsedStatus is the status statement of a parsed node; "" is current.
func parsedStatus(s string) schema.Status {
	switch s {
	case "deprecated":
		return schema.Deprecated
	case "obsolete":
		return schema.Obsolete
	}
	return schema.Current
}

var statusNames = [...]string{schema.Current: "current", schema.Deprecated: "deprecated", schema.Obsolete: "obsolete"}

// checkStatus ports lysc_check_status: a definition may not reference a less current one of the
// same module (mod1, mod2 are compared by identity; callers pass what libyang passes).
func checkStatus(st1 schema.Status, mod1 any, name1 string, st2 schema.Status, mod2 any, name2 string) error {
	if st1 < st2 && mod1 == mod2 {
		return verr(ly.Reference, "A %s definition \"%s\" is not allowed to reference %s definition \"%s\".",
			statusNames[st1], name1, statusNames[st2], name2)
	}
	return nil
}
