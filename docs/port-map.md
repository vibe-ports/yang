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
