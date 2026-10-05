// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/boolean.c (BSD-3-Clause, © CESNET).

package types

// storeBool ports lyplg_type_store_boolean.
func storeBool(a *storeArgs) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	switch a.lex {
	case "true":
		return Value{typ: a.t, i: 1, canon: a.lex}, nil
	case "false":
		return Value{typ: a.t, canon: a.lex}, nil
	}
	return Value{}, errf("Invalid boolean value \"%s\".", a.lex)
}
