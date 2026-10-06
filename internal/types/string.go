// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/string.c and src/plugins_types.c
// (lyplg_type_validate_patterns) (BSD-3-Clause, © CESNET).

package types

import (
	"fmt"
	"unicode/utf8"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/xsdre"
)

// storeString ports lyplg_type_store_string + lyplg_type_validate_value_string.
func storeString(a *storeArgs) (Value, *Diag) {
	if d := checkChars(a.lex); d != nil {
		return Value{}, d
	}
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	if a.only {
		return Value{typ: a.t, canon: a.lex}, nil
	}
	return storeStringRestrictions(a)
}

// storeStringRestrictions ports lyplg_type_validate_value_string: length in characters, then
// patterns.
func storeStringRestrictions(a *storeArgs) (Value, *Diag) {
	if d := validateString(a.t, a.lex); d != nil {
		return Value{}, d
	}
	return Value{typ: a.t, canon: a.lex}, nil
}

// validateString is lyplg_type_validate_value_string on the canonical value s.
func validateString(t *schema.Type, s string) *Diag {
	if t.Length != nil {
		n := int64(utf8.RuneCountInString(s))
		if d := checkRange(schema.String, t.Length, n, s); d != nil {
			return d
		}
	}
	return checkPatterns(t.Patterns, s)
}

// checkChars ports string_check_chars / ly_checkutf8: valid UTF-8 without surrogates and without
// control characters other than tab, LF and CR.
func checkChars(s string) *Diag {
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && n <= 1) || (r < 0x20 && r != '\t' && r != '\n' && r != '\r') {
			return errf("Invalid character 0x%x.", s[i])
		}
		i += n
	}
	return nil
}

// CompilePattern compiles p and records the program in p.Compiled; compile calls it once per
// pattern so that stores never recompile.
func CompilePattern(p *schema.Pattern) error {
	re, err := xsdre.Compile(p.Expr)
	if err != nil {
		return err
	}
	p.Compiled = re
	return nil
}

// checkPatterns ports lyplg_type_validate_patterns.
func checkPatterns(pats []*schema.Pattern, s string) *Diag {
	for _, p := range pats {
		re, ok := p.Compiled.(*xsdre.Pattern)
		if !ok { // not prepared by compile (tests, hand-built types)
			var err error
			if re, err = xsdre.Compile(p.Expr); err != nil {
				return errf("Regular expression \"%s\" is not valid (%v).", p.Expr, err)
			}
		}
		if re.Match(s) == p.Invert {
			if p.Msg != "" {
				return &Diag{Code: CodeData, Msg: p.Msg, AppTag: p.AppTag}
			}
			inv := ""
			if p.Invert {
				inv = "inverted "
			}
			return &Diag{Code: CodeData, AppTag: p.AppTag,
				Msg: fmt.Sprintf("Unsatisfied pattern - \"%s\" does not match %s\"%s\".", s, inv, p.Expr)}
		}
	}
	return nil
}
