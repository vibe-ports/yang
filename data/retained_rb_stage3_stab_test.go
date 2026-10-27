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
		err := dst.MergeDiffTree(a, src.top.first().kids.first(), MergeDiffOptions{Callback: func(s, d *Node) error {
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
	leaves("sl", g.vals(count(4)))
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
		if n.schema != nil && n.schema.Name == sn.Name && n.parent != nil && n.parent.schema.Name == sn.Parent.Name {
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
	"validate", "validate-op", "parse-op"}

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
			if n.schema != nil && !n.schema.IsKey() && (!terms || n.isTerm()) {
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
				if n.parent != nil {
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
	}
	if s.op == nil {
		return nil
	}
	return s
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

// fzCheck runs one scenario's probes; build rebuilds it identically.
func fzCheck(t *testing.T, seed uint64, build func() *fzScenario) (refusals int, wantErr string) {
	ref := build()
	if ref == nil {
		return 0, "skipped"
	}
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d (%s): %s", seed, ref.entry, fmt.Sprintf(format, args...))
	}
	wantErr = fzErrText(ref.op())
	want := ref.dump()
	calls := 0
	if ref.cb != nil {
		calls = ref.cb.calls
	}
	// refused checks one refusal: no change, then a retry without the limit
	refused := func(s *fzScenario, before []hashBudgetSnapshot, err error, what string, reset func()) {
		t.Helper()
		if err == nil || !errors.Is(err, yang.ErrBudget) {
			if fzErrText(err) != wantErr {
				fail("%s: %v, want %q or ErrBudget", what, err, wantErr)
			}
			if got := s.dump(); !reflect.DeepEqual(got, want) {
				fail("%s: unrefused result differs:\n%v\nwant\n%v", what, got, want)
			}
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
		reset()
		if err := s.op(); fzErrText(err) != wantErr {
			fail("%s: retry: %v, want %q", what, err, wantErr)
		}
		if got := s.dump(); !reflect.DeepEqual(got, want) {
			fail("%s: refusal+retry differs:\n%v\nwant\n%v", what, got, want)
		}
	}
	for k := range ref.knobs {
		s := build()
		var points []uint64
		s.knobs[k].set(0, &points)
		if fzErrText(s.op()) != wantErr {
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
			keep := rand.New(rand.NewPCG(seed, uint64(k))) //nolint:gosec // deterministic sample
			total := len(limits)
			for limit := range limits {
				if keep.IntN(total) >= fzSample {
					delete(limits, limit)
				}
			}
		}
		for limit := range limits {
			s := build()
			before := s.snapshot()
			s.knobs[k].set(limit, nil)
			err := s.op()
			refused(s, before, err, fmt.Sprintf("%s limit %d/%d", s.knobs[k].name, limit, need),
				func() { s.knobs[k].set(0, nil) })
		}
	}
	for at := range calls {
		s := build()
		before := s.snapshot()
		s.cb.failAt = at
		err := s.op()
		if !errors.Is(err, yang.ErrBudget) {
			fail("callback %d/%d: %v, want ErrBudget", at, calls, err)
		}
		refused(s, before, err, fmt.Sprintf("callback %d/%d", at, calls), func() { s.cb.calls, s.cb.failAt = 0, -1 })
	}
	return refusals, wantErr
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
		seed := base + uint64(i)                        //nolint:gosec // nonnegative iteration
		entry := fzEntries[seed%uint64(len(fzEntries))] // YANG_STAGE3_FUZZ_SEED=seed ITERS=1 repeats it
		build := func() *fzScenario {
			g := &fzGen{t: t, rng: rand.New(rand.NewPCG(0x301, seed)), set: set, set2: set2} //nolint:gosec // deterministic scenario
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
