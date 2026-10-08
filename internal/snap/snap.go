// SPDX-License-Identifier: BSD-3-Clause

// Package snap holds the read-only handles of a compiled schema snapshot (design 06 §4, design 07
// §0.4). Package yang aliases them; they have no exported fields and return values and iterators,
// never internal slices or writable structs. A Schema is immutable: the compiler publishes a deep
// copy per Load, so handles stay valid and consistent while the context loads more modules.
// Handles are fresh wrappers per call: compare what they return (names, paths), never the
// handles themselves with ==.
package snap

import (
	"fmt"
	"iter"
	"strings"

	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// Schema is an immutable snapshot of every module of a context.
type Schema struct{ s *schema.Set }

// New wraps a compiled set that nobody writes any more.
func New(s *schema.Set) *Schema { return &Schema{s} }

// Set is the compiled set of x, for the internal readers (data validation).
func Set(x *Schema) *schema.Set { return x.s }

// Modules yields every module, import-only ones included, in context order.
func (x *Schema) Modules() iter.Seq[*Module] {
	return func(yield func(*Module) bool) {
		for _, m := range x.s.Modules {
			if !yield(&Module{m}) {
				return
			}
		}
	}
}

// Module returns the module with the name and revision ("" = the implemented one, else the
// newest), nil if there is none.
func (x *Schema) Module(name, revision string) *Module { return wrapMod(x.s.Module(name, revision)) }

// Implemented returns the implemented module with the name, nil if there is none.
func (x *Schema) Implemented(name string) *Module { return wrapMod(x.s.Implemented(name)) }

// FindSchema returns the schema node of a data path "/mod:a/b/mod2:c" (prefixes are module
// names, the first one mandatory; choices and cases are looked through, rpc input is searched
// before output).
func (x *Schema) FindSchema(path string) (*Node, error) {
	if !strings.HasPrefix(path, "/") || len(path) < 2 {
		return nil, fmt.Errorf("schema path %q is not absolute", path)
	}
	var n *schema.Node
	var mod *schema.Module
	for _, seg := range strings.Split(path[1:], "/") {
		name := seg
		if i := strings.IndexByte(seg, ':'); i >= 0 {
			if mod = x.s.Implemented(seg[:i]); mod == nil {
				return nil, fmt.Errorf("schema path %q: module %q not implemented", path, seg[:i])
			}
			name = seg[i+1:]
		} else if mod == nil {
			return nil, fmt.Errorf("schema path %q: first node without a module name", path)
		}
		var top []*schema.Node
		if n == nil {
			top = mod.Top
		}
		c := schema.FindChild(n, top, mod, name, 0)
		if c == nil && n != nil && (n.Kind == schema.RPC || n.Kind == schema.Action) {
			c = schema.FindChild(n, nil, mod, name, schema.GetNextOutput)
		}
		if c == nil {
			return nil, fmt.Errorf("schema path %q: %q not found", path, seg)
		}
		n = c
	}
	return &Node{n}, nil
}

func wrapMod(m *schema.Module) *Module {
	if m == nil {
		return nil
	}
	return &Module{m}
}

// ModuleOf is the handle of a compiled module, nil for nil (the data tree's Meta.Module).
func ModuleOf(m *schema.Module) *Module { return wrapMod(m) }

// NodeOf is the handle of a compiled schema node, nil for nil (the data tree's Node.Schema).
func NodeOf(n *schema.Node) *Node { return wrapNode(n) }

func wrapNode(n *schema.Node) *Node {
	if n == nil {
		return nil
	}
	return &Node{n}
}

func nodes(ns []*schema.Node) iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		for _, n := range ns {
			if !yield(&Node{n}) {
				return
			}
		}
	}
}

// Status is the YANG status statement.
type Status uint8

// Status values.
const (
	Current Status = iota
	Deprecated
	Obsolete
)

func (s Status) String() string { return [...]string{"current", "deprecated", "obsolete"}[s] }

// --- Module ---

// Module is a module of a snapshot.
type Module struct{ m *schema.Module }

// Name is the module name.
func (m *Module) Name() string { return m.m.Name }

// Revision is the newest revision date, "" if the module has none.
func (m *Module) Revision() string { return m.m.Revision }

// Namespace is the module's namespace URI.
func (m *Module) Namespace() string { return m.m.Namespace }

// Prefix is the module's own prefix.
func (m *Module) Prefix() string { return m.m.Prefix }

// Implemented reports whether the module is implemented (not import-only).
func (m *Module) Implemented() bool { return m.m.Implemented }

// FeatureEnabled reports whether the module (or one of its submodules) defines the enabled
// feature name.
func (m *Module) FeatureEnabled(name string) bool {
	f := m.m.Feature(name)
	return f != nil && f.Enabled
}

// Features yields every feature with its enabled flag, module first, then per submodule.
func (m *Module) Features() iter.Seq2[string, bool] {
	return func(yield func(string, bool) bool) {
		for _, f := range m.m.Features {
			if !yield(f.Name, f.Enabled) {
				return
			}
		}
	}
}

// Identities yields the module's identities (of its submodules too).
func (m *Module) Identities() iter.Seq[*Identity] {
	return func(yield func(*Identity) bool) {
		for _, id := range m.m.Identities {
			if !yield(&Identity{id}) {
				return
			}
		}
	}
}

// Extensions yields the extension instances of the module statement.
func (m *Module) Extensions() iter.Seq[*Extension] { return exts(m.m.Exts) }

// Top yields the top-level data nodes, then the rpcs, then the notifications.
func (m *Module) Top() iter.Seq[*Node] { return nodes(m.m.Top) }

// --- Identity ---

// Identity is a compiled identity.
type Identity struct{ i *schema.Identity }

// Name is the identity name.
func (i *Identity) Name() string { return i.i.Name }

// Module is the module defining the identity.
func (i *Identity) Module() *Module { return &Module{i.i.Module} }

// Derived yields the identities naming this one as a base directly.
func (i *Identity) Derived() iter.Seq[*Identity] {
	return func(yield func(*Identity) bool) {
		for _, d := range i.i.Derived {
			if !yield(&Identity{d}) {
				return
			}
		}
	}
}

// --- Extension ---

// Extension is a compiled extension instance.
type Extension struct{ e *schema.ExtInstance }

func exts(es []*schema.ExtInstance) iter.Seq[*Extension] {
	return func(yield func(*Extension) bool) {
		for _, e := range es {
			if !yield(&Extension{e}) {
				return
			}
		}
	}
}

// Module is the name of the module defining the extension.
func (e *Extension) Module() string { return e.e.Def.Name }

// Name is the extension name.
func (e *Extension) Name() string { return e.e.Name }

// Argument is the instance's argument, "" if it has none.
func (e *Extension) Argument() string { return e.e.Argument }

// Tree yields the top-level nodes of the schema tree the instance holds: the container of a
// structure (also when it is empty), the nodes of a yang-data; nothing for other extensions.
func (e *Extension) Tree() iter.Seq[*Node] {
	if e.e.Root != nil {
		return nodes([]*schema.Node{e.e.Root})
	}
	return nodes(e.e.Nodes)
}

// --- Node ---

// Kind is a schema node kind.
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

// String is the YANG keyword of the kind.
func (k Kind) String() string {
	return [...]string{"", "container", "choice", "case", "leaf", "leaf-list", "list", "anyxml", "anydata", "rpc",
		"action", "input", "output", "notification"}[k]
}

// Node is a compiled schema node.
type Node struct{ n *schema.Node }

// Kind is the node kind.
func (n *Node) Kind() Kind { return Kind(n.n.Kind) }

// Name is the node name ("input"/"output" for those).
func (n *Node) Name() string { return n.n.Name }

// Module is the module whose namespace the node is in.
func (n *Node) Module() *Module { return &Module{n.n.Module} }

// Parent is the parent node (choice, case, input and output included), nil at the top level.
func (n *Node) Parent() *Node { return wrapNode(n.n.Parent) }

// Children yields the data children (a choice's cases, an rpc's input and output).
func (n *Node) Children() iter.Seq[*Node] { return nodes(n.n.Children) }

// Actions yields the actions defined in a container or list.
func (n *Node) Actions() iter.Seq[*Node] { return nodes(n.n.Actions) }

// Notifications yields the notifications defined in a container or list.
func (n *Node) Notifications() iter.Seq[*Node] { return nodes(n.n.Notifs) }

// Child returns the data child of module (name; "" = n's module) with the name, looking through
// choice and case, nil if there is none.
func (n *Node) Child(module, name string) *Node {
	mod := n.n.Module
	for c := range schema.GetNext(n.n, nil, 0) {
		if c.Name == name && (module == "" && c.Module == mod || c.Module.Name == module) {
			return &Node{c}
		}
	}
	return nil
}

// Path is the schema path libyang logs (lysc_path LYSC_PATH_LOG).
func (n *Node) Path() string { return n.n.LogPath() }

// Config reports config true; false also for nodes inside an rpc, action or notification, and
// for nodes without a config flag (see HasConfig).
func (n *Node) Config() bool { return n.n.Config }

// HasConfig is false for a node of an extension instance's tree compiled without a config flag
// (yang-data, structure): its Config is false, but it is not state data either.
func (n *Node) HasConfig() bool { return !n.n.ConfigUnset }

// Mandatory is the mandatory flag (min-elements > 0, propagated to non-presence containers).
func (n *Node) Mandatory() bool { return n.n.Mandatory }

// Presence reports a presence container.
func (n *Node) Presence() bool { return n.n.Presence }

// UserOrdered reports ordered-by user (lists, leaf-lists).
func (n *Node) UserOrdered() bool { return n.n.UserOrdered }

// Keys yields the keys of a list in key order.
func (n *Node) Keys() iter.Seq[*Node] { return nodes(n.n.Keys) }

// IsKey is lysc_is_key: n is a key leaf of its list.
func (n *Node) IsKey() bool { return n.n.Kind == schema.Leaf && n.n.IsKey() }

// IsDupInstList is lysc_is_dup_inst_list: a keyless list or a state leaf-list, whose data
// instances may be equal (they are not identified by keys or values).
func (n *Node) IsDupInstList() bool { return n.n.IsDupInstList() }

// DefaultSet is LYS_SET_DFLT: a leaf or leaf-list whose default comes from its own (or a refined)
// default statement rather than its type, or the default case of a choice. Defaults yields the
// values either way.
func (n *Node) DefaultSet() bool { return n.n.DefaultSet }

// MinElements is min-elements of a list or leaf-list.
func (n *Node) MinElements() uint32 { return n.n.Min }

// MaxElements is max-elements of a list or leaf-list; bounded false for unbounded.
func (n *Node) MaxElements() (limit uint32, bounded bool) { return n.n.Max, n.n.Max != 0 }

// Defaults yields the default values of a leaf or leaf-list in canonical form, the text as
// written when it cannot be stored without data (lyd_value_validate_dflt).
func (n *Node) Defaults() iter.Seq[string] {
	return func(yield func(string) bool) {
		for _, d := range n.n.Default {
			s := d.Lex
			if v, diag := types.Store(n.n.Type, d.Lex, types.FormatSchemaResolved, types.HintSchema, d.NS, n.n); diag == nil {
				s = v.Canonical()
			}
			if !yield(s) {
				return
			}
		}
	}
}

// DefaultCase is the default case of a choice, nil if none or if it was removed by an
// if-feature.
func (n *Node) DefaultCase() *Node { return wrapNode(n.n.DefaultCase) }

// DefaultCaseName is the name the choice's default statement names, also when that case was
// removed by an if-feature (DefaultCase is nil then; libyang still reports the name, D-0070).
func (n *Node) DefaultCaseName() string { return n.n.DefaultCaseName }

// Type is the type of a leaf or leaf-list, nil for other nodes.
func (n *Node) Type() *Type {
	if n.n.Type == nil {
		return nil
	}
	return &Type{n.n.Type}
}

// Units is the units of a leaf or leaf-list (own or from the typedef chain); "" also when there
// is no units statement.
func (n *Node) Units() string {
	if n.n.Units == nil {
		return ""
	}
	return *n.n.Units
}

// Status is the node's status.
func (n *Node) Status() Status { return Status(n.n.Status) }

// HasStatus is false only for the container of a structure extension instance written without
// a status statement (it has no status flag; Status reports current); true for every other node.
func (n *Node) HasStatus() bool { return !n.n.StatusUnset }

// Musts yields the must restrictions.
func (n *Node) Musts() iter.Seq[*Must] {
	return func(yield func(*Must) bool) {
		for _, m := range n.n.Musts {
			if !yield(&Must{m}) {
				return
			}
		}
	}
}

// Whens yields the when conditions, own first, then those inherited from uses and augments.
func (n *Node) Whens() iter.Seq[*When] {
	return func(yield func(*When) bool) {
		for _, w := range n.n.Whens {
			if !yield(&When{w}) {
				return
			}
		}
	}
}

// Extensions yields the extension instances of the node.
func (n *Node) Extensions() iter.Seq[*Extension] { return exts(n.n.Exts) }

// HasExtensionList reports whether the node has an extension-instance list at all: true also when
// every instance written on it was dropped by an extension plugin (libyang keeps the empty array,
// e.g. a misplaced NACM instance), false when none was written.
func (n *Node) HasExtensionList() bool { return n.n.Exts != nil }

// LeafrefTargets yields every leafref of the node's type (the type itself or its union members,
// in member order) with its target compiled from this node, nil when it does not resolve.
func (n *Node) LeafrefTargets() iter.Seq2[*Type, *Node] {
	return func(yield func(*Type, *Node) bool) {
		if n.n.Type == nil {
			return
		}
		ms := []*schema.Type{n.n.Type}
		if n.n.Type.Base == schema.Union {
			ms = n.n.Type.Union
		}
		for _, t := range ms {
			if t.Base != schema.Leafref {
				continue
			}
			if !yield(&Type{t}, wrapNode(target(n.n, t))) {
				return
			}
		}
	}
}

func target(n *schema.Node, t *schema.Type) *schema.Node {
	e, msg := lyxp.ParsePath(t.Path, lyxp.Opts{Begin: lyxp.BeginEither, Prefix: lyxp.PrefixOptional,
		Pred: lyxp.PredLeafref, Leafref: true, Extended: t.PathExtended})
	if msg != "" {
		return nil
	}
	p, _, err := types.CompileLeafref(n, e, t.Prefixes, n.InOutput(), t.PathExtended)
	if err != nil || len(p) == 0 {
		return nil
	}
	return p[len(p)-1].Node
}

// --- Must, When ---

// Must is a must restriction.
type Must struct{ m *schema.Must }

// Expr is the XPath expression.
func (m *Must) Expr() string { return m.m.Src }

// ErrorAppTag is the error-app-tag, "" if none.
func (m *Must) ErrorAppTag() string { return m.m.AppTag }

// ErrorMessage is the error-message, "" if none.
func (m *Must) ErrorMessage() string { return m.m.Msg }

// When is a when condition.
type When struct{ w *schema.When }

// Expr is the XPath expression.
func (w *When) Expr() string { return w.w.Src }

// ContextNode is the node the condition is evaluated on, nil for the root.
func (w *When) ContextNode() *Node { return wrapNode(w.w.ContextNode) }

// Module is the name of the module the condition is written in.
func (w *When) Module() string {
	if m := w.w.Ctx[""]; m != nil {
		return m.Name
	}
	return ""
}

// --- Type ---

// Type is a compiled type.
type Type struct{ t *schema.Type }

// Base is the built-in type name ("string", "leafref", ...).
func (t *Type) Base() string { return t.t.Base.String() }

// Typedef is the name of the nearest typedef compiled into this type, "" for a built-in used
// directly.
func (t *Type) Typedef() string { return t.t.Typedef }

// Members yields the member types of a union (nested unions flattened).
func (t *Type) Members() iter.Seq[*Type] {
	return func(yield func(*Type) bool) {
		for _, m := range t.t.Union {
			if !yield(&Type{m}) {
				return
			}
		}
	}
}

// LeafrefPath is the path of a leafref.
func (t *Type) LeafrefPath() string { return t.t.Path }

// RequireInstance is require-instance of a leafref or instance-identifier.
func (t *Type) RequireInstance() bool { return t.t.RequireInstance }

// FractionDigits is fraction-digits of a decimal64.
func (t *Type) FractionDigits() int { return int(t.t.FracDigits) }

// Enums yields the enabled enum names with their values.
func (t *Type) Enums() iter.Seq2[string, int32] {
	return func(yield func(string, int32) bool) {
		for _, e := range t.t.Enums {
			if !e.Disabled && !yield(e.Name, e.Value) {
				return
			}
		}
	}
}

// Bits yields the enabled bit names with their positions, by position.
func (t *Type) Bits() iter.Seq2[string, uint32] {
	return func(yield func(string, uint32) bool) {
		for _, b := range t.t.Bits {
			if !b.Disabled && !yield(b.Name, b.Position) {
				return
			}
		}
	}
}

// Bases yields the bases of an identityref.
func (t *Type) Bases() iter.Seq[*Identity] {
	return func(yield func(*Identity) bool) {
		for _, b := range t.t.Bases {
			if !yield(&Identity{b}) {
				return
			}
		}
	}
}

// Patterns yields the pattern restrictions with their invert-match flag.
func (t *Type) Patterns() iter.Seq2[string, bool] {
	return func(yield func(string, bool) bool) {
		for _, p := range t.t.Patterns {
			if !yield(p.Expr, p.Invert) {
				return
			}
		}
	}
}

// Range yields the intervals of a range restriction (integers, decimal64) as their lower and
// upper bound in canonical text; ok false when the type has none.
func (t *Type) Range() (parts iter.Seq2[string, string], ok bool) { return t.bounds(t.t.Range) }

// Length yields the intervals of a length restriction (string, binary); ok false when none.
func (t *Type) Length() (parts iter.Seq2[string, string], ok bool) { return t.bounds(t.t.Length) }

func (t *Type) bounds(r *schema.Range) (iter.Seq2[string, string], bool) {
	if r == nil {
		return nil, false
	}
	return func(yield func(string, string) bool) {
		for _, p := range r.Parts {
			if !yield(bound(t.t, p.Min, p.MinU), bound(t.t, p.Max, p.MaxU)) {
				return
			}
		}
	}, true
}

// bound formats a range bound: decimal64 scaled by its fraction digits, unsigned for the
// unsigned integers, string and binary lengths.
func bound(t *schema.Type, s int64, u uint64) string {
	switch {
	case t.Base == schema.Dec64:
		a := uint64(s) //nolint:gosec // magnitude below
		sign := ""
		if s < 0 {
			a, sign = -a, "-"
		}
		p := uint64(1)
		for range t.FracDigits {
			p *= 10
		}
		return fmt.Sprintf("%s%d.%0*d", sign, a/p, int(t.FracDigits), a%p)
	case t.Base < schema.Dec64:
		return fmt.Sprint(u)
	}
	return fmt.Sprint(s)
}
