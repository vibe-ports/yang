// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

// The retained values really have incomplete leafref types; only gone is deleted.
// Keep setup compatible with origin/main for the counted-work comparison.
func retainedTypeQueue(t *testing.T, size int) (*valCtx, *Node) {
	t.Helper()
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 leaf gone { type leafref { path "/m:target"; } when "false()"; }
 leaf target { type string; }
 leaf-list r { type leafref { path "/m:target"; } }
 }`)
	var b strings.Builder
	b.WriteString(`{"m:gone":"gone","m:r":[`)
	for i := range size {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"%d"`, size-i) // adverse insertion order
	}
	b.WriteString(`]}`)
	tr, _, err := parseWith(context.Background(), strings.NewReader(b.String()), set,
		parseOpts{ParseOptions: ParseOptions{Unknown: Opaque, ParseOnly: true, WhenTrue: true}, whenTrue: true}, parseJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	gone := tr.top.first()
	q := &nodeSet{}
	for n := range tr.top.all() {
		q.add(n)
	}
	tr.work.hashUsed = 0
	return &valCtx{t: tr, log: &logger{set: set}, nodeWhen: &nodeSet{items: []*Node{gone}}, nodeTypes: q,
		budget: xpathBudget{ctx: context.Background()}}, gone
}

// #343: one deleted terminal must not hide an unreserved, unpolled scan of a
// large retained type queue behind its diff. Cancellation lands inside that
// index builder, and a failed builder must not publish a partial position map.
func TestWhenRetainedTypeQueuePreflight(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancel), func(t *testing.T) {
			vc, gone := retainedTypeQueue(t, 32768)
			before := dumpJSON(t, vc.t)
			queued := slices.Clone(vc.nodeTypes.items)
			flags := gone.flags
			ctx := &cancelAtContext{Context: context.Background()}
			vc.budget.ctx = ctx
			if cancel {
				ctx.at = 128
			} else {
				vc.t.work.hashLimit = 128
			}
			emitted := false
			vc.diff = func(*Node, diffOp) error { emitted = true; return nil }
			p := &whenPass{vc: vc, live: 1}
			err := p.del(gone)
			p.finish()
			want := yang.ErrBudget
			if cancel {
				want = context.Canceled
			}
			if !errors.Is(err, want) || emitted || gone.tree != vc.t || gone.flags != flags || vc.nodeWhen.len() != 1 ||
				vc.nodeWhen.items[0] != gone || !slices.Equal(vc.nodeTypes.items, queued) || vc.nodeTypes.pos != nil || dumpJSON(t, vc.t) != before {
				t.Fatalf("refusal: err=%v diff=%v scans=%d roots=%d whens=%d", err, emitted, vc.nodeTypes.scans, vc.t.top.len(), vc.nodeWhen.len())
			}
			if cancel && (vc.nodeTypes.scans == 0 || vc.nodeTypes.scans >= len(queued)) {
				t.Fatalf("cancellation missed type index construction: scans=%d", vc.nodeTypes.scans)
			}
		})
	}
}

func TestWhenRetainedTypeQueueCountedWork(t *testing.T) {
	var previous uint64
	for _, size := range []int{512, 4096} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			vc, gone := retainedTypeQueue(t, size)
			ctx := &cancelAtContext{Context: context.Background()}
			vc.budget.ctx = ctx
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			if err := vc.unresWhen(); err != nil {
				t.Fatal(err)
			}
			runtime.ReadMemStats(&after)
			used := vc.t.work.hashUsed
			t.Logf("n=%d hash=%d bytes=%d checks=%d type-scans=%d", size, used, after.TotalAlloc-before.TotalAlloc, ctx.calls, vc.nodeTypes.scans)
			if gone.tree != nil || vc.nodeWhen.len() != 0 || vc.nodeTypes.len() != size || vc.nodeTypes.scans != size+1 {
				t.Fatal("deletion/type index not reached")
			}
			if previous != 0 && used > 9*previous {
				t.Fatal("nonlinear retained-queue work")
			}
			previous = used
		})
	}
}

// A successful when needs no deletion index, but finish still scans its whole
// queue. Reserve and poll that scan before resolving even its first entry.
func TestWhenFinishQueuePreflight(t *testing.T) {
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m; leaf keep { type string; when "true()"; } }`)
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancel), func(t *testing.T) {
			tr := parseWhenTrue(t, set, `{"m:keep":"v"}`)
			keep := tr.top.first()
			keep.flags &^= FlagWhenTrue
			flags := keep.flags
			q := &nodeSet{items: make([]*Node, 32768)}
			for i := range q.items {
				q.items[i] = keep
			}
			ctx := &cancelAtContext{Context: context.Background()}
			if cancel {
				ctx.at = 128
			} else {
				tr.work.hashLimit = 128
			}
			tr.work.hashUsed = 0
			vc := &valCtx{t: tr, nodeWhen: q, log: &logger{set: set}, budget: xpathBudget{ctx: ctx}}
			err := vc.unresWhen()
			want := yang.ErrBudget
			if cancel {
				want = context.Canceled
			}
			if !errors.Is(err, want) || keep.flags != flags || q.len() != 32768 {
				t.Fatalf("refusal: %v flags=%v queue=%d", err, keep.flags, q.len())
			}
		})
	}
}

// One deleted referrer and many retained referrers of its target. The cleanup
// index used to be built (or the whole retained side scanned) after the diff.
func TestWhenRetainedLinkIndexPreflight(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancel), func(t *testing.T) {
			vc, gone := retainedTypeQueue(t, 4096)
			target := vc.nodeTypes.items[1]
			for _, n := range vc.nodeTypes.items {
				if n != target {
					if err := linkLeafrefNode(target, n); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Isolate link-index planning from the separately tested type-position index.
			vc.nodeTypes = nil
			target.links.leafrefs = append(target.links.leafrefs[1:], gone) // the isolated scan reaches the tail
			refs := slices.Clone(target.links.leafrefs)
			ctx := &cancelAtContext{Context: context.Background()}
			vc.budget.ctx = ctx
			vc.t.work.hashUsed = 0
			if cancel {
				ctx.at = 128
			} else {
				vc.t.work.hashLimit = 128
			}
			emitted := false
			vc.diff = func(*Node, diffOp) error { emitted = true; return nil }
			p := &whenPass{vc: vc, live: 1}
			err := p.del(gone)
			p.finish()
			want := yang.ErrBudget
			if cancel {
				want = context.Canceled
			}
			if !errors.Is(err, want) || emitted || gone.tree != vc.t || gone.links == nil || !slices.Equal(target.links.leafrefs, refs) {
				t.Fatalf("refusal: %v diff=%v checks=%d hash=%d", err, emitted, ctx.calls, vc.t.work.hashUsed)
			}
		})
	}
}

func TestDropTypesRetainedQueuePreflight(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancel), func(t *testing.T) {
			vc, gone := retainedTypeQueue(t, 4096)
			before := slices.Clone(vc.nodeTypes.items)
			ctx := &cancelAtContext{Context: context.Background()}
			vc.budget.ctx = ctx
			if cancel {
				ctx.at = 128
			} else {
				vc.t.work.hashLimit = 128
			}
			err := vc.dropTypes(gone, true)
			want := yang.ErrBudget
			if cancel {
				want = context.Canceled
			}
			if !errors.Is(err, want) || !slices.Equal(vc.nodeTypes.items, before) || vc.nodeTypes.pos != nil {
				t.Fatalf("refusal: %v scans=%d", err, vc.nodeTypes.scans)
			}
		})
	}
}

// The other deletion caller must also prepare the type positions before its
// diff; dropTypes' non-cancellable suffix must only read a completed index.
func TestAutodelRetainedTypeQueuePreflight(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancel), func(t *testing.T) {
			vc, gone := retainedTypeQueue(t, 4096)
			before := dumpJSON(t, vc.t)
			queued := slices.Clone(vc.nodeTypes.items)
			ctx := &cancelAtContext{Context: context.Background()}
			vc.budget.ctx = ctx
			if cancel {
				ctx.at = 128
			} else {
				vc.t.work.hashLimit = 128
			}
			emitted := false
			vc.diff = func(*Node, diffOp) error { emitted = true; return nil }
			err := vc.autodel([]*Node{gone}, true)
			want := yang.ErrBudget
			if cancel {
				want = context.Canceled
			}
			if !errors.Is(err, want) || emitted || gone.tree != vc.t ||
				!slices.Equal(vc.nodeTypes.items, queued) || vc.nodeTypes.pos != nil || dumpJSON(t, vc.t) != before {
				t.Fatalf("refusal: err=%v diff=%v scans=%d", err, emitted, vc.nodeTypes.scans)
			}
			if cancel && (vc.nodeTypes.scans == 0 || vc.nodeTypes.scans >= len(queued)) {
				t.Fatalf("cancellation missed type index construction: scans=%d", vc.nodeTypes.scans)
			}
		})
	}
}

func TestAutodelRetainedTypeQueueCountedWork(t *testing.T) {
	var previous uint64
	for _, size := range []int{512, 4096} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			vc, gone := retainedTypeQueue(t, size)
			ctx := &cancelAtContext{Context: context.Background()}
			vc.budget.ctx = ctx
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			if err := vc.autodel([]*Node{gone}, true); err != nil {
				t.Fatal(err)
			}
			runtime.ReadMemStats(&after)
			used := vc.t.work.hashUsed
			t.Logf("n=%d hash=%d bytes=%d checks=%d type-scans=%d", size, used, after.TotalAlloc-before.TotalAlloc, ctx.calls, vc.nodeTypes.scans)
			if gone.tree != nil || vc.nodeTypes.len() != size || vc.nodeTypes.scans != size+1 {
				t.Fatal("autodeletion/type index not reached")
			}
			if previous != 0 && used > 9*previous {
				t.Fatal("nonlinear retained-queue work")
			}
			previous = used
		})
	}
}

func TestWhenRetainedLinkCountedWork(t *testing.T) {
	var previous uint64
	for _, size := range []int{512, 4096} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			vc, gone := retainedTypeQueue(t, size)
			target := vc.nodeTypes.items[1]
			for _, n := range vc.nodeTypes.items {
				if n != target {
					if err := linkLeafrefNode(target, n); err != nil {
						t.Fatal(err)
					}
				}
			}
			vc.nodeTypes = nil
			ctx := &cancelAtContext{Context: context.Background()}
			vc.budget.ctx = ctx
			vc.t.work.hashUsed = 0
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			if err := vc.unresWhen(); err != nil {
				t.Fatal(err)
			}
			runtime.ReadMemStats(&after)
			used := vc.t.work.hashUsed
			t.Logf("n=%d hash=%d bytes=%d checks=%d", size, used, after.TotalAlloc-before.TotalAlloc, ctx.calls)
			if gone.tree != nil || gone.links != nil || len(target.links.leafrefs) != size-1 {
				t.Fatal("link cleanup not reached")
			}
			if previous != 0 && used > 9*previous {
				t.Fatal("nonlinear retained-link work")
			}
			previous = used
		})
	}
}

// No terminal means no type lookup. A large unrelated queue must stay untouched.
func TestAutodelEmptySkipsTypeIndex(t *testing.T) {
	vc, _ := retainedTypeQueue(t, 4096)
	empty := newInner(&schema.Node{Kind: schema.Container, Name: "empty", Presence: true, Module: vc.t.set.Implemented("m")})
	if err := vc.dropTypes(empty, true); err != nil {
		t.Fatal(err)
	}
	if err := vc.autodel([]*Node{empty}, true); err != nil {
		t.Fatal(err)
	}
	if vc.nodeTypes.pos != nil || vc.nodeTypes.scans != 0 {
		t.Fatalf("indexed unrelated values: scans=%d", vc.nodeTypes.scans)
	}
}

// Two removed referrers share a large retained side. Cancellation must reach
// the full index built for the second removal, before either record changes.
func TestWhenRepeatedLinkIndexPreflight(t *testing.T) {
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 container gone { presence p; when "false()";
  leaf a { type leafref { path "/m:target"; } }
  leaf b { type leafref { path "/m:target"; } }
 }
 leaf target { type string; }
 leaf-list r { config false; type leafref { path "/m:target"; } }
 }`)
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancel), func(t *testing.T) {
			const size = 4096
			input := `{"m:gone":{"a":"v","b":"v"},"m:target":"v","m:r":[` + strings.TrimSuffix(strings.Repeat(`"v",`, size), ",") + `]}`
			tr := parseWhenTrue(t, set, input)
			gone, target := tr.top.At(0), tr.top.At(1)
			for n := range gone.Children() {
				if err := linkLeafrefNode(target, n); err != nil {
					t.Fatal(err)
				}
			}
			for n := range tr.top.all() {
				if n.Name() == "r" {
					if err := linkLeafrefNode(target, n); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := slices.Clone(target.links.leafrefs)
			ctx := &cancelAtContext{Context: context.Background()}
			tr.work.hashUsed = 0
			if cancel {
				ctx.at = 128
			} else {
				tr.work.hashLimit = 2*size + 128
			}
			vc := &valCtx{t: tr, nodeWhen: &nodeSet{items: []*Node{gone}}, budget: xpathBudget{ctx: ctx}}
			emitted := false
			vc.diff = func(*Node, diffOp) error { emitted = true; return nil }
			p := &whenPass{vc: vc, live: 1}
			err := p.del(gone)
			p.finish()
			want := yang.ErrBudget
			if cancel {
				want = context.Canceled
			}
			if !errors.Is(err, want) || emitted || gone.tree != tr || !slices.Equal(target.links.leafrefs, before) {
				t.Fatalf("refusal: %v diff=%v checks=%d hash=%d", err, emitted, ctx.calls, tr.work.hashUsed)
			}
		})
	}
}
