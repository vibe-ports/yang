// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/json.c (BSD-3-Clause, © CESNET).

// Package lyjson is a port of libyang's generic JSON lexer (src/json.c). It is deliberately not
// encoding/json: it reproduces libyang's diagnostics (message text, validation-error code, input
// line), its canonical number lexemes (exponent rewriting, "-0", stored as text), its status
// machine and its resource limits, none of which encoding/json exposes.
//
// Input is treated like libyang's NUL-terminated memory input: the first NUL byte ends the input.
//
// Budgets: the status stack is bounded by [MaxDepth] (libyang's own limit, exceeding it fails with
// an error wrapping [ErrNesting]); numbers are bounded by libyang's 22-byte limit; strings and
// names are bounded by the input, whose size the caller limits (design 07 U-0040).
package lyjson

import (
	"bytes"
	"errors"
	"strconv"
)

// Token is libyang's LYJSON_PARSER_STATUS: what the lexer is positioned on.
type Token uint8

// The values are libyang's, in order. TokenError is never a lexer position (libyang's error
// return); it is kept so the numbering matches.
const (
	TokenError Token = iota
	TokenObject
	TokenObjectNext
	TokenObjectClosed
	TokenArray
	TokenArrayNext
	TokenArrayClosed
	TokenObjectName
	TokenNumber
	TokenString
	TokenTrue
	TokenFalse
	TokenNull
	TokenEnd
)

// MaxDepth is libyang's status-stack limit, LY_MAX_BLOCK_DEPTH*10 (JS:905).
const MaxDepth = 5000

// numberMaxLen is LY_NUMBER_MAXLEN: the longest number lexeme including its terminating NUL.
const numberMaxLen = 22

// ErrNesting is wrapped by the error returned when the status stack exceeds [MaxDepth].
var ErrNesting = errors.New("lyjson: nesting limit exceeded")

// VECode is the validation-error code libyang attaches to a diagnostic (LYVE_*).
type VECode uint8

// The only codes json.c uses. LYVE_SYNTAX_JSON is attached by the data parser, not by this file.
const (
	VENone      VECode = iota // LOGERR without a validation code (nesting limit)
	VESyntax                  // LYVE_SYNTAX
	VESemantics               // LYVE_SEMANTICS
)

// Diag is one logged libyang error: code, message and input line (1-based; 0 for errors libyang
// logs without an input location, i.e. VENone).
type Diag struct {
	Code VECode
	Msg  string
	Line uint64
}

// Error carries every diagnostic libyang logs for one failed call (the unterminated string logs
// two) and, for the nesting limit, the wrapped sentinel.
type Error struct {
	Diags []Diag
	Err   error // ErrNesting or nil
}

func (e *Error) Error() string { return e.Diags[len(e.Diags)-1].Msg }

func (e *Error) Unwrap() error { return e.Err }

// String is lyjson_token2str.
func (t Token) String() string {
	names := [...]string{"error", "object", "object next", "object closed", "array", "array next",
		"array closed", "object name", "number", "string", "true", "false", "null", "end of input"}
	if int(t) >= len(names) {
		return ""
	}
	return names[t]
}

// Lexer is struct lyjson_ctx.
type Lexer struct {
	in     []byte // up to the first NUL
	pos    int
	line   uint64
	status []Token
	value  string
	backup struct {
		status Token
		count  int
		value  string
		pos    int
		set    bool
	}
}

// Status is lyjson_ctx_status.
func (l *Lexer) Status() Token {
	if len(l.status) == 0 {
		return TokenEnd
	}
	return l.status[len(l.status)-1]
}

// Depth is lyjson_ctx_depth.
func (l *Lexer) Depth() int { return len(l.status) }

// Value is the text of the current String, Number, ObjectName, True, False or Null token (a null
// is ""); "" otherwise.
func (l *Lexer) Value() string { return l.value }

// Line is the current input line (1-based), as libyang reports it in diagnostics.
func (l *Lexer) Line() uint64 { return l.line }

// Offset is the number of input bytes consumed so far.
func (l *Lexer) Offset() int { return l.pos }

// cur is *in->current: the NUL terminator reads as 0.
func (l *Lexer) cur() byte { return l.at(l.pos) }

func (l *Lexer) at(i int) byte {
	if i < len(l.in) {
		return l.in[i]
	}
	return 0
}

func (l *Lexer) fail(code VECode, msg string) error {
	return &Error{Diags: []Diag{{code, msg, l.line}}}
}

func (l *Lexer) errEOF() error { return l.fail(VESyntax, "Unexpected end-of-input.") }

// errExpected is LY_VCODE_INSTREXP at the current position.
func (l *Lexer) errExpected(what string) error {
	n := 0 // LY_VCODE_INSTREXP_len
	for n < 20 && l.pos+n < len(l.in) {
		n++
	}
	return l.fail(VESyntax, `Invalid character sequence "`+string(l.in[l.pos:l.pos+n])+`", expected `+what+`.`)
}

// New is lyjson_ctx_new: it positions the lexer on the first token.
func New(in []byte) (*Lexer, error) {
	if i := bytes.IndexByte(in, 0); i >= 0 {
		in = in[:i]
	}
	l := &Lexer{in: in, line: 1}
	l.skipWS()
	if l.cur() == 0 {
		return nil, l.fail(VESyntax, "Empty JSON file.")
	}
	if _, err := l.Next(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Lexer) skipWS() { // lyjson_skip_ws
	for {
		switch l.cur() {
		case '\n':
			l.line++
		case 0x20, 0x9, 0xd:
		default:
			return
		}
		l.pos++
	}
}

func (l *Lexer) push(t Token) { l.status = append(l.status, t) }

func (l *Lexer) pop() { // LYJSON_STATUS_POP; the guard only matters after an earlier error
	if len(l.status) > 0 {
		l.status = l.status[:len(l.status)-1]
	}
}

// Next is lyjson_ctx_next: move to the next token and return it.
func (l *Lexer) Next() (Token, error) {
	var err error
	cur := l.Status()
	switch cur {
	case TokenObject:
		err = l.nextObjectName()
	case TokenArray:
		err = l.nextValue(true)
	case TokenObjectNext:
		l.pop()
		err = l.nextObjectName()
	case TokenArrayNext:
		l.pop()
		err = l.nextValue(false)
	case TokenObjectName:
		l.value = ""
		l.pop()
		err = l.nextValue(false)
	case TokenObjectClosed, TokenArrayClosed, TokenNumber, TokenString, TokenTrue, TokenFalse, TokenNull:
		if cur == TokenObjectClosed || cur == TokenArrayClosed {
			l.pop()
		}
		l.value = ""
		l.pop()
		switch l.Status() {
		case TokenObject:
			err = l.nextObjectItem()
		case TokenArray:
			err = l.nextArrayItem()
		default: // TokenEnd: nothing more is lexed (trailing data is the caller's business)
			return TokenEnd, nil
		}
	case TokenEnd:
		err = l.nextValue(false)
	default:
		return TokenError, &Error{Diags: []Diag{{VENone, "Internal error (json.c:1043).", 0}}}
	}
	if err != nil {
		return TokenError, err
	}
	l.skipWS()
	return l.Status(), nil
}

func (l *Lexer) nextObjectName() error { // lyjson_next_object_name
	switch l.cur() {
	case 0:
		return l.errEOF()
	case '"':
		l.pos++
		if err := l.str(); err != nil {
			return err
		}
		l.skipWS()
		if l.cur() != ':' {
			return l.errExpected("a JSON value name-separator ':'")
		}
		l.pos++
		l.push(TokenObjectName)
	case '}':
		l.pos++
		l.push(TokenObjectClosed)
	default:
		return l.errExpected("a JSON object name")
	}
	return nil
}

func (l *Lexer) literal(word string, t Token, val string) bool {
	if string(l.in[l.pos:min(l.pos+len(word), len(l.in))]) != word {
		return false
	}
	l.value = val
	l.pos += len(word)
	l.push(t)
	return true
}

func (l *Lexer) nextValue(arrayEnd bool) error { // lyjson_next_value
	ok := true
	switch c := l.cur(); {
	case c == 0:
		return l.errEOF()
	case c == '"':
		l.pos++
		if err := l.str(); err != nil {
			return err
		}
		l.push(TokenString)
	case c == '-' || (c >= '0' && c <= '9'):
		if err := l.number(); err != nil {
			return err
		}
		l.push(TokenNumber)
	case c == '{':
		l.pos++
		l.push(TokenObject)
	case c == '[':
		l.pos++
		l.push(TokenArray)
	case c == 't':
		ok = l.literal("true", TokenTrue, "true")
	case c == 'f':
		ok = l.literal("false", TokenFalse, "false")
	case c == 'n':
		ok = l.literal("null", TokenNull, "")
	case c == ']' && arrayEnd:
		l.pos++
		l.push(TokenArrayClosed)
	default:
		ok = false
	}
	if !ok {
		return l.errExpected("a JSON value")
	}
	if len(l.status) > MaxDepth {
		msg := "Maximum number " + strconv.Itoa(MaxDepth) + " of nestings has been exceeded."
		return &Error{Diags: []Diag{{VENone, msg, 0}}, Err: ErrNesting}
	}
	return nil
}

func (l *Lexer) nextObjectItem() error { // lyjson_next_object_item
	switch l.cur() {
	case 0:
		return l.errEOF()
	case '}':
		l.pos++
		l.push(TokenObjectClosed)
	case ',':
		l.pos++
		l.push(TokenObjectNext)
	default:
		return l.errExpected("a JSON object-end or next item")
	}
	return nil
}

func (l *Lexer) nextArrayItem() error { // lyjson_next_array_item
	switch l.cur() {
	case 0:
		return l.errEOF()
	case ']':
		l.pos++
		l.push(TokenArrayClosed)
	case ',':
		l.pos++
		l.push(TokenArrayNext)
	default:
		return l.errExpected("a JSON array-end or next item")
	}
	return nil
}

// Backup is lyjson_ctx_backup.
func (l *Lexer) Backup() {
	l.backup.status, l.backup.count, l.backup.value, l.backup.pos, l.backup.set =
		l.Status(), len(l.status), l.value, l.pos, true
}

// Restore is lyjson_ctx_restore. Without a prior Backup it does nothing (libyang: undefined).
// Like libyang it restores only the top status entry and the stack height, and not the line.
func (l *Lexer) Restore() {
	b := &l.backup
	if !b.set || b.count > cap(l.status) {
		return
	}
	l.status = l.status[:b.count]
	if b.count > 0 {
		l.status[b.count-1] = b.status
	}
	l.value, l.pos = b.value, b.pos
}

// isStrChar is is_jsonstrchar.
func isStrChar(c uint32) bool {
	return c == 0x20 || c == 0x21 || (c >= 0x23 && c <= 0x5b) || (c >= 0x5d && c <= 0x10ffff)
}

// getUTF8 is ly_getutf8 on s[0:]: ok is false for what libyang rejects (overlong 2/3-byte forms,
// surrogates, 0xfffe/0xffff, > 0x10ffff, control characters other than TAB, LF, CR). Like libyang
// it accepts 4-byte forms of 0x1000..0xffff and non-characters such as U+1FFFE.
func getUTF8(s []byte) (c uint32, n int, ok bool) {
	b := func(i int) uint32 {
		if i < len(s) {
			return uint32(s[i])
		}
		return 0
	}
	c = b(0)
	switch {
	case c&0x80 == 0:
		return c, 1, c >= 0x20 || c == 9 || c == 0xa || c == 0xd
	case c&0xe0 == 0xc0:
		n, c = 2, c&0x1f
	case c&0xf0 == 0xe0:
		n, c = 3, c&0x0f
	case c&0xf8 == 0xf0:
		n, c = 4, c&0x07
	default:
		return 0, 0, false
	}
	for i := 1; i < n; i++ {
		if b(i)&0xc0 != 0x80 {
			return 0, 0, false
		}
		c = c<<6 | b(i)&0x3f
	}
	switch n {
	case 2:
		ok = c >= 0x80
	case 3:
		ok = c >= 0x800 && (c <= 0xd7ff || c >= 0xe000) && c <= 0xfffd
	default:
		ok = c >= 0x1000 && c <= 0x10ffff
	}
	return c, n, ok
}

// putUTF8 is ly_pututf8: ok is false for values that are not YANG characters.
func putUTF8(v uint32) ([]byte, bool) {
	switch {
	case v < 0x80:
		return []byte{byte(v)}, v >= 0x20 || v == 9 || v == 0xa || v == 0xd
	case v < 0x800:
		return []byte{0xc0 | byte(v>>6&0x1f), 0x80 | byte(v&0x3f)}, true
	case v < 0xfffe:
		if v&0xf800 == 0xd800 || (v >= 0xfdd0 && v <= 0xfdef) {
			return nil, false
		}
		return []byte{0xe0 | byte(v>>12&0x0f), 0x80 | byte(v>>6&0x3f), 0x80 | byte(v&0x3f)}, true
	case v < 0x10fffe:
		if v&0xffe == 0xffe {
			return nil, false
		}
		return []byte{0xf0 | byte(v>>18&0x07), 0x80 | byte(v>>12&0x3f), 0x80 | byte(v>>6&0x3f), 0x80 | byte(v&0x3f)}, true
	}
	return nil, false
}

// hexDigit mirrors the C chain in lyjson_string, including its acceptance of non-hex letters and
// of punctuation. C's char is signed: bytes >= 0x80 are negative, and the value wraps in uint32.
func hexDigit(b byte) uint32 {
	x := uint32(b)
	switch {
	case b >= '0' && b <= '9':
		return x - '0'
	case b > 'F' && b < 0x80:
		return 10 + x - 'a'
	}
	if b >= 0x80 {
		x -= 256
	}
	return 10 + x - 'A'
}

// str is lyjson_string: the string after its opening quote; the result is stored in l.value.
func (l *Lexer) str() error {
	startLine := l.line
	in := l.in
	var buf []byte
	escaped := false
	p := l.pos // read position; the string starts at l.pos until success
	run := p   // start of the not yet copied run (differs from l.pos after an escape)
	for p < len(in) {
		c := in[p]
		switch c {
		case '\\':
			esc := p // c in the C source
			escaped = true
			var v uint32
			var adv int
			switch e := l.at(p + 1); e {
			case '"':
				v, adv = 0x22, 2
			case '\\':
				v, adv = 0x5c, 2
			case '/':
				v, adv = 0x2f, 2
			case 'b':
				v, adv = 0x08, 2
			case 'f':
				v, adv = 0x0c, 2
			case 'n':
				v, adv = 0x0a, 2
			case 'r':
				v, adv = 0x0d, 2
			case 't':
				v, adv = 0x09, 2
			case 'u':
				for i := 0; i < 4; i++ {
					if p+2+i >= len(in) {
						return l.fail(VESyntax, `Invalid basic multilingual plane character "`+string(in[esc:])+`".`)
					}
					v = 16*v + hexDigit(in[p+2+i])
				}
				adv = 6
			default:
				m := "Invalid character escape sequence \\"
				if e != 0 { // %c of NUL ends the C message
					m += string([]byte{e}) + "."
				}
				return l.fail(VESyntax, m)
			}
			b, ok := putUTF8(v)
			if !ok {
				return l.fail(VESyntax, `Invalid character reference "`+string(in[esc:p+adv])+`" (0x`+hex8(v)+`).`)
			}
			buf = append(append(buf, in[run:p]...), b...)
			p += adv
			run = p
		case '"':
			l.pos = p + 1
			if escaped {
				l.value = string(append(buf, in[run:p]...))
			} else {
				l.value = string(in[run:p])
			}
			return nil
		default:
			r, n, ok := getUTF8(in[p:])
			if !ok {
				return l.fail(VESyntax, "Invalid character 0x"+strconv.FormatUint(uint64(c), 16)+".")
			}
			if !isStrChar(r) {
				return l.fail(VESyntax, `Invalid character in JSON string "`+string(in[l.pos:p+n])+`" (0x`+hex8(r)+`).`)
			}
			p += n
		}
	}
	// EOF reached before the closing quote: two diagnostics, the second at the string's line
	return &Error{Diags: []Diag{
		{VESyntax, "Unexpected end-of-input.", l.line},
		{VESyntax, "Missing quotation-mark at the end of a JSON string.", startLine},
	}}
}

func hex8(v uint32) string {
	s := strconv.FormatUint(uint64(v), 16)
	return "00000000"[len(s):] + s
}
