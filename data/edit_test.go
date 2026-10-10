// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

const pv2Dir = "../conformance/corpus/protocol-v2"

// editSet compiles pv2-edit (protocol-v2/schemas), the module of the seq/* edit fixtures.
func editSet(t *testing.T) *schema.Set {
	t.Helper()
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS(filepath.Join(pv2Dir, "schemas")))
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load("pv2-edit", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	return set
}

// typedLine is the part of the oracle's typed dump the edits decide: path, flags, value.
func typedLine(set *schema.Set, n *Node) string {
	var fl []string
	for _, f := range []struct {
		f    Flags
		name string
	}{{FlagDefault, "default"}, {FlagNew, "new"}, {FlagWhenTrue, "when_true"}} {
		if n.flags&f.f != 0 {
			fl = append(fl, f.name)
		}
	}
	s := lydPath(set, n, false) + " " + strings.Join(fl, ",")
	switch {
	case n.isTerm():
		s += " = " + n.value.Canonical()
	case n.opaq != nil:
		s += " = " + n.opaq.Value // the oracle's typed dump gives an opaque node's value too
	}
	return s
}

func typedDump(tr *Tree) []string {
	out := []string{}
	for top := range tr.Top() {
		for n := range top.All() {
			out = append(out, typedLine(tr.set, n))
		}
	}
	return out
}

type goldenStep struct {
	Skipped     bool `json:"skipped"`
	Diagnostics []struct {
		Code       struct{ Name string } `json:"code"`
		Msg        string                `json:"msg"`
		VecodeName string                `json:"vecode_name"`
		DataPath   *string               `json:"data_path"`
		SchemaPath *string               `json:"schema_path"`
	} `json:"diagnostics"`
	RC    struct{ Name string } `json:"rc"`
	Typed []struct {
		Path  string          `json:"path"`
		Flags map[string]bool `json:"flags"`
		Value *struct {
			Canonical string `json:"canonical"`
		} `json:"value"`
	} `json:"typed"`
	Tree   struct{ JSON, XML string } `json:"tree"`
	Change struct{ Name string }      `json:"change"`
}

func (g goldenStep) dump() []string {
	out := []string{}
	for _, n := range g.Typed {
		var fl []string
		for _, f := range []string{"default", "new", "when_true"} {
			if n.Flags[f] {
				fl = append(fl, f)
			}
		}
		s := n.Path + " " + strings.Join(fl, ",")
		if n.Value != nil {
			s += " = " + n.Value.Canonical
		}
		out = append(out, s)
	}
	return out
}

func (g goldenStep) diags() []string {
	var out []string
	for _, d := range g.Diagnostics {
		dp, sp := "", ""
		if d.DataPath != nil {
			dp = *d.DataPath
		}
		if d.SchemaPath != nil {
			sp = *d.SchemaPath
		}
		out = append(out, fmt.Sprintf("%s %s %s %s", d.VecodeName, dp, sp, d.Msg))
	}
	return out
}

func loadSteps(t *testing.T, name string) []goldenStep {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(pv2Dir, "golden", name+".json")) //nolint:gosec // corpus path
	if err != nil {
		t.Fatal(err)
	}
	var g struct{ Steps []goldenStep }
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g.Steps
}

// editStep is one set or delete step of a seq/* fixture (as in the corpus manifest).
type editStep struct{ set, value, del string }

// apply runs a step the way the oracle's step_edit does; it returns the rc name libyang reports
// for a delete that finds nothing and the diagnostics of a failure.
func (s editStep) apply(tr *Tree) (rc string, diags []string) {
	var err error
	if s.del == "" {
		_, err = tr.NewPath(s.set, s.value, NewPathOptions{Update: true})
	} else {
		lg := &logger{set: tr.set}
		var n *Node
		if n, err = tr.findPath(lg, s.del); err == nil {
			err = n.Remove()
		} else {
			err = lg.done(err)
		}
	}
	switch {
	case errors.Is(err, errNotFound):
		return "LY_ENOTFOUND", nil
	case errors.Is(err, errPartial):
		return "LY_EINCOMPLETE", nil
	}
	var ve *ValidationError
	if errors.As(err, &ve) {
		for _, d := range ve.Diags {
			diags = append(diags, fmt.Sprintf("%s %s %s %s", d.Code, d.DataPath, d.SchemaPath, d.Msg))
		}
		return "error", diags
	} else if err != nil {
		return "error", nil // not logged
	}
	return "LY_SUCCESS", nil
}

// TestEditGoldens replays the protocol-v2 seq/* fixtures made of set/delete steps: every step's
// diagnostics and resulting tree (paths, flags, values) against the oracle's.
func TestEditGoldens(t *testing.T) {
	set := editSet(t)
	x := func(path, value string) editStep { return editStep{set: "/pv2-edit:c" + path, value: value} }
	del := func(path string) editStep { return editStep{del: "/pv2-edit:c" + path} }
	fixtures := map[string][]editStep{
		"seq-new-path-update": {x("/a", "x"), x("/l[k='b']/v", "1"), x("/l[k='a']/v", "2"), x("/a", "y"), x("/a", "y"),
			x("/ll", "z"), x("/ll[.='m']", "ignored"), x("/ll", "z"), x("/st/x", "1"), x("/st/x", "1"), x("/st[1]/x", "2"),
			x("/sll", "s"), x("/sll", "s"), x("/np/d", "6"), del("/l[k='b']"), del("/st[2]")},
		"seq-new-path-predicate-missing": {x("/a", "x"), x("/l/v", "1")},
		"seq-new-path-invalid-value":     {x("/a", "x"), x("/l[k='a']/v", "300")},
		"seq-new-path-invalid-update":    {x("/b", "1"), x("/b", "x")},
		"seq-new-path-bad-path":          {x("/nope", "1")},
		"seq-delete-not-found":           {x("/a", "x"), del("/l[k='a']/v")},
		"seq-delete-bad-path":            {x("/a", "x"), del("/l")},
		"seq-new-path-leaflist-invalid":  {x("/a", "x"), x("/nl", "300")},
	}
	for name, steps := range fixtures {
		t.Run(name, func(t *testing.T) {
			want := loadSteps(t, name)
			tr := newTree(set)
			for i, s := range steps {
				rc, diags := s.apply(tr)
				if w := want[i]; rc == "error" || strings.HasPrefix(w.RC.Name, "LY_E") {
					if !reflect.DeepEqual(diags, w.diags()) || rc != "error" && rc != w.RC.Name {
						t.Fatalf("step %d: %s %q\nwant %s %q", i, rc, diags, w.RC.Name, w.diags())
					}
				} else if rc != "LY_SUCCESS" {
					t.Fatalf("step %d: %s", i, rc)
				}
				if got, w := typedDump(tr), want[i].dump(); !reflect.DeepEqual(got, w) {
					t.Fatalf("step %d tree:\n got  %q\n want %q", i, got, w)
				}
				if want[i].RC.Name != "LY_SUCCESS" {
					return // the sequence stops at the first failing step
				}
			}
		})
	}
}

// TestMerge merges the content of data/edit-merge.json, built with NewPath, as the seq/merge-basic
// fixture does and compares the result with the oracle's.
func TestMerge(t *testing.T) {
	set := editSet(t)
	tr, src := newTree(set), newTree(set)
	for _, s := range []editStep{{set: "/pv2-edit:c/a", value: "x"}, {set: "/pv2-edit:c/l[k='a']/v", value: "1"},
		{set: "/pv2-edit:c/ll", value: "b"}, {set: "/pv2-edit:c/st/x", value: "1"}} {
		if rc, d := s.apply(tr); rc != "LY_SUCCESS" {
			t.Fatal(rc, d)
		}
	}
	for _, s := range []editStep{{set: "/pv2-edit:c/a", value: "m"}, {set: "/pv2-edit:c/l[k='c']"},
		{set: "/pv2-edit:c/l[k='a']/v", value: "3"}, {set: "/pv2-edit:c/ll", value: "x"}, {set: "/pv2-edit:c/ll", value: "b"},
		{set: "/pv2-edit:c/np/d", value: "7"}, {set: "/pv2-edit:c/st/x", value: "1"}, {set: "/pv2-edit:c/st/x", value: "1"}} {
		if rc, d := s.apply(src); rc != "LY_SUCCESS" {
			t.Fatal(rc, d)
		}
	}
	for top := range src.Top() { // New must come from the merge
		for n := range top.All() {
			n.flags &^= FlagNew
		}
	}
	before := typedDump(src)
	if err := tr.Merge(src); err != nil {
		t.Fatal(err)
	}
	steps := loadSteps(t, "seq-merge-basic")
	if got, want := typedDump(tr), steps[len(steps)-1].dump(); !reflect.DeepEqual(got, want) {
		t.Errorf("merged:\n got  %q\n want %q", got, want)
	}
	if !reflect.DeepEqual(typedDump(src), before) {
		t.Error("Merge changed its source")
	}
	if err := tr.Merge(newTree(&schema.Set{})); err == nil || !strings.Contains(err.Error(), "Different contexts") {
		t.Errorf("merge across sets: %v", err)
	}
}

// TestNewPathAPI: the cases the oracle's sequence cannot express (no UPDATE, return values,
// Find, key refusal).
func TestNewPathAPI(t *testing.T) {
	set := editSet(t)
	tr := newTree(set)
	n, err := tr.NewPath("/pv2-edit:c/l[k='a']/v", "1", NewPathOptions{})
	if err != nil || n == nil || n.Name() != "c" {
		t.Fatalf("first created node: %v %v", n, err)
	}
	if _, err := tr.NewPath("/pv2-edit:c/l[k='a']/v", "2", NewPathOptions{}); err == nil ||
		!strings.Contains(err.Error(), `Path "/pv2-edit:c/l[k='a']/v" already exists.`) {
		t.Errorf("existing leaf without Update: %v", err)
	}
	if n, err := tr.NewPath("/pv2-edit:c/l[k='a']/k", "zz", NewPathOptions{Update: true}); n != nil || err != nil {
		t.Errorf("key update: %v %v", n, err)
	}
	if n, err := tr.NewPath("/pv2-edit:c/l[k='a']/v", "1", NewPathOptions{Update: true}); n != nil || err != nil {
		t.Errorf("same value: %v %v", n, err)
	}
	if _, err := tr.NewPath("pv2-edit:c/a", "1", NewPathOptions{}); err == nil {
		t.Error("relative path accepted")
	}
	// lyd_new_path's return codes that are not those of its log: LY_EINVAL after the predicate
	// message, LY_EVALID with nothing logged for an invalid leaf-list value
	var ve *ValidationError
	if _, err := tr.NewPath("/pv2-edit:c/l/v", "1", NewPathOptions{}); !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" ||
		len(ve.Diags) != 1 || ve.Diags[0].Err != "LY_EVALID" {
		t.Errorf("predicate missing: %v", err)
	}
	if _, err := tr.NewPath("/pv2-edit:c/nl", "300", NewPathOptions{}); !errors.As(err, &ve) || ve.RC() != "LY_EVALID" ||
		len(ve.Diags) != 0 {
		t.Errorf("invalid leaf-list value: %v", err)
	}
	// a type plugin's own rc passes through: date (ly_time_str2time) fails with LY_EINVAL
	f := newFixture()
	dt := &schema.Type{Base: schema.String, Typedef: "date", TypedefModule: &schema.Module{Name: "ietf-yang-types"}}
	dl := &schema.Node{Kind: schema.LeafList, Name: "dl", Module: f.b, Parent: f.c, Type: dt, Config: true}
	f.c.Children = append(f.c.Children, dl)
	if _, err := newTree(f.set).NewPath("/b:c/dl", "2024-13-01", NewPathOptions{}); !errors.As(err, &ve) ||
		ve.RC() != "LY_EINVAL" || len(ve.Diags) != 0 {
		t.Errorf("invalid date leaf-list value: %v", err)
	}
	key, err := tr.Find("/pv2-edit:c/l[k='a']/k")
	if err != nil || key == nil || key.value.Canonical() != "a" {
		t.Fatalf("Find key: %v %v", key, err)
	}
	if err := key.Remove(); err == nil || !strings.Contains(err.Error(), `Cannot free a list key "k"`) {
		t.Errorf("key removal: %v", err)
	}
	if n, err := tr.Find("/pv2-edit:c/a"); n != nil || err != nil {
		t.Errorf("missing node: %v %v", n, err)
	}
	if n, err := newTree(set).Find("/pv2-edit:c"); n != nil || err != nil {
		t.Errorf("empty tree: %v %v", n, err)
	}
	// a default leaf is set without Update and loses its default flag, up the NP containers
	np, _ := tr.NewPath("/pv2-edit:c/np", "", NewPathOptions{})
	sn := np.schema.Children[0]
	v, dg := types.Store(sn.Type, "5", types.FormatJSON, types.HintData, nil, sn)
	if dg != nil {
		t.Fatal(dg.Msg)
	}
	d := newTerm(sn, v)
	d.flags = FlagDefault
	tr.insert(np, d, insertDefault)
	if n, err := tr.NewPath("/pv2-edit:c/np/d", "5", NewPathOptions{}); n != d || err != nil ||
		d.flags&FlagDefault != 0 || np.flags&FlagDefault != 0 || d.flags&FlagNew != 0 {
		t.Errorf("default leaf: %v %v flags %v %v", n, err, d.flags, np.flags)
	}
}

// TestMergeWork: merging n keyed instances into a level holding them costs O(n) dup-inst work
// (each instance's equal set comes from its own hash run, not from all siblings).
func TestMergeWork(t *testing.T) {
	set := editSet(t)
	build := func(n int) *Tree {
		tr := newTree(set)
		for i := range n {
			if _, err := tr.NewPath(fmt.Sprintf("/pv2-edit:c/l[k='%d']/v", i), "1", NewPathOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		return tr
	}
	const n = 4000
	tr, src := build(n), build(n)
	tr.work.Store(0)
	tr.work.visits = true
	if err := tr.Merge(src); err != nil {
		t.Fatal(err)
	}
	if limit := 8 * n; int(tr.work.Load()) > limit {
		t.Fatalf("merging %d equal instances: %d units of work, limit %d", n, int(tr.work.Load()), limit)
	}
}

// TestEditEdges: Merge(nil) merges nothing, a malformed path is rejected on an empty tree too.
func TestEditEdges(t *testing.T) {
	set := editSet(t)
	tr := newTree(set)
	if err := tr.Merge(nil); err != nil {
		t.Errorf("Merge(nil): %v", err)
	}
	var ve *ValidationError
	if _, err := tr.Find("/pv2-edit:c/l"); !errors.As(err, &ve) || ve.Diags[0].Msg != `Predicate missing for list "l" in path.` {
		t.Errorf("malformed path on an empty tree: %v", err)
	}
	// seq/new-path-leaflist-invalid replays the invalid leaf-list value
}
