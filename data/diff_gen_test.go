// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

// TestDiffDisjointLeavesWork counts unsuccessful schema lookups when each side
// holds N distinct sibling leaves from one context. A name fallback here would
// visit every sibling for every leaf.
func TestDiffDisjointLeavesWork(t *testing.T) {
	var work []int64
	for _, n := range []int{64, 128, 256, 512} {
		var module strings.Builder
		module.WriteString(`module m { namespace "urn:m"; prefix m; container c {`)
		for i := range 2 * n {
			fmt.Fprintf(&module, "leaf v%d { type string; }", i)
		}
		module.WriteString("} }")
		set := opIndexSet(t, module.String())
		first, second := newTree(set), newTree(set)
		for i := range n {
			if _, err := first.NewPath(fmt.Sprintf("/m:c/v%d", i), "x", NewPathOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := second.NewPath(fmt.Sprintf("/m:c/v%d", n+i), "x", NewPathOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		first.work.Store(0)
		second.work.Store(0)
		first.work.visits, second.work.visits = true, true
		if _, err := Diff(first, second, DiffOptions{}); err != nil {
			t.Fatal(err)
		}
		units := first.work.Load() + second.work.Load()
		work = append(work, units)
		if units > int64(32*n) {
			t.Errorf("%d disjoint leaves: %d work units", n, units)
		}
	}
	for i := 1; i < len(work); i++ {
		if work[i] > 3*work[i-1] {
			t.Errorf("work doubles from %d to %d leaves: %d to %d units", 64<<(i-1), 64<<i, work[i-1], work[i])
		}
	}
}

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

// TestDiffDetachedUserOrdered: lyd_diff_tree treats an unlinked user-ordered list or leaf-list
// as a singleton sibling set and emits its deletion.
func TestDiffDetachedUserOrdered(t *testing.T) {
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS(filepath.Join(pv2Dir, "schemas")))
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load("pv2-detached", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	set := c.Snapshot()
	for _, path := range []string{
		"/pv2-detached:l[k='a']",
		"/pv2-detached:ll[.='a']",
		"/pv2-detached:c/l[k='a']",
		"/pv2-detached:c/ll[.='a']",
	} {
		tr, _, err := parseJSONString(set, `{"pv2-detached:l":[{"k":"a"}],"pv2-detached:ll":["a"],`+
			`"pv2-detached:c":{"l":[{"k":"a"}],"ll":["a"]}}`, Reject, false)
		if err != nil {
			t.Fatal(err)
		}
		n, err := tr.Find(path)
		if err != nil || n == nil {
			t.Fatal(path, n, err)
		}
		if err := n.Unlink(); err != nil {
			t.Fatal(path, err)
		}
		diff, err := DiffTree(n, nil, DiffOptions{})
		if n.schema.DataParent() != nil {
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || diff != nil {
				t.Fatalf("DiffTree(%s, nil) = %v, %v; want contextless LY_EINVAL", path, diff, err)
			}
			continue
		}
		if err != nil || diff == nil {
			t.Fatalf("DiffTree(%s, nil) = %v, %v", path, diff, err)
		}
		if op, found := diffGetOp(diff.top.first()); !found || op != diffDelete {
			t.Fatalf("DiffTree(%s, nil) operation = %v, %v", path, op, found)
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
