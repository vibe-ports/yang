// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.h and src/plugins_types/union.c (BSD-3-Clause, © CESNET).

package data

import (
	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/snap"
	"github.com/vibe-ports/yang/internal/types"
)

// ValueType returns the stored realtype (libyang tree_data.h, lyd_value.realtype),
// nil for non-terminal and opaque nodes. As in lyoracle.c value_json, this may
// differ from the schema type: a leafref stores its target's type, while a union
// stores the union type. Type.Base and Type.Typedef supply the oracle's value.type
// and value.typedef fields.
func (n *Node) ValueType() *yang.Type {
	if !n.isTerm() {
		return nil
	}
	return snap.TypeOf(n.value.Type())
}

// UnionMember describes the declared union member and stored member realtype
// (libyang tree_data.h, lyd_value_union.value.realtype; lyoracle.c union_member_json).
// Index is zero-based in the flattened union, or -1 for the oracle's unmatched
// fallback, in which case Type and RealType both describe the stored type.
type UnionMember struct {
	Index          int
	Type, RealType *yang.Type
}

// UnionMember returns the first flattened member whose type, or leafref target
// realtype, matches the stored member realtype, as in lyoracle.c union_member_json
// and libyang plugins_types/union.c lyplg_type_sort_union. It does not report the
// internal selection index. Type is the declared member; RealType is the stored
// type. It returns false for non-union values, including non-terminal and opaque nodes.
func (n *Node) UnionMember() (UnionMember, bool) {
	if !n.isTerm() || n.value.Type() == nil || n.value.Type().Base != schema.Union {
		return UnionMember{}, false
	}
	v, _ := n.value.Union().Member()
	stored := v.Type()
	for i, member := range n.value.Type().Union {
		tp := member
		if tp.Base == schema.Leafref {
			tp = tp.Realtype
		}
		if tp == stored {
			return UnionMember{Index: i, Type: snap.TypeOf(member), RealType: snap.TypeOf(stored)}, true
		}
	}
	return UnionMember{Index: -1, Type: snap.TypeOf(stored), RealType: snap.TypeOf(stored)}, true
}

// OpaqueInfo describes libyang tree_data.h's lyd_node_opaq name, format and hints,
// as dumped by lyoracle.c opaq_name_json and hints_json. Empty identity fields
// mean absent; Module applies to JSON, Namespace to XML. Hints is caller-owned
// and uses the oracle's names and order: string, decnum, octnum, hexnum, num64,
// boolean, empty, string_datatypes, list, leaflist, container.
type OpaqueInfo struct {
	Prefix, Module, Namespace string
	Format                    Format
	Hints                     []string
}

// OpaqueInfo returns the opaque node's identity, format and a copy of its hints
// (libyang tree_data.h, lyd_node_opaq; lyoracle.c opaq_name_json/hints_json).
// It returns false for schema-bound nodes. Name and Value provide the node's
// local name and original value.
func (n *Node) OpaqueInfo() (OpaqueInfo, bool) {
	if n.schema != nil {
		return OpaqueInfo{}, false
	}
	o := n.opaq
	info := OpaqueInfo{Prefix: o.Prefix, Format: FormatJSON, Module: o.ModuleNS, Hints: []string{}}
	if o.Format == types.FormatXML {
		info.Format, info.Namespace, info.Module = FormatXML, o.ModuleNS, ""
	}
	for _, hint := range []struct {
		bit  types.Hints
		name string
	}{
		{types.HintString, "string"}, {types.HintDecNum, "decnum"},
		{types.HintOctNum, "octnum"}, {types.HintHexNum, "hexnum"},
		{types.HintNum64, "num64"}, {types.HintBoolean, "boolean"},
		{types.HintEmpty, "empty"}, {types.HintStringDatatypes, "string_datatypes"},
		{hintList, "list"}, {hintLeafList, "leaflist"}, {hintContainer, "container"},
	} {
		if o.Hints&hint.bit != 0 {
			info.Hints = append(info.Hints, hint.name)
		}
	}
	return info, true
}

// Attr is a copied generic attribute of an opaque node (libyang tree_data.h,
// lyd_attr; lyoracle.c typed_add). Name is local, Prefix is as written, Module
// is the XML namespace or JSON module name, and Value is the original text.
type Attr struct{ Name, Prefix, Module, Value string }

// Attrs returns copied opaque attributes in their stored order (libyang
// tree_data.h, lyd_node_opaq.attr and lyd_attr; lyoracle.c typed_add), or nil
// for schema-bound nodes or no attributes. Callers cannot mutate the tree
// through the returned slice.
func (n *Node) Attrs() []Attr {
	if n.schema != nil || len(n.opaq.Attrs) == 0 {
		return nil
	}
	attrs := make([]Attr, len(n.opaq.Attrs))
	for i, a := range n.opaq.Attrs {
		attrs[i] = Attr{Name: a.Name, Prefix: a.Prefix, Module: a.ModuleNS, Value: a.Value}
	}
	return attrs
}
