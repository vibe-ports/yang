// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/snap"
)

// TestNewTree builds the tree of the seq/new-tree-from-paths fixture with NewPath on an empty
// tree, validates it and compares it with the oracle's dump of the last step.
func TestNewTree(t *testing.T) {
	set := editSet(t)
	tr := NewTree(snap.New(set))
	if len(tr.top.list) != 0 {
		t.Fatal("NewTree is not empty")
	}
	for _, s := range []editStep{{set: "/pv2-edit:c/l[k='a']/v", value: "1"}, {set: "/pv2-edit:c/a", value: "x"},
		{set: "/pv2-edit:c/ll", value: "z"}} {
		if rc, d := s.apply(tr); rc != "LY_SUCCESS" {
			t.Fatal(rc, d)
		}
	}
	if _, err := tr.Validate(context.Background(), ValidateOptions{}); err != nil {
		t.Fatal(err)
	}
	steps := loadSteps(t, "seq-new-tree-from-paths")
	// the oracle's context also holds ietf-yang-library and its implicit defaults (editSet does not)
	own := func(l []string) []string {
		return slices.DeleteFunc(l, func(s string) bool { return !strings.HasPrefix(s, "/pv2-edit:") })
	}
	if got, want := own(typedDump(tr)), own(steps[len(steps)-1].dump()); !reflect.DeepEqual(got, want) {
		t.Errorf("tree:\n got  %q\n want %q", got, want)
	}
	// a zero Tree has no schema: the path's module is not found
	if _, err := new(Tree).NewPath("/pv2-edit:c/a", "x", NewPathOptions{}); err == nil {
		t.Error("NewPath on a zero Tree succeeded")
	}
}
