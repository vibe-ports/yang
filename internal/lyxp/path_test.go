// SPDX-License-Identifier: BSD-3-Clause

package lyxp

import "testing"

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
		{"", instID, `XPath "" was expected to be absolute.`},
	} {
		if _, msg := ParsePath(c.src, c.o); msg != c.msg {
			t.Errorf("%q: got %q, want %q", c.src, msg, c.msg)
		}
	}
}
