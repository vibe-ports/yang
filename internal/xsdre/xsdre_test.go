package xsdre

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

type tc struct {
	name    string
	pattern string
	match   []string
	nomatch []string
}

// Pattern strings from IETF modules are copied verbatim (YANG '+' concatenation
// joined): RFC 6991 (ietf-yang-types / ietf-inet-types, 2013-07-15) and
// RFC 9911 (Common YANG Data Types, 2025, obsoletes RFC 6991).
const (
	dottedQuad = `(([0-9]|[1-9][0-9]|1[0-9][0-9]|2[0-4][0-9]|25[0-5])\.){3}` +
		`([0-9]|[1-9][0-9]|1[0-9][0-9]|2[0-4][0-9]|25[0-5])`
	ipv6Head = `((:|[0-9a-fA-F]{0,4}):)([0-9a-fA-F]{0,4}:){0,5}` +
		`((([0-9a-fA-F]{0,4}:)?(:|[0-9a-fA-F]{0,4}))|` +
		`(((25[0-5]|2[0-4][0-9]|[01]?[0-9]?[0-9])\.){3}` +
		`(25[0-5]|2[0-4][0-9]|[01]?[0-9]?[0-9])))`
	ipv6Loose = `(([^:]+:){6}(([^:]+:[^:]+)|(.*\..*)))|` +
		`((([^:]+:)*[^:]+)?::(([^:]+:)*[^:]+)?)`
	tz9911 = `(Z|[\+\-]((1[0-3]|0[0-9]):([0-5][0-9])|14:00))?`
)

var cases = []tc{
	// RFC 7950 §9.4.7 / §9.4.8 examples.
	{"rfc7950-hex", `[0-9a-fA-F]*`, []string{"", "0aF9"}, []string{"g", "0x1"}},
	{"rfc7950-identifier", `[a-zA-Z_][a-zA-Z0-9\-_.]*`, []string{"_a-b.c", "X"}, []string{"", "1a", "a b"}},
	{"rfc7950-xml-prefix", `[xX][mM][lL].*`, []string{"xml", "XmLfoo"}, []string{"xm", "axml"}},

	// RFC 6991 ietf-yang-types.
	{"6991-object-identifier", `(([0-1](\.[1-3]?[0-9]))|(2\.(0|([1-9]\d*))))(\.(0|([1-9]\d*)))*`,
		[]string{"1.3.6.1", "2.999", "0.39"}, []string{"3.1", "1", "1.3.06"}},
	{"6991-object-identifier-128", `\d*(\.\d*){1,127}`, []string{"1.2", "1." + strings.Repeat("1.", 126)[:252]}, []string{"1", "1" + strings.Repeat(".1", 128)}},
	{"6991-yang-identifier-noxml", `.|..|[^xX].*|.[^mM].*|..[^lL].*`, []string{"x", "xm", "abc", "xmq"}, []string{"xml", "XML", "xMl-foo"}},
	{"6991-date-and-time", `\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[\+\-]\d{2}:\d{2})`,
		[]string{"2024-02-29T12:00:00Z", "2024-02-29T12:00:00.123+01:00", "٢٠٢٤-02-29T12:00:00Z"},
		[]string{"2024-02-29T12:00:00", "2024-02-29 12:00:00Z", "2024-02-29T12:00:00Z\n"}},
	{"6991-phys-address", `([0-9a-fA-F]{2}(:[0-9a-fA-F]{2})*)?`, []string{"", "00:1a", "ff"}, []string{"0", "00:1", "00-1a"}},
	{"6991-mac-address", `[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}`, []string{"00:1A:2b:3c:4d:5e"}, []string{"00:1a:2b:3c:4d", "00:1a:2b:3c:4d:5e:6f"}},
	{"6991-uuid", `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`,
		[]string{"123e4567-e89b-12d3-a456-426614174000"}, []string{"123e4567e89b12d3a456426614174000", "123e4567-e89b-12d3-a456-42661417400g"}},
	{"6991-dotted-quad", dottedQuad, []string{"0.0.0.0", "255.255.255.255"}, []string{"256.1.1.1", "1.2.3", "01.2.3.4x"}},
	{"6991-ipv4-address", dottedQuad + `(%[\p{N}\p{L}]+)?`, []string{"10.0.0.1", "10.0.0.1%eth0", "10.0.0.1%ет0"}, []string{"10.0.0.1%", "10.0.0.1%eth-0", "10.0.0.256"}},
	{"6991-ipv6-address-1", ipv6Head + `(%[\p{N}\p{L}]+)?`, []string{"::", "::1", "2001:db8::1", "::ffff:10.0.0.1", "fe80::1%eth0"}, []string{"2001:db8::g", "1:2:3:4:5:6:7:8:9"}},
	{"6991-ipv6-address-2", ipv6Loose + `(%.+)?`, []string{"::", "2001:db8::1", "1:2:3:4:5:6:7:8"}, []string{"1:2:3", "::1::"[:0] + "12345"}},
	{"6991-ipv4-address-no-zone", `[0-9\.]*`, []string{"1.2.3.4", ""}, []string{"1.2.3.4%x"}},
	{"6991-ipv6-address-no-zone", `[0-9a-fA-F:\.]*`, []string{"fe80::1"}, []string{"fe80::1%eth0"}},
	{"6991-ipv4-prefix", dottedQuad + `/(([0-9])|([1-2][0-9])|(3[0-2]))`, []string{"10.0.0.0/8", "0.0.0.0/0"}, []string{"10.0.0.0/33", "10.0.0.0"}},
	{"6991-ipv6-prefix-1", ipv6Head + `(/(([0-9])|([0-9]{2})|(1[0-1][0-9])|(12[0-8])))`, []string{"2001:db8::/32", "::/0"}, []string{"2001:db8::/129", "2001:db8::"}},
	{"6991-ipv6-prefix-2", ipv6Loose + `(/.+)`, []string{"2001:db8::/32"}, []string{"2001:db8::"}},
	{"6991-domain-name", `((([a-zA-Z0-9_]([a-zA-Z0-9\-_]){0,61})?[a-zA-Z0-9]\.)*([a-zA-Z0-9_]([a-zA-Z0-9\-_]){0,61})?[a-zA-Z0-9]\.?)|\.`,
		[]string{"example.com", "example.com.", ".", "_sip._udp.example.org", "a"}, []string{"-a.com", "a..b", "a-.com", "", strings.Repeat("a", 64)}},

	// RFC 9911 revisions.
	{"9911-object-identifier", `(([0-1](\.[1-3]?[0-9]))|(2\.(0|([1-9][0-9]*))))(\.(0|([1-9][0-9]*)))*`, []string{"1.3.6.1"}, []string{"1.3.٦"}},
	{"9911-object-identifier-128", `[0-9]*(\.[0-9]*){1,127}`, []string{"1.2.3"}, []string{"1"}},
	{"9911-date-and-time", `[0-9]{4}-(1[0-2]|0[1-9])-(0[1-9]|[1-2][0-9]|3[0-1])T(0[0-9]|1[0-9]|2[0-3]):[0-5][0-9]:([0-5][0-9]|60)(\.[0-9]+)?` + tz9911,
		[]string{"2016-12-31T23:59:60Z", "2024-01-01T00:00:00", "2024-01-01T00:00:00.5+14:00"}, []string{"2024-13-01T00:00:00Z", "2024-01-01T24:00:00Z", "2024-01-01T00:00:00+14:01"}},
	{"9911-date", `[0-9]{4}-(1[0-2]|0[1-9])-(0[1-9]|[1-2][0-9]|3[0-1])` + tz9911, []string{"2024-02-29", "2024-02-29Z", "2024-02-29+05:30"}, []string{"2024-02-32"}},
	{"9911-date-no-zone", `[0-9]{4}-(1[0-2]|0[1-9])-(0[1-9]|[1-2][0-9]|3[0-1])`, []string{"2024-02-29"}, []string{"2024-02-29Z"}},
	{"9911-time", `(0[0-9]|1[0-9]|2[0-3]):[0-5][0-9]:([0-5][0-9]|60)(\.[0-9]+)?` + tz9911, []string{"23:59:60", "00:00:00.001Z"}, []string{"24:00:00"}},
	{"9911-time-no-zone", `(0[0-9]|1[0-9]|2[0-3]):[0-5][0-9]:([0-5][0-9]|60)(\.[0-9]+)?`, []string{"12:34:56"}, []string{"12:34:56Z"}},
	{"9911-ipv4-address", dottedQuad + `(%.+)?`, []string{"10.0.0.1%eth0.100"}, []string{"10.0.0.1%"}},
	{"9911-ipv6-address-1", ipv6Head + `(%[A-Za-z0-9][A-Za-z0-9\-\._~/]*)?`, []string{"fe80::1%eth0.1", "fe80::1%en/0~x"}, []string{"fe80::1%-eth0", "fe80::1%ет0"}},
	{"9911-ipv4-address-link-local", `169\.254\..*`, []string{"169.254.1.1"}, []string{"169.255.1.1"}},
	{"9911-ipv6-address-link-local", `[fF][eE][89aAbB][0-9a-fA-F]:.*`, []string{"fe80::1", "FEBF::"}, []string{"fec0::1"}},
	{"9911-host-name", `[a-zA-Z0-9\-\.]+`, []string{"host-1.example"}, []string{"host_1"}},
	{"9911-uri", `[a-z][a-z0-9+.-]*:.*`, []string{"https://example.com", "urn:ietf:x"}, []string{"1http:x", "Http:x"}},
	{"9911-email-address", `.+@.+`, []string{"a@b", "ö@ü"}, []string{"@b", "a@"}},

	// Anchoring and XSD literal '^' / '$'.
	{"anchored", `abc`, []string{"abc"}, []string{"xabc", "abcx", "abc\n", ""}},
	{"caret-dollar-literal", `^a$`, []string{"^a$"}, []string{"a"}},
	{"caret-in-class", `[a^]+`, []string{"^a^"}, []string{"b"}},
	{"alternation-longest", `(a|ab)(c|bcd)(d*)`, []string{"abcd", "abc", "ac"}, []string{"abce"}},
	{"empty-branch", `a|b|`, []string{"", "a", "b"}, []string{"ab"}},
	{"empty-group", `()`, []string{""}, []string{"a"}},
	{"empty-pattern", ``, []string{""}, []string{"a"}},

	// Quantifiers and '{' rules.
	{"repeat-zero", `a{0}`, []string{""}, []string{"a"}},
	{"repeat-min", `a{2,}`, []string{"aa", "aaaa"}, []string{"a"}},
	{"repeat-range", `a{2,3}`, []string{"aa", "aaa"}, []string{"a", "aaaa"}},
	{"repeat-1000", `a{1000}`, []string{strings.Repeat("a", 1000)}, []string{strings.Repeat("a", 999)}},
	{"escaped-braces", `\{a\}`, []string{"{a}"}, []string{"a"}},
	{"braces-in-class", `[{}|]+`, []string{"{|}"}, []string{"a"}},
	{"optional-group", `(ab)?c`, []string{"c", "abc"}, []string{"ac"}},

	// Dot: any char except \n and \r; a supplementary char is one char.
	{"dot", `.`, []string{"a", "😀", "\t", "\u2028"}, []string{"\n", "\r", "", "ab"}},
	{"dot-surrogate-pair", `..`, []string{"😀a"}, []string{"😀"}},
	{"dot-in-class-literal", `[.]`, []string{"."}, []string{"a"}},

	// Multi-character escapes (XSD definitions, not Perl ones).
	{"s", `\s+`, []string{" \t\n\r"}, []string{"\u00a0", "\v", "\f", "\u2003"}},
	{"S", `\S`, []string{"a", "\u00a0", "\v"}, []string{" ", "\n"}},
	{"d", `\d+`, []string{"0123", "٣", "߀"}, []string{"a", "²", "½"}},
	{"D", `\D`, []string{"a", "²"}, []string{"5", "٣"}},
	{"w", `\w`, []string{"a", "5", "€", "+", "é", "\u0301"}, []string{"_", "-", " ", ".", "\u0378", "\u00ad", "\x00", "\ue000"}},
	{"W", `\W`, []string{"_", "-", " ", "\u0378"}, []string{"a", "€"}},
	{"i-c", `\i\c*`, []string{"_x1", "a-b.c", "é·", ":x", "\U00010000"}, []string{"1a", "-a", ".a", "a b", "·"}},
	{"I", `\I`, []string{"1", "-", " "}, []string{"a", "_"}},
	{"C", `\C`, []string{" ", "/"}, []string{"a", "-", "·"}},
	{"escapes", `\n\r\t\\\|\.\?\*\+\(\)\{\}\-\[\]\^`, []string{"\n\r\t\\|.?*+(){}-[]^"}, []string{""}},

	// Category and block escapes.
	{"Lu", `\p{Lu}+`, []string{"ABÄ"}, []string{"a", "A1"}},
	{"L-N", `[\p{L}\p{N}]+`, []string{"a1ж٣"}, []string{"_"}},
	{"P-L", `\P{L}`, []string{"1", " "}, []string{"a"}},
	{"Cn", `\p{Cn}`, []string{"\u0378", "\U000E0080"}, []string{"a", "\ue000"}},
	{"pC", `\p{C}`, []string{"\u0378", "\x00", "\u00ad", "\ue000"}, []string{"a"}},
	{"Cs-empty", `a\p{Cs}?`, []string{"a"}, []string{"a\ufffd"}},
	{"Nd", `\p{Nd}`, []string{"7", "٣"}, []string{"Ⅷ"}},
	{"IsBasicLatin", `\p{IsBasicLatin}+`, []string{"abc~\x00\x7f"}, []string{"é"}},
	{"NotIsBasicLatin", `\P{IsBasicLatin}`, []string{"é", "😀"}, []string{"a"}},
	{"IsGreek-legacy", `\p{IsGreek}`, []string{"α", "Ͱ"}, []string{"ἀ", "a"}},
	{"IsGreekExtended", `\p{IsGreekExtended}`, []string{"ἀ"}, []string{"α"}},
	{"IsGreekandCoptic", `\p{IsGreekandCoptic}`, []string{"α"}, []string{"ἀ"}},
	{"IsCyrillic", `\p{IsCyrillic}+`, []string{"жЖ"}, []string{"a"}},
	{"IsLatin1-minus-Ll", `[\p{IsLatin-1Supplement}-[\p{Ll}]]`, []string{"À", "\u00a0"}, []string{"é", "a"}},
	{"IsMathAlnum", `\p{IsMathematicalAlphanumericSymbols}`, []string{"𝐀"}, []string{"A"}},
	{"IsPrivateUse-legacy", `\p{IsPrivateUse}`, []string{"\ue000", "\U000F0000", "\U0010FFFD"}, []string{"a"}},
	{"IsHighSurrogates", `a\p{IsHighSurrogates}?`, []string{"a"}, []string{"a\ufffd", "a😀"}},

	// Character class subtraction.
	{"sub", `[a-z-[aeiou]]+`, []string{"bcd", "xyz"}, []string{"abc", "e"}},
	{"sub-nested", `[a-z-[aeiou-[e]]]+`, []string{"bed"}, []string{"bad"}},
	{"sub-neg", `[^a-z-[0-9]]`, []string{"A", "_", "😀"}, []string{"b", "5"}},
	{"sub-with-escape", `[\w-[\d]]+`, []string{"ab€"}, []string{"a1"}},
	{"sub-to-empty", `a[a-[a]]?`, []string{"a"}, []string{"aa"}},
	{"neg", `[^abc]`, []string{"d", "\n", "😀"}, []string{"a", ""}},
	{"neg-multi", `[^\d\s]`, []string{"a"}, []string{"1", " "}},

	// Escaped and positional '-'.
	{"dash-escaped", `[a\-z]`, []string{"a", "-", "z"}, []string{"b"}},
	{"dash-first", `[-a]`, []string{"-", "a"}, []string{"b"}},
	{"dash-last", `[a-]`, []string{"-", "a"}, []string{"b"}},
	{"dash-after-range", `[a-c-]`, []string{"b", "-"}, []string{"d"}},
	{"dash-neg-first", `[^-a]`, []string{"b"}, []string{"-", "a"}},
	{"dash-only", `[-]`, []string{"-"}, []string{"a"}},
	{"range-escape-ends", `[\--/]+`, []string{"-./"}, []string{","}},
	{"range-unicode", `[α-ω]+`, []string{"αβω"}, []string{"Α"}},
}

var badSyntax = []string{
	`[a-z-[aeiou]x]`, `[]`, `[^]`, `a**`, `a+?`, `a{2`, `a{,3}`, `a{3,2}`, `{`, `a}`, `a{x}`, `x{1}{2}`,
	`\x`, `\`, `(a`, `a)`, `]`, `[a`, `[a-c-e]`, `[z-a]`, `\p{Foo}`, `\p{IsFoo}`, `\p{isBasicLatin}`,
	`\p{IsBasic Latin}`, `\p{L`, `\pL`, `[\s-z]`, `[a-\d]`, `\$`, `*a`, `a|*`, `[[]`, `(?:a)`, `\1`, `\b`,
	`[a-[b]`, `[--a]`, `[a--]`, "\xff",
}

var unsupported = []string{
	`a{1001}`, `a{0,1001}`, `a{99999999999999999999}`, `(a{100}){100}`,
	strings.Repeat("(", 1001) + strings.Repeat(")", 1001),
	strings.Repeat("[a-", 1001) + "[a]" + strings.Repeat("]", 1001),
}

func TestCases(t *testing.T) {
	for _, c := range cases {
		p, err := Compile(c.pattern)
		if err != nil {
			t.Errorf("%s: Compile(%q): %v", c.name, c.pattern, err)
			continue
		}
		for _, s := range c.match {
			if !p.Match(s) {
				t.Errorf("%s: %q should match %q", c.name, c.pattern, s)
			}
		}
		for _, s := range c.nomatch {
			if p.Match(s) {
				t.Errorf("%s: %q should not match %q", c.name, c.pattern, s)
			}
		}
	}
}

func TestErrors(t *testing.T) {
	for _, want := range []struct {
		kind error
		list []string
	}{{ErrSyntax, badSyntax}, {ErrUnsupported, unsupported}} {
		for _, pat := range want.list {
			_, err := Compile(pat)
			var e *Error
			if !errors.Is(err, want.kind) || !errors.As(err, &e) || e.Reason == "" {
				t.Errorf("Compile(%.40q) = %v, want %v", pat, err, want.kind)
			}
		}
	}
}

func TestInvalidUTF8NeverMatches(t *testing.T) {
	p, _ := Compile(`.*`)
	if p.Match("a\xffb") {
		t.Error("invalid UTF-8 matched")
	}
}

// Blocks only get added between Unicode editions, so a table newer than the
// running Go's unicode package is fine (Go 1.26 = 15.0.0, Go 1.27 = 17.0.0);
// an older one misses blocks.
func TestUnicodeVersion(t *testing.T) {
	if versionLess(blocksUnicodeVersion, unicode.Version) {
		t.Fatalf("Blocks.txt is Unicode %s but unicode package is %s: run go generate", blocksUnicodeVersion, unicode.Version)
	}
}

func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x < y
		}
	}
	return len(as) < len(bs)
}

// Every block name listed in XSD Part 2 (2nd ed.) §F.1.1 resolves.
func TestXSD10BlockNames(t *testing.T) {
	for _, n := range strings.Fields(`BasicLatin Latin-1Supplement LatinExtended-A LatinExtended-B IPAExtensions
	SpacingModifierLetters CombiningDiacriticalMarks Greek Cyrillic Armenian Hebrew Arabic Syriac Thaana
	Devanagari Bengali Gurmukhi Gujarati Oriya Tamil Telugu Kannada Malayalam Sinhala Thai Lao Tibetan Myanmar
	Georgian HangulJamo Ethiopic Cherokee UnifiedCanadianAboriginalSyllabics Ogham Runic Khmer Mongolian
	LatinExtendedAdditional GreekExtended GeneralPunctuation SuperscriptsandSubscripts CurrencySymbols
	CombiningMarksforSymbols LetterlikeSymbols NumberForms Arrows MathematicalOperators MiscellaneousTechnical
	ControlPictures OpticalCharacterRecognition EnclosedAlphanumerics BoxDrawing BlockElements GeometricShapes
	MiscellaneousSymbols Dingbats BraillePatterns CJKRadicalsSupplement KangxiRadicals
	IdeographicDescriptionCharacters CJKSymbolsandPunctuation Hiragana Katakana Bopomofo HangulCompatibilityJamo
	Kanbun BopomofoExtended EnclosedCJKLettersandMonths CJKCompatibility CJKUnifiedIdeographsExtensionA
	CJKUnifiedIdeographs YiSyllables YiRadicals HangulSyllables HighSurrogates HighPrivateUseSurrogates
	LowSurrogates PrivateUse CJKCompatibilityIdeographs AlphabeticPresentationForms ArabicPresentationForms-A
	CombiningHalfMarks CJKCompatibilityForms SmallFormVariants ArabicPresentationForms-B Specials
	HalfwidthandFullwidthForms OldItalic Gothic Deseret ByzantineMusicalSymbols MusicalSymbols
	MathematicalAlphanumericSymbols CJKUnifiedIdeographsExtensionB CJKCompatibilityIdeographsSupplement Tags`) {
		if _, ok := block(n); !ok {
			t.Errorf("block %q unknown", n)
		}
	}
}

func TestCharSetAlgebra(t *testing.T) {
	a := normalize([]rng{{'a', 'z'}, {'0', '9'}, {'5', 'c'}})
	if len(a) != 1 || a[0] != (rng{'0', 'z'}) {
		t.Fatalf("normalize: %v", a)
	}
	v := normalize([]rng{{'a', 'a'}, {'e', 'e'}})
	s := subtract(span('a', 'f'), v)
	if s.contains('a') || !s.contains('b') || s.contains('e') || !s.contains('f') {
		t.Fatalf("subtract: %v", s)
	}
	if got := intersect(span('a', 'm'), span('h', 'z')); len(got) != 1 || got[0] != (rng{'h', 'm'}) {
		t.Fatalf("intersect: %v", got)
	}
	if got := complement(complement(v)); len(got) != 2 || got[1] != (rng{'e', 'e'}) {
		t.Fatalf("complement: %v", got)
	}
	all := charSet{}
	for _, c := range []string{"L", "M", "N", "P", "S", "Z", "C"} {
		s, _ := category(c)
		all = union(all, s)
	}
	if len(all) != 1 || all[0] != (rng{0, unicode.MaxRune}) {
		t.Fatalf("general categories do not partition the code space: %v", all[:min(len(all), 3)])
	}
	if cn := unassigned(); !cn.contains(0x378) || cn.contains('a') || cn.contains(0xE000) {
		t.Fatalf("Cn wrong")
	}
	if got := complement(anyChar()); len(got) != 0 {
		t.Fatalf("complement(all): %v", got)
	}
}

func FuzzCompile(f *testing.F) {
	for _, c := range cases {
		f.Add(c.pattern, strings.Join(c.match, ""))
	}
	for _, p := range badSyntax {
		f.Add(p, "a")
	}
	f.Fuzz(func(t *testing.T, pat, s string) {
		p, err := Compile(pat)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("Compile(%q): non-typed error %v", pat, err)
			}
			return
		}
		p.Match(s)
	})
}
