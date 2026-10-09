// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

const head = "version: 2\noracle: {libyang: v, libyang_commit: c}\nfixtures:\n"

func fx(id string) string {
	return "  - {id: " + id + ", dir: d, golden: g.json, source: {url: u, license: l, commit: null}, rfc: [], areas: [types], request: {op: x, data_file: in.json}}\n"
}

// corpusFS is a corpus with manifest.yaml (head + inline) and the given extra files.
func corpusFS(inline string, files map[string]string) fstest.MapFS {
	m := fstest.MapFS{"manifest.yaml": {Data: []byte(head + inline)}}
	for p, s := range files {
		m[p] = &fstest.MapFile{Data: []byte(s)}
	}
	return m
}

func TestFragmentsLoad(t *testing.T) {
	m, err := LoadManifestFS(corpusFS(fx("c/z"), map[string]string{
		"manifest.d/b/y.yaml": "fixtures:\n" + fx("b/y"),
		"manifest.d/a/x.yaml": "# a comment\nfixtures:\n" + fx("a/x"),
	}), "manifest.yaml", "corpus")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, f := range m.Fixtures {
		ids = append(ids, f.ID)
	}
	if got := strings.Join(ids, " "); got != "c/z a/x b/y" { // inline, then fragments by path
		t.Errorf("ids %s", got)
	}
	if got := m.Inputs(m.Fixtures[1]); len(got) != 1 || got[0].Path != filepath.Join("corpus", "d", "in.json") {
		t.Errorf("inputs resolve against the corpus, not the fragment: %v", got)
	}
}

func TestFragmentsRejectBad(t *testing.T) {
	for name, c := range map[string]struct{ inline, path, text, want string }{
		"empty":           {"", "a/x.yaml", "", "empty fragment"},
		"comment only":    {"", "a/x.yaml", "# nothing\n", "empty fragment"},
		"no fixtures":     {"", "a/x.yaml", "fixtures: []\n", "0 fixtures, want exactly one"},
		"two fixtures":    {"", "a/x.yaml", "fixtures:\n" + fx("a/x") + fx("a/y"), "2 fixtures"},
		"malformed":       {"", "a/x.yaml", "fixtures:\n  - {id: a/x\n", "a/x.yaml: yaml"},
		"unknown key":     {"", "a/x.yaml", "version: 2\nfixtures:\n" + fx("a/x"), "field version not found"},
		"two documents":   {"", "a/x.yaml", "fixtures:\n" + fx("a/x") + "---\nfixtures: []\n", "more than one YAML document"},
		"id not the path": {"", "a/y.yaml", "fixtures:\n" + fx("a/x"), `id "a/x" does not match the file path \(want "a/y"\)`},
		"not yaml":        {"", "a/x.yml", "fixtures:\n" + fx("a/x"), "a/x.yml: not a .yaml file"},
		"dup of inline":   {fx("a/x"), "a/x.yaml", "fixtures:\n" + fx("a/x"), "a/x: duplicate id"},
		"unknown anchor":  {"", "a/x.yaml", "fixtures:\n  - {id: a/x, source: *own}\n", "unknown anchor 'own'"},
	} {
		_, err := LoadManifestFS(corpusFS(c.inline, map[string]string{"manifest.d/" + c.path: c.text}), "manifest.yaml", "corpus")
		if err == nil || !regexp.MustCompile(c.want).MatchString(err.Error()) {
			t.Errorf("%s: %v, want %s", name, err, c.want)
		}
	}
}

func TestFragmentMissingInput(t *testing.T) {
	dir := t.TempDir()
	for p, s := range map[string]string{
		"manifest.yaml":       head,
		"manifest.d/a/x.yaml": "fixtures:\n" + fx("a/x"),
		"d/g.json":            "{}\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := LoadManifest(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	err = m.CheckFiles(true)
	if err == nil || !strings.Contains(err.Error(), "a/x: request.data_file: stat "+filepath.Join(dir, "d", "in.json")) {
		t.Errorf("missing input: %v", err)
	}
}

const splitSrc = `# header
version: 2
oracle: {libyang: v, libyang_commit: c}
fixtures:
  - id: a/x
    dir: d
    source: &own {url: "u", commit: null, license: l}   # trailing
    rfc: []
    areas: [types]
    # interior comment
    request: {op: x}
    golden: g.json
# END a

  # ---- group b ----
# BEGIN b
  - id: b/y
    dir: d
    source: *own
    rfc: []
    areas: [types]
    request:
      op: x
      modules: [{"name": "m"}]
      data: "*own &own"
    golden: g.json

  # trailing note
# END b
`

func TestSplitManifest(t *testing.T) {
	man, frags, err := SplitManifest([]byte(splitSrc), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "# header\nversion: 2\noracle: {libyang: v, libyang_commit: c}\nfixtures:\n" + SplitHint; string(man) != want {
		t.Errorf("manifest:\n%s", man)
	}
	want := map[string]string{
		"a/x.yaml": `fixtures:
  - id: a/x
    dir: d
    source: {url: "u", commit: null, license: l}   # trailing
    rfc: []
    areas: [types]
    # interior comment
    request: {op: x}
    golden: g.json
`,
		"b/y.yaml": `fixtures:
  # ---- group b ----
# BEGIN b
  - id: b/y
    dir: d
    source: {url: "u", commit: null, license: l}
    rfc: []
    areas: [types]
    request:
      op: x
      modules: [{"name": "m"}]
      data: "*own &own"
    golden: g.json

  # trailing note
`,
	}
	for p := range maps.Keys(want) {
		if string(frags[p]) != want[p] {
			t.Errorf("%s:\n%s", p, frags[p])
		}
	}
	if len(frags) != len(want) {
		t.Errorf("fragments %v", slices.Sorted(maps.Keys(frags)))
	}
	before, err := LoadManifestFS(fstest.MapFS{"manifest.yaml": {Data: []byte(splitSrc)}}, "manifest.yaml", "corpus")
	if err != nil {
		t.Fatal(err)
	}
	after, err := SplitFS(fstest.MapFS{}, man, frags)
	if err != nil {
		t.Fatal(err)
	}
	am, err := LoadManifestFS(after, "manifest.yaml", "corpus")
	if err != nil {
		t.Fatal(err)
	}
	if err := SameFixtures(before, am); err != nil || before.Digest() != am.Digest() {
		t.Error(err)
	}
	am.Fixtures[0].Golden = "other.json"
	if err := SameFixtures(before, am); err == nil || !strings.Contains(err.Error(), "a/x: differs") {
		t.Errorf("changed golden: %v", err)
	}
	if again, more, err := SplitManifest(man, nil); err != nil || string(again) != string(man) || len(more) != 0 {
		t.Errorf("second split: %q %v %v", again, more, err)
	}
	if _, _, err := SplitManifest([]byte(head+"  - id: a/x\n    source: *nope\n"), nil); err == nil || !strings.Contains(err.Error(), "*nope") {
		t.Errorf("unknown alias: %v", err)
	}
}

// TestSplitCorpus: every inline fixture of the corpus manifest converts to a fragment with the
// same decoded record (what scripts/manifest-split -check proves before a split).
func TestSplitCorpus(t *testing.T) {
	before := load(t)
	src, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	man, frags, err := SplitManifest(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(manifestPath)
	after, err := SplitFS(os.DirFS(dir), man, frags)
	if err != nil {
		t.Fatal(err)
	}
	am, err := LoadManifestFS(after, "manifest.yaml", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := SameFixtures(before, am); err != nil {
		t.Error(err)
	}
}
