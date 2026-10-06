// SPDX-License-Identifier: BSD-3-Clause

// Package conformance is the Go side of the libyang oracle harness: fixture manifest (v2),
// goldens, the Engine interface and the per-area comparison report.
package conformance

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Areas is the allowed set of fixture areas.
var Areas = []string{"schema", "types", "xpath", "validation", "defaults", "codecs", "diff", "operations", "nmda"}

// Manifest is corpus/manifest.yaml (format: manifest.schema.md).
type Manifest struct {
	Version  int       `yaml:"version"`
	Oracle   Oracle    `yaml:"oracle"`
	Fixtures []Fixture `yaml:"fixtures"`

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
}

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

// LoadManifest parses and validates path (version, unique ids, required fields, areas, deviation
// ids from ../deviations.md). File existence is checked separately by CheckFiles.
func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path) //nolint:gosec // dev tool, caller-chosen path
	if err != nil {
		return nil, err
	}
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m.corpus = filepath.Dir(path)
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &m, nil
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
		if f.Assert == nil {
			continue
		}
		if !slices.Contains(verdicts, f.Assert.Verdict) {
			fail("%s: assert.verdict %q not one of %v", id, f.Assert.Verdict, verdicts)
		}
		if len(f.Assert.Waive) > 0 && f.Assert.Deviation == nil {
			fail("%s: assert.waive without assert.deviation", id)
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
		base := filepath.Join(m.corpus, f.Dir)
		if goldens {
			if err := exists(m.GoldenPath(f)); err != nil {
				errs = append(errs, fmt.Errorf("%s: golden: %w", f.ID, err))
			}
		}
		for k, v := range f.Request {
			var paths []string
			switch {
			case k == "searchdirs":
				if l, ok := v.([]any); ok {
					for _, e := range l {
						paths = append(paths, fmt.Sprint(e))
					}
				}
			case strings.HasSuffix(k, "_file"):
				paths = append(paths, fmt.Sprint(v))
			}
			for _, p := range paths {
				if err := exists(filepath.Join(base, p)); err != nil {
					errs = append(errs, fmt.Errorf("%s: request.%s: %w", f.ID, k, err))
				}
			}
		}
	}
	return errors.Join(errs...)
}
