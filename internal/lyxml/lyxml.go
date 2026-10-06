// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xml.c, src/xml.h and the UTF-8 helpers of
// src/ly_common.c (BSD-3-Clause, © CESNET).

// Package lyxml is libyang's generic pull XML lexer (lyxml_ctx): elements,
// attributes, text, the namespace stack, entity and character-reference
// decoding and libyang's whitespace handling, with libyang's messages and line
// numbers. It is not a general XML parser: DOCTYPE and non-predefined
// entities are refused, comments and declarations are skipped, and text
// (whitespace-only text included) is returned as it is, never trimmed.
//
// Like libyang the lexer works on a NUL-terminated buffer: the first NUL byte
// ends the input. Positions follow libyang exactly, including that Peek does
// not restore the line counter. All errors libyang logs here use LYVE_SYNTAX
// (not LYVE_SYNTAX_XML) except the nesting limit, which has no vecode.
package lyxml

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/vibe-ports/yang/internal/ly"
)

// MaxDepth is LY_MAX_BLOCK_DEPTH: the number of open elements allowed.
const MaxDepth = 500

// ErrBudget is wrapped by errors caused by an exceeded limit.
var ErrBudget = errors.New("lyxml: resource budget exceeded")

// Error is a lexer error: libyang's vecode (ly.Success for LOGERR errors),
// message (empty where libyang fails without logging) and line.
type Error struct {
	Code ly.Code
	Msg  string
	Line uint64
	err  error
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return "xml: invalid input (line " + strconv.FormatUint(e.Line, 10) + ")"
	}
	return fmt.Sprintf("%s (line %d)", e.Msg, e.Line)
}

// Unwrap returns ErrBudget for budget errors.
func (e *Error) Unwrap() error { return e.err }

// Status is LYXML_PARSER_STATUS: what the last Next parsed.
type Status uint8

// LYXML_PARSER_STATUS values.
const (
	Element     Status = iota // opening element: Prefix, Name
	ElemClose                 // closing element
	ElemContent               // element text: Value, WSOnly
	Attribute                 // attribute name: Prefix, Name
	AttrContent               // attribute value: Value, WSOnly
	End                       // end of input (also after any error)
)

// NS is a namespace declaration (struct lyxml_ns). Prefix is "" for the
// default namespace, as an XML prefix is never empty.
type NS struct {
	Prefix string
	URI    string
	depth  int
}

type elem struct{ prefix, name string }

// Ctx is the lexer (struct lyxml_ctx). Fields are valid as the Status says.
type Ctx struct {
	Status Status
	Prefix string // Element, Attribute
	Name   string // Element, Attribute
	Value  string // ElemContent, AttrContent
	WSOnly bool   // ElemContent, AttrContent: empty or whitespace only

	in    []byte
	pos   int
	line  uint64
	elems stack[elem]
	ns    stack[NS]

	// Duplicate check of the element being opened (replaces lyxml_ns_add's
	// scan of that element's declarations, which is quadratic): prefix ->
	// URI, "" for the default namespace; valid for depth nsSeenDepth.
	nsSeen      map[string]string
	nsSeenDepth int

	// copied counts stack entries copied by copy-on-write (test hook).
	copied int
}

// owner is shared by a stack's array and every backup of it: frozen is the
// highest length any of them has seen, so entries below it must not be
// overwritten in place.
type owner struct{ frozen int }

// stack is a slice that Backup shares instead of copying. Pushing below the
// frozen floor (only possible after a pop) first copies the array, so a
// Backup/Restore pair is O(1) and the copy cost is paid only when the
// restored state is modified below the backed-up depth.
type stack[T any] struct {
	s []T
	o *owner
}

func (k *stack[T]) push(v T, copied *int) {
	if k.o != nil && len(k.s) < k.o.frozen {
		*copied += len(k.s)
		k.s = append([]T(nil), k.s...)
		k.o = nil
	}
	k.s = append(k.s, v)
}

// freeze protects the current entries and returns the previous floor.
func (k *stack[T]) freeze() int {
	if k.o == nil {
		k.o = &owner{}
	}
	prev := k.o.frozen
	k.o.frozen = max(prev, len(k.s))
	return prev
}

// Line is libyang's input line counter (1-based).
func (c *Ctx) Line() uint64 { return c.line }

// Depth is the number of open elements.
func (c *Ctx) Depth() int { return len(c.elems.s) }

// NS returns the declarations in scope, innermost last. Do not modify it; it
// is invalidated by the next call to Next.
func (c *Ctx) NS() []NS { return c.ns.s }

// New is lyxml_ctx_new: it reads up to the first element and leaves the
// lexer at Element (namespaces of the element parsed) or End on empty input.
func New(data []byte) (*Ctx, error) {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		data = data[:i]
	}
	if uint64(len(data)) > math.MaxUint32 { // libyang's uint32 lengths
		return nil, &Error{Code: ly.Syntax, Msg: "XML value too long.", Line: 1, err: ErrBudget}
	}
	c := &Ctx{in: data, line: 1}
	prefix, name, closing, err := c.nextElement()
	if err != nil {
		return nil, err
	}
	switch {
	case c.cur() == 0:
		c.Status = End
	case closing:
		return nil, c.errf(ly.Syntax, "Stray closing element tag (\"%s\").", name)
	default:
		if err := c.openElement(prefix, name); err != nil {
			return nil, err
		}
		c.Prefix, c.Name, c.Status = prefix, name, Element
	}
	return c, nil
}

// Next is lyxml_ctx_next. After an error the status is End. Prefix and Name
// are set only for Element, Attribute and ElemClose, Value and WSOnly only
// for ElemContent and AttrContent; they are zero otherwise (at End too).
func (c *Ctx) Next() error {
	if c.Status == End {
		return nil
	}
	c.Prefix, c.Name, c.Value, c.WSOnly = "", "", "", false
	err := c.next()
	if err != nil {
		c.Status = End
	}
	if c.Status == End {
		c.Prefix, c.Name, c.Value, c.WSOnly = "", "", "", false
	}
	return err
}

func (c *Ctx) next() error {
	switch c.Status {
	case ElemContent:
		if c.cur() == '/' { // "<elem/>"
			e := c.elems.s[len(c.elems.s)-1]
			if err := c.closeElement(e.prefix, e.name, true); err != nil {
				return err
			}
			c.Status = ElemClose
			return nil
		}
		fallthrough
	case ElemClose:
		prefix, name, closing, err := c.nextElement()
		if err != nil {
			return err
		}
		switch {
		case c.cur() == 0:
			c.Status = End
		case closing:
			c.Prefix, c.Name = prefix, name
			if err := c.closeElement(prefix, name, false); err != nil {
				return err
			}
			c.Status = ElemClose
		default:
			c.Prefix, c.Name = prefix, name
			if err := c.openElement(prefix, name); err != nil {
				return err
			}
			c.Status = Element
		}
	case Element, AttrContent:
		prefix, name, err := c.nextAttribute()
		if err != nil {
			return err
		}
		switch c.cur() {
		case '>':
			c.skip(1)
			if c.cur() == 0 {
				return c.eof()
			}
			c.Value, c.WSOnly, err = c.parseValue('<')
			if err != nil {
				return err
			}
			c.Status = ElemContent
		case '/': // no content, but it is still returned
			c.Value, c.WSOnly, c.Status = "", true, ElemContent
		default:
			c.Prefix, c.Name, c.Status = prefix, name, Attribute
		}
	case Attribute:
		var err error
		if c.Value, c.WSOnly, err = c.nextAttrContent(); err != nil {
			return err
		}
		c.Status = AttrContent
	case End:
	}
	return nil
}

// Peek is lyxml_ctx_peek: the status Next would produce. Like libyang it
// restores the input position but not the line counter.
func (c *Ctx) Peek() (Status, error) {
	prev := c.pos
	defer func() { c.pos = prev }()
	switch c.Status {
	case ElemContent:
		if c.cur() == '/' {
			return ElemClose, nil
		}
		fallthrough
	case ElemClose:
		_, _, closing, err := c.nextElement()
		switch {
		case err != nil:
			return 0, err
		case c.cur() == 0:
			return End, nil
		case closing:
			return ElemClose, nil
		}
		return Element, nil
	case Element, AttrContent:
		if _, _, err := c.nextAttribute(); err != nil {
			return 0, err
		}
		if k := c.cur(); k == '>' || k == '/' {
			return ElemContent, nil
		}
		return Attribute, nil
	case Attribute:
		return AttrContent, nil
	}
	return End, nil
}

// Backup is the saved state of lyxml_ctx_backup. Like libyang's it is single
// use and strictly nested (LIFO): pass it to exactly one of Restore or Discard,
// before any older backup is.
type Backup struct {
	c            Ctx
	prevE, prevN int // floors before this backup, put back when it is consumed
	used         bool
}

// Backup is lyxml_ctx_backup, O(1): the stacks are shared, not copied.
func (c *Ctx) Backup() *Backup {
	b := &Backup{prevE: c.elems.freeze(), prevN: c.ns.freeze()}
	b.c = *c
	return b
}

func (b *Backup) consume() {
	if b.used {
		panic("lyxml: Backup used twice")
	}
	b.used = true
	if b.c.elems.o != nil {
		b.c.elems.o.frozen = b.prevE
	}
	if b.c.ns.o != nil {
		b.c.ns.o.frozen = b.prevN
	}
}

// Restore is lyxml_ctx_restore. It consumes b and lowers the stacks' frozen
// floor to what it was before the Backup, so lexing on pushes without copying.
func (c *Ctx) Restore(b *Backup) {
	b.consume()
	copied := c.copied
	*c = b.c
	c.copied = copied
	c.nsSeen = nil
}

// Discard consumes a Backup that will not be restored (the lexer kept going),
// lowering the frozen floor like Restore does.
func (c *Ctx) Discard(b *Backup) { b.consume() }

// GetNS is lyxml_ns_get: the innermost declaration of prefix ("" is the
// default namespace).
func GetNS(set []NS, prefix string) (NS, bool) {
	for i := len(set) - 1; i >= 0; i-- {
		if set[i].Prefix == prefix {
			return set[i], true
		}
	}
	return NS{}, false
}

// GetNS is lyxml_ns_get on the lexer's own stack.
func (c *Ctx) GetNS(prefix string) (NS, bool) { return GetNS(c.ns.s, prefix) }

// AppendText is lyxml_dump_text: it appends text with &, <, > (and, for
// attribute values, ") escaped.
func AppendText(dst []byte, text string, attribute bool) []byte {
	for i := 0; i < len(text); i++ {
		switch ch := text[i]; {
		case ch == '&':
			dst = append(dst, "&amp;"...)
		case ch == '<':
			dst = append(dst, "&lt;"...)
		case ch == '>':
			dst = append(dst, "&gt;"...)
		case ch == '"' && attribute:
			dst = append(dst, "&quot;"...)
		default:
			dst = append(dst, ch)
		}
	}
	return dst
}

// nsAdd is lyxml_ns_add. Only the declarations of the element being opened
// can be duplicates (libyang stops its scan at the parents' entries), so
// they are looked up in a per-element map: the same first-duplicate errors.
func (c *Ctx) nsAdd(prefix, uri string) error {
	if c.nsSeen == nil || c.nsSeenDepth != len(c.elems.s) {
		c.nsSeen, c.nsSeenDepth = map[string]string{}, len(c.elems.s)
	}
	if old, dup := c.nsSeen[prefix]; dup {
		if old == uri {
			return nil
		}
		if prefix == "" {
			return c.errf(ly.Syntax, "Duplicate default XML namespaces \"%s\" and \"%s\".", old, uri)
		}
		return c.errf(ly.Syntax, "Duplicate XML NS prefix \"%s\" used for namespaces \"%s\" and \"%s\".", prefix, old, uri)
	}
	c.nsSeen[prefix] = uri
	c.ns.push(NS{prefix, uri, len(c.elems.s)}, &c.copied)
	return nil
}

// nsRm is lyxml_ns_rm: drops the declarations of the element just closed.
func (c *Ctx) nsRm() {
	for len(c.ns.s) > 0 && c.ns.s[len(c.ns.s)-1].depth == len(c.elems.s)+1 {
		c.ns.s = c.ns.s[:len(c.ns.s)-1]
	}
	if len(c.ns.s) == 0 {
		c.ns = stack[NS]{}
	}
}

// --- input access (C: NUL-terminated buffer) ---

func (c *Ctx) at(i int) byte {
	if p := c.pos + i; p < len(c.in) {
		return c.in[p]
	}
	return 0
}

func (c *Ctx) cur() byte { return c.at(0) }

func (c *Ctx) skip(n int) { c.pos = min(c.pos+n, len(c.in)) }

// str is up to n bytes from absolute offset p (C: "%.*s").
func (c *Ctx) str(p, n int) string { return string(c.in[p:min(p+n, len(c.in))]) }

func (c *Ctx) errf(code ly.Code, format string, a ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...), Line: c.line}
}

func (c *Ctx) eof() error { return c.errf(ly.Syntax, "Unexpected end-of-input.") }

// instrexp is LY_VCODE_INSTREXP for the input at absolute offset p.
func (c *Ctx) instrexp(p int, expected string) error {
	return c.errf(ly.Syntax, "Invalid character sequence \"%s\", expected %s.", c.str(p, 20), expected)
}

func (c *Ctx) inchar(b byte) error { return c.errf(ly.Syntax, "Invalid character 0x%x.", b) }

// moveInput is move_input: skip n bytes, EOF is an error.
func (c *Ctx) moveInput(n int) error {
	c.skip(n)
	if c.cur() == 0 {
		return c.eof()
	}
	return nil
}

func isWS(b byte) bool { return b == 0x20 || b == 0x9 || b == 0xa || b == 0xd }

// ignWS is ign_xmlws.
func (c *Ctx) ignWS() {
	for isWS(c.cur()) {
		if c.cur() == '\n' {
			c.line++
		}
		c.skip(1)
	}
}

func isNameStart(c rune) bool {
	return (c >= 'a' && c <= 'z') || c == '_' || (c >= 'A' && c <= 'Z') ||
		(c >= 0x370 && c <= 0x1fff && c != 0x37e) || (c >= 0xc0 && c <= 0x2ff && c != 0xd7 && c != 0xf7) ||
		c == 0x200c || c == 0x200d || (c >= 0x2070 && c <= 0x218f) || (c >= 0x2c00 && c <= 0x2fef) ||
		(c >= 0x3001 && c <= 0xd7ff) || (c >= 0xf900 && c <= 0xfdcf) || (c >= 0xfdf0 && c <= 0xfffd) ||
		(c >= 0x10000 && c <= 0xeffff)
}

func isNameChar(c rune) bool {
	return isNameStart(c) || c == '-' || (c >= '0' && c <= '9') || c == '.' || c == 0xb7 ||
		(c >= 0x300 && c <= 0x36f) || (c >= 0x203f && c <= 0x2040)
}

// getUTF8 is ly_getutf8 on in[p:] (NUL past the end): the character and its
// length, 0 length on error. Its quirks are kept (four-byte sequences start at
// U+1000, U+FFFE/U+FFFF and control characters other than TAB/LF/CR are errors).
func (c *Ctx) getUTF8(p int) (rune, int) {
	b := func(i int) rune {
		if p+i < len(c.in) {
			return rune(c.in[p+i])
		}
		return 0
	}
	r := b(0)
	var n int
	switch {
	case r&0x80 == 0:
		if r < 0x20 && r != 9 && r != 0xa && r != 0xd {
			return 0, 0
		}
		return r, 1
	case r&0xe0 == 0xc0:
		r, n = r&0x1f, 2
	case r&0xf0 == 0xe0:
		r, n = r&0x0f, 3
	case r&0xf8 == 0xf0:
		r, n = r&0x07, 4
	default:
		return 0, 0
	}
	for i := 1; i < n; i++ {
		a := b(i)
		if a&0xc0 != 0x80 {
			return 0, 0
		}
		r = r<<6 | a&0x3f
	}
	ok := false
	switch n {
	case 2:
		ok = r >= 0x80
	case 3:
		ok = r >= 0x800 && (r <= 0xd7ff || r >= 0xe000) && r <= 0xfffd
	case 4:
		ok = r >= 0x1000 && r <= 0x10ffff
	}
	if !ok {
		return 0, 0
	}
	return r, n
}

// putUTF8 is ly_pututf8: the encoding of v, false where libyang refuses it
// (YANG control characters, surrogates, noncharacters).
func putUTF8(v uint32) ([]byte, bool) {
	switch {
	case v < 0x80:
		if v < 0x20 && v != 9 && v != 0xa && v != 0xd {
			return nil, false
		}
		return []byte{byte(v)}, true
	case v < 0x800:
		return utf8.AppendRune(nil, rune(v)), true
	case v < 0xfffe:
		if v&0xf800 == 0xd800 || (v >= 0xfdd0 && v <= 0xfdef) {
			return nil, false
		}
		return utf8.AppendRune(nil, rune(v)), true
	case v < 0x10fffe:
		if v&0xffe == 0xffe {
			return nil, false
		}
		return utf8.AppendRune(nil, rune(v)), true
	}
	return nil, false
}

// --- lexing ---

// skipSection is skip_section: skip to just after delim; the input is left
// untouched on failure.
func (c *Ctx) skipSection(delim, sect string) error {
	i := bytes.Index(c.in[c.pos:], []byte(delim))
	if i < 0 {
		return c.errf(ly.Syntax, "%s not terminated.", sect)
	}
	for _, ch := range c.in[c.pos : c.pos+i] {
		if ch == '\n' {
			c.line++
		}
	}
	c.skip(i + len(delim))
	return nil
}

// parseIdentifier is lyxml_parse_identifier.
func (c *Ctx) parseIdentifier() (string, error) {
	start := c.pos
	r, n := c.getUTF8(c.pos)
	if n == 0 {
		return "", c.inchar(c.cur())
	}
	if !isNameStart(r) {
		// libyang prints the whole rest of the input ("%s"); kept for
		// fidelity: it is one copy of at most the input, made once on the
		// error that ends the parse, so it adds no amplification.
		return "", c.errf(ly.Syntax, "Identifier \"%s\" starts with an invalid character.", c.in[c.pos:])
	}
	for {
		c.skip(n)
		if r, n = c.getUTF8(c.pos); n == 0 {
			return "", c.inchar(c.cur())
		}
		if !isNameChar(r) {
			return string(c.in[start:c.pos]), nil
		}
	}
}

// parseQName is lyxml_parse_qname (prefix "" if none).
func (c *Ctx) parseQName() (prefix, name string, err error) {
	if name, err = c.parseIdentifier(); err != nil {
		return "", "", err
	}
	if c.cur() == ':' {
		prefix = name
		if err = c.moveInput(1); err != nil {
			return "", "", err
		}
		if name, err = c.parseIdentifier(); err != nil {
			return "", "", err
		}
	}
	return prefix, name, nil
}

// skipUntilEndOrAfterOTag is lyxml_skip_until_end_or_after_otag.
func (c *Ctx) skipUntilEndOrAfterOTag() error {
	for {
		c.ignWS()
		switch c.cur() {
		case 0:
			if len(c.elems.s) > 0 {
				return c.eof()
			}
			return nil
		case '<':
		default:
			return c.instrexp(c.pos, "element tag start ('<')")
		}
		if err := c.moveInput(1); err != nil {
			return err
		}
		switch c.cur() {
		case '!':
			if err := c.moveInput(1); err != nil {
				return err
			}
			switch {
			case bytes.HasPrefix(c.in[c.pos:], []byte("--")):
				if err := c.moveInput(2); err != nil {
					return err
				}
				if err := c.skipSection("-->", "Comment"); err != nil {
					return err
				}
			case bytes.HasPrefix(c.in[c.pos:], []byte("DOCTYPE")):
				return c.errf(ly.Syntax, "Document Type Declaration not supported.")
			default:
				return c.errf(ly.Syntax, "Unknown XML section \"%s\".", c.str(c.pos-2, 20))
			}
		case '?':
			if err := c.skipSection("?>", "Declaration"); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

// nextElement is lyxml_next_element; at EOF it returns empty names (cur()==0).
func (c *Ctx) nextElement() (prefix, name string, closing bool, err error) {
	if err = c.skipUntilEndOrAfterOTag(); err != nil || c.cur() == 0 {
		return "", "", false, err
	}
	if c.cur() == '/' {
		if err = c.moveInput(1); err != nil {
			return "", "", false, err
		}
		closing = true
	}
	c.ignWS()
	prefix, name, err = c.parseQName()
	return prefix, name, closing, err
}

func isDigit(b byte) bool  { return b >= '0' && b <= '9' }
func isXDigit(b byte) bool { return isDigit(b) || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F') }

// parseValue is lyxml_parse_value: text up to the unescaped endchar (left
// unconsumed), entities and CDATA resolved. wsOnly is true for empty and
// whitespace-only text, which is returned unchanged.
func (c *Ctx) parseValue(end byte) (string, bool, error) {
	start, p, seg := c.pos, c.pos, c.pos
	var buf []byte
	dyn, ws := false, true
	b := func(i int) byte {
		if i < len(c.in) {
			return c.in[i]
		}
		return 0
	}
	for b(p) != 0 {
		switch {
		case b(p) == '&':
			ws = false
			buf = append(buf, c.in[seg:p]...)
			dyn = true
			q := p + 1
			if b(q) != '#' { // only the predefined entities
				rest := c.in[q:]
				var ent string
				var out byte
				switch {
				case bytes.HasPrefix(rest, []byte("lt;")):
					ent, out = "&lt;", '<'
				case bytes.HasPrefix(rest, []byte("gt;")):
					ent, out = "&gt;", '>'
				case bytes.HasPrefix(rest, []byte("amp;")):
					ent, out = "&amp;", '&'
				case bytes.HasPrefix(rest, []byte("apos;")):
					ent, out = "&apos;", '\''
				case bytes.HasPrefix(rest, []byte("quot;")):
					ent, out = "&quot;", '"'
				default:
					return "", false, c.errf(ly.Syntax, "Entity reference \"%s\" not supported, only predefined references allowed.", c.str(p, 10))
				}
				buf = append(buf, out)
				p += len(ent)
			} else {
				q++
				var n uint32 // wraps like libyang's
				switch {
				case isDigit(b(q)):
					for ; isDigit(b(q)); q++ {
						n = 10*n + uint32(b(q)-'0')
					}
				case b(q) == 'x' && isXDigit(b(q+1)):
					for q++; isXDigit(b(q)); q++ {
						switch d := b(q); {
						case isDigit(d):
							n = 16*n + uint32(d-'0')
						case d > 'F':
							n = 16*n + 10 + uint32(d-'a')
						default:
							n = 16*n + 10 + uint32(d-'A')
						}
					}
				default:
					return "", false, c.errf(ly.Syntax, "Invalid character reference \"%s\".", c.str(p, 12))
				}
				if b(q) != ';' {
					return "", false, c.instrexp(q, ";")
				}
				q++
				enc, ok := putUTF8(n)
				if !ok {
					return "", false, c.errf(ly.Syntax, "Invalid character reference \"%s\" (0x%08x).", c.str(p, 12), n)
				}
				buf = append(buf, enc...)
				p = q
			}
			seg = p
		case bytes.HasPrefix(c.in[p:], []byte("<![CDATA[")):
			body := p + len("<![CDATA[")
			u := bytes.Index(c.in[body:], []byte("]]>"))
			if u < 0 {
				return "", false, c.errf(ly.Syntax, "%s not terminated.", "CDATA")
			}
			buf = append(buf, c.in[seg:p]...)
			dyn = true
			for _, ch := range c.in[body : body+u] {
				if ch == '\n' {
					c.line++
				} else if !isWS(ch) {
					ws = false
				}
			}
			buf = append(buf, c.in[body:body+u]...)
			p = body + u + len("]]>")
			seg = p
		case b(p) == end:
			c.pos = p
			if dyn {
				return string(append(buf, c.in[seg:p]...)), ws, nil
			}
			return string(c.in[start:p]), ws, nil
		default:
			if !isWS(b(p)) {
				ws = false
			}
			if b(p) == '\n' {
				c.line++
			}
			_, n := c.getUTF8(p)
			if n == 0 {
				return "", false, c.inchar(b(p))
			}
			p += n
		}
	}
	return "", false, c.eof()
}

// closeElement is lyxml_close_element.
func (c *Ctx) closeElement(prefix, name string, empty bool) error {
	if len(c.elems.s) == 0 {
		return c.errf(ly.Syntax, "Stray closing element tag (\"%s\").", name)
	}
	e := c.elems.s[len(c.elems.s)-1]
	if e.prefix != prefix || e.name != name {
		return c.errf(ly.Syntax, "Opening (\"%s\") and closing (\"%s\") elements tag mismatch.", qn(e.prefix, e.name), qn(prefix, name))
	}
	c.elems.s = c.elems.s[:len(c.elems.s)-1]
	c.nsRm()
	c.ignWS()
	if empty && c.cur() == '/' {
		if err := c.moveInput(1); err != nil {
			return err
		}
	}
	if c.cur() != '>' {
		return c.instrexp(c.pos, "element tag termination ('>')")
	}
	c.skip(1) // no EOF check
	return nil
}

func qn(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + ":" + name
}

func isXMLNS(prefix, name string) bool {
	return prefix == "xmlns" || (prefix == "" && name == "xmlns")
}

// openElement is lyxml_open_element: it pushes the element and declares all
// its namespaces, then rewinds to the first non-namespace attribute.
func (c *Ctx) openElement(prefix, name string) error {
	c.elems.push(elem{prefix, name}, &c.copied)
	c.nsSeen = nil
	if len(c.elems.s) > MaxDepth {
		return &Error{Code: ly.Success, Msg: "The maximum number of open elements has been exceeded.", Line: c.line, err: ErrBudget}
	}
	c.ignWS()
	prevPos, prevLine := c.pos, c.line
	isNS := true
	for c.cur() != 0 {
		r, n := c.getUTF8(c.pos)
		if n == 0 {
			return &Error{Line: c.line} // libyang fails here without logging
		}
		if !isNameStart(r) {
			break
		}
		pfx, nm, err := c.parseQName()
		if err != nil {
			return err
		}
		val, _, err := c.nextAttrContent()
		if err != nil {
			return err
		}
		if isXMLNS(pfx, nm) {
			ns := ""
			if pfx != "" {
				ns = nm
			}
			if err := c.nsAdd(ns, val); err != nil {
				return err
			}
		} else {
			isNS = false
		}
		c.ignWS()
		if isNS {
			prevPos, prevLine = c.pos, c.line
		}
	}
	c.pos, c.line = prevPos, prevLine
	return nil
}

// nextAttrContent is lyxml_next_attr_content.
func (c *Ctx) nextAttrContent() (string, bool, error) {
	c.ignWS()
	if c.cur() == 0 {
		return "", false, c.eof()
	} else if c.cur() != '=' {
		return "", false, c.instrexp(c.pos, "'='")
	}
	if err := c.moveInput(1); err != nil {
		return "", false, err
	}
	c.ignWS()
	if c.cur() == 0 {
		return "", false, c.eof()
	} else if c.cur() != '\'' && c.cur() != '"' {
		return "", false, c.instrexp(c.pos, "either single or double quotation mark")
	}
	quot := c.cur()
	if err := c.moveInput(1); err != nil {
		return "", false, err
	}
	v, ws, err := c.parseValue(quot)
	if err != nil {
		return "", false, err
	}
	c.skip(1) // the ending quote, no EOF check
	return v, ws, nil
}

// nextAttribute is lyxml_next_attribute: the next non-namespace attribute
// name, or (at '>' or '/') nothing.
func (c *Ctx) nextAttribute() (prefix, name string, err error) {
	c.ignWS()
	for c.cur() != '>' && c.cur() != '/' {
		if c.cur() == 0 {
			return "", "", c.eof()
		}
		if r, n := c.getUTF8(c.pos); n == 0 || !isNameStart(r) {
			return "", "", c.instrexp(c.pos, "element tag end ('>' or '/>') or an attribute")
		}
		if prefix, name, err = c.parseQName(); err != nil {
			return "", "", err
		}
		if !isXMLNS(prefix, name) {
			break
		}
		if _, _, err = c.nextAttrContent(); err != nil { // namespaces were stored by openElement
			return "", "", err
		}
		c.ignWS()
	}
	return prefix, name, nil
}
