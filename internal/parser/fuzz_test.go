// SPDX-License-Identifier: BSD-3-Clause

package parser

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// libyangSrc is a libyang v5.8.6 source tree with tests/ and modules/:
// .cache/libyang (make libyang-src) or the dev image's copy; "" if none.
// The files are not part of the repo.
func libyangSrc() string {
	for _, d := range []string{filepath.Join("..", "..", ".cache", "libyang"), "/opt/libyang/src"} {
		if _, err := os.Stat(filepath.Join(d, "tests")); err == nil {
			return d
		}
	}
	return ""
}

// addLibyangCorpus seeds f with libyang's fuzz corpus and modules if available.
func addLibyangCorpus(f *testing.F) {
	root := libyangSrc()
	if root == "" {
		return
	}
	for _, g := range []string{"tests/fuzz/corpus/lys_parse_mem/*", "tests/modules/yang/*.yang", "modules/*.yang"} {
		files, _ := filepath.Glob(filepath.Join(root, g))
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
	for _, c := range g1Cases {
		f.Add([]byte(c.src))
	}
	for _, c := range buildErrors {
		f.Add([]byte(c.src))
	}
	for _, c := range iffErrors {
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
		if _, err := Build(s); err != nil {
			t.Fatalf("Build of a parsed tree: %v", err)
		}
	})
}

// FuzzIfFeature: parseIfFeature never panics, at any length, and a result
// has exactly the features of the expression.
func FuzzIfFeature(f *testing.F) {
	for _, s := range []string{"a", "a or b and c", "not not (x:y)", "a)(", "((a)", "or a", "", "a and", "android or notify"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		e, why := parseIfFeature(s, true)
		if (e == nil) == (why == "") {
			t.Fatalf("%q: tree %v, reason %q", s, e, why)
		}
		for stack := []*IffExpr{e}; e != nil && len(stack) > 0; {
			x := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			switch {
			case x == nil:
				t.Fatalf("%q: missing operand", s)
			case x.Op == IffFeature:
				if !strings.Contains(s, x.Name) {
					t.Fatalf("%q: feature %q not in the expression", s, x.Name)
				}
			case x.Op == IffNot:
				stack = append(stack, x.X)
			default:
				stack = append(stack, x.X, x.Y)
			}
		}
	})
}
