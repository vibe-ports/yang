// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

func opIndexParse(t *testing.T, set *schema.Set, input string, typ opType, unknown UnknownPolicy) (*Tree, *Node) {
	t.Helper()
	tr, op, diags, err := parseOp(context.Background(), strings.NewReader(input), set,
		FormatJSON, typ, nil, unknown)
	if err != nil {
		t.Fatal(err, diags)
	}
	return tr, op
}

func TestOpIndexMergeKeylessIntoOpaqueOnlyOperation(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 rpc r { input { list l { leaf v { type string; } } } } }`)
	dst, op := opIndexParse(t, set, `{"m:r":{"unknown":"opaque"}}`, opRPC, Opaque)
	src, _ := opIndexParse(t, set, `{"m:r":{"l":[{"v":"x"}]}}`, opRPC, Opaque)

	if len(op.kids.list) != 0 || len(op.kids.opq) != 1 {
		t.Fatalf("destination children = %d schema, %d opaque; want 0, 1", len(op.kids.list), len(op.kids.opq))
	}
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	if len(op.kids.list) != 1 || op.kids.list[0].Name() != "l" || len(op.kids.opq) != 1 {
		t.Fatalf("merged children = %d schema, %d opaque", len(op.kids.list), len(op.kids.opq))
	}
}

func TestOpIndexDetachedOperationDiff(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 rpc r { input {
   leaf plain { type string; }
   leaf-list ordered { type string; ordered-by user; }
 } } }`)
	for _, mod := range set.Modules {
		mod.Owner = set
	}

	for _, tc := range []struct {
		name, input, path string
	}{
		{name: "leaf", input: `{"m:r":{"plain":"x"}}`, path: "/m:r/plain"},
		{name: "user-ordered-leaf-list", input: `{"m:r":{"ordered":["x"]}}`, path: "/m:r/ordered[.='x']"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, _ := opIndexParse(t, set, tc.input, opRPC, Reject)
			n, err := tr.Find(tc.path)
			if err != nil || n == nil {
				t.Fatalf("Find(%s) = %v, %v", tc.path, n, err)
			}
			if err := n.Unlink(); err != nil {
				t.Fatal(err)
			}
			if ctxOf(n) != set {
				t.Fatal("detached operation child lost its schema snapshot")
			}
			diff, err := DiffTree(n, n, DiffOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if diff != nil {
				t.Fatalf("DiffTree(detached, detached) = %v, want nil", diff)
			}
		})
	}
}

func TestOpIndexNotificationOpaqueOrderDoesNotSelectOperationLookup(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 notification n { leaf-list v { type uint32; } } }`)
	input := `{"m:n":{"v":[1,2,3,4],"unknown":"opaque"}}`
	dst, notif := opIndexParse(t, set, input, opNotif, Opaque)
	src, _ := opIndexParse(t, set, input, opNotif, Opaque)
	if notif.kids.ht == nil || notif.kids.order == nil {
		t.Fatal("notification reproducer needs both children hash and opaque order indexes")
	}
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	var values int
	for _, n := range notif.kids.list {
		if n.Name() == "v" {
			values++
		}
	}
	if values != 4 {
		t.Fatalf("leaf-list instances after identical merge = %d, want 4", values)
	}
}

func TestOpIndexKeylessDigestIgnoresForeignOrderingFlags(t *testing.T) {
	module := func(order string) string {
		return `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 rpc r { input { list outer { list inner { key id; ` + order + ` leaf id { type uint32; } } } } } }`
	}
	dstSet := opIndexSet(t, module(""))
	foreignSet := opIndexSet(t, module("ordered-by user;"))
	for _, set := range []*schema.Set{dstSet, foreignSet} {
		for _, mod := range set.Modules {
			mod.Owner = set
		}
	}

	dst, dstOp := opIndexParse(t, dstSet, `{"m:r":{"outer":[{"inner":[{"id":1}]}]}}`, opRPC, Reject)
	src, srcOp := opIndexParse(t, dstSet, `{"m:r":{}}`, opRPC, Reject)
	_, foreignOp := opIndexParse(t, foreignSet, `{"m:r":{"outer":[{"inner":[{"id":1}]}]}}`, opRPC, Reject)
	outer := foreignOp.kids.list[0]
	if err := outer.Unlink(); err != nil {
		t.Fatal(err)
	}
	if err := src.link(srcOp, &srcOp.kids, outer, 0); err != nil {
		t.Fatal(err)
	}

	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	var outers int
	for _, n := range dstOp.kids.list {
		if n.Name() == "outer" {
			outers++
		}
	}
	if outers != 1 {
		t.Fatalf("outer instances after cross-context merge = %d, want 1", outers)
	}
}
