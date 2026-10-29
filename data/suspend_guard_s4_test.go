// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

// #240 step 4 (design 13 §2.4, G8): the suspension guard. User code (diff callbacks, the Reader of
// a ParseOp with a parent, context methods and the methods of the errors they return) runs with
// its operation's state suspended; any capture, lend, begin or link-record edit that reaches an
// object governed by a suspended state is refused with LY_EINVAL before it writes.

func s4LinksSet(t *testing.T) *schema.Set {
	t.Helper()
	set := corpusSet(t, []string{"../conformance/corpus/protocol-v2/schemas"}, []string{"pv2-links"})
	set.LeafrefLinking = true
	return set
}

func s4ParseLinks(t *testing.T, set *schema.Set, in string) *Tree {
	t.Helper()
	tr, diags, err := parseWith(context.Background(), strings.NewReader(in), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
	if err != nil {
		t.Fatal(err, diags)
	}
	return tr
}

// s4LinkedPair returns two trees connected by a retained leafref link: A holds l[k='a'] (moved
// from B), B holds r, a leafref in B targeting A's k.
func s4LinkedPair(t *testing.T, set *schema.Set) (a, b *Tree, k, r *Node) {
	t.Helper()
	b = s4ParseLinks(t, set, `{"pv2-links:c":{"l":[{"k":"a"}],"r":"a"}}`)
	if _, err := b.LinkLeafrefs(context.Background()); err != nil {
		t.Fatal(err)
	}
	a = s4ParseLinks(t, set, `{"pv2-links:c":{"t":["y"]}}`)
	list, r := b.top.first().kids.At(0), b.top.first().kids.At(1)
	k = list.kids.At(0)
	if err := a.Insert(a.top.first(), list); err != nil {
		t.Fatal(err)
	}
	if lrefs, _ := k.LeafrefLinks(); len(lrefs) != 1 || lrefs[0] != r || r.treeOf() != b || k.treeOf() != a {
		t.Fatalf("setup: no cross-tree link: %v", lrefs)
	}
	return a, b, k, r
}

// s4Diff is a diff of a's content that adds t='z' (and, with del, deletes l[k='a']).
func s4Diff(t *testing.T, set *schema.Set, a *Tree, del bool) *Tree {
	t.Helper()
	target := `{"pv2-links:c":{"l":[{"k":"a"}],"t":["y","z"]}}`
	if del {
		target = `{"pv2-links:c":{"t":["y","z"]}}`
	}
	diff, err := Diff(a, s4ParseLinks(t, set, target), DiffOptions{})
	if err != nil || diff == nil {
		t.Fatal(diff, err)
	}
	return diff
}

func s4Dumps(trees ...*Tree) [][]string {
	out := make([][]string, len(trees))
	for i, tr := range trees {
		out[i] = typedDump(tr)
	}
	return out
}

// TestS4GuardLinkedCallbackEdits is G8(a) and (b): an ApplyDiff on A whose callback edits B's
// leafref, which is linked to A, is refused each time with both trees unchanged at that point;
// a journaled nested operation that has written before reaching the link (Merge into B) restores
// its own writes. ApplyDiff then completes by its callback contract, and each tree is mutated
// independently afterwards, exactly as in the reference without the attempts.
func TestS4GuardLinkedCallbackEdits(t *testing.T) {
	set := s4LinksSet(t)
	further := func(t *testing.T, a, b *Tree) {
		t.Helper()
		if _, err := a.NewTerm(a.top.first(), "t", "q", NewOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := b.NewTerm(b.top.first(), "t", "w", NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	a, b, _, _ := s4LinkedPair(t, set)
	if err := a.ApplyDiff(s4Diff(t, set, a, false), ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	further(t, a, b)
	want := s4Dumps(a, b)

	edits := map[string]func(b *Tree, r *Node) error{
		"unlink": func(_ *Tree, r *Node) error { return r.Unlink() },
		"free":   func(_ *Tree, r *Node) error { return r.Remove() },
		"value": func(_ *Tree, r *Node) error {
			_, err := r.SetValue("b")
			return err
		},
		"merge": func(b *Tree, _ *Node) error { // writes l[k='z'] first, then reaches r
			src := s4ParseLinks(t, set, `{"pv2-links:c":{"l":[{"k":"z"}],"r":"b"}}`)
			return b.Merge(src)
		},
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			a, b, _, r := s4LinkedPair(t, set)
			attempts := 0
			err := a.ApplyDiff(s4Diff(t, set, a, false), ApplyDiffOptions{Callback: func(_, _ *Node) error {
				attempts++
				snapA, snapB := snapshotHashBudgetTree(a), snapshotHashBudgetTree(b)
				if err := edit(b, r); !s4Refused(err) {
					t.Errorf("%s from the callback: %v, want the guard's refusal", name, err)
				}
				if !reflect.DeepEqual(snapshotHashBudgetTree(a), snapA) || !reflect.DeepEqual(snapshotHashBudgetTree(b), snapB) {
					t.Errorf("%s: the refused edit changed a tree", name)
				}
				return nil // the callback contract: ApplyDiff continues
			}})
			if err != nil || attempts == 0 {
				t.Fatalf("ApplyDiff: %v after %d attempts", err, attempts)
			}
			s4Check(t, name, []*Tree{a, b})
			further(t, a, b)
			if got := s4Dumps(a, b); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: history differs from the reference:\n%v\nwant\n%v", name, got, want)
			}
		})
	}
}

// TestS4GuardThirdTreeAllowed is G8(e): a callback that mutates an unlinked third tree proceeds.
func TestS4GuardThirdTreeAllowed(t *testing.T) {
	set := s4LinksSet(t)
	a, _, _, _ := s4LinkedPair(t, set)
	third := s4ParseLinks(t, set, `{"pv2-links:c":{"t":["p"]}}`)
	err := a.ApplyDiff(s4Diff(t, set, a, false), ApplyDiffOptions{Callback: func(_, _ *Node) error {
		_, err := third.NewTerm(third.top.first(), "t", "o", NewOptions{})
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := typedDump(third); !strings.Contains(strings.Join(got, "\n"), "o") {
		t.Fatalf("the third tree was not changed: %v", got)
	}
}

// TestS4GuardSameTreeBegin is G8(f): a callback calling NewPath or Insert on the tree being
// changed is refused; the outer journal stays installed, and the outer operation's later
// budget rollback restores everything, writes before the callback included.
func TestS4GuardSameTreeBegin(t *testing.T) {
	set := s4LinksSet(t)
	for _, op := range []string{"newpath", "insert"} {
		t.Run(op, func(t *testing.T) {
			a, b, _, _ := s4LinkedPair(t, set)
			before := s4Dumps(a, b)
			var outer *hashTransaction
			err := a.ApplyDiff(s4Diff(t, set, a, false), ApplyDiffOptions{Callback: func(_, _ *Node) error {
				outer = a.work.journal
				var err error
				if op == "newpath" {
					_, err = a.NewPath("/pv2-links:c/t[.='n']", "", NewPathOptions{})
				} else {
					err = a.Insert(a.top.first(), newTerm(a.top.first().kids.At(0).schema, a.top.first().kids.At(0).value, &a.work))
				}
				if !s4Refused(err) {
					t.Errorf("%s on the tree being changed: %v, want the guard's refusal", op, err)
				}
				if a.work.journal != outer || outer == nil {
					t.Error("the refused begin replaced the outer journal")
				}
				return yang.ErrBudget // the outer operation then fails on budget
			}})
			if !errors.Is(err, yang.ErrBudget) {
				t.Fatalf("ApplyDiff: %v, want the callback's budget error", err)
			}
			if got := s4Dumps(a, b); !reflect.DeepEqual(got, before) {
				t.Fatal("the outer budget rollback did not restore the trees")
			}
			s4Check(t, op, []*Tree{a, b})
		})
	}
}

// TestS4GuardEnrolledCounterpart is G8(g), review 5's reproducer: the outer operation removes
// A's last link to B, and then its callback mutates B. The mutation is refused (B's counter was
// enrolled when the link's counterpart was captured). After the outer rollback, B and its links
// are consistent, and both trees continue independently.
func TestS4GuardEnrolledCounterpart(t *testing.T) {
	set := s4LinksSet(t)
	a, b, k, r := s4LinkedPair(t, set)
	before := s4Dumps(a, b)
	refused := false
	err := a.ApplyDiff(s4Diff(t, set, a, true), ApplyDiffOptions{Callback: func(_, n *Node) error {
		if n.Name() != "t" || n.Value() != "z" {
			return nil
		}
		if _, targets := r.LeafrefLinks(); len(targets) != 0 {
			t.Error("the delete did not remove the link")
		}
		_, err := b.NewTerm(b.top.first(), "t", "m", NewOptions{})
		refused = s4Refused(err)
		return yang.ErrBudget
	}})
	if !errors.Is(err, yang.ErrBudget) || !refused {
		t.Fatalf("ApplyDiff: %v, B mutation refused %t", err, refused)
	}
	if got := s4Dumps(a, b); !reflect.DeepEqual(got, before) {
		t.Fatal("the rollback did not restore the trees")
	}
	if lrefs, _ := k.LeafrefLinks(); len(lrefs) != 1 || lrefs[0] != r {
		t.Fatal("the rollback did not restore the link")
	}
	if b.work.journal != nil || a.work.journal != nil {
		t.Fatal("an enrollment outlived its operation")
	}
	s4Check(t, "after", []*Tree{a, b})
	if _, err := b.NewTerm(b.top.first(), "t", "m", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.NewTerm(a.top.first(), "t", "m", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	s4Check(t, "independent", []*Tree{a, b})
}

// TestS4GuardCallbackPanic is G8(i): a callback's panic propagates, the state is not left
// suspended (nor installed), and the next operation on both trees succeeds.
func TestS4GuardCallbackPanic(t *testing.T) {
	set := s4LinksSet(t)
	a, b, _, _ := s4LinkedPair(t, set)
	var j *hashTransaction
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the callback's panic did not propagate")
			}
		}()
		_ = a.ApplyDiff(s4Diff(t, set, a, false), ApplyDiffOptions{Callback: func(_, _ *Node) error {
			j = a.work.journal
			panic("callback")
		}})
	}()
	if j == nil || j.suspendDepth != 0 {
		t.Fatalf("suspension left behind: %v", j)
	}
	if a.work.journal != nil || a.work.hashDepth != 0 {
		t.Fatalf("after the panic: journal %v, hash-work depth %d", a.work.journal, a.work.hashDepth)
	}
	if _, err := a.NewTerm(a.top.first(), "t", "p", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.NewTerm(b.top.first(), "t", "p", NewOptions{}); err != nil {
		t.Fatal(err)
	}
}

// TestS4GuardNesting is G8(k): a callback runs an XPath query on the tree being changed (its
// evaluation polls a context) and then mutates the same tree: refused, the depth still 1. A
// mutation attempted from a nested user-code site, at depth 2, is refused too. After the
// callback the depth is 0 and ApplyDiff finishes by its contract.
func TestS4GuardNesting(t *testing.T) {
	set := s4LinksSet(t)
	a, _, _, _ := s4LinkedPair(t, set)
	var j *hashTransaction
	err := a.ApplyDiff(s4Diff(t, set, a, false), ApplyDiffOptions{Callback: func(_, _ *Node) error {
		j = a.work.journal
		if _, _, err := a.FindXPath("/pv2-links:c/t", XPathOptions{}); err != nil {
			t.Error(err)
		}
		if j.suspendDepth != 1 {
			t.Errorf("depth after the query %d, want 1", j.suspendDepth)
		}
		if _, err := a.NewTerm(a.top.first(), "t", "n", NewOptions{}); !s4Refused(err) {
			t.Errorf("mutation after the query: %v", err)
		}
		inner := a.work.callUser(func() error { // a nested user-code site
			if j.suspendDepth != 2 {
				t.Errorf("nested depth %d, want 2", j.suspendDepth)
			}
			_, err := a.NewTerm(a.top.first(), "t", "n", NewOptions{})
			return err
		})
		if !s4Refused(restoreUser(inner)) || j.suspendDepth != 1 {
			t.Errorf("nested mutation: %v, depth %d", inner, j.suspendDepth)
		}
		return nil
	}})
	if err != nil || j == nil || j.suspendDepth != 0 {
		t.Fatalf("ApplyDiff: %v, depth after %v", err, j)
	}
}

// TestS4GuardMergeDiffCallback is G1's MergeDiff variant: a MergeDiff callback that moves the
// diff's top level into another tree is refused with both trees unchanged; each tree is then
// mutated independently.
func TestS4GuardMergeDiffCallback(t *testing.T) {
	set := reviewDiffSet(t, s4JournalModule)
	d1, err := Diff(s4DataTree(t, set, "3", "7"), s4DataTree(t, set, "3", "9"), DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d2, err := Diff(s4DataTree(t, set, "3", "9"), s4DataTree(t, set, "3", "9", "11"), DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	other := newTree(set)
	refused := false
	err = d1.MergeDiff(d2, MergeDiffOptions{Callback: func(_, _ *Node) error {
		if refused {
			return nil
		}
		snapD, snapO := snapshotHashBudgetTree(d1), snapshotHashBudgetTree(other)
		refused = s4Refused(other.Insert(nil, d1.top.first()))
		if !reflect.DeepEqual(snapshotHashBudgetTree(d1), snapD) || !reflect.DeepEqual(snapshotHashBudgetTree(other), snapO) {
			t.Error("the refused move changed a tree")
		}
		return nil
	}})
	if err != nil || !refused {
		t.Fatalf("MergeDiff: %v, refused %t", err, refused)
	}
	if _, err := other.NewTerm(nil, "s4j:t", "y", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	if d1.work.journal != nil || other.work.journal != nil {
		t.Fatal("a state outlived MergeDiff")
	}
}

// TestS4IndexEditErrorsPropagate pins design 13 §4.5: the index edits that swallowed a refused
// touch (orderBuild, opOrderAdd, opRemove, refreshRB) return it and make it the counter's sticky
// error, before writing; refreshExact's skipped change stays sticky too.
func TestS4IndexEditErrorsPropagate(t *testing.T) {
	set := reviewDiffSet(t, s4JournalModule)
	tr, op := s4OpTree(t, set, "5", "1", "9")
	s := &op.kids
	n := s4Child(op, "w", "9")
	before := snapshotHashBudgetTree(tr)
	edits := map[string]func() error{
		"orderBuild": func() error { return s.orderBuild(tr) },
		"opOrderAdd": func() error { return s.opOrderAdd(tr, newTerm(n.schema, n.value, &tr.work), 0) },
		"opRemove": func() error {
			_, _, err := s.opRemove(n, 0)
			return err
		},
		"refreshRB": func() error { return s.refreshRB(n) },
		"refreshExact": func() error {
			s.refreshExact(n, func() { t.Error("refreshExact ran its change after a refusal") })
			return tr.work.hashErr
		},
	}
	for name, edit := range edits {
		done := tr.work.beginHashWork()
		j := beginHashTransaction(tr)
		j.suspendDepth++ // the operation waits on user code; the edit below is another one's
		err := edit()
		sticky := tr.work.hashErr
		j.suspendDepth--
		j.end(nil)
		done()
		if !errors.Is(err, errInProgress) || !errors.Is(sticky, errInProgress) {
			t.Fatalf("%s: %v (sticky %v), want the guard's refusal", name, err, sticky)
		}
		if !reflect.DeepEqual(snapshotHashBudgetTree(tr), before) {
			t.Fatalf("%s wrote before refusing", name)
		}
	}
}

// TestS4LinkGuardRefusalScaling: the link guard of an unlink charges each counterpart before it
// visits it, so a removal refused at its first units does the same work and allocations with 8
// or 4,096 leafref counterparts (snapshot review 1, finding 5).
func TestS4LinkGuardRefusalScaling(t *testing.T) {
	s4SkipUnderRace(t)
	set := reviewDiffSet(t, s4JournalModule)
	measure := func(degree int, limit uint64) (int64, uint64) {
		tr := s4DataTree(t, set, "3")
		c := tr.top.first()
		target := s4Child(c, "v", "m")
		for i := range degree {
			ref, err := tr.NewTerm(c, "u", fmt.Sprint("r", i), NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := linkLeafrefNode(target, ref); err != nil {
				t.Fatal(err)
			}
		}
		tr.work.Store(0)
		tr.work.visits = true
		tr.work.hashLimit = limit
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		err := target.Unlink() // prepareUnlink's link guard runs before its first charge
		runtime.ReadMemStats(&after)
		if !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("degree %d limit %d: %v, want a refusal", degree, limit, err)
		}
		return tr.work.Load(), after.Mallocs - before.Mallocs
	}
	for limit := uint64(1); limit <= 6; limit++ {
		if raceEnabled && !fzFull && limit > 1 {
			break // swept in the normal suite
		}
		smallWork, smallAllocs := measure(8, limit)
		largeWork, largeAllocs := measure(4096, limit)
		if largeWork > smallWork+16 || largeAllocs > smallAllocs+16 {
			t.Fatalf("limit %d: refusal work %d -> %d, allocations %d -> %d", limit, smallWork, largeWork, smallAllocs, largeAllocs)
		}
	}
}

// TestS4CounterpartCaptureRefusalScaling: a journaled capture of a node with leafref
// counterparts charges them before its suspension scan visits them and before it copies them,
// so a capture refused at any limit the 8-counterpart capture needs does the same work and
// allocations with 4,096 counterparts (snapshot review 2, finding 3).
func TestS4CounterpartCaptureRefusalScaling(t *testing.T) {
	s4SkipUnderRace(t)
	set := reviewDiffSet(t, s4JournalModule)
	build := func(degree int) (*Tree, *siblings) {
		a := s4DataTree(t, set, "3")
		b := s4DataTree(t, set, "3")
		target := s4Child(a.top.first(), "v", "m")
		for i := range degree {
			ref, err := b.NewTerm(b.top.first(), "u", fmt.Sprint("r", i), NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := linkLeafrefNode(target, ref); err != nil {
				t.Fatal(err)
			}
		}
		return a, &a.top.first().kids
	}
	capture := func(degree int, limit uint64) (int64, uint64, error) {
		a, s := build(degree)
		done := a.work.beginHashWork()
		defer done()
		j := beginHashTransaction(a)
		a.work.Store(0)
		a.work.visits = true
		a.work.hashLimit = limit
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		err := s.touch()
		runtime.ReadMemStats(&after)
		a.work.hashLimit = 0
		j.end(nil)
		return a.work.Load(), after.Mallocs - before.Mallocs, err
	}
	a, s := build(8)
	done := a.work.beginHashWork()
	j := beginHashTransaction(a)
	if err := s.touch(); err != nil {
		t.Fatal(err)
	}
	need := a.work.hashUsed
	j.end(nil)
	done()
	for limit := uint64(1); limit < need; limit++ {
		smallWork, smallAllocs, err := capture(8, limit)
		if err == nil {
			continue
		}
		if raceEnabled && !fzFull && limit > 1 {
			break // swept in the normal suite
		}
		largeWork, largeAllocs, err := capture(4096, limit)
		if !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if largeWork > smallWork+16 || largeAllocs > smallAllocs+16 {
			t.Fatalf("limit %d: refusal work %d -> %d, allocations %d -> %d", limit, smallWork, largeWork, smallAllocs, largeAllocs)
		}
	}
}

// TestS4GuardRefusedValidationRestores: B.Validate from a callback of A's ApplyDiff (B holds a
// leafref linked into A) changes B's default flags, then reaches its linked leafref, governed by
// A's suspended state: the refusal restores B completely instead of committing the earlier flag
// change as an ordinary validation failure (PR #327 review).
func TestS4GuardRefusedValidationRestores(t *testing.T) {
	set := s4LinksSet(t)
	for _, diff := range []bool{false, true} {
		a, b, _, _ := s4LinkedPair(t, set)
		var refused error
		var before, after hashBudgetSnapshot
		called := false
		err := a.ApplyDiff(s4Diff(t, set, a, false), ApplyDiffOptions{Callback: func(_, _ *Node) error {
			if called {
				return nil
			}
			called = true
			before = snapshotHashBudgetTree(b)
			if diff {
				_, _, refused = b.ValidateDiff(context.Background(), ValidateOptions{})
			} else {
				_, refused = b.Validate(context.Background(), ValidateOptions{})
			}
			after = snapshotHashBudgetTree(b)
			return nil
		}})
		if err != nil || !called {
			t.Fatalf("diff=%t: ApplyDiff %v, called %t", diff, err, called)
		}
		if !s4Refused(refused) {
			t.Fatalf("diff=%t: validation from the callback: %v, want the guard's refusal", diff, refused)
		}
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("diff=%t: the refused validation kept changes to B", diff)
		}
		if b.work.journal != nil {
			t.Fatalf("diff=%t: B's journal stayed installed", diff)
		}
		s4Check(t, "after", []*Tree{a, b})
	}
}

// TestS4GuardNestedRefusalLeavesOuterOperation: every public mutator of the tree an ApplyDiff is
// changing, called from its callback, is refused at entry, before it charges or sets the shared
// counter's sticky error; the callback catches the refusal and returns nil, and ApplyDiff then
// succeeds with exactly the reference result (PR #327 review, round 2).
func TestS4GuardNestedRefusalLeavesOuterOperation(t *testing.T) {
	set := s4LinksSet(t)
	ref, _, _, _ := s4LinkedPair(t, set)
	if err := ref.ApplyDiff(s4Diff(t, set, ref, false), ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	want := typedDump(ref)
	type call func(a *Tree, c, leaf *Node) ([]yang.Diagnostic, error)
	noDiags := func(f func(a *Tree, c, leaf *Node) error) call {
		return func(a *Tree, c, leaf *Node) ([]yang.Diagnostic, error) { return nil, f(a, c, leaf) }
	}
	mutators := map[string]call{
		"SetValue": noDiags(func(_ *Tree, _, leaf *Node) error { _, err := leaf.SetValue("b"); return err }),
		"SetValueCanonical": noDiags(func(_ *Tree, _, leaf *Node) error {
			_, err := leaf.SetValueCanonical("b")
			return err
		}),
		"NewTerm":  noDiags(func(a *Tree, c, _ *Node) error { _, err := a.NewTerm(c, "t", "n", NewOptions{}); return err }),
		"NewInner": noDiags(func(a *Tree, _, _ *Node) error { _, err := a.NewInner(nil, "pv2-links:c", NewOptions{}); return err }),
		"NewList":  noDiags(func(a *Tree, c, _ *Node) error { _, err := a.NewList(c, "l", []string{"n"}, NewOptions{}); return err }),
		"NewList2": noDiags(func(a *Tree, c, _ *Node) error { _, err := a.NewList2(c, "l", "[k='n']", NewOptions{}); return err }),
		"NewOpaque": noDiags(func(a *Tree, c, _ *Node) error {
			_, err := a.NewOpaque(c, "o", "v", OpaqueOptions{})
			return err
		}),
		"NewPath": noDiags(func(a *Tree, _, _ *Node) error {
			_, err := a.NewPath("/pv2-links:c/t[.='n']", "", NewPathOptions{})
			return err
		}),
		"NewMeta": noDiags(func(a *Tree, _, leaf *Node) error { _, err := a.NewMeta(leaf, "x:y", "z"); return err }),
		"Insert":  noDiags(func(a *Tree, c, leaf *Node) error { return a.Insert(c, leaf) }),
		"Remove":  noDiags(func(_ *Tree, _, leaf *Node) error { return leaf.Remove() }),
		"Unlink":  noDiags(func(_ *Tree, _, leaf *Node) error { return leaf.Unlink() }),
		"Merge": noDiags(func(a *Tree, _, _ *Node) error {
			return a.Merge(s4ParseLinks(t, set, `{"pv2-links:c":{"t":["m"]}}`))
		}),
		"ApplyDiff": noDiags(func(a *Tree, _, _ *Node) error { return a.ApplyDiff(s4Diff(t, set, a, false), ApplyDiffOptions{}) }),
		"MergeDiff": noDiags(func(a *Tree, _, _ *Node) error { return a.MergeDiff(s4Diff(t, set, a, false), MergeDiffOptions{}) }),
		"MergeDiffTree": noDiags(func(a *Tree, _, _ *Node) error {
			return a.MergeDiffTree(nil, s4Diff(t, set, a, false).top.first(), MergeDiffOptions{})
		}),
		"Dup": func(a *Tree, c, leaf *Node) ([]yang.Diagnostic, error) {
			_, d, err := a.Dup(leaf, c, DupOptions{})
			return d, err
		},
		"DupSiblings": func(a *Tree, c, leaf *Node) ([]yang.Diagnostic, error) {
			_, d, err := a.DupSiblings(leaf, c, DupOptions{})
			return d, err
		},
		"TrimXPath": func(a *Tree, _, _ *Node) ([]yang.Diagnostic, error) {
			return a.TrimXPath("/pv2-links:c/t", XPathOptions{})
		},
		"Validate": func(a *Tree, _, _ *Node) ([]yang.Diagnostic, error) {
			return a.Validate(context.Background(), ValidateOptions{})
		},
		"ValidateModule": func(a *Tree, _, _ *Node) ([]yang.Diagnostic, error) {
			return a.ValidateModule(context.Background(), "pv2-links", ValidateOptions{})
		},
		"ValidateModuleFinal": func(a *Tree, _, _ *Node) ([]yang.Diagnostic, error) {
			return a.ValidateModuleFinal(context.Background(), "pv2-links", ValidateOptions{})
		},
		"ValidateDiff": func(a *Tree, _, _ *Node) ([]yang.Diagnostic, error) {
			_, d, err := a.ValidateDiff(context.Background(), ValidateOptions{})
			return d, err
		},
		"ValidateModuleDiff": func(a *Tree, _, _ *Node) ([]yang.Diagnostic, error) {
			_, d, err := a.ValidateModuleDiff(context.Background(), "pv2-links", ValidateOptions{})
			return d, err
		},
		"ValidateOp": func(a *Tree, _, _ *Node) ([]yang.Diagnostic, error) {
			return a.ValidateOp(context.Background(), OpRPC, ValidateOpOptions{})
		},
		"LinkLeafrefs": func(a *Tree, _, _ *Node) ([]yang.Diagnostic, error) {
			return a.LinkLeafrefs(context.Background())
		},
	}
	returnsDiags := map[string]bool{"Dup": true, "DupSiblings": true, "TrimXPath": true, "Validate": true,
		"ValidateModule": true, "ValidateModuleFinal": true, "ValidateDiff": true, "ValidateModuleDiff": true,
		"ValidateOp": true, "LinkLeafrefs": true}
	for name, mutate := range mutators {
		t.Run(name, func(t *testing.T) {
			a, _, _, _ := s4LinkedPair(t, set)
			called := false
			err := a.ApplyDiff(s4Diff(t, set, a, false), ApplyDiffOptions{Callback: func(_, _ *Node) error {
				if called {
					return nil
				}
				called = true
				c := a.top.first()
				leaf := s4Child(c, "t", "y")
				used := a.work.hashUsed
				diags, err := mutate(a, c, leaf)
				s4PublicRefusal(t, name, diags, err, returnsDiags[name])
				if a.work.hashErr != nil || a.work.hashUsed != used {
					t.Errorf("%s: the refusal leaked into the operation's counter (%v, %d units)", name, a.work.hashErr, a.work.hashUsed-used)
				}
				return nil // the refusal is caught: the callback succeeds
			}})
			if err != nil || !called {
				t.Fatalf("ApplyDiff: %v (called %t), want success", err, called)
			}
			if got := typedDump(a); !reflect.DeepEqual(got, want) {
				t.Fatalf("history differs from the reference:\n%v\nwant\n%v", got, want)
			}
		})
	}
}

// s4PublicRefusal asserts the public form of the guard's refusal: a *ValidationError whose RC is
// LY_EINVAL and whose diagnostics carry the message, and the same diagnostic in the returned
// slice of a call that returns one (wantDiags).
func s4PublicRefusal(t *testing.T, name string, diags []yang.Diagnostic, err error, wantDiags bool) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" {
		t.Errorf("%s: %T %v, want a *ValidationError with RC LY_EINVAL", name, err, err)
		return
	}
	found := false
	for _, d := range ve.Diags {
		found = found || strings.Contains(d.Msg, errInProgress.Msg)
	}
	if !found {
		t.Errorf("%s: no diagnostic names the refusal: %v", name, ve.Diags)
	}
	if wantDiags && (len(diags) == 0 || !strings.Contains(diags[len(diags)-1].Msg, errInProgress.Msg)) {
		t.Errorf("%s: the returned diagnostics %v miss the refusal", name, diags)
	}
}
