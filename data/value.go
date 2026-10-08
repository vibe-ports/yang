// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_common.c (lyd_value_validate, lyd_value_validate3,
// lyd_value_compare) (BSD-3-Clause, © CESNET).

package data

import (
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// valueValidate3 is lyd_value_validate3: store value with the type of the leaf or leaf-list sn
// and, when the value needs the data tree and a context node is given, resolve it there (tree is
// the types.Tree evaluated for ctxNode). incomplete is LY_EINCOMPLETE: the value needs the tree
// and there is no context node. realtype is the stored value's type, a union's selected member
// type; canonical its canonical text. A rejected value is logged at ctxNode and sn when log is
// set, else only returned.
func (l *logger) valueValidate3(sn *schema.Node, value string, f types.Format, pc types.PrefixCtx, h types.Hints,
	ctxNode *Node, tree types.Tree, log bool) (realtype *schema.Type, canonical string, incomplete bool, err error) {
	v, d := types.Store(sn.Type, value, f, h, pc, sn)
	if d == nil && v.NeedsTree() {
		if ctxNode == nil {
			incomplete = true
		} else {
			v, d = types.ValidateTree(sn.Type, v, tree)
		}
	}
	if d != nil {
		if log {
			return nil, "", false, l.storeErr(ctxNode, sn, d)
		}
		return nil, "", false, d
	}
	realtype = v.Type()
	if u := v.Union(); u != nil {
		m, _ := u.Member()
		realtype = m.Type()
	}
	return realtype, v.Canonical(), incomplete, nil
}

// validateValue is lyd_value_validate: valueValidate3 for a JSON value with data hints, logged.
func (l *logger) validateValue(sn *schema.Node, value string, ctxNode *Node, tree types.Tree) (
	realtype *schema.Type, canonical string, incomplete bool, err error) {
	return l.valueValidate3(sn, value, types.FormatJSON, types.ModuleNames{Set: l.set}, types.HintData, ctxNode, tree,
		true)
}

// valueCompare is lyd_value_compare: whether the JSON value, stored with the type of the term
// node n (lyd_value_store, a rejection logged at n), equals n's value (LY_SUCCESS; false is
// LY_ENOT).
func (l *logger) valueCompare(n *Node, value string) (bool, error) {
	v, d := types.Store(n.schema.Type, value, types.FormatJSON, types.HintData, types.ModuleNames{Set: l.set}, n.schema)
	if d != nil {
		return false, l.storeErr(n, n.schema, d)
	}
	return types.Equal(n.value, v), nil
}
