// SPDX-License-Identifier: BSD-3-Clause

// Package conformance is the Go side of the libyang oracle harness: fixture manifest (v2),
// goldens, the Engine interface and the per-area comparison report.
package conformance

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Areas is the allowed set of fixture areas.
var Areas = []string{"schema", "types", "xpath", "validation", "defaults", "codecs", "diff", "operations", "nmda"}

// FragmentDir holds one file per fixture, <set>/<name>.yaml for fixture id <set>/<name>, next to
// manifest.yaml (format: manifest.schema.md).
const FragmentDir = "manifest.d"

// Manifest is corpus/manifest.yaml plus the fixtures of its fragments (format: manifest.schema.md).
type Manifest struct {
	Version  int       `yaml:"version"`
	Oracle   Oracle    `yaml:"oracle"`
	Fixtures []Fixture `yaml:"fixtures"` // inline ones (transitional), then the fragments' by path

	corpus string // directory holding manifest.yaml
}

// Oracle records the libyang build the goldens came from.
type Oracle struct {
	Libyang       string `yaml:"libyang"`
	LibyangCommit string `yaml:"libyang_commit"`
}

// Fixture is one corpus entry.
type Fixture struct {
	ID      string         `yaml:"id"`
	Dir     string         `yaml:"dir"` // relative to the corpus dir; becomes the oracle base_dir
	Source  Source         `yaml:"source"`
	RFC     []string       `yaml:"rfc"`
	Areas   []string       `yaml:"areas"`
	Request map[string]any `yaml:"request"` // lyoracle request, verbatim
	Golden  string         `yaml:"golden"`  // relative to Dir; OBSERVED oracle output
	Assert  *Assert        `yaml:"assert"`  // NORMATIVE expectation, optional
	// Host, when set (a GOARCH, only "amd64" is used), says the golden is pinned to that oracle
	// architecture because libyang's behaviour there is undefined (C11 UB, deviation D-0028):
	// `cmd/golden -check` skips the fixture on other architectures.
	Host string `yaml:"host"`
}

// RunsOn reports whether the fixture's golden is comparable on goarch (always, unless it is host-specific).
func (f Fixture) RunsOn(goarch string) bool { return f.Host == "" || f.Host == goarch }

// Source says where a fixture's inputs came from. Commit is nil for hand-written inputs.
type Source struct {
	URL       string  `yaml:"url"`
	Commit    *string `yaml:"commit"`
	License   string  `yaml:"license"`
	commitSet bool    // the key was present (null is a valid value, absence is not)
}

// UnmarshalYAML decodes Source and records whether `commit` was given.
func (s *Source) UnmarshalYAML(n *yaml.Node) error {
	type plain Source
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*s = Source(p)
	for i := 0; i+1 < len(n.Content); i += 2 {
		switch n.Content[i].Value {
		case "commit":
			s.commitSet = true
		case "url", "license":
		default:
			return fmt.Errorf("line %d: unknown source key %q", n.Content[i].Line, n.Content[i].Value)
		}
	}
	return nil
}

// Assert is what the RFC requires (hand-written). Diagnostics match as a subset: every listed
// item must equal some actual diagnostic on all the fields it names.
type Assert struct {
	Verdict     string           `yaml:"verdict"`
	Diagnostics []map[string]any `yaml:"diagnostics"`
	RFC         []string         `yaml:"rfc"`
	Deviation   *string          `yaml:"deviation"` // deviations.md id when libyang differs
	// Waive names further response fields the deviation covers (top-level and per module, e.g.
	// schema_tree, compiled); only with Deviation.
	Waive []string `yaml:"waive"`
}

// Corpus returns the corpus directory the manifest was loaded from.
func (m *Manifest) Corpus() string { return m.corpus }

// GoldenPath is the absolute-or-corpus-relative path of the fixture's golden file.
func (m *Manifest) GoldenPath(f Fixture) string {
	return filepath.Join(m.corpus, f.Dir, f.Golden)
}

// Verdicts lyoracle can produce (request-error is a harness failure, never a verdict to assert).
var verdicts = []string{"valid", "invalid", "data-error", "schema-error", "operational-error", "empty-tree"}

var deviationRow = regexp.MustCompile(`(?m)^\|\s*(D-\d+)\b`)

// LoadManifest parses and validates path and the fragments under FragmentDir next to it (version,
// unique ids, required fields, areas, deviation ids from ../deviations.md). File existence is
// checked separately by CheckFiles.
func LoadManifest(path string) (*Manifest, error) {
	dir := filepath.Dir(path)
	return LoadManifestFS(os.DirFS(dir), filepath.Base(path), dir)
}

// LoadManifestFS is LoadManifest reading manifest name and FragmentDir from fsys; corpus is the
// directory fixture paths resolve against.
func LoadManifestFS(fsys fs.FS, name, corpus string) (*Manifest, error) {
	path := filepath.Join(corpus, name)
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m.corpus = corpus
	frags, err := loadFragments(fsys)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(corpus, FragmentDir), err)
	}
	m.Fixtures = append(m.Fixtures, frags...)
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &m, nil
}

// loadFragments reads every file under FragmentDir (absent: none), in path order.
func loadFragments(fsys fs.FS) ([]Fixture, error) {
	var out []Fixture
	var errs []error
	err := fs.WalkDir(fsys, FragmentDir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil && p == FragmentDir && errors.Is(err, fs.ErrNotExist):
			return fs.SkipAll
		case err != nil:
			return err
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			errs = append(errs, fmt.Errorf("%s: not a regular file", p))
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		f, err := ParseFragment(strings.TrimPrefix(p, FragmentDir+"/"), b)
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		out = append(out, f)
		return nil
	})
	return out, errors.Join(append(errs, err)...)
}

// ParseFragment decodes fragment rel (path under FragmentDir): a `fixtures:` sequence of exactly
// one fixture whose id is rel without ".yaml".
func ParseFragment(rel string, b []byte) (Fixture, error) {
	id, ok := strings.CutSuffix(rel, ".yaml")
	if !ok {
		return Fixture{}, fmt.Errorf("%s: not a .yaml file", rel)
	}
	var fr struct {
		Fixtures []Fixture `yaml:"fixtures"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&fr); err != nil {
		if errors.Is(err, io.EOF) {
			return Fixture{}, fmt.Errorf("%s: empty fragment", rel)
		}
		return Fixture{}, fmt.Errorf("%s: %w", rel, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Fixture{}, fmt.Errorf("%s: more than one YAML document", rel)
	}
	if len(fr.Fixtures) != 1 {
		return Fixture{}, fmt.Errorf("%s: %d fixtures, want exactly one", rel, len(fr.Fixtures))
	}
	if f := fr.Fixtures[0]; f.ID != id {
		return Fixture{}, fmt.Errorf("%s: id %q does not match the file path (want %q)", rel, f.ID, id)
	}
	return fr.Fixtures[0], nil
}

func (m *Manifest) validate() error {
	var errs []error
	fail := func(f string, a ...any) { errs = append(errs, fmt.Errorf(f, a...)) }
	if m.Version != 2 {
		fail("version is %d, want 2", m.Version)
	}
	if m.Oracle.Libyang == "" || m.Oracle.LibyangCommit == "" {
		fail("oracle.libyang and oracle.libyang_commit are required")
	}
	var devs []string
	devLoaded := false
	seen := map[string]bool{}
	for i, f := range m.Fixtures {
		id := f.ID
		if id == "" {
			fail("fixture #%d: id is required", i)
			continue
		}
		if seen[id] {
			fail("%s: duplicate id", id)
		}
		seen[id] = true
		if f.Dir == "" || f.Golden == "" || f.Source.URL == "" || f.Source.License == "" || len(f.Request) == 0 {
			fail("%s: dir, golden, source.url, source.license and request are required", id)
		}
		if !filepath.IsLocal(f.Dir) || !filepath.IsLocal(f.Golden) {
			fail("%s: dir and golden must be local relative paths", id)
		}
		if op, _ := f.Request["op"].(string); op == "" {
			fail("%s: request.op is required", id)
		}
		if !f.Source.commitSet {
			fail("%s: source.commit is required (null for hand-written)", id)
		}
		if f.RFC == nil {
			fail("%s: rfc is required ([] only for libyang-specific behaviour)", id)
		}
		if len(f.Areas) == 0 {
			fail("%s: areas is required", id)
		}
		for _, a := range f.Areas {
			if !slices.Contains(Areas, a) {
				fail("%s: unknown area %q", id, a)
			}
		}
		if f.Host != "" && f.Host != "amd64" {
			fail("%s: host %q is not amd64", id, f.Host)
		}
		if f.Assert == nil {
			continue
		}
		if !slices.Contains(verdicts, f.Assert.Verdict) {
			fail("%s: assert.verdict %q not one of %v", id, f.Assert.Verdict, verdicts)
		}
		if len(f.Assert.Waive) > 0 && f.Assert.Deviation == nil {
			fail("%s: assert.waive without assert.deviation", id)
		}
		for _, w := range f.Assert.Waive {
			if !slices.Contains(Waivable, w) {
				fail("%s: assert.waive %q not one of %v", id, w, Waivable)
			}
		}
		if d := f.Assert.Deviation; d != nil {
			if !devLoaded {
				devLoaded = true
				t, err := os.ReadFile(filepath.Join(m.corpus, "..", "deviations.md"))
				if err != nil {
					return errors.Join(append(errs, err)...)
				}
				for _, mm := range deviationRow.FindAllSubmatch(t, -1) {
					devs = append(devs, string(mm[1]))
				}
			}
			if !slices.Contains(devs, *d) {
				fail("%s: assert.deviation %q not in deviations.md", id, *d)
			}
		}
	}
	return errors.Join(errs...)
}

// CheckFiles verifies that files the manifest references exist: request inputs (`*_file` keys,
// `searchdirs`) and, when goldens is set, the goldens.
func (m *Manifest) CheckFiles(goldens bool) error {
	var errs []error
	exists := func(p string) error { _, err := os.Stat(p); return err }
	for _, f := range m.Fixtures {
		if goldens {
			if err := exists(m.GoldenPath(f)); err != nil {
				errs = append(errs, fmt.Errorf("%s: golden: %w", f.ID, err))
			}
		}
		for _, in := range m.Inputs(f) {
			if err := exists(in.Path); err != nil {
				errs = append(errs, fmt.Errorf("%s: request.%s: %w", f.ID, in.Key, err))
			}
		}
	}
	return errors.Join(errs...)
}

// Input is a file or directory a fixture's request names.
type Input struct {
	Key  string // request key
	Path string // resolved like GoldenPath
}

// Inputs lists the request inputs of f (`*_file` keys, `searchdirs`), sorted by key.
func (m *Manifest) Inputs(f Fixture) []Input {
	var out []Input
	base := filepath.Join(m.corpus, f.Dir)
	for _, k := range slices.Sorted(maps.Keys(f.Request)) {
		v := f.Request[k]
		switch {
		case k == "searchdirs":
			if l, ok := v.([]any); ok {
				for _, e := range l {
					out = append(out, Input{k, filepath.Join(base, fmt.Sprint(e))})
				}
			}
		case strings.HasSuffix(k, "_file"):
			out = append(out, Input{k, filepath.Join(base, fmt.Sprint(v))})
		}
	}
	return out
}
