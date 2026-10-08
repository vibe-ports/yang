// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_common.c (lyd_node_schema, lyd_parse_opaq_error,
// lyd_parse_opaq_list_error, ly_value_validate) (BSD-3-Clause, © CESNET).

package data

import (
	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// nodeSchema is lyd_node_schema: the schema node of n, for an opaque node the one its module and
// name would have under its parent's (nil when there is none).
func nodeSchema(set *schema.Set, n *Node) *schema.Node {
	if n == nil || n.schema != nil {
		if n == nil {
			return nil
		}
		return n.schema
	}
	var prev *Node
	var sn *schema.Node
	for it := n.parent; it != nil; it = it.parent {
		if it.schema != nil {
			prev, sn = it, it.schema
			break
		}
	}
	for {
		it := n
		for it.parent != prev {
			it = it.parent
		}
		mod := nodeModule(set, it)
		if mod == nil {
			return nil
		}
		top := mod.Top
		sn = schema.FindChild(sn, top, mod, it.Name(), 0)
		prev = it
		if sn == nil || it == n {
			return sn
		}
	}
}

// valueValidate is ly_value_validate: store the opaque value with the schema node's type and log
// a rejection at the schema node (no data node).
func (l *logger) valueValidate(sn *schema.Node, o *opaque) error {
	if _, d := types.Store(sn.Type, o.Value, o.Format, o.Hints, o.Prefixes, sn); d != nil {
		return l.storeErr(nil, sn, d)
	}
	return nil
}

// opaqError is lyd_parse_opaq_error: why the opaque node n cannot be a schema node, reported
// when validation meets it (design 07 §3.1). It always fails; LOGERR when the node would be valid.
func (l *logger) opaqError(n *Node) error {
	o, parent := n.opaq, n.parent
	sparent := nodeSchema(l.set, parent)
	if o.ModuleNS == "" {
		return l.val(parent, "", ly.Reference, "Unknown module of node \"%s\".", o.Name)
	}
	var mod *schema.Module
	switch o.Format {
	case types.FormatXML:
		if sparent == nil || o.ModuleNS != sparent.Module.Namespace {
			if mod = l.set.ByNamespace(o.ModuleNS); mod == nil {
				return l.val(parent, "", ly.Reference, "No (implemented) module with namespace \"%s\" of node \"%s\" in the context.",
					o.ModuleNS, o.Name)
			}
		} else {
			mod = sparent.Module
		}
	case types.FormatJSON:
		if sparent == nil || o.ModuleNS != sparent.Module.Name {
			if mod = l.set.Implemented(o.ModuleNS); mod == nil {
				return l.val(parent, "", ly.Reference, "No (implemented) module named \"%s\" of node \"%s\" in the context.",
					o.ModuleNS, o.Name)
			}
		} else {
			mod = sparent.Module
		}
	default:
		return l.logErr("LY_EINVAL", "Unsupported value format.")
	}
	sn := schema.FindChild(sparent, mod.Top, mod, o.Name, 0)
	if sn == nil && sparent != nil && (sparent.Kind == schema.RPC || sparent.Kind == schema.Action) {
		sn = schema.FindChild(sparent, mod.Top, mod, o.Name, schema.GetNextOutput) // maybe an output node
	}
	if sn == nil {
		if sparent != nil {
			return l.val(parent, "", ly.Reference, "Node \"%s\" not found as a child of \"%s\" node.", o.Name, sparent.Name)
		}
		return l.val(parent, "", ly.Reference, "Node \"%s\" not found in the \"%s\" module.", o.Name, mod.Name)
	}
	l.locSet(sn)
	defer l.locBack(1)
	switch sn.Kind {
	case schema.Leaf, schema.LeafList:
		if err := l.valueValidate(sn, o); err != nil {
			return err
		}
	case schema.List:
		if err := l.opaqListError(n, sn); err != nil {
			return err
		}
	case schema.Container, schema.RPC, schema.Action, schema.Notification:
		// an opaque value is never NULL in libyang (lyd_create_opaq stores ""), so an inner node
		// always fails here
		return l.val(nil, "", ly.Data, "Invalid value \"%s\" for %s \"%s\".", o.Value, nodetypeStr(sn.Kind), sn.Name)
	default:
		return l.logErr("LY_EINVAL", "Unexpected opaque schema node %s \"%s\".", nodetypeStr(sn.Kind), sn.Name)
	}
	return l.logErr("LY_EINVAL", "Unexpected valid opaque node %s \"%s\".", nodetypeStr(sn.Kind), sn.Name)
}

// opaqListError is lyd_parse_opaq_list_error: an invalid opaque key value, else the first
// missing key.
func (l *logger) opaqListError(n *Node, sn *schema.Node) error {
	keys := append([]*schema.Node(nil), sn.Keys...)
	for c := range n.kids.all() {
		i := 0
		for i < len(keys) && keys[i].Name != c.Name() {
			i++
		}
		if i == len(keys) {
			continue // some other node
		}
		k := keys[i]
		keys = append(keys[:i], keys[i+1:]...)
		if c.schema != nil {
			continue // valid key
		}
		if err := l.valueValidate(k, c.opaq); err != nil {
			return err
		}
	}
	if len(keys) > 0 {
		return l.val(n.parent, "", ly.Data, "List instance is missing its key \"%s\".", keys[0].Name)
	}
	return nil
}
