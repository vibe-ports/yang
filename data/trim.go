// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_trim_xpath, lyd_trim_equal_cb)
// (BSD-3-Clause, © CESNET).

package data

import (
	"fmt"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/xpath"
)

// trimStepBudget is the step budget one TrimXPath evaluation and its trim share; tests lower it.
var trimStepBudget = int64(xpath.DefaultMaxSteps)

// appendTrimSubtree appends root and its descendants in pre-order, charging each node before
// looking at its children. The stack is bounded by tree depth: wide sibling lists are read one
// element at a time instead of being copied into Node.All's stack before the next charge.
func appendTrimSubtree(dst *[]*Node, root *Node, charge func(int64) bool) bool {
	type frame struct {
		n    *Node
		next int
	}
	if !charge(1) {
		return false
	}
	*dst = append(*dst, root)
	stack := []frame{{n: root}}
	for len(stack) > 0 {
		f := &stack[len(stack)-1]
		if f.next == f.n.kids.len() {
			stack = stack[:len(stack)-1]
			continue
		}
		if !charge(1) {
			return false
		}
		i := f.next
		f.next++
		var child *Node
		if i < len(f.n.kids.list) {
			child = f.n.kids.list[i]
		} else {
			child = f.n.kids.opq[i-len(f.n.kids.list)]
		}
		f.n.kids.visit()
		*dst = append(*dst, child)
		stack = append(stack, frame{n: child})
	}
	return true
}

// TrimXPath is lyd_trim_xpath: the expression is evaluated over t with the first top-level node
// as the context and current node (as libyang does; a non-nil o.Node is an LY_EINVAL
// *ValidationError), when conditions ignored, and every node that is neither selected (with its
// subtree) nor an ancestor of a selected node is removed; list keys stay with their instance.
// o.Vars, the expression, diagnostics, errors and budget are as for FindXPath. On an error the
// tree is not changed. Like libyang, a trim that removes nothing returns LY_EEXIST (a
// *ValidationError without diagnostics) when the last parent of a selected node it recorded was
// recorded before (`//.` on a nested tree): the tree is unchanged. A modification: it needs
// exclusive access to t.
func (t *Tree) TrimXPath(expr string, o XPathOptions) ([]yang.Diagnostic, error) {
	l := &logger{set: t.set}
	if o.Node != nil {
		err := l.done(l.logErr("LY_EINVAL", "Invalid argument %s (%s()).", "XPathOptions.Node (always the first top-level node)", "lyd_trim_xpath"))
		return l.diags, err
	}
	topLen := t.top.len()
	if topLen == 0 {
		return nil, nil // nothing to do
	}
	if o.Format != XPathJSON || o.Namespaces != nil || o.Module != "" {
		err := l.done(l.logErr("LY_EINVAL", "Invalid argument %s (%s()).", "XPathOptions.Format, Namespaces, Module (JSON only)", "lyd_trim_xpath"))
		return l.diags, err
	}
	budgetErr := func() ([]yang.Diagnostic, error) {
		err := fmt.Errorf("%w: XPath step budget (%d per evaluation, %d per operation): %w",
			yang.ErrBudget, xpath.DefaultMaxSteps, DefaultMaxXPathSteps, xpath.ErrBudget)
		return l.diags, l.done(err)
	}
	// Do not even materialize an input view larger than one evaluation can inspect.
	if topLen > xpath.DefaultMaxSteps {
		return budgetErr()
	}
	top := t.top.nodes()
	vars := make([]xpath.Var, len(o.Vars))
	for i, v := range o.Vars {
		vars[i] = xpath.Var(v)
	}
	r, err := t.evalXPath4(l, top[0], expr, jsonNS{set: t.set}, vars, false, xpath.NodeSet)
	if err != nil {
		return l.diags, l.done(err)
	}
	// The evaluation and the trim are one bounded operation. Everything below is read-only until
	// the final link cleanup and unlinkAll, so exhausting the remaining per-evaluation budget still
	// leaves the tree intact.
	left := trimStepBudget - r.Steps
	charge := func(n int64) bool {
		if n > left {
			return false
		}
		left -= n
		return true
	}
	if !charge(2*int64(topLen) + 1) { // top and evaluator views, plus their setup
		return budgetErr()
	}
	// the selected element nodes and every parent of one (libyang's parent_ht; a map stands for
	// lyd_trim_equal_cb's pointer equality). libyang looks results up in the set's own hash table,
	// which an attribute step leaves stale; the port uses the set's element items (D-0101).
	// libyang: ret keeps the result of the last parent insert (LY_EEXIST when it found a parent
	// already there), and only ly_set_add of a node to free resets it
	results, parents := map[*Node]bool{}, map[*Node]bool{}
	exists := false
	if r.Type == xpath.NodeSet {
		for _, n := range nodesOf(r) {
			if !charge(1) { // result map insertion
				return budgetErr()
			}
			results[n] = true
			for p := n.parent; p != nil; p = p.parent {
				if !charge(1) { // parent map lookup/insertion
					return budgetErr()
				}
				if exists = parents[p]; exists {
					break // shared parent, done
				}
				parents[p] = true
			}
		}
	}
	var free []*Node
	var freeNodes []*Node
	var dfs func(n *Node) bool
	dfs = func(n *Node) bool {
		if !charge(2) { // this walk and the later sibling-list compaction
			return false
		}
		switch {
		case n.isKey():
		case results[n]: // the whole subtree stays
			return true
		case !parents[n]: // neither selected nor above a selected node
			free = append(free, n)
			if !appendTrimSubtree(&freeNodes, n, charge) { // freeTree's subtree link cleanup
				return false
			}
			return true
		}
		for c := range n.kids.all() {
			if !dfs(c) {
				return false
			}
		}
		return true
	}
	for _, n := range top {
		if !dfs(n) {
			return budgetErr()
		}
	}
	if len(free) == 0 && exists {
		return l.diags, l.done(rcError("LY_EEXIST")) // libyang returns it without a message
	}
	if !chargeBulkFreeLinks(freeNodes, charge, &t.work) {
		return budgetErr()
	}
	defaults := npContDfltAffected(free)
	// unlinkAllDefaults groups its first-instance simulation by sibling set. Preflight the
	// grouping, each set's position table, and its own removal walk before any mutation.
	groups := map[*siblings]int{}
	for _, n := range free {
		if sib := n.siblingsOf(); sib != nil && sib.ht != nil && len(sib.list) != 0 && opChild(sib.list[0].schema) {
			if !charge(1) { // group lookup/append
				return budgetErr()
			}
			groups[sib]++
		}
	}
	for sib, count := range groups {
		// Each removal may also remove a first-instance record, test its successor, and append
		// that successor. Reserve all three constant-time record operations as well as the
		// set-local removal walk.
		if !charge(int64(len(sib.list) + 4*count)) { // position table, removal walk, record updates
			return budgetErr()
		}
	}
	for _, p := range defaults {
		// One ancestor-plan entry and at most one visit of every current child. Removed
		// children make the eventual post-unlink scan no larger than this preflight bound.
		if !charge(1 + int64(p.kids.len())) {
			return budgetErr()
		}
	}
	cleaner := bulkLinkCleaner{work: &t.work}
	for _, n := range freeNodes {
		cleaner.free(n)
	}
	if err := t.unlinkAllDefaults(free, defaults); err != nil {
		return l.diags, l.done(err)
	}
	return l.diags, nil
}
