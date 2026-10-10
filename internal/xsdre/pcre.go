// SPDX-License-Identifier: BSD-3-Clause AND BSD-3-Clause WITH PCRE2-exception
// Ported from libyang v5.8.6 src/ly_common.c (ly_pat_compile_xmlschema,
// ly_pat_compile_xmlschema_chblocks_xmlschema2perl) (BSD-3-Clause, © CESNET) and from PCRE2 10.46
// src/pcre2_compile.c (parse_regex, check_escape, get_ucp, read_repeat_counts, read_number,
// check_posix_syntax) and src/pcre2_error.c (messages)
// (BSD-3-Clause WITH PCRE2-exception, © University of Cambridge; see NOTICE).

package xsdre

import (
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// CompileCompat compiles pattern as libyang v5.8.6 does (issue #73): libyang rewrites the XSD
// pattern textually (escapes ^ and $ outside brackets, replaces \p{IsX} by its block table)
// and hands the result to PCRE2 with PCRE2_UTF | PCRE2_UCP | PCRE2_DOLLAR_ENDONLY |
// PCRE2_NO_AUTO_CAPTURE, anchored at both ends. The PCRE2 pattern is parsed with PCRE2 10.46's
// grammar and semantics (\d = \p{Nd}; \w = L, N, Mn, Pc; \s = Z, \h, \v; '.' all but \n) and
// translated to RE2. An invalid pattern returns an *Error with Compat set whose Error() is the
// text libyang prints between the parentheses of `Regular expression "…" is not valid (…).`.
// A PCRE2 construct RE2 cannot express, or that this port does not translate, is ErrUnsupported
// (U-0011), never approximated.
func CompileCompat(pattern string) (*Pattern, error) {
	if !utf8.ValidString(pattern) {
		return nil, &Error{ErrSyntax, pattern, 0, "pattern is not valid UTF-8", false}
	}
	perl, src, err := lyPerlRegex(pattern)
	if err != nil {
		return nil, err
	}
	p := &pcreParser{pat: pattern, s: perl, src: src, badRef: -1}
	n, err := p.alt(0)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.s) { // only an unmatched ')' stops the top level early
		p.pos++
		return nil, p.fail(22, p.pos-1) // FAILED_BACK
	}
	if p.badRef >= 0 { // found by compile_regex, after parsing
		return nil, p.fail(15, p.badRef)
	}
	lo, hi := p.size(n, false)
	lo, hi = lo+1, hi+1 // OP_END, counted in the length too
	switch {
	case lo > pcreMaxSize: // compile_regex: "regular expression is too large", at the end
		return nil, p.fail(20, len(p.s))
	case hi > pcreMaxSize:
		return nil, p.unsupported("pattern near the 64 KiB compiled-size limit")
	}
	return build(pattern, n)
}

// lyErr is libyang's "Regular expression ... is not valid" detail: the rest of the (rewritten)
// pattern where the error was found and the message; off is the offset in the YANG pattern.
func lyErr(pattern string, off int, rest, msg string) *Error {
	return &Error{ErrSyntax, pattern, off, `"` + rest + `": ` + msg, true}
}

// lyPerlRegex ports the rewriting of ly_pat_compile_xmlschema and
// ly_pat_compile_xmlschema_chblocks_xmlschema2perl. src maps every byte of the result (and its
// end) to the offset in pattern it comes from. libyang substitutes the blocks by searching the
// whole string again after each replacement; a replacement never contains or forms "\p{Is", so
// one forward pass finds the same occurrences, and the bracket depth libyang counts over the
// already rewritten prefix is kept as the output grows (linear in the pattern length).
func lyPerlRegex(pattern string) (perl string, src []int, err error) {
	var b []byte
	brack, escaped := 0, false
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '$', '^':
			if brack == 0 {
				b, src = append(b, '\\'), append(src, i)
			}
		case '\\':
			escaped = !escaped
			b, src = append(b, c), append(src, i)
			continue
		case '[':
			if !escaped {
				brack++
			}
		case ']':
			if brack == 0 && !escaped {
				return "", nil, lyErr(pattern, i, pattern[i:], "character group doesn't begin with '['")
			} else if !escaped {
				brack--
			}
		}
		b, src = append(b, pattern[i]), append(src, i)
		escaped = false
	}
	in, inSrc := string(b), src
	out, outSrc := make([]byte, 0, len(in)), make([]int, 0, len(in)+1)
	depth := 0 // brackets open in out, as libyang counts them (a '[' or ']' not after '\\')
	emit := func(s string, from int) {
		for k := range len(s) {
			if c := s[k]; (c == '[' || c == ']') && (len(out) == 0 || out[len(out)-1] != '\\') {
				if c == '[' {
					depth++
				} else {
					depth--
				}
			}
			out, outSrc = append(out, s[k]), append(outSrc, from)
		}
	}
	for i := 0; i < len(in); {
		j := strings.Index(in[i:], `\p{Is`)
		if j < 0 {
			for k := i; k < len(in); k++ {
				emit(in[k:k+1], inSrc[k])
			}
			break
		}
		start := i + j
		for k := i; k < start; k++ {
			emit(in[k:k+1], inSrc[k])
		}
		e := strings.IndexByte(in[start:], '}')
		if e < 0 {
			return "", nil, lyErr(pattern, inSrc[start+2], in[start+2:], "unterminated character property")
		}
		bi := slices.IndexFunc(lyBlocks, func(bl [2]string) bool { return strings.HasPrefix(in[start+5:], bl[0]) })
		if bi < 0 {
			return "", nil, lyErr(pattern, inSrc[start+5], in[start+5:], "unknown block name")
		}
		urange := lyBlocks[bi][1]
		if depth != 0 { // already in brackets
			urange = urange[1 : len(urange)-1]
		}
		emit(urange, inSrc[start])
		i = start + e + 1
	}
	return string(out), append(outSrc, len(pattern)), nil
}

// lyBlocks is ublock2urange, searched in order by prefix (D-0006).
var lyBlocks = [][2]string{
	{"BasicLatin", `[\x{0000}-\x{007F}]`},
	{"Latin-1Supplement", `[\x{0080}-\x{00FF}]`},
	{"LatinExtended-A", `[\x{0100}-\x{017F}]`},
	{"LatinExtended-B", `[\x{0180}-\x{024F}]`},
	{"IPAExtensions", `[\x{0250}-\x{02AF}]`},
	{"SpacingModifierLetters", `[\x{02B0}-\x{02FF}]`},
	{"CombiningDiacriticalMarks", `[\x{0300}-\x{036F}]`},
	{"Greek", `[\x{0370}-\x{03FF}]`},
	{"Cyrillic", `[\x{0400}-\x{04FF}]`},
	{"Armenian", `[\x{0530}-\x{058F}]`},
	{"Hebrew", `[\x{0590}-\x{05FF}]`},
	{"Arabic", `[\x{0600}-\x{06FF}]`},
	{"Syriac", `[\x{0700}-\x{074F}]`},
	{"Thaana", `[\x{0780}-\x{07BF}]`},
	{"Devanagari", `[\x{0900}-\x{097F}]`},
	{"Bengali", `[\x{0980}-\x{09FF}]`},
	{"Gurmukhi", `[\x{0A00}-\x{0A7F}]`},
	{"Gujarati", `[\x{0A80}-\x{0AFF}]`},
	{"Oriya", `[\x{0B00}-\x{0B7F}]`},
	{"Tamil", `[\x{0B80}-\x{0BFF}]`},
	{"Telugu", `[\x{0C00}-\x{0C7F}]`},
	{"Kannada", `[\x{0C80}-\x{0CFF}]`},
	{"Malayalam", `[\x{0D00}-\x{0D7F}]`},
	{"Sinhala", `[\x{0D80}-\x{0DFF}]`},
	{"Thai", `[\x{0E00}-\x{0E7F}]`},
	{"Lao", `[\x{0E80}-\x{0EFF}]`},
	{"Tibetan", `[\x{0F00}-\x{0FFF}]`},
	{"Myanmar", `[\x{1000}-\x{109F}]`},
	{"Georgian", `[\x{10A0}-\x{10FF}]`},
	{"HangulJamo", `[\x{1100}-\x{11FF}]`},
	{"Ethiopic", `[\x{1200}-\x{137F}]`},
	{"Cherokee", `[\x{13A0}-\x{13FF}]`},
	{"UnifiedCanadianAboriginalSyllabics", `[\x{1400}-\x{167F}]`},
	{"Ogham", `[\x{1680}-\x{169F}]`},
	{"Runic", `[\x{16A0}-\x{16FF}]`},
	{"Khmer", `[\x{1780}-\x{17FF}]`},
	{"Mongolian", `[\x{1800}-\x{18AF}]`},
	{"LatinExtendedAdditional", `[\x{1E00}-\x{1EFF}]`},
	{"GreekExtended", `[\x{1F00}-\x{1FFF}]`},
	{"GeneralPunctuation", `[\x{2000}-\x{206F}]`},
	{"SuperscriptsandSubscripts", `[\x{2070}-\x{209F}]`},
	{"CurrencySymbols", `[\x{20A0}-\x{20CF}]`},
	{"CombiningMarksforSymbols", `[\x{20D0}-\x{20FF}]`},
	{"LetterlikeSymbols", `[\x{2100}-\x{214F}]`},
	{"NumberForms", `[\x{2150}-\x{218F}]`},
	{"Arrows", `[\x{2190}-\x{21FF}]`},
	{"MathematicalOperators", `[\x{2200}-\x{22FF}]`},
	{"MiscellaneousTechnical", `[\x{2300}-\x{23FF}]`},
	{"ControlPictures", `[\x{2400}-\x{243F}]`},
	{"OpticalCharacterRecognition", `[\x{2440}-\x{245F}]`},
	{"EnclosedAlphanumerics", `[\x{2460}-\x{24FF}]`},
	{"BoxDrawing", `[\x{2500}-\x{257F}]`},
	{"BlockElements", `[\x{2580}-\x{259F}]`},
	{"GeometricShapes", `[\x{25A0}-\x{25FF}]`},
	{"MiscellaneousSymbols", `[\x{2600}-\x{26FF}]`},
	{"Dingbats", `[\x{2700}-\x{27BF}]`},
	{"BraillePatterns", `[\x{2800}-\x{28FF}]`},
	{"CJKRadicalsSupplement", `[\x{2E80}-\x{2EFF}]`},
	{"KangxiRadicals", `[\x{2F00}-\x{2FDF}]`},
	{"IdeographicDescriptionCharacters", `[\x{2FF0}-\x{2FFF}]`},
	{"CJKSymbolsandPunctuation", `[\x{3000}-\x{303F}]`},
	{"Hiragana", `[\x{3040}-\x{309F}]`},
	{"Katakana", `[\x{30A0}-\x{30FF}]`},
	{"Bopomofo", `[\x{3100}-\x{312F}]`},
	{"HangulCompatibilityJamo", `[\x{3130}-\x{318F}]`},
	{"Kanbun", `[\x{3190}-\x{319F}]`},
	{"BopomofoExtended", `[\x{31A0}-\x{31BF}]`},
	{"EnclosedCJKLettersandMonths", `[\x{3200}-\x{32FF}]`},
	{"CJKCompatibility", `[\x{3300}-\x{33FF}]`},
	{"CJKUnifiedIdeographsExtensionA", `[\x{3400}-\x{4DB5}]`},
	{"CJKUnifiedIdeographs", `[\x{4E00}-\x{9FFF}]`},
	{"YiSyllables", `[\x{A000}-\x{A48F}]`},
	{"YiRadicals", `[\x{A490}-\x{A4CF}]`},
	{"HangulSyllables", `[\x{AC00}-\x{D7A3}]`},
	{"PrivateUse", `[\x{E000}-\x{F8FF}]`},
	{"CJKCompatibilityIdeographs", `[\x{F900}-\x{FAFF}]`},
	{"AlphabeticPresentationForms", `[\x{FB00}-\x{FB4F}]`},
	{"ArabicPresentationForms-A", `[\x{FB50}-\x{FDFF}]`},
	{"CombiningHalfMarks", `[\x{FE20}-\x{FE2F}]`},
	{"CJKCompatibilityForms", `[\x{FE30}-\x{FE4F}]`},
	{"SmallFormVariants", `[\x{FE50}-\x{FE6F}]`},
	{"ArabicPresentationForms-B", `[\x{FE70}-\x{FEFE}]`},
	{"HalfwidthandFullwidthForms", `[\x{FF00}-\x{FFEF}]`},
	{"Specials", `[\x{FEFF}|\x{FFF0}-\x{FFFD}]`},
}

// pcreMsgs are the PCRE2 10.46 compile error texts (pcre2_error.c) of the errors detected here.
var pcreMsgs = map[int]string{
	1:  `\ at end of pattern`,
	2:  `\c at end of pattern`,
	26: "a relative value of zero is not allowed",
	55: `missing opening brace after \o`,
	57: `\g is not followed by a braced, angle-bracketed, or quoted name/number or by a plain number`,
	64: `non-octal character in \o{} (closing brace missing?)`,
	68: `\c must be followed by a printable ASCII character`,
	3:  `unrecognized character follows \`,
	4:  "numbers out of order in {} quantifier",
	5:  "number too big in {} quantifier",
	6:  "missing terminating ] for character class",
	7:  "escape sequence is invalid in character class",
	8:  "range out of order in character class",
	9:  "quantifier does not follow a repeatable item",
	20: "regular expression is too large",
	12: "POSIX named classes are supported only within a class",
	13: "POSIX collating elements are not supported",
	14: "missing closing parenthesis",
	15: "reference to non-existent subpattern",
	19: "parentheses are too deeply nested",
	22: "unmatched closing parenthesis",
	30: "unknown POSIX class name",
	34: `character code point value in \x{} or \o{} is too large`,
	37: `PCRE2 does not support \F, \L, \l, \N{name}, \U, or \u`,
	46: `malformed \P or \p sequence`,
	47: `unknown property after \P or \p`,
	50: "invalid range in character class",
	61: "subpattern number is too big",
	67: `non-hex character in \x{} (closing brace missing?)`,
	71: `\N is not supported in a class`,
	73: "disallowed Unicode code point (>= 0xd800 && <= 0xdfff)",
	78: `digits missing after \x or in \x{} or \o{} or \N{U+}`,
}

const (
	pcreMaxRepeat  = 65535 // MAX_REPEAT_COUNT
	pcreMaxGroup   = 65535 // MAX_GROUP_NUMBER
	pcreNestLimit  = 250   // PARENS_NEST_LIMIT (default build)
	pcreUnlimited  = -1
	pcreMaxNameLen = 50 // get_ucp name[50]
	// pcreMaxSize is MAX_PATTERN_SIZE for the 8-bit library with the default LINK_SIZE 2: the
	// compiled length above which pcre2_compile fails with ERR20.
	pcreMaxSize = 1 << 16
)

type pcreParser struct {
	pat    string // the YANG pattern (for messages)
	s      string // the PCRE2 pattern libyang compiles
	src    []int  // offset in pat of every byte of s, and of its end
	pos    int
	badRef int              // offset of the first back reference: every group is non-capturing (ERR15)
	cost   map[*node][2]int // compiled length bounds of single items (size)
	multi  bool             // the last class lists more than one character
	wide   bool             // the last class has a property or type item, or a char above 0xff
	// collapse is the compiled length of the last class when PCRE2 compiles it as one character
	// (a one-character class, or a positive class of a case pair), else 0
	collapse int
	anyClass bool // the last class is positive and lists \p{Any} (or a double negation of it)
	// propItems is the number of items of the last class when they are all properties or
	// character types other than \h \v (each one XCL_PROP item), else 0
	propItems int
	// classExpansion counts ranges copied into parsed character classes, bounding memory before
	// the PCRE2 compiled-size bounds and RE2 translation are computed (U-0001).
	classExpansion int
}

// item records a single item and the bounds of its compiled length.
func (p *pcreParser) item(n *node, lo, hi int) *node {
	if p.cost == nil {
		p.cost = map[*node][2]int{}
	}
	p.cost[n] = [2]int{lo, hi}
	return n
}

// size bounds the length pcre2_compile computes for n (compile_regex's length pass) without
// porting its code generation: lo never exceeds it, hi is never below it. A single item costs
// its opcode and operand and is repeated by count opcodes; a group (and the whole pattern) is
// OP_BRA … OP_KET, one OP_ALT per extra branch; a repeated group is copied once per
// repetition (max copies when max is finite, else max(min, 1)), each optional copy with
// OP_BRAZERO and its own brackets; a group repeated {0} is kept once behind OP_SKIPZERO, and a
// single item repeated {0} still counts, as the length pass never gives back code it allocated. alt is set for the branches of an alternation, which share
// its brackets.
func (p *pcreParser) size(n *node, alt bool) (lo, hi int64) {
	const ceil = 1 << 40 // saturates far above the limit; operands stay below 2^41 * 2^16
	sat := func(x int64) int64 { return min(x, ceil) }
	if c, ok := p.cost[n]; ok {
		return int64(c[0]), int64(c[1])
	}
	switch n.op {
	case opConcat, opAlt:
		for _, s := range n.subs {
			l, h := p.size(s, n.op == opAlt)
			lo, hi = sat(lo+l), sat(hi+h)
		}
		if n.op == opAlt {
			k := int64(len(n.subs))
			lo, hi = sat(lo+3*(k-1)), sat(hi+3*k)
		}
		if !alt {
			lo, hi = sat(lo+6), sat(hi+6)
		}
		return lo, hi
	case opRepeat:
		l, h := p.size(n.subs[0], false)
		if _, single := p.cost[n.subs[0]]; single {
			if n.max == 0 { // the item's code is dropped, its length stays counted
				return l, h
			}
			return l, sat(2*h + 8)
		}
		if n.max == 0 { // kept once behind OP_SKIPZERO (a single item's code is counted too)
			return sat(l + 1), sat(h + 1)
		}
		copies := int64(max(n.min, 1))
		if n.max >= 0 {
			copies = int64(n.max)
		}
		return sat(copies * l), sat(copies*(h+8) + 8)
	}
	return 0, 16
}

// fail is a PCRE2 compile error at offset at of s.
func (p *pcreParser) fail(code, at int) error {
	return lyErr(p.pat, p.src[at], p.s[at:], pcreMsgs[code])
}

func (p *pcreParser) unsupported(what string) error {
	return &Error{ErrUnsupported, p.pat, p.src[min(p.pos, len(p.s))], "PCRE2 " + what + " is not translated to RE2 (U-0011)", false}
}

func (p *pcreParser) addClassExpansion(n int) error {
	if n > maxClassExpansion-p.classExpansion {
		return &Error{ErrUnsupported, p.pat, p.src[min(p.pos, len(p.s))],
			"character-class expansion exceeds 1048576 ranges (U-0001)", false}
	}
	p.classExpansion += n
	return nil
}

// item kinds returned by escape
const (
	escChar    = iota // a code point
	escSet            // a character type or property
	escStart          // \A
	escEnd            // \z
	escBackref        // \1 … (always an error, no group captures)
	escIgnore         // an isolated \E
)

// alt parses branches up to ')' or the end (the nest of parse_regex).
func (p *pcreParser) alt(depth int) (*node, error) {
	alt := &node{op: opAlt}
	for {
		br, err := p.seq(depth)
		if err != nil {
			return nil, err
		}
		alt.subs = append(alt.subs, br)
		if p.pos >= len(p.s) || p.s[p.pos] != '|' {
			break
		}
		p.pos++
	}
	if len(alt.subs) == 1 {
		return alt.subs[0], nil
	}
	return alt, nil
}

// seq parses one branch: the main loop of parse_regex for one nesting level.
func (p *pcreParser) seq(depth int) (*node, error) {
	cat := &node{op: opConcat}
	okq := false // prev_okquantifier: the last item can be repeated
	for p.pos < len(p.s) {
		c, size := utf8.DecodeRuneInString(p.s[p.pos:])
		var item *node
		switch c {
		case '|', ')':
			return cat, nil
		case '*', '+', '?', '{':
			lo, hi := 0, pcreUnlimited
			switch c {
			case '+':
				lo = 1
			case '?':
				hi = 1
			case '{':
				q, ok, err := p.repeatCounts(p.pos + 1)
				if err != nil {
					return nil, err
				}
				if !ok { // not a quantifier: a literal '{'
					p.pos++
					item, okq = p.item(&node{op: opSet, set: single('{')}, 2, 2), true
					cat.subs = append(cat.subs, item)
					continue
				}
				lo, hi = q.lo, q.hi
				p.pos = q.end - 1
			}
			p.pos++
			if !okq {
				return nil, p.fail(9, p.pos-1) // FAILED_BACK
			}
			for strings.HasPrefix(p.s[p.pos:], `\E`) { // ignored before a + or ? modifier too
				p.pos += 2
			}
			if p.pos < len(p.s) && p.s[p.pos] == '+' {
				return nil, p.unsupported("possessive quantifier")
			}
			if p.pos < len(p.s) && p.s[p.pos] == '?' { // lazy: the same full-match language
				p.pos++
			}
			last := len(cat.subs) - 1
			cat.subs[last] = &node{op: opRepeat, subs: []*node{cat.subs[last]}, min: lo, max: hi}
			okq = false
			continue
		case '(':
			p.pos++
			if p.pos >= len(p.s) {
				return nil, p.fail(14, p.pos)
			}
			switch {
			case p.s[p.pos] == '*':
				return nil, p.unsupported("(* verb or alpha assertion")
			case strings.HasPrefix(p.s[p.pos:], "?:"):
				p.pos += 2
			case p.s[p.pos] == '?':
				return nil, p.unsupported("(? group")
			}
			if p.pos < len(p.s) && depth+1 > pcreNestLimit {
				return nil, p.fail(19, p.pos)
			}
			sub, err := p.alt(depth + 1)
			if err != nil {
				return nil, err
			}
			if p.pos >= len(p.s) {
				return nil, p.fail(14, p.pos)
			}
			p.pos++
			item, okq = sub, true
		case '[':
			from := p.pos
			p.pos++
			set, err := p.class()
			if err != nil {
				return nil, err
			}
			// PCRE2 10.46 compiles a class (compile_branch META_CLASS, compile_class_not_nested)
			// to one of:
			// - OP_CHAR/OP_NOT and the character for one literal, OP_CHARI for a case pair
			//   (classCollapse, exact);
			// - OP_ALLANY (1 byte) when it matches every character: exactly for a positive class
			//   of \p{Any} items only, possibly for others such as [\d\D];
			// - OP_CLASS/OP_NCLASS and a 32-byte bitmap when it has only characters up to 0xff;
			// - OP_XCLASS (header, optional bitmap, items: at least 5 bytes) otherwise, where
			//   each source byte adds at most part of a range or property item.
			// The lower bound takes the smallest of those that can apply; it never exceeds the
			// real length (an under-estimate only widens the band refused as ErrUnsupported).
			lo, hi := 2, 33
			switch {
			case p.collapse > 0:
				lo, hi = p.collapse, p.collapse
			case p.anyClass: // PT_ANY sets the whole bitmap and needs no XCLASS: OP_ALLANY
				lo, hi = 1, 1
			case p.propItems > 0: // OP_XCLASS, LINK, flags, an XCL_PROP item each, XCL_END
				lo, hi = 5+3*p.propItems, 5+3*p.propItems
			case len(set) == 1 && set[0] == rng{0, unicode.MaxRune}:
				lo, hi = 1, max(33, 40+12*(p.pos-from))
			case p.wide:
				hi = 40 + 12*(p.pos-from)
			case p.multi:
				lo = 33
			}
			item, okq = p.item(&node{op: opSet, set: set}, lo, hi), true
		case '^':
			p.pos++
			item, okq = p.item(&node{op: opAssert}, 1, 1), false // OP_CIRC
		case '$': // PCRE2_DOLLAR_ENDONLY
			p.pos++
			item, okq = p.item(&node{op: opAssert, min: 1}, 1, 1), false // OP_DOLL
		case '.':
			p.pos++
			item, okq = p.item(&node{op: opSet, set: complement(single('\n'))}, 1, 1), true // OP_ANY
		case '\\':
			if strings.HasPrefix(p.s[p.pos:], `\E`) { // ignored, the quantification state too
				p.pos += 2
				continue
			}
			var letter byte // the escape letter; 0 for a trailing '\\' (ERR1)
			if p.pos+1 < len(p.s) {
				letter = p.s[p.pos+1]
			}
			kind, r, set, err := p.escape(false)
			if err != nil {
				return nil, err
			}
			switch kind {
			case escChar:
				item, okq = p.item(&node{op: opSet, set: single(r)}, 1+utf8.RuneLen(r), 1+utf8.RuneLen(r)), true
			case escSet:
				if err := p.addClassExpansion(len(set)); err != nil {
					return nil, err
				}
				// compile_branch: \h \H \v \V \N are one opcode, \p{Any} is OP_ALLANY, \P{Any}
				// an empty 32-byte OP_CLASS, the other properties and types OP_PROP/OP_NOTPROP
				n := 3
				switch {
				case strings.IndexByte("hHvVN", letter) >= 0:
					n = 1
				case len(set) == 0:
					n = 33
				case len(set) == 1 && set[0] == rng{0, unicode.MaxRune}:
					n = 1
				}
				item, okq = p.item(&node{op: opSet, set: set}, n, n), true
			case escStart:
				item, okq = p.item(&node{op: opAssert}, 1, 1), false // OP_SOD
			case escEnd:
				item, okq = p.item(&node{op: opAssert, min: 1}, 1, 1), false // OP_EOD
			case escBackref:
				item, okq = p.item(&node{op: opSet}, 1, 8), true // never compiled: ERR15
			}
		default:
			p.pos += size
			item, okq = p.item(&node{op: opSet, set: single(c)}, 1+size, 1+size), true // OP_CHAR c
		}
		cat.subs = append(cat.subs, item)
	}
	return cat, nil
}

type repeat struct{ lo, hi, end int }

// repeatCounts ports read_repeat_counts from pos (after '{'): ok false when the text is not a
// quantifier (a literal '{'); end is the offset after '}'.
func (p *pcreParser) repeatCounts(pos int) (repeat, bool, error) {
	s := p.s
	space := func(i int) int {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		return i
	}
	digits := func(i int) int {
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		return i
	}
	// syntax check first
	q := space(pos)
	pp := digits(q)
	hadMin := pp > q
	pp = space(pp)
	if pp >= len(s) {
		return repeat{}, false, nil
	}
	if s[pp] == '}' {
		if !hadMin {
			return repeat{}, false, nil
		}
	} else {
		if s[pp] != ',' {
			return repeat{}, false, nil
		}
		pp = space(pp + 1)
		if pp >= len(s) {
			return repeat{}, false, nil
		}
		if d := digits(pp); d > pp {
			pp = d
		} else if !hadMin {
			return repeat{}, false, nil
		}
		pp = space(pp)
		if pp >= len(s) || s[pp] != '}' {
			return repeat{}, false, nil
		}
	}
	// read_number(…, MAX_REPEAT_COUNT, ERR5, …)
	number := func(i int) (n, end int, found bool, err error) {
		if i >= len(s) || s[i] < '0' || s[i] > '9' {
			return 0, i, false, nil
		}
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			n = n*10 + int(s[i]-'0')
			i++
			if n > pcreMaxRepeat {
				return 0, digits(i), false, p.fail(5, digits(i))
			}
		}
		return n, i, true, nil
	}
	r := repeat{hi: pcreUnlimited}
	n, i, found, err := number(q)
	if err != nil {
		return repeat{}, false, err
	}
	if !found { // {,m}
		i = space(i + 1)
		if r.hi, i, found, err = number(i); err != nil {
			return repeat{}, false, err
		}
		_ = found
	} else {
		r.lo = n
		i = space(i)
		if s[i] == '}' {
			r.hi = n
		} else {
			i = space(i + 1)
			m, j, found, err := number(i)
			if err != nil {
				return repeat{}, false, err
			}
			i = j
			if found {
				r.hi = m
				if m < n {
					return repeat{}, false, p.fail(4, i)
				}
			}
		}
	}
	r.end = space(i) + 1
	return r, true, nil
}

// escape ports check_escape (and its use in parse_regex) from p.pos, at the backslash.
func (p *pcreParser) escape(class bool) (kind int, r rune, set charSet, err error) {
	s := p.s
	p.pos++
	if p.pos >= len(s) {
		return 0, 0, nil, p.fail(1, p.pos)
	}
	c, size := utf8.DecodeRuneInString(s[p.pos:])
	p.pos += size
	if c < '0' || c > 'z' {
		return escChar, c, nil, nil
	}
	switch c {
	case ':', ';', '<', '=', '>', '?', '@', '[', '\\', ']', '^', '_', '`':
		return escChar, c, nil, nil
	case 'a':
		return escChar, 7, nil, nil
	case 'e':
		return escChar, 0x1b, nil, nil
	case 'f':
		return escChar, '\f', nil, nil
	case 'n':
		return escChar, '\n', nil, nil
	case 'r':
		return escChar, '\r', nil, nil
	case 't':
		return escChar, '\t', nil, nil
	case 'd', 'D', 's', 'S', 'w', 'W', 'h', 'H', 'v', 'V':
		return escSet, 0, pcreType(c), nil
	case 'p', 'P':
		set, err := p.property(c == 'P')
		return escSet, 0, set, err
	case 'N':
		if p.pos < len(s) && s[p.pos] == '{' {
			return 0, 0, nil, p.unsupported(`\N{`)
		}
		if class {
			return 0, 0, nil, p.fail(71, p.pos)
		}
		return escSet, 0, memoSet("N", func() charSet { return complement(single('\n')) }), nil
	case 'b':
		if class {
			return escChar, 8, nil, nil
		}
		return 0, 0, nil, p.unsupported(`\b (Unicode word boundary)`)
	case 'k':
		if class {
			return escChar, 'k', nil, nil
		}
		return 0, 0, nil, p.unsupported(`\k`)
	case 'g':
		if class {
			return escChar, 'g', nil, nil
		}
		return p.escG()
	case 'B', 'R', 'X':
		if class {
			return 0, 0, nil, p.fail(7, p.pos-1)
		}
		return 0, 0, nil, p.unsupported(`\` + string(c))
	case 'A', 'z', 'Z', 'G', 'K', 'C':
		if class {
			return 0, 0, nil, p.fail(7, p.pos-1)
		}
		switch c {
		case 'A':
			return escStart, 0, nil, nil
		case 'z':
			return escEnd, 0, nil, nil
		}
		return 0, 0, nil, p.unsupported(`\` + string(c))
	case 'E':
		return escIgnore, 0, nil, nil
	case 'Q':
		return 0, 0, nil, p.unsupported(`\Q…\E quoting`)
	case 'F', 'l', 'L', 'u', 'U':
		return 0, 0, nil, p.fail(37, p.pos)
	case 'x':
		r, err := p.hex()
		return escChar, r, nil, err
	case 'c':
		if p.pos >= len(s) {
			return 0, 0, nil, p.fail(2, p.pos)
		}
		v := s[p.pos]
		if v >= 'a' && v <= 'z' {
			v -= 'a' - 'A'
		}
		if v < 32 || v > 126 {
			return 0, 0, nil, p.fail(68, p.pos)
		}
		p.pos++
		return escChar, rune(v ^ 0x40), nil, nil
	case 'o':
		r, err := p.octal()
		return escChar, r, nil, err
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		if !class {
			old := p.pos
			i, n := p.pos-1, 0
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				if n = n*10 + int(s[i]-'0'); n > pcreMaxGroup {
					n = pcreMaxGroup + 1
				}
				i++
			}
			if n < 10 || c >= '8' { // a back reference; NO_AUTO_CAPTURE: bracount is 0
				p.pos = i
				if n > pcreMaxGroup {
					return 0, 0, nil, p.fail(61, p.pos)
				}
				if p.badRef < 0 {
					p.badRef = p.pos - 1
				}
				return escBackref, 0, nil, nil
			}
			p.pos = old
		}
		if c >= '8' {
			return escChar, c, nil, nil
		}
		fallthrough
	case '0':
		v := c - '0'
		for i := 0; i < 2 && p.pos < len(s) && s[p.pos] >= '0' && s[p.pos] <= '7'; i++ {
			v = v*8 + rune(s[p.pos]-'0')
			p.pos++
		}
		return escChar, v, nil, nil
	}
	// any other letter
	p.pos--
	return 0, 0, nil, p.fail(3, p.pos)
}

// escG ports the \g handling of check_escape outside a class, from p.pos after 'g': a numbered
// back reference (always an error: no group captures) or an error; a name or a subroutine call
// is not translated.
func (p *pcreParser) escG() (kind int, r rune, set charSet, err error) {
	s := p.s
	if p.pos >= len(s) {
		return 0, 0, nil, p.fail(57, p.pos)
	}
	if s[p.pos] == '<' || s[p.pos] == '\'' {
		return 0, 0, nil, p.unsupported(`\g subroutine call`)
	}
	// read_number with allow_sign 0 (bracount): +n is n; -n, +0 and too big are errors
	number := func(i int) (n, end, code int, found bool) {
		sign := 0
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			sign = 1
			if s[i] == '-' {
				sign = -1
			}
			i++
		}
		if i >= len(s) || s[i] < '0' || s[i] > '9' {
			return 0, i, 0, false
		}
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			if n = n*10 + int(s[i]-'0'); n > pcreMaxGroup {
				for i < len(s) && s[i] >= '0' && s[i] <= '9' {
					i++
				}
				return 0, i, 61, false
			}
			i++
		}
		switch {
		case sign != 0 && n == 0:
			return 0, i, 26, false
		case sign < 0:
			return 0, i, 15, false
		}
		return n, i, 0, true
	}
	var n int
	if s[p.pos] == '{' {
		i := p.pos + 1
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		m, end, code, found := number(i)
		switch {
		case code != 0: // reported at the \g, p is a copy
			return 0, 0, nil, p.fail(code, p.pos)
		case !found:
			return 0, 0, nil, p.unsupported(`\g{name}`)
		}
		for end < len(s) && (s[end] == ' ' || s[end] == '\t') {
			end++
		}
		if end >= len(s) || s[end] != '}' {
			return 0, 0, nil, p.fail(57, p.pos)
		}
		n, p.pos = m, end+1
	} else {
		m, end, code, found := number(p.pos)
		switch {
		case code != 0:
			p.pos = end
			return 0, 0, nil, p.fail(code, p.pos)
		case !found:
			return 0, 0, nil, p.fail(57, p.pos)
		}
		n, p.pos = m, end
	}
	if n <= 0 {
		return 0, 0, nil, p.fail(15, p.pos)
	}
	if p.badRef < 0 {
		p.badRef = p.pos - 1
	}
	return escBackref, 0, nil, nil
}

// octal ports the \o{…} handling of check_escape, from p.pos after 'o'.
func (p *pcreParser) octal() (rune, error) {
	s := p.s
	if p.pos >= len(s) { // ptr-- without the ++
		return 0, p.fail(55, p.pos-1)
	}
	if s[p.pos] != '{' {
		return 0, p.fail(55, p.pos)
	}
	p.pos++
	for p.pos < len(s) && (s[p.pos] == ' ' || s[p.pos] == '\t') {
		p.pos++
	}
	if p.pos >= len(s) || s[p.pos] == '}' {
		return 0, p.fail(78, p.pos)
	}
	var c rune
	overflow := false
	for p.pos < len(s) && s[p.pos] >= '0' && s[p.pos] <= '7' {
		d := rune(s[p.pos] - '0')
		p.pos++
		if c == 0 && d == 0 {
			continue
		}
		if c = c<<3 + d; c > unicode.MaxRune {
			overflow = true
			break
		}
	}
	for p.pos < len(s) && (s[p.pos] == ' ' || s[p.pos] == '\t') {
		p.pos++
	}
	switch {
	case overflow:
		for p.pos < len(s) && s[p.pos] >= '0' && s[p.pos] <= '7' {
			p.pos++
		}
		return 0, p.fail(34, p.pos)
	case p.pos < len(s) && s[p.pos] == '}':
		p.pos++
		if c >= 0xd800 && c <= 0xdfff {
			return 0, p.fail(73, p.pos-1)
		}
		return c, nil
	case p.pos < len(s):
		return 0, p.fail(64, p.pos)
	default:
		return 0, p.fail(64, p.pos-1)
	}
}

func xdigit(b byte) (rune, bool) {
	switch {
	case b >= '0' && b <= '9':
		return rune(b - '0'), true
	case b >= 'a' && b <= 'f':
		return rune(b-'a') + 10, true
	case b >= 'A' && b <= 'F':
		return rune(b-'A') + 10, true
	}
	return 0, false
}

// hex ports the Perl-style \x handling of check_escape, from p.pos after 'x'.
func (p *pcreParser) hex() (rune, error) {
	s := p.s
	if p.pos < len(s) && s[p.pos] == '{' {
		p.pos++
		for p.pos < len(s) && (s[p.pos] == ' ' || s[p.pos] == '\t') {
			p.pos++
		}
		if p.pos >= len(s) || s[p.pos] == '}' {
			return 0, p.fail(78, p.pos)
		}
		var c rune
		overflow := false
		for p.pos < len(s) {
			d, ok := xdigit(s[p.pos])
			if !ok {
				break
			}
			p.pos++
			if c == 0 && d == 0 {
				continue
			}
			if c = c<<4 | d; c > unicode.MaxRune {
				overflow = true
				break
			}
		}
		for p.pos < len(s) && (s[p.pos] == ' ' || s[p.pos] == '\t') {
			p.pos++
		}
		switch {
		case overflow:
			for p.pos < len(s) {
				if _, ok := xdigit(s[p.pos]); !ok {
					break
				}
				p.pos++
			}
			return 0, p.fail(34, p.pos)
		case p.pos < len(s) && s[p.pos] == '}':
			p.pos++
			if c >= 0xd800 && c <= 0xdfff {
				return 0, p.fail(73, p.pos-1)
			}
			return c, nil
		case p.pos < len(s): // *ptr++ != '}', then ptr--
			return 0, p.fail(67, p.pos)
		default:
			return 0, p.fail(67, p.pos-1)
		}
	}
	if p.pos >= len(s) {
		return 0, p.fail(78, p.pos)
	}
	c, ok := xdigit(s[p.pos])
	if !ok {
		return 0, p.fail(78, p.pos)
	}
	p.pos++
	if p.pos < len(s) {
		if d, ok := xdigit(s[p.pos]); ok {
			c = c<<4 | d
			p.pos++
		}
	}
	return c, nil
}

// pcreType is \d \s \w \h \v (upper case: complement) under PCRE2_UCP (handle_escdsw,
// pcre2_xclass.c PT_SPACE and PT_WORD).
func pcreType(c rune) charSet {
	return memoSet("t"+string(c), func() charSet { return pcreTypeSet(c) })
}

// memoSets caches the character sets of escapes, properties and POSIX classes by name: they are
// immutable, and a pattern may repeat one many times (a class of 100 000 \w).
var memoSets sync.Map

func memoSet(key string, f func() charSet) charSet {
	if v, ok := memoSets.Load(key); ok {
		return v.(charSet)
	}
	v, _ := memoSets.LoadOrStore(key, f())
	return v.(charSet)
}

func pcreTypeSet(c rune) charSet {
	cat := func(n string) charSet { s, _ := category(n); return s }
	h := normalize([]rng{{'\t', '\t'}, {' ', ' '}, {0xA0, 0xA0}, {0x1680, 0x1680}, {0x180E, 0x180E},
		{0x2000, 0x200A}, {0x202F, 0x202F}, {0x205F, 0x205F}, {0x3000, 0x3000}})
	v := normalize([]rng{{'\n', '\r'}, {0x85, 0x85}, {0x2028, 0x2029}})
	var s charSet
	switch unicode.ToLower(c) {
	case 'd':
		s = cat("Nd")
	case 's':
		s = union(union(cat("Z"), h), v)
	case 'w':
		s = union(union(cat("L"), cat("N")), union(cat("Mn"), cat("Pc")))
	case 'h':
		s = h
	case 'v':
		s = v
	}
	if unicode.IsUpper(c) {
		s = complement(s)
	}
	return s
}

// property ports get_ucp from p.pos (after 'p' or 'P').
func (p *pcreParser) property(neg bool) (charSet, error) {
	s := p.s
	if p.pos >= len(s) {
		return nil, p.fail(46, p.pos)
	}
	c := s[p.pos]
	p.pos++
	negated := false
	var name []byte
	vptr := -1
	switch {
	case c == '{':
		if p.pos >= len(s) {
			return nil, p.fail(46, p.pos)
		}
		closed := false
		for len(name) < pcreMaxNameLen-1 {
			if p.pos >= len(s) {
				return nil, p.fail(46, p.pos)
			}
			c = s[p.pos]
			p.pos++
			for c == '_' || c == '-' || c == ' ' || (c >= '\t' && c <= '\r') {
				if p.pos >= len(s) {
					return nil, p.fail(46, p.pos)
				}
				c = s[p.pos]
				p.pos++
			}
			if len(name) == 0 && !negated && c == '^' {
				negated = true
				continue
			}
			if c == '}' {
				closed = true
				break
			}
			if c < '&' || c > 'z' {
				return nil, p.fail(46, p.pos)
			}
			if c >= 'A' && c <= 'Z' {
				c |= 0x20
			} else if (c == ':' || c == '=') && vptr < 0 {
				vptr = len(name)
			}
			name = append(name, c)
		}
		if !closed {
			return nil, p.fail(46, p.pos)
		}
	case c >= 'A' && c <= 'Z':
		name = []byte{c | 0x20}
	case c >= 'a' && c <= 'z':
		name = []byte{c}
	default:
		return nil, p.fail(46, p.pos)
	}
	if vptr >= 0 {
		switch string(name[:vptr]) {
		case "bidiclass", "bc", "script", "sc", "scriptextensions", "scx":
			return nil, p.unsupported(`\p{` + string(name) + `}`)
		}
		return nil, p.fail(47, p.pos)
	}
	n := string(name)
	if _, ok := slices.BinarySearch(pcreUCPNames, n); !ok {
		return nil, p.fail(47, p.pos)
	}
	key := "p" + n
	if neg != negated {
		key = "P" + n
	}
	set := memoSet(key, func() charSet {
		var set charSet
		switch {
		case n == "any":
			set = anyChar()
		case n == "l&" || n == "lc":
			set = union(union(fromTable(unicode.Lu), fromTable(unicode.Ll)), fromTable(unicode.Lt))
		case len(n) <= 2 && strings.ContainsAny(n[:1], "clmnpsz"):
			set, _ = category(strings.ToUpper(n[:1]) + n[1:])
		}
		if set != nil && neg != negated {
			set = complement(set)
		}
		return set
	})
	if set == nil { // a script, binary property or Bidi class; also "ci" "di" "ri" "sd" "vs" "yi"
		return nil, p.unsupported(`\p{` + n + `}`)
	}
	return set, nil
}

// Character class range states (parse_regex); the two OK values must be last.
const (
	rangeNo = iota
	rangeStarted
	rangeForbidNo
	rangeForbidStarted
	rangeOKEscaped
	rangeOKLiteral
)

// checkPOSIX ports check_posix_syntax from pos (at ':', '.' or '='): the offset of the
// terminator before "]", or -1.
func (p *pcreParser) checkPOSIX(pos int) int {
	s := p.s
	term := s[pos]
	for pos++; len(s)-pos >= 2; pos++ {
		switch {
		case s[pos] == '\\' && (s[pos+1] == ']' || s[pos+1] == '\\'):
			pos++
		case (s[pos] == '[' && s[pos+1] == term) || s[pos] == ']':
			return -1
		case s[pos] == term && s[pos+1] == ']':
			return pos
		}
	}
	return -1
}

var posixNames = []string{"alpha", "lower", "upper", "alnum", "ascii", "blank", "cntrl", "digit", "graph",
	"print", "punct", "space", "word", "xdigit"}

// posixClass is a POSIX class under PCRE2_UCP (posix_substitutes); nil for graph, print, punct
// and xdigit, not translated.
func posixClass(name string) charSet {
	cat := func(n string) charSet { s, _ := category(n); return s }
	switch name {
	case "alpha":
		return cat("L")
	case "lower":
		return cat("Ll")
	case "upper":
		return cat("Lu")
	case "alnum":
		return union(cat("L"), cat("N"))
	case "ascii": // not converted: the ASCII print and cntrl bitmaps
		return span(0, 0x7F)
	case "blank":
		return pcreType('h')
	case "cntrl":
		return cat("Cc")
	case "digit":
		return cat("Nd")
	case "space":
		return pcreType('s')
	case "word":
		return pcreType('w')
	}
	return nil
}

// classCollapse ports the one-character optimizations of compile_branch (META_CLASS): a class
// of one literal is OP_CHAR or OP_NOT and the character; a positive class of two literals that
// are case partners (the first has no caseless set of more than two, and the second is its other
// case: the ASCII flip below 128, the Unicode other case above) is OP_CHARI. It returns that
// length, or 0 when the class stays a class.
func classCollapse(lits []rune, neg bool) int {
	switch {
	case len(lits) == 1:
		return 1 + utf8.RuneLen(lits[0])
	case len(lits) != 2 || neg:
		return 0
	}
	c := lits[0]
	orbit := 1
	for f := unicode.SimpleFold(c); f != c; f = unicode.SimpleFold(f) {
		orbit++
	}
	if orbit > 2 { // UCD_CASESET(c) != 0
		return 0
	}
	d := c
	switch {
	case c <= 127 && c >= 'a' && c <= 'z', c <= 127 && c >= 'A' && c <= 'Z':
		d = c ^ 0x20
	case c > 127:
		d = unicode.SimpleFold(c)
	}
	if c != d && lits[1] == d {
		return 1 + utf8.RuneLen(c)
	}
	return 0
}

// class parses a character class from p.pos (after '[') through ']' (parse_regex).
func (p *pcreParser) class() (charSet, error) {
	s := p.s
	p.wide = false
	if strings.HasPrefix(s[p.pos:], "[:<:]]") || strings.HasPrefix(s[p.pos:], "[:>:]]") {
		return nil, p.unsupported("[[:<:]] word boundary")
	}
	if p.pos < len(s) && (s[p.pos] == ':' || s[p.pos] == '.' || s[p.pos] == '=') && p.checkPOSIX(p.pos) >= 0 {
		if s[p.pos] == ':' {
			return nil, p.fail(12, p.pos-1)
		}
		return nil, p.fail(13, p.pos-1)
	}
	next := func() (rune, error) {
		if p.pos >= len(s) {
			return 0, p.fail(6, p.pos)
		}
		c, size := utf8.DecodeRuneInString(s[p.pos:])
		p.pos += size
		return c, nil
	}
	neg := false
	var c rune
	for {
		var err error
		if c, err = next(); err != nil {
			return nil, err
		}
		if c == '\\' && p.pos < len(s) && s[p.pos] == 'E' {
			p.pos++
			continue
		}
		if c == '\\' && strings.HasPrefix(s[p.pos:], `Q\E`) {
			p.pos += 3
			continue
		}
		if neg || c != '^' {
			break
		}
		neg = true
	}
	// the members are collected and normalized when the list has doubled (and once at the end),
	// not merged one by one: a class may have 100 000 members
	var raw []rng
	normalized := 0
	shared := map[*rng]bool{} // memoSet results already added: a repeated \w adds nothing
	add := func(s charSet) error {
		if len(s) > 1 {
			if shared[&s[0]] {
				return nil
			}
			shared[&s[0]] = true
		}
		if err := p.addClassExpansion(len(s)); err != nil {
			return err
		}
		raw = append(raw, s...)
		if len(raw) > 2*normalized+4096 {
			raw = normalize(raw)
			normalized = len(raw)
		}
		return nil
	}
	// lits are the literal characters of PCRE2's parsed class while it has nothing else (other):
	// a range of one character is one literal, a dangling '-' is one too
	var lits []rune
	other := false
	hasAny, propOnly, members := false, true, 0
	state, forbidPtr := rangeNo, 0
	var start rune
	literal := func(c rune, isLiteral bool) error {
		switch state {
		case rangeStarted:
			if c < start {
				return p.fail(8, p.pos-1) // FAILED_BACK
			}
			if err := add(span(start, c)); err != nil {
				return err
			}
			other = other || c != start
			state = rangeNo
		case rangeForbidStarted:
			return p.fail(50, forbidPtr)
		default:
			if err := add(single(c)); err != nil {
				return err
			}
			lits = append(lits, c)
			start, state = c, rangeOKLiteral
			if !isLiteral {
				state = rangeOKEscaped
			}
		}
		return nil
	}
	if c == ']' { // literal at the start
		if err := literal(c, true); err != nil {
			return nil, err
		}
		var err error
		if c, err = next(); err != nil {
			return nil, err
		}
	}
	for {
		switch {
		case c == '[' && len(s)-p.pos >= 3 && (s[p.pos] == ':' || s[p.pos] == '.' || s[p.pos] == '='):
			if t := p.checkPOSIX(p.pos); t >= 0 {
				switch {
				case state == rangeStarted:
					return nil, p.fail(50, t+2)
				case state == rangeForbidStarted:
					return nil, p.fail(50, forbidPtr)
				case s[p.pos] != ':':
					return nil, p.fail(13, t+2)
				}
				name, negPOSIX := strings.CutPrefix(s[p.pos+1:t], "^")
				p.pos = t + 2
				if !slices.Contains(posixNames, name) {
					return nil, p.fail(30, p.pos)
				}
				key := "x" + name
				if negPOSIX {
					key = "X" + name
				}
				ps := memoSet(key, func() charSet {
					ps := posixClass(name)
					if ps != nil && negPOSIX {
						return complement(ps)
					}
					return ps
				})
				if ps == nil {
					return nil, p.unsupported("POSIX class [:" + name + ":]")
				}
				if err := add(ps); err != nil {
					return nil, err
				}
				other, propOnly = true, false
				p.wide = true
				state = rangeForbidNo
				break
			}
			if err := literal(c, true); err != nil {
				return nil, err
			}
		case c == ']':
			if state == rangeStarted {
				if err := add(single('-')); err != nil {
					return nil, err
				}
				lits = append(lits, '-')
			}
			p.anyClass = hasAny && !neg
			p.propItems = 0
			if propOnly && len(lits) == 0 {
				p.propItems = members
			}
			p.collapse = 0
			if !other {
				p.collapse = classCollapse(lits, neg)
			}
			set := normalize(raw)
			if len(set) > 0 && set[len(set)-1].hi > 0xff {
				p.wide = true
			}
			p.multi = len(set) > 1 || len(set) == 1 && set[0].hi > set[0].lo
			if neg {
				set = complement(set)
			}
			return set, nil
		case c == '\\':
			p.pos--
			var letter byte
			if p.pos+1 < len(s) {
				letter = s[p.pos+1]
			}
			kind, r, cs, err := p.escape(true)
			if err != nil {
				return nil, err
			}
			if kind == escIgnore {
				break
			}
			if kind == escChar {
				if err := literal(r, false); err != nil {
					return nil, err
				}
				break
			}
			switch state {
			case rangeStarted:
				return nil, p.fail(50, p.pos)
			case rangeForbidStarted:
				return nil, p.fail(50, forbidPtr)
			}
			if err := add(cs); err != nil {
				return nil, err
			}
			members++
			full := len(cs) == 1 && cs[0] == rng{0, unicode.MaxRune}
			hasAny = hasAny || full && (letter == 'p' || letter == 'P')
			propOnly = propOnly && len(cs) > 0 && !full && strings.IndexByte("dDwWsSpP", letter) >= 0
			p.wide, other = true, true
			state = rangeForbidNo
		case c == '-' && state >= rangeOKEscaped:
			state = rangeStarted
		case c == '-' && state == rangeForbidNo:
			if err := add(single('-')); err != nil {
				return nil, err
			}
			lits = append(lits, '-')
			state, forbidPtr = rangeForbidStarted, p.pos
		default:
			if err := literal(c, true); err != nil {
				return nil, err
			}
		}
		var err error
		if c, err = next(); err != nil {
			return nil, err
		}
	}
}
