// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_new.c (lyd_create_list2: the key predicates, through
// src/path.c ly_path_parse_predicate and ly_path_compile_predicate) (BSD-3-Clause, © CESNET).

package types

import (
	"fmt"
	"strings"

	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/schema"
)

// CompileKeys is the key part of lyd_create_list2: ly_path_parse_predicate (LY_PATH_PREFIX_OPTIONAL,
// LY_PATH_PRED_KEYS) and ly_path_compile_predicate of the key predicates keys of the list, each
// key value stored with LYD_HINT_DATA in the JSON format. It returns libyang's message (LOGVAL,
// LYVE_XPATH, or the type's) when they do not parse, compile or store.
func CompileKeys(set *schema.Set, list *schema.Node, keys string) ([]PathPred, string) {
	preds, d := CompileKeysDiag(set, list, keys)
	if d != nil {
		return nil, d.Msg
	}
	for _, p := range preds {
		if p.Var != "" { // lyd_create_list without variables (lyxp_vars_find)
			return nil, fmt.Sprintf("Variable \"%s\" not defined.", p.Var)
		}
	}
	return preds, ""
}

// CompileKeysDiag is CompileKeys with libyang's error item: the type's own item when a key value
// does not store (located at the key: At), else LYVE_XPATH, with LY_ENOTFOUND for a key or
// module ly_path_compile_snode does not find and LY_EVALID otherwise. The key values are stored
// and validated as ly_path_compile_predicate does; LYD_NEW_VAL_STORE_ONLY does not reach them. A
// variable value ([k=$v]) compiles to a PathPred with Var, which lyd_create_list resolves.
func CompileKeysDiag(set *schema.Set, list *schema.Node, keys string) ([]PathPred, *Diag) {
	e, msg := lyxp.ParsePredicate(keys, lyxp.PrefixOptional, lyxp.PredKeys)
	if msg != "" {
		return nil, &Diag{Code: "LYVE_XPATH", Msg: msg}
	}
	a := &storeArgs{f: FormatJSON, pc: ModuleNames{set}, ctx: list, vars: true}
	preds, _, msg := compilePredicate(a, list, e, 0)
	switch {
	case msg == "":
		return preds, nil
	case a.keyErr != nil:
		return nil, a.keyErr
	}
	d := &Diag{Code: "LYVE_XPATH", Msg: msg}
	for _, p := range []string{"Not found node ", "Not implemented module ", "No module connected "} {
		if strings.HasPrefix(msg, p) { // ly_path_compile_snode's LY_ENOTFOUND
			d.Err = "LY_ENOTFOUND"
		}
	}
	return nil, d
}
