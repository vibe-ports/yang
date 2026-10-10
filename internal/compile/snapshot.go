// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"slices"

	"github.com/vibe-ports/yang/internal/schema"
)

// Snapshot returns a deep copy of the compiled schema of every module in the context, in context
// order (design 06 §4): modules, nodes, identities, features, types, musts, whens and extension
// instances are new objects, so a later Load, which edits the compiled modules in place, never
// touches it. Compiled XPath expressions and patterns are immutable and shared; an expression's
// prefix bindings still name the context-side modules, read only for their (immutable) Name. The features are
// the parsed flags (lys_feature_value): a failed Load keeps a changed flag without recompiling.
// A leafref's PathCompiled is not copied (it names nodes; readers compile the path themselves).
//
// ponytail: the whole context is copied per Load (O(schema size)); copy-on-write per module when
// that shows up.
func (c *Context) Snapshot() *schema.Set {
	cp := &snapCopy{mods: map[*schema.Module]*schema.Module{}, nodes: map[*schema.Node]*schema.Node{},
		idents: map[*schema.Identity]*schema.Identity{}, types: map[*schema.Type]*schema.Type{}}
	set := &schema.Set{LeafrefLinking: c.opts.LeafrefLinking}
	for _, m := range c.Modules { // shells first: everything below may point to any module
		if m.Schema == nil {
			continue
		}
		o := m.Schema
		n := &schema.Module{Name: o.Name, Revision: o.Revision, Namespace: o.Namespace, Prefix: o.Prefix,
			Version: o.Version, Implemented: m.Implemented, Submodules: slices.Clone(o.Submodules),
			BuiltinPluginsOnly: o.BuiltinPluginsOnly}
		cp.mods[o] = n
		for _, id := range o.Identities {
			cp.idents[id] = &schema.Identity{Name: id.Name, Module: n, Status: id.Status, Disabled: id.Disabled}
		}
		set.Modules = append(set.Modules, n)
	}
	for _, m := range c.Modules {
		if m.Schema == nil {
			continue
		}
		o, n := m.Schema, cp.mods[m.Schema]
		for _, im := range o.Imports {
			n.Imports = append(n.Imports, schema.Import{Prefix: im.Prefix, Module: cp.mod(im.Module)})
		}
		for _, f := range m.features {
			n.Features = append(n.Features, &schema.Feature{Name: f.p.Name, Module: n, Enabled: f.enabled,
				Status: parsedStatus(f.p.Status)})
		}
		for _, id := range o.Identities {
			x := cp.idents[id]
			for _, d := range id.Derived {
				x.Derived = append(x.Derived, cp.ident(d))
			}
			n.Identities = append(n.Identities, x)
		}
		for _, t := range o.Top {
			n.Top = append(n.Top, cp.tree(t, nil))
		}
		n.Exts = cp.exts(o.Exts)
	}
	for o, n := range cp.nodes { // references to other nodes, once all are copied
		cp.refs(o, n)
	}
	return set
}

type snapCopy struct {
	mods   map[*schema.Module]*schema.Module
	nodes  map[*schema.Node]*schema.Node
	idents map[*schema.Identity]*schema.Identity
	types  map[*schema.Type]*schema.Type
}

// mod, ident and node map a pointer of the context to its copy; one outside the snapshot (nil,
// or a module dropped from the context) maps to nil.
func (cp *snapCopy) mod(m *schema.Module) *schema.Module       { return cp.mods[m] }
func (cp *snapCopy) ident(i *schema.Identity) *schema.Identity { return cp.idents[i] }
func (cp *snapCopy) node(n *schema.Node) *schema.Node          { return cp.nodes[n] }

func (cp *snapCopy) ns(ns schema.NSCtx) schema.NSCtx {
	if ns == nil {
		return nil
	}
	out := make(schema.NSCtx, len(ns))
	for p, m := range ns {
		out[p] = cp.mod(m)
	}
	return out
}

// tree copies n and its subtree (children, actions, notifications) under parent.
func (cp *snapCopy) tree(o, parent *schema.Node) *schema.Node {
	n := &schema.Node{}
	*n = *o
	n.Module, n.Parent = cp.mod(o.Module), parent
	n.Children, n.Actions, n.Notifs = nil, nil, nil
	n.Keys, n.Uniques, n.DefaultCase, n.Whens = nil, nil, nil, nil
	cp.nodes[o] = n
	for _, c := range o.Children {
		n.Children = append(n.Children, cp.tree(c, n))
	}
	for _, c := range o.Actions {
		n.Actions = append(n.Actions, cp.tree(c, n))
	}
	for _, c := range o.Notifs {
		n.Notifs = append(n.Notifs, cp.tree(c, n))
	}
	n.Default = nil
	for _, d := range o.Default {
		n.Default = append(n.Default, schema.DefaultValue{Lex: d.Lex, NS: cp.ns(d.NS)})
	}
	n.Type = cp.typ(o.Type)
	n.Musts = nil
	for _, m := range o.Musts {
		x := *m
		x.Ctx = cp.ns(m.Ctx)
		n.Musts = append(n.Musts, &x)
	}
	n.Exts = cp.exts(o.Exts)
	return n
}

// refs fills the references of n (the copy of o) to other nodes.
func (cp *snapCopy) refs(o, n *schema.Node) {
	for _, k := range o.Keys {
		n.Keys = append(n.Keys, cp.node(k))
	}
	for _, u := range o.Uniques {
		var l []*schema.Node
		for _, x := range u {
			l = append(l, cp.node(x))
		}
		n.Uniques = append(n.Uniques, l)
	}
	n.DefaultCase = cp.node(o.DefaultCase)
	for _, w := range o.Whens {
		x := *w
		x.Ctx, x.ContextNode = cp.ns(w.Ctx), cp.node(w.ContextNode)
		n.Whens = append(n.Whens, &x)
	}
}

func (cp *snapCopy) exts(es []*schema.ExtInstance) []*schema.ExtInstance {
	if es == nil {
		return nil
	}
	out := make([]*schema.ExtInstance, 0, len(es)) // an empty array stays empty, not nil
	for _, e := range es {
		x := &schema.ExtInstance{Def: cp.mod(e.Def), Name: e.Name, Argument: e.Argument, Module: cp.mod(e.Module),
			Plugin: e.Plugin, Exts: cp.exts(e.Exts), Type: cp.typ(e.Type)}
		for _, n := range e.Nodes {
			x.Nodes = append(x.Nodes, cp.tree(n, nil))
		}
		if e.Root != nil {
			x.Root = cp.tree(e.Root, nil)
		}
		out = append(out, x)
	}
	return out
}

// typ copies a type, keeping the sharing between types (one copy per object).
func (cp *snapCopy) typ(o *schema.Type) *schema.Type {
	if o == nil {
		return nil
	}
	if t, ok := cp.types[o]; ok {
		return t
	}
	t := &schema.Type{}
	*t = *o
	cp.types[o] = t
	t.TypedefModule = cp.mod(o.TypedefModule)
	t.From = cp.typ(o.From)
	t.Realtype = cp.typ(o.Realtype)
	t.Prefixes = cp.ns(o.Prefixes)
	t.PathCompiled = nil
	t.Bases = nil
	for _, b := range o.Bases {
		t.Bases = append(t.Bases, cp.ident(b))
	}
	t.Union = nil
	for _, u := range o.Union {
		t.Union = append(t.Union, cp.typ(u))
	}
	t.Patterns = slices.Clone(o.Patterns)
	if o.Range != nil {
		r := *o.Range
		r.Parts = slices.Clone(o.Range.Parts)
		t.Range = &r
	}
	if o.Length != nil {
		r := *o.Length
		r.Parts = slices.Clone(o.Length.Parts)
		t.Length = &r
	}
	t.Enums = nil
	for _, e := range o.Enums {
		x := *e
		t.Enums = append(t.Enums, &x)
	}
	if o.Enums != nil && t.Enums == nil {
		t.Enums = []*schema.Enum{}
	}
	t.Bits = nil
	for _, b := range o.Bits {
		x := *b
		t.Bits = append(t.Bits, &x)
	}
	if o.Bits != nil && t.Bits == nil {
		t.Bits = []*schema.Bit{}
	}
	t.Exts = cp.exts(o.Exts)
	return t
}
