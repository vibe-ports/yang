// SPDX-License-Identifier: BSD-3-Clause

package data

import "testing"

// TestEqualNil: lyd_compare_single(NULL, NULL) is LY_SUCCESS, one NULL node is LY_ENOT (the
// oracle's compare step always passes nodes; ut-compare/* and seq/compare-* cover the rest).
func TestEqualNil(t *testing.T) {
	tr := newTree(editSet(t))
	n, err := tr.NewPath("/pv2-edit:c/a", "x", NewPathOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var none *Node
	if !none.Equal(nil, CompareOptions{}) || n.Equal(nil, CompareOptions{}) || none.Equal(n, CompareOptions{}) ||
		!n.Equal(n, CompareOptions{FullRecursion: true, Defaults: true, Opaque: true}) {
		t.Error("nil handling")
	}
}
