// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

// utDiffSet compiles the ut-diff module defaults (tests/utests/data/test_diff.c).
func utDiffSet(t *testing.T) *schema.Set {
	t.Helper()
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true},
		os.DirFS("../conformance/corpus/ut-diff/schemas"))
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load("defaults", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	return set
}

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

// TestDiffMixedSnapshots: top-level nodes have nil data parents, so that check alone must not
// allow trees compiled in different schema snapshots to be diffed (D-0069: libyang matches them by
// schema lineage, not ported yet, #236).
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
			var ve *ValidationError
			if got != nil || !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" {
				t.Fatalf("Diff = (%v, %v)", got, err)
			}
		})
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
