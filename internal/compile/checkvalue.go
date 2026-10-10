// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (warn_equality_value: the type plugin store call)
// (BSD-3-Clause, © CESNET).

package compile

import (
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// CheckValue is what a compiled SchemaNode answers to xpath.SchemaNode.CheckValue: lex, written
// with the prefix data of the expression (ns: the prefixes of the module the must or when is
// written in, "" being that module, import-only modules included), stored as a value of the type of
// the leaf or leaf-list n (the store of warn_equality_value: no store-only, schema-resolved
// prefixes, data hints). A value that needs the data tree to be complete (leafref,
// instance-identifier) fits. ok=false carries the store's message.
func CheckValue(n *schema.Node, lex string, ns schema.NSCtx) (msg string, ok bool) {
	return checkValue(n, lex, types.FormatSchemaResolved, ns)
}

func checkValue(n *schema.Node, lex string, format types.Format, prefixes types.PrefixCtx) (msg string, ok bool) {
	if _, d := types.Store(n.Type, lex, format, types.HintData, prefixes, n); d != nil {
		return d.Msg, false
	}
	return "", true
}
