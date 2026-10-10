// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_trim_xpath, lyd_trim_equal_cb)
// (BSD-3-Clause, © CESNET).

package data

import (
	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/xpath"
)

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
	top := t.top.nodes()
	if len(top) == 0 {
		return nil, nil // nothing to do
	}
	if o.Format != XPathJSON || o.Namespaces != nil || o.Module != "" {
		err := l.done(l.logErr("LY_EINVAL", "Invalid argument %s (%s()).", "XPathOptions.Format, Namespaces, Module (JSON only)", "lyd_trim_xpath"))
		return l.diags, err
	}
	vars := make([]xpath.Var, len(o.Vars))
	for i, v := range o.Vars {
		vars[i] = xpath.Var(v)
	}
	r, err := t.evalXPath4(l, top[0], expr, jsonNS{set: t.set}, vars, false, xpath.NodeSet)
	if err != nil {
		return l.diags, l.done(err)
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
			results[n] = true
			for p := n.parent; p != nil; p = p.parent {
				if exists = parents[p]; exists {
					break // shared parent, done
				}
				parents[p] = true
			}
		}
	}
	var free []*Node
	var dfs func(n *Node)
	dfs = func(n *Node) {
		switch {
		case n.isKey():
		case results[n]: // the whole subtree stays
			return
		case !parents[n]: // neither selected nor above a selected node
			free = append(free, n)
			return
		}
		for c := range n.kids.all() {
			dfs(c)
		}
	}
	for _, n := range top {
		dfs(n)
	}
	if len(free) == 0 && exists {
		return l.diags, l.done(rcError("LY_EEXIST")) // libyang returns it without a message
	}
	for _, n := range free {
		if err := freeTree(n); err != nil {
			return l.diags, l.done(err)
		}
	}
	return l.diags, nil
}
