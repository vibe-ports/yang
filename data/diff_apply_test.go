// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

func TestApplyDiffCrossContextContainerLeaf(t *testing.T) {
	load := func() *schema.Set {
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS(filepath.Join(pv2Dir, "schemas")))
		if err != nil {
			t.Fatal(err)
		}
		if _, diags, err := c.Load("pv2-detached", "", nil); err != nil {
			t.Fatal(err, diags)
		}
		return c.Snapshot()
	}
	setA, setB := load(), load()
	parse := func(set *schema.Set, value string) *Tree {
		tr, diags, err := parseJSONString(set, `{"pv2-detached:c":{"base":"`+value+`"}}`, Reject, false)
		if err != nil {
			t.Fatal(err, diags)
		}
		return tr
	}
	a, oldB, newB := parse(setA, "x"), parse(setB, "x"), parse(setB, "y")
	diff, err := Diff(oldB, newB, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	n, err := a.Find("/pv2-detached:c/base")
	if err != nil || n == nil || n.Value() != "y" {
		t.Fatalf("matched leaf: %v, %v", n, err)
	}
}

func TestApplyDiffMixedRootsFindAndNewPath(t *testing.T) {
	load := func() *schema.Set {
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS(filepath.Join(pv2Dir, "schemas")))
		if err != nil {
			t.Fatal(err)
		}
		if _, diags, err := c.Load("pv2-detached", "", nil); err != nil {
			t.Fatal(err, diags)
		}
		return c.Snapshot()
	}
	a, b := load(), load()
	parse := func(set *schema.Set, input string) *Tree {
		tr, diags, err := parseJSONString(set, input, Reject, false)
		if err != nil {
			t.Fatal(err, diags)
		}
		return tr
	}
	const retained = `{"pv2-detached:l":[{"k":"a"}]}`
	target := parse(a, retained)
	before := parse(b, retained)
	after := parse(b, `{"pv2-detached:l":[{"k":"a"}],"pv2-detached:c":{"base":"x"}}`)
	diff, err := Diff(before, after, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := target.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	base, err := target.Find("/pv2-detached:c/base")
	if err != nil || base == nil || base.Value() != "x" {
		t.Fatalf("Find foreign root child: %v, %v", base, err)
	}
	foundL, err := target.Find("/pv2-detached:l[k='a']")
	if err != nil || foundL == nil {
		t.Fatalf("Find retained root from first context: %v, %v", foundL, err)
	}
	changed, err := target.NewPath("/pv2-detached:c/base", "y", NewPathOptions{Update: true})
	if err != nil || changed != base {
		t.Fatalf("NewPath foreign root child: %v, %v", changed, err)
	}
	if base.Value() != "y" {
		t.Fatalf("updated value = %q", base.Value())
	}
	// The next diff is compiled in A, the Tree's original context, while
	// the matched container and term still belong to B.
	oldA := parse(a, `{"pv2-detached:c":{"base":"y"}}`)
	newA := parse(a, `{"pv2-detached:c":{"base":"z"}}`)
	aDiff, err := Diff(oldA, newA, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := target.ApplyDiff(aDiff, ApplyDiffOptions{}); err != nil {
		t.Fatalf("ApplyDiff with target context equal to original Tree context: %v", err)
	}
	if base.Value() != "z" {
		t.Fatalf("second diff value = %q", base.Value())
	}
	var roots int
	for n := range target.Top() {
		if n.Name() == "c" {
			roots++
		}
	}
	if roots != 1 {
		t.Fatalf("container roots = %d, want 1", roots)
	}
}

func TestXPathUsesFirstRootContextWithForeignContextNode(t *testing.T) {
	load := func(modules ...string) *schema.Set {
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS(filepath.Join(pv2Dir, "schemas")))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range modules {
			if _, diags, err := c.Load(name, "", nil); err != nil {
				t.Fatal(name, err, diags)
			}
		}
		return c.Snapshot()
	}
	a, b := load("pv2-detached", "pv2-edit"), load("pv2-edit")
	target, diags, err := parseJSONString(a, `{"pv2-detached:l":[{"k":"a"}]}`, Reject, false)
	if err != nil {
		t.Fatal(err, diags)
	}
	source, diags, err := parseJSONString(b, `{"pv2-edit:c":{"a":"b"}}`, Reject, false)
	if err != nil {
		t.Fatal(err, diags)
	}
	diff, err := Diff(nil, source, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := target.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	foreign, err := target.Find("/pv2-edit:c")
	if err != nil || foreign == nil {
		t.Fatalf("foreign root: %v, %v", foreign, err)
	}
	if !target.top.mixedContexts() {
		t.Fatal("foreign root did not mark mixed sibling contexts")
	}
	r, diags, err := target.EvalXPath("/pv2-detached:l[k='a']", XPathOptions{Node: foreign})
	if err != nil || len(r.Nodes) != 1 {
		t.Fatalf("tree-context XPath: result %+v, diagnostics %+v, error %v", r, diags, err)
	}
	if err := foreign.Unlink(); err != nil {
		t.Fatal(err)
	}
	if target.top.mixedContexts() {
		t.Fatal("unlink left a stale foreign context count")
	}
}

func TestXPathRetainedNodesAfterForeignRootCreate(t *testing.T) {
	load := func() *schema.Set {
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS(filepath.Join(pv2Dir, "schemas")))
		if err != nil {
			t.Fatal(err)
		}
		if _, diags, err := c.Load("pv2-detached", "", nil); err != nil {
			t.Fatal(err, diags)
		}
		return c.Snapshot()
	}
	a, b := load(), load()
	source, diags, err := parseJSONString(b, `{"pv2-detached:l":[{"k":"b"}]}`, Reject, false)
	if err != nil {
		t.Fatal(err, diags)
	}
	diff, err := Diff(nil, source, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ data, expr, path string }{
		{`{"pv2-detached:c":{"base":"a"}}`, "/pv2-detached:c", "/pv2-detached:c"},
		{`{"pv2-detached:c":{"base":"a"}}`, "/pv2-detached:c/base", "/pv2-detached:c/base"},
		{`{"pv2-detached:ll":["a"]}`, "/pv2-detached:ll[.='a']", "/pv2-detached:ll[.='a']"},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			target, diags, err := parseJSONString(a, tc.data, Reject, false)
			if err != nil {
				t.Fatal(err, diags)
			}
			if err := target.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
				t.Fatal(err)
			}
			if !target.top.mixedContexts() || ctxOf(target.top.list[0]) != b {
				t.Fatal("foreign root was not first in a mixed-context tree")
			}
			r, diags, err := target.EvalXPath(tc.expr, XPathOptions{})
			if err != nil || len(r.Nodes) != 1 || r.Nodes[0].Path() != tc.path {
				t.Fatalf("XPath %q: result %+v, diagnostics %+v, error %v", tc.expr, r, diags, err)
			}
		})
	}
}

// lyd_diff_apply_r duplicates each create in its source context, then
// lyd_insert_child checks the original schema parent pointer. A preceding
// replace remains applied when the later create fails.
func TestApplyDiffCrossContextCreateKeepsPartialState(t *testing.T) {
	load := func() *schema.Set {
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS(filepath.Join(pv2Dir, "schemas")))
		if err != nil {
			t.Fatal(err)
		}
		if _, diags, err := c.Load("pv2-detached", "", []string{"extra"}); err != nil {
			t.Fatal(err, diags)
		}
		return c.Snapshot()
	}
	setA, setB := load(), load()
	target, _, err := parseJSONString(setA, `{"pv2-detached:c":{"base":"old"}}`, Reject, false)
	if err != nil {
		t.Fatal(err)
	}
	diff := parseOnlyXML(t, setB, `<c xmlns="urn:vibe-ports:yang:conformance:pv2-detached"`+
		` xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none">`+
		`<base yang:operation="replace">new</base><extra yang:operation="create">x</extra></c>`)
	err = target.ApplyDiff(diff, ApplyDiffOptions{})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || len(ve.Diags) != 1 ||
		ve.Diags[0].Msg != `Cannot insert, parent of "extra" is not "c".` {
		t.Fatalf("ApplyDiff = %v", err)
	}
	base, _ := target.Find("/pv2-detached:c/base")
	extra, _ := target.Find("/pv2-detached:c/extra")
	if base == nil || base.Value() != "new" || extra != nil {
		t.Fatalf("partial tree: base=%v extra=%v", base, extra)
	}
}

func TestMergeDiffTreeDetachedParentForeignSource(t *testing.T) {
	load := func() *schema.Set {
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS(filepath.Join(pv2Dir, "schemas")))
		if err != nil {
			t.Fatal(err)
		}
		if _, diags, err := c.Load("pv2-detached", "", nil); err != nil {
			t.Fatal(err, diags)
		}
		return c.Snapshot()
	}
	setA, setB := load(), load()
	target, _, err := parseJSONString(setA, `{"pv2-detached:c":{}}`, Reject, false)
	if err != nil {
		t.Fatal(err)
	}
	parent := topOf(target)
	if err := parent.Unlink(); err != nil {
		t.Fatal(err)
	}
	source := parseOnlyXML(t, setB, `<c xmlns="urn:vibe-ports:yang:conformance:pv2-detached" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none"><base yang:operation="create">x</base></c>`)
	err = target.MergeDiffTree(parent, firstNoKeys(topOf(source)), MergeDiffOptions{})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || len(ve.Diags) != 1 ||
		ve.Diags[0].Msg != `Different "node" and "parent" contexts used in node duplication.` {
		t.Fatalf("MergeDiffTree: %v", err)
	}
}

// TestMergeDiffTreeMixedRootParent uses an A parent after a B root in the same diff tree.
// The source and parent share A, regardless of the first root's context.
func TestMergeDiffTreeMixedRootParent(t *testing.T) {
	load := func() *schema.Set {
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS(filepath.Join(pv2Dir, "schemas")))
		if err != nil {
			t.Fatal(err)
		}
		if _, diags, err := c.Load("pv2-detached", "", nil); err != nil {
			t.Fatal(err, diags)
		}
		return c.Snapshot()
	}
	a, b := load(), load()
	ns := ` xmlns="urn:vibe-ports:yang:conformance:pv2-detached" xmlns:yang="urn:ietf:params:xml:ns:yang:1"`
	target := parseOnlyXML(t, a, `<c`+ns+` yang:operation="none"/>`)
	parent := topOf(target)
	foreign := parseOnlyXML(t, b, `<l`+ns+`><k>b</k></l>`)
	diff, err := Diff(nil, foreign, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := target.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	if target.context() != b || ctxOf(parent) != a {
		t.Fatal("expected B root before A parent")
	}
	source := parseOnlyXML(t, a, `<c`+ns+` yang:operation="none"><base yang:operation="create">x</base></c>`)
	if err := target.MergeDiffTree(parent, firstNoKeys(topOf(source)), MergeDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	child := firstNoKeys(parent)
	if child == nil || child.Name() != "base" || ctxOf(child) != a || child.Value() != "x" {
		t.Fatalf("merged child = %v", child)
	}
}

// TestApplyOpaqueCreate: an opaque diff node with its own yang:operation="create" crashes
// libyang v5.8.6 in lyd_diff_apply_all (oracle exit 139, so no fixture); the port
// reads the attribute and safely refuses the unsupported operation.
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
	if want := `Operation "create" is invalid for opaque node "zz".`; ve.Diags[0].Msg != want {
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

// TestMergeDiffTreeDetachedParent: lyd_diff_merge_tree accepts an unlinked live diff parent and
// uses its retained context; the merged child remains in that detached subtree.
func TestMergeDiffTreeDetachedParent(t *testing.T) {
	set := utDiffSet(t)
	target := parseOnlyXML(t, set, `<df xmlns="urn:libyang:tests:defaults"/>`)
	source := parseOnlyXML(t, set, `<df xmlns="urn:libyang:tests:defaults" xmlns:yang="urn:ietf:params:xml:ns:yang:1"`+
		` yang:operation="none"><foo yang:operation="create">1</foo></df>`)
	parent := topOf(target)
	if err := parent.Unlink(); err != nil {
		t.Fatal(err)
	}
	if err := target.MergeDiffTree(parent, firstNoKeys(topOf(source)), MergeDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	if child := firstNoKeys(parent); child == nil || child.Name() != "foo" || child.parent != parent {
		t.Fatalf("detached merged child = %v", child)
	}
}

func topOf(t *Tree) *Node { return t.top.first() }
