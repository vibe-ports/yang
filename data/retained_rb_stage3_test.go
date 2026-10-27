// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/contextopts"
	"github.com/vibe-ports/yang/internal/schema"
)

func TestStage3ParserOneTopology(t *testing.T) {
	a, _ := retainedRBSets(t)
	tr := newTree(a)
	sn := retainedRBTop(t, a)
	lc := &lydCtx{tree: tr, ctx: context.Background()}
	ref := &refModel{}
	for step, v := range []int{11, 7, 15, 3, 9, 13, 17, 8, 6, 12, 4, 10, 2, 14, 16, 18} {
		n := retainedRBTerm(t, sn, fmt.Sprint(v))
		if err := lc.nodeInsert(nil, nil, n); err != nil {
			t.Fatal(err)
		}
		ref.insert(n)
		if ord := lc.dirtyOrd[dirtyRun{&tr.top, sn}]; ord != nil && tr.top.rbOrders[n.rbGroup] != ord {
			t.Fatal("parser created a second topology")
		}
		checkTreeRetainedRB(t, tr)
		if ref.tree {
			checkRBShape(t, &tr.top, tr.top.logical().At(0), &ref.ref)
		}
		if step%4 == 3 {
			if err := lc.flushDirtyAll(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := lc.flushDirtyAll(); err != nil {
		t.Fatal(err)
	}
	checkRBShape(t, &tr.top, tr.top.first(), &ref.ref)
}

func TestStage3JournalReservesBeforeAllocation(t *testing.T) {
	a, _ := retainedRBSets(t)
	tr := newTree(a)
	sn := retainedRBTop(t, a)
	// Wide duplicate-heavy input makes snapshot allocation visible independently of timing.
	for range 16384 {
		mustLink(t, tr.top.appendNode(retainedRBTerm(t, sn, "5")))
	}
	for _, n := range tr.top.SchemaAll() {
		n.tree = tr
	}
	tr.top.setWork(&tr.work)
	j := beginHashTransaction(tr)
	tr.work.hashLimit = tr.work.hashUsed + 1
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	err := tr.top.touch()
	runtime.ReadMemStats(&after)
	j.end(&err)
	if !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("capture: %v", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 128<<10 {
		t.Fatalf("refused capture allocated %d bytes", grew)
	}
	if len(j.siblings) != 0 || len(j.nodes) != 0 {
		t.Fatal("failed reservation published a snapshot")
	}
}

func TestStage3RollbackPreservesParserAlias(t *testing.T) {
	a, _ := retainedRBSets(t)
	tr := parseOnlyXML(t, a, retainedRBXML("top", 11, 7, 15, 3, 9, 13, 17))
	lc := &lydCtx{tree: tr, ctx: context.Background()}
	sn := retainedRBTop(t, a)
	if err := lc.nodeInsert(nil, nil, retainedRBTerm(t, sn, "8")); err != nil {
		t.Fatal(err)
	}
	k := dirtyRun{&tr.top, sn}
	before := snapshotHashBudgetTree(tr)
	original := lc.dirtyOrd[k]
	if original == nil {
		t.Fatal("probe did not defer publication")
	}
	j := beginHashTransaction(tr)
	if err := tr.top.touch(); err != nil {
		t.Fatal(err)
	}
	if err := lc.nodeInsert(nil, nil, retainedRBTerm(t, sn, "6")); err != nil {
		t.Fatal(err)
	}
	err := yang.ErrBudget
	j.end(&err)
	if tr.top.pending[sn] != original || tr.top.rbOrders[tr.top.rbGroups[original.first()]] != original {
		t.Fatal("rollback split pending and retained topology identities")
	}
	if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(before, after) {
		t.Fatal("rollback changed graph or indexes")
	}
	tr.work.hashUsed, tr.work.hashErr = 0, nil
	if err := lc.nodeInsert(nil, nil, retainedRBTerm(t, sn, "10")); err != nil {
		t.Fatal(err)
	}
	if err := lc.flushDirtyAll(); err != nil {
		t.Fatal(err)
	}
	checkTreeRetainedRB(t, tr)
}

func TestStage3NativeRemovalSelectionRefusal(t *testing.T) {
	a, b := retainedRBSets(t)
	tr := parseOnlyXML(t, a, retainedRBXML("top", 1, 3, 5, 7))
	foreign := retainedRBTerm(t, retainedRBTop(t, b), "4")
	if err := tr.insert(nil, foreign, insertDefault); err != nil {
		t.Fatal(err)
	}
	before := snapshotHashBudgetTree(tr)
	// A foreign schema interrupts pointer-identical runs. Native lyd_unlink selects this node
	// as its own leader, without metadata, rather than following its cached membership.
	err := foreign.Unlink()
	if err == nil || !strings.Contains(err.Error(), "D-0121") {
		t.Fatalf("unlink: %v", err)
	}
	if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(before, after) {
		t.Fatal("refused removal changed graph")
	}
	checkTreeRetainedRB(t, tr)
}

func TestStage3BulkRemovalHistory(t *testing.T) {
	a, _ := retainedRBSets(t)
	rng := rand.New(rand.NewPCG(0x301, 3)) //nolint:gosec // deterministic native-history probe
	for seed := range 32 {
		values := rng.Perm(40)
		seq := parseOnlyXML(t, a, retainedRBXML("top", values...))
		bulk := parseOnlyXML(t, a, retainedRBXML("top", values...))
		remove := rng.Perm(40)[:30]
		var nodes []*Node
		for _, i := range remove {
			nodes = append(nodes, bulk.top.At(i))
		}
		for _, n := range nodes {
			other := seq.top.At(slices.IndexFunc(seq.top.schemaSpan().Collect(), func(c *Node) bool { return c.Value() == n.Value() }))
			if err := other.Unlink(); err != nil {
				t.Fatal(err)
			}
		}
		if err := bulk.unlinkAll(nodes); err != nil {
			t.Fatal(err)
		}
		checkTreeRetainedRB(t, bulk)
		shape := func(tr *Tree) string {
			var visit func(*dirtyOrderNode) string
			visit = func(n *dirtyOrderNode) string {
				if n == nil {
					return "."
				}
				return fmt.Sprintf("(%s:%t%s%s)", n.node.Value(), n.red, visit(n.left), visit(n.right))
			}
			return visit(tr.top.rbOrders[tr.top.rbGroups[tr.top.first()]].root)
		}
		if shape(seq) != shape(bulk) {
			t.Fatalf("seed %d: bulk history differs from sequential removals", seed)
		}
	}
}

func TestStage3AnyTransferAndDiscard(t *testing.T) {
	a, _ := retainedRBSets(t)
	// The payload can retain its own context beneath an opaque parent; its authoritative
	// topology moves intact and the old forest must release every reference.
	tr, _, x := anyFixture()
	payload := parseOnlyXML(t, a, retainedRBXML("top", 11, 7, 15, 3, 9, 13, 17))
	owned := payload.top.rbOrders[payload.top.rbGroups[payload.top.first()]]
	n := &Node{schema: x, any: &anyValue{}}
	mustLink(t, tr.insert(nil, n, insertDefault))
	if err := n.takeAnyChildren(tr, payload.top.first()); err != nil {
		t.Fatal(err)
	}
	if payload.top.len() != 0 || len(payload.top.rbOrders) != 0 {
		t.Fatal("source still owns payload")
	}
	if n.any.children.rbOrders[n.any.children.rbGroups[n.any.children.first()]] != owned {
		t.Fatal("transfer reconstructed topology")
	}
	roots := n.any.children.schemaSpan().Collect()
	j := beginHashTransaction(tr)
	if err := n.clearAny(); err != nil {
		t.Fatal(err)
	}
	for _, c := range roots {
		if c.inRB || c.rbGroup != nil || c.parent != nil {
			t.Fatal("discarded payload root retains membership")
		}
	}
	err := yang.ErrBudget
	j.end(&err)
	if n.any.children.rbOrders[n.any.children.rbGroups[n.any.children.first()]] != owned {
		t.Fatal("payload rollback changed topology identity")
	}
	checkTreeRetainedRB(t, tr)
}

func TestStage3DupIntoParentTopology(t *testing.T) {
	f := newFixture()
	tr, c := f.cont(t)
	for _, v := range []string{"5", "1", "9"} {
		mustLink(t, tr.insert(c, f.term(t, f.ll, v), insertDefault))
	}
	src, nodes := f.srcList(t, false, "7", "3", "6")
	_ = src
	ref := &refRB{}
	// destination construction history, then native dup sorted insertions in source order
	for _, v := range []string{"5", "1", "9"} {
		for _, n := range c.kids.SchemaAll() {
			if n.Value() == v {
				ref.insert(n)
			}
		}
	}
	if _, _, err := tr.DupSiblings(nodes[1], c, DupOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"3", "6", "7"} {
		for _, n := range c.kids.SchemaAll() {
			if n.Value() == v {
				ref.insert(n)
			}
		}
	}
	checkTreeRetainedRB(t, tr)
	checkRBShape(t, &c.kids, c.kids.first(), ref)
}

// The same test is run against origin/main and the stage-3 worktree. It deliberately uses
// APIs available on both, counts algorithmic work, and reports allocations and enforced work.
func TestStage3CountedWork(t *testing.T) {
	for _, duplicates := range []bool{false, true} {
		for _, count := range []int{256, 512} {
			t.Run(fmt.Sprintf("duplicates=%t/n=%d", duplicates, count), func(t *testing.T) {
				a, _ := retainedRBSets(t)
				values := make([]int, count)
				for i := range values {
					if !duplicates {
						values[i] = count - i
					}
				}
				input := retainedRBXML("top", values...)
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				tr := parseOnlyXML(t, a, input)
				runtime.ReadMemStats(&after)
				t.Logf("parse work=%d hash=%d bytes=%d", tr.work.Load(), tr.work.hashUsed, after.TotalAlloc-before.TotalAlloc)
				tr.work.hashUsed = 0
				tr.work.Store(0)
				remove := tr.top.schemaSpan().Slice(0, count/2).Collect()
				if err := tr.unlinkAll(remove); err != nil {
					t.Fatal(err)
				}
				t.Logf("bulk work=%d hash=%d", tr.work.Load(), tr.work.hashUsed)
				tr.work.hashUsed = 0
				tr.work.Store(0)
				tr.work.visits = true
				if err := contextopts.DiscardTree(tr); err != nil {
					t.Fatal(err)
				}
				t.Logf("discard work=%d hash=%d", tr.work.Load(), tr.work.hashUsed)
				for _, op := range []string{"dup", "move", "single-move", "value"} {
					src := parseOnlyXML(t, a, input)
					dst := parseOnlyXML(t, a, retainedRBXML("top", 999))
					target := dst
					if op == "value" {
						target = src
					}
					target.work.Store(0)
					runtime.ReadMemStats(&before)
					var err error
					switch op {
					case "dup":
						_, _, err = dst.DupSiblings(src.top.first(), nil, DupOptions{})
					case "move":
						err = dst.Insert(nil, src.top.first())
					case "single-move":
						err = dst.Insert(nil, src.top.At(count/2))
					default:
						_, err = src.top.first().SetValue("1000")
					}
					if err != nil {
						t.Fatal(err)
					}
					runtime.ReadMemStats(&after)
					t.Logf("%s work=%d hash=%d bytes=%d", op, target.work.Load(), target.work.hashUsed, after.TotalAlloc-before.TotalAlloc)
				}
			})
		}
	}
}

func TestStage3FreeDropsOwnedTopology(t *testing.T) {
	a, _ := retainedRBSets(t)
	for _, discard := range []bool{false, true} {
		tr := parseOnlyXML(t, a, retainedRBXML("v", 7, 3, 11, 1, 5))
		root := tr.top.first()
		members := root.kids.schemaSpan().Collect()
		if len(root.kids.rbOrders) == 0 {
			t.Fatal("fixture did not build retained topology")
		}
		var err error
		if discard {
			err = contextopts.DiscardTree(tr)
		} else {
			err = root.Remove()
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(root.kids.rbOrders)+len(root.kids.rbGroups)+len(root.kids.pending) != 0 {
			t.Fatal("freed owner retains a topology")
		}
		for _, n := range members {
			if n.inRB || n.rbGroup != nil || n.detachedRB != nil || len(n.carriedRB) != 0 {
				t.Fatal("freed member retains topology membership")
			}
		}
		checkTreeRetainedRB(t, tr)
	}
}

func TestStage3KeyChangeCountedWork(t *testing.T) {
	for _, equal := range []bool{false, true} {
		for _, count := range []int{256, 512} {
			for _, lower := range []bool{false, true} {
				t.Run(fmt.Sprintf("equal=%t/n=%d/lower=%t", equal, count, lower), func(t *testing.T) {
					a := reviewDiffSet(t, `module stage3-count-keys {yang-version 1.1;namespace "urn:stage3-count-keys";prefix s;
 list l {key k;leaf k {type uint32;}}}`)
					tr := newTree(a)
					for i := range count {
						k := count - i
						if equal {
							k = 128
						}
						if _, err := tr.NewList(nil, "stage3-count-keys:l", []string{fmt.Sprint(k)}, NewOptions{}); err != nil {
							t.Fatal(err)
						}
					}
					n, value := tr.top.first(), "1000"
					if lower {
						n, value = tr.top.At(count-1), "0"
					}
					tr.work.Store(0)
					var before, after runtime.MemStats
					runtime.ReadMemStats(&before)
					if _, err := n.kids.At(0).SetValue(value); err != nil {
						t.Fatal(err)
					}
					runtime.ReadMemStats(&after)
					t.Logf("key work=%d hash=%d bytes=%d", tr.work.Load(), tr.work.hashUsed, after.TotalAlloc-before.TotalAlloc)
				})
			}
		}
	}
}

func TestStage3DuplicateRetainsEmptyMetadata(t *testing.T) {
	a, _ := retainedRBSets(t)
	src := parseOnlyXML(t, a, retainedRBXML("top", 3, 5, 7))
	dst := newTree(a)
	n, _, err := dst.Dup(src.top.first(), nil, DupOptions{NoSort: true})
	if err != nil {
		t.Fatal(err)
	}
	groups := dst.top.metaGroups(n)
	if len(groups) != 1 || dst.top.rbOrders[groups[0]].root != nil || n.inRB {
		t.Fatal("duplicate did not retain the native empty metadata slot")
	}
	if err := n.Unlink(); err != nil {
		t.Fatal(err)
	}
	if len(n.carriedRB) != 1 || n.carriedRB[0].order.root != nil {
		t.Fatal("unlink discarded empty metadata")
	}
	if err := dst.Insert(nil, n); err != nil {
		t.Fatal(err)
	}
	checkTreeRetainedRB(t, dst)
}

func TestStage3EmptyMetadataUnlinkBudget(t *testing.T) {
	a, _ := retainedRBSets(t)
	checkEveryHashBudgetPoint(t, func() (*Tree, func() error) {
		src := parseOnlyXML(t, a, retainedRBXML("top", 3, 5, 7))
		dst := newTree(a)
		n, _, err := dst.Dup(src.top.first(), nil, DupOptions{NoSort: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(dst.top.metaGroups(n)) != 1 {
			t.Fatal("fixture did not retain the NULL-root metadata")
		}
		return dst, func() error {
			err := n.Unlink()
			if err == nil && dst.work.hashErr != nil {
				t.Fatal("unlink hid the original owner's budget error after detachment")
			}
			return err
		}
	})
}

func TestStage3NonmemberMetadataUnlink(t *testing.T) {
	f := newFixture()
	tr, parent := f.cont(t)
	for _, value := range []string{"3", "5"} {
		mustLink(t, tr.insert(parent, f.term(t, f.ll, value), insertDefault))
	}
	g := parent.kids.rbGroups[parent.kids.first()]
	for _, value := range []string{"9", "1"} {
		mustLink(t, tr.insert(parent, f.term(t, f.ll, value), insertLastBySchema))
	}
	for range 2 {
		if err := parent.kids.first().Remove(); err != nil {
			t.Fatal(err)
		}
	}
	n := parent.kids.first()
	if parent.kids.rbGroups[n] != g || parent.kids.rbOrders[g].root != nil {
		t.Fatal("fixture did not retain NULL-root metadata")
	}
	if err := n.Unlink(); err != nil {
		t.Fatal(err)
	}
	if parent.kids.rbGroups[parent.kids.first()] != g || len(n.carriedRB) != 0 {
		t.Fatal("nonmember unlink failed to move the selected NULL metadata")
	}
	checkTreeRetainedRB(t, tr)
}

func TestStage3KeylessReinsertRefusalPreservesHistory(t *testing.T) {
	a := reviewDiffSet(t, `module stage3-reinsert {yang-version 1.1; namespace "urn:stage3-reinsert";prefix s;
 container c {list l {key k; leaf k {type uint32;}}}}`)
	tr := newTree(a)
	parent, err := tr.NewInner(nil, "stage3-reinsert:c", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"1", "2", "3", "4", "5"} {
		if _, err := tr.NewList(parent, "stage3-reinsert:l", []string{k}, NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	key := parent.kids.At(1).kids.At(0)
	_ = unlink(parent.kids.first().kids.At(0)) // native parser/internal history can leave a keyless member
	before := snapshotHashBudgetTree(tr)
	if _, err := key.SetValue("0"); err == nil || !strings.Contains(err.Error(), "D-0121") {
		t.Fatalf("unsafe reinsertion error=%v", err)
	}
	if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(before, after) {
		t.Fatal("refused reinsertion changed physical links, registration history or topology")
	}
	checkTreeRetainedRB(t, tr)
}

func TestStage3MergeIntoEmptyMetadataSelection(t *testing.T) {
	a, _ := retainedRBSets(t)
	prototype := parseOnlyXML(t, a, retainedRBXML("top", 3, 11))
	dst := newTree(a)
	first, _, err := dst.Dup(prototype.top.first(), nil, DupOptions{NoSort: true})
	if err != nil {
		t.Fatal(err)
	}
	src := parseOnlyXML(t, a, retainedRBXML("top", 5, 7, 9))
	group := src.top.rbGroups[src.top.first()]
	if err := dst.Insert(nil, src.top.first()); err != nil {
		t.Fatal(err)
	}
	if dst.top.rbTree[first] || !dst.top.ownsGroup(first, group) || len(dst.top.metaGroups(first)) != 2 {
		t.Fatal("nodes2 did not append source metadata behind the destination NULL selection")
	}
	checkTreeRetainedRB(t, dst)
	for _, value := range []string{"4", "6"} {
		if _, err := dst.NewTerm(nil, "retained-rb-round5:top", value, NewOptions{}); err != nil {
			t.Fatal(err)
		}
		checkTreeRetainedRB(t, dst)
	}
}

func TestStage3ManyMetadataMoveFailure(t *testing.T) {
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("move-error recovery panicked: %v", p)
		}
	}()
	a := reviewDiffSet(t, `module stage3-many-meta {yang-version 1.1;namespace "urn:stage3-many-meta";prefix s;
 list l {key k;leaf k {type uint32;}}}`)
	add := func(tr *Tree, values ...string) {
		t.Helper()
		for _, value := range values {
			if _, err := tr.NewList(nil, "stage3-many-meta:l", []string{value}, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	prototype := newTree(a)
	add(prototype, "3", "11")
	for range 32 {
		dst, src := newTree(a), newTree(a)
		if _, _, err := dst.Dup(prototype.top.first(), nil, DupOptions{NoSort: true}); err != nil {
			t.Fatal(err)
		}
		add(src, "5", "7", "9")
		if err := dst.Insert(nil, src.top.first()); err != nil {
			t.Fatal(err)
		}
		prototype = dst
	}
	source, dst := newTree(a), newTree(a)
	n, _, err := source.Dup(prototype.top.first(), nil, DupOptions{NoSort: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(source.top.metaGroups(n)) < 32 {
		t.Fatal("history did not accumulate metadata")
	}
	add(dst, "0", "10")
	_ = unlink(dst.top.first().kids.first())
	_ = unlink(dst.top.At(1).kids.first())
	beforeSource, beforeDest := snapshotHashBudgetTree(source), snapshotHashBudgetTree(dst)
	if err := dst.Insert(nil, n); err == nil || !strings.Contains(err.Error(), "D-0121") {
		t.Fatalf("unsafe insertion error=%v", err)
	}
	if !reflect.DeepEqual(beforeSource, snapshotHashBudgetTree(source)) || !reflect.DeepEqual(beforeDest, snapshotHashBudgetTree(dst)) {
		t.Fatal("refused move did not preserve both forests and metadata queues")
	}
}

func TestStage3ManyMetadataMoveBudget(t *testing.T) {
	a := reviewDiffSet(t, `module stage3-meta-budget {yang-version 1.1;namespace "urn:stage3-meta-budget";prefix s;
 container c {leaf x {type string;}leaf y {type string;}leaf z {type string;}
 list l {key k;leaf k {type uint32;}}}}`)
	parent := func(tr *Tree) *Node {
		t.Helper()
		c, err := tr.NewInner(nil, "stage3-meta-budget:c", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	forest := func(values ...string) *Tree {
		t.Helper()
		tr := newTree(a)
		c := parent(tr)
		for _, k := range values {
			if _, err := tr.NewList(c, "l", []string{k}, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		out := newTree(a)
		if _, _, err := out.DupSiblings(c.kids.first(), nil, DupOptions{Recursive: true}); err != nil {
			t.Fatal(err)
		}
		extra, err := tr.NewList(c, "l", []string{"17"}, NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := extra.Unlink(); err != nil {
			t.Fatal(err)
		}
		if err := out.Insert(nil, extra); err != nil {
			t.Fatal(err)
		}
		return out
	}
	prototype := forest("3", "11")
	for range 32 {
		dst := newTree(a)
		if _, _, err := dst.Dup(prototype.top.first(), nil, DupOptions{Recursive: true, NoSort: true}); err != nil {
			t.Fatal(err)
		}
		src := forest("5", "7", "9")
		if err := dst.Insert(nil, src.top.first()); err != nil {
			t.Fatal(err)
		}
		prototype = dst
	}
	checkEveryHashBudgetPointPair(t, func() ([]*Tree, func() error) {
		src, dst := newTree(a), newTree(a)
		c, d := parent(src), parent(dst)
		for _, name := range []string{"x", "y", "z"} {
			if _, err := src.NewTerm(c, name, name, NewOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := dst.NewTerm(d, name, name, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		n, _, err := src.Dup(prototype.top.first(), c, DupOptions{Recursive: true, NoSort: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(c.kids.metaGroups(n)) < 32 {
			t.Fatal("history did not accumulate metadata")
		}
		for _, k := range []string{"0", "10"} {
			if _, err := dst.NewList(d, "l", []string{k}, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		return []*Tree{src, dst}, func() error { return dst.Insert(d, n) }
	}, func(t *testing.T, trees []*Tree) {
		if trees[0].top.first().kids.SchemaLen() != 3 || trees[1].top.first().kids.SchemaLen() != 6 {
			t.Fatal("move did not complete")
		}
		checkTreeRetainedRB(t, trees[0])
		checkTreeRetainedRB(t, trees[1])
	})
}

func TestStage3MixedMultikeyHistory(t *testing.T) {
	const module = `module stage3-keys { yang-version 1.1; namespace "urn:stage3-keys"; prefix s;
 list l { key "a b"; leaf a { type uint16; } leaf b { type string; } leaf p { type string; } action act {input {leaf x {type string;}}} } }`
	a, b := reviewDiffSet(t, module), reviewDiffSet(t, module)
	tr := newTree(a)
	rng := rand.New(rand.NewPCG(0x301, 33)) //nolint:gosec // persisted mutation seed
	for step := range 2048 {
		tr.work.hashUsed, tr.work.hashErr = 0, nil
		switch rng.IntN(7) {
		case 0, 1:
			owner := newTree(a)
			if rng.IntN(2) != 0 {
				owner = newTree(b)
			}
			n, err := owner.NewList(nil, "stage3-keys:l", []string{fmt.Sprint(rng.IntN(32)), fmt.Sprint(rng.IntN(32))}, NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := n.Unlink(); err != nil {
				t.Fatal(err)
			}
			if err := tr.insert(nil, n, insertDefault); err != nil {
				t.Fatalf("step %d insert: %v", step, err)
			}
		case 2:
			if tr.top.SchemaLen() > 0 {
				n := tr.top.At(rng.IntN(tr.top.SchemaLen()))
				if err := n.Unlink(); err != nil && !strings.Contains(err.Error(), "D-0121") {
					t.Fatalf("step %d unlink: %v", step, err)
				}
			}
		case 3:
			if tr.top.SchemaLen() > 0 {
				n := tr.top.At(rng.IntN(tr.top.SchemaLen()))
				if _, err := n.kids.At(1).SetValue(fmt.Sprint(rng.IntN(32))); err != nil && !strings.Contains(err.Error(), "D-0121") {
					t.Fatalf("step %d key change: %v", step, err)
				}
			}
		case 4:
			if tr.top.SchemaLen() > 0 {
				probe := parseOnlyXML(t, tr.context(), `<l xmlns="urn:stage3-keys"><a>15</a><b>p</b></l>`)
				if err := tr.Merge(probe); err != nil {
					t.Fatalf("step %d merge: %v", step, err)
				}
			}
		case 6:
			if tr.top.SchemaLen() > 0 {
				n := tr.top.first()
				op, err := tr.NewInner(n, "act", NewOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tr.NewTerm(op, "x", "q", NewOptions{}); err != nil {
					t.Fatal(err)
				}
				if _, err := tr.ValidateOp(context.Background(), OpRPC, ValidateOpOptions{}); err != nil && !strings.Contains(err.Error(), "D-0121") {
					t.Fatalf("step %d temporary validation: %v", step, err)
				}
				checkTreeRetainedRB(t, tr)
				if err := op.Remove(); err != nil {
					t.Fatal(err)
				}
			}

		case 5:
			if tr.top.SchemaLen() > 80 {
				j := beginHashTransaction(tr)
				err := tr.unlinkAll(tr.top.schemaSpan().Slice(0, 20).Collect())
				if err != nil && !strings.Contains(err.Error(), "D-0121") {
					t.Fatal(err)
				}
				// Roll back a successfully reserved mutation too, then continue through that exact graph.
				err = yang.ErrBudget
				j.end(&err)
			}
		}
		checkTreeRetainedRB(t, tr)
	}
}

func TestStage3NativeSearchRefusesUnreachable(t *testing.T) {
	a, _ := retainedRBSets(t)
	tr := parseOnlyXML(t, a, retainedRBXML("top", 5, 3, 7))
	n := tr.top.first()
	// Parser/store callbacks can change compared data in place. The physical order still
	// projects the original topology; numeric BST ordering is deliberately not an invariant.
	n.value = retainedRBTerm(t, n.schema, "9").value
	before := snapshotHashBudgetTree(tr)
	err := n.Unlink()
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || !strings.Contains(err.Error(), "D-0121") {
		t.Fatalf("unreachable native search: %v", err)
	}
	if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
		t.Fatal("refused search changed graph")
	}
	checkTreeRetainedRB(t, tr)
}

func TestStage3RollbackCancelsNewParserPublication(t *testing.T) {
	a, _ := retainedRBSets(t)
	tr := parseOnlyXML(t, a, retainedRBXML("top", 11, 7, 15, 3, 9, 13, 17))
	lc := &lydCtx{tree: tr, ctx: context.Background()}
	sn := retainedRBTop(t, a)
	before := snapshotHashBudgetTree(tr)
	j := beginHashTransaction(tr)
	if err := lc.nodeInsert(nil, nil, retainedRBTerm(t, sn, "6")); err != nil {
		t.Fatal(err)
	}
	err := yang.ErrBudget
	j.end(&err)
	if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
		t.Fatal("rollback changed clean run")
	}
	tr.work.hashErr, tr.work.hashUsed = nil, 0
	if err := lc.nodeInsert(nil, nil, retainedRBTerm(t, sn, "10")); err != nil {
		t.Fatal(err)
	}
	if err := lc.flushDirtyAll(); err != nil {
		t.Fatal(err)
	}
	checkTreeRetainedRB(t, tr)
	if got := retainedRBValues(&tr.top); !slices.Equal(got, []string{"3", "7", "9", "10", "11", "13", "15", "17"}) {
		t.Fatalf("resume after rollback: %v", got)
	}
}

func TestStage3AnyTransferRollback(t *testing.T) {
	a, _ := retainedRBSets(t)
	src := parseOnlyXML(t, a, retainedRBXML("top", 5, 3, 7))
	dst, _, sn := anyFixture()
	n := &Node{schema: sn, any: &anyValue{}}
	mustLink(t, dst.insert(nil, n, insertDefault))
	beforeSrc, beforeDst := snapshotHashBudgetTree(src), snapshotHashBudgetTree(dst)
	j := beginHashTransaction(dst)
	if err := n.takeAnyChildren(dst, src.top.first()); err != nil {
		t.Fatal(err)
	}
	err := yang.ErrBudget
	j.end(&err)
	if !reflect.DeepEqual(beforeSrc, snapshotHashBudgetTree(src)) || !reflect.DeepEqual(beforeDst, snapshotHashBudgetTree(dst)) {
		t.Fatal("payload transfer rollback changed a forest")
	}
	checkTreeRetainedRB(t, src)
	checkTreeRetainedRB(t, dst)
}

func TestStage3DiscardReservationRefusal(t *testing.T) {
	a, _ := retainedRBSets(t)
	tr := parseOnlyXML(t, a, retainedRBXML("top", 11, 7, 15, 3, 9, 13, 17))
	before := snapshotHashBudgetTree(tr)
	tr.work.hashLimit = 1
	if err := contextopts.DiscardTree(tr); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("discard: %v", err)
	}
	if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
		t.Fatal("refused discard changed forest")
	}
	checkTreeRetainedRB(t, tr)
}

func TestStage3WholeRunBudgetRestoresBothForests(t *testing.T) {
	f := newFixture()
	build := func() ([]*Tree, func() error) {
		dst, c := f.cont(t)
		for _, v := range []string{"2", "6", "9"} {
			mustLink(t, dst.insert(c, f.term(t, f.ll, v), insertDefault))
		}
		src, _ := f.srcList(t, false, "7", "3", "5", "1")
		mustLink(t, src.insert(nil, f.term(t, f.z, "z"), insertDefault))
		return []*Tree{dst, src}, func() error { return dst.Insert(c, src.top.first()) }
	}
	checkEveryHashBudgetPointPair(t, build, func(t *testing.T, trees []*Tree) {
		t.Helper()
		if trees[1].top.len() != 0 {
			t.Fatal("source still owns moved members")
		}
		checkTreeRetainedRB(t, trees[0])
		checkTreeRetainedRB(t, trees[1])
	})
}

func TestStage3DefaultsDummyWhenAndPathUpdate(t *testing.T) {
	const module = `module stage3-defaults {yang-version 1.1;namespace "urn:stage3-defaults";prefix s;
 leaf-list d {type uint32;default 2;default 8;}
 container c {leaf gate {type string;default "off";} leaf required {when "../gate = 'on'";type string;mandatory true;} leaf-list v {type uint32;}}}`
	a := reviewDiffSet(t, module)
	tr := parseOnlyXML(t, a, `<c xmlns="urn:stage3-defaults"><v>7</v><v>3</v><v>11</v></c>`)
	for _, v := range []string{"off", "still-off"} {
		if _, err := tr.NewPath("/stage3-defaults:c/gate", v, NewPathOptions{Update: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := tr.ValidateModule(context.Background(), "stage3-defaults", ValidateOptions{}); err != nil {
			t.Fatal(err)
		}
		checkTreeRetainedRB(t, tr)
	}
	if _, err := tr.NewTerm(nil, "stage3-defaults:d", "4", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.ValidateModule(context.Background(), "stage3-defaults", ValidateOptions{}); err != nil {
		t.Fatal(err)
	}
	checkTreeRetainedRB(t, tr)
}

func TestStage3EnvelopeCleanupInvariants(t *testing.T) {
	a := reviewDiffSet(t, `module stage3-envelope {yang-version 1.1;namespace "urn:stage3-envelope";prefix s;notification n;}`)
	for _, tc := range []struct {
		f     Format
		typ   opType
		input string
	}{
		{FormatXML, opNotifNetconf, `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2013-12-21T00:01:00Z</eventTime><n xmlns="urn:stage3-envelope"/></notification>`},
		{FormatJSON, opNotifRestconf, `{"ietf-restconf:notification":{"eventTime":"2013-12-21T00:01:00Z","stage3-envelope:n":{}}}`},
	} {
		tr, _, env, _, err := parseOpWith(context.Background(), strings.NewReader(tc.input), a, tc.f, tc.typ, nil, Reject, Budget{})
		if err != nil {
			t.Fatal(err)
		}
		checkTreeRetainedRB(t, tr)
		checkTreeRetainedRB(t, env)
	}
}

func TestStage3BulkRefusalPublishesCompletedPrefix(t *testing.T) {
	a, b := retainedRBSets(t)
	tr := parseOnlyXML(t, a, retainedRBXML("top", 1, 3, 5))
	foreign := retainedRBTerm(t, retainedRBTop(t, b), "4")
	mustLink(t, tr.insert(nil, foreign, insertDefault))
	first := tr.top.first()
	err := tr.unlinkAll([]*Node{first, foreign})
	if err == nil || !strings.Contains(err.Error(), "D-0121") {
		t.Fatalf("bulk native selection: %v", err)
	}
	if first.siblingsOf() != nil || foreign.siblingsOf() != &tr.top {
		t.Fatal("refusal lost completed prefix or detached the refused member")
	}
	if got := retainedRBValues(&tr.top); !slices.Equal(got, []string{"3", "4", "5"}) {
		t.Fatalf("bulk refusal state: %v", got)
	}
	checkTreeRetainedRB(t, tr)
}

func TestStage3JournalBudgetOutranksLoggedError(t *testing.T) {
	a, _ := retainedRBSets(t)
	tr := parseOnlyXML(t, a, retainedRBXML("top", 5, 3, 7))
	before := snapshotHashBudgetTree(tr)
	j := beginHashTransaction(tr)
	if err := tr.top.touch(); err != nil {
		t.Fatal(err)
	}
	tr.top.first().flags ^= FlagNew
	tr.work.hashLimit = tr.work.hashUsed + 1
	if err := j.charge(2); !errors.Is(err, yang.ErrBudget) {
		t.Fatal(err)
	}
	var err error = &opError{"LY_EINVAL", "later diagnostic"}
	j.end(&err)
	if !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("journal error lost: %v", err)
	}
	if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
		t.Fatal("budget error committed a partial mutation")
	}
}

func TestStage3DetachedSingletonKeepsMetadata(t *testing.T) {
	const module = `module stage3-detached {yang-version 1.1;namespace "urn:stage3-detached";prefix s;
 container c {list l {key k;leaf k {type uint32;}leaf p {type string;}}}}`
	a := reviewDiffSet(t, module)
	src := parseOnlyXML(t, a, `<c xmlns="urn:stage3-detached"><l><k>5</k><p>carry</p></l><l><k>7</k><p>other</p></l></c>`)
	c := src.top.first()
	n := c.kids.first()
	if err := c.kids.At(1).Remove(); err != nil {
		t.Fatal(err)
	}
	old := c.kids.rbOrders[n.rbGroup]
	if err := n.Unlink(); err != nil {
		t.Fatal(err)
	}
	if old == nil || len(c.kids.rbOrders) != 0 {
		t.Fatal("singleton unlink discarded or shared metadata")
	}
	dst := parseOnlyXML(t, a, `<c xmlns="urn:stage3-detached"/>`)
	parent := dst.top.first()
	if err := dst.Insert(parent, n); err != nil {
		t.Fatal(err)
	}
	tail := parseOnlyXML(t, a, `<c xmlns="urn:stage3-detached"><l><k>5</k><p>tail</p></l></c>`)
	if _, _, err := dst.Dup(tail.top.first().kids.first(), parent, DupOptions{Recursive: true, NoSort: true}); err != nil {
		t.Fatal(err)
	}
	incoming := parseOnlyXML(t, a, `<c xmlns="urn:stage3-detached"><l><k>5</k><p>new</p></l></c>`)
	if err := dst.Insert(parent, incoming.top.first().kids.first()); err != nil {
		t.Fatal(err)
	}
	var payloads []string
	for _, l := range parent.kids.SchemaAll() {
		payloads = append(payloads, l.kids.At(1).Value())
	}
	if !slices.Equal(payloads, []string{"carry", "new", "tail"}) {
		t.Fatalf("native singleton history: %v", payloads)
	}
	if parent.kids.rbOrders[parent.kids.rbGroups[n]] != old || len(old.nodes) != 2 {
		t.Fatal("direct transfer rebuilt singleton history")
	}
	checkTreeRetainedRB(t, dst)
	checkTreeRetainedRB(t, src)
}

func TestStage3TemporaryOperationNativeTopology(t *testing.T) {
	const module = `module stage3-op {yang-version 1.1;namespace "urn:stage3-op";prefix s;
 list l {key k;leaf k {type uint32;}action act {input {leaf x {type string;}}}}}`
	a := reviewDiffSet(t, module)
	tr, _, _, err := parseOp(context.Background(), strings.NewReader(`<l xmlns="urn:stage3-op"><k>1</k><act><x>q</x></act></l>`), a, FormatXML, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err)
	}
	first := tr.top.first()
	ref := &refRB{}
	ref.insert(first)
	for _, v := range []string{"3", "5", "7", "9", "11", "13"} {
		n, err := tr.NewList(nil, "stage3-op:l", []string{v}, NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ref.insert(n)
	}
	if _, err := tr.ValidateOp(context.Background(), OpRPC, ValidateOpOptions{}); err != nil {
		t.Fatal(err)
	}
	ref.erase(first)
	if first.inRB || tr.top.rbTree[first] {
		t.Fatal("temporary positional restoration reinserted or moved native RB metadata")
	}
	leader := tr.top.At(1)
	checkRBShape(t, &tr.top, leader, ref)
	checkTreeRetainedRB(t, tr)
	if _, err := tr.NewList(nil, "stage3-op:l", []string{"2"}, NewOptions{}); err != nil {
		t.Fatal(err)
	}
	checkTreeRetainedRB(t, tr)
}

func TestStage3RollbackDisarmsRemovalUndo(t *testing.T) {
	a, _ := retainedRBSets(t)
	tr := parseOnlyXML(t, a, retainedRBXML("top", 11, 7, 15, 3, 9, 13, 17))
	before := snapshotHashBudgetTree(tr)
	j := beginHashTransaction(tr)
	n := tr.top.At(2)
	_, arm, err := tr.linkOrigin(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := arm(); err != nil {
		t.Fatal(err)
	}
	_ = unlink(n)
	err = yang.ErrBudget
	j.end(&err)
	if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
		t.Fatal("rollback changed graph")
	}
	for _, d := range tr.top.rbOrders {
		if d.undo != nil {
			t.Fatal("rollback retained an operation-local removal journal")
		}
	}
}

func TestStage3RollbackPreservesReboundTopologyWork(t *testing.T) {
	a, _ := retainedRBSets(t)
	src := parseOnlyXML(t, a, retainedRBXML("v", 7, 3, 11))
	root := src.top.first()
	d := root.kids.rbOrders[root.kids.rbGroups[root.kids.first()]]
	j := beginHashTransaction(src)
	tmp := newTree(a)
	if err := bindSubtreeWork(root, &tmp.work); err != nil {
		t.Fatal(err)
	}
	if err := root.kids.touch(); err != nil {
		t.Fatal(err)
	}
	d.root.red = true // graph capture occurred after work rebinding, as in temporary validation
	err := yang.ErrBudget
	j.end(&err)
	if root.kids.work != &src.work || d.work != &src.work || d.root.red {
		t.Fatal("rollback did not restore both the sibling and retained graph work ownership")
	}
	checkTreeRetainedRB(t, src)
}

func TestStage3WholeMoveKeepsAdditionalMetadata(t *testing.T) {
	const module = `module stage3-extra {yang-version 1.1;namespace "urn:stage3-extra";prefix s;
 list l {key k;leaf k {type uint32;}leaf p {type string;}action act {input {leaf x {type string;}}}}}`
	for _, promote := range []bool{false, true} {
		t.Run(fmt.Sprintf("promote=%t", promote), func(t *testing.T) {
			a := reviewDiffSet(t, module)
			src, _, _, err := parseOp(context.Background(), strings.NewReader(`<l xmlns="urn:stage3-extra"><k>1</k><act><x>q</x></act></l>`), a, FormatXML, opRPC, nil, Reject)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range []string{"3", "5", "7", "9", "11", "13"} {
				if _, err := src.NewList(nil, "stage3-extra:l", []string{v}, NewOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := src.ValidateOp(context.Background(), OpRPC, ValidateOpOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := src.NewList(nil, "stage3-extra:l", []string{"2"}, NewOptions{}); err != nil {
				t.Fatal(err)
			}
			if len(src.top.rbOrders) != 2 {
				t.Fatalf("fixture has %d graphs, want two", len(src.top.rbOrders))
			}
			if promote {
				for range 2 {
					if err := src.top.first().Remove(); err != nil {
						t.Fatal(err)
					}
				}
			}
			// The metadata left on k=3 is independent of the selected tree rebuilt at k=1.
			oldOwner := src.top.At(2)
			if promote {
				oldOwner = src.top.first()
			}
			oldGroup := src.top.rbGroups[oldOwner]
			if promote {
				oldGroup = src.top.rbMore[oldOwner][0]
			}
			old := src.top.rbOrders[oldGroup]
			for _, values := range [][]int{nil, {0, 4, 8}, {10, 12, 14}} {
				t.Run(fmt.Sprint(values), func(t *testing.T) {
					// Duplicate construction is independent of native tree metadata; use the complete source
					// again only for the first variant, then the already moved forest as the next source.
					dst := newTree(a)
					for _, v := range values {
						if _, err := dst.NewList(nil, "stage3-extra:l", []string{fmt.Sprint(v)}, NewOptions{}); err != nil {
							t.Fatal(err)
						}
					}
					if err := dst.Insert(nil, src.top.first()); err != nil {
						t.Fatal(err)
					}
					if src.top.len() != 0 || len(src.top.rbOrders) != 0 || len(src.top.rbMore) != 0 {
						t.Fatal("source still owns moved graphs")
					}
					if dst.top.rbOrders[oldGroup] != old || !dst.top.ownsGroup(oldOwner, oldGroup) || old.work != &dst.work {
						t.Fatal("additional metadata was rebuilt, lost, or retained the source counter")
					}
					checkTreeRetainedRB(t, dst)
					src = dst
				})
			}
		})
	}
}

func TestStage3BindingReservesBeforeAllocation(t *testing.T) {
	a, _ := retainedRBSets(t)
	tr := newTree(a)
	old := new(workCounter)
	root := &Node{}
	root.kids.work = old
	children := make([]*Node, 65536)
	leaf := &Node{}
	leaf.kids.work = old
	for i := range children {
		children[i] = leaf
	}
	mustLink(t, root.kids.buildSchema(children))
	j := beginHashTransaction(tr)
	tr.work.hashLimit = tr.work.hashUsed + 3
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	err := bindSubtreeWork(root, &tr.work)
	runtime.ReadMemStats(&after)
	j.end(&err)
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 128<<10 {
		t.Fatalf("refused binding allocated %d bytes (error %v)", grew, err)
	}
	if !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("binding: %v", err)
	}
	if root.kids.work != old || leaf.kids.work != old {
		t.Fatal("failed binding changed the source work counter")
	}
}

// TestStage3MoveRBRootBudgetPoints moves the retained root of [1,2,3] into another tree at
// every charge boundary of either tree. A refused source removal must stop the insertion (it
// once went on to link the still-linked node), and every budget failure restores both forests.
func TestStage3MoveRBRootBudgetPoints(t *testing.T) {
	a, _ := retainedRBSets(t)
	checkEveryHashBudgetPointPair(t, func() ([]*Tree, func() error) {
		src := parseOnlyXML(t, a, retainedRBXML("top", 1, 2, 3))
		dst := parseOnlyXML(t, a, retainedRBXML("top", 0, 5))
		root := src.top.rbOrders[src.top.rbGroups[src.top.first()]].root.node
		return []*Tree{src, dst}, func() error { return dst.Insert(nil, root) }
	}, func(t *testing.T, trees []*Tree) {
		t.Helper()
		for _, tr := range trees {
			checkTreeRetainedRB(t, tr)
		}
		if got := retainedRBValues(&trees[0].top); !slices.Equal(got, []string{"1", "3"}) {
			t.Fatalf("source after move: %v", got)
		}
	})
}

// TestStage3TrimBudgetRestoresBatch trims every retained member at every charge boundary.
// Bulk removal charges each native removal as it runs; TrimXPath has no journal, so a late
// budget failure must not publish the removals that preceded it (the tree is not changed).
func TestStage3TrimBudgetRestoresBatch(t *testing.T) {
	a, _ := retainedRBSets(t)
	checkEveryHashBudgetPoint(t, func() (*Tree, func() error) {
		tr := parseOnlyXML(t, a, retainedRBXML("v", 4)+retainedRBXML("top", 7, 3, 11, 1, 9, 5, 13, 2))
		return tr, func() error {
			_, err := tr.TrimXPath("/retained-rb-round5:c", XPathOptions{})
			return err
		}
	})
}

// TestStage3UnlinkPreflightChargesScan: locating a node in a wide run for its removal charges
// every inspected slot before reading it, so a tiny quota stops the scan early.
func TestStage3UnlinkPreflightChargesScan(t *testing.T) {
	a, _ := retainedRBSets(t)
	values := make([]int, 4096)
	for i := range values {
		values[i] = i
	}
	tr := parseOnlyXML(t, a, retainedRBXML("top", values...))
	done := tr.work.beginHashWork()
	defer done()
	tr.work.hashLimit = 3
	before := tr.work.Load()
	if err := preflightUnlink(tr.top.At(len(values) / 2)); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("preflight: %v", err)
	}
	if scanned := tr.work.Load() - before; scanned > 3 {
		t.Fatalf("refused preflight scanned %d slots", scanned)
	}
}

// TestStage3ValidateOpCleanupRefusalRestores validates an action under k=2 of snapshot A
// against a dependency whose first root is from A but whose sorted instances k=1,3 are from
// snapshot B. Moving the action's list instance back out of the dependency is a D-0121 removal;
// that refusal must restore both trees, not strand the operation in the dependency.
func TestStage3ValidateOpCleanupRefusalRestores(t *testing.T) {
	module := `module stage3-op-dep {yang-version 1.1;namespace "urn:stage3-op-dep";prefix s;
 container r {}
 list l {key k;leaf k {type uint32;} action act {input {leaf x {type string;}}}}}`
	a, b := reviewDiffSet(t, module), reviewDiffSet(t, module)
	dep := parseOnlyXML(t, a, `<r xmlns="urn:stage3-op-dep"/>`)
	foreign := parseOnlyXML(t, b, `<l xmlns="urn:stage3-op-dep"><k>1</k></l><l xmlns="urn:stage3-op-dep"><k>3</k></l>`)
	for _, n := range foreign.top.schemaSpan().Collect() {
		mustLink(t, unlink(n))
		mustLink(t, dep.insert(nil, n, insertDefault))
	}
	opTree, op, diags, err := parseOp(context.Background(), strings.NewReader(
		`<l xmlns="urn:stage3-op-dep"><k>2</k><act><x>a</x></act></l>`), a, FormatXML, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	beforeOp, beforeDep := snapshotHashBudgetTree(opTree), snapshotHashBudgetTree(dep)
	_, err = validateOp(context.Background(), a, op, dep, opRPC, Budget{})
	if err == nil || !strings.Contains(err.Error(), "D-0121") {
		t.Fatalf("cleanup refusal: %v", err)
	}
	if !reflect.DeepEqual(beforeOp, snapshotHashBudgetTree(opTree)) || !reflect.DeepEqual(beforeDep, snapshotHashBudgetTree(dep)) {
		t.Fatal("refused cleanup left the operation moved into the dependency")
	}
	checkTreeRetainedRB(t, dep)
	checkTreeRetainedRB(t, opTree)
}

// TestStage3TrimRefusalCleansPrefixLinks trims a container whose leafref is linked to a
// surviving target, then reaches an unsafe foreign member. The refusal publishes the completed
// removal prefix, which must still drop the removed descendant's reciprocal links.
func TestStage3TrimRefusalCleansPrefixLinks(t *testing.T) {
	a, b := retainedRBSets(t)
	tr := parseOnlyXML(t, a, retainedRBXML("v", 4)+retainedRBXML("top", 1, 3, 5))
	foreign := retainedRBTerm(t, retainedRBTop(t, b), "4")
	mustLink(t, tr.insert(nil, foreign, insertDefault))
	ref := tr.top.first().kids.first() // c/v
	target := tr.top.At(tr.top.SchemaLen() - 1)
	linkLeafrefNode(target, ref)
	if target.links == nil || ref.links == nil {
		t.Fatal("fixture link missing")
	}
	_, err := tr.TrimXPath("/retained-rb-round5:top[.='5']", XPathOptions{})
	if err == nil || !strings.Contains(err.Error(), "D-0121") {
		t.Fatalf("trim refusal: %v", err)
	}
	if ref.treeOf() != nil || ref.parent.siblingsOf() != nil {
		t.Fatal("the container was not part of the published prefix")
	}
	if target.links != nil || ref.links != nil {
		t.Fatalf("removed descendant kept reciprocal links: target=%v ref=%v", target.links, ref.links)
	}
	checkTreeRetainedRB(t, tr)
}

// TestStage3DetachedReplyBudgetCleanup parses a reply into a detached request with a node
// budget that fails after the first output leaf. A detached parent has no journal, so the
// parsed nodes must still be freed: failed reply data must not stay under the request.
func TestStage3DetachedReplyBudgetCleanup(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input { leaf i { type string; } } output { leaf o1 { type string; } leaf o2 { type string; } } } }`)
	for _, tc := range []struct {
		f     Format
		reply string
	}{
		{FormatXML, `<o1 xmlns="urn:m">a</o1><o2 xmlns="urn:m">b</o2>`},
		{FormatJSON, `{"m:o1":"a","m:o2":"b"}`},
	} {
		_, op := opIndexParseRPC(t, set, `{"m:r":{"i":"x"}}`)
		mustLink(t, unlink(op))
		before := op.kids.Len()
		_, _, _, _, err := parseOpWith(context.Background(), strings.NewReader(tc.reply), set, tc.f, opReply, op, Reject, Budget{MaxNodes: 1})
		if !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("format %v: %v", tc.f, err)
		}
		if got := op.kids.Len(); got != before {
			t.Fatalf("format %v: failed reply left %d children under the detached request, want %d", tc.f, got, before)
		}
	}
}

// stage3SingletonRoundTrip mirrors the native "singleton" probe: a copy of a many-slot owner
// becomes a singleton whose first metadata slot is populated and whose secondary slots are
// empty; it is unlinked, linked into an empty tree, unlinked again and reinserted sorted.
// It returns the probe's output lines.
func stage3SingletonRoundTrip(t *testing.T, set *schema.Set) string {
	t.Helper()
	add := func(tr *Tree, key string) *Node {
		t.Helper()
		n, err := tr.NewList(nil, "stage3-extra:l", []string{key}, NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	src := newTree(set)
	add(src, "3")
	add(src, "11")
	for range 32 {
		dst := newTree(set)
		if _, _, err := dst.Dup(src.top.first(), nil, DupOptions{Recursive: true, NoSort: true}); err != nil {
			t.Fatal(err)
		}
		src = newTree(set)
		for _, key := range []string{"5", "7", "9"} {
			add(src, key)
		}
		if err := dst.Insert(nil, src.top.first()); err != nil {
			t.Fatal(err)
		}
		src = dst
	}
	holder := newTree(set)
	d, _, err := holder.Dup(src.top.first(), nil, DupOptions{Recursive: true, NoSort: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := add(holder, "20").Remove(); err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for step := range 2 {
		if err := d.Unlink(); err != nil {
			t.Fatalf("unlink %d: %v", step, err)
		}
		slots := len(d.carriedRB)
		if d.detachedRB != nil {
			slots++
		}
		fmt.Fprintf(&got, "unlinked-slots=%d\n", slots)
		if step == 0 {
			empty := newTree(set)
			if err := empty.Insert(nil, d); err != nil {
				t.Fatal(err)
			}
			checkTreeRetainedRB(t, empty)
		}
	}
	dst := newTree(set)
	add(dst, "1")
	if err := dst.Insert(nil, d); err != nil {
		t.Fatal(err)
	}
	for step := range 2 {
		if step == 1 {
			add(dst, "2")
		}
		checkTreeRetainedRB(t, dst)
		fmt.Fprintf(&got, "slots=%d\n", len(dst.top.metaGroups(d)))
		var out bytes.Buffer
		if err := dst.PrintJSON(&out, PrintOptions{Shrink: true}); err != nil {
			t.Fatal(err)
		}
		got.WriteString(strings.TrimSpace(out.String()) + "\n")
	}
	return got.String()
}

// TestStage3SingletonMetadataRoundTrip: the selected singleton tree stays ahead of its carried
// secondary slots through an unlink/link/unlink round trip, and a sorted reinsertion frees only
// the first slot (lyds_insert). Expected output is the native probe's (TestStage3NativeMetadataMoves/singleton).
func TestStage3SingletonMetadataRoundTrip(t *testing.T) {
	set := reviewDiffSet(t, `module stage3-extra {yang-version 1.1;namespace "urn:stage3-extra";prefix s;
 list l {key k;leaf k {type uint32;}leaf p {type string;}action act {input {leaf x {type string;}}}}
 container c {list l {key k;leaf k {type uint32;}}}}`)
	want := `unlinked-slots=33
unlinked-slots=33
slots=32
{"stage3-extra:l":[{"k":1},{"k":3}]}
slots=32
{"stage3-extra:l":[{"k":1},{"k":2},{"k":3}]}
`
	if got := stage3SingletonRoundTrip(t, set); got != want {
		t.Fatalf("got\n%swant\n%s", got, want)
	}
}

// TestStage3DetachedReplyHashBudgetCleanup: a reply into a detached request links a small
// output leaf, then exhausts stored-hash work on sparse empty bits values (512 MiB of zero
// bytes each). The sticky hash error refuses any cleanup unlink; the parser tree's journal
// restores the request instead.
func TestStage3DetachedReplyHashBudgetCleanup(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  rpc r { input { leaf i { type string; } } output { leaf o1 { type string; }
    leaf-list o2 { type bits { bit high { position 4294967294; } } } } } }`)
	for _, tc := range []struct {
		f     Format
		reply string
	}{
		{FormatXML, `<o1 xmlns="urn:m">a</o1>` + strings.Repeat(`<o2 xmlns="urn:m"/>`, 1024)},
		{FormatJSON, `{"m:o1":"a","m:o2":[` + strings.Repeat(`"",`, 1023) + `""]}`},
	} {
		_, op := opIndexParseRPC(t, set, `{"m:r":{"i":"x"}}`)
		mustLink(t, unlink(op))
		before := op.kids.Len()
		_, _, _, _, err := parseOpWith(context.Background(), strings.NewReader(tc.reply), set, tc.f, opReply, op, Reject, Budget{})
		if !errors.Is(err, yang.ErrBudget) || !strings.Contains(err.Error(), "stored-hash") {
			t.Fatalf("format %v: %v", tc.f, err)
		}
		if got := op.kids.Len(); got != before {
			t.Fatalf("format %v: failed reply left %d children under the detached request, want %d", tc.f, got, before)
		}
	}
}
