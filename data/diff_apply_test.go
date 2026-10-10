// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"os"
	"testing"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

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
