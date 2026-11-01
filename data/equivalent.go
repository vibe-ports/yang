// SPDX-License-Identifier: BSD-3-Clause

package data

import "github.com/vibe-ports/yang/internal/schema"

// equivalentIn is sn's equivalent in set: the node with the same lineage, the one
// lyd_compare_schema_equal with all parents accepts (node type, module name and node name at
// every level, choice/case and operation input/output included), nil if none. It maps the
// lineage root first and then each level through that parent's child index in set
// (schema.Set.ChildByName): O(depth) fixed-size map reads once the indexes are built. Each level
// is paid through charge (nil: uncharged), as is every cold index build; a refusal is returned,
// never taken for a miss, and stores nothing. Only set's own nodes are indexed, so nothing of
// sn's context is retained.
func equivalentIn(sn *schema.Node, set *schema.Set, charge func(int64) error) (*schema.Node, error) {
	var lineage []*schema.Node
	for n := sn; n != nil; n = n.Parent {
		lineage = append(lineage, n)
	}
	var k *schema.Node
	for i := len(lineage) - 1; i >= 0; i-- {
		if charge != nil {
			if err := charge(1); err != nil {
				return nil, err
			}
		}
		n := lineage[i]
		next, err := set.ChildByName(k, schema.ChildKey{Kind: n.Kind, ID: n.ID()}, charge)
		if err != nil || next == nil {
			return nil, err
		}
		k = next
	}
	return k, nil
}
