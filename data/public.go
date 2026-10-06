// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/vibe-ports/yang"
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
		return nil, nil, errors.New("data: Parse without a schema")
	}
	var fp formatParser
	switch f {
	case FormatJSON:
		fp = parseJSON
	case FormatXML:
		fp = parseXML
	default:
		return nil, nil, fmt.Errorf("data: unknown format %d", f)
	}
	return parseWith(ctx, r, snap.Set(s), parseOpts{ParseOptions: o}, fp, nil)
}

// Validate is lyd_validate_all over the whole tree: when conditions, value checks needing the
// tree, implicit default nodes (added to t), must, mandatory, min/max-elements, unique and
// duplicate instances. A node whose when is false is removed silently if it was created as
// valid earlier (FlagWhenTrue), else it is an error. Diagnostics and err as for Parse.
func (t *Tree) Validate(ctx context.Context, o ValidateOptions) ([]yang.Diagnostic, error) {
	return t.validateAll(ctx, o, Budget{}, nil)
}
