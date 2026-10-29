// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/vibe-ports/yang"
)

// splitRunSeparator copies [u × n, sep, u × n] from a user-ordered source into a system-ordered
// tree and gives the first run an RB tree, so removing sep joins a run whose leader unlink marks
// unsorted (design 14).
func splitRunSeparator(t *testing.T, n int) (*Tree, *Node) {
	t.Helper()
	src := newTree(opIndexSet(t, fmt.Sprintf(splitRunModelSchema, "ordered-by user;")))
	root, err := src.NewInner(nil, "m:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2*n + 1 {
		name, value := "u", fmt.Sprint(2*i+2)
		if i == n {
			name, value = "sep", "x"
		}
		if err := src.link(root, &root.kids, modelFreshTerm(t, root, name, value), root.kids.SchemaLen()); err != nil {
			t.Fatal(err)
		}
	}
	dst := newTree(opIndexSet(t, fmt.Sprintf(splitRunModelSchema, "")))
	copyRoot, _, err := dst.Dup(root, nil, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dst.NewTerm(copyRoot, "u", "1", NewOptions{}); err != nil { // the first tree
		t.Fatal(err)
	}
	for _, c := range copyRoot.kids.SchemaAll() {
		if c.Name() == "sep" {
			return dst, c
		}
	}
	t.Fatal("separator missing")
	return nil, nil
}

// Removing a separator finds the left run's leader by a charged walk back from its predecessor
// in the unlink preflight: the work is linear in the run, every step is charged, and a budget
// one unit short refuses before anything changes.
func TestUnlinkSeparatorLeftRunCharged(t *testing.T) {
	for _, n := range scaleSizes([]int{1024, 8192}, 1) {
		tr, sep := splitRunSeparator(t, n)
		tr.work.visits = true
		tr.work.Store(0)
		if err := sep.Remove(); err != nil {
			t.Fatal(err)
		}
		work, used := tr.work.Load(), tr.work.hashUsed
		t.Logf("n=%d: separator removal work %d, charged %d", n, work, used)
		if work > 4*int64(n) {
			t.Errorf("n=%d: separator removal work %d exceeds 4 per left-run member", n, work)
		}
		if used < uint64(n) { //nolint:gosec // a test size is nonnegative
			t.Errorf("n=%d: separator removal charged %d for a left run of %d", n, used, n)
		}
		opidxAssertTreeInvariant(t, tr)

		tr, sep = splitRunSeparator(t, n)
		before := snapshotHashBudgetTree(tr)
		tr.work.hashLimit = used - 1
		if err := sep.Remove(); !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("n=%d: separator removal with budget %d = %v, want ErrBudget", n, used-1, err)
		}
		if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(after, before) {
			t.Fatalf("n=%d: refused separator removal changed the tree", n)
		}
		opidxAssertTreeInvariant(t, tr)
	}
}

// exposedKeyList copies a list instance [a, b] from a schema keyed "a b" into one keyed "b" and
// links n more b after it: removing the non-key a exposes a repeated-key prefix of n+1.
func exposedKeyList(t *testing.T, n int) (*Tree, *Node) {
	t.Helper()
	src := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  container r { list l { key "a b"; leaf a { type string; } leaf b { type string; } } } }`)
	st, _, err := parseJSONString(src, `{"m:r":{"l":[{"a":"x","b":"y"}]}}`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	tr := newTree(opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  container r { list l { key "b"; leaf a { type string; } leaf b { type string; } } } }`))
	root, _, err := tr.Dup(st.top.first(), nil, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	list := root.kids.At(0)
	for i := range n {
		if err := tr.link(list, &list.kids, modelFreshTerm(t, list, "b", fmt.Sprint(i)), list.kids.SchemaLen()); err != nil {
			t.Fatal(err)
		}
	}
	if a := list.kids.At(0); a.Name() != "a" || list.keyN != 0 {
		t.Fatalf("fixture: first child %s, keyN %d", a.Name(), list.keyN)
	}
	return tr, list
}

// Removing a non-key in front of a repeated-key prefix exposes the whole prefix (the declared
// key count does not bound it): the walk follows the threads, is linear, is reserved by the
// unlink preflight, and a budget one unit short refuses before anything changes.
func TestUnlinkExposesRepeatedKeysCharged(t *testing.T) {
	for _, n := range scaleSizes([]int{1024, 8192}, 1) {
		tr, list := exposedKeyList(t, n)
		tr.work.visits = true
		tr.work.Store(0)
		if err := list.kids.At(0).Remove(); err != nil {
			t.Fatal(err)
		}
		work, used := tr.work.Load(), tr.work.hashUsed
		t.Logf("n=%d: key exposure work %d, charged %d", n, work, used)
		if list.keyN != n+1 {
			t.Fatalf("n=%d: exposed %d keys, want %d", n, list.keyN, n+1)
		}
		if work > 4*int64(n) {
			t.Errorf("n=%d: key exposure work %d exceeds 4 per exposed key", n, work)
		}
		if used < uint64(n) { //nolint:gosec // a test size is nonnegative
			t.Errorf("n=%d: key exposure charged %d for %d exposed keys", n, used, n)
		}
		opidxAssertTreeInvariant(t, tr)

		tr, list = exposedKeyList(t, n)
		before := snapshotHashBudgetTree(tr)
		tr.work.hashLimit = used - 1
		if err := list.kids.At(0).Remove(); !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("n=%d: key exposure with budget %d = %v, want ErrBudget", n, used-1, err)
		}
		if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(after, before) {
			t.Fatalf("n=%d: refused key exposure changed the tree", n)
		}
		opidxAssertTreeInvariant(t, tr)
	}
}
