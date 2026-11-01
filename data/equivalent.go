// SPDX-License-Identifier: BSD-3-Clause

package data

import "github.com/vibe-ports/yang/internal/schema"

// equivalentIn is sn's equivalent in set: the node with the same lineage, the one
// lyd_compare_schema_equal with all parents accepts (node type, module name and node name at
// every level, choice/case, operation input/output and extension-instance roots included), nil if
// none. Several nodes of set can have that lineage only under equal extension-instance roots; the
// first in index order (schema.Set.ChildrenByName) is returned.
//
// The lineage is collected with one unit per ancestor, paid before it is recorded, then mapped
// from its root down, each level through the candidate parents' child indexes in set, one unit per
// candidate read: O(depth) fixed-size map reads once the indexes are built (cold builds pay their
// own way). charge (nil: uncharged) also polls: a refusal is returned, never taken for a miss, and
// stores nothing. Only set's own nodes are indexed, so nothing of sn's context is retained.
func equivalentIn(sn *schema.Node, set *schema.Set, charge func(int64) error) (*schema.Node, error) {
	pay := func() error {
		if charge == nil {
			return nil
		}
		return charge(1)
	}
	var lineage []*schema.Node
	for n := sn; n != nil; n = n.Parent {
		if err := pay(); err != nil {
			return nil, err
		}
		lineage = append(lineage, n)
	}
	cands := []*schema.Node{nil} // the top level
	for i := len(lineage) - 1; i >= 0 && len(cands) > 0; i-- {
		key := schema.ChildKey{Kind: lineage[i].Kind, ID: lineage[i].ID()}
		var next []*schema.Node
		for _, p := range cands {
			if err := pay(); err != nil {
				return nil, err
			}
			ks, err := set.ChildrenByName(p, key, charge)
			if err != nil {
				return nil, err
			}
			next = append(next, ks...)
		}
		cands = next
	}
	if len(cands) == 0 {
		return nil, nil
	}
	return cands[0], nil
}
