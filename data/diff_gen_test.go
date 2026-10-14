// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// utDiffNamedSet compiles a module from the ut-diff schemas.
func utDiffNamedSet(t *testing.T, name string) *schema.Set {
	return utDiffNamedSetFeatures(t, name, nil)
}

func utDiffNamedSetFeatures(t *testing.T, name string, features []string) *schema.Set {
	return utDiffNamedSetDir(t, "schemas", name, features)
}

func utDiffNamedSetDir(t *testing.T, dir, name string, features []string) *schema.Set {
	return utDiffNamedSetDirs(t, name, features, dir)
}

func utDiffNamedSetDirs(t *testing.T, name string, features []string, dirs ...string) *schema.Set {
	t.Helper()
	search := make([]fs.FS, len(dirs))
	for i, dir := range dirs {
		search[i] = os.DirFS("../conformance/corpus/ut-diff/" + dir)
	}
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, search...)
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load(name, "", features); err != nil {
		t.Fatal(err, diags)
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	return set
}

func crossContextPluginSet(t *testing.T) *schema.Set {
	return utDiffNamedSetDirs(t, "diff-cross-context-plugins", nil, "schemas", "../types/schemas")
}

// utDiffSet compiles the ut-diff module defaults (tests/utests/data/test_diff.c).
func utDiffSet(t *testing.T) *schema.Set { return utDiffNamedSet(t, "defaults") }

// parseOnlyXML parses XML data parse-only (unknown nodes opaque) over set.
func parseOnlyXML(t *testing.T, set *schema.Set, in string) *Tree {
	t.Helper()
	tr, diags, err := parseWith(context.Background(), strings.NewReader(in), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true, Unknown: Opaque}}, parseXML, nil)
	if err != nil {
		t.Fatal(err, diags)
	}
	return tr
}

func inlineDiffSet(t *testing.T, module string) *schema.Set {
	t.Helper()
	name := strings.Fields(module)[1] // "module <name> {"
	models := fstest.MapFS{name + ".yang": {Data: []byte(module)}}
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, models)
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

// TestDiffKeyRefused: a list key as an argument is LY_EINVAL (libyang asserts it never is).
func TestDiffKeyRefused(t *testing.T) {
	set := utDiffSet(t)
	tr := parseOnlyXML(t, set, `<df xmlns="urn:libyang:tests:defaults"><list><name>a</name></list></df>`)
	l, err := tr.Find("/defaults:df/list[name='a']/name")
	if err != nil || l == nil {
		t.Fatal(l, err)
	}
	for _, f := range []func(a, b *Node, o DiffOptions) (*Tree, error){DiffSiblings, DiffTree} {
		_, err := f(l, nil, DiffOptions{})
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" {
			t.Errorf("key argument: %v", err)
		}
	}
}

// TestDiffMixedSnapshots: lyd_find_sibling_schema matches top-level nodes from different contexts
// by module and node name, so identical trees have no diff.
func TestDiffMixedSnapshots(t *testing.T) {
	set1, set2 := utDiffSet(t), utDiffSet(t)
	first := parseOnlyXML(t, set1, `<df xmlns="urn:libyang:tests:defaults"><foo>1</foo></df>`)
	second := parseOnlyXML(t, set2, `<df xmlns="urn:libyang:tests:defaults"><foo>1</foo></df>`)
	for name, diff := range map[string]func() (*Tree, error){
		"trees":    func() (*Tree, error) { return Diff(first, second, DiffOptions{}) },
		"siblings": func() (*Tree, error) { return DiffSiblings(topOf(first), topOf(second), DiffOptions{}) },
		"subtrees": func() (*Tree, error) { return DiffTree(topOf(first), topOf(second), DiffOptions{}) },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := diff()
			if got != nil || err != nil {
				t.Fatalf("Diff = (%v, %v)", got, err)
			}
		})
	}
}

// TestDiffMixedSnapshotsReplace: a changed leaf across contexts produces exactly the diff made
// from two trees in one context, including its replace operation and original value.
func TestDiffMixedSnapshotsReplace(t *testing.T) {
	set1, set2 := utDiffSet(t), utDiffSet(t)
	first := parseOnlyXML(t, set1, `<df xmlns="urn:libyang:tests:defaults"><foo>1</foo></df>`)
	within := parseOnlyXML(t, set1, `<df xmlns="urn:libyang:tests:defaults"><foo>2</foo></df>`)
	cross := parseOnlyXML(t, set2, `<df xmlns="urn:libyang:tests:defaults"><foo>2</foo></df>`)

	want, err := Diff(first, within, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Diff(first, cross, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || want == nil {
		t.Fatalf("cross-context Diff nil = %t, want nil = %t", got == nil, want == nil)
	}
	if gotDump, wantDump := dumpTree(got), dumpTree(want); !slices.Equal(gotDump, wantDump) {
		t.Fatalf("cross-context Diff = %v, want %v", gotDump, wantDump)
	}
}

// TestDiffMixedSnapshotsInstances covers keyed and duplicate-instance matching using the original
// target values while only its schema is mapped for the sibling index lookup.
func TestDiffMixedSnapshotsInstances(t *testing.T) {
	set1, set2 := utDiffSet(t), utDiffSet(t)
	firstXML := `<df xmlns="urn:libyang:tests:defaults"><llist>1</llist><llist>2</llist><ul><l1>a</l1><l2>1</l2></ul><ul><l1>b</l1><l2>2</l2></ul><kll>x</kll><kll>x</kll></df>`
	first := parseOnlyXML(t, set1, firstXML)
	identical := parseOnlyXML(t, set2, firstXML)
	if got, err := Diff(first, identical, DiffOptions{}); got != nil || err != nil {
		t.Fatalf("identical cross-context instances Diff = (%v, %v)", got, err)
	}

	for _, tc := range []struct {
		name string
		xml  string
	}{
		{"changed keyed list leaf", `<df xmlns="urn:libyang:tests:defaults"><llist>1</llist><llist>2</llist><ul><l1>a</l1><l2>1</l2></ul><ul><l1>b</l1><l2>3</l2></ul><kll>x</kll><kll>x</kll></df>`},
		{"added leaf-list value", `<df xmlns="urn:libyang:tests:defaults"><llist>1</llist><llist>2</llist><llist>3</llist><ul><l1>a</l1><l2>1</l2></ul><ul><l1>b</l1><l2>2</l2></ul><kll>x</kll><kll>x</kll></df>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			within := parseOnlyXML(t, set1, tc.xml)
			cross := parseOnlyXML(t, set2, tc.xml)
			want, err := Diff(first, within, DiffOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := Diff(first, cross, DiffOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || want == nil {
				t.Fatalf("cross-context Diff nil = %t, want nil = %t", got == nil, want == nil)
			}
			if gotDump, wantDump := dumpTree(got), dumpTree(want); !slices.Equal(gotDump, wantDump) {
				t.Fatalf("cross-context Diff = %v, want %v", gotDump, wantDump)
			}
		})
	}
}

func TestDiffMixedSnapshotsSharedParent(t *testing.T) {
	set1, set2 := utDiffSet(t), utDiffSet(t)
	first := parseOnlyXML(t, set1, `<df xmlns="urn:libyang:tests:defaults"><foo>1</foo><b1_1>2</b1_1></df>`)
	within := parseOnlyXML(t, set1, `<df xmlns="urn:libyang:tests:defaults"><foo>2</foo><b1_1>3</b1_1></df>`)
	cross := parseOnlyXML(t, set2, `<df xmlns="urn:libyang:tests:defaults"><foo>2</foo><b1_1>3</b1_1></df>`)
	want, err := Diff(first, within, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Diff(first, cross, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if gotDump, wantDump := dumpTree(got), dumpTree(want); !slices.Equal(gotDump, wantDump) {
		t.Fatalf("cross-context Diff = %v, want %v", gotDump, wantDump)
	}
}

func TestDiffMixedSnapshotsRepeatedAncestors(t *testing.T) {
	set1, set2 := utDiffNamedSet(t, "diff-cross-context"), utDiffNamedSet(t, "diff-cross-context")
	first := parseOnlyXML(t, set1, `<top xmlns="urn:vibe-ports:yang:diff-cross-context"><x><x><a>1</a><b>2</b></x></x></top>`)
	within := parseOnlyXML(t, set1, `<top xmlns="urn:vibe-ports:yang:diff-cross-context"><x><x><a>3</a><b>4</b></x></x></top>`)
	cross := parseOnlyXML(t, set2, `<top xmlns="urn:vibe-ports:yang:diff-cross-context"><x><x><a>3</a><b>4</b></x></x></top>`)
	want, err := Diff(first, within, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Diff(first, cross, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if gotDump, wantDump := dumpTree(got), dumpTree(want); !slices.Equal(gotDump, wantDump) {
		t.Fatalf("cross-context Diff = %v, want %v", gotDump, wantDump)
	}
	if n, err := got.Find("/diff-cross-context:top/x/x/b"); err != nil || n == nil {
		t.Fatalf("nested changed leaf b = (%v, %v)", n, err)
	}
}

// TestDiffMixedSnapshotsKeyless pins the mixed-context shape: the created keyless instance keeps
// the second context's schema, sorts before the first-context delete, and is module-qualified by
// the JSON printer because its parent belongs to the first context.
func TestDiffMixedSnapshotsKeyless(t *testing.T) {
	set1, set2 := utDiffNamedSet(t, "diff-cross-context"), utDiffNamedSet(t, "diff-cross-context")
	const before = `<top xmlns="urn:vibe-ports:yang:diff-cross-context"><keyless><value>one</value><nested><value>same</value></nested></keyless><keyless><value>two</value><nested><value>old</value></nested></keyless></top>`
	const after = `<top xmlns="urn:vibe-ports:yang:diff-cross-context"><keyless><value>one</value><nested><value>same</value></nested></keyless><keyless><value>two</value><nested><value>new</value></nested></keyless></top>`
	first, second := parseOnlyXML(t, set1, before), parseOnlyXML(t, set2, after)
	got, err := Diff(first, second, DiffOptions{})
	if err != nil || got == nil {
		t.Fatalf("Diff = (%v, %v)", got, err)
	}
	top := got.top.first()
	if top == nil || len(top.kids.list) != 2 {
		t.Fatalf("diff children = %v", dumpTree(got))
	}
	created, deleted := top.kids.list[0], top.kids.list[1]
	if created.schema.Module != set2.Implemented("diff-cross-context") || deleted.schema.Module != set1.Implemented("diff-cross-context") {
		t.Fatalf("diff schema contexts = (%p, %p), want (second, first)", created.schema.Module, deleted.schema.Module)
	}
	if op, _ := diffGetOp(created); op != diffCreate {
		t.Fatalf("first keyless operation = %s, want create", op)
	}
	if op, _ := diffGetOp(deleted); op != diffDelete {
		t.Fatalf("second keyless operation = %s, want delete", op)
	}
	var out bytes.Buffer
	if err := got.PrintJSON(&out, PrintOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"diff-cross-context:keyless"`) {
		t.Fatalf("created keyless is not module-qualified:\n%s", out.String())
	}
}

// TestDiffMixedSnapshotsMixedOrder pins lyd_insert_get_next_anchor's non-hash owner-module
// boundary. The second-context duplicate-instance create precedes the first existing native
// child even though its schema follows that child's schema.
func TestDiffMixedSnapshotsMixedOrder(t *testing.T) {
	set1, set2 := utDiffNamedSet(t, "diff-cross-context"), utDiffNamedSet(t, "diff-cross-context")
	const before = `<top xmlns="urn:vibe-ports:yang:diff-cross-context"><x><x><a>old</a></x></x><keyless><value>old</value></keyless></top>`
	const after = `<top xmlns="urn:vibe-ports:yang:diff-cross-context"><x><x><a>new</a></x></x><keyless><value>new</value></keyless></top>`
	first, second := parseOnlyXML(t, set1, before), parseOnlyXML(t, set2, after)
	got, err := Diff(first, second, DiffOptions{})
	if err != nil || got == nil {
		t.Fatalf("Diff = (%v, %v)", got, err)
	}
	top := topOf(got)
	if top == nil || len(top.kids.list) != 3 {
		t.Fatalf("diff children = %v", dumpTree(got))
	}
	created, deleted, changed := top.kids.list[0], top.kids.list[1], top.kids.list[2]
	if created.Name() != "keyless" || deleted.Name() != "keyless" || changed.Name() != "x" {
		t.Fatalf("diff child order = %v, want [keyless keyless x]", names(top.Children()))
	}
	if created.schema.Module != set2.Implemented("diff-cross-context") ||
		deleted.schema.Module != set1.Implemented("diff-cross-context") {
		t.Fatal("duplicate-instance create/delete did not retain their source contexts")
	}
	if op, _ := diffGetOp(created); op != diffCreate {
		t.Fatalf("first keyless operation = %s, want create", op)
	}
	if op, _ := diffGetOp(deleted); op != diffDelete {
		t.Fatalf("last keyless operation = %s, want delete", op)
	}
	if len(changed.kids.list) != 1 || len(changed.kids.list[0].kids.list) != 1 {
		t.Fatalf("changed subtree = %v", dumpTree(got))
	}
	leaf := changed.kids.list[0].kids.list[0]
	if leaf.Name() != "a" {
		t.Fatalf("changed leaf = %s, want a", leaf.Name())
	}
	if op, _ := diffGetOp(leaf); op != diffReplace {
		t.Fatalf("changed leaf operation = %s, want replace", op)
	}
	want := "{\n" +
		"  \"diff-cross-context:top\": {\n" +
		"    \"@\": {\n" +
		"      \"yang:operation\": \"none\"\n" +
		"    },\n" +
		"    \"keyless\": [\n" +
		"      {\n" +
		"        \"@\": {\n" +
		"          \"yang:operation\": \"create\",\n" +
		"          \"yang:position\": [null]\n" +
		"        },\n" +
		"        \"value\": \"new\"\n" +
		"      }\n" +
		"    ],\n" +
		"    \"diff-cross-context:keyless\": [\n" +
		"      {\n" +
		"        \"@\": {\n" +
		"          \"yang:operation\": \"delete\",\n" +
		"          \"yang:orig-position\": [null]\n" +
		"        },\n" +
		"        \"value\": \"old\"\n" +
		"      }\n" +
		"    ],\n" +
		"    \"x\": {\n" +
		"      \"x\": {\n" +
		"        \"a\": \"new\",\n" +
		"        \"@a\": {\n" +
		"          \"yang:operation\": \"replace\",\n" +
		"          \"yang:orig-default\": false,\n" +
		"          \"yang:orig-value\": \"old\"\n" +
		"        }\n" +
		"      }\n" +
		"    }\n" +
		"  }\n" +
		"}\n"
	assertPrintedJSON(t, got, want)
}

// TestDiffMixedSnapshotsCreatedDupInstParentsKeepContext covers lyd_diff_dup's no-parent path:
// LYD_DUP_WITH_PARENTS retains the second tree's context on the whole duplicated ancestor chain,
// both for a keyless list and a state leaf-list.
func TestDiffMixedSnapshotsCreatedDupInstParentsKeepContext(t *testing.T) {
	for _, tc := range []struct {
		name, secondXML, childName, qualified string
	}{
		{"keyless", `<top xmlns="urn:vibe-ports:yang:diff-cross-context-create"><keyless><value>new</value></keyless></top>`,
			"keyless", `"diff-cross-context-create:keyless"`},
		{"state-leaf-list", `<top xmlns="urn:vibe-ports:yang:diff-cross-context-create"><state-value>new</state-value></top>`,
			"state-value", `"diff-cross-context-create:state-value"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set1 := utDiffNamedSet(t, "diff-cross-context-create")
			set2 := utDiffNamedSet(t, "diff-cross-context-create")
			first := parseOnlyXML(t, set1, `<top xmlns="urn:vibe-ports:yang:diff-cross-context-create"/>`)
			second := parseOnlyXML(t, set2, tc.secondXML)
			if topOf(first) == nil {
				t.Fatal("empty top was not retained")
			}
			got, err := Diff(first, second, DiffOptions{})
			if err != nil || got == nil {
				t.Fatalf("Diff = (%v, %v)", got, err)
			}
			gotTop, secondTop := topOf(got), topOf(second)
			if gotTop.schema != secondTop.schema {
				t.Fatal("created ancestor was remapped into the first context")
			}
			var gotChild, secondChild *Node
			for _, n := range gotTop.kids.list {
				if n.Name() == tc.childName {
					gotChild = n
				}
			}
			for _, n := range secondTop.kids.list {
				if n.Name() == tc.childName {
					secondChild = n
				}
			}
			if gotChild == nil || secondChild == nil || gotChild.schema != secondChild.schema {
				t.Fatalf("created child schemas = (%v, %v), want second-context identity", gotChild, secondChild)
			}
			var out bytes.Buffer
			if err := got.PrintJSON(&out, PrintOptions{}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), tc.qualified) {
				t.Fatalf("created child is spuriously module-qualified:\n%s", out.String())
			}
		})
	}
}

func TestDiffMixedSnapshotsCanonicalValues(t *testing.T) {
	set1, set2 := utDiffNamedSet(t, "diff-cross-context"), utDiffNamedSet(t, "diff-cross-context")
	const in = `<top xmlns="urn:vibe-ports:yang:diff-cross-context" xmlns:d="urn:vibe-ports:yang:diff-cross-context"><identity>derived</identity><identities>derived</identities><instance>/d:top/d:x/d:x/d:a</instance><instances>/d:top/d:x/d:x/d:b</instances></top>`
	first, second := parseOnlyXML(t, set1, in), parseOnlyXML(t, set2, in)
	if got, err := Diff(first, second, DiffOptions{}); got != nil || err != nil {
		t.Fatalf("canonical cross-context values Diff = (%v, %v)", got, err)
	}
}

// TestDiffMixedSnapshotsUnionIdentityref compares stored canonical values, not their local JSON
// spelling. The same XML value selects identityref with the feature enabled and string without it;
// both print as "d" in JSON, but libyang reports a replacement.
func TestDiffMixedSnapshotsUnionIdentityref(t *testing.T) {
	enabled := utDiffNamedSetFeatures(t, "diff-cross-context-union", []string{"derived"})
	disabled := utDiffNamedSet(t, "diff-cross-context-union")
	const in = `<top xmlns="urn:vibe-ports:yang:diff-cross-context-union"><value>d</value></top>`
	first, second := parseOnlyXML(t, enabled, in), parseOnlyXML(t, disabled, in)
	a, _ := first.Find("/diff-cross-context-union:top/value")
	b, _ := second.Find("/diff-cross-context-union:top/value")
	if a == nil || b == nil || a.value.Canonical() != "diff-cross-context-union:d" || b.value.Canonical() != "d" {
		t.Fatalf("stored values = (%v, %v)", a, b)
	}
	if types.EqualLYB(a.value, b.value) {
		t.Fatal("distinct selected union members had equal LYB values")
	}
	got, err := Diff(first, second, DiffOptions{})
	if err != nil || got == nil {
		t.Fatalf("Diff = (%v, %v), want replacement", got, err)
	}
	n, err := got.Find("/diff-cross-context-union:top/value")
	if err != nil || n == nil {
		t.Fatalf("changed value = (%v, %v)", n, err)
	}
	if i := findYangMeta(n, "operation"); i < 0 || n.meta[i].value.Canonical() != "replace" {
		t.Fatalf("changed value operation = %v", n.meta)
	}
}

// TestDiffMixedSnapshotsIntegerRealtypes pins lyd_hash followed by lyd_compare_single_value:
// integer LYB uses the shortest bit representation, so positive int8 and uint8 values hash alike,
// and their equal canonical text makes the cross-context leaves equal.
func TestDiffMixedSnapshotsIntegerRealtypes(t *testing.T) {
	int8Set := utDiffNamedSetDir(t, "schemas/cross-context-int8", "diff-cross-context-integer", nil)
	uint8Set := utDiffNamedSetDir(t, "schemas/cross-context-uint8", "diff-cross-context-integer", nil)
	const in = `<top xmlns="urn:vibe-ports:yang:diff-cross-context-integer"><value>1</value></top>`
	first, second := parseOnlyXML(t, int8Set, in), parseOnlyXML(t, uint8Set, in)
	a, _ := first.Find("/diff-cross-context-integer:top/value[.='1']")
	b, _ := second.Find("/diff-cross-context-integer:top/value[.='1']")
	if a == nil || b == nil || a.value.Type().Base != schema.Int8 || b.value.Type().Base != schema.Uint8 {
		t.Fatalf("stored realtypes = (%v, %v)", a, b)
	}
	if !hashEqual(first, a, b) {
		t.Fatal("int8 1 and uint8 1 did not have equal LYB/canonical hash input")
	}
	if got, err := Diff(first, second, DiffOptions{}); got != nil || err != nil {
		t.Fatalf("Diff = (%v, %v), want null", got, err)
	}
}

// TestDiffMixedSnapshotsHashCollision pins lyd_compare_single_data's hash gate followed by its
// canonical-value comparison. These two distinct 32-bit enumeration LYB payloads were generated
// to collide after the common module and node names have been hashed.
func TestDiffMixedSnapshotsHashCollision(t *testing.T) {
	setA := utDiffNamedSetDir(t, "schemas/cross-context-collision-a", "diff-cross-context-collision", nil)
	setB := utDiffNamedSetDir(t, "schemas/cross-context-collision-b", "diff-cross-context-collision", nil)
	const in = `<top xmlns="urn:vibe-ports:yang:diff-cross-context-collision"><collision>same</collision></top>`
	first, second := parseOnlyXML(t, setA, in), parseOnlyXML(t, setB, in)
	a, b := topOf(first).kids.list[0], topOf(second).kids.list[0]
	if types.EqualLYB(a.value, b.value) {
		t.Fatal("generated collision unexpectedly has identical LYB payloads")
	}
	ah, aok := lydHash(first, a)
	bh, bok := lydHash(first, b)
	if !aok || !bok || ah != 0x04191187 || bh != ah {
		t.Fatalf("lyd hashes = (%#x, %#x), want collision %#x", ah, bh, uint32(0x04191187))
	}
	if got, err := Diff(first, second, DiffOptions{}); got != nil || err != nil {
		t.Fatalf("collision Diff = (%v, %v), want null", got, err)
	}
}

func TestDiffSparseBitsHashWork(t *testing.T) {
	const model = `module diff-sparse-bits {
  yang-version 1.1;
  namespace "urn:vibe-ports:yang:diff-sparse-bits";
  prefix dsb;
  container top {
    leaf-list item {
      config false;
      type bits { bit high { position 4294967295; } }
    }
  }
}`
	setA, setB := inlineDiffSet(t, model), inlineDiffSet(t, model)
	data := func(value string) string {
		var b strings.Builder
		b.WriteString(`<top xmlns="urn:vibe-ports:yang:diff-sparse-bits">`)
		for range 200 {
			b.WriteString("<item>")
			b.WriteString(value)
			b.WriteString("</item>")
		}
		b.WriteString("</top>")
		return b.String()
	}
	first := parseOnlyXML(t, setA, data("high"))
	equal := parseOnlyXML(t, setB, data("high"))
	disjoint := parseOnlyXML(t, setB, data(""))
	a, b := topOf(first).kids.list[0], topOf(equal).kids.list[0]

	type result struct {
		same, changed    *Tree
		sameErr, diffErr error
		equal, compared  bool
	}
	done := make(chan result, 1)
	go func() {
		same, sameErr := Diff(first, equal, DiffOptions{})
		changed, diffErr := Diff(first, disjoint, DiffOptions{})
		done <- result{same: same, changed: changed, sameErr: sameErr, diffErr: diffErr,
			equal: a.Equal(b, CompareOptions{}), compared: compareSingle(&Tree{}, a, b, false)}
	}()
	var got result
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("sparse bits comparisons did not finish within 30 seconds")
	}
	if got.same != nil || got.sameErr != nil {
		t.Fatalf("identical Diff = (%v, %v), want nil", got.same, got.sameErr)
	}
	if !got.equal || !got.compared {
		t.Fatalf("equal sparse values: Node.Equal=%v compareSingle=%v", got.equal, got.compared)
	}
	if got.changed == nil || got.diffErr != nil {
		t.Fatalf("disjoint Diff = (%v, %v)", got.changed, got.diffErr)
	}
	creates, deletes := 0, 0
	var countOps func(*siblings)
	countOps = func(s *siblings) {
		for n := range s.all() {
			if op, ok := diffGetOp(n); ok {
				switch op {
				case diffCreate:
					creates++
				case diffDelete:
					deletes++
				}
			}
			countOps(&n.kids)
		}
	}
	countOps(&got.changed.top)
	if creates != 200 || deletes != 200 {
		t.Fatalf("disjoint operations = %d creates, %d deletes; want 200 each", creates, deletes)
	}
}

func TestSparseBitsDifferentPositionsUseHash(t *testing.T) {
	model := func(position uint32) string {
		return fmt.Sprintf(`module diff-sparse-bits {
  yang-version 1.1;
  namespace "urn:vibe-ports:yang:diff-sparse-bits";
  prefix dsb;
  leaf-list item { type bits { bit same { position %d; } } }
}`, position)
	}
	first := parseOnlyXML(t, inlineDiffSet(t, model(1)),
		`<item xmlns="urn:vibe-ports:yang:diff-sparse-bits">same</item>`)
	second := parseOnlyXML(t, inlineDiffSet(t, model(9)),
		`<item xmlns="urn:vibe-ports:yang:diff-sparse-bits">same</item>`)
	a, b := topOf(first), topOf(second)
	hash := func(n *Node) uint32 {
		h := types.HashMulti32(0, []byte(n.schema.Module.Name))
		h = types.HashMulti32(h, []byte(n.schema.Name))
		var ok bool
		if h, ok = types.HashLYB(h, n.value); !ok {
			t.Fatal("HashLYB rejected bits value")
		}
		return types.FinishHash32(h)
	}
	wantEqual := hash(a) == hash(b)
	if got := a.Equal(b, CompareOptions{}); got != wantEqual {
		t.Fatalf("Node.Equal = %v, want hash equality %v", got, wantEqual)
	}
	if got := compareSingle(&Tree{}, a, b, false); got != wantEqual {
		t.Fatalf("compareSingle = %v, want hash equality %v", got, wantEqual)
	}
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil || (diff == nil) != wantEqual {
		t.Fatalf("Diff = (%v, %v), want hash equality %v", diff, err, wantEqual)
	}
}

func TestDiffMixedSnapshotsChoiceLookupPaths(t *testing.T) {
	setA := utDiffNamedSetDir(t, "schemas/cross-context-choice-a", "diff-cross-context-choice", nil)
	setB := utDiffNamedSetDir(t, "schemas/cross-context-choice-b", "diff-cross-context-choice", nil)
	for _, tc := range []struct {
		name, xml string
		want      map[diffOp]int
	}{
		{"top-level", `<x xmlns="urn:vibe-ports:yang:diff-cross-context-choice">1</x>`, map[diffOp]int{diffReplace: 1}},
		{"small-parent", `<small xmlns="urn:vibe-ports:yang:diff-cross-context-choice"><x>1</x><y>same</y></small>`, map[diffOp]int{diffReplace: 1}},
		{"hashed-parent", `<hashed xmlns="urn:vibe-ports:yang:diff-cross-context-choice"><x>1</x><a>same</a><b>same</b><d>same</d></hashed>`, map[diffOp]int{diffCreate: 1, diffDelete: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, second := parseOnlyXML(t, setA, tc.xml), parseOnlyXML(t, setB, tc.xml)
			diff, err := Diff(first, second, DiffOptions{})
			if err != nil || diff == nil {
				t.Fatalf("Diff = (%v, %v)", diff, err)
			}
			got := map[diffOp]int{}
			var walk func(*siblings)
			walk = func(s *siblings) {
				for n := range s.all() {
					if n.Name() == "x" {
						if op, ok := diffGetOp(n); ok {
							got[op]++
						}
					}
					walk(&n.kids)
				}
			}
			walk(&diff.top)
			if !maps.Equal(got, tc.want) {
				t.Fatalf("x operations = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestApplyDiffMixedSnapshotsChoiceTopLevel(t *testing.T) {
	setA := utDiffNamedSetDir(t, "schemas/cross-context-choice-a", "diff-cross-context-choice", nil)
	setB := utDiffNamedSetDir(t, "schemas/cross-context-choice-b", "diff-cross-context-choice", nil)
	const before = `<x xmlns="urn:vibe-ports:yang:diff-cross-context-choice">1</x>`
	const after = `<x xmlns="urn:vibe-ports:yang:diff-cross-context-choice">2</x>`
	first, second := parseOnlyXML(t, setA, before), parseOnlyXML(t, setB, after)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil || diff == nil {
		t.Fatalf("Diff = (%v, %v)", diff, err)
	}
	target := parseOnlyXML(t, setA, before)
	want := dumpTree(target)
	requireD0122Refusal(t, target.ApplyDiff(diff, ApplyDiffOptions{}), "lyd_diff_apply_all")
	if got := dumpTree(target); !slices.Equal(got, want) {
		t.Fatalf("refused apply changed tree = %v, want %v", got, want)
	}
}

func TestDiffMixedSnapshotsLYBRepresentationFamilies(t *testing.T) {
	setA := utDiffNamedSetDirs(t, "diff-cross-context-lyb-families", nil, "schemas", "../types/schemas")
	setB := utDiffNamedSetDirs(t, "diff-cross-context-lyb-families", nil, "schemas", "../types/schemas")
	const in = `<top xmlns="urn:vibe-ports:yang:diff-cross-context-lyb-families"><binary-value>AQID</binary-value><boolean-value>true</boolean-value><decimal-value>12.34</decimal-value><empty-value/><ipv4-zone>192.0.2.1%z</ipv4-zone><ipv4-no-zone>192.0.2.2</ipv4-no-zone><ipv4-prefix>192.0.2.129/24</ipv4-prefix><ipv6-zone>2001:db8::1%z</ipv6-zone><ipv6-no-zone>2001:db8::2</ipv6-no-zone><ipv6-prefix>2001:db8::ffff/64</ipv6-prefix><hex-value>0a:0b:0c</hex-value><time-period>2w</time-period><union-value>7</union-value></top>`
	first, second := parseOnlyXML(t, setA, in), parseOnlyXML(t, setB, in)
	if got, err := Diff(first, second, DiffOptions{}); got != nil || err != nil {
		t.Fatalf("LYB representation-family Diff = (%v, %v), want null", got, err)
	}
}

func TestDiffMixedSnapshotsExactLYBRealtypes(t *testing.T) {
	setA := utDiffNamedSetDir(t, "schemas/cross-context-lyb-a", "diff-cross-context-lyb", nil)
	setB := utDiffNamedSetDir(t, "schemas/cross-context-lyb-b", "diff-cross-context-lyb", nil)
	for _, tc := range []struct {
		name, xml string
		equal     bool
	}{
		{"ipv4-versus-string", `<top xmlns="urn:vibe-ports:yang:diff-cross-context-lyb"><address>1.2.3.4</address></top>`, false},
		{"enumeration-same-byte-width", `<top xmlns="urn:vibe-ports:yang:diff-cross-context-lyb"><enum-value>same</enum-value></top>`, true},
		{"enumeration-negative-byte-width", `<top xmlns="urn:vibe-ports:yang:diff-cross-context-lyb"><enum-negative>same</enum-negative></top>`, false},
		{"bits-same-byte-width", `<top xmlns="urn:vibe-ports:yang:diff-cross-context-lyb"><bits-value>same</bits-value></top>`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, second := parseOnlyXML(t, setA, tc.xml), parseOnlyXML(t, setB, tc.xml)
			a, b := topOf(first).kids.list[0], topOf(second).kids.list[0]
			if a.value.Canonical() != b.value.Canonical() {
				t.Fatalf("canonical values differ: %q and %q", a.value.Canonical(), b.value.Canonical())
			}
			if got := types.EqualLYB(a.value, b.value); got != tc.equal {
				t.Fatalf("equal LYB hash bytes = %v, want %v", got, tc.equal)
			}
			got, err := Diff(first, second, DiffOptions{})
			if err != nil || (got == nil) != tc.equal {
				t.Fatalf("Diff = (%v, %v), equal %v", got, err, tc.equal)
			}
		})
	}
}

func TestDiffMixedSnapshotsPluginLYB(t *testing.T) {
	setA, setB := crossContextPluginSet(t), crossContextPluginSet(t)
	const in = `<top xmlns="urn:vibe-ports:yang:diff-cross-context-plugins" xmlns:dcp="urn:vibe-ports:yang:diff-cross-context-plugins"><target><id>a</id></target><date>2024-01-02Z</date><time>12:34:56.25Z</time><xpath>count(/dcp:top/dcp:target)</xpath><node-instance-id>/dcp:top/dcp:target[dcp:id='a']</node-instance-id><instance-id-keys>[dcp:id='a']</instance-id-keys></top>`
	first, second := parseOnlyXML(t, setA, in), parseOnlyXML(t, setB, in)
	if got, err := Diff(first, second, DiffOptions{}); got != nil || err != nil {
		t.Fatalf("identical plugin values Diff = (%v, %v), want nil", got, err)
	}
}

func TestDiffMixedSnapshotsPluginSort(t *testing.T) {
	setA, setB := crossContextPluginSet(t), crossContextPluginSet(t)
	for _, tc := range []struct {
		name, first, second string
		wantOrder           []string
	}{
		{
			"ipv4",
			`<top xmlns="urn:vibe-ports:yang:diff-cross-context-plugins"><ipv4>2.0.0.0</ipv4></top>`,
			`<top xmlns="urn:vibe-ports:yang:diff-cross-context-plugins"><ipv4>10.0.0.0</ipv4></top>`,
			[]string{"2.0.0.0", "10.0.0.0"},
		},
		{
			"date-time",
			`<top xmlns="urn:vibe-ports:yang:diff-cross-context-plugins"><date-time>2024-01-02T00:00:00Z</date-time></top>`,
			`<top xmlns="urn:vibe-ports:yang:diff-cross-context-plugins"><date-time>2023-12-31T00:00:00Z</date-time></top>`,
			[]string{"2023-12-31T00:00:00Z", "2024-01-02T00:00:00Z"},
		},
		{
			"separate-plugin-runs",
			`<top xmlns="urn:vibe-ports:yang:diff-cross-context-plugins"><ipv4>2.0.0.0</ipv4><date-time>2024-01-02T00:00:00Z</date-time></top>`,
			`<top xmlns="urn:vibe-ports:yang:diff-cross-context-plugins"><ipv4>10.0.0.0</ipv4><date-time>2023-12-31T00:00:00Z</date-time></top>`,
			[]string{"2.0.0.0", "10.0.0.0", "2023-12-31T00:00:00Z", "2024-01-02T00:00:00Z"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := parseOnlyXML(t, setA, tc.first)
			cross := parseOnlyXML(t, setB, tc.second)
			got, err := Diff(first, cross, DiffOptions{})
			if err != nil {
				t.Fatal(err)
			}
			gotDump := dumpTree(got)
			positions := make([]int, len(tc.wantOrder))
			for i, value := range tc.wantOrder {
				positions[i] = slices.IndexFunc(gotDump, func(line string) bool {
					return strings.Contains(line, "="+value+" ")
				})
			}
			if slices.Contains(positions, -1) || !slices.IsSorted(positions) {
				t.Fatalf("cross-context Diff order = %v, want %v", gotDump, tc.wantOrder)
			}
		})
	}
}

// TestDiffMixedSnapshotsLYBGoldenJSON reproduces the oracle JSON payloads of the two LYB
// cross-context fixtures without depending on the separate conformance module.
func TestDiffMixedSnapshotsLYBGoldenJSON(t *testing.T) {
	setA := utDiffNamedSetDir(t, "schemas/cross-context-lyb-a", "diff-cross-context-lyb", nil)
	setB := utDiffNamedSetDir(t, "schemas/cross-context-lyb-b", "diff-cross-context-lyb", nil)
	for _, tc := range []struct {
		name, xml, want string
	}{
		{
			"fixed-width",
			`<top xmlns="urn:vibe-ports:yang:diff-cross-context-lyb"><enum-value>same</enum-value><enum-negative>same</enum-negative><bits-value>same</bits-value></top>`,
			"{\n" +
				"  \"diff-cross-context-lyb:top\": {\n" +
				"    \"@\": {\n" +
				"      \"yang:operation\": \"none\"\n" +
				"    },\n" +
				"    \"enum-negative\": [\n" +
				"      \"same\"\n" +
				"    ],\n" +
				"    \"@enum-negative\": [\n" +
				"      {\n" +
				"        \"yang:operation\": \"delete\"\n" +
				"      }\n" +
				"    ],\n" +
				"    \"diff-cross-context-lyb:enum-negative\": [\n" +
				"      \"same\"\n" +
				"    ],\n" +
				"    \"@diff-cross-context-lyb:enum-negative\": [\n" +
				"      {\n" +
				"        \"yang:operation\": \"create\"\n" +
				"      }\n" +
				"    ]\n" +
				"  }\n" +
				"}\n",
		},
		{
			"ipv4-string",
			`<top xmlns="urn:vibe-ports:yang:diff-cross-context-lyb"><address>1.2.3.4</address></top>`,
			"{\n" +
				"  \"diff-cross-context-lyb:top\": {\n" +
				"    \"@\": {\n" +
				"      \"yang:operation\": \"none\"\n" +
				"    },\n" +
				"    \"diff-cross-context-lyb:address\": [\n" +
				"      \"1.2.3.4\"\n" +
				"    ],\n" +
				"    \"@diff-cross-context-lyb:address\": [\n" +
				"      {\n" +
				"        \"yang:operation\": \"create\"\n" +
				"      }\n" +
				"    ],\n" +
				"    \"address\": [\n" +
				"      \"1.2.3.4\"\n" +
				"    ],\n" +
				"    \"@address\": [\n" +
				"      {\n" +
				"        \"yang:operation\": \"delete\"\n" +
				"      }\n" +
				"    ]\n" +
				"  }\n" +
				"}\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, second := parseOnlyXML(t, setA, tc.xml), parseOnlyXML(t, setB, tc.xml)
			got, err := Diff(first, second, DiffOptions{})
			if err != nil || got == nil {
				t.Fatalf("Diff = (%v, %v)", got, err)
			}
			assertPrintedJSON(t, got, tc.want)
		})
	}
}

func assertPrintedJSON(t *testing.T, tr *Tree, want string) {
	t.Helper()
	var out bytes.Buffer
	if err := tr.PrintJSON(&out, PrintOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != want {
		t.Fatalf("JSON diff:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// mixedSnapshotSingletonWork diffs N corresponding singleton leaves from separate contexts and
// returns sibling visits. The schema order and values vary without changing the linear lookup
// obligation.
func mixedSnapshotSingletonWork(t *testing.T, n int, pattern string) int64 {
	t.Helper()
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
	makeTree := func() *Tree {
		m := &schema.Module{Name: "work", Namespace: "urn:work", Prefix: "w", Implemented: true}
		set := &schema.Set{Modules: []*schema.Module{m}}
		str := &schema.Type{Base: schema.String}
		for _, i := range order {
			m.Top = append(m.Top, &schema.Node{Kind: schema.Leaf, Name: fmt.Sprintf("n%06d", i), Module: m,
				Type: str, Config: true})
		}
		tr := newTree(set)
		for i, sn := range m.Top {
			value := fmt.Sprintf("v%06d", i)
			if pattern == "duplicate-heavy" {
				value = fmt.Sprintf("v%d", i%4)
			}
			v, d := types.StoreOnly(sn.Type, value, types.FormatXML, types.HintData, nil, sn)
			if d != nil {
				t.Fatal(d.Msg)
			}
			tr.insert(nil, newTerm(sn, v), insertDefault)
		}
		tr.work.Store(0)
		tr.work.visits = true
		return tr
	}
	first, second := makeTree(), makeTree()
	if got, err := Diff(first, second, DiffOptions{}); got != nil || err != nil {
		t.Fatalf("Diff = (%v, %v)", got, err)
	}
	return first.work.Load() + second.work.Load()
}

func TestDiffMixedSnapshotsSchemaLookupWork(t *testing.T) {
	for _, pattern := range []string{"sorted", "reverse", "shuffled", "duplicate-heavy"} {
		t.Run(pattern, func(t *testing.T) {
			const n = 64
			small := mixedSnapshotSingletonWork(t, n, pattern)
			large := mixedSnapshotSingletonWork(t, 4*n, pattern)
			if small == 0 || large > 6*small {
				t.Fatalf("work(%d) = %d, work(%d) = %d; want work(4N) <= 6*work(N)", n, small, 4*n, large)
			}
		})
	}
}

// mixedSnapshotKeyedWork diffs N keyed list instances from separate contexts. Unlike the
// singleton test, this exercises the key-value contribution to lyd_hash as well as lookup.
func mixedSnapshotKeyedWork(t *testing.T, n int, pattern string) int64 {
	t.Helper()
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
	makeTree := func(set *schema.Set) *Tree {
		var in strings.Builder
		in.WriteString(`<df xmlns="urn:libyang:tests:defaults">`)
		for _, i := range order {
			key := fmt.Sprintf("k%06d", i)
			if pattern == "duplicate-heavy" {
				key = fmt.Sprintf("k%d", i%4)
			}
			fmt.Fprintf(&in, "<list><name>%s</name><value>same</value></list>", key)
		}
		in.WriteString(`</df>`)
		tr := parseOnlyXML(t, set, in.String())
		tr.work.Store(0)
		tr.work.visits = true
		return tr
	}
	first, second := makeTree(utDiffSet(t)), makeTree(utDiffSet(t))
	if got, err := Diff(first, second, DiffOptions{}); got != nil || err != nil {
		t.Fatalf("Diff = (%v, %v)", got, err)
	}
	return first.work.Load() + second.work.Load()
}

func TestDiffMixedSnapshotsKeyHashWork(t *testing.T) {
	for _, pattern := range []string{"sorted", "reverse", "shuffled", "duplicate-heavy"} {
		t.Run(pattern, func(t *testing.T) {
			const n = 32
			small := mixedSnapshotKeyedWork(t, n, pattern)
			large := mixedSnapshotKeyedWork(t, 4*n, pattern)
			if small == 0 || large > 6*small {
				t.Fatalf("work(%d) = %d, work(%d) = %d; want work(4N) <= 6*work(N)", n, small, 4*n, large)
			}
		})
	}
}

// TestDiffMixedSnapshotsParentCheck: lyd_diff still compares non-nil data-parent schema pointers,
// so corresponding nested nodes from separate contexts are unrelated entry points.
func TestDiffMixedSnapshotsParentCheck(t *testing.T) {
	set1, set2 := utDiffSet(t), utDiffSet(t)
	first := parseOnlyXML(t, set1, `<df xmlns="urn:libyang:tests:defaults"><foo>1</foo></df>`)
	second := parseOnlyXML(t, set2, `<df xmlns="urn:libyang:tests:defaults"><foo>1</foo></df>`)
	a, err := first.Find("/defaults:df/foo")
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Find("/defaults:df/foo")
	if err != nil {
		t.Fatal(err)
	}
	got, err := DiffSiblings(a, b, DiffOptions{})
	var ve *ValidationError
	if got != nil || !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" {
		t.Fatalf("DiffSiblings = (%v, %v)", got, err)
	}
}

// TestDiffEmptyMixedSnapshots: an empty tree is libyang's NULL even when it names a different
// schema snapshot. A diff with one populated input belongs to that input's snapshot.
func TestDiffEmptyMixedSnapshots(t *testing.T) {
	set1, set2 := utDiffSet(t), utDiffSet(t)
	first := parseOnlyXML(t, set1, `<df xmlns="urn:libyang:tests:defaults"><foo>1</foo></df>`)
	second := parseOnlyXML(t, set2, `<df xmlns="urn:libyang:tests:defaults"><foo>2</foo></df>`)

	tests := []struct {
		name      string
		first     *Tree
		second    *Tree
		nilFirst  *Tree
		nilSecond *Tree
		resultSet *schema.Set
	}{
		{"both empty", newTree(set1), newTree(set2), nil, nil, nil},
		{"empty then populated", newTree(set1), second, nil, second, set2},
		{"populated then empty", first, newTree(set2), first, nil, set1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Diff(tc.first, tc.second, DiffOptions{})
			want, wantErr := Diff(tc.nilFirst, tc.nilSecond, DiffOptions{})
			if err != nil || wantErr != nil {
				t.Fatalf("Diff errors = (%v, %v)", err, wantErr)
			}
			if got == nil || want == nil {
				if got != want {
					t.Fatalf("Diff nil result = (%v, %v)", got == nil, want == nil)
				}
				return
			}
			if got.set != tc.resultSet {
				t.Errorf("result schema set = %p, want %p", got.set, tc.resultSet)
			}
			if gotDump, wantDump := dumpTree(got), dumpTree(want); !slices.Equal(gotDump, wantDump) {
				t.Errorf("Diff tree = %v, want nil-equivalent %v", gotDump, wantDump)
			}
		})
	}
}
