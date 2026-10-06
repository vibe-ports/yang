// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_schema.c (lysc_path_until, lysc_tree_dfs_full,
// lysc_module_dfs_full) and src/tree_schema_common.c (lysc_owner_module) (BSD-3-Clause, © CESNET).

package schema

import (
	"iter"
	"strings"
)

// PathType is LYSC_PATH_TYPE.
type PathType uint8

// Path types.
const (
	// PathLog is LYSC_PATH_LOG: every node, choice/case/input/output included.
	PathLog PathType = iota
	// PathData is LYSC_PATH_DATA: data nodes only, as in a data path.
	PathData
	// PathDataPattern is LYSC_PATH_DATA_PATTERN: PathData with "[key='%s']" predicates on lists.
	PathDataPattern
)

// Path is lysc_path: the schema path of n. A module name prefixes the first node and every node
// whose module differs from its parent's (for the data types, its data parent's).
func (n *Node) Path(pt PathType) string {
	var segs []string
	data := pt == PathData || pt == PathDataPattern
	for it := n; it != nil; it = it.Parent {
		if data && (it.Kind == Choice || it.Kind == Case || it.Kind == Input || it.Kind == Output) {
			continue // schema-only node
		}
		var b strings.Builder
		par := it.Parent
		if data {
			par = it.DataParent()
		}
		b.WriteByte('/')
		if par == nil || par.Module != it.Module {
			b.WriteString(it.Module.Name)
			b.WriteByte(':')
		}
		b.WriteString(it.Name)
		if pt == PathDataPattern && it.Kind == List {
			// lys_getnext from the start while the nodes are keys: the keys come first
			for k := range GetNext(it, nil, 0) {
				if !k.IsKey() {
					break
				}
				b.WriteString("[" + k.Name + "='%s']")
			}
		}
		segs = append(segs, b.String())
	}
	if len(segs) == 0 {
		return "/"
	}
	var b strings.Builder
	for i := len(segs) - 1; i >= 0; i-- {
		b.WriteString(segs[i])
	}
	return b.String()
}

// DFS is lysc_tree_dfs_full: n, the subtrees of its actions and notifications, then the same
// for each of its children, depth first. The walk is iterative: depth is bounded by the
// compile budget, not by the Go stack.
func (n *Node) DFS() iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		type frame struct {
			n    *Node
			full bool // also the actions and notifications (lysc_tree_dfs_full); plain inside them
		}
		stack := []frame{{n, true}}
		for len(stack) > 0 {
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !yield(f.n) {
				return
			}
			for i := len(f.n.Children) - 1; i >= 0; i-- {
				stack = append(stack, frame{f.n.Children[i], f.full})
			}
			if !f.full {
				continue
			}
			for i := len(f.n.Notifs) - 1; i >= 0; i-- {
				stack = append(stack, frame{f.n.Notifs[i], false})
			}
			for i := len(f.n.Actions) - 1; i >= 0; i-- {
				stack = append(stack, frame{f.n.Actions[i], false})
			}
		}
	}
}

// DFS is lysc_module_dfs_full: Node.DFS of every top-level node (data, rpcs, notifications).
func (m *Module) DFS() iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		for _, root := range m.Top {
			for n := range root.DFS() {
				if !yield(n) {
					return
				}
			}
		}
	}
}

// OwnerModule is lysc_owner_module: the module of n's top-level ancestor (n's own for a
// top-level node), the module whose data tree n belongs to.
func (n *Node) OwnerModule() *Module {
	if n == nil {
		return nil
	}
	for n.Parent != nil {
		n = n.Parent
	}
	return n.Module
}
