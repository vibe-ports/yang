// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// TestOpIndexStaleRecordDropped is D-0117. [a,a2,v1,v2] gets its table with the record v1; the
// output o and then v3 are appended, v3 starting a run (records v1,v3). Freeing o leaves v3's
// record although v3 no longer starts a run. lyd_unlink_hash (tree_data_hash.c:207-245) removes
// only the exact-hash record of a node that does not start a run, so freeing v3 leaves libyang a
// dangling record; freeing v1 then appends v2 behind it and lyd_find_sibling_schema returns the
// freed v3 (use after free). The port drops the record with its node: the lookup finds v2.
func TestOpIndexStaleRecordDropped(t *testing.T) {
	fixture := readOpsFixture(t, filepath.Join(opsManifest, "opidx-leader-replace-dup.yaml"))
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
		if err != nil {
			t.Fatal(err)
		}
		if err := b.log.done(nil); err != nil {
			t.Fatal(err)
		}
		return n
	}
	o := add("o", "o", true)
	v3 := add("v", "3", false)
	vs := v3.schema
	v1 := opidxSchemaNodes(op, vs)[0]
	if !slices.Equal(op.kids.records(vs), []*Node{v1, v3}) {
		t.Fatalf("records %q, want v1,v3", opidxRecords(op, vs))
	}
	for _, n := range []*Node{o, v3, v1} {
		if err := freeTree(n); err != nil {
			t.Fatal(err)
		}
	}
	if got := opidxRecords(op, vs); !slices.Equal(got, []string{"2"}) {
		t.Errorf("records %q, want [2] (D-0117: the freed v3 is not kept)", got)
	}
	if got := tr.schemaLookup(&op.kids, vs); got == nil || got.value.Canonical() != "2" {
		t.Errorf("first-instance lookup = %s, want v2", opidxLabel(got))
	}
}

func opidxRecords(op *Node, sn *schema.Node) []string {
	var s []string
	for _, n := range op.kids.records(sn) {
		s = append(s, n.value.Canonical())
	}
	return s
}

// TestOpIndexParseSortsRunWithoutRBTree: a system-ordered input run that already has several
// instances but no RB tree (linked with LYD_INSERT_NODE_LAST, as lyd_dup does) gets one at the
// next sorted insertion: lyds_additionally_create_rb_tree (tree_data_sorted.c) inserts the
// existing instances in order, so the run ends sorted even when the new value is in order
// relative to the last one. The parser's deferred sort must cover the existing instances too.
func TestOpIndexParseSortsRunWithoutRBTree(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r { input { leaf-list v { type uint32; } } }
}`)
	mod := opIndexModule(t, set)
	rpc := mod.Child(nil, "r")
	sn := schema.FindChild(rpc, nil, mod, "v", 0)
	var op *Node
	fp := func(lc *lydCtx, _ []byte) error {
		op = newInner(rpc)
		lc.tree.insert(nil, op, insertDefault)
		for _, v := range []int{3, 1} {
			lc.tree.insert(op, opIndexTerm(t, sn, v), insertLast)
		}
		lc.nodeInsert(op, nil, opIndexTerm(t, sn, 4))
		return lc.closeInner(op, nil)
	}
	o := parseOpts{ParseOptions: ParseOptions{Unknown: Reject, ParseOnly: true}}
	if _, diags, err := parseWith(context.Background(), strings.NewReader(""), set, o, fp, nil); err != nil {
		t.Fatal(err, diags)
	}
	var got []string
	for _, n := range op.kids.list {
		got = append(got, n.value.Canonical())
	}
	if want := []string{"1", "3", "4"}; !slices.Equal(got, want) {
		t.Errorf("run %v, want %v (lyds_additionally_create_rb_tree sorts the existing instances)", got, want)
	}
}
