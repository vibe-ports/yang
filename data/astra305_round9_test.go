// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

func TestMergeKeylessReplySplitRunDoesNotCacheHiddenInsertion(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r {
    input { leaf input { type string; } }
    output {
      leaf before { type string; }
      list l { leaf value { type string; } }
    }
  } }`)
	dst, op := opIndexParseRPC(t, set, `{"m:r":{"input":"kept"}}`)
	if _, _, diags, err := parseOp(context.Background(), strings.NewReader(
		`{"m:before":"before","m:l":[{"value":"a"},{"value":"b"},{"value":"c"}]}`),
		set, FormatJSON, opReply, op, Reject); err != nil {
		t.Fatal(err, diags)
	}
	wantShape := []string{"before", "l:a", "l:b", "input", "l:c"}
	if got := round9ChildShape(op); !reflect.DeepEqual(got, wantShape) || op.kids.ht == nil {
		t.Fatalf("reply shape = %v (table=%t), want %v with a table", got, op.kids.ht != nil, wantShape)
	}

	src, srcOp := opIndexParseRPC(t, set, `{"m:r":{}}`)
	if _, _, diags, err := parseOp(context.Background(), strings.NewReader(
		`{"m:l":[{"value":"c"},{"value":"c"}]}`),
		set, FormatJSON, opReply, srcOp, Reject); err != nil {
		t.Fatal(err, diags)
	}
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	var values []string
	for _, child := range op.kids.SchemaAll() {
		if child.Name() == "l" {
			values = append(values, child.kids.At(0).value.Canonical())
		}
	}
	if want := []string{"a", "b", "c", "c", "c"}; !reflect.DeepEqual(values, want) {
		t.Fatalf("keyless instances after split-run merge = %v, want %v", values, want)
	}
}

func round9ChildShape(op *Node) []string {
	shape := make([]string, 0, op.kids.SchemaLen())
	for _, child := range op.kids.SchemaAll() {
		label := child.Name()
		if label == "l" && child.kids.SchemaLen() != 0 {
			label += ":" + child.kids.At(0).value.Canonical()
		}
		shape = append(shape, label)
	}
	return shape
}

func round9NestedMerge(t *testing.T, set *schema.Set, count int) (*Tree, *Tree) {
	t.Helper()
	input := func() string {
		var b strings.Builder
		b.WriteString(`{"m:r":{"outer":[`)
		for i := range count {
			if i != 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"inner":[{"id":`)
			b.WriteString(strconv.Itoa(i))
			b.WriteString(`}]}`)
		}
		b.WriteString(`]}}`)
		return b.String()
	}()
	dst, _ := opIndexParseRPC(t, set, input)
	src, _ := opIndexParseRPC(t, set, input)
	return dst, src
}

func round9NestedSet(t *testing.T) *schema.Set {
	t.Helper()
	return opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input {
    list outer { list inner { key "id"; leaf id { type uint32; } } }
  } } }`)
}

func TestMergeNestedKeylessComparisonWorkIsCumulative(t *testing.T) {
	set := round9NestedSet(t)
	measure := func(count int) uint64 {
		dst, src := round9NestedMerge(t, set, count)
		if err := dst.Merge(src); err != nil {
			t.Fatal(err)
		}
		return dst.work.hashUsed
	}
	small, large := measure(96), measure(192)
	t.Logf("nested keyless comparison work: 96=%d, 192=%d", small, large)
	if small == 0 || large < 3*small || large > 5*small {
		t.Fatalf("charged nested comparison work did not scale quadratically: %d -> %d", small, large)
	}
}

func TestMergeNestedKeylessComparisonBudget(t *testing.T) {
	set := round9NestedSet(t)
	full, fullSrc := round9NestedMerge(t, set, 64)
	if err := full.Merge(fullSrc); err != nil {
		t.Fatal(err)
	}
	need := full.work.hashUsed
	if need < 2 {
		t.Fatalf("unbounded merge used only %d work units", need)
	}

	dst, src := round9NestedMerge(t, set, 64)
	before := snapshotHashBudgetTree(dst)
	dst.work.hashLimit = need - 1
	err := dst.Merge(src)
	if !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("merge with comparison budget %d = %v, want ErrBudget (full work %d)", need-1, err, need)
	}
	if after := snapshotHashBudgetTree(dst); !reflect.DeepEqual(after, before) {
		t.Fatal("comparison budget failure changed the destination")
	}
	opidxAssertTreeInvariant(t, dst)
}
