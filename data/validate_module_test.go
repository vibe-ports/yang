// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

var (
	reSeqMods = regexp.MustCompile(`(?m)^      modules: \[(.*)\]$`)
	reSeqMod  = regexp.MustCompile(`\{name: "?([\w-]+)"?\}`)
)

type yangDiag = yang.Diagnostic

// TestValidateModuleGoldens replays the seq/validate-module-* fixtures (protocol-v2) step by
// step as the oracle's sequence does: parse (parse only), validate (lyd_validate_all, or with
// module lyd_validate_module = validateModule, with final_only lyd_validate_module_final =
// validateModuleFinal), edit delete, dump. Every step's rc, diagnostics, implicit diff and the
// tree after it must equal the oracle's; the sequence stops at the first failing step.
func TestValidateModuleGoldens(t *testing.T) {
	paths, _ := filepath.Glob("../conformance/corpus/manifest.d/seq/validate-module-*.yaml")
	if len(paths) < 12 {
		t.Fatalf("%d fixtures", len(paths))
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(path) //nolint:gosec // corpus path
			if err != nil {
				t.Fatal(err)
			}
			c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS(filepath.Join(pv2Dir, "schemas")))
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range reSeqMod.FindAllStringSubmatch(reSeqMods.FindStringSubmatch(string(b))[1], -1) {
				if _, diags, err := c.Load(m[1], "", nil); err != nil {
					t.Fatal(err, diags)
				}
			}
			set := &schema.Set{}
			for _, m := range c.Modules {
				set.Modules = append(set.Modules, m.Schema)
			}
			var steps []map[string]any
			for _, line := range strings.Split(string(b), "\n") {
				if s, ok := strings.CutPrefix(line, "        - "); ok {
					var m map[string]any
					if err := json.Unmarshal([]byte(s), &m); err != nil {
						t.Fatal(err)
					}
					steps = append(steps, m)
				}
			}
			want := loadSteps(t, "seq-"+name)
			g := implicitDiffs(t, "seq-"+name)
			ctx := context.Background()
			o := ValidateOptions{NoState: true, MultiError: true} // data_type config
			var tr *Tree
			nv := 0
			for i, st := range steps {
				var diags []string
				rc := "LY_SUCCESS"
				var err error
				switch st["do"] {
				case "parse":
					unknown := Reject
					if st["unknown"] == "opaque" {
						unknown = Opaque
					}
					var ds []yangDiag
					tr, ds, err = parseWith(ctx, strings.NewReader(st["data"].(string)), set,
						parseOpts{ParseOptions: ParseOptions{Unknown: unknown, ParseOnly: true, NoState: true}}, parseJSON, nil)
					diags = diagLines(ds)
				case "validate":
					mod, _ := st["module"].(string)
					final, _ := st["final_only"].(bool)
					dt := newTree(set)
					var ds []yangDiag
					switch {
					case final:
						ds, err = tr.validateModuleFinal(ctx, set.Implemented(mod), o, Budget{})
					case mod != "":
						ds, err = tr.validateModule(ctx, set.Implemented(mod), o, Budget{}, dt.valDiffAdd)
					default:
						ds, err = tr.validateAll(ctx, o, Budget{}, dt.valDiffAdd)
					}
					diags = diagLines(ds)
					gotDiff := ""
					if dt.top.len() > 0 {
						var buf bytes.Buffer
						if err := dt.PrintJSON(&buf, PrintOptions{WithDefaults: WDAll}); err != nil {
							t.Fatal(err)
						}
						gotDiff = buf.String()
					}
					if gotDiff != g[nv] {
						t.Errorf("step %d implicit diff\n got  %s\n want %s", i, gotDiff, g[nv])
					}
					nv++
				case "edit":
					var n *Node
					lg := &logger{set: set}
					if n, err = tr.findPath(lg, st["delete"].(string)); err == nil {
						err = n.Remove()
					}
				case "dump":
				}
				if err != nil {
					rc = "LY_EVALID"
					var ve *ValidationError
					if !errors.As(err, &ve) {
						t.Fatalf("step %d: %v", i, err)
					}
				}
				w := want[i]
				if rc != w.RC.Name {
					t.Fatalf("step %d rc %s %v, want %s", i, rc, err, w.RC.Name)
				}
				if !reflect.DeepEqual(diags, w.diags()) {
					t.Errorf("step %d diagnostics\n got  %q\n want %q", i, diags, w.diags())
				}
				if rc != "LY_SUCCESS" {
					return // the sequence stops
				}
				if got, wd := typedDump(tr), w.dump(); !reflect.DeepEqual(got, wd) {
					t.Errorf("step %d tree\n got  %q\n want %q", i, got, wd)
				}
			}
		})
	}
}

// diagLines renders diagnostics as goldenStep.diags does.
func diagLines(ds []yangDiag) []string {
	var out []string
	for _, d := range ds {
		out = append(out, fmt.Sprintf("%s %s %s %s", d.Code, d.DataPath, d.SchemaPath, d.Msg))
	}
	return out
}
