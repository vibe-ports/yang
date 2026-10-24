// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"weak"

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

// TestDiffCrossContextSortedRunUsesRBTraversal pins rb_insert_node's comparison path after its
// deterministic rotation. int16 reads each int8 union payload at int16 width until inserting -1
// rotates -2 to the root; the final int8 comparator then puts 0 after -1.
func TestDiffCrossContextSortedRunUsesRBTraversal(t *testing.T) {
	module := func(typ string) string {
		return `module review-rb-order { yang-version 1.1; namespace "urn:review-rb-order"; prefix r;
  container top { presence "retain"; leaf-list v { type ` + typ + `; } }
}`
	}
	firstSet, secondSet := reviewDiffSet(t, module("int16")), reviewDiffSet(t, module("int8"))
	first := parseOnlyXML(t, firstSet, `<top xmlns="urn:review-rb-order"><v>1</v></top>`)
	second := parseOnlyXML(t, secondSet, `<top xmlns="urn:review-rb-order"><v>-2</v><v>-1</v><v>0</v></top>`)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(topOf(diff).Children()), []string{"v=1", "v=-2", "v=-1", "v=0"}; !slices.Equal(got, want) {
		t.Fatalf("diff order = %v, want native RB order %v", got, want)
	}
}

// TestDiffCrossContextIntegerCallbackReadsZeroedStringStorage covers different sort callbacks
// whose C storage comparison is nevertheless defined. string_store zeroes the value union, so
// the int8 callback compares -1 with zero and links string "2" after it.
func TestDiffCrossContextIntegerCallbackReadsZeroedStringStorage(t *testing.T) {
	module := func(typ string) string {
		return `module review-int-string { yang-version 1.1; namespace "urn:review-int-string"; prefix r;
  container top { presence "retain"; leaf-list v { type ` + typ + `; } }
}`
	}
	firstSet, secondSet := reviewDiffSet(t, module("int8")), reviewDiffSet(t, module("string"))
	first := parseOnlyXML(t, firstSet, `<top xmlns="urn:review-int-string"><v>-1</v></top>`)
	second := parseOnlyXML(t, secondSet, `<top xmlns="urn:review-int-string"><v>2</v></top>`)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(topOf(diff).Children()), []string{"v=-1", "v=2"}; !slices.Equal(got, want) {
		t.Fatalf("diff order = %v, want zero-initialized storage order %v", got, want)
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

// TestDiffCrossContextDisjointSchemaLookupScaling covers indexed misses, not only hits. Once the
// foreign lineage index misses, a second sibling scan is both redundant and outside its budget.
func TestDiffCrossContextDisjointSchemaLookupScaling(t *testing.T) {
	const n = 256
	module := func(side string) string {
		var text strings.Builder
		fmt.Fprint(&text, `module review-disjoint { namespace "urn:review-disjoint"; prefix r; container t {`)
		for i := range n {
			fmt.Fprintf(&text, "leaf %s%d { type string; }", side, i)
		}
		fmt.Fprint(&text, "} }")
		return text.String()
	}
	data := func(side string) string {
		var text strings.Builder
		fmt.Fprint(&text, `<t xmlns="urn:review-disjoint">`)
		for i := range n {
			fmt.Fprintf(&text, "<%s%d>x</%s%d>", side, i, side, i)
		}
		fmt.Fprint(&text, "</t>")
		return text.String()
	}
	firstSet, secondSet := reviewDiffSet(t, module("a")), reviewDiffSet(t, module("b"))
	first, second := parseOnlyXML(t, firstSet, data("a")), parseOnlyXML(t, secondSet, data("b"))
	first.work.Store(0)
	second.work.Store(0)
	first.work.visits, second.work.visits = true, true
	if _, err := diffNodesLookupLimit(topOf(first), topOf(second), DiffOptions{}, false, first, second, 8*n); err != nil {
		t.Fatalf("disjoint leaves within linear lookup quota: %v", err)
	}
	if work := first.work.Load() + second.work.Load(); work > 16*n {
		t.Fatalf("%d disjoint leaves: %d foreign sibling visits, want linear work", n, work)
	}
}

func diffWithWeakInputs(t *testing.T, set *schema.Set) (*Tree, weak.Pointer[Tree], weak.Pointer[Tree]) {
	t.Helper()
	first := parseOnlyXML(t, set, `<top xmlns="urn:review-retention"><v>old</v></top>`)
	second := parseOnlyXML(t, set, `<top xmlns="urn:review-retention"><v>new</v></top>`)
	firstWeak, secondWeak := weak.Make(first), weak.Make(second)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil || diff == nil {
		t.Fatalf("Diff = (%v, %v), want a retained delta", diff, err)
	}
	return diff, firstWeak, secondWeak
}

// TestDiffResultDoesNotRetainInputs keeps the returned delta live while weakly observing both
// same-context inputs. Diff-time sibling/schema indexes must not escape with the result.
func TestDiffResultDoesNotRetainInputs(t *testing.T) {
	set := reviewDiffSet(t, `module review-retention { yang-version 1.1;
  namespace "urn:review-retention"; prefix r;
  container top { leaf v { type string; } }
}`)
	diff, first, second := diffWithWeakInputs(t, set)
	for range 20 {
		if first.Value() == nil && second.Value() == nil {
			break
		}
		runtime.GC()
		runtime.Gosched()
	}
	if first.Value() != nil || second.Value() != nil {
		t.Fatal("returned diff retains one or both input trees")
	}
	runtime.KeepAlive(diff)
}
