// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_schema_common.c and src/parser_yang.c
// (BSD-3-Clause, © CESNET).

package compile

import (
	"slices"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/parser"
)

// builtinTypes are the names lysp_type_str2builtin knows.
var builtinTypes = map[string]bool{"binary": true, "bits": true, "boolean": true, "decimal64": true, "empty": true,
	"enumeration": true, "identityref": true, "instance-identifier": true, "int8": true, "int16": true, "int32": true,
	"int64": true, "leafref": true, "string": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"union": true}

// scopes collects what parser_yang.c records for the collision checks: the
// statements that hold typedefs (tpdfs_nodes, added when their first typedef
// ends) and groupings (grps_nodes, added when a grouping ends, so inner
// before outer), plus the parent of every statement.
type scopes struct {
	tpdfs, grps []*parser.Stmt
	parent      map[*parser.Stmt]*parser.Stmt
}

func (sc *scopes) collect(s *parser.Stmt, top bool) {
	for _, c := range s.Subs {
		if c.ExtPrefix != "" {
			continue
		}
		sc.parent[c] = s
		sc.collect(c, false)
		if top {
			continue
		}
		if c.Keyword == "typedef" && !slices.Contains(sc.tpdfs, s) {
			sc.tpdfs = append(sc.tpdfs, s)
		}
		if c.Keyword == "grouping" && !slices.Contains(sc.grps, s) {
			sc.grps = append(sc.grps, s)
		}
	}
}

// collectExt is the tpdfs_nodes part of parser_common.c lysp_stmt_typedef for the subtree of an
// extension instance a plugin parsed (lyplg_ext_parse_extension_instance): its top-level
// statements have no parent node, and a typedef is recorded only in a statement that is not a
// grouping, an operation or its input/output (the instance's own typedefs are not recorded, and
// groupings never are).
func (sc *scopes) collectExt(s *parser.Stmt, top bool) {
	for _, c := range s.Subs {
		if c.ExtPrefix != "" {
			continue
		}
		sc.parent[c] = s // the instance itself ends the parent walk (inAncestors), as the module does
		sc.collectExt(c, false)
		if top || c.Keyword != "typedef" || slices.Contains(sc.tpdfs, s) {
			continue
		}
		switch s.Keyword {
		case "grouping", "rpc", "action", "input", "output", "notification":
		default:
			sc.tpdfs = append(sc.tpdfs, s)
		}
	}
}

func subs(s *parser.Stmt, kw string) []*parser.Stmt {
	var r []*parser.Stmt
	for _, c := range s.Subs {
		if c.ExtPrefix == "" && c.Keyword == kw {
			r = append(r, c)
		}
	}
	return r
}

// checkDups runs lysp_check_dup_typedefs, _groupings, _features and
// _identities for the module parsed in p (after its imports and includes).
func (c *Context) checkDups(p *pctx) error {
	sc := &scopes{parent: map[*parser.Stmt]*parser.Stmt{}}
	tops := []*parser.Stmt{p.main.Parsed.Stmt}
	for _, inc := range p.main.Includes {
		tops = append(tops, inc.Sub.Parsed.Stmt)
	}
	sc.collect(p.main.Parsed.Stmt, true)
	for _, s := range p.done {
		sc.collect(s.Parsed.Stmt, true)
	}
	// then the subtrees the extension plugins parsed (lysp_resolve_ext_instance_records)
	for _, pm := range append([]*pmod{&p.main.pmod}, func() (l []*pmod) {
		for _, s := range p.done {
			l = append(l, &s.pmod)
		}
		return
	}()...) {
		for _, owner := range parser.ExtOwners(pm.Parsed.Stmt) {
			arr, _ := c.ownedExts(owner)
			for _, e := range arr {
				if c.extParsed[e] != nil {
					sc.collectExt(e, true)
				}
			}
		}
	}
	dup := func(name, kw, detail string) error {
		return c.logVal(ly.SyntaxYang, 0, "Duplicate identifier \"%s\" of %s statement - %s.", name, kw, detail)
	}

	// typedefs
	global := map[string]bool{}
	for _, t := range tops {
		for _, td := range subs(t, "typedef") {
			if builtinTypes[td.Arg] {
				return dup(td.Arg, "typedef", "name collision with a built-in type")
			}
			if global[td.Arg] {
				return dup(td.Arg, "typedef", "name collision with another top-level type")
			}
			global[td.Arg] = true
		}
	}
	for _, n := range sc.tpdfs {
		tds := subs(n, "typedef")
		for i, td := range tds {
			name := td.Arg
			switch {
			case builtinTypes[name]:
				return dup(name, "typedef", "name collision with a built-in type")
			case slices.ContainsFunc(tds[:i], func(o *parser.Stmt) bool { return o.Arg == name }):
				return dup(name, "typedef", "name collision with sibling type")
			case sc.inAncestors(n, "typedef", name):
				return dup(name, "typedef", "name collision with another scoped type")
			case global[name]:
				return dup(name, "typedef", "scoped type collide with a top-level type")
			}
		}
	}

	// groupings
	global = map[string]bool{}
	for _, t := range tops {
		for _, g := range subs(t, "grouping") {
			if global[g.Arg] {
				return dup(g.Arg, "grouping", "name collision with another top-level grouping")
			}
			global[g.Arg] = true
		}
	}
	for _, n := range sc.grps {
		gs := subs(n, "grouping")
		for i, g := range gs {
			name := g.Arg
			switch {
			case slices.ContainsFunc(gs[:i], func(o *parser.Stmt) bool { return o.Arg == name }):
				return dup(name, "grouping", "name collision with sibling grouping")
			case sc.inAncestors(n, "grouping", name):
				return dup(name, "grouping", "name collision with another scoped grouping")
			case global[name]:
				return dup(name, "grouping", "scoped grouping collide with a top-level grouping")
			}
		}
	}

	for _, kw := range []string{"feature", "identity"} {
		seen := map[string]bool{}
		for _, t := range tops {
			for _, s := range subs(t, kw) {
				if seen[s.Arg] {
					return dup(s.Arg, kw, "name collision with another top-level "+kw)
				}
				seen[s.Arg] = true
			}
		}
	}
	return nil
}

// inAncestors reports whether a parent statement of n (below the module) has
// a kw substatement named name (lysp_typedef_match, lysp_grouping_match).
func (sc *scopes) inAncestors(n *parser.Stmt, kw, name string) bool {
	for p := sc.parent[n]; p != nil && sc.parent[p] != nil; p = sc.parent[p] {
		if slices.ContainsFunc(subs(p, kw), func(o *parser.Stmt) bool { return o.Arg == name }) {
			return true
		}
	}
	return false
}
