// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_new.c (lyd_create_list2: the key predicates, through
// src/path.c ly_path_parse_predicate and ly_path_compile_predicate) (BSD-3-Clause, © CESNET).

package types

import (
	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/schema"
)

// CompileKeys is the key part of lyd_create_list2: ly_path_parse_predicate (LY_PATH_PREFIX_OPTIONAL,
// LY_PATH_PRED_KEYS) and ly_path_compile_predicate of the key predicates keys of the list, each
// key value stored with LYD_HINT_DATA in the JSON format. It returns libyang's message (LOGVAL,
// LYVE_XPATH, or the type's) when they do not parse, compile or store.
func CompileKeys(set *schema.Set, list *schema.Node, keys string) ([]PathPred, string) {
	e, msg := lyxp.ParsePredicate(keys, lyxp.PrefixOptional, lyxp.PredKeys)
	if msg != "" {
		return nil, msg
	}
	a := &storeArgs{f: FormatJSON, pc: ModuleNames{set}, ctx: list}
	preds, _, msg := compilePredicate(a, list, e, 0)
	return preds, msg
}
