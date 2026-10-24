// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/instanceid_keys.c, src/plugins_types/xpath1.0.c
// (lyplg_type_xpath10_print_token) and src/ly_common.c (ly_value_prefix_next)
// (BSD-3-Clause, © CESNET).

package types

import (
	"strings"
	"unicode/utf8"

	"github.com/vibe-ports/yang/internal/lyxml"
	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/schema"
)

func init() {
	plugins[pluginKey{"yang", "", "instance-identifier-keys"}] = &plugin{id: "instance-identifier-keys", store: storeInstanceIDKeys}
}

// keysValue is the stored form of yang:instance-identifier-keys (struct
// lyd_value_instance_identifier_keys) and of ietf-yang-types:xpath1.0 (struct lyd_value_xpath10,
// the same layout): the parsed expression and the prefix data of the value.
type keysValue struct {
	e  *lyxp.Expr
	f  Format
	pc PrefixCtx // resolves the prefixes the value was written with
}

// prefixSnap is the prefix data libyang copies out of the parser's namespace context
// (lyplg_type_prefix_data_new): only the prefixes the value uses.
type prefixSnap map[string]*schema.Module

// Resolve implements PrefixCtx.
func (p prefixSnap) Resolve(prefix string) *schema.Module { return p[prefix] }

// prefixDataNew ports lyplg_type_prefix_data_new: the prefix data a value keeps for printing it
// later. For the schema formats the modules of the prefixes the value uses are copied out of a.pc;
// for XML the namespace declarations they use, and the default one, are (ly_store_prefix_data
// copies prefix - URI pairs, resolved in the value's context when it is printed); the parser's
// namespace context does not outlive the value. The other formats name modules and keep a.pc.
func prefixDataNew(a *storeArgs) (PrefixCtx, *Diag) {
	switch a.f {
	case FormatSchema, FormatSchemaResolved:
	case FormatXML:
		if x, ok := a.pc.(XMLScope); ok {
			return xmlPrefixData(a.lex, x)
		}
	default:
		return a.pc, nil
	}
	snap := prefixSnap{}
	if a.pc != nil {
		// XML's default namespace and schema text's local module are also
		// needed by union members with an unprefixed identityref value.
		snap[""] = a.pc.Resolve("")
	}
	for i := 0; ; {
		n, isPrefix, next, ok := lyxml.ValuePrefixNext(a.lex, i)
		if !ok {
			// lyplg_type_prefix_data_new propagates ly_store_prefix_data's
			// LY_EINVAL without an error item. lyd_value_store then emits this
			// generic LYVE_OTHER item (tree_data_common.c:514-526).
			return nil, &Diag{Code: CodeOther, Msg: "Storing value failed.", Err: "LY_EINVAL", NoItem: true}
		}
		if n == 0 {
			break
		}
		if isPrefix && a.pc != nil {
			p := a.lex[i : i+n]
			if m := a.pc.Resolve(p); m != nil {
				snap[p] = m
			}
		}
		if next < 0 {
			break
		}
		i = next
	}
	return snap, nil
}

// xmlPrefixData is the LY_VALUE_XML case of ly_store_prefix_data (tree_data_common.c:1378-1427):
// the default namespace, then each declared prefix the value uses, as prefix - URI pairs.
func xmlPrefixData(lex string, x XMLScope) (PrefixCtx, *Diag) {
	snap := XMLNamespaces{Set: x.Context(), NS: map[string]string{}}
	if u, ok := x.Namespace(""); ok {
		snap.NS[""], snap.Order = u, append(snap.Order, "")
	}
	for i := 0; ; {
		n, isPrefix, next, ok := lyxml.ValuePrefixNext(lex, i)
		if !ok {
			return nil, &Diag{Code: CodeOther, Msg: "Storing value failed.", Err: "LY_EINVAL", NoItem: true}
		}
		if n == 0 {
			break
		}
		if p := lex[i : i+n]; isPrefix {
			if _, done := snap.NS[p]; !done {
				if u, ok := x.Namespace(p); ok {
					snap.NS[p], snap.Order = u, append(snap.Order, p)
				}
			}
		}
		if next < 0 {
			break
		}
		i = next
	}
	return snap, nil
}

// storeInstanceIDKeys ports lyplg_type_store_instanceid_keys.
func storeInstanceIDKeys(a *storeArgs) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	if a.t.Length != nil {
		if d := checkRange(schema.String, a.t.Length, int64(utf8.RuneCountInString(a.lex)), a.lex); d != nil {
			return Value{}, d
		}
	}
	if d := checkPatterns(a.t.Patterns, a.lex); d != nil {
		return Value{}, d
	}
	// optional prefixes, even though they should be mandatory
	if a.lex != "" && a.lex[0] != '[' {
		return Value{}, errf("Invalid first character '%s', list key predicates expected.", a.lex[:1])
	}
	e, msg := lyxp.ParsePredicate(a.lex, lyxp.PrefixOptional, lyxp.PredKeys)
	if msg != "" {
		return Value{}, errf("%s", msg)
	}
	pc, d := prefixDataNew(a)
	if d != nil {
		return Value{}, d
	}
	k := &keysValue{e: e, f: a.f, pc: pc}
	v := Value{typ: a.t, ext: k, canon: a.lex}
	switch a.f {
	case FormatSchema, FormatSchemaResolved, FormatXML:
		// the JSON format with prefixes is the canonical one
		canon, d := printKeys(k, FormatJSON, &PrintCtx{})
		if d != nil {
			return Value{}, d
		}
		v.canon = canon
	}
	return v, nil
}

// printKeys ports instanceid_keys_print_value, which only the store calls (for the canonical JSON
// form): the predicates in format f, tokens that may carry prefixes converted and the others
// copied, with no per-bracket context. Printing in other formats goes through printXPath10.
func printKeys(k *keysValue, f Format, pc *PrintCtx) (string, *Diag) {
	if f == FormatXML { // null the local module so that all the prefixes are printed
		defer func(local *schema.Module) { pc.Local = local }(pc.Local)
		pc.Local = nil
	}
	var b strings.Builder
	var ctxMod *schema.Module
	for i, t := range k.e.Toks {
		if t != lyxp.TokNameTest && t != lyxp.TokLiteral {
			b.WriteString(k.e.Text(i))
			continue
		}
		s, d := printXPathToken(k.e.Text(i), t == lyxp.TokNameTest, &ctxMod, k.pc, f, pc)
		if d != nil {
			return "", d
		}
		b.WriteString(s)
	}
	return b.String(), nil
}

// printXPathToken ports lyplg_type_xpath10_print_token: a token in the target format f with the
// prefix data pc. resolve finds the modules of the prefixes the token was written with; ctxMod is
// the module of the previous name test, which the JSON formats inherit instead of repeating it.
func printXPathToken(tok string, isNameTest bool, ctxMod **schema.Module, resolve PrefixCtx, f Format, pc *PrintCtx) (string, *Diag) {
	var b strings.Builder
	hasPrefix := false
	for i := 0; ; {
		n, isPrefix, next, ok := lyxml.ValuePrefixNext(tok, i)
		if !ok {
			return "", &Diag{Err: "LY_EINVAL", NoItem: true}
		}
		if n == 0 {
			break
		}
		seg := tok[i : i+n]
		switch {
		case !isPrefix && !hasPrefix && isNameTest && f == FormatXML && *ctxMod != nil:
			b.WriteString(pc.prefix(*ctxMod, f) + ":" + seg)
		case !isPrefix:
			// the first expression node may come without a prefix, which is allowed
			b.WriteString(seg)
		default:
			hasPrefix = true
			var mod *schema.Module
			if resolve != nil {
				mod = resolve.Resolve(seg)
			}
			if mod == nil && isNameTest {
				return "", errf("Failed to resolve prefix \"%s\".", seg)
			}
			// the JSON formats inherit the prefix of the previous name test and do not print it again
			if !isNameTest || (f != FormatJSON && f != FormatCanon) || *ctxMod != mod {
				p := seg // an invalid prefix is just copied
				if mod != nil {
					p = pc.prefix(mod, f)
				}
				b.WriteString(p + ":")
			}
			if isNameTest {
				*ctxMod = mod
			}
		}
		if next < 0 {
			break
		}
		i = next
	}
	return b.String(), nil
}
