// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
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
	for _, move := range []bool{false, true} {
		build := func() (*Tree, func() error, *Tree) {
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
			if move {
				// a cross-tree move of the RB member out of the incomplete list (Insert)
				other := newTree(dstSet)
				r2, err := other.NewInner(nil, "m:r", NewOptions{})
				if err != nil {
					t.Fatal(err)
				}
				l2, err := other.NewList(r2, "l", []string{"A", "X"}, NewOptions{})
				if err != nil {
					t.Fatal(err)
				}
				return other, func() error { return other.Insert(l2, target) }, dst
			}
			return dst, func() error { _, err := target.SetValue("z"); return err }, dst
		}
		// checkEveryHashBudgetPoint without its Find cross-check: a path cannot address a list
		// instance whose key prefix is incomplete.
		full, apply, _ := build()
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
			tr, edit, source := build()
			before, sourceBefore := snapshotHashBudgetTree(tr), snapshotHashBudgetTree(source)
			tr.work.hashLimit = at - 1
			switch err := edit(); {
			case errors.Is(err, yang.ErrBudget):
				refused++
				if !reflect.DeepEqual(snapshotHashBudgetTree(tr), before) || !reflect.DeepEqual(snapshotHashBudgetTree(source), sourceBefore) {
					t.Fatalf("move=%t limit %d: refused relocation changed a tree", move, at-1)
				}
				// the reindex refusal follows the armed removal undo: it is disarmed again
				if w := (&fzScenario{trees: []*Tree{tr, source}}).armedUndo(); w != "" {
					t.Fatalf("limit %d: refused relocation left a removal undo armed at %s", at-1, w)
				}
			case err != nil:
				t.Fatalf("limit %d: %v", at-1, err)
			case !reflect.DeepEqual(typedDump(tr), want):
				t.Fatalf("limit %d: relocation differs from the unlimited one", at-1)
			}
		}
		if refused == 0 {
			t.Fatalf("move=%t: no refused boundary", move)
		}
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
// that can write below a detached destination, fed from a third tree. Destinations: a default
// NP-container chain a/b detached from a holder tree (linking the first explicit child clears the
// chain's default flags), a populated detached a/b whose existing descendants the operation
// reorders or replaces, a detached list instance without its key (a key or keyless child changes
// its hash in its own sibling set), and a detached diff a/b/x whose x a merge replaces. Each
// refusal must leave the operation's tree, the source tree and the whole detached tree (flags,
// values, hashes, children, work bindings) exactly as they were; retrying without a limit gives
// the unrefused result.
func TestStage3DetachedDestinationChainBudget(t *testing.T) {
	set := reviewDiffSet(t, `module stage3-chain {yang-version 1.1;namespace "urn:stage3-chain";prefix s;
 container a {container b {leaf-list w {type string;} list l {key k; leaf k {type string;} leaf p {type string;}}
  leaf x {type string;} leaf d {type string; default "x";}}}}`)
	const full = `<a xmlns="urn:stage3-chain"><b><w>p</w><w>q</w><w>r</w></b></a>`
	values := func(tr *Tree) []*Node { return tr.top.first().kids.first().kids.schemaSpan().Collect() }
	wSchema := values(parseOnlyXML(t, set, full))[0].schema
	detach := func(tr *Tree) *Node {
		a := tr.top.first()
		mustLink(t, a.Unlink())
		return a
	}
	defaultChain := func() (*Node, *Node) {
		holder := newTree(set)
		a, err := holder.NewInner(nil, "stage3-chain:a", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		b, err := holder.NewInner(a, "b", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		mustLink(t, a.Unlink())
		if a.flags&FlagDefault == 0 || b.flags&FlagDefault == 0 {
			t.Fatalf("setup: chain not default (%#x, %#x)", a.flags, b.flags)
		}
		return a, b
	}
	populated := func() (*Node, *Node) {
		a := detach(parseOnlyXML(t, set, `<a xmlns="urn:stage3-chain"><b><w>m</w><w>c</w><w>s</w>`+
			`<l><k>5</k><p>z</p></l><x>1</x></b></a>`))
		return a, a.kids.first()
	}
	lSchema := values(parseOnlyXML(t, set, `<a xmlns="urn:stage3-chain"><b><l><k>5</k></l></b></a>`))[0].schema
	// the only list instance, still without its key (as during parsing), among indexed siblings
	keylessList := func() (*Node, *Node) {
		tr := parseOnlyXML(t, set, `<a xmlns="urn:stage3-chain"><b><w>m</w><w>c</w>`+
			`<w>s</w><x>1</x></b></a>`)
		b := tr.top.first().kids.first()
		l := newInner(lSchema, &tr.work)
		mustLink(t, tr.insert(b, l, insertDefault))
		a := detach(tr)
		if b.kids.ht == nil || l.keyN != 0 {
			t.Fatalf("setup: indexed %t, %d keys", b.kids.ht != nil, l.keyN)
		}
		return a, l
	}
	xTree := func(v string) *Tree {
		return parseOnlyXML(t, set, `<a xmlns="urn:stage3-chain"><b><x>`+v+`</x></b></a>`)
	}
	diff := func(from, to string) *Tree {
		d, err := Diff(xTree(from), xTree(to), DiffOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	diffChain := func() (*Node, *Node) {
		a := detach(diff("1", "2"))
		return a, a
	}
	// chain builds the detached destination (root, parent); op prepares one call below parent:
	// the source tree (nil: none) and the call. Without a chain the default a/b chain is used.
	type entry struct {
		name  string
		chain func() (*Node, *Node)
		op    func(dst *Tree, parent *Node) (*Tree, func() error)
	}
	insert := func(detached bool) func(*Tree, *Node) (*Tree, func() error) {
		return func(dst *Tree, b *Node) (*Tree, func() error) {
			src := parseOnlyXML(t, set, full)
			n := values(src)[1]
			if detached {
				mustLink(t, n.Unlink())
			}
			return src, func() error { return dst.Insert(b, n) }
		}
	}
	forest := func(dst *Tree, b *Node) (*Tree, func() error) {
		src := newTree(set)
		for _, v := range []string{"q", "p", "r"} {
			mustLink(t, src.insert(nil, retainedRBTerm(t, wSchema, v), insertDefault))
		}
		return src, func() error { return dst.Insert(b, src.top.first()) }
	}
	dup := func(dst *Tree, b *Node) (*Tree, func() error) {
		src := parseOnlyXML(t, set, full)
		return src, func() error {
			_, _, err := dst.DupSiblings(values(src)[0], b, DupOptions{})
			return err
		}
	}
	newTerm := func(name, value string) func(*Tree, *Node) (*Tree, func() error) {
		return func(dst *Tree, parent *Node) (*Tree, func() error) {
			return nil, func() error {
				_, err := dst.NewTerm(parent, name, value, NewOptions{})
				return err
			}
		}
	}
	newOpaque := func(dst *Tree, parent *Node) (*Tree, func() error) {
		return nil, func() error {
			_, err := dst.NewOpaque(parent, "o", "v", OpaqueOptions{Module: "stage3-chain"})
			return err
		}
	}
	entries := []entry{
		{"insert", nil, insert(false)},
		{"insert-detached-node", nil, insert(true)},
		{"insert-forest", nil, forest},
		{"dup", nil, dup},
		{"new-term", nil, newTerm("w", "p")},
		{"new-list", nil, func(dst *Tree, b *Node) (*Tree, func() error) {
			return nil, func() error {
				_, err := dst.NewList(b, "l", []string{"k1"}, NewOptions{})
				return err
			}
		}},
		{"new-opaque", nil, newOpaque},
		{"merge-diff-tree", nil, func(dst *Tree, b *Node) (*Tree, func() error) {
			d, err := Diff(newTree(set), parseOnlyXML(t, set, full), DiffOptions{})
			if err != nil {
				t.Fatal(err)
			}
			src := values(d)[0]
			return d, func() error { return dst.MergeDiffTree(b, src, MergeDiffOptions{}) }
		}},
		{"populated-insert", populated, insert(false)},
		{"populated-insert-forest", populated, forest},
		{"populated-dup", populated, dup},
		{"list-parent-dup", keylessList, func(dst *Tree, l *Node) (*Tree, func() error) {
			src := parseOnlyXML(t, set, `<a xmlns="urn:stage3-chain"><b><l><k>9</k><p>q</p></l></b></a>`)
			p := src.top.first().kids.first().kids.first().kids.schemaSpan().Collect()[1]
			return src, func() error {
				_, _, err := dst.Dup(p, l, DupOptions{})
				return err
			}
		}},
		{"list-parent-key", keylessList, newTerm("k", "7")},
		{"list-parent-opaque", keylessList, newOpaque},
		{"detached-source-mixed", populated, func(dst *Tree, b *Node) (*Tree, func() error) {
			// an opaque destination switches lookup mode (switchJournal) while the moved node
			// leaves a detached source tree, which the journal must hold whole
			o, err := dst.NewOpaque(nil, "o", "", OpaqueOptions{Module: "stage3-chain"})
			if err != nil {
				t.Fatal(err)
			}
			n := b.kids.schemaSpan().Collect()[1]
			return nil, func() error { return dst.Insert(o, n) }
		}},
		{"diff-nested-replace", diffChain, func(dst *Tree, a *Node) (*Tree, func() error) {
			src := diff("2", "3")
			return src, func() error { return dst.MergeDiffTree(a, src.top.first().kids.first(), MergeDiffOptions{}) }
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
			chain := e.chain
			if chain == nil {
				chain = defaultChain
			}
			a, parent := chain()
			dst := newTree(set)
			_, op := e.op(dst, parent)
			before := snapshotHashBudgetDetached(set, a)
			var points []uint64
			dst.work.hashTrace = &points
			if err := op(); err != nil {
				t.Fatalf("unlimited: %v", err)
			}
			dst.work.hashTrace = nil
			need := dst.work.hashUsed
			if e.chain == nil && a.flags&FlagDefault != 0 {
				t.Fatal("unlimited run left the detached chain default")
			}
			want := snapshotHashBudgetDetached(set, a).dump
			if reflect.DeepEqual(want, before.dump) {
				t.Fatal("setup: the operation changed nothing in the detached tree")
			}
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
				a, parent := chain()
				dst := newTree(set)
				src, op := e.op(dst, parent)
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
	// A callback budget error after the nested replacement of x is a refusal too: the detached
	// diff, x's value included, stays as it was.
	t.Run("diff-nested-replace-callback", func(t *testing.T) {
		a, _ := diffChain()
		dst, src := newTree(set), diff("2", "3")
		before := snapshot(dst, src, a)
		calls := 0
		err := dst.MergeDiffTree(a, src.top.first().kids.first(), MergeDiffOptions{Callback: func(_, d *Node) error {
			if d != nil && d.schema != nil && d.schema.Name == "x" {
				calls++
				return yang.ErrBudget
			}
			return nil
		}})
		if !errors.Is(err, yang.ErrBudget) || calls != 1 {
			t.Fatalf("%v after %d x callbacks, want ErrBudget after one", err, calls)
		}
		if after := snapshot(dst, src, a); !reflect.DeepEqual(after, before) {
			t.Fatalf("callback refusal changed the detached diff:\n%v\nwas\n%v", after.chain.dump, before.chain.dump)
		}
	})
}

// The refusal fuzzer: random linked and detached, empty and populated trees (depth 1-4: keyed,
// keyless and user-ordered lists, leaf-lists, NP containers with defaults, leafref links, a second
// context for diffs and copies) under every public mutation entry of the design-12 inventory.
// Each scenario is rebuilt from its seed for every probe. Hash-work limits below the traced total
// and every reservation point (a seeded sample of fzSample per knob by default, all of them with
// YANG_STAGE3_FUZZ_FULL=1), node limits of an operation parse and every callback refuse it in
// turn: the refusal must leave all
// involved trees and detached roots byte-identical (snapshotHashBudgetRoots), and a retry
// without the limit must give the unrefused result.
const fzModule = `module fz {yang-version 1.1;namespace "urn:fz";prefix f;
 container top {
  leaf dl {type string; default "d";}
  container np {leaf-list ll {type string;} leaf dd {type string; default "z";}
   container np2 {leaf v {type string;} leaf-list ll2 {type string;} leaf d2 {type string; default "y";}}}
  list kl {key k; leaf k {type string;} leaf p {type string;} leaf-list q {type string;}
   container c {leaf r {type leafref {path "/top/kl/k";}} leaf d3 {type string; default "w";}}}
  list ul {key k; ordered-by user; leaf k {type string;} leaf p {type string;}}
  leaf-list ull {type string; ordered-by user;}
  leaf-list sl {type string;}
  leaf ref {type leafref {path "../kl/k";}}
  list nk {config false; leaf a {type string;}}
  list al {key k; leaf k {type string;}
   action go {input {leaf x {type string;}} output {leaf y {type string;} leaf-list z {type string;}}}}
 }
 rpc op {input {leaf i {type string;}} output {leaf o {type string;} leaf-list w {type string;}}}}`

// fzSample bounds the limits probed per knob unless fzFull (YANG_STAGE3_FUZZ_FULL=1) is set.
const fzSample = 24

var fzFull = os.Getenv("YANG_STAGE3_FUZZ_FULL") == "1"

type fzKnob struct {
	name string
	set  func(limit uint64, trace *[]uint64) // limit 0: none
	used func() uint64
}

func fzWork(name string, w *workCounter) fzKnob {
	return fzKnob{name, func(limit uint64, trace *[]uint64) { w.hashLimit, w.hashTrace = limit, trace },
		func() uint64 { return w.hashUsed }}
}

// fzCallback refuses its failAt-th call (from 0) with a budget error; -1 never.
type fzCallback struct{ calls, failAt int }

func (c *fzCallback) hit() error {
	c.calls++
	if c.calls-1 == c.failAt {
		return fmt.Errorf("fz callback %d: %w", c.failAt, yang.ErrBudget)
	}
	return nil
}

type fzScenario struct {
	entry string
	set   *schema.Set
	trees []*Tree
	roots []*Node // detached roots (holders of detached chains and detached sources)
	knobs []fzKnob
	cb    *fzCallback // nil: the entry takes no callback
	op    func() error
	// then is a further mutation of the same objects after op (nil: none). Its refusals are
	// swept too, after an unrefused op: success or refusal must follow the unrefused history.
	then     func() error
	thenKind string // the entry then is an instance of
	// ref, when set, is an independent history (another public path native takes the same
	// way) whose outcome got must reproduce after op and then.
	ref, got func() string
}

// armedUndo names a retained graph of the scenario that still holds an operation-local removal
// undo (dirtyOrder.undo), which a refused or finished operation must not leave behind.
func (s *fzScenario) armedUndo() string {
	check := func(where string, sib *siblings) string {
		for _, d := range sib.rbOrders {
			if d.undo != nil {
				return where
			}
		}
		return ""
	}
	nodes := func(top *Node) string {
		for n := range top.allAny() {
			if w := check(n.Path(), childStorage(n)); w != "" {
				return w
			}
			if n.detachedRB != nil && n.detachedRB.undo != nil {
				return n.Path() + " (detached)"
			}
		}
		return ""
	}
	for _, tr := range s.trees {
		if w := check("top", &tr.top); w != "" {
			return w
		}
		for top := range tr.Top() {
			if w := nodes(top); w != "" {
				return w
			}
		}
	}
	for _, r := range s.roots {
		if w := nodes(r); w != "" {
			return w
		}
	}
	return ""
}

// counters are the distinct work counters bound to any sibling set of the scenario.
func (s *fzScenario) counters() []*workCounter {
	var out []*workCounter
	add := func(w *workCounter) {
		if w != nil && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	for _, tr := range s.trees {
		add(&tr.work)
		add(tr.top.work)
		for top := range tr.Top() {
			for n := range top.allAny() {
				add(childStorage(n).work)
			}
		}
	}
	for _, r := range s.roots {
		for n := range r.allAny() {
			add(childStorage(n).work)
		}
	}
	return out
}

func (s *fzScenario) dump() [][]string {
	var out [][]string
	for _, tr := range s.trees {
		out = append(out, typedDump(tr))
	}
	for _, r := range s.roots {
		var lines []string
		for n := range r.allAny() {
			lines = append(lines, typedLine(s.set, n))
		}
		out = append(out, lines)
	}
	return out
}

func (s *fzScenario) snapshot() []hashBudgetSnapshot {
	var out []hashBudgetSnapshot
	for _, tr := range s.trees {
		out = append(out, snapshotHashBudgetTree(tr))
	}
	for _, r := range s.roots {
		out = append(out, snapshotHashBudgetDetached(s.set, r))
	}
	return out
}

type fzGen struct {
	t         *testing.T
	rng       *rand.Rand
	set, set2 *schema.Set
	pad       int // extra instances in every wide sibling set (refusal cost checks)
}

// fzPadded reports a padding instance, which no scenario picks.
func fzPadded(n *Node) bool { return strings.HasPrefix(n.Value(), "pad") }

func (g *fzGen) pads(name string) string {
	var b strings.Builder
	for i := range g.pad {
		fmt.Fprintf(&b, "<%s>pad%04d</%s>", name, i, name)
	}
	return b.String()
}

func (g *fzGen) vals(n int) []string {
	pool := strings.Split("a b c d e f g h i j k l", " ")
	g.rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	return pool[:n]
}

// xml is a random document: fill 0 is an empty top container, 2 has every node.
func (g *fzGen) xml(fill int, state bool) string {
	some := func() bool { return fill == 2 || fill == 1 && g.rng.IntN(2) == 0 }
	count := func(most int) int {
		switch fill {
		case 0:
			return 0
		case 2:
			return most
		}
		return g.rng.IntN(most + 1)
	}
	var b strings.Builder
	leaves := func(name string, vs []string) {
		for _, v := range vs {
			b.WriteString("<" + name + ">" + v + "</" + name + ">")
		}
	}
	b.WriteString(`<top xmlns="urn:fz">`)
	if some() {
		leaves("dl", g.vals(1))
	}
	if some() {
		b.WriteString("<np>")
		leaves("ll", g.vals(count(3)))
		b.WriteString(g.pads("ll"))
		if some() {
			b.WriteString("<np2>")
			if some() {
				leaves("v", g.vals(1))
			}
			leaves("ll2", g.vals(count(3)))
			b.WriteString("</np2>")
		}
		b.WriteString("</np>")
	}
	keys := g.vals(count(3))
	for _, k := range keys {
		b.WriteString("<kl><k>" + k + "</k>")
		if some() {
			leaves("p", g.vals(1))
		}
		leaves("q", g.vals(count(2)))
		if some() {
			b.WriteString("<c><r>" + keys[g.rng.IntN(len(keys))] + "</r></c>")
		}
		b.WriteString("</kl>")
	}
	for _, k := range g.vals(count(3)) {
		b.WriteString("<ul><k>" + k + "</k><p>" + k + "</p></ul>")
	}
	leaves("ull", g.vals(count(3)))
	b.WriteString(g.pads("ull"))
	leaves("sl", g.vals(count(4)))
	b.WriteString(g.pads("sl"))
	if len(keys) > 0 && some() {
		leaves("ref", keys[:1])
	}
	if state {
		for range count(2) {
			leaves("nk", []string{"<a>" + g.vals(1)[0] + "</a>"})
		}
	}
	for _, k := range g.vals(count(2)) {
		b.WriteString("<al><k>" + k + "</k></al>")
	}
	b.WriteString("</top>")
	return b.String()
}

// tree parses a random document of set and links its leafrefs; some trees are validated first,
// which adds default leaves and default NP containers.
func (g *fzGen) tree(set *schema.Set, fill int) *Tree {
	validated := g.rng.IntN(3) == 0
	tr := parseOnlyXML(g.t, set, g.xml(fill, !validated))
	if validated {
		_, _ = tr.Validate(context.Background(), ValidateOptions{NoState: true})
	}
	var keys []*Node
	var refs []*Node
	for top := range tr.Top() {
		for n := range top.All() {
			switch {
			case n.schema == nil:
			case n.schema.IsKey() && n.parent.schema.Name == "kl":
				keys = append(keys, n)
			case n.schema.Name == "r" || n.schema.Name == "ref":
				refs = append(refs, n)
			}
		}
	}
	for _, r := range refs {
		for _, k := range keys {
			if k.Value() == r.Value() {
				linkLeafrefNode(k, r)
			}
		}
	}
	return tr
}

func fzInner(root *Node) []*Node {
	var out []*Node
	for n := range root.All() {
		if n.schema != nil && (n.schema.Kind == schema.Container || n.schema.Kind == schema.List) {
			out = append(out, n)
		}
	}
	return out
}

func fzChildren(p *Node, kinds ...schema.Kind) []*schema.Node {
	var out []*schema.Node
	for _, c := range p.schema.Children {
		if !c.IsKey() && slices.Contains(kinds, c.Kind) {
			out = append(out, c)
		}
	}
	return out
}

func fzPick[T any](g *fzGen, xs []T) T { return xs[g.rng.IntN(len(xs))] }

// dest is a destination parent: an inner node of a linked tree (returned) or of a subtree
// detached from a holder tree (root returned).
func (g *fzGen) dest(fill int, kinds ...schema.Kind) (tr *Tree, root, parent *Node) {
	tr = g.tree(g.set, fill)
	var inner []*Node
	for _, n := range fzInner(tr.top.first()) {
		if len(fzChildren(n, kinds...)) != 0 {
			inner = append(inner, n)
		}
	}
	parent = fzPick(g, inner)
	if g.rng.IntN(2) == 0 {
		root = parent // a random ancestor-or-self of parent leaves its holder
		for root.parent != nil && g.rng.IntN(2) == 0 {
			root = root.parent
		}
		if err := root.Unlink(); err != nil {
			g.t.Fatal(err)
		}
		return nil, root, parent
	}
	return tr, nil, parent
}

// instance is a node of schema sn from a full third tree (of set2 sometimes), detached sometimes.
func (g *fzGen) instance(sn *schema.Node, s *fzScenario) *Node {
	set := g.set
	if g.rng.IntN(5) == 0 {
		set = g.set2
	}
	src := parseOnlyXML(g.t, set, g.xml(2, true))
	var match []*Node
	for n := range src.top.first().All() {
		if n.schema != nil && n.schema.Name == sn.Name && n.parent != nil && n.parent.schema.Name == sn.Parent.Name && !fzPadded(n) {
			match = append(match, n)
		}
	}
	if len(match) == 0 {
		return nil
	}
	n := fzPick(g, match)
	s.trees = append(s.trees, src)
	s.knobs = append(s.knobs, fzWork("source", &src.work))
	if g.rng.IntN(3) == 0 {
		if err := n.Unlink(); err != nil {
			g.t.Fatal(err)
		}
		s.roots = append(s.roots, n)
	}
	return n
}

func (g *fzGen) diff(a, b *Tree) *Tree {
	d, err := Diff(a, b, DiffOptions{})
	if err != nil {
		g.t.Fatal(err)
	}
	if d == nil {
		return newTree(a.set) // no difference
	}
	return d
}

var fzEntries = []string{"insert", "insert-forest", "insert-top", "dup", "merge", "merge-diff",
	"merge-diff-tree", "apply-diff", "new", "new-path", "set-value", "trim", "remove", "unlink",
	"validate", "validate-op", "parse-op", "insert-bound-root", "move-detached-switch", "merge-runs"}

func (g *fzGen) scenario(entry string) *fzScenario {
	s := &fzScenario{entry: entry, set: g.set}
	ctx := context.Background()
	// destination: the operation's tree t (a holder of a detached destination does not join)
	all := []schema.Kind{schema.Leaf, schema.LeafList, schema.List, schema.Container}
	foreign := false // a detached destination's operation tree of the second context
	destination := func(fill int, kinds ...schema.Kind) (*Tree, *Node) {
		tr, root, parent := g.dest(fill, kinds...)
		if root != nil {
			s.roots = append(s.roots, root)
			tr = newTree(g.set)
			if foreign = g.rng.IntN(3) == 0; foreign {
				tr = newTree(g.set2)
			}
		}
		s.trees = append(s.trees, tr)
		s.knobs = append(s.knobs, fzWork("destination", &tr.work))
		return tr, parent
	}
	linked := func(fill int) *Tree {
		tr := g.tree(g.set, fill)
		s.trees = append(s.trees, tr)
		s.knobs = append(s.knobs, fzWork("tree", &tr.work))
		return tr
	}
	nonKey := func(tr *Tree, terms bool) *Node {
		var out []*Node
		for n := range tr.top.first().All() {
			if n.schema != nil && !n.schema.IsKey() && (!terms || n.isTerm()) && !fzPadded(n) {
				out = append(out, n)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return fzPick(g, out)
	}
	switch entry {
	case "insert", "dup":
		dst, parent := destination(g.rng.IntN(3), all...)
		n := g.instance(fzPick(g, fzChildren(parent, all...)), s)
		if n == nil {
			return nil
		}
		if entry == "insert" {
			s.op = func() error { return dst.Insert(parent, n) }
			break
		}
		siblings, recursive := g.rng.IntN(2) == 0, g.rng.IntN(2) == 0
		s.op = func() error {
			o := DupOptions{Recursive: recursive}
			var err error
			if siblings {
				_, _, err = dst.DupSiblings(n, parent, o)
			} else {
				_, _, err = dst.Dup(n, parent, o)
			}
			return err
		}
	case "insert-forest":
		dst, parent := destination(g.rng.IntN(3), schema.LeafList)
		sn := fzPick(g, fzChildren(parent, schema.LeafList))
		src := newTree(g.set)
		for _, v := range g.vals(1 + g.rng.IntN(4)) {
			mustLink(g.t, src.insert(nil, retainedRBTerm(g.t, sn, v), insertDefault))
		}
		s.trees = append(s.trees, src)
		s.knobs = append(s.knobs, fzWork("source", &src.work))
		s.op = func() error { return dst.Insert(parent, src.top.first()) }
	case "insert-top":
		dst := newTree(g.set)
		s.trees = append(s.trees, dst)
		s.knobs = append(s.knobs, fzWork("destination", &dst.work))
		src := linked(g.rng.IntN(3))
		n := src.top.first()
		s.op = func() error { return dst.Insert(nil, n) }
	case "merge":
		dst := linked(g.rng.IntN(3))
		src := g.tree(g.set, 1+g.rng.IntN(2))
		s.trees = append(s.trees, src)
		s.op = func() error { return dst.Merge(src) }
	case "merge-diff", "merge-diff-tree", "apply-diff":
		set := g.set
		if g.rng.IntN(4) == 0 {
			set = g.set2
		}
		docs := []string{g.xml(g.rng.IntN(3), false), g.xml(g.rng.IntN(3), false), g.xml(g.rng.IntN(3), false)}
		parse := func(set *schema.Set, i int) *Tree { return parseOnlyXML(g.t, set, docs[i]) }
		s.cb = &fzCallback{failAt: -1}
		switch entry {
		case "apply-diff":
			dst := parseOnlyXML(g.t, g.set, docs[0])
			s.trees = append(s.trees, dst)
			s.knobs = append(s.knobs, fzWork("tree", &dst.work))
			d := g.diff(parse(set, 0), parse(set, 1))
			s.trees = append(s.trees, d)
			cb := s.cb
			s.op = func() error {
				return dst.ApplyDiff(d, ApplyDiffOptions{Callback: func(_, _ *Node) error { return cb.hit() }})
			}
		case "merge-diff":
			dst := g.diff(parse(g.set, 0), parse(g.set, 1))
			src := g.diff(parse(set, 1), parse(set, 2))
			s.trees = append(s.trees, dst, src)
			s.knobs = append(s.knobs, fzWork("tree", &dst.work))
			cb := s.cb
			s.op = func() error {
				return dst.MergeDiff(src, MergeDiffOptions{Callback: func(_, _ *Node) error { return cb.hit() }})
			}
		default:
			dst := g.diff(parse(g.set, 0), parse(g.set, 1))
			src := g.diff(parse(set, 1), parse(set, 2))
			if dst.top.first() == nil || src.top.first() == nil {
				return nil
			}
			var nodes []*Node
			for n := range src.top.first().All() {
				if n.parent != nil && !fzPadded(n) {
					nodes = append(nodes, n)
				}
			}
			if len(nodes) == 0 {
				return nil
			}
			sn := fzPick(g, nodes)
			var parent *Node
			for n := range dst.top.first().All() {
				if n.Path() == sn.parent.Path() {
					parent = n
				}
			}
			if parent == nil {
				parent, sn = dst.top.first(), src.top.first().kids.first()
				if sn == nil {
					return nil
				}
			}
			t := dst
			if g.rng.IntN(2) == 0 {
				root := dst.top.first()
				mustLink(g.t, root.Unlink())
				s.roots = append(s.roots, root)
				t = newTree(g.set)
			}
			s.trees = append(s.trees, t, src)
			s.knobs = append(s.knobs, fzWork("tree", &t.work))
			cb := s.cb
			s.op = func() error {
				return t.MergeDiffTree(parent, sn, MergeDiffOptions{Callback: func(_, _ *Node) error { return cb.hit() }})
			}
		}
	case "new":
		dst, parent := destination(g.rng.IntN(3), all...)
		cs := fzChildren(parent, all...)
		v := g.vals(1)[0]
		if len(cs) == 0 || g.rng.IntN(5) == 0 {
			s.op = func() error {
				_, err := dst.NewOpaque(parent, "o", v, OpaqueOptions{Module: "fz"})
				return err
			}
			break
		}
		sn := fzPick(g, cs)
		name := sn.Name
		if foreign {
			name = "fz:" + name // a node of the operation tree's context under parent
		}
		s.op = func() error {
			var err error
			switch {
			case sn.Kind == schema.Container:
				_, err = dst.NewInner(parent, name, NewOptions{})
			case sn.Kind == schema.List && len(sn.Keys) == 0:
				_, err = dst.NewList(parent, name, nil, NewOptions{})
			case sn.Kind == schema.List:
				_, err = dst.NewList(parent, name, []string{v}, NewOptions{})
			default:
				_, err = dst.NewTerm(parent, name, v, NewOptions{})
			}
			return err
		}
	case "new-path":
		dst := linked(g.rng.IntN(3))
		v := g.vals(1)[0]
		path := fzPick(g, []string{"/fz:top/np/ll", "/fz:top/np/np2/v", "/fz:top/np/np2/ll2", "/fz:top/sl",
			"/fz:top/kl[k='" + v + "']/p", "/fz:top/kl[k='" + v + "']/c/r", "/fz:top/ul[k='" + v + "']",
			"/fz:top/dl", "/fz:top/np/dd", "/fz:top/kl[k='" + v + "']/q"})
		update := g.rng.IntN(2) == 0
		s.op = func() error {
			_, err := dst.NewPath(path, v, NewPathOptions{Update: update})
			return err
		}
	case "set-value":
		dst := linked(1 + g.rng.IntN(2))
		n := nonKey(dst, true)
		if n == nil {
			return nil
		}
		v := g.vals(1)[0]
		s.op = func() error {
			_, err := n.SetValue(v)
			return err
		}
	case "trim":
		dst := linked(1 + g.rng.IntN(2))
		expr := fzPick(g, []string{"/fz:top/sl", "/fz:top/kl[p]", "/fz:top/np/ll", "/fz:top/np", "/fz:top/kl/q",
			"/fz:top/ul", "/fz:top/kl/c/r", "/fz:top/kl[k='a']", "/fz:top/ull"})
		s.op = func() error {
			_, err := dst.TrimXPath(expr, XPathOptions{})
			return err
		}
	case "remove", "unlink":
		dst := linked(1 + g.rng.IntN(2))
		n := nonKey(dst, false)
		if n == nil {
			return nil
		}
		if entry == "remove" {
			s.op = n.Remove
			break
		}
		s.op = n.Unlink
	case "validate":
		dst := parseOnlyXML(g.t, g.set, g.xml(g.rng.IntN(3), false))
		s.trees = append(s.trees, dst)
		s.knobs = append(s.knobs, fzWork("tree", &dst.work))
		o := ValidateOptions{NoState: true, MultiError: g.rng.IntN(2) == 0}
		s.op = func() error {
			_, err := dst.Validate(ctx, o)
			return err
		}
	case "validate-op", "parse-op":
		action := g.rng.IntN(2) == 0
		in, reply := `{"fz:op":{"i":"x"}}`, `<o xmlns="urn:fz">a</o><w xmlns="urn:fz">z</w><w xmlns="urn:fz">b</w>`
		if action {
			in = `{"fz:top":{"al":[{"k":"a","go":{"x":"1"}}]}}`
			reply = `<y xmlns="urn:fz">a</y><z xmlns="urn:fz">z</z><z xmlns="urn:fz">b</z><z xmlns="urn:fz">m</z>`
		}
		req, op := opIndexParseRPC(g.t, g.set, in)
		if entry == "validate-op" {
			s.trees = append(s.trees, req)
			s.knobs = append(s.knobs, fzWork("request", &req.work))
			oper := linked(g.rng.IntN(3))
			s.op = func() error {
				_, err := req.ValidateOp(ctx, OpRPC, ValidateOpOptions{Operational: oper})
				return err
			}
			break
		}
		if g.rng.IntN(2) == 0 {
			root := req.top.first()
			mustLink(g.t, root.Unlink())
			s.roots = append(s.roots, root)
		} else {
			s.trees = append(s.trees, req)
			s.knobs = append(s.knobs, fzWork("request", &req.work))
		}
		var b Budget
		s.knobs = append(s.knobs, fzKnob{"nodes", func(limit uint64, _ *[]uint64) { b.MaxNodes = int(limit) }, //nolint:gosec // small test limits
			func() uint64 { return 8 }})
		s.op = func() error {
			_, _, _, _, err := parseOpWith(ctx, strings.NewReader(reply), g.set, FormatXML, opReply, op, Reject, b)
			return err
		}
	case "insert-bound-root":
		// A populated subtree detached from one tree gets a child through some tree's NewTerm
		// (its root's children bound to that tree), then replaces its counterpart in another
		// copy of the document. A further mutation reaches its descendants.
		doc := g.xml(1+g.rng.IntN(2), false)
		src := parseOnlyXML(g.t, g.set, doc)
		var inner []*Node
		for _, n := range fzInner(src.top.first()) {
			if n.parent != nil && len(fzChildren(n, schema.Leaf, schema.LeafList)) != 0 && len(n.kids.nodes()) != 0 {
				inner = append(inner, n)
			}
		}
		if len(inner) == 0 {
			return nil
		}
		a := fzPick(g, inner)
		path, parentPath := a.Path(), a.parent.Path()
		mustLink(g.t, a.Unlink())
		dst := linkedTree(g, s, doc)
		var at, parent *Node
		for n := range dst.top.first().All() {
			switch n.Path() {
			case path:
				at = n
			case parentPath:
				parent = n
			}
		}
		if at == nil || parent == nil {
			return nil
		}
		mustLink(g.t, at.Remove())
		other := dst
		if g.rng.IntN(2) == 0 {
			other = newTree(g.set)
		}
		sn := fzPick(g, fzChildren(a, schema.Leaf, schema.LeafList))
		if _, err := other.NewTerm(a, sn.Name, "m"+g.vals(1)[0], NewOptions{}); err != nil {
			return nil
		}
		s.roots = append(s.roots, a)
		s.op = func() error { return dst.Insert(parent, a) }
		s.then = g.followUp(s, dst, a)
	case "move-detached-switch":
		// A child of a detached subtree moves under a destination that switches to the
		// unordered lookup mode (switchJournal): an opaque parent, or another context.
		src := parseOnlyXML(g.t, g.set, g.xml(2, false))
		var inner []*Node
		for _, n := range fzInner(src.top.first()) {
			if n.parent != nil && len(n.kids.nodes()) > 1 {
				inner = append(inner, n)
			}
		}
		if len(inner) == 0 {
			return nil
		}
		c := fzPick(g, inner)
		cpath := c.Path()
		mustLink(g.t, c.Unlink())
		s.roots = append(s.roots, c)
		var kids []*Node
		for _, k := range c.kids.nodes() {
			if !k.isKey() && !fzPadded(k) {
				kids = append(kids, k)
			}
		}
		if len(kids) == 0 {
			return nil
		}
		n := fzPick(g, kids)
		var dst *Tree
		var parent *Node
		if g.rng.IntN(2) == 0 {
			dst = linkedTree(g, s, g.xml(g.rng.IntN(3), false))
			o, err := dst.NewOpaque(fzPick(g, []*Node{nil, dst.top.first()}), "o", "", OpaqueOptions{Module: "fz"})
			if err != nil {
				g.t.Fatal(err)
			}
			parent = o
		} else {
			dst = parseOnlyXML(g.t, g.set2, g.xml(2, false))
			s.trees = append(s.trees, dst)
			s.knobs = append(s.knobs, fzWork("destination", &dst.work))
			for m := range dst.top.first().All() {
				if m.Path() == cpath {
					parent = m
				}
			}
			if parent == nil {
				return nil
			}
		}
		s.op = func() error { return dst.Insert(parent, n) }
		rest := slices.DeleteFunc(slices.Clone(kids), func(k *Node) bool { return k == n })
		if len(rest) != 0 {
			next := fzPick(g, rest)
			if g.rng.IntN(2) == 0 {
				s.then, s.thenKind = func() error { return dst.Insert(parent, next) }, "insert"
			} else {
				s.then, s.thenKind = next.Remove, "remove"
			}
		}
	case "merge-runs":
		// lyd_merge_sibling_r inserts each copy with lyd_insert_node: merging retained runs as
		// one batch must leave the metadata and later removals as merging them one by one.
		vals := g.vals(2 + g.rng.IntN(8))
		cut := 1 + g.rng.IntN(len(vals)-1)
		srcVals, dstVals := vals[:cut], vals[cut:]
		if g.rng.IntN(2) == 0 {
			slices.Sort(srcVals)
			slices.Sort(dstVals)
		}
		doc := func(vs []string) string {
			var b strings.Builder
			b.WriteString(`<top xmlns="urn:fz">`)
			for _, v := range vs {
				b.WriteString("<sl>" + v + "</sl>")
			}
			b.WriteString(g.pads("sl") + `</top>`)
			return b.String()
		}
		order := slices.Clone(vals)
		g.rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		dst := linkedTree(g, s, doc(dstVals))
		src := parseOnlyXML(g.t, g.set, doc(srcVals))
		s.trees = append(s.trees, src)
		s.op = func() error { return dst.Merge(src) }
		// then removes one member; got is the removal's result, every member's metadata slots
		// and the tree
		remove := func(tr *Tree) error {
			for _, n := range tr.top.first().kids.All() {
				if n.Value() == order[0] {
					return n.Remove()
				}
			}
			return nil
		}
		outcome := func(tr *Tree, err string) string {
			var slots []string
			sib := &tr.top.first().kids
			for _, n := range sib.All() {
				slots = append(slots, fmt.Sprintf("%s:%d:%t", n.Value(), len(sib.metaGroups(n)), n.inRB))
			}
			return fmt.Sprint(err, slots, typedDump(tr))
		}
		var removed string
		s.thenKind = "remove"
		s.then = func() error {
			err := remove(dst)
			removed = fzErrText(err)
			return err
		}
		s.got = func() string { return outcome(dst, removed) }
		s.ref = func() string {
			ref := parseOnlyXML(g.t, g.set, doc(dstVals))
			for _, v := range srcVals {
				if err := ref.Merge(parseOnlyXML(g.t, g.set, doc([]string{v}))); err != nil {
					return err.Error()
				}
			}
			return outcome(ref, fzErrText(remove(ref)))
		}
	}
	if s.op == nil {
		return nil
	}
	return s
}

// linkedTree is a linked tree of doc swept as "tree".
func linkedTree(g *fzGen, s *fzScenario, doc string) *Tree {
	tr := parseOnlyXML(g.t, g.set, doc)
	s.trees = append(s.trees, tr)
	s.knobs = append(s.knobs, fzWork("tree", &tr.work))
	return tr
}

// followUp is a journaled mutation of dst that reaches the descendants of a (validation defaults,
// a merge, a trim, a removal).
func (g *fzGen) followUp(s *fzScenario, dst *Tree, a *Node) func() error {
	switch g.rng.IntN(4) {
	case 0:
		s.thenKind = "validate"
		return func() error {
			_, err := dst.Validate(context.Background(), ValidateOptions{NoState: true})
			return err
		}
	case 1:
		src := parseOnlyXML(g.t, g.set, g.xml(2, false))
		s.trees = append(s.trees, src)
		s.thenKind = "merge"
		return func() error { return dst.Merge(src) }
	case 2:
		expr := fzPick(g, []string{"/fz:top/np/ll", "/fz:top/np/np2", "/fz:top/kl/q", "/fz:top/kl/c", "/fz:top/kl[p]"})
		s.thenKind = "trim"
		return func() error {
			_, err := dst.TrimXPath(expr, XPathOptions{})
			return err
		}
	}
	var under []*Node
	for n := range a.All() {
		if n != a && !n.isKey() {
			under = append(under, n)
		}
	}
	if len(under) == 0 {
		return nil
	}
	s.thenKind = "remove"
	return fzPick(g, under).Remove
}

// fzSnapDiff names what a refusal changed in one snapshot.
func fzSnapDiff(was, now hashBudgetSnapshot) string {
	var out []string
	if !reflect.DeepEqual(was.dump, now.dump) {
		out = append(out, fmt.Sprintf("dump %v\nwas  %v", now.dump, was.dump))
	}
	for n, v := range now.index {
		if was.index[n] != v {
			out = append(out, fmt.Sprintf("node %s:\n  now %s\n  was %s", n.Path(), v, was.index[n]))
		}
	}
	for s, v := range now.sibs {
		if was.sibs[s] != v {
			out = append(out, fmt.Sprintf("siblings %p:\n  now %s\n  was %s", s, v, was.sibs[s]))
		}
	}
	for n := range was.index {
		if _, ok := now.index[n]; !ok {
			out = append(out, "node gone: "+n.Path())
		}
	}
	return strings.Join(out, "\n")
}

func fzErrText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// fzPad widens every padded sibling set for the refusal cost check.
const fzPad = 256

// fzCheck runs one scenario's probes; build rebuilds it identically, its wide sibling sets
// padded by pad instances.
func fzCheck(t *testing.T, seed uint64, build func(pad int) *fzScenario) (refusals int, wantErr string) {
	ref := build(0)
	if ref == nil {
		return 0, "skipped"
	}
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d (%s): %s", seed, ref.entry, fmt.Sprintf(format, args...))
	}
	wantErr = fzErrText(ref.op())
	calls := 0
	if ref.cb != nil {
		calls = ref.cb.calls
	}
	wantThen := ""
	if ref.then != nil {
		wantThen = fzErrText(ref.then())
	}
	want := ref.dump()
	if ref.ref != nil {
		if got, exp := ref.got(), ref.ref(); got != exp {
			fail("history differs from its reference history:\n%s\nwant\n%s", got, exp)
		}
	}
	for phase := range 2 { // 0 refuses op; 1 refuses then, after an unrefused op
		if phase == 1 && ref.then == nil {
			break
		}
		wantRun := wantErr
		run := func(s *fzScenario) func() error { return s.op }
		if phase == 1 {
			wantRun = wantThen
			run = func(s *fzScenario) func() error { return s.then }
		}
		prepare := func(s *fzScenario) {
			t.Helper()
			if phase == 1 && fzErrText(s.op()) != wantErr {
				fail("rebuilt op differs")
			}
		}
		// finish completes the history after phase's operation and compares it with want.
		finish := func(s *fzScenario, what string) {
			t.Helper()
			if phase == 0 && s.then != nil {
				if err := s.then(); fzErrText(err) != wantThen {
					fail("%s: then: %v, want %q", what, err, wantThen)
				}
			}
			if got := s.dump(); !reflect.DeepEqual(got, want) {
				fail("%s: history differs:\n%v\nwant\n%v", what, got, want)
			}
			if s.got != nil && ref.got != nil {
				if got, exp := s.got(), ref.got(); got != exp {
					fail("%s: history outcome differs:\n%s\nwant\n%s", what, got, exp)
				}
			}
		}
		// refused checks one refusal: no change, then a retry without the limit
		refused := func(s *fzScenario, before []hashBudgetSnapshot, err error, what string, reset func()) {
			t.Helper()
			if err == nil || !errors.Is(err, yang.ErrBudget) {
				if fzErrText(err) != wantRun {
					fail("%s: %v, want %q or ErrBudget", what, err, wantRun)
				}
				finish(s, what+" (unrefused)")
				return
			}
			if after := s.snapshot(); !reflect.DeepEqual(after, before) {
				for i := range after {
					if !reflect.DeepEqual(after[i], before[i]) {
						fail("%s: refusal changed tree/root %d:\n%s", what, i, fzSnapDiff(before[i], after[i]))
					}
				}
			}
			refusals++
			for _, tr := range s.trees {
				checkTreeRetainedRB(t, tr)
			}
			if w := s.armedUndo(); w != "" {
				fail("%s: refusal left a removal undo armed at %s", what, w)
			}
			reset()
			if err := run(s)(); fzErrText(err) != wantRun {
				fail("%s: retry: %v, want %q", what, err, wantRun)
			}
			finish(s, what+" refusal+retry")
		}
		for k := range ref.knobs {
			s := build(0)
			prepare(s)
			var points []uint64
			s.knobs[k].set(0, &points)
			if fzErrText(run(s)()) != wantRun {
				fail("traced run differs")
			}
			s.knobs[k].set(0, nil)
			need := s.knobs[k].used()
			limits := map[uint64]bool{}
			for limit := uint64(1); limit < need; limit++ {
				limits[limit] = true
			}
			for _, at := range points {
				if at > 1 && at <= need {
					limits[at-1] = true
				}
			}
			if !fzFull && len(limits) > fzSample {
				// a seeded sample keeps the default run short; YANG_STAGE3_FUZZ_FULL=1 sweeps all
				keep := rand.New(rand.NewPCG(seed, uint64(k)+uint64(phase)<<32)) //nolint:gosec // deterministic sample
				total := len(limits)
				for limit := range limits {
					if keep.IntN(total) >= fzSample {
						delete(limits, limit)
					}
				}
			}
			for limit := range limits {
				s := build(0)
				prepare(s)
				before := s.snapshot()
				s.knobs[k].set(limit, nil)
				err := run(s)()
				refused(s, before, err, fmt.Sprintf("phase %d %s limit %d/%d", phase, s.knobs[k].name, limit, need),
					func() { s.knobs[k].set(0, nil) })
			}
		}
		entry := ref.entry
		if phase == 1 {
			entry = ref.thenKind
		}
		fzCost(t, fail, entry, phase, build, run)
		if phase == 1 {
			break
		}
		for at := range calls {
			s := build(0)
			before := s.snapshot()
			s.cb.failAt = at
			err := s.op()
			if !errors.Is(err, yang.ErrBudget) {
				fail("callback %d/%d: %v, want ErrBudget", at, calls, err)
			}
			refused(s, before, err, fmt.Sprintf("callback %d/%d", at, calls), func() { s.cb.calls, s.cb.failAt = 0, -1 })
		}
	}
	return refusals, wantErr
}

// fzReadFirst are the entries that read input in proportion to its size before their first
// mutation, under budgets of their own (XPath, validation, diffs, parsing); fzLinearAlloc adds
// main's read-only source bound walk (Dup, Merge: TestStage3RefusalCostIndependentOfInput),
// which allocates but does not visit.
var (
	fzReadFirst   = []string{"merge-diff", "merge-diff-tree", "apply-diff", "trim", "validate", "validate-op", "parse-op", "new-path"}
	fzLinearAlloc = []string{"dup", "merge"}
)

// fzCost refuses phase's operation at the first unit of every knob, for the scenario and for its
// padded twin: the sibling visits (siblings.visit, on every counter involved) and the allocation
// must not grow with the padding. State comparison cannot see a scan that runs before the
// first charge; this does.
func fzCost(t *testing.T, fail func(string, ...any), entry string, phase int, build func(int) *fzScenario,
	run func(*fzScenario) func() error) {
	t.Helper()
	if slices.Contains(fzReadFirst, entry) {
		return
	}
	measure := func(pad int) (allocated uint64, visits int64, ok bool) {
		s := build(pad)
		if s == nil {
			return 0, 0, false
		}
		if phase == 1 {
			_ = s.op()
		}
		counters := s.counters()
		for _, c := range counters {
			c.visits = true
			c.Store(0)
		}
		for _, k := range s.knobs {
			k.set(1, nil)
		}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		err := run(s)()
		runtime.ReadMemStats(&after)
		for _, c := range counters {
			visits += c.Load()
			c.visits = false
		}
		return after.TotalAlloc - before.TotalAlloc, visits, errors.Is(err, yang.ErrBudget)
	}
	smallBytes, smallVisits, ok := measure(0)
	if !ok {
		return
	}
	largeBytes, largeVisits, ok := measure(fzPad)
	if !ok {
		return
	}
	if largeVisits > 2*smallVisits+64 || !slices.Contains(fzLinearAlloc, entry) && largeBytes > 2*smallBytes+64<<10 {
		if os.Getenv("FZ_COSTLOG") != "" {
			t.Logf("COST %s phase %d: %d B/%d visits, padded %d B/%d visits", entry, phase, smallBytes, smallVisits, largeBytes, largeVisits)
			return
		}
		fail("%s refused at one unit (phase %d): its work grows with the input (%d B/%d visits, padded %d B/%d visits)",
			entry, phase, smallBytes, smallVisits, largeBytes, largeVisits)
	}
}

func TestStage3RefusalIsNoOpFuzz(t *testing.T) {
	set, set2 := reviewDiffSet(t, fzModule), reviewDiffSet(t, fzModule)
	iters := 200
	if raceEnabled {
		iters = 40
	}
	if v := os.Getenv("YANG_STAGE3_FUZZ_ITERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatal(err)
		}
		iters = n
	}
	base := uint64(0x3017)
	if v := os.Getenv("YANG_STAGE3_FUZZ_SEED"); v != "" {
		n, err := strconv.ParseUint(v, 0, 64)
		if err != nil {
			t.Fatal(err)
		}
		base = n
	}
	refusals, ran, failed, skipped := 0, map[string]int{}, map[string]int{}, map[string]int{}
	for i := range iters {
		seed := base + uint64(i)
		entry := fzEntries[seed%uint64(len(fzEntries))] // YANG_STAGE3_FUZZ_SEED=seed ITERS=1 repeats it
		if only := os.Getenv("YANG_STAGE3_FUZZ_ENTRY"); only != "" {
			entry = only // every seed builds this entry's scenario
		}
		build := func(pad int) *fzScenario {
			g := &fzGen{t: t, rng: rand.New(rand.NewPCG(0x301, seed)), set: set, set2: set2, pad: pad} //nolint:gosec // deterministic scenario
			return g.scenario(entry)
		}
		n, wantErr := fzCheck(t, seed, build)
		refusals += n
		switch wantErr {
		case "":
			ran[entry]++
		case "skipped":
			skipped[entry]++
		default:
			failed[entry]++ // an unrefused failure (an argument or validation error): still swept
		}
	}
	t.Logf("%d iterations from seed %#x, %d refusals; succeeding scenarios %v, failing %v, not applicable %v",
		iters, base, refusals, ran, failed, skipped)
}

// TestStage3DetachedBindingCoversTree: linking into a detached tree binds the whole tree to the
// linking tree's counter, so a later binding of its root (Insert) cannot stop at a set already
// bound and leave descendants on a counter no journal sees.
func TestStage3DetachedBindingCoversTree(t *testing.T) {
	set := reviewDiffSet(t, `module stage3-bind {yang-version 1.1;namespace "urn:stage3-bind";prefix s;
 container a {container b {leaf-list w {type string;} list l {key k; leaf k {type string;} leaf-list q {type string;}}}}}`)
	tr := parseOnlyXML(t, set, `<a xmlns="urn:stage3-bind"><b><w>m</w><l><k>1</k><q>x</q><q>y</q></l></b></a>`)
	a := tr.top.first()
	mustLink(t, a.Unlink())
	dst := newTree(set)
	b := a.kids.first()
	if _, err := dst.NewTerm(b, "w", "z", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	bound := func(when string) {
		t.Helper()
		for n := range a.allAny() {
			if w := childStorage(n).work; w != &dst.work {
				t.Fatalf("%s: %s is bound to %p, not the destination's %p", when, n.Path(), w, &dst.work)
			}
		}
	}
	bound("after NewTerm")
	if err := dst.Insert(nil, a); err != nil {
		t.Fatal(err)
	}
	bound("after Insert")
	checkTreeRetainedRB(t, dst)
}

// TestStage3BatchMergeFreesIncomingSlot: copies merged as one batch into a retained run lose their
// first (empty) metadata slot as each native lyds_insert frees it, so each member can be removed
// again (a kept empty slot ahead of the destination's graph made removal refuse, D-0121).
func TestStage3BatchMergeFreesIncomingSlot(t *testing.T) {
	set := reviewDiffSet(t, stage3StabModule)
	dst := parseOnlyXML(t, set, `<a xmlns="urn:stage3-stab"><v>3</v><v>4</v></a>`)
	src := parseOnlyXML(t, set, `<a xmlns="urn:stage3-stab"><v>1</v><v>2</v></a>`)
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	checkTreeRetainedRB(t, dst)
	a := dst.top.first()
	for _, n := range a.kids.All() {
		if groups := a.kids.metaGroups(n); len(groups) > 1 {
			t.Fatalf("%s keeps %d metadata slots after the merge", n.Path(), len(groups))
		}
	}
	for _, v := range []string{"1", "2", "3", "4"} {
		var n *Node
		for _, c := range a.kids.All() {
			if c.Value() == v {
				n = c
			}
		}
		if err := n.Remove(); err != nil {
			t.Fatalf("removing %s after the merge: %v", v, err)
		}
		checkTreeRetainedRB(t, dst)
	}
}

// TestStage3AdoptCarriedChargesBeforeScan: adoption (attach, batch publication, whole-forest
// moves) reserves its scan of a 33-slot carried queue before reading it, so an exhausted quota
// neither scans nor publishes a slot.
func TestStage3AdoptCarriedChargesBeforeScan(t *testing.T) {
	set := reviewDiffSet(t, stage3ExtraModule)
	d := stage3DetachedSingleton(t, set, 32)
	if err := stage3AddList(t, d.tree, "20").Remove(); err != nil {
		t.Fatal(err)
	}
	mustLink(t, d.Unlink())
	d.carriedRB = append([]rbCarry{{d.detachedGroup, d.detachedRB}}, d.carriedRB...)
	d.detachedRB, d.detachedGroup = nil, nil
	slots := len(d.carriedRB)
	dst := newTree(set)
	dst.work.hashLimit, dst.work.visits = 1, true
	if err := dst.adoptCarried(&dst.top, d); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("low quota: %v, want ErrBudget", err)
	}
	if visits := dst.work.Load(); visits != 0 || len(d.carriedRB) != slots || len(dst.top.rbOrders) != 0 {
		t.Fatalf("refused adoption scanned %d of %d slots, left %d carried and published %d",
			visits, slots, len(d.carriedRB), len(dst.top.rbOrders))
	}
}

// TestStage3DetachedCaptureChargesAncestors: the detached-parent capture walks to its root in
// charged steps and stops at an already failed journal before any walk or capture.
func TestStage3DetachedCaptureChargesAncestors(t *testing.T) {
	set := reviewDiffSet(t, `module stage3-anc {yang-version 1.1;namespace "urn:stage3-anc";prefix s;
 container a {container b {container c {leaf-list w {type string;}}}}}`)
	tr := parseOnlyXML(t, set, `<a xmlns="urn:stage3-anc"><b><c><w>x</w></c></b></a>`)
	a := tr.top.first()
	c := a.kids.first().kids.first()
	mustLink(t, a.Unlink())
	for _, failed := range []bool{true, false} {
		dst := newTree(set)
		j := beginHashTransaction(dst)
		dst.work.visits = true
		if failed {
			j.err = fmt.Errorf("earlier: %w", yang.ErrBudget) // with quota left: no walk either
		} else {
			dst.work.hashLimit = dst.work.hashUsed // nothing left after the journal's own capture
		}
		used := dst.work.hashUsed
		if err := j.captureDetachedParent(c); !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("failed=%t: %v, want ErrBudget", failed, err)
		}
		if len(j.nodes) != 0 || len(j.siblings) != 0 || dst.work.hashUsed != used || dst.work.Load() != 0 {
			t.Fatalf("failed=%t: refused capture journaled %d nodes, %d sets, spent %d, walked %d ancestors", failed,
				len(j.nodes), len(j.siblings), dst.work.hashUsed-used, dst.work.Load())
		}
		dst.work.journal = nil
	}
}

// TestStage3DetachedFirstRBCreationBudget: a NewTerm or one-node Insert under a detached parent
// whose run gets its first retained graph (rbPos) binds the detached tree before that
// publication, so the graph cannot raise link's binding bound past its reservation. Every hash
// limit refuses with the detached tree and the source as they were; a retry then succeeds.
func TestStage3DetachedFirstRBCreationBudget(t *testing.T) {
	set := reviewDiffSet(t, `module stage3-first {yang-version 1.1;namespace "urn:stage3-first";prefix s;
 container a {container b {leaf-list w {type string;}}}}`)
	for _, how := range []string{"new-term", "insert-linked", "insert-detached"} {
		build := func() (*Tree, func() []hashBudgetSnapshot, func() error) {
			tr := parseOnlyXML(t, set, `<a xmlns="urn:stage3-first"><b><w>m</w></b></a>`)
			root := tr.top.first()
			mustLink(t, root.Unlink())
			b := root.kids.first()
			dst := newTree(set)
			src := parseOnlyXML(t, set, `<a xmlns="urn:stage3-first"><b><w>c</w><w>x</w></b></a>`)
			w := src.top.first().kids.first().kids.first()
			snap := func() []hashBudgetSnapshot {
				return []hashBudgetSnapshot{snapshotHashBudgetDetached(set, root), snapshotHashBudgetTree(src),
					snapshotHashBudgetDetached(set, w)}
			}
			switch how {
			case "new-term":
				return dst, snap, func() error { _, err := dst.NewTerm(b, "w", "c", NewOptions{}); return err }
			case "insert-detached":
				mustLink(t, w.Unlink())
			}
			return dst, snap, func() error { return dst.Insert(b, w) }
		}
		dst, _, run := build()
		var points []uint64
		dst.work.hashTrace = &points
		if err := run(); err != nil {
			t.Fatalf("%s: %v", how, err)
		}
		need := dst.work.hashUsed
		for limit := uint64(1); limit < need; limit++ {
			dst, snap, run := build()
			before := snap()
			dst.work.hashLimit = limit
			err := run()
			if err == nil {
				continue
			}
			if !errors.Is(err, yang.ErrBudget) {
				t.Fatalf("%s limit %d/%d: %v", how, limit, need, err)
			}
			if after := snap(); !reflect.DeepEqual(after, before) {
				for i := range after {
					if !reflect.DeepEqual(after[i], before[i]) {
						t.Fatalf("%s limit %d/%d: refusal changed %d:\n%s", how, limit, need, i, fzSnapDiff(before[i], after[i]))
					}
				}
			}
			dst.work.hashLimit = 0
			if err := run(); err != nil {
				t.Fatalf("%s limit %d/%d: retry: %v", how, limit, need, err)
			}
		}
	}
}

// TestStage3DetachedCaptureOncePerRoot: an operation's second boundary (Insert's, then
// linkOrigin's) does not walk a detached tree its journal holds whole again; the repeat costs
// only the charged ancestor walk, whatever the tree's width.
func TestStage3DetachedCaptureOncePerRoot(t *testing.T) {
	set := reviewDiffSet(t, `module stage3-once {yang-version 1.1;namespace "urn:stage3-once";prefix s;
 container a {container b {leaf-list w {type string;}}}}`)
	var in strings.Builder
	in.WriteString(`<a xmlns="urn:stage3-once"><b>`)
	for i := range 512 {
		fmt.Fprintf(&in, "<w>%d</w>", i)
	}
	in.WriteString(`</b></a>`)
	tr := parseOnlyXML(t, set, in.String())
	a := tr.top.first()
	b := a.kids.first()
	mustLink(t, a.Unlink())
	dst := newTree(set)
	j := beginHashTransaction(dst)
	if err := j.captureSiblings(&b.kids); err != nil { // journaled lazily before the boundary
		t.Fatal(err)
	}
	start := dst.work.hashUsed
	if err := j.captureDetachedParent(b.kids.first()); err != nil {
		t.Fatal(err)
	}
	if first := dst.work.hashUsed - start; first < 512 {
		t.Fatalf("walking a captured set of 512 children charged %d", first)
	}
	again := dst.work.hashUsed
	if err := j.captureDetachedParent(b.kids.first()); err != nil {
		t.Fatal(err)
	}
	if repeat := dst.work.hashUsed - again; repeat > 4 {
		t.Fatalf("a repeated capture charged %d", repeat)
	}
	dst.work.journal = nil
}

// TestStage3CompareCostChargedPerKey: a comparison's bound is charged one key pair at a time, so
// a refused quota stops before reading the rest of a long key prefix, and an unlimited charge
// equals rbCompareCost.
func TestStage3CompareCostChargedPerKey(t *testing.T) {
	set := reviewDiffSet(t, `module stage3-keys {yang-version 1.1;namespace "urn:stage3-keys";prefix s;
 list l {key "a b c d e f"; leaf a {type string;} leaf b {type string;} leaf c {type string;}
  leaf d {type string;} leaf e {type string;} leaf f {type string;}}}`)
	tr := parseOnlyXML(t, set, `<l xmlns="urn:stage3-keys"><a>1</a><b>2</b><c>3</c><d>4</d><e>5</e><f>6</f></l>`+
		`<l xmlns="urn:stage3-keys"><a>1</a><b>2</b><c>3</c><d>4</d><e>5</e><f>7</f></l>`)
	x, y := tr.top.At(0), tr.top.At(1)
	full := &workCounter{}
	if err := chargeCompareCost(full, x, y); err != nil || full.hashUsed != rbCompareCost(x, y) {
		t.Fatalf("unlimited: %v, charged %d, rbCompareCost %d", err, full.hashUsed, rbCompareCost(x, y))
	}
	w := &workCounter{hashLimit: 2}
	if err := chargeCompareCost(w, x, y); !errors.Is(err, yang.ErrBudget) || w.hashUsed > 2 {
		t.Fatalf("limited: %v after %d units", err, w.hashUsed)
	}
}

// TestStage3TransferMovesIndexWorkHooks: a whole-forest transfer (payload adoption) moves the
// ordered index too; its work hooks then count on the new owner's counter, not the emptied
// source's (which has none and would drop the work).
func TestStage3TransferMovesIndexWorkHooks(t *testing.T) {
	set := reviewDiffSet(t, stage3StabModule)
	tr := stage3StabTree(t, set, []string{"1", "2", "3"}, "")
	src := &tr.top.first().kids
	src.orderBuild(tr)
	var dst siblings
	mustLink(t, dst.transferFrom(src))
	w := &workCounter{}
	dst.setWork(w)
	dst.order.seq.InsertBefore(nil, &Node{}) // any index operation reports its visits
	if w.Load() == 0 {
		t.Fatal("index work after the transfer was not counted on the new owner")
	}
}

// TestStage3FailedTouchIsSticky: the index maintenance helpers (orderBuild, opOrderAdd,
// opRemove, refreshRB, refreshExact) skip their update when touch fails. That is safe only
// because a failed touch always leaves the sticky budget error the operation boundary returns
// (and its journal restores on): a refused capture, and a set captured before a later refusal.
func TestStage3FailedTouchIsSticky(t *testing.T) {
	set := reviewDiffSet(t, stage3StabModule)
	for _, captured := range []bool{false, true} {
		tr := stage3StabTree(t, set, []string{"1", "2", "3"}, "")
		s := &tr.top.first().kids
		j := beginHashTransaction(tr)
		if captured {
			mustLink(t, s.touch())
		}
		tr.work.hashLimit = tr.work.hashUsed
		if captured {
			if err := j.charge(1); err == nil { // a later refusal in the same operation
				t.Fatal("charge past the limit accepted")
			}
		}
		if err := s.touch(); !errors.Is(err, yang.ErrBudget) || !errors.Is(tr.work.hashErr, yang.ErrBudget) {
			t.Fatalf("captured=%t: touch %v, sticky %v", captured, err, tr.work.hashErr)
		}
		tr.work.journal = nil
	}
}

// TestStage3ReservationHistoryBudget: reserveHash checks the remaining quota without debiting
// it; the reserved work is charged when it runs. Every edit of a multi-edit history (sorted
// insertions into a linked and a detached run, retained graph created, grown and rebalanced)
// is swept at every hash limit: a reservation followed by other charges before its own work
// must never let a refusal publish part of an edit.
func TestStage3ReservationHistoryBudget(t *testing.T) {
	set := reviewDiffSet(t, `module stage3-hist {yang-version 1.1;namespace "urn:stage3-hist";prefix s;
 container a {container b {leaf-list w {type string;} list l {key k; leaf k {type string;}}}}}`)
	values := []string{"m", "c", "x", "a", "q", "e", "z", "b"}
	for _, detached := range []bool{false, true} {
		build := func(done int) (*Tree, *Node, func() []hashBudgetSnapshot) {
			tr := parseOnlyXML(t, set, `<a xmlns="urn:stage3-hist"><b/></a>`)
			root := tr.top.first()
			dst := tr
			if detached {
				mustLink(t, root.Unlink())
				dst = newTree(set)
			}
			b := root.kids.first()
			for _, v := range values[:done] {
				if _, err := dst.NewTerm(b, "w", v, NewOptions{}); err != nil {
					t.Fatal(err)
				}
				if _, err := dst.NewList(b, "l", []string{v}, NewOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			return dst, b, func() []hashBudgetSnapshot { return []hashBudgetSnapshot{snapshotHashBudgetDetached(set, root)} }
		}
		for i, v := range values {
			for _, kind := range []string{"term", "list", "move"} {
				// move: a one-node Insert from another tree (no journal); the destination set
				// has a children hash table from the fourth child on, so the sweep crosses the
				// whole link and attach (index, adoption, binding, hashes, keys)
				edit := func(dst *Tree, b *Node) error {
					switch kind {
					case "list":
						_, err := dst.NewList(b, "l", []string{v}, NewOptions{})
						return err
					case "move":
						src := parseOnlyXML(t, set, `<a xmlns="urn:stage3-hist"><b><l><k>`+v+`</k></l></b></a>`)
						return dst.Insert(b, src.top.first().kids.first().kids.first())
					}
					_, err := dst.NewTerm(b, "w", v, NewOptions{})
					return err
				}
				dst, b, _ := build(i)
				if err := edit(dst, b); err != nil {
					t.Fatal(err)
				}
				need := dst.work.hashUsed
				for limit := uint64(1); limit < need; limit++ {
					dst, b, snap := build(i)
					before := snap()
					dst.work.hashLimit = limit
					err := edit(dst, b)
					if err == nil {
						continue
					}
					if !errors.Is(err, yang.ErrBudget) {
						t.Fatalf("detached=%t edit %d %s limit %d: %v", detached, i, kind, limit, err)
					}
					if after := snap(); !reflect.DeepEqual(after, before) {
						t.Fatalf("detached=%t edit %d %s limit %d/%d: refusal changed the tree:\n%s",
							detached, i, kind, limit, need, fzSnapDiff(before[0], after[0]))
					}
					dst.work.hashLimit = 0
					if err := edit(dst, b); err != nil {
						t.Fatalf("detached=%t edit %d %s limit %d: retry: %v", detached, i, kind, limit, err)
					}
				}
			}
		}
	}
}

// TestStage3NewPathCleanupWithoutJournal: in the production configuration (default quota, no
// trace: no journal) a NewPath whose later segment fails removes what it created within the
// allowance reserved before the first insertion, leaving the tree as it was.
func TestStage3NewPathCleanupWithoutJournal(t *testing.T) {
	set := reviewDiffSet(t, `module stage3-np {yang-version 1.1;namespace "urn:stage3-np";prefix s;
 container c {list l {key k; leaf k {type string;} leaf v {type uint8;}} leaf-list w {type string;}}}`)
	var in strings.Builder
	in.WriteString(`<c xmlns="urn:stage3-np">`)
	for i := range 64 {
		fmt.Fprintf(&in, "<l><k>%03d</k></l>", i)
	}
	in.WriteString(`</c>`)
	tr := parseOnlyXML(t, set, in.String())
	before := snapshotHashBudgetTree(tr)
	if _, err := tr.NewPath("/stage3-np:c/l[k='010x']/v", "300", NewPathOptions{}); err == nil {
		t.Fatal("an out-of-range value was accepted")
	}
	if tr.work.journal != nil {
		t.Fatal("journal left open")
	}
	// lyd_new_path then lyd_free_tree: the nodes, values, hashes and links are as they were;
	// the set's registration history and generation advance as natively
	if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(after.dump, before.dump) || !reflect.DeepEqual(after.index, before.index) {
		t.Fatalf("failed NewPath changed the tree:\n%s", fzSnapDiff(before, after))
	}
	checkTreeRetainedRB(t, tr)
	if _, err := tr.NewPath("/stage3-np:c/l[k='010x']/v", "30", NewPathOptions{}); err != nil {
		t.Fatal(err)
	}
	// a created container between 1,000 siblings on each side: the cleanup's position scan is
	// linear in the destination, and the allowance covers it
	wide := reviewDiffSet(t, `module stage3-wide {yang-version 1.1;namespace "urn:stage3-wide";prefix s;
 container c {leaf-list a {type string; ordered-by user;} container m {leaf x {type uint8;}}
  leaf-list z {type string; ordered-by user;}}}`)
	in.Reset()
	in.WriteString(`<c xmlns="urn:stage3-wide">`)
	for i := range 1000 {
		fmt.Fprintf(&in, "<a>%d</a>", i)
	}
	for i := range 1000 {
		fmt.Fprintf(&in, "<z>%d</z>", i)
	}
	in.WriteString(`</c>`)
	tr = parseOnlyXML(t, wide, in.String())
	before = snapshotHashBudgetTree(tr)
	if _, err := tr.NewPath("/stage3-wide:c/m/x", "300", NewPathOptions{}); err == nil || errors.Is(err, yang.ErrBudget) {
		t.Fatalf("wide: %v, want the value error", err)
	}
	if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(after.dump, before.dump) || !reflect.DeepEqual(after.index, before.index) {
		t.Fatalf("wide: failed NewPath changed the tree:\n%s", fzSnapDiff(before, after))
	}
	// long keys at the top level: the cleanup's retained-tree search compares 4 KiB key texts,
	// and the allowance covers those comparisons too
	top := reviewDiffSet(t, `module stage3-top {yang-version 1.1;namespace "urn:stage3-top";prefix s;
 list l {key k; leaf k {type string;} leaf v {type uint8;}}}`)
	tr = parseOnlyXML(t, top, `<l xmlns="urn:stage3-top"><k>`+strings.Repeat("a", 4096)+`</k></l><l xmlns="urn:stage3-top"><k>`+
		strings.Repeat("b", 4096)+`</k></l>`)
	before = snapshotHashBudgetTree(tr)
	if _, err := tr.NewPath("/stage3-top:l[k='"+strings.Repeat("z", 4096)+"']/v", "300", NewPathOptions{}); err == nil || errors.Is(err, yang.ErrBudget) {
		t.Fatalf("long keys: %v, want the value error", err)
	}
	if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(after.dump, before.dump) || !reflect.DeepEqual(after.index, before.index) {
		t.Fatalf("long keys: failed NewPath changed the tree:\n%s", fzSnapDiff(before, after))
	}
}

// TestStage3DupAnyKeepsNoStaleCounter: a copied anydata node's live child storage is its payload;
// its unused kids are bound to no counter, so a copy moved to another tree retains and consults
// only the counter its payload is bound to.
func TestStage3DupAnyKeepsNoStaleCounter(t *testing.T) {
	set := reviewDiffSet(t, `module stage3-any {yang-version 1.1;namespace "urn:stage3-any";prefix s;
 anydata p; container c {leaf-list v {type string;}}}`)
	src := parseOnlyXML(t, set, `<p xmlns="urn:stage3-any"><c><v>1</v><v>2</v></c></p>`)
	tmp := newTree(set)
	d, _, err := tmp.Dup(src.top.first(), nil, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if d.kids.work != nil {
		t.Fatalf("the copy's unused kids are bound to %p", d.kids.work)
	}
	dst := newTree(set)
	if err := dst.Insert(nil, d); err != nil {
		t.Fatal(err)
	}
	if d.kids.work != nil || d.any.children.work != &dst.work {
		t.Fatalf("after the move: kids %p, payload %p, destination %p", d.kids.work, d.any.children.work, &dst.work)
	}
}

// TestStage3SnapshotSeesSlotsAndPayloads: the rollback evidence (snapshotHashBudgetRoots) tells
// apart two empty primary metadata owners swapped, and a change inside an anydata payload.
func TestStage3SnapshotSeesSlotsAndPayloads(t *testing.T) {
	set := reviewDiffSet(t, `module stage3-snap {yang-version 1.1;namespace "urn:stage3-snap";prefix s;
 anydata p; container c {leaf-list v {type string;}}}`)
	tr := parseOnlyXML(t, set, `<c xmlns="urn:stage3-snap"><v>1</v><v>2</v></c><p xmlns="urn:stage3-snap"><c><v>3</v></c></p>`)
	var sib *siblings
	for n := range tr.Top() {
		if n.schema.Name == "c" {
			sib = &n.kids
		}
	}
	a, b := sib.At(0), sib.At(1)
	if sib.rbGroups == nil {
		sib.rbGroups = map[*Node]*rbGroup{}
	}
	sib.rbGroups[a], sib.rbGroups[b] = &rbGroup{}, &rbGroup{}
	before := snapshotHashBudgetTree(tr)
	sib.rbGroups[a], sib.rbGroups[b] = sib.rbGroups[b], sib.rbGroups[a]
	if reflect.DeepEqual(snapshotHashBudgetTree(tr), before) {
		t.Fatal("swapped empty primary owners are not visible")
	}
	before = snapshotHashBudgetTree(tr)
	var payload *Node
	for n := range tr.Top() {
		if n.any != nil {
			payload = n.any.children.first()
		}
	}
	if payload == nil {
		t.Fatal("setup: no parsed payload")
	}
	payload.kids.gen++
	if reflect.DeepEqual(snapshotHashBudgetTree(tr), before) {
		t.Fatal("a payload descendant's change is not visible")
	}
}

// TestStage3ValueChangeRefusedTouchIsSticky: a value change whose node capture is refused by a
// journal lent from another tree (whose counter the refusal charges) leaves the value as it was
// and the changing tree's sticky error set, so changeTermVal reports no change.
func TestStage3ValueChangeRefusedTouchIsSticky(t *testing.T) {
	set := reviewDiffSet(t, stage3StabModule)
	tr := parseOnlyXML(t, set, `<a xmlns="urn:stage3-stab"><r>3</r></a>`)
	other := newTree(set)
	j := beginHashTransaction(other)
	tr.work.journal = j
	other.work.hashLimit = other.work.hashUsed // the capture of the changed node is refused
	r := tr.top.first().kids.first()
	v, diag := types.Store(r.schema.Type, "5", types.FormatJSON, types.HintData, types.ModuleNames{Set: set}, r.schema)
	if diag != nil {
		t.Fatal(diag)
	}
	changed, _ := tr.changeTermVal(r, v, false)
	if changed || r.Value() != "3" || !errors.Is(tr.work.hashErr, yang.ErrBudget) {
		t.Fatalf("changed %t, value %q, sticky %v", changed, r.Value(), tr.work.hashErr)
	}
	tr.work.journal, other.work.journal = nil, nil
}

// TestStage3ForeignOperationLeaderCharged: an operation child's leader lookup among siblings of
// another context scans them by name (schemaIndex's foreignFor branch); that scan is reserved
// before it runs, so an exhausted quota visits none of a wide set.
func TestStage3ForeignOperationLeaderCharged(t *testing.T) {
	const module = `module stage3-op {yang-version 1.1;namespace "urn:stage3-op";prefix s;
 rpc r {input {leaf-list w {type string;} leaf-list u {type string;}}}}`
	set, set2 := reviewDiffSet(t, module), reviewDiffSet(t, module)
	var in strings.Builder
	in.WriteString(`{"stage3-op:r":{"w":[`)
	for i := range 512 {
		if i > 0 {
			in.WriteString(",")
		}
		fmt.Fprintf(&in, `"%d"`, i)
	}
	in.WriteString(`]}}`)
	tr, op := opIndexParseRPC(t, set2, in.String())
	_, own := opIndexParseRPC(t, set, `{"stage3-op:r":{"u":["x"]}}`)
	u := own.kids.first().schema
	n := newTerm(u, types.Value{})
	sib := &op.kids
	if !opChild(u) || mixedSortedLookup(sib, n) || !sib.foreignFor(u) {
		t.Fatal("setup: not the foreign single-context branch")
	}
	// a quota above a logarithmic lookup and far below the scan
	tr.work.hashLimit, tr.work.visits, tr.work.hashUsed, tr.work.hashErr = 64, true, 0, nil
	tr.work.Store(0)
	sib.work = &tr.work
	if lo := tr.sortedLeaderIndex(sib, n); lo != -1 || !errors.Is(tr.work.hashErr, yang.ErrBudget) || tr.work.Load() > 8 {
		t.Fatalf("leader %d, sticky %v, %d sibling visits", lo, tr.work.hashErr, tr.work.Load())
	}
}

// TestStage3SnapshotSeesDetachedGraphs: the rollback evidence covers a detached node's own and
// carried retained graphs (topology and work binding), not only their identities.
func TestStage3SnapshotSeesDetachedGraphs(t *testing.T) {
	set := reviewDiffSet(t, stage3ExtraModule)
	d := stage3DetachedSingleton(t, set, 2)
	if err := stage3AddList(t, d.tree, "20").Remove(); err != nil {
		t.Fatal(err)
	}
	mustLink(t, d.Unlink())
	if d.detachedRB == nil || len(d.carriedRB) == 0 {
		t.Fatalf("setup: detached %v, %d carried", d.detachedRB != nil, len(d.carriedRB))
	}
	snap := func() hashBudgetSnapshot { return snapshotHashBudgetDetached(set, d) }
	before := snap()
	d.detachedRB.work = &workCounter{}
	if reflect.DeepEqual(snap(), before) {
		t.Fatal("a detached graph's work binding is not visible")
	}
	before = snap()
	d.carriedRB[0].order.work = &workCounter{}
	if reflect.DeepEqual(snap(), before) {
		t.Fatal("a carried graph's work binding is not visible")
	}
}
