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

// ordered2Set loads pv2-ordered2, then the other modules, into a context of its own (an owned
// snapshot).
func ordered2Set(t testing.TB, more ...string) *schema.Set {
	t.Helper()
	c, ds, err := yang.NewContext(yang.Options{NoYangLibrary: true}, os.DirFS("../conformance/corpus/protocol-v2/schemas"))
	if err != nil {
		t.Fatal(ds, err)
	}
	for _, m := range append([]string{"pv2-ordered2"}, more...) {
		if ds, err := c.Load(m, "", nil); err != nil {
			t.Fatal(ds, err)
		}
	}
	return snap.Set(c.Schema())
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

// TestInterleavedLookupForeignInstance: a keyed probe of one context against an interleaved set
// whose schema nodes all come from another (a homogeneous foreign set): the no-table
// lyd_find_sibling_first compares equivalent schemas across contexts, so the probe is
// translated into the siblings' context before it matches.
func TestInterleavedLookupForeignInstance(t *testing.T) {
	setA, setB := ordered2Set(t), ordered2Set(t)
	trB, err := parseOrdered(t, setB, false, `{"pv2-ordered2:c":{"l":[{"k":"w"}],"zz":"u","l":[{"k":"x"}]}}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	var c *Node
	for c = range trB.Top() {
		break
	}
	if !c.kids.st.interleaved() || c.kids.ht != nil {
		t.Fatal("want an interleaved set without a children table")
	}
	var l *schema.Node
	for _, n := range setA.Implemented("pv2-ordered2").Top[0].Children {
		if n.Name == "l" {
			l = n
		}
	}
	for _, key := range []string{"w", "x"} {
		p, err := xs{s: l, set: setA}.PrepareInstance([]string{key}, func(int64) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		got, handled, err := interleavedLookup(&c.kids, setB, p.(*xprobe))
		if err != nil || !handled || len(got) != 1 || got[0].(xn).n.Path() != "/pv2-ordered2:c/l[k='"+key+"']" {
			t.Fatalf("key %s: %v %v %v", key, got, handled, err)
		}
	}
}

// TestForestIntoSplitRunRecord: a sorted forest moved into the second piece of a split run
// (lyd_move_nodes_by_schema, rbPublish) replaces that piece's first-instance record: the replay
// reads lyd_insert_hash_add's node->prev in document order, which is the opaque separator, not the
// first piece's last instance (visibleNeighbours, design 15).
func TestForestIntoSplitRunRecord(t *testing.T) {
	set := opIndexSet(t, orderedSchema)
	tr, err := parseOrdered(t, set, true, `<c xmlns="urn:m"><a>x</a><b>y</b><ll>3</ll><ll>4</ll><ll>6</ll><zz>u</zz><ll>2</ll><ll>5</ll></c>`, nil)
	if err != nil {
		t.Fatal(err)
	}
	var c *Node
	for c = range tr.Top() {
		break
	}
	if _, err := tr.NewTerm(c, "ll", "1", NewOptions{}); err != nil { // the first piece's record moves to 1
		t.Fatal(err)
	}
	srcTree, err := parseOrdered(t, set, false, `{"m:c":{"ll":[0,7]}}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	var src *Node
	for src = range srcTree.Top() {
		break
	}
	var first *Node
	for first = range src.Children() {
		break
	}
	forest, _, err := srcTree.DupSiblings(first, nil, DupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for n := range forest.tree.Top() { // the copies' tree also holds an empty c: the forest is ll 0, 7
		if n.Name() == "c" {
			if err := n.Unlink(); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	ll := c.kids.At(2).schema
	if err := tr.Insert(c, forest); err != nil {
		t.Fatal(err)
	}
	opidxAssertTreeInvariant(t, tr)
	for _, r := range c.kids.records(ll) {
		if p, _ := c.kids.docNeighbours(r); p != nil && p.schema == ll {
			t.Fatalf("record %s does not start a run: %v", r.Value(), names(c.Children()))
		}
	}
	if _, err := tr.NewTerm(c, "ll", "9", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	opidxAssertTreeInvariant(t, tr)
}

// TestForeignPositionPastOpaque: a positional lookup in a top level that holds a root of
// another context walks LYD_LIST_FOR_INST's next pointers from the first instance, which the
// opaque root ends: the second state leaf-list instance, past it, is not found (instanceRun,
// design 15; oracle: protocol-v2/ordered-foreign-position-past-opaque).
func TestForeignPositionPastOpaque(t *testing.T) {
	owned := func() *schema.Set {
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
	const ns = `xmlns="urn:vibe-ports:yang:conformance:pv2-ordered3"`
	tr, err := parseOrdered(t, setA, true, `<a `+ns+`>x</a><st `+ns+`>1</st><zz `+ns+`>u</zz><st `+ns+`>2</st>`, nil)
	if err != nil {
		t.Fatal(err)
	}
	trB, err := parseOrdered(t, setB, false, `{"pv2-ordered3:b":"y"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	var b *Node
	for b = range trB.Top() {
		break
	}
	if err := tr.Insert(nil, b); err != nil {
		t.Fatal(err)
	}
	if n, _ := tr.Find("/pv2-ordered3:st[2]"); n != nil {
		t.Fatalf("st[2] found past the opaque root: %v (%v)", n.Value(), names(tr.Top()))
	}
	// the walk behind positional predicates and lys_getnext_data, through the mixed-context branch
	st := tr.top.At(2).schema
	if !tr.top.foreignFor(st) {
		t.Fatal("the top level does not take the mixed-context branch")
	}
	var got []string
	for n := range tr.instances(&tr.top, st) {
		got = append(got, n.Value())
	}
	if !slices.Equal(got, []string{"1"}) {
		t.Fatalf("LYD_LIST_FOR_INST from the first instance = %v, want [1] (%v)", got, names(tr.Top()))
	}
}

// TestAloneLeaderUnlink: a single unlink reads node->prev and node->next in document order,
// whatever their kind, as LYD_NODE_IS_ALONE does: ll 1 sits between two opaque nodes, so its
// RB leader is alone although a later piece of ll holds the last sibling. Single and batch
// unlink agree, and so does reinserting the node (oracle: protocol-v2/ordered-unlink-alone-leader).
func TestAloneLeaderUnlink(t *testing.T) {
	set := ordered2Set(t) // an owned snapshot: the detached node can be inserted again
	build := func() (*Tree, *Node, *Node) {
		tr, err := parseOrdered(t, set, false, `{"pv2-ordered2:c":{"zz":"u","ll":[1],"yy":"v","ll":[9,10,11]}}`, nil)
		if err != nil {
			t.Fatal(err)
		}
		var c *Node
		for c = range tr.Top() {
			break
		}
		two, err := tr.NewTerm(c, "ll", "2", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := two.Unlink(); err != nil {
			t.Fatal(err)
		}
		for n := range c.Children() {
			if n.Name() == "ll" {
				return tr, c, n
			}
		}
		t.Fatal("no ll")
		return nil, nil, nil
	}
	single, sc, n := build()
	if err := n.Unlink(); err != nil {
		t.Fatal(err)
	}
	batch, bc, m := build()
	if err := batch.unlinkAll([]*Node{m}); err != nil {
		t.Fatal(err)
	}
	opidxAssertTreeInvariant(t, single)
	opidxAssertTreeInvariant(t, batch)
	if got, want := typedDump(batch), typedDump(single); !slices.Equal(got, want) {
		t.Fatalf("batch %v, single %v", got, want)
	}
	if n.detachedRB == nil || (m.detachedRB == nil) != (n.detachedRB == nil) {
		t.Fatalf("the alone leader's RB metadata: single %v, batch %v", n.detachedRB != nil, m.detachedRB != nil)
	}
	for _, x := range []struct {
		tr   *Tree
		c, n *Node
	}{{single, sc, n}, {batch, bc, m}} {
		if err := x.tr.Insert(x.c, x.n); err != nil {
			t.Fatal(err)
		}
		if _, err := x.tr.NewTerm(x.c, "ll", "0", NewOptions{}); err != nil {
			t.Fatal(err)
		}
		opidxAssertTreeInvariant(t, x.tr)
		if got := names(x.c.Children()); !slices.Equal(got, []string{"zz", "yy", "ll=0", "ll=1", "ll=9", "ll=10", "ll=11"}) {
			t.Fatalf("after reinsertion: %v", got)
		}
	}
}

// TestForeignFirstSchemaHashed: a merge whose matched parent is of another context links foreign
// children into a child level (lyd_merge_sibling_r inserts its copies without
// lyd_insert_check_schema). When
// the physical first sibling is the tree's own opaque node but the first schema node is
// foreign, lyd_insert_get_next_anchor reads LYD_CTX(first_sibling), the opaque node's: a local
// insertion takes the children table path and goes before the next schema sibling's instance.
func TestForeignFirstSchemaHashed(t *testing.T) {
	setA, setB := ordered2Set(t, "pv2-ordered3"), ordered2Set(t)
	tr, err := parseOrdered(t, setA, false, `{"pv2-ordered2:c":{"zz":"u","d":"q","ll":[1,2]}}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	var c *Node
	for c = range tr.Top() {
		break
	}
	for _, in := range []string{`{"pv2-ordered2:c":{"a":"x"}}`, `{"pv2-ordered2:c":{"b":"y"}}`} {
		// a source whose first root is of the tree's context, then a foreign c (as
		// protocol-v2/merge-foreign-child builds one)
		src, err := parseOrdered(t, setA, false, `{"pv2-ordered3:a":"x"}`, nil)
		if err != nil {
			t.Fatal(err)
		}
		fb, _, err := parseJSONString(setB, in, Reject, false)
		if err != nil {
			t.Fatal(err)
		}
		f := fb.top.first()
		if err := unlink(f); err != nil {
			t.Fatal(err)
		}
		if err := src.link(nil, &src.top, f, src.top.SchemaLen()); err != nil {
			t.Fatal(err)
		}
		if err := tr.Merge(src); err != nil {
			t.Fatal(err)
		}
	}
	// the first merge's linear walk stops at the opaque first sibling; the second one's first
	// sibling is foreign, as b is: by the table, before d
	if got := names(c.Children()); !slices.Equal(got, []string{"a=x", "zz", "b=y", "d=q", "ll=1", "ll=2"}) {
		t.Fatalf("after the merges: %v", got)
	}
	var a *Node
	for a = range c.Children() {
		break
	}
	if err := a.Unlink(); err != nil {
		t.Fatal(err)
	}
	if c.kids.ht == nil || !c.kids.st.interleaved() || ctxOf(c.kids.At(0)) != setB {
		t.Fatal("want an interleaved child level with a table and a foreign first schema node")
	}
	if _, err := tr.NewTerm(c, "c", "z", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	opidxAssertTreeInvariant(t, tr)
	if got := names(c.Children()); !slices.Equal(got, []string{"zz", "b=y", "c=z", "d=q", "ll=1", "ll=2"}) {
		t.Fatalf("after the insertion: %v", got)
	}
}

// forwardMetaInput is an interleaved level with n metadata containers ahead of their opaque
// targets (the first n) and, after the data, m more opaque siblings that no container names.
func forwardMetaInput(n, m int, before bool) string {
	var b strings.Builder
	b.WriteString(`{"m:c":{"zz":"u","a":"x"`)
	meta := func() {
		for i := range n {
			fmt.Fprintf(&b, `,"@o%d":{"m:q":"1"}`, i)
		}
	}
	if before {
		meta()
	}
	for i := range n + m {
		fmt.Fprintf(&b, `,"o%d":"v"`, i)
	}
	if !before {
		meta()
	}
	b.WriteString(`}}`)
	return b.String()
}

// TestForwardMetadataScaling: lydjson_metadata_finish's document-order matching (design 15)
// indexes the siblings once, so the work per forward metadata container stays flat as their
// number grows; the walk is charged to the parse's enforced budget.
func TestForwardMetadataScaling(t *testing.T) {
	set := opIndexSet(t, orderedSchema)
	work := func(n int) float64 {
		var tr *Tree
		if _, err := parseOrdered(t, set, false, forwardMetaInput(n, 0, true), func(lc *lydCtx) { tr = lc.tree }); err != nil {
			t.Fatal(err)
		}
		var c *Node
		for c = range tr.Top() {
			break
		}
		for o := range c.Children() {
			if o.schema == nil && o.Name() != "zz" && len(o.Attrs()) != 1 {
				t.Fatalf("%s: attributes %v", o.Name(), o.Attrs())
			}
		}
		return float64(tr.work.Load()+int64(tr.work.hashUsed)) / float64(n) //nolint:gosec // small counts
	}
	small, large := work(256), work(2048)
	if large > 2*small {
		t.Fatalf("work per container: %.1f at 256, %.1f at 2048", small, large)
	}
	// refused when the enforced budget is short, with every limit
	_, err := parseOrdered(t, set, false, forwardMetaInput(64, 0, true), func(lc *lydCtx) { lc.tree.work.hashLimit = 1 })
	if !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("budget: %v", err)
	}
}

// TestForwardMetadataCancel: the document-order index polls the parse's context, and a parse
// cancelled at its checks, the index's included, fails and leaves a consistent tree.
func TestForwardMetadataCancel(t *testing.T) {
	set := opIndexSet(t, orderedSchema)
	polls := func(before bool, m int) (total int) {
		ctx := &cancelAtContext{Context: context.Background()}
		o := parseOpts{ParseOptions: ParseOptions{Unknown: Opaque, ParseOnly: true, Ordered: true}, ordered: true}
		if _, _, err := parseWith(ctx, strings.NewReader(forwardMetaInput(4, m, before)), set, o, parseJSON, nil); err != nil {
			t.Fatal(err)
		}
		return ctx.calls
	}
	// 3000 more siblings: the index polls about every 1k of them on top of the parse's own checks
	if forward, after := polls(true, 3000)-polls(true, 0), polls(false, 3000)-polls(false, 0); forward < after+2 {
		t.Fatalf("the metadata index does not poll: %d more checks, %d without forward metadata", forward, after)
	}
	// cancelled at a sample of the checks, the index's last ones (at the end of the parse) included
	total := polls(true, 3000)
	for _, at := range []int{1, total / 4, total / 2, total - 3, total - 2, total - 1, total} {
		ctx := &cancelAtContext{Context: context.Background(), at: at}
		var tr *Tree
		o := parseOpts{ParseOptions: ParseOptions{Unknown: Opaque, ParseOnly: true, Ordered: true}, ordered: true}
		_, _, err := parseWith(ctx, strings.NewReader(forwardMetaInput(4, 3000, true)), set, o, parseJSON, func(lc *lydCtx) { tr = lc.tree })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel at %d of %d: %v", at, total, err)
		}
		opidxAssertTreeInvariant(t, tr)
	}
}

// TestSchemaSplitMergeUnbatched: Merge into a list run that another schema node splits looks each
// new instance's leader up again (lyd_insert_node): k0 takes over the first piece's record, so
// k5 joins the piece after b. The copies are not batched into the first piece's RB tree
// (oracle, with the source in input order: protocol-v2/ordered-schema-split-merge-into).
func TestSchemaSplitMergeUnbatched(t *testing.T) {
	set := ordered2Set(t)
	tr, err := parseOrdered(t, set, false, `{"pv2-ordered2:c":{"a":"x","l":[{"k":"k2"},{"k":"k4"}],"b":"y","l":[{"k":"k6"}],"c":"z"}}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{`{"pv2-ordered2:c":{"l":[{"k":"k3"}]}}`, `{"pv2-ordered2:c":{"l":[{"k":"k0"},{"k":"k5"}]}}`} {
		src, _, err := parseJSONString(set, in, Reject, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := tr.Merge(src); err != nil {
			t.Fatal(err)
		}
	}
	opidxAssertTreeInvariant(t, tr)
	var c *Node
	for c = range tr.Top() {
		break
	}
	var got []string
	for n := range c.Children() {
		if n.Name() == "l" {
			got = append(got, "l="+n.kids.At(0).Value())
		} else {
			got = append(got, n.Name())
		}
	}
	if want := []string{"a", "l=k0", "l=k2", "l=k3", "l=k4", "b", "l=k5", "l=k6", "c"}; !slices.Equal(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}

// TestInterleavedLookupMatrix: every XPath child lookup of an interleaved set answers by the
// native rules and never declines to the evaluator's scan of all children (design 15): with and
// without a children table, siblings of one context or of two (a merge of another context's
// child), a probe of the siblings' context or of another one, and the document root. The opaque
// b in the middle is not found (lyd_find_sibling_opaq_next searches the trailing opaque block).
func TestInterleavedLookupMatrix(t *testing.T) {
	setA, setB := ordered2Set(t, "pv2-ordered3"), ordered2Set(t, "pv2-ordered3")
	child := func(in string, mixed bool) (*Node, *Tree) {
		tr, err := parseOrdered(t, setA, false, in, nil)
		if err != nil {
			t.Fatal(err)
		}
		if mixed {
			src, err := parseOrdered(t, setA, false, `{"pv2-ordered3:a":"r"}`, nil)
			if err != nil {
				t.Fatal(err)
			}
			fb, _, err := parseJSONString(setB, `{"pv2-ordered2:c":{"st":[3]}}`, Reject, false)
			if err != nil {
				t.Fatal(err)
			}
			f := fb.top.first()
			if err := unlink(f); err != nil {
				t.Fatal(err)
			}
			if err := src.link(nil, &src.top, f, src.top.SchemaLen()); err != nil {
				t.Fatal(err)
			}
			if err := tr.Merge(src); err != nil {
				t.Fatal(err)
			}
		}
		for n := range tr.Top() {
			if n.Name() == "c" {
				return n, tr
			}
		}
		t.Fatal("no c")
		return nil, nil
	}
	find := func(set *schema.Set, mod, name string) *schema.Node {
		for _, n := range set.Implemented(mod).Top {
			if mod == "pv2-ordered3" && n.Name == name {
				return n
			}
			for _, k := range n.Children { // pv2-ordered2: the children of c
				if k.Name == name && mod == "pv2-ordered2" {
					return k
				}
			}
		}
		t.Fatalf("no %s:%s", mod, name)
		return nil
	}
	type lookup interface {
		LookupPrepared(xpath.InstanceProbe) ([]xpath.Node, bool, error)
	}
	check := func(label string, l lookup, set *schema.Set, mod, name string, vals []string, want bool) {
		t.Helper()
		p, err := xs{s: find(set, mod, name), set: set}.PrepareInstance(vals, func(int64) error { return nil })
		if err != nil {
			t.Fatal(label, err)
		}
		got, handled, err := l.LookupPrepared(p)
		if err != nil || !handled || (len(got) == 1) != want || len(got) > 1 {
			t.Fatalf("%s %s %v: %v handled %v err %v", label, name, vals, got, handled, err)
		}
	}
	for _, tc := range []struct {
		label string
		in    string
		mixed bool
		table bool
	}{
		{"no table", `{"pv2-ordered2:c":{"a":"x","b":false,"c":"z"}}`, false, false},
		{"table", `{"pv2-ordered2:c":{"a":"x","b":false,"c":"z","d":"d","ll":[1]}}`, false, true},
		{"table, two contexts", `{"pv2-ordered2:c":{"a":"x","b":false,"c":"z","d":"d","ll":[1]}}`, true, true},
		{"no table, two contexts", `{"pv2-ordered2:c":{"a":"x","b":false,"c":"z"}}`, true, false},
	} {
		c, _ := child(tc.in, tc.mixed)
		if !c.kids.st.interleaved() || (c.kids.ht != nil) != tc.table || c.kids.mixedContexts() != tc.mixed {
			t.Fatalf("%s: interleaved %v table %v mixed %v", tc.label, c.kids.st.interleaved(), c.kids.ht != nil, c.kids.mixedContexts())
		}
		for _, probeSet := range []*schema.Set{setA, setB} {
			label := tc.label
			if probeSet == setB {
				label += ", probe of another context"
			}
			x := xn{c, setA}
			check(label, x, probeSet, "pv2-ordered2", "b", nil, false)
			check(label, x, probeSet, "pv2-ordered2", "a", nil, true)
			// without a table lyd_find_sibling_schema stops at the opaque b (only the inputs with a
			// table hold ll)
			check(label, x, probeSet, "pv2-ordered2", "c", nil, tc.table)
			check(label, x, probeSet, "pv2-ordered2", "ll", []string{"1"}, tc.table)
			check(label, x, probeSet, "pv2-ordered2", "ll", []string{"7"}, false)
			check(label, x, probeSet, "pv2-ordered2", "st", []string{"3"}, tc.mixed)
		}
	}
	// the document root: no table
	tr, err := parseOrdered(t, setA, false, `{"pv2-ordered3:a":"x","pv2-ordered3:zz":"u","pv2-ordered3:b":"y"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.top.st.interleaved() {
		t.Fatal("the top level is not interleaved")
	}
	for _, probeSet := range []*schema.Set{setA, setB} {
		r := xroot{sib: &tr.top, set: setA}
		check("root", r, probeSet, "pv2-ordered3", "a", nil, true)
		check("root", r, probeSet, "pv2-ordered3", "b", nil, false) // past the first opaque root
	}
}

// TestInterleavedForwardRecovery: a relocation within an interleaved set that the running state's
// current level holds rolls forward (design 14, step 6); restoreLink puts the node back between
// its document neighbours (originPlace), a schema node between two opaque ones and an opaque
// node between two schema ones, and the state's rollback restores the tree exactly.
func TestInterleavedForwardRecovery(t *testing.T) {
	set := opIndexSet(t, orderedSchema)
	for _, pick := range []string{"c", "zz", "yy"} {
		for _, commit := range []bool{false, true} {
			tr, err := parseOrdered(t, set, false, `{"m:c":{"a":"x","zz":"u","c":"z","yy":"t","ll":[3]}}`, nil)
			if err != nil {
				t.Fatal(err)
			}
			var c *Node
			for c = range tr.Top() {
				break
			}
			capture := func(t *testing.T) {
				if err := c.kids.touch(); err != nil {
					t.Fatal(err)
				}
			}
			forwardRecoveryInState(t, tr, capture, func() *Node {
				for n := range c.Children() {
					if n.Name() == pick {
						return n
					}
				}
				t.Fatalf("no %s", pick)
				return nil
			}, commit)
			if got := orderedChildren(t, tr); !slices.Equal(got, []string{"a=x", "zz", "c=z", "yy", "ll=3"}) {
				t.Fatalf("%s: %v", pick, got)
			}
		}
	}
}

// TestFirstChildMixedCharged: FirstChild in an interleaved child level with a table and siblings
// of two contexts reads every first-instance record bucket (tableFirst); that work goes to the
// evaluation's charge, so the XPath budget refuses it and a cancelled context stops it, however
// many distinct list schemas the level holds.
func TestFirstChildMixedCharged(t *testing.T) {
	const lists = 300
	var mod, data strings.Builder
	mod.WriteString("module m { yang-version 1.1; namespace \"urn:m\"; prefix m; leaf top { type string; } container c { leaf a { type string; } leaf b { type string; }")
	data.WriteString(`{"m:c":{"a":"x","zz":"u"`)
	for i := range lists {
		fmt.Fprintf(&mod, " list l%d { key k; leaf k { type string; } }", i)
		fmt.Fprintf(&data, `,"l%d":[{"k":"1"}]`, i)
	}
	mod.WriteString(" } }")
	data.WriteString("}}")
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/m.yang", []byte(mod.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	load := func() *schema.Set {
		c, ds, err := yang.NewContext(yang.Options{NoYangLibrary: true}, os.DirFS(dir))
		if err != nil {
			t.Fatal(ds, err)
		}
		if ds, err := c.Load("m", "", nil); err != nil {
			t.Fatal(ds, err)
		}
		return snap.Set(c.Schema())
	}
	setA, setB := load(), load()
	tr, err := parseOrdered(t, setA, false, data.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	src, err := parseOrdered(t, setA, false, `{"m:top":"t"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	fb, _, err := parseJSONString(setB, `{"m:c":{"b":"y"}}`, Reject, false)
	if err != nil {
		t.Fatal(err)
	}
	f := fb.top.first()
	if err := unlink(f); err != nil {
		t.Fatal(err)
	}
	if err := src.link(nil, &src.top, f, src.top.SchemaLen()); err != nil {
		t.Fatal(err)
	}
	if err := tr.Merge(src); err != nil {
		t.Fatal(err)
	}
	var c *Node
	for n := range tr.Top() {
		if n.Name() == "c" {
			c = n
		}
	}
	if !c.kids.st.interleaved() || c.kids.ht == nil || !c.kids.mixedContexts() {
		t.Fatal("fixture: want an interleaved level with a table and two contexts")
	}
	var l7 *schema.Node
	for _, k := range setA.Implemented("m").Top[1].Children {
		if k.Name == "l7" {
			l7 = k
		}
	}
	var charged int64
	got, ok := (xn{c, setA}).FirstChild(xs{l7, setA}, func(n int64) error { charged += n; return nil })
	if !ok || got == nil || got.(xn).n.schema != l7 || charged < lists {
		t.Fatalf("found %v %v, charged %d", got, ok, charged)
	}
	for _, refusal := range []error{xpath.ErrBudget, context.Canceled} {
		if _, ok := (xn{c, setA}).FirstChild(xs{l7, setA}, func(int64) error { return refusal }); ok {
			t.Fatalf("%v: the lookup went on", refusal)
		}
	}
}

// TestCaseReplacedReversedFirst: an old case kept as b, a (ordered input) whose b is the module's
// first node is replaced. lyd_validate_cases frees a, then b, moving lyd_validate's first after
// each free (lyd_del_move_root): past b it skips a, already freed, so the duplicate replacement
// leaves are still checked. The deletion is one batch here (delMoveRoots counts the earlier
// members as freed). The tree holding both cases' data comes from a MultiError validation that
// fails yet clears FlagNew, which no oracle sequence continues past; the fixture
// protocol-v2/ordered-validate-case-reversed-first covers the replacement placed between b and a.
func TestCaseReplacedReversedFirst(t *testing.T) {
	set := ordered2Set(t, "pv2-lyds", "pv2-ordered6")
	tr, err := parseOrdered(t, set, false, `{"pv2-lyds:ll":[1],"pv2-ordered6:b":"b","pv2-ordered6:a":"a","pv2-ordered6:c":"1","pv2-ordered6:c":"2"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Validate(context.Background(), ValidateOptions{MultiError: true}); err == nil {
		t.Fatal("both cases validated")
	}
	o6 := func(n *Node) bool { return n.schema != nil && n.schema.Module.Name == "pv2-ordered6" }
	for n := range tr.Top() {
		if o6(n) && n.Name() == "c" {
			if _, err := n.SetValue("3"); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	d, err := tr.ValidateModule(context.Background(), "pv2-ordered6", ValidateOptions{})
	if err == nil || !slices.ContainsFunc(d, func(x yang.Diagnostic) bool { return strings.Contains(x.Msg, `Duplicate instance of "c"`) }) {
		t.Fatalf("%v %v", d, err)
	}
	var got []string
	for n := range tr.Top() {
		if o6(n) {
			got = append(got, n.Name()+"="+n.Value())
		}
	}
	if !slices.Equal(got, []string{"c=3", "c=2"}) {
		t.Fatalf("%v", got)
	}
}

// TestKeyRunPastOpaque: an Ordered ParseOnly list instance k=x, zz, k=y hashes the leading key
// run in document order, which stops at zz (lyd_hash). Unlinking zz joins the run without a
// rehash (lyd_unlink does not rehash the parent); the next native rehash, a changed key value,
// takes both keys (oracle: protocol-v2/ordered-list-key-past-opaque for the lookup and the dup).
func TestKeyRunPastOpaque(t *testing.T) {
	set := ordered2Set(t)
	tr, err := parseOrdered(t, set, false, `{"pv2-ordered2:c":{"l":[{"k":"x","zz":"u","k":"y"}]}}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	l, err := tr.Find("/pv2-ordered2:c/l[k='x']")
	if err != nil || l == nil {
		t.Fatalf("lookup by the hashed key: %v %v", l, err)
	}
	if l.keyRun() != 1 || l.lkeyN != 1 {
		t.Fatalf("run %d, hashed %d", l.keyRun(), l.lkeyN)
	}
	hash := l.hash
	var zz, k *Node
	for c := range l.Children() {
		switch {
		case c.schema == nil:
			zz = c
		case k == nil:
			k = c
		}
	}
	if err := zz.Unlink(); err != nil {
		t.Fatal(err)
	}
	if l.keyRun() != 2 || l.lkeyN != 1 || l.hash != hash {
		t.Fatalf("after the unlink: run %d, hashed %d, hash changed %v", l.keyRun(), l.lkeyN, l.hash != hash)
	}
	if _, err := k.SetValue("z"); err != nil {
		t.Fatal(err)
	}
	if l.lkeyN != 2 {
		t.Fatalf("after the rehash: hashed %d", l.lkeyN)
	}
	opidxAssertTreeInvariant(t, tr)
}

// TestMetaDocIndexReuseCancelled: the forward-metadata matching polls the parse's context also
// when it reuses the document index another container built, before and while it merges the
// candidate lists.
func TestMetaDocIndexReuseCancelled(t *testing.T) {
	set := opIndexSet(t, orderedSchema)
	var in strings.Builder
	in.WriteString(`{"m:c":{"zz":"u","a":"x","u":"1"`)
	for range 3000 {
		in.WriteString(`,"v":"1"`)
	}
	in.WriteString("}}")
	tr, err := parseOrdered(t, set, false, in.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var c *Node
	for c = range tr.Top() {
		break
	}
	var x metaDocIndex
	if _, err := x.match(&lydCtx{ctx: context.Background(), tree: tr}, &c.kids, nil, "u"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := x.match(&lydCtx{ctx: ctx, tree: tr}, &c.kids, nil, "v"); !errors.Is(err, context.Canceled) {
		t.Fatalf("reused index: %v", err)
	}
	// cancelled during the merge: the poll before it passes, a later one fails
	at := &cancelAtContext{Context: context.Background(), at: 2}
	if _, err := x.match(&lydCtx{ctx: at, tree: tr}, &c.kids, nil, "v"); !errors.Is(err, context.Canceled) {
		t.Fatalf("during the merge: %v", err)
	}
}

// TestRepeatedKeysPastOpaqueScaling: Ordered ParseOnly k=x, zz, then many repeated k=y. lyd_hash's
// key run stops at zz, so each repeated key's link reserves and hashes only that run: the work
// per key stays flat, and the parse stays inside the default quota.
func TestRepeatedKeysPastOpaqueScaling(t *testing.T) {
	set := ordered2Set(t)
	work := func(n int) float64 {
		var in strings.Builder
		in.WriteString(`{"pv2-ordered2:c":{"l":[{"k":"x","zz":"u"`)
		for range n {
			in.WriteString(`,"k":"y"`)
		}
		in.WriteString(`}]}}`)
		var tr *Tree
		if _, err := parseOrdered(t, set, false, in.String(), func(lc *lydCtx) { tr = lc.tree }); err != nil {
			t.Fatal(n, err)
		}
		return float64(tr.work.hashUsed) / float64(n)
	}
	small, large := work(1000), work(8000)
	if large > 2*small {
		t.Fatalf("reserved work per key: %.1f at 1000, %.1f at 8000", small, large)
	}
}

// TestKeyBookkeepingPastOpaque: in an Ordered ParseOnly list instance k=x, v, zz, then many
// repeated k=y, unlinking v and changing the first key walk the keys only up to zz (keyRun),
// so their work does not grow with the repeated keys, and every walk is reserved before the
// change: a refusal at any charge point leaves the tree as it was.
func TestKeyBookkeepingPastOpaque(t *testing.T) {
	set := opIndexSet(t, orderedSchema)
	build := func(n int) (*Tree, *Node, *Node) {
		var in strings.Builder
		in.WriteString(`{"m:c":{"l":[{"k":"x","v":"v","zz":"u"`)
		for range n {
			in.WriteString(`,"k":"y"`)
		}
		in.WriteString(`}]}}`)
		tr, err := parseOrdered(t, set, false, in.String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var l *Node
		for c := range tr.Top() {
			for l = range c.Children() {
				break
			}
		}
		var key, v *Node
		for c := range l.Children() {
			switch c.Name() {
			case "k":
				if key == nil {
					key = c
				}
			case "v":
				v = c
			}
		}
		return tr, key, v
	}
	edit := func(key, v *Node) error {
		if err := v.Unlink(); err != nil {
			return err
		}
		_, err := key.SetValue("z")
		return err
	}
	work := func(n int) int64 {
		tr, key, v := build(n)
		before := tr.work.Load()
		if err := edit(key, v); err != nil {
			t.Fatal(err)
		}
		return tr.work.Load() - before
	}
	if small, large := work(200), work(3200); large > 2*small {
		t.Fatalf("bookkeeping work: %d at 200 keys, %d at 3200", small, large)
	}
	checkEveryHashBudgetPoint(t, func() (*Tree, func() error) {
		tr, key, v := build(64)
		return tr, func() error { return edit(key, v) }
	})
}

// TestUnorderedAnchorCharged: lyd_insert_get_next_anchor's linear walk over a top level out of
// schema order is charged step by step, so a small enforced quota refuses a long walk; in an
// interleaved top level the walk stops at the first opaque sibling and stays cheap.
func TestUnorderedAnchorCharged(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	  leaf a { type string; } leaf-list u { type uint32; ordered-by user; } leaf b { type string; } }`)
	const n = 20000
	parse := func(opaque bool) *Tree {
		var in strings.Builder
		in.WriteString(`{`)
		if opaque {
			in.WriteString(`"m:a":"a","m:zz":"u",`)
		}
		in.WriteString(`"m:u":[`)
		for i := range n {
			if i > 0 {
				in.WriteString(",")
			}
			fmt.Fprint(&in, i)
		}
		in.WriteString(`]`)
		if !opaque {
			in.WriteString(`,"m:a":"a"`)
		}
		in.WriteString(`}`)
		tr, err := parseOrdered(t, set, false, in.String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	tr := parse(false)
	tr.work.hashLimit = 8192 // per operation (beginHashWork)
	if _, err := tr.NewTerm(nil, "m:b", "x", NewOptions{}); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("a walk over %d siblings under a small quota: %v", n, err)
	}
	opidxAssertTreeInvariant(t, tr)
	tr = parse(true)
	tr.work.hashLimit = 8192 // per operation (beginHashWork)
	if _, err := tr.NewTerm(nil, "m:b", "x", NewOptions{}); err != nil {
		t.Fatalf("the walk stops at the opaque sibling: %v", err)
	}
	if got := names(tr.Top())[:3]; !slices.Equal(got, []string{"a=a", "b=x", "zz"}) {
		t.Fatalf("%v", got)
	}
}

// mixedTableLevel parses an Ordered c of pv2-ordered8 in context A (a children table) and links a
// child z of context B last, the layout of the merge construction of protocol-v2/merge-foreign-child
// once the foreign child no longer leads.
func mixedTableLevel(t *testing.T, in string) (*Tree, *Node) {
	t.Helper()
	setA, setB := ordered2Set(t, "pv2-ordered8"), ordered2Set(t, "pv2-ordered8")
	tr, err := parseOrdered(t, setA, false, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	var c *Node
	for c = range tr.Top() {
		break
	}
	fb, _, err := parseJSONString(setB, `{"pv2-ordered8:c":{"z":"zz"}}`, Reject, false)
	if err != nil {
		t.Fatal(err)
	}
	z := fb.top.first().kids.first()
	if err := unlink(z); err != nil {
		t.Fatal(err)
	}
	if err := tr.link(c, &c.kids, z, c.kids.SchemaLen()); err != nil {
		t.Fatal(err)
	}
	if c.kids.ht == nil || !c.kids.mixedContexts() || !c.kids.schemaUnordered || c.kids.st.interleaved() {
		t.Fatal("fixture: want a table level of two contexts out of schema order, not interleaved")
	}
	return tr, c
}

// TestMixedTableDupInstRun: lyd_find_sibling_first of a duplicate-instance list with a children
// table starts at the first-instance record and walks node->next while the schema matches, so
// the value of the piece after b is not found, also among siblings of two contexts.
func TestMixedTableDupInstRun(t *testing.T) {
	tr, _ := mixedTableLevel(t, `{"pv2-ordered8:c":{"p":"p","st":[1],"b":"b","st":[2]}}`)
	if n, err := tr.Find("/pv2-ordered8:c/st[.='2']"); err != nil || n != nil {
		t.Fatalf("the later piece: %v %v", n, err)
	}
	if n, err := tr.Find("/pv2-ordered8:c/st[.='1']"); err != nil || n == nil {
		t.Fatalf("the first piece: %v %v", n, err)
	}
}

// TestMixedTableInsertLeader: inserting 0 replaces the first ll piece's record, so the anchor of a
// later insertion of a (lyd_insert_get_next_anchor's lyd_find_sibling_schema) is the other piece's
// record, also when only a schema node splits the run among siblings of two contexts.
func TestMixedTableInsertLeader(t *testing.T) {
	tr, c := mixedTableLevel(t, `{"pv2-ordered8:c":{"p":"p","ll":[2],"b":"b","ll":[4]}}`)
	if _, err := tr.NewTerm(c, "ll", "0", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.NewTerm(c, "a", "x", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	opidxAssertTreeInvariant(t, tr)
	if got := names(c.Children()); !slices.Equal(got, []string{"p=p", "ll=0", "ll=2", "b=b", "a=x", "ll=4", "z=zz"}) {
		t.Fatalf("%v", got)
	}
}

// TestMixedTableReadsShareNothing: Find in a table level of two contexts out of schema order
// answers by the table's registration order (tableSchemaFirst) without writing shared tree
// state: concurrent readers race on nothing, and any number of repeated lookups gives the same
// node, the second x, re-registered after the first one's unlink and reinsertion.
func TestMixedTableReadsShareNothing(t *testing.T) {
	tr, c := mixedTableLevel(t, `{"pv2-ordered8:c":{"p":"p","x":"first","b":"b","x":"second"}}`)
	var first *Node
	for n := range c.Children() {
		if n.Name() == "x" {
			first = n
			break
		}
	}
	if err := first.Unlink(); err != nil {
		t.Fatal(err)
	}
	if err := tr.Insert(c, first); err != nil {
		t.Fatal(err)
	}
	want := func() string {
		n, err := tr.Find("/pv2-ordered8:c/x")
		if err != nil || n == nil {
			t.Fatalf("%v %v", n, err)
		}
		return n.Value()
	}()
	if want != "second" {
		t.Fatalf("the table's first registered x: %s", want)
	}
	used := tr.work.lookupUsed
	for range 20000 {
		if n, err := tr.Find("/pv2-ordered8:c/x"); err != nil || n == nil || n.Value() != want {
			t.Fatalf("a repeated lookup: %v %v", n, err)
		}
	}
	if tr.work.lookupUsed != used || tr.work.lookupErr != nil {
		t.Fatal("a read-only lookup wrote the tree's lookup quota")
	}
	// an instance lookup of x (lyd_find_sibling_first): a leaf matches by schema alone in the
	// table (lyd_hash_table_val_equal), so a probe with the first one's value finds the second
	probe, _, err := parseJSONString(ctxOf(first), `{"pv2-ordered8:c":{"x":"first"}}`, Reject, false)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := tr.findFirstWork(&c.kids, probe.top.first().kids.first(), nil); err != nil || m == nil || m.Value() != "second" {
		t.Fatalf("instance lookup: %v %v", m, err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 200 {
				if n, err := tr.Find("/pv2-ordered8:c/x"); err != nil || n == nil || n.Value() != want {
					t.Errorf("a concurrent lookup: %v %v", n, err)
					return
				}
				var b strings.Builder
				if err := tr.PrintJSON(&b, PrintOptions{EmptyLeafList: true}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	// an insertion anchor (lyd_insert_get_next_anchor's lyd_find_sibling_schema) is the table's
	// first registered x as well: a goes right before the second x
	if _, err := tr.NewTerm(c, "a", "v", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	opidxAssertTreeInvariant(t, tr)
	var seq []string
	for n := range c.Children() {
		seq = append(seq, n.Name()+"="+n.Value())
	}
	if i := slices.Index(seq, "a=v"); i < 0 || i+1 >= len(seq) || seq[i+1] != "x=second" {
		t.Fatalf("a's anchor: %v", seq)
	}
}

// TestTableLookupCallersAgree: every caller of the children-table lookup answers alike, for each
// node kind (a repeated leaf, a leaf-list split by another node, a state leaf-list), siblings of
// one or two contexts, in or out of schema order: Find, schemaLookup, FirstChild and the keyed
// XPath lookup, and for a value Find, findFirstWork and LookupChild. Out of schema order with a
// re-registered first x the answer is the second x, libyang's first registered one.
func TestTableLookupCallersAgree(t *testing.T) {
	for _, ordered := range []bool{false, true} {
		for _, mixed := range []bool{false, true} {
			setA, setB := ordered2Set(t, "pv2-ordered8"), ordered2Set(t, "pv2-ordered8")
			in := `{"pv2-ordered8:c":{"p":"p","x":"first","ll":[2],"b":"b","st":[1,2]}}`
			if ordered {
				in = `{"pv2-ordered8:c":{"p":"p","x":"first","ll":[2],"st":[1],"b":"b","x":"second","ll":[4],"st":[2]}}`
			}
			var tr *Tree
			var err error
			if ordered {
				tr, err = parseOrdered(t, setA, false, in, nil)
			} else {
				tr, _, err = parseJSONString(setA, in, Opaque, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			var c *Node
			for c = range tr.Top() {
				break
			}
			if mixed {
				fb, _, err := parseJSONString(setB, `{"pv2-ordered8:c":{"z":"zz"}}`, Reject, false)
				if err != nil {
					t.Fatal(err)
				}
				z := fb.top.first().kids.first()
				if err := unlink(z); err != nil {
					t.Fatal(err)
				}
				if err := tr.link(c, &c.kids, z, c.kids.SchemaLen()); err != nil {
					t.Fatal(err)
				}
			}
			if ordered { // re-register the first x
				for n := range c.Children() {
					if n.Name() == "x" {
						if err := n.Unlink(); err != nil {
							t.Fatal(err)
						}
						if err := tr.Insert(c, n); err != nil {
							t.Fatal(err)
						}
						break
					}
				}
			}
			label := fmt.Sprintf("ordered %v, mixed %v", ordered, mixed)
			for _, name := range []string{"x", "ll", "st"} {
				var sn *schema.Node
				for _, k := range setA.Implemented("pv2-ordered8").Top[0].Children {
					if k.Name == name {
						sn = k
					}
				}
				direct := tr.schemaLookup(&c.kids, sn)
				byPath := direct
				if name == "x" { // a leaf-list path needs a predicate
					if byPath, err = tr.Find("/pv2-ordered8:c/" + name); err != nil {
						t.Fatal(label, name, err)
					}
				}
				fc, ok := (xn{c, setA}).FirstChild(xs{sn, setA}, nil)
				var first *Node
				if ok && fc != nil {
					first = fc.(xn).n
				} else {
					first = direct // declined: the evaluator's charged scan
				}
				if byPath != direct || first != direct {
					t.Fatalf("%s %s: Find %v, schemaLookup %v, FirstChild %v", label, name, byPath, direct, first)
				}
				if name == "x" && ordered && mixed && direct.Value() != "second" {
					t.Fatalf("%s: the first registered x is the second one, got %s", label, direct.Value())
				}
			}
			for _, v := range []string{"2", "4"} {
				byPath, err := tr.Find("/pv2-ordered8:c/ll[.='" + v + "']")
				if err != nil {
					t.Fatal(label, err)
				}
				var sn *schema.Node
				for _, k := range setA.Implemented("pv2-ordered8").Top[0].Children {
					if k.Name == "ll" {
						sn = k
					}
				}
				got, handled := (xn{c, setA}).LookupChild(xs{sn, setA}, []string{v})
				var viaXPath *Node
				if handled && len(got) == 1 {
					viaXPath = got[0].(xn).n
				} else if !handled {
					viaXPath = byPath // declined: the evaluator's scan
				}
				probe, _, err := parseJSONString(setA, `{"pv2-ordered8:c":{"ll":[`+v+`]}}`, Reject, false)
				if err != nil {
					t.Fatal(err)
				}
				inst, err := tr.findFirstWork(&c.kids, probe.top.first().kids.first(), nil)
				if err != nil {
					t.Fatal(err)
				}
				if byPath != inst || viaXPath != inst {
					t.Fatalf("%s ll=%s: Find %v, findFirstWork %v, LookupChild %v", label, v, byPath, inst, viaXPath)
				}
			}
		}
	}
}

// TestMixedTableMergeScaling: merging instances of n distinct list schemas into a table level of
// two contexts out of schema order looks each one up in the children table's bucket of its own
// module and node names (equivalents: one candidate per context), so the work per schema stays
// flat; every lookup step is charged to the merge, and a refusal at any charge point leaves the
// tree as it was.
func TestMixedTableMergeScaling(t *testing.T) {
	setup := func(n int) (func() (*Tree, func() error), *schema.Set) {
		var mod, have, add strings.Builder
		mod.WriteString("module m { yang-version 1.1; namespace \"urn:m\"; prefix m; leaf top { type string; } container c { leaf a { type string; } leaf z { type string; }")
		have.WriteString(`{"m:c":{"z":"z0","a":"x"`)
		add.WriteString(`{"m:c":{`)
		for i := range n {
			fmt.Fprintf(&mod, " list l%d { key k; leaf k { type string; } }", i)
			fmt.Fprintf(&have, `,"l%d":[{"k":"1"}]`, i)
			if i > 0 {
				add.WriteString(",")
			}
			fmt.Fprintf(&add, `"l%d":[{"k":"2"}]`, i)
		}
		mod.WriteString(" } }")
		have.WriteString("}}")
		add.WriteString("}}")
		dir := t.TempDir()
		if err := os.WriteFile(dir+"/m.yang", []byte(mod.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		load := func() *schema.Set {
			c, ds, err := yang.NewContext(yang.Options{NoYangLibrary: true}, os.DirFS(dir))
			if err != nil {
				t.Fatal(ds, err)
			}
			if ds, err := c.Load("m", "", nil); err != nil {
				t.Fatal(ds, err)
			}
			return snap.Set(c.Schema())
		}
		setA, setB := load(), load()
		return func() (*Tree, func() error) {
			tr, err := parseOrdered(t, setA, false, have.String(), nil)
			if err != nil {
				t.Fatal(err)
			}
			var c *Node
			for c = range tr.Top() {
				break
			}
			fb, _, err := parseJSONString(setB, `{"m:c":{"a":"y"}}`, Reject, false)
			if err != nil {
				t.Fatal(err)
			}
			f := fb.top.first().kids.first()
			if err := unlink(f); err != nil {
				t.Fatal(err)
			}
			if err := tr.link(c, &c.kids, f, c.kids.SchemaLen()); err != nil {
				t.Fatal(err)
			}
			if c.kids.ht == nil || !c.kids.mixedContexts() || !c.kids.schemaUnordered {
				t.Fatal("fixture: want a table level of two contexts out of schema order")
			}
			src, _, err := parseJSONString(setA, add.String(), Reject, false)
			if err != nil {
				t.Fatal(err)
			}
			return tr, func() error { return tr.Merge(src) }
		}, setA
	}
	work := func(n int) float64 {
		build, _ := setup(n)
		tr, merge := build()
		tr.work.Store(0)
		tr.work.visits = true
		if err := merge(); err != nil {
			t.Fatal(err)
		}
		return float64(tr.work.Load()) / float64(n)
	}
	if small, large := work(100), work(800); large > 2*small {
		t.Fatalf("merge work per schema: %.1f at 100, %.1f at 800", small, large)
	}
	build, _ := setup(6)
	checkEveryHashBudgetPoint(t, build)
}
