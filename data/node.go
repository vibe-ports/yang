// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.h, src/tree_data.c and src/tree_data_common.c
// (BSD-3-Clause, © CESNET).

package data

import (
	"iter"
	"sync"
	"sync/atomic"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// Flags are the per-node validation flags (libyang LYD_* node flags, tree_data.h:799-803).
type Flags uint8

// Node flags; the values are libyang's.
const (
	// FlagDefault marks an implicit node (added by validation) or an NP container with only
	// default descendants (LYD_DEFAULT).
	FlagDefault Flags = 0x01
	// FlagWhenTrue: all when conditions of the node were true at the last evaluation
	// (LYD_WHEN_TRUE); such a node whose when turns false is deleted silently.
	FlagWhenTrue Flags = 0x02
	// FlagNew: created or changed since the last validation (LYD_NEW).
	FlagNew Flags = 0x04
	// FlagWhenFalse: a when condition was false during a multi-error validation; the node is kept,
	// XPath treats it as absent and the final checks skip it (LYD_WHEN_FALSE, design 07 §2).
	FlagWhenFalse Flags = 0x10

	// flagDead marks a node deleted by the running when pass but not unlinked yet (internal).
	flagDead Flags = 0x80
)

// opaque is the data of a node without a schema node (lyd_node_opaq): unknown input kept by
// lenient parsing. The parsers fill it (design 07 D4).
type opaque struct {
	Name     string
	Prefix   string // as written in the input
	ModuleNS string // XML namespace, JSON module name (lyd_node_opaq.name.module_ns/module_name)
	Value    string
	// Format is how ModuleNS and the value's prefixes are given: types.FormatXML or
	// types.FormatJSON; Prefixes resolves the value's prefixes (val_prefix_data), Hints are the
	// parser's value hints.
	Format   types.Format
	Prefixes types.PrefixCtx
	Hints    types.Hints
	Attrs    []attr // generic attributes (lyd_node_opaq.attr), in order
	// set is the node's context (lyd_node_opaq.ctx): an opaque node has no schema node to tell it,
	// and it keeps its context when detached
	set *schema.Set
}

// Node is a data node (lyd_node and its subtypes). A term (leaf, leaf-list instance) holds a
// value; an inner node (container, list instance) holds children, kept in libyang's sibling order
// (schema order, sorted system-ordered instances, opaque nodes last).
type Node struct {
	schema *schema.Node // nil for an opaque node
	parent *Node
	tree   *Tree // set for top-level nodes linked into a tree
	kids   siblings
	value  types.Value
	opaq   *opaque
	flags  Flags
	meta   []*meta // metadata (lyd_meta), in order
	hkey   idxKey  // bucket of the node in its parent's children index (lyd_node.hash)
	hashed bool
	inRB   bool          // in its run's RB tree (lyds) of a system-ordered list or leaf-list
	links  *leafrefLinks // leafref links record (LY_CTX_LEAFREF_LINKING), nil: none
}

// Tree is a data tree: its top-level siblings and the schema snapshot it is built over.
type Tree struct {
	set *schema.Set
	top siblings
	// rank caches the position of schema nodes among their schema siblings (lys_getnext
	// order); lookups only read the tree otherwise, so the cache has its own lock.
	rankMu sync.Mutex
	rank   map[*schema.Node]int
	work   workCounter
}

// workCounter counts the work of a tree for tests (work, not time): comparisons made by
// insertions and, when visits is set, sibling visits. Reads of a tree count nothing unless a test
// sets visits, so concurrent readers do not contend on it (#186).
type workCounter struct {
	atomic.Int64
	visits bool // count sibling visits too (tests only; set before the calls it measures)
}

// newTree returns an empty tree over the compiled schema s (the public constructor over a
// schema snapshot is design 07 D12).
func newTree(s *schema.Set) *Tree { return &Tree{set: s} }

// Top yields the top-level nodes in order.
func (t *Tree) Top() iter.Seq[*Node] { return t.top.all() }

// newTerm returns an unlinked leaf or leaf-list instance (lyd_create_term with a stored value).
func newTerm(s *schema.Node, v types.Value) *Node { return &Node{schema: s, value: v} }

// newInner returns an unlinked container or list instance (lyd_create_inner).
func newInner(s *schema.Node) *Node { return &Node{schema: s} }

// newOpaque returns an unlinked opaque node (lyd_create_opaq).
func newOpaque(o opaque) *Node { return &Node{opaq: &o} }

// Parent returns the parent node, nil at the top level.
func (n *Node) Parent() *Node { return n.parent }

// Flags returns the validation flags.
func (n *Node) Flags() Flags { return n.flags }

// Name returns the node name (LYD_NAME).
func (n *Node) Name() string {
	if n.schema == nil {
		return n.opaq.Name
	}
	return n.schema.Name
}

// Children yields the children in order.
func (n *Node) Children() iter.Seq[*Node] { return n.kids.all() }

// isTerm reports whether n is a leaf or leaf-list instance (LYD_NODE_TERM).
func (n *Node) isTerm() bool {
	return n.schema != nil && (n.schema.Kind == schema.Leaf || n.schema.Kind == schema.LeafList)
}

// isKey reports whether n is a list key linked into its list (lysc_is_key && parent).
func (n *Node) isKey() bool { return n.schema != nil && n.schema.IsKey() && n.parent != nil }

// All yields n and its descendants in pre-order (LYD_TREE_DFS_BEGIN/END).
func (n *Node) All() iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		// explicit stack: tree depth is bounded by the input, not by the Go stack
		stack := []*Node{n}
		for len(stack) > 0 {
			c := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !yield(c) {
				return
			}
			for i := len(c.kids.opq) - 1; i >= 0; i-- {
				stack = append(stack, c.kids.opq[i])
			}
			for i := len(c.kids.list) - 1; i >= 0; i-- {
				stack = append(stack, c.kids.list[i])
			}
		}
	}
}

// siblingsOf returns the sibling list n is linked in, nil if n is not linked.
func (n *Node) siblingsOf() *siblings {
	switch {
	case n.parent != nil:
		return &n.parent.kids
	case n.tree != nil:
		return &n.tree.top
	}
	return nil
}

// metaAnnotation is lyd_get_meta_annotation: the compiled annotation named name of mod (an
// instance of the metadata plugin's extension, ietf-yang-metadata@2016-08-05 annotation), or nil.
func metaAnnotation(mod *schema.Module, name string) *schema.ExtInstance {
	if mod == nil {
		return nil
	}
	for _, e := range mod.Exts {
		if e.Def != nil && e.Def.Name == "ietf-yang-metadata" && e.Def.Revision == "2016-08-05" &&
			e.Name == "annotation" && e.Argument == name {
			return e
		}
	}
	return nil
}
