// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

const relocationModule = `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  container c { presence p; leaf-list v { type uint32; ordered-by user; } } }`

// relocationTrees parses c/v = [1..n] and a diff that moves each of 2..n before the first
// instance (yang:value=""), one replace entry per value: the consult's step-6 reproducer.
func relocationTrees(t *testing.T, set *schema.Set, n int) (*Tree, *Tree) {
	t.Helper()
	var data, diff strings.Builder
	data.WriteString(`<c xmlns="urn:m">`)
	diff.WriteString(`<c xmlns="urn:m" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none">`)
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&data, "<v>%d</v>", i)
		if i > 1 {
			fmt.Fprintf(&diff, `<v yang:operation="replace" yang:orig-value="%d" yang:value="">%d</v>`, i-1, i)
		}
	}
	data.WriteString(`</c>`)
	diff.WriteString(`</c>`)
	return followupsDiff(t, set, data.String()), followupsDiff(t, set, diff.String())
}

// TestApplyDiffRepeatedMoveFirstScaling (design 14, step 6): N moves before the first instance
// in one ApplyDiff capture the sibling set once, not once per move; the work and the charge per
// entry stay flat as N grows, and the moves land in libyang's order.
func TestApplyDiffRepeatedMoveFirstScaling(t *testing.T) {
	set := reviewDiffSet(t, relocationModule)
	type sample struct {
		work, used, captures int64
	}
	measure := func(n int) sample {
		tr, diff := relocationTrees(t, set, n)
		captures := int64(0)
		captureHook = func(*siblings) { captures++ }
		defer func() { captureHook = nil }()
		tr.work.visits = true
		tr.work.Store(0)
		tr.work.hashUsed = 0
		if err := tr.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
			t.Fatal(err)
		}
		c := tr.top.first()
		got := make([]string, 0, n)
		for v := range c.kids.all() {
			got = append(got, v.value.Canonical())
		}
		for i, v := range got {
			if want := fmt.Sprint(n - i); v != want {
				t.Fatalf("n=%d: position %d holds %s, want %s", n, i, v, want)
			}
		}
		opidxAssertTreeInvariant(t, tr)
		return sample{tr.work.Load(), int64(tr.work.hashUsed), captures} //nolint:gosec // a test budget
	}
	sizes := scaleSizes([]int{512, 4096}, 1)
	if len(sizes) == 1 {
		sizes = []int{256, 2048}
	}
	a, b := measure(sizes[0]), measure(sizes[1])
	t.Logf("n=%d: work %d, charged %d, captures %d; n=%d: work %d, charged %d, captures %d",
		sizes[0], a.work, a.used, a.captures, sizes[1], b.work, b.used, b.captures)
	ratio := int64(sizes[1] / sizes[0])
	// per entry: within 1.5x (the point edits are logarithmic)
	if 2*b.work > 3*ratio*a.work || 2*b.used > 3*ratio*a.used {
		t.Fatalf("per-entry work or charge grew with N: %+v -> %+v", a, b)
	}
	if b.captures > 4 {
		t.Fatalf("n=%d: %d whole sibling captures, want a constant", sizes[1], b.captures)
	}
}

// TestApplyDiffRepeatedMoveFirstRollback: a budget refusal anywhere in the repeated moves restores
// the tree exactly; an ordinary failure of a later entry keeps the moves before it, as libyang's
// lyd_diff_apply does.
func TestApplyDiffRepeatedMoveFirstRollback(t *testing.T) {
	set := reviewDiffSet(t, relocationModule)
	const n = 24
	tr, diff := relocationTrees(t, set, n)
	tr.work.hashUsed = 0
	if err := tr.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	need := tr.work.hashUsed
	refused := 0
	for limit := uint64(1); limit < need; limit++ {
		if !s4RaceLimit(limit, need) {
			continue
		}
		tr, diff := relocationTrees(t, set, n)
		before := snapshotHashBudgetTree(tr)
		tr.work.hashUsed, tr.work.hashLimit = 0, limit
		err := tr.ApplyDiff(diff, ApplyDiffOptions{})
		tr.work.hashLimit = 0
		if err == nil {
			continue
		}
		if !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("limit %d: %v", limit, err)
		}
		refused++
		if !reflect.DeepEqual(snapshotHashBudgetTree(tr), before) {
			t.Fatalf("limit %d: the refused diff changed the tree", limit)
		}
		opidxAssertTreeInvariant(t, tr)
	}
	if refused == 0 {
		t.Fatal("no limit refused the diff")
	}

	// an anchor that does not exist fails the 9th entry: the 8 moves before it stay
	tr, _ = relocationTrees(t, set, n)
	var bad strings.Builder
	bad.WriteString(`<c xmlns="urn:m" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none">`)
	for i := 2; i <= 9; i++ {
		fmt.Fprintf(&bad, `<v yang:operation="replace" yang:orig-value="%d" yang:value="">%d</v>`, i-1, i)
	}
	bad.WriteString(`<v yang:operation="replace" yang:orig-value="9" yang:value="999">10</v></c>`)
	if err := tr.ApplyDiff(followupsDiff(t, set, bad.String()), ApplyDiffOptions{}); err == nil {
		t.Fatal("a missing anchor applied")
	}
	var got []string
	for v := range tr.top.first().kids.all() {
		got = append(got, v.value.Canonical())
	}
	want := []string{"9", "8", "7", "6", "5", "4", "3", "2", "1"}
	for i := 10; i <= n; i++ {
		want = append(want, fmt.Sprint(i))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after the failed entry: %v, want %v", got, want)
	}
	opidxAssertTreeInvariant(t, tr)
}

// TestRelocationForwardRecoveryInState: inside a state whose current level holds the set, a
// relocation opens no level; a failure after its unlink rolls forward (the node returns to its
// place, the moves before it stay), and the state's rollback still restores the tree exactly.
// The fixtures: a plain set, an indexed one (an operation's children: the recovery relinks with
// a fresh handle), a separator between two runs whose removal marks the left run unsorted, and a
// list's non-key in front of its keys (it keeps its level: the unlink exposes the keys).
func TestRelocationForwardRecoveryInState(t *testing.T) {
	set := reviewDiffSet(t, relocationModule)
	opSet := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	  rpc r { input { leaf-list v { type uint32; ordered-by user; } } } }`)
	byValue := func(t *testing.T, c *Node, v string) *Node {
		for n := range c.kids.all() {
			if n.value.Canonical() == v {
				return n
			}
		}
		t.Fatalf("no value %s", v)
		return nil
	}
	// moveTwo moves 2 before the first instance: the state's current level captures the set
	moveTwo := func(tr *Tree, c *Node) func(*testing.T) {
		return func(t *testing.T) {
			if err := tr.diffInsert(c, byValue(t, c, "2"), byValue(t, c, "2"), ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, commit := range []bool{false, true} {
		tr, _ := relocationTrees(t, set, 16)
		c := tr.top.first()
		forwardRecoveryInState(t, tr, moveTwo(tr, c), func() *Node { return byValue(t, c, "3") }, commit)
		tr, op := opIndexParseRPC(t, opSet, `{"m:r":{"v":[`+opIndexValues(16, false)+`]}}`)
		if op.kids.order == nil {
			t.Fatal("fixture: the operation's children are not indexed")
		}
		forwardRecoveryInState(t, tr, moveTwo(tr, op), func() *Node { return byValue(t, op, "3") }, commit)
		tr, sep := splitRunSeparator(t, 8)
		if p, err := prepareUnlink(sep); err != nil || p == nil || p.left == nil {
			t.Fatalf("fixture: the separator joins no RB run (%v)", err)
		}
		capture := func(t *testing.T) {
			if err := sep.siblingsOf().touch(); err != nil {
				t.Fatal(err)
			}
		}
		forwardRecoveryInState(t, tr, capture, func() *Node { return sep }, commit)
		// a list instance's child: removing the non-key exposes the keys, so it keeps a level
		tr, list := exposedKeyList(t, 0)
		capture = func(t *testing.T) {
			if err := list.kids.touch(); err != nil {
				t.Fatal(err)
			}
		}
		forwardRecoveryInState(t, tr, capture, func() *Node { return list.kids.At(0) }, commit)
	}
}

// forwardRecoveryInState opens a state, runs prepare (which must capture the set), relocates
// pick() within its set with a failure injected after the unlink, then ends the state.
func forwardRecoveryInState(t *testing.T, tr *Tree, prepare func(*testing.T), pick func() *Node, commit bool) {
	t.Helper()
	original := snapshotHashBudgetTree(tr)
	j := beginHashTransaction(tr)
	prepare(t)
	n := pick()
	sib := n.siblingsOf()
	if !captured(j, j.siblings, sib) {
		t.Fatal("prepare left the set uncaptured")
	}
	moved := snapshotHashBudgetTree(tr)
	restore, arm, err := tr.linkOrigin(n)
	if err != nil {
		t.Fatal(err)
	}
	disarm, err := arm(sib)
	if err != nil {
		t.Fatal(err)
	}
	finish, err := tr.relocation(n, sib, restore)
	if err != nil {
		t.Fatal(err)
	}
	if list := n.parent != nil && n.parent.schema != nil && n.parent.schema.Kind == schema.List; len(j.levels) != 0 != list {
		t.Fatalf("a relocation in a held set opened %d levels (a list's child: %t)", len(j.levels), list)
	}
	if err := unlink(n); err != nil {
		t.Fatal(err)
	}
	failed := errors.New("injected failure after the unlink")
	finish(&failed)
	disarm()
	if !reflect.DeepEqual(snapshotHashBudgetTree(tr), moved) {
		t.Fatal("the forward recovery did not restore the set as prepare left it")
	}
	if commit {
		j.end(nil)
		if !reflect.DeepEqual(snapshotHashBudgetTree(tr), moved) {
			t.Fatal("the kept operation lost prepare's change")
		}
	} else {
		j.rollback = true
		outer := errors.New("outer failure")
		j.end(&outer)
		if !reflect.DeepEqual(snapshotHashBudgetTree(tr), original) {
			t.Fatal("the state's rollback did not restore the tree")
		}
	}
	opidxAssertTreeInvariant(t, tr)
}
