// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"reflect"
	"testing"

	"github.com/vibe-ports/yang"
)

// #240 step 4 (design 13 §4, G9): the prepaid scopes of link, the single unlink and the
// in-place value change. Every sweep of the package runs with overdraftPanics on, so a bound
// missing a term of its suffix panics instead of debiting silently.

// TestS4PrepaidSuffixPreconditions pins what G9a needs from the S4 histories: their operation
// children carry both the children table and the ordered index, so op/insert-term, op/remove
// and op/set-value-in-place sweep every limit of the three scopes over such a set.
func TestS4PrepaidSuffixPreconditions(t *testing.T) {
	if !overdraftPanics {
		t.Fatal("test builds must panic on an overdrawn credit")
	}
	set := reviewDiffSet(t, s4JournalModule)
	_, op := s4OpTree(t, set, "5", "1", "9")
	if op.kids.ht == nil || op.kids.order == nil {
		t.Fatalf("operation children: table %t, index %t", op.kids.ht != nil, op.kids.order != nil)
	}
}

// TestS4PrepaidOverdraftProductionPolicy is G9b: with the production policy (no panic) and
// link's bounds under-reserved by prepaySkew (its apply bound and its index term), a multi-step operation (two insertions into
// an operation's indexed children under one recovery state and quota scope, the set captured up
// front) completes its first step over the limit; the second step refuses at its first check,
// and the state restores both. With the panic switch on, the same run panics in the first step.
// A separate operation afterwards starts from a reset quota and charges what it charges on a
// fresh tree.
func TestS4PrepaidOverdraftProductionPolicy(t *testing.T) {
	s4SkipUnderRace(t)
	set := reviewDiffSet(t, s4JournalModule)
	run := func(tr *Tree, op *Node) (err error) {
		j := beginHashTransaction(tr)
		defer j.end(&err)
		if err := op.kids.touch(); err != nil { // no capture can refuse inside the steps
			return err
		}
		if _, err := tr.NewTerm(op, "w", "4", NewOptions{}); err != nil {
			return err
		}
		_, err = tr.NewTerm(op, "w", "6", NewOptions{})
		return err
	}
	defer func(panics bool) { overdraftPanics, prepaySkew, overdraftHook = panics, 0, nil }(overdraftPanics)
	overdraftPanics, prepaySkew = false, 1<<20
	tr, op := s4OpTree(t, set, "5", "1", "9")
	if err := run(tr, op); err != nil {
		t.Fatal(err)
	}
	need := tr.work.hashUsed
	fresh, fop := s4OpTree(t, set, "5", "1", "9")
	if _, err := fresh.NewTerm(fop, "w", "3", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	separate := fresh.work.hashUsed
	exceeded := 0
	for limit := uint64(1); limit < need; limit++ { // every limit, under race too: few, and the overdraft is at some
		tr, op := s4OpTree(t, set, "5", "1", "9")
		before := snapshotHashBudgetTree(tr)
		over := false
		overdraftHook = func(w *workCounter) {
			if w == &tr.work && w.hashUsed > limit {
				over = true
			}
		}
		tr.work.hashLimit = limit
		err := run(tr, op)
		overdraftHook = nil
		tr.work.hashLimit = 0
		if err == nil {
			continue
		}
		if !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if !reflect.DeepEqual(snapshotHashBudgetTree(tr), before) {
			t.Fatalf("limit %d: the refused operation changed the tree", limit)
		}
		s4Check(t, "after refusal", []*Tree{tr})
		if over {
			exceeded++
		}
		if _, err := tr.NewTerm(op, "w", "3", NewOptions{}); err != nil {
			t.Fatalf("limit %d: an operation after the refusal: %v", limit, err)
		}
		if tr.work.hashUsed != separate {
			t.Fatalf("limit %d: the next operation charged %d, on a fresh tree %d", limit, tr.work.hashUsed, separate)
		}
		s4Check(t, "separate operation", []*Tree{tr})
	}
	if exceeded == 0 {
		t.Fatal("no refused operation went over its limit through an overdraft first")
	}
	tr, op = s4OpTree(t, set, "5", "1", "9")
	overdraftPanics = true
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("an overdraft did not panic under the test switch")
			}
		}()
		_ = run(tr, op)
	}()
}

// TestS4PrepaidValueChangeAncestorReindex is G9c: an in-place key change of a list instance
// whose parent set has a children table reindexes the instance there; every limit refuses as a
// no-op (the suffix is prepaid, overdraftPanics on), and a further mutation matches the
// reference.
func TestS4PrepaidValueChangeAncestorReindex(t *testing.T) {
	s4SkipUnderRace(t)
	set := reviewDiffSet(t, s4JournalModule)
	s4History(t, s4Scenario{name: "data/set-key-in-place", limited: []int{0},
		build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			tr := s4DataTree(t, set, "3", "7", "11", "15", "19")
			if tr.top.first().kids.ht == nil {
				t.Fatal("setup: the list's parent set has no children table")
			}
			key := s4Child(tr.top.first(), "l", "7").kids.first()
			return []*Tree{tr}, func() error {
					_, err := key.SetValue("8") // stays between 3 and 11: in place
					return err
				}, func() error {
					_, err := tr.NewList(tr.top.first(), "l", []string{"9"}, NewOptions{})
					return err
				}
		}}, 0)
}

// TestS4PrepaidCumulativeEnforcement is G4: several index edits in one quota scope, each within
// its own bound but together over the quota. The edit that exceeds the remainder refuses before
// any write: the tree right after it equals the tree right before it (the state, still open,
// has not restored anything yet).
func TestS4PrepaidCumulativeEnforcement(t *testing.T) {
	s4SkipUnderRace(t)
	set := reviewDiffSet(t, s4JournalModule)
	values := []string{"2", "4", "6", "8", "10", "12"}
	run := func(tr *Tree, op *Node, check func(i int, before hashBudgetSnapshot, err error)) (err error) {
		j := beginHashTransaction(tr)
		defer j.end(&err)
		for i, v := range values {
			before := snapshotHashBudgetTree(tr)
			_, err := tr.NewTerm(op, "w", v, NewOptions{})
			if check != nil {
				check(i, before, err)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	tr, op := s4OpTree(t, set, "5", "1", "9")
	if err := run(tr, op, nil); err != nil {
		t.Fatal(err)
	}
	need := tr.work.hashUsed
	refusedLater := 0
	for limit := uint64(1); limit < need; limit++ {
		if !s4RaceLimit(limit, need) {
			continue
		}
		tr, op := s4OpTree(t, set, "5", "1", "9")
		start := snapshotHashBudgetTree(tr)
		tr.work.hashLimit = limit
		err := run(tr, op, func(i int, before hashBudgetSnapshot, err error) {
			if err == nil {
				return
			}
			if !errors.Is(err, yang.ErrBudget) {
				t.Fatalf("limit %d edit %d: %v", limit, i, err)
			}
			if !reflect.DeepEqual(snapshotHashBudgetTree(tr), before) {
				t.Fatalf("limit %d: the refused edit %d wrote before refusing", limit, i)
			}
			if i > 0 {
				refusedLater++
			}
		})
		tr.work.hashLimit = 0
		if err == nil {
			continue
		}
		if !reflect.DeepEqual(snapshotHashBudgetTree(tr), start) {
			t.Fatalf("limit %d: the state did not restore the earlier edits", limit)
		}
		s4Check(t, "cumulative", []*Tree{tr})
	}
	if refusedLater == 0 {
		t.Fatal("no limit refused a later edit of the scope")
	}
}
