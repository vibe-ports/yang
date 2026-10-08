// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/xpath1.0.c (lyplg_type_store_xpath10,
// lyplg_type_print_xpath10_value, xpath10_print_subexpr_r) (BSD-3-Clause, © CESNET).

package types

import (
	"errors"
	"strings"

	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/xpath"
)

func init() {
	plugins[pluginKey{"ietf-yang-types", "", "xpath1.0"}] = &plugin{id: "xpath1.0", store: storeXPath10}
}

// storeXPath10 ports lyplg_type_store_xpath10: the expression must parse (lyxp_expr_parse with
// reparse; its error is the value's); the canonical form is the value as given in the JSON and
// canonical formats, else the expression printed in JSON (module-name prefixes, a prefix inherited
// by the following name tests of its module left out). No restriction of the type is checked.
func storeXPath10(a *storeArgs) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	if _, err := xpath.Compile(a.lex, nil); err != nil {
		var xe *xpath.Error
		if errors.As(err, &xe) {
			return Value{}, &Diag{Code: xe.VECode, Msg: xe.Msg}
		}
		return Value{}, &Diag{Code: CodeNone, Msg: err.Error()}
	}
	e, msg := lyxp.Lex(a.lex)
	if msg != "" { // the parse above accepted it
		return Value{}, &Diag{Code: CodeNone, Msg: msg}
	}
	// the expression and its prefix data, for printing in other formats (lyplg_type_print_xpath10)
	k := &keysValue{e: e, f: a.f, pc: prefixDataNew(a)}
	v := Value{typ: a.t, canon: a.lex, ext: k}
	switch a.f {
	case FormatCanon, FormatJSON:
		return v, nil
	}
	canon, d := printXPath10(e, k.pc, FormatJSON, &PrintCtx{})
	if d != nil {
		return Value{}, d
	}
	v.canon = canon
	return v, nil
}

// printXPath10 ports lyplg_type_print_xpath10_value: the expression e in format f, the prefixes it
// was written with resolved through resolve, the target prefixes taken from pc. For XML the local
// module is nulled so that all the prefixes are printed.
func printXPath10(e *lyxp.Expr, resolve PrefixCtx, f Format, pc *PrintCtx) (string, *Diag) {
	if f == FormatXML {
		defer func(local *schema.Module) { pc.Local = local }(pc.Local)
		pc.Local = nil
	}
	var b strings.Builder
	i := 0
	if d := xpath10Subexpr(&b, e, &i, lyxp.TokNone, nil, resolve, f, pc); d != nil {
		return "", d
	}
	return b.String(), nil
}

// xpath10Subexpr ports xpath10_print_subexpr_r: tokens from *i up to end (TokNone: all), name tests
// and literals with their prefixes converted to format f, operators of logic, union and math with
// spaces around (they reset the context module), brackets and parentheses as subexpressions with
// the current context module.
func xpath10Subexpr(b *strings.Builder, e *lyxp.Expr, i *int, end lyxp.Tok, ctx *schema.Module, resolve PrefixCtx, f Format, pc *PrintCtx) *Diag {
	orig := ctx
	for *i < len(e.Toks) {
		tok, text := e.Toks[*i], e.Text(*i)
		if tok == lyxp.TokNameTest || tok == lyxp.TokLiteral {
			s, d := printXPathToken(text, tok == lyxp.TokNameTest, &ctx, resolve, f, pc)
			if d != nil {
				return d
			}
			b.WriteString(s)
			*i++
			continue
		}
		if tok == lyxp.TokOperLog || tok == lyxp.TokOperUni || tok == lyxp.TokOperMath {
			b.WriteString(" " + text + " ")
			ctx = orig
		} else {
			b.WriteString(text)
		}
		*i++
		switch {
		case end != lyxp.TokNone && tok == end:
			return nil
		case tok == lyxp.TokBrack1:
			if d := xpath10Subexpr(b, e, i, lyxp.TokBrack2, ctx, resolve, f, pc); d != nil {
				return d
			}
		case tok == lyxp.TokPar1:
			if d := xpath10Subexpr(b, e, i, lyxp.TokPar2, ctx, resolve, f, pc); d != nil {
				return d
			}
		}
	}
	return nil
}
