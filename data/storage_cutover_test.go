// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

// TestRBPublishTableHistory (design 14, attach replay): rbPublish links its batch at once but
// attaches the nodes one by one, as libyang links them. A children table created during the
// replay is built from the siblings attached so far, in sibling order; the later nodes register
// after them in attach order. Every existing-sibling count around the table threshold, every
// attach order of the batch.
func TestRBPublishTableHistory(t *testing.T) {
	for existing := 1; existing < htMinItems; existing++ { // rbPublish publishes beside a sibling
		for _, perm := range [][]int{{0, 1, 2, 3}, {3, 2, 1, 0}, {2, 0, 3, 1}} {
			t.Run(fmt.Sprint(existing, perm), func(t *testing.T) {
				f := newFixture()
				tr := newTree(f.set)
				parent := newInner(f.c)
				mustLink(t, tr.insert(nil, parent, insertDefault))
				var old []*Node
				for i := range existing {
					n := f.term(t, f.ul, fmt.Sprint(i))
					mustLink(t, tr.insert(parent, n, insertLast))
					old = append(old, n)
				}
				sib := &parent.kids
				if sib.ht != nil {
					t.Fatal("the fixture already has a table")
				}
				sorted := make([]*Node, len(perm))
				for i := range sorted {
					sorted[i] = f.term(t, f.ll, fmt.Sprint(10+i))
				}
				d, _ := newDirtyOrder(&tr.work, spanOf(sorted), tr.compareSorted, true)
				attach := make([]*Node, len(perm))
				for i, p := range perm {
					attach[i] = sorted[p]
				}
				mustLink(t, tr.rbPublish(parent, sib, 0, 0, &rbGroup{}, d, attach))
				if err := storeCheckInvariant(sib); err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(sib.CollectRange(0, sib.Len()), append(slices.Clone(sorted), old...)) {
					t.Fatal("published order")
				}
				// the expected registration order
				created := htMinItems - existing - 1 // the attach that reaches the threshold
				if created >= len(attach) {
					if sib.ht != nil {
						t.Fatal("a table below the threshold")
					}
					return
				}
				visible := map[*Node]bool{}
				for _, n := range old {
					visible[n] = true
				}
				for _, n := range attach[:created+1] {
					visible[n] = true
				}
				var want []*Node
				for _, n := range sib.SchemaAll() {
					if visible[n] {
						want = append(want, n)
					}
				}
				want = append(want, attach[created+1:]...)
				got := sib.CollectRange(0, sib.Len())
				sort.SliceStable(got, func(i, j int) bool {
					return sib.history[got[i]][got[i].hash].order < sib.history[got[j]][got[j].hash].order
				})
				if !slices.Equal(got, want) {
					t.Fatalf("registration order %v, want %v", valueNames(got), valueNames(want))
				}
			})
		}
	}
}

func valueNames(ns []*Node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.value.Canonical()
	}
	return out
}

// cancelAt is a context whose Err cancels from its k-th call on.
type cancelAt struct {
	context.Context
	polls, k int
}

func (c *cancelAt) Err() error {
	c.polls++
	if c.k > 0 && c.polls >= c.k {
		return context.Canceled
	}
	return nil
}

// TestParserPublicationFunded (design 14, completion funding): a deferred run's publication
// draws on the credit its members paid as they were parsed, so at no budget, cancellation point
// or parse error does publishing refuse (publishDirty panics in test builds if it does), and a
// failed parse still leaves every run it published in value order.
func TestParserPublicationFunded(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	  container c { leaf-list v { type uint32; } leaf-list w { type uint32; } leaf x { type string; } }
	  rpc r { input { leaf-list v { type uint32; } leaf-list w { type uint32; } } } }`)
	check := func(t *testing.T, where string, tr *Tree) {
		t.Helper()
		if tr == nil {
			return
		}
		opidxAssertTreeInvariant(t, tr)
		checkTreeRetainedRB(t, tr)
		for root := range tr.top.all() {
			var last *Node
			for _, n := range root.kids.SchemaAll() {
				if last != nil && last.schema == n.schema && n.schema.Kind == schema.LeafList &&
					n.value.Canonical() < last.value.Canonical() {
					t.Fatalf("%s: run of %s left out of value order", where, n.schema.Name)
				}
				last = n
			}
		}
	}
	for _, tc := range []struct {
		name, input string
		op          bool
	}{
		{"data", `{"m:c":{"v":[5,3,9,1,7],"w":[4,2,8],"x":"y"}}`, false},
		{"operation", `{"m:r":{"v":[5,3,9,1,7],"w":[4,2,8]}}`, true},
	} {
		parse := func(ctx context.Context, input string, limit uint64) (*Tree, error) {
			var tr *Tree
			fp := parseJSON
			o := parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}
			_, _, err := parseWith(ctx, strings.NewReader(input), set, o, fp, func(lc *lydCtx) {
				if tc.op { // parseOpWith's options of an rpc
					lc.op, lc.rcOnly, lc.dataType = opOpts{rpc: true, action: true, noSiblings: true}, true, opRPC
				}
				lc.tree.work.hashLimit = limit
				tr = lc.tree
			})
			return tr, err
		}
		t.Run(tc.name, func(t *testing.T) {
			benign := &cancelAt{Context: context.Background()}
			tr, err := parse(benign, tc.input, 0)
			if err != nil {
				t.Fatal(err)
			}
			check(t, "benign", tr)
			need := tr.work.hashUsed
			for limit := uint64(1); limit <= need; limit++ {
				tr, err := parse(context.Background(), tc.input, limit)
				if err != nil && !errors.Is(err, yang.ErrBudget) {
					t.Fatalf("limit %d: %v", limit, err)
				}
				check(t, fmt.Sprint("limit ", limit), tr)
			}
			for k := 1; k <= benign.polls; k++ {
				tr, err := parse(&cancelAt{Context: context.Background(), k: k}, tc.input, 0)
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel at %d: %v", k, err)
				}
				check(t, fmt.Sprint("cancel at ", k), tr)
			}
			for cut := len(tc.input) - 1; cut > 0; cut -= 3 { // a parse error after any prefix
				tr, _ := parse(context.Background(), tc.input[:cut], 0)
				check(t, fmt.Sprint("cut at ", cut), tr)
			}
		})
	}
}

// TestStorageMiddleEditScaling (design 14, the #240 criterion): positional insertions
// (lyd_insert_after, behind ApplyDiff's positional insert) and removals in the middle of a large
// user-ordered run cost O(log n + L) each, counted work and charged hash work alike: in an
// ordinary set (unlabeled storage) and in an operation's input (labeled, indexed), outside a
// journal and as many edits of one journaled state (its whole capture of the set's compatibility
// state is paid once; per-entry capture is step 6). Quadrupling n twice may not even triple the
// per-edit work.
func TestStorageMiddleEditScaling(t *testing.T) {
	ordinary := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	  container c { leaf-list v { type uint32; ordered-by user; } }
	  rpc r { input { leaf-list v { type uint32; ordered-by user; } } } }`)
	const edits = 128
	measure := func(t *testing.T, op, journaled bool, n int) (work, hash float64) {
		var tr *Tree
		var parent *Node
		if op {
			tr, parent = opIndexParseRPC(t, ordinary, `{"m:r":{"v":[`+opIndexValues(n, false)+`]}}`)
		} else {
			var err error
			tr, _, err = parseJSONString(ordinary, `{"m:c":{"v":[`+opIndexValues(n, false)+`]}}`, Reject, true)
			if err != nil {
				t.Fatal(err)
			}
			parent = tr.top.first()
		}
		sib := &parent.kids
		fresh := make([]*Node, edits)
		for i := range fresh {
			fresh[i] = newTerm(sib.At(0).schema, sib.At(0).value)
		}
		var j *hashTransaction
		if journaled {
			j = beginHashTransaction(tr)
			mustLink(t, sib.touch()) // the state's one whole capture, before the measurement
		}
		end := tr.work.beginHashWork()
		tr.work.Store(0)
		tr.work.visits = true
		used := tr.work.hashUsed
		for i, n := range fresh {
			if err := tr.insertAfter(sib.At(sib.Len()/2), n); err != nil {
				t.Fatal(err)
			}
			if i%2 == 1 {
				if err := unlinkTree(sib.At(sib.Len() / 3)); err != nil {
					t.Fatal(err)
				}
			}
		}
		work, hash = float64(tr.work.Load())/edits, float64(tr.work.hashUsed-used)/edits
		end()
		if j != nil {
			var err error
			j.end(&err)
		}
		opidxAssertTreeInvariant(t, tr)
		return work, hash
	}
	for _, op := range []bool{false, true} {
		for _, journaled := range []bool{false, true} {
			t.Run(fmt.Sprintf("op=%t/journal=%t", op, journaled), func(t *testing.T) {
				small, smallHash := measure(t, op, journaled, 1024)
				large, largeHash := measure(t, op, journaled, raceScale(16384, 4))
				t.Logf("per edit: work %.0f -> %.0f, hash %.0f -> %.0f", small, large, smallHash, largeHash)
				if large > 3*small || largeHash > 3*smallHash {
					t.Fatalf("per-edit work grew from %.0f to %.0f, hash from %.0f to %.0f", small, large, smallHash, largeHash)
				}
			})
		}
	}
}
