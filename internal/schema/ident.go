// SPDX-License-Identifier: BSD-3-Clause

package schema

import (
	"errors"
	"unique"
)

// Ident is a schema node's module name and node name, the identifiers lyd_compare_schema_equal
// compares at one level, each interned on its own: equal names have equal handles, so comparing
// or hashing an Ident is O(1) whatever the names' length, and a module's name is interned once
// for all its nodes (a handle of the pair would copy the module name per node).
type Ident struct{ Mod, Name unique.Handle[string] }

// ID is n's Ident. A snapshot node carries it (SetID); another node interns its names here.
func (n *Node) ID() Ident {
	if n.Interned() {
		return n.ident
	}
	return Ident{n.Module.NameID(), unique.Make(n.Name)}
}

// Interned reports whether n carries its Ident (SetID).
func (n *Node) Interned() bool { return n.ident != (Ident{}) }

// SetID interns n's Ident into n. Only the snapshot copy calls it, before the set is shared;
// n's module is interned first (Module.SetID).
func (n *Node) SetID() { n.ident = Ident{n.Module.NameID(), unique.Make(n.Name)} }

// NameID is m's interned name ("" for a nil module).
func (m *Module) NameID() unique.Handle[string] {
	switch {
	case m == nil:
		return unique.Make("")
	case m.nameID != (unique.Handle[string]{}):
		return m.nameID
	}
	return unique.Make(m.Name)
}

// SetID interns m's name into m (the snapshot copy, before the set is shared).
func (m *Module) SetID() { m.nameID = unique.Make(m.Name) }

// ChildKey identifies a schema node under its parent: the node type and the interned
// identifiers, the fields lyd_compare_schema_equal compares at one level.
type ChildKey struct {
	Kind Kind
	ID   Ident
}

// ErrAmbiguousRoot is ChildByName's outcome for a top-level key held by an extension-instance
// root together with another top-level node of the set: equal identities, so no single node is
// the key's child.
var ErrAmbiguousRoot = errors.New("schema: ambiguous extension-instance root")

// ambiguous marks an ambiguous key in a top-level index.
var ambiguous = new(Node)

// ChildByName is the child of parent with the given key, nil if none. parent is nil (the top
// level: every module's top nodes in load order, then the roots of the modules' extension
// instances, a structure's Root or yang-data's Nodes) or a node of this set (its Module.Owner); a
// node of another set, or of an ownerless hand-built set, has no children here and nothing is
// stored for it, so the set's index holds only the set's own nodes. A parent's children are its
// data children (a choice's cases, an operation's input and output) and its actions and
// notifications; the first equal key wins, as a sibling walk would find it. An extension root
// is indexed only when no other top-level node has its key; a shared key returns
// ErrAmbiguousRoot.
//
// A parent's index is built on first use and paid through charge (nil: uncharged) as it goes:
// one unit up front, one per module and extension instance enumerated and one per child (a
// fixed-size key), so a charge that polls a cancellation stops the build partway. A refusal
// stores nothing and is returned; only a complete index is published. Concurrent cold builders
// each build and the first stored index wins (they are equal). A warm lookup is one map read and
// charges nothing.
func (s *Set) ChildByName(parent *Node, key ChildKey, charge func(int64) error) (*Node, error) {
	if parent != nil && (parent.Module == nil || parent.Module.Owner != s) {
		return nil, nil
	}
	v, ok := s.children.Load(parent)
	if !ok {
		idx, err := s.childIndex(parent, charge)
		if err != nil {
			return nil, err
		}
		v, _ = s.children.LoadOrStore(parent, idx)
	}
	if n := v.(map[ChildKey]*Node)[key]; n != ambiguous {
		return n, nil
	}
	return nil, ErrAmbiguousRoot
}

// childIndex builds parent's index for ChildByName, paid through charge.
func (s *Set) childIndex(parent *Node, charge func(int64) error) (map[ChildKey]*Node, error) {
	pay := func() error {
		if charge == nil {
			return nil
		}
		return charge(1)
	}
	if err := pay(); err != nil {
		return nil, err
	}
	idx := make(map[ChildKey]*Node)
	add := func(ns []*Node, ext bool) error {
		for _, n := range ns {
			if err := pay(); err != nil {
				return err
			}
			switch k := (ChildKey{n.Kind, n.ID()}); {
			case idx[k] == nil:
				idx[k] = n
			case ext:
				idx[k] = ambiguous
			}
		}
		return nil
	}
	if parent != nil {
		for _, l := range [][]*Node{parent.Children, parent.Actions, parent.Notifs} {
			if err := add(l, false); err != nil {
				return nil, err
			}
		}
		return idx, nil
	}
	for _, m := range s.Modules {
		if err := pay(); err != nil {
			return nil, err
		}
		if err := add(m.Top, false); err != nil {
			return nil, err
		}
	}
	var exts func(es []*ExtInstance) error
	exts = func(es []*ExtInstance) error {
		for _, e := range es {
			if err := pay(); err != nil {
				return err
			}
			roots := e.Nodes
			if e.Root != nil {
				roots = []*Node{e.Root}
			}
			if err := add(roots, true); err != nil {
				return err
			}
			if err := exts(e.Exts); err != nil {
				return err
			}
		}
		return nil
	}
	for _, m := range s.Modules {
		if err := exts(m.Exts); err != nil {
			return nil, err
		}
	}
	return idx, nil
}
