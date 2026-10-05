// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/leafref.c (BSD-3-Clause, © CESNET).

package types

// storeLeafref ports lyplg_type_store_leafref: the value is stored as the leafref's real
// (target) type; with require-instance it still needs ValidateTree.
func storeLeafref(a *storeArgs) (Value, *Diag) {
	if a.t.Realtype == nil {
		return Value{}, errf("Internal error: leafref without a resolved target type.")
	}
	b := *a
	b.t = a.t.Realtype
	v, d := storeArgsDispatch(&b)
	if d != nil {
		return Value{}, d
	}
	v.needsTree = a.t.RequireInstance // whether the target type itself needed resolving is irrelevant
	return v, nil
}
