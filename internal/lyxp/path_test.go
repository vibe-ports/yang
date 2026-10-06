// SPDX-License-Identifier: BSD-3-Clause

package lyxp

import (
	"fmt"
	"strings"
	"testing"
)

var (
	lref   = Opts{BeginEither, PrefixOptional, PredLeafref, true, false}
	lrefX  = Opts{BeginEither, PrefixOptional, PredLeafref, true, true}
	instID = Opts{BeginAbsolute, PrefixStrictInherit, PredSimple, false, false}
	instMd = Opts{BeginAbsolute, PrefixMandatory, PredSimple, false, false}
)

// Messages of the oracle-checked parser cases (parser/build_test.go) are repeated there;
// these cover the other option combinations.
func TestParsePath(t *testing.T) {
	for _, c := range []struct {
		src string
		o   Opts
		msg string
	}{
		{"../a/b", lref, ""},
		{"/a:x/b[k=current()/../../c/d][j=current()/../e]", lref, ""},
		{"/x", lref, ""},
		{"x", Opts{BeginEither, PrefixOptional, PredLeafref, false, false}, ""}, // not lref: relative needs no '..'
		// libyang compares the token after the name with "deref", so a deref() is accepted
		{"deref(../x)/../y", lrefX, ""},
		{"deref(../x)/../y", lref, `Unexpected XPath token "FunctionName" ("deref(../x)/../"), expected "..".`},
		{"deref(foo(.))/../y", lrefX, "Embedded function XPath function inside deref function within the path is not allowed"},
		{"/m:a/m:b", instID, `Duplicate prefix for "m:b" in path.`},
		{"/m:a/n:b/b", instID, ""},
		{"/a", instID, `Prefix missing for "a" in path.`},
		{"/a", instMd, `Prefix missing for "a" in path.`},
		{"/m:a[m:k='1']", instID, `Redundant prefix for "m:k" in path.`},
		{"/m:a[k='1'][k='2']", instID, `Duplicate predicate key "k" in path.`},
		{"/m:a[.='x']", instID, ""},
		{"/m:a[2]", instID, ""},
		{"/m:a[0]", instID, `Invalid positional predicate "0".`},
		{"/m:a[.=]", instID, `Unexpected XPath token "]" ("]").`},
		{"m:a", instID, `XPath "m:a" was expected to be absolute.`},
		{"//m:x", instID, `Unexpected XPath token "Operator(Recursive Path)" ("//m:x"), expected "Operator(Path)".`},
		{"/m:a[1]", instID, ""},
		{"/m:a[4294967296]", instID, `Invalid positional predicate "4294967296".`}, // (int)strtol wraps to 0
		{"/m:a[4294967297]", instID, ""},
		{"", instID, `XPath "" was expected to be absolute.`},
	} {
		if _, msg := ParsePath(c.src, c.o); msg != c.msg {
			t.Errorf("%q: got %q, want %q", c.src, msg, c.msg)
		}
	}
}

func TestTrunc15(t *testing.T) {
	if got := Trunc15("\u00e9\u00e9\u00e9\u00e9\u00e9\u00e9\u00e9\u00e9\u00e9"); len(got) != 15 {
		t.Errorf("len %d, want 15 bytes", len(got))
	}
}

// 100k predicate keys must not take quadratic work (the duplicate-key check looks each key up
// once, not against every previous key). Counted, not timed: a wall-clock bound flakes on slow
// CI machines (#183).
func TestManyKeys(t *testing.T) {
	const n = 100000
	var b strings.Builder
	b.WriteString("/m:a")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "[k%d='1']", i)
	}
	e, msg := ParsePath(b.String(), instID)
	if msg != "" {
		t.Fatal(msg)
	}
	if limit := 2 * n; e.work > limit {
		t.Errorf("%d key lookups for %d keys, limit %d", e.work, n, limit)
	}
}

func TestLexMax(t *testing.T) {
	if e, msg, over := LexMax("a|b|c", 5); e == nil || msg != "" || over {
		t.Fatal(e, msg, over)
	}
	if _, msg, over := LexMax("a|b|c", 4); !over || msg != "" {
		t.Fatalf("over=%v msg=%q", over, msg)
	}
	for src, want := range map[string]string{
		"namespace::x": "Invalid character 'n'[1] of expression 'namespace::x'.",
		"\xff":         "Invalid character '\xff'[1] of expression '\xff'.",
		"child::":      "Invalid character '",
	} {
		if _, msg, _ := LexMax(src, 0); msg != want {
			t.Errorf("%q: %q, want %q", src, msg, want)
		}
	}
}
