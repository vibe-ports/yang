// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"slices"
	"testing"
)

// TestChildrenNoKeys: lyd_child_no_keys skips the leading keys of a list instance only.
func TestChildrenNoKeys(t *testing.T) {
	tr := newTree(editSet(t))
	for _, p := range []string{"/pv2-edit:c/a", "/pv2-edit:c/l[k='a']/v", "/pv2-edit:c/st/x"} {
		if _, err := tr.NewPath(p, "1", NewPathOptions{}); err != nil {
			t.Fatal(p, err)
		}
	}
	names := func(path string, all bool) []string {
		n, err := tr.Find(path)
		if err != nil || n == nil {
			t.Fatal(path, err)
		}
		var out []string
		seq := n.ChildrenNoKeys()
		if all {
			seq = n.Children()
		}
		for c := range seq {
			out = append(out, c.Name())
		}
		return out
	}
	for _, tc := range []struct {
		path string
		want []string
	}{
		{"/pv2-edit:c/l[k='a']", []string{"v"}},
		{"/pv2-edit:c/st[1]", []string{"x"}},        // keyless list
		{"/pv2-edit:c", names("/pv2-edit:c", true)}, // a container: every child
		{"/pv2-edit:c/a", nil},                      // a term has none
	} {
		if got := names(tc.path, false); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.path, got, tc.want)
		}
	}
	if got := names("/pv2-edit:c/l[k='a']", true); !slices.Equal(got, []string{"k", "v"}) {
		t.Errorf("Children of the list: %q", got)
	}
}
