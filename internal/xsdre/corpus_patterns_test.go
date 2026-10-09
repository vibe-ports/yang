// SPDX-License-Identifier: BSD-3-Clause

package xsdre

import (
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	yparser "github.com/vibe-ports/yang/internal/parser"
)

// corpusPatternDeviations are the patterns libyang v5.8.6 accepted in some corpus fixture but
// Compile refuses, each with the deviations.md entry that explains it (ADR 0003 addendum, #40).
var corpusPatternDeviations = map[string]string{
	`\p{IsBasicLatinBogus}`: "D-0006", // libyang's block-name prefix match resolves it to BasicLatin
}

// compatSets are the corpus sets whose fixtures run with context option pattern_compat
// (Options.PatternCompat): their patterns must compile with CompileCompat, not Compile.
var compatSets = map[string]bool{"pcre-compat": true}

// TestCorpusPatterns is the pattern completeness scan of ADR 0003 (#40): every pattern libyang
// accepted somewhere in the conformance corpus must compile, or be a recorded deviation
// (D-0002…D-0008, U-0001). libyang accepted a pattern when it is in a golden's compiled type
// (`patterns[].expr`, the original text) or in a module (or submodule) that some golden of the
// same corpus set reports as accepted.
func TestCorpusPatterns(t *testing.T) {
	root := filepath.Join("..", "..", "conformance", "corpus")
	fsys := os.DirFS(root)
	pats := map[string]string{}           // accepted pattern -> where it was seen
	strict := map[string]bool{}           // accepted pattern seen in a set without pattern_compat
	accepted := map[string]bool{}         // "<set>/<module>" accepted in some golden
	files := map[string]map[string]bool{} // "<set>/<module>" -> its .yang files' patterns
	var yangFiles, unparsed int
	err := fs.WalkDir(fsys, ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		set := strings.SplitN(rel, "/", 2)[0]
		switch {
		case strings.HasSuffix(rel, ".json") && path.Base(path.Dir(rel)) == "golden":
			b, err := fs.ReadFile(fsys, rel)
			if err != nil {
				return err
			}
			var v map[string]any
			if json.Unmarshal(b, &v) != nil {
				return nil // not a response
			}
			collectPatterns(v, func(p string) { pats[p], strict[p] = rel, strict[p] || !compatSets[set] })
			for _, m := range asList(v["modules"]) {
				if m, ok := m.(map[string]any); ok && m["accepted"] == true {
					accepted[set+"/"+m["name"].(string)] = true
				}
			}
		case strings.HasSuffix(rel, ".yang"):
			yangFiles++
			b, err := fs.ReadFile(fsys, rel)
			if err != nil {
				return err
			}
			s, err := yparser.Parse(rel, b, nil)
			if err != nil {
				unparsed++ // invalid on purpose; libyang rejects it too (fixture verdicts)
				return nil
			}
			name := s.Arg
			if s.Keyword == "submodule" {
				for _, c := range s.Subs {
					if c.Keyword == "belongs-to" {
						name = c.Arg
					}
				}
			}
			key := set + "/" + name
			if files[key] == nil {
				files[key] = map[string]bool{}
			}
			walkStmts(s, func(c *yparser.Stmt) {
				if c.Keyword == "pattern" && c.ExtPrefix == "" {
					files[key][c.Arg] = true
				}
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, ps := range files {
		if accepted[key] {
			for p := range ps {
				if _, ok := pats[p]; !ok {
					pats[p] = key
				}
				strict[p] = strict[p] || !compatSets[strings.SplitN(key, "/", 2)[0]]
			}
		}
	}
	if len(pats) < 40 || yangFiles < 1000 { // guards against a silent walk of the wrong directory
		t.Fatalf("only %d accepted patterns from %d .yang files under %s", len(pats), yangFiles, root)
	}
	var refused int
	for _, p := range slices.Sorted(maps.Keys(pats)) {
		compile := Compile
		if !strict[p] {
			compile = CompileCompat
		}
		_, err := compile(p)
		dev, listed := corpusPatternDeviations[p]
		switch {
		case err == nil && listed:
			t.Errorf("%q (%s) compiles now: drop it from corpusPatternDeviations (%s)", p, pats[p], dev)
		case err != nil && !listed:
			t.Errorf("%q (%s): libyang accepts it, Compile: %v", p, pats[p], err)
		case err != nil:
			refused++
		}
	}
	t.Logf("%d .yang files (%d not parsed), %d distinct patterns libyang accepted, %d refused as recorded deviations",
		yangFiles, unparsed, len(pats), refused)
}

// TestPublicModelPatterns compiles every pattern of every .yang file under $XSDRE_SCAN (for
// example a YangModels/yang checkout) and lists the ones Compile refuses; skipped without it.
// ADR 0003 records the run on the IETF and IANA modules.
func TestPublicModelPatterns(t *testing.T) {
	dir := os.Getenv("XSDRE_SCAN")
	if dir == "" {
		t.Skip("XSDRE_SCAN not set")
	}
	seen := map[string]bool{}
	var files int
	fsys := os.DirFS(dir)
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yang") {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		s, err := yparser.Parse(p, b, nil)
		if err != nil {
			t.Logf("%s: not parsed: %v", p, err)
			return nil
		}
		files++
		walkStmts(s, func(c *yparser.Stmt) {
			if c.Keyword != "pattern" || c.ExtPrefix != "" || seen[c.Arg] {
				return
			}
			seen[c.Arg] = true
			if _, err := Compile(c.Arg); err != nil {
				t.Errorf("%s: %q: %v", path.Base(p), c.Arg, err)
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d modules, %d distinct patterns", files, len(seen))
}

// collectPatterns calls f with every patterns[].expr string in a decoded golden.
func collectPatterns(v any, f func(string)) {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			if k != "patterns" {
				collectPatterns(c, f)
				continue
			}
			for _, p := range asList(c) {
				if m, ok := p.(map[string]any); ok {
					if e, ok := m["expr"].(string); ok {
						f(e)
					}
				}
			}
		}
	case []any:
		for _, c := range x {
			collectPatterns(c, f)
		}
	}
}

func walkStmts(s *yparser.Stmt, f func(*yparser.Stmt)) {
	f(s)
	for _, c := range s.Subs {
		walkStmts(c, f)
	}
}

func asList(v any) []any { l, _ := v.([]any); return l }
