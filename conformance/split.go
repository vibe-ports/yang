// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing/fstest"

	"go.yaml.in/yaml/v3"
)

// SplitHint is the comment SplitManifest leaves under `fixtures:` in manifest.yaml.
const SplitHint = "  # one file per fixture: " + FragmentDir + "/<set>/<name>.yaml (manifest.schema.md)\n"

var (
	itemStart = regexp.MustCompile(`^( *)- `)
	anchorDef = regexp.MustCompile(`([:-] +)&([A-Za-z0-9_-]+) +`)
	aliasRef  = regexp.MustCompile(`([:-] +)\*([A-Za-z0-9_-]+)(\s|,|\]|\}|$)`)
	endMarker = regexp.MustCompile(`^ *# END\b`)
)

// SplitManifest moves every inline fixture of manifest text src into its own fragment text,
// returning the new manifest (everything up to `fixtures:`, then SplitHint) and the fragments by
// path under FragmentDir. Each fragment is the fixture's lines verbatim with the comments above
// it, under `fixtures:`; anchors are dropped and aliases expanded (a fragment cannot see another
// file's anchors), resolved against anchors, the manifest text defining them (src when nil).
// `# END` group markers are dropped. Lines are kept as text, so the line scanners of the root
// module (internal/compile tests) read fragments as they read manifest.yaml.
func SplitManifest(src, anchors []byte) ([]byte, map[string][]byte, error) {
	if anchors == nil {
		anchors = src
	}
	lines := strings.SplitAfter(string(src), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	i0 := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "fixtures:") })
	if i0 < 0 {
		return nil, nil, errors.New("no top-level fixtures: key")
	}
	head := strings.Join(lines[:i0], "") + "fixtures:\n" + SplitHint
	body := lines[i0+1:]
	defs, err := anchorValues(string(anchors))
	if err != nil {
		return nil, nil, err
	}
	// item starts and content ends (last non-blank line that is not a comment at or left of the
	// item indent: such comments lead the next item)
	var starts, ends []int
	indent := -1
	for i, l := range body {
		t := strings.TrimSpace(l)
		lead := len(l) - len(strings.TrimLeft(l, " "))
		switch {
		case t == "" || l == SplitHint:
		case lead == 0 && t[0] != '#':
			return nil, nil, fmt.Errorf("line %d: top-level key after the fixtures key", i0+2+i)
		case itemStart.MatchString(l) && (indent < 0 || lead == indent):
			indent = lead
			starts, ends = append(starts, i), append(ends, i+1)
		case len(starts) > 0 && (t[0] != '#' || lead > indent):
			ends[len(ends)-1] = i + 1
		}
	}
	frags := map[string][]byte{}
	gap := 0
	for k, s := range starts {
		var b strings.Builder
		b.WriteString("fixtures:\n")
		for _, l := range body[gap:s] {
			if isComment(l) && !endMarker.MatchString(l) && l != SplitHint {
				b.WriteString(l)
			}
		}
		e := ends[k]
		if k == len(starts)-1 {
			e = len(body)
		}
		var item strings.Builder
		for _, l := range body[s:e] {
			if k == len(starts)-1 && endMarker.MatchString(l) {
				continue
			}
			l = anchorDef.ReplaceAllString(l, "$1")
			var bad error
			l = aliasRef.ReplaceAllStringFunc(l, func(m string) string {
				p := aliasRef.FindStringSubmatch(m)
				v, ok := defs[p[2]]
				if !ok {
					bad = fmt.Errorf("alias *%s: no anchor", p[2])
				}
				return p[1] + v + p[3]
			})
			if bad != nil {
				return nil, nil, bad
			}
			item.WriteString(l)
		}
		b.WriteString(strings.TrimRight(item.String(), " \n") + "\n")
		f, err := idOf(b.String())
		if err != nil {
			return nil, nil, fmt.Errorf("fixture at line %d: %w", i0+2+s, err)
		}
		rel := f + ".yaml"
		if _, dup := frags[rel]; dup {
			return nil, nil, fmt.Errorf("%s: duplicate id", f)
		}
		if _, err := ParseFragment(rel, []byte(b.String())); err != nil {
			return nil, nil, err
		}
		frags[rel] = []byte(b.String())
		gap = ends[k]
	}
	return []byte(head), frags, nil
}

func isComment(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "#") }

// idOf is the id of the one fixture in fragment text b.
func idOf(b string) (string, error) {
	var fr struct {
		Fixtures []struct {
			ID string `yaml:"id"`
		} `yaml:"fixtures"`
	}
	if err := yaml.Unmarshal([]byte(b), &fr); err != nil {
		return "", err
	}
	if len(fr.Fixtures) != 1 || !fs.ValidPath(fr.Fixtures[0].ID) || !strings.Contains(fr.Fixtures[0].ID, "/") {
		return "", fmt.Errorf("want one fixture with an id <set>/<name>, got %v", fr.Fixtures)
	}
	return fr.Fixtures[0].ID, nil
}

// anchorValues maps each anchor of src to the text of its value, which must be a one-line flow
// collection or plain scalar.
func anchorValues(src string) (map[string]string, error) {
	defs := map[string]string{}
	for _, l := range strings.Split(src, "\n") {
		for _, m := range anchorDef.FindAllStringSubmatchIndex(l, -1) {
			name := l[m[4]:m[5]]
			v, err := flowValue(l[m[1]:])
			if err != nil {
				return nil, fmt.Errorf("anchor &%s: %w", name, err)
			}
			defs[name] = v
		}
	}
	return defs, nil
}

// flowValue is the YAML value at the start of s: a flow collection up to its closing bracket, or
// a plain scalar up to a comment or the end of the line.
func flowValue(s string) (string, error) {
	if s == "" || (s[0] != '{' && s[0] != '[') {
		v, _, _ := strings.Cut(s, " #")
		if v = strings.TrimSpace(v); v == "" || strings.ContainsAny(v[:1], `"'|>`) {
			return "", fmt.Errorf("unsupported value %q", s)
		}
		return v, nil
	}
	depth, quote := 0, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '"' && c == '\\':
			i++
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
		case c == '"' || c == '\'':
			quote = c
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			if depth--; depth == 0 {
				return s[:i+1], nil
			}
		}
	}
	return "", fmt.Errorf("unterminated flow value %q", s)
}

// SplitFS is corpus fsys once SplitManifest's result is written: manifest.yaml is man, and
// FragmentDir holds the fragments already in fsys plus frags (a path in both is an error).
func SplitFS(fsys fs.FS, man []byte, frags map[string][]byte) (fstest.MapFS, error) {
	out := fstest.MapFS{"manifest.yaml": {Data: man}}
	err := fs.WalkDir(fsys, FragmentDir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil && p == FragmentDir && errors.Is(err, fs.ErrNotExist):
			return fs.SkipAll
		case err != nil || d.IsDir():
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		out[p] = &fstest.MapFile{Data: b, Mode: d.Type()}
		return err
	})
	for rel, b := range frags {
		p := FragmentDir + "/" + rel
		if _, ok := out[p]; ok {
			return nil, fmt.Errorf("%s: already exists (duplicate id)", p)
		}
		out[p] = &fstest.MapFile{Data: b}
	}
	return out, err
}

// SameFixtures reports how b's decoded fixture set differs from a's (ids, records, resolved golden
// and input paths), nil when identical; the order of fixtures is not compared.
func SameFixtures(a, b *Manifest) error {
	var errs []error
	if a.Version != b.Version || a.Oracle != b.Oracle {
		errs = append(errs, errors.New("version or oracle differ"))
	}
	ia, ib := byID(a), byID(b)
	for id, fa := range ia {
		fb, ok := ib[id]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("%s: missing", id))
		case !reflect.DeepEqual(fa, fb) || a.GoldenPath(fa) != b.GoldenPath(fb) ||
			!reflect.DeepEqual(a.Inputs(fa), b.Inputs(fb)):
			errs = append(errs, fmt.Errorf("%s: differs", id))
		}
	}
	for id := range ib {
		if _, ok := ia[id]; !ok {
			errs = append(errs, fmt.Errorf("%s: added", id))
		}
	}
	if len(a.Fixtures) != len(ia) || len(b.Fixtures) != len(ib) {
		errs = append(errs, errors.New("duplicate ids"))
	}
	slices.SortFunc(errs, func(x, y error) int { return strings.Compare(x.Error(), y.Error()) })
	return errors.Join(errs...)
}

func byID(m *Manifest) map[string]Fixture {
	out := map[string]Fixture{}
	for _, f := range m.Fixtures {
		out[f.ID] = f
	}
	return out
}

// Digest is a sha256 over the decoded fixtures sorted by id (record, resolved golden and input
// paths relative to the corpus): equal digests, equal fixture sets.
func (m *Manifest) Digest() string {
	fx := slices.Clone(m.Fixtures)
	slices.SortFunc(fx, func(a, b Fixture) int { return strings.Compare(a.ID, b.ID) })
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, f := range fx {
		rel := func(p string) string { return strings.TrimPrefix(p, m.corpus+"/") }
		var in []string
		for _, i := range m.Inputs(f) {
			in = append(in, i.Key+"="+rel(i.Path))
		}
		if err := enc.Encode([]any{f, f.Source.commitSet, rel(m.GoldenPath(f)), in}); err != nil {
			panic(err) // fixtures are decoded YAML: always encodable
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
