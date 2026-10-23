// SPDX-License-Identifier: BSD-3-Clause

package resource_test

import (
	"fmt"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/lyjson"
	"github.com/vibe-ports/yang/internal/lyxml"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/resource"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
	"github.com/vibe-ports/yang/internal/xsdre"
)

// Keep this inventory aligned with every exported Err* sentinel in the root module. Only limits
// are structural resource failures; syntax, unsupported-feature, kind, incomplete, and no-match
// sentinels remain ordinary failures that speculative XPath compilation may own.
func TestExportedSentinelClassification(t *testing.T) {
	for name, err := range map[string]error{
		"yang.ErrBudget":    yang.ErrBudget,
		"compile.ErrBudget": compile.ErrBudget,
		"parser.ErrBudget":  parser.ErrBudget,
		"xpath.ErrBudget":   xpath.ErrBudget,
		"lyxml.ErrBudget":   lyxml.ErrBudget,
		"lyjson.ErrNesting": lyjson.ErrNesting,
	} {
		if !resource.Is(fmt.Errorf("wrapped: %w", err)) {
			t.Errorf("%s is not classified as a resource failure", name)
		}
	}
	for name, err := range map[string]error{
		"yang.ErrUnsupported":    yang.ErrUnsupported,
		"compile.ErrUnsupported": compile.ErrUnsupported,
		"data.ErrUnsupported":    data.ErrUnsupported,
		"types.ErrUnsupported":   types.ErrUnsupported,
		"parser.ErrKind":         parser.ErrKind,
		"xpath.ErrIncomplete":    xpath.ErrIncomplete,
		"xpath.ErrNoMatch":       xpath.ErrNoMatch,
		"xsdre.ErrSyntax":        xsdre.ErrSyntax,
		"xsdre.ErrUnsupported":   xsdre.ErrUnsupported,
	} {
		if resource.Is(err) {
			t.Errorf("%s classified as a resource failure", name)
		}
	}
}
