// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const base = `version: 2
oracle: {libyang: v, libyang_commit: c}
fixtures:
  - id: a/x
    dir: d
    source: &own {url: u, commit: null, license: l}
    rfc: []
    areas: [types]
    request: {op: x}
    golden: g.json
# END a
`

const appended = `
# BEGIN b
  - id: b/y
    dir: d
    source: *own
    rfc: []
    areas: [types]
    request: {op: x}
    golden: g.json
`

func setup(t *testing.T, manifest string) (dir, baseFile string) {
	t.Helper()
	dir = t.TempDir()
	baseFile = filepath.Join(t.TempDir(), "base.yaml")
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baseFile, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, baseFile
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p) //nolint:gosec // test path
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// -since moves only the appended fixtures and leaves manifest.yaml as the base had it.
func TestSince(t *testing.T) {
	dir, baseFile := setup(t, base+appended)
	if err := do(dir, baseFile, false); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "manifest.yaml")); got != base {
		t.Errorf("manifest.yaml:\n%s", got)
	}
	want := "fixtures:\n# BEGIN b\n  - id: b/y\n    dir: d\n    source: {url: u, commit: null, license: l}\n" +
		"    rfc: []\n    areas: [types]\n    request: {op: x}\n    golden: g.json\n"
	if got := read(t, filepath.Join(dir, "manifest.d", "b", "y.yaml")); got != want {
		t.Errorf("fragment:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.d", "a")); err == nil {
		t.Error("a base fixture was moved")
	}
	// a second run has nothing left to move; the full split then moves a/x and is idempotent
	if err := do(dir, baseFile, false); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := do(dir, "", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.d", "a", "x.yaml")); err != nil {
		t.Error(err)
	}
}

func TestSinceRejects(t *testing.T) {
	dir, baseFile := setup(t, strings.Replace(base, "g.json", "h.json", 1)+appended)
	if err := do(dir, baseFile, false); err == nil || !strings.Contains(err.Error(), "plus appended lines") {
		t.Errorf("edited base line: %v", err)
	}
	dir, baseFile = setup(t, base+appended)
	if err := os.MkdirAll(filepath.Join(dir, "manifest.d", "b"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.d", "b", "y.yaml"), []byte(strings.Replace("fixtures:\n"+strings.TrimPrefix(appended, "\n# BEGIN b\n"), "*own", "{url: u, commit: null, license: l}", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := do(dir, baseFile, true); err == nil || !strings.Contains(err.Error(), "b/y: duplicate id") {
		t.Errorf("existing fragment: %v", err)
	}
}
