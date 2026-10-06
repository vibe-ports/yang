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
	p, msg, _ := CompilePath(set, ctxNode, path, output, true)
	if msg != "" {
		return nil, msg
	}
	return p[len(p)-1].Node, ""
}

// CompilePath is ly_path_parse (LY_PATH_BEGIN_EITHER, LY_PATH_PREFIX_FIRST, LY_PATH_PRED_SIMPLE)
// with ly_path_compile of a JSON data path: key and leaf-list predicate values are stored with
// LYD_HINT_DATA; many is LY_PATH_TARGET_MANY (lyd_new_path), else a list or leaf-list needs its
// predicate (LY_PATH_TARGET_SINGLE, lyd_find_path). On failure it returns libyang's message
// (LOGVAL, LYVE_XPATH) and the schema node it is located at (nil: none).
func CompilePath(set *schema.Set, ctxNode *schema.Node, path string, output, many bool) (Path, string, *schema.Node) {
	e, msg := lyxp.ParsePath(path, lyxp.Opts{Begin: lyxp.BeginEither, Prefix: lyxp.PrefixFirst, Pred: lyxp.PredSimple})
	if msg != "" {
		return nil, msg, nil
	}
	a := &storeArgs{f: FormatJSON, pc: ModuleNames{set}, ctx: ctxNode}
	return pathCompileAt(a, e, ctxNode, output, many)
}
