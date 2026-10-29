// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/snap"
	"github.com/vibe-ports/yang/internal/xpath"
)

// orderedSchema is conformance/corpus/protocol-v2/schemas/pv2-ordered2.yang's shape.
const orderedSchema = `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  container c { leaf a { type string; } leaf b { type string; } leaf c { type string; }
    leaf d { type string; default "dflt"; } leaf-list ll { type uint8; }
    list l { key k; leaf k { type string; } leaf v { type string; } } } }`

func parseOrdered(t testing.TB, set *schema.Set, xml bool, in string, setup func(*lydCtx)) (*Tree, error) {
	t.Helper()
	fp := parseJSON
	if xml {
		fp = parseXML
	}
	o := parseOpts{ParseOptions: ParseOptions{Unknown: Opaque, ParseOnly: true, Ordered: true}, ordered: true}
	tr, _, err := parseWith(context.Background(), strings.NewReader(in), set, o, fp, setup)
	return tr, err
}

func orderedChildren(t testing.TB, tr *Tree) []string {
	t.Helper()
	for c := range tr.Top() {
		return names(c.Children())
	}
	return nil
}

// TestOrderedParseKeepsInputOrder: Ordered appends every parsed node after the last sibling
// (LYD_INSERT_NODE_LAST, design 15): out of schema order, after an opaque sibling, an XML opaque
// node right after its same-name anchor; the same input parsed without Ordered is placed by
// schema. The storage stays consistent (interleaved where it must be).
func TestOrderedParseKeepsInputOrder(t *testing.T) {
	set := opIndexSet(t, orderedSchema)
	const ns = `xmlns="urn:m"`
	for _, tc := range []struct {
		name      string
		xml       bool
		in        string
		ordered   []string
		unordered []string
		mixed     bool
	}{
		{"schema-order", false, `{"m:c":{"b":"y","a":"x"}}`, []string{"b=y", "a=x"}, []string{"a=x", "b=y"}, false},
		{"opaque-between", false, `{"m:c":{"a":"x","zz":"u","b":"y"}}`, []string{"a=x", "zz", "b=y"}, []string{"a=x", "b=y", "zz"}, true},
		{"runs", false, `{"m:c":{"ll":[3,1],"zz":"u","a":"x","l":[{"k":"2"},{"k":"1"}]}}`,
			[]string{"ll=3", "ll=1", "zz", "a=x", "l[2]", "l[1]"}, []string{"a=x", "ll=1", "ll=3", "l[1]", "l[2]", "zz"}, true},
		{"xml-anchor", true, `<c ` + ns + `><zz>1</zz><a>x</a><zz>2</zz><b>y</b></c>`,
			[]string{"zz", "zz", "a=x", "b=y"}, []string{"a=x", "b=y", "zz", "zz"}, true},
		{"xml-split-run", true, `<c ` + ns + `><ll>1</ll><zz>u</zz><ll>2</ll></c>`,
			[]string{"ll=1", "zz", "ll=2"}, []string{"ll=1", "ll=2", "zz"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := parseOrdered(t, set, tc.xml, tc.in, nil)
			if err != nil {
				t.Fatal(err)
			}
			opidxAssertTreeInvariant(t, tr)
			if got := orderedChildren(t, tr); !slices.Equal(got, tc.ordered) {
				t.Fatalf("ordered: %v, want %v", got, tc.ordered)
			}
			for c := range tr.Top() {
				if c.kids.st.interleaved() != tc.mixed {
					t.Fatalf("interleaved %v, want %v", c.kids.st.interleaved(), tc.mixed)
				}
			}
			o := parseOpts{ParseOptions: ParseOptions{Unknown: Opaque, ParseOnly: true}}
			fp := parseJSON
			if tc.xml {
				fp = parseXML
			}
			plain, _, err := parseWith(context.Background(), strings.NewReader(tc.in), set, o, fp, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := orderedChildren(t, plain); !slices.Equal(got, tc.unordered) {
				t.Fatalf("without Ordered: %v, want %v", got, tc.unordered)
			}
		})
	}
}

// TestOrderedParseFunded sweeps every budget limit and every cancellation point of ordered
// parses that interleave and leave sorted runs out of value order: each attempt either succeeds
// or refuses, and the tree it leaves is consistent.
func TestOrderedParseFunded(t *testing.T) {
	set := opIndexSet(t, orderedSchema)
	for _, tc := range []struct {
		name string
		xml  bool
		in   string
	}{
		{"json", false, `{"m:c":{"ll":[9,3,5],"zz":"u","b":"y","yy":"t","a":"x","l":[{"k":"2","v":"w"},{"k":"1"}],"c":"z"}}`},
		{"xml", true, `<c xmlns="urn:m"><zz>1</zz><ll>4</ll><a>x</a><zz>2</zz><ll>2</ll><yy/><b>y</b></c>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parse := func(ctx context.Context, limit uint64) (*Tree, error) {
				var tr *Tree
				fp := parseJSON
				if tc.xml {
					fp = parseXML
				}
				o := parseOpts{ParseOptions: ParseOptions{Unknown: Opaque, ParseOnly: true, Ordered: true}, ordered: true}
				_, _, err := parseWith(ctx, strings.NewReader(tc.in), set, o, fp, func(lc *lydCtx) {
					lc.tree.work.hashLimit = limit
					tr = lc.tree
				})
				return tr, err
			}
			benign := &cancelAt{Context: context.Background()}
			tr, err := parse(benign, 0)
			if err != nil {
				t.Fatal(err)
			}
			opidxAssertTreeInvariant(t, tr)
			for limit := uint64(1); limit <= tr.work.hashUsed; limit++ {
				tr, err := parse(context.Background(), limit)
				if err != nil && !errors.Is(err, yang.ErrBudget) {
					t.Fatalf("limit %d: %v", limit, err)
				}
				opidxAssertTreeInvariant(t, tr)
			}
			for k := 1; k <= benign.polls; k++ {
				tr, err := parse(&cancelAt{Context: context.Background(), k: k}, 0)
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel at %d: %v", k, err)
				}
				opidxAssertTreeInvariant(t, tr)
			}
		})
	}
}

// TestInterleavedEditBudgetPoints: edits of an ordered tree whose opaque nodes lie between
// schema nodes, refused at every charge boundary, leave the tree and its registrations as they
// were, and succeed as the unbounded edit does. Where they place their nodes is the oracle's
// (protocol-v2/ordered-insert-*, -unlink-between, -merge-into-interleaved, -dup-interleaved).
func TestInterleavedEditBudgetPoints(t *testing.T) {
	set := opIndexSet(t, orderedSchema)
	for _, in := range []string{
		`{"m:c":{"a":"x","zz":"u","c":"z","ll":[3],"yy":"t"}}`,                          // no children table
		`{"m:c":{"ll":[3],"a":"x","zz":"u","c":"z","yy":"t","l":[{"k":"k2"}]}}`,         // with one
		`<c xmlns="urn:m"><ll>3</ll><zz>u</zz><ll>1</ll><ll>2</ll><a>x</a><c>z</c></c>`, // a split run
	} {
		tree := func() (*Tree, *Node) {
			tr, err := parseOrdered(t, set, strings.HasPrefix(in, "<"), in, nil)
			if err != nil {
				t.Fatal(err)
			}
			for c := range tr.Top() {
				return tr, c
			}
			return nil, nil
		}
		// prep runs before the edit: unlink the opaque nodes, or every non-key child, of the
		// interleaved set (it stays interleaved)
		prep := map[string]func(n *Node) bool{
			"opaque-gone": func(n *Node) bool { return n.schema == nil },
			"emptied":     func(n *Node) bool { return !n.isKey() },
		}
		for _, tc := range []struct {
			name string
			edit func(tr *Tree, c *Node) error
		}{
			{"new-path", func(tr *Tree, _ *Node) error { _, err := tr.NewPath("/m:c/b", "y", NewPathOptions{}); return err }},
			{"sorted", func(tr *Tree, c *Node) error { _, err := tr.NewTerm(c, "ll", "5", NewOptions{}); return err }},
			{"merge", func(tr *Tree, _ *Node) error {
				src, err := parseOrdered(t, set, false, `{"m:c":{"b":"y","ll":[1],"l":[{"k":"k1"}]}}`, nil)
				if err != nil {
					return err
				}
				return tr.Merge(src)
			}},
			{"unlink", func(_ *Tree, c *Node) error {
				for n := range c.Children() {
					if n.Name() == "c" {
						return n.Unlink()
					}
				}
				return fmt.Errorf("no c")
			}},
			{"dup", func(tr *Tree, c *Node) error { _, _, err := tr.Dup(c, nil, DupOptions{Recursive: true}); return err }},
			{"opaque-gone", func(tr *Tree, _ *Node) error { _, err := tr.NewPath("/m:c/b", "y", NewPathOptions{}); return err }},
			{"emptied", func(tr *Tree, c *Node) error { _, err := tr.NewTerm(c, "ll", "4", NewOptions{}); return err }},
		} {
			build := func() (*Tree, *Node) {
				tr, c := tree()
				if gone := prep[tc.name]; gone != nil {
					for _, n := range slices.Collect(c.Children()) {
						if gone(n) {
							if err := n.Unlink(); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				return tr, c
			}
			t.Run(tc.name, func(t *testing.T) {
				tr, c := build()
				if err := tc.edit(tr, c); err != nil {
					t.Fatal(err)
				}
				opidxAssertTreeInvariant(t, tr)
				checkEveryHashBudgetPoint(t, func() (*Tree, func() error) {
					tr, c := build()
					return tr, func() error { return tc.edit(tr, c) }
				})
			})
		}
	}
}

// TestSplitRunInsertScaling: sorted insertions into the first piece of a run that an opaque node
// splits work on that piece only (runPiece cuts the scans first), however long the other piece.
func TestSplitRunInsertScaling(t *testing.T) {
	set := opIndexSet(t, orderedSchema)
	measure := func(n int) int64 {
		var b strings.Builder
		b.WriteString(`<c xmlns="urn:m"><ll>1</ll><zz>u</zz>`)
		for i := range n {
			fmt.Fprintf(&b, "<ll>%d</ll>", 100+i%150)
		}
		b.WriteString(`</c>`)
		tr, err := parseOrdered(t, set, true, b.String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var c *Node
		for c = range tr.Top() {
			break
		}
		// the first insertion builds the set's ordered index, once, over all of it
		if _, err := tr.NewTerm(c, "ll", "60", NewOptions{}); err != nil {
			t.Fatal(err)
		}
		tr.work.Store(0)
		tr.work.visits = true
		for v := range 8 {
			if _, err := tr.NewTerm(c, "ll", fmt.Sprint(2+v), NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		tr.work.visits = false
		opidxAssertTreeInvariant(t, tr)
		return tr.work.Load()
	}
	small, large := measure(64), measure(4096)
	if large > 2*small+64 {
		t.Fatalf("work %d for a 4096-member second piece, %d for 64: the scans cross the opaque node", large, small)
	}
}

// TestSplitRunBulkUnlink: a batch removal (TrimXPath's unlinkAll) of a split run's second-piece
// leader sees libyang's prev, the opaque node, as a single unlink does: it leads its own piece
// (oracle: protocol-v2/ordered-delete-split-leader for the single unlink).
func TestSplitRunBulkUnlink(t *testing.T) {
	set := opIndexSet(t, orderedSchema)
	build := func() (*Tree, *Node) {
		tr, err := parseOrdered(t, set, true, `<c xmlns="urn:m"><a>x</a><b>y</b><ll>4</ll><ll>2</ll><zz>u</zz><ll>3</ll><c>z</c></c>`, nil)
		if err != nil {
			t.Fatal(err)
		}
		var c *Node
		for c = range tr.Top() {
			break
		}
		var zero *Node
		for _, v := range []string{"1", "0"} {
			n, err := tr.NewTerm(c, "ll", v, NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			zero = n
		}
		return tr, zero
	}
	single, n := build()
	if err := n.Unlink(); err != nil {
		t.Fatal(err)
	}
	batch, m := build()
	if err := batch.unlinkAll([]*Node{m}); err != nil {
		t.Fatal(err)
	}
	opidxAssertTreeInvariant(t, batch)
	if got, want := typedDump(batch), typedDump(single); !slices.Equal(got, want) {
		t.Fatalf("batch %v, single %v", got, want)
	}
	for _, tr := range []*Tree{single, batch} {
		var c *Node
		for c = range tr.Top() {
			break
		}
		if _, err := tr.NewTerm(c, "ll", "5", NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := typedDump(batch), typedDump(single); !slices.Equal(got, want) {
		t.Fatalf("after an insertion: batch %v, single %v", got, want)
	}
}

// TestValidateOpRestoresDocumentOrder: lyd_validate_op moves the operation's top node into the
// dependency for the validation and back; in an interleaved top level it returns between its
// document neighbours, not by its logical position (originPlace).
func TestValidateOpRestoresDocumentOrder(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	  container c { action act { input { leaf i { type string; } } } } container d { leaf x { type string; } } }`)
	ctx := context.Background()
	tr, err := parseOrdered(t, set, false, `{"m:c":{},"m:zz":"u","m:d":{"x":"y"}}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.top.st.interleaved() {
		t.Fatal("the top level is not interleaved")
	}
	var c *Node
	for c = range tr.Top() {
		break
	}
	act, err := tr.NewInner(c, "act", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := parseOrdered(t, set, false, `{"m:d":{"x":"z"}}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateOp(ctx, set, act, dep, opRPC, Budget{}); err != nil {
		t.Fatal(err)
	}
	opidxAssertTreeInvariant(t, tr)
	if got := names(tr.Top()); !slices.Equal(got, []string{"c", "zz", "d"}) {
		t.Fatalf("top level after ValidateOp: %v", got)
	}
}

// TestSeparatorUnlinkThenMerge: unlinking the opaque node that splits a run joins two pieces
// whose RB trees stay apart; the left one is marked unsorted, so a later Merge inserts its
// instances one by one (oracle, through a trim: protocol-v2/ordered-separator-gone-merge), at
// every charge boundary.
func TestSeparatorUnlinkThenMerge(t *testing.T) {
	set := opIndexSet(t, orderedSchema)
	build := func() (*Tree, func() error) {
		tr, err := parseOrdered(t, set, true, `<c xmlns="urn:m"><ll>3</ll><zz>u</zz><ll>1</ll></c>`, nil)
		if err != nil {
			t.Fatal(err)
		}
		var c *Node
		for c = range tr.Top() {
			break
		}
		if _, err := tr.NewTerm(c, "ll", "4", NewOptions{}); err != nil {
			t.Fatal(err)
		}
		for n := range c.Children() {
			if n.schema == nil {
				if err := n.Unlink(); err != nil {
					t.Fatal(err)
				}
				break
			}
		}
		src, _, err := parseJSONString(set, `{"m:c":{"ll":[5,6]}}`, Reject, false)
		if err != nil {
			t.Fatal(err)
		}
		return tr, func() error { return tr.Merge(src) }
	}
	tr, merge := build()
	if err := merge(); err != nil {
		t.Fatal(err)
	}
	opidxAssertTreeInvariant(t, tr)
	for c := range tr.Top() {
		if got := names(c.Children()); !slices.Equal(got, []string{"ll=3", "ll=4", "ll=5", "ll=6", "ll=1"}) {
			t.Fatalf("%v", got)
		}
	}
	checkEveryHashBudgetPoint(t, build)
}

// TestMergeOpaqueIntoDataNodeRefused: from an opaque first sibling lyd_find_sibling_opaq_next
// compares LYD_NAME, so an opaque source finds the data node of its name; merging one into the
// other is undefined natively, and Merge refuses it before any change (design 15). The tree
// still prints.
func TestMergeOpaqueIntoDataNodeRefused(t *testing.T) {
	set := opIndexSet(t, orderedSchema)
	tr, err := parseOrdered(t, set, false, `{"m:c":{"zz":"u","b":"y"}}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	src, err := parseOrdered(t, set, false, `{"m:c":{"b":false}}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := typedDump(tr)
	err = tr.Merge(src)
	if err == nil || !strings.Contains(err.Error(), `Cannot merge opaque node "b" into data node "b".`) {
		t.Fatalf("Merge = %v", err)
	}
	if !slices.Equal(typedDump(tr), before) {
		t.Fatal("the refused merge changed the tree")
	}
	var b strings.Builder
	if err := tr.PrintJSON(&b, PrintOptions{}); err != nil || !strings.Contains(b.String(), `"b": "y"`) {
		t.Fatalf("print after the refusal: %v %q", err, b.String())
	}
}

// TestOpaqNextSharedSourceRace: lyd_find_sibling_opaq_next over a source that diffMergeDelete
// and findMatch only read (MergeDiff's src, Diff's trees) charges the calling operation's counter;
// concurrent callers over one shared interleaved set leave its tree's counter alone (run under
// -race).
func TestOpaqNextSharedSourceRace(t *testing.T) {
	set := opIndexSet(t, orderedSchema)
	src, err := parseOrdered(t, set, false, `{"m:c":{"zz":"u","a":"x","yy":"t"}}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	var c *Node
	for c = range src.Top() {
		break
	}
	src.work.hashUsed = 0
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			callers := newTree(set) // each operation's own counter
			defer callers.work.beginHashWork()()
			for range 50 {
				n, err := opaqNext(&callers.work, &c.kids, "yy")
				if err != nil || n == nil || n.Name() != "yy" {
					t.Errorf("opaqNext = %v, %v", n, err)
					return
				}
			}
			if callers.work.hashUsed == 0 {
				t.Error("the walk was not charged to its caller")
			}
		})
	}
	wg.Wait()
	if src.work.hashUsed != 0 || src.work.hashErr != nil {
		t.Fatalf("the shared source was charged: used %d, err %v", src.work.hashUsed, src.work.hashErr)
	}
}

// TestRootLookupMixedContexts: the root lookup of an interleaved top level whose roots come from
// two contexts follows the linear lyd_find_sibling_schema across contexts (module and node names)
// and stops at the first opaque root.
func TestRootLookupMixedContexts(t *testing.T) {
	owned := func() *schema.Set { // a context of its own (distinct Module.Owner)
		c, ds, err := yang.NewContext(yang.Options{NoYangLibrary: true}, os.DirFS("../conformance/corpus/protocol-v2/schemas"))
		if err != nil {
			t.Fatal(ds, err)
		}
		if ds, err := c.Load("pv2-ordered3", "", nil); err != nil {
			t.Fatal(ds, err)
		}
		return snap.Set(c.Schema())
	}
	setA, setB := owned(), owned()
	trA, err := parseOrdered(t, setA, false, `{"pv2-ordered3:a":"x","pv2-ordered3:zz":"u"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	trB, err := parseOrdered(t, setB, false, `{"pv2-ordered3:b":"y"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, zz := trA.top.At(0), trA.top.opaqueAt(0)
	b := trB.top.At(0)
	probe := func(name string) xpath.InstanceProbe {
		var sn *schema.Node
		for _, c := range setA.Implemented("pv2-ordered3").Top {
			if c.Name == name {
				sn = c
			}
		}
		p, err := xs{s: sn, set: setA}.PrepareInstance(nil, func(int64) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, tc := range []struct {
		order []*Node
		found bool
	}{
		{[]*Node{a, zz, b}, false}, // past the opaque root: the walk stopped there
		{[]*Node{b, zz, a}, true},  // before it, matched by module and name across contexts
	} {
		var sib siblings
		sib.view(tc.order...)
		sib.addContext(a)
		sib.addContext(b)
		got, handled, err := xroot{sib: &sib, set: setA}.LookupPrepared(probe("b"))
		if err != nil || !handled || (len(got) == 1) != tc.found {
			t.Fatalf("order %v: %v %v %v", names(slices.Values(tc.order)), got, handled, err)
		}
	}
}
