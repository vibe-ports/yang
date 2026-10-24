// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

type opidxSnapshot struct {
	json  string
	typed []string
}

func opidxID(t *testing.T, n *Node) string {
	t.Helper()
	for _, m := range n.meta {
		if m.mod.Name == "opidx239" && m.name == "id" {
			return m.value.Canonical()
		}
	}
	t.Fatalf("%s has no opidx239:id metadata", n.Path())
	return ""
}

func opidxChildOrder(t *testing.T, op *Node) []string {
	t.Helper()
	var got []string
	for n := range op.kids.all() {
		name := n.Name()
		if name == "v" || name == "u" {
			name += ":" + opidxID(t, n)
		}
		got = append(got, name)
	}
	return got
}

func opidxSchemaNodes(op *Node, sn *schema.Node) []*Node {
	var nodes []*Node
	for n := range op.kids.all() {
		if n.schema == sn {
			nodes = append(nodes, n)
		}
	}
	return nodes
}

func opidxChildNamed(op *Node, name string) *Node {
	for n := range op.kids.all() {
		if n.Name() == name {
			return n
		}
	}
	return nil
}

func opidxSnapshotOf(t *testing.T, tr *Tree) opidxSnapshot {
	t.Helper()
	var out bytes.Buffer
	if err := tr.PrintJSON(&out, PrintOptions{}); err != nil {
		t.Fatal(err)
	}
	return opidxSnapshot{json: out.String(), typed: typedDump(tr)}
}

func opidxCompareGoldens(t *testing.T, fixture opsFixture, indexes []int, got []opidxSnapshot) {
	t.Helper()
	t.Run("golden", func(t *testing.T) {
		b, err := os.ReadFile(filepath.Join("../conformance/corpus", fixture.dir, fixture.golden))
		if errors.Is(err, fs.ErrNotExist) {
			t.Skip("golden not generated yet")
		}
		if err != nil {
			t.Fatal(err)
		}
		var golden struct {
			Steps []diffOpGoldenStep `json:"steps"`
		}
		if err := json.Unmarshal(b, &golden); err != nil {
			t.Fatal(err)
		}
		if len(indexes) != len(got) {
			t.Fatal("snapshot index mismatch")
		}
		for i, step := range indexes {
			if step >= len(golden.Steps) || golden.Steps[step].Do != "dump" || golden.Steps[step].Tree == nil {
				t.Fatalf("golden step %d is not a dump", step)
			}
			compareDiffOpJSON(t, "step dump", got[i].json, golden.Steps[step].Tree.JSON)
			if want := golden.Steps[step].dump(); !reflect.DeepEqual(got[i].typed, want) {
				t.Errorf("step %d typed:\n got  %q\n want %q", step, got[i].typed, want)
			}
		}
	})
}

// opidxValues lists the children of op, a leaf-list instance as name:value.
func opidxValues(op *Node) []string {
	var got []string
	for n := range op.kids.all() {
		s := n.Name()
		if n.schema != nil && n.schema.Kind == schema.LeafList {
			s += ":" + n.value.Canonical()
		}
		got = append(got, s)
	}
	return got
}

func opidxLabel(n *Node) string {
	if n == nil {
		return "nil"
	}
	return "v" + n.value.Canonical()
}

// TestOpIndexLeaderOracle replays ops/opidx-leader-replace-dup. The table exists once
// [a,a2,v1,v2] are linked (records: v1); o and then v3 are appended, v3 starting a second run
// (lyd_insert_hash_add, tree_data_hash.c:128-163: records v1,v3). Freeing v1 (lyd_unlink_hash,
// tree_data_hash.c:207-245) removes its record and appends v2's: v3,v2. lyd_find_sibling_schema
// (tree_data_common.c:1101-1115) returns the first record and lyd_insert_get_next_anchor
// (tree_data.c:522-535) anchors between before it: before v3. The recursive copy inserts with
// LYD_INSERT_NODE_LAST into an empty table (records v2,v3): between goes before v2.
func TestOpIndexLeaderOracle(t *testing.T) {
	fixture := readOpsFixture(t, filepath.Join(opsManifest, "opidx-leader-replace-dup.yaml"))
	set := opsSet(t, fixture)
	mod := set.Implemented("opidx239")
	tr, op, diags, err := parseOp(context.Background(), strings.NewReader(
		`{"opidx239:l":{"a":"1","a2":"2","@v":[{"opidx239:id":"v1"},{"opidx239:id":"v2"}],"v":[1,2]}}`),
		set, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	b := newBuilder{set: set, log: &logger{set: set}}
	add := func(name, value string, output bool) {
		t.Helper()
		if _, err := b.newTerm(op, mod, name, value, newValOptions{output: output}); err != nil {
			t.Fatal(err)
		}
		if err := b.log.done(nil); err != nil {
			t.Fatal(err)
		}
	}
	add("o", "o", true)
	add("v", "3", false)
	if got, want := opidxValues(op), []string{"a", "a2", "v:1", "v:2", "o", "v:3"}; !slices.Equal(got, want) {
		t.Fatalf("children %v, want %v", got, want)
	}
	v := opidxSchemaNodes(op, op.kids.list[2].schema)
	vschema := v[0].schema
	if err := freeTree(v[0]); err != nil {
		t.Fatal(err)
	}
	if got := tr.schemaLookup(&op.kids, vschema); got == nil || got.value.Canonical() != "3" {
		t.Errorf("first-instance lookup after freeing v1 = %s, want v3", opidxLabel(got))
	}
	add("between", "x", false)
	if got, want := opidxValues(op), []string{"a", "a2", "v:2", "o", "between", "v:3"}; !slices.Equal(got, want) {
		t.Errorf("after freeing v1 %v, want %v", got, want)
	}
	first := opidxSnapshotOf(t, tr)

	if err := freeTree(opidxChildNamed(op, "between")); err != nil {
		t.Fatal(err)
	}
	dst := newTree(set)
	op, err = dst.dup(op)
	if err != nil {
		t.Fatal(err)
	}
	mustLink(t, dst.insert(nil, op, insertDefault))
	if got := dst.schemaLookup(&op.kids, vschema); got == nil || got.value.Canonical() != "2" {
		t.Errorf("first-instance lookup in the recursive copy = %s, want v2", opidxLabel(got))
	}
	add("between", "x", false)
	if got, want := opidxValues(op), []string{"a", "a2", "between", "v:2", "o", "v:3"}; !slices.Equal(got, want) {
		t.Errorf("recursive copy %v, want %v", got, want)
	}
	opidxCompareGoldens(t, fixture, []int{5, 9}, []opidxSnapshot{first, opidxSnapshotOf(t, dst)})
}

func opidxDupSetIDs(t *testing.T, tr *Tree, op, target *Node) []string {
	t.Helper()
	cache := &dupCache{}
	var got []string
	for {
		n, err := tr.findMatch(&op.kids, target, true, cache)
		if err != nil {
			t.Fatal(err)
		}
		if n == nil {
			return got
		}
		got = append(got, opidxID(t, n))
	}
}

// TestOpIndexDupSetOracle replays ops/opidx-dupset-change-diff. The user-ordered input leaf-list
// [1,2,1,1] (a,b,c,d) gets its table when the fourth instance is linked (lyd_insert_hash,
// tree_data_hash.c:181-197, records in sibling order). lyd_change_term of b to 1 is
// lyd_unlink_hash + lyd_insert_hash for a user-ordered instance (tree_data_new.c:1178-1194), so
// b's record goes behind c's and d's. lyd_find_sibling_dup_inst_set (tree_data.c:3312-3370) is the
// first match (a) then the other records in registration order: a,c,d,b, which lyd_dup_inst_next
// (tree_data_common.c:108-143) hands to the second loop of lyd_diff_siblings_r; with LYD_DIFF_META
// lyd_diff_userord_attrs reports the metadata of the pairs b-c, c-d, d-b. The recursive copy
// registers in sibling order and the same diff is empty.
func TestOpIndexDupSetOracle(t *testing.T) {
	fixture := readOpsFixture(t, filepath.Join(opsManifest, "opidx-dupset-change-diff.yaml"))
	set := opsSet(t, fixture)
	const rpc = `{"opidx239:r":{"@u":[{"opidx239:id":"a"},{"opidx239:id":"b"},{"opidx239:id":"c"},{"opidx239:id":"d"}],"u":[1,2,1,1]}}`
	const second = `{"opidx239:r":{"@u":[{"opidx239:id":"a"},{"opidx239:id":"b"},{"opidx239:id":"c"},{"opidx239:id":"d"}],"u":[1,1,1,1]}}`
	parse := func(in string) (*Tree, *Node) {
		tr, op, diags, err := parseOp(context.Background(), strings.NewReader(in), set, FormatJSON, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		return tr, op
	}
	tr, op := parse(rpc)
	u := opidxSchemaNodes(op, op.kids.list[0].schema)
	if len(u) != 4 {
		t.Fatalf("u instances %d", len(u))
	}
	if rc := tr.changeTerm(u[1], "1"); rc != "" {
		t.Fatalf("changeTerm: %s", rc)
	}
	wantDoc := []string{"u:a", "u:b", "u:c", "u:d"}
	if got := opidxChildOrder(t, op); !slices.Equal(got, wantDoc) {
		t.Errorf("document %v, want %v", got, wantDoc)
	}
	if got, want := opidxDupSetIDs(t, tr, op, u[0]), []string{"a", "c", "d", "b"}; !slices.Equal(got, want) {
		t.Errorf("duplicate set %v, want %v", got, want)
	}
	_, sop := parse(second)
	diff1, err := DiffTree(op, sop, DiffOptions{Meta: true})
	if err != nil {
		t.Fatalf("DiffTree: %v", err)
	}
	dump1 := opidxSnapshotOf(t, tr)

	dst := newTree(set)
	op, err = dst.dup(op)
	if err != nil {
		t.Fatal(err)
	}
	mustLink(t, dst.insert(nil, op, insertDefault))
	if got, want := opidxDupSetIDs(t, dst, op, op.kids.list[0]), []string{"a", "b", "c", "d"}; !slices.Equal(got, want) {
		t.Errorf("copied duplicate set %v, want %v", got, want)
	}
	diff2, err := DiffTree(op, sop, DiffOptions{Meta: true})
	if err != nil {
		t.Fatalf("DiffTree: %v", err)
	}
	dump2 := opidxSnapshotOf(t, dst)

	t.Run("golden", func(t *testing.T) {
		b, err := os.ReadFile(filepath.Join("../conformance/corpus", fixture.dir, fixture.golden))
		if err != nil {
			t.Fatal(err)
		}
		var golden struct {
			Steps []diffOpGoldenStep `json:"steps"`
		}
		if err := json.Unmarshal(b, &golden); err != nil {
			t.Fatal(err)
		}
		if len(golden.Steps) != 7 {
			t.Fatalf("golden has %d steps", len(golden.Steps))
		}
		compareDiffOpPrinted(t, "diff step 2", diff1, golden.Steps[2].Diff, PrintOptions{WithDefaults: WDAll})
		compareDiffOpPrinted(t, "diff step 5", diff2, golden.Steps[5].Diff, PrintOptions{WithDefaults: WDAll})
		for i, snap := range map[int]opidxSnapshot{3: dump1, 6: dump2} {
			compareDiffOpJSON(t, "dump", snap.json, golden.Steps[i].Tree.JSON)
			if want := golden.Steps[i].dump(); !reflect.DeepEqual(snap.typed, want) {
				t.Errorf("step %d typed:\n got  %q\n want %q", i, snap.typed, want)
			}
		}
	})
}
