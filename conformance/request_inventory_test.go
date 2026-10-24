// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// requestInventory records request reads in oracle/lyoracle.c, independently of
// the engine. Scopes are root ops, step:<kind>, edit:<nested-kind>, and target:<kind>.
// Dotted paths select nested objects; [] selects the first array entry. Helper
// str_of (193) treats absent/null as NULL and empty as present; wrong types die.
// flags_of (204) treats absent as zero; null, scalar and unknown/nonstring flags
// die. Bool fields reject null/nonbool; truth fields use cJSON_IsTrue instead.
// Enum fields first use str_of; missing/null use the documented default, while
// empty/bogus strings die unless a caller deliberately ignores their semantics.
// Input fields call input_of (260): NULL is absence, empty is a real memory input.
// Read lines may be shared by scopes; notes record conditional/phase differences.
// keys_only (1706-1722,1725-1735,1790,1808,1855,1925,1989) rejects ALL unlisted
// step/nested keys even when null. Root operations ignore all unlisted keys.
// Execution reads are skipped after a failed step; preflight reads are not.
type inventoryRow struct {
	scopes, key, lines, kind, valid, behavior string
}

var requestInventory = []inventoryRow{
	{"step:parse", "parent", "2524,2536,2537", "string", "", "Execution-only parent selector: operation data only, excludes rpc and requires parse_only; parsed into the retained node."},
	{"step:parse", "rpc", "2533", "input", "", "Execution: parent excludes a separate rpc input."},
	{"step:parse", "data", "2539", "input", "", "Execution: input parsed into the retained parent."},
	{"schema data xpath diff sequence atoms", "op", "3301", "enum", "schema data xpath diff sequence atoms", "Required dispatch; absent/null missing op; empty/bogus unknown op; checked before context."},
	{"schema data xpath diff sequence atoms", "base_dir", "3303", "directory", "", "Absent/null keep process directory; empty/bogus cannot chdir; valid directory selects file/search base before dispatch."},
	{"step:change_term", "node_xpath", "1873,3084", "string", "", "Exactly one of node/node_xpath must be non-null; execution XPath must select exactly one retained node."},
	{"step:change_term", "child", "1870,3097", "positive-integer", "", "Optional one-based actual child position; present requires an integer from 1 through INT32_MAX; execution requires that child to exist."},
	{"step:edit", "merge_self", "1728,1777,2602", "bool", "", "Exactly one edit; present must be true. Recursively duplicate retained siblings and merge them back during execution."},
	{"step:xpath", "vars", "2086,2956", "object", "", "Absent no variables; present null/nonobject request-error; every value must be a string XPath expression."},
	{"step:xpath", "vars.v", "2086,2956", "var", "", "Dynamic key v represents ANY variable name; non-string including null request-error; string values are evaluated as XPath expressions."},
	{"schema data xpath diff sequence atoms", "context_options", "1094", "flags", "all_implemented ref_implemented no_yanglibrary enable_imp_features compile_obsolete leafref_extended leafref_linking builtin_plugins_only pattern_compat", "Context creation; pattern_compat maps to zero in C. Target context is execution-only after step preflight."},
	{"schema data xpath diff sequence atoms", "searchdirs", "1101", "iteration", "", "cJSON_ArrayForEach: absent/null/scalars have no children; arrays and object values iterate; every child must name an accessible directory or request-error."},
	{"schema data xpath diff sequence atoms", "modules", "1091", "iteration", "", "cJSON_ArrayForEach: absent/null/scalars load none; array/object children each read as a module. Load failures are schema-error (invalid for schema op)."},
	{"schema data xpath diff sequence atoms", "modules[].name", "1113", "string", "", "Required (absent/null request-error); empty or missing module fails schema load; valid pv2 loads schema."},
	{"schema data xpath diff sequence atoms", "modules[].revision", "1113", "string", "", "Absent/null choose latest; empty string passed literally to module loader; malformed revision or missing revision fails load."},
	{"schema data xpath diff sequence atoms", "modules[].features", "1114", "features", "", "Absent passes NULL; present null/scalars pass an empty NULL-terminated list; array/object children valuestring used without type validation. Non-string children terminate the list; strings enable named features; * enables all."},
	{"target:dup target:diff", "context_options", "1094", "flags", "all_implemented ref_implemented no_yanglibrary enable_imp_features compile_obsolete leafref_extended leafref_linking builtin_plugins_only pattern_compat", "Context creation; pattern_compat maps to zero in C. Target context is execution-only after step preflight."},
	{"target:dup target:diff", "searchdirs", "1101", "iteration", "", "cJSON_ArrayForEach: absent/null/scalars have no children; arrays and object values iterate; every child must name an accessible directory or request-error."},
	{"target:dup target:diff", "modules", "1091", "iteration", "", "cJSON_ArrayForEach: absent/null/scalars load none; array/object children each read as a module. Load failures are schema-error (invalid for schema op)."},
	{"target:dup target:diff", "modules[].name", "1113", "string", "", "Required (absent/null request-error); empty or missing module fails schema load; valid pv2 loads schema."},
	{"target:dup target:diff", "modules[].revision", "1113", "string", "", "Absent/null choose latest; empty string passed literally to module loader; malformed revision or missing revision fails load."},
	{"target:dup target:diff", "modules[].features", "1114", "features", "", "Absent passes NULL; present null/scalars pass an empty NULL-terminated list; array/object children valuestring used without type validation. Non-string children terminate the list; strings enable named features; * enables all."},
	{"data xpath diff", "data_type", "1210,1223", "enum", "config data-operational data get getconfig edit rpc reply notif rpc-netconf notif-netconf reply-netconf rpc-restconf notif-restconf reply-restconf", "Absent/null data-operational. Presets 1223-1244; envelopes require parse_only. Top diff, insert, compare, diff_parse/merge restrict to datastore; parse/diff operation steps require parse_only. Validate only uses vopts; edit merge only uses popts."},
	{"data xpath diff", "unknown", "1210,1216", "enum", "reject skip opaque", "Absent/null reject; selects STRICT/zero/OPAQ. Ignored by validate."},
	{"data xpath diff", "format", "1214", "enum", "json xml", "Absent/null json; ignored by validate."},
	{"data xpath diff", "parse_only", "1219", "truth", "", "Only JSON true sets flag; all other types false. Datastore sets ONLY; operation validation disabled only by true."},
	{"data xpath diff", "keep_input", "1220", "truth", "", "Only true retains RPC children; all other types false. Ignored without an RPC input and for datastore."},
	{"data xpath diff", "rpc_parent", "1221,1281,1308", "string", "", "Absent/null no selector; non-null requires RPC input, keep_input true, data_type rpc; empty/value looked up during execution."},
	{"data xpath diff", "parse_options", "1254,1255", "flags", "only strict opaq no_state ordered when_true store_only json_null json_string_datatypes anydata_strict", "strict/opaq request-error (use unknown). All others OR into presets. Operation parser masks to STRICT/OPAQ, discarding ALL extra flags; validate step ignores popts."},
	{"data xpath diff", "validate_options", "1259", "flags", "no_state present multi_error operational no_defaults not_final", "OR into defaults. Ignored by operation parser/validator, parse-only datastore and edit merge. validate step passes vopts."},
	{"step:parse step:validate step:edit step:insert step:compare step:diff step:diff_parse step:diff_merge", "data_type", "1210,1223", "enum", "config data-operational data get getconfig edit rpc reply notif rpc-netconf notif-netconf reply-netconf rpc-restconf notif-restconf reply-restconf", "Absent/null data-operational. Presets 1223-1244; envelopes require parse_only. Top diff, insert, compare, diff_parse/merge restrict to datastore; parse/diff operation steps require parse_only. Validate only uses vopts; edit merge only uses popts."},
	{"step:parse step:validate step:edit step:insert step:compare step:diff step:diff_parse step:diff_merge", "unknown", "1210,1216", "enum", "reject skip opaque", "Absent/null reject; selects STRICT/zero/OPAQ. Ignored by validate."},
	{"step:parse step:validate step:edit step:insert step:compare step:diff step:diff_parse step:diff_merge", "format", "1214", "enum", "json xml", "Absent/null json; ignored by validate."},
	{"step:parse step:validate step:edit step:insert step:compare step:diff step:diff_parse step:diff_merge", "parse_only", "1219", "truth", "", "Only JSON true sets flag; all other types false. Datastore sets ONLY; operation validation disabled only by true."},
	{"step:parse step:validate step:edit step:insert step:compare step:diff step:diff_parse step:diff_merge", "keep_input", "1220", "truth", "", "Only true retains RPC children; all other types false. Ignored without an RPC input and for datastore."},
	{"step:parse step:validate step:edit step:insert step:compare step:diff step:diff_parse step:diff_merge", "rpc_parent", "1221,1281,1308", "string", "", "Absent/null no selector; non-null requires RPC input, keep_input true, data_type rpc; empty/value looked up during execution."},
	{"step:parse step:validate step:edit step:insert step:compare step:diff step:diff_parse step:diff_merge", "parse_options", "1254,1255", "flags", "only strict opaq no_state ordered when_true store_only json_null json_string_datatypes anydata_strict", "strict/opaq request-error (use unknown). All others OR into presets. Operation parser masks to STRICT/OPAQ, discarding ALL extra flags; validate step ignores popts."},
	{"step:parse step:validate step:edit step:insert step:compare step:diff step:diff_parse step:diff_merge", "validate_options", "1259", "flags", "no_state present multi_error operational no_defaults not_final", "OR into defaults. Ignored by operation parser/validator, parse-only datastore and edit merge. validate step passes vopts."},
	{"data xpath diff", "operational", "1400", "input", "", "Absent/null skip load; empty/value parse with ONLY and unknown skip; failure operational-error before body."},
	{"data xpath diff", "operational_format", "1401", "enum", "json xml", "str_of always checked. Absent/null inherits format. Enum checked only when operational input exists; otherwise every string ignored."},
	{"data", "data", "1448", "input", "", "Required input: absent/null request-error."},
	{"data", "rpc", "1451", "input", "", "Optional: absent/null no input. xpath passes NULL to parse_one; diff data read during execution (preflight only with target); insert falls back to retained node; edit needs exactly one edit. RPC allowed only for reply steps, ignored for top-level datastore/notif."},
	{"xpath", "data", "1529", "input", "", "Optional: absent/null no input. xpath passes NULL to parse_one; diff data read during execution (preflight only with target); insert falls back to retained node; edit needs exactly one edit. RPC allowed only for reply steps, ignored for top-level datastore/notif."},
	{"diff", "first", "1645", "input", "", "Required input: absent/null request-error."},
	{"diff", "second", "1646", "input", "", "Required input: absent/null request-error."},
	{"step:parse", "data", "1750,2546", "input", "", "Required input: absent/null request-error."},
	{"step:parse", "rpc", "1747,2546", "input", "", "Optional: absent/null no input. xpath passes NULL to parse_one; diff data read during execution (preflight only with target); insert falls back to retained node; edit needs exactly one edit. RPC allowed only for reply steps, ignored for top-level datastore/notif."},
	{"step:insert", "data", "1958,2401", "input", "", "Optional: absent/null no input. xpath passes NULL to parse_one; diff data read during execution (preflight only with target); insert falls back to retained node; edit needs exactly one edit. RPC allowed only for reply steps, ignored for top-level datastore/notif."},
	{"step:compare", "data", "3336,1987,2497", "input", "", "Exactly one of data and insert_opaq is required."},
	{"step:diff", "data", "2037,2802", "input", "", "Optional: absent/null no input. xpath passes NULL to parse_one; diff data read during execution (preflight only with target); insert falls back to retained node; edit needs exactly one edit. RPC allowed only for reply steps, ignored for top-level datastore/notif."},
	{"step:diff", "rpc", "2013,2829", "input", "", "Optional: absent/null no input. xpath passes NULL to parse_one; diff data read during execution (preflight only with target); insert falls back to retained node; edit needs exactly one edit. RPC allowed only for reply steps, ignored for top-level datastore/notif."},
	{"step:diff_parse step:diff_merge", "data", "2007,2869,2894", "input", "", "Required input: absent/null request-error."},
	{"step:edit", "merge", "1768,2590", "input", "", "Optional: absent/null no input. xpath passes NULL to parse_one; diff data read during execution (preflight only with target); insert falls back to retained node; edit needs exactly one edit. RPC allowed only for reply steps, ignored for top-level datastore/notif."},
	{"data", "print_subtree", "1441", "string", "", "Optional path selects output subtree after parse."},
	{"data step:dump step:detached_dump", "with_defaults", "1364,1442,1889,3161,3183,3181,3182", "enum", "explicit trim all all-tagged implicit-tagged", "Absent/null explicit; changes printed defaults."},
	{"step:detached_dump", "child", "1889,3181", "bool", "true false", "Inspect the first child of the retained detached root."},
	{"data", "print_options", "1442", "flags", "shrink empty_leaf_list empty_cont json_no_nested_prefix", "OR into printer options."},
	{"diff", "diff_options", "1657", "flags", "defaults meta", "Execution only after BOTH inputs parse successfully."},
	{"xpath", "xpath", "1495", "string", "", "Required; empty/bogus evaluated after parsing data; parse failure bypasses evaluation."},
	{"xpath", "context_path", "1495,1574", "string", "", "Optional; execution XPath must select exactly one node or request-error."},
	{"xpath", "cur_module", "1495", "string", "", "Optional; after successful data parse must name implemented module (empty fails)."},
	{"xpath", "xpath_format", "1500", "enum", "json xml schema", "Absent/null json; enum checked only after successful data parse."},
	{"xpath", "namespaces", "1503", "array", "", "Presence requires array even null; checked after successful parse; permitted only with xpath_format xml."},
	{"xpath", "namespaces[].prefix", "1547", "string", "", "Only read with xpath_format xml after successful data parse; absent/null/empty use default namespace."},
	{"xpath", "namespaces[].uri", "1547", "string", "", "Only read with xpath_format xml; absent/null request-error; empty is a valid string."},
	{"xpath step:trim", "vars", "1510,2060,2920", "object", "", "Absent no variables; present null/nonobject request-error; every value must be string (including empty)."},
	{"xpath step:trim", "vars.v", "1510,2060,2920", "var", "", "Dynamic key v represents ANY variable name; non-string including null request-error; absent no binding; string including empty accepted."},
	{"sequence", "steps", "3031", "array", "", "Required array, including empty. All steps preflight before execution."},
	{"atoms", "xpath", "3218", "string", "", "Exactly one of xpath/path non-null. Evaluates schema atoms."},
	{"atoms", "path", "3218", "string", "", "Exactly one of xpath/path non-null. Evaluates compiled path atoms; only output option used."},
	{"atoms", "context_path", "3218,3235", "string", "", "Optional schema node; not found (including empty) request-error."},
	{"atoms", "atom_options", "3219", "flags", "schema output no_match_error", "Absent zero. Path ignores schema/no_match_error; xpath uses all flags."},
	{"step:parse step:validate step:edit step:change_term step:dump step:print_xml step:any_direct step:dup step:insert step:compare step:diff step:diff_parse step:diff_merge step:diff_apply step:diff_reverse step:trim step:xpath step:link step:links step:unlink step:find_meta", "do", "1700,3049", "enum", "parse validate edit change_term dump print_xml any_direct dup insert compare diff diff_parse diff_merge diff_apply diff_reverse trim xpath link links unlink find_meta", "Required string; absent/null step without do; unknown string request-error after keys_only. All step keys restricted at 1706-1725."},
	{"step:any_direct", "case", "1894,2198", "enum", "nil_boundaries text_nul node_then_empty equal_repeated_collisions equal_repeated_date_collisions equal_mixed_parent_payload dup_payload_parent_context trim_payload_links merge_mixed_into_tree merge_opaque_mixed_schema_parents dup_siblings_into_empty_anydata dup_siblings_into_empty_anyxml dup_into_default_any_parent", "Required direct-call probe selector."},
	{"step:any_direct", "target", "1893,2322,3425", "object", "", "Optional second context; required by the cross-context probes."},
	{"target:any_direct", "context_options", "1094", "flags", "all_implemented ref_implemented no_yanglibrary enable_imp_features compile_obsolete leafref_extended leafref_linking builtin_plugins_only pattern_compat", "Second context creation for pointer-level probes."},
	{"target:any_direct", "searchdirs", "1101", "iteration", "", "Second context search paths."},
	{"target:any_direct", "modules", "1091", "iteration", "", "Second context modules."},
	{"target:any_direct", "modules[].name", "1113", "string", "", "Required target module name."},
	{"target:any_direct", "modules[].revision", "1113", "string", "", "Optional target revision."},
	{"target:any_direct", "modules[].features", "1114", "features", "", "Optional target features."},
	{"step:validate", "module", "1755,2558", "string", "", "Optional; preflight lookup in original context; empty/unknown request-error."},
	{"step:validate", "final_only", "1754,1758,2559", "bool", "", "Absent false; true requires module and uses final-only validator."},
	{"step:change_term", "node", "1875,3109,1873,3095,3083", "string", "", "Required; execution lookup must find node."},
	{"step:change_term", "value", "1875,3111,1873", "string", "", "Required string including empty; change term during execution."},
	{"step:change_term", "canon", "1869,3110", "bool", "", "Absent false; true selects canonical value API."},
	{"step:dup", "node", "1917,2121,1921,2165,1918", "string", "", "Required unless detached is true; execution selects the attached node to duplicate."},
	{"step:dup", "parent", "1924,1935,2121", "string", "", "Optional; cannot coexist with target; selects parent during execution."},
	{"step:dup", "options", "1925,2127", "flags", "recursive no_meta with_parents with_flags no_lyds", "Dup flags; absent zero."},
	{"step:dup", "siblings", "1913,1926,2128", "bool", "", "Absent false; true duplicates siblings."},
	{"step:dup", "target", "1932,1933,1938,2122,1945", "object", "", "Presence requires context object and no parent; allowed keys searchdirs modules context_options; built during execution."},
	{"step:insert", "node", "1953,1966,2401,1972", "string", "", "Required unless data input or detached is present; absent with data selects its first source node; empty path passed to lookup."},
	{"step:insert", "parent", "1954,2401", "string", "", "Optional destination parent; absent/null top level; empty still looked up."},
	{"step:compare", "first", "1991,2477", "string", "", "Optional path in retained tree; absent first sibling."},
	{"step:compare", "second", "1992,2477", "string", "", "Optional path in parsed tree; absent first sibling."},
	{"step:compare", "options", "1993,2508", "flags", "full_recursion defaults opaq", "Comparison flags."},
	{"step:compare", "insert_opaq", "3335", "object", "", "Exactly one of data and insert_opaq is required; the latter constructs the second comparison root."},
	{"compare:insert_opaq", "name", "3353,3367,3370", "string", "", "Required opaque local name."},
	{"compare:insert_opaq", "module", "3353,3368,3371", "string", "", "Required module name or XML namespace."},
	{"compare:insert_opaq", "value", "3357,3367,3370", "string", "", "Optional opaque value."},
	{"compare:insert_opaq", "prefix", "3358,3367,3370", "string", "", "Optional opaque prefix."},
	{"compare:insert_opaq", "xml", "3359", "bool", "", "Absent false selects lyd_new_opaq; true selects lyd_new_opaq2."},
	{"step:diff", "target", "2011,2040,2801", "object", "", "Presence requires context object, datastore type, data input. Build target during execution."},
	{"step:diff", "keep_input", "2012,2022", "truth", "", "Presence (even null/false) requires rpc; true keeps request children."},
	{"step:diff", "options", "2026,2805", "flags", "defaults meta", "Diff flags."},
	{"step:diff", "merge_options", "2027,2846", "flags", "defaults", "Syntax always checked; used only with merge true."},
	{"step:diff", "node", "2028,2802", "string", "", "Optional retained node selector."},
	{"step:diff", "data_node", "2029,2802", "string", "", "Optional second-tree selector."},
	{"step:diff", "single", "2042,2835", "bool", "", "Absent false; true diff_tree instead of siblings."},
	{"step:diff", "merge", "2043,2844", "bool", "", "Absent false; true merges result into diff register."},
	{"step:diff_merge", "options", "2048,2888", "flags", "defaults", "Diff merge flags."},
	{"step:diff_merge", "module", "2049,2053,2779", "string", "", "Optional implemented module; cannot coexist with src_node; lookup execution-only."},
	{"step:diff_merge", "parent", "2050,2887", "string", "", "Optional selector requires src_node; execution lookup."},
	{"step:diff_merge", "src_node", "2050,2053,2887", "string", "", "Optional source selector; mutually exclusive with module."},
	{"step:diff_apply", "module", "2058,2779", "string", "", "Optional module; lookup during execution."},
	{"step:trim", "xpath", "2062,2925", "string", "", "Required expression; execution trim."},
	{"step:edit", "set", "1729,1769,1855,2591,1728", "object", "", "Exactly one edit. Presence selects keys_only do + this key (1727-1736); null counts present except delete/merge use str_of. Nested object checked preflight; delete needs string."},
	{"step:edit", "delete", "1730,1768,2590", "string", "", "Exactly one edit. Presence selects keys_only do + this key (1727-1736); null counts present except delete/merge use str_of. Nested object checked preflight; delete needs string."},
	{"step:edit", "insert_term", "1731,1770,2592", "object", "", "Exactly one edit. Presence selects keys_only do + this key (1727-1736); null counts present except delete/merge use str_of. Nested object checked preflight; delete needs string."},
	{"step:edit", "insert_inner", "1732,1771,2593", "object", "", "Exactly one edit. Presence selects keys_only do + this key (1727-1736); null counts present except delete/merge use str_of. Nested object checked preflight; delete needs string."},
	{"step:edit", "insert_list", "1733,1772,2594", "object", "", "Exactly one edit. Presence selects keys_only do + this key (1727-1736); null counts present except delete/merge use str_of. Nested object checked preflight; delete needs string."},
	{"step:edit", "insert_list2", "1734,1773,2595", "object", "", "Exactly one edit. Presence selects keys_only do + this key (1727-1736); null counts present except delete/merge use str_of. Nested object checked preflight; delete needs string."},
	{"step:edit", "insert_opaq", "1735,1774,2596", "object", "", "Exactly one edit. Presence selects keys_only do + this key (1727-1736); null counts present except delete/merge use str_of. Nested object checked preflight; delete needs string."},
	{"step:edit", "new_meta", "1736,1775,2597", "object", "", "Exactly one edit. Presence selects keys_only do + this key (1727-1736); null counts present except delete/merge use str_of. Nested object checked preflight; delete needs string."},
	{"step:edit", "free_meta", "1737,1776,2598", "object", "", "Exactly one edit. Presence selects keys_only do + this key (1727-1736); null counts present except delete/merge use str_of. Nested object checked preflight; delete needs string."},
	{"edit:set", "path", "1855,2739", "string", "", "Required path; execution NewPath UPDATE."},
	{"edit:set", "value", "1862,2739", "string", "", "Optional NULL value; empty is literal empty value."},
	{"edit:set", "parent", "1863,2718", "string", "", "Optional node path; absent/null retains the first root, otherwise execution resolves the supplied parent (lyd_find_path, else the first node whose lyd_path matches)."},
	{"edit:set", "update", "1859,2719", "bool", "", "Absent/true LYD_NEW_PATH_UPDATE; false creates without update (an existing node is LY_EEXIST); non-bool request-error."},
	{"edit:new_meta edit:free_meta", "node", "1791,2614,2615", "string", "", "Required; execution node lookup."},
	{"edit:new_meta edit:free_meta", "name", "1791,2618,2620", "string", "", "Required module-qualified name containing colon (empty fails)."},
	{"edit:new_meta", "value", "1795,2618", "string", "", "Required including empty."},
	{"edit:insert_term edit:insert_inner edit:insert_list edit:insert_list2 edit:insert_opaq", "parent", "1807,2626", "string", "", "Optional; cannot coexist with parent_opaq; execution lookup."},
	{"edit:insert_term edit:insert_inner edit:insert_list edit:insert_list2 edit:insert_opaq", "parent_opaq", "1808,2626", "string", "", "Optional opaque parent XPath; exactly one node during execution."},
	{"edit:insert_term edit:insert_inner edit:insert_list edit:insert_list2 edit:insert_opaq", "name", "1809,2627", "string", "", "Required local node name including empty; schema lookup execution."},
	{"edit:insert_term edit:insert_inner edit:insert_list edit:insert_list2 edit:insert_opaq", "module", "1820,1827,2626", "string", "", "Required for opaq; otherwise needs module or either parent. Implemented-module lookup execution; opaq uses namespace string."},
	{"edit:insert_term", "value", "1833,2648", "string", "", "Required including empty; value parsed during execution."},
	{"edit:insert_term edit:insert_list edit:insert_list2", "options", "1830,2629", "flags", "output store_only canon", "Creation flags; absent zero."},
	{"edit:insert_inner", "options", "1830,2629", "flags", "output", "Output selector; absent zero."},
	{"edit:insert_list", "keys", "1839,2656", "keys", "", "Absent no keys; present must be array <=16 strings/nulls. NULL key values passed into lyd_new_list."},
	{"edit:insert_list2", "keys", "1837,2652", "string", "", "Optional predicate string; absent/null passed as NULL."},
	{"edit:insert_opaq", "value", "1818,2673,2675", "string", "", "Optional value."},
	{"edit:insert_opaq", "prefix", "1819,2673,2675", "string", "", "Optional prefix."},
	{"edit:insert_opaq", "xml", "1823,2672", "bool", "", "Absent false JSON namespace; true XML namespace."},
	{"step:parse step:validate step:edit step:change_term step:dump step:dup step:insert step:compare step:diff step:diff_parse step:diff_merge step:diff_apply step:diff_reverse step:trim step:link step:links step:xpath", "do", "1700,3049", "enum", "parse validate edit change_term dump dup insert compare diff diff_parse diff_merge diff_apply diff_reverse trim link links xpath", "Required string; absent/null step without do; unknown string request-error after keys_only. All step keys restricted at 1706-1722."},
	{"step:dup", "node_xpath", "1918,1921,2121", "string", "", "Alternative single-node XPath selector, including operation output nodes."},
	{"step:dup", "source", "1914,1940,2123", "object", "", "If present, requires an object with data and parse_only true; excludes target. Its data is checked during preflight, but parse parameters are checked during execution."},
	{"step:dup", "source.format", "1214,2135", "enum", "json xml", "Absent/null defaults to json; nonstring, empty or unknown strings fail during execution before source parsing."},
	{"step:dup", "source.data_type", "1210,1223,2135", "enum", "config data-operational data get getconfig edit rpc reply notif rpc-netconf notif-netconf reply-netconf rpc-restconf notif-restconf reply-restconf", "Absent/null defaults to data-operational. Type/enum checks happen during execution before source parsing; envelopes require parse_only true."},
	{"step:dup", "source.unknown", "1210,1216,2135", "enum", "reject skip opaque", "Absent/null defaults to reject; nonstring, empty or unknown strings fail during execution before source parsing."},
	{"step:dup", "source.data", "1941,2136", "input", "", "Required input, read during preflight before keys_only and target exclusion; reread after dparams_of during execution. Empty is present; parse failure stops selection and duplication."},
	{"step:dup", "source.parse_only", "1219,1941", "truth", "", "Must be true, so an operation source is parsed without validation."},
	{"step:dup", "source.rpc", "2136", "input", "", "Optional request tree for a separately parsed reply; read after source parameters and data at execution, with no preflight reply-only restriction."},
	{"step:dup", "source.keep_input", "1220,2135", "truth", "", "Only JSON true retains RPC input children; other values are false and are not type-checked."},
	{"step:compare", "self", "1980,2483", "truth", "", "Only JSON true compares two nodes in the retained tree, permitting only do/self/first/second/options; other values preserve parsed-tree comparison."},
	{"step:xpath", "xpath", "2062,2926,2076,2967", "string", "", "Required expression with optional vars; evaluated against the retained tree from the node at node, the detached node when selected, or the tree's first node. Empty tree returns LY_EINVAL and null result without diagnostics."},
	{"step:insert", "target", "1951,2402", "object", "", "Optional source context when inserting parsed data."},
	{"step:dup step:insert step:links step:diff", "detached", "1915,1950,2044,2105,2131,2409,2809,3073", "bool", "", "Absent false; true selects the node/subtree retained by unlink. Insert clears the retained register on success; diff uses it as the first input."},
	{"step:compare", "target", "1978,2478", "object", "", "Optional source context for cross-context comparison."},
	{"step:diff_merge", "detached_parent", "2048,2050,2898", "bool", "true false", "Use the retained detached node as diff_merge_tree parent."},
	{"step:xpath", "node", "2079,2080,2938", "string", "", "Optional attached XPath context node; @last selects the final root, including one from a foreign context."},
	{"step:xpath", "detached", "2074,2938,2961,2966", "bool", "true false", "Use the unlinked node as XPath context; absolute paths still use the retained tree. Mutually exclusive with node."},
	{"step:unlink", "node", "2096,3131", "string", "", "Required unless diff_node is set; execution unlinks and retains the selected data node."},
	{"step:unlink", "diff_node", "2096,3131", "string", "", "Required unless node is set; execution unlinks and retains the selected diff-register node."},
	{"step:find_meta", "node", "2100,3146", "string", "", "Optional attached-node selector; absent uses the node retained by unlink."},
	{"step:find_meta", "name", "2101,3153", "string", "", "Required metadata name; execution calls lyd_find_meta."},
	{"step:edit", "merge_into", "1778,2683,2689", "string", "", "Parse a destination in the retained tree context and merge the retained tree into it."},
	{"edit:insert_term edit:insert_inner edit:insert_list", "detached_parent", "1808,2640", "bool", "true false", "Select the node retained by unlink as the constructor parent; mutually exclusive with parent and parent_opaq."},
	{"data xpath diff", "operational_file", "1400,260", "file", "", "str_of null semantics; empty is a filename and fails open. Any present inline + file (including empty) conflicts before read; valid file supplies its bytes. Absent/null skip load; empty/value parse with ONLY and unknown skip; failure operational-error before body."},
	{"data", "data_file", "1448,260", "file", "", "str_of null semantics; empty is a filename and fails open. Any present inline + file (including empty) conflicts before read; valid file supplies its bytes. Required input: absent/null request-error."},
	{"data", "rpc_file", "1451,260", "file", "", "str_of null semantics; empty is a filename and fails open. Any present inline + file (including empty) conflicts before read; valid file supplies its bytes. Optional: absent/null no input. xpath passes NULL to parse_one; diff data read during execution (preflight only with target); insert falls back to retained node; edit needs exactly one edit. RPC allowed only for reply steps, ignored for top-level datastore/notif."},
	{"xpath", "data_file", "1529,260", "file", "", "str_of null semantics; empty is a filename and fails open. Any present inline + file (including empty) conflicts before read; valid file supplies its bytes. Optional: absent/null no input. xpath passes NULL to parse_one; diff data read during execution (preflight only with target); insert falls back to retained node; edit needs exactly one edit. RPC allowed only for reply steps, ignored for top-level datastore/notif."},
	{"diff", "first_file", "1645,260", "file", "", "str_of null semantics; empty is a filename and fails open. Any present inline + file (including empty) conflicts before read; valid file supplies its bytes. Required input: absent/null request-error."},
	{"diff", "second_file", "1646,260", "file", "", "str_of null semantics; empty is a filename and fails open. Any present inline + file (including empty) conflicts before read; valid file supplies its bytes. Required input: absent/null request-error."},
	{"step:parse", "data_file", "1750,2546,260", "file", "", "str_of null semantics; empty is a filename and fails open. Any present inline + file (including empty) conflicts before read; valid file supplies its bytes. Required input: absent/null request-error."},
	{"step:parse", "rpc_file", "1747,2546,260", "file", "", "str_of null semantics; empty is a filename and fails open. Any present inline + file (including empty) conflicts before read; valid file supplies its bytes. Optional: absent/null no input. xpath passes NULL to parse_one; diff data read during execution (preflight only with target); insert falls back to retained node; edit needs exactly one edit. RPC allowed only for reply steps, ignored for top-level datastore/notif."},
	{"step:insert", "data_file", "1958,2401,260", "file", "", "str_of null semantics; empty is a filename and fails open. Any present inline + file (including empty) conflicts before read; valid file supplies its bytes. Optional: absent/null no input. xpath passes NULL to parse_one; diff data read during execution (preflight only with target); insert falls back to retained node; edit needs exactly one edit. RPC allowed only for reply steps, ignored for top-level datastore/notif."},
	{"step:compare", "data_file", "3336,260,1987,2497", "file", "", "str_of null semantics; empty is a filename and fails open. Any present inline + file (including empty) conflicts before read; valid file supplies its bytes. Exactly one of data and insert_opaq is required."},
	{"step:diff", "data_file", "2037,2802,260", "file", "", "str_of null semantics; empty is a filename and fails open. Any present inline + file (including empty) conflicts before read; valid file supplies its bytes. Optional: absent/null no input. xpath passes NULL to parse_one; diff data read during execution (preflight only with target); insert falls back to retained node; edit needs exactly one edit. RPC allowed only for reply steps, ignored for top-level datastore/notif."},
	{"step:diff", "rpc_file", "2013,2829,260", "file", "", "str_of null semantics; empty is a filename and fails open. Any present inline + file (including empty) conflicts before read; valid file supplies its bytes. Optional: absent/null no input. xpath passes NULL to parse_one; diff data read during execution (preflight only with target); insert falls back to retained node; edit needs exactly one edit. RPC allowed only for reply steps, ignored for top-level datastore/notif."},
	{"step:diff_parse step:diff_merge", "data_file", "2007,2869,2894,260", "file", "", "str_of null semantics; empty is a filename and fails open. Any present inline + file (including empty) conflicts before read; valid file supplies its bytes. Required input: absent/null request-error."},
	{"step:edit", "merge_file", "1768,2590,260", "file", "", "str_of null semantics; empty is a filename and fails open. Any present inline + file (including empty) conflicts before read; valid file supplies its bytes. Optional: absent/null no input. xpath passes NULL to parse_one; diff data read during execution (preflight only with target); insert falls back to retained node; edit needs exactly one edit. RPC allowed only for reply steps, ignored for top-level datastore/notif."},
}

// inventoryCase is a mutation location in a valid small request. Prefix steps
// establish a retained tree; suffix steps expose whether execution stopped.
type inventorySeed struct {
	name, scope string
	params      map[string]any
	path        []string
	moduleIndex int // modules[] mutations select this entry; zero is the ordinary seed
}

type inventoryValue struct {
	name  string
	value any
}

func inventoryCommon(op string) map[string]any {
	return map[string]any{"op": op, "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}}, "context_options": []any{"no_yanglibrary"}}
}

const inventoryData = `{"pv2:c":{"mode":"on","l":[{"k":"a"}]}}`
const inventoryRPC = `{"pv2:ping":{"n":1}}`
const inventoryStateData = `{"pv2:c":{"stats":[{"hits":1}]}}`
const inventoryFalseWhenData = `{"pv2:c":{"mode":"off","x":"remove"}}`

func inventorySeeds() []inventorySeed {
	var out []inventorySeed
	for _, op := range strings.Fields("schema data xpath diff sequence atoms") {
		p := inventoryCommon(op)
		switch op {
		case "data":
			p["data"], p["parse_only"] = inventoryData, true
		case "xpath":
			p["data"], p["parse_only"], p["xpath"] = inventoryData, true, "/pv2:c"
		case "diff":
			p["first"], p["second"], p["parse_only"] = inventoryData, inventoryData, true
		case "sequence":
			p["steps"] = []any{map[string]any{"do": "dump"}}
		case "atoms":
			p["xpath"] = "/pv2:c"
		}
		out = append(out, inventorySeed{op, op, p, nil, 0})
	}
	// Exercise repeated-load state: nested module fields mutate the second entry only,
	// after pv2 has already been implemented by the first entry.
	repeated := inventoryCommon("schema")
	repeated["modules"] = []any{map[string]any{"name": "pv2"}, map[string]any{"name": "pv2"}}
	out = append(out, inventorySeed{"schema/repeated-load", "schema", repeated, nil, 1})
	// These roots make dispatch isolation observable. no_state rejects pv2:stats (config false)
	// for all three callers of parse_one; when_true marks pv2:x as previously valid so its false
	// when auto-deletes it during validation (libyang parser_common.c:295-303,
	// validation.c:479-515). Without when_true the same new node is invalid.
	for _, op := range []string{"data", "xpath", "diff"} {
		p := inventoryCommon(op)
		p["data_type"], p["validate_options"] = "data", []any{"no_state"}
		switch op {
		case "data":
			p["data"] = inventoryStateData
		case "xpath":
			p["data"], p["xpath"] = inventoryStateData, "/pv2:c/stats"
		case "diff":
			p["first"], p["second"] = inventoryStateData, inventoryStateData
		}
		out = append(out, inventorySeed{op + "/state-no-state", op, p, nil, 0})
	}
	for _, prior := range []bool{false, true} {
		p := inventoryCommon("data")
		p["data_type"], p["data"] = "data", inventoryFalseWhenData
		name := "data/when-false"
		if prior {
			name = "data/when-true-false-when"
			p["parse_options"] = []any{"when_true"}
		}
		out = append(out, inventorySeed{name, "data", p, nil, 0})
	}
	for _, typ := range strings.Fields("data-operational data config get getconfig edit rpc reply notif notif-netconf notif-restconf") {
		input, format := inventoryData, "json"
		switch typ {
		case "rpc":
			input = inventoryRPC
		case "reply":
			input = `{"pv2:ping":{"rtt":1}}`
		case "notif":
			input = `{"pv2:alarm":{"sev":1}}`
		case "notif-netconf":
			format, input = "xml", `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2092-01-01T00:00:00Z</eventTime><alarm xmlns="urn:vibe-ports:yang:conformance:pv2"><sev>1</sev></alarm></notification>`
		case "notif-restconf":
			input = `{"ietf-restconf:notification":{"eventTime":"2092-01-01T00:00:00Z","pv2:alarm":{"sev":1}}}`
		}
		p := inventoryCommon("data")
		p["data_type"], p["data"], p["format"], p["parse_only"] = typ, input, format, true
		if typ == "rpc" {
			delete(p, "parse_only")
		} // exercises validation with operational:null
		out = append(out, inventorySeed{"data/" + typ, "data", p, nil, 0})
		xp := inventoryCommon("xpath")
		xp["data_type"], xp["data"], xp["format"], xp["parse_only"], xp["xpath"] = typ, input, format, true, "/pv2:c/mode"
		switch typ {
		case "rpc":
			xp["xpath"] = "/pv2:ping"
		case "reply":
			xp["xpath"] = "/pv2:ping/rtt"
		case "notif", "notif-netconf", "notif-restconf":
			xp["xpath"] = "/pv2:alarm"
		}
		out = append(out, inventorySeed{"xpath/" + typ, "xpath", xp, nil, 0})
		if typ == "rpc" {
			validated := inventoryClone(xp)
			delete(validated, "parse_only")
			out = append(out, inventorySeed{"xpath/rpc-validated", "xpath", validated, nil, 0})
		}
		for _, kind := range []string{"parse", "diff"} {
			st := map[string]any{"do": kind, "data_type": typ, "data": input, "format": format, "parse_only": true}
			p := inventoryCommon("sequence")
			p["steps"] = []any{st, map[string]any{"do": "dump"}}
			out = append(out, inventorySeed{kind + "/" + typ, "step:" + kind, p, []string{"steps", "0"}, 0})
		}
	}
	// parse_one can parse another RPC input into a retained RPC request. pv2:ping has only one
	// input leaf, so rpc_parent's missing-path classes use its output leaf as the closest compiled
	// schema path; the native matrix records the exact lookup result.
	p := inventoryCommon("data")
	p["data_type"], p["rpc"], p["keep_input"], p["data"], p["parse_only"] = "rpc", inventoryRPC, true, `{"n":1}`, true
	out = append(out, inventorySeed{"data/rpc-with-request", "data", p, nil, 0})
	for _, kind := range strings.Fields("parse validate edit change_term dump print_xml any_direct dup insert compare diff diff_parse diff_merge diff_apply diff_reverse trim xpath link links unlink find_meta") {
		p := inventoryCommon("sequence")
		st := map[string]any{"do": kind}
		parseData := inventoryData
		switch kind {
		case "parse", "compare", "diff":
			st["data"], st["parse_only"] = inventoryData, true
		case "insert":
			st["data"], st["parse_only"], st["node"], st["parent"] = `{"pv2:c":{"grouped":"new"}}`, true, "/pv2:c/grouped", "/pv2:c"
		case "diff_parse", "diff_merge":
			st["data"] = `{}`
		case "edit":
			st["merge"] = `{}`
		case "validate":
			st["module"] = "pv2"
		case "change_term":
			st["node"], st["value"] = "/pv2:c/mode", "off"
		case "dup":
			st["node"], st["options"] = "/pv2:c", []any{"recursive"}
		case "trim":
			st["xpath"] = "/pv2:c/l"
		case "any_direct":
			st["case"] = "nil_boundaries"
		case "xpath":
			st["xpath"] = "count(/pv2:c)"
		case "unlink":
			st["node"] = "/pv2:c/mode"
		case "find_meta":
			st["node"], st["name"] = "/pv2:c", "pv2-meta:ann"
			p["modules"] = append(list(p["modules"]), map[string]any{"name": "pv2-meta"})
			parseData = `{"pv2:c":{"@":{"pv2-meta:ann":"x"},"mode":"on","l":[{"k":"a"}]}}`
		}
		if kind == "link" || kind == "links" {
			p["context_options"] = []any{"no_yanglibrary", "leafref_linking"}
		}
		p["steps"] = []any{map[string]any{"do": "parse", "data": parseData, "parse_only": true}, st, map[string]any{"do": "dump"}}
		out = append(out, inventorySeed{"step/" + kind, "step:" + kind, p, []string{"steps", "1"}, 0})
		if kind == "dup" || kind == "diff" || kind == "any_direct" {
			q := inventoryClone(p)
			if kind == "diff" {
				inventorySubject(q, []string{"steps", "0"})["data"] = "{}"
			}
			target := inventoryCommon("schema")
			delete(target, "op")
			inventorySubject(q, []string{"steps", "1"})["target"] = target
			out = append(out, inventorySeed{kind + "/target", "target:" + kind, q, []string{"steps", "1", "target"}, 0})
		}
	}
	// The oracle can construct a compare operand that the XML parser would reject.
	p = inventoryCommon("sequence")
	p["steps"] = []any{
		map[string]any{"do": "parse", "data": inventoryData, "parse_only": true},
		map[string]any{"do": "compare", "insert_opaq": map[string]any{"name": "n", "module": "pv2", "value": "v"}},
		map[string]any{"do": "dump"},
	}
	out = append(out, inventorySeed{"compare/insert-opaq", "compare:insert_opaq", p, []string{"steps", "1", "insert_opaq"}, 0})
	{
		p := inventoryCommon("sequence")
		p["steps"] = []any{
			map[string]any{"do": "parse", "data": inventoryData, "parse_only": true},
			map[string]any{"do": "dup", "node": "/pv2:c/l[k='b']", "parent": "/pv2:c", "options": []any{"recursive"},
				"source": map[string]any{"format": "json", "data_type": "data", "data": `{"pv2:c":{"l":[{"k":"b"}]}}`, "parse_only": true}},
			map[string]any{"do": "dump"},
		}
		out = append(out, inventorySeed{"step/dup-source", "step:dup", p, []string{"steps", "1"}, 0})
	}
	// Each new mutation selector gets a valid setup, including selection of an
	// actual child without relying on the child's schema path.
	for _, variant := range []struct {
		name, scope string
		step        map[string]any
	}{
		{"change-term-xpath", "step:change_term", map[string]any{"do": "change_term", "node_xpath": "/pv2:c/mode", "value": "off"}},
		{"change-term-child", "step:change_term", map[string]any{"do": "change_term", "node": "/pv2:c", "child": 1, "value": "off"}},
		{"change-term-xpath-child", "step:change_term", map[string]any{"do": "change_term", "node_xpath": "/pv2:c", "child": 1, "value": "off"}},
		{"edit-merge-self", "step:edit", map[string]any{"do": "edit", "merge_self": true}},
		{"xpath-vars", "step:xpath", map[string]any{"do": "xpath", "xpath": "/pv2:c/l[k=$v]", "vars": map[string]any{"v": "'a'"}}},
	} {
		p := inventoryCommon("sequence")
		p["steps"] = []any{map[string]any{"do": "parse", "data": inventoryData, "parse_only": true}, variant.step, map[string]any{"do": "dump"}}
		out = append(out, inventorySeed{"step/" + variant.name, variant.scope, p, []string{"steps", "1"}, 0})
	}
	// Keep an opaque node as the first (and only) retained top-level node so selectors
	// reach lyd_find_path's ctx_node->schema argument check.
	for _, seed := range []struct {
		name, scope string
		step        map[string]any
	}{
		{"step/edit-opaque-first", "step:edit", map[string]any{"do": "edit", "delete": "/pv2:c/mode"}},
		{"step/dup-opaque-first", "step:dup", map[string]any{"do": "dup", "node": "/pv2:c/mode", "parent": "/pv2:c"}},
	} {
		p := inventoryCommon("sequence")
		p["steps"] = []any{
			map[string]any{"do": "parse", "data": `{"pv2:nosuch":"x"}`, "unknown": "opaque", "parse_only": true},
			seed.step,
			map[string]any{"do": "dump"},
		}
		out = append(out, inventorySeed{seed.name, seed.scope, p, []string{"steps", "1"}, 0})
	}
	for _, kind := range strings.Fields("set insert_term insert_inner insert_list insert_list2 insert_opaq new_meta free_meta") {
		x := map[string]any{"module": "pv2", "name": "c"}
		switch kind {
		case "set":
			x = map[string]any{"path": "/pv2:c/mode", "value": "on"}
		case "insert_term":
			x = map[string]any{"parent": "/pv2:c", "name": "grouped", "value": "x"}
		case "insert_list", "insert_list2":
			x = map[string]any{"parent": "/pv2:c", "name": "l", "keys": []any{"b"}}
			if kind == "insert_list2" {
				x["keys"] = "[k='b']"
			}
		case "insert_inner":
			x = map[string]any{"parent": "/pv2:c", "name": "pres"}
		case "insert_opaq":
			x = map[string]any{"module": "pv2", "name": "unknown"}
		case "new_meta", "free_meta":
			x = map[string]any{"node": "/pv2:c", "name": "pv2-meta:ann"}
			if kind == "new_meta" {
				x["value"] = "merge"
			}
		}
		p := inventoryCommon("sequence")
		if kind == "new_meta" || kind == "free_meta" {
			p["modules"] = append(list(p["modules"]), map[string]any{"name": "pv2-meta"})
		}
		p["steps"] = []any{map[string]any{"do": "parse", "data": inventoryData, "parse_only": true}, map[string]any{"do": "edit", kind: x}, map[string]any{"do": "dump"}}
		out = append(out, inventorySeed{"edit/" + kind, "edit:" + kind, p, []string{"steps", "1", kind}, 0})
		if kind == "set" {
			// set as the first step: lyd_new_path gets no parent, so its own argument check
			// (path[0] == '/' before tokenizing) is reached for every path class.
			q := inventoryCommon("sequence")
			q["steps"] = []any{map[string]any{"do": "edit", kind: inventoryClone(x)}, map[string]any{"do": "dump"}}
			out = append(out, inventorySeed{"edit/set-empty-tree", "edit:set", q, []string{"steps", "0", kind}, 0})
		}
	}
	// A real reply request enables keep_input, independently of standalone replies.
	for _, kind := range []string{"parse", "diff"} {
		p := inventoryCommon("sequence")
		p["steps"] = []any{map[string]any{"do": kind, "data_type": "reply", "parse_only": true, "rpc": inventoryRPC, "data": `{"pv2:rtt":1}`}, map[string]any{"do": "dump"}}
		out = append(out, inventorySeed{kind + "/reply-with-rpc", "step:" + kind, p, []string{"steps", "0"}, 0})
	}
	return out
}

func inventoryClone(p map[string]any) map[string]any {
	b, _ := json.Marshal(p)
	var q map[string]any
	_ = json.Unmarshal(b, &q)
	return q
}

func inventorySubject(p map[string]any, path []string) map[string]any {
	var v any = p
	for _, k := range path {
		switch x := v.(type) {
		case map[string]any:
			v = x[k]
		case []any:
			if k == "0" {
				v = x[0]
			} else {
				v = x[1]
			}
		}
	}
	return v.(map[string]any)
}

func inventoryRows(scope string) []inventoryRow {
	byKey := map[string]inventoryRow{}
	for _, r := range requestInventory {
		if !strings.Contains(" "+r.scopes+" ", " "+scope+" ") {
			continue
		}
		if prev, ok := byKey[r.key]; ok {
			r = mergeInventoryRow(prev, r)
		}
		byKey[r.key] = r
	}
	// Cross every direct key with every op/step: root ignores unrelated fields,
	// steps reject them via keys_only. Nested contexts reject via keys_only too.
	for _, r := range requestInventory {
		if strings.Contains(r.key, ".") {
			continue
		}
		if prev, ok := byKey[r.key]; ok {
			if strings.HasPrefix(prev.behavior, "Unlisted root key") {
				prev.kind = mergeWords(prev.kind, r.kind)
				prev.valid = mergeWords(prev.valid, r.valid)
				byKey[r.key] = prev
			}
			continue
		}
		r.scopes, r.lines, r.behavior = scope, "1706", "Unlisted root key: ignored, all values. Unlisted step/nested key: keys_only request-error, all present values."
		byKey[r.key] = r
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		// Context probes belong to schema or explicit target-context cases.
		if inventoryContextKey(k) && scope != "schema" && !strings.HasPrefix(scope, "target:") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]inventoryRow, 0, len(keys))
	for _, k := range keys {
		out = append(out, byKey[k])
	}
	return out
}

func mergeInventoryRow(a, b inventoryRow) inventoryRow {
	b.lines = a.lines + "," + b.lines
	b.behavior = a.behavior + " " + b.behavior
	b.kind = mergeWords(a.kind, b.kind)
	b.valid = mergeWords(a.valid, b.valid)
	return b
}

func mergeWords(a, b string) string {
	words := strings.Fields(a)
	seen := set(words...)
	for _, word := range strings.Fields(b) {
		if !seen[word] {
			words = append(words, word)
			seen[word] = true
		}
	}
	return strings.Join(words, " ")
}

func containsWords(have, want string) bool {
	set := set(strings.Fields(have)...)
	for _, word := range strings.Fields(want) {
		if !set[word] {
			return false
		}
	}
	return true
}

func inventoryValues(row inventoryRow, seed inventorySeed) []inventoryValue {
	vals := []inventoryValue{{"absent", nil}, {"null", nil}, {"empty", ""}, {"wrong-type", 42}, {"false", false}, {"true", true}, {"bogus", "bogus"}}
	for _, v := range strings.Fields(row.valid) {
		value := any(v)
		if strings.Contains(" "+row.kind+" ", " flags ") {
			value = []any{v}
		}
		vals = append(vals, inventoryValue{"valid-" + v, value})
	}
	for _, kind := range strings.Fields(row.kind) {
		switch kind {
		case "positive-integer":
			vals = append(vals, inventoryValue{"valid-index", 1}, inventoryValue{"zero-index", 0},
				inventoryValue{"negative-index", -1}, inventoryValue{"fractional-index", 1.5},
				inventoryValue{"overflow-index", 2147483648.0})
		case "flags":
			vals = append(vals, inventoryValue{"empty-flags", []any{}}, inventoryValue{"unknown-flag", []any{"bogus"}}, inventoryValue{"nonstring-flag", []any{false}})
		case "input":
			value := any(inventoryData)
			if v, ok := inventorySubject(seed.params, seed.path)[row.key]; ok {
				value = v
			}
			if row.key == "rpc" {
				value = inventoryRPC
			}
			vals = append(vals, inventoryValue{"valid-input", value})
		case "file":
			vals = append(vals, inventoryValue{"valid-file", "@file"}, inventoryValue{"empty-file", "@empty-file"}, inventoryValue{"inline-conflict", "@conflict"})
		case "directory":
			vals = append(vals, inventoryValue{"valid-directory", "@base"})
		case "iteration":
			value := []any{"schemas"}
			if row.key == "modules" {
				value = []any{map[string]any{"name": "pv2"}}
			}
			vals = append(vals, inventoryValue{"empty-array", []any{}}, inventoryValue{"valid-array", value}, inventoryValue{"object-iteration", map[string]any{"entry": value[0]}}, inventoryValue{"bad-element", []any{false}})
		case "features":
			vals = append(vals, inventoryValue{"empty-array", []any{}}, inventoryValue{"feature", []any{"extra"}}, inventoryValue{"all-features", []any{"*"}}, inventoryValue{"unknown-feature", []any{"bogus"}}, inventoryValue{"null-element", []any{nil}})
		case "keys":
			vals = append(vals, inventoryValue{"empty-array", []any{}}, inventoryValue{"valid-keys", []any{"b"}}, inventoryValue{"null-key", []any{nil}}, inventoryValue{"wrong-key", []any{false}})
		case "object":
			vals = append(vals, inventoryValue{"empty-object", map[string]any{}}, inventoryValue{"unknown-key", map[string]any{"bogus": nil}})
		case "array":
			vals = append(vals, inventoryValue{"empty-array", []any{}}, inventoryValue{"bad-element", []any{false}})
		}
	}
	// Retain the seed's value as a known valid representative where possible.
	subject := inventorySubject(seed.params, seed.path)
	if value, ok := subject[row.key]; ok {
		vals = append(vals, inventoryValue{"seed-value", value})
	}
	switch row.key {
	case "modules[].name", "module", "cur_module":
		vals = append(vals, inventoryValue{"valid-module", "pv2"})
	case "modules[].revision":
		vals = append(vals, inventoryValue{"valid-revision", "2074-01-01"})
	case "namespaces[].uri":
		vals = append(vals, inventoryValue{"valid-uri", "urn:vibe-ports:yang:conformance:pv2"})
	case "namespaces[].prefix":
		vals = append(vals, inventoryValue{"valid-prefix", "p"})
	case "vars.v":
		vals = append(vals, inventoryValue{"valid-variable", "on"})
	case "node", "parent", "first", "second", "context_path", "print_subtree", "data_node", "src_node", "path", "xpath", "node_xpath":
		if strings.Contains(" "+row.kind+" ", " string ") {
			vals = append(vals, inventoryValue{"valid-path", "/pv2:c"})
		}
	}
	if inventoryPathKey(row) {
		vals = append(vals, inventoryPathValues(row)...)
	}
	return vals
}

var inventoryPathKeys = map[string]bool{
	"print_subtree": true, "rpc_parent": true, "parent": true, "parent_opaq": true,
	"src_node": true, "data_node": true, "node": true, "first": true, "second": true,
	"delete": true, "path": true, "context_path": true, "xpath": true, "node_xpath": true,
}

func inventoryPathKey(row inventoryRow) bool {
	return strings.Contains(" "+row.kind+" ", " string ") && inventoryPathKeys[row.key]
}

func inventoryPathValues(row inventoryRow) []inventoryValue {
	if row.key == "rpc_parent" {
		return []inventoryValue{
			{"valid-abs", "/pv2:ping"}, {"spaced-abs", " /pv2:ping"}, {"valid-rel", "n"},
			{"missing-abs", "/pv2:ping/rtt"}, {"missing-rel", "rtt"},
			{"malformed", "/pv2:ping["},
		}
	}
	return []inventoryValue{
		{"valid-abs", "/pv2:c/mode"}, {"spaced-abs", " /pv2:c/mode"}, {"valid-rel", "mode"},
		{"missing-abs", "/pv2:c/grouped"}, {"missing-rel", "grouped"},
		{"malformed", "/pv2:c/mode["},
	}
}

func TestRequestInventoryPathCoverage(t *testing.T) {
	seeds := inventorySeeds()
	allScopes := map[string]bool{}
	for _, seed := range seeds {
		allScopes[seed.scope] = true
		rows := inventoryRows(seed.scope)
		generated := map[string]map[string]bool{}
		for _, row := range rows {
			generated[row.key] = map[string]bool{}
			for _, value := range inventoryValues(row, seed) {
				generated[row.key][value.name] = true
			}
			if inventoryPathKey(row) {
				for _, name := range []string{"valid-abs", "spaced-abs", "valid-rel", "missing-abs", "missing-rel", "malformed"} {
					if !generated[row.key][name] {
						t.Errorf("%s/%s lacks path class %s", seed.name, row.key, name)
					}
				}
			}
		}
	}
	for scope := range allScopes {
		got := map[string]bool{}
		for _, row := range inventoryRows(scope) {
			got[row.key] = true
		}
		for _, row := range requestInventory {
			if strings.Contains(row.key, ".") || inventoryContextKey(row.key) && scope != "schema" && !strings.HasPrefix(scope, "target:") {
				continue
			}
			if !got[row.key] {
				t.Errorf("scope %s does not generate direct key %s", scope, row.key)
			}
		}
	}

	hasCase := func(seedName, key, value string, want map[string]any) bool {
		for _, seed := range seeds {
			if seed.name != seedName {
				continue
			}
			for _, row := range inventoryRows(seed.scope) {
				if row.key != key {
					continue
				}
				for _, v := range inventoryValues(row, seed) {
					if v.name != value {
						continue
					}
					p := inventoryMutation(t, seed, row, v, t.TempDir(), false)
					for k, w := range want {
						if !sameJSON(p[k], w) {
							return false
						}
					}
					return true
				}
			}
		}
		return false
	}
	if !hasCase("xpath/rpc", "rpc_parent", "valid-abs", map[string]any{"op": "xpath", "data_type": "rpc", "data": inventoryRPC,
		"parse_only": true, "rpc_parent": "/pv2:ping", "xpath": "/pv2:ping"}) {
		t.Error("matrix lacks the xpath rpc_parent parse_one regression request")
	}
	if !hasCase("data/data", "print_subtree", "valid-rel", map[string]any{"op": "data", "data_type": "data", "data": inventoryData,
		"parse_only": true, "print_subtree": "mode"}) {
		t.Error("matrix lacks the relative print_subtree request")
	}
}

func TestRequestInventoryRootIgnoredKeys(t *testing.T) {
	stepKinds := strings.Fields("parse validate edit change_term dump print_xml any_direct dup insert compare diff diff_parse diff_merge diff_apply diff_reverse trim xpath link links unlink find_meta")
	seeds := inventorySeeds()
	rootScopes := set("schema", "data", "xpath", "diff", "sequence", "atoms")
	seenScopes := map[string]bool{}
	for i := range seeds {
		seed := &seeds[i]
		if !rootScopes[seed.scope] {
			continue
		}
		seenScopes[seed.scope] = true
		var do *inventoryRow
		for _, row := range inventoryRows(seed.scope) {
			if row.key == "do" {
				do = &row
				break
			}
		}
		if do == nil {
			t.Fatalf("root seed %s does not receive ignored do", seed.name)
		}
		got := map[string]bool{}
		for _, value := range inventoryValues(*do, *seed) {
			got[value.name] = true
		}
		for _, class := range []string{"absent", "null", "empty", "wrong-type", "bogus"} {
			if !got[class] {
				t.Errorf("root seed %s do lacks %s class", seed.name, class)
			}
		}
		for _, kind := range stepKinds {
			if !got["valid-"+kind] {
				t.Errorf("root seed %s do lacks step kind %s", seed.name, kind)
			}
		}
	}
	for _, op := range strings.Fields("schema data xpath diff sequence atoms") {
		if !seenScopes[op] {
			t.Fatalf("missing root seed %s", op)
		}
		rows := inventoryRows(op)
		for _, row := range requestInventory {
			if strings.Contains(row.key, ".") || inventoryContextKey(row.key) {
				continue
			}
			var generated inventoryRow
			found := false
			for _, candidate := range rows {
				if candidate.key == row.key {
					generated = candidate
					found = true
					break
				}
			}
			switch {
			case !found:
				t.Errorf("root seed %s lacks ignored step-only key %s", op, row.key)
			case !strings.HasPrefix(generated.behavior, "Unlisted root key"):
				continue // this root reads a same-named key, so it is not ignored at this level
			case !containsWords(generated.kind, row.kind) || !containsWords(generated.valid, row.valid):
				t.Errorf("root seed %s key %s lost value classes: kind/valid %q/%q, want %q/%q",
					op, row.key, generated.kind, generated.valid, row.kind, row.valid)
			}
		}
	}
	for _, name := range []string{"data/state-no-state", "xpath/state-no-state", "diff/state-no-state", "data/when-false", "data/when-true-false-when"} {
		if !slices.ContainsFunc(seeds, func(seed inventorySeed) bool { return seed.name == name }) {
			t.Errorf("missing observable root seed %s", name)
		}
	}
}

// TestRequestInventory audits literal reads against the C source as well as
// coverage of every keys_only step kind. Response-internal reads are excluded:
// modules accepted/name (1163/1166), verdict (1431), diagnostic level/code/err
// (2911/2912). Dynamic str_of/input_of/flags_of reads are expanded by the table;
// arbitrary variable keys are represented by vars.v.
func TestRequestInventory(t *testing.T) {
	source, err := os.ReadFile("oracle/lyoracle.c")
	if err != nil {
		t.Fatal(err)
	}
	read := regexp.MustCompile(`(?:str_of|input_of|flags_of|cJSON_GetObjectItemCaseSensitive)\([^,\n]+,\s*"([^"]+)"`)
	covered := map[string]bool{}
	scopes := map[string]bool{}
	keys, readPairs := map[string]bool{}, map[string]bool{}
	for _, row := range requestInventory {
		keys[row.key] = true
		for _, line := range strings.Split(row.lines, ",") {
			covered[line+":"+strings.TrimSuffix(row.key[strings.LastIndex(row.key, ".")+1:], "[]")] = true
		}
		for _, scope := range strings.Fields(row.scopes) {
			scopes[scope] = true
			readPairs[scope+"/"+row.key] = true
		}
	}
	for i, line := range strings.Split(string(source), "\n") {
		n := i + 1
		if n == 1163 || n == 1166 || n == 1431 || n == 3187 || n == 3188 {
			continue
		}
		for _, match := range read.FindAllStringSubmatch(line, -1) {
			if !covered[fmt.Sprint(n)+":"+match[1]] {
				t.Errorf("un-inventoried read at lyoracle.c:%d key %s: %s", n, match[1], line)
			}
		}
	}
	for kind := range seqKeys {
		if !scopes["step:"+kind] {
			t.Errorf("no inventory for %s", kind)
		}
	}
	seeds := inventorySeeds()
	pairs, cases := 0, 0
	var md strings.Builder
	fmt.Fprintf(&md, "# lyoracle request key inventory\n\nSource: conformance/oracle/lyoracle.c, SHA-256 %x. All line numbers below refer to that file.\n\n", sha256.Sum256(source))
	md.WriteString("str_of (193): absent/null = NULL, empty string = present, other types = request-error. flags_of (204): absent = zero; null/scalars/non-string or unknown entries = request-error; each listed valid flag is tested separately. input_of (260): absent/null = NULL; empty = memory input; inline and file conflict before file read. Bool rejects null/non-bool; truth uses only JSON true. Enums use str_of then their documented defaults/checks.\n\nStep preflight runs for ALL steps; execution after a failure is skipped. Root unlisted keys are ignored. Step/nested unlisted keys are rejected by keys_only (1706-1722,1725-1735,1790,1808,1855,1925,1989). The generated matrix crosses the union of direct keys with every seed, plus every applicable nested key.\n\n| Scopes | Key | Read lines | Kind | Valid values | Semantics / phase |\n|---|---|---|---|---|---|\n")
	for _, row := range requestInventory {
		fmt.Fprintf(&md, "| %s | %s | %s | %s | %s | %s |\n", row.scopes, row.key, row.lines, row.kind, row.valid, strings.ReplaceAll(row.behavior, "|", "/"))
	}
	for _, seed := range seeds {
		rows := inventoryRows(seed.scope)
		pairs += len(rows)
		for _, r := range rows {
			n := len(inventoryValues(r, seed))
			cases += n
			if strings.HasPrefix(seed.scope, "step:") || strings.HasPrefix(seed.scope, "edit:") || strings.HasPrefix(seed.scope, "target:") {
				cases += n
			}
		}
	}
	fmt.Fprintf(&md, "\n%d distinct key paths; %d explicit key × scope read pairs.\n", len(keys), len(readPairs))
	fmt.Fprintf(&md, "\n%d declarative rows; %d scopes; %d seeds; %d key × seed pairs; %d generated cases (including execution skipped variants).\n", len(requestInventory), len(scopes), len(seeds), pairs, cases)
	md.WriteString("\nExcluded reads are response internals, not input keys: 1163/1166 (module accepted/name), 1431 (verdict), 3187/3188 (diagnostic level/code/err). Dynamic variable names use representative v. Path-valued keys add valid-abs, spaced-abs, valid-rel, missing-abs, missing-rel and malformed classes; XPath has every data-type seed and data has an RPC-with-request seed. Values are representative classes for unbounded strings, arrays and objects, not an exhaustive enumeration of contents. The C oracle may reject or crash on malformed unchecked nested inputs; a signal matching exactly one registered DEV crash class is checked against that deviation's result, while unregistered crashes and all other transport failures remain mismatches.\n")

	scopeNames := make([]string, 0, len(scopes))
	for scope := range scopes {
		scopeNames = append(scopeNames, scope)
	}
	sort.Strings(scopeNames)
	md.WriteString("\n## Expanded key × scope ledger\n\nThis includes ignored/rejected cross-kind keys, so every generated mutation is reviewable.\n\n| Scope | Key | C read lines | Phase |\n|---|---|---|---|\n")
	for _, scope := range scopeNames {
		for _, row := range inventoryRows(scope) {
			lines := row.lines
			if strings.HasPrefix(row.behavior, "Unlisted root key") {
				lines = "none (keys_only 1706-1722 for steps; nested keys_only 1790/1808/1855/1925/1989)"
			}
			fmt.Fprintf(&md, "| %s | %s | %s | %s |\n", scope, row.key, lines, inventoryPhase(scope, row))
		}
	}
	if path := os.Getenv("YANG_REQUEST_INVENTORY_MD"); path != "" {
		if err := os.WriteFile(path, []byte(md.String()), 0600); err != nil { //nolint:gosec // Explicit test-runner ledger destination, never request input.
			t.Fatal(err)
		}
	}
	t.Logf("%d rows, %d distinct key paths, %d explicit key × scope reads, %d scopes, %d seeds, %d key × seed pairs, %d cases", len(requestInventory), len(keys), len(readPairs), len(scopes), len(seeds), pairs, cases)
}

// TestRequestInventorySeeds keeps the matrix's successful setup paths live.
// Capability gaps are reported here; only the oracle differential may classify
// one as an allowlisted exception or a mismatch.
func TestRequestInventorySeeds(t *testing.T) {
	for _, seed := range inventorySeeds() {
		t.Run(seed.name, func(t *testing.T) {
			r, err := (Yang{}).Run(Request{BaseDir: "corpus/protocol-v2", Params: seed.params})
			if strings.HasSuffix(seed.name, "/state-no-state") || seed.name == "data/when-false" {
				if err != nil || r.Verdict() != "invalid" && r.Verdict() != "data-error" {
					t.Fatalf("observable invalid seed = %v, %v", r, err)
				}
				return
			}
			if strings.HasSuffix(seed.name, "opaque-first") {
				if seed.scope == "step:edit" && (err != nil || r.Verdict() != "invalid") {
					t.Fatalf("opaque-first edit seed = %v, %v; want an invalid LY_EINVAL result", r, err)
				}
				if seed.scope == "step:dup" && (err == nil || !strings.HasSuffix(err.Error(), "(a request-error)")) {
					t.Fatalf("opaque-first dup seed = %v, %v; want a selector request-error", r, err)
				}
				return
			}
			if errors.Is(err, ErrUnsupported) {
				t.Logf("baseline capability gap (not waived): %v", err)
				return
			}
			if err != nil || r["verdict"] != "valid" {
				t.Errorf("base request not valid: %v; %v", r, err)
			}
		})
	}
}

// This is the oracle's keys_only table (1706-1723), deliberately independent
// of seqKeys. dparams_of reads some additional keys, but keys_only rejects
// those keys before that helper is called for this step kind.
var inventoryStepKeys = map[string]string{
	"parse":         "do format data_type data data_file unknown parse_only parse_options validate_options rpc keep_input parent",
	"validate":      "do data_type validate_options module final_only",
	"edit":          "do merge merge_file merge_self set delete insert_term insert_inner insert_list insert_list2 insert_opaq new_meta free_meta format data_type unknown parse_options merge_into",
	"change_term":   "do node node_xpath child value canon",
	"insert":        "do node parent format data_type data data_file unknown parse_only parse_options validate_options detached target",
	"dump":          "do with_defaults",
	"detached_dump": "do with_defaults child",
	"print_xml":     "do",
	"any_direct":    "do case target",
	"dup":           "do node node_xpath parent options siblings target source detached",
	"compare":       "do format data_type data data_file unknown parse_only parse_options validate_options first second options insert_opaq self target",
	"diff":          "do format data_type data data_file unknown parse_only parse_options validate_options rpc keep_input node data_node single options merge merge_options target detached",
	"diff_parse":    "do format data_type data data_file unknown parse_options",
	"diff_merge":    "do format data_type data data_file unknown parse_options options module src_node parent detached_parent",
	"diff_apply":    "do module", "trim": "do xpath vars", "xpath": "do xpath vars detached node", "link": "do", "links": "do detached", "diff_reverse": "do",
	"unlink": "do node diff_node", "find_meta": "do node name",
}

func inventoryPhase(scope string, row inventoryRow) string {
	if strings.HasPrefix(row.behavior, "Unlisted root key") {
		if strings.Contains(scope, ":") {
			return "request: keys_only rejects every present value"
		}
		return "ignored: no read in this op"
	}
	if strings.HasPrefix(scope, "step:") {
		allowed := inventoryStepKeys[strings.TrimPrefix(scope, "step:")]
		key := strings.Split(row.key, ".")[0]
		if !strings.Contains(" "+allowed+" ", " "+key+" ") {
			return "request: keys_only rejects before helper read"
		}
		if scope == "step:parse" && key == "parent" {
			return "execution: parent selector and operation checks"
		}
		if scope == "step:diff" && (key == "data" || key == "data_file") {
			return "request if target present; otherwise execution"
		}
		return "request: check_step; execution: value used unless discarded (see semantics)"
	}
	if strings.HasPrefix(scope, "target:") {
		return "execution: build_ctx_in; parent target object keys_only checked in request"
	}
	if strings.HasPrefix(scope, "edit:") {
		return "request: nested shape/types; execution: lookup and edit"
	}
	switch row.key {
	case "diff_options":
		return "execution: only after both parses succeed"
	case "namespaces", "namespaces[].prefix", "namespaces[].uri":
		return "execution: after data parse and cur_module lookup"
	case "xpath_format", "cur_module", "context_path", "print_subtree":
		return "request: str_of; execution: semantic check after successful parse"
	}
	return "request: dispatch/context/prelude/body in C read order; execution: input/flag consumed"
}
