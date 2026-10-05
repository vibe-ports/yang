// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (BSD-3-Clause, © CESNET).

// Package xpath parses and evaluates XPath 1.0 expressions with the YANG
// function library (RFC 7950 §10) over a data tree it reaches only through
// the Node interface, following libyang v5.8.6 semantics — including its
// quirks, which are marked "libyang:" in comments.
//
// The evaluation context is explicit (docs/design/03-xpath-context.md):
// the namespace context is bound at Compile, the context node, accessible
// tree and hooks at Eval.
package xpath

import "fmt"

// NamespaceCtx binds prefixes for one expression (design 03, rule 1).
type NamespaceCtx interface {
	// Resolve maps a prefix to an implemented module name.
	Resolve(prefix string) (module string, ok bool)
	// Prefix is the prefix name() prints for module.
	Prefix(module string) string
	// Default is the module of unprefixed names; "" matches any module (JSON format).
	Default() string
}

// Error is an XPath error with libyang's LY_ERR / LYVE codes.
type Error struct {
	Err    string // LY_EVALID, LY_EINVAL, LY_ENOTFOUND
	VECode string // LYVE_XPATH, LYVE_DATA, or "" for none
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func xpErr(format string, a ...any) *Error {
	return &Error{Err: "LY_EVALID", VECode: "LYVE_XPATH", Msg: fmt.Sprintf(format, a...)}
}

// Expr is a compiled expression.
type Expr struct {
	src  string
	root ast
	ns   NamespaceCtx
}

// String returns the source text.
func (e *Expr) String() string { return e.src }

// Compile tokenizes and parses src (lyxp_expr_parse with reparse). Unknown
// functions and wrong argument counts are compile errors; prefixes are
// resolved through ns at evaluation, as libyang does.
func Compile(src string, ns NamespaceCtx) (*Expr, error) {
	toks, src, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{src: src, toks: toks}
	root, err := p.orExpr(0)
	if err != nil {
		return nil, err
	}
	if p.i < len(toks) {
		return nil, xpErr("Unparsed characters \"%s\" left at the end of an XPath expression.", src[toks[p.i].pos:])
	}
	return &Expr{src: src, root: root, ns: ns}, nil
}
