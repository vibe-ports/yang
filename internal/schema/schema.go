// SPDX-License-Identifier: BSD-3-Clause

// Package schema holds a compiled YANG schema: the Go counterpart of libyang's lysc_* structures
// (src/tree_schema.h). It is plain data filled by internal/compile and read by internal/types,
// internal/xpath and the data tree; besides small lookups it has no behaviour. A Set is immutable
// once compiled, so concurrent readers need no locking. Fields are exported for compile and the
// internal readers only; the public yang package wraps them in read-only handles.
package schema

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
	Implemented                       bool
	Imports                           []Import
	Features                          []*Feature
	Identities                        []*Identity
	Top                               []*Node // top-level data nodes, rpcs and notifications in schema order
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
	Kind     Kind
	Name     string
	Module   *Module // module whose namespace the node is in (the augmenting module for augments)
	Parent   *Node
	Children []*Node // for choice: its cases; for rpc/action: input and output

	Config, Mandatory, Presence bool
	UserOrdered                 bool           // ordered-by user
	Keys                        []*Node        // list keys in key order
	Min, Max                    uint32         // min-elements, max-elements; Max 0 = unbounded
	Default                     []DefaultValue // leaf, leaf-list; a choice keeps its default case in Children order
	Type                        *Type          // leaf, leaf-list
	Musts                       []*Must
	Whens                       []*When
	Status                      Status
}

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
type Type struct {
	Base BaseType
	// Typedef is the name of the typedef this type was compiled from ("" for a built-in used
	// directly); TypedefModule is the module defining it; From is the compiled type of that
	// typedef's own type, nil when the typedef's type is a built-in. The chain selects
	// type-specific handlers such as ietf-inet-types:ipv4-address.
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
	// compile parsed it into, Realtype is the first non-leafref type in the chain.
	Path            string
	Prefixes        NSCtx
	PathCompiled    any
	Realtype        *Type
	RequireInstance bool // leafref, instance-identifier

	Union []*Type
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
}

// Bit is one bits item.
type Bit struct {
	Name     string
	Position uint32
	Status   Status
}

// NSCtx resolves the prefixes of an expression written in schema text (libyang lysc_prefix
// array); the key "" is the module the expression is defined in.
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
