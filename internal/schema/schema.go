// SPDX-License-Identifier: BSD-3-Clause

// Package schema holds a compiled YANG schema: the Go counterpart of libyang's lysc_* structures
// (src/tree_schema.h). It is plain data filled by internal/compile and read by internal/types,
// internal/xpath and the data tree; besides small lookups it has no behaviour. A Set is immutable
// once compiled, so concurrent readers need no locking. Fields are exported for compile and the
// internal readers only; the public yang package wraps them in read-only handles.
package schema

import "iter"

// Set is every module of one context, in load order (libyang ly_ctx module list).
type Set struct {
	Modules []*Module
}

// Module returns the module with the name and revision; revision "" means the implemented
// module, or the newest revision when none is implemented.
func (s *Set) Module(name, revision string) *Module {
	var best *Module
	for _, m := range s.Modules {
		if m.Name != name {
			continue
		}
		if revision != "" {
			if m.Revision == revision {
				return m
			}
			continue
		}
		if m.Implemented {
			return m
		}
		if best == nil || m.Revision > best.Revision {
			best = m
		}
	}
	return best
}

// All yields every module with the name, import-only ones included, in load order (several
// revisions of one module may coexist; at most one is implemented).
func (s *Set) All(name string) iter.Seq[*Module] {
	return func(yield func(*Module) bool) {
		for _, m := range s.Modules {
			if m.Name == name && !yield(m) {
				return
			}
		}
	}
}

// Implemented returns the implemented module with the name, nil if there is none.
func (s *Set) Implemented(name string) *Module {
	for _, m := range s.Modules {
		if m.Implemented && m.Name == name {
			return m
		}
	}
	return nil
}

// ByNamespace returns the implemented module with the namespace, nil if there is none.
func (s *Set) ByNamespace(ns string) *Module {
	for _, m := range s.Modules {
		if m.Implemented && m.Namespace == ns {
			return m
		}
	}
	return nil
}

// ByPrefix returns the first implemented module whose own prefix is prefix. Prefixes are not
// unique across modules; schema text resolves them through Module.Import instead.
func (s *Set) ByPrefix(prefix string) *Module {
	for _, m := range s.Modules {
		if m.Implemented && m.Prefix == prefix {
			return m
		}
	}
	return nil
}

// Module is a compiled module (lys_module + lysc_module).
type Module struct {
	Name, Revision, Namespace, Prefix string
	Version                           uint8 // Version1 (also when yang-version is absent) or Version11
	Implemented                       bool
	Imports                           []Import
	Submodules                        []Submodule  // included submodules (lys_compile_submodules), in include order
	Extensions                        []*Extension // extension definitions of the module and its submodules (lysc_ext)
	Features                          []*Feature
	Identities                        []*Identity
	// Top holds the top-level data nodes, then the rpcs, then the notifications, each group in
	// compile order (libyang lysc_module data, rpcs, notifs). Nodes that augments of this module
	// add to other modules live in their targets, never here.
	Top  []*Node
	Exts []*ExtInstance // extension instances of the module statement
}

// Extension is a compiled extension definition (lysc_ext).
type Extension struct {
	Name, ArgName string
	Module        *Module
	Exts          []*ExtInstance // extension instances written inside the definition
}

// Module.Version values (libyang LYS_VERSION_1_0, LYS_VERSION_1_1).
const (
	Version1  uint8 = 1 // YANG 1.0 (RFC 6020)
	Version11 uint8 = 2 // YANG 1.1 (RFC 7950)
)

// Submodule is an included submodule; its content is compiled into the main module.
type Submodule struct {
	Name, Revision string
}

// Import is one import statement resolved to its module.
type Import struct {
	Prefix string
	Module *Module
}

// Import resolves a prefix used in this module's text: its own prefix or an import prefix.
func (m *Module) Import(prefix string) *Module {
	if prefix == m.Prefix {
		return m
	}
	for _, im := range m.Imports {
		if im.Prefix == prefix {
			return im.Module
		}
	}
	return nil
}

// Identity returns the module's identity with the name.
func (m *Module) Identity(name string) *Identity {
	for _, id := range m.Identities {
		if id.Name == name {
			return id
		}
	}
	return nil
}

// Feature returns the module's feature with the name.
func (m *Module) Feature(name string) *Feature {
	for _, f := range m.Features {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// Child returns the top-level data node (or rpc/notification) of module mod with the name,
// looking through choice and case like libyang lys_find_child. mod nil means m.
func (m *Module) Child(mod *Module, name string) *Node {
	if mod == nil {
		mod = m
	}
	return findChild(m.Top, mod, name)
}

// Status is the YANG status statement.
type Status uint8

// Status values.
const (
	Current Status = iota
	Deprecated
	Obsolete
)

// Feature is a compiled feature with its enabled state.
type Feature struct {
	Name    string
	Module  *Module
	Enabled bool
	Status  Status
}

// Identity is a compiled identity (lysc_ident).
type Identity struct {
	Name     string
	Module   *Module
	Derived  []*Identity // identities naming this one as a base directly
	Status   Status
	Disabled bool // an if-feature of the identity is false
}

// Kind is a schema node type (libyang LYS_* nodetype).
type Kind uint16

// Node kinds.
const (
	Container Kind = iota + 1
	Choice
	Case
	Leaf
	LeafList
	List
	AnyXML
	AnyData
	RPC
	Action
	Input
	Output
	Notification
)

// Node is a compiled schema node (lysc_node and its subtypes).
type Node struct {
	Kind   Kind
	Name   string
	Module *Module // module whose namespace the node is in (the augmenting module for augments)
	Parent *Node
	// Children are the data children (for a choice its cases, for an rpc/action its input and
	// output). Actions and notifications defined in a container or list are kept apart in
	// Actions and Notifs (libyang lysc_node_container.actions/notifs), so Child and XPath never
	// see them; their Parent is still this node.
	Children []*Node
	Actions  []*Node
	Notifs   []*Node

	Config, Mandatory, Presence bool
	UserOrdered                 bool           // ordered-by user
	Keys                        []*Node        // list keys in key order; nil for a keyless list
	Uniques                     [][]*Node      // list: the leaves of each unique statement, in statement order
	Min, Max                    uint32         // min-elements, max-elements; Max 0 = unbounded
	Default                     []DefaultValue // leaf, leaf-list
	DefaultCase                 *Node          // choice: the default case, nil if none
	Type                        *Type          // leaf, leaf-list
	Units                       string         // leaf, leaf-list: own units, else inherited from the typedef chain
	Musts                       []*Must
	Whens                       []*When
	Status                      Status
	Exts                        []*ExtInstance
}

// Keyless reports whether n is a list without keys (libyang LYS_KEYLESS).
func (n *Node) Keyless() bool { return n.Kind == List && n.Keys == nil }

// Child returns the data child of module mod with the name, looking through choice and case
// like libyang lys_find_child. mod nil means n.Module.
func (n *Node) Child(mod *Module, name string) *Node {
	if mod == nil {
		mod = n.Module
	}
	return findChild(n.Children, mod, name)
}

// DefaultValue is a default statement as written: the lexical text plus the prefixes in scope
// where it was written. Canonical values are derived by internal/types at use time, because a
// union default may resolve to a different member depending on data (libyang keeps the
// original too, schema_compile.c lys_compile_unres_dflt).
type DefaultValue struct {
	Lex string
	NS  NSCtx
}

// IsKey reports whether n is a key of its parent list.
func (n *Node) IsKey() bool {
	if n.Parent == nil || n.Parent.Kind != List {
		return false
	}
	for _, k := range n.Parent.Keys {
		if k == n {
			return true
		}
	}
	return false
}

// InOutput reports whether n is inside an rpc/action output (libyang LYS_IS_OUTPUT).
func (n *Node) InOutput() bool {
	for p := n; p != nil; p = p.Parent {
		if p.Kind == Output {
			return true
		}
	}
	return false
}

func findChild(nodes []*Node, mod *Module, name string) *Node {
	for _, c := range nodes {
		if c.Kind == Choice || c.Kind == Case {
			if r := findChild(c.Children, mod, name); r != nil {
				return r
			}
			continue
		}
		if c.Name == name && c.Module == mod {
			return c
		}
	}
	return nil
}

// BaseType is a YANG built-in type, in libyang LY_DATA_TYPE order (range checks rely on it).
type BaseType uint8

// Built-in types.
const (
	Unknown BaseType = iota
	Binary
	Uint8
	Uint16
	Uint32
	Uint64
	String
	Bits
	Bool
	Dec64
	Empty
	Enumeration
	IdentityRef
	InstanceID
	Leafref
	Union
	Int8
	Int16
	Int32
	Int64
)

var baseNames = [...]string{"unknown", "binary", "uint8", "uint16", "uint32", "uint64", "string", "bits",
	"boolean", "decimal64", "empty", "enumeration", "identityref", "instance-identifier", "leafref", "union",
	"int8", "int16", "int32", "int64"}

// String returns the YANG name of the type (libyang lys_datatype2str).
func (b BaseType) String() string {
	if int(b) < len(baseNames) {
		return baseNames[b]
	}
	return "unknown"
}

// Type is a compiled type (lysc_type and its subtypes); only the fields of its Base are set.
//
// Compiled types are shared, and pointer identity is meaningful (Realtype, union members): a
// typedef's compiled type is cached and reused by its users, a typedef that adds nothing reuses
// its base's type, and a leaf whose type adds nothing shares the typedef's type. A Type is
// never written after compile returns.
type Type struct {
	Base BaseType
	// Typedef is libyang lysc_type.name: the name of the nearest typedef compiled into this type,
	// "" for a built-in used directly. A typedef that adds nothing (no restriction, no extension,
	// not a leafref) reuses its base's type, name included, so `typedef my-addr { type
	// inet:ipv4-address; }` yields Typedef "ipv4-address"; a leaf type with own restrictions is
	// named after the nearest typedef. TypedefModule is the module defining that typedef. From is
	// libyang's base: the compiled type this one was derived from, nil when derived from a
	// built-in. Type-specific handlers such as ietf-inet-types:ipv4-address are found through this
	// chain (the same answer as libyang's plugin inheritance only because compile keeps the reused
	// pointers); some handlers key on the name itself, e.g. host bits are zeroed only for the
	// ipv4-prefix/ipv6-prefix typedefs, not ones derived from them.
	Typedef       string
	TypedefModule *Module
	From          *Type

	Range      *Range // integers, decimal64
	Length     *Range // string, binary
	Patterns   []*Pattern
	FracDigits uint8
	Enums      []*Enum
	Bits       []*Bit // ordered by position
	Bases      []*Identity

	// Leafref: Path is the path source, Prefixes resolves its prefixes, PathCompiled holds what
	// compile parsed it into. Prefixes[""] is the module that *instantiates* the leafref type
	// (libyang ctx->cur_mod), not the one where the path is written, unlike Must/When.Ctx[""].
	// Realtype is the first non-leafref type of the target chain: the same pointer as the
	// target leaf's Type when that is not a leafref.
	Path            string
	Prefixes        NSCtx
	PathCompiled    any
	Realtype        *Type
	RequireInstance bool // leafref, instance-identifier

	// Union holds the member types with nested unions flattened into this list (libyang
	// lys_compile_type_union does it): member indexes and the union error text depend on it.
	// A union typedef used unchanged shares its member pointers with its users.
	Union []*Type

	Exts []*ExtInstance // instances on the type statement and on its typedef chain
}

// Range is a compiled range or length restriction. Parts are sorted and disjoint; signed bases
// (int*, decimal64 scaled by 10^fraction-digits) use Min/Max, unsigned ones (uint*, string and
// binary length) use MinU/MaxU, mirroring libyang's lysc_range_part union.
type Range struct {
	Parts       []RangePart
	Msg, AppTag string // error-message, error-app-tag
}

// RangePart is one interval of a Range.
type RangePart struct {
	Min, Max   int64
	MinU, MaxU uint64
}

// Pattern is a pattern restriction. Compiled is set by compile to the internal/xsdre program.
type Pattern struct {
	Expr        string
	Invert      bool
	Msg, AppTag string
	Compiled    any
}

// Enum is one enumeration item.
type Enum struct {
	Name   string
	Value  int32
	Status Status
	// Disabled is set by compile when an if-feature of the item is false (libyang LYS_DISABLED);
	// such items are removed at the end of compilation and never seen by readers.
	Disabled bool
}

// Bit is one bits item. A value's bitmap has Position/8+1 bytes of the highest position, so
// compile must bound positions by a resource budget (YANG allows up to 4294967295).
type Bit struct {
	Name     string
	Position uint32
	Status   Status
	Disabled bool // as Enum.Disabled
}

// NSCtx resolves the prefixes of an expression written in schema text (libyang lysc_prefix
// array). The key "" is the module of unprefixed names: the defining module for must and when,
// the instantiating module for a leafref path (see Type.Prefixes).
type NSCtx map[string]*Module

// Resolve returns the module bound to prefix.
func (c NSCtx) Resolve(prefix string) *Module { return c[prefix] }

// Must is a must restriction. Compiled holds the compiled XPath expression set by compile.
type Must struct {
	Src         string
	Msg, AppTag string
	Ctx         NSCtx
	Compiled    any
}

// When is a when condition. ContextNode is the node it is evaluated on (libyang: the parent for
// conditions on augment/uses/case/choice targets).
type When struct {
	Src         string
	Ctx         NSCtx
	ContextNode *Node
	Status      Status
	Compiled    any
}

// ExtInstance is a compiled extension instance (lysc_ext_instance). An instance whose extension
// has no ported plugin stays generic: its substatements are not interpreted.
type ExtInstance struct {
	Def      *Module // module defining the extension
	Name     string  // extension name in Def
	Argument string
	Exts     []*ExtInstance // nested instances
	Type     *Type          // ietf-yang-metadata annotation: the compiled type of the metadata value
}
