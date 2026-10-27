// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

// Stage-3 stabilization: every refused mutation leaves its trees and inputs as they were, and
// the next mutation on the same objects behaves exactly as if the refusal never happened.

const stage3StabModule = `module stage3-stab {yang-version 1.1;namespace "urn:stage3-stab";prefix s;
 container a {
  list l {key k;leaf k {type uint32;}leaf p {type string;}}
  leaf-list v {type string;}
  leaf r {type leafref {path "../l/k";}}
 }
 rpc op {input {leaf i {type string;}} output {leaf o {type string;} leaf-list w {type string;}}}}`

func stage3StabTree(t *testing.T, set *schema.Set, keys []string, ref string) *Tree {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<a xmlns="urn:stage3-stab">`)
	for _, k := range keys {
		b.WriteString("<l><k>" + k + "</k><p>" + k + "</p></l>")
	}
	for _, v := range []string{"m", "c", "x"} {
		b.WriteString("<v>" + v + "</v>")
	}
	if ref != "" {
		b.WriteString("<r>" + ref + "</r>")
	}
	b.WriteString("</a>")
	return parseOnlyXML(t, set, b.String())
}

func stage3StabList(tr *Tree, key string) *Node {
	for _, n := range tr.top.first().kids.SchemaAll() {
		if n.schema.Name == "l" && n.kids.first().Value() == key {
			return n
		}
	}
	return nil
}

// stage3FailThenRetry runs op at every hash-work limit below what it needs on the tree chosen
// by limited. Each refusal must leave all trees unchanged (topology, hash records, metadata,
// reciprocal links); retrying without a limit and then running next must give exactly the
// trees an unrefused run gives.
func stage3FailThenRetry(t *testing.T, name string, build func() (trees []*Tree, op func() error),
	limited int, next func(t *testing.T, trees []*Tree)) {
	t.Helper()
	trees, op := build()
	var points []uint64
	trees[limited].work.hashTrace = &points
	if err := op(); err != nil {
		t.Fatalf("%s: unlimited: %v", name, err)
	}
	trees[limited].work.hashTrace = nil
	need := trees[limited].work.hashUsed
	next(t, trees)
	want := make([][]string, len(trees))
	for i, tr := range trees {
		want[i] = typedDump(tr)
	}
	if need < 2 {
		t.Fatalf("%s: unlimited run charged %d units", name, need)
	}
	// every limit below the work used, and every reservation check above it
	limits := map[uint64]bool{}
	for limit := uint64(1); limit < need; limit++ {
		limits[limit] = true
	}
	for _, at := range points {
		if at > 1 {
			limits[at-1] = true
		}
	}
	for limit := range limits {
		trees, op := build()
		before := make([]hashBudgetSnapshot, len(trees))
		for i, tr := range trees {
			before[i] = snapshotHashBudgetTree(tr)
		}
		trees[limited].work.hashLimit = limit
		err := op()
		if err == nil && limit >= need {
			trees[limited].work.hashLimit = 0
			next(t, trees)
			for i, tr := range trees {
				if got := typedDump(tr); !reflect.DeepEqual(got, want[i]) {
					t.Fatalf("%s: limit %d: tree %d differs from the unlimited run", name, limit, i)
				}
			}
			continue
		}
		if !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("%s: limit %d/%d: %v, want ErrBudget", name, limit, need, err)
		}
		for i, tr := range trees {
			if !reflect.DeepEqual(snapshotHashBudgetTree(tr), before[i]) {
				t.Fatalf("%s: limit %d/%d: refusal changed tree %d", name, limit, need, i)
			}
			checkTreeRetainedRB(t, tr)
		}
		trees[limited].work.hashLimit = 0
		if err := op(); err != nil {
			t.Fatalf("%s: limit %d: retry: %v", name, limit, err)
		}
		next(t, trees)
		for i, tr := range trees {
			checkTreeRetainedRB(t, tr)
			if got := typedDump(tr); !reflect.DeepEqual(got, want[i]) {
				t.Fatalf("%s: limit %d: tree %d after refusal+retry differs:\n%v\nwant\n%v", name, limit, i, got, want[i])
			}
		}
	}
}

func TestStage3FailureThenMutation(t *testing.T) {
	set := reviewDiffSet(t, stage3StabModule)
	// next: one more insertion and one removal on every tree, through the retained topology.
	next := func(t *testing.T, trees []*Tree) {
		t.Helper()
		for _, tr := range trees {
			a := tr.top.first()
			if _, err := tr.NewList(a, "l", []string{"6"}, NewOptions{}); err != nil {
				t.Fatal(err)
			}
			if n := stage3StabList(tr, "11"); n != nil {
				if err := n.Remove(); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	two := func() (*Tree, *Tree) {
		return stage3StabTree(t, set, []string{"3", "7", "11", "15"}, "7"),
			stage3StabTree(t, set, []string{"1", "5", "9"}, "5")
	}
	for limited := range 2 {
		stage3FailThenRetry(t, "move", func() ([]*Tree, func() error) {
			src, dst := two()
			n := stage3StabList(src, "7")
			// reciprocal links on the moved node: unlink frees both endpoints' records
			for _, c := range src.top.first().kids.SchemaAll() {
				if c.schema.Name == "r" {
					linkLeafrefNode(n, c)
				}
			}
			if n.links == nil {
				t.Fatal("setup: no link")
			}
			return []*Tree{dst, src}, func() error { return dst.Insert(dst.top.first(), n) }
		}, limited, next)
	}
	stage3FailThenRetry(t, "remove", func() ([]*Tree, func() error) {
		src, _ := two()
		n := stage3StabList(src, "7")
		return []*Tree{src}, func() error { return n.Remove() }
	}, 0, next)
	stage3FailThenRetry(t, "dup", func() ([]*Tree, func() error) {
		src, dst := two()
		return []*Tree{dst, src}, func() error {
			_, _, err := dst.Dup(stage3StabList(src, "7"), dst.top.first(), DupOptions{Recursive: true})
			return err
		}
	}, 0, next)
	stage3FailThenRetry(t, "merge", func() ([]*Tree, func() error) {
		src, dst := two()
		return []*Tree{dst, src}, func() error { return dst.Merge(src) }
	}, 0, next)
	stage3FailThenRetry(t, "insert-detached", func() ([]*Tree, func() error) {
		src, dst := two()
		n := stage3StabList(src, "7")
		if err := n.Unlink(); err != nil {
			t.Fatal(err)
		}
		return []*Tree{dst, src}, func() error { return dst.Insert(dst.top.first(), n) }
	}, 0, next)
}

// TestStage3DetachedParentFailureThenMutation: an operation parse refused by its node budget
// leaves the detached request as it was (children, flags); a following successful parse and
// linking the request somewhere give the same result as without the refusal.
func TestStage3DetachedParentFailureThenMutation(t *testing.T) {
	set := reviewDiffSet(t, stage3StabModule)
	const reply = `<o xmlns="urn:stage3-stab">a</o><w xmlns="urn:stage3-stab">z</w><w xmlns="urn:stage3-stab">b</w><w xmlns="urn:stage3-stab">m</w>`
	request := func() *Node {
		_, op := opIndexParseRPC(t, set, `{"stage3-stab:op":{"i":"x"}}`)
		mustLink(t, unlink(op))
		return op
	}
	parse := func(op *Node, b Budget) error {
		_, _, _, _, err := parseOpWith(context.Background(), strings.NewReader(reply), set, FormatXML, opReply, op, Reject, b)
		return err
	}
	dump := func(op *Node) []string {
		tr := newTree(set)
		if err := tr.Insert(nil, op); err != nil {
			t.Fatal(err)
		}
		checkTreeRetainedRB(t, tr)
		return typedDump(tr)
	}
	ref := request()
	if err := parse(ref, Budget{}); err != nil {
		t.Fatal(err)
	}
	want := dump(ref)
	for nodes := 1; nodes < 4; nodes++ {
		op := request()
		children, flags := op.kids.Len(), op.flags
		if err := parse(op, Budget{MaxNodes: nodes}); !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("MaxNodes %d: %v, want ErrBudget", nodes, err)
		}
		if op.kids.Len() != children || op.flags != flags || op.parent != nil || op.tree != nil {
			t.Fatalf("MaxNodes %d: refused parse changed the detached request", nodes)
		}
		if err := parse(op, Budget{}); err != nil {
			t.Fatalf("MaxNodes %d: retry: %v", nodes, err)
		}
		if got := dump(op); !reflect.DeepEqual(got, want) {
			t.Fatalf("MaxNodes %d: refusal+retry gives\n%v\nwant\n%v", nodes, got, want)
		}
	}
}

// TestStage3RefusalCostIndependentOfInput measures, rather than sweeps: a mutation refused at
// its first unit of hash work must not allocate or visit in proportion to the run, metadata
// queue or subtree it was about to touch. A sweep over existing charge points cannot find a
// scan with no charge point at all; this compares a small and a large input instead.
func TestStage3RefusalCostIndependentOfInput(t *testing.T) {
	set := reviewDiffSet(t, stage3StabModule)
	keys := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = strconv.Itoa(2*i + 1)
		}
		return out
	}
	type refused struct {
		name string
		op   func(src, dst *Tree) (limited *Tree, run func() error)
		// linear: refused after main's read-only source bound walk (preflightDupHash,
		// preflightBatchHash: Node.All stacks each sibling set). That walk predates stage 3
		// and is only logged here.
		linear bool
	}
	ops := []refused{
		{"insert", func(_, dst *Tree) (*Tree, func() error) {
			fresh := stage3StabTree(t, set, nil, "")
			l, err := fresh.NewList(fresh.top.first(), "l", []string{"4"}, NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			mustLink(t, unlink(l))
			return dst, func() error { return dst.Insert(dst.top.first(), l) }
		}, false},
		{"move", func(src, dst *Tree) (*Tree, func() error) {
			n := stage3StabList(src, "3")
			return dst, func() error { return dst.Insert(dst.top.first(), n) }
		}, false},
		{"remove", func(src, _ *Tree) (*Tree, func() error) {
			n := stage3StabList(src, "3")
			return src, func() error { return n.Remove() }
		}, false},
		{"dup", func(src, dst *Tree) (*Tree, func() error) {
			return dst, func() error {
				_, _, err := dst.Dup(src.top.first(), nil, DupOptions{Recursive: true})
				return err
			}
		}, true},
		{"merge", func(src, dst *Tree) (*Tree, func() error) {
			return dst, func() error { return dst.Merge(src) }
		}, true},
	}
	measure := func(size int, o refused) (allocated uint64, visits int64) {
		src := stage3StabTree(t, set, keys(size), "")
		dst := stage3StabTree(t, set, keys(size), "")
		limited, run := o.op(src, dst)
		limited.work.hashLimit = 1
		limited.work.visits = true
		limited.work.Store(0)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		err := run()
		runtime.ReadMemStats(&after)
		if !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("%s size %d: %v, want ErrBudget", o.name, size, err)
		}
		return after.TotalAlloc - before.TotalAlloc, limited.work.Load()
	}
	for _, o := range ops {
		smallBytes, smallVisits := measure(8, o)
		largeBytes, largeVisits := measure(2048, o)
		t.Logf("%s refused at one unit: 8 -> %d bytes %d visits; 2048 -> %d bytes %d visits",
			o.name, smallBytes, smallVisits, largeBytes, largeVisits)
		if !o.linear && largeBytes > 2*smallBytes+16<<10 || largeVisits > 2*smallVisits+16 {
			t.Errorf("%s: refused work grows with the input (8 -> %d B/%d visits, 2048 -> %d B/%d visits)",
				o.name, smallBytes, smallVisits, largeBytes, largeVisits)
		}
	}
}

// TestStage3SetValueRelocationReindexBudget: relocating a leaf-list value inside a list
// instance with an incomplete key prefix reindexes that list in its hashed siblings after the
// removal. The move's recovery reservation spends the same quota, so the reindex is rechecked
// after it: at every boundary the edit is refused before the value leaves its list.
func TestStage3SetValueRelocationReindexBudget(t *testing.T) {
	const module = `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  container r { list l { config false; key "%s";
    leaf b { type string; } leaf-list s { type string; } leaf a { type string; }
  } }
}`
	srcSet := opIndexSet(t, fmt.Sprintf(module, "b a"))
	dstSet := opIndexSet(t, fmt.Sprintf(module, "a b"))
	var in strings.Builder
	in.WriteString(`{"m:r":{"l":[`)
	for i := range 6 {
		if i > 0 {
			in.WriteString(",")
		}
		in.WriteString(`{"b":"B` + strconv.Itoa(i) + `","a":"A"}`)
	}
	in.WriteString(`]}}`)
	src, _, err := parseJSONString(srcSet, in.String(), Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	build := func() (*Tree, func() error) {
		dst := newTree(dstSet)
		root, err := dst.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var target *Node
		for _, l := range src.top.first().kids.SchemaAll() {
			list, _, err := dst.Dup(l, root, DupOptions{Recursive: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range []string{"b", "d", "f"} { // non-keys between the copied keys
				c, err := dst.NewTerm(list, "s", v, NewOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if target == nil {
					target = c
				}
			}
			if list.keyN >= 2 {
				t.Fatalf("setup: key prefix complete (%d): %v", list.keyN, prefixNames(list))
			}
		}
		if root.kids.ht == nil || target == nil {
			t.Fatalf("setup: hashed=%v target=%v", root.kids.ht != nil, target != nil)
		}
		return dst, func() error { _, err := target.SetValue("z"); return err }
	}
	// checkEveryHashBudgetPoint without its Find cross-check: a path cannot address a list
	// instance whose key prefix is incomplete.
	full, apply := build()
	var points []uint64
	full.work.hashTrace = &points
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	want := typedDump(full)
	refused := 0
	for _, at := range points {
		if at == 0 {
			continue
		}
		tr, edit := build()
		before := snapshotHashBudgetTree(tr)
		tr.work.hashLimit = at - 1
		switch err := edit(); {
		case errors.Is(err, yang.ErrBudget):
			refused++
			if !reflect.DeepEqual(snapshotHashBudgetTree(tr), before) {
				t.Fatalf("limit %d: refused relocation changed the tree", at-1)
			}
		case err != nil:
			t.Fatalf("limit %d: %v", at-1, err)
		case !reflect.DeepEqual(typedDump(tr), want):
			t.Fatalf("limit %d: relocation differs from the unlimited one", at-1)
		}
	}
	if refused == 0 {
		t.Fatal("no refused boundary")
	}
}

// TestStage3DetachedOriginMoveBudget: moving a child out of a detached parent, refused at every
// hash-work boundary, puts it back under that parent with the parent's own work binding (not
// the destination's), and the parent's other children and later moves are unaffected.
func TestStage3DetachedOriginMoveBudget(t *testing.T) {
	set := reviewDiffSet(t, stage3StabModule)
	build := func() (*Tree, *Node, *Node) {
		src := stage3StabTree(t, set, []string{"3", "7", "11"}, "")
		a := src.top.first()
		if err := a.Unlink(); err != nil {
			t.Fatal(err)
		}
		dst := stage3StabTree(t, set, []string{"1", "5"}, "")
		var x *Node
		for _, c := range a.kids.SchemaAll() {
			if c.schema.Name == "l" && c.kids.first().Value() == "7" {
				x = c
			}
		}
		return dst, a, x
	}
	for _, origin := range []bool{false, true} {
		full, fullA, x := build()
		counter := func(dst *Tree, a *Node) *workCounter {
			if origin {
				return a.kids.work // the detached parent's own counter
			}
			return &dst.work
		}
		fullCounter := counter(full, fullA)
		if err := full.Insert(full.top.first(), x); err != nil {
			t.Fatal(err)
		}
		need := fullCounter.hashUsed
		if need < 2 {
			t.Fatalf("origin=%v: %d units", origin, need)
		}
		for limit := uint64(1); limit < need; limit++ {
			dst, a, x := build()
			parentWork, childWork, children := a.kids.work, x.kids.work, a.kids.Len()
			before := snapshotHashBudgetTree(dst)
			limited := counter(dst, a)
			limited.hashLimit = limit
			if err := dst.Insert(dst.top.first(), x); !errors.Is(err, yang.ErrBudget) {
				t.Fatalf("limit %d/%d: %v, want ErrBudget", limit, need, err)
			}
			if x.parent != a || a.kids.Len() != children || !reflect.DeepEqual(snapshotHashBudgetTree(dst), before) {
				t.Fatalf("limit %d/%d: refused move did not restore both sides", limit, need)
			}
			if a.kids.work != parentWork || x.kids.work != childWork {
				t.Fatalf("limit %d/%d: refused move left the detached origin bound to the destination", limit, need)
			}
			limited.hashLimit = 0
			if err := dst.Insert(dst.top.first(), x); err != nil {
				t.Fatalf("limit %d: retry: %v", limit, err)
			}
			checkTreeRetainedRB(t, dst)
			holder := newTree(set)
			if err := holder.Insert(nil, a); err != nil {
				t.Fatalf("limit %d: linking the origin afterwards: %v", limit, err)
			}
			checkTreeRetainedRB(t, holder)
		}
	}
}

// TestStage3DupIntoDetachedParentBudget: copying values into a detached default NP container,
// refused at every boundary, restores the default flags of the whole detached ancestor chain.
func TestStage3DupIntoDetachedParentBudget(t *testing.T) {
	set := reviewDiffSet(t, `module stage3-np {yang-version 1.1;namespace "urn:stage3-np";prefix s;
 container n1 {container n2 {leaf-list w {type string;} leaf d {type string; default "x";}}}}`)
	src := parseOnlyXML(t, set, `<n1 xmlns="urn:stage3-np"><n2><w>p</w><w>q</w><w>r</w></n2></n1>`)
	first := src.top.first().kids.first().kids.first()
	build := func() (*Tree, *Node, *Node) {
		holder := newTree(set)
		n1, err := holder.NewInner(nil, "stage3-np:n1", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		n2, err := holder.NewInner(n1, "n2", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := n1.Unlink(); err != nil {
			t.Fatal(err)
		}
		return newTree(set), n1, n2
	}
	full, n1, n2 := build()
	if n1.flags&FlagDefault == 0 || n2.flags&FlagDefault == 0 {
		t.Skipf("setup: NP containers not default (%#x, %#x)", n1.flags, n2.flags)
	}
	if _, _, err := full.DupSiblings(first, n2, DupOptions{}); err != nil {
		t.Fatal(err)
	}
	need := full.work.hashUsed
	for limit := uint64(1); limit < need; limit++ {
		dst, n1, n2 := build()
		f1, f2, children := n1.flags, n2.flags, n2.kids.Len()
		dst.work.hashLimit = limit
		if _, _, err := dst.DupSiblings(first, n2, DupOptions{}); !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("limit %d/%d: %v, want ErrBudget", limit, need, err)
		}
		if n1.flags != f1 || n2.flags != f2 || n2.kids.Len() != children {
			t.Fatalf("limit %d/%d: refused copy changed the detached chain (%#x->%#x, %#x->%#x, %d->%d children)",
				limit, need, f1, n1.flags, f2, n2.flags, children, n2.kids.Len())
		}
		dst.work.hashLimit = 0
		if _, _, err := dst.DupSiblings(first, n2, DupOptions{}); err != nil {
			t.Fatalf("limit %d: retry: %v", limit, err)
		}
		if n1.flags&FlagDefault != 0 || n2.flags&FlagDefault != 0 {
			t.Fatalf("limit %d: retry left the chain default", limit)
		}
	}
}

// snapshotHashBudgetDetached is snapshotHashBudgetTree of the detached subtree of root.
func snapshotHashBudgetDetached(set *schema.Set, root *Node) hashBudgetSnapshot {
	s := hashBudgetSnapshot{dump: []string{}, index: map[*Node]string{}, sibs: map[*siblings]string{}}
	for n := range root.allAny() {
		s.dump = append(s.dump, typedLine(set, n))
	}
	snapshotHashBudgetRoots(&s, []*Node{root})
	return s
}

// TestStage3DetachedDestinationChainBudget sweeps the hash-work quota of every public mutation
// that can write below a detached destination: a default NP-container chain a/b detached from a
// holder tree and filled from a third tree. Linking the first explicit child clears the default
// flags of the whole chain, whose counter has no journal of its own. Each refusal must leave the
// operation's tree, the source tree and the detached chain (flags, hashes, children, work
// bindings) exactly as they were; retrying without a limit gives the unrefused result.
func TestStage3DetachedDestinationChainBudget(t *testing.T) {
	set := reviewDiffSet(t, `module stage3-chain {yang-version 1.1;namespace "urn:stage3-chain";prefix s;
 container a {container b {leaf-list w {type string;} list l {key k; leaf k {type string;}}
  leaf d {type string; default "x";}}}}`)
	const full = `<a xmlns="urn:stage3-chain"><b><w>p</w><w>q</w><w>r</w></b></a>`
	values := func(tr *Tree) []*Node { return tr.top.first().kids.first().kids.schemaSpan().Collect() }
	wSchema := values(parseOnlyXML(t, set, full))[0].schema
	chain := func() (*Node, *Node) {
		holder := newTree(set)
		a, err := holder.NewInner(nil, "stage3-chain:a", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		b, err := holder.NewInner(a, "b", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Unlink(); err != nil {
			t.Fatal(err)
		}
		if a.flags&FlagDefault == 0 || b.flags&FlagDefault == 0 {
			t.Fatalf("setup: chain not default (%#x, %#x)", a.flags, b.flags)
		}
		return a, b
	}
	// op prepares one entry below the detached parent b: the source tree (nil: none) and the call.
	type entry struct {
		name string
		op   func(dst *Tree, b *Node) (*Tree, func() error)
	}
	entries := []entry{
		{"insert", func(dst *Tree, b *Node) (*Tree, func() error) {
			src := parseOnlyXML(t, set, full)
			n := values(src)[1]
			return src, func() error { return dst.Insert(b, n) }
		}},
		{"insert-detached-node", func(dst *Tree, b *Node) (*Tree, func() error) {
			src := parseOnlyXML(t, set, full)
			n := values(src)[1]
			mustLink(t, n.Unlink())
			return src, func() error { return dst.Insert(b, n) }
		}},
		{"insert-forest", func(dst *Tree, b *Node) (*Tree, func() error) {
			src := newTree(set)
			for _, v := range []string{"q", "p", "r"} {
				mustLink(t, src.insert(nil, retainedRBTerm(t, wSchema, v), insertDefault))
			}
			return src, func() error { return dst.Insert(b, src.top.first()) }
		}},
		{"dup", func(dst *Tree, b *Node) (*Tree, func() error) {
			src := parseOnlyXML(t, set, full)
			return src, func() error {
				_, _, err := dst.DupSiblings(values(src)[0], b, DupOptions{})
				return err
			}
		}},
		{"new-term", func(dst *Tree, b *Node) (*Tree, func() error) {
			return nil, func() error {
				_, err := dst.NewTerm(b, "w", "p", NewOptions{})
				return err
			}
		}},
		{"new-list", func(dst *Tree, b *Node) (*Tree, func() error) {
			return nil, func() error {
				_, err := dst.NewList(b, "l", []string{"k1"}, NewOptions{})
				return err
			}
		}},
		{"new-opaque", func(dst *Tree, b *Node) (*Tree, func() error) {
			return nil, func() error {
				_, err := dst.NewOpaque(b, "o", "v", OpaqueOptions{Module: "stage3-chain"})
				return err
			}
		}},
		{"merge-diff-tree", func(dst *Tree, b *Node) (*Tree, func() error) {
			diff, err := Diff(newTree(set), parseOnlyXML(t, set, full), DiffOptions{})
			if err != nil {
				t.Fatal(err)
			}
			src := values(diff)[0]
			return diff, func() error { return dst.MergeDiffTree(b, src, MergeDiffOptions{}) }
		}},
	}
	type state struct {
		dst, src, chain hashBudgetSnapshot
	}
	snapshot := func(dst, src *Tree, a *Node) state {
		s := state{dst: snapshotHashBudgetTree(dst), chain: snapshotHashBudgetDetached(set, a)}
		if src != nil {
			s.src = snapshotHashBudgetTree(src)
		}
		return s
	}
	for _, e := range entries {
		t.Run(e.name, func(t *testing.T) {
			a, b := chain()
			dst := newTree(set)
			_, op := e.op(dst, b)
			var points []uint64
			dst.work.hashTrace = &points
			if err := op(); err != nil {
				t.Fatalf("unlimited: %v", err)
			}
			dst.work.hashTrace = nil
			need := dst.work.hashUsed
			if a.flags&FlagDefault != 0 {
				t.Fatal("unlimited run left the detached chain default")
			}
			want := snapshotHashBudgetDetached(set, a).dump
			limits := map[uint64]bool{}
			for limit := uint64(1); limit < need; limit++ {
				limits[limit] = true
			}
			for _, at := range points {
				if at > 1 && at <= need {
					limits[at-1] = true
				}
			}
			for limit := range limits {
				a, b := chain()
				dst := newTree(set)
				src, op := e.op(dst, b)
				before := snapshot(dst, src, a)
				dst.work.hashLimit = limit
				err := op()
				if err == nil {
					// the traced reference charged more than the call needs (a journal opened for
					// the trace): success must still give the unrefused result
					if got := snapshotHashBudgetDetached(set, a).dump; !reflect.DeepEqual(got, want) {
						t.Fatalf("limit %d/%d: success gives\n%v\nwant\n%v", limit, need, got, want)
					}
					continue
				}
				if !errors.Is(err, yang.ErrBudget) {
					t.Fatalf("limit %d/%d: %v, want ErrBudget", limit, need, err)
				}
				if after := snapshot(dst, src, a); !reflect.DeepEqual(after, before) {
					t.Fatalf("limit %d/%d: refusal changed state:\nchain %v\nwas   %v", limit, need,
						after.chain.dump, before.chain.dump)
				}
				checkTreeRetainedRB(t, dst)
				if src != nil {
					checkTreeRetainedRB(t, src)
				}
				dst.work.hashLimit = 0
				if err := op(); err != nil {
					t.Fatalf("limit %d: retry: %v", limit, err)
				}
				if got := snapshotHashBudgetDetached(set, a).dump; !reflect.DeepEqual(got, want) {
					t.Fatalf("limit %d: refusal+retry gives\n%v\nwant\n%v", limit, got, want)
				}
			}
		})
	}
}
