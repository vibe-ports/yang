// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	mathrand "math/rand"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
		s += " = " + n.opaq.Value // the oracle's canonical of an opaque node: its text
	}
	return s
}

func typedDump(tr *Tree) []string {
	out := []string{}
	for top := range tr.Top() {
		for n := range top.allAny() {
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

// TestFindPathRelative covers lyd_find_path's LY_PATH_BEGIN_EITHER lookup from the first
// top-level node. Relative evaluation starts at that node's children, not at the node itself.
func TestFindPathRelative(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	mustLink(t, tr.insert(nil, c, insertDefault))
	z := f.term(t, f.z, "value")
	mustLink(t, tr.insert(c, z, insertDefault))
	f.list(t, tr, c, "a", "")

	if got, err := tr.Find("z"); err != nil || got != z {
		t.Fatalf("Find(relative) = %v, %v; want z", got, err)
	}
	if got, err := tr.Find("x"); err != nil || got != nil {
		t.Fatalf("Find(missing relative) = %v, %v; want nil, nil", got, err)
	}
	if got, err := tr.findPath(&logger{set: f.set}, "l[k='a']/v"); got != nil || !errors.Is(err, errPartial) {
		t.Fatalf("findPath(partial relative) = %v, %v; want errPartial", got, err)
	}
	if _, err := tr.Find("z["); err == nil || err.Error() != "Unexpected XPath expression end." {
		t.Fatalf("Find(malformed relative) = %v; want LY_EVALID XPath parse error", err)
	} else {
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.RC() != "LY_EVALID" {
			t.Fatalf("Find(malformed relative) = %T %v; want LY_EVALID", err, err)
		}
	}
	// ly_path_parse logs at ctx_node (LOG_LOCSET, path.c:340): the first top-level node's schema.
	for _, path := range []string{"z[", "/b:c["} {
		var ve *ValidationError
		if _, err := tr.Find(path); !errors.As(err, &ve) || len(ve.Diags) == 0 || ve.Diags[0].SchemaPath == "" {
			t.Fatalf("Find(%q) = %v; want a parse item located at the context node", path, err)
		}
	}
	// lyxp skips XML whitespace before the first token: still an absolute path.
	if got, err := tr.Find(" \t/b:c/z"); err != nil || got != z {
		t.Fatalf("Find(whitespace absolute) = %v, %v; want z", got, err)
	}
	if got, err := tr.Find(" z"); err != nil || got != z {
		t.Fatalf("Find(whitespace relative) = %v, %v; want z", got, err)
	}
	for _, path := range []string{"/b:c/z \t\r\n", " /b:c / z "} {
		if got, err := tr.Find(path); err != nil || got != z {
			t.Fatalf("Find(%q) = %v, %v; want z", path, got, err)
		}
	}
	if _, err := tr.Find(" \t\r\n"); err == nil {
		t.Fatal("Find(whitespace only) succeeded; want LY_EVALID XPath parse error")
	}
	if _, err := tr.Find(""); err == nil || err.Error() != "Unexpected XPath expression end." {
		t.Fatalf("Find(empty) = %v; want LY_EVALID XPath parse error", err)
	}
}

func TestFindPathRelativeContextArguments(t *testing.T) {
	f := newFixture()

	empty := newTree(f.set)
	if got, err := empty.Find("z"); err != nil || got != nil {
		t.Fatalf("empty.Find(relative) = %v, %v; want nil, nil", got, err)
	}

	opaqueTree := newTree(f.set)
	mustLink(t, opaqueTree.insert(nil, newOpaque(opaque{Name: "unknown", ModuleNS: "urn:unknown", Format: types.FormatJSON}), insertDefault))
	for _, path := range []string{"/b:c", "z"} {
		_, err := opaqueTree.Find(path)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || err.Error() != "Invalid argument ctx_node->schema (lyd_find_path())." || len(ve.Diags) != 0 {
			t.Fatalf("opaque.Find(%q) = %v; want ctx_node->schema LY_EINVAL", path, err)
		}
	}

	leaf := newTree(f.set)
	top := f.term(t, f.top, "value")
	mustLink(t, leaf.insert(nil, top, insertDefault))
	// ly_path_compile resolves a relative path against the leaf's schema: no child, LY_EVALID.
	if _, err := leaf.Find("z"); err == nil {
		t.Fatal("top-leaf.Find(relative) succeeded; want the compile error")
	}
	if _, err := leaf.Find("z["); err == nil || err.Error() != "Unexpected XPath expression end." {
		t.Fatalf("top-leaf.Find(malformed relative) = %v; want XPath parse error", err)
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

// TestMergeRepeatedKey merges parse-only instances with repeated JSON key members. libyang keeps
// both malformed source instances separate even though their leading key values are the same.
func TestMergeRepeatedKey(t *testing.T) {
	set := editSet(t)
	tr := newTree(set)
	for _, key := range []string{"m", "z"} {
		if _, err := tr.NewPath(fmt.Sprintf("/pv2-edit:c/l[k='%s']", key), "", NewPathOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	src, diags, err := parseWith(context.Background(), strings.NewReader(
		`{"pv2-edit:c":{"l":[{"k":"a","k":"b","v":1},{"k":"a","k":"c","v":2}]}}`), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
	if err != nil {
		t.Fatal(err, diags)
	}
	if err := tr.Merge(src); err != nil {
		t.Fatal(err)
	}
	c := tr.top.list[0]
	if got := len(c.kids.list); got != 4 {
		t.Fatalf("%d list instances after merge, want 4", got)
	}
	want := []string{
		"/pv2-edit:c new",
		"/pv2-edit:c/l[k='a'][k='b'] new",
		"/pv2-edit:c/l[k='a'][k='b']/k new = a",
		"/pv2-edit:c/l[k='a'][k='b']/k new = b",
		"/pv2-edit:c/l[k='a'][k='b']/v new = 1",
		"/pv2-edit:c/l[k='a'][k='c'] new",
		"/pv2-edit:c/l[k='a'][k='c']/k new = a",
		"/pv2-edit:c/l[k='a'][k='c']/k new = c",
		"/pv2-edit:c/l[k='a'][k='c']/v new = 2",
		"/pv2-edit:c/l[k='m'] new",
		"/pv2-edit:c/l[k='m']/k new = m",
		"/pv2-edit:c/l[k='z'] new",
		"/pv2-edit:c/l[k='z']/k new = z",
	}
	if got := typedDump(tr); !reflect.DeepEqual(got, want) {
		t.Fatalf("merged:\n got  %q\n want %q", got, want)
	}
}

// TestMergeRepeatedKeyMultikey verifies both malformed member orders against a valid (x,z)
// destination. For a,b,a the hash gate passes but the positional key loop rejects child[1]=a
// against destination b=z; for a,a,b one side has hash 0, so the hash gate itself rejects it.
// In both cases libyang therefore inserts the source instance instead of merging it.
func TestMergeRepeatedKeyMultikey(t *testing.T) {
	set := pv2Set(t, "pv2-sort-keys")
	cSchema := set.Implemented("pv2-sort-keys").Top[0]
	lSchema := schema.FindChild(cSchema, nil, cSchema.Module, "l", 0)
	a := schema.FindChild(lSchema, nil, lSchema.Module, "a", 0)
	b := schema.FindChild(lSchema, nil, lSchema.Module, "b", 0)
	for _, tc := range []struct {
		name  string
		order []struct {
			sn  *schema.Node
			val string
		}
	}{
		{"hashed-a-b-a", []struct {
			sn  *schema.Node
			val string
		}{{a, "x"}, {b, "z"}, {a, "a"}}},
		{"unhashed-a-a-b", []struct {
			sn  *schema.Node
			val string
		}{{a, "x"}, {a, "a"}, {b, "z"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := newTree(set)
			if _, err := dst.NewPath("/pv2-sort-keys:c/l[a='x'][b='z']", "", NewPathOptions{}); err != nil {
				t.Fatal(err)
			}
			src := newTree(set)
			c := newInner(cSchema)
			mustLink(t, src.insert(nil, c, insertDefault))
			list := newInner(lSchema)
			for _, member := range tc.order {
				v, d := types.Store(member.sn.Type, member.val, types.FormatJSON,
					types.JSONHints("string")|types.HintStringDatatypes, nil, member.sn)
				if d != nil {
					t.Fatal(d.Msg)
				}
				mustLink(t, src.insert(list, newTerm(member.sn, v), insertDefault))
			}
			// The low-level libyang merge accepts an already linked malformed list; force the
			// equivalent state because the public parser rejects its missing positional key.
			mustLink(t, src.insert(c, list, insertDefault))
			if err := dst.Merge(src); err != nil {
				t.Fatal(err)
			}
			if got := len(dst.top.list[0].kids.list); got != 2 {
				t.Fatalf("%d list instances after merge, want 2", got)
			}
		})
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
	mustLink(t, tr.insert(np, d, insertDefault))
	if n, err := tr.NewPath("/pv2-edit:c/np/d", "5", NewPathOptions{}); n != d || err != nil ||
		d.flags&FlagDefault != 0 || np.flags&FlagDefault != 0 || d.flags&FlagNew != 0 {
		t.Errorf("default leaf: %v %v flags %v %v", n, err, d.flags, np.flags)
	}
}

// TestMergeWork: merging n keyed instances into a level holding them costs O(n) dup-inst work,
// including the context-mix check at every sibling lookup and context bookkeeping on mutation.
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
	if limit := 13 * n; int(tr.work.Load()) > limit {
		t.Fatalf("merging %d equal instances: %d units of work, limit %d", n, int(tr.work.Load()), limit)
	}
}

// TestInsertScaling: parsing sorted, reverse, and shuffled runs and merging duplicate-heavy runs
// stay linear or n log n. The deterministic work counter includes comparisons, sibling visits,
// and shifted slots; 4x the input may cost at most 6x the work (not the 16x of quadratic growth).
func TestInsertScaling(t *testing.T) {
	set := editSet(t)
	measured := func(tr *Tree) *Tree {
		tr.work.Store(0)
		tr.work.visits, tr.work.shifts = true, true
		return tr
	}
	build := func(n int, val func(i int) int) *Tree {
		tr := newTree(set)
		for i := range n {
			if _, err := tr.NewPath("/pv2-edit:c/ll", fmt.Sprintf("v%07d", val(i)), NewPathOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		return tr
	}
	parseWork := func(order []int) int64 {
		vals := make([]string, len(order))
		for i, v := range order {
			vals[i] = fmt.Sprintf(`"v%07d"`, v)
		}
		in := `{"pv2-edit:c":{"ll":[` + strings.Join(vals, ",") + `]}}`
		var lc *lydCtx
		tr, diags, err := parseWith(context.Background(), strings.NewReader(in), set,
			parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON,
			func(l *lydCtx) {
				lc = l
				l.tree.work.visits, l.tree.work.shifts = true, true
			})
		if err != nil {
			t.Fatal(err, diags)
		}
		if got := len(tr.top.list[0].kids.list); got != len(order) {
			t.Fatalf("%d parsed instances, want %d", got, len(order))
		}
		return lc.tree.work.Load()
	}
	cases := []struct {
		name string
		work func(n int) int64
	}{
		{"parse-sorted", func(n int) int64 {
			order := make([]int, n)
			for i := range order {
				order[i] = i
			}
			return parseWork(order)
		}},
		{"parse-reverse", func(n int) int64 {
			order := make([]int, n)
			for i := range order {
				order[i] = n - i
			}
			return parseWork(order)
		}},
		{"parse-shuffled", func(n int) int64 {
			return parseWork(mathrand.New(mathrand.NewSource(1)).Perm(n)) //nolint:gosec // deterministic shuffle
		}},
		{"new-path-reversed", func(n int) int64 {
			tr := measured(newTree(set))
			for i := range n {
				if _, err := tr.NewPath("/pv2-edit:c/ll", fmt.Sprintf("v%07d", n-i), NewPathOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			return tr.work.Load()
		}},
		{"merge-interleaved", func(n int) int64 {
			tr := measured(build(n, func(i int) int { return 2 * i }))
			if err := tr.Merge(build(n, func(i int) int { return 2*i + 1 })); err != nil {
				t.Fatal(err)
			}
			if got := len(tr.top.list[0].kids.list); got != 2*n {
				t.Fatalf("%d instances after the merge, want %d", got, 2*n)
			}
			if !slices.IsSortedFunc(tr.top.list[0].kids.list, compareSorted) {
				t.Fatal("merged run not sorted")
			}
			return tr.work.Load()
		}},
		{"merge-duplicate-heavy", func(n int) int64 {
			// a parse-only source [v1, v1, v3, v3, ...] into [v0, v2, ...]: one batch, the equal
			// instance matches the pending copy
			vals := make([]string, 0, 2*n)
			for i := range n {
				v := fmt.Sprintf(`"v%07d"`, 2*i+1)
				vals = append(vals, v, v)
			}
			in := `{"pv2-edit:c":{"ll":[` + strings.Join(vals, ",") + `]}}`
			src, diags, err := parseWith(context.Background(), strings.NewReader(in), set,
				parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
			if err != nil {
				t.Fatal(err, diags)
			}
			tr := measured(build(n, func(i int) int { return 2 * i }))
			if err := tr.Merge(src); err != nil {
				t.Fatal(err)
			}
			if l := tr.top.list[0].kids.list; len(l) != 2*n || !slices.IsSortedFunc(l, compareSorted) {
				t.Fatalf("%d instances after the merge, want %d, sorted", len(l), 2*n)
			}
			return tr.work.Load()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w1, w4 := tc.work(1000), tc.work(4000); w4 > 6*w1 {
				t.Fatalf("work %d for 1000 values, %d for 4000: not linear", w1, w4)
			}
		})
	}
}

// TestMergeSpliceHash: a splice that brings a level to htMinItems indexes each node once, so a
// key stays unique in the children table and the XPath looks it up there.
func TestMergeSpliceHash(t *testing.T) {
	set := editSet(t)
	tree := func(keys ...string) *Tree {
		tr := newTree(set)
		for _, k := range keys {
			if _, err := tr.NewPath(fmt.Sprintf("/pv2-edit:c/l[k='%s']", k), "", NewPathOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		return tr
	}
	tr := tree("a", "b")
	if err := tr.Merge(tree("c", "d")); err != nil {
		t.Fatal(err)
	}
	c := tr.top.list[0]
	if c.kids.ht == nil {
		t.Fatal("no children table")
	}
	for k, b := range c.kids.ht {
		if b.count != 1 {
			t.Errorf("bucket %v holds %d nodes", k, b.count)
		}
	}
	if hit, ok := (xn{c, tr.set}).LookupChild(wrapSchema(tr.set, c.kids.list[0].schema), []string{"d"}); !ok || len(hit) != 1 {
		t.Errorf("indexed lookup: %v %v", hit, ok)
	}
	if ns, _, err := tr.FindXPath("/pv2-edit:c/l[k='d']", XPathOptions{}); err != nil || len(ns) != 1 {
		t.Errorf("FindXPath: %v %v", ns, err)
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

// TestKeyLookupWork: a list-key lookup (Find, the instance-identifier and diff-anchor lookups)
// re-stores the keys and searches the run by value: n lookups among n instances stay n log n,
// work(4n) <= 6 x work(n), for lookups in sorted, reverse, shuffled and duplicate-heavy order.
func TestKeyLookupWork(t *testing.T) {
	set := editSet(t)
	work := func(n int, order string) int64 {
		tr := newTree(set)
		keys := make([]string, n)
		for i := range keys {
			keys[i] = fmt.Sprintf("k%06d", i)
			if _, err := tr.NewPath("/pv2-edit:c/l[k='"+keys[i]+"']/v", "1", NewPathOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		switch order {
		case "reverse":
			slices.Reverse(keys)
		case "shuffled":
			r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // deterministic test order
			r.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
		case "duplicates":
			for i := range keys {
				keys[i] = keys[i%3]
			}
		}
		tr.work.Store(0)
		tr.work.visits = true
		for _, k := range keys {
			if n, err := tr.Find("/pv2-edit:c/l[k='" + k + "']"); err != nil || n == nil {
				t.Fatalf("%s: %v %v", k, n, err)
			}
		}
		return tr.work.Load()
	}
	const n = 1500
	for _, order := range []string{"sorted", "reverse", "shuffled", "duplicates"} {
		w1, w4 := work(n, order), work(4*n, order)
		if w1 < n || w4 > 6*w1 { // each lookup counts at least one sibling visit
			t.Errorf("%s: work(%d) = %d, work(%d) = %d: more than 6x", order, n, w1, 4*n, w4)
		}
	}
}
