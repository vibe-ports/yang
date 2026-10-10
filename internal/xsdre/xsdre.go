// SPDX-License-Identifier: BSD-3-Clause

// Package xsdre compiles XML Schema (XSD Part 2, Appendix F) regular
// expressions, as used by the YANG "pattern" statement (RFC 7950 §9.4.5),
// into Go RE2 regexps with identical match semantics.
//
// The pattern is parsed into an AST whose character classes are code point
// range sets (union / intersection / subtraction are exact set algebra), and
// the AST is emitted as an RE2 expression anchored to the whole string.
// Nothing is approximated: a pattern Go's engine cannot represent is rejected
// with ErrUnsupported.
package xsdre

//go:generate go run gen_blocks.go

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrSyntax means the pattern is not a valid XSD regular expression.
	ErrSyntax = errors.New("invalid XSD regular expression")
	// ErrUnsupported means the pattern is valid XSD but exceeds a compiler resource budget or
	// what Go's RE2 engine accepts (repeat count > 1000, nesting depth, program size).
	ErrUnsupported = errors.New("XSD regular expression not supported")
)

// Error describes a rejected pattern; errors.Is(err, ErrSyntax|ErrUnsupported).
type Error struct {
	Kind    error // ErrSyntax or ErrUnsupported
	Pattern string
	Offset  int // byte offset in Pattern
	Reason  string
	// Compat marks a CompileCompat syntax error: Reason is then libyang's detail text,
	// `"<rest of the PCRE2 pattern>": <message>`, and Error() returns it alone.
	Compat bool
}

func (e *Error) Error() string {
	if e.Compat {
		return e.Reason
	}
	return fmt.Sprintf("%v %q at offset %d: %s", e.Kind, e.Pattern, e.Offset, e.Reason)
}
func (e *Error) Unwrap() error { return e.Kind }

// maxRepeat is Go's regexp/syntax repeat limit; maxDepth bounds parser recursion.
const (
	maxRepeat         = 1000
	maxDepth          = 1000
	maxClassExpansion = 1 << 20 // ranges materialized while parsing character classes (U-0001)
)

// Pattern is a compiled XSD regular expression.
type Pattern struct {
	src string
	re  *regexp.Regexp
}

// Compile parses an XSD regular expression.
func Compile(pattern string) (*Pattern, error) {
	if !utf8.ValidString(pattern) {
		return nil, &Error{ErrSyntax, pattern, 0, "pattern is not valid UTF-8", false}
	}
	p := &parser{src: pattern}
	n, err := p.regExp(0)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.src) { // only an unmatched ')' stops the top level early
		return nil, p.errf(ErrSyntax, "unmatched ')'")
	}
	return build(pattern, n)
}

// build emits n as an RE2 program anchored to the whole string.
func build(pattern string, n *node) (*Pattern, error) {
	var b strings.Builder
	b.WriteString(`\A(?:`)
	emit(&b, n)
	b.WriteString(`)\z`)
	re, err := regexp.Compile(b.String())
	if err != nil {
		var se *syntax.Error
		if errors.As(err, &se) && (se.Code == syntax.ErrInvalidRepeatSize || se.Code == syntax.ErrNestingDepth || se.Code == syntax.ErrLarge) {
			return nil, &Error{ErrUnsupported, pattern, 0, "Go RE2 limit: " + string(se.Code), false}
		}
		return nil, fmt.Errorf("xsdre: internal error translating %q: %w", pattern, err)
	}
	return &Pattern{src: pattern, re: re}, nil
}

// Match reports whether the whole of s matches. Invalid UTF-8 never matches.
func (p *Pattern) Match(s string) bool { return utf8.ValidString(s) && p.re.MatchString(s) }

// String returns the XSD source.
func (p *Pattern) String() string { return p.src }

// ---- AST ----

type op int

const (
	opSet    op = iota // one character from set
	opConcat           // subs in sequence (empty = empty string)
	opAlt              // one of subs
	opRepeat           // subs[0]{min,max}, max<0 = unbounded
	opAssert           // CompileCompat only: start (min 0) or end (min 1) of the string
)

type node struct {
	op       op
	set      charSet
	subs     []*node
	min, max int
}

// ---- parser (XSD Part 2 §F, productions [1]-[37]) ----

type parser struct {
	src            string
	pos            int
	classExpansion int
}

func (p *parser) errf(kind error, format string, a ...any) error {
	return &Error{kind, p.src, p.pos, fmt.Sprintf(format, a...), false}
}

func (p *parser) addClassExpansion(n int) error {
	if n > maxClassExpansion-p.classExpansion {
		return p.errf(ErrUnsupported, "character-class expansion exceeds %d ranges (U-0001)", maxClassExpansion)
	}
	p.classExpansion += n
	return nil
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

func (p *parser) peek() rune {
	r, _ := utf8.DecodeRuneInString(p.src[p.pos:])
	return r
}

func (p *parser) peekAt(k int) rune { // k-th rune ahead, -1 at end
	s := p.src[p.pos:]
	for ; k > 0 && s != ""; k-- {
		_, n := utf8.DecodeRuneInString(s)
		s = s[n:]
	}
	if s == "" {
		return -1
	}
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

func (p *parser) next() rune {
	r, n := utf8.DecodeRuneInString(p.src[p.pos:])
	p.pos += n
	return r
}

// regExp ::= branch ( '|' branch )*
func (p *parser) regExp(depth int) (*node, error) {
	if depth > maxDepth {
		return nil, p.errf(ErrUnsupported, "group nesting deeper than %d", maxDepth)
	}
	alt := &node{op: opAlt}
	for {
		br, err := p.branch(depth)
		if err != nil {
			return nil, err
		}
		alt.subs = append(alt.subs, br)
		if p.eof() || p.peek() != '|' {
			break
		}
		p.next()
	}
	if len(alt.subs) == 1 {
		return alt.subs[0], nil
	}
	return alt, nil
}

// branch ::= piece*
func (p *parser) branch(depth int) (*node, error) {
	cat := &node{op: opConcat}
	for !p.eof() && p.peek() != '|' && p.peek() != ')' {
		pc, err := p.piece(depth)
		if err != nil {
			return nil, err
		}
		cat.subs = append(cat.subs, pc)
	}
	return cat, nil
}

// piece ::= atom quantifier?
func (p *parser) piece(depth int) (*node, error) {
	a, err := p.atom(depth)
	if err != nil {
		return nil, err
	}
	if p.eof() {
		return a, nil
	}
	lo, hi := 0, 0
	switch p.peek() {
	case '?':
		hi = 1
	case '*':
		hi = -1
	case '+':
		lo, hi = 1, -1
	case '{':
		p.next()
		if lo, hi, err = p.quantity(); err != nil {
			return nil, err
		}
		return &node{op: opRepeat, subs: []*node{a}, min: lo, max: hi}, nil
	default:
		return a, nil
	}
	p.next()
	return &node{op: opRepeat, subs: []*node{a}, min: lo, max: hi}, nil
}

// quantity ::= quantRange | quantMin | QuantExact, after '{', consumes '}'.
func (p *parser) quantity() (lo, hi int, err error) {
	start := p.pos
	if lo, err = p.number(); err != nil {
		return
	}
	hi = lo
	if !p.eof() && p.peek() == ',' {
		p.next()
		hi = -1
		if !p.eof() && p.peek() != '}' {
			if hi, err = p.number(); err != nil {
				return
			}
		}
	}
	if p.eof() || p.peek() != '}' {
		return 0, 0, p.errf(ErrSyntax, "quantifier: expected '}'")
	}
	p.next()
	if hi >= 0 && lo > hi {
		return 0, 0, &Error{ErrSyntax, p.src, start, "quantifier {n,m} with n > m", false}
	}
	if lo > maxRepeat || hi > maxRepeat {
		return 0, 0, &Error{ErrUnsupported, p.src, start, fmt.Sprintf("repeat count above Go RE2 limit %d", maxRepeat), false}
	}
	return
}

// QuantExact ::= [0-9]+ (saturates instead of overflowing).
func (p *parser) number() (int, error) {
	n, digits := 0, 0
	for !p.eof() && p.peek() >= '0' && p.peek() <= '9' {
		n = min(n*10+int(p.next()-'0'), 1<<30)
		digits++
	}
	if digits == 0 {
		return 0, p.errf(ErrSyntax, "quantifier: expected digit")
	}
	return n, nil
}

// atom ::= NormalChar | charClass | '(' regExp ')'
func (p *parser) atom(depth int) (*node, error) {
	switch c := p.peek(); c {
	case '(':
		p.next()
		n, err := p.regExp(depth + 1)
		if err != nil {
			return nil, err
		}
		if p.eof() || p.peek() != ')' {
			return nil, p.errf(ErrSyntax, "missing ')'")
		}
		p.next()
		return n, nil
	case '[':
		p.next()
		s, err := p.charGroup(depth + 1)
		if err != nil {
			return nil, err
		}
		return &node{op: opSet, set: s}, nil
	case '.':
		p.next() // XSD '.' = [^\n\r]
		return &node{op: opSet, set: complement(normalize([]rng{{'\n', '\n'}, {'\r', '\r'}}))}, nil
	case '\\':
		s, _, err := p.escape()
		if err != nil {
			return nil, err
		}
		if err := p.addClassExpansion(len(s)); err != nil {
			return nil, err
		}
		return &node{op: opSet, set: s}, nil
	case '?', '*', '+', '{', '}', ']', ')', '|':
		// '{' and '}' are metacharacters (XSD §F.1, NormalChar in XSD 1.1).
		return nil, p.errf(ErrSyntax, "unexpected %q (escape it as a literal)", c)
	default:
		p.next()
		return &node{op: opSet, set: single(c)}, nil
	}
}

// escape parses '\' ... ; isChar is true for a SingleCharEsc (usable as a range end).
func (p *parser) escape() (s charSet, isChar bool, err error) {
	start := p.pos
	p.next() // '\'
	if p.eof() {
		return nil, false, p.errf(ErrSyntax, "trailing '\\'")
	}
	c := p.next()
	switch c {
	case 'n':
		return single('\n'), true, nil
	case 'r':
		return single('\r'), true, nil
	case 't':
		return single('\t'), true, nil
	case '\\', '|', '.', '?', '*', '+', '(', ')', '{', '}', '-', '[', ']', '^':
		return single(c), true, nil
	case 'p', 'P':
		if p.eof() || p.next() != '{' {
			return nil, false, &Error{ErrSyntax, p.src, start, "expected '{' after \\p", false}
		}
		end := strings.IndexByte(p.src[p.pos:], '}')
		if end < 0 {
			return nil, false, &Error{ErrSyntax, p.src, start, "unterminated \\p{", false}
		}
		name := p.src[p.pos : p.pos+end]
		p.pos += end + 1
		var ok bool
		if b, isBlock := strings.CutPrefix(name, "Is"); isBlock {
			s, ok = block(b)
		} else {
			s, ok = category(name)
		}
		if !ok {
			return nil, false, &Error{ErrSyntax, p.src, start, fmt.Sprintf("unknown category or block %q", name), false}
		}
		if c == 'P' {
			s = complement(s)
		}
		return s, false, nil
	}
	if s, ok := multiChar(c); ok {
		return s, false, nil
	}
	return nil, false, &Error{ErrSyntax, p.src, start, fmt.Sprintf("unknown escape \\%c", c), false}
}

// charGroup parses after '[' through the closing ']':
// charGroup ::= posCharGroup | negCharGroup | charClassSub
func (p *parser) charGroup(depth int) (charSet, error) {
	if depth > maxDepth {
		return nil, p.errf(ErrUnsupported, "class nesting deeper than %d", maxDepth)
	}
	neg := !p.eof() && p.peek() == '^'
	if neg {
		p.next()
	}
	var raw []rng
	normalized := 0
	maybeNormalize := func() {
		if len(raw) > 2*normalized+4096 {
			raw = normalize(raw)
			normalized = len(raw)
		}
	}
	addRange := func(lo, hi rune) error {
		if err := p.addClassExpansion(1); err != nil {
			return err
		}
		raw = append(raw, rng{lo, hi})
		maybeNormalize()
		return nil
	}
	addSet := func(s charSet) error {
		if err := p.addClassExpansion(len(s)); err != nil {
			return err
		}
		raw = append(raw, s...)
		maybeNormalize()
		return nil
	}
	first := true
	for {
		if p.eof() {
			return nil, p.errf(ErrSyntax, "missing ']'")
		}
		c := p.peek()
		switch {
		case c == ']':
			if first {
				return nil, p.errf(ErrSyntax, "empty character group")
			}
			p.next()
			set := normalize(raw)
			if neg {
				set = complement(set)
			}
			return set, nil
		case c == '-' && !first && p.peekAt(1) == '[': // charClassSub
			p.next()
			p.next()
			sub, err := p.charGroup(depth + 1)
			if err != nil {
				return nil, err
			}
			if p.eof() || p.peek() != ']' {
				return nil, p.errf(ErrSyntax, "subtraction must be last in a character class")
			}
			p.next()
			set := normalize(raw)
			if neg {
				set = complement(set)
			}
			return subtract(set, sub), nil
		case c == '-' && !first && p.peekAt(1) != ']':
			return nil, p.errf(ErrSyntax, "'-' is literal only at the start or end of a group; escape it as \\-")
		case c == '[':
			return nil, p.errf(ErrSyntax, "unescaped '[' in character class")
		}
		first = false
		var lo rune
		if c == '\\' {
			s, isChar, err := p.escape()
			if err != nil {
				return nil, err
			}
			if !isChar {
				if err := addSet(s); err != nil {
					return nil, err
				}
				continue
			}
			lo = s[0].lo
		} else {
			lo = p.next()
		}
		// seRange ::= charOrEsc '-' charOrEsc (not '-[' and not '-]')
		if c == '-' || p.eof() || p.peek() != '-' || p.peekAt(1) == ']' || p.peekAt(1) == '[' {
			if err := addRange(lo, lo); err != nil {
				return nil, err
			}
			continue
		}
		p.next() // '-'
		var hi rune
		switch e := p.peek(); e {
		case '\\':
			s, isChar, err := p.escape()
			if err != nil {
				return nil, err
			}
			if !isChar {
				return nil, p.errf(ErrSyntax, "multi-character escape cannot end a range")
			}
			hi = s[0].lo
		case '-', '[', -1:
			return nil, p.errf(ErrSyntax, "invalid range end %q", e)
		default:
			hi = p.next()
		}
		if hi < lo {
			return nil, p.errf(ErrSyntax, "range out of order")
		}
		if err := addRange(lo, hi); err != nil {
			return nil, err
		}
	}
}

// ---- emitter ----

func emit(b *strings.Builder, n *node) {
	switch n.op {
	case opSet:
		emitSet(b, n.set)
	case opAssert:
		b.WriteString([]string{`\A`, `\z`}[n.min])
	case opConcat:
		for _, s := range n.subs {
			if s.op == opAlt {
				b.WriteString("(?:")
				emit(b, s)
				b.WriteString(")")
			} else {
				emit(b, s)
			}
		}
	case opAlt:
		for i, s := range n.subs {
			if i > 0 {
				b.WriteByte('|')
			}
			emit(b, s)
		}
	case opRepeat:
		b.WriteString("(?:")
		emit(b, n.subs[0])
		b.WriteString(")")
		switch {
		case n.min == 0 && n.max == 1:
			b.WriteByte('?')
		case n.min == 0 && n.max < 0:
			b.WriteByte('*')
		case n.min == 1 && n.max < 0:
			b.WriteByte('+')
		case n.max < 0:
			fmt.Fprintf(b, "{%d,}", n.min)
		default:
			fmt.Fprintf(b, "{%d,%d}", n.min, n.max)
		}
	}
}

func emitSet(b *strings.Builder, s charSet) {
	// Surrogates never occur in valid UTF-8, so they are dropped from classes.
	s = subtract(s, span(0xD800, 0xDFFF))
	if len(s) == 0 {
		b.WriteString(`[^\x00-\x{10FFFF}]`)
		return
	}
	if len(s) == 1 && s[0].lo == s[0].hi && s[0].lo > ' ' && s[0].lo < unicode.MaxASCII {
		b.WriteString(regexp.QuoteMeta(string(s[0].lo)))
		return
	}
	b.WriteByte('[')
	for _, r := range s {
		b.WriteString(`\x{` + strconv.FormatInt(int64(r.lo), 16) + `}`)
		if r.hi != r.lo {
			b.WriteString(`-\x{` + strconv.FormatInt(int64(r.hi), 16) + `}`)
		}
	}
	b.WriteByte(']')
}
