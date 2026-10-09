// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/node_instanceid.c (BSD-3-Clause, © CESNET).

package types

import "github.com/vibe-ports/yang/internal/lyxp"

// plugins_node_instanceid: compare and sort by the canonical string, no validate_value.
func init() {
	plugins[pluginKey{"ietf-netconf-acm", "", "node-instance-identifier"}] = &plugin{
		id: "node-instance-identifier", store: storeNodeInstanceID}
}

// nodeInstanceID is the storage of a node-instance-identifier (lyd_value.target); a nil path is
// the special path "/".
type nodeInstanceID struct{ path Path }

// storeNodeInstanceID ports lyplg_type_store_node_instanceid: "/" alone, else an absolute path
// with simple predicates compiled for many targets (lists and leaf-lists without predicates).
func storeNodeInstanceID(a *storeArgs) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	if a.lex == "/" {
		return Value{typ: a.t, canon: "/", ext: &nodeInstanceID{}}, nil
	}
	prefix := lyxp.PrefixStrictInherit
	if a.f == FormatSchema || a.f == FormatSchemaResolved || a.f == FormatXML {
		prefix = lyxp.PrefixMandatory
	}
	e, msg := lyxp.ParsePath(a.lex, lyxp.Opts{Begin: lyxp.BeginAbsolute, Prefix: prefix, Pred: lyxp.PredSimple})
	if msg != "" {
		return Value{}, nodeIDErr(a, msg, "syntax")
	}
	if !implementPrefixes(a, e) {
		return Value{}, &Diag{Code: CodeData} // no error item: lys_compile_expr_implement's rc alone
	}
	p, msg, _ := pathCompileAt(a, e, nil, a.ctx != nil && a.ctx.InOutput(), true)
	if msg != "" {
		return Value{}, nodeIDErr(a, msg, "semantic")
	}
	v := Value{typ: a.t, canon: p.String(), ext: &nodeInstanceID{p}} // JSON with prefixes is canonical
	if a.f == FormatCanon {
		v.canon = a.lex
	}
	return v, nil
}

// nodeIDErr is the plugin's error item after the path error msg, which ly_path_parse /
// ly_path_compile logged (LYVE_XPATH, or the predicate value's own error item) and the plugin
// leaves in the context log; inside a union nothing is logged.
func nodeIDErr(a *storeArgs, msg, kind string) *Diag {
	d := errf("Invalid node-instance-identifier \"%s\" value - %s error.", a.lex, kind)
	switch {
	case a.quiet:
	case a.keyErr != nil: // a predicate value: its own error item, not LYVE_XPATH
		d.Logged = a.keyErr
	default:
		d.Logged = &Diag{Code: "LYVE_XPATH", Msg: msg}
	}
	return d
}
