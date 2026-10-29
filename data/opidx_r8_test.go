// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

func opidxR8Sets(t *testing.T) (opsFixture, *schema.Set, *schema.Set) {
	t.Helper()
	fixture := readOpsFixture(t, filepath.Join(opsManifest, "opidx-leader-parse-transitions.yaml"))
	set1 := opsSet(t, fixture)
	target := fixture
	target.searchdirs = []string{"opidxu.2"}
	return fixture, set1, opsSet(t, target)
}

func opidxR8AddTerm(t *testing.T, set *schema.Set, mod *schema.Module, parent *Node, name, value string, output bool) *Node {
	t.Helper()
	b := newBuilder{set: set, log: &logger{set: set}}
	n, err := b.newTerm(parent, mod, name, value, newValOptions{output: output})
	if err = b.log.done(err); err != nil {
		t.Fatal(err)
	}
	return n
}

func opidxR8DupOperation(t *testing.T, src *Node, target *schema.Set) *Node {
	t.Helper()
	// libyang's lyd_dup_single_to_ctx refuses the output node o (native golden of
	// ops/opidx-leader-parse-transitions: LY_ENOTFOUND at the dup step), and so does the port.
	if _, err := dupTo(src, target, nil, dupRecursive, false); err == nil {
		t.Fatal("recursive cross-context copy of output data succeeded, libyang refuses it")
	}
	// Duplicate the operation shell across contexts, then reproduce recursive duplication's
	// LYD_INSERT_NODE_LAST child links so this test reaches the first-instance transition under test.
	op, err := dupTo(src, target, nil, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	tr := treeOf(op)
	mod := target.Implemented("opidxu")
	rpc := mod.Child(nil, "l")
	for child := range src.kids.all() {
		var opts schema.GetNextOpt
		if child.Name() == "o" {
			opts = schema.GetNextOutput
		}
		sn := schema.FindChild(rpc, nil, mod, child.Name(), opts)
		if sn == nil {
			t.Fatalf("target schema %s not compiled", child.Name())
		}
		var n *Node
		if sn.Kind == schema.LeafList {
			value, err := strconv.Atoi(child.value.Canonical())
			if err != nil {
				t.Fatal(err)
			}
			n = opIndexTerm(t, sn, value)
		} else {
			n = opidxStringTerm(t, sn, child.value.Canonical())
		}
		mustLink(t, tr.insert(op, n, insertLast))
	}
	return op
}

// TestOpIndexParseLeaderTransitions builds the state natively unreachable (see
// ops/opidx-leader-parse-transitions) by hand: the cross-context copy links the two system-ordered s runs LAST, so the joined run has records
// [5,3] but no RB tree. Parsing 3 builds the tree and relocates the old 3 ahead of 5, changing
// the record to [3]. The equal parsed 3 follows it, then parsed 1 becomes the new first and must
// replace that record, leaving [1] for the insertion anchor of pre.
func TestOpIndexParseLeaderTransitions(t *testing.T) {
	fixture, set1, set2 := opidxR8Sets(t)
	mod1 := set1.Implemented("opidxu")
	_, src, diags, err := parseOp(context.Background(), strings.NewReader(
		`{"opidxu:l":{"a":"1","a2":"2","a3":"3","s":[5]}}`), set1, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	opidxR8AddTerm(t, set1, mod1, src, "o", "o", true)
	opidxR8AddTerm(t, set1, mod1, src, "s", "3", false)
	mod2 := set2.Implemented("opidxu")
	rpc2 := mod2.Child(nil, "l")
	if rpc2 == nil {
		t.Fatal("target operation schema not compiled")
	}
	if os := schema.FindChild(rpc2, nil, mod2, "o", schema.GetNextOutput); os == nil {
		t.Fatal("target output schema not compiled")
	}

	op := opidxR8DupOperation(t, src, set2)
	tr := treeOf(op)
	ss := schema.FindChild(rpc2, nil, mod2, "s", 0)
	if ss == nil {
		t.Fatal("target s schema not compiled")
	}
	if _, err := tr.TrimXPath("/opidxu:l/a | /opidxu:l/a2 | /opidxu:l/a3 | /opidxu:l/s", XPathOptions{}); err != nil {
		t.Fatal(err)
	}
	if op.kids.hasRB(ss) {
		t.Fatal("copied and joined s run unexpectedly has an RB tree")
	}
	if got, want := opidxRecords(op, ss), []string{"5", "3"}; !slices.Equal(got, want) {
		t.Fatalf("records before parse = %v, want %v", got, want)
	}

	if _, _, diags, err := parseOp(context.Background(), strings.NewReader(
		`{"opidxu:s":[3,1]}`), set2, FormatJSON, opRPC, op, Reject); err != nil {
		t.Fatal(err, diags)
	}
	if got, want := opidxRecords(op, ss), []string{"1"}; !slices.Equal(got, want) {
		t.Errorf("records after parse = %v, want %v", got, want)
	}
	opidxCheckRecords(t, &op.kids)

	pre := opidxR8AddTerm(t, set2, mod2, op, "pre", "x", false)
	preAt, oneAt := -1, -1
	for i, child := range op.kids.SchemaAll() {
		if child == pre {
			preAt = i
		}
		if child.schema == ss && child.value.Canonical() == "1" {
			oneAt = i
		}
	}
	if preAt < 0 || oneAt != preAt+1 {
		t.Errorf("pre index = %d, s=1 index = %d; want pre immediately before s=1 (children %v)",
			preAt, oneAt, opidxValues(op))
	}
	// The native run stops at the cross-context copy: the golden records libyang's refusal.
	b, err := os.ReadFile(filepath.Join("../conformance/corpus", fixture.dir, fixture.golden))
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		FailedStep *int `json:"failed_step"`
		Steps      []struct {
			RC struct {
				Name string `json:"name"`
			} `json:"rc"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	if golden.FailedStep == nil || *golden.FailedStep != 3 || golden.Steps[3].RC.Name != "LY_ENOTFOUND" {
		t.Errorf("golden: want the dup step (3) to fail with LY_ENOTFOUND, got %+v", golden)
	}
}

// TestOpIndexParseLeaderTransitionsWork scales the same transition. A copied pair of s runs is
// joined without creating an RB tree, then one parse extends it with descending values. Every
// parsed value is a new minimum, so the first-instance record must finish on the final value.
func TestOpIndexParseLeaderTransitionsWork(t *testing.T) {
	_, set1, set2 := opidxR8Sets(t)
	mod1 := set1.Implemented("opidxu")
	mod2 := set2.Implemented("opidxu")
	rpc2 := mod2.Child(nil, "l")
	if rpc2 == nil {
		t.Fatal("target operation schema not compiled")
	}
	ss := schema.FindChild(rpc2, nil, mod2, "s", 0)
	if ss == nil {
		t.Fatal("target s schema not compiled")
	}

	opIndexScaling(t, scaleSizes([]int{512, 1024, 2048, 4096}, 3), func(n int) int64 {
		_, src, diags, err := parseOp(context.Background(), strings.NewReader(fmt.Sprintf(
			`{"opidxu:l":{"a":"1","a2":"2","a3":"3","s":[%d]}}`, n+2)),
			set1, FormatJSON, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		opidxR8AddTerm(t, set1, mod1, src, "o", "o", true)
		opidxR8AddTerm(t, set1, mod1, src, "s", fmt.Sprint(n+1), false)

		op := opidxR8DupOperation(t, src, set2)
		tr := treeOf(op)
		if _, err := tr.TrimXPath("/opidxu:l/a | /opidxu:l/a2 | /opidxu:l/a3 | /opidxu:l/s", XPathOptions{}); err != nil {
			t.Fatal(err)
		}
		if op.kids.hasRB(ss) {
			t.Fatal("copied and joined s run unexpectedly has an RB tree")
		}
		if got, want := opidxRecords(op, ss), []string{fmt.Sprint(n + 2), fmt.Sprint(n + 1)}; !slices.Equal(got, want) {
			t.Fatalf("records before parse = %v, want %v", got, want)
		}

		var values strings.Builder
		for value := n; value > 0; value-- {
			if value != n {
				values.WriteByte(',')
			}
			fmt.Fprint(&values, value)
		}
		input := `{"opidxu:s":[` + values.String() + `]}`
		tr.work.Store(0)
		tr.work.visits = true
		if _, _, diags, err := parseOp(context.Background(), strings.NewReader(input),
			set2, FormatJSON, opRPC, op, Reject); err != nil {
			t.Fatal(err, diags)
		}
		work := tr.work.Load()
		if got, want := opidxRecords(op, ss), []string{"1"}; !slices.Equal(got, want) {
			t.Errorf("n=%d records after parse = %v, want %v", n, got, want)
		}
		opidxCheckRecords(t, &op.kids)
		return work
	})
}
