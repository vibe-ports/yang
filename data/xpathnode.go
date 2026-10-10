// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (the data and schema accessors the evaluator reads:
// set_comp_canonize, lysc_has_when / LYD_WHEN_* in moveto, lyxp_get_root_type) and
// src/tree_data_common.c (lyd_node_module) (BSD-3-Clause, © CESNET).

package data

import (
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/snap"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
)

// xn adapts a data node to xpath.Node. It is a comparable value: two xn of one *Node are ==.
type xn struct {
	n   *Node
	set *schema.Set
}

func wrap(set *schema.Set, n *Node) xpath.Node {
	if n == nil {
		return nil
	}
	return xn{n, set}
}

func (x xn) Parent() xpath.Node { return wrap(x.set, x.n.parent) }

func (x xn) Children() []xpath.Node {
	if x.n.flags&flagDead != 0 {
		return nil // deleted by the running when pass: its subtree is gone for XPath too
	}
	out := make([]xpath.Node, 0, x.n.kids.len())
	for c := range x.n.kids.all() {
		if c.flags&flagDead == 0 {
			out = append(out, xn{c, x.set})
		}
	}
	return out
}

// LookupChild is the children hash table lookup of moveto_node_hash_child (lyd_find_sibling_val /
// lyd_find_sibling_first): the live instances of sn among the children of x whose list keys or
// leaf-list value have the canonical texts vals, all instances for nil vals. It answers only from
// one bucket and declines (ok=false: the evaluator's charged scan) everything that needs a scan:
// a parent without a table (fewer than htMinItems schema children), a bucket of several
// instances (duplicate state instances, which also need document order), and a miss while
// opaque children could be the fallback.
func (x xn) LookupChild(sn xpath.SchemaNode, vals []string) ([]xpath.Node, bool) {
	s, ok := sn.(xs)
	sib := &x.n.kids
	if !ok || sib.ht == nil || x.n.flags&flagDead != 0 {
		return nil, false
	}
	k := idxKey{s: s.s}
	if s.s.Kind == schema.LeafList && len(vals) == 1 {
		k.key = vals[0]
	} else if vals != nil {
		k.key = strings.Join(vals, "\x00") + "\x00" // hashOf of a list
	}
	b := sib.ht[k]
	live := len(b) == 1 && b[0].flags&flagDead == 0
	switch {
	case len(b) > 1, !live && len(sib.opq) > 0:
		return nil, false
	case live:
		return []xpath.Node{xn{b[0], x.set}}, true
	}
	return nil, true
}

// FirstChild is lyd_find_sibling_schema over the children of x: their first live instance of sn,
// found by the binary search of the sorted sibling list (declined for a node outside a tree or
// with dead children of sn, which the scan skips).
func (x xn) FirstChild(sn xpath.SchemaNode) (xpath.Node, bool) {
	s, ok := sn.(xs)
	t := treeOf(x.n)
	if !ok || t == nil || x.n.flags&flagDead != 0 {
		return nil, false
	}
	i := t.schemaIndex(&x.n.kids, s.s)
	if i < 0 {
		return nil, true
	}
	if c := x.n.kids.list[i]; c.flags&flagDead == 0 {
		return xn{c, x.set}, true
	}
	return nil, false
}

// OpaqueSchema is lyd_node_schema of an opaque node, for the name tests of the evaluator.
func (x xn) OpaqueSchema() (xpath.SchemaNode, bool) {
	sn := nodeSchema(x.set, x.n)
	if sn == nil {
		return nil, false
	}
	return xs{sn, x.set}, hasWhen(sn)
}

func (x xn) Name() string { return x.n.Name() }

func (x xn) Module() string {
	if m := nodeModule(x.set, x.n); m != nil {
		return m.Name
	}
	return ""
}

func (x xn) Schema() xpath.SchemaNode { return wrapSchema(x.set, x.n.schema) }

func (x xn) Value() xpath.Value {
	if x.n.opaq != nil {
		return opqVal(x.n.opaq.Value) // lyd_get_value of an opaque node: its text
	}
	if !x.n.isTerm() {
		return nil
	}
	return xval{x.n.value, x.n.schema}
}

// Meta is the node's metadata (lyd_node.meta) for the attribute axis and lang().
func (x xn) Meta() []xpath.Meta {
	if len(x.n.meta) == 0 {
		return nil
	}
	out := make([]xpath.Meta, len(x.n.meta))
	for i, m := range x.n.meta {
		out[i] = xpath.Meta{Module: m.mod.Name, Namespace: m.mod.Namespace, Name: m.name, Value: xval{m.value, nil}}
	}
	return out
}

// opqVal is the value of an opaque node: text only, no typed value behind it.
type opqVal string

func (v opqVal) String() string                 { return string(v) }
func (opqVal) Identity() (string, string, bool) { return "", "", false }
func (opqVal) Enum() (int, bool)                { return 0, false }
func (opqVal) Bits() ([]string, bool)           { return nil, false }

// When is the when state the evaluator reads (xpath.c moveto checks): false when flagged
// WhenFalse; unresolved when the node or a choice/case ancestor has a when and the node was not
// evaluated true yet; else true.
func (x xn) When() xpath.WhenState {
	switch {
	case x.n.flags&(FlagWhenFalse|flagDead) != 0: // a node deleted by the running when pass too
		return xpath.WhenFalse
	case x.n.schema != nil && hasWhen(x.n.schema) && x.n.flags&FlagWhenTrue == 0:
		return xpath.WhenUnresolved
	}
	return xpath.WhenTrue
}

// xval adapts a stored value to xpath.Value; s is the term node's schema node, nil for metadata.
type xval struct {
	v types.Value
	s *schema.Node
}

func (v xval) String() string { return v.v.Canonical() }

// selected is the stored value of the selected union member, or the value itself.
func (v xval) selected() types.Value {
	if u := v.v.Union(); u != nil {
		m, _ := u.Member()
		return m
	}
	return v.v
}

func (v xval) Identity() (string, string, bool) {
	s := v.selected()
	if s.Type() == nil || s.Type().Base != schema.IdentityRef || s.Ident() == nil {
		return "", "", false
	}
	return s.Ident().Module.Name, s.Ident().Name, true
}

// Enum: libyang checks the leaf's schema type, so an enum union member does not count.
func (v xval) Enum() (int, bool) {
	if v.s == nil || v.s.Type == nil || v.s.Type.Base != schema.Enumeration || v.v.Enum() == nil {
		return 0, false
	}
	return int(v.v.Enum().Value), true
}

func (v xval) Bits() ([]string, bool) {
	s := v.selected()
	if s.Type() == nil || s.Type().Base != schema.Bits {
		return nil, false
	}
	var names []string
	for _, b := range s.Bits() {
		names = append(names, b.Name)
	}
	return names, true
}

// xs adapts a compiled schema node to xpath.SchemaNode (a comparable value).
type xs struct {
	s   *schema.Node
	set *schema.Set
}

func wrapSchema(set *schema.Set, s *schema.Node) xpath.SchemaNode {
	if s == nil {
		return nil
	}
	return xs{s, set}
}

var xkinds = map[schema.Kind]xpath.Kind{
	schema.Container: xpath.KindContainer, schema.List: xpath.KindList, schema.Leaf: xpath.KindLeaf,
	schema.LeafList: xpath.KindLeafList, schema.AnyData: xpath.KindAnydata, schema.AnyXML: xpath.KindAnyxml,
	schema.RPC: xpath.KindRPC, schema.Action: xpath.KindAction, schema.Notification: xpath.KindNotif,
	schema.Choice: xpath.KindChoice, schema.Case: xpath.KindCase, schema.Input: xpath.KindInput,
	schema.Output: xpath.KindOutput,
}

func (x xs) Kind() xpath.Kind         { return xkinds[x.s.Kind] }
func (x xs) Name() string             { return x.s.Name }
func (x xs) Module() string           { return x.s.Module.Name }
func (x xs) Config() bool             { return !configR(x.s) }
func (x xs) Namespace() string        { return x.s.Module.Namespace }
func (x xs) Path() string             { return x.s.LogPath() }
func (x xs) Parent() xpath.SchemaNode { return wrapSchema(x.set, x.s.Parent) }

func (x xs) Keys() []string {
	var k []string
	for _, n := range x.s.Keys {
		k = append(k, n.Name)
	}
	return k
}

func (x xs) wrapAll(l []*schema.Node) []xpath.SchemaNode {
	out := make([]xpath.SchemaNode, len(l))
	for i, n := range l {
		out[i] = xs{n, x.set}
	}
	return out
}

func (x xs) Children() []xpath.SchemaNode      { return x.wrapAll(x.s.Children) }
func (x xs) Actions() []xpath.SchemaNode       { return x.wrapAll(x.s.Actions) }
func (x xs) Notifications() []xpath.SchemaNode { return x.wrapAll(x.s.Notifs) }

// Child is lys_find_child through choice and case; an operation's child from its input or its
// output, nil when both have one (eval_name_test_with_predicate_get_scnode: a name in both may be
// either, so the evaluator scans by name instead of looking the instance up).
func (x xs) Child(module, name string) xpath.SchemaNode {
	mod := x.set.Implemented(module)
	if mod == nil {
		return nil
	}
	c := schema.FindChild(x.s, nil, mod, name, 0)
	if x.s.Kind == schema.RPC || x.s.Kind == schema.Action {
		out := schema.FindChild(x.s, nil, mod, name, schema.GetNextOutput)
		switch {
		case c != nil && out != nil:
			return nil
		case out != nil:
			c = out
		}
	}
	if c == nil {
		return nil
	}
	return xs{c, x.set}
}

// Canonical is set_comp_canonize with the schema node's type: built-in string, boolean and
// enumeration need nothing; an invalid value is left as it is. The value's prefixes are the
// expression's: module names in a JSON expression (a query, pc.Default() ""), the module's own and
// import prefixes in a schema one (must, when, leafref path: LY_VALUE_SCHEMA_RESOLVED, an
// unprefixed name in the expression's module).
func (x xs) Canonical(lex string, pc xpath.NamespaceCtx) (string, bool) {
	t := x.s.Type
	if t == nil || types.Plugin(t) == nil && (t.Base == schema.String || t.Base == schema.Bool || t.Base == schema.Enumeration) {
		return "", false
	}
	f, p := types.FormatJSON, types.PrefixCtx(types.ModuleNames{Set: x.set})
	if pc != nil && pc.Default() != "" {
		f, p = types.FormatSchemaResolved, nsPrefixes{x.set, pc}
	}
	v, d := types.Store(t, lex, f, types.HintData, p, x.s)
	if d != nil {
		return "", false
	}
	return v.Canonical(), true
}

func (x xs) CheckValue(lex string, pc xpath.NamespaceCtx) (string, bool) {
	if _, d := types.Store(x.s.Type, lex, types.FormatJSON, types.HintData, nsPrefixes{x.set, pc}, x.s); d != nil {
		return d.Msg, false
	}
	return "", true
}

// LeafrefTarget is the target of a leaf whose own type is a leafref. A snapshot drops the
// compiled path because it names the compiler's nodes, so resolve it lazily against the copy.
func (x xs) LeafrefTarget() xpath.SchemaNode {
	t := x.s.Type
	if t == nil || t.Base != schema.Leafref {
		return nil
	}
	if p, ok := t.PathCompiled.(types.Path); ok && len(p) > 0 {
		return xs{p[len(p)-1].Node, x.set}
	}
	return wrapSchema(x.set, snap.LeafrefTarget(x.s, t))
}

func (x xs) Type() xpath.SchemaType {
	if x.s.Type == nil {
		return nil
	}
	return xt{x.s.Type}
}

// xt adapts a compiled type to xpath.SchemaType.
type xt struct{ t *schema.Type }

var xbases = map[schema.BaseType]xpath.BaseType{
	schema.Binary: xpath.TypeBinary, schema.Uint8: xpath.TypeUint8, schema.Uint16: xpath.TypeUint16,
	schema.Uint32: xpath.TypeUint32, schema.Uint64: xpath.TypeUint64, schema.String: xpath.TypeString,
	schema.Bits: xpath.TypeBits, schema.Bool: xpath.TypeBool, schema.Dec64: xpath.TypeDec64,
	schema.Empty: xpath.TypeEmpty, schema.Enumeration: xpath.TypeEnum, schema.IdentityRef: xpath.TypeIdent,
	schema.InstanceID: xpath.TypeInst, schema.Leafref: xpath.TypeLeafref, schema.Union: xpath.TypeUnion,
	schema.Int8: xpath.TypeInt8, schema.Int16: xpath.TypeInt16, schema.Int32: xpath.TypeInt32,
	schema.Int64: xpath.TypeInt64,
}

func (x xt) Base() xpath.BaseType { return xbases[x.t.Base] }

func (x xt) Union() []xpath.SchemaType {
	var out []xpath.SchemaType
	for _, m := range x.t.Union {
		out = append(out, xt{m})
	}
	return out
}

func (x xt) Realtype() xpath.SchemaType {
	if x.t.Realtype == nil {
		return nil
	}
	return xt{x.t.Realtype}
}

// nsPrefixes turns an expression's xpath.NamespaceCtx into the types.PrefixCtx of a stored value.
type nsPrefixes struct {
	set *schema.Set
	ns  xpath.NamespaceCtx
}

func (p nsPrefixes) Resolve(prefix string) *schema.Module {
	if p.ns == nil {
		return nil
	}
	name, ok := p.ns.Resolve(prefix) // "": the expression's own module where it has one
	if prefix == "" && !ok {
		name, ok = p.ns.Default(), true
	}
	if !ok {
		return nil
	}
	return p.set.Module(name, "")
}

// info adapts the schema set to xpath.SchemaInfo.
type info struct{ set *schema.Set }

func (i info) ident(id xpath.Ident) *schema.Identity {
	for m := range i.set.All(id.Module) {
		if d := m.Identity(id.Name); d != nil {
			return d
		}
	}
	return nil
}

func (i info) HasIdentity(id xpath.Ident) bool { return i.ident(id) != nil }

func (i info) IsDerived(base, id xpath.Ident) bool {
	b, d := i.ident(base), i.ident(id)
	return b != nil && d != nil && types.IsDerived(b, d)
}

func (i info) TopLevel(module, name string) []xpath.SchemaNode {
	var out []xpath.SchemaNode
	for _, m := range i.set.Modules {
		if !m.Implemented || module != "" && m.Name != module {
			continue
		}
		if n := schema.FindChild(nil, m.Top, m, name, 0); n != nil {
			out = append(out, xs{n, i.set})
		}
	}
	return out
}

func (i info) Modules() []string {
	var out []string
	for _, m := range i.set.Modules {
		if m.Implemented {
			out = append(out, m.Name)
		}
	}
	return out
}

func (i info) ModuleNodes(module string) (data, rpcs, notifs []xpath.SchemaNode) {
	m := i.set.Implemented(module)
	if m == nil {
		return nil, nil, nil
	}
	for _, n := range m.Top {
		switch n.Kind {
		case schema.RPC:
			rpcs = append(rpcs, xs{n, i.set})
		case schema.Notification:
			notifs = append(notifs, xs{n, i.set})
		default:
			data = append(data, xs{n, i.set})
		}
	}
	return data, rpcs, notifs
}

// rootType is lyxp_get_root_type for a data evaluation with LYXP_SCHEMA: inside an operation
// everything, else config-only when the context node is config (or the document root).
func rootType(ctx *Node) xpath.RootKind {
	if ctx == nil || ctx.schema == nil {
		return xpath.RootConfig
	}
	for op := ctx.schema; op != nil; op = op.Parent {
		switch op.Kind {
		case schema.RPC, schema.Action, schema.Notification:
			return xpath.RootAll
		}
	}
	if ctx.schema.Config {
		return xpath.RootConfig
	}
	return xpath.RootAll
}
