// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

func reviewDiffSet(t *testing.T, module string) *schema.Set {
	t.Helper()
	name := strings.Fields(module)[1]
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fstest.MapFS{
		name + ".yang": {Data: []byte(module)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load(name, "", nil); err != nil {
		t.Fatal(err, diags)
	}
	return c.Snapshot()
}

// TestDiffCrossContextHashAnchorContinuesForeignSiblings covers the children_ht branch of
// lyd_insert_get_next_anchor. lys_getnext receives a foreign last node and a local parent, so it
// follows the foreign node's next sibling before it eventually restarts at the local children.
func TestDiffCrossContextHashAnchorContinuesForeignSiblings(t *testing.T) {
	module := `module review-anchor { yang-version 1.1; namespace "urn:review-anchor"; prefix r;
  container t {
    leaf a { type string; } leaf b { type string; } leaf c { type string; }
    leaf d { type string; } leaf e { type string; }
  }
}`
	a, b := reviewDiffSet(t, module), reviewDiffSet(t, module)
	first := parseOnlyXML(t, a, `<t xmlns="urn:review-anchor"><a>x</a><c>x</c><d>x</d><e>old</e></t>`)
	second := parseOnlyXML(t, b, `<t xmlns="urn:review-anchor"><b>y</b><e>new</e></t>`)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(topOf(diff).Children()), []string{"e=new", "a=x", "b=y", "c=x", "d=x"}; !slices.Equal(got, want) {
		t.Fatalf("diff order = %v, want %v", got, want)
	}
}

// TestDiffCrossContextSortedRunMissUsesAnchor covers a system-ordered node with no matching run.
// lyd_insert_node still asks lyd_insert_get_next_anchor for its foreign-context schema position.
func TestDiffCrossContextSortedRunMissUsesAnchor(t *testing.T) {
	module := `module review-sorted { yang-version 1.1; namespace "urn:review-sorted"; prefix r;
  container t { leaf a { type string; } leaf-list b { type string; } }
}`
	a, b := reviewDiffSet(t, module), reviewDiffSet(t, module)
	first := parseOnlyXML(t, a, `<t xmlns="urn:review-sorted"><a>x</a></t>`)
	second := parseOnlyXML(t, b, `<t xmlns="urn:review-sorted"><b>y</b></t>`)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(topOf(diff).Children()), []string{"b=y", "a=x"}; !slices.Equal(got, want) {
		t.Fatalf("diff order = %v, want %v", got, want)
	}
}

// TestDiffCrossContextSchemaLookupScaling verifies that independently compiled snapshots build
// one lineage index per input sibling set instead of rescanning N siblings for every leaf. The
// lowered quota also proves that the public operation's fixed ceiling returns ErrBudget.
func TestDiffCrossContextSchemaLookupScaling(t *testing.T) {
	var previous int64
	for _, n := range []int{64, 128, 256, 512} {
		var module, data strings.Builder
		module.WriteString(`module review-scale { namespace "urn:review-scale"; prefix r; container t {`)
		data.WriteString(`<t xmlns="urn:review-scale">`)
		for i := range n {
			fmt.Fprintf(&module, "leaf v%d { type string; }", i)
			fmt.Fprintf(&data, "<v%d>x</v%d>", i, i)
		}
		module.WriteString("} }")
		data.WriteString("</t>")
		a, b := reviewDiffSet(t, module.String()), reviewDiffSet(t, module.String())
		first, second := parseOnlyXML(t, a, data.String()), parseOnlyXML(t, b, data.String())
		first.work.Store(0)
		second.work.Store(0)
		first.work.visits, second.work.visits = true, true
		if diff, err := Diff(first, second, DiffOptions{}); diff != nil || err != nil {
			t.Fatalf("%d leaves: Diff = (%v, %v)", n, diff, err)
		}
		work := first.work.Load() + second.work.Load()
		if work > int64(16*n) {
			t.Errorf("%d leaves: %d sibling visits, want linear work", n, work)
		}
		if previous != 0 && work > 3*previous {
			t.Errorf("doubling leaves grew work from %d to %d", previous, work)
		}
		previous = work

		if n == 64 {
			diff, err := diffNodesLookupLimit(topOf(first), topOf(second), DiffOptions{}, false, first, second, 32)
			if diff != nil || !errors.Is(err, yang.ErrBudget) {
				t.Fatalf("lookup limit: Diff = (%v, %v), want nil, ErrBudget", diff, err)
			}
		}
	}
}
