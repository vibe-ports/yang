// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (BSD-3-Clause, © CESNET).

package xpath

import "strings"

type tokKind uint8

const (
	tNone tokKind = iota
	tPar1
	tPar2
	tBrack1
	tBrack2
	tDot
	tDDot
	tAt
	tComma
	tDColon
	tNameTest
	tNodeType
	tVarRef
	tFuncName
	tOperLog
	tOperEqual
	tOperNEqual
	tOperComp
	tOperMath
	tOperUni
	tOperPath
	tOperRPath
	tAxisName
	tLiteral
	tNumber
)

// tokNames is lyxp_token2str.
var tokNames = [...]string{"", "(", ")", "[", "]", ".", "..", "@", ",", "::", "NameTest", "NodeType",
	"VariableReference", "FunctionName", "Operator(Logic)", "Operator(Equal)", "Operator(Non-equal)",
	"Operator(Comparison)", "Operator(Math)", "Operator(Union)", "Operator(Path)",
	"Operator(Recursive Path)", "AxisName", "Literal", "Number"}

type token struct {
	k        tokKind
	pos, len int
}

var axisNames = map[string]bool{"self": true, "child": true, "parent": true, "ancestor": true,
	"attribute": true, "following": true, "preceding": true, "descendant": true, "ancestor-or-self": true,
	"following-sibling": true, "preceding-sibling": true, "descendant-or-self": true}

func isXMLWS(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isQNameStart(c rune) bool {
	return (c >= 'a' && c <= 'z') || c == '_' || (c >= 'A' && c <= 'Z') ||
		(c >= 0x370 && c <= 0x1fff && c != 0x37e) || (c >= 0xc0 && c <= 0x2ff && c != 0xd7 && c != 0xf7) ||
		c == 0x200c || c == 0x200d || (c >= 0x2070 && c <= 0x218f) || (c >= 0x2c00 && c <= 0x2fef) ||
		(c >= 0x3001 && c <= 0xd7ff) || (c >= 0xf900 && c <= 0xfdcf) || (c >= 0xfdf0 && c <= 0xfffd) ||
		(c >= 0x10000 && c <= 0xeffff)
}

func isQNameChar(c rune) bool {
	return isQNameStart(c) || c == '-' || (c >= '0' && c <= '9') || c == '.' || c == 0xb7 ||
		(c >= 0x300 && c <= 0x36f) || (c >= 0x203f && c <= 0x2040)
}

// getUTF8 is ly_getutf8: one character, size 0 on error (incl. control characters).
func getUTF8(s string) (rune, int) {
	if s == "" {
		return 0, 0
	}
	c := rune(s[0])
	switch {
	case c < 0x80:
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			return 0, 0
		}
		return c, 1
	case c&0xe0 == 0xc0:
		if len(s) < 2 || s[1]&0xc0 != 0x80 {
			return 0, 0
		}
		c = (c&0x1f)<<6 | rune(s[1]&0x3f)
		if c < 0x80 {
			return 0, 0
		}
		return c, 2
	case c&0xf0 == 0xe0:
		if len(s) < 3 || s[1]&0xc0 != 0x80 || s[2]&0xc0 != 0x80 {
			return 0, 0
		}
		c = (c&0xf)<<12 | rune(s[1]&0x3f)<<6 | rune(s[2]&0x3f)
		if c < 0x800 || (c >= 0xd800 && c <= 0xdfff) || c == 0xfffe || c == 0xffff {
			return 0, 0
		}
		return c, 3
	case c&0xf8 == 0xf0:
		if len(s) < 4 || s[1]&0xc0 != 0x80 || s[2]&0xc0 != 0x80 || s[3]&0xc0 != 0x80 {
			return 0, 0
		}
		c = (c&0x7)<<18 | rune(s[1]&0x3f)<<12 | rune(s[2]&0x3f)<<6 | rune(s[3]&0x3f)
		if c < 0x10000 || c > 0x10ffff {
			return 0, 0
		}
		return c, 4
	}
	return 0, 0
}

// parseNCName is parse_ncname: length of the NCName at s, 0 if none, -n on an
// invalid character after n valid bytes.
func parseNCName(s string) int {
	c, size := getUTF8(s)
	if size == 0 || !isQNameStart(c) {
		return 0
	}
	n := 0
	for {
		n += size
		if n >= len(s) {
			return n
		}
		c, size = getUTF8(s[n:])
		if size == 0 {
			return -n
		}
		if !isQNameChar(c) {
			return n
		}
	}
}

// MaxTokens caps the size of an expression (and so of its AST); libyang's only
// limit is the UINT32_MAX expression length (U-0003).
const MaxTokens = 1 << 22

// at returns s[i] or 0 past the end (C string semantics).
func at(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0
}

// invalidChar is LY_VCODE_XP_INEXPR; a NUL character truncates the C message.
func invalidChar(src string, i int) *Error {
	c := at(src, i)
	if c == 0 {
		return xpErr("Invalid character '")
	}
	return xpErr("Invalid character '%s'[%d] of expression '%s'.", string([]byte{c}), i+1, src)
}

// lex is the tokenizer of lyxp_expr_parse. It returns the source truncated at
// the first NUL (the C string ends there).
func lex(src string) ([]token, string, error) {
	if i := strings.IndexByte(src, 0); i >= 0 {
		src = src[:i]
	}
	if src == "" {
		return nil, src, xpErr("Unexpected XPath expression end.")
	}
	var toks []token
	last := func() tokKind {
		if len(toks) == 0 {
			return tNone
		}
		return toks[len(toks)-1].k
	}
	lastText := func() string { t := toks[len(toks)-1]; return src[t.pos : t.pos+t.len] }
	prevNType, prevFunc := false, false
	p := 0
	for p < len(src) && isXMLWS(src[p]) {
		p++
	}
	for {
		var k tokKind
		n := 1
		c := at(src, p)
		switch {
		case c == '(':
			k = tPar1
			if prevNType && last() == tNameTest && (lastText() == "node" || lastText() == "text" || lastText() == "comment") {
				toks[len(toks)-1].k = tNodeType
				prevNType, prevFunc = false, false
			} else if prevFunc && last() == tNameTest {
				toks[len(toks)-1].k = tFuncName
				prevNType, prevFunc = false, false
			}
		case c == ')':
			k = tPar2
		case c == '[':
			k = tBrack1
		case c == ']':
			k = tBrack2
		case strings.HasPrefix(src[p:], ".."):
			k, n = tDDot, 2
		case c == '.' && !isDigit(at(src, p+1)):
			k = tDot
		case c == '@':
			k = tAt
		case c == ',':
			k = tComma
		case c == '\'' || c == '"':
			end := strings.IndexByte(src[p+1:], c)
			if end < 0 {
				rest := src[p:]
				if len(rest) > 15 {
					rest = rest[:15]
				}
				return nil, src, xpErr("Unterminated string delimited with %s (%s).", string([]byte{c}), rest)
			}
			k, n = tLiteral, end+2
		case c == '.' || isDigit(c):
			n = 0
			for isDigit(at(src, p+n)) {
				n++
			}
			if at(src, p+n) == '.' {
				n++
				for isDigit(at(src, p+n)) {
					n++
				}
			}
			k = tNumber
		case c == '$':
			p++
			l := parseNCName(src[p:])
			if l < 1 {
				return nil, src, invalidChar(src, p-l)
			}
			if at(src, p+l) == ':' {
				return nil, src, xpErr("Variable with prefix is not supported.")
			}
			k, n = tVarRef, l
		case c == '/':
			k = tOperPath
			if at(src, p+1) == '/' {
				k, n = tOperRPath, 2
			}
		case strings.HasPrefix(src[p:], "!="):
			k, n = tOperNEqual, 2
		case strings.HasPrefix(src[p:], "<=") || strings.HasPrefix(src[p:], ">="):
			k, n = tOperComp, 2
		case c == '|':
			k = tOperUni
		case c == '+' || c == '-':
			k = tOperMath
		case c == '=':
			k = tOperEqual
		case c == '<' || c == '>':
			k = tOperComp
		case len(toks) > 0 && operatorFollows(last()):
			// Operator '*', 'or', 'and', 'mod', or 'div' (libyang: prefix match, "order" lexes as "or" "der")
			switch rest := src[p:]; {
			case c == '*':
				k = tOperMath
			case strings.HasPrefix(rest, "or"):
				k, n = tOperLog, 2
			case strings.HasPrefix(rest, "and"):
				k, n = tOperLog, 3
			case strings.HasPrefix(rest, "mod") || strings.HasPrefix(rest, "div"):
				k, n = tOperMath, 3
			case prevNType || prevFunc:
				return nil, src, xpErr("Invalid character 0x%x ('%s'), perhaps \"%s\" is supposed to be a function call.",
					c, string([]byte{c}), lastText())
			default:
				return nil, src, invalidChar(src, p)
			}
		default:
			// (AxisName '::')? ((NCName ':')? '*' | QName) or NodeType/FunctionName
			l := 1
			if c != '*' {
				if l = parseNCName(src[p:]); l < 1 {
					return nil, src, invalidChar(src, p-l)
				}
			}
			hasAxis := false
			if strings.HasPrefix(src[p+l:], "::") {
				if !axisNames[src[p:p+l]] {
					return nil, src, invalidChar(src, p)
				}
				toks = append(toks, token{tAxisName, p, l}, token{tDColon, p + l, 2})
				p += l + 2
				l = 1
				if at(src, p) != '*' {
					if l = parseNCName(src[p:]); l < 1 {
						return nil, src, invalidChar(src, p-l)
					}
				}
				hasAxis = true
			}
			if at(src, p+l) == ':' {
				l++
				if at(src, p+l) == '*' {
					l++
				} else {
					m := parseNCName(src[p+l:])
					if m < 1 {
						// libyang reports the position relative to the token start
						return nil, src, invalidChar(src, p-m)
					}
					l += m
				}
				prevNType, prevFunc = false, false
			} else {
				prevNType = at(src, p) != '*'
				prevFunc = prevNType && !hasAxis
			}
			k, n = tNameTest, l
		}
		toks = append(toks, token{k, p, n})
		if len(toks) > MaxTokens {
			return nil, src, xpErr("XPath expression has more than %d tokens.", MaxTokens)
		}
		p += n
		for p < len(src) && isXMLWS(src[p]) {
			p++
		}
		if p >= len(src) {
			return toks, src, nil
		}
	}
}

// operatorFollows: after these tokens '*' and the operator names are operators.
func operatorFollows(k tokKind) bool {
	switch k {
	case tAt, tPar1, tBrack1, tComma, tOperLog, tOperEqual, tOperNEqual, tOperComp, tOperMath, tOperUni,
		tOperPath, tOperRPath:
		return false
	}
	return true
}
