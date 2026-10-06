// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/leafref.c (lyplg_type_validate_tree_leafref),
// src/plugins_types/instanceid.c (lyplg_type_validate_tree_instanceid),
// src/plugins_types/union.c (lyplg_type_validate_tree_union) and src/plugins_types.c
// (lyplg_type_resolve_leafref messages) (BSD-3-Clause, © CESNET).

package types

import (
	"fmt"

	"github.com/vibe-ports/yang/internal/schema"
)

// Tree is what tree-time validation needs from the data tree, evaluated for the data node the
// value belongs to. The data package implements it with XPath over its tree.
//
// LeafrefTarget must follow lyplg_type_resolve_leafref (plugins_types.c): when the canonical
// value does not contain both quote kinds, libyang evaluates the path with a value predicate
// appended (`path[.='v']`, or `list[key='v']/key` when the target is a list key), otherwise the
// plain path and then compares; targets whose stored realtype differs from v.Type() are skipped
// (compile sets a leafref's Realtype to the target leaf's own *schema.Type, so pointer identity
// holds); a target compiled away by if-feature makes the reference succeed (the path no longer
// compiles and libyang returns success).
type Tree interface {
	// LeafrefTarget reports whether the leafref path of t, evaluated from the node, selects an
	// instance whose value equals v. err is an XPath evaluation failure.
	LeafrefTarget(t *schema.Type, v Value) (found bool, err error)
	// InstanceExists reports whether the instance-identifier target exists in the tree.
	InstanceExists(p Path) bool
}

// ValidateTree runs the checks of a value that need the data tree (libyang validate_tree
// callbacks): leafref and instance-identifier require-instance, and union member re-selection.
// t is the type of the schema node (for a leafref value Value.Type is the target type). It
// returns the value to keep: a union may select another member. Call it only when
// v.NeedsTree() is true — libyang runs validate_tree only for values whose store returned
// LY_EINCOMPLETE; a union whose selected member needed nothing is never re-resolved.
func ValidateTree(t *schema.Type, v Value, tree Tree) (Value, *Diag) {
	switch t.Base {
	case schema.Leafref:
		if !t.RequireInstance {
			return v, nil
		}
		found, err := tree.LeafrefTarget(t, v)
		switch {
		case err != nil:
			return v, &Diag{Code: CodeData, AppTag: "instance-required",
				Msg: fmt.Sprintf("Invalid leafref value \"%s\" - XPath evaluation error (%s).", v.canon, err)}
		case !found:
			return v, &Diag{Code: CodeData, AppTag: "instance-required", Msg: noLeafrefMsg(v.canon, t.Path)}
		}
	case schema.InstanceID:
		if t.RequireInstance && !tree.InstanceExists(v.path) {
			// the error item carries ly_path_eval's LY_ENOTFOUND (lyplg_type_validate_tree_instanceid)
			return v, &Diag{Code: CodeData, AppTag: "instance-required", Err: "LY_ENOTFOUND",
				Msg: fmt.Sprintf("Invalid instance-identifier \"%s\" value - required instance not found.", v.canon)}
		}
	case schema.Union:
		if v.union == nil {
			return v, nil
		}
		u := *v.union
		if d := unionFind(t, &u, false, tree); d != nil {
			return v, d
		}
		return Value{typ: v.typ, canon: u.member.canon, union: &u}, nil
	}
	return v, nil
}

func noLeafrefMsg(val, path string) string {
	return fmt.Sprintf("Invalid leafref value \"%s\" - no target instance \"%s\" with the same value.", val, path)
}
