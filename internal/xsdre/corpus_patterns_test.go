// SPDX-License-Identifier: BSD-3-Clause

package xsdre

import (
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"

	yparser "github.com/vibe-ports/yang/internal/parser"
)

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

func walkStmts(s *yparser.Stmt, f func(*yparser.Stmt)) {
	f(s)
	for _, c := range s.Subs {
		walkStmts(c, f)
	}
}
