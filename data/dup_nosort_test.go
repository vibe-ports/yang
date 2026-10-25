// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"slices"
	"testing"

	"github.com/vibe-ports/yang/data"
)

// TestDupNoSort checks DupOptions.NoSort (LYD_DUP_NO_LYDS): the copies of leaf-list instances go
// after the instances of their schema node (tree_data.c:2461-2478) instead of to their value
// position, a later ordinary insertion goes on working, and the zero value keeps the sorted copy.
func TestDupNoSort(t *testing.T) {
	s := fzContext(t).Schema()
	values := func(tr *data.Tree) []string {
		c, err := tr.Find("/fz:c")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for ch := range c.Children() {
			if ch.Name() == "ll" {
				out = append(out, ch.Value())
			}
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		o        data.DupOptions
		wantDup  []string
		wantMore []string // after NewPath of ll=4 and ll=2
	}{
		{"sorted", data.DupOptions{Recursive: true}, []string{"1", "3", "5", "9"}, []string{"1", "2", "3", "4", "5", "9"}},
		{"nosort", data.DupOptions{Recursive: true, NoSort: true}, []string{"3", "5", "1", "9"}, []string{"2", "3", "4", "5", "1", "9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, _, err := parse(s, `{"fz:c":{"ll":[9,1]}}`, data.FormatJSON, data.ParseOptions{})
			if err != nil {
				t.Fatal(err)
			}
			dst, _, err := parse(s, `{"fz:c":{"ll":[3,5]}}`, data.FormatJSON, data.ParseOptions{})
			if err != nil {
				t.Fatal(err)
			}
			first, err := src.Find("/fz:c/ll[.='1']")
			if err != nil {
				t.Fatal(err)
			}
			parent, err := dst.Find("/fz:c")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := dst.DupSiblings(first, parent, tc.o); err != nil {
				t.Fatal(err)
			}
			if got := values(dst); !slices.Equal(got, tc.wantDup) {
				t.Fatalf("after dup %v, want %v", got, tc.wantDup)
			}
			for _, v := range []string{"4", "2"} {
				if _, err := dst.NewPath("/fz:c/ll", v, data.NewPathOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("after insertions %v", values(dst))
			if tc.wantMore != nil {
				if got := values(dst); !slices.Equal(got, tc.wantMore) {
					t.Fatalf("after insertions %v, want %v", got, tc.wantMore)
				}
			}
		})
	}
}
