// SPDX-License-Identifier: BSD-3-Clause

package data

import "github.com/vibe-ports/yang/internal/types"

// preflightChangedReinsert checks native list comparator preconditions on the graph after
// removal, before hash registrations or physical links change. A parser-mutated list can
// leave a keyless member that native comparison would dereference. The prepaid logarithmic
// removal undo restores every topology object even if the prospective search exhausts quota.
func (t *Tree) preflightChangedReinsert(term, target *Node, value types.Value) error {
	if !term.isKey() {
		return nil
	}
	sib := target.siblingsOf()

	p := t.work.unlinkPlan
	if sib == nil || p == nil || p.node != target || p.sib != sib {
		return nil
	}
	d := p.order
	if p.leader == target && p.next != nil && sib.rbGroups[p.next] != nil {
		d = sib.rbOrders[sib.rbGroups[p.next]]
	}
	if err := sib.touch(); err != nil {
		return err
	}
	if err := term.touch(); err != nil {
		return err
	}
	if d != nil && d.root != nil {
		if err := t.work.chargeHash(d.removalBound()); err != nil {
			return err
		}
		d.work = &t.work
		undo := d.armRemovalUndo(target, &t.work)
		d.remove(target)
		defer undo()
	}
	if d == nil || d.root == nil {
		lo := t.sortedLeaderIndex(sib, target)
		if lo < 0 {
			return t.work.hashErr
		}
		hi := t.retainedRunEnd(sib, lo)
		if err := t.work.chargeHash(uint64(hi-lo) + 1); err != nil { //nolint:gosec // bounded slice length
			return err
		}
		d = &dirtyOrder{}
		for _, member := range sib.schemaSpan().Slice(lo, hi).All() {
			if member == target {
				continue
			}
			if _, ok := t.rbAdd(d, member, t.compareSortedCompatible); !ok {
				if t.work.hashErr != nil {
					return t.work.hashErr
				}
				return sortedInsertRefusal(sib, target)
			}
		}
	}
	old := term.value
	term.value = value
	defer func() { term.value = old }()
	_, _, _, ok := d.search(target, t.chargedCompare(t.compareSortedCompatible))
	if t.work.hashErr != nil {
		return t.work.hashErr
	}
	if !ok {
		return sortedInsertRefusal(sib, target)
	}
	return nil
}
