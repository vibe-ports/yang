// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
	"weak"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

const equivModule = `module eq { yang-version 1.1; namespace "urn:eq"; prefix eq;
  container c { leaf a { type string; }
    choice ch { case k1 { leaf x { type string; } } case k2 { container z { leaf y { type string; } } } } }
  rpc r { input { leaf in { type string; } } output { leaf-list v { type uint32; } leaf w { type string; } } } }`

// eqIn is equivalentIn uncharged, for tests.
func eqIn(sn *schema.Node, set *schema.Set) *schema.Node {
	k, _ := equivalentIn(sn, set, nil)
	return k
}

// child is the child of n named name of kind kind (its data children, actions and notifs).
func child(t *testing.T, n *schema.Node, kind schema.Kind, name string) *schema.Node {
	t.Helper()
	for _, l := range [][]*schema.Node{n.Children, n.Actions, n.Notifs} {
		for _, c := range l {
			if c.Kind == kind && c.Name == name {
				return c
			}
		}
	}
	t.Fatalf("%s has no %v %s", n.Name, kind, name)
	return nil
}

// TestEquivalentCacheKeepsNoContext: equivalentIn reads the target set's lineage index, which holds
// only that set's own nodes, so concurrent readers share it safely, and a dropped context becomes
// collectable after lookups, as a source and as a target.
func TestEquivalentCacheKeepsNoContext(t *testing.T) {
	first := func(set *schema.Set) *schema.Node { return set.Implemented("eq").Top[0].Children[0] }
	collected := func(w func() bool) bool {
		for range 50 {
			runtime.GC()
			if w() {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}
	setA := reviewDiffSet(t, equivModule)
	var wb weak.Pointer[schema.Set]
	func() {
		setB := reviewDiffSet(t, equivModule)
		wb = weak.Make(setB)
		snB := first(setB)
		var wg sync.WaitGroup
		for range 16 {
			wg.Go(func() {
				for range 500 {
					if eqIn(snB, setA) != first(setA) {
						t.Error("a concurrent reader saw another equivalent")
						return
					}
				}
			})
		}
		wg.Wait()
		// a parent of another set has no children in setA and stores nothing there
		if k, err := setA.ChildByName(snB.Parent, schema.ChildKey{Kind: snB.Kind, ID: snB.ID()}, nil); k != nil || err != nil {
			t.Fatalf("a foreign parent's child: %v %v", k, err)
		}
	}()
	if !collected(func() bool { return wb.Value() == nil }) {
		t.Fatal("the dropped source context is still reachable")
	}
	var wc weak.Pointer[schema.Set]
	func() {
		setC := reviewDiffSet(t, equivModule)
		wc = weak.Make(setC)
		if eqIn(first(setA), setC) != first(setC) {
			t.Fatal("no equivalent")
		}
	}()
	if !collected(func() bool { return wc.Value() == nil }) {
		t.Fatal("the dropped target context is still reachable")
	}
	runtime.KeepAlive(setA)
}

// TestEquivalentInOutputAndScaling: equivalentIn resolves a node by its full lineage, an rpc
// output descendant and choice/case levels included (a case's leaf is not the equally named
// leaf of a context without the choice), and agrees with compareSchemaEqual over all parents. Each
// lookup reads the lineage index, so mapping many distinct schemas of a wide context costs
// O(depth) apiece after one linear build of the index.
func TestEquivalentInOutputAndScaling(t *testing.T) {
	setA, setB := reviewDiffSet(t, equivModule), reviewDiffSet(t, equivModule)
	var walk func(n *schema.Node)
	nodes := 0
	walk = func(n *schema.Node) {
		nodes++
		k := eqIn(n, setB)
		if k == nil || k.Module.Owner != setB || !compareSchemaEqual(n, k, true) {
			t.Fatalf("%s: equivalent %v", n.LogPath(), k)
		}
		for _, l := range [][]*schema.Node{n.Children, n.Actions, n.Notifs} {
			for _, c := range l {
				walk(c)
			}
		}
	}
	for _, n := range setA.Implemented("eq").Top {
		walk(n)
	}
	if nodes < 14 {
		t.Fatalf("walked %d nodes", nodes)
	}
	// the same leaf directly under c in another context is not x's equivalent: the choice and
	// case levels are part of the lineage
	flat := reviewDiffSet(t, `module eq { yang-version 1.1; namespace "urn:eq"; prefix eq;
  container c { leaf x { type string; } } }`)
	ch := child(t, setA.Implemented("eq").Top[0], schema.Choice, "ch")
	x := child(t, child(t, ch, schema.Case, "k1"), schema.Leaf, "x")
	flatX := child(t, flat.Implemented("eq").Top[0], schema.Leaf, "x")
	if k := eqIn(x, flat); k != nil || !compareSchemaEqual(x, flatX, false) || compareSchemaEqual(x, flatX, true) {
		t.Fatalf("a case leaf mapped to %v", k)
	}
	// many distinct schemas of a wide context: each lookup reads the index
	var mod strings.Builder
	mod.WriteString(`module w { yang-version 1.1; namespace "urn:w"; prefix w; container c {`)
	const n = 4000
	for i := range n {
		fmt.Fprintf(&mod, " leaf l%d { type string; }", i)
	}
	mod.WriteString(" } }")
	wa, wb := reviewDiffSet(t, mod.String()), reviewDiffSet(t, mod.String())
	start := time.Now()
	for _, c := range wa.Implemented("w").Top[0].Children {
		if eqIn(c, wb) == nil {
			t.Fatalf("no equivalent of %s", c.Name)
		}
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("%d cold lookups in a wide context took %v", n, d)
	}
}

// deepModule is a lineage of depth containers with 16 KiB names over one leaf x.
func deepModule(depth int) string {
	name := strings.Repeat("n", 16<<10)
	var mod strings.Builder
	mod.WriteString(`module d { yang-version 1.1; namespace "urn:d"; prefix d;`)
	for i := range depth {
		fmt.Fprintf(&mod, " container %s%d {", name, i)
	}
	mod.WriteString(" leaf x { type string; }")
	mod.WriteString(strings.Repeat(" }", depth))
	mod.WriteString(" }")
	return mod.String()
}

func deepLeaf(set *schema.Set) *schema.Node {
	leaf := set.Implemented("d").Top[0]
	for leaf.Kind != schema.Leaf {
		leaf = leaf.Children[0]
	}
	return leaf
}

// TestEquivalentInColdBudgetAndDepth: mapping a node into another context pays every level and
// every cold child index before building it, so a refusal builds nothing and is returned; and a
// deep lineage of long names maps with memory linear in the schema (no concatenated ancestry).
func TestEquivalentInColdBudgetAndDepth(t *testing.T) {
	const depth = 256
	mod := deepModule(depth)
	setA, setB := reviewDiffSet(t, mod), reviewDiffSet(t, mod)
	leaf := deepLeaf(setA)
	refused := errors.New("refused")
	if k, err := equivalentIn(leaf, setB, func(int64) error { return refused }); k != nil || !errors.Is(err, refused) {
		t.Fatalf("a refused cold mapping: %v %v", k, err)
	}
	var charged int64
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	k, err := equivalentIn(leaf, setB, func(n int64) error { charged += n; return nil })
	runtime.ReadMemStats(&after)
	if err != nil || k == nil || k.Name != "x" || k.Module.Owner != setB {
		t.Fatalf("the deep mapping: %v %v", k, err)
	}
	if charged < depth {
		t.Fatalf("charged %d for %d levels", charged, depth)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 8<<20 {
		t.Fatalf("a deep mapping allocated %d bytes", grown)
	}
	// a warm mapping is one ancestor step and one fixed-size map read per level (interned
	// identifiers, 16 KiB names or not), and pays exactly that
	if !leaf.Interned() || !k.Interned() {
		t.Fatal("snapshot nodes without interned identifiers")
	}
	charged = 0
	for range 512 {
		if _, err := equivalentIn(leaf, setB, func(n int64) error { charged += n; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if charged != 512*2*(depth+1) { // collecting each ancestor, then one index read per level
		t.Fatalf("warm mappings charged %d", charged)
	}
	// a cold build pays per module and child as it goes: a refusal partway stores nothing
	setC := reviewDiffSet(t, mod)
	left := 3
	if _, err := setC.ChildByName(nil, schema.ChildKey{Kind: leaf.Kind, ID: leaf.ID()}, func(int64) error {
		if left--; left < 0 {
			return refused
		}
		return nil
	}); !errors.Is(err, refused) {
		t.Fatalf("a refused cold build: %v", err)
	}
	if k, err := equivalentIn(leaf, setC, nil); err != nil || k == nil {
		t.Fatalf("after a refused build: %v %v", k, err)
	}
}

// TestEquivalentInCancel: a charge that polls a cancellation stops a cold mapping partway, at the
// top-level build's last unit (after every earlier child was inserted), and publishes no partial
// index: the next lookup builds (and pays) it again and finds the node, and is warm after that.
func TestEquivalentInCancel(t *testing.T) {
	setA, setB, setC := reviewDiffSet(t, equivModule), reviewDiffSet(t, equivModule), reviewDiffSet(t, equivModule)
	sn := setA.Implemented("eq").Top[0]
	var total int64
	if k, err := equivalentIn(sn, setC, func(n int64) error { total += n; return nil }); err != nil || k == nil {
		t.Fatalf("a cold mapping: %v %v", k, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var polls int64
	poll := func(int64) error {
		if polls++; polls == total {
			cancel()
		}
		return ctx.Err()
	}
	if k, err := equivalentIn(sn, setB, poll); k != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled mapping: %v %v", k, err)
	}
	key := schema.ChildKey{Kind: sn.Kind, ID: sn.ID()}
	var cold, warm int64
	if k, err := setB.ChildByName(nil, key, func(n int64) error { cold += n; return nil }); err != nil || k == nil {
		t.Fatalf("after a cancelled build: %v %v", k, err)
	}
	if k, err := setB.ChildByName(nil, key, func(n int64) error { warm += n; return nil }); err != nil || k == nil {
		t.Fatalf("a warm lookup: %v %v", k, err)
	}
	if cold != total-2 || warm != 0 { // the build is the cold mapping less its ancestor and level units
		t.Fatalf("after a cancellation the build charged %d (a full one %d), then %d", cold, total-2, warm)
	}
	if k, err := equivalentIn(sn, setB, ctxCharge(context.Background())); err != nil || k == nil || !compareSchemaEqual(sn, k, true) {
		t.Fatalf("after a cancellation: %v %v", k, err)
	}
}

// ctxCharge is a charge that only polls ctx.
func ctxCharge(ctx context.Context) func(int64) error { return func(int64) error { return ctx.Err() } }

// TestEquivalentInConcurrentColdBuild: concurrent first lookups into a cold set (run under -race)
// all build or read the same published indexes and agree on every equivalent.
func TestEquivalentInConcurrentColdBuild(t *testing.T) {
	setA := reviewDiffSet(t, equivModule)
	var all []*schema.Node
	var walk func(n *schema.Node)
	walk = func(n *schema.Node) {
		all = append(all, n)
		for _, l := range [][]*schema.Node{n.Children, n.Actions, n.Notifs} {
			for _, c := range l {
				walk(c)
			}
		}
	}
	for _, n := range setA.Implemented("eq").Top {
		walk(n)
	}
	for range 8 {
		setB := reviewDiffSet(t, equivModule)
		got := make([][]*schema.Node, 16)
		var wg sync.WaitGroup
		for g := range got {
			wg.Go(func() {
				for i := range all {
					n := all[(i+g)%len(all)]
					got[g] = append(got[g], eqIn(n, setB))
				}
			})
		}
		wg.Wait()
		for g := range got {
			for i, k := range got[g] {
				n := all[(i+g)%len(all)]
				if k == nil || k != eqIn(n, setB) || !compareSchemaEqual(n, k, true) {
					t.Fatalf("goroutine %d, %s: %v", g, n.LogPath(), k)
				}
			}
		}
	}
}

// extSet compiles module (named by its second word) with the libyang test copy of ietf-restconf.
func extSet(t *testing.T, module string) *schema.Set {
	t.Helper()
	rc, err := os.ReadFile("../conformance/corpus/ut-ext/common/ietf-restconf@2017-01-26.yang")
	if err != nil {
		t.Fatal(err)
	}
	name := strings.Fields(module)[1]
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fstest.MapFS{
		name + ".yang":                  {Data: []byte(module)},
		"ietf-restconf@2017-01-26.yang": {Data: rc},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load(name, "", nil); err != nil {
		t.Fatal(err, diags)
	}
	return c.Snapshot()
}

// extNodes is every node of m's extension-instance trees.
func extNodes(m *schema.Module) []*schema.Node {
	var all []*schema.Node
	var walk func(n *schema.Node)
	walk = func(n *schema.Node) {
		all = append(all, n)
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, e := range m.Exts {
		if e.Root != nil {
			walk(e.Root)
		}
		for _, n := range e.Nodes {
			walk(n)
		}
	}
	return all
}

// TestEquivalentInExtensionRoots: a structure's root and a yang-data instance's top nodes are
// the roots of their lineages, so equivalentIn maps them and their descendants as
// compareSchemaEqual with all parents matches them. Two yang-data instances with equal roots
// leave no single equivalent: the mapping returns schema.ErrAmbiguousRoot, never a guess, while a
// node of the target set itself still maps to itself.
func TestEquivalentInExtensionRoots(t *testing.T) {
	const unique = `module e { yang-version 1.1; namespace "urn:e"; prefix e;
  import ietf-yang-structure-ext { prefix sx; } import ietf-restconf { prefix rc; }
  sx:structure box { leaf x { type string; } }
  rc:yang-data one { container d { leaf p { type string; } } } }`
	setA, setB := extSet(t, unique), extSet(t, unique)
	names := map[string]bool{}
	for _, n := range extNodes(setA.Implemented("e")) {
		names[n.Name] = true
		k, err := equivalentIn(n, setB, nil)
		if err != nil || k == nil || k.Module.Owner != setB || !compareSchemaEqual(n, k, true) {
			t.Fatalf("%s: equivalent %v %v", n.Name, k, err)
		}
	}
	for _, want := range []string{"box", "x", "d", "p"} {
		if !names[want] {
			t.Fatalf("no %s walked: %v", want, names)
		}
	}
	const dup = `module e { yang-version 1.1; namespace "urn:e"; prefix e;
  import ietf-restconf { prefix rc; }
  rc:yang-data one { container d { leaf p { type string; } } }
  rc:yang-data two { container d { leaf q { type string; } } } }`
	dupA, dupB := extSet(t, dup), extSet(t, dup)
	for _, n := range extNodes(dupA.Implemented("e")) {
		if k, err := equivalentIn(n, dupB, nil); k != nil || !errors.Is(err, schema.ErrAmbiguousRoot) {
			t.Fatalf("%s under a duplicate root: %v %v", n.Name, k, err)
		}
		if k, err := equivalentIn(n, dupA, nil); k != n || err != nil {
			t.Fatalf("%s in its own set: %v %v", n.Name, k, err)
		}
	}
}

// TestCompareSchemaEqualNilModule: a level without a module never compares equal, so two
// lineages whose parents lack modules are unequal (as compareSchemaParentsEqual finds them) and
// equivalentIn maps neither.
func TestCompareSchemaEqualNilModule(t *testing.T) {
	mk := func() *schema.Node {
		p := &schema.Node{Kind: schema.Container, Name: "c"}
		return &schema.Node{Kind: schema.Leaf, Name: "x", Module: &schema.Module{Name: "m"}, Parent: p}
	}
	a, b := mk(), mk()
	if compareSchemaEqual(a, b, true) || compareSchemaParentsEqual(a, b) {
		t.Fatal("nil-module parents compared equal")
	}
	if !compareSchemaEqual(a, b, false) {
		t.Fatal("equal nodes without parents compared unequal")
	}
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m; container c { leaf x { type string; } } }`)
	if k, err := equivalentIn(a, set, nil); k != nil || err != nil {
		t.Fatalf("a nil-module lineage mapped: %v %v", k, err)
	}
	// a hand-built set whose top node has no module either: equal kind and name, still no match
	bare := &schema.Set{Modules: []*schema.Module{{Name: "m", Top: []*schema.Node{{Kind: schema.Container, Name: "c"}}}}}
	if k, err := equivalentIn(a.Parent, bare, nil); k != nil || err != nil {
		t.Fatalf("a nil-module node mapped: %v %v", k, err)
	}
}

// TestEquivalentInDeepRootMiss: collecting a deep lineage is paid ancestor by ancestor, so a
// lineage whose root is missing pays one unit per level and one index read, and a charge that
// refuses at once stops before the lineage is collected (no allocation).
func TestEquivalentInDeepRootMiss(t *testing.T) {
	const depth = 256
	leaf := deepLeaf(reviewDiffSet(t, deepModule(depth)))
	target := &schema.Set{}
	if _, err := target.ChildByName(nil, schema.ChildKey{}, nil); err != nil { // a warm empty index
		t.Fatal(err)
	}
	var paid int64
	if k, err := equivalentIn(leaf, target, func(n int64) error { paid += n; return nil }); k != nil || err != nil {
		t.Fatalf("a root miss: %v %v", k, err)
	}
	if paid != depth+2 {
		t.Fatalf("a root miss of %d levels paid %d", depth+1, paid)
	}
	refused := errors.New("refused")
	refuse := func(int64) error { return refused }
	if allocs := testing.AllocsPerRun(100, func() {
		if _, err := equivalentIn(leaf, target, refuse); !errors.Is(err, refused) {
			t.Fatal(err)
		}
	}); allocs > 0 {
		t.Fatalf("a refused mapping allocated %v times", allocs)
	}
}
