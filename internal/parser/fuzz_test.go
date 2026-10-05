// SPDX-License-Identifier: BSD-3-Clause

package parser

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// addLibyangCorpus seeds f with libyang's fuzz corpus and modules when
// .cache/libyang exists (make libyang-src); they are not part of the repo.
func addLibyangCorpus(f *testing.F) {
	for _, g := range []string{"tests/fuzz/corpus/lys_parse_mem/*", "tests/modules/yang/*.yang", "modules/*.yang"} {
		files, _ := filepath.Glob(filepath.Join("..", "..", ".cache", "libyang", g))
		for _, p := range files {
			if b, err := os.ReadFile(p); err == nil { //nolint:gosec // test seeds from the local cache
				f.Add(b)
			}
		}
	}
}

func treeSize(s *Stmt, depth int) (n, maxDepth int) {
	n, maxDepth = 1, depth
	for _, c := range s.Subs {
		cn, cd := treeSize(c, depth+1)
		n += cn
		maxDepth = max(maxDepth, cd)
	}
	return n, maxDepth
}

// FuzzParse: Parse never panics, every error is an *Error and a parsed tree stays within the budget.
func FuzzParse(f *testing.F) {
	for _, c := range fidelity {
		f.Add([]byte(mod("1.1", c.src)))
	}
	for _, c := range parseErrors {
		f.Add([]byte(c.src))
	}
	addLibyangCorpus(f)
	b := &Budget{MaxBytes: 1 << 20, MaxDepth: 64, MaxStmts: 4096, MaxArgLen: 1 << 16}
	f.Fuzz(func(t *testing.T, src []byte) {
		s, err := Parse("f.yang", src, b)
		var e *Error
		if err != nil {
			if !errors.As(err, &e) {
				t.Fatalf("not an *Error: %v", err)
			}
			return
		}
		if n, d := treeSize(s, 1); n > b.MaxStmts || d > b.MaxDepth+1 { // the root is outside any block
			t.Fatalf("budget exceeded: %d statements, depth %d", n, d)
		}
	})
}
