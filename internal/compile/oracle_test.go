// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// The goldens of conformance/corpus/ctypes (oracle op "schema", libyang v5.8.6) replayed through
// the type compiler: each request module is compiled in request order with one shared typedef
// cache, and every leaf type of the golden schema_tree must match, as must the first diagnostic
// and the module rc of a rejected module. Leafref targets need the unres phase (C7); until then
// the target's module is compared with the module the path's last prefix binds to, which is
// what decides the binding in the shared-member cases.

type goldenSchema struct {
	Verdict string `json:"verdict"`
	Modules []struct {
		Name     string `json:"name"`
		Accepted bool   `json:"accepted"`
		Rc       *struct {
			Name string `json:"name"`
		} `json:"rc"`
		Diagnostics []struct {
			VecodeName string  `json:"vecode_name"`
			SchemaPath *string `json:"schema_path"`
			Msg        string  `json:"msg"`
		} `json:"diagnostics"`
		Tree []struct {
			Path     string   `json:"path"`
			Type     *gType   `json:"type"`
			Defaults []string `json:"defaults"`
		} `json:"schema_tree"`
	} `json:"modules"`
}

type gType struct {
	Base           string   `json:"base"`
	Typedefs       []string `json:"typedefs"`
	Range          *string  `json:"range"`
	Length         *string  `json:"length"`
	Patterns       []gPat   `json:"patterns"`
	FractionDigits *int     `json:"fraction_digits"`
	Enums          []gEnum  `json:"enums"`
	Bits           []gBit   `json:"bits"`
	Bases          []string `json:"bases"`
	Leafref        *gLref   `json:"leafref"`
	Union          []*gType `json:"union"`
}

type gPat struct {
	Expr   string `json:"expr"`
	Invert bool   `json:"invert"`
}

type gEnum struct {
	Name  string `json:"name"`
	Value int32  `json:"value"`
}

type gBit struct {
	Name     string `json:"name"`
	Position uint32 `json:"position"`
}

type gLref struct {
	Path            string  `json:"path"`
	RequireInstance bool    `json:"require_instance"`
	Target          *string `json:"target"`
}

// fmtBound formats a range bound like the oracle (lyoracle.c fmt_bound).
func fmtBound(t *schema.Type, s int64, u uint64) string {
	switch {
	case t.Base == schema.Dec64:
		a := uint64(s) //nolint:gosec // magnitude below
		sign := ""
		if s < 0 {
			a, sign = -a, "-"
		}
		p := uint64(1)
		for range t.FracDigits {
			p *= 10
		}
		return fmt.Sprintf("%s%d.%0*d", sign, a/p, int(t.FracDigits), a%p)
	case t.Base < schema.Dec64:
		return fmt.Sprint(u)
	}
	return fmt.Sprint(s)
}

func fmtRange(t *schema.Type, r *schema.Range) *string {
	if r == nil {
		return nil
	}
	var b strings.Builder
	for i, p := range r.Parts {
		if i > 0 {
			b.WriteString(" | ")
		}
		lo, hi := fmtBound(t, p.Min, p.MinU), fmtBound(t, p.Max, p.MaxU)
		b.WriteString(lo)
		if lo != hi {
			b.WriteString(".." + hi)
		}
	}
	s := b.String()
	return &s
}

// dumpType renders t as the oracle's type_json; a leafref target becomes the name of the module
// the path's last prefix (or "") binds to.
func dumpType(t *schema.Type) *gType {
	g := &gType{Base: t.Base.String()}
	if t.Typedef != "" {
		g.Typedefs = []string{t.Typedef}
	}
	switch t.Base {
	case schema.String, schema.Binary:
		g.Length = fmtRange(t, t.Length)
	case schema.Int8, schema.Int16, schema.Int32, schema.Int64, schema.Uint8, schema.Uint16, schema.Uint32,
		schema.Uint64, schema.Dec64:
		g.Range = fmtRange(t, t.Range)
	}
	for _, p := range t.Patterns {
		g.Patterns = append(g.Patterns, gPat{p.Expr, p.Invert})
	}
	if t.Base == schema.Dec64 {
		fd := int(t.FracDigits)
		g.FractionDigits = &fd
	}
	for _, e := range t.Enums {
		if !e.Disabled {
			g.Enums = append(g.Enums, gEnum{e.Name, e.Value})
		}
	}
	for _, b := range t.Bits {
		if !b.Disabled {
			g.Bits = append(g.Bits, gBit{b.Name, b.Position})
		}
	}
	for _, id := range t.Bases {
		g.Bases = append(g.Bases, id.Module.Name+":"+id.Name)
	}
	if t.Base == schema.Leafref {
		prefix := ""
		if i := strings.LastIndexByte(t.Path, ':'); i >= 0 {
			j := strings.LastIndexAny(t.Path[:i], "/[ ")
			prefix = t.Path[j+1 : i]
		}
		var mod *string
		if m := t.Prefixes[prefix]; m != nil {
			mod = &m.Name
		}
		g.Leafref = &gLref{t.Path, t.RequireInstance, mod}
	}
	for _, m := range t.Union {
		g.Union = append(g.Union, dumpType(m))
	}
	return g
}

// targetModules replaces every leafref target path by the module of its last node.
func targetModules(g *gType) {
	if g.Leafref != nil && g.Leafref.Target != nil {
		p := *g.Leafref.Target
		i := strings.LastIndexByte(p, ':')
		m := p[strings.LastIndexByte(p[:i], '/')+1 : i]
		g.Leafref.Target = &m
	}
	for _, u := range g.Union {
		targetModules(u)
	}
}

func loadSchemaGolden(t *testing.T, name string) *goldenSchema {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ctypesDir, "golden", name+".json")) //nolint:gosec // corpus path
	if err != nil {
		t.Fatal(err)
	}
	var g goldenSchema
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return &g
}

var ctypesDir = filepath.Join("..", "..", "conformance", "corpus", "ctypes")

func TestOracleSchemaTypes(t *testing.T) {
	for _, fx := range []struct{ golden, dir string }{
		{"unchanged-typedef-reuse", "reuse"},
		{"typedef-cycle", "cycle"},
		{"restriction-wrong-type", "wrongtype"},
		{"compile-errors", "errors"},
		{"union-nested-index", "union-nested"},
		{"lref-typedef-prefix-direct", "lref-direct"},
		{"lref-union-typedef-direct", "lref-union-direct"},
		{"lref-union-typedef-via-derived-typedef", "lref-via-derived"},
		{"lref-typedef-across-loads", "lref-across"},
	} {
		t.Run(fx.golden, func(t *testing.T) {
			g := loadSchemaGolden(t, fx.golden)
			h := newHarness(t)
			h.addDir(filepath.Join(ctypesDir, fx.dir))
			for _, gm := range g.Modules {
				leaves, err := h.compile(gm.Name)
				if !gm.Accepted {
					checkRejected(t, gm.Name, err, gm.Rc.Name, gm.Diagnostics[0].VecodeName,
						*gm.Diagnostics[0].SchemaPath, gm.Diagnostics[0].Msg)
					continue
				}
				if err != nil {
					t.Fatalf("%s: %v, golden accepted", gm.Name, err)
				}
				for _, n := range gm.Tree {
					if n.Type == nil {
						continue
					}
					l := leafAt(t, leaves, n.Path)
					targetModules(n.Type)
					if got := dumpType(l.node.Type); !reflect.DeepEqual(got, n.Type) {
						gj, _ := json.Marshal(got)
						wj, _ := json.Marshal(n.Type)
						t.Errorf("%s:\n got  %s\n want %s", n.Path, gj, wj)
					}
					if l.dflt != nil && (len(n.Defaults) != 1 || n.Defaults[0] != l.dflt.lex) {
						t.Errorf("%s: typedef default %q, golden %v", n.Path, l.dflt.lex, n.Defaults)
					}
				}
			}
		})
	}
}

func checkRejected(t *testing.T, mod string, err error, rc, vecode, path, msg string) {
	t.Helper()
	var ce *compileErr
	var ve *vErr
	if !errors.As(err, &ce) || !errors.As(err, &ve) {
		t.Fatalf("%s: got %v, golden rejected: %s", mod, err, msg)
	}
	if ce.path != path || ve.Code.String() != vecode || ve.Err != rc {
		t.Errorf("%s: got %s %s %s, golden %s %s %s", mod, ve.Err, ve.Code, ce.path, rc, vecode, path)
	}
	// pattern errors carry the regex engine's own text (libyang: PCRE2)
	if ve.Msg != msg && !strings.HasPrefix(msg, "Regular expression ") {
		t.Errorf("%s: message\n got  %s\n want %s", mod, ve.Msg, msg)
	}
}
