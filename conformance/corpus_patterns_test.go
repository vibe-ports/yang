// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"slices"
	"strings"
	"testing"

	yparser "github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/xsdre"
)

// corpusPatternDeviations are patterns libyang v5.8.6 accepts but xsdre refuses, each with the
// deviations.md entry that explains it (ADR 0003 addendum, #40; U-0001).
var corpusPatternDeviations = map[string]string{
	`\p{IsBasicLatinBogus}`:       "D-0006", // libyang's block-name prefix match resolves it to BasicLatin
	strings.Repeat(`\p{L}`, 2000): "U-0001", // ~1.37M expanded ranges exceed the 1,048,576-range budget on any supported Go
}

// TestCorpusPatterns is the pattern completeness scan of ADR 0003 (#40): every pattern libyang
// accepted somewhere in the conformance corpus must compile in that fixture's mode, or be a
// recorded deviation. libyang accepted a pattern when it is in a golden's compiled type
// (`patterns[].expr`, the original text) or in a module (or submodule) that some golden of the
// same corpus set reports as accepted.
func TestCorpusPatterns(t *testing.T) {
	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	fsys := os.DirFS(m.Corpus())
	compatGoldens, err := patternCompatGoldens(m)
	if err != nil {
		t.Fatal(err)
	}
	pats := map[string]string{}           // accepted pattern -> where it was seen
	strict := map[string]bool{}           // accepted pattern seen in a fixture without pattern_compat
	accepted := map[string]patternModes{} // "<set>/<module>" and the fixture modes accepting it
	files := map[string]map[string]bool{} // "<set>/<module>" -> its .yang files' patterns
	var yangFiles, unparsed int
	err = fs.WalkDir(fsys, ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		set := strings.SplitN(rel, "/", 2)[0]
		switch {
		case strings.HasSuffix(rel, ".json") && path.Base(path.Dir(rel)) == "golden":
			compat, ok := compatGoldens[rel]
			if !ok {
				return fmt.Errorf("%s has no manifest fixture", rel)
			}
			b, err := fs.ReadFile(fsys, rel)
			if err != nil {
				return err
			}
			var v map[string]any
			if json.Unmarshal(b, &v) != nil {
				return nil // not a response
			}
			collectPatterns(v, func(p string) { pats[p], strict[p] = rel, strict[p] || !compat })
			for _, item := range list(v["modules"]) {
				if mod, ok := item.(map[string]any); ok && mod["accepted"] == true {
					key := set + "/" + mod["name"].(string)
					modes := accepted[key]
					modes.seen, modes.strict = true, modes.strict || !compat
					accepted[key] = modes
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
			walkPatternStmts(s, func(c *yparser.Stmt) {
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
		if modes := accepted[key]; modes.seen {
			for p := range ps {
				if _, ok := pats[p]; !ok {
					pats[p] = key
				}
				strict[p] = strict[p] || modes.strict
			}
		}
	}
	if len(pats) < 40 || yangFiles < 1000 { // guards against a silent walk of the wrong directory
		t.Fatalf("only %d accepted patterns from %d .yang files under %s", len(pats), yangFiles, m.Corpus())
	}
	var refused int
	for _, p := range slices.Sorted(maps.Keys(pats)) {
		compile := xsdre.Compile
		if !strict[p] {
			compile = xsdre.CompileCompat
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

type patternModes struct {
	seen   bool
	strict bool
}

func patternCompatGoldens(m *Manifest) (map[string]bool, error) {
	out := make(map[string]bool, len(m.Fixtures))
	for _, f := range m.Fixtures {
		key := path.Join(f.Dir, f.Golden)
		if _, exists := out[key]; exists {
			return nil, fmt.Errorf("%s: duplicate golden %s", f.ID, key)
		}
		out[key] = fixturePatternCompat(f)
	}
	return out, nil
}

func fixturePatternCompat(f Fixture) bool {
	for _, option := range list(f.Request["context_options"]) {
		if option == "pattern_compat" {
			return true
		}
	}
	return false
}

func TestPatternFixtureModes(t *testing.T) {
	for name, options := range map[string]string{
		"block-list":       "context_options:\n        - pattern_compat",
		"trailing-comment": "context_options: [pattern_compat] # select libyang-compatible patterns",
	} {
		t.Run(name, func(t *testing.T) {
			src := "fixtures:\n  - id: test/example\n    dir: test\n    request:\n      op: schema\n      " + options + "\n    golden: golden/example.json\n"
			f, err := ParseFragment("test/example.yaml", []byte(src))
			if err != nil {
				t.Fatal(err)
			}
			compile := xsdre.Compile
			if fixturePatternCompat(f) {
				compile = xsdre.CompileCompat
			}
			if _, err := compile(`(?:a)`); err != nil {
				t.Fatalf("fixture did not select compatibility mode: %v", err)
			}
		})
	}
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
			for _, p := range list(c) {
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

func walkPatternStmts(s *yparser.Stmt, f func(*yparser.Stmt)) {
	f(s)
	for _, c := range s.Subs {
		walkPatternStmts(c, f)
	}
}
