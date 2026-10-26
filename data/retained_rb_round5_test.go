// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

const retainedRBModule = `module retained-rb-round5 { yang-version 1.1;
  namespace "urn:vibe-ports:yang:retained-rb-round5"; prefix r;
  container c { leaf-list v { type uint32; } }
  leaf-list top { type uint32; }
}`

func retainedRBSets(t *testing.T) (*schema.Set, *schema.Set) {
	t.Helper()
	return reviewDiffSet(t, retainedRBModule), reviewDiffSet(t, retainedRBModule)
}

func retainedRBXML(element string, values ...int) string {
	var out string
	if element == "v" {
		out = `<c xmlns="urn:vibe-ports:yang:retained-rb-round5">`
	}
	for _, value := range values {
		if element == "top" {
			out += fmt.Sprintf(`<top xmlns="urn:vibe-ports:yang:retained-rb-round5">%d</top>`, value)
		} else {
			out += fmt.Sprintf("<%s>%d</%s>", element, value, element)
		}
	}
	if element == "v" {
		out += "</c>"
	}
	return out
}

func retainedRBValues(sib *siblings) []string {
	out := make([]string, 0, sib.SchemaLen())
	for _, n := range sib.SchemaAll() {
		if n.schema != nil && n.schema.Kind == schema.LeafList {
			out = append(out, n.Value())
		}
	}
	return out
}

// TestDiffForeignSortedInsertionScalingAndBudget covers a one-member retained uint32 run and N
// foreign creates. Compatibility preflight must walk only the retained RB insertion path.
func TestDiffForeignSortedInsertionScalingAndBudget(t *testing.T) {
	measure := func(n int) (int64, error) {
		t.Helper()
		a, b := retainedRBSets(t)
		first := parseOnlyXML(t, a, retainedRBXML("v", 0))
		values := make([]int, n)
		for i := range values {
			values[i] = i + 1
		}
		second := parseOnlyXML(t, b, retainedRBXML("v", values...))
		diff, err := Diff(first, second, DiffOptions{})
		if diff == nil {
			return 0, err
		}
		return diff.work.Load(), err
	}
	one, err := measure(128)
	if err != nil {
		t.Fatal(err)
	}
	two, err := measure(256)
	if err != nil {
		t.Fatal(err)
	}
	if two > 3*one {
		t.Fatalf("doubling foreign creates grew retained-RB work from %d to %d", one, two)
	}
	a, b := retainedRBSets(t)
	values := make([]int, 256)
	for i := range values {
		values[i] = i + 1
	}
	retained := parseOnlyXML(t, a, retainedRBXML("top", values...))
	var foreignSchema *schema.Node
	for _, module := range b.Modules {
		for _, sn := range module.Top {
			if sn.Name == "top" {
				foreignSchema = sn
			}
		}
	}
	if foreignSchema == nil {
		t.Fatal("foreign top leaf-list schema not compiled")
	}
	v, diag := types.Store(foreignSchema.Type, "257", types.FormatJSON, types.JSONHints("number"),
		types.ModuleNames{Set: foreignSchema.Module.Owner}, foreignSchema)
	if diag != nil {
		t.Fatal(diag)
	}
	foreign := newTerm(foreignSchema, v)
	retained.work.hashLimit = 1
	end := retained.work.beginHashWork()
	err = retained.insert(nil, foreign, insertDefault)
	end()
	retained.work.hashLimit = 0
	if !errors.Is(err, yang.ErrBudget) || retained.work.hashUsed > 1 || foreign.siblingsOf() != nil {
		t.Fatalf("limited foreign insertion: used=%d linked=%t err=%v, want unlinked ErrBudget within one unit",
			retained.work.hashUsed, foreign.siblingsOf() != nil, err)
	}
}

// TestDiffForeignNewMinimumTransfersRBTopology is lyds_link_data_node's no-predecessor branch:
// the retained tree metadata moves to the foreign-context value 1 before 7 is inserted.
func TestDiffForeignNewMinimumTransfersRBTopology(t *testing.T) {
	a, b := retainedRBSets(t)
	first := parseOnlyXML(t, a, retainedRBXML("v", 5))
	second := parseOnlyXML(t, b, retainedRBXML("v", 1, 7))
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := topOf(diff)
	if got, want := retainedRBValues(&c.kids), []string{"1", "5", "7"}; !slices.Equal(got, want) {
		t.Fatalf("diff leaf-list order = %v, want %v", got, want)
	}
	checkTreeRetainedRB(t, diff)
}

// TestMergeThenForeignDiffInsertRetainsRBMembership covers flushMerge/splice followed by a
// foreign-context ApplyDiff create. The merged 5 and 7 must participate in the retained topology.
func TestMergeThenForeignDiffInsertRetainsRBMembership(t *testing.T) {
	a, b := retainedRBSets(t)
	dst := parseOnlyXML(t, a, retainedRBXML("top", 1, 3))
	src := parseOnlyXML(t, a, retainedRBXML("top", 5, 7))
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	checkTreeRetainedRB(t, dst)
	foreign := parseOnlyXML(t, b, retainedRBXML("top", 6))
	diff, err := Diff(nil, foreign, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, want := retainedRBValues(&dst.top), []string{"1", "3", "5", "6", "7"}; !slices.Equal(got, want) {
		t.Fatalf("merge then foreign insert = %v, want %v", got, want)
	}
	checkTreeRetainedRB(t, dst)
}

// checkTreeRetainedRB is the debug-only retained-topology invariant checker. For every selected
// system-ordered run it checks exact membership, physical/in-order equality, parent/size links,
// red-black colouring, and equal black height.
func checkTreeRetainedRB(t *testing.T, tr *Tree) {
	t.Helper()
	var checkSiblings func(*siblings)
	checkSiblings = func(sib *siblings) {
		for group, order := range sib.rbOrders {
			hasLeader := false
			for leader := range sib.rbGroups {
				if sib.ownsGroup(leader, group) {
					hasLeader = true
					break
				}
			}
			if !hasLeader {
				t.Fatalf("retained RB group %p has no physical leader", group)
			}
			physical := make([]*Node, 0, len(order.nodes))
			for _, n := range sib.logical().All() {
				if n.inRB && n.rbGroup == group {
					physical = append(physical, n)
				}
			}
			seen := make(map[*dirtyOrderNode]bool, len(physical))
			inorder := make([]*Node, 0, len(physical))
			var walk func(*dirtyOrderNode, *dirtyOrderNode) (int, int)
			walk = func(n, parent *dirtyOrderNode) (int, int) {
				if n == nil {
					return 0, 1
				}
				if seen[n] {
					t.Fatalf("cycle in retained RB group %p", group)
				}
				seen[n] = true
				if n.parent != parent || !n.active || order.nodes[n.node] != n {
					t.Fatalf("invalid retained RB links for %p", n.node)
				}
				if n.red && (dirtyRed(n.left) || dirtyRed(n.right)) {
					t.Fatalf("red retained RB node %p has a red child", n.node)
				}
				lc, lb := walk(n.left, n)
				inorder = append(inorder, n.node)
				rc, rb := walk(n.right, n)
				if lb != rb {
					t.Fatalf("retained RB node %p has black heights %d and %d", n.node, lb, rb)
				}
				count := lc + rc + 1
				if n.size != count {
					t.Fatalf("retained RB node %p size=%d, want %d", n.node, n.size, count)
				}
				return count, lb + btoi(!n.red)
			}
			if order.root != nil && order.root.red {
				t.Fatalf("retained RB group %p has a red root", group)
			}
			count, _ := walk(order.root, nil)
			// A node can keep its node in an earlier tree after its own run got a new one
			// (dual membership, as libyang's rb nodes): it must still be a linked RB member.
			linked := make(map[*Node]bool, sib.SchemaLen())
			for _, n := range sib.SchemaAll() {
				linked[n] = true
			}
			own := make([]*Node, 0, len(inorder))
			for _, n := range inorder {
				switch {
				case n.inRB && n.rbGroup == group:
					own = append(own, n)
				case !linked[n] || !n.inRB:
					t.Fatalf("retained RB group %p holds %p, not a linked RB member", group, n)
				}
			}
			inorder = own
			if count != len(order.nodes) || !slices.Equal(inorder, physical) {
				values := func(nodes []*Node) []string {
					out := make([]string, len(nodes))
					for i, n := range nodes {
						out[i] = n.Value()
					}
					return out
				}
				t.Fatalf("retained RB group %p: reachable=%d mapped=%d physical=%v inorder=%v",
					group, count, len(order.nodes), values(physical), values(inorder))
			}
		}
		for leader := range sib.rbTree {
			group := sib.rbGroups[leader]
			if group == nil || sib.rbOrders[group] == nil {
				t.Fatalf("RB leader %p has no retained topology", leader)
			}
		}
		for _, n := range sib.SchemaAll() {
			checkSiblings(&n.kids)
			if n.any != nil {
				checkSiblings(&n.any.children)
			}
		}
	}
	checkSiblings(&tr.top)
}

// TestRetainedRBRandomMixedContextMutations runs a deterministic few-thousand-operation mutation
// stream over equivalent schemas from two contexts and checks the retained topology after every
// successful mutation.
func TestRetainedRBRandomMixedContextMutations(t *testing.T) {
	a, b := retainedRBSets(t)
	findTop := func(set *schema.Set) *schema.Node {
		for _, module := range set.Modules {
			for _, sn := range module.Top {
				if sn.Name == "top" {
					return sn
				}
			}
		}
		return nil
	}
	schemaA, schemaB := findTop(a), findTop(b)
	if schemaA == nil || schemaB == nil {
		t.Fatal("top leaf-list schema not compiled")
	}
	tr := parseOnlyXML(t, a, retainedRBXML("top", 9, 1, 5))
	checkTreeRetainedRB(t, tr) // parser append plus deferred sort/reorder
	rollbackSource := parseOnlyXML(t, a, retainedRBXML("top", 13, 15, 17))
	beforeRollback := slices.Clone(retainedRBValues(&tr.top))
	tr.work.hashLimit = 1
	if err := tr.Merge(rollbackSource); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("rollback probe error = %v, want ErrBudget", err)
	}
	tr.work.hashLimit = 0
	tr.work.hashErr, tr.work.hashUsed = nil, 0
	if got := retainedRBValues(&tr.top); !slices.Equal(got, beforeRollback) {
		t.Fatalf("budget rollback changed values from %v to %v", beforeRollback, got)
	}
	checkTreeRetainedRB(t, tr)
	rng := rand.New(rand.NewPCG(0x301, 5)) //nolint:gosec // deterministic mutation sequence
	steps := 4096
	if raceEnabled {
		steps = 1024 // fewer random mutations under the race detector
	}
	for step := range steps {
		// Internal primitives below represent distinct public operations.
		tr.work.hashErr, tr.work.hashUsed = nil, 0
		op := rng.IntN(12)
		switch op {
		case 0, 1:
			sn := schemaA
			if rng.IntN(2) != 0 {
				sn = schemaB
			}
			value := fmt.Sprint(rng.IntN(512))
			v, diag := types.Store(sn.Type, value, types.FormatJSON, types.JSONHints("number"), types.ModuleNames{Set: sn.Module.Owner}, sn)
			if diag != nil {
				t.Fatal(diag)
			}
			if err := tr.insert(nil, newTerm(sn, v, &tr.work), insertDefault); err != nil {
				t.Fatalf("step %d insert: %v", step, err)
			}
		case 2:
			if tr.top.SchemaLen() > 2 {
				if err := tr.top.At(rng.IntN(tr.top.SchemaLen())).Remove(); err != nil && !strings.Contains(err.Error(), "D-0121") {
					t.Fatalf("step %d remove: %v", step, err)
				}
			}
		case 3:
			if tr.top.SchemaLen() > 0 {
				n := tr.top.At(rng.IntN(tr.top.SchemaLen()))
				if err := n.Unlink(); err != nil {
					if strings.Contains(err.Error(), "D-0121") {
						checkTreeRetainedRB(t, tr)
						continue
					}
					t.Fatalf("step %d unlink: %v", step, err)
				}
				if err := tr.Insert(nil, n); err != nil {
					t.Fatalf("step %d reinsert: %v", step, err)
				}
			}
		case 4:
			if tr.top.SchemaLen() > 0 {
				n := tr.top.At(rng.IntN(tr.top.SchemaLen()))
				if _, err := n.SetValue(fmt.Sprint(rng.IntN(512))); err != nil && !strings.Contains(err.Error(), "D-0121") {
					t.Fatalf("step %d SetValue: %v", step, err)
				}
			}
		case 5:
			set := tr.context()
			foreign := false
			if rng.IntN(2) != 0 {
				foreign = true
				if set == a {
					set = b
				} else {
					set = a
				}
			}
			src := parseOnlyXML(t, set, retainedRBXML("top", rng.IntN(512), rng.IntN(512)))
			if !foreign {
				if err := tr.Merge(src); err != nil {
					t.Fatalf("step %d merge: %v", step, err)
				}
			} else {
				diff, err := Diff(nil, src, DiffOptions{})
				if err != nil {
					t.Fatalf("step %d foreign diff: %v", step, err)
				}
				if err := tr.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
					t.Fatalf("step %d foreign apply: %v", step, err)
				}
			}
		case 6:
			src := parseOnlyXML(t, a, retainedRBXML("top", rng.IntN(512), rng.IntN(512)))
			if _, _, err := tr.DupSiblings(src.top.first(), nil, DupOptions{}); err != nil {
				t.Fatalf("step %d dup siblings: %v", step, err)
			}
		case 8:
			// Resume the same retained mixed-context forest through parser primitives.
			sn := schemaA
			if tr.context() == b {
				sn = schemaB
			}
			lc := &lydCtx{tree: tr, ctx: context.Background()}
			for _, v := range []int{6, 8, 4} {
				if err := lc.nodeInsert(nil, nil, retainedRBTerm(t, sn, fmt.Sprint(v))); err != nil {
					t.Fatal(err)
				}
				checkTreeRetainedRB(t, tr)
			}
			if err := lc.flushDirtyAll(); err != nil {
				t.Fatal(err)
			}
		case 9:
			if tr.top.SchemaLen() > 1 {
				before := snapshotHashBudgetTree(tr)
				j := beginHashTransaction(tr)
				err := tr.unlinkAll([]*Node{tr.top.At(0)})
				if err != nil && !strings.Contains(err.Error(), "D-0121") {
					t.Fatal(err)
				}
				err = yang.ErrBudget
				j.end(&err)
				if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
					t.Fatalf("step %d rollback changed graph", step)
				}
			}
		case 10:
			payload := parseOnlyXML(t, a, retainedRBXML("top", rng.IntN(512), rng.IntN(512)))
			dst, _, sn := anyFixture()
			n := &Node{schema: sn, any: &anyValue{}}
			mustLink(t, dst.insert(nil, n, insertDefault))
			if err := n.takeAnyChildren(dst, payload.top.first()); err != nil {
				t.Fatal(err)
			}
			checkTreeRetainedRB(t, dst)
			if err := n.clearAny(); err != nil {
				t.Fatal(err)
			}
			checkTreeRetainedRB(t, dst)
		case 11:
			src := parseOnlyXML(t, tr.context(), retainedRBXML("top", rng.IntN(512), rng.IntN(512), rng.IntN(512)))
			if err := tr.Insert(nil, src.top.first()); err != nil && !strings.Contains(err.Error(), "D-0121") {
				t.Fatal(err)
			}
			checkTreeRetainedRB(t, src)
		case 7:
			if tr.top.SchemaLen() > 128 {
				remove := tr.top.schemaSpan().Slice(0, tr.top.SchemaLen()/2).Collect()
				if err := tr.unlinkAll(remove); err != nil && !strings.Contains(err.Error(), "D-0121") {
					t.Fatalf("step %d unlinkAll: %v", step, err)
				}
			}
		}
		checkTreeRetainedRB(t, tr)
	}
}
