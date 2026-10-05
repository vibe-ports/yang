// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/empty.c (BSD-3-Clause, © CESNET).

package types

// storeEmpty ports lyplg_type_store_empty.
func storeEmpty(a *storeArgs) (Value, *Diag) {
	if a.lex != "" {
		return Value{}, errf("Invalid empty value size %d b.", len(a.lex)*8)
	}
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	return Value{typ: a.t}, nil
}
