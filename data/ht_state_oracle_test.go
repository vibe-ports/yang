// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestHashTableStateOracle replays the ops/ht-* sequence fixtures. The relative order of the
// rpc's input and output children makes libyang's otherwise internal children-hash-table state
// observable after insertion, deletion, and recursive or non-recursive duplication.
func TestHashTableStateOracle(t *testing.T) {
	tests := []struct {
		name         string
		data         string
		deletes      []string
		dupRecursive bool
		dupShallow   bool
		insertIn2    bool
		insertIn     bool
	}{
		{name: "ht-create-insert-output", data: `{"rb:r":{"in":"a","in2":"b","in3":"c","in4":"d"}}`},
		{name: "ht-none-insert-output", data: `{"rb:r":{"in":"a"}}`},
		{name: "ht-shrink-insert-output", data: `{"rb:r":{"in":"a","in2":"b","in3":"c","in4":"d"}}`, deletes: []string{"in2", "in3", "in4"}},
		{name: "ht-dup-shrunk-insert-output", data: `{"rb:r":{"in":"a","in2":"b","in3":"c","in4":"d"}}`, deletes: []string{"in2", "in3", "in4"}, dupRecursive: true},
		{name: "ht-dup-none-insert-output", data: `{"rb:r":{"in":"a"}}`, dupRecursive: true},
		{name: "ht-dup-nonrecursive-insert-output", data: `{"rb:r":{"in":"a","in2":"b","in3":"c","in4":"d"}}`, dupShallow: true, insertIn: true},
		{name: "ht-grow-after-shrink-insert-output", data: `{"rb:r":{"in":"a","in2":"b","in3":"c","in4":"d"}}`, deletes: []string{"in2", "in3", "in4"}, insertIn2: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			goldenPath := filepath.Join("..", "conformance", "corpus", "ut-parser", "golden", "ops-"+tc.name+".json")
			raw, err := os.ReadFile(goldenPath) //nolint:gosec // fixed corpus path
			if os.IsNotExist(err) {
				t.Skip("golden not generated yet")
			}
			if err != nil {
				t.Fatal(err)
			}
			var golden struct {
				Steps []struct {
					Typed []struct {
						Path string `json:"path"`
					} `json:"typed"`
				} `json:"steps"`
			}
			if err := json.Unmarshal(raw, &golden); err != nil || len(golden.Steps) == 0 {
				t.Fatal(err)
			}
			last := golden.Steps[len(golden.Steps)-1]
			if len(last.Typed) == 0 || last.Typed[0].Path != "/rb:r" {
				t.Fatalf("final golden typed tree has no /rb:r root: %+v", last.Typed)
			}
			want := make([]string, 0, len(last.Typed)-1)
			for _, n := range last.Typed[1:] {
				want = append(want, n.Path)
			}

			fixture := readOpsFixture(t, filepath.Join(opsManifest, tc.name+".yaml"))
			set := opsSet(t, fixture)
			tr, op, diags, err := parseOp(context.Background(), strings.NewReader(tc.data), set,
				FormatJSON, opRPC, nil, Reject)
			if err != nil {
				t.Fatal(err, diags)
			}
			for _, name := range tc.deletes {
				n, findErr := tr.Find("/rb:r/" + name)
				if findErr != nil || n == nil {
					t.Fatalf("find %s: %v", name, findErr)
				}
				if err := freeTree(n); err != nil {
					t.Fatalf("delete %s: %v", name, err)
				}
			}
			if tc.dupRecursive {
				dst := newTree(set)
				op, err = dst.dup(op)
				if err != nil {
					t.Fatal(err)
				}
				mustLink(t, dst.insert(nil, op, insertDefault))
				tr = dst
			}
			if tc.dupShallow {
				dst := newTree(set)
				op, err = dst.dupR(op, nil, true, insertDefault, 0)
				if err != nil {
					t.Fatal(err)
				}
				tr = dst
			}

			b := newBuilder{set: set, log: &logger{set: set}}
			mod := set.Implemented("rb")
			if tc.insertIn {
				if _, err := b.newTerm(op, mod, "in", "x", newValOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.insertIn2 {
				if _, err := b.newTerm(op, mod, "in2", "x", newValOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := b.newInner(op, mod, "oc", true); err != nil {
				t.Fatal(err)
			}
			if err := b.log.done(nil); err != nil {
				t.Fatal(err)
			}

			got := make([]string, 0, len(op.kids.list))
			for n := range op.kids.all() {
				got = append(got, n.Path())
			}
			if !slices.Equal(got, want) {
				t.Errorf("children %v, oracle %v", got, want)
			}
		})
	}
}
