// SPDX-License-Identifier: BSD-3-Clause

package lyxp

import (
	"bufio"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// seedExprs are XPath expressions of the xpath package's libyang-checked cases (a few hundred
// KiB of them, capped) plus the path cases of this package.
func seedExprs(f *testing.F) {
	for _, s := range []string{"../a/b", "/a:x/b[k=current()/../../c/d][j=current()/../e]", "deref(../x)/../y",
		"/m:a[k='1'][k='2']", "/m:a[.='x']", "/m:a[4294967297]", "//m:x", "/m:a/n:b/b", "a | b", "count(//*) > 1",
		"/a[b='\"']", "ns:*", "text()", "a[1][2]", "1.5e3", "'unterminated", "$v", "a::b", "..//..", "\x00"} {
		f.Add(s, uint8(0))
		f.Add(s, uint8(0x7f))
	}
	fh, err := os.Open("../xpath/testdata/cases.jsonl")
	if err != nil {
		return
	}
	defer fh.Close() //nolint:errcheck // read-only seed file
	sc := bufio.NewScanner(fh)
	sc.Buffer(nil, 1<<20)
	for n := 0; sc.Scan() && n < 400; n++ {
		var c struct{ X string }
		if json.Unmarshal(sc.Bytes(), &c) == nil && c.X != "" {
			f.Add(c.X, uint8(n))
		}
	}
}

// checkTokens asserts what every token stream must satisfy: parallel slices, tokens inside the
// source, non-empty, in order and not overlapping.
func checkTokens(t *testing.T, e *Expr) {
	t.Helper()
	if len(e.Pos) != len(e.Toks) || len(e.Len) != len(e.Toks) {
		t.Fatalf("slices of %q differ: %d tokens, %d pos, %d len", e.Src, len(e.Toks), len(e.Pos), len(e.Len))
	}
	end := 0
	for i := range e.Toks {
		if e.Len[i] <= 0 || e.Pos[i] < end || e.Pos[i]+e.Len[i] > len(e.Src) {
			t.Fatalf("token %d (%s) of %q at %d+%d, previous end %d", i, e.Toks[i], e.Src, e.Pos[i], e.Len[i], end)
		}
		end = e.Pos[i] + e.Len[i]
	}
}

func sameTokens(a, b *Expr) bool {
	return slices.Equal(a.Toks, b.Toks) && slices.Equal(a.Pos, b.Pos) && slices.Equal(a.Len, b.Len)
}

// FuzzLex: Lex never panics; an error has a message and no tokens; the tokens are well-formed, a
// second Lex of the cut source gives the same stream, and LexMax with a token cap agrees with Lex
// (too many tokens, or the same result).
func FuzzLex(f *testing.F) {
	seedExprs(f)
	f.Fuzz(func(t *testing.T, src string, limit uint8) {
		e, msg := Lex(src)
		if (e == nil) == (msg == "") {
			t.Fatalf("Lex(%q): expression %v with message %q", src, e != nil, msg)
		}
		if e == nil {
			if _, m2, tooMany := LexMax(src, int(limit)); m2 == "" && !tooMany {
				t.Fatalf("LexMax(%q, %d) accepted what Lex rejects: %q", src, limit, msg)
			}
			return
		}
		checkTokens(t, e)
		if e2, m2 := Lex(e.Src); m2 != "" || !sameTokens(e, e2) {
			t.Fatalf("re-lexing %q: %q", e.Src, m2)
		}
		e3, m3, tooMany := LexMax(src, int(limit)+1)
		switch {
		case tooMany != (len(e.Toks) > int(limit)+1):
			t.Fatalf("LexMax(%q, %d): tooMany %v with %d tokens", src, int(limit)+1, tooMany, len(e.Toks))
		case !tooMany && (m3 != "" || !sameTokens(e, e3)):
			t.Fatalf("LexMax(%q, %d) differs from Lex: %q", src, int(limit)+1, m3)
		}
	})
}

// FuzzParsePath: ParsePath over every option combination (sel picks Begin, Prefix, Pred, Leafref
// and Extended) never panics; an error has a message, an accepted path has well-formed tokens that
// Lex reproduces exactly from the (NUL-cut) source; an absolute path starts with '/'.
func FuzzParsePath(f *testing.F) {
	seedExprs(f)
	f.Fuzz(func(t *testing.T, src string, sel uint8) {
		o := Opts{Begin: Begin(sel & 1), Prefix: Prefix(sel >> 1 & 3), Pred: Pred(sel >> 3 % 3),
			Leafref: sel&32 != 0, Extended: sel&64 != 0}
		e, msg := ParsePath(src, o)
		if (e == nil) == (msg == "") {
			t.Fatalf("ParsePath(%q, %+v): expression %v with message %q", src, o, e != nil, msg)
		}
		if e == nil {
			return
		}
		checkTokens(t, e)
		if i := strings.IndexByte(src, 0); i >= 0 {
			src = src[:i]
		}
		if e.Src != src {
			t.Fatalf("ParsePath(%q): source %q", src, e.Src)
		}
		l, m := Lex(e.Src)
		if m != "" || !sameTokens(e, l) {
			t.Fatalf("accepted path %q does not re-tokenize to the same tokens (%q)", e.Src, m)
		}
		if len(e.Toks) == 0 {
			t.Fatalf("accepted path %q has no tokens", e.Src)
		}
		if o.Begin == BeginAbsolute && e.Toks[0] != TokOperPath {
			t.Fatalf("absolute path %q starts with %s", e.Src, e.Toks[0])
		}
	})
}
