// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_free.c (lyd_free_meta_single) and
// src/plugins_exts/metadata.h (lyd_get_meta_value) (BSD-3-Clause, © CESNET).

package data

import (
	"iter"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/snap"
)

// Meta is a metadata instance of a data node (RFC 7952 annotation, libyang struct lyd_meta), such
// as ietf-netconf:operation or the yang:operation, yang:key, yang:value, yang:position and
// yang:orig-* metadata of libyang's diff format. It is a handle of the instance on its node:
// handles are fresh per call, compare Module, Name and Value, not the handles.
type Meta struct {
	node *Node
	m    *meta
}

// Module is the module that defines the annotation (lyd_meta.annotation->module).
func (m *Meta) Module() *yang.Module { return snap.ModuleOf(m.m.mod) }

// Name is the annotation name, without its module.
func (m *Meta) Name() string { return m.m.name }

// Value is lyd_get_meta_value: the canonical text of the value.
func (m *Meta) Value() string { return m.m.value.Canonical() }

// Remove is lyd_free_meta_single: the instance leaves its node. Removing it again does nothing.
func (m *Meta) Remove() {
	for i, x := range m.node.meta {
		if x == m.m {
			m.node.meta = append(m.node.meta[:i:i], m.node.meta[i+1:]...)
			return
		}
	}
}

// Meta yields the metadata of n in order (the lyd_node.meta list). Removing an instance while
// iterating is safe. An opaque node has generic attributes instead and yields nothing. libyang's
// internal yang:lyds_tree metadata (lyd_meta_is_internal, the RB tree of sorted instances) does
// not exist in the port, which keeps no such tree.
func (n *Node) Meta() iter.Seq[*Meta] {
	return func(yield func(*Meta) bool) {
		for _, m := range n.meta { // Remove builds a new slice: this one stays as it was
			if !yield(&Meta{n, m}) {
				return
			}
		}
	}
}

// FindMeta is lyd_find_meta: the first metadata instance of n named name ("module:name", the
// module being the latest revision of that name), nil if there is none. A name without a module
// is an error; an invalid name or an unknown module is a *ValidationError with libyang's message.
func (n *Node) FindMeta(name string) (*Meta, error) {
	set := setOf(n)
	if set == nil { // a removed subtree: no context to resolve the module in, match its name
		if !strings.Contains(name, ":") {
			return nil, errMetaArg
		}
		prefix, local, _, _, _ := parseNodeID(name) // invalid: "", matching nothing
		for _, m := range n.meta {
			if m.mod.Name == prefix && m.name == local {
				return &Meta{n, m}, nil
			}
		}
		return nil, nil
	}
	lg := &logger{set: set}
	m, err := lg.findMeta(n.meta, nil, name)
	if err != nil || m == nil {
		return nil, lg.done(err)
	}
	return &Meta{n, m}, nil
}

// NewMeta is lyd_new_meta: the metadata name ("module:name" of an implemented module) with the
// value in the JSON format, stored and validated by the annotation's type, appended to the
// metadata of n, a node of t. A name without a module or a nil n is an error; an invalid name, an
// unknown module or annotation, a value the type rejects or an opaque n is a *ValidationError with
// libyang's diagnostics and return code (RC). The Default flags of n and its parents are kept.
func (t *Tree) NewMeta(n *Node, name, value string) (*Meta, error) {
	if n == nil {
		return nil, errMetaArg
	}
	lg := &logger{set: t.set}
	if !sameSet(setOf(n), t.set) {
		return nil, lg.done(lg.logErr("LY_EINVAL", "Different contexts mixed in a \"lyd_new_meta\" function call."))
	}
	m, err := lg.newMeta(n, nil, name, value, false)
	if err != nil {
		return nil, lg.done(err)
	}
	return &Meta{n, m}, nil
}
