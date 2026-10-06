// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

// pv2Set compiles the protocol-v2 module name.
func pv2Set(t *testing.T, name string) *schema.Set {
	t.Helper()
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS(filepath.Join(pv2Dir, "schemas")))
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load(name, "", nil); err != nil {
		t.Fatal(err, diags)
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	return set
}

// diffStep is a step of a seq/diff-* fixture: a set (the JSON input of a parse step written as
// NewPath calls) or a validate with data_type config.
type diffStep struct{ set, value string }

var validateStep = diffStep{}

// implicitDiffs are the implicit_diff strings of a golden's validate steps, "" for null.
func implicitDiffs(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(pv2Dir, "golden", name+".json")) //nolint:gosec // corpus path
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Steps []struct {
			Do   string  `json:"do"`
			Diff *string `json:"implicit_diff"`
		}
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range g.Steps {
		if s.Do == "validate" {
			d := ""
			if s.Diff != nil {
				d = *s.Diff
			}
			out = append(out, d)
		}
	}
	return out
}

// TestDiffMerge: the merges no validation fixture reaches (a node deleted, then created again in
// one diff): delete + create of another value is a replace with orig-value and orig-default,
// a further delete restores the original value and default flag; delete + create of the same
// value is none, kept while the default flag differs; create + delete of a term is none with
// orig-default (and removed as redundant); an operation merged into an incompatible one fails.
func TestDiffMerge(t *testing.T) {
	set := pv2Set(t, "pv2-diff")
	node := func(value string, dflt bool) *Node {
		tr := newTree(set)
		if _, err := tr.NewPath("/pv2-diff:c/d", value, NewPathOptions{}); err != nil {
			t.Fatal(err)
		}
		n, err := tr.Find("/pv2-diff:c/d")
		if err != nil {
			t.Fatal(err)
		}
		if dflt {
			n.flags |= FlagDefault
		}
		return n
	}
	metas := func(n *Node) map[string]string {
		m := map[string]string{}
		for _, x := range n.meta {
			m[x.mod.Name+":"+x.name] = x.value.Canonical()
		}
		return m
	}
	dOf := func(dt *Tree) *Node {
		for c := range dt.Top() {
			for n := range c.Children() {
				return n
			}
		}
		return nil
	}

	dt := newTree(set)
	explicit, dflt := node("9", false), node("5", true)
	if err := dt.valDiffAdd(explicit, diffDelete); err != nil {
		t.Fatal(err)
	}
	if err := dt.valDiffAdd(dflt, diffCreate); err != nil {
		t.Fatal(err)
	}
	d := dOf(dt)
	if got := metas(d); d.value.Canonical() != "5" || d.flags&FlagDefault == 0 || got["yang:operation"] != "replace" ||
		got["yang:orig-value"] != "9" || got["yang:orig-default"] != "false" {
		t.Errorf("delete + create: %s %b %v", d.value.Canonical(), d.flags, got)
	}
	if err := dt.valDiffAdd(dflt, diffDelete); err != nil {
		t.Fatal(err)
	}
	if got := metas(d); d.value.Canonical() != "9" || d.flags&FlagDefault != 0 || len(got) != 1 || got["yang:operation"] != "delete" {
		t.Errorf("replace + delete: %s %b %v", d.value.Canonical(), d.flags, got)
	}

	dt = newTree(set)
	if err := dt.valDiffAdd(node("5", false), diffDelete); err != nil {
		t.Fatal(err)
	}
	if err := dt.valDiffAdd(dflt, diffCreate); err != nil {
		t.Fatal(err)
	}
	if d := dOf(dt); d == nil || metas(d)["yang:operation"] != "none" || metas(d)["yang:orig-default"] != "false" {
		t.Errorf("delete + create of the same value: %v", d)
	}

	dt = newTree(set)
	if err := dt.valDiffAdd(dflt, diffCreate); err != nil {
		t.Fatal(err)
	}
	if err := dt.valDiffAdd(dflt, diffDelete); err != nil {
		t.Fatal(err)
	}
	if dt.top.len() != 0 {
		t.Errorf("create + delete left %d nodes", dt.top.len())
	}

	dt = newTree(set)
	if err := dt.valDiffAdd(dflt, diffCreate); err != nil {
		t.Fatal(err)
	}
	err := dt.valDiffAdd(dflt, diffCreate)
	if err == nil || err.Error() != `Unable to merge operation "create" with "create" for node "/pv2-diff:c/d".` {
		t.Errorf("create + create: %v", err)
	}
}

// TestValidateDiffGoldens replays the implicit diffs of the seq/diff-* fixtures (pv2-diff) and of
// protocol-v2/choice-default-case (pv2): every validate step's diff printed as the oracle prints
// it (JSON, LYD_PRINT_WD_ALL), null when the validation changed nothing.
func TestValidateDiffGoldens(t *testing.T) {
	set := func(path, value string) diffStep { return diffStep{set: path, value: value} }
	for _, f := range []struct {
		module, golden string
		steps          []diffStep
	}{
		{"pv2-diff", "seq-diff-defaults", []diffStep{set("/pv2-diff:c/mode", "off"), validateStep}},
		{"pv2-diff", "seq-diff-when-delete", []diffStep{set("/pv2-diff:c/mode", "on"), set("/pv2-diff:c/w", "a"),
			set("/pv2-diff:c/wc/y", "b"), validateStep, set("/pv2-diff:c/mode", "off"), validateStep}},
		{"pv2-diff", "seq-diff-when-same-pass", []diffStep{set("/pv2-diff:c/mode", "off"), set("/pv2-diff:c/wc/y", "b"), validateStep}},
		{"pv2-diff", "seq-diff-case-replace", []diffStep{set("/pv2-diff:c/mode", "off"), validateStep,
			set("/pv2-diff:c/c2a", "x"), validateStep}},
		{"pv2-diff", "seq-diff-userord-list", []diffStep{set("/pv2-diff:c/mode", "off"), set("/pv2-diff:c/ol[k='x']", ""),
			set("/pv2-diff:c/ol[k='y']", ""), validateStep}},
		{"pv2-diff", "seq-diff-explicit-default", []diffStep{set("/pv2-diff:c/mode", "on"), set("/pv2-diff:c/wc/y", "b"),
			set("/pv2-diff:c/wc/z", "9"), validateStep, set("/pv2-diff:c/mode", "off"), validateStep}},
		{"pv2", "choice-default-case", []diffStep{validateStep, set("/pv2:c/s", "picked"), validateStep}},
	} {
		t.Run(f.golden, func(t *testing.T) {
			want := implicitDiffs(t, f.golden)
			tr := newTree(pv2Set(t, f.module))
			i := 0
			for _, s := range f.steps {
				if s.set != "" {
					if _, err := tr.NewPath(s.set, s.value, NewPathOptions{Update: true}); err != nil {
						t.Fatal(s.set, err)
					}
					continue
				}
				diff, _, _ := tr.ValidateDiff(context.Background(), ValidateOptions{NoState: true, MultiError: true})
				got := ""
				if diff != nil {
					var b bytes.Buffer
					if err := diff.PrintJSON(&b, PrintOptions{WithDefaults: WDAll}); err != nil {
						t.Fatal(err)
					}
					got = b.String()
				}
				if got != want[i] {
					t.Errorf("validate %d:\n%s\nwant:\n%s", i, got, want[i])
				}
				i++
			}
		})
	}
}
