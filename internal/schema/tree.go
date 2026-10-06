// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_schema.c (lys_getnext, lys_find_child, lysc_path) and
// src/tree_schema_common.c (lysc_data_node) (BSD-3-Clause, © CESNET).

package schema

import (
	"iter"
	"strings"
)

// GetNextOpt are the lys_getnext options (LYS_GETNEXT_*).
type GetNextOpt uint8

// GetNext options.
const (
	GetNextWithChoice GetNextOpt = 1 << iota // return choices instead of entering them
	GetNextNoChoice                          // skip choices
	GetNextWithCase                          // return cases instead of entering them
	GetNextOutput                            // enter an operation's output instead of its input
)

// GetNext yields what repeated lys_getnext calls return under parent: its data children, entering
// cases, choices, input and output as opts say, then its actions, then its notifications. A nil
// parent means the top-level nodes top of a module (data, rpcs, notifications, as Module.Top).
// Nodes of extension instances are not yielded.
func GetNext(parent *Node, top []*Node, opts GetNextOpt) iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		var walk func([]*Node) bool
		walk = func(list []*Node) bool {
			for _, n := range list {
				var ok bool
				switch n.Kind {
				case Case:
					ok = opts&GetNextWithCase != 0 && yield(n) || opts&GetNextWithCase == 0 && walk(n.Children)
				case Choice:
					switch {
					case opts&GetNextWithChoice != 0:
						ok = yield(n)
					case opts&GetNextNoChoice != 0:
						ok = true
					default:
						ok = walk(n.Children)
					}
				case Input:
					ok = opts&GetNextOutput != 0 || walk(n.Children)
				case Output:
					ok = opts&GetNextOutput == 0 || walk(n.Children)
				default:
					ok = yield(n)
				}
				if !ok {
					return false
				}
			}
			return true
		}
		if parent == nil {
			walk(top)
			return
		}
		_ = walk(parent.Children) && walk(parent.Actions) && walk(parent.Notifs)
	}
}

// FindChild is lys_find_child: the first node GetNext yields that is named name in module mod;
// nil when mod is not implemented.
func FindChild(parent *Node, top []*Node, mod *Module, name string, opts GetNextOpt) *Node {
	if mod == nil || !mod.Implemented {
		return nil
	}
	for n := range GetNext(parent, top, opts) {
		if n.Module == mod && n.Name == name {
			return n
		}
	}
	return nil
}

// DataNode is lysc_data_node: n or its nearest ancestor that is not a choice, case, input or
// output (nil when there is none).
func DataNode(n *Node) *Node {
	for n != nil && (n.Kind == Choice || n.Kind == Case || n.Kind == Input || n.Kind == Output) {
		n = n.Parent
	}
	return n
}

// DataParent is lysc_data_parent: the nearest ancestor that is a data node, rpc, action or
// notification.
func (n *Node) DataParent() *Node { return DataNode(n.Parent) }

// LogPath is lysc_path(n, LYSC_PATH_LOG): every node, the module name on a module change.
func (n *Node) LogPath() string {
	var segs []string
	for it := n; it != nil; it = it.Parent {
		if it.Parent == nil || it.Parent.Module != it.Module {
			segs = append(segs, "/"+it.Module.Name+":"+it.Name)
		} else {
			segs = append(segs, "/"+it.Name)
		}
	}
	var b strings.Builder
	for i := len(segs) - 1; i >= 0; i-- {
		b.WriteString(segs[i])
	}
	return b.String()
}
