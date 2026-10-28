// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestOpIndexRehashKeepsRecord replays ops/opidx-leader-rehash-stale. Trimming o leaves v3's
// first-instance record even though v3 no longer starts a run. lyd_change_term unlinks and inserts
// the user-ordered v3 but touches only its exact-value record, so the first-instance record remains.
func TestOpIndexRehashKeepsRecord(t *testing.T) {
	fixture := readOpsFixture(t, filepath.Join(opsManifest, "opidx-leader-rehash-stale.yaml"))
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

	add("o", "o", true)
	v3 := add("v", "3", false)
	vs := v3.schema
	vsiblings := opidxSchemaNodes(op, vs)
	if len(vsiblings) != 3 {
		t.Fatalf("v instances %d, want 3", len(vsiblings))
	}
	v1 := vsiblings[0]
	v2 := vsiblings[1]

	// Use the fixture's TrimXPath operation instead of freeing o directly.
	if _, err := tr.TrimXPath("/opidx239:l/a | /opidx239:l/a2 | /opidx239:l/v", XPathOptions{}); err != nil {
		t.Fatal(err)
	}
	if rc := tr.changeTerm(v3, "5"); rc != "" {
		t.Fatalf("changeTerm: %s", rc)
	}
	if got, want := op.kids.records(vs), []*Node{v1, v3}; !slices.Equal(got, want) {
		t.Fatalf("record nodes %v, want v1,v3", opidxRecords(op, vs))
	}
	if got, want := opidxRecords(op, vs), []string{"1", "5"}; !slices.Equal(got, want) {
		t.Fatalf("records %v, want %v", got, want)
	}

	if err := freeTree(v1); err != nil {
		t.Fatal(err)
	}
	if got := tr.schemaLookup(&op.kids, vs); got != v3 {
		t.Fatalf("first-instance lookup = %s, want v5", opidxLabel(got))
	}
	if got, want := opidxRecords(op, vs), []string{"5", "2"}; !slices.Equal(got, want) {
		t.Fatalf("records %v, want %v", got, want)
	}
	if got := opidxSchemaNodes(op, vs); len(got) != 2 || got[0] != v2 || got[1] != v3 {
		t.Fatalf("v document instances %v, want v2,v5", opidxValues(op))
	}

	add("between", "x", false)
	if got, want := opidxValues(op), []string{"a", "a2", "v:2", "between", "v:5"}; !slices.Equal(got, want) {
		t.Errorf("children %v, want %v", got, want)
	}
	opidxCompareGoldens(t, fixture, []int{7}, []opidxSnapshot{opidxSnapshotOf(t, tr)})
}

// TestOpIndexDirtyFlushManyRuns covers parser bookkeeping when many system-ordered list runs are
// pending for the operation's sibling set while each keyed list instance closes its own inner set.
func TestOpIndexDirtyFlushManyRuns(t *testing.T) {
	sizes := scaleSizes([]int{128, 256, 512, 1024}, 3)
	opIndexScaling(t, sizes, func(n int) int64 {
		var yang, input strings.Builder
		yang.WriteString(`module m { yang-version 1.1; namespace "urn:m"; prefix m; rpc r { input {`)
		input.WriteString(`{"m:r":{`)
		for i := range n {
			fmt.Fprintf(&yang, " list l%d { key k; leaf k { type uint32; } }", i)
			if i != 0 {
				input.WriteByte(',')
			}
			fmt.Fprintf(&input, `"l%d":[{"k":1},{"k":0}]`, i)
		}
		yang.WriteString(` } } }`)
		input.WriteString(`}}`)

		set := opIndexSet(t, yang.String())
		tr, op := opIndexParseRPC(t, set, input.String())
		counts := make(map[string]int, n)
		for child := range op.kids.all() {
			name := child.Name()
			at := counts[name]
			if at >= 2 {
				t.Fatalf("%s has more than 2 instances", name)
			}
			if child.kids.SchemaLen() != 1 {
				t.Fatalf("%s[%d] has %d children, want key k", name, at, child.kids.SchemaLen())
			}
			if got, want := child.kids.At(0).value.Canonical(), fmt.Sprint(at); got != want {
				t.Fatalf("%s[%d].k = %s, want %s", name, at, got, want)
			}
			counts[name]++
		}
		if len(counts) != n {
			t.Fatalf("parsed %d list runs, want %d", len(counts), n)
		}
		for i := range n {
			if got := counts[fmt.Sprintf("l%d", i)]; got != 2 {
				t.Fatalf("l%d has %d instances, want 2", i, got)
			}
		}
		return tr.work.Load()
	})
}
