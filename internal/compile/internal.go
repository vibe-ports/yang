// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_schema.c (BSD-3-Clause, © CESNET).

package compile

import (
	"strings"

	"github.com/vibe-ports/yang/internal/parser"
)

// stmt builds one synthesized statement; a "prefix:name" keyword is an extension instance.
func stmt(kw, arg string, subs ...*parser.Stmt) *parser.Stmt {
	s := &parser.Stmt{Keyword: kw, Arg: arg, HasArg: arg != "", Subs: subs}
	if prefix, name, ok := strings.Cut(kw, ":"); ok {
		s.ExtPrefix, s.Keyword = prefix, name
	}
	return s
}

// enumType is a type enumeration statement with the given enums.
func enumType(names ...string) *parser.Stmt {
	t := stmt("type", "enumeration")
	for _, n := range names {
		t.Subs = append(t.Subs, stmt("enum", n))
	}
	return t
}

// addInternal is the lys_parse_in step that adds the internal data of the modules
// ietf-netconf, ietf-netconf-with-defaults and yang (lysp_add_internal_ietf_netconf,
// lysp_add_internal_ietf_netconf_with_defaults, lysp_add_internal_yang) to the parsed module
// statement st. It reports whether anything was added. The statements are appended in libyang's
// array order (typedefs, extension instances, data and imports each go last); libyang's
// LYS_INTERNAL flag only hides them from the parsed-schema printers, which are not ported.
func addInternal(st *parser.Stmt, v11 bool) bool {
	switch st.Arg {
	case "ietf-netconf":
		xpath := stmt("enum", "xpath")
		if v11 {
			xpath.Subs = append(xpath.Subs, stmt("if-feature", "xpath"))
		}
		st.Subs = append(st.Subs,
			stmt("md_:annotation", "operation", enumType("merge", "replace", "create", "delete", "remove")),
			stmt("md_:annotation", "type", stmt("type", "enumeration", stmt("enum", "subtree"), xpath)),
			stmt("md_:annotation", "select", stmt("type", "yang_:xpath1.0")),
			// the rest are opaque nodes, error-message (because of 'xml:lang' attribute) and
			// error-info (because can be any nodes)
			stmt("container", "rpc-error", stmt("presence", "presence"),
				stmt("leaf", "error-type", enumType("transport", "rpc", "protocol", "application")),
				stmt("leaf", "error-tag", enumType("in-use", "invalid-value", "too-big", "missing-attribute",
					"bad-attribute", "unknown-attribute", "missing-element", "bad-element", "unknown-element",
					"unknown-namespace", "access-denied", "lock-denied", "resource-denied", "rollback-failed",
					"data-exists", "data-missing", "operation-not-supported", "operation-failed",
					"partial-operation", "malformed-message")),
				stmt("leaf", "error-severity", enumType("error", "warning")),
				stmt("leaf", "error-app-tag", stmt("type", "string")),
				stmt("leaf", "error-path", stmt("type", "yang_:xpath1.0"))),
			stmt("import", "ietf-yang-metadata", stmt("prefix", "md_")),
			stmt("import", "ietf-yang-types", stmt("prefix", "yang_")))
	case "ietf-netconf-with-defaults":
		st.Subs = append(st.Subs,
			stmt("md_:annotation", "default", stmt("type", "boolean")),
			stmt("import", "ietf-yang-metadata", stmt("prefix", "md_")))
	case "yang":
		st.Subs = append(st.Subs,
			stmt("typedef", "lyds_tree", stmt("type", "uint64")),
			stmt("md:annotation", "lyds_tree", stmt("type", "lyds_tree")),
			// a date-and-time leaf so that such values can be validated (there is a compiled type)
			stmt("leaf", "date-and-time", stmt("type", "yang_:date-and-time")),
			stmt("import", "ietf-yang-types", stmt("prefix", "yang_")))
	default:
		return false
	}
	return true
}
