// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/xpath1.0.c (lyplg_type_store_xpath10,
// lyplg_type_print_xpath10_value, xpath10_print_subexpr_r) (BSD-3-Clause, © CESNET).

package types

import (
	"errors"

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
	pc, d := prefixDataNew(a)
	if d != nil {
		return Value{}, d
	}
	k := &keysValue{e: e, f: a.f, pc: pc}
	v := Value{typ: a.t, canon: a.lex, ext: k}
	switch a.f {
	case FormatCanon, FormatJSON:
		return v, nil
	}
	canon, err := printXPath10(e, k.pc, FormatJSON, &PrintCtx{}, nil)
	if err != nil {
		var d *Diag
		if errors.As(err, &d) {
			return Value{}, d
		}
		return Value{}, &Diag{Code: CodeNone, Msg: err.Error()}
	}
	v.canon = canon
	return v, nil
}

// printXPath10 ports lyplg_type_print_xpath10_value: the expression e in format f, the prefixes it
// was written with resolved through resolve, the target prefixes taken from pc. For XML the local
// module is nulled so that all the prefixes are printed.
func printXPath10(e *lyxp.Expr, resolve PrefixCtx, f Format, pc *PrintCtx, budget *printBudget) (string, error) {
	if f == FormatXML {
		defer func(local *schema.Module) { pc.Local = local }(pc.Local)
		pc.Local = nil
	}
	b := printBuilder{p: budget}
	i := 0
	if err := xpath10Subexpr(&b, e, &i, lyxp.TokNone, nil, resolve, f, pc, budget); err != nil {
		return "", err
	}
	return b.String(), nil
}

// xpath10Subexpr ports xpath10_print_subexpr_r: tokens from *i up to end (TokNone: all), name tests
// and literals with their prefixes converted to format f, operators of logic, union and math with
// spaces around (they reset the context module), brackets and parentheses as subexpressions with
// the current context module.
func xpath10Subexpr(b *printBuilder, e *lyxp.Expr, i *int, end lyxp.Tok, ctx *schema.Module,
	resolve PrefixCtx, f Format, pc *PrintCtx, budget *printBudget) error {
	orig := ctx
	for *i < len(e.Toks) {
		tok, text := e.Toks[*i], e.Text(*i)
		if tok == lyxp.TokNameTest || tok == lyxp.TokLiteral {
			s, err := printXPathToken(text, tok == lyxp.TokNameTest, &ctx, resolve, f, pc, budget)
			if err != nil {
				return err
			}
			if err := b.writeString(s); err != nil {
				return err
			}
			*i++
			continue
		}
		if tok == lyxp.TokOperLog || tok == lyxp.TokOperUni || tok == lyxp.TokOperMath {
			if err := budget.charge(len(text)); err != nil {
				return err
			}
			for _, part := range []string{" ", text, " "} {
				if err := b.writeString(part); err != nil {
					return err
				}
			}
			ctx = orig
		} else {
			if err := budget.charge(len(text)); err != nil {
				return err
			}
			if err := b.writeString(text); err != nil {
				return err
			}
		}
		*i++
		switch {
		case end != lyxp.TokNone && tok == end:
			return nil
		case tok == lyxp.TokBrack1:
			if err := xpath10Subexpr(b, e, i, lyxp.TokBrack2, ctx, resolve, f, pc, budget); err != nil {
				return err
			}
		case tok == lyxp.TokPar1:
			if err := xpath10Subexpr(b, e, i, lyxp.TokPar2, ctx, resolve, f, pc, budget); err != nil {
				return err
			}
		}
	}
	return nil
}
