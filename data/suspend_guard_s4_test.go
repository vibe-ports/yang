// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"reflect"
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
