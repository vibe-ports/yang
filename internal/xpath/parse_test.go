// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"bufio"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestSyntaxErrors: test_invalid of libyang test_xpath.c plus lexer/parser messages.
func TestSyntaxErrors(t *testing.T) {
	for x, msg := range map[string]string{
		"/a:foo2[.=]":  `Unexpected XPath token "]" ("]").`,
		"/a:":          `Invalid character 'a'[2] of expression '/a:'.`,
		"":             `Unexpected XPath expression end.`,
		" \x00":        `Invalid character '`,
		"$p:var":       `Variable with prefix is not supported.`,
		"'abc":         `Unterminated string delimited with ' ('abc).`,
		"node()x":      `Invalid character 'x'[7] of expression 'node()x'.`,
		"count(1, 2)":  `Invalid number of arguments (2) for the XPath function count.`,
		"foo()":        `Unknown XPath function "foo".`,
		"l[1":          `Unexpected XPath expression end.`,
		"child::1":     `Invalid character '1'[8] of expression 'child::1'.`,
		"@1":           `Unexpected XPath token "Number" ("1").`,
		"node(1)":      `Unexpected XPath token "Number" ("1)"), expected ")".`,
		"1 2":          `Unparsed characters "2" left at the end of an XPath expression.`,
		"abc def":      `Invalid character 0x64 ('d'), perhaps "abc" is supposed to be a function call.`,
		"a:b c":        `Invalid character 'c'[5] of expression 'a:b c'.`,
		"text x":       `Invalid character 0x78 ('x'), perhaps "text" is supposed to be a function call.`,
		"nope::x":      `Invalid character 'n'[1] of expression 'nope::x'.`,
		"namespace::x": `Invalid character 'n'[1] of expression 'namespace::x'.`,
		"l\x01":        `Invalid character '` + "\x01" + `'[2] of expression 'l` + "\x01" + `'.`,
		strings.Repeat("(", 101) + "1" + strings.Repeat(")", 101): `The maximum nesting of expressions has been exceeded.`,
	} {
		_, err := Compile(x, nil)
		var xe *Error
		if !errors.As(err, &xe) || xe.Msg != msg || xe.VECode != "LYVE_XPATH" {
			t.Errorf("%q: want %q, got %v", x, msg, err)
		}
	}
	if _, err := Compile(strings.Repeat("(", 99)+"1"+strings.Repeat(")", 99), nil); err != nil {
		t.Errorf("depth 99: %v", err)
	}
}

// parsePhase reports whether a libyang message comes from lyxp_expr_parse.
func parsePhase(msg string) bool {
	for _, p := range []string{"Unexpected XPath", "Invalid character", "Unterminated string", "Unknown XPath function",
		"Invalid number of arguments", "Unparsed characters", "The maximum nesting", "Variable with prefix"} {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}

// replayCompile: an expression compiles exactly when libyang's parser accepted
// it, and a rejection carries libyang's message.
func replayCompile(t *testing.T, cases []oracleCase) {
	for _, c := range cases {
		if len(c.Vars) > 0 && strings.Contains(c.X, "$") {
			continue // a parse error may come from a variable's value, found only by evaluation
		}
		var want struct{ Err, Vecode, Msg string }
		if c.Error != nil {
			if err := json.Unmarshal(c.Error, &want); err != nil {
				t.Fatal(err)
			}
		}
		_, err := Compile(c.X, nil)
		var xe *Error
		switch {
		case parsePhase(want.Msg):
			if !errors.As(err, &xe) || xe.Msg != want.Msg || xe.VECode != want.Vecode || xe.Err != want.Err {
				t.Errorf("%q: libyang %s %s %q, we %v", c.X, want.Err, want.Vecode, want.Msg, err)
			}
		case err != nil:
			t.Errorf("%q: libyang parses it, we %v", c.X, err)
		}
	}
}

func TestCompileOracle(t *testing.T) {
	cases := readCases(t, "oracle-pv2.jsonl")
	if len(cases) < 500 {
		t.Fatalf("only %d oracle cases", len(cases))
	}
	replay(t, cases)
}

// TestLongChains: operator chains are lists, so a 1M-operator expression
// compiles without deep recursion; the token cap bounds the AST.
func TestLongChains(t *testing.T) {
	src := "1" + strings.Repeat(" + 1", 1_000_000)
	if _, err := Compile(src, nil); err != nil {
		t.Fatal(err)
	}
	_, err := Compile("1"+strings.Repeat("+1", MaxTokens/2), nil)
	var xe *Error
	if !errors.As(err, &xe) || !strings.Contains(xe.Msg, "tokens") {
		t.Fatalf("token cap: %v", err)
	}
}

func FuzzParse(f *testing.F) {
	sc := bufio.NewScanner(strings.NewReader(fuzzSeeds))
	for sc.Scan() {
		f.Add(sc.Text())
	}
	f.Fuzz(func(t *testing.T, src string) {
		if e, err := Compile(src, nil); err == nil && e.String() == "" {
			t.Fatal("empty expression compiled")
		}
	})
}

const fuzzSeeds = `//*
count(l[k = current()/ref]) + 1 div 3
l[last()]/k/preceding::*[1]
derived-from-or-self(id, 'pv2:one') and bit-is-set(flags, 'x')
substring(normalize-space(string(.)), 2, 3)
(l | ll)[position() > 1] = 'b'
-ll mod 2
re-match(x, '[a-z]+')
deref(ref)/../k
name(..) | text()`
