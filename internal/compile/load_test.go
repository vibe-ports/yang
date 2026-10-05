// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-ports/yang/internal/parser"
)

// TestReadBudget: a module file larger than Parse.MaxBytes is not read whole;
// setting features is C1b's.
func TestReadBudget(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "big.yang", "module big { namespace urn:big; prefix big; }"+string(make([]byte, 40000)))
	c, _, err := NewContext(Options{Parse: parser.Budget{MaxBytes: 30000}}, os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Load("big", "", nil); !errors.Is(err, ErrBudget) {
		t.Fatalf("got %v", err)
	}
	if _, _, err := c.Load("big", "", []string{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("features: got %v", err)
	}
}

const corpus = "../../conformance/corpus"

type goldenDiag struct {
	Phase string `json:"phase"`
	Level string `json:"level"`
	Code  struct {
		Name string `json:"name"`
	} `json:"code"`
	Vecode     string `json:"vecode_name"`
	SchemaPath string `json:"schema_path"`
	Line       int    `json:"line"`
	Msg        string `json:"msg"`
}

type goldenModule struct {
	Name        string       `json:"name"`
	Accepted    bool         `json:"accepted"`
	Phase       string       `json:"phase"`
	Revision    *string      `json:"revision"`
	Diagnostics []goldenDiag `json:"diagnostics"`
}

func (d Diagnostic) golden() goldenDiag {
	g := goldenDiag{Phase: d.Phase, Level: "error", Vecode: d.Code.String(), SchemaPath: d.SchemaPath, Line: d.Line, Msg: d.Msg}
	if d.Level == LevelWarning {
		g.Level = "warning"
	}
	g.Code.Name = d.Err
	return g
}

// TestLoadGoldens loads the modules of the conformance/corpus/load fixtures in
// order and compares the parse phase with libyang's: verdict, every
// diagnostic (message and line included) and the loaded revision.
func TestLoadGoldens(t *testing.T) {
	fixtures := []struct {
		id, dir string
		revs    map[string]string // requested revision per module name
	}{
		{"import-cycle", "import-cycle", nil},
		{"include-cycle", "include-cycle", nil},
		{"wrong-revision-file", "wrong-rev", map[string]string{"wr": "2020-01-01"}},
		{"imported-rev-binding", "imported-rev", nil}, // r@2020-01-01 is the second module, see below
		{"filename-warning", "filename", nil},
		{"import-not-found", "not-found", nil},
		{"symlink-dir", "symlink", nil},
		{"symlink-file", "symlink", nil},
		{"dup-typedef-scopes", "dup", nil},
		{"include-errors", "include", nil},
		{"submodule-collisions", "subcol", nil},
		{"ext-instance-resolution", "ext", nil},
		{"two-failures", "two-failures", nil},
	}
	for _, f := range fixtures {
		t.Run(f.id, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(corpus, "load", "golden", f.id+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var g struct{ Modules []goldenModule }
			if err := json.Unmarshal(b, &g); err != nil {
				t.Fatal(err)
			}
			c, _, err := NewContext(Options{}, os.DirFS(filepath.Join(corpus, "load", f.dir)))
			if err != nil {
				t.Fatal(err)
			}
			for i, gm := range g.Modules {
				rev := f.revs[gm.Name]
				if f.id == "imported-rev-binding" && i == 1 {
					rev = "2020-01-01"
				}
				m, diags, err := c.Load(gm.Name, rev, nil)
				var want []goldenDiag
				for _, d := range gm.Diagnostics {
					if d.Phase == "parse" {
						want = append(want, d)
					}
				}
				var got []goldenDiag
				for _, d := range diags {
					got = append(got, d.golden())
				}
				if (err != nil) != (gm.Phase == "parse") {
					t.Errorf("%s: err %v, golden phase %q", gm.Name, err, gm.Phase)
				}
				if js(got) != js(want) {
					t.Errorf("%s diagnostics:\n got %s\nwant %s", gm.Name, js(got), js(want))
				}
				if err == nil && gm.Accepted && gm.Revision != nil && m.Revision != *gm.Revision {
					t.Errorf("%s: revision %q, golden %q", gm.Name, m.Revision, *gm.Revision)
				}
				if f.id == "imported-rev-binding" && gm.Name == "ib" && m.Imports[0].Revision != "2021-01-01" {
					t.Errorf("ib imports r@%s, want the IMPORTED_REV module r@2021-01-01", m.Imports[0].Revision)
				}
			}
		})
	}
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestInternalModules(t *testing.T) {
	c, diags, err := NewContext(Options{NoYangLibrary: true})
	if err != nil || len(diags) != 0 {
		t.Fatal(err, diags)
	}
	var got []string
	for _, m := range c.Modules {
		s := m.Name + "@" + m.Revision
		if m.Implemented {
			s += "*"
		}
		got = append(got, s)
	}
	want := `["ietf-inet-types@2025-12-22","ietf-yang-types@2025-12-22","ietf-yang-metadata@2016-08-05*",` +
		`"yang@2025-01-29*","default@2025-06-18*","ietf-yang-schema-mount@2019-01-14*","ietf-yang-structure-ext@2020-06-17"]`
	if js(got) != want {
		t.Errorf("got  %s\nwant %s", js(got), want)
	}
}

// TestLoadM1 loads the m1 slice modules (they import the internal modules).
func TestLoadM1(t *testing.T) {
	c, _, err := NewContext(Options{}, os.DirFS(filepath.Join(corpus, "m1", "schemas")))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ietf-interfaces", "iana-if-type", "ietf-ip", "m1-ext"} {
		if _, diags, err := c.Load(n, "", nil); err != nil || len(diags) != 0 {
			t.Fatal(n, err, diags)
		}
	}
}

func write(t *testing.T, dir, name, src string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSymlinkCycle: libyang descends a symlink cycle until the path is longer
// than PATH_MAX; two looping links make that exponential, MaxSearchDirs stops it (U-0022).
func TestSymlinkCycle(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(".", filepath.Join(dir, "x")); err != nil {
		t.Skip("no symlinks:", err)
	}
	c, _, err := NewContext(Options{}, os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	// one loop: bounded by the path length, ends as "not found"
	start := time.Now()
	if _, _, err := c.Load("absent", "", nil); !errors.Is(err, eNotFound) {
		t.Fatalf("one loop: %v", err)
	}
	if err := os.Symlink(".", filepath.Join(dir, "y")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Load("absent", "", nil); !errors.Is(err, ErrBudget) {
		t.Fatalf("two loops: %v", err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestYinUnsupported(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "yn.yin", `<module name="yn" xmlns="urn:ietf:params:xml:ns:yang:yin:1"/>`)
	c, _, err := NewContext(Options{}, os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Load("yn", "", nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
	if len(c.Modules) != 9 {
		t.Errorf("%d modules after a failed load", len(c.Modules))
	}
}

// TestLoaderOrder: the callback is asked first unless PreferSearchdirs.
func TestLoaderOrder(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "lm.yang", "module lm { namespace urn:lm; prefix lm; revision 2001-01-01; }")
	loader := func(mod, _, sub, _ string) ([]byte, bool) {
		if mod == "lm" && sub == "" {
			return []byte("module lm { namespace urn:lm; prefix lm; revision 2002-02-02; }"), true
		}
		return nil, false
	}
	for _, prefer := range []bool{false, true} {
		c, _, err := NewContext(Options{Loader: loader, PreferSearchdirs: prefer}, os.DirFS(dir))
		if err != nil {
			t.Fatal(err)
		}
		m, _, err := c.Load("lm", "", nil)
		want := map[bool]string{false: "2002-02-02", true: "2001-01-01"}[prefer]
		if err != nil || m.Revision != want {
			t.Errorf("prefer=%v: %v %v, want %s", prefer, m, err, want)
		}
	}
}
