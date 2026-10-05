// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (lyxp_expr_parse, parse_ncname, expr_parse_axis,
// lyxp_check_token, lyxp_token2str) (BSD-3-Clause, © CESNET).

package types

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// The XPath tokenizer instance-identifier values go through (libyang parses them with the generic
// XPath lexer and re-checks the token sequence in path.c). internal/xpath owns full XPath; this
// is the lexer alone, kept here so types does not depend on the evaluator.

type xpTok uint8

const (
	tokNone xpTok = iota
	tokPar1
	tokPar2
	tokBrack1
	tokBrack2
	tokDot
	tokDDot
	tokAt
	tokComma
	tokDColon
	tokNameTest
	tokNodeType
	tokFuncName
	tokOperLog
	tokOperEqual
	tokOperNEqual
	tokOperComp
	tokOperMath
	tokOperUni
	tokOperPath
	tokOperRPath
	tokAxisName
	tokLiteral
	tokNumber
	tokVarRef
)

var tokNames = [...]string{"none", "(", ")", "[", "]", ".", "..", "@", ",", "::", "NameTest", "NodeType",
	"FunctionName", "Operator(Logic)", "Operator(Equal)", "Operator(Non-equal)", "Operator(Comparison)",
	"Operator(Math)", "Operator(Union)", "Operator(Path)", "Operator(Recursive Path)", "AxisName", "Literal",
	"Number", "VariableReference"}

func (t xpTok) String() string { return tokNames[t] }

type xpExpr struct {
	src  string
	toks []xpTok
	pos  []int
	len  []int
}

func (e *xpExpr) text(i int) string { return e.src[e.pos[i] : e.pos[i]+e.len[i]] }

// rest is the expression from token i on (C prints &expr[tok_pos] as a string).
func (e *xpExpr) rest(i int) string { return e.src[e.pos[i]:] }

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

// xpLex ports lyxp_expr_parse without reparse: it only tokenizes.
func xpLex(src string) (*xpExpr, string) {
	if src == "" || src[0] == 0 {
		return nil, errXPEOF
	}
	if i := strings.IndexByte(src, 0); i >= 0 {
		src = src[:i]
	}
	e := &xpExpr{src: src}
	at := func(i int) byte {
		if i < len(src) {
			return src[i]
		}
		return 0
	}
	inexpr := func(p int) string {
		return fmt.Sprintf("Invalid character '%c'[%d] of expression '%s'.", at(p), p+1, src)
	}
	add := func(t xpTok, p, l int) {
		e.toks, e.pos, e.len = append(e.toks, t), append(e.pos, p), append(e.len, l)
	}
	last := func() xpTok {
		if len(e.toks) == 0 {
			return tokNone
		}
		return e.toks[len(e.toks)-1]
	}
	p := 0
	for p < len(src) && isXMLWS(src[p]) {
		p++
	}
	prevFunc, prevNType := false, false
	for {
		var tl int
		var tt xpTok
		c := at(p)
		switch {
		case c == '(':
			tl, tt = 1, tokPar1
			if n := len(e.toks); n > 0 && e.toks[n-1] == tokNameTest {
				name := e.text(n - 1)
				if prevNType && (name == "node" || name == "text" || name == "comment") {
					e.toks[n-1] = tokNodeType
					prevNType, prevFunc = false, false
				} else if prevFunc {
					e.toks[n-1] = tokFuncName
					prevNType, prevFunc = false, false
				}
			}
		case c == ')':
			tl, tt = 1, tokPar2
		case c == '[':
			tl, tt = 1, tokBrack1
		case c == ']':
			tl, tt = 1, tokBrack2
		case strings.HasPrefix(src[p:], ".."):
			tl, tt = 2, tokDDot
		case c == '.' && !isDigit(at(p+1)):
			tl, tt = 1, tokDot
		case c == '@':
			tl, tt = 1, tokAt
		case c == ',':
			tl, tt = 1, tokComma
		case c == '\'' || c == '"':
			end := strings.IndexByte(src[p+1:], c)
			if end < 0 {
				q := src[p:]
				if len(q) > 15 {
					q = q[:15]
				}
				return nil, fmt.Sprintf("Unterminated string delimited with %c (%s).", c, q)
			}
			tl, tt = end+2, tokLiteral
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
			tt = tokNumber
		case c == '$':
			p++
			n := parseNCName(src[p:])
			if n < 1 {
				return nil, inexpr(p - n)
			}
			if at(p+n) == ':' {
				return nil, "Variable with prefix is not supported."
			}
			tl, tt = n, tokVarRef
		case c == '/':
			tl, tt = 1, tokOperPath
			if at(p+1) == '/' {
				tl, tt = 2, tokOperRPath
			}
		case strings.HasPrefix(src[p:], "!="):
			tl, tt = 2, tokOperNEqual
		case strings.HasPrefix(src[p:], "<=") || strings.HasPrefix(src[p:], ">="):
			tl, tt = 2, tokOperComp
		case c == '|':
			tl, tt = 1, tokOperUni
		case c == '+' || c == '-':
			tl, tt = 1, tokOperMath
		case c == '=':
			tl, tt = 1, tokOperEqual
		case c == '<' || c == '>':
			tl, tt = 1, tokOperComp
		case len(e.toks) > 0 && !afterOperand(last()):
			switch {
			case c == '*':
				tl, tt = 1, tokOperMath
			case strings.HasPrefix(src[p:], "or"):
				tl, tt = 2, tokOperLog
			case strings.HasPrefix(src[p:], "and"):
				tl, tt = 3, tokOperLog
			case strings.HasPrefix(src[p:], "mod") || strings.HasPrefix(src[p:], "div"):
				tl, tt = 3, tokOperMath
			case prevNType || prevFunc:
				return nil, fmt.Sprintf("Invalid character 0x%x ('%c'), perhaps \"%s\" is supposed to be a function call.",
					c, c, e.text(len(e.toks)-1))
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
				add(tokAxisName, p, tl)
				p += tl
				add(tokDColon, p, 2)
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
			tt = tokNameTest
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
func afterOperand(t xpTok) bool {
	switch t {
	case tokAt, tokPar1, tokBrack1, tokComma, tokOperLog, tokOperEqual, tokOperNEqual, tokOperComp,
		tokOperMath, tokOperUni, tokOperPath, tokOperRPath:
		return true
	}
	return false
}

// check ports lyxp_check_token: "" when token i is want (tokNone = any token).
func (e *xpExpr) check(i int, want xpTok) string {
	if i >= len(e.toks) {
		return errXPEOF
	}
	if want != tokNone && e.toks[i] != want {
		return fmt.Sprintf("Unexpected XPath token \"%s\" (\"%.15s\"), expected \"%s\".", e.toks[i], e.rest(i), want)
	}
	return ""
}

// is reports whether token i exists and is t (lyxp_check_token without logging).
func (e *xpExpr) is(i int, t xpTok) bool { return i < len(e.toks) && e.toks[i] == t }
