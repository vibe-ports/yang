// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// harness drives type compilation the way the node walk (design 06 C4a) will: modules are
// parsed and bound to schema.Modules, then the leaves of each compiled module get their types
// in schema order through one typeCache shared by all compiles, as in one context.
type harness struct {
	t      *testing.T
	byName map[string]*Module
	parsed map[*schema.Module]*Module
	cache  *typeCache
	budget Budget
	last   *typeCtx // the context of the last compile
}

func newHarness(t *testing.T) *harness {
	return &harness{t: t, byName: map[string]*Module{}, parsed: map[*schema.Module]*Module{}, cache: newTypeCache()}
}

// add parses a module text; imports are bound when a module is compiled.
func (h *harness) add(src string) *Module {
	h.t.Helper()
	st, err := parser.Parse("test.yang", []byte(src), nil)
	if err != nil {
		h.t.Fatalf("parse: %v", err)
	}
	p, err := parser.Build(st)
	if err != nil {
		h.t.Fatalf("build: %v", err)
	}
	m := &schema.Module{Name: p.Name, Namespace: p.Namespace, Prefix: p.Prefix, Version: schema.Version1}
	if p.Version == "1.1" {
		m.Version = schema.Version11
	}
	if len(p.Revisions) > 0 {
		m.Revision = p.Revisions[0].Date
	}
	for _, id := range p.Identities {
		m.Identities = append(m.Identities, &schema.Identity{Name: id.Name, Module: m})
	}
	for _, f := range p.Features {
		m.Features = append(m.Features, &schema.Feature{Name: f.Name, Module: m})
	}
	lm := &Module{pmod: pmod{Parsed: p, mod: m}, Name: m.Name, Revision: m.Revision, Namespace: m.Namespace}
	h.byName[p.Name] = lm
	h.parsed[m] = lm
	return lm
}

// addDir adds every .yang file of a corpus directory.
func (h *harness) addDir(dir string) {
	h.t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.yang"))
	if err != nil || len(files) == 0 {
		h.t.Fatalf("no modules in %s: %v", dir, err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // corpus path
		if err != nil {
			h.t.Fatal(err)
		}
		h.add(string(b))
	}
}

func (h *harness) bind(m *Module) {
	h.t.Helper()
	for _, im := range m.Parsed.Imports {
		dep := h.byName[im.Name]
		if dep == nil {
			h.t.Fatalf("%s: import %s not added", m.Name, im.Name)
		}
		m.Imports = append(m.Imports, dep)
		m.mod.Imports = append(m.mod.Imports, schema.Import{Prefix: im.Prefix, Module: dep.mod})
	}
}

// compiledLeaf is a leaf or leaf-list with its compiled type and the typedef default it inherits.
type compiledLeaf struct {
	path string
	node *schema.Node
	dflt *typeDefault
}

// compileErr is a failed type with the schema path it is reported at.
type compileErr struct {
	path string
	err  error
}

func (e *compileErr) Error() string { return e.path + ": " + e.err.Error() }

func (e *compileErr) Unwrap() error { return e.err }

// compile compiles the types of every data leaf of module name (one "Load": fresh budget).
func (h *harness) compile(name string) ([]compiledLeaf, error) {
	h.t.Helper()
	m := h.byName[name]
	if m == nil {
		h.t.Fatalf("module %s not added", name)
	}
	for _, other := range h.byName {
		if len(other.Imports) != len(other.Parsed.Imports) {
			h.bind(other)
		}
	}
	pm := &m.pmod
	pm.mod.Implemented = true
	c := &typeCtx{cur: pm.mod, pmod: pm, parsed: h.parsed, cache: h.cache, budget: h.budget, iff: h.iff}
	h.last = c
	var out []compiledLeaf
	var walk func(nodes []*parser.Node, sc *scope, path string, parent schema.Status) error
	walk = func(nodes []*parser.Node, sc *scope, path string, parent schema.Status) error {
		for _, n := range nodes {
			st := parent
			if n.Status != "" {
				st = parsedStatus(n.Status)
			}
			p := path + "/" + n.Name
			if path == "" {
				p = "/" + name + ":" + n.Name
			}
			switch n.Kind {
			case "leaf", "leaf-list":
				k := schema.Leaf
				if n.Kind == "leaf-list" {
					k = schema.LeafList
				}
				sn := &schema.Node{Kind: k, Name: n.Name, Module: pm.mod, Status: st}
				t, _, dflt, err := c.compileNodeType(sc, sn, n.Type, pm, n.Units == nil)
				if err != nil {
					return &compileErr{p, err}
				}
				sn.Type = t
				out = append(out, compiledLeaf{p, sn, dflt})
			case "container", "list", "choice", "case":
				if err := walk(n.Children, &scope{n, sc}, p, st); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err := walk(pm.Parsed.Children, nil, "", schema.Current)
	return out, err
}

// iff evaluates if-features against the harness modules' feature flags.
func (h *harness) iff(pm *pmod, ifs []*parser.IfFeature) (bool, error) {
	for _, f := range ifs {
		if f.Err != nil {
			return false, f.Err
		}
		var lookupErr error
		ok := f.AST.Eval(func(name string) bool {
			prefix, fname := "", name
			if i := strings.IndexByte(name, ':'); i >= 0 {
				prefix, fname = name[:i], name[i+1:]
			}
			if m := pm.resolve(prefix); m != nil {
				if ft := m.Feature(fname); ft != nil {
					return ft.Enabled
				}
			}
			lookupErr = errors.New("unknown feature " + name)
			return false
		})
		if lookupErr != nil {
			return false, lookupErr
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// leaf returns the compiled leaf with the path.
func leafAt(t *testing.T, ls []compiledLeaf, path string) *compiledLeaf {
	t.Helper()
	for i := range ls {
		if ls[i].path == path {
			return &ls[i]
		}
	}
	t.Fatalf("no leaf %s", path)
	return nil
}
