// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"strconv"
	"strings"
	"testing"
)

// argSchema is top { s string; n uint8; ll leaf-list string; c container } with the leaves w1..wN
// whose when is evaluated from the leaf itself (so paths start with ../), as the fixtures.
func argSchema(mod string, wn int) (top *tschema, ws []*tschema) {
	top = newTop(mod)
	top.leaf("s", bt(TypeString))
	top.leaf("n", bt(TypeUint8))
	top.add(&tschema{kind: KindLeafList, mod: mod, name: "ll", config: true, typ: bt(TypeString)})
	top.add(&tschema{kind: KindContainer, mod: mod, name: "c", config: true}).leaf("x", bt(TypeString))
	for i := 1; i <= wn; i++ {
		ws = append(ws, top.leaf("w"+strconv.Itoa(i), bt(TypeString)))
	}
	return top, ws
}

// warnsFrom atomizes x with ctx as the context node.
func warnsFrom(t *testing.T, top, ctx *tschema, x string) []string {
	t.Helper()
	e, err := Compile(x, schemaNS{"": top.mod, "munp": top.mod})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	if _, err = e.Atomize(AtomizeContext{Node: ctx, SchemaRules: true, Schema: tinfo{schema: []SchemaNode{top}},
		Warn: func(m string) { out = append(out, m) }}); err != nil {
		t.Fatalf("%s: %v", x, err)
	}
	return out
}

// TestArgWarnings: every function, every checked argument position, with a container, a non-string
// leaf and a string leaf; the quirks.
func TestArgWarnings(t *testing.T) {
	top, ws := argSchema("m", 1)
	ctx := ws[0]
	cont := func(fn string, n int) string {
		return `Argument #` + strconv.Itoa(n) + ` of ` + fn + ` is a container node "c".`
	}
	notStr := func(fn string, n int) string {
		return `Argument #` + strconv.Itoa(n) + ` of ` + fn + ` is node "n", not of string-type.`
	}
	notNum := func(fn string, n int) string {
		return `Argument #` + strconv.Itoa(n) + ` of ` + fn + ` is node "s", not of numeric type.`
	}
	// each function with its string arguments; every other argument is a string leaf
	for _, f := range []struct {
		xp, c string
		args  int
	}{
		{"contains", "xpath_contains", 2}, {"lang", "xpath_lang", 1}, {"re-match", "xpath_re_match", 2},
		{"starts-with", "xpath_starts_with", 2}, {"substring-after", "xpath_substring_after", 2},
		{"substring-before", "xpath_substring_before", 2}, {"translate", "xpath_translate", 3},
		{"normalize-space", "xpath_normalize_space", 1}, {"string-length", "xpath_string_length", 1},
		{"concat", "xpath_concat", 3},
	} {
		for pos := 1; pos <= f.args; pos++ {
			for _, k := range []struct {
				arg  string
				want func(string, int) string
			}{{"../c", cont}, {"../n", notStr}, {"../s", nil}} {
				args := make([]string, f.args)
				for i := range args {
					args[i] = "../s"
				}
				args[pos-1] = k.arg
				var want []string
				if k.want != nil {
					want = []string{k.want(f.c, pos)}
				}
				if got := warnsFrom(t, top, ctx, f.xp+"("+strings.Join(args, ", ")+")"); strings.Join(got, "|") != strings.Join(want, "|") {
					t.Errorf("%s arg %d %s: got %q want %q", f.xp, pos, k.arg, got, want)
				}
			}
		}
	}
	// substring: string, then two numbers (the third optional)
	for _, c := range []struct {
		x    string
		want []string
	}{
		{"substring(../c, 1)", []string{cont("xpath_substring", 1)}},
		{"substring(../n, 1)", []string{notStr("xpath_substring", 1)}},
		{"substring(../s, ../c)", []string{cont("xpath_substring", 2)}},
		{"substring(../s, ../s)", []string{notNum("xpath_substring", 2)}},
		{"substring(../s, ../n)", nil},
		{"substring(../s, 1, ../c)", []string{cont("xpath_substring", 3)}},
		{"substring(../s, 1, ../s)", []string{notNum("xpath_substring", 3)}},
		{"substring(../s, 1, ../n)", nil},
		{"substring(../c, ../c, ../s)", []string{cont("xpath_substring", 1), cont("xpath_substring", 2), notNum("xpath_substring", 3)}},
		// libyang quirks
		{"string-length()", nil}, // the context node is not in context at the start
		{"../n[string-length() > 1]", []string{notStr("xpath_string_length", 0)}},
		{"../c[string-length() > 1]", []string{cont("xpath_string_length", 0)}},
		{"../s[string-length() > 1]", nil},
		{"normalize-space()", nil},
		{"../n[normalize-space() = '']", nil},                                                                  // checked only when an argument is given
		{"concat(../s, ../n, ../c, ../ll, 'x')", []string{notStr("xpath_concat", 2), cont("xpath_concat", 3)}}, // 1-based, every argument
		{"concat('a', 'b')", nil},
		{"contains('a', ../s)", nil},
		{"contains(count(../c), ../s)", nil}, // a function result is no node in context
	} {
		if got := warnsFrom(t, top, ctx, c.x); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%q: got %q want %q", c.x, got, c.want)
		}
	}
}
