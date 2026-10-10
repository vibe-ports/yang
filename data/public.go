// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"io"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/snap"
)

// Parse is lyd_parse_data with LYD_PARSE_STRICT/LYD_PARSE_OPAQ from o.Unknown: it reads the
// whole input in format f against the schema snapshot s and, unless o.ParseOnly, validates it
// (lyd_validate over the parser's work queues). It returns the tree only when nothing failed:
// libyang frees the whole tree on any error. The diagnostics are returned in log order, warnings
// included; err is nil when only warnings were logged, else a *ValidationError, or an error
// wrapping yang.ErrBudget (Budget, the input's nesting limits) or ctx.Err(). The tree keeps s:
// a later Load into the context does not change it.
func Parse(ctx context.Context, r io.Reader, f Format, s *yang.Schema, o ParseOptions) (*Tree, []yang.Diagnostic, error) {
	if s == nil {
		lg := &logger{}
		err := lg.done(argErr("ctx || parent", "lyd_parse_data"))
		return nil, lg.diags, err
	}
	if r == nil {
		lg := &logger{set: snap.Set(s)}
		err := lg.done(argErr("in", "lyd_parse_data"))
		return nil, lg.diags, err
	}
	var fp formatParser
	switch f {
	case FormatJSON:
		fp = parseJSON
	case FormatXML:
		fp = parseXML
	default:
		lg := &logger{set: snap.Set(s)}
		err := lg.done(argErr("format", "lyd_parse"))
		return nil, lg.diags, err
	}
	if o.StoreOnly {
		o.ParseOnly = true // LYD_PARSE_STORE_ONLY = 0x01010000 contains LYD_PARSE_ONLY = 0x010000
	}
	return parseWith(ctx, r, snap.Set(s), parseOpts{ParseOptions: o, storeOnly: o.StoreOnly, whenTrue: o.WhenTrue, jsonNull: o.JSONNull,
		jsonStringDatatypes: o.JSONStringDatatypes}, fp, nil)
}

// Path is lyd_path(LYD_PATH_STD): "/mod:a/b[k='v']/c", the module name on every module change,
// list keys and config leaf-list values as predicates, the position of keyless list and state
// leaf-list instances.
func (n *Node) Path() string {
	root := n
	for root.parent != nil {
		root = root.parent
	}
	var set *schema.Set
	if root.tree != nil {
		set = root.tree.set
	}
	return lydPath(set, n, false)
}

// Value is lyd_get_value: the canonical text of a leaf or leaf-list value, the text of an
// opaque node as it was written, "" for other nodes.
func (n *Node) Value() string {
	switch {
	case n.schema == nil:
		return n.opaq.Value
	case n.isTerm():
		return n.value.Canonical()
	}
	return ""
}

// Schema is the schema node of n, nil for an opaque node.
func (n *Node) Schema() *yang.SchemaNode { return snap.NodeOf(n.schema) }

// Validate is lyd_validate_all over the whole tree: when conditions, value checks needing the
// tree, implicit default nodes (added to t), must, mandatory, min/max-elements, unique and
// duplicate instances. A node whose when is false is removed silently if it was created as
// valid earlier (FlagWhenTrue), else it is an error. Diagnostics and err as for Parse.
func (t *Tree) Validate(ctx context.Context, o ValidateOptions) ([]yang.Diagnostic, error) {
	return t.validateAll(ctx, o, Budget{}, nil)
}

// ValidateModule is lyd_validate_module over only the data of the implemented module named
// module: when conditions, value checks needing the tree, implicit default nodes (added to t),
// must, mandatory, min/max-elements, unique and duplicate instances. It does not validate data
// of other modules. ValidateOptions.Present is refused with LY_EINVAL and no diagnostic, matching
// libyang's NULL-context argument check. An unknown or not implemented module also
// returns LY_EINVAL without a diagnostic. Other diagnostics and err are as for Validate. See
// ValidateModuleDiff for the diff-returning form and its mutation and error semantics.
func (t *Tree) ValidateModule(ctx context.Context, module string, o ValidateOptions) ([]yang.Diagnostic, error) {
	mod, d, err := t.validateModuleArgs(module, o)
	if err != nil {
		return d, err
	}
	return t.validateModule(ctx, mod, o, Budget{}, nil)
}

// validateModuleArgs applies lyd_validate_module's public module and option argument checks.
func (t *Tree) validateModuleArgs(module string, o ValidateOptions) (*schema.Module, []yang.Diagnostic, error) {
	mod := t.set.Implemented(module)
	if mod == nil {
		// validation.c:2304 checks module with a NULL logging context.
		err := &ValidationError{err: argErr("module", "lyd_validate_module"), rc: "LY_EINVAL"}
		return nil, nil, err
	}
	if o.Present {
		// LY_CHECK_ARG_RET(NULL, ...): the argument error has no context in which to be logged.
		err := &ValidationError{err: argErr("!(val_opts & LYD_VALIDATE_PRESENT)", "lyd_validate_module"), rc: "LY_EINVAL"}
		return nil, nil, err
	}
	return mod, nil, nil
}
