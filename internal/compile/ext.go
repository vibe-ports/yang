// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_schema.c and src/tree_schema_common.c
// (BSD-3-Clause, © CESNET).

package compile

import (
	"strings"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/parser"
)

// resolveExts is the first loop of lysp_resolve_ext_instance_records: every
// extension instance of the module and of its submodules (in the order their
// parser contexts were merged) is bound to its definition
// (lysp_ext_find_definition) and its argument checked
// (lysp_ext_instance_resolve_argument). Errors carry libyang's
// lysp_ext_instance_path and no line. Plugin parse callbacks are C1b's.
func (c *Context) resolveExts(p *pctx) error {
	pms := []*pmod{&p.main.pmod}
	for _, s := range p.done {
		pms = append(pms, &s.pmod)
	}
	for _, pm := range pms {
		parent := map[*parser.Stmt]*parser.Stmt{}
		var link func(s *parser.Stmt)
		link = func(s *parser.Stmt) {
			for _, ch := range s.Subs {
				parent[ch] = s
				link(ch)
			}
		}
		link(pm.Parsed.Stmt)
		for _, e := range parser.ExtInstances(pm.Parsed.Stmt) {
			name := e.ExtPrefix + ":" + e.Keyword
			path := func() string { return extPath(p.main.Name, parent, e) }
			mod := c.prefixModule(pm, p.main, e.ExtPrefix) // ly_resolve_prefix (LY_VALUE_SCHEMA)
			if mod == nil {
				return c.logPath(ly.Reference, path(), "Invalid prefix \"%s\" used for extension instance identifier.", e.ExtPrefix)
			}
			def := findExtension(mod, e.Keyword)
			if def == nil {
				return c.logPath(ly.Reference, path(), "Extension definition of extension instance \"%s\" not found.", name)
			}
			for _, a := range def.Subs {
				if a.ExtPrefix != "" || a.Keyword != "argument" || e.HasArg {
					continue
				}
				elem := ""
				for _, y := range a.Subs {
					if y.ExtPrefix == "" && y.Keyword == "yin-element" && y.Arg == "true" {
						elem = "element "
					}
				}
				return c.logPath(ly.Semantics, path(), "Extension instance \"%s\" missing argument %s\"%s\".", name, elem, a.Arg)
			}
		}
	}
	return nil
}

// prefixModule is ly_schema_resolve_prefix for the (sub)module pm of main.
func (c *Context) prefixModule(pm *pmod, main *Module, prefix string) *Module {
	if prefix == pm.Parsed.Prefix {
		return main
	}
	for u, imp := range pm.Parsed.Imports {
		if imp.Prefix == prefix {
			return pm.Imports[u]
		}
	}
	return nil
}

// findExtension looks for the extension statement in the module, then in its submodules.
func findExtension(m *Module, name string) *parser.Stmt {
	roots := []*parser.Stmt{m.Parsed.Stmt}
	for _, inc := range m.Includes {
		if inc.Sub != nil {
			roots = append(roots, inc.Sub.Parsed.Stmt)
		}
	}
	for _, r := range roots {
		for _, s := range r.Subs {
			if s.ExtPrefix == "" && s.Keyword == "extension" && s.Arg == name {
				return s
			}
		}
	}
	return nil
}

// nodeKw are the statements of LY_STMT_NODE_MASK, and their lysp_path_until segment.
var nodeKw = map[string]string{"container": "", "choice": "", "leaf": "", "leaf-list": "", "list": "", "anyxml": "",
	"anydata": "", "case": "", "rpc": "", "action": "", "notification": "", "input": "", "output": "",
	"uses": "uses", "grouping": "grouping", "augment": "augment"}

// argless are the statements lysp_ext_instance_path prints as "{kw}".
var argless = map[string]bool{"config": true, "contact": true, "description": true, "mandatory": true,
	"max-elements": true, "min-elements": true, "namespace": true, "organization": true, "prefix": true,
	"yang-version": true}

// extPath is lysp_ext_instance_path; nodes of submodules are printed with the
// main module's name, as libyang does with the main parser context.
func extPath(main string, parent map[*parser.Stmt]*parser.Stmt, e *parser.Stmt) string {
	var b strings.Builder
	p := parent[e]
	switch {
	case parent[p] == nil: // module or submodule
		b.WriteString("/" + p.Arg + ":")
	case p.ExtPrefix == "" && hasKey(p.Keyword):
		b.WriteString(nodePath(main, parent, p))
	default:
		b.WriteString("/" + main + ":")
		stmtPath(&b, main, parent, p)
	}
	appendSeg(&b, "{ext-inst='"+e.ExtPrefix+":"+e.Keyword+"'}")
	if e.HasArg {
		b.WriteString("/" + e.Arg)
	}
	return b.String()
}

func hasKey(kw string) bool { _, ok := nodeKw[kw]; return ok }

// appendSeg adds "/" unless the path ends with the module's ':'.
func appendSeg(b *strings.Builder, seg string) {
	if s := b.String(); s != "" && s[len(s)-1] != ':' {
		b.WriteString("/")
	}
	b.WriteString(seg)
}

// stmtPath is lysp_ext_instance_path_stmt_append_r for a non-node statement s.
func stmtPath(b *strings.Builder, main string, parent map[*parser.Stmt]*parser.Stmt, s *parser.Stmt) {
	owner := parent[s]
	kw := s.Keyword
	switch {
	case s.ExtPrefix != "":
		appendSeg(b, "{ext-inst='"+s.ExtPrefix+":"+s.Keyword+"'}")
		if s.HasArg {
			b.WriteString("/" + s.Arg)
		}
	case kw == "argument" || kw == "yin-element":
		if kw == "yin-element" {
			owner = parent[owner]
		}
		appendSeg(b, "{extension='"+owner.Arg+"'}")
		b.WriteString("/{" + kw + "}")
	case kw == "fraction-digits" || kw == "path" || kw == "require-instance" || kw == "modifier" ||
		kw == "position" || kw == "value":
		stmtPath(b, main, parent, owner)
		b.WriteString("/{" + kw + "}")
	case kw == "key" || kw == "ordered-by":
		// libyang replaces the buffer by the list's path
		path := nodePath(main, parent, owner)
		b.Reset()
		b.WriteString(path + "/{" + kw + "}")
	case kw == "error-app-tag" || kw == "error-message":
		appendSeg(b, "{restriction}/{"+kw+"}")
	case argless[kw]:
		appendSeg(b, "{"+kw+"}")
	case kw == "belongs-to":
		appendSeg(b, "{belongs-to='"+owner.Arg+"'}")
	default:
		appendSeg(b, "{"+kw+"='"+s.Arg+"'}")
	}
}

// nodePath is lysp_path_until(node, NULL, main).
func nodePath(main string, parent map[*parser.Stmt]*parser.Stmt, n *parser.Stmt) string {
	path := ""
	for it := n; parent[it] != nil; it = parent[it] {
		prefix := ""
		if parent[parent[it]] == nil {
			prefix = main + ":"
		}
		seg := it.Arg
		if it.Keyword == "input" || it.Keyword == "output" {
			seg = it.Keyword
		}
		if k := nodeKw[it.Keyword]; k != "" {
			seg = "{" + k + "='" + it.Arg + "'}"
		}
		path = "/" + prefix + seg + path
	}
	return path
}
