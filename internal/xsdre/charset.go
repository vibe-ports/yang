package xsdre

import (
	"slices"
	"unicode"
)

// rng is an inclusive code point range.
type rng struct{ lo, hi rune }

// charSet is a sorted, non-overlapping, non-adjacent list of ranges over
// [0, unicode.MaxRune]. All operations return new normalized sets.
type charSet []rng

func single(r rune) charSet    { return charSet{{r, r}} }
func span(lo, hi rune) charSet { return charSet{{lo, hi}} }
func anyChar() charSet         { return span(0, unicode.MaxRune) }
func (a charSet) contains(r rune) bool {
	_, ok := slices.BinarySearchFunc(a, r, func(x rng, r rune) int {
		switch {
		case x.hi < r:
			return -1
		case x.lo > r:
			return 1
		}
		return 0
	})
	return ok
}

func normalize(s []rng) charSet {
	slices.SortFunc(s, func(a, b rng) int { return int(a.lo - b.lo) })
	out := charSet{}
	for _, r := range s {
		if n := len(out); n > 0 && r.lo <= out[n-1].hi+1 {
			out[n-1].hi = max(out[n-1].hi, r.hi)
			continue
		}
		out = append(out, r)
	}
	return out
}

func union(a, b charSet) charSet { return normalize(append(slices.Clone(a), b...)) }

func complement(a charSet) charSet {
	out := charSet{}
	next := rune(0)
	for _, r := range a {
		if r.lo > next {
			out = append(out, rng{next, r.lo - 1})
		}
		next = r.hi + 1
	}
	if next <= unicode.MaxRune {
		out = append(out, rng{next, unicode.MaxRune})
	}
	return out
}

func intersect(a, b charSet) charSet { return complement(union(complement(a), complement(b))) }
func subtract(a, b charSet) charSet  { return intersect(a, complement(b)) }

func fromTable(t *unicode.RangeTable) charSet {
	var s []rng
	for _, r := range t.R16 {
		for c := rune(r.Lo); c <= rune(r.Hi); c += rune(r.Stride) {
			if r.Stride == 1 {
				s = append(s, rng{c, rune(r.Hi)})
				break
			}
			s = append(s, rng{c, c})
		}
	}
	for _, r := range t.R32 {
		for c := rune(r.Lo); c <= rune(r.Hi); c += rune(r.Stride) { //nolint:gosec // unicode.Range32 bounds are <= unicode.MaxRune
			if r.Stride == 1 {
				s = append(s, rng{c, rune(r.Hi)}) //nolint:gosec // unicode.Range32 bounds are <= unicode.MaxRune
				break
			}
			s = append(s, rng{c, c})
		}
	}
	return normalize(s)
}

// category returns the set for a Unicode general category name as allowed by
// XSD Part 2 §F.1.1 (plus Cs). Go's unicode package has no Cn table; its "C"
// table already includes unassigned code points, so Cn = C - Cc - Cf - Co - Cs.
func category(name string) (charSet, bool) {
	if name == "Cn" {
		return unassigned(), true
	}
	if len(name) > 2 || name == "LC" {
		return nil, false
	}
	t, ok := unicode.Categories[name]
	if !ok {
		return nil, false
	}
	return fromTable(t), true
}

func unassigned() charSet {
	s := fromTable(unicode.C)
	for _, t := range []*unicode.RangeTable{unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs} {
		s = subtract(s, fromTable(t))
	}
	return s
}

// block resolves an XSD block escape name (after "Is").
func block(name string) (charSet, bool) {
	// libyang's table (ly_common.c ublock2urange) gives these two blocks exact ranges that differ
	// from Unicode 15: Specials is the XSD 1.0 FEFF plus FFF0-FFFD, written there as the Perl class
	// [\x{FEFF}|\x{FFF0}-\x{FFFD}] whose '|' is a literal member (reproduced on purpose), and
	// ArabicPresentationForms-B stops before FEFF.
	switch name {
	case "Specials":
		return normalize([]rng{{0xFEFF, 0xFEFF}, {'|', '|'}, {0xFFF0, 0xFFFD}}), true
	case "ArabicPresentationForms-B":
		return span(0xFE70, 0xFEFE), true
	}
	if r, ok := blocks[name]; ok {
		return span(r[0], r[1]), true
	}
	// XSD 1.0 (Unicode 3.1) names that Unicode later renamed or merged.
	switch name {
	case "Greek":
		return block("GreekandCoptic")
	case "CombiningMarksforSymbols":
		return block("CombiningDiacriticalMarksforSymbols")
	case "PrivateUse":
		a, _ := block("PrivateUseArea")
		b, _ := block("SupplementaryPrivateUseArea-A")
		c, _ := block("SupplementaryPrivateUseArea-B")
		return union(union(a, b), c), true
	}
	return nil, false
}

// XML 1.0 Fifth Edition, productions [4] NameStartChar and [4a] NameChar.
var nameStartChar = normalize([]rng{
	{':', ':'}, {'A', 'Z'}, {'_', '_'}, {'a', 'z'}, {0xC0, 0xD6}, {0xD8, 0xF6},
	{0xF8, 0x2FF}, {0x370, 0x37D}, {0x37F, 0x1FFF}, {0x200C, 0x200D},
	{0x2070, 0x218F}, {0x2C00, 0x2FEF}, {0x3001, 0xD7FF}, {0xF900, 0xFDCF},
	{0xFDF0, 0xFFFD}, {0x10000, 0xEFFFF},
})

var nameChar = union(nameStartChar, normalize([]rng{
	{'-', '-'}, {'.', '.'}, {'0', '9'}, {0xB7, 0xB7}, {0x300, 0x36F}, {0x203F, 0x2040},
}))

// multiChar returns the XSD Part 2 §F.1.1 multi-character escape set for
// \s \i \c \d \w (lower case); upper case is the complement.
func multiChar(c rune) (charSet, bool) {
	var s charSet
	switch unicode.ToLower(c) {
	case 's':
		s = normalize([]rng{{' ', ' '}, {'\t', '\t'}, {'\n', '\n'}, {'\r', '\r'}})
	case 'i':
		s = nameStartChar
	case 'c':
		s = nameChar
	case 'd':
		s = fromTable(unicode.Nd)
	case 'w':
		p, _ := category("P")
		z, _ := category("Z")
		cc, _ := category("C")
		s = complement(union(union(p, z), cc))
	default:
		return nil, false
	}
	if unicode.IsUpper(c) {
		s = complement(s)
	}
	return s, true
}
