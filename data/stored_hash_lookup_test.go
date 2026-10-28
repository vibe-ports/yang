// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vibe-ports/yang/internal/xpath"
)

const storedHashLookupSchema = `module m {
  yang-version 1.1; namespace "urn:stored-hash-lookup"; prefix m;
  grouping nodes {
    leaf a { type string; } leaf b { type string; } leaf c { type string; }
    list l { key k; leaf k { type string; } leaf v { type string; } }
  }
  uses nodes;
  container r { uses nodes; }
}`

// moveto_node_hash_child makes one fresh list instance before lyd_find_sibling_first.
// The repeated keys hash as "ab", so a predicate for "a" must miss with and without
// a children table, at the document root too. A real predicate evaluation still
// sees both keys when libyang cannot compile the predicate into an instance lookup.
func TestStoredHashXPathLookup(t *testing.T) {
	set := opIndexSet(t, storedHashLookupSchema)
	for _, nested := range []bool{false, true} {
		for _, table := range []bool{false, true} {
			for _, valid := range []bool{false, true} {
				t.Run(fmt.Sprintf("nested=%t/table=%t/valid=%t", nested, table, valid), func(t *testing.T) {
					list := `[{"k":"a","k":"b","v":"bad"}`
					if valid {
						// Two valid instances force LookupChild to decline the bucket.
						list += `,{"k":"a","v":"first"},{"k":"a","v":"second"}`
					}
					input, path := `"m:l":`+list+`]`, "/m:l"
					if table {
						input += `,"m:a":"a","m:b":"b","m:c":"c"`
					}
					if nested {
						input, path = `"m:r":{`+input+`}`, "/m:r/l"
					}
					tr, _, err := parseJSONString(set, `{`+input+`}`, Reject, true)
					if err != nil {
						t.Fatal(err)
					}
					if nested && (tr.top.At(0).kids.ht != nil) != table {
						t.Fatal("unexpected children-table state")
					}
					query := path + "[k='a']"
					var wg sync.WaitGroup
					for range 4 {
						wg.Go(func() {
							got, err := findXPath(tr, &logger{set: set}, nil, query, nil)
							if err != nil || len(got) != boolInt(valid) {
								t.Errorf("query = %v, %v", got, err)
							} else if valid && got[0].kids.At(1).Value() != "first" {
								t.Error("scan matched the repeated-key instance")
							}
						})
					}
					wg.Wait()
					got, err := findXPath(tr, &logger{set: set}, nil, path+"[k=$key]",
						[]xpath.Var{{Name: "key", Value: "'a'"}})
					if err != nil || len(got) != 1+2*boolInt(valid) {
						t.Fatalf("ordinary predicate = %v, %v", got, err)
					}
					// Find and NewPath share ly_path_eval_partial and must obey the gate too.
					if got, _ := tr.Find(query); (got != nil) != valid {
						t.Fatal("Find disagrees with the hash lookup")
					}
					if !valid {
						if _, err := tr.NewPath(query+"/v", "created", NewPathOptions{}); err != nil {
							t.Fatal(err)
						}
						if got, err := tr.Find(query + "/v"); err != nil || got.Value() != "created" {
							t.Fatal("NewPath did not create a separate instance", err)
						}
					}
				})
			}
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestStoredHashXPathOpaqueFallback(t *testing.T) {
	set := opIndexSet(t, storedHashLookupSchema)
	for _, table := range []bool{false, true} {
		input := `"l":[{"k":"a","k":"b"}]`
		if table {
			input += `,"a":"a","b":"b","c":"c"`
		}
		tr, _, err := parseJSONString(set, `{"m:r":{`+input+`}}`, Reject, true)
		if err != nil {
			t.Fatal(err)
		}
		root := tr.top.At(0)
		opq := newOpaque(opaque{Name: "l"})
		mustLink(t, tr.insert(root, opq, insertDefault))
		got, err := findXPath(tr, &logger{set: set}, nil, "/m:r/l[k='a']", nil)
		if err != nil || len(got) != 1 || got[0] != opq {
			t.Fatalf("table=%t: opaque fallback = %v, %v", table, got, err)
		}
	}
}

func TestStoredHashXPathScanWork(t *testing.T) {
	set := opIndexSet(t, storedHashLookupSchema)
	expr, err := xpath.Compile("/m:l[k='a']", jsonNS{set: set})
	if err != nil {
		t.Fatal(err)
	}
	for _, order := range []string{"sorted", "reverse", "shuffled", "duplicates"} {
		t.Run(order, func(t *testing.T) {
			opIndexScaling(t, scaleSizes([]int{64, 128, 256, 512}, 3), func(n int) int64 {
				keys := make([]string, n)
				for i := range keys {
					keys[i] = fmt.Sprintf("%04d", i)
				}
				switch order {
				case "reverse":
					slices.Reverse(keys)
				case "shuffled":
					rand.New(rand.NewPCG(7, 11)).Shuffle(n, func(i, j int) { keys[i], keys[j] = keys[j], keys[i] }) //nolint:gosec // deterministic test order
				case "duplicates":
					for i := range keys {
						keys[i] = "b"
					}
				}
				var input strings.Builder
				input.WriteString(`{"m:l":[`)
				for i, key := range keys {
					if i > 0 {
						input.WriteByte(',')
					}
					fmt.Fprintf(&input, `{"k":"a","k":%q}`, key)
				}
				input.WriteString(`]}`)
				tr, _, err := parseJSONString(set, input.String(), Reject, true)
				if err != nil {
					t.Fatal(err)
				}
				ec := xpath.EvalContext{Schema: info{set: set}}
				for node := range tr.top.all() {
					ec.Tree = append(ec.Tree, xn{node, set})
				}
				got, err := expr.Eval(ec)
				if err != nil || len(got.Nodes) != 0 {
					t.Fatalf("scan = %v, %v", got, err)
				}
				for _, budget := range []int{1, n / 2} {
					ec.MaxSteps = budget
					if _, err := expr.Eval(ec); !errors.Is(err, xpath.ErrBudget) {
						t.Fatalf("budget %d: %v", budget, err)
					}
				}
				ec.MaxSteps = 0
				if retry, err := expr.Eval(ec); err != nil || len(retry.Nodes) != 0 || retry.Steps != got.Steps {
					t.Fatalf("retry = %v, %v", retry, err)
				}
				return got.Steps
			})
		})
	}
}
