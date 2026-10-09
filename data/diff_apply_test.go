// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"testing"
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
