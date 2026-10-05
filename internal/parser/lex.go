// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/parser_yang.c, src/tree_schema_common.c and
// src/ly_common.c (BSD-3-Clause, © CESNET).

// Package parser reads YANG 1.0/1.1 module text into a checked statement
// tree (Parse) and builds a typed parsed module from it (Build).
//
// Parse is a port of libyang's tokenizer (get_keyword, get_argument,
// read_qstring, skip_comment) with the checks of parser_yang.c's typed
// parse_* functions run while reading (allowed substatements, cardinality,
// argument values, mandatory substatements), so that errors, messages and
// lines are libyang's. Argument strings, escapes, indentation trimming of
// multi-line double-quoted strings and the accepted character set are
// byte-for-byte libyang's.
package parser

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/vibe-ports/yang/internal/ly"
)

// Pos is a position in the module text: 1-based line and byte column.
type Pos struct{ Line, Col int }

// Stmt is one YANG statement with its substatements in source order.
type Stmt struct {
	Keyword   string // YANG keyword, or the name part of an extension instance
	ExtPrefix string // prefix of an extension instance ("" for YANG keywords)
	Arg       string
	HasArg    bool
	Quote     byte // '\'' or '"' when the argument (its first part) was quoted
	Pos       Pos  // keyword
	End       Pos  // terminating ';' or '}'
	Subs      []*Stmt
}

// ErrBudget is wrapped by errors caused by an exceeded Budget limit.
var ErrBudget = errors.New("parser: resource budget exceeded")

// Error is a parse error with libyang's code, message and line (Pos.Line is
// libyang's line counter; 0 when libyang reports a schema path instead).
type Error struct {
	File string
	Pos  Pos
	Code ly.Code
	Msg  string
	err  error
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%d:%d: %s", e.Pos.Line, e.Pos.Col, e.Msg)
	if e.File != "" {
		s = e.File + ":" + s
	}
	return s
}

func (e *Error) Unwrap() error { return e.err }

// Budget bounds the resources one Parse may use. Zero fields take defaults.
type Budget struct {
	MaxBytes  int // input size, default 64 MiB
	MaxDepth  int // nested blocks, default 500 (libyang LY_MAX_BLOCK_DEPTH)
	MaxStmts  int // statements, default 1<<20
	MaxArgLen int // bytes of one argument, default 1<<24
}

type argKind uint8

// enum yang_arg; the order matters (arg <= argPrefIdent requires a value).
const (
	argIdent argKind = iota
	argPrefIdent
	argStr
	argMaybeStr
	argNone // input, output: no argument at all
)

const tabSpaces = 8 // Y_TAB_SPACES

// argOf returns the argument kind libyang's parse_* function passes to get_argument.
func argOf(kw, parent string, inExt bool) argKind {
	switch {
	case inExt:
		return argMaybeStr // parse_ext / parse_ext_substmt
	case kw == "input" || kw == "output":
		return argNone
	case kw == "default" && parent == "choice":
		return argPrefIdent
	}
	switch kw {
	case "module", "submodule", "belongs-to", "include", "import", "prefix", "anydata", "anyxml", "bit", "leaf",
		"leaf-list", "typedef", "rpc", "action", "notification", "grouping", "case", "choice", "container", "list",
		"argument", "extension", "feature", "identity":
		return argIdent
	case "type", "base", "uses":
		return argPrefIdent
	}
	return argStr
}

type lexer struct {
	src    []byte
	off    int
	indent int // ctx->indent: current column in YANG spaces, a tab counts Y_TAB_SPACES
	depth  int
	nstmt  int
	b      Budget
	file   string
	line   int // libyang's in->line: also counts newlines substituted for '\r'
	lineAt int // offset of the current line's first byte (for Pos.Col)
	chk    *checker
}

// Parse reads one YANG module or submodule (yang_parse_module / yang_parse_submodule
// without the context part). name is used only in error messages.
func Parse(name string, src []byte, b *Budget) (*Stmt, error) {
	l := &lexer{src: src, file: name, line: 1, chk: &checker{imports: map[string]string{}, extDefs: map[string]*Stmt{}}}
	if b != nil {
		l.b = *b
	}
	if l.b.MaxBytes <= 0 {
		l.b.MaxBytes = 64 << 20
	}
	if l.b.MaxDepth <= 0 {
		l.b.MaxDepth = 500
	}
	if l.b.MaxStmts <= 0 {
		l.b.MaxStmts = 1 << 20
	}
	if l.b.MaxArgLen <= 0 {
		l.b.MaxArgLen = 1 << 24
	}
	if len(src) > l.b.MaxBytes {
		e := l.errf(ly.Success, "The input of %d bytes exceeds the maximum of %d bytes.", len(src), l.b.MaxBytes)
		e.Pos, e.err = Pos{}, ErrBudget
		return nil, e
	}
	// libyang reads a NUL-terminated buffer: a NUL byte ends the input.
	if i := bytes.IndexByte(src, 0); i >= 0 {
		l.src = src[:i]
	}
	if err := l.skipRedundant(); err != nil {
		return nil, err
	}
	kw, ext, start, err := l.getKeyword()
	if err != nil {
		return nil, err
	}
	if ext || (kw != "module" && kw != "submodule") {
		return nil, l.errf(ly.Syntax, "Invalid keyword \"%s\", expected \"module\" or \"submodule\".", kwName(kw, ext))
	}
	s, err := l.stmt(kw, false, start, nil, false)
	if err != nil {
		return nil, err
	}
	if err := l.skipRedundant(); err != nil {
		return nil, err
	}
	if rest := l.src[l.off:]; len(rest) > 0 {
		more := ""
		if len(rest) > 15 {
			rest, more = rest[:15], "..."
		}
		return nil, l.errf(ly.Syntax, "Trailing garbage \"%s%s\" after %s, expected end-of-input.", rest, more, kw)
	}
	return s, l.chk.resolveExts(name)
}

func kwName(kw string, ext bool) string {
	if ext {
		return "extension instance"
	}
	return kw
}

func (l *lexer) c(i int) byte {
	if l.off+i < len(l.src) {
		return l.src[l.off+i]
	}
	return 0
}

// move is MOVE_INPUT.
func (l *lexer) move(n int) { l.off += n; l.indent += n }

// newline is LY_IN_NEW_LINE for the '\n' at the current offset.
func (l *lexer) newline() {
	l.line++
	l.lineAt = l.off + 1
}

// pos is the position at offset off of the current line.
func (l *lexer) pos(off int) Pos { return Pos{l.line, off - l.lineAt + 1} }

func (l *lexer) errf(code ly.Code, format string, a ...any) *Error {
	return &Error{File: l.file, Pos: l.pos(l.off), Code: code, Msg: fmt.Sprintf(format, a...)}
}

func (l *lexer) budget(what string) *Error {
	e := l.errf(ly.Success, "The maximum number of %s has been exceeded.", what)
	e.err = ErrBudget
	return e
}

// stmt reads the argument and substatements of a statement whose keyword was
// just read; pf is the parent's frame (nil for the module). With l.chk set,
// the grammar checks run where parser_yang.c makes them.
func (l *lexer) stmt(kw string, ext bool, start Pos, pf *frame, inExt bool) (*Stmt, error) {
	if l.nstmt++; l.nstmt > l.b.MaxStmts {
		return nil, l.budget("statements")
	}
	s := &Stmt{Keyword: kw, Pos: start}
	if ext {
		s.ExtPrefix, s.Keyword, _ = strings.Cut(kw, ":")
	}
	parent := ""
	if pf != nil {
		parent = pf.s.Keyword
	}
	// live: parsed by libyang's typed parser (YANG statements outside extension
	// instances, and extension instances under live statements)
	f := &frame{s: s, live: l.chk != nil && (ext || !inExt) && (pf == nil || pf.live), seen: map[string]bool{}}
	if ak := argOf(kw, parent, inExt || ext); ak != argNone {
		w, q, err := l.getArgument(ak)
		if err != nil {
			return nil, err
		}
		if len(w.buf) > l.b.MaxArgLen {
			return nil, l.budget("argument bytes")
		}
		s.Arg, s.HasArg, s.Quote = string(w.buf), w.has || len(w.buf) > 0, q
	}
	if f.live && !ext {
		if err := l.chk.arg(l, pf, s); err != nil {
			return nil, err
		}
	}
	inExt = inExt || ext
	// YANG_READ_SUBSTMT_FOR_GOTO
	k, e, _, err := l.getKeyword()
	if err != nil {
		return nil, err
	}
	switch {
	case k == ";" && !e:
		s.End = l.pos(l.off - 1)
		return s, l.close(pf, f)
	case k != "{" || e:
		return nil, l.errf(ly.SyntaxYang, "Invalid keyword \"%s\", expected \";\" or \"{\".", kwName(k, e))
	}
	for {
		k, e, st, err := l.getKeyword()
		if err != nil {
			return nil, err
		}
		if !e && k == "}" {
			s.End = l.pos(l.off - 1)
			return s, l.close(pf, f)
		}
		if !e && (k == ";" || k == "{") && !inExt { // parse_ext_substmt keeps them as statements
			return nil, l.errf(ly.SyntaxYang, "Invalid keyword \"%s\" as a child of \"%s\".", k, kwName(kw, ext))
		}
		if f.live && !ext {
			if err := l.chk.child(l, f, k, e); err != nil {
				return nil, err
			}
		}
		c, err := l.stmt(k, e, st, f, inExt)
		if err != nil {
			return nil, err
		}
		s.Subs = append(s.Subs, c)
	}
}

func (l *lexer) close(pf, f *frame) error {
	if !f.live {
		return nil
	}
	return l.chk.close(l, pf, f)
}

// skipRedundant is skip_redundant_chars: whitespace and comments around the module.
func (l *lexer) skipRedundant() error {
	for l.off < len(l.src) {
		switch c := l.c(0); {
		case c == '/' && (l.c(1) == '/' || l.c(1) == '*'):
			l.off += 2
			if err := l.skipComment(commentKind(l.src[l.off-1])); err != nil {
				return err
			}
		case c == ' ' || (c >= '\t' && c <= '\r'):
			if c == '\n' {
				l.newline()
			}
			l.off++
		default:
			return nil
		}
	}
	return nil
}

// commentKind maps the second character of "//" or "/*" to skip_comment's kind.
func commentKind(c byte) int {
	if c == '/' {
		return 1
	}
	return 2
}

// skipComment is skip_comment: comment 1 = line comment, 2 = block comment.
func (l *lexer) skipComment(comment int) error {
	for l.c(0) != 0 && comment != 0 {
		c := l.c(0)
		switch comment {
		case 1:
			if c == '\n' {
				comment = 0
			}
		case 2:
			if c == '*' {
				comment = 3
			}
		case 3:
			if c == '/' {
				comment = 0
			} else if c != '*' {
				comment = 2
			}
		}
		if c == '\n' {
			l.newline()
			l.indent = 0
		} else {
			l.indent++
		}
		l.off++
	}
	if l.c(0) == 0 && comment >= 2 {
		return l.errf(ly.Syntax, "Unexpected end-of-input, non-terminated comment.")
	}
	return nil
}

// utf8At is ly_getutf8, including its range quirks (4-byte sequences from U+1000).
func (l *lexer) utf8At() (rune, int, bool) {
	c := rune(l.c(0))
	var n int
	switch {
	case c&0x80 == 0:
		return c, 1, c >= 0x20 || c == '\t' || c == '\n' || c == '\r'
	case c&0xe0 == 0xc0:
		c, n = c&0x1f, 2
	case c&0xf0 == 0xe0:
		c, n = c&0x0f, 3
	case c&0xf8 == 0xf0:
		c, n = c&0x07, 4
	default:
		return 0, 0, false
	}
	for i := 1; i < n; i++ {
		a := rune(l.c(i))
		if a&0xc0 != 0x80 {
			return 0, 0, false
		}
		c = c<<6 | a&0x3f
	}
	switch n {
	case 2:
		return c, n, c >= 0x80
	case 3:
		return c, n, c >= 0x800 && (c <= 0xd7ff || c >= 0xe000) && c <= 0xfffd
	}
	return c, n, c >= 0x1000 && c <= 0x10ffff
}

// isYangChar is is_yangutf8char, including libyang's empty 0x40000 range.
func isYangChar(c rune) bool {
	switch {
	case c >= 0x20 && c <= 0xd7ff, c == 0x09, c == 0x0a, c == 0x0d, c >= 0xe000 && c <= 0xfdcf,
		c >= 0xfdf0 && c <= 0xfffd:
		return true
	case c >= 0x10000 && c <= 0x10ffff && c&0xffff <= 0xfffd:
		return c>>16 != 4 // (c >= 0x40000 && c <= 0x2fffd) in libyang
	}
	return false
}

func identStart(c rune) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' }

func identChar(c rune) bool {
	return identStart(c) || c >= '0' && c <= '9' || c == '-' || c == '.'
}

// cChar renders (char)c as libyang's "%c" does.
func cChar(c rune) string { return string([]byte{byte(c)}) } //nolint:gosec // C (char) truncation, as libyang

// checkIdentChar is lysp_check_identifierchar. prefix: 0 no colon yet, 1 colon just read, 2 after prefix.
func (l *lexer) checkIdentChar(c rune, first bool, prefix *uint8) error {
	switch {
	case first || prefix != nil && *prefix == 1:
		if !identStart(c) {
			if c < 0xff && c >= 0x20 && c < 0x7f {
				return l.errf(ly.SyntaxYang, "Invalid identifier first character '%s' (0x%04x).", cChar(c), c)
			}
			return l.errf(ly.SyntaxYang, "Invalid identifier first character 0x%04x.", c)
		}
		if prefix != nil {
			*prefix = 2
			if first {
				*prefix = 0
			}
		}
	case c == ':' && prefix != nil && *prefix == 0:
		*prefix = 1
	case !identChar(c):
		return l.errf(ly.SyntaxYang, "Invalid identifier character '%s' (0x%04x).", cChar(c), c)
	}
	return nil
}

type word struct {
	buf    []byte
	has    bool
	prefix uint8
}

// storeChar is buf_store_char: one character at the input (or sub, a replacement
// that does not advance the input) is checked against arg and appended to w.
func (l *lexer) storeChar(arg argKind, w *word, sub byte) error {
	c, raw := rune(sub), []byte{sub}
	if sub == 0 {
		r, n, ok := l.utf8At()
		if !ok {
			return l.errf(ly.Syntax, "Invalid character 0x%x.", l.c(0))
		}
		c, raw = r, l.src[l.off:l.off+n]
	}
	if c == '\n' {
		l.indent = 0
		l.line++ // also for a substituted '\n'
		if sub == 0 {
			l.lineAt = l.off + 1
		}
	} else {
		l.indent++ // a multibyte character counts as 1
	}
	switch arg {
	case argIdent:
		if err := l.checkIdentChar(c, len(w.buf) == 0, nil); err != nil {
			return err
		}
	case argPrefIdent:
		if err := l.checkIdentChar(c, len(w.buf) == 0, &w.prefix); err != nil {
			return err
		}
	default:
		if !isYangChar(c) {
			return l.errf(ly.Syntax, "Invalid character 0x%x.", byte(c)) //nolint:gosec // (char)c, as libyang
		}
	}
	w.buf = append(w.buf, raw...)
	if sub == 0 {
		l.off += len(raw)
	}
	return nil
}

// readQString is read_qstring.
func (l *lexer) readQString(arg argKind, w *word) error {
	const (
		ended = iota
		single
		double
		escaped
		pausedNext // string finished, skipping whitespace looking for '+'
		pausedCont // after '+', skipping whitespace
	)
	var blockIndent, curIndent, trailing int
	state := single
	if l.c(0) == '"' {
		state = double
		curIndent = l.indent + 1
		blockIndent = curIndent
	}
	l.move(1)
loop:
	for l.c(0) != 0 && state != ended {
		c := l.c(0)
		var err error
		switch state {
		case single:
			if c == '\'' {
				state = pausedNext
				l.move(1)
			} else {
				err = l.storeChar(arg, w, 0)
			}
		case double:
			switch c {
			case '"':
				state = pausedNext
				l.move(1)
				trailing = 0
			case '\\':
				// RFC 7950 6.1.3: trailing whitespace is trimmed before escapes are substituted
				state = escaped
				l.off++
				trailing = 0
				curIndent = blockIndent
			case ' ':
				if curIndent < blockIndent {
					curIndent++
					l.move(1)
				} else {
					err = l.storeChar(arg, w, 0)
					trailing++
				}
			case '\t':
				if curIndent < blockIndent {
					curIndent += tabSpaces
					l.indent += tabSpaces
					for ; err == nil && curIndent > blockIndent; curIndent, l.indent = curIndent-1, l.indent-1 {
						// the part of the tab beyond the indentation is kept as spaces
						err = l.storeChar(arg, w, ' ')
						trailing++
					}
					l.off++
				} else {
					err = l.storeChar(arg, w, 0)
					trailing++
					l.indent += tabSpaces - 1
				}
			case '\r', '\n':
				if c == '\r' && (l.c(1) == '\n' || l.c(1) == '\\' && l.c(2) == 'n') {
					l.off++
				}
				if blockIndent != 0 {
					w.buf = w.buf[:len(w.buf)-trailing]
					curIndent = 0
				}
				if l.c(0) != '\n' { // a lone '\r' is stored as '\n'
					if err = l.storeChar(arg, w, '\n'); err == nil {
						l.off++
					}
				} else {
					err = l.storeChar(arg, w, 0)
				}
				l.indent = 0
				trailing = 0
			default:
				curIndent = blockIndent
				err = l.storeChar(arg, w, 0)
				trailing = 0
			}
		case escaped:
			switch c {
			case 'n':
				c = '\n'
				l.line-- // storeChar counts it, the escape is not a new line
			case 't':
				c = '\t'
			case '"', '\\':
			default:
				return l.errf(ly.SyntaxYang, "Double-quoted string unknown special character '\\%s'.", cChar(rune(c)))
			}
			if err = l.storeChar(arg, w, c); err == nil {
				l.off++
				state = double
			}
		case pausedNext, pausedCont:
			switch c {
			case '\r', '\n':
				if c == '\r' {
					if l.c(1) != '\n' {
						return l.errf(ly.Syntax, "Invalid character 0x%x.", c)
					}
					l.move(1)
				}
				l.newline()
				l.move(1)
			case ' ', '\t':
				l.move(1)
			case '+':
				if state == pausedCont {
					return l.errf(ly.SyntaxYang, "Both string parts divided by '+' must be quoted.")
				}
				state = pausedCont
				l.move(1)
			default:
				if state == pausedNext {
					break loop
				}
				switch {
				case c == '\'':
					state = single
					l.move(1)
				case c == '"': // block indentation is kept from the first part
					state = double
					l.move(1)
				case c == '/' && (l.c(1) == '/' || l.c(1) == '*'):
					l.move(2)
					err = l.skipComment(commentKind(l.c(-1)))
				default:
					return l.errf(ly.SyntaxYang, "Both string parts divided by '+' must be quoted.")
				}
			}
		}
		if err != nil {
			return err
		}
	}
	if arg <= argPrefIdent && len(w.buf) == 0 {
		return l.errf(ly.SyntaxYang, "Statement argument is required.")
	}
	return nil
}

// getArgument is get_argument.
func (l *lexer) getArgument(arg argKind) (w word, quote byte, err error) {
	const unq = "unquoted string character, optsep, semicolon or opening brace"
	for {
		switch c := l.c(0); c {
		case '\'', '"':
			if len(w.buf) > 0 {
				return w, 0, l.errf(ly.Syntax, "Invalid character sequence \"%c\", expected %s.", c, unq)
			}
			w.has = true
			return w, c, l.readQString(arg, &w)
		case '/':
			if l.c(1) != '/' && l.c(1) != '*' {
				err = l.storeChar(arg, &w, 0)
				break
			}
			if len(w.buf) > 0 {
				return w, 0, l.errf(ly.Syntax, "Invalid comment sequence \"%s\" in an unquoted string.", l.src[l.off:l.off+2])
			}
			l.move(2)
			err = l.skipComment(commentKind(l.c(-1)))
		case ' ', '\t', '\r', '\n':
			if c == '\r' && l.c(1) != '\n' {
				return w, 0, l.errf(ly.Syntax, "Invalid character 0x%x.", c)
			}
			if len(w.buf) > 0 && c != '\r' {
				return w, 0, nil
			}
			switch c {
			case '\t':
				l.indent += tabSpaces
				l.off++
			case '\n':
				l.newline()
				l.off++
				l.indent = 0
			default:
				l.move(1)
			}
		case ';', '{':
			if len(w.buf) > 0 || arg == argMaybeStr {
				return w, 0, nil
			}
			return w, 0, l.errf(ly.Syntax, "Invalid character sequence \"%c\", expected an argument.", c)
		case '}':
			return w, 0, l.errf(ly.Syntax, "Invalid character sequence \"%c\", expected %s.", c, unq)
		case 0:
			return w, 0, l.errf(ly.Syntax, "Unexpected end-of-input.")
		default:
			err = l.storeChar(arg, &w, 0)
		}
		if err != nil {
			return w, 0, err
		}
	}
}

// kwTrie mirrors lysp_match_kw: on a partial match the matched prefix stays consumed.
type kwt struct {
	s   string
	sub []kwt // nil: s completes a keyword
}

var kwTrie = map[byte][]kwt{
	'a': {{"rgument", nil}, {"ugment", nil}, {"ction", nil}, {"ny", []kwt{{"data", nil}, {"xml", nil}}}},
	'b': {{"ase", nil}, {"elongs-to", nil}, {"it", nil}},
	'c': {{"ase", nil}, {"hoice", nil}, {"on", []kwt{{"fig", nil}, {"ta", []kwt{{"ct", nil}, {"iner", nil}}}}}},
	'd': {{"e", []kwt{{"fault", nil}, {"scription", nil}, {"viat", []kwt{{"e", nil}, {"ion", nil}}}}}},
	'e': {{"num", nil}, {"rror-", []kwt{{"app-tag", nil}, {"message", nil}}}, {"xtension", nil}},
	'f': {{"eature", nil}, {"raction-digits", nil}},
	'g': {{"rouping", nil}},
	'i': {{"dentity", nil}, {"f-feature", nil}, {"mport", nil}, {"n", []kwt{{"clude", nil}, {"put", nil}}}},
	'k': {{"ey", nil}},
	'l': {{"e", []kwt{{"af-list", nil}, {"af", nil}, {"ngth", nil}}}, {"ist", nil}},
	'm': {{"a", []kwt{{"ndatory", nil}, {"x-elements", nil}}}, {"in-elements", nil}, {"ust", nil},
		{"od", []kwt{{"ule", nil}, {"ifier", nil}}}},
	'n': {{"amespace", nil}, {"otification", nil}},
	'o': {{"r", []kwt{{"dered-by", nil}, {"ganization", nil}}}, {"utput", nil}},
	'p': {{"ath", nil}, {"attern", nil}, {"osition", nil}, {"re", []kwt{{"fix", nil}, {"sence", nil}}}}, //nolint:misspell // "pre"+"sence"
	'r': {{"ange", nil}, {"e", []kwt{{"f", []kwt{{"erence", nil}, {"ine", nil}}}, {"quire-instance", nil},
		{"vision-date", nil}, {"vision", nil}}}, {"pc", nil}},
	's': {{"tatus", nil}, {"ubmodule", nil}},
	't': {{"ypedef", nil}, {"ype", nil}},
	'u': {{"ni", []kwt{{"que", nil}, {"ts", nil}}}, {"ses", nil}},
	'v': {{"alue", nil}},
	'w': {{"hen", nil}},
	'y': {{"ang-version", nil}, {"in-element", nil}},
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// matchKw is lysp_match_kw: "" for no keyword; ";", "{", "}" for the syntax tokens.
func (l *lexer) matchKw() string {
	start := l.off
	c := l.c(0)
	if c == ';' || c == '{' || c == '}' {
		l.move(1)
		return string(c)
	}
	ts, ok := kwTrie[c]
	if !ok {
		return ""
	}
	l.move(1)
	for found := true; found; {
		found = false
		for _, t := range ts {
			if rest := l.src[l.off:]; len(rest) >= len(t.s) && string(rest[:len(t.s)]) == t.s {
				l.move(len(t.s))
				if t.sub == nil {
					if isAlnum(l.c(0)) { // not terminated; libyang keeps the indent increment
						l.off = start
						return ""
					}
					return string(l.src[start:l.off])
				}
				ts, found = t.sub, true
				break
			}
		}
	}
	if isAlnum(l.c(0)) {
		l.off = start
	}
	return ""
}

// getKeyword is get_keyword: ext reports an extension instance ("prefix:name").
func (l *lexer) getKeyword() (kw string, ext bool, startPos Pos, err error) {
skip:
	for l.c(0) != 0 {
		switch l.c(0) {
		case '/':
			if l.c(1) != '/' && l.c(1) != '*' {
				return "", false, Pos{}, l.errf(ly.SyntaxYang, "Invalid identifier first character '/'.")
			}
			l.move(2)
			if err := l.skipComment(commentKind(l.c(-1))); err != nil {
				return "", false, Pos{}, err
			}
			continue
		case '\n':
			l.newline()
			l.indent = 0
		case ' ':
			l.indent++
		case '\t':
			l.indent += tabSpaces
		case '\r':
			if l.c(1) != '\n' {
				break skip
			}
		default:
			break skip
		}
		l.off++
	}
	start := l.off
	startPos = l.pos(start)
	kw = l.matchKw()
	switch kw {
	case ";":
		return kw, false, startPos, nil
	case "{":
		if l.depth++; l.depth > l.b.MaxDepth {
			return "", false, Pos{}, l.budget("block nestings")
		}
		return kw, false, startPos, nil
	case "}":
		l.depth--
		return kw, false, startPos, nil
	}
	var prefix uint8
	if kw != "" {
		switch l.c(0) {
		case '\r':
			if l.c(1) != '\n' {
				return "", false, Pos{}, l.errf(ly.Syntax, "Invalid character 0x%x.", l.c(0))
			}
			l.move(1)
			return kw, false, startPos, nil
		case '\n', '\t', ' ':
			return kw, false, startPos, nil
		case ':': // a prefix of an extension instance
			prefix = 1
			l.move(1)
		case '{', ';':
			if kw == "input" || kw == "output" {
				return kw, false, startPos, nil
			}
			fallthrough
		default:
			if l.c(0) != 0 {
				l.move(1)
			}
			return "", false, Pos{}, l.errf(ly.Syntax, "Invalid character sequence \"%s\", expected a keyword followed by a separator.",
				l.src[start:l.off])
		}
	}
	for c := l.c(0); c != 0 && c != ' ' && c != '\t' && c != '\n' && c != '\r' && c != '{' && c != ';'; c = l.c(0) {
		r, n, ok := l.utf8At()
		if !ok {
			return "", false, Pos{}, l.errf(ly.Syntax, "Invalid character 0x%x.", c)
		}
		first := l.off == start
		l.off += n
		l.indent++
		if err := l.checkIdentChar(r, first, &prefix); err != nil {
			return "", false, Pos{}, err
		}
	}
	if l.c(0) == 0 {
		return "", false, Pos{}, l.errf(ly.Syntax, "Unexpected end-of-input.")
	}
	if prefix != 2 {
		return "", false, Pos{}, l.errf(ly.Syntax, "Invalid character sequence \"%s\", expected a keyword.", l.src[start:l.off])
	}
	return string(l.src[start:l.off]), true, startPos, nil
}
