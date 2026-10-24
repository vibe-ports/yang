// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

func round13Set(t *testing.T) *schema.Set {
	t.Helper()
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true},
		os.DirFS("../conformance/corpus/protocol-v2/astra305-round13"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ds, err := c.Load("pv2-astra305-r13", "", nil); err != nil {
		t.Fatal(err, ds)
	}
	return c.Snapshot()
}

func round13RPC(t *testing.T, set *schema.Set, body string) (*Tree, *Node) {
	t.Helper()
	tr, op, ds, err := parseOp(context.Background(), strings.NewReader(`{"pv2-astra305-r13:r":`+body+`}`),
		set, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, ds)
	}
	return tr, op
}

const round13ApplyTarget = `{"outer":[{"inner":[{"id":1},{"id":1}]},{"inner":[{"id":1}]}]}`
const round13ApplySource = `{"@":{"yang:operation":"none"},"outer":[
 {"@":{"yang:operation":"none"},"inner":[{"id":1,"@":{"yang:operation":"create"}}]},
 {"@":{"yang:operation":"none"},"inner":[{"id":1},{"id":1}]},
 {"@":{"yang:operation":"none"},"inner":[{"id":1},{"id":1}]}]}`
const round13MergeTarget = `{"l":[{"b":"b"}]}`
const round13MergeSource = `{"l":[{"b":"b"},{"b":"b"},{"b":"b"}]}`
const round13Reply = `{"pv2-astra305-r13:out":"x"}`

func TestApplyDiffKeylessDescendantDigest(t *testing.T) {
	set := round13Set(t)
	dst, op := round13RPC(t, set, round13ApplyTarget)
	diff, _ := round13RPC(t, set, round13ApplySource)
	var reached int
	if err := dst.ApplyDiff(diff, ApplyDiffOptions{Callback: func(_, n *Node) error {
		if n.Name() == "outer" {
			reached++
		}
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	if reached != 3 {
		t.Fatalf("reached %d outer entries, want 3", reached)
	}
	if len(op.kids.list) != 2 {
		t.Fatalf("outer count = %d", len(op.kids.list))
	}
	for _, outer := range op.kids.list {
		if len(outer.kids.list) != 2 {
			t.Fatalf("inner count = %d", len(outer.kids.list))
		}
	}
}

func TestMergeKeylessBeforeOutputWithoutTable(t *testing.T) {
	set := round13Set(t)
	dst, op := round13RPC(t, set, round13MergeTarget)
	if _, _, ds, err := parseOp(context.Background(), strings.NewReader(round13Reply),
		set, FormatJSON, opReply, op, Reject); err != nil {
		t.Fatal(err, ds)
	}
	if op.kids.ht != nil || len(op.kids.list) != 2 || op.kids.list[0].Name() != "out" {
		t.Fatalf("want output then input without children table: %v", op.kids.list)
	}
	src, _ := round13RPC(t, set, round13MergeSource)
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	var count int
	for _, child := range op.kids.list {
		if child.Name() == "l" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("merged keyless instances = %d, want 2", count)
	}
}

// Duplicate caches belong to a Merge/ApplyDiff sibling walk, not to the Tree.
// Public edits and validation run between walks; a later Merge must see live
// descendants. These cover every external descendant-mutating path separately.
func TestKeylessCachesAcrossPublicMutations(t *testing.T) {
	set := round13Set(t)
	for _, tc := range []struct {
		name, before, after string
		mutate              func(*Tree, *Node) error
	}{
		{"set-value", `[{"b":"a"},{"b":"b"}]`, `[{"b":"b"},{"b":"b"}]`, func(_ *Tree, first *Node) error {
			_, err := first.kids.list[0].SetValue("b")
			return err
		}},
		{"new-path-update", `[{"b":"a"},{"b":"b"}]`, `[{"b":"b"},{"b":"b"}]`, func(tr *Tree, _ *Node) error {
			_, err := tr.NewPath("/pv2-astra305-r13:state/l[1]/b", "b", NewPathOptions{Update: true})
			return err
		}},
		{"insert", `[{"b":"b"},{"b":"b","extra":"x"}]`, `[{"b":"b","extra":"x"},{"b":"b","extra":"x"}]`, func(tr *Tree, first *Node) error {
			src, ds, err := parseJSONString(set, `{"pv2-astra305-r13:state":{"l":[{"extra":"x"}]}}`, Reject, false)
			if err != nil {
				t.Fatal(err, ds)
			}
			n := src.top.first().kids.list[0].kids.list[0]
			if err := n.Unlink(); err != nil {
				return err
			}
			return tr.Insert(first, n)
		}},
		{"remove", `[{"b":"b","extra":"x"},{"b":"b"}]`, `[{"b":"b"},{"b":"b"}]`, func(_ *Tree, first *Node) error {
			return first.kids.list[1].Remove()
		}},
		{"validation-defaults", `[{"b":"defaults"},{"b":"defaults","d":"z"}]`, `[{"b":"defaults","d":"z"},{"b":"defaults","d":"z"}]`, func(tr *Tree, _ *Node) error {
			_, err := tr.Validate(context.Background(), ValidateOptions{})
			return err
		}},
		{"validation-autodelete", `[{"b":"b","gone":"x"},{"b":"b"}]`, `[{"b":"b"},{"b":"b"}]`, func(tr *Tree, _ *Node) error {
			_, err := tr.Validate(context.Background(), ValidateOptions{})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parse := func(lists string) *Tree {
				o := parseOpts{ParseOptions: ParseOptions{Unknown: Reject, ParseOnly: true}, whenTrue: true}
				tr, ds, err := parseWith(context.Background(), strings.NewReader(`{"pv2-astra305-r13:state":{"l":`+lists+`}}`), set, o, parseJSON, nil)
				if err != nil {
					t.Fatal(err, ds)
				}
				return tr
			}
			dst, warm := parse(tc.before), parse(tc.before)
			if err := dst.Merge(warm); err != nil {
				t.Fatal(err)
			}
			first := dst.top.first().kids.list[0]
			if err := tc.mutate(dst, first); err != nil {
				t.Fatal(err)
			}
			if err := dst.Merge(parse(tc.after)); err != nil {
				t.Fatal(err)
			}
			state, err := dst.Find("/pv2-astra305-r13:state")
			if err != nil || state == nil {
				t.Fatal("state missing", err)
			}
			if got := len(state.kids.list); got != 2 {
				t.Fatalf("instances after edit and merge = %d, want 2", got)
			}
		})
	}
}

// Match a recursive native copy of user-ordered input split by another input node.
// LYD_INSERT_NODE_LAST retains the split; copying omits the source's RB metadata.
func round13SplitCopy(t *testing.T, set *schema.Set, count int) (*Tree, *Node) {
	t.Helper()
	values := make([]string, count)
	for i := range values {
		values[i] = strconv.Itoa(i + 5)
	}
	tr, op := round13RPC(t, set, `{"a":"a","b":"b","c":"c","v":[`+strings.Join(values, ",")+`]}`)
	if _, err := tr.NewTerm(op, "out", "x", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	_, source := round13RPC(t, set, `{"v":[1]}`)
	tail := source.kids.list[0]
	if err := tail.Unlink(); err != nil {
		t.Fatal(err)
	}
	if err := tr.insert(op, tail, insertLast); err != nil {
		t.Fatal(err)
	}
	dup, err := dupTo(op, set, nil, dupRecursive, false)
	if err != nil {
		t.Fatal(err)
	}
	return treeOf(dup), dup
}
