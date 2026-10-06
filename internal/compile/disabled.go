// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile.c (lys_compile_unres_check_disabled and the
// disabled-node loop of lys_compile_unres_depset) and src/tree_schema_free.c (lysc_node_free
// with unlink) (BSD-3-Clause, © CESNET).

package compile

import (
	"slices"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/schema"
)

// removeDisabled is step j of lys_compile_unres_depset (design 06 §2, P6): every node of the
// disabled set, in the order it was added, is checked and unlinked with its subtree. A disabled
// key is an error; a disabled unique leaf is dropped from the uniques of its list ancestors.
// The unres loop of design 06 C7 calls it at its place in P6; until then the node-walk tests do.
func (c *Context) removeDisabled() error {
	for _, n := range c.disabled {
		if err := c.checkDisabled(n); err != nil {
			return err
		}
		unlink(n)
	}
	c.disabled = nil
	return nil
}

// checkDisabled is lys_compile_unres_check_disabled.
func (c *Context) checkDisabled(n *schema.Node) error {
	if n.IsKey() {
		return c.logPath(ly.Reference, n.LogPath(), "Key \"%s\" is disabled.", n.Name)
	}
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Kind != schema.List {
			continue
		}
		for u, uq := range p.Uniques {
			v := slices.Index(uq, n)
			if v < 0 {
				continue
			}
			if len(uq) > 1 {
				p.Uniques[u] = slices.Delete(uq, v, v+1)
			} else if p.Uniques = slices.Delete(p.Uniques, u, u+1); len(p.Uniques) == 0 {
				p.Uniques = nil
			}
			break
		}
	}
	return nil
}

// unlink is lysc_node_free(node, 1) without the freeing: n leaves its parent or its module's top
// level. An input or output stays (it is part of its action); only its children go.
func unlink(n *schema.Node) {
	if n.Kind == schema.Input || n.Kind == schema.Output {
		n.Children = nil
		return
	}
	drop := func(list *[]*schema.Node) {
		*list = slices.DeleteFunc(*list, func(x *schema.Node) bool { return x == n })
	}
	switch p := n.Parent; {
	case p == nil:
		drop(&n.Module.Top)
	case n.Kind == schema.Action:
		drop(&p.Actions)
	case n.Kind == schema.Notification:
		drop(&p.Notifs)
	default:
		drop(&p.Children)
	}
}
