// SPDX-License-Identifier: BSD-3-Clause

package xsdre

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestCompat pins a few CompileCompat results; TestOracleCompat (build tag oracle) is the proof.
func TestCompat(t *testing.T) {
	for _, c := range compatCases {
		p, err := CompileCompat(c.pattern)
		if err != nil {
			t.Errorf("%q: %v", c.pattern, err)
			continue
		}
		for _, s := range c.match {
			if !p.Match(s) {
				t.Errorf("%q should match %q", c.pattern, s)
			}
		}
		for _, s := range c.nomatch {
			if p.Match(s) {
				t.Errorf("%q should not match %q", c.pattern, s)
			}
		}
	}
	for p, want := range map[string]string{
		`*a`:        `"*a": quantifier does not follow a repeatable item`,
		`a]`:        `"]": character group doesn't begin with '['`,
		`\i`:        `"i": unrecognized character follows \`,
		`[\d-z]`:    `"z]": invalid range in character class`,
		`\p{IsFoo}`: `"Foo}": unknown block name`,
		`\1`:        `"1": reference to non-existent subpattern`,
	} {
		if _, err := CompileCompat(p); err == nil || err.Error() != want || !errors.Is(err, ErrSyntax) {
			t.Errorf("%q: %v, want %s", p, err, want)
		}
	}
	for _, p := range []string{`\b`, `\g<1>`, `\g{name}`, `(?=a)`, `a*+`, `\p{Greek}`, `\Qa\E`, `[[:punct:]]`, `a{1001}`} {
		if _, err := CompileCompat(p); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%q: %v, want ErrUnsupported", p, err)
		}
	}
}

// TestCompatOffsets: Offset is in the YANG pattern, through libyang's rewriting.
func TestCompatOffsets(t *testing.T) {
	for p, want := range map[string]int{
		`*^^`: 0, `^**`: 2, `a]`: 1, `\p{IsFoo}`: 5, `\p{IsBasicLatin}*?+`: 18, `\p{IsGreek}{2,1}`: 15,
		`(?:abcdefghijklmnopqrstuvwxyz0123456789){1000}`: 46,
	} {
		var e *Error
		if _, err := CompileCompat(p); !errors.As(err, &e) || e.Offset != want {
			t.Errorf("%q: %#v, want offset %d", p, err, want)
		}
	}
}

// TestCompatLinear: libyang's block substitution is done in one pass (a quadratic rewrite took
// minutes here); the result exceeds PCRE2's compiled-size limit.
func TestCompatLinear(t *testing.T) {
	p := strings.Repeat(`\p{IsBasicLatin}`, 50000)
	if _, err := CompileCompat(p); err == nil || err.Error() != `"": regular expression is too large` {
		t.Fatal(err)
	}
}

// TestCompatLargeClass: a class's members are merged once, not one by one (quadratic), and an
// escape's set is built once per pattern; the compiled-size check rejects or refuses the result.
func TestCompatLargeClass(t *testing.T) {
	var b strings.Builder
	b.WriteByte('[')
	for i := range 100000 {
		b.WriteRune(0x10000 + 2*rune(i))
	}
	b.WriteByte(']')
	for _, p := range []string{b.String(), "[" + strings.Repeat(`\w`, 100000) + "]", "[" + strings.Repeat(`\p{L}\P{Nd}`, 50000) + "]"} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		_, err := CompileCompat(p)
		d := time.Since(start)
		runtime.ReadMemStats(&after)
		if err == nil {
			t.Errorf("%.20q…: accepted", p)
		}
		if alloc := after.TotalAlloc - before.TotalAlloc; d > 5*time.Second || alloc > 512<<20 {
			t.Errorf("%.20q… (%d bytes): %v, %d bytes allocated", p, len(p), d, alloc)
		}
	}
}

// TestClassExpansionBudget covers the adversarial shape from issue #169 in both compilation
// modes. The budget is charged while parsing each class, before all 40 000 expanded sets can be
// retained in the AST.
func TestClassExpansionBudget(t *testing.T) {
	for _, brackets := range []bool{false, true} {
		part := `\W`
		if brackets {
			part = `[` + part + `]`
		}
		for name, compile := range map[string]func(string) (*Pattern, error){
			"strict": Compile,
			"compat": CompileCompat,
		} {
			t.Run(fmt.Sprintf("%s/brackets=%t", name, brackets), func(t *testing.T) {
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				start := time.Now()
				_, err := compile(strings.Repeat(part, 40000))
				d := time.Since(start)
				runtime.ReadMemStats(&after)
				if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "class expansion") {
					t.Fatalf("Compile: %v, want class-expansion ErrUnsupported", err)
				}
				if alloc := after.TotalAlloc - before.TotalAlloc; d > 5*time.Second || alloc > 128<<20 {
					t.Errorf("%v, %d bytes allocated", d, alloc)
				}
			})
		}
	}
}

func FuzzCompileCompat(f *testing.F) {
	for _, c := range compatCases {
		f.Add(c.pattern, strings.Join(c.match, ""))
	}
	for _, p := range compatBad {
		f.Add(p, "a")
	}
	f.Fuzz(func(t *testing.T, pat, s string) {
		p, err := CompileCompat(pat)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("CompileCompat(%q): non-typed error %v", pat, err)
			}
			return
		}
		p.Match(s)
	})
}

// compatCases are PCRE-only constructs and invalid patterns for CompileCompat: the PCRE2
// pattern libyang compiles, its match semantics and its error messages.
var compatCases = []tc{
	{"hex", `[\x20-\x7F]+`, []string{"a b~", " "}, []string{"é", ""}},
	{"hex", `\x41\x{42}\x{000043}`, []string{"ABC"}, []string{"abc"}},
	{"hex", `[\x{e9}-\x{ff}]`, []string{"é", "ÿ"}, []string{"e", "Ā"}},
	{"caret-in-class", `[a-z\^]+`, []string{"a^b", "^"}, []string{"\\", "A"}},
	{"caret-in-class", `[\^]`, []string{"^"}, []string{"\\", "a"}},
	{"caret-in-class", `[^\^a]`, []string{"b"}, []string{"^", "a"}},
	{"caret", `\^a`, nil, []string{"^a", "\\a", "a"}},
	{"dollar", `a\$`, []string{"a\\"}, []string{"a$", "a"}},
	{"anchors", `^a$`, []string{"^a$"}, []string{"a"}},
	{"anchors", `\Aab\z`, []string{"ab"}, []string{"a"}},
	{"digit", `\d+`, []string{"0", "٣٤", "１"}, []string{"²", "a", "Ⅰ"}},
	{"word", `\w+`, []string{"a_1", "é", "²", "Ⅰ", "a\u0301", "‿"}, []string{"-", " ", "ः", "€"}},
	{"space", `\s`, []string{" ", "\t", "\u00a0", "\u2003", "\u0085", "\u180e", "\u2028"}, []string{"a", "\u200b"}},
	{"types", `[\D][\W][\S]`, []string{"a-a", "--x", "a a"}, []string{"1-a", "a_a"}},
	{"types", `\h\v\H\V\N`, []string{"\t\na a", "\u3000\u2029xyz"}, []string{"aaaaa"}},
	{"types", `[\d\s]+`, []string{"1 2"}, []string{"a"}},
	{"dot", `.`, []string{"\r", "a", "😀"}, []string{"\n", "ab"}},
	{"property", `\p{L}\pN\P{Lu}\P{Nd}`, []string{"a1aa", "ж²ж-"}, []string{"a1Aa", "a1A1"}},
	{"property", `\p{L&}\p{Lc}\p{ l u }\p{Any}`, []string{"aBCx", "ǅǅÀ😀"}, []string{"ʰaAa"}},
	{"property", `\p{Cn}|\p{Co}|\p{Zs}`, []string{"\ue000", " "}, []string{"a"}},
	{"quantifier", `a{,3}`, []string{"", "aaa"}, []string{"aaaa", "a{,3}"}},
	{"quantifier", `a{ 1 , 2 }b{2, }`, []string{"abb", "aabbb"}, []string{"bb"}},
	{"quantifier", `a{`, []string{"a{"}, []string{"a"}},
	{"quantifier", `a{1`, []string{"a{1"}, nil},
	{"quantifier", `a{x}|{|}|a}|a{,}`, []string{"a{x}", "{", "}", "a}", "a{,}"}, []string{"a"}},
	{"quantifier", `(?:ab)+?c|a*?|b??`, []string{"ababc", "", "aa", "b"}, []string{"abab"}},
	{"class", `[[]`, []string{"["}, []string{"]"}},
	{"class", `[--a][a-c-e]`, []string{"--", "a-", "_e"}, []string{"b", "ad"}},
	{"class", `[\w-][-\w]`, []string{"--", "a_"}, []string{"a "}},
	{"class", `[a-z-[aeiou]]`, []string{"b]", "-]", "[]"}, []string{"b", "B]"}},
	{"class", `[[:alpha:]][[:^digit:]][[:alnum:]][[:space:]][[:word:]][[:lower:]][[:upper:]][[:blank:]][[:cntrl:]][[:ascii:]]`,
		[]string{"éa1 _aA\t\u007fz"}, []string{"éa1 _aA\taz"}},
	{"class", `[\b]|[\101\8\k\g]`, []string{"A", "8", "k", "g"}, []string{"b"}},
	{"octal", `\101\12\0181`, nil, []string{"A"}},
	{"block", `\p{IsGreekExtended}[\p{IsBasicLatin}]`, []string{"αa"}, []string{"ἀa"}},
	{"size", `(?:ab){500}`, []string{strings.Repeat("ab", 500)}, []string{"ab"}},
	{"size", `(?:(?:abcdefgh){100}){5}x|(?:[a-z][0-9]){200}`, []string{strings.Repeat("abcdefgh", 500) + "x"}, []string{"x"}},
	{"size", strings.Repeat(`[a-z]{0}`, 1900) + "b", []string{"b"}, []string{"a"}},
	{"size", "(?:" + strings.Repeat("a", 30000) + "){0}c", []string{"c"}, []string{"a"}},
	{"quantifier", `a*\E?b|a\E{2}|a*\E\E?c`, []string{"aab", "b", "aa", "c"}, []string{"a"}},
	{"size", strings.Repeat("a", 32764), []string{strings.Repeat("a", 32764)}, []string{"a"}},
	{"size", "(?:" + strings.Repeat(`[\p{Any}]`, 40) + "){1000}", []string{strings.Repeat("é", 40000)}, []string{"a"}},
	{"class", `[\P{Any}]|a`, []string{"a"}, []string{"b", ""}},
	{"size", strings.Repeat(`\P{Any}`, 1985) + "|a", []string{"a"}, []string{"b"}},
	{"size", `(?:[\p{L}]){600}`, []string{strings.Repeat("ж", 600)}, []string{"a"}},
	{"size", `(?:[\p{Any}x]){1000}`, []string{strings.Repeat("é", 1000)}, []string{"a"}},
	{"size", strings.Repeat(`(?:ab){0}`, 3000) + "c", []string{"c"}, []string{"abc"}},
	{"size", "(?:" + strings.Repeat(`[\d\D][\s\S][^\P{Any}][[:^alpha:][:alpha:]]`, 10) + "){2}", []string{strings.Repeat("x", 80)}, []string{"a"}},
	{"size", "(?:" + strings.Repeat(`[^a][^é][a-a][é]`, 10) + "){300}", []string{strings.Repeat("bbaébbaé", 1500)}, []string{"a"}},
	{"size", `(?:[Aa][Bb]){1000}`, []string{strings.Repeat("aB", 1000)}, []string{"ab"}},
	{"size", `(?:[ÉÉé][-][a-a]){1000}`, []string{strings.Repeat("é-a", 1000)}, []string{"a"}},
	{"size", `[a-z]{1000}x{1000}`, []string{strings.Repeat("a", 1000) + strings.Repeat("x", 1000)}, []string{"a"}},
	{"escape", `\cA\c[\o{101}\o{ 0 }`, []string{"\u0001\u001bA\u0000"}, []string{"cA"}},
	{"escape", `a\E+\Eb|[\Ea][^\Ea]`, []string{"aab", "ab"}, []string{"a\\E", "aa"}},
	{"class", `[\p{^L}][\P{^Lu}][^\p{^N}]`, []string{"1A2"}, []string{"aA2", "1a2", "1Aa"}},
	{"block", `[\p{IsSpecials}]`, []string{"|", "\ufffd"}, []string{"a"}},
}

// compatBad are patterns libyang rejects; CompileCompat must reject them with libyang's message.
var compatBad = []string{
	`*a`, `a**`, `a{2}{3}`, `{1}`, `x|*b`, `(?:*)`, `*?a`, `a*?*`, `^*`, `a)`, `a)b`, `(a`, `(a|`, `(?:a`, `[a`,
	`[^]`, `[]`, `[]a]`, `[z-a]`, `[\x7F-\x20]x`, `[a-\d]x`, `[\d-z]`, `[\pL-z]`, `[a-\pL]`, `[\w-z]`,
	`a{3,2}`, `a{70000}`, `a{70000,}`, `a{1,70000}`, `a{ 3 , 2 }`, `a\`, `\i`, `a\ib`, `[\i]`, `\Fa`, `\ua`, `\L`,
	`[\Fa]`, `[\B]`, `[\R]`, `[\X]`, `[\A]`, `[\z]`, `[\N]`, `\1`, `a\1b`, `\9`, `\12345678`, `\1(`,
	`\p{Foo}`, `\p{Foo}x`, `\p{Fo`, `\p{`, `\p`, `\pq`, `\p{a.b}`, `\p{^^L}`, `\p{^Lu}`, `\p{Lx}`, `\p{foo=bar}`,
	`\P{IsGreek}`, `\p{IsGreek`, `\p{IsFoo}`, `a]`, `\]]`, `\x`, `\xg`, `\x{}`, `\x{zz}`, `\x{41`, `\x{ 41`,
	`\x{110000}`, `\x{d800}`, `[\x]`, `[[:foo:]]`, `[[.a.]]`, `[[=a=]]`, `[:alpha:]`, `[a-[:digit:]]`,
	`[\d-[:digit:]]`, `\i\c`, `\I`,
	strings.Repeat("(", 251) + "a" + strings.Repeat(")", 251),
	`\c`, `a\c`, `\c€`, `[\c]`, `\o`, `a\o`, `\oa`, `\o{`, `\o{}`, `\o{18}`, `\o{101`, `[\o]`, `\o{77777777}`,
	`\o{154000}`, `\g`, `a\g`, `\g1`, `a\g{1}b`, `\g{ 2 }`, `\g{-1}`, `\g+1`, `\g-1`, `\g0`, `\g+0`, `\gx`,
	`\g{1`, `\g{+0}`, `\g99999`, `[\E]`, `[^\E]`, `[\c]]`, `[\Q\E]a]`,
	strings.Repeat("a", 32765), `(?:[Kk][Ss]){1000}`, `(?:[^a][b-b]x){12000}`,
	// PCRE2's 64 KiB compiled-size limit (ERR20): {0} keeps a single item's length and the group
	strings.Repeat(`[a-z]{0}`, 2100), strings.Repeat(`a{0}`, 40000), "(?:" + strings.Repeat("a", 40000) + "){0}",
	// PCRE2's 64 KiB compiled-size limit (ERR20)
	`(?:abcdefghijklmnopqrstuvwxyz0123456789){1000}`, `(?:(?:abcdefgh){100}){40}x`, `(?:[a-z][0-9]){1000}`,
	strings.Repeat(`[\p{IsBasicLatin}]`, 3000),
	// \P{Any} outside a class is an empty 32-byte OP_CLASS
	`(?:\P{Any}\P{Any}){1000}`, strings.Repeat(`\P{Any}`, 1986),
}
