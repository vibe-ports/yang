//go:build oracle

// Differential test against libyang (yanglint, PCRE2 backend):
//
//	go test -tags oracle -run Oracle -v ./internal/xsdre/
//
// Authoritative run: ./dev go test -tags oracle ./internal/xsdre/ (pinned libyang + pcre2).
// YANGLINT overrides the binary (default: yanglint from PATH).
// Disagreements are reported, not failed: libyang rewrites XSD into PCRE2
// syntax textually, so it inherits Perl semantics for several constructs
// (see docs/decisions/0003-xsd-regex.md). The test fails only if the harness
// itself breaks.
package xsdre

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

var probes = []string{"", "a", "Z", "_", "-", "0", "٣", " ", "\t", "\n", "\r", "\v", " ", "é", "ж", "α",
	"ἀ", "€", "+", ".", ":", "{", "}", "[", "]", "^", "$", "͸", "", "😀", "𝐀", "·", "²", "ab", "aa", "xml"}

func yanglint() string {
	if p := os.Getenv("YANGLINT"); p != "" {
		return p
	}
	if p, err := exec.LookPath("yanglint"); err == nil {
		return p
	}
	return "yanglint"
}

func yangQuote(s string) string { // single-quoted YANG string, "'" via concatenation
	return "'" + strings.ReplaceAll(s, "'", `' + "'" + '`) + "'"
}

// oracleSchema writes a module with one pattern-restricted leaf; ok = libyang accepts it.
func oracleSchema(dir, pattern string) (ok bool, msg string) {
	mod := fmt.Sprintf("module x { yang-version 1.1; namespace \"urn:x\"; prefix x;\n leaf l { type string { pattern %s; } } }\n", yangQuote(pattern))
	if err := os.WriteFile(filepath.Join(dir, "x.yang"), []byte(mod), 0o644); err != nil {
		panic(err)
	}
	out, err := exec.Command(yanglint(), filepath.Join(dir, "x.yang")).CombinedOutput()
	return err == nil, strings.TrimSpace(string(out))
}

func oracleMatch(dir string, i int, s string) bool {
	b, _ := json.Marshal(map[string]string{"x:l": s})
	f := filepath.Join(dir, fmt.Sprintf("d%d.json", i))
	if err := os.WriteFile(f, b, 0o644); err != nil {
		panic(err)
	}
	return exec.Command(yanglint(), "-t", "config", filepath.Join(dir, "x.yang"), f).Run() == nil
}

func candidates(c tc, r *rand.Rand) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		// libyang's data parsers accept only XML 1.0 Chars (no NUL, no \v, ...).
		if !seen[s] && utf8.ValidString(s) && strings.IndexFunc(s, notXMLChar) < 0 {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range append(append(append([]string{}, c.match...), c.nomatch...), probes...) {
		add(s)
	}
	alpha := []rune(c.pattern + "az09.:-_ é😀\n")
	for range 12 {
		n := r.IntN(8)
		var b strings.Builder
		for range n {
			b.WriteRune(alpha[r.IntN(len(alpha))])
		}
		add(b.String())
	}
	return out
}

func TestOracle(t *testing.T) {
	if _, err := os.Stat(yanglint()); err != nil {
		if os.Getenv("YANG_ORACLE_REQUIRED") != "" {
			t.Fatal("yanglint not found:", err)
		}
		t.Skip("yanglint not found:", err)
	}
	type diff struct{ name, pattern, input, ours, theirs string }
	var (
		mu                      sync.Mutex
		diffs                   []diff
		total, agree            int
		schemaTotal, schemaAgre int
		ietfTotal, ietfAgree    int
	)
	record := func(d diff, same bool, schema bool) {
		mu.Lock()
		defer mu.Unlock()
		if schema {
			schemaTotal++
			if same {
				schemaAgre++
			}
		} else {
			total++
			if same {
				agree++
			}
			if ietf := strings.HasPrefix(d.name, "rfc") || strings.HasPrefix(d.name, "6991") || strings.HasPrefix(d.name, "9911"); ietf {
				ietfTotal++
				if same {
					ietfAgree++
				}
			}
		}
		if !same {
			diffs = append(diffs, d)
		}
	}

	var all []tc
	all = append(all, cases...)
	for _, p := range badSyntax {
		all = append(all, tc{name: "bad", pattern: p})
	}
	for _, p := range unsupported {
		all = append(all, tc{name: "unsupported", pattern: p})
	}

	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	r := rand.New(rand.NewPCG(1, 2))
	for _, c := range all {
		cands := candidates(c, r)
		if !utf8.ValidString(c.pattern) || len(c.pattern) > 2000 {
			continue // cannot be put in a YANG module / too deep for a useful probe
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			dir := t.TempDir()
			p, err := Compile(c.pattern)
			lyOK, lyMsg := oracleSchema(dir, c.pattern)
			ours := "accept"
			if err != nil {
				ours = "reject: " + err.Error()
			}
			theirs := "accept"
			if !lyOK {
				theirs = "reject: " + lyMsg
			}
			record(diff{c.name, c.pattern, "<schema>", ours, theirs}, (err == nil) == lyOK, true)
			if err != nil || !lyOK {
				return
			}
			for i, s := range cands {
				o, l := p.Match(s), oracleMatch(dir, i, s)
				record(diff{c.name, c.pattern, s, fmt.Sprint(o), fmt.Sprint(l)}, o == l, false)
			}
		}()
	}
	wg.Wait()

	sort.Slice(diffs, func(i, j int) bool {
		if diffs[i].pattern != diffs[j].pattern {
			return diffs[i].pattern < diffs[j].pattern
		}
		return diffs[i].input < diffs[j].input
	})
	for _, d := range diffs {
		t.Logf("DIFF %-24s %-40q input=%-12q xsdre=%s libyang=%s", d.name, d.pattern, d.input, d.ours, firstLine(d.theirs))
	}
	// Every disagreement must be a registered libyang deviation (conformance/deviations.md).
	unexplained := map[string]bool{}
	for _, d := range diffs {
		if knownDeviation(d.name, d.pattern) == "" {
			unexplained[d.pattern] = true
		}
	}
	for p := range unexplained {
		t.Errorf("unregistered disagreement with libyang for pattern %q: fix xsdre or add it to knownDeviations with a D-id", p)
	}
	t.Logf("schema verdicts: %d/%d agree", schemaAgre, schemaTotal)
	t.Logf("match verdicts:  %d/%d agree (%d disagree)", agree, total, total-agree)
	t.Logf("  of which RFC 7950/6991/9911 patterns: %d/%d agree", ietfAgree, ietfTotal)
	if total == 0 {
		t.Fatal("no candidates evaluated")
	}
}

func notXMLChar(r rune) bool {
	return !(r == '\t' || r == '\n' || r == '\r' || r >= 0x20 && r <= 0xD7FF || r >= 0xE000 && r <= 0xFFFD || r >= 0x10000)
}

func firstLine(s string) string { l, _, _ := strings.Cut(s, "\n"); return l }

// knownDeviation returns the conformance/deviations.md id that explains a disagreement between
// xsdre and libyang on this pattern, or "" if none does. Kept by pattern (not by input) so that a
// new, unrelated disagreement on a listed pattern still needs review when its class changes.
func knownDeviation(name, pattern string) string {
	if name == "unsupported" {
		return "U-0001"
	}
	if id, ok := knownDeviations[pattern]; ok {
		return id
	}
	if name == "bad" {
		return "D-0008" // invalid XSD that PCRE accepts
	}
	return ""
}

var knownDeviations = map[string]string{
	// D-0002 class subtraction
	"[\\p{IsLatin-1Supplement}-[\\p{Ll}]]": "D-0002",
	"[\\w-[\\d]]+":                         "D-0002",
	"[^a-z-[0-9]]":                         "D-0002",
	"[a-z-[aeiou-[e]]]+":                   "D-0002",
	"[a-z-[aeiou]]+":                       "D-0002",
	"a[a-[a]]?":                            "D-0002",
	// D-0003 Perl \w \s \d
	"[^\\d\\s]": "D-0003",
	"\\S":       "D-0003",
	"\\s+":      "D-0003",
	"\\w":       "D-0003",
	"\\W":       "D-0003",
	// D-0004 '.' matches \r
	".": "D-0004",
	// D-0005 \C, \i/\c/\I, \P{IsX}, non-BMP blocks
	"\\C":                                    "D-0005",
	"\\I":                                    "D-0005",
	"\\i\\c*":                                "D-0005",
	"\\P{IsBasicLatin}":                      "D-0005",
	"\\p{IsMathematicalAlphanumericSymbols}": "D-0005",
	"\\p{IsPrivateUse}":                      "D-0005",
	"a\\p{IsHighSurrogates}?":                "D-0005",
	// D-0006 block names matched by prefix
	"\\p{IsGreekExtended}": "D-0006",
	// D-0007 escaped ^ never matches
	"\\n\\r\\t\\\\\\|\\.\\?\\*\\+\\(\\)\\{\\}\\-\\[\\]\\^": "D-0007",
}
