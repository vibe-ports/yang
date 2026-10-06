// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang/internal/parser"
)

var moduleName = regexp.MustCompile(`(?m)^\s*(?:sub)?module\s+([A-Za-z_][A-Za-z0-9_.-]*)`)

// fuzzSep separates the files of one fuzz input.
const fuzzSep = "\n//====\n"

// FuzzLoad builds an fs.FS from the input (files separated by fuzzSep, each stored as
// <its module name>.yang) and loads the first file's module with NewContext + Load.
//
// Budgets: every limit is small, so a run cannot exhaust memory or time: 64 KiB per file, parser
// depth 50 and 5000 statements, 2000 types and nodes, 200 union members, bit positions up to 1000,
// compile depth 50, 20 search directories. An exceeded limit is an error wrapping ErrBudget.
//
// Properties: no panic; a failure has a message and returns no module; a success returns the
// requested module and a second Load of it returns the same one; every diagnostic names its phase
// and has a message.
func FuzzLoad(f *testing.F) {
	f.Add([]byte("module a { namespace urn:a; prefix a; leaf x { type string; } }"))
	f.Add([]byte("module a { yang-version 1.1; namespace urn:a; prefix a; import b { prefix b; } leaf x { type b:t; } }" +
		fuzzSep + "module b { namespace urn:b; prefix b; typedef t { type uint8 { range 1..5; } } }"))
	f.Add([]byte("module a { namespace urn:a; prefix a; include s; }" + fuzzSep + "submodule s { belongs-to a { prefix a; } container c; }"))
	f.Add([]byte("module a { namespace urn:a; prefix a; typedef u { type union { type u; } } }"))
	f.Add([]byte("module a { namespace urn:a; prefix a; leaf x { type bits { bit b { position 4294967295; } } } }"))
	f.Add([]byte("module a { namespace urn:a; prefix a; leaf x { type union { type union { type uint8; type string; } type int8; } } }"))
	// the schemas of the conformance corpus
	paths, _ := filepath.Glob("../../conformance/corpus/*/schemas/*.yang")
	more, _ := filepath.Glob("../../conformance/corpus/ctypes/*/*.yang")
	for i, p := range append(paths, more...) {
		if b, err := os.ReadFile(p); err == nil && len(b) < 8<<10 && i%2 == 0 { //nolint:gosec // seeds from the repo
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		files := fstest.MapFS{}
		first := ""
		for i, chunk := range bytes.SplitN(data, []byte(fuzzSep), 8) {
			m := moduleName.FindSubmatch(chunk)
			if m == nil {
				continue
			}
			if i == 0 || first == "" {
				first = string(m[1])
			}
			files[string(m[1])+".yang"] = &fstest.MapFile{Data: chunk}
		}
		if first == "" {
			return
		}
		c, _, err := NewContext(Options{
			MaxSearchDirs: 20,
			Parse:         parser.Budget{MaxBytes: 64 << 10, MaxDepth: 50, MaxStmts: 5000, MaxArgLen: 4 << 10},
			Budget: Budget{MaxTypes: 2000, MaxUnionMembers: 200, MaxBitPosition: 1000, MaxNodes: 2000,
				MaxDepth: 50},
		}, files)
		if err != nil {
			t.Fatalf("NewContext: %v", err)
		}
		m, diags, err := c.Load(first, "", nil)
		for _, d := range diags {
			if d.Msg == "" || (d.Phase != "parse" && d.Phase != "compile") {
				t.Fatalf("diagnostic without message or phase: %+v", d)
			}
		}
		if err != nil {
			if err.Error() == "" || m != nil {
				t.Fatalf("Load(%q): error %q, module %v", first, err, m)
			}
			return
		}
		if m == nil || m.Name != first {
			t.Fatalf("Load(%q) returned %v", first, m)
		}
		if m2, _, err := c.Load(first, "", nil); err != nil || m2 != m {
			t.Fatalf("second Load(%q): %v, %v", first, m2, err)
		}
	})
}
