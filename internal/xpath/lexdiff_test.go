// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"bufio"
	"math/rand"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/lyxp"
)

// TestLexMatchesLyxp requires internal/lyxp's tokenizer and this package's own lex to agree on
// token kinds, positions, lengths and error text over the oracle corpus, the fuzz seeds and
// deterministic mutations of them (issue #73: run before lex.go is deleted).
func TestLexMatchesLyxp(t *testing.T) {
	var corpus []string
	for _, f := range []string{"cases.jsonl", "oracle-pv2.jsonl"} {
		for _, c := range readCases(t, f) {
			corpus = append(corpus, c.X)
		}
	}
	sc := bufio.NewScanner(strings.NewReader(fuzzSeeds))
	for sc.Scan() {
		corpus = append(corpus, sc.Text())
	}
	corpus = append(corpus, "", "\x00", "a\x00b", "$", "$a:b", "a::b", "namespace::x", "child::*", "'abc", "node (", "a b",
		"1.", ".5", "..5", "a:", "a:*", "*:a", "\xff", "a\xc3", "\xef\xbf\xbe", "\xf0\x90\x80\x80", "\xc0\x80", "a\x01")
	rng := rand.New(rand.NewSource(1))
	alphabet := []string{"(", ")", "[", "]", ".", "..", "@", ",", "::", "*", "/", "//", "|", "+", "-", "=", "!=", "<", ">=",
		"or", "and", "mod", "div", "node", "text", "comment", "child", "namespace", "ancestor-or-self", "'s'", "\"d\"",
		"12", "3.5", "$v", "a", "p:b", "p:*", "f(", " ", "\t", "\n", "\x00", "\xc3\xa9", "\xff", "\xe2\x81\x90"}
	n := len(corpus)
	for i := 0; i < 20000; i++ {
		var b strings.Builder
		if i%2 == 0 { // mutate a corpus entry
			s := corpus[rng.Intn(n)]
			p := rng.Intn(len(s) + 1)
			b.WriteString(s[:p])
			b.WriteString(alphabet[rng.Intn(len(alphabet))])
			b.WriteString(s[p:])
		} else {
			for j := rng.Intn(8) + 1; j > 0; j-- {
				b.WriteString(alphabet[rng.Intn(len(alphabet))])
			}
		}
		corpus = append(corpus, b.String())
	}
	for _, src := range corpus {
		toks, tsrc, err := lex(src)
		e, msg := lyxp.Lex(src)
		switch {
		case err != nil:
			if msg != err.(*Error).Msg { //nolint:errorlint // test
				t.Fatalf("%q: error %q, lyxp %q", src, err.(*Error).Msg, msg) //nolint:errorlint // test
			}
			continue
		case msg != "":
			t.Fatalf("%q: ok, lyxp error %q", src, msg)
		}
		if e.Src != tsrc || len(e.Toks) != len(toks) {
			t.Fatalf("%q: %d tokens %q, lyxp %d tokens %q", src, len(toks), tsrc, len(e.Toks), e.Src)
		}
		for i, tk := range toks {
			if tokNames[tk.k] != e.Toks[i].String() || tk.pos != e.Pos[i] || tk.len != e.Len[i] {
				t.Fatalf("%q token %d: %s %d+%d, lyxp %s %d+%d", src, i, tokNames[tk.k], tk.pos, tk.len, e.Toks[i], e.Pos[i], e.Len[i])
			}
		}
	}
}
