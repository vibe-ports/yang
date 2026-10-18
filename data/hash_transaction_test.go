// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
)

// lyd_change_meta edits a metadata instance in place: a budget failure after a
// replaced leaf and its replaced annotation restores both.
func TestApplyDiffBudgetRestoresMetadataValue(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
	 import ietf-yang-metadata { prefix md; }
	 md:annotation ann { type string; }
	 container c { leaf a { type string; } leaf b { type string; } } }`)
	parse := func(in string) *Tree {
		tr, diags, err := parseJSONString(set, in, Reject, true)
		if err != nil {
			t.Fatal(err, diags)
		}
		return tr
	}
	first := parse(`{"m:c":{"a":"x","@a":{"m:ann":"one"},"b":"y"}}`)
	diff, err := Diff(first, parse(`{"m:c":{"a":"z","@a":{"m:ann":"two"},"b":"y"}}`), DiffOptions{Meta: true})
	if err != nil || diff == nil {
		t.Fatal(diff, err)
	}
	before := snapshotHashBudgetTree(first)
	a := first.top.first().kids.list[0]
	called := false
	err = first.ApplyDiff(diff, ApplyDiffOptions{Callback: func(_, n *Node) error {
		if n == a {
			called = true
			if n.Value() != "z" || n.meta[0].value.Canonical() != "two" {
				t.Errorf("callback saw %q @%q, want the applied change", n.Value(), n.meta[0].value.Canonical())
			}
			return yang.ErrBudget
		}
		return nil
	}})
	if !called || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("called=%t err=%v; want ErrBudget from the leaf's callback", called, err)
	}
	if got := a.meta[0].value.Canonical(); a.Value() != "x" || got != "one" {
		t.Fatalf("rolled back to %q @%q, want \"x\" @\"one\"", a.Value(), got)
	}
	if after := snapshotHashBudgetTree(first); !reflect.DeepEqual(after, before) {
		t.Fatalf("budget failure changed the tree\nbefore=%v\nafter=%v", before.index, after.index)
	}
}

// A list instance moved into another tree keeps its leafref links to the tree it left. Rolling
// back an ApplyDiff that deleted it restores the counterpart record outside the tree as well:
// both ends of the link agree again.
func TestApplyDiffBudgetRestoresCrossTreeLinks(t *testing.T) {
	set := corpusSet(t, []string{"../conformance/corpus/protocol-v2/schemas"}, []string{"pv2-links"})
	set.LeafrefLinking = true
	parse := func(in string) *Tree {
		tr, diags, err := parseWith(context.Background(), strings.NewReader(in), set,
			parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
		if err != nil {
			t.Fatal(err, diags)
		}
		return tr
	}
	src := parse(`{"pv2-links:c":{"l":[{"k":"a"}],"r":"a"}}`)
	if _, err := src.LinkLeafrefs(context.Background()); err != nil {
		t.Fatal(err)
	}
	dst := parse(`{"pv2-links:c":{"t":["y"]}}`)
	list, r := src.top.first().kids.list[0], src.top.first().kids.list[1]
	k := list.kids.list[0]
	if err := dst.Insert(dst.top.first(), list); err != nil {
		t.Fatal(err)
	}
	if lrefs, _ := k.LeafrefLinks(); len(lrefs) != 1 || lrefs[0] != r || r.treeOf() != src || k.treeOf() != dst {
		t.Fatalf("no cross-tree link to roll back: %v", lrefs)
	}
	diff, err := Diff(dst, parse(`{"pv2-links:c":{"t":["y","z"]}}`), DiffOptions{})
	if err != nil || diff == nil {
		t.Fatal(diff, err)
	}
	beforeDst, beforeSrc := snapshotHashBudgetTree(dst), snapshotHashBudgetTree(src)
	err = dst.ApplyDiff(diff, ApplyDiffOptions{Callback: func(_, n *Node) error {
		if n.Name() == "t" && n.Value() == "z" { // after the delete of l[k='a'] unlinked k
			if _, targets := r.LeafrefLinks(); len(targets) != 0 {
				t.Error("the delete did not unlink the counterpart")
			}
			return yang.ErrBudget
		}
		return nil
	}})
	if !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("err=%v; want ErrBudget", err)
	}
	lrefs, _ := k.LeafrefLinks()
	_, targets := r.LeafrefLinks()
	if len(lrefs) != 1 || lrefs[0] != r || len(targets) != 1 || targets[0] != k {
		t.Fatalf("asymmetric link after the rollback: k.leafrefs=%v r.targets=%v", lrefs, targets)
	}
	if after := snapshotHashBudgetTree(dst); !reflect.DeepEqual(after, beforeDst) {
		t.Fatalf("budget failure changed the destination\nbefore=%v\nafter=%v", beforeDst.index, after.index)
	}
	if after := snapshotHashBudgetTree(src); !reflect.DeepEqual(after, beforeSrc) {
		t.Fatalf("budget failure changed the other tree\nbefore=%v\nafter=%v", beforeSrc.index, after.index)
	}
}

const dupJournalSchema = `module m { yang-version 1.1; namespace urn:m; prefix m;
 container big { list e { key k; leaf k { type string; } leaf-list v { type string; } } }
 container np { container inner { leaf x { type string; } leaf y { type string; default "d"; } } }
 container p { presence "p"; container q { leaf z { type string; } } } }`

// dupJournalTree has an unrelated subtree of n list instances (each with children) beside the
// implicit default NP containers np/inner.
func dupJournalTree(t *testing.T, parse func(string) *Tree, n int) *Tree {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"m:big":{"e":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"k":"%d","v":["a","b","c"]}`, i)
	}
	b.WriteString(`]}}`)
	return parse(b.String())
}

// The local journal of a duplication covers what it changes: the default flags of the NP
// containers above the copy, the destination level, and top-level copies of the parents. Every
// charge boundary leaves the tree as it was, beside an unrelated indexed subtree it does not copy.
func TestDupLocalJournalChargePoints(t *testing.T) {
	set := opIndexSet(t, dupJournalSchema)
	parse := func(in string) *Tree {
		tr, diags, err := parseWith(context.Background(), strings.NewReader(in), set,
			parseOpts{ParseOptions: ParseOptions{Unknown: Reject, Validate: ValidateOptions{Present: true}}}, parseJSON, nil)
		if err != nil {
			t.Fatal(err, diags)
		}
		return tr
	}
	for _, tc := range []struct {
		name string
		edit func(dst, src *Tree) error
		ok   func(t *testing.T, dst *Tree)
	}{
		{"np-default-ancestors", func(dst, src *Tree) error {
			inner, err := dst.Find("/m:np/inner")
			if err != nil || inner == nil || inner.flags&FlagDefault == 0 {
				t.Fatalf("no default NP container: %v", err)
			}
			x, _ := src.Find("/m:np/inner/x")
			_, _, err = dst.Dup(x, inner, DupOptions{})
			return err
		}, func(t *testing.T, dst *Tree) {
			np, _ := dst.Find("/m:np")
			if np.flags&FlagDefault != 0 || np.kids.list[0].flags&FlagDefault != 0 {
				t.Fatal("the copy left its NP-container ancestors default")
			}
		}},
		{"with-parents", func(dst, src *Tree) error {
			z, _ := src.Find("/m:p/q/z")
			_, _, err := dst.Dup(z, nil, DupOptions{WithParents: true})
			return err
		}, func(t *testing.T, dst *Tree) {
			if z, err := dst.Find("/m:p/q/z"); err != nil || z == nil {
				t.Fatalf("no copy with parents: %v", err)
			}
		}},
		{"siblings-into-list", func(dst, src *Tree) error {
			e, _ := dst.Find("/m:big/e[k='3']")
			v, _ := src.Find("/m:big/e[k='0']/v[.='a']")
			_, _, err := dst.DupSiblings(v, e, DupOptions{})
			return err
		}, func(t *testing.T, dst *Tree) {
			e, _ := dst.Find("/m:big/e[k='3']")
			if got := len(e.kids.list); got != 8 { // the key, three values and the four copies
				t.Fatalf("%d children after the copy, want 8", got)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkEveryHashBudgetPointPair(t, func() ([]*Tree, func() error) {
				dst := dupJournalTree(t, parse, 8)
				src := parse(`{"m:np":{"inner":{"x":"v"}},"m:p":{"q":{"z":"w"}},"m:big":{"e":[{"k":"0","v":["a","b","c","d"]}]}}`)
				return []*Tree{dst, src}, func() error { return tc.edit(dst, src) }
			}, func(t *testing.T, trees []*Tree) {
				t.Helper()
				tc.ok(t, trees[0])
				checkFindAgainstLinear(t, trees[0])
			})
		})
	}
}

// A duplication journals (and charges) only the destination level and the path above it:
// neither its hash-work charge nor its allocations grow with an unrelated subtree, so repeated
// small copies into a growing tree stay linear.
func TestDupJournalIgnoresUnrelatedSubtree(t *testing.T) {
	set := opIndexSet(t, dupJournalSchema)
	parse := func(in string) *Tree {
		tr, diags, err := parseWith(context.Background(), strings.NewReader(in), set,
			parseOpts{ParseOptions: ParseOptions{Unknown: Reject, Validate: ValidateOptions{Present: true}}}, parseJSON, nil)
		if err != nil {
			t.Fatal(err, diags)
		}
		return tr
	}
	src := parse(`{"m:np":{"inner":{"x":"v"}}}`)
	x, _ := src.Find("/m:np/inner/x")
	const copies = 16
	measure := func(n int) (uint64, uint64) {
		dst := dupJournalTree(t, parse, n)
		inner, _ := dst.Find("/m:np/inner")
		var charged, bytes uint64
		for i := range copies {
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			// a fresh parent each time keeps the destination level the same size
			_, _, err := dst.Dup(x, inner, DupOptions{})
			runtime.ReadMemStats(&after)
			if err != nil {
				t.Fatal(err)
			}
			charged += dst.work.hashUsed
			bytes += after.TotalAlloc - before.TotalAlloc
			if i < copies-1 {
				if err := inner.kids.list[slices.IndexFunc(inner.kids.list, func(c *Node) bool { return c.Name() == "x" })].Remove(); err != nil {
					t.Fatal(err)
				}
			}
		}
		return charged, bytes
	}
	sizes := scaleSizes([]int{256, 2048, 8192}, 2)
	c0, b0 := measure(sizes[0])
	for _, n := range sizes[1:] {
		c, b := measure(n)
		if c != c0 || b > 2*b0 {
			t.Fatalf("%d copies beside %d unrelated instances: charged %d, allocated %d; beside %d: charged %d, allocated %d",
				copies, n, c, b, sizes[0], c0, b0)
		}
	}
}

const journalScaleSchema = `module m { yang-version 1.1; namespace urn:m; prefix m;
 container big { list e { key k; leaf k { type string; } leaf v { type string; }
  action act { input { leaf i { type string; } leaf id { type string; default "d"; } }
   output { leaf o { type string; } } } } }
 container small { leaf a { type string; } leaf d { type string; default "x"; } } }`

const journalScaleSchema2 = `module m2 { yang-version 1.1; namespace urn:m2; prefix m2;
 container s2 { leaf a { type string; } leaf d { type string; default "x"; } } }`

// journalScaleTree is the data of n instances of /m:big/e beside small, unvalidated.
func journalScaleTree(t *testing.T, parse func(string) *Tree, n int, rest string) *Tree {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"m:big":{"e":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"k":"%d","v":"v"}`, i)
	}
	b.WriteString(`]}` + rest + `}`)
	return parse(b.String())
}

// Every journaled entry point copies (and charges) only what it changes: neither the charged
// hash work nor the allocations of a small change grow with a large unrelated destination
// subtree. libyang touches only the matched nodes and keeps no snapshot.
func TestJournalIgnoresUnrelatedSubtree(t *testing.T) {
	dir := t.TempDir()
	for name, src := range map[string]string{"m.yang": journalScaleSchema, "m2.yang": journalScaleSchema2} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	set := opsSet(t, opsFixture{dir: dir, searchdirs: []string{"."}, modules: []struct {
		name, revision string
		features       []string
	}{{name: "m"}, {name: "m2"}}})
	parse := func(in string) *Tree {
		tr, diags, err := parseWith(context.Background(), strings.NewReader(in), set,
			parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
		if err != nil {
			t.Fatal(err, diags)
		}
		return tr
	}
	parseOpTree := func(typ opType, in string, parent *Node) (*Tree, *Node) {
		tr, op, diags, err := parseOp(context.Background(), strings.NewReader(in), set, FormatJSON, typ, parent, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		return tr, op
	}
	// the request /m:big/e[k='-1']/act among n other instances of e
	request := func(n int) (*Tree, *Node) {
		tr, op := parseOpTree(opRPC, `{"m:big":{"e":[{"k":"-1","act":{"i":"x"}}]}}`, nil)
		if err := tr.Merge(journalScaleTree(t, parse, n, "")); err != nil {
			t.Fatal(err)
		}
		return tr, op
	}
	for _, tc := range []struct {
		name string
		// build returns the destination tree and the small operation on it
		build func(n int) (*Tree, func() error)
		// scan is the part of the operation that walks the whole destination by design: its
		// allocations are discounted (ApplyDiff refuses a split operation-child run anywhere
		// in t before changing it, D-0116)
		scan func(*Tree) error
	}{
		{"ApplyDiff", func(n int) (*Tree, func() error) {
			dst := journalScaleTree(t, parse, n, `,"m:small":{"a":"1"}`)
			diff, err := Diff(parse(`{"m:small":{"a":"1"}}`), parse(`{"m:small":{"a":"2"}}`), DiffOptions{})
			if err != nil || diff == nil {
				t.Fatal(diff, err)
			}
			return dst, func() error { return dst.ApplyDiff(diff, ApplyDiffOptions{}) }
		}, func(dst *Tree) error { return dst.ApplyDiff(nil, ApplyDiffOptions{}) }},
		{"Merge", func(n int) (*Tree, func() error) {
			dst := journalScaleTree(t, parse, n, `,"m:small":{"a":"1"}`)
			src := parse(`{"m:small":{"a":"2"}}`)
			return dst, func() error { return dst.Merge(src) }
		}, nil},
		{"MergeDiff", func(n int) (*Tree, func() error) {
			dst, err := Diff(newTree(set), journalScaleTree(t, parse, n, `,"m:small":{"a":"1"}`), DiffOptions{})
			if err != nil {
				t.Fatal(err)
			}
			src, err := Diff(parse(`{"m:small":{"a":"1"}}`), parse(`{"m:small":{"a":"2"}}`), DiffOptions{})
			if err != nil || src == nil {
				t.Fatal(src, err)
			}
			return dst, func() error { return dst.MergeDiff(src, MergeDiffOptions{}) }
		}, nil},
		{"ParseOp-reply-into-request", func(n int) (*Tree, func() error) {
			tr, op := request(n)
			return tr, func() error {
				_, _, diags, err := parseOp(context.Background(), strings.NewReader(`{"m:o":"y"}`), set, FormatJSON, opReply, op, Reject)
				if err != nil {
					t.Log(diags)
				}
				return err
			}
		}, nil},
		{"ValidateOp", func(n int) (*Tree, func() error) {
			tr, _ := parseOpTree(opRPC, `{"m:big":{"e":[{"k":"0","act":{"i":"x"}}]}}`, nil)
			dep := journalScaleTree(t, parse, n, "")
			return tr, func() error {
				diags, err := tr.ValidateOp(context.Background(), OpRPC, ValidateOpOptions{Operational: dep})
				if err != nil {
					t.Log(diags)
				}
				return err
			}
		}, nil},
		{"ValidateModule", func(n int) (*Tree, func() error) {
			dst := journalScaleTree(t, parse, n, `,"m2:s2":{"a":"1"}`)
			return dst, func() error {
				diags, err := dst.ValidateModule(context.Background(), "m2", ValidateOptions{})
				if err != nil {
					t.Log(diags)
				}
				return err
			}
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allocated := func(op func() error) uint64 {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				err := op()
				runtime.ReadMemStats(&after)
				if err != nil {
					t.Fatal(err)
				}
				return after.TotalAlloc - before.TotalAlloc
			}
			measure := func(n int) (uint64, uint64) {
				dst, op := tc.build(n)
				var scanned uint64
				if tc.scan != nil {
					scanned = allocated(func() error { return tc.scan(dst) })
				}
				bytes := allocated(op)
				return dst.work.hashUsed, bytes - min(bytes, scanned)
			}
			sizes := scaleSizes([]int{2048, 16384}, 1)
			if len(sizes) == 1 {
				sizes = []int{256, 2048} // the race detector: smaller trees, same comparison
			}
			c0, b0 := measure(sizes[0])
			if c0 == 0 {
				t.Fatal("nothing charged: the journal and the change are hash work")
			}
			c, b := measure(sizes[1])
			if c != c0 || b > 2*b0 {
				t.Fatalf("beside %d unrelated instances: charged %d, allocated %d; beside %d: charged %d, allocated %d",
					sizes[1], c, b, sizes[0], c0, b0)
			}
		})
	}
}

// A hash collision chain is as long as its duplicates: the values of a state leaf-list may all
// be equal. Journaling the sibling set walks the chain without recursion, so even a small stack
// copes with it.
func TestJournalLongCollisionChain(t *testing.T) {
	if testing.Short() {
		t.Skip("200k-node tree")
	}
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
	 container c { config false; leaf-list s { type string; } } }`)
	n := 200000
	if raceEnabled {
		n = 50000
	}
	var b strings.Builder
	b.WriteString(`{"m:c":{"s":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"x"`)
	}
	b.WriteString(`]}}`)
	parse := func(in string) *Tree {
		tr, diags, err := parseWith(context.Background(), strings.NewReader(in), set,
			parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
		if err != nil {
			t.Fatal(err, diags)
		}
		return tr
	}
	dst, src := parse(b.String()), parse(`{"m:c":{"s":["x"]}}`)
	c, x := dst.top.first(), src.top.first().kids.list[0]
	if len(c.kids.ht) != 1 {
		t.Fatalf("%d hash chains, want the one of all the duplicates", len(c.kids.ht))
	}
	// the old recursion needed a frame per chain record: far more than 1 MiB here
	defer debug.SetMaxStack(debug.SetMaxStack(1 << 20))
	if _, _, err := dst.Dup(x, c, DupOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := len(c.kids.list); got != n+1 {
		t.Fatalf("%d values after the copy, want %d", got, n+1)
	}
}

// ValidateOp without a dependency rebinds the operation's children to a temporary tree before
// its implicit defaults are linked there, so their sibling set is journaled after the rebind. A
// budget failure binds them back to the operation's tree: later edits are journaled again and
// their own budget failure restores the list and the node parents together.
func TestValidateOpBudgetRestoresReboundWork(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
	 rpc r { input { leaf a { type string; default "x"; } } } }`)
	parse := func(in string) *Tree {
		tr, _, diags, err := parseOp(context.Background(), strings.NewReader(in), set, FormatJSON, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		return tr
	}
	validate := func(tr *Tree) error {
		_, err := tr.ValidateOp(context.Background(), OpRPC, ValidateOpOptions{})
		return err
	}
	full := parse(`{"m:r":{}}`)
	var points []uint64
	full.work.hashTrace = &points
	if err := validate(full); err != nil {
		t.Fatal("unbounded validation:", err)
	}
	failed := 0
	for _, at := range slices.Compact(slices.Sorted(slices.Values(points))) {
		if at == 0 {
			continue
		}
		tr := parse(`{"m:r":{}}`)
		before := snapshotHashBudgetTree(tr)
		tr.work.hashLimit = at - 1
		if err := validate(tr); !errors.Is(err, yang.ErrBudget) {
			continue
		}
		failed++
		r := tr.top.first()
		if r.kids.work != &tr.work {
			t.Fatalf("limit %d: rolled-back operation's children bound to another tree's counter", at-1)
		}
		if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(after, before) {
			t.Fatalf("limit %d: budget failure changed the tree\nbefore=%v\nafter=%v", at-1, before.dump, after.dump)
		}
		tr.work.hashLimit = 0
		diff, err := Diff(tr, parse(`{"m:r":{"a":"y"}}`), DiffOptions{})
		if err != nil || diff == nil {
			t.Fatal(diff, err)
		}
		err = tr.ApplyDiff(diff, ApplyDiffOptions{Callback: func(_, n *Node) error {
			if n.Name() == "a" {
				return yang.ErrBudget
			}
			return nil
		}})
		if !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("limit %d: ApplyDiff err=%v; want ErrBudget", at-1, err)
		}
		if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(after, before) {
			t.Fatalf("limit %d: ApplyDiff budget failure changed the tree\nbefore=%v\nafter=%v", at-1, before.dump, after.dump)
		}
		if len(r.kids.list) != 0 {
			t.Fatalf("limit %d: %d children left after the rollback", at-1, len(r.kids.list))
		}
	}
	if failed == 0 {
		t.Fatal("no charge point failed the validation")
	}
}
