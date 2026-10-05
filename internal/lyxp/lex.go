// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (lyxp_expr_parse, parse_ncname, expr_parse_axis,
// lyxp_check_token, lyxp_token2str) (BSD-3-Clause, © CESNET).

// Package lyxp is the libyang XPath tokenizer (lyxp_expr_parse) and the path grammar built on
// it (ly_path_parse, ly_path_check_predicate). Stdlib only, so the YANG parser, the type
// plugins and the XPath evaluator can share it. Errors are libyang's LYVE_XPATH messages.
package lyxp

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Tok is a token kind (enum lyxp_token).
type Tok uint8

// Token kinds, in libyang's order.
const (
	TokNone Tok = iota
	TokPar1
	TokPar2
	TokBrack1
	TokBrack2
	TokDot
	TokDDot
	TokAt
	TokComma
	TokDColon
	TokNameTest
	TokNodeType
	TokFuncName
	TokOperLog
	TokOperEqual
	TokOperNEqual
	TokOperComp
	TokOperMath
	TokOperUni
	TokOperPath
	TokOperRPath
	TokAxisName
	TokLiteral
	TokNumber
	TokVarRef
)

var tokNames = [...]string{"none", "(", ")", "[", "]", ".", "..", "@", ",", "::", "NameTest", "NodeType",
	"FunctionName", "Operator(Logic)", "Operator(Equal)", "Operator(Non-equal)", "Operator(Comparison)",
	"Operator(Math)", "Operator(Union)", "Operator(Path)", "Operator(Recursive Path)", "AxisName", "Literal",
	"Number", "VariableReference"}

func (t Tok) String() string { return tokNames[t] }

// Expr is a tokenized expression (the token part of struct lyxp_expr).
type Expr struct {
	Src  string
	Toks []Tok
	Pos  []int
	Len  []int
}

// Text is the text of token i.
func (e *Expr) Text(i int) string { return e.Src[e.Pos[i] : e.Pos[i]+e.Len[i]] }

// Rest is the expression from token i on (C prints &expr[tok_pos] as a string).
func (e *Expr) Rest(i int) string { return e.Src[e.Pos[i]:] }

func isXMLWS(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isNameStart(c rune) bool {
	return c >= 'a' && c <= 'z' || c == '_' || c >= 'A' && c <= 'Z' ||
		c >= 0x370 && c <= 0x1fff && c != 0x37e || c >= 0xc0 && c <= 0x2ff && c != 0xd7 && c != 0xf7 ||
		c == 0x200c || c == 0x200d || c >= 0x2070 && c <= 0x218f || c >= 0x2c00 && c <= 0x2fef ||
		c >= 0x3001 && c <= 0xd7ff || c >= 0xf900 && c <= 0xfdcf || c >= 0xfdf0 && c <= 0xfffd ||
		c >= 0x10000 && c <= 0xeffff
}

func isNameChar(c rune) bool {
	return isNameStart(c) || c == '-' || c >= '0' && c <= '9' || c == '.' || c == 0xb7 ||
		c >= 0x300 && c <= 0x36f || c >= 0x203f && c <= 0x2040
}

// getUTF8 ports ly_getutf8: the code point and its size, size 0 when invalid.
func getUTF8(s string) (rune, int) {
	if s == "" {
		return 0, 0
	}
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && n <= 1 || r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0xfffe || r == 0xffff {
		return 0, 0
	}
	return r, n
}

// parseNCName ports parse_ncname: the length of the NCName at s, 0 if none starts there and
// -n if an invalid character follows n valid bytes.
func parseNCName(s string) int {
	c, n := getUTF8(s)
	if n == 0 || !isNameStart(c) {
		return 0
	}
	l := 0
	for {
		l += n
		if l >= len(s) || s[l] == 0 {
			return l
		}
		if c, n = getUTF8(s[l:]); n == 0 {
			return -l
		}
		if !isNameChar(c) {
			return l
		}
	}
}

var axes = map[string]bool{"self": true, "child": true, "parent": true, "ancestor": true, "attribute": true,
	"following": true, "namespace": true, "preceding": true, "descendant": true, "ancestor-or-self": true,
	"following-sibling": true, "preceding-sibling": true, "descendant-or-self": true}

const errXPEOF = "Unexpected XPath expression end."

// Lex ports lyxp_expr_parse without reparse: it only tokenizes.
func Lex(src string) (*Expr, string) {
	if src == "" || src[0] == 0 {
		return nil, errXPEOF
	}
	if i := strings.IndexByte(src, 0); i >= 0 {
		src = src[:i]
	}
	e := &Expr{Src: src}
	at := func(i int) byte {
		if i < len(src) {
			return src[i]
		}
		return 0
	}
	inexpr := func(p int) string {
		return fmt.Sprintf("Invalid character '%c'[%d] of expression '%s'.", at(p), p+1, src)
	}
	add := func(t Tok, p, l int) {
		e.Toks, e.Pos, e.Len = append(e.Toks, t), append(e.Pos, p), append(e.Len, l)
	}
	last := func() Tok {
		if len(e.Toks) == 0 {
			return TokNone
		}
		return e.Toks[len(e.Toks)-1]
	}
	p := 0
	for p < len(src) && isXMLWS(src[p]) {
		p++
	}
	prevFunc, prevNType := false, false
	for {
		var tl int
		var tt Tok
		c := at(p)
		switch {
		case c == '(':
			tl, tt = 1, TokPar1
			if n := len(e.Toks); n > 0 && e.Toks[n-1] == TokNameTest {
				name := e.Text(n - 1)
				if prevNType && (name == "node" || name == "text" || name == "comment") {
					e.Toks[n-1] = TokNodeType
					prevNType, prevFunc = false, false
				} else if prevFunc {
					e.Toks[n-1] = TokFuncName
					prevNType, prevFunc = false, false
				}
			}
		case c == ')':
			tl, tt = 1, TokPar2
		case c == '[':
			tl, tt = 1, TokBrack1
		case c == ']':
			tl, tt = 1, TokBrack2
		case strings.HasPrefix(src[p:], ".."):
			tl, tt = 2, TokDDot
		case c == '.' && !isDigit(at(p+1)):
			tl, tt = 1, TokDot
		case c == '@':
			tl, tt = 1, TokAt
		case c == ',':
			tl, tt = 1, TokComma
		case c == '\'' || c == '"':
			end := strings.IndexByte(src[p+1:], c)
			if end < 0 {
				q := src[p:]
				if len(q) > 15 {
					q = q[:15]
				}
				return nil, fmt.Sprintf("Unterminated string delimited with %c (%s).", c, q)
			}
			tl, tt = end+2, TokLiteral
		case c == '.' || isDigit(c):
			tl = 0
			for isDigit(at(p + tl)) {
				tl++
			}
			if at(p+tl) == '.' {
				tl++
				for isDigit(at(p + tl)) {
					tl++
				}
			}
			tt = TokNumber
		case c == '$':
			p++
			n := parseNCName(src[p:])
			if n < 1 {
				return nil, inexpr(p - n)
			}
			if at(p+n) == ':' {
				return nil, "Variable with prefix is not supported."
			}
			tl, tt = n, TokVarRef
		case c == '/':
			tl, tt = 1, TokOperPath
			if at(p+1) == '/' {
				tl, tt = 2, TokOperRPath
			}
		case strings.HasPrefix(src[p:], "!="):
			tl, tt = 2, TokOperNEqual
		case strings.HasPrefix(src[p:], "<=") || strings.HasPrefix(src[p:], ">="):
			tl, tt = 2, TokOperComp
		case c == '|':
			tl, tt = 1, TokOperUni
		case c == '+' || c == '-':
			tl, tt = 1, TokOperMath
		case c == '=':
			tl, tt = 1, TokOperEqual
		case c == '<' || c == '>':
			tl, tt = 1, TokOperComp
		case len(e.Toks) > 0 && !afterOperand(last()):
			switch {
			case c == '*':
				tl, tt = 1, TokOperMath
			case strings.HasPrefix(src[p:], "or"):
				tl, tt = 2, TokOperLog
			case strings.HasPrefix(src[p:], "and"):
				tl, tt = 3, TokOperLog
			case strings.HasPrefix(src[p:], "mod") || strings.HasPrefix(src[p:], "div"):
				tl, tt = 3, TokOperMath
			case prevNType || prevFunc:
				return nil, fmt.Sprintf("Invalid character 0x%x ('%c'), perhaps \"%s\" is supposed to be a function call.",
					c, c, e.Text(len(e.Toks)-1))
			default:
				return nil, inexpr(p)
			}
		default:
			n := 1
			if c != '*' {
				if n = parseNCName(src[p:]); n < 1 {
					return nil, inexpr(p - n)
				}
			}
			tl = n
			hasAxis := false
			if strings.HasPrefix(src[p+tl:], "::") {
				if !axes[src[p:p+n]] {
					return nil, inexpr(p)
				}
				add(TokAxisName, p, tl)
				p += tl
				add(TokDColon, p, 2)
				p += 2
				n = 1
				if at(p) != '*' {
					if n = parseNCName(src[p:]); n < 1 {
						return nil, inexpr(p - n)
					}
				}
				tl, hasAxis = n, true
			}
			if at(p+tl) == ':' {
				tl++
				if at(p+tl) == '*' {
					tl++
				} else {
					n := parseNCName(src[p+tl:])
					if n < 1 {
						return nil, inexpr(p - n)
					}
					tl += n
				}
				prevNType, prevFunc = false, false
			} else {
				prevNType = at(p) != '*'
				prevFunc = prevNType && !hasAxis
			}
			tt = TokNameTest
		}
		add(tt, p, tl)
		p += tl
		for p < len(src) && isXMLWS(src[p]) {
			p++
		}
		if p >= len(src) {
			return e, ""
		}
	}
}

// afterOperand: the previous token cannot be followed by an operator-name ('*', "and", ...).
func afterOperand(t Tok) bool {
	switch t {
	case TokAt, TokPar1, TokBrack1, TokComma, TokOperLog, TokOperEqual, TokOperNEqual, TokOperComp,
		TokOperMath, TokOperUni, TokOperPath, TokOperRPath:
		return true
	}
	return false
}

// Check ports lyxp_check_token: "" when token i is want (TokNone = any token).
func (e *Expr) Check(i int, want Tok) string {
	if i >= len(e.Toks) {
		return errXPEOF
	}
	if want != TokNone && e.Toks[i] != want {
		return fmt.Sprintf("Unexpected XPath token \"%s\" (\"%s\"), expected \"%s\".", e.Toks[i], Trunc15(e.Rest(i)), want)
	}
	return ""
}

// Is reports whether token i exists and is t (lyxp_check_token without logging).
func (e *Expr) Is(i int, t Tok) bool { return i < len(e.Toks) && e.Toks[i] == t }
