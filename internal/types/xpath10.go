// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/xpath1.0.c (lyplg_type_store_xpath10,
// lyplg_type_print_xpath10_value, xpath10_print_subexpr_r, lyplg_type_xpath10_print_token) and
// src/ly_common.c (ly_value_prefix_next) (BSD-3-Clause, © CESNET).

package types

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

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
	v := Value{typ: a.t, canon: a.lex}
	switch a.f {
	case FormatCanon, FormatJSON:
		return v, nil
	}
	e, msg := lyxp.Lex(a.lex)
	if msg != "" { // the parse above accepted it
		return Value{}, &Diag{Code: CodeNone, Msg: msg}
	}
	var b strings.Builder
	i := 0
	if d := xpath10Subexpr(&b, e, &i, lyxp.TokNone, nil, a.pc); d != nil {
		return Value{}, d
	}
	v.canon = b.String()
	return v, nil
}

// xpath10Subexpr ports xpath10_print_subexpr_r for the JSON format: tokens from *i up to end
// (TokNone: all), name tests and literals with their prefixes converted, operators of logic, union
// and math with spaces around (they reset the context module), brackets and parentheses as
// subexpressions with the current context module.
func xpath10Subexpr(b *strings.Builder, e *lyxp.Expr, i *int, end lyxp.Tok, ctx *schema.Module, pc PrefixCtx) *Diag {
	orig := ctx
	for *i < len(e.Toks) {
		tok, text := e.Toks[*i], e.Text(*i)
		if tok == lyxp.TokNameTest || tok == lyxp.TokLiteral {
			s, d := xpath10Token(text, tok == lyxp.TokNameTest, &ctx, pc)
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
			if d := xpath10Subexpr(b, e, i, lyxp.TokBrack2, ctx, pc); d != nil {
				return d
			}
		case tok == lyxp.TokPar1:
			if d := xpath10Subexpr(b, e, i, lyxp.TokPar2, ctx, pc); d != nil {
				return d
			}
		}
	}
	return nil
}

// xpath10Token ports lyplg_type_xpath10_print_token for the JSON format: each prefix of the token
// resolved through pc and printed as its module name, left out for a name test in the context
// module; an unknown prefix fails a name test and is copied in a literal.
func xpath10Token(tok string, nameTest bool, ctx **schema.Module, pc PrefixCtx) (string, *Diag) {
	var b strings.Builder
	for s := tok; s != ""; {
		n, isPrefix, next := valuePrefixNext(s)
		if n == 0 {
			break
		}
		if !isPrefix {
			b.WriteString(s[:n])
		} else {
			var mod *schema.Module
			if pc != nil {
				mod = pc.Resolve(s[:n])
			}
			if mod == nil && nameTest {
				return "", &Diag{Code: CodeData, Msg: fmt.Sprintf("Failed to resolve prefix \"%s\".", s[:n])}
			}
			switch {
			case nameTest && *ctx == mod: // inherited, not printed again
			case mod != nil:
				b.WriteString(mod.Name + ":")
			default:
				b.WriteString(s[:n] + ":")
			}
			if nameTest {
				*ctx = mod
			}
		}
		s = next
	}
	return b.String(), nil
}

// valuePrefixNext ports ly_value_prefix_next: the length of the first chunk of s, whether it is
// a prefix (an XML QName followed by ':', which is consumed), and the rest of s ("" when done).
func valuePrefixNext(s string) (n int, isPrefix bool, next string) {
	stop, prefix, found := 0, -1, false
	var c rune
	var size int
	read := func() {
		c, size = utf8.DecodeRuneInString(s[stop:])
		stop += size
	}
	for {
		read() // the beginning of a name
		for !lyxp.IsQNameStart(c) && stop < len(s) {
			read()
		}
		if stop >= len(s) {
			break
		}
		prefix = stop - size
		read() // its end
		for lyxp.IsQNameChar(c) && stop < len(s) {
			read()
		}
		found = c == ':'
		if stop >= len(s) || found {
			break
		}
	}
	switch {
	case prefix == 0 && found:
		return stop - size, true, s[stop:]
	case prefix > 0 && found:
		return prefix, false, s[prefix:]
	}
	return stop, false, ""
}
