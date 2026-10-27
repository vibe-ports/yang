// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"testing"
)

// lyd_diff compares non-nil data-parent schema pointers, so corresponding nested
// nodes from separate contexts are unrelated entry points.
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
