// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

func requireD0122Refusal(t *testing.T, err error, fn string) {
	t.Helper()
	want := "Invalid arguments - the diff has nodes of another schema context, applying it is not supported (D-0122) (" + fn + "())."
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || len(ve.Diags) != 1 ||
		ve.Diags[0].Err != "LY_EINVAL" || ve.Diags[0].Code != "LYVE_SUCCESS" || ve.Diags[0].Msg != want {
		t.Fatalf("cross-context diff: %#v, want one LY_EINVAL/LYVE_SUCCESS diagnostic %q", err, want)
	}
}

// TestApplyDiffMixedSnapshotsRoundTrip checks that a target rejects every diff node from another
// context before changing, while a diff made wholly of the target's context remains applicable.
func TestApplyDiffMixedSnapshotsRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
		secondAllowed       bool
	}{
		{"nested-replace", `<x><x><a>old</a><b>same</b></x></x>`, `<x><x><a>new</a><b>changed</b></x></x>`, true},
		{"nested-create-delete", `<x><x><a>old</a></x></x>`, `<x><x><b>new</b></x></x>`, false},
		{"foreign-parent", `<x><x><a>same</a></x></x>`, `<x><x><a>same</a></x></x><keyless><value>new</value></keyless>`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set1, set2 := utDiffNamedSet(t, "diff-cross-context"), utDiffNamedSet(t, "diff-cross-context")
			wrap := func(s string) string { return `<top xmlns="urn:vibe-ports:yang:diff-cross-context">` + s + `</top>` }
			first, second := parseOnlyXML(t, set1, wrap(tc.before)), parseOnlyXML(t, set2, wrap(tc.after))
			diff, err := Diff(first, second, DiffOptions{})
			if err != nil || diff == nil {
				t.Fatalf("Diff = (%v, %v)", diff, err)
			}
			beforeFirst := dumpTree(first)
			requireD0122Refusal(t, first.ApplyDiff(diff, ApplyDiffOptions{}), "lyd_diff_apply_all")
			if got := dumpTree(first); !slices.Equal(got, beforeFirst) {
				t.Fatalf("refused apply changed first-context tree: %v, want %v", got, beforeFirst)
			}

			target := parseOnlyXML(t, set2, wrap(tc.before))
			beforeSecond := dumpTree(target)
			err = target.ApplyDiff(diff, ApplyDiffOptions{})
			if tc.secondAllowed {
				if err != nil {
					t.Fatal(err)
				}
				if got, want := dumpTree(target), dumpTree(second); !slices.Equal(got, want) {
					t.Fatalf("applied tree = %v, want %v", got, want)
				}
			} else {
				requireD0122Refusal(t, err, "lyd_diff_apply_all")
				if got := dumpTree(target); !slices.Equal(got, beforeSecond) {
					t.Fatalf("refused apply changed second-context tree: %v, want %v", got, beforeSecond)
				}
			}
		})
	}
}

func TestApplyDiffForeignUserOrderedCreate(t *testing.T) {
	const model = `module foreign-create {
  yang-version 1.1;
  namespace "urn:foreign-create";
  prefix fc;
  container top { presence "keep";
    leaf marker { type string; }
    leaf-list item { type string; ordered-by user; }
  }
}`
	set1, set2 := inlineDiffSet(t, model), inlineDiffSet(t, model)
	const xml = `<top xmlns="urn:foreign-create" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none"><item yang:operation="create" yang:value="">new</item></top>`
	diff := parseOnlyXML(t, set2, xml)
	for _, children := range []string{"", "<marker>same</marker>"} {
		before := `<top xmlns="urn:foreign-create">` + children + `</top>`
		target := parseOnlyXML(t, set1, before)
		err := target.ApplyDiff(diff, ApplyDiffOptions{})
		requireD0122Refusal(t, err, "lyd_diff_apply_all")
		if got, want := dumpTree(target), dumpTree(parseOnlyXML(t, set1, before)); !slices.Equal(got, want) {
			t.Fatalf("failed insertion changed tree: %v, want %v", got, want)
		}
	}
	// A whole foreign subtree is refused before its top-level node can be inserted.
	created := parseOnlyXML(t, set2, `<top xmlns="urn:foreign-create"><item>new</item></top>`)
	diff, err := Diff(nil, created, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	target := newTree(set1)
	requireD0122Refusal(t, target.ApplyDiff(diff, ApplyDiffOptions{}), "lyd_diff_apply_all")
	if target.top.len() != 0 {
		t.Fatal("refused foreign subtree create changed the empty target")
	}
}

// TestApplyDiffMixedSnapshotsModule: D-0122 is checked for the whole source diff before the module
// filter can skip any roots.
func TestApplyDiffMixedSnapshotsModule(t *testing.T) {
	makeSet := func() *schema.Set {
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS("../conformance/corpus/ut-diff/schemas"))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"defaults", "diff-cross-context"} {
			if _, diags, err := c.Load(name, "", nil); err != nil {
				t.Fatal(err, diags)
			}
		}
		set := &schema.Set{}
		for _, m := range c.Modules {
			set.Modules = append(set.Modules, m.Schema)
		}
		return set
	}
	const before = `<df xmlns="urn:libyang:tests:defaults"><foo>1</foo></df><top xmlns="urn:vibe-ports:yang:diff-cross-context"><x><x><a>old</a></x></x></top>`
	const after = `<df xmlns="urn:libyang:tests:defaults"><foo>2</foo></df><top xmlns="urn:vibe-ports:yang:diff-cross-context"><x><x><a>new</a></x></x></top>`
	set1, set2 := makeSet(), makeSet()
	first, second := parseOnlyXML(t, set1, before), parseOnlyXML(t, set2, after)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil || diff == nil {
		t.Fatalf("Diff = (%v, %v)", diff, err)
	}
	requireD0122Refusal(t, first.ApplyDiff(diff, ApplyDiffOptions{Module: "diff-cross-context"}), "lyd_diff_apply_all")
	if got, want := dumpTree(first), dumpTree(parseOnlyXML(t, set1, before)); !slices.Equal(got, want) {
		t.Fatalf("filtered tree = %v, want %v", got, want)
	}
}

// TestApplyDiffMixedSnapshotsPartialError checks that D-0122 replaces libyang's partial mutation:
// even an otherwise successful first replacement is not applied.
func TestApplyDiffMixedSnapshotsPartialError(t *testing.T) {
	set1, set2 := utDiffNamedSet(t, "diff-cross-context"), utDiffNamedSet(t, "diff-cross-context")
	first := parseOnlyXML(t, set1, `<top xmlns="urn:vibe-ports:yang:diff-cross-context"><x><x><a>old</a><b>old</b></x></x></top>`)
	second := parseOnlyXML(t, set2, `<top xmlns="urn:vibe-ports:yang:diff-cross-context"><x><x><a>new</a><b>new</b></x></x></top>`)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	target := parseOnlyXML(t, set1, `<top xmlns="urn:vibe-ports:yang:diff-cross-context"><x><x><a>old</a></x></x></top>`)
	err = target.ApplyDiff(diff, ApplyDiffOptions{})
	requireD0122Refusal(t, err, "lyd_diff_apply_all")
	want := parseOnlyXML(t, set1, `<top xmlns="urn:vibe-ports:yang:diff-cross-context"><x><x><a>old</a></x></x></top>`)
	if got, want := dumpTree(target), dumpTree(want); !slices.Equal(got, want) {
		t.Fatalf("partial tree = %v, want %v", got, want)
	}
}

func TestApplyDiffCrossContextRefusedBeforeMutation(t *testing.T) {
	const model = `module diff-refusal {
  yang-version 1.1;
  namespace "urn:diff-refusal";
  prefix dr;
  leaf a-local { type string; }
  leaf z-foreign { type string; }
}`
	set1, set2 := inlineDiffSet(t, model), inlineDiffSet(t, model)
	first := parseOnlyXML(t, set1, `<a-local xmlns="urn:diff-refusal">old</a-local>`)
	second := parseOnlyXML(t, set2, `<z-foreign xmlns="urn:diff-refusal">new</z-foreign>`)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil || diff == nil || len(diff.top.list) != 2 {
		t.Fatalf("Diff = (%v, %v), want two roots", diff, err)
	}
	if setOf(diff.top.list[0]) == set2 {
		slices.Reverse(diff.top.list) // exercise a successful local operation before the foreign root
	}
	if setOf(diff.top.list[0]) != set1 || setOf(diff.top.list[1]) != set2 {
		t.Fatalf("diff roots are %s(set1=%t,set2=%t), %s(set1=%t,set2=%t), want local then foreign",
			diff.top.list[0].Name(), setOf(diff.top.list[0]) == set1, setOf(diff.top.list[0]) == set2,
			diff.top.list[1].Name(), setOf(diff.top.list[1]) == set1, setOf(diff.top.list[1]) == set2)
	}

	target := parseOnlyXML(t, set1, `<a-local xmlns="urn:diff-refusal">old</a-local>`)
	want := dumpTree(target)
	requireD0122Refusal(t, target.ApplyDiff(diff, ApplyDiffOptions{}), "lyd_diff_apply_all")
	if got := dumpTree(target); !slices.Equal(got, want) {
		t.Fatalf("refused apply changed target = %v, want %v", got, want)
	}
}

func TestMergeDiffCrossContextRefused(t *testing.T) {
	const model = `module diff-merge-refusal {
  yang-version 1.1;
  namespace "urn:diff-merge-refusal";
  prefix dmr;
  leaf value { type string; }
}`
	set1, set2 := inlineDiffSet(t, model), inlineDiffSet(t, model)
	makeDiff := func(set *schema.Set, before, after string) *Tree {
		t.Helper()
		wrap := func(v string) string { return `<value xmlns="urn:diff-merge-refusal">` + v + `</value>` }
		d, err := Diff(parseOnlyXML(t, set, wrap(before)), parseOnlyXML(t, set, wrap(after)), DiffOptions{})
		if err != nil || d == nil {
			t.Fatalf("Diff = (%v, %v)", d, err)
		}
		return d
	}
	src := makeDiff(set2, "middle", "new")
	for _, tc := range []struct {
		name  string
		merge func(*Tree) error
	}{
		{"all", func(dst *Tree) error { return dst.MergeDiff(src, MergeDiffOptions{}) }},
		{"tree", func(dst *Tree) error { return dst.MergeDiffTree(nil, src.top.first(), MergeDiffOptions{}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := makeDiff(set1, "old", "middle")
			want := dumpTree(dst)
			requireD0122Refusal(t, tc.merge(dst), "lyd_diff_merge_all")
			if got := dumpTree(dst); !slices.Equal(got, want) {
				t.Fatalf("refused merge changed destination = %v, want %v", got, want)
			}
		})
	}
}

// TestApplyDiffRepeatedRoots ensures that an apply index stays live when later
// diff roots revisit siblings after deletes and creates, including duplicate singleton data.
func TestApplyDiffRepeatedRoots(t *testing.T) {
	set1 := utDiffNamedSet(t, "diff-cross-context")
	target := parseOnlyXML(t, set1, `<top xmlns="urn:vibe-ports:yang:diff-cross-context"><x><x><a>first</a><a>second</a></x></x></top>`)
	wrap := func(children string) string {
		return `<top xmlns="urn:vibe-ports:yang:diff-cross-context" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none"><x><x>` + children + `</x></x></top>`
	}
	diff := parseOnlyXML(t, set1,
		wrap(`<a yang:operation="delete">first</a><b yang:operation="create">created</b>`)+
			wrap(`<a yang:operation="replace">updated</a><b yang:operation="replace">changed</b>`)+
			wrap(`<a yang:operation="delete">updated</a><b yang:operation="delete">changed</b>`)+
			wrap(`<a yang:operation="replace">missing</a>`))
	var seen []string
	err := target.ApplyDiff(diff, ApplyDiffOptions{Callback: func(_, n *Node) error {
		if n.isTerm() {
			seen = append(seen, n.Name()+"="+n.value.Canonical())
		}
		return nil
	}})
	var ve *ValidationError
	// The diff and data share a context, so LOGERR_NOINST is visible (diff.c:1808).
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || len(ve.Diags) != 1 {
		t.Fatalf("ApplyDiff: %#v", err)
	}
	if want := []string{"b=created", "a=updated", "b=changed"}; !slices.Equal(seen, want) {
		t.Fatalf("applied terms = %v, want %v", seen, want)
	}
	want := parseOnlyXML(t, set1, `<top xmlns="urn:vibe-ports:yang:diff-cross-context"><x><x/></x></top>`)
	if got, want := dumpTree(target), dumpTree(want); !slices.Equal(got, want) {
		t.Fatalf("partial tree = %v, want %v", got, want)
	}
}

func TestApplyDiffMixedSnapshotsSchemaLookupWork(t *testing.T) {
	for _, pattern := range []string{"sorted", "reverse", "shuffled", "duplicate-heavy"} {
		t.Run(pattern, func(t *testing.T) {
			measure := func(n int) int64 {
				order := make([]int, n)
				for i := range order {
					order[i] = i
				}
				switch pattern {
				case "reverse":
					slices.Reverse(order)
				case "shuffled", "duplicate-heavy":
					for i := range order {
						j := (i*37 + 11) % n
						order[i], order[j] = order[j], order[i]
					}
				}
				makeTree := func(changed bool) *Tree {
					m := &schema.Module{Name: "work", Namespace: "urn:work", Prefix: "w", Implemented: true}
					set := utDiffSet(t) // includes the built-in yang diff annotations
					set.Modules = append(set.Modules, m)
					for _, i := range order {
						m.Top = append(m.Top, &schema.Node{Kind: schema.Leaf, Name: fmt.Sprintf("n%06d", i), Module: m,
							Type: &schema.Type{Base: schema.String}, Config: true})
					}
					tr := newTree(set)
					for i, sn := range m.Top {
						if pattern == "duplicate-heavy" {
							i %= 4
						}
						v, d := types.StoreOnly(sn.Type, fmt.Sprintf("v%d-%t", i, changed), types.FormatXML, types.HintData, nil, sn)
						if d != nil {
							t.Fatal(d.Msg)
						}
						tr.insert(nil, newTerm(sn, v), insertDefault)
					}
					return tr
				}
				first, second := makeTree(false), makeTree(true)
				diff, err := Diff(first, second, DiffOptions{})
				if err != nil || diff == nil {
					t.Fatalf("Diff = (%v, %v)", diff, err)
				}
				for _, tr := range []*Tree{first, diff} {
					tr.work.Store(0)
					tr.work.visits = true
				}
				before := dumpTree(first)
				requireD0122Refusal(t, first.ApplyDiff(diff, ApplyDiffOptions{}), "lyd_diff_apply_all")
				work := first.work.Load() + diff.work.Load()
				if got := dumpTree(first); !slices.Equal(got, before) {
					t.Fatal("refused apply changed values")
				}
				return work
			}
			const n = 64
			small, large := measure(n), measure(4*n)
			if small == 0 || large > 6*small {
				t.Fatalf("work(%d) = %d, work(%d) = %d; want work(4N) <= 6*work(N)", n, small, 4*n, large)
			}
		})
	}
}

// TestApplyOpaqueCreate: an opaque diff node with its own yang:operation="create" crashes
// libyang v5.8.6 in lyd_diff_apply_all (oracle exit 139, so no fixture); the port reads no
// operation from opaque nodes, takes the parent's none and fails to find the node: LY_EINVAL.
func TestApplyOpaqueCreate(t *testing.T) {
	set := utDiffSet(t)
	data := parseOnlyXML(t, set, `<df xmlns="urn:libyang:tests:defaults"><foo>1</foo></df>`)
	diff := parseOnlyXML(t, set, `<df xmlns="urn:libyang:tests:defaults" xmlns:yang="urn:ietf:params:xml:ns:yang:1"`+
		` yang:operation="none"><zz yang:operation="create">1</zz></df>`)
	err := data.ApplyDiff(diff, ApplyDiffOptions{})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" {
		t.Fatalf("ApplyDiff: %v", err)
	}
	if want := `Failed to find node "/defaults:df/zz" instance in data.`; ve.Diags[0].Msg != want {
		t.Errorf("message %q, want %q", ve.Diags[0].Msg, want)
	}
}

// TestApplyOpaqueInheritedCreate: an opaque child cannot apply the create it inherits from its
// parent; libyang v5.8.6 crashes here (oracle exit 139, so no fixture; deviations.md crash cases). In particular, applying its metadata must not dereference its nil schema node.
func TestApplyOpaqueInheritedCreate(t *testing.T) {
	set := diffApplyMetaSet(t)
	data := newTree(set)
	diff := parseOnlyXML(t, set, `<root xmlns="urn:vibe-ports:yang:diff-apply-meta"`+
		` xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="create"><unknown>value</unknown></root>`)
	err := data.ApplyDiff(diff, ApplyDiffOptions{})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || len(ve.Diags) != 1 {
		t.Fatalf("ApplyDiff: %#v", err)
	}
	if d := ve.Diags[0]; d.Err != "LY_EINVAL" || d.Code != "LYVE_SUCCESS" {
		t.Fatalf("diagnostic: %+v", d)
	}
}

// TestApplyOpaqueInheritedNone: an opaque child that inherits none and matches an opaque data
// node crashes libyang v5.8.6 when lyd_diff_apply_r dereferences match->schema. The port returns
// LY_EINVAL before applying metadata or descending into the opaque node (deviations.md crash cases).
func TestApplyOpaqueInheritedNone(t *testing.T) {
	set := diffApplyMetaSet(t)
	data := parseOnlyXML(t, set, `<root xmlns="urn:vibe-ports:yang:diff-apply-meta"><unknown>value</unknown></root>`)
	diff := parseOnlyXML(t, set, `<root xmlns="urn:vibe-ports:yang:diff-apply-meta"`+
		` xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none"><unknown>value</unknown></root>`)
	err := data.ApplyDiff(diff, ApplyDiffOptions{})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || len(ve.Diags) != 1 {
		t.Fatalf("ApplyDiff: %#v", err)
	}
	d := ve.Diags[0]
	if d.Err != "LY_EINVAL" || d.Code != "LYVE_SUCCESS" ||
		d.Msg != `Operation "none" is invalid for opaque node "unknown".` ||
		d.DataPath != "" || d.SchemaPath != "" || d.AppTag != "" {
		t.Fatalf("diagnostic: %+v", d)
	}
	if got := dumpTree(data); len(got) != 2 || got[1] != `/diff-apply-meta:root/unknown opaque="value" 0x1` {
		t.Fatalf("data after ApplyDiff: %q", got)
	}
}

// TestApplyMetaCreateError: lyd_new_meta failures retain the helper's return code and complete
// diagnostic instead of being flattened into an LY_EINVAL message.
func TestApplyMetaCreateError(t *testing.T) {
	set := diffApplyMetaSet(t)
	for _, tc := range []struct {
		name, value, rc, err, code, dataPath, schemaPath, appTag string
	}{
		{"missing module", "missing:a=x", "LY_ENOTFOUND", "LY_EINVAL", "LYVE_SUCCESS", "", "", ""},
		{"unknown annotation", "diff-apply-meta:missing=x", "LY_EINVAL", "LY_EVALID", "LYVE_REFERENCE", "/diff-apply-meta:root", "", ""},
		{"invalid value", "diff-apply-meta:limited=11", "LY_EVALID", "LY_EVALID", "LYVE_DATA", "", "/diff-apply-meta:root", "limited-value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := parseOnlyXML(t, set, `<root xmlns="urn:vibe-ports:yang:diff-apply-meta"/>`)
			diff := parseOnlyXML(t, set, `<root xmlns="urn:vibe-ports:yang:diff-apply-meta"`+
				` xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none"`+
				` yang:meta-create="`+tc.value+`"/>`)
			err := data.ApplyDiff(diff, ApplyDiffOptions{})
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.RC() != tc.rc || len(ve.Diags) != 1 {
				t.Fatalf("ApplyDiff: %#v", err)
			}
			d := ve.Diags[0]
			if d.Err != tc.err || d.Code != tc.code || d.DataPath != tc.dataPath ||
				d.SchemaPath != tc.schemaPath || d.AppTag != tc.appTag {
				t.Fatalf("diagnostic: %+v", d)
			}
		})
	}
}

func diffApplyMetaSet(t *testing.T) *schema.Set {
	t.Helper()
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true},
		os.DirFS("../conformance/corpus/ut-diff/schemas"))
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load("diff-apply-meta", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	return set
}

// TestMergeDiffTreeForeignParent: a parent that is not a node of the diff is refused.
func TestMergeDiffTreeForeignParent(t *testing.T) {
	set := utDiffSet(t)
	d1 := parseOnlyXML(t, set, `<df xmlns="urn:libyang:tests:defaults"/>`)
	d2 := parseOnlyXML(t, set, `<df xmlns="urn:libyang:tests:defaults" xmlns:yang="urn:ietf:params:xml:ns:yang:1"`+
		` yang:operation="none"><foo yang:operation="create">1</foo></df>`)
	parent := topOf(d2)
	src := firstNoKeys(parent)
	err := d1.MergeDiffTree(parent, src, MergeDiffOptions{})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" {
		t.Fatalf("MergeDiffTree: %v", err)
	}
}

func topOf(t *Tree) *Node { return t.top.first() }
