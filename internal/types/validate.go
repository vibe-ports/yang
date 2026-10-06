// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/{integer,decimal64,string,binary,hex_string}.c
// (the validate_value callbacks), src/tree_data_new.c (lyd_new_term_raw: the call of the callback
// outside a store; lyd_create_term, lyd_new_implicit_r: defaults) (BSD-3-Clause, © CESNET).

package types

import "github.com/vibe-ports/yang/internal/schema"

// Validate ports the validate_value callbacks of the type plugins: the restrictions of t (range,
// length, patterns) re-checked on a value stored without them (StoreOnly, libyang
// LYPLG_TYPE_STORE_ONLY), with the diagnostics Store would give. t is the type of the schema
// node; only integers, decimal64, strings (incl. hex-string typedefs) and binary have a callback.
// Leafref and instance-identifier are not resolved here (ValidateTree does that) and a union is
// not re-checked: libyang has no validate_value for them either.
func Validate(t *schema.Type, v Value) *Diag {
	if v.typ == nil || v.typ.Base != t.Base {
		return nil
	}
	canon := v.canon
	switch t.Base {
	case schema.Int8, schema.Int16, schema.Int32, schema.Int64, schema.Dec64:
		if t.Range != nil {
			return checkRange(t.Base, t.Range, v.i, canon)
		}
	case schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64:
		if t.Range != nil {
			return checkRange(t.Base, t.Range, int64(v.u), canon) //nolint:gosec // reinterpreted as uint64 by checkRange
		}
	case schema.String:
		if p := pluginFor(t); p == nil || p.validate {
			return validateString(t, canon)
		}
	case schema.Binary:
		if t.Length != nil {
			return checkRange(schema.Binary, t.Length, int64(len(v.bin)), canon)
		}
	}
	return nil
}

// StoreDefault stores the default value d of the leaf or leaf-list n as libyang does for implicit
// nodes (lyd_new_implicit_r: lyd_create_term with store_only, LY_VALUE_SCHEMA_RESOLVED, the
// prefixes in scope of the default statement and LYD_HINT_SCHEMA). The restrictions were checked
// when the schema was compiled, so they are not checked again; v.NeedsTree() says whether the
// value is incomplete (libyang's node_types).
func StoreDefault(n *schema.Node, d schema.DefaultValue) (Value, *Diag) {
	return StoreOnly(n.Type, d.Lex, FormatSchemaResolved, HintSchema, d.NS, n)
}
