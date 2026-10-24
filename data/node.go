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
	any    *anyValue
	// payloadSet preserves LYD_CTX at a payload boundary, even when the enclosing
	// any node belongs to another snapshot. It also survives unlinking the root.
	payloadSet *schema.Set
	value      types.Value
	opaq       *opaque
	flags      Flags
	meta       []*meta // metadata (lyd_meta), in order
	hkey       idxKey  // bucket of the node in its parent's children index (lyd_node.hash)
	hashed     bool
	hash       uint32        // lyd_node.hash, independent of table membership and the current value
	keyN       int           // current leading key run
	lkeyN      int           // leading keys captured by the last native hash event
	lkeyFrozen []*Node       // historical prefix displaced by an earlier repeated key
	lkeyOK     bool          // false means no native hash event yet (stored hash zero)
	lkeyPrefix uint32        // running hash before finalization, for amortized prefix extension
	lkeyDirty  bool          // a captured value changed during validation, without a native rehash
	keySlot    int           // 1-based position in the parent's captured prefix
	inRB       bool          // in its run's RB tree (lyds) of a system-ordered list or leaf-list
	rbGroup    *rbGroup      // identity of that tree; adjacent runs can have different trees
	links      *leafrefLinks // leafref links record (LY_CTX_LEAFREF_LINKING), nil: none
}

// Tree is a data tree: its top-level siblings and the schema snapshot it is built over.
type Tree struct {
	set *schema.Set
	top siblings
	// rank caches the position of schema nodes among their schema siblings (lys_getnext
	// order); lookups only read the tree otherwise, so the cache has its own lock.
	rankMu sync.Mutex
	rank   map[*schema.Node]int
	orders map[orderKey][]*schema.Node // an operation's input or output children (opOrder)
	work   workCounter
	// dupLog is the logger of an exported Dup or DupSiblings while it runs: the metadata values
	// the target snapshot rejects are logged there (lyd_dup_meta_single_to_ctx).
	dupLog *logger
	// Diagnostics from lyd_find_meta calls during a diff merge retain their
	// emitting context even when a later wrapper reports the operation error.
	diffLogs []diffLogEntry
	// mixedDiff makes insertion follow libyang's cross-context scans from the first copied node,
	// before the destination itself contains nodes from both input contexts.
	mixedDiff bool
	// foreignSchema indexes sibling schema lineages only while a cross-context diff is running.
	// The inputs are immutable for that operation, so the cache needs no mutation invalidation.
	foreignSchema      map[foreignSchemaCacheKey]map[foreignSchemaIndexKey]int
	foreignSchemaInput map[*siblings]bool
	schemaLineage      map[*schema.Node]schemaLineageID
	lineageComponents  map[schemaLineageComponent]schemaLineageID
	nextLineageID      schemaLineageID
}

// workCounter counts the work of a tree for tests (work, not time): comparisons made by
// insertions and, when visits is set, sibling and bulk link-record visits. Reads of a tree count
// nothing unless a test sets visits, so concurrent readers do not contend on it (#186).
// The hash quota fields belong to a mutation operation; read-only probes use a
// private counter rather than changing the queried tree's quota.
type workCounter struct {
	atomic.Int64
	visits      bool                      // count sibling/link-record visits too (tests only; set before measured calls)
	shifts      bool                      // count the sibling slots moved by insertions too (tests only)
	hashUsed    uint64                    // cumulative reserved hash transitions in the current mutation
	hashLimit   uint64                    // zero takes the internal default; tests may lower it
	hashErr     error                     // the first hash budget error, propagated at the operation boundary
	hashDepth   int                       // nested mutation helpers share their enclosing operation's quota
	hashTrace   *[]uint64                 // optional charge/check thresholds for exhaustive budget tests
	hashNames   map[*schema.Node]uint32   // immutable module/name prefix, once per schema node in this tree
	indexNames  map[*schema.Node][32]byte // immutable positional-index name digest for hand-built schemas
	journal     *hashTransaction          // the running operation's undo journal (beginHashTransaction)
	lookupUsed  uint64                    // cross-context schema-index work in the current diff
	lookupLimit uint64                    // zero takes the internal default; tests may lower it
	lookupErr   error                     // first lookup budget error, propagated at the diff boundary
	lookupDepth int                       // recursive diff helpers share one lookup quota
}

// newTree returns an empty tree over the compiled schema s (the public constructor over a
// schema snapshot is design 07 D12).
func newTree(s *schema.Set) *Tree { return &Tree{set: s} }

// context is LYD_CTX(tree's first node), falling back to the constructor's
// snapshot while the tree is empty or a hand-built schema has no Owner. Diff
// apply may retain roots from several snapshots, so set alone does not
// describe a populated tree.
func (t *Tree) context() *schema.Set {
	if n := t.top.first(); n != nil {
		if set := ctxOf(n); set != nil {
			return set
		}
	}
	return t.set
}

// Top yields the top-level nodes in order.
func (t *Tree) Top() iter.Seq[*Node] { return t.top.all() }

// newTerm returns an unlinked leaf or leaf-list instance (lyd_create_term with a stored value).
func newTerm(s *schema.Node, v types.Value, counters ...*workCounter) *Node {
	n := &Node{schema: s, value: v}
	var work *workCounter
	if len(counters) != 0 {
		work = counters[0]
	}
	n.refreshHash(work)
	return n
}

// newInner returns an unlinked container or list instance (lyd_create_inner).
func newInner(s *schema.Node, counters ...*workCounter) *Node {
	n := &Node{schema: s}
	var work *workCounter
	if len(counters) != 0 {
		work = counters[0]
	}
	if s.Kind != schema.List || s.Keyless() {
		n.refreshHash(work)
	}
	return n
}

// newOpaque returns an unlinked opaque node (lyd_create_opaq).
func newOpaque(o opaque) *Node { return &Node{opaq: &o} }

// Parent returns the parent node, nil at the top level.
func (n *Node) Parent() *Node {
	if n == nil {
		return nil
	}
	return n.parent
}

// Flags returns the validation flags.
func (n *Node) Flags() Flags {
	if n == nil {
		return 0
	}
	return n.flags
}

// Name returns the node name (LYD_NAME).
func (n *Node) Name() string {
	if n == nil {
		return ""
	}
	if n.schema == nil {
		return n.opaq.Name
	}
	return n.schema.Name
}

// Children yields ordinary children in order, excluding anydata/anyxml payloads.
func (n *Node) Children() iter.Seq[*Node] {
	if n == nil {
		return func(func(*Node) bool) {}
	}
	return n.kids.all()
}

// isTerm reports whether n is a leaf or leaf-list instance (LYD_NODE_TERM).
func (n *Node) isTerm() bool {
	return n.schema != nil && (n.schema.Kind == schema.Leaf || n.schema.Kind == schema.LeafList)
}

// isKey reports whether n is a list key linked into its list (lysc_is_key && parent).
func (n *Node) isKey() bool { return n.schema != nil && n.schema.IsKey() && n.parent != nil }

// All yields n and its ordinary descendants in pre-order (LYD_TREE_DFS_BEGIN/END),
// excluding anydata/anyxml payloads.
func (n *Node) All() iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		if n == nil {
			return
		}
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
		if n.parent.any != nil {
			return &n.parent.any.children
		}
		return &n.parent.kids
	case n.tree != nil:
		return &n.tree.top
	}
	return nil
}
