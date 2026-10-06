// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_schema.c (lys_find_path) and src/path.c (_ly_path_compile
// with LY_PATH_TARGET_MANY) (BSD-3-Clause, © CESNET).

package types

import (
	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/schema"
)

// FindPath is lys_find_path: the schema node a JSON-style path names (module names as prefixes,
// the first node prefixed, key, leaf-list and position predicates allowed but not required),
// absolute or relative to ctxNode; output selects an operation's output. A list in the middle of
// the path needs no predicate (LY_PATH_TARGET_MANY). It returns nil and libyang's message (the
// LOGVAL text, LYVE_XPATH) when the path does not resolve.
func FindPath(set *schema.Set, ctxNode *schema.Node, path string, output bool) (*schema.Node, string) {
	e, msg := lyxp.ParsePath(path, lyxp.Opts{Begin: lyxp.BeginEither, Prefix: lyxp.PrefixFirst, Pred: lyxp.PredSimple})
	if msg != "" {
		return nil, msg
	}
	a := &storeArgs{f: FormatJSON, pc: ModuleNames{set}, ctx: ctxNode}
	p, msg := pathCompileAt(a, e, ctxNode, output, true)
	if msg != "" {
		return nil, msg
	}
	return p[len(p)-1].Node, ""
}
