// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (warn_equality_value: the type plugin store call)
// (BSD-3-Clause, © CESNET).

package compile

import (
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
)

// CheckValue is what a compiled SchemaNode answers to xpath.SchemaNode.CheckValue: lex, written
// with the prefixes of ns, stored as a value of the type of the leaf or leaf-list n (the store of
// warn_equality_value: no store-only, schema-resolved prefixes, data hints). A value that needs
// the data tree to be complete (leafref, instance-identifier) fits. ok=false carries the store's
// message.
func CheckValue(set *schema.Set, n *schema.Node, lex string, ns xpath.NamespaceCtx) (msg string, ok bool) {
	if _, d := types.Store(n.Type, lex, types.FormatSchemaResolved, types.HintData, exprPrefixes{set, ns}, n); d != nil {
		return d.Msg, false
	}
	return "", true
}

// exprPrefixes resolves the prefixes of an expression (xpath.NamespaceCtx names modules, "" is
// the module the expression is written in) to implemented modules.
type exprPrefixes struct {
	set *schema.Set
	ns  xpath.NamespaceCtx
}

func (e exprPrefixes) Resolve(prefix string) *schema.Module {
	name, ok := e.ns.Default(), true
	if prefix != "" {
		name, ok = e.ns.Resolve(prefix)
	}
	if !ok {
		return nil
	}
	return e.set.Implemented(name)
}
