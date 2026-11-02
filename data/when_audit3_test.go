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
)

const whenRewindSchema = `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 leaf keep { type string; }
 leaf z { type string; when "/m:keep and count(1) >= 0"; }
 container a { presence "a"; when "false()"; leaf c { type string; when "true()"; } }
 leaf n { type string; when "false()"; }
}`

// D-0150: duplicate entries for freed native nodes have undefined behaviour.
// The rewind skips n's earlier entry; it must not survive into the next pass.
func TestWhenDuplicateAncestorRewindTwoPasses(t *testing.T) {
	set := reviewDiffSet(t, whenRewindSchema)
	tr := parseWhenTrue(t, set, `{"m:keep":"v","m:z":"v","m:a":{"c":"v"},"m:n":"v"}`)
	keep, z, a, n := tr.top.At(0), tr.top.At(1), tr.top.At(2), tr.top.At(3)
	c := a.kids.first()
	a.flags &^= FlagWhenTrue
	vc := &valCtx{t: tr, log: &logger{set: set}, opts: ValidateOptions{MultiError: true},
		nodeWhen: &nodeSet{items: []*Node{z, c, a, n, a, n}}, nodeTypes: &nodeSet{}, budget: xpathBudget{ctx: context.Background()}}
	var deleted []*Node
	vc.diff = func(n *Node, op diffOp) error {
		if op == diffDelete {
			deleted = append(deleted, n)
		}
		return nil
	}
	if err := vc.unresWhen(); err == nil {
		t.Fatal("expected when and XPath errors")
	}
	if !slices.Equal(vc.nodeWhen.items, []*Node{z, a}) {
		t.Errorf("first pass queued %v, want [z a]", nodeNames(vc.nodeWhen.items))
	}
	if err := vc.unresWhen(); err == nil {
		t.Fatal("expected retained XPath error")
	}
	if !slices.Equal(deleted, []*Node{n}) || tr.top.At(0) != keep || z.tree != tr || tr.top.len() != 3 {
		t.Fatalf("deleted %v, roots %s", nodeNames(deleted), dumpJSON(t, tr))
	}
	assertNoTransient(t, tr)
}

func nodeNames(nodes []*Node) []string {
	var names []string
	for _, n := range nodes {
		if n == nil {
			names = append(names, "nil")
		} else {
			names = append(names, n.Name())
		}
	}
	return names
}

func TestWhenDropTopNonmember(t *testing.T) {
	set := reviewDiffSet(t, whenRewindSchema)
	tr := parseWhenTrue(t, set, `{"m:keep":"v","m:z":"v"}`)
	vc := &valCtx{t: tr}
	top, err := vc.topNodes()
	if err != nil {
		t.Fatal(err)
	}
	if err := vc.indexTop(top); err != nil {
		t.Fatal(err)
	}
	foreign := parseWhenTrue(t, set, `{"m:n":"v"}`).top.first()
	vc.dropTop(foreign)
	if vc.topDead != 0 || vc.topHead != 0 || len(vc.topPos) != 2 {
		t.Fatalf("nonmember changed view: dead=%d head=%d members=%d", vc.topDead, vc.topHead, len(vc.topPos))
	}
	first := tr.top.first()
	vc.dropTop(first)
	vc.dropTop(first)
	if vc.topDead != 1 || vc.topHead != 1 || len(vc.topPos) != 1 {
		t.Fatalf("repeated removal changed view: dead=%d head=%d members=%d", vc.topDead, vc.topHead, len(vc.topPos))
	}
}

const whenOrdinarySchema = `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 leaf keep { type string; when "true()"; }
 container gone { presence "gone"; when "false()";
  leaf-list x { config false; type string; must "false()"; }
 }
}`

func whenOrdinaryInput(n int) string {
	var b strings.Builder
	b.WriteString(`{"m:keep":"v","m:gone":{"x":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"%d"`, i)
	}
	b.WriteString(`]}}`)
	return b.String()
}

// live>0 forces queue indexing. Refusal must precede the ordinary child stack,
// even when no descendant has a when (the payload-only tests cannot reach this).
func TestWhenOrdinaryPreflight(t *testing.T) {
	set := reviewDiffSet(t, whenOrdinarySchema)
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancel), func(t *testing.T) {
			tr := parseWhenTrue(t, set, whenOrdinaryInput(32768))
			keep, gone := tr.top.At(0), tr.top.At(1)
			ctx := &cancelAtContext{Context: context.Background()}
			tr.work.hashUsed = 0
			if cancel {
				ctx.at = 5
			} else {
				tr.work.hashLimit = 64
			}
			vc := &valCtx{t: tr, nodeWhen: &nodeSet{items: []*Node{keep, gone}}, nodeTypes: &nodeSet{}, budget: xpathBudget{ctx: ctx}}
			emitted := false
			vc.diff = func(*Node, diffOp) error { emitted = true; return nil }
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			err := (&whenPass{vc: vc, live: 2}).del(gone)
			runtime.ReadMemStats(&after)
			want := yang.ErrBudget
			if cancel {
				want = context.Canceled
			}
			if !errors.Is(err, want) || emitted || tr.top.len() != 2 || vc.nodeWhen.len() != 2 {
				t.Fatalf("preflight: %v, diff=%v roots=%d queue=%d", err, emitted, tr.top.len(), vc.nodeWhen.len())
			}
			allocated := after.TotalAlloc - before.TotalAlloc
			t.Logf("allocated=%d checks=%d hash=%d", allocated, ctx.calls, tr.work.hashUsed)
			if allocated > 64<<10 || ctx.calls > 8 {
				t.Fatalf("unreserved ordinary traversal: allocated=%d checks=%d", allocated, ctx.calls)
			}
		})
	}
}

// Check every stored queue, including duplicate metadata positions. Must checks
// have no stored queue: finalR walks the retained tree after unres.
func TestWhenDeleteAllQueues(t *testing.T) {
	set := reviewDiffSet(t, whenOrdinarySchema)
	tr := parseWhenTrue(t, set, whenOrdinaryInput(3))
	keep, gone := tr.top.At(0), tr.top.At(1)
	kids := slices.Collect(gone.Children())
	km, gm, cm := &meta{name: "keep"}, &meta{name: "gone"}, &meta{name: "child"}
	keep.meta = []*meta{km}
	gone.meta = []*meta{gm}
	kids[1].meta = []*meta{cm}
	mq := []*meta{gm, km, cm, gm, cm}
	vc := &valCtx{t: tr, log: &logger{set: set}, budget: xpathBudget{ctx: context.Background()},
		nodeWhen:  &nodeSet{items: []*Node{gone, keep, kids[1], gone, kids[1]}},
		nodeTypes: &nodeSet{items: []*Node{kids[2], keep, kids[0], kids[1]}}, metaTypes: &mq}
	p := &whenPass{vc: vc, live: 5}
	if err := p.del(gone); err != nil {
		t.Fatal(err)
	}
	p.finish()
	if !slices.Equal(vc.nodeWhen.items, []*Node{keep}) || !slices.Equal(vc.nodeTypes.items, []*Node{keep}) || !slices.Equal(mq, []*meta{km}) {
		t.Fatalf("retained when=%v types=%v meta=%v", nodeNames(vc.nodeWhen.items), nodeNames(vc.nodeTypes.items), mq)
	}
	if gone.flags&flagDead != 0 {
		t.Fatal("dead mark survived finish")
	}
	// The false must on each removed ordinary child must never run.
	if _, err := tr.Validate(context.Background(), ValidateOptions{Present: true}); err != nil {
		t.Fatal(err)
	}
	assertNoTransient(t, tr)
}

func TestWhenDeleteQueuesCancellation(t *testing.T) {
	set := reviewDiffSet(t, whenOrdinarySchema)
	eachCancelPoint(t, func(ctx *cancelAtContext) {
		tr := parseWhenTrue(t, set, whenOrdinaryInput(3))
		keep, gone := tr.top.At(0), tr.top.At(1)
		kids := slices.Collect(gone.Children())
		km, gm, cm := &meta{name: "keep"}, &meta{name: "gone"}, &meta{name: "child"}
		keep.meta = []*meta{km}
		gone.meta = []*meta{gm}
		kids[1].meta = []*meta{cm}
		mq := []*meta{gm, km, cm, gm, cm}
		whens := []*Node{gone, keep, kids[1], gone, kids[1]}
		vc := &valCtx{t: tr, log: &logger{set: set}, budget: xpathBudget{ctx: ctx},
			nodeWhen: &nodeSet{items: slices.Clone(whens)}, nodeTypes: &nodeSet{items: slices.Clone(kids)}, metaTypes: &mq}
		emitted := false
		vc.diff = func(*Node, diffOp) error { emitted = true; return nil }
		p := &whenPass{vc: vc, live: len(whens)}
		err := p.del(gone)
		p.finish()
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if emitted {
			if err != nil || tr.top.len() != 1 || !slices.Equal(mq, []*meta{km}) || !slices.Equal(vc.nodeWhen.items, []*Node{keep}) || vc.nodeTypes.len() != 0 {
				t.Fatalf("checkpoint %d: incomplete deletion: %v", ctx.at, err)
			}
		} else if tr.top.len() != 2 || !slices.Equal(mq, []*meta{gm, km, cm, gm, cm}) || !slices.Equal(vc.nodeWhen.items, whens) || !slices.Equal(vc.nodeTypes.items, kids) {
			t.Fatalf("checkpoint %d: cancelled preflight changed queues", ctx.at)
		}
		if gone.flags&flagDead != 0 {
			t.Fatal("dead mark survived finish")
		}
	})
}
