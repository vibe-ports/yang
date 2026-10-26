// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// opaqueOnlyChildrenTable builds the state left by lyd_unlink_hash: a container whose children
// table survives after every schema child is removed, while an opaque child remains.
func opaqueOnlyChildrenTable(t *testing.T) (*Tree, *Node) {
	t.Helper()
	tr, diags, err := parseJSONString(pjSchema(),
		`{"pj:c":{"s":"a","n":1,"e":[null],"ll":[2],"unknown":"opaque"}}`, Opaque, true)
	if err != nil {
		t.Fatalf("parse: %v %v", err, diags)
	}
	c, err := tr.Find("/pj:c")
	if err != nil || c == nil || c.kids.ht == nil {
		t.Fatalf("container/table: %v %v table=%v", c, err, c != nil && c.kids.ht != nil)
	}
	for _, path := range []string{"/pj:c/s", "/pj:c/n", "/pj:c/e", "/pj:c/ll[.='2']"} {
		n, findErr := tr.Find(path)
		if findErr != nil || n == nil {
			t.Fatalf("find %s: %v", path, findErr)
		}
		if err := n.Remove(); err != nil {
			t.Fatalf("remove %s: %v", path, err)
		}
	}
	if c.kids.ht == nil || c.kids.SchemaLen() != 0 || c.kids.OpaqueLen() != 1 {
		t.Fatalf("opaque-only children: schema=%d opaque=%d table=%v", c.kids.SchemaLen(), c.kids.OpaqueLen(), c.kids.ht != nil)
	}
	return tr, c
}

func childSchema(t *testing.T, parent *schema.Node, name string) *schema.Node {
	t.Helper()
	for _, n := range parent.Children {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("schema child %s not found", name)
	return nil
}

// TestFindFirstOpaqueOnlyHashTable covers lyd_find_sibling_first both through lyd_find_path and
// directly. Before the empty-list guard both calls panic at s.list[0].
func TestFindFirstOpaqueOnlyHashTable(t *testing.T) {
	t.Run("Find", func(t *testing.T) {
		tr, _ := opaqueOnlyChildrenTable(t)
		if n, err := tr.Find("/pj:c/l[k='missing']"); err != nil || n != nil {
			t.Fatalf("Find: %v %v", n, err)
		}
	})

	t.Run("findFirst", func(t *testing.T) {
		tr, c := opaqueOnlyChildrenTable(t)
		sn := childSchema(t, c.schema, "ll")
		v, d := types.Store(sn.Type, "9", types.FormatJSON, types.HintData, types.ModuleNames{Set: tr.set}, sn)
		if d != nil {
			t.Fatal(d.Msg)
		}
		if n := tr.findFirst(&c.kids, newTerm(sn, v)); n != nil {
			t.Fatalf("findFirst returned %v", n)
		}
	})
}

// TestFindFirstOpaqueOnlyHashTableAfterDup covers lyd_dup_r copying an empty children table.
// Before the empty-list guard the exported lookup on the recursive copy panics at s.list[0].
func TestFindFirstOpaqueOnlyHashTableAfterDup(t *testing.T) {
	src, c := opaqueOnlyChildrenTable(t)
	dst := newTree(src.set)
	d, diags, err := dst.Dup(c, nil, DupOptions{Recursive: true})
	if err != nil || len(diags) != 0 {
		t.Fatalf("Dup: %v %v", err, diags)
	}
	if d.kids.ht == nil || d.kids.SchemaLen() != 0 || d.kids.OpaqueLen() != 1 {
		t.Fatalf("duplicate children: schema=%d opaque=%d table=%v", d.kids.SchemaLen(), d.kids.OpaqueLen(), d.kids.ht != nil)
	}
	if n, err := dst.Find("/pj:c/ll[.='7']"); err != nil || n != nil {
		t.Fatalf("Find duplicate: %v %v", n, err)
	}
}

// TestXPathOpaqueOnlyHashTable covers the XPath children-table lookup and its opaque fallback.
func TestXPathOpaqueOnlyHashTable(t *testing.T) {
	tr, _ := opaqueOnlyChildrenTable(t)
	nodes, diags, err := tr.FindXPath("/pj:c/l[k='missing']", XPathOptions{})
	if err != nil || len(diags) != 0 || len(nodes) != 0 {
		t.Fatalf("FindXPath: nodes=%v diagnostics=%v error=%v", nodes, diags, err)
	}
}

// TestInsertOpaqueOnlyHashTable covers insertion and run-bound calculation after the schema
// children shrink to none. The schema child must still be ordered before the opaque tail.
func TestInsertOpaqueOnlyHashTable(t *testing.T) {
	tr, c := opaqueOnlyChildrenTable(t)
	sn := childSchema(t, c.schema, "ll")
	v, d := types.Store(sn.Type, "5", types.FormatJSON, types.HintData, types.ModuleNames{Set: tr.set}, sn)
	if d != nil {
		t.Fatal(d.Msg)
	}
	if lo, hi := tr.runBounds(&c.kids, newTerm(sn, v)); lo != 0 || hi != 0 {
		t.Fatalf("runBounds = %d,%d", lo, hi)
	}
	if _, err := tr.NewTerm(c, "ll", "5", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := names(c.Children()); !slices.Equal(got, []string{"ll=5", "unknown"}) {
		t.Fatalf("children %v", got)
	}
}

// TestValidateOpaqueOnlyHashTable covers validation readers with an empty schema-child list.
// The opaque node is invalid final data, but validation must return that error instead of panic.
func TestValidateOpaqueOnlyHashTable(t *testing.T) {
	tr, _ := opaqueOnlyChildrenTable(t)
	diags, err := tr.Validate(context.Background(), ValidateOptions{})
	if err == nil || len(diags) == 0 || diags[0].DataPath != "/pj:c" {
		t.Fatalf("Validate: diagnostics=%v error=%v", diags, err)
	}
}

// TestInsertOutputOpaqueOnlyHashTable covers operation anchoring after all indexed input children
// are removed. A new output child is inserted before the opaque tail.
func TestInsertOutputOpaqueOnlyHashTable(t *testing.T) {
	set := opsSet(t, readOpsFixture(t, opsManifest+"/ht-create-insert-output.yaml"))
	tr, op, diags, err := parseOp(context.Background(), strings.NewReader(
		`{"rb:r":{"in":"a","in2":"b","in3":"c","in4":"d","unknown":"opaque"}}`),
		set, FormatJSON, opRPC, nil, Opaque)
	if err != nil {
		t.Fatalf("parse operation: %v %v", err, diags)
	}
	for _, name := range []string{"in", "in2", "in3", "in4"} {
		n, findErr := tr.Find("/rb:r/" + name)
		if findErr != nil || n == nil {
			t.Fatalf("find %s: %v", name, findErr)
		}
		if err := n.Remove(); err != nil {
			t.Fatalf("remove %s: %v", name, err)
		}
	}
	if op.kids.ht == nil || op.kids.SchemaLen() != 0 || op.kids.OpaqueLen() != 1 {
		t.Fatalf("opaque-only operation: schema=%d opaque=%d table=%v", op.kids.SchemaLen(), op.kids.OpaqueLen(), op.kids.ht != nil)
	}
	var v *schema.Node
	for _, sn := range tr.opOrder(op.schema, true) {
		if sn.Name == "v" {
			v = sn
			break
		}
	}
	if v == nil {
		t.Fatal("output leaf-list v not found")
	}
	value, d := types.Store(v.Type, "1", types.FormatJSON, types.HintData, types.ModuleNames{Set: tr.set}, v)
	if d != nil {
		t.Fatal(d.Msg)
	}
	if lo, hi := tr.runBounds(&op.kids, newTerm(v, value)); lo != 0 || hi != 0 {
		t.Fatalf("operation runBounds = %d,%d", lo, hi)
	}
	if _, err := tr.NewInner(op, "oc", NewOptions{Output: true}); err != nil {
		t.Fatal(err)
	}
	if got := names(op.Children()); !slices.Equal(got, []string{"oc", "unknown"}) {
		t.Fatalf("operation children %v", got)
	}
}
