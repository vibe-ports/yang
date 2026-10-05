// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/path.c (ly_path_parse, ly_path_parse_deref,
// ly_path_check_predicate) (BSD-3-Clause, © CESNET).

package lyxp

import (
	"fmt"
	"strings"
)

// Begin is the LY_PATH_BEGIN_* option.
type Begin uint8

// Begin values: the path must start with '/', or may be relative.
const (
	BeginAbsolute Begin = iota
	BeginEither
)

// Prefix is the LY_PATH_PREFIX_* option.
type Prefix uint8

// Prefix values.
const (
	PrefixOptional Prefix = iota
	PrefixMandatory
	PrefixFirst
	PrefixStrictInherit
)

// Pred is the LY_PATH_PRED_* option.
type Pred uint8

// Pred values.
const (
	PredKeys Pred = iota
	PredSimple
	PredLeafref
)

// Opts are the arguments of ly_path_parse. Leafref is its lref flag; Extended is the
// LY_CTX_LEAFREF_EXTENDED context option (deref(); off unless the context asks for it).
type Opts struct {
	Begin    Begin
	Prefix   Prefix
	Pred     Pred
	Leafref  bool
	Extended bool
}

// ParsePath ports ly_path_parse: it tokenizes src and checks the path grammar, returning
// the tokens or libyang's LYVE_XPATH message.
func ParsePath(src string, o Opts) (*Expr, string) {
	if i := strings.IndexByte(src, 0); i >= 0 {
		src = src[:i] // a C string ends at NUL
	}
	if o.Begin == BeginAbsolute && (src == "" || src[0] != '/') {
		return nil, fmt.Sprintf("XPath \"%s\" was expected to be absolute.", src)
	}
	e, msg := Lex(src)
	if msg != "" {
		return nil, msg
	}
	i, isAbs := 0, true
	switch {
	case o.Begin == BeginAbsolute || e.Is(0, TokOperPath):
		i++
	default: // relative
		isAbs = false
		if !o.Leafref {
			break
		}
		if o.Extended && e.Is(i, TokFuncName) {
			if i, msg = e.deref(i, o); msg != "" {
				return nil, msg
			}
			if i, msg = e.next(i, TokOperPath); msg != "" {
				return nil, msg
			}
		}
		if i, msg = e.next(i, TokDDot); msg != "" {
			return nil, msg
		}
		for {
			if i, msg = e.next(i, TokOperPath); msg != "" {
				return nil, msg
			}
			if !e.Is(i, TokDDot) {
				break
			}
			i++
		}
	}
	prev := ""
	for {
		if msg = e.Check(i, TokNameTest); msg != "" {
			return nil, msg
		}
		cur := e.Text(i)
		colon := strings.IndexByte(cur, ':')
		switch {
		case o.Prefix == PrefixMandatory:
			if colon < 0 {
				return nil, fmt.Sprintf("Prefix missing for \"%s\" in path.", cur)
			}
		case (o.Prefix == PrefixFirst || o.Prefix == PrefixStrictInherit) && prev == "" && isAbs:
			if colon < 0 {
				return nil, fmt.Sprintf("Prefix missing for \"%s\" in path.", cur)
			}
			prev = cur
		case o.Prefix == PrefixStrictInherit && prev != "" && colon >= 0:
			if p := strings.IndexByte(prev, ':'); p == colon && prev[:p] == cur[:colon] {
				return nil, fmt.Sprintf("Duplicate prefix for \"%s\" in path.", cur)
			}
			prev = cur
		}
		i++
		if i, msg = e.CheckPredicate(i, o.Prefix, o.Pred); msg != "" {
			return nil, msg
		}
		if !e.Is(i, TokOperPath) {
			break
		}
		i++
	}
	if i < len(e.Toks) {
		return nil, fmt.Sprintf("Unparsed characters \"%s\" left at the end of path.", e.Rest(i))
	}
	return e, ""
}

// next is lyxp_next_token: check token i, skip it.
func (e *Expr) next(i int, t Tok) (int, string) {
	if msg := e.Check(i, t); msg != "" {
		return i, msg
	}
	return i + 1, ""
}

// deref ports ly_path_parse_deref: the `deref(path)` prefix of an extended leafref, i at its name.
func (e *Expr) deref(i int, o Opts) (int, string) {
	i, msg := e.next(i, TokFuncName)
	if msg != "" {
		return i, msg
	}
	// libyang tests the token after the name, which is always '(', so this never fires.
	if i < len(e.Toks) && strings.HasPrefix(e.Rest(i), "deref") {
		return i, fmt.Sprintf("Unexpected XPath function \"%s\" in path, expected \"deref(...)\"", e.Text(i))
	}
	if i, msg = e.next(i, TokPar1); msg != "" {
		return i, msg
	}
	begin := i
	for i < len(e.Toks) && e.Toks[i] != TokPar2 {
		if e.Toks[i] == TokFuncName {
			return i, "Embedded function XPath function inside deref function within the path is not allowed"
		}
		i++
	}
	if i, msg = e.next(i, TokPar2); msg != "" {
		return i, msg
	}
	arg := e.Src[e.Pos[begin]:e.Pos[i-1]]
	if arg == "" { // path_len 0 means "to the end of the string"
		arg = e.Src[e.Pos[begin]:]
	}
	_, msg = ParsePath(arg, Opts{BeginEither, PrefixOptional, PredLeafref, true, o.Extended})
	return i, msg
}

// CheckPredicate ports ly_path_check_predicate: the optional predicates at token i, returning
// the index after them. It checks syntax only.
func (e *Expr) CheckPredicate(i int, prefix Prefix, pred Pred) (int, string) {
	if !e.Is(i, TokBrack1) {
		return i, ""
	}
	i++
	var msg string
	switch {
	case e.Is(i, TokNameTest): // key predicates (all three preds)
		leafref := pred == PredLeafref
		var seen []string
		for {
			if msg = e.Check(i, TokNameTest); msg != "" {
				return i, msg
			}
			full := e.Text(i)
			c := strings.IndexByte(full, ':')
			if !leafref && prefix == PrefixMandatory && c < 0 {
				return i, fmt.Sprintf("Prefix missing for \"%s\" in path.", full)
			}
			if !leafref && prefix == PrefixStrictInherit && c >= 0 {
				return i, fmt.Sprintf("Redundant prefix for \"%s\" in path.", full)
			}
			name := full[c+1:] // c == -1 keeps all of it
			for _, s := range seen {
				if s == name {
					return i, fmt.Sprintf("Duplicate predicate key \"%s\" in path.", name)
				}
			}
			seen = append(seen, name)
			if i, msg = e.next(i+1, TokOperEqual); msg != "" {
				return i, msg
			}
			switch {
			case leafref:
				i, msg = e.leafrefKey(i)
			case e.Is(i, TokLiteral) || e.Is(i, TokNumber) || e.Is(i, TokVarRef):
				i++
			default:
				msg = e.Check(i, TokLiteral)
			}
			if msg != "" {
				return i, msg
			}
			if i, msg = e.next(i, TokBrack2); msg != "" {
				return i, msg
			}
			if !e.Is(i, TokBrack1) {
				return i, ""
			}
			i++
		}
	case pred == PredSimple && e.Is(i, TokDot):
		if i, msg = e.next(i+1, TokOperEqual); msg != "" {
			return i, msg
		}
		if msg = e.Check(i, TokNone); msg != "" {
			return i, msg
		}
		if !e.Is(i, TokLiteral) && !e.Is(i, TokNumber) {
			return i, fmt.Sprintf("Unexpected XPath token \"%s\" (\"%.15s\").", e.Toks[i], e.Rest(i))
		}
		i++
	case pred == PredSimple && e.Is(i, TokNumber):
		if atoi(e.Text(i)) == 0 {
			return i, fmt.Sprintf("Invalid positional predicate \"%s\".", e.Text(i))
		}
		i++
	case i >= len(e.Toks):
		return i, errXPEOF
	default:
		return i, fmt.Sprintf("Unexpected XPath token \"%s\" (\"%.15s\").", e.Toks[i], e.Rest(i))
	}
	return e.next(i, TokBrack2)
}

// leafrefKey is the right side of a leafref predicate: current()/..(/..)*/name(/name)*.
func (e *Expr) leafrefKey(i int) (int, string) {
	if msg := e.Check(i, TokFuncName); msg != "" {
		return i, msg
	}
	if e.Text(i) != "current" {
		return i, fmt.Sprintf("Invalid function \"%s\" invocation in path.", e.Text(i))
	}
	i++
	var msg string
	for _, t := range []Tok{TokPar1, TokPar2, TokOperPath, TokDDot} {
		if i, msg = e.next(i, t); msg != "" {
			return i, msg
		}
	}
	for {
		if i, msg = e.next(i, TokOperPath); msg != "" {
			return i, msg
		}
		if !e.Is(i, TokDDot) {
			break
		}
		i++
	}
	if i, msg = e.next(i, TokNameTest); msg != "" {
		return i, msg
	}
	for e.Is(i, TokOperPath) {
		if i, msg = e.next(i+1, TokNameTest); msg != "" {
			return i, msg
		}
	}
	return i, ""
}

// atoi is C atoi on the digit prefix (0 when there is none).
func atoi(s string) int {
	n := 0
	for i := 0; i < len(s) && isDigit(s[i]); i++ {
		n = n*10 + int(s[i]-'0')
		if n > 1<<30 {
			break
		}
	}
	return n
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
