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
