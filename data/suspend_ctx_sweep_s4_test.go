// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/snap"
)

// #240 step 4 (design 13 §2.4): every public entry that takes a context holds its trees from
// before the first poll until after the last. The sweep counts the polls each entry makes on a
// small fixture (N), then for every k = 1..N runs it again with a context whose Err attempts a
// set of mutations of the held tree at poll k. Each mutation must be refused in the public form
// (*ValidationError, LY_EINVAL, the diagnostic), nothing may panic, and the entry's result must
// equal the benign run's (reciprocal leafref records consistent).

// pollCtx counts its Err calls and runs at at poll k.
type pollCtx struct {
	context.Context
	polls, k int
	at       func()
}

func (c *pollCtx) Err() error {
	c.polls++
	if c.polls == c.k && c.at != nil {
		c.at()
	}
	return nil
}

// s4ctxFixture is one public ctx entry on fresh trees: held is the tree it must hold, term a
// term node in it, and run the call, its observable result returned as text.
type s4ctxFixture struct {
	held        *Tree
	term        *Node
	name, value string // a term NewTerm can create under held's first root
	run         func(ctx context.Context) string
}

// s4LinksConsistent fails if a leafref record of tr is not reciprocated.
func s4LinksConsistent(t *testing.T, where string, tr *Tree) {
	t.Helper()
	for root := range tr.Top() {
		for n := range root.allAny() {
			if n.links == nil {
				continue
			}
			for _, l := range n.links.leafrefs {
				if l.links == nil || !containsNode(l.links.targets, n) {
					t.Fatalf("%s: a leafref record is not reciprocated", where)
				}
			}
			for _, x := range n.links.targets {
				if x.links == nil || !containsNode(x.links.leafrefs, n) {
					t.Fatalf("%s: a target record is not reciprocated", where)
				}
			}
		}
	}
}

func containsNode(ns []*Node, n *Node) bool {
	for _, x := range ns {
		if x == n {
			return true
		}
	}
	return false
}

func TestS4CtxEntriesHoldTheirTrees(t *testing.T) {
	lset := s4LinksSet(t)
	jset := reviewDiffSet(t, s4JournalModule)
	oset, oparent := s4OpsFixture(t)
	linked := func() *Tree {
		tr := s4ParseLinks(t, lset, `{"pv2-links:c":{"l":[{"k":"a"},{"k":"b"}],"t":["y"],"r":"a"}}`)
		if _, err := tr.LinkLeafrefs(context.Background()); err != nil {
			t.Fatal(err)
		}
		return tr
	}
	result := func(tr *Tree, diags any, err error) string {
		return fmt.Sprintf("%v|%v|%v", typedDump(tr), diags, err)
	}
	validate := func(f func(tr *Tree, ctx context.Context) (any, error)) func() s4ctxFixture {
		return func() s4ctxFixture {
			tr := linked()
			return s4ctxFixture{held: tr, term: s4Child(tr.top.first(), "r", "a"), name: "t", value: "n",
				run: func(ctx context.Context) string { d, err := f(tr, ctx); return result(tr, d, err) }}
		}
	}
	fixtures := map[string]func() s4ctxFixture{
		"Validate": validate(func(tr *Tree, ctx context.Context) (any, error) { return tr.Validate(ctx, ValidateOptions{}) }),
		"ValidateModule": validate(func(tr *Tree, ctx context.Context) (any, error) {
			return tr.ValidateModule(ctx, "pv2-links", ValidateOptions{})
		}),
		"ValidateModuleFinal": validate(func(tr *Tree, ctx context.Context) (any, error) {
			return tr.ValidateModuleFinal(ctx, "pv2-links", ValidateOptions{})
		}),
		"ValidateDiff": validate(func(tr *Tree, ctx context.Context) (any, error) {
			diff, d, err := tr.ValidateDiff(ctx, ValidateOptions{})
			if diff != nil {
				return fmt.Sprint(d, typedDump(diff)), err
			}
			return d, err
		}),
		"ValidateModuleDiff": validate(func(tr *Tree, ctx context.Context) (any, error) {
			diff, d, err := tr.ValidateModuleDiff(ctx, "pv2-links", ValidateOptions{})
			if diff != nil {
				return fmt.Sprint(d, typedDump(diff)), err
			}
			return d, err
		}),
		"LinkLeafrefs": validate(func(tr *Tree, ctx context.Context) (any, error) { return tr.LinkLeafrefs(ctx) }),
		"ValidateOp": func() s4ctxFixture {
			req, op := s4OpTree(t, jset, "5", "1", "9")
			oper := s4DataTree(t, jset, "3", "7")
			return s4ctxFixture{held: req, term: s4Child(op, "w", "5"), name: "w", value: "6",
				run: func(ctx context.Context) string {
					d, err := req.ValidateOp(ctx, OpRPC, ValidateOpOptions{Operational: oper})
					return result(req, d, err) + typedDump(oper)[0]
				}}
		},
		"ParseOp": func() s4ctxFixture {
			tree, op := oparent()
			return s4ctxFixture{held: tree, term: op.kids.first(), name: "al", value: "7",
				run: func(ctx context.Context) string {
					res, d, err := ParseOp(ctx, strings.NewReader(`{}`), FormatJSON, snap.New(oset), OpReply, ParseOpOptions{Request: op})
					return result(tree, d, err) + fmt.Sprint(res.Op != nil)
				}}
		},
	}
	total := 0
	for name, build := range fixtures {
		t.Run(name, func(t *testing.T) {
			benign := &pollCtx{Context: context.Background()}
			f := build()
			want := f.run(benign)
			s4LinksConsistent(t, "benign", f.held)
			if benign.polls == 0 {
				t.Fatalf("%s polls no context on the fixture", name)
			}
			for k := 1; k <= benign.polls; k++ {
				total++
				f := build()
				root := f.held.top.first()
				scratch := newTerm(f.term.schema, f.term.value)
				other := newTree(f.held.set)
				mutations := map[string]func() error{
					"SetValue":      func() error { _, err := f.term.SetValue("zz"); return err },
					"RemoveRoot":    func() error { return root.Remove() },
					"UnlinkRoot":    func() error { return root.Unlink() },
					"NewTermUnder":  func() error { _, err := f.held.NewTerm(root, f.name, f.value, NewOptions{}); return err },
					"InsertUnder":   func() error { return f.held.Insert(root, scratch) },
					"MergeIntoHeld": func() error { return f.held.Merge(other) },
					"NewPathInHeld": func() error { _, err := f.held.NewPath(root.Path(), "", NewPathOptions{}); return err },
				}
				ctx := &pollCtx{Context: context.Background(), k: k, at: func() {
					for m, mutate := range mutations {
						s4PublicRefusal(t, fmt.Sprintf("%s poll %d %s", name, k, m), nil, mutate(), false)
					}
				}}
				got := f.run(ctx)
				if ctx.polls < k {
					t.Fatalf("poll %d: the entry polled only %d times", k, ctx.polls)
				}
				if got != want {
					t.Fatalf("poll %d: the result differs from the benign run:\n%s\nwant\n%s", k, got, want)
				}
				s4LinksConsistent(t, fmt.Sprintf("poll %d", k), f.held)
			}
		})
	}
	t.Logf("%d entries, %d mutated polls", len(fixtures), total)
}

// TestS4DetachedNodeNotHeldByItsOldTree: a node unlinked before an operation keeps its old
// counter only as accounting: user code of a LinkLeafrefs or ApplyDiff on that tree may still
// edit it (snapshot review 4, finding 1).
func TestS4DetachedNodeNotHeldByItsOldTree(t *testing.T) {
	set := s4LinksSet(t)
	tr := s4ParseLinks(t, set, `{"pv2-links:c":{"l":[{"k":"a"}],"t":["y","x"],"r":"a"}}`)
	detached := s4Child(tr.top.first(), "t", "y")
	if err := detached.Unlink(); err != nil {
		t.Fatal(err)
	}
	var editErr error
	ctx := &pollCtx{Context: context.Background(), k: 1, at: func() { _, editErr = detached.SetValue("z") }}
	if _, err := tr.LinkLeafrefs(ctx); err != nil {
		t.Fatal(err)
	}
	if editErr != nil || detached.Value() != "z" {
		t.Fatalf("editing a node detached earlier: %v (value %q), want success", editErr, detached.Value())
	}
	other := s4ParseLinks(t, set, `{"pv2-links:c":{"t":["q"]}}`)
	err := other.ApplyDiff(s4Diff(t, set, other, false), ApplyDiffOptions{Callback: func(_, _ *Node) error {
		_, err := detached.SetValue("w")
		return err
	}})
	if err != nil {
		t.Fatalf("ApplyDiff whose callback edits an unrelated detached node: %v", err)
	}
}

// TestS4HeldCheckIsConstantOnTheCommonPath: touching every node of a journaled deep chain,
// whose state waits on nothing, makes no guard root walk (snapshot review 4, finding 2).
func TestS4HeldCheckIsConstantOnTheCommonPath(t *testing.T) {
	set := reviewDiffSet(t, s4JournalModule)
	tr, op := s4OpTree(t, set, "5", "1", "9")
	var nodes []*Node
	for n := range op.allAny() {
		nodes = append(nodes, n)
	}
	j := beginHashTransaction(tr)
	heldWalks = 0
	for _, n := range nodes {
		if err := n.touch(); err != nil {
			t.Fatal(err)
		}
		if err := childStorage(n).touch(); err != nil {
			t.Fatal(err)
		}
	}
	walks := heldWalks
	j.end(nil)
	if walks != 0 {
		t.Fatalf("%d guard root walks on the common path", walks)
	}
}

// TestS4CallbackHoldsTheDiffSource: an ApplyDiff callback that changes a leaf of the source
// diff before the traversal reaches it is refused (the source is held through callbacks), and
// ApplyDiff gives the reference result (snapshot review 4, direction note).
func TestS4CallbackHoldsTheDiffSource(t *testing.T) {
	set := s4LinksSet(t)
	ref, _, _, _ := s4LinkedPair(t, set)
	if err := ref.ApplyDiff(s4Diff(t, set, ref, false), ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	want := typedDump(ref)
	a, _, _, _ := s4LinkedPair(t, set)
	diff := s4Diff(t, set, a, false)
	var leaf *Node
	for n := range diff.top.first().allAny() {
		if n.schema != nil && n.isTerm() && n.Value() == "z" {
			leaf = n
		}
	}
	if leaf == nil {
		t.Fatal("setup: the diff has no created t='z'")
	}
	called := false
	err := a.ApplyDiff(diff, ApplyDiffOptions{Callback: func(_, _ *Node) error {
		if !called {
			called = true
			_, err := leaf.SetValue("zz")
			s4PublicRefusal(t, "source SetValue", nil, err, false)
		}
		return nil
	}})
	if err != nil || !called {
		t.Fatalf("ApplyDiff: %v (called %t)", err, called)
	}
	if got := typedDump(a); !reflect.DeepEqual(got, want) {
		t.Fatalf("history differs from the reference:\n%v\nwant\n%v", got, want)
	}
}
