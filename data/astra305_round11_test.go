// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
)

// lyd_find_sibling_first deliberately uses opposite comparison directions in
// its children-table and linear branches. Recursive sibling comparison is
// asymmetric because a system-ordered lookup may reuse one matching instance.
func TestMergeKeylessChildrenTableUsesTargetFirstComparison(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input {
    leaf a { type string; } leaf b { type string; } leaf c { type string; }
    list outer { list inner { key id; leaf id { type uint32; } } }
  } } }`)
	dst, op := opIndexParseRPC(t, set,
		`{"m:r":{"a":"a","b":"b","c":"c","outer":[{"inner":[{"id":1},{"id":1}]}]}}`)
	src, _ := opIndexParseRPC(t, set,
		`{"m:r":{"a":"a","b":"b","c":"c","outer":[{"inner":[{"id":1},{"id":2}]}]}}`)
	if op.kids.ht == nil {
		t.Fatal("fixture needs a destination children table")
	}
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	var outer int
	for _, child := range op.kids.list {
		if child.Name() == "outer" {
			outer++
		}
	}
	if outer != 2 {
		t.Fatalf("outer instances after merge = %d, want 2", outer)
	}
}

// Every first insertion into a distinct ordinary system-ordered leaf-list
// probes hasRB. An absent schema is a constant-time map miss; it must not scan
// all preceding siblings when no document-order index exists.
func TestParseDistinctDatastoreLeafListsScaling(t *testing.T) {
	const large = 1024
	var model strings.Builder
	model.WriteString(`module m { yang-version 1.1; namespace "urn:m"; prefix m; container r {`)
	for i := range large {
		fmt.Fprintf(&model, `leaf-list l%04d { type string; }`, i)
	}
	model.WriteString(`} }`)
	set := opIndexSet(t, model.String())

	measure := func(count int) int64 {
		t.Helper()
		var input strings.Builder
		input.WriteString(`{"m:r":{`)
		for i := range count {
			if i != 0 {
				input.WriteByte(',')
			}
			fmt.Fprintf(&input, `"l%04d":["v"]`, i)
		}
		input.WriteString(`}}`)
		var lc *lydCtx
		tr, diags, err := parseWith(context.Background(), strings.NewReader(input.String()), set,
			parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, func(c *lydCtx) {
				lc = c
				c.tree.work.visits, c.tree.work.shifts = true, true
			})
		if err != nil {
			t.Fatal(err, diags)
		}
		root := tr.top.first()
		if root == nil {
			t.Fatal("parsed tree has no root")
		}
		if got := len(root.kids.list); got != count {
			t.Fatalf("parsed children = %d, want %d", got, count)
		}
		return lc.tree.work.Load()
	}
	smallWork, largeWork := measure(large/4), measure(large)
	t.Logf("distinct leaf-list parse work: %d=%d, %d=%d", large/4, smallWork, large, largeWork)
	if smallWork == 0 || largeWork > 6*smallWork {
		t.Fatalf("quadrupling distinct leaf-lists changed work from %d to %d", smallWork, largeWork)
	}
}

func TestKeyedInstanceIdentifierProbeBudgetPropagates(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  container r {
    list item { key id; leaf id { type string; } }
    leaf ref { type instance-identifier; }
    leaf union-ref { type union { type instance-identifier; type string; } }
  } }`)
	tr, _, err := parseJSONString(set,
		`{"m:r":{"item":[{"id":"x"}],"ref":"/m:r/item[id='x']","union-ref":"/m:r/item[id='x']"}}`,
		Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	root := tr.top.first()
	var ref, unionRef *Node
	for _, child := range root.kids.list {
		switch child.Name() {
		case "ref":
			ref = child
		case "union-ref":
			unionRef = child
		}
	}
	if ref == nil || unionRef == nil {
		t.Fatal("instance-identifier fixture leaves not found")
	}

	newContext := func() *valCtx {
		vc := &valCtx{t: tr, log: &logger{set: set}}
		vc.probes = newTree(set)
		vc.probes.work.hashLimit = 1
		return vc
	}

	vc := newContext()
	tt := &typeTree{vc: vc, n: ref}
	if tt.InstanceExists(ref.value.Path()) || !errors.Is(tt.err, yang.ErrBudget) {
		t.Fatalf("InstanceExists = true, error %v; want ErrBudget", tt.err)
	}

	vc = newContext()
	if _, err := vc.derefType(ref, ref.value, ref.schema.Type, true); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("deref instance-identifier error = %v, want ErrBudget", err)
	}

	vc = newContext()
	if _, err := vc.derefType(unionRef, unionRef.value, unionRef.schema.Type, true); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("deref union error = %v, want ErrBudget instead of string fallback", err)
	}
}
