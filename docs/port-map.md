# Port map: libyang v5.8.6 → Go

One row per ported libyang function. Status: `ported` · `partial` · `replaced` (different Go design,
same behaviour) · `skipped` (out of v1 scope, see PLAN §1).

| libyang file | C function | Go symbol | Status | Fixtures | Notes |
|---|---|---|---|---|---|
| src/plugins_types.c | lyplg_type_check_hints, type_get_hints_base | types.checkHints, types.hintsBase | ported | types/json-* | |
| src/plugins_types.c | lyplg_type_validate_range | types.checkRange | ported | types/string-length, types/binary-length | |
| src/plugins_types.c | lyplg_type_validate_patterns | types.checkPatterns | ported | types/string-invert-match | regex via internal/xsdre |
| src/plugins_types.c | lyplg_type_parse_int, lyplg_type_parse_uint | types.parseInt, types.parseUint | ported | types/int-*, types/uint64-* | |
| src/ly_common.c | ly_parse_int, ly_parse_uint | types.parseInt, types.parseUint, types.cStrtou | ported | types/int64-overflow | C strtoll/strtoull semantics incl. base auto-detection |
| src/plugins_types.c | lyplg_type_parse_dec64 | types.parseDec64 | ported | types/dec64-* | |
| src/plugins_types.c | lyplg_type_compare_simple, lyplg_type_sort_simple | types.Equal, types.Compare | ported | — | |
| src/plugins_types/integer.c | lyplg_type_store_int, lyplg_type_validate_value_int | types.storeInt | ported | types/int-* | LYB out of v1 |
| src/plugins_types/integer.c | lyplg_type_store_uint, lyplg_type_validate_value_uint | types.storeUint | ported | types/uint64-* | |
| src/plugins_types/integer.c | lyplg_type_compare_int, lyplg_type_sort_int, lyplg_type_compare_uint, lyplg_type_sort_uint | types.Equal, types.Compare | ported | — | |
| src/plugins_types/decimal64.c | lyplg_type_store_decimal64, lyplg_type_validate_value_decimal64 | types.storeDec64 | ported | types/dec64-* | |
| src/plugins_types/decimal64.c | decimal64_num2str | types.dec64String | ported | types/dec64-canonical | |
| src/plugins_types/decimal64.c | lyplg_type_compare_decimal64, lyplg_type_sort_decimal64 | types.Equal, types.Compare | ported | — | |
| src/plugins_types/string.c | lyplg_type_store_string, lyplg_type_validate_value_string | types.storeString | ported | types/string-* | |
| src/plugins_types/string.c | string_check_chars (ly_checkutf8) | types.checkChars | ported | — | |
| src/plugins_types/boolean.c | lyplg_type_store_boolean, lyplg_type_compare_boolean, lyplg_type_sort_boolean | types.storeBool, types.Equal, types.Compare | ported | types/json-bool-string | |
| src/plugins_types/empty.c | lyplg_type_store_empty | types.storeEmpty | ported | types/json-empty-string | |
| src/plugins_types/enumeration.c | lyplg_type_store_enum, lyplg_type_sort_enum | types.storeEnum, types.Compare | ported | types/json-valid | |
| src/plugins_types/bits.c | lyplg_type_store_bits, bits_str2bitmap, bits_bitmap2items, bits_items2canon | types.storeBits | ported | types/bits-* | |
| src/plugins_types/bits.c | lyplg_type_compare_bits, lyplg_type_sort_bits | types.Equal, types.Compare | ported | — | bitmap in libyang's little-endian layout |
| src/plugins_types/binary.c | lyplg_type_store_binary, lyplg_type_validate_value_binary | types.storeBinary | ported | types/binary-* | decoding via encoding/base64 after libyang's validation |
| src/plugins_types/binary.c | binary_base64_newlines, binary_base64_validate | types.base64Newlines, types.base64Validate | ported | types/binary-padding | |
| src/plugins_types/binary.c | lyplg_type_compare_binary, lyplg_type_sort_binary | types.Equal, types.Compare | ported | — | |
| src/tree_schema.h | struct lysc_* (module, node, type, ident, must, when) | internal/schema | replaced | — | plain Go structs, lookups only |
| parser_yang.c | `buf_add_char` | `lexer.storeChar` (`append`) | replaced | internal/parser lex_test | Go slices instead of the zero-copy/buffer split |
| parser_yang.c | `buf_store_char` | `lexer.storeChar` | ported | internal/parser lex_test, oracle_test | indent counting, character checks per argument kind |
| parser_yang.c | `skip_comment` | `lexer.skipComment` | ported | internal/parser lex_test | |
| parser_yang.c | `read_qstring` | `lexer.readQString` | ported | internal/parser lex_test, oracle_test (fidelity) | escapes, indentation trimming incl. tabs, `+` concatenation, CR handling |
| parser_yang.c | `get_argument` | `lexer.getArgument` | ported | internal/parser lex_test | |
| parser_yang.c | `get_keyword` | `lexer.getKeyword` | ported | internal/parser lex_test | `MaxDepth` = `LY_MAX_BLOCK_DEPTH` |
| parser_yang.c | `parse_ext`, `parse_ext_substmt` | `lexer.stmt` (generic `Stmt`) | replaced | internal/parser lex_test | every statement is read into a `Stmt`; extension instances stay generic, YANG statements are checked by `checker` |
| parser_yang.c | `skip_redundant_chars` | `lexer.skipRedundant` | ported | internal/parser lex_test | |
| parser_yang.c | `yang_parse_module`, `yang_parse_submodule` | `Parse` | partial | internal/parser lex_test | no context: the module/submodule decision is the caller's |
| tree_schema_common.c | `lysp_match_kw` | `lexer.matchKw` (`kwTrie`) | ported | internal/parser lex_test | partial matches keep libyang's input/indent effects |
| tree_schema_common.c | `lysp_check_identifierchar` | `lexer.checkIdentChar` | ported | internal/parser lex_test | |
| tree_schema_common.c | `lysp_check_stringchar` | `isYangChar` (in `storeChar`) | ported | internal/parser lex_test | incl. libyang's empty U+40000 range |
| ly_common.c | `ly_getutf8` | `lexer.utf8At` | ported | internal/parser lex_test | incl. 4-byte sequences accepted from U+1000 |
| log.h | `LY_VECODE` | `ly.Code` | ported | internal/ly code_test | shared by all ported packages |
| parser_yang.c | `parse_module`, `parse_submodule` | `checker.child` (`grammar`, `section`), `checker.close` (`mandatory`), `Build` | ported | internal/parser build_test, oracle_test | checks run inside `lexer.stmt` in libyang's order and at its line; `belongs-to` name and context duplicates are the loader's |
| parser_yang.c | `parse_*` substatement switches (`parse_leaf` … `parse_list`, `parse_type`, `parse_restr`, `parse_when`, `parse_any`, `parse_inout`, `parse_action`, `parse_notif`, `parse_grouping`, `parse_augment`, `parse_uses`, `parse_refine`, `parse_case`, `parse_choice`, `parse_typedef`, `parse_extension`, `parse_argument`, `parse_feature`, `parse_identity`, `parse_deviation`, `parse_include`, `parse_import`, `parse_revision`, `parse_belongsto`, `parse_text_field(s)`, `parse_qnames`) | `grammar` + `checker.child`/`arg`/`close`; `Build`, `builder.node`, `builder.typ`, `restriction` | replaced | internal/parser build_test, oracle_test | one table: allowed child, YANG 1.1 only, duplicate, mandatory |
| parser_yang.c | `parse_yangversion`, `parse_config`, `parse_mandatory`, `parse_status`, `parse_orderedby`, `parse_type_reqinstance`, `parse_type_pattern_modifier`, `parse_yinelement`, `parse_minelements`, `parse_maxelements`, `parse_type_fracdigits` | `checker.arg` | ported | internal/parser build_test, oracle_test | yang-version takes effect when reached |
| parser_yang.c | `parse_type_enum_value_pos` | `enumValue` | ported | internal/parser build_test, oracle_test | strtoll/strtoull semantics |
| parser_yang.c | `parse_type_enum` | `checker.arg` (CHECK_UNIQUENESS) | ported | internal/parser build_test, oracle_test | |
| parser_yang.c | `parse_deviate` | `deviateAllows`, `checker.child` | partial | internal/parser build_test, oracle_test | deviations kept as `Stmt` until M2 |
| parser_yang.c | `parse_type` (`path`: `ly_path_parse`) | `Type.Path` | partial | — | U-0005: syntax not checked until the XPath lexer (M1-3) |
| parser_yang.c | `YANG_READ_SUBSTMT_NEXT_ITER` (exts arrays into `ext_inst`) | `checker.close`, `extOwner`, `appendOwned` | ported | internal/parser build_test, oracle_test | registration on close, libyang's resolution order |
| tree_schema_common.c | `lys_check_date` | `checker.arg` | ported | internal/parser build_test, oracle_test | |
| tree_schema_common.c | `lysp_check_enum_name` | `checker.arg` | ported | internal/parser build_test, oracle_test | warning not emitted (U-0006) |
| tree_schema_common.c | `lysp_check_prefix` | `checker.close` | ported | internal/parser build_test, oracle_test | |
| tree_schema.c, tree_schema_common.c | `lysp_resolve_ext_instance_records`, `lysp_ext_find_definition` | `checker.resolveExts` | partial | internal/parser build_test, oracle_test | prefix check; definition lookup and missing argument for this module's extensions; imported definitions and their arguments are the compiler's |
| schema_features.c | `lys_compile_iffeature` (expression syntax) | `parseIfFeature`, `IfFeature.Err` | ported | internal/parser build_test, oracle_test | same two passes, iterative, builds an `IffExpr` tree; feature lookup is the compiler's; D-0020 |
| tree_schema_common.c | `lysp_ext_instance_resolve_argument` | `checker.resolveExts` | partial | internal/parser build_test, oracle_test | YANG input only (no YIN); this module's definitions |
| schema_features.c | `lysc_iffeature_value` | `IffExpr.Eval` | ported | internal/parser build_test | iterative |
| src/plugins_types/identityref.c | lyplg_type_store_identityref, identityref_str2ident, identityref_check_ident, identityref_check_base | types.storeIdentityRef, types.checkBases | ported | types/ident-* | LYPLG_TYPE_STORE_IMPLEMENT left to compile |
| src/plugins_types/identityref.c | lyplg_type_compare_identityref, lyplg_type_sort_identityref | types.Equal, types.Compare | ported | — | |
| src/plugins_types.c | lyplg_type_identity_isderived | types.IsDerived | ported | types/ident-not-derived | |
| src/plugins_types.c | lyplg_type_check_status, lyplg_type_lypath_check_status | types.checkStatus, types.checkPathStatus | ported | — | |
| src/tree_schema.c | lys_find_module (ly_resolve_prefix and per-format resolvers) | types.resolveModule, types.PrefixCtx implementations | replaced | types/ident-json | prefix data as an interface |
| src/plugins_types/instanceid.c | lyplg_type_store_instanceid, instanceid_path2str | types.storeInstanceID, types.Path.String | ported | types/instid-* | canonical = JSON form |
| src/plugins_types/instanceid.c | lyplg_type_validate_tree_instanceid | types.ValidateTree | ported | types/instid-no-instance | existence via types.Tree |
| src/plugins_types.c | lyplg_type_lypath_new | types.lypathNew | partial | types/instid-* | LYPLG_TYPE_STORE_IMPLEMENT (lys_compile_expr_implement of referenced modules) left to compile |
| src/path.c | ly_path_parse, ly_path_parse_deref, ly_path_check_predicate | lyxp.ParsePath, lyxp.Expr.deref, lyxp.Expr.CheckPredicate | ported | lyxp tests, parser/leafref path cases, types/instid-* | all begin/prefix/pred modes, leafref + `deref()` (LY_CTX_LEAFREF_EXTENDED, off in the parser as in yanglint); `ly_path_parse_predicate` not ported (unused) |
| src/path.c | _ly_path_compile, ly_path_compile_snode, ly_path_compile_predicate | types.pathCompile, types.compileSNode, types.compilePredicate | partial | types/instid-* | not leafref, single target; no extension nodes |
| src/xpath.c | lyxp_expr_parse (tokenizer), parse_ncname, expr_parse_axis, lyxp_check_token, exp_check_token2, lyxp_token2str | lyxp.Lex, lyxp.parseNCName, lyxp.Expr.Check, lyxp.Tok.String | ported | lyxp tests, types/instid-*, parser leafref cases | leaf package shared by parser, types and (follow-up) xpath. TODO after PR #16 merges: internal/xpath/lex.go should switch to lyxp.Lex and drop its duplicate tokenizer (lexer-only; the reparse stays in xpath) |
| src/plugins_types/leafref.c | lyplg_type_store_leafref | types.storeLeafref | ported | types/leafref-missing | |
| src/plugins_types/leafref.c | lyplg_type_validate_tree_leafref | types.ValidateTree | ported | types/leafref-missing | target lookup via types.Tree |
| src/plugins_types.c | lyplg_type_resolve_leafref (messages) | types.ValidateTree | partial | types/leafref-missing | XPath evaluation is the data layer's |
| src/plugins_types/union.c | lyplg_type_store_union, union_find_type, union_store_type, union_update_lref_err | types.storeUnion, types.unionFind, types.unionStoreType | ported | types/union-* | |
| src/plugins_types/union.c | lyplg_type_validate_tree_union | types.ValidateTree | ported | types/union-fallback | |
| src/plugins_types/union.c | lyplg_type_compare_union, lyplg_type_sort_union | types.Equal, types.compareUnion | ported | — | |
| src/plugins.c | lyplg_type_plugin_find | types.pluginFor | replaced | — | registry keyed by (module, revision, typedef) |
| src/plugins_types/ipv4_address.c | lyplg_type_store_ipv4_address, ipv4address_str2ip, lyplg_type_compare_ipv4_address, lyplg_type_sort_ipv4_address | types.storeIPAddr, types.equalIP, types.compareIP | ported | types/inet-* | inet_pton via net/netip; NUL handling D-0027 |
| src/plugins_types/ipv4_address_no_zone.c | lyplg_type_store_ipv4_address_no_zone (+ compare, sort) | types.storeIPNoZone | ported | types/inet-bad-v4 | |
| src/plugins_types/ipv4_address_prefix.c | lyplg_type_store_ipv4_address_prefix, ipv4prefix_str2ip, ipv4prefix_zero_host (+ compare, sort, print) | types.storeIPPrefix | ported | types/inet-canonical | |
| src/plugins_types/ipv6_address.c | lyplg_type_store_ipv6_address, ipv6address_str2ip, lyplg_type_print_ipv6_address (+ compare, sort) | types.storeIPAddr, types.ntop6 | ported | types/inet-* | glibc inet_ntop6 algorithm |
| src/plugins_types/ipv6_address_no_zone.c | lyplg_type_store_ipv6_address_no_zone, ipv6addressnozone_str2ip (+ compare, sort, print) | types.storeIPNoZone | ported | types/inet-mapped | |
| src/plugins_types/ipv6_address_prefix.c | lyplg_type_store_ipv6_address_prefix, ipv6prefix_str2ip, ipv6prefix_zero_host (+ compare, sort, print) | types.storeIPPrefix | ported | types/inet-canonical | |
| src/plugins_types/date_and_time.c | lyplg_type_store_date_and_time (old/new revision), lyplg_type_print_date_and_time, lyplg_type_compare_date_and_time, lyplg_type_sort_date_and_time | types.storeDateAndTime, types.equalDateAndTime, types.compareDateAndTime | ported | types/dt-* | known offsets print in UTC (D-0025); `-00:MM` sign quirk mirrored (D-0026) |
| src/tree_data_common.c | ly_time_str2time, ly_time_time2str, ly_time_tz_offset_at | types.timeStr2Time, types.storeDateAndTime | ported | types/dt-* | |
| src/plugins_types/hex_string.c | lyplg_type_store_hex_string | types.storeHexString | ported | types/hex-lowercase | phys-address, mac-address, hex-string, uuid |
| src/plugins_types/date.c, time.c, time_period.c, xpath1.0.c, node_instanceid.c, instanceid_keys.c | (all) | — | skipped | — | U-0010 |
| src/xpath.c | lyxp_expr_parse, eval_number | xpath.Compile, lex, parseNumberToken | ported | protocol-v2/xpath-* | number tokens as x87 long double (`ld`, D-0010); tokenizer incl. NodeType/FunctionName disambiguation and its error messages; truncates at NUL like the C string |
| src/xpath.c | parse_ncname, ly_getutf8 (ly_common.c), is_xmlqname*char (xml.h) | parseNCName, getUTF8, isQNameStart, isQNameChar | ported | protocol-v2/xpath-* |  |
| src/xpath.c | expr_parse_axis | axisNames | ported | protocol-v2/xpath-* | `namespace::` rejected as invalid character, as libyang |
| src/xpath.c | reparse_or_expr … reparse_unary_expr | parser.orExpr … parser.unaryExpr | replaced | protocol-v2/xpath-* | recursive descent building an AST instead of the token `repeat` array; each precedence level is an operand list (chainExpr) evaluated iteratively; same errors, depth limit LYXP_MAX_BLOCK_DEPTH = 100, token cap MaxTokens (U-0003) |
| src/xpath.c | reparse_path_expr, reparse_relative_location_path, reparse_absolute_location_path, reparse_predicate | parser.pathExpr, parser.relPath, parser.predicates | replaced | protocol-v2/xpath-* |  |
| src/xpath.c | reparse_function_call | parser.call | ported | protocol-v2/xpath-* | unknown function and arity are compile-time errors |
| src/xpath.c | lyxp_check_token, exp_check_token2, lyxp_token2str | parser.check, parser.peek, tokNames | ported | protocol-v2/xpath-* |  |
| src/context.c | ly_ctx_new (internal modules), ly_ctx_load_module (parse part) | compile.NewContext, compile.Context.Load, yang.NewContext, yang.Context.Load | partial | load/* | features, compile, dep sets: C1b |
| src/context.c | ly_ctx_get_module, ly_ctx_get_module_latest(_ns), ly_ctx_get_module_implemented, ly_ctx_get_submodule(2)_latest | Context.module, latest, latestBy, implemented, submoduleLatest | ported | load/* | |
| src/tree_schema.c | lys_search_localfile, lys_search_localfile_file_type | Context.search | ported | load/symlink-dir, load/symlink-file | fs.FS; MaxSearchDirs (U-0022), `.yin` → ErrUnsupported (U-0021) |
| src/tree_schema.c | lys_parse_in (up to the name-collision checks), lys_parse_submodule | Context.parseModule, parseSubmodule | partial | load/* | ext plugin parse callbacks, P0, lysp_add_internal_*: C1b |
| src/tree_schema.c | lysp_resolve_import_include, ly_check_module_filename, lysp_load_module_data_check | Context.resolveImportsIncludes, checkFilename, checkLoadData | ported | load/import-cycle, load/filename-warning, load/wrong-revision-file | |
| src/tree_schema.c | lys_implement (collision check), _lys_set_implemented, lys_unres_glob_revert (created/implemented modules) | Context.implement, Context.Load | partial | load/imported-rev-binding | |
| src/tree_schema_common.c | lys_parse_load, lys_get_module_without_revision, lys_load_mod_from_clb_or_file, lys_check_circular_dependency | Context.parseLoad, withoutRevision, loadModule | ported | load/imported-rev-binding, load/import-not-found | |
| src/tree_schema_common.c | lysp_load_submodules, lysp_main_pmod_get_submodule, lysp_parsed_mods_get_submodule, lysp_inject_submodule, lysp_load_submod_from_clb_or_file | Context.loadSubmodules, mainSubmodule, parsedSubmodule, loadSubmodule | ported | load/include-errors, load/include-cycle | |
| src/tree_schema_common.c | lysp_check_dup_typedef(s), lysp_check_dup_grouping(s), lysp_check_dup_features, lysp_check_dup_identities | Context.checkDups | ported | load/dup-typedef-scopes, load/include-errors | tpdfs_nodes/grps_nodes order from the statement tree |
| src/tree_schema_common.c | lysp_last_revision, lys_check_date | Context.lastRevision, validDate | ported | load/imported-rev-binding | |
| src/parser_yang.c | parse_belongsto, parse_include, parse_module / parse_submodule (context name-collision checks, 1.1 include warning), yang_parse_module / yang_parse_submodule (kind check) | parser.ParseIn, parser.Context, checker.arg, checker.child, checker.close | ported | load/include-errors, load/submodule-collisions | the loader passes the context lookups; the checks run at libyang's position |
| src/tree_schema.c | lysp_resolve_ext_instance_records (definition and argument loop), lysp_ext_instance_path, lysp_ext_instance_path_stmt_append_r, lysp_path_until | Context.resolveExts, extPath, stmtPath, nodePath, parser.ExtInstances | partial | load/ext-instance-resolution | plugin parse callbacks: C1b |
| src/tree_schema_common.c | lysp_ext_find_definition, lysp_ext_instance_resolve_argument (YANG input), ly_schema_resolve_prefix | Context.resolveExts, prefixModule, findExtension | ported | load/ext-instance-resolution | YIN branch out of v1 |
| src/xpath.c | lyxp_eval, eval_expr_select | (*Expr).Eval, evaluator.eval | replaced | protocol-v2/xpath-* | AST walk; context set passed by value |
| src/xpath.c | eval_or_expr … eval_union_expr, eval_unary_expr | evaluator.binary, negExpr | ported | protocol-v2/xpath-* | lazy and/or; even number of '-' leaves the operand uncast |
| src/xpath.c | eval_path_expr, eval_relative_location_path, eval_absolute_location_path, eval_name_test_with_predicate, eval_node_type_with_predicate | evaluator.path, evaluator.step | ported | protocol-v2/xpath-* | '//' ignored before node()/text() as libyang; attribute axis empty (U-0002) |
| src/xpath.c | eval_predicate | evaluator.predicates | ported | protocol-v2/xpath-* | predicate applies to the whole step result; numeric predicate truncated |
| src/xpath.c | eval_function_call, eval_literal, eval_number, eval_variable_reference | evaluator.eval | ported | protocol-v2/xpath-* | variables always undefined (no variable API) |
| src/xpath.c | moveto_resolve_module | evaluator.resolveName | ported | protocol-v2/xpath-* | via NamespaceCtx |
| src/xpath.c | moveto_root, moveto_node, moveto_node_check, moveto_node_alldesc_child, xpath_pi_node, xpath_pi_text | evaluator.moveto, evaluator.check, evaluator.allDescChild, evaluator.text | ported | protocol-v2/xpath-* | when-false nodes invisible, unresolved when → ErrIncomplete, config-false hidden under RootConfig, other operations hidden (context_op) |
| src/xpath.c | eval_name_test_with_predicate_get_scnode, eval_name_test_with_predicate_find_scnode, moveto_node_hash_child | evaluator.schemaTarget, evaluator.hashChild | ported | protocol-v2/xpath-* | restricts a child NameTest to one schema node (unprefixed JSON names: the context node's module), first opaque node as fallback; via SchemaNode.Child / SchemaInfo.TopLevel |
| src/xpath.c | eval_name_test_try_compile_predicates, eval_name_test_try_compile_predicate_key/append | evaluator.hashPredicates, evaluator.atomsOK, evaluator.walkAtoms | partial | protocol-v2/xpath-aug-key-* | key module/axis checks ported; value atoms found by walking paths over the schema instead of lyxp_atomize (D-0013) |
| src/xpath.c | moveto_axis_node_next, moveto_axis_node_next_first, moveto_axis_node_next_dfs_forward/backward | evaluator.axis | ported | protocol-v2/xpath-* | libyang quirks kept: following/preceding empty without a sibling in that direction, preceding includes ancestors |
| src/xpath.c | set_sort, set_sort_compare, set_assign_pos, set_sorted_merge, moveto_union, set_dup_node_check | evaluator.index, evaluator.sortUnique, evaluator.key | replaced | protocol-v2/xpath-* | order by ancestor chain of sibling indexes; each sibling list numbered lazily once per Eval (no whole-tree walk); sorted after non-child axes and unions only, as libyang |
| src/xpath.c | lyxp_set_cast, cast_node_set_to_string, cast_string_elem, cast_string_recursive, cast_string_to_number | evaluator.toBool/toNum/toString/stringValue, cStrtod, numToString | ported | protocol-v2/xpath-* | libyang string-value (indented dump), %03.1Lf numbers, numbers as x87 long double (`ld`: parse, + - * div mod, conversions, %Lf printing), strtold incl. ERANGE (D-0010, D-0011) |
| src/xpath.c | moveto_op_comp, moveto_op_comp_item, moveto_num_cmp, set_comp_cast, set_comp_canonize | evaluator.compare, evaluator.compareItem, numCmp, cfmt | ported | protocol-v2/xpath-* | numbers compared as %Lf text; literal canonized by SchemaNode.Canonical |
| src/xpath.c | moveto_op_math | ldOp, ldMod (evaluator.chain) | ported | protocol-v2/xpath-* | long double arithmetic; fmodl exact via big.Int |
| src/xpath.c | xpath_* core functions (boolean … translate) | fn* in funcs.go | ported | protocol-v2/xpath-* | byte-based string functions, normalize-space pre-scan, truncating floor/round/ceiling with x86 integer-indefinite conversions (D-0011) |
| src/xpath.c | xpath_current, xpath_deref, xpath_deref_type | fnCurrent, fnDeref | partial | protocol-v2/xpath-* | leafref / instance-identifier resolution through EvalContext.Deref, targets unsorted as libyang Release; text() argument accepted |
| src/xpath.c | xpath_derived_, xpath_derived_from(_or_self), xpath_derived_ident_module | derived | ported | protocol-v2/xpath-* | stored value via Value.Identity, identities via SchemaInfo |
| src/xpath.c | xpath_enum_value, xpath_bit_is_set, xpath_re_match | fnEnumValue, fnBitIsSet, fnReMatch | ported | protocol-v2/xpath-* | stored value, text() arguments as libyang; re-match via internal/xsdre (D-0002…D-0008), patterns cached per Expr |
| src/xpath.c | xpath_lang, moveto_attr, moveto_attr_alldesc | fnLang | partial |  | no metadata in Node yet (U-0002) |
