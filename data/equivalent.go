// SPDX-License-Identifier: BSD-3-Clause

package data

import "github.com/vibe-ports/yang/internal/schema"

// equivalentIn is sn's equivalent in set: sn itself when it is a node of set, else the node with
// the same lineage, the one lyd_compare_schema_equal with all parents accepts (node type, module
// name and node name at every level, choice/case, operation input/output and extension-instance
// roots included), nil if none or if a level has no module. A lineage rooted at an extension
// root whose identity another top-level node of set shares has no single equivalent:
// schema.ErrAmbiguousRoot is returned, never a guess.
//
// The lineage is collected with one unit per ancestor, paid before it is recorded, then mapped
// from its root down, one unit per level and one ChildByName read: O(depth) fixed-size map reads
// once the indexes are built (cold builds pay their own way). charge (nil: uncharged) also polls:
// a refusal is returned, never taken for a miss, and stores nothing. Only set's own nodes are
// indexed, so nothing of sn's context is retained.
func equivalentIn(sn *schema.Node, set *schema.Set, charge func(int64) error) (*schema.Node, error) {
	if sn == nil || sn.Module != nil && sn.Module.Owner == set {
		return sn, nil
	}
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
		if n.Module == nil {
			return nil, nil
		}
		lineage = append(lineage, n)
	}
	var k *schema.Node
	for i := len(lineage) - 1; i >= 0; i-- {
		if err := pay(); err != nil {
			return nil, err
		}
		next, err := set.ChildByName(k, schema.ChildKey{Kind: lineage[i].Kind, ID: lineage[i].ID()}, charge)
		if err != nil || next == nil {
			return nil, err
		}
		k = next
	}
	return k, nil
}
