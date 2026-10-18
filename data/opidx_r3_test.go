// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// TestOpIndexSortRelocatesRecord replays ops/opidx-leader-sort-dup-ctx. The
// recursive cross-context copy has no RB tree and keeps the string order
// 10,2,3,4 after the values become uint32. Creating the RB tree for the next
// sorted insertion relocates 2,3,4; lyd_unlink_ignore_lyds followed by
// lyd_insert_hash makes 2 the first-instance record (tree_data_sorted.c:
// lyds_additionally_create_rb_nodes, tree_data_hash.c:lyd_insert_hash_add).
func TestOpIndexSortRelocatesRecord(t *testing.T) {
	fixture := readOpsFixture(t, filepath.Join(opsManifest, "opidx-leader-sort-dup-ctx.yaml"))
	set1 := opsSet(t, fixture)
	fixture2 := fixture
	fixture2.searchdirs = []string{"opidxs.2"}
	set2 := opsSet(t, fixture2)

	_, src, diags, err := parseOp(context.Background(), strings.NewReader(
		`{"opidxs:r":{"s":["10","2","3","4"]}}`), set1, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	op, err := dupTo(src, set2, nil, dupRecursive, false)
	if err != nil {
		t.Fatal(err)
	}
	tr := treeOf(op)
	mod := set2.Implemented("opidxs")
	b := newBuilder{set: set2, log: &logger{set: set2}}
	add := func(name, value string) *Node {
		t.Helper()
		n, err := b.newTerm(op, mod, name, value, newValOptions{})
		if err = b.log.done(err); err != nil {
			t.Fatal(err)
		}
		b.log = &logger{set: set2}
		return n
	}

	s11 := add("s", "11")
	ss := s11.schema
	if got, want := opidxRecords(op, ss), []string{"2"}; !slices.Equal(got, want) {
		t.Errorf("records after inserting 11 = %v, want %v", got, want)
	}
	if got := tr.schemaLookup(&op.kids, ss); got == nil || got.value.Canonical() != "2" {
		t.Errorf("first-instance lookup = %s, want v2", opidxLabel(got))
	}
	wantValues := []string{"2", "3", "4", "10", "11"}
	var gotValues []string
	for n := range tr.instances(&op.kids, ss) {
		gotValues = append(gotValues, n.value.Canonical())
	}
	if !slices.Equal(gotValues, wantValues) {
		t.Errorf("instance walk = %v, want %v", gotValues, wantValues)
	}
	gotValues = gotValues[:0]
	for _, n := range tr.opRun(&op.kids, ss) {
		gotValues = append(gotValues, n.value.Canonical())
	}
	if !slices.Equal(gotValues, wantValues) {
		t.Errorf("operation run = %v, want %v", gotValues, wantValues)
	}

	add("pre", "x")
	if got, want := opidxValues(op), []string{"pre", "s:2", "s:3", "s:4", "s:10", "s:11"}; !slices.Equal(got, want) {
		t.Errorf("children = %v, want %v", got, want)
	}
	opidxCompareGoldens(t, fixture, []int{4}, []opidxSnapshot{opidxSnapshotOf(t, tr)})
}

func opidxDupSetNodes(tr *Tree, op, target *Node) []*Node {
	cache := &dupCache{}
	var got []*Node
	for {
		n := tr.findMatch(&op.kids, target, true, cache)
		if n == nil {
			return got
		}
		got = append(got, n)
	}
}

// TestOpIndexSortRelocatesEqualValues covers the exact-value hash order while
// lyds_additionally_create_rb_nodes sorts a run that was linked without lyds.
// Both equal 1 instances are relocated in walk order. Each is unregistered and
// registered again, leaving the exact bucket and duplicate set in a,b order.
func TestOpIndexSortRelocatesEqualValues(t *testing.T) {
	fixture := readOpsFixture(t, filepath.Join(opsManifest, "opidx-leader-sort-dup-ctx.yaml"))
	fixture.searchdirs = []string{"opidxs.2"}
	set := opsSet(t, fixture)
	tr, op, diags, err := parseOp(context.Background(), strings.NewReader(
		`{"opidxs:r":{"pre":"keep"}}`), set, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	mod := set.Implemented("opidxs")
	ss := schema.FindChild(mod.Child(nil, "r"), nil, mod, "s", 0)
	if ss == nil {
		t.Fatal("input leaf-list s not compiled")
	}
	five := opIndexTerm(t, ss, 5)
	oneA := opIndexTerm(t, ss, 1)
	oneB := opIndexTerm(t, ss, 1)
	for _, n := range []*Node{five, oneA, oneB} {
		mustLink(t, tr.insert(op, n, insertLast))
	}
	if op.kids.hasRB(ss) {
		t.Fatal("insertLast run unexpectedly has an RB tree")
	}

	six := opIndexTerm(t, ss, 6)
	mustLink(t, tr.insert(op, six, insertDefault))
	wantEqual := []*Node{oneA, oneB}
	if got := op.kids.bucket(storedValueIndexKey(t, ss, "1")); !slices.Equal(got, wantEqual) {
		t.Errorf("exact-value bucket = %p, want a,b (%p)", got, wantEqual)
	}
	probe := opIndexTerm(t, ss, 1)
	if got := opidxDupSetNodes(tr, op, probe); !slices.Equal(got, wantEqual) {
		t.Errorf("duplicate set = %p, want a,b (%p)", got, wantEqual)
	}
	if got := opidxSchemaNodes(op, ss); !slices.Equal(got, []*Node{oneA, oneB, five, six}) {
		t.Errorf("sorted run values = %v, want [1 1 5 6]", opidxValues(op))
	}
}

// TestOpIndexUnlinkRegisteredSuccessor replays
// ops/opidx-leader-unlink-registered. Removing v1 tries to register v3 as its
// successor even though v3 already owns a first-instance record. The native
// golden records libyang's LY_EINT; D-0118 keeps the resulting record state but
// does not treat that internal-consistency report as a data verdict.
func TestOpIndexUnlinkRegisteredSuccessor(t *testing.T) {
	fixture := readOpsFixture(t, filepath.Join(opsManifest, "opidx-leader-unlink-registered.yaml"))
	set := opsSet(t, fixture)
	mod := set.Implemented("opidx239")
	tr, op, diags, err := parseOp(context.Background(), strings.NewReader(
		`{"opidx239:l":{"a":"1","a2":"2","v":[1,2]}}`), set, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	b := newBuilder{set: set, log: &logger{set: set}}
	add := func(name, value string, output bool) *Node {
		t.Helper()
		n, err := b.newTerm(op, mod, name, value, newValOptions{output: output})
		if err = b.log.done(err); err != nil {
			t.Fatal(err)
		}
		b.log = &logger{set: set}
		return n
	}
	add("o", "o", true)
	v3 := add("v", "3", false)
	vs := v3.schema
	values := opidxSchemaNodes(op, vs)
	v1, v2 := values[0], values[1]
	if _, err := tr.TrimXPath("/opidx239:l/a | /opidx239:l/a2 | /opidx239:l/v", XPathOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := v2.Remove(); err != nil {
		t.Errorf("removing v2: %v", err)
	}
	if err := v1.Remove(); err != nil {
		t.Errorf("removing v1: %v", err)
	}
	if got, want := opidxValues(op), []string{"a", "a2", "v:3"}; !slices.Equal(got, want) {
		t.Errorf("children = %v, want %v", got, want)
	}
	if got, want := opidxRecords(op, vs), []string{"3"}; !slices.Equal(got, want) {
		t.Errorf("records = %v, want %v", got, want)
	}
}

// TestOpIndexBulkUnlinkManyActions measures one bulk unlink spanning many
// operation-child sibling sets. Each action starts with five children, so its
// operation index has a hash table; d1, d2, and the first v are removed while
// v=2 and v=3 survive.
func TestOpIndexBulkUnlinkManyActions(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  list l {
    key k;
    leaf k { type uint32; }
    action act { input {
      leaf d1 { type string; }
      leaf d2 { type string; }
      leaf-list v { type uint32; }
    } }
  }
}`)
	mod := opIndexModule(t, set)
	ls := mod.Child(nil, "l")
	ks := schema.FindChild(ls, nil, mod, "k", 0)
	as := schema.FindChild(ls, nil, mod, "act", 0)
	d1s := schema.FindChild(as, nil, mod, "d1", 0)
	d2s := schema.FindChild(as, nil, mod, "d2", 0)
	vs := schema.FindChild(as, nil, mod, "v", 0)
	if ls == nil || ks == nil || as == nil || d1s == nil || d2s == nil || vs == nil {
		t.Fatal("bulk-unlink schema not compiled")
	}

	opIndexScaling(t, []int{128, 256, 512, 1024}, func(n int) int64 {
		tr := newTree(set)
		actions := make([]*Node, 0, n)
		remove := make([]*Node, 0, 3*n)
		for i := range n {
			list := newInner(ls)
			mustLink(t, tr.insert(list, opIndexTerm(t, ks, i), insertDefault))
			mustLink(t, tr.insert(nil, list, insertDefault))
			action := newInner(as)
			mustLink(t, tr.insert(list, action, insertDefault))
			d1 := opidxStringTerm(t, d1s, "x")
			d2 := opidxStringTerm(t, d2s, "y")
			mustLink(t, tr.insert(action, d1, insertDefault))
			mustLink(t, tr.insert(action, d2, insertDefault))
			var first *Node
			for value := 1; value <= 3; value++ {
				v := opIndexTerm(t, vs, value)
				mustLink(t, tr.insert(action, v, insertDefault))
				if value == 1 {
					first = v
				}
			}
			if action.kids.ht == nil {
				t.Fatalf("action %d has no children hash table", i)
			}
			actions = append(actions, action)
			remove = append(remove, d1, d2, first)
		}

		tr.work.Store(0)
		tr.work.visits = true
		tr.work.shifts = true
		if err := tr.unlinkAll(remove); err != nil {
			t.Fatal(err)
		}
		work := tr.work.Load()
		for i, action := range actions {
			if got := opidxValues(action); !slices.Equal(got, []string{"v:2", "v:3"}) {
				t.Fatalf("action %d children = %v, want [v:2 v:3]", i, got)
			}
			if got := tr.schemaLookup(&action.kids, vs); got == nil || got.value.Canonical() != "2" {
				t.Fatalf("action %d first v = %s, want v2", i, opidxLabel(got))
			}
		}
		return work
	})
}

func opidxStringTerm(t *testing.T, sn *schema.Node, value string) *Node {
	t.Helper()
	v, d := types.Store(sn.Type, value, types.FormatJSON,
		types.JSONHints("string")|types.HintStringDatatypes, nil, sn)
	if d != nil {
		t.Fatal(d.Msg)
	}
	n := newTerm(sn, v)
	if n.value.Canonical() != value {
		t.Fatalf("%s = %q, want %q", sn.Name, n.value.Canonical(), value)
	}
	return n
}
