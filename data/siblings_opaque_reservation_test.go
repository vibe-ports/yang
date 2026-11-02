// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/types"
)

// Dup preserves the source's captured key prefix even though the target declares another
// key. Four identical copies make the destination index compare the physical child prefix.
func opaqueReservationTree(t *testing.T) (*Tree, *Node) {
	t.Helper()
	srcSet := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
	  container r { list l { key a; leaf a { type string; } } } }`)
	dstSet := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
	  container r { list l { key "a b"; leaf a { type string; } leaf b { type string; } } } }`)
	src, diags, err := parseJSONString(srcSet, `{"m:r":{"l":[{"a":"x"}]}}`, Reject, true)
	if err != nil {
		t.Fatal(err, diags)
	}
	dst := newTree(dstSet)
	root, err := dst.NewInner(nil, "m:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var list *Node
	for range 4 {
		list, _, err = dst.Dup(src.top.first().kids.At(0), root, DupOptions{Recursive: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	if root.kids.ht == nil || list.keyN != 1 || !list.lkeyOK {
		t.Fatal("fixture needs indexed, incomplete copied lists with a captured prefix")
	}
	return dst, list
}

func TestSiblingPlacementOpaqueBeforeOpaqueReservation(t *testing.T) {
	if !overdraftPanics {
		t.Fatal("test builds must panic on an overdrawn credit")
	}
	for _, journaled := range []bool{false, true} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("journal=%t/legacy=%t", journaled, legacy), func(t *testing.T) {
				tr, list := opaqueReservationTree(t)
				old := newOpaque(opaque{Name: "old", Value: "x", Format: types.FormatJSON})
				mustLink(t, tr.link(list, &list.kids, old, -1))
				before := snapshotHashBudgetTree(tr)
				n := newOpaque(opaque{Name: "new", Value: strings.Repeat("x", 4096), Format: types.FormatJSON})
				done := tr.work.beginHashWork()
				defer done()
				var j *hashTransaction
				if journaled {
					j = beginHashTransaction(tr)
					defer func() { err := yang.ErrBudget; j.end(&err) }()
					// Raw link's detached input has no governing journal yet; relocation callers
					// capture its source before linking it into the destination.
					mustLink(t, j.captureNode(n))
				}
				p := list.kids.physicalPlacement(n, old)
				if legacy {
					p = list.kids.linkPlacement(n, 0)
				}
				if p.docAt != 1 || list.kids.linkMixes(n, p) {
					t.Fatal("fixture must insert before the opaque tail without interleaving")
				}
				bound := tr.linkIndexPlacedBound(list, &list.kids, n, p)
				t.Logf("discriminator reservation=%d, want 8258", bound)
				amount, err := tr.linkPlacedBound(list, &list.kids, n, p)
				if err != nil {
					t.Fatal(err)
				}
				mustLink(t, tr.reserveHash(amount))
				mustLink(t, tr.linkPlaced(list, &list.kids, n, p))
				if bound != 8258 || bound != indexWorkBound(list, nil, nil, &tr.work) {
					t.Fatalf("reservation %d disagrees with the inserted prefix", bound)
				}
				if list.kids.docSpan().At(1) != n || list.kids.docSpan().At(2) != old || list.kids.st.interleaved() {
					t.Fatal("link disagrees with the resolved placement")
				}
				if want, ok := hashOf(list); !ok || list.hkey != want {
					t.Fatal("compared parent retained a stale discriminator")
				}
				if journaled {
					err := yang.ErrBudget
					j.end(&err)
					j = nil
					if !reflect.DeepEqual(snapshotHashBudgetTree(tr), before) || n.parent != nil {
						t.Fatal("rollback did not restore the original prefix and index")
					}
				}
			})
		}
	}
}

// Uses the legacy link entry point so this exact test also runs on origin/main. Repeated
// opaque appends and prepends must bound only the two compared positions, regardless of tail length.
func TestSiblingOpaqueReservationWork(t *testing.T) {
	for _, at := range []int{-1, 0} {
		for _, count := range []int{64, 256, 1024} {
			t.Run(fmt.Sprintf("at=%d/n=%d", at, count), func(t *testing.T) {
				tr, list := opaqueReservationTree(t)
				tr.work.visits = true
				tr.work.Store(0)
				tr.work.hashUsed = 0
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				for range count {
					n := newOpaque(opaque{Name: "repeat", Value: "x", Format: types.FormatJSON})
					amount, err := tr.linkBound(list, &list.kids, n, at)
					if err != nil {
						t.Fatal(err)
					}
					mustLink(t, tr.reserveHash(amount))
					mustLink(t, tr.link(list, &list.kids, n, at))
				}
				runtime.ReadMemStats(&after)
				work := tr.work.Load()
				t.Logf("n=%d visits/shifts=%d hash=%d bytes=%d", count, work, tr.work.hashUsed, after.TotalAlloc-before.TotalAlloc)
				if work > int64(128*count) || tr.work.hashUsed > uint64(512*count) { //nolint:gosec // fixed positive counts
					t.Fatal("opaque tail caused excessive reservation/link work")
				}
				if err := tr.PrintJSON(io.Discard, PrintOptions{Shrink: true}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
