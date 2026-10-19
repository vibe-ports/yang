// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"testing"

	"github.com/vibe-ports/yang/internal/contextopts"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// Distinct root schemas used to trigger one has() scan per Remove. Bulk disposal
// does no sibling visits proportional to the remaining root count.
func TestDiscardTreeLinearDistinctRoots(t *testing.T) {
	for _, count := range []int{128, 256, 512} {
		set := &schema.Set{}
		mod := &schema.Module{Name: "bulk", Owner: set, Implemented: true}
		set.Modules = []*schema.Module{mod}
		tr := newTree(set)
		for i := range count {
			sn := &schema.Node{Kind: schema.Leaf, Name: fmt.Sprintf("leaf%d", i), Module: mod, Type: &schema.Type{Base: schema.String}, Config: true}
			mod.Top = append(mod.Top, sn)
			v, diag := types.StoreOnly(sn.Type, "x", types.FormatJSON, types.HintData, types.ModuleNames{Set: set}, sn)
			if diag != nil {
				t.Fatal(diag)
			}
			if err := tr.insert(nil, newTerm(sn, v), insertDefault); err != nil {
				t.Fatal(err)
			}
		}
		tr.work.Store(0)
		tr.work.visits = true
		if err := contextopts.DiscardTree(tr); err != nil {
			t.Fatal(err)
		}
		if tr.top.len() != 0 {
			t.Fatalf("count=%d: %d roots remain", count, tr.top.len())
		}
		if work := tr.work.Load(); work > int64(count*8) {
			t.Fatalf("count=%d: %d work units", count, work)
		}
	}
}
