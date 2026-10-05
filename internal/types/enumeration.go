// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/enumeration.c (BSD-3-Clause, © CESNET).

package types

// storeEnum ports lyplg_type_store_enum.
func storeEnum(a *storeArgs) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	for _, e := range a.t.Enums {
		if e.Name == a.lex {
			return Value{typ: a.t, enum: e, canon: a.lex}, nil
		}
	}
	return Value{}, errf("Invalid enumeration value \"%s\".", a.lex)
}
