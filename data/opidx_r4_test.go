// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// TestOpIndexBulkUnlinkScrambledRecords measures bulk removal of operation
// children after rehashing alternating user-ordered leaf-list run starts has
// scrambled their first-instance record chain.
func TestOpIndexBulkUnlinkScrambledRecords(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r {
    input { leaf-list v { type uint32; ordered-by user; } }
    output { leaf-list o { type uint32; ordered-by user; } }
  }
}`)
	mod := opIndexModule(t, set)
	rpc := mod.Child(nil, "r")
	vs := schema.FindChild(rpc, nil, mod, "v", 0)
	if rpc == nil || vs == nil {
		t.Fatal("bulk-unlink schema not compiled")
	}

	type removeKind int
	const (
		removeAllV removeKind = iota
		trimToOutput
	)
	for _, tc := range []struct {
		name string
		kind removeKind
	}{
		{name: "unlinkAll", kind: removeAllV},
		{name: "TrimXPath", kind: trimToOutput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opIndexScaling(t, []int{512, 1024, 2048, 4096}, func(n int) int64 {
				tr, op, diags, err := parseOp(context.Background(), strings.NewReader(
					`{"m:r":{}}`), set, FormatJSON, opRPC, nil, Reject)
				if err != nil {
					t.Fatal(err, diags)
				}
				b := newBuilder{set: set, log: &logger{set: set}}
				add := func(name, value string, output bool) *Node {
					t.Helper()
					node, err := b.newTerm(op, mod, name, value, newValOptions{output: output})
					if err = b.log.done(err); err != nil {
						t.Fatal(err)
					}
					b.log = &logger{set: set}
					return node
				}

				// Establish the children hash table before making each v a
				// separate run by alternating it with an output instance.
				for i := range 4 {
					add("o", fmt.Sprint(i), true)
				}
				if op.kids.ht == nil {
					t.Fatal("operation has no children hash table")
				}
				remove := make([]*Node, 0, n)
				for i := range n {
					remove = append(remove, add("v", fmt.Sprint(i), false))
					add("o", fmt.Sprint(i+4), true)
				}
				if got := len(op.kids.records(vs)); got != n {
					t.Fatalf("v first-instance records = %d, want %d", got, n)
				}

				// Rehash every odd-numbered run start. Unlinking and inserting
				// it moves its record to the back of the schema chain.
				for i := 0; i < n; i += 2 {
					if rc := tr.changeTerm(remove[i], fmt.Sprint(n+i)); rc != "" {
						t.Fatalf("changeTerm v[%d]: %s", i, rc)
					}
				}

				tr.work.Store(0)
				tr.work.visits = true
				tr.work.shifts = true
				switch tc.kind {
				case removeAllV:
					if err := tr.unlinkAll(remove); err != nil {
						t.Fatal(err)
					}
				case trimToOutput:
					if _, err := tr.TrimXPath("/m:r/o", XPathOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				work := tr.work.Load()

				for child := range op.kids.all() {
					if child.schema == vs {
						t.Fatalf("v remains after bulk removal: %s", child.value.Canonical())
					}
				}
				if got := tr.schemaLookup(&op.kids, vs); got != nil {
					t.Fatalf("first-instance lookup = %s, want nil", opidxLabel(got))
				}
				return work
			})
		})
	}
}
