// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

func TestValidateOpRefreshesExactLeafListIndex(t *testing.T) {
	const model = `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input {
    leaf a { type string; } leaf b { type string; } leaf c { type string; }
    leaf target { type int32; }
    leaf-list v { type union {
      type leafref { path "../target"; }
      type string;
    } }
  } } }`
	set := opIndexSet(t, model)
	parse := func() (*Tree, *Node) {
		t.Helper()
		tr, op, diags, err := parseOp(context.Background(), strings.NewReader(
			`<r xmlns="urn:m"><a>a</a><b>b</b><c>c</c><v>01</v></r>`),
			set, FormatXML, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		if diags, err = validateOp(context.Background(), set, op, nil, opRPC, Budget{}); err != nil {
			t.Fatal(err, diags)
		}
		return tr, op
	}
	dst, op := parse()
	_, srcOp := parse()
	value := op.kids.list[3]
	if got := value.value.Canonical(); got != "01" {
		t.Fatalf("validated value = %q, want 01", got)
	}
	if op.kids.order == nil || op.kids.ht == nil {
		t.Fatal("fixture needs both operation-child indexes")
	}
	if got := op.kids.exactFirst(value.schema, value); got != value {
		t.Fatalf("exact lookup after validation = %p, want %p", got, value)
	}
	if err := dst.Merge(treeOf(srcOp)); err != nil {
		t.Fatal(err)
	}
	if got := len(opidxSchemaNodes(op, value.schema)); got != 1 {
		t.Fatalf("leaf-list instances after merge = %d, want 1", got)
	}
}

func TestValidateOpExactIndexBudgetRollback(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input {
    leaf a { type string; } leaf b { type string; } leaf c { type string; }
    leaf target { type int32; }
    leaf-list v { type union {
      type leafref { path "../target"; }
      type string;
    } }
  } } }`)
	build := func() ([]*Tree, func() error) {
		tr, op, diags, err := parseOp(context.Background(), strings.NewReader(
			`<r xmlns="urn:m"><a>a</a><b>b</b><c>c</c><v>01</v></r>`),
			set, FormatXML, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		return []*Tree{tr}, func() error {
			_, err := validateOp(context.Background(), set, op, nil, opRPC, Budget{})
			return err
		}
	}
	checkEveryHashBudgetPointPair(t, build, func(t *testing.T, trees []*Tree) {
		t.Helper()
		opidxAssertTreeInvariant(t, trees[0])
		op := trees[0].top.first()
		value := op.kids.list[3]
		if value.value.Canonical() != "01" || op.kids.exactFirst(value.schema, value) != value {
			t.Fatal("successful validation left the exact-value index stale")
		}
	})
}

func TestMergeForeignKeylessListMatchesMappedKeyedList(t *testing.T) {
	module := func(key string) string {
		return `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input {
    leaf a { type string; } leaf b { type string; } leaf c { type string; }
    list l { ` + key + ` leaf id { type string; } }
  } } }`
	}
	keyed := opIndexSet(t, module(`key "id";`))
	keyless := opIndexSet(t, module(""))
	for _, set := range []*schema.Set{keyed, keyless} {
		for _, mod := range set.Modules {
			mod.Owner = set
		}
	}
	dst, op := opIndexParseRPC(t, keyed,
		`{"m:r":{"a":"a","b":"b","c":"c","l":[{"id":""}]}}`)
	src, srcOp := opIndexParseRPC(t, keyed, `{"m:r":{"a":"a","b":"b","c":"c"}}`)
	_, foreignOp := opIndexParseRPC(t, keyless,
		`{"m:r":{"a":"a","b":"b","c":"c","l":[{"id":""}]}}`)
	foreign := foreignOp.kids.list[3]
	if err := foreign.Unlink(); err != nil {
		t.Fatal(err)
	}
	if err := src.link(srcOp, &srcOp.kids, foreign, len(srcOp.kids.list)); err != nil {
		t.Fatal(err)
	}
	if op.kids.ht == nil {
		t.Fatal("fixture needs an operation children table")
	}
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	var lists int
	for n := range op.kids.all() {
		if n.Name() == "l" {
			lists++
		}
	}
	if lists != 1 {
		t.Fatalf("list instances after merge = %d, want 1", lists)
	}
}

func TestMergeKeylessDigestInvalidatedAfterDescendantChange(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input { list outer { list inner { key "id"; leaf id { type uint32; } } } } } }`)
	dst, op := opIndexParseRPC(t, set,
		`{"m:r":{"outer":[{"inner":[{"id":1},{"id":1}]},{"inner":[{"id":1},{"id":1},{"id":2}]}]}}`)
	src, _ := opIndexParseRPC(t, set,
		`{"m:r":{"outer":[{"inner":[{"id":1},{"id":2}]},{"inner":[{"id":1},{"id":1},{"id":2}]}]}}`)
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	var outer int
	for n := range op.kids.all() {
		if n.Name() == "outer" {
			outer++
		}
	}
	if outer != 3 {
		t.Fatalf("outer instances after merge = %d, want 3", outer)
	}
}

func TestApplyForeignOutputLeafListDeleteWithoutTable(t *testing.T) {
	const model = `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { output { leaf-list v { type uint32; } } } }`
	a, b := opIndexSet(t, model), opIndexSet(t, model)
	for _, set := range []*schema.Set{a, b} {
		for _, module := range set.Modules {
			module.Owner = set
		}
	}
	parseReply := func(set *schema.Set, output string) (*Tree, *Node) {
		t.Helper()
		tr, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"m:r":{}}`), set,
			FormatJSON, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		if _, _, diags, err = parseOp(context.Background(), strings.NewReader(output), set,
			FormatJSON, opReply, op, Reject); err != nil {
			t.Fatal(err, diags)
		}
		return tr, op
	}
	target, _ := parseReply(a, `{"m:v":[1]}`)
	_, before := parseReply(b, `{"m:v":[1]}`)
	_, after := parseReply(b, `{}`)
	diff, err := DiffTree(before, after, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := target.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatalf("ApplyDiff foreign output delete: %v", err)
	}
	root := target.top.first()
	if root == nil || len(root.kids.list) != 0 {
		t.Fatalf("output children after delete = %v", root)
	}
}

func TestMergeForeignKeyDeclarationDigestIsConservative(t *testing.T) {
	const prefix = `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input { leaf a { type string; } leaf b { type string; } leaf c { type string; }
    list outer { list inner { `
	const suffix = `leaf id { type string; } } } } } }`
	keyed := opIndexSet(t, prefix+`key "id"; `+suffix)
	keyless := opIndexSet(t, prefix+suffix)
	for _, set := range []*schema.Set{keyed, keyless} {
		for _, module := range set.Modules {
			module.Owner = set
		}
	}
	dst, op := opIndexParseRPC(t, keyless,
		`{"m:r":{"a":"a","b":"b","c":"c","outer":[{"inner":[{"id":""}]}]}}`)
	src, srcOp := opIndexParseRPC(t, keyless, `{"m:r":{"a":"a","b":"b","c":"c"}}`)
	_, foreignOp := opIndexParseRPC(t, keyed,
		`{"m:r":{"outer":[{"inner":[{"id":""}]}]}}`)
	foreignOuter := foreignOp.kids.list[0]
	if err := foreignOuter.Unlink(); err != nil {
		t.Fatal(err)
	}
	if err := src.link(srcOp, &srcOp.kids, foreignOuter, 0); err != nil {
		t.Fatal(err)
	}
	if op.kids.ht == nil {
		t.Fatal("fixture needs an operation children table")
	}
	var localOuter *Node
	for n := range op.kids.all() {
		if n.Name() == "outer" {
			localOuter = n
			break
		}
	}
	if localOuter == nil {
		t.Fatal("destination outer instance not found")
	}
	left, leftOK := subtreeEquivalenceKey(localOuter, &dst.work)
	right, rightOK := subtreeEquivalenceKey(foreignOuter, &dst.work)
	if !leftOK || !rightOK || left != right {
		t.Fatalf("conservative digests differ: ok=(%t,%t), equal=%t", leftOK, rightOK, left == right)
	}
	if !compareSingle(dst, localOuter, foreignOuter, true) {
		t.Fatal("foreign list declarations do not compare equal")
	}
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	var outer int
	for n := range op.kids.all() {
		if n.Name() == "outer" {
			outer++
		}
	}
	if outer != 1 {
		t.Fatalf("outer instances after merge = %d, want 1", outer)
	}
}

func TestInsertDetachedChildIntoCopiedEmptyOperation(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input {
    leaf a { type string; } leaf b { type string; }
    leaf c { type string; } leaf d { type string; }
  } } }`)
	for _, mod := range set.Modules {
		mod.Owner = set
	}
	_, op := opIndexParseRPC(t, set, `{"m:r":{"a":"a","b":"b","c":"c","d":"d"}}`)
	if op.kids.ht == nil || op.kids.order == nil {
		t.Fatal("four operation children must create both indexes")
	}
	for _, child := range slices.Clone(op.kids.list) {
		if err := child.Remove(); err != nil {
			t.Fatal(err)
		}
	}
	dup, err := dupTo(op, set, nil, dupRecursive, false)
	if err != nil {
		t.Fatal(err)
	}
	if dup.kids.ht == nil || dup.kids.order != nil || len(dup.kids.list) != 0 {
		t.Fatalf("copied children: table=%v order=%v count=%d", dup.kids.ht != nil,
			dup.kids.order != nil, len(dup.kids.list))
	}

	_, source := opIndexParseRPC(t, set, `{"m:r":{"a":"detached"}}`)
	child := source.kids.list[0]
	if err := child.Unlink(); err != nil {
		t.Fatal(err)
	}
	if err := treeOf(dup).Insert(dup, child); err != nil {
		t.Fatal(err)
	}
	if len(dup.kids.list) != 1 || dup.kids.list[0] != child {
		t.Fatalf("copied operation children = %v, want inserted child", dup.kids.list)
	}
}

func TestMergeKeylessSplitOperationRootsWithoutTable(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input {
    list l { leaf v { type string; } }
    leaf split { type string; }
  } } }`)
	_, op := opIndexParseRPC(t, set, `{"m:r":{"l":[{"v":"a"},{"v":"b"}],"split":"x"}}`)
	var lists []*Node
	var split *Node
	for _, child := range op.kids.list {
		if child.Name() == "l" {
			lists = append(lists, child)
		} else if child.Name() == "split" {
			split = child
		}
	}
	if len(lists) != 2 || split == nil {
		t.Fatal("operation source does not contain the required children")
	}

	dst := newTree(set)
	appendCopy := func(source *Node) {
		t.Helper()
		dup, err := dupTo(source, set, nil, dupRecursive, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := dup.Unlink(); err != nil {
			t.Fatal(err)
		}
		if err := dst.insert(nil, dup, insertLast); err != nil {
			t.Fatal(err)
		}
	}
	appendCopy(lists[0])
	appendCopy(split)
	appendCopy(lists[1])
	if dst.top.ht != nil || len(dst.top.list) != 3 || dst.top.list[0].schema != lists[0].schema ||
		dst.top.list[1].schema != split.schema || dst.top.list[2].schema != lists[0].schema {
		t.Fatalf("destination is not the no-table split run: %v", dst.top.list)
	}

	mergeRoot, err := dupTo(lists[1], set, nil, dupRecursive, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.Merge(treeOf(mergeRoot)); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range dst.top.list {
		if n.schema == lists[0].schema {
			got = append(got, n.kids.list[0].value.Canonical())
		}
	}
	if want := []string{"a", "b"}; !slices.Equal(got, want) {
		t.Fatalf("keyless instances after merge = %v, want %v", got, want)
	}
}

func TestInsertDetachedOperationLeafListScaling(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input { leaf-list v { type uint32; } } } }`)
	for _, module := range set.Modules {
		module.Owner = set
	}
	mod := opIndexModule(t, set)
	rpc := mod.Child(nil, "r")
	values := schema.FindChild(rpc, nil, mod, "v", 0)
	if values == nil {
		t.Fatal("leaf-list schema not compiled")
	}
	type measurement struct {
		work  int64
		alloc uint64
	}
	measure := func(n int) measurement {
		t.Helper()
		tr, op := opIndexParseRPC(t, set, `{"m:r":{}}`)
		detached := make([]*Node, n)
		for i := range n {
			detached[i] = opIndexTerm(t, values, i)
		}
		tr.work.Store(0)
		tr.work.visits, tr.work.shifts = true, true
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for i, node := range detached {
			if err := tr.Insert(op, node); err != nil {
				t.Fatalf("Insert(%d): %v", i, err)
			}
		}
		runtime.ReadMemStats(&after)
		return measurement{tr.work.Load(), after.TotalAlloc - before.TotalAlloc}
	}
	small, large := measure(512), measure(1024)
	t.Logf("n=512 work=%d alloc=%d; n=1024 work=%d alloc=%d",
		small.work, small.alloc, large.work, large.alloc)
	if large.work > 3*small.work {
		t.Fatalf("counted work doubled input by more than 3x: %d -> %d", small.work, large.work)
	}
	if large.alloc > 3*small.alloc {
		t.Fatalf("allocated bytes doubled input by more than 3x: %d -> %d", small.alloc, large.alloc)
	}
}
