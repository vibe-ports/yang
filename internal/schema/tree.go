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

// Plugin ids of the extension plugins whose instances hold a schema tree.
const (
	PluginYangData  = "ly2 yang-data"
	PluginStructure = "ly2 structure"
)

// FindExtNode is lys_find_child_node_ext without a data parent: the node named name that an
// extension instance provides, looked up in the instances of sparent, then (sparent's instances
// providing none) in those of module mod (nil: prefix, else sparent's module). prefix is the
// module the name's prefix resolved to, nil for an unprefixed name; xpath selects the plugins' snode_xpath callback (XPath and schema
// paths), else snode (data parsing). It returns the node and its instance, or nils.
func FindExtNode(sparent *Node, mod, prefix *Module, name string, xpath bool) (*Node, *ExtInstance) {
	var exts []*ExtInstance
	if sparent != nil {
		exts = sparent.Exts
	}
	for _, e := range exts {
		if n := e.findNode(sparent, prefix, name, xpath); n != nil {
			return n, e
		}
	}
	if mod == nil { // lys_find_module: the prefix's module, else (JSON) the parent's
		mod = prefix
		if mod == nil && sparent != nil {
			mod = sparent.Module
		}
	}
	if mod == nil || !mod.Implemented {
		return nil, nil
	}
	for _, e := range mod.Exts {
		if n := e.findNode(sparent, prefix, name, xpath); n != nil {
			return n, e
		}
	}
	return nil, nil
}

// findNode is lys_ext_find_node: the plugin's snode_xpath or snode callback. Instances of other
// plugins hold no schema tree (libyang's generic fallback for a plugin without the callbacks
// cannot match: it starts after the first node and dereferences a NULL output).
func (e *ExtInstance) findNode(sparent *Node, prefix *Module, name string, xpath bool) *Node {
	if prefix != nil && prefix != e.Module {
		return nil
	}
	switch e.Plugin {
	case PluginYangData: // yangdata_snode: top-level data only, the first node when name is ""
		if !xpath && sparent != nil || len(e.Nodes) == 0 { // snode_xpath passes no parent
			return nil
		}
		if name == "" {
			return e.Nodes[0]
		}
		for _, n := range e.Nodes {
			if n.Name == name {
				return n
			}
		}
	case PluginStructure:
		if e.Root == nil {
			return nil // augment-structure: same plugin id, no tree
		}
		if !xpath { // structure_snode: the data tree starts at the top-level container
			if name != "" && name != e.Root.Name {
				return nil
			}
			return e.Root
		}
		for n := range GetNext(e.Root, nil, 0) { // structure_snode_xpath
			if n.Name == name {
				return n
			}
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
