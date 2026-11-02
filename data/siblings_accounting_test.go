// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

// This test uses only main's APIs. Run the identical file on both revisions to compare exact
// allocation counts and work units for descending duplicate-heavy links, with both journal
// modes. The measured batch includes parse, reservation, link and traversal.
func TestSiblingFacadeOrdinaryAccounting(t *testing.T) {
	set := round13Set(t)
	for _, journaled := range []bool{false, true} {
		for _, count := range []int{64, 256} {
			t.Run(fmt.Sprintf("%t/%d", journaled, count), func(t *testing.T) {
				var visits int64
				var hashes uint64
				allocs := testing.AllocsPerRun(3, func() {
					tr, op := round13RPC(t, set, `{}`)
					tr.work.visits = true
					tr.work.Store(0)
					tr.work.hashUsed = 0
					var j *hashTransaction
					if journaled {
						j = beginHashTransaction(tr)
					}
					var err error
					for i := count - 1; i >= 0; i-- {
						if _, err = tr.NewTerm(op, "v", strconv.Itoa(i%17), NewOptions{}); err != nil {
							break
						}
					}
					j.end(&err)
					if err != nil {
						t.Fatal(err)
					}
					seen := 0
					for range op.All() {
						seen++
					}
					if seen != count+1 {
						t.Fatal("missing child")
					}
					visits, hashes = tr.work.Load(), tr.work.hashUsed
				})
				wantVisits, wantHash := int64(3491), uint64(122)
				if count == 256 {
					wantVisits, wantHash = 19927, 158
				}
				if journaled {
					wantHash = 8035
					if count == 256 {
						wantHash = 40796
					}
				}
				if visits != wantVisits || hashes != wantHash {
					t.Fatalf("work changed: visits=%d hash=%d", visits, hashes)
				}
				t.Logf("visits=%d hash=%d allocations=%.2f", visits, hashes, allocs)
			})
		}
	}
}

// Measure the key preflight and successful/unsuccessful keyLinked paths independently of the
// leaf-list workload above. Identical on main: repeated single, first and last keys exercise
// running hashes and incomplete prefixes, with all work and allocations included.
func TestSiblingFacadeKeyAccounting(t *testing.T) {
	for _, mode := range []string{"single", "first", "last"} {
		t.Run(mode, func(t *testing.T) {
			module, key, prefix, suffix := "pv2-sorted", "k", "", ""
			valid := mode != "first"
			if mode != "single" {
				module = "pv2-sort-keys"
				if mode == "first" {
					key, suffix = "a", `,"b":"x"`
				} else {
					key, prefix = "b", `"a":"q",`
				}
			}
			set := pv2Set(t, module)
			var previousVisits int64
			var previousHash uint64
			var previousAllocs float64
			for _, count := range []int{64, 256} {
				member := fmt.Sprintf(`%q:"q"`, key)
				input := `{"` + module + `:c":{"l":[{` + prefix + strings.Repeat(member+",", count-1) + member + suffix + `}]}}`
				var visits int64
				var hashes uint64
				allocs := testing.AllocsPerRun(3, func() {
					var parsed *Tree
					_, diags, err := parseWith(context.Background(), strings.NewReader(input), set,
						parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, func(lc *lydCtx) {
							parsed = lc.tree
							parsed.work.visits = true
						})
					if (err == nil) != valid || parsed == nil {
						t.Fatalf("parse valid=%t: %v %v", valid, err, diags)
					}
					visits, hashes = parsed.work.Load(), parsed.work.hashUsed
				})
				if previousVisits != 0 && (visits > 6*previousVisits || hashes > 6*previousHash || allocs > 6*previousAllocs) {
					t.Fatal("key work/allocation growth regressed")
				}
				t.Logf("n=%d visits=%d hash=%d allocations=%.2f", count, visits, hashes, allocs)
				previousVisits, previousHash, previousAllocs = visits, hashes, allocs
			}
		})
	}
}

// appendNode has always captured the whole set, even inside a narrow transaction. It is used
// by payload adoption and XPath probes as well as storage helpers; sharing InsertAt's narrow
// path changes its ordinary work/capture contract. Keep this test runnable on main.
func TestSiblingFacadeAppendAccounting(t *testing.T) {
	for _, narrow := range []bool{false, true} {
		for _, count := range []int{64, 256} {
			t.Run(fmt.Sprintf("%t/%d", narrow, count), func(t *testing.T) {
				tr := newTree(nil)
				for range count {
					mustLink(t, tr.top.appendNode(&Node{schema: &schema.Node{}}))
				}
				mustLink(t, tr.top.appendNode(&Node{}))
				tr.top.setWork(&tr.work)
				tr.work.visits = true
				tr.work.Store(0)
				var j *hashTransaction
				if narrow {
					j = beginNarrowTransaction(tr)
				} else {
					j = beginHashTransaction(tr)
				}
				defer func() { err := yang.ErrBudget; j.end(&err) }()
				mustLink(t, tr.top.appendNode(&Node{schema: &schema.Node{}}))
				if _, ok := j.siblings[&tr.top]; !ok {
					t.Fatal("append lost main's whole capture")
				}
				t.Logf("n=%d visits=%d hash=%d", count, tr.work.Load(), tr.work.hashUsed)
			})
		}
	}
}
