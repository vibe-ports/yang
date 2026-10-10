# libyang `tests/` inventory — backbone of the conformance corpus (M1-pre, stream C)

Source: libyang tag `v5.8.6` (`make libyang-src` -> `.cache/libyang`, gitignored). Counted with the
prototype in `conformance/tools/extract-utests/` plus plain `grep`; numbers below are reproducible
from a clean checkout.

**Attribution.** libyang's tests are BSD-3-Clause, (c) CESNET, z.s.p.o. (`tests/utests/**`,
`tests/yanglint/**`, `tests/modules/**`). Every schema, data document, asserted outcome and message
extracted from them is derived from those files: each extracted fixture must carry
`source: {url: <libyang file @ v5.8.6>, commit: 47351e59…, license: BSD-3-Clause}` in the manifest,
the CESNET copyright/licence text is reproduced in `conformance/NOTICE` (PLAN §5), which also lists the derived directories and tools, and the
prototype already stamps `libyang_tag`, `license`, `copyright` and `file:func:line` into every
`case.json`. Nothing here endorses or is endorsed by CESNET.

## 1. What is in the suite

| Part | Size | Notes |
|---|--:|---|
| `tests/utests/**/*.c` (cmocka) | 57 files, 525 test functions, 46,268 LOC | tests are *functions*; one function holds 1 to ~400 checks, so function counts understate volume. Unit of work below is the **case** (one schema-load or data-parse step with an asserted outcome). |
| `tests/yanglint/non-interactive/*.test` (Tcl) | 15 files, 87 tests | CLI behaviour: `ly_cmd "args" "regexp"`. Backed by `tests/yanglint/{modules,data}` (28 + 42 files). |
| `tests/yanglint/interactive/*.test` | 18 files, 112 tests | REPL of yanglint: not applicable (we ship a non-interactive `cmd/yanglint-go`). |
| `tests/yangre/*.test` | 4 files, 21 tests | `yangre` regex CLI; overlaps `internal/xsdre` work (pattern semantics reusable). |
| `tests/fuzz/corpus` | 78 seed files (json 16, xml 11, yang 51) | Ready seeds for our M1 fuzz targets. |
| `tests/modules/yang` | 23 modules | used by utests via search dir; public, reusable as-is. |
| `tests/plugins`, `perf`, `style` | small | out (plugin API, perf, C++ compat). |

There are **no** cmocka registrations of the form `cmocka_unit_test*` except in `test_yin.c`; every other
file registers through the project's `UTEST(fn)` macro (`tests/utests/utests.h`). Test functions were
counted as lines matching `UTEST(` or `cmocka_unit_test*(`.

## 2. Per-file inventory

Columns. **Milestone** = owner in PLAN §4. **v1**: `out` = PLAN §1 exclusion (YIN, LYB,
schema-mount, tree printer, extension plugins beyond yang-data/structure/metadata, public plugin API,
sorted/hash perf tricks, C-internal helper structures). **Tier** = portability to the oracle-fixture
format (schema + data + asserted verdict/diagnostics):

- **A** mechanical now: schema and data are string literals (through file-local macros), outcome is
  `LY_SUCCESS`/`LY_EVALID` + `CHECK_LOG_CTX`; one case is one oracle request.
- **B** mechanical after extractor extension: same literals, but the case needs state (module set
  built over several steps, several parses on one tree), or the *asserted result* is not a
  verdict (xpath value, diff text, printed output, `lyd_merge`), or needs a new oracle op/field.
- **C** needs C-level setup: internal API (`lyxml_ctx`, `parse_*` fragment parsers, `lyd_new_*`,
  hash/insert/dup, `ly_ctx_*` options and callbacks). The input strings are reusable in Go unit tests,
  not as oracle fixtures.
- **N** not applicable (out of v1 scope, or pure C data structures).

**Prototype cases** = steps the generic extractor finds (upper bound, not fixtures). In brackets
`(a/b)`: `a` = cases whose asserted verdict equals what libyang v5.8.6's own `yanglint` answers,
`b` = cases where that comparison is possible (parse-only/store-only flags have no yanglint
equivalent). Only tier A is reliable today; for tier B/C files the bracket shows *what the extension
work must fix* (multi-module ordering for `deviation`/`augment`, non-verdict results for xpath/diff).

| File | Tests | LOC | Area | Milestone | v1 | Tier | Prototype cases (agree/verifiable) | libyang target |
|---|--:|--:|---|---|---|---|--:|---|
| `basic/test_common.c` | 6 | 416 | internal helpers | - | out | N | - | common.c (ly_getutf8, ly_strcat, path/prefix helpers) |
| `basic/test_context.c` | 10 | 1052 | context | M2 | in | C | - | context.c (ly_ctx_new, searchdirs, options, import callback, module lookup). **Imported (#41): 7 op `schema` and 2 op `data` fixtures in `conformance/corpus/ut-context/`** (submodule revision choice, import with revision-date, import of the implemented revision, circular import, includes, explicit compile, all_implemented). test_explicit_compile is imported for its modules only: lyoracle compiles after every module, so the C test's assertion that nothing is compiled before the one explicit ly_ctx_compile is not ported (API-only). all_implemented is also observed through data (all-implemented-data, with the all-implemented-off-data control). API-only, not imported: test_searchdirs (set/unset/get), the invalid-format and the two name-collision cases of test_models (the import callback returns a different text for the same name, which no search dir can do), the second test_imports case (callback serves an older revision than the search dir), test_get_models, test_ylmem (M6), test_set_priv_parsed, test_free_parsed (C API). |
| `basic/test_hash_table.c` | 5 | 262 | internal helpers | - | out | N | - | hash_table.c |
| `basic/test_inout.c` | 9 | 407 | internal helpers | - | out | N | - | in.c, out.c (ly_in/ly_out) |
| `basic/test_json.c` | 5 | 773 | codec lexer | M5 | in | C | - | json.c (lyjson_ctx_*): token stream + error message per input string |
| `basic/test_plugins.c` | 4 | 232 | plugins | - | out | N | - | plugins.c (public plugin registration) |
| `basic/test_set.c` | 6 | 276 | internal helpers | - | out | N | - | set.c |
| `basic/test_xml.c` | 6 | 691 | codec lexer | M5 | in | C | - | xml.c (lyxml_ctx_*): token stream + error message per input string |
| `basic/test_xpath.c` | 16 | 1353 | xpath | M3 | in | B | 32 (1/32) | xpath.c (lyxp_eval, lyxp_atomize), plugins_types xpath1.0 canonical form |
| `basic/test_yanglib.c` | 1 | 144 | yang-library | M6 | in | C | - | context.c (ly_ctx_new_yldata), yanglib.c |
| `data/test_diff.c` | 25 | 1768 | diff | M6 | in | B | 57 (2/2) | diff.c (lyd_diff_siblings/tree, lyd_diff_apply_all, lyd_diff_merge_*, lyd_diff_reverse_all); imported: corpus/ut-diff/ (all 25 functions, every diff/apply/merge/reverse step, as sequences; #137) |
| `data/test_lyb.c` | 13 | 2905 | lyb | - | out | N | - | parser_lyb.c, printer_lyb.c |
| `data/test_merge.c` | 11 | 756 | merge | M6 | in | B | 27 (22/26) | tree_data.c (lyd_merge_siblings/module/tree) |
| `data/test_new.c` | 4 | 580 | data tree API | M4 | in | C | - | tree_data_new.c (lyd_new_*, lyd_new_path). **Partially imported (#102): 9 `seq/new-path-ut-*` fixtures cover the `lyd_new_path` calls expressible by `edit.set` (top-level nodes, schema order, keyed/keyless lists, canonical keys, leaf-lists, update, and failures).** Skipped: API-only argument/return-pointer assertions; calls without `LYD_NEW_PATH_UPDATE` where an existing target must return `LY_EEXIST`; `LYD_NEW_PATH_OPAQ`, `LYD_NEW_PATH_WITH_OPAQ`, `LYD_NEW_PATH_ANY_DATATREE`, `LYD_NEW_VAL_BIN`, `LYD_NEW_VAL_CANON`, `LYD_NEW_VAL_STORE_ONLY`, and `LYD_NEW_VAL_OUTPUT` options absent from `edit.set`; anydata/anyxml values (need `lyd_new_path2` value type); and `test_path_ext` calls that require an extension-instance data context. `test_opaq` is owned by the opaque constructor fixtures. |
| `data/test_parser_json.c` | 18 | 1084 | codec json | M5 | in | B | 71 (41/60) | parser_json.c |
| `data/test_parser_xml.c` | 20 | 1132 | codec xml | M5 | in | B | 35 (20/30) | parser_xml.c |
| `data/test_printer_json.c` | 4 | 170 | codec json | M5 | in | B | 8 (8/8) | printer_json.c |
| `data/test_printer_xml.c` | 2 | 347 | codec xml | M5 | in | B | 10 (10/10) | printer_xml.c |
| `data/test_tree_data.c` | 11 | 856 | data tree API | M4 | in | C | 36 (35/35) | tree_data.c, tree_data_common.c, tree_data_hash.c (find, dup, insert, path). **Imported (#103): 5 conformance cases in `conformance/corpus/ut-tree-data/` derived from** `test_lyxp_vars`, `test_find_path`, `test_list_pos`, `test_first_sibling`, and `test_target`; the `test_find_path` case directly exercises `lyd_find_path` / `Tree.Find`. Pending the oracle `dup` and `link`/`links` steps (#222): `test_dup`, `test_data_leafref_nodes`, `test_data_leafref_nodes2`. Tier C remaining: `test_compare` (six expressible cases were imported earlier in `ut-compare`; the rest needs C-level tree construction), `test_compare_diff_ctx`, `test_data_hash`. |
| `data/test_tree_data_sorted.c` | 38 | 1691 | data tree sorted index | - | out | N | - | tree_data_sorted.c (sorted/hash child index: perf trick, PLAN 1 out until profiling) |
| `data/test_validation.c` | 21 | 1905 | validation | M4 | in | B | 62 (59/59) | validation.c (mandatory, min/max, unique, when, must, leafref, dup, choice, defaults, operational) |
| `extensions/test_metadata.c` | 2 | 205 | extensions | M5 | in | B | 7 (6/6) | plugins_exts/metadata.c (RFC 7952) |
| `extensions/test_nacm.c` | 2 | 124 | ext plugin nacm | - | out | N | - | plugins_exts/nacm.c (extension plugin outside yang-data/structure/metadata) |
| `extensions/test_openconfig.c` | 3 | 186 | ext plugin openconfig | - | out | N | - | plugins_exts/openconfig.c (extension plugin outside yang-data/structure/metadata) |
| `extensions/test_schema_mount.c` | 10 | 1966 | schema-mount | - | out | N | - | plugins_exts/schema_mount.c (RFC 8528) |
| `extensions/test_structure.c` | 4 | 462 | extensions | M2 | in | B | 15 (15/15) | plugins_exts/structure.c (RFC 8791) |
| `extensions/test_yangdata.c` | 3 | 266 | extensions | M2 | in | B | 13 (8/13) | plugins_exts/yangdata.c (RFC 8040 yang-data) |
| `node/list.c` | 7 | 1625 | list node | M4 | in | B | 61 (61/61) | schema_compile_node.c (lys_compile_node_list), parser_xml/json.c, validation.c (keys, unique, order) |
| `restriction/test_pattern.c` | 4 | 394 | types: restrictions | M2 | in | A | 9 (9/9) | plugins_types/string.c, schema_compile_amend.c (pattern, invert-match) |
| `restriction/test_range.c` | 4 | 424 | types: restrictions | M2 | in | A | 15 (15/15) | plugins_types/{integer,decimal64}.c, schema_compile_node.c (range/length) |
| `schema/test_printer_tree.c` | 32 | 2338 | tree printer | - | out | N | - | printer_tree.c (RFC 8340) |
| `schema/test_schema.c` | 20 | 2325 | schema API + printers | M2 | in | B | 7 (7/7) | tree_schema.c, tree_schema_common.c, printer_yang.c/yin/info (debug only), schema_features.c |
| `schema/test_tree_schema_compile.c` | 32 | 4182 | schema compile | M2 | in | B | 151 (151/151) | schema_compile.c, schema_compile_node.c, schema_compile_amend.c (uses/grouping/augment/refine/deviation/identity/feature) |
| `schema/test_yang.c` | 24 | 1744 | yang parser (internal fns) | M1 | in | C | 2 (2/2) | parser_yang.c (parse_* helpers; fragments, not whole modules) |
| `schema/test_yin.c` | 57 | 3579 | yin parser | - | out | N | - | parser_yin.c |
| `types/binary.c` | 5 | 359 | types: binary | M2 | in | A | 7 (5/5) | plugins_types/binary.c |
| `types/bits.c` | 13 | 1111 | types: bits | M2 | in | A | 62 (58/58) | plugins_types/bits.c |
| `types/boolean.c` | 2 | 109 | types: boolean | M2 | in | A | 9 (7/7) | plugins_types/boolean.c |
| `types/decimal64.c` | 2 | 130 | types: decimal64 | M2 | in | A | 14 (12/12) | plugins_types/decimal64.c |
| `types/empty.c` | 2 | 106 | types: empty | M2 | in | A | 7 (6/6) | plugins_types/empty.c |
| `types/enumeration.c` | 4 | 140 | types: enumeration | M2 | in | A | 10 (8/8) | plugins_types/enumeration.c |
| `types/identityref.c` | 2 | 134 | types: identityref | M2 | in | A | 10 (9/9) | plugins_types/identityref.c |
| `types/inet_types.c` | 4 | 338 | types: inet_types | M2 | in | A | 30 (22/22) | plugins_types/ietf_inet_types.c |
| `types/instanceid.c` | 2 | 292 | types: instance-identifier | M2 | in | A | 32 (32/32) | plugins_types/instanceid.c |
| `types/instanceid_keys.c` | 1 | 76 | types: instanceid_keys | M2 | in | A | 5 (5/5) | plugins_types/instanceid_keys.c |
| `types/int8.c` | 12 | 1762 | types: int8 | M2 | in | A | 120 (116/116) | plugins_types/integer.c |
| `types/int16.c` | 1 | 74 | types: int16 | M2 | in | A | 2 (2/2) | plugins_types/integer.c |
| `types/int32.c` | 1 | 74 | types: int32 | M2 | in | A | 2 (2/2) | plugins_types/integer.c |
| `types/int64.c` | 1 | 80 | types: int64 | M2 | in | A | 4 (4/4) | plugins_types/integer.c |
| `types/uint8.c` | 1 | 87 | types: uint8 | M2 | in | A | 4 (3/3) | plugins_types/integer.c |
| `types/uint16.c` | 1 | 74 | types: uint16 | M2 | in | A | 2 (2/2) | plugins_types/integer.c |
| `types/uint32.c` | 1 | 74 | types: uint32 | M2 | in | A | 2 (2/2) | plugins_types/integer.c |
| `types/uint64.c` | 1 | 80 | types: uint64 | M2 | in | A | 4 (4/4) | plugins_types/integer.c |
| `types/leafref.c` | 7 | 368 | types: leafref | M2 | in | A | 22 (21/21) | plugins_types/leafref.c |
| `types/string.c` | 12 | 1441 | types: string | M2 | in | A | 134 (131/131) | plugins_types/string.c |
| `types/union.c` | 6 | 370 | types: union | M2 | in | A | 21 (18/18) | plugins_types/union.c |
| `types/yang_types.c` | 5 | 369 | types: yang_types | M2 | in | A | 43 (34/35) | plugins_types/ietf_yang_types.c |

Most type files run three encodings of each value (schema, XML, JSON) and a LYB round trip; the LYB legs
are out of v1 and are skipped by the extractor (they show up as `n/a`, not as failures).

## 3. Totals per area and per milestone

Test functions (`UTEST`), v1 status as in PLAN §1.

| Milestone | Test fns | Tier A fns | Tier B fns | Tier C fns | Prototype cases A | Prototype cases B |
|---|--:|--:|--:|--:|--:|--:|
| M1 (parser internals; slice draws its fixtures from the M2 rows below) | 24 | 0 | 0 | 24 | 0 | 0 |
| M2 schema, types, restrictions, yang-data/structure, context | 163 | 94 | 59 | 10 | 570 | 186 |
| M3 xpath | 16 | 0 | 16 | 0 | 0 | 32 |
| M4 validation, list, data-tree API | 43 | 0 | 28 | 15 | 0 | 123 |
| M5 codecs (json/xml parse+print, lexers, metadata) | 57 | 0 | 46 | 11 | 0 | 131 |
| M6 diff, merge, yang-library | 37 | 0 | 36 | 1 | 0 | 84 |
| **In v1** | **340** | 94 | 185 | 61 | **570** | **556** |
| Out of v1 | 185 | - | - | - | - | - |
| **Total** | **525** | | | | | |

Out of v1 (185 functions): YIN parser 57, sorted-children index 38, tree printer 32, LYB 13,
schema-mount 10, C helpers (hash table, set, in/out, common) 26, plugin registry 4, nacm/openconfig
extension plugins 5.

Per area:

| Area | Test fns | Prototype cases (tier A/B) | v1 | Milestone |
|---|--:|--:|---|---|
| types (+restrictions) | 94 | 570 | in | M2 |
| yin parser | 57 | - | out | - |
| data tree sorted index | 38 | - | out | - |
| tree printer | 32 | - | out | - |
| schema compile | 32 | 151 | in | M2 |
| internal helpers | 26 | - | out | - |
| diff | 25 | 57 | in | M6 |
| yang parser (internal fns) | 24 | - | in | M1 |
| codec json | 22 | 79 | in | M5 |
| codec xml | 22 | 45 | in | M5 |
| validation | 21 | 62 | in | M4 |
| schema API + printers | 20 | 7 | in | M2 |
| xpath | 16 | 32 | in | M3 |
| data tree API | 15 | - | in | M4 |
| lyb | 13 | - | out | - |
| codec lexer | 11 | - | in | M5 |
| merge | 11 | 27 | in | M6 |
| context | 10 | - | in | M2 |
| schema-mount | 10 | - | out | - |
| extensions (metadata, structure, yang-data) | 9 | 35 | in | M2/M5 |
| list node | 7 | 61 | in | M4 |
| plugins | 4 | - | out | - |
| ext plugins nacm/openconfig | 5 | - | out | - |
| yang-library | 1 | - | in | M6 |

Reading the numbers: **340 of 525 test functions (65 %) are in v1**; of those 279 (82 %) are tier A/B,
i.e. reachable by extraction, and they hold **about 1,126 candidate cases** (570 tier A, 556 tier B,
generic mode, before dedupe; expect 10-20 % to collapse or be waived: identical schema+data appear
in several files, and stateful ones need the `sequence` op). Tier C is 61 functions (18 %): reuse
their input strings in Go unit tests of the owning package (`internal/parser`, `internal/xpath`,
codec lexers), not in the oracle corpus.

The Tcl tests add 87 non-interactive tests (CLI, tier N for oracle fixtures, but their **inputs**, the
28 modules and 42 data files under `tests/yanglint/`, are ready-made corpus documents; their
`ly_cmd` regexps become `cmd/yanglint-go` tests) and 21 yangre tests (pattern + string -> match,
directly usable as `xsdre` cases).

## 4. Coverage targets

Measured on a fixed denominator: the in-scope tier A+B candidate cases above (1,126 today, frozen
when the first full extraction lands, then re-counted in this file). A case counts as *converted* when
it is a manifest fixture with golden **and** an `assert` block (verdict + diagnostics subset) copied
from the C test and the oracle agrees with that assert (section 6 gate).

| End of | Cumulative in-scope A+B cases converted | What must be in |
|---|--:|---|
| M1 | >= 20 % (~225) | integer types (int8..uint64 = 141 cases), boolean, enumeration, empty (26), basic string, plus slice-relevant `list.c` and `test_validation.c` (when/must/mandatory/leafref) |
| M2 | >= 67 % (~755) | >= 95 % of all types/restriction cases, >= 90 % of `test_tree_schema_compile` (needs the multi-module `schema` step), structure, yang-data |
| M3 | >= 70 % | >= 95 % of `test_xpath` expressions (needs a result-carrying extractor, section 5.4) |
| M4 | >= 81 % | >= 95 % of `test_validation` and `list.c`; tier C data-tree API dispositioned |
| M5 | >= 93 % | >= 95 % of json/xml parser+printer cases and metadata |
| M6 | >= 95 % | diff/merge cases; every skipped case has a reason in `conformance/utests-waivers.md` |

Rules that go with the numbers:

1. **Disposition, not just conversion.** Every in-scope test function ends up in one of: fixtures
   (tier A/B), Go unit test (tier C), waived with a reason (e.g. asserts a C struct layout). 100 % of the
   61 tier C functions and 100 % of the 87 yanglint non-interactive tests get a disposition by the
   milestone that owns their area.
2. **Out-of-scope stays visible.** The 185 out-of-v1 functions are listed once (this file) and never
   silently dropped; if v1 scope changes the table is re-cut, not the targets.
3. **Agreement is not conformance** (PLAN §5): converted cases are reported next to RFC-section coverage.

### RFC-section coverage (PLAN §5 idea, made concrete)

libyang's tests carry no RFC tags, so the tag is assigned in two steps:

1. **Default tag per source file/function**, stored in a small table beside the extractor
   (`types/int8.c` -> `RFC7950#9.2`, `test_validation:test_mandatory` -> `RFC7950#7.6.5`,
   `test_validation:test_unique*` -> `RFC7950#7.8.3`, `restriction/test_pattern.c` -> `RFC7950#9.4.5-6`,
   `test_metadata.c` -> `RFC7952`, ...). A fixture inherits it and may add more.
2. **Denominator = RFC 2119 statements.** A script splits RFC 7950/7951/7952/6243/8791 text into
   sections and lists each `MUST/MUST NOT/SHALL/REQUIRED` statement as `RFC7950#<section>:<n>`
   (RFC 7950 has ~240 lines carrying those words, RFC 7951 ~10; measured with `grep -c` on the RFC text,
   a line-count proxy, statements split across lines are counted once per line). A statement is
   *covered* when at least one fixture whose `rfc:` tag names its section has an `assert` exercising
   it; *waived* statements (transport, YANG Patch, etc.) are listed with a reason.
   Coverage % = covered / (total - waived), reported per chapter in the CI summary next to oracle
   agreement. Target: no chapter of RFC 7950 §6-9 below 80 % at M4, all in-scope chapters >= 80 % at M6.

## 5. Extraction approach

### 5.1 Can a script do it? Yes, for tier A and most of B, without a C parser

The tests are macro-heavy but *regular*: schemas and data are string-literal concatenations
(`"module " MOD_NAME " {" ...`) wrapped in file-local macros; outcomes are a fixed vocabulary:
`UTEST_ADD_MODULE(schema, LYS_IN_YANG, features, &mod)` (success),
`UTEST_INVALID_MODULE(schema, fmt, features, LY_EVALID)`,
`CHECK_PARSE_LYD_PARAM(data, LYD_XML|LYD_JSON, parse_opts, validate_opts, LY_x, tree)`,
`assert_int_equal(LY_x, lys_parse_mem(ctx, text, fmt, &mod))` / `lyd_parse_data_mem(...)`, and the
diagnostics `CHECK_LOG_CTX(msg, path, line)` / `CHECK_LOG_CTX_APPTAG(msg, path, line, apptag)` which
follow the failing step. That is a tiny language, so the prototype is a **C-subset interpreter**, not a
grammar: tokenizer (strings with escapes, idents, comments) -> `#define` table -> token-substitution
macro expander -> statement walker that tracks (a) string variables, (b) the module set loaded so far
in the current test function (libyang's per-test context), (c) the last case for log attachment.
Anything it cannot resolve is counted under `skipped`/`unextracted`, never guessed.

### 5.2 Prototype: `conformance/tools/extract-utests`

Standalone Go program (own `go.mod`, stdlib only, one file + one unit test), no fixtures added to the
manifest:

```sh
cd conformance/tools/extract-utests
go test ./...
go run . -out "$SCRATCH" -verify ../../../.cache/libyang/tests/utests/types/int8.c
```

Output per case: `<out>/<file>/<func>/<n>/{<module>.yang, data.xml|data.json, case.json}`.
`case.json` = source attribution + module load order + format + asserted return code -> verdict +
asserted log entries (`msg`, `path`, `line`, `apptag`) + an **oracle request draft** in protocol-v2 shape
(`op`, `modules`, `format`, `data_file`, `unknown` derived from `LYD_PARSE_STRICT/OPAQ`, `parse_only`).
`-verify` (optional, test-only) runs the local `yanglint` of the same libyang version on each case and
compares valid/invalid to the C test's assertion; that is how precision is measured without the
container.

### 5.3 Prototype results (measured against the pinned oracle yanglint)

Measured inside the dev container (`./dev`; its `yanglint --version` is 5.8.6, the pinned build)
with the hardened extractor of 5.6. These numbers replace an earlier run that used a host yanglint
and a looser extractor.

`types/int8.c` (richest type test): **120 cases**. Skipped, with reasons: 25 YIN loads (YIN is out
of v1; after the first one the rest of that test function is skipped as "context incomplete", which
also removes cases that were previously emitted against a wrong module set), 1 `LY_EEXIST`, 4 LYB.
Verdict check: **116 agree, 0 differ, 4 n/a**.

All 24 files under `types/` and `restriction/` (tier A): 570 cases, **527 agree, 1 differ, 42 n/a**
(n/a = parse-only/store-only flags, per-module feature lists, LYB legs).
- The earlier `enumeration` difference was a verifier bug, not libyang: yanglint enables every
  feature unless `-F <module>:` is given, while the C tests load with none. The verifier now passes
  `-F <module>:` for modules loaded with a NULL feature list, and that case agrees.
- The one remaining difference, `yang_types.c:162`, injects namespaces through a macro argument and
  imports ietf-yang-library / ietf-datastores data. Root cause not confirmed: **unverified**.

Tier B files, same extractor, verdicts vs yanglint (agree/differ/n/a): `test_validation` 59/0/3,
`list.c` 61/0/0, `test_tree_schema_compile` 151/0/0 (151 cases; the earlier 408 included cases built
on a wrong module set), `test_structure` 15/0/0, `test_printer_json` 8/0/0, `test_printer_xml`
10/0/0, `test_merge` 22/4/1, `test_parser_json` 41/19/11, `test_parser_xml` 20/10/5,
`test_yangdata` 8/5/0, `test_xpath` 1/31/0.

**Not root-caused, treat as unverified**: the 4 `test_merge`, 29 `test_parser_*`, 5 `test_yangdata`
and 31 `test_xpath` disagreements. Reading the C shows these files use
their own wrapper macros (`PARSER_CHECK_ERROR`, `CHECK_PARSE_LYD`, `LYD_TREE_CREATE`) or assert
results that are not verdicts (xpath values), which the generic extractor does not model, so they
are the extension backlog (5.4) and not evidence of a libyang disagreement. This is a reading of
the sources, not a proof. Out-of-v1 files (`test_nacm`, `test_schema_mount`, `test_lyb`) also
differ and are ignored.

Interpretation: tier A verdict agreement is 527/528 (99.8 %) of verifiable cases. That checks the
extractor, not conformance; the real gate is the oracle (5.5). The extractor never claims more than
it resolved: a case with C-side semantics the request cannot carry (feature lists, unknown flags)
gets `needs_extension` and no `oracle_request`.

Caveat on the verifier: yanglint `-t data` is operational (violations become warnings), so the check
uses `-t config -e [-n]` as the closest match to `lyd_parse_data` without `LYD_VALIDATE_OPERATIONAL`;
state-data cases can mis-verify.

### 5.4 Extension backlog (what tier B needs)

1. **Stateful sequences**: emit one `sequence` request per test function instead of independent cases
   when it re-loads names, sets options or edits (`LY_EEXIST`, `lyd_new_*`, `lyd_merge_*`).
2. **Result-carrying macros**: `CHECK_LYD_STRING_PARAM` / `CHECK_LYD` (printed tree text), diff/merge
   expected strings, xpath `lyxp_eval` results; needs oracle ops `xpath`/`diff` (already in v1 oracle)
   and an `assert.result` field. The oracle golden covers the value today; the extracted expectation
   becomes the normative `assert`.
3. **Value checks** `CHECK_LYD_VALUE_*` (canonical form, union member): map to `assert.value`
   (oracle typed dump already has `canonical`, `type`, `union_member`).
4. Context options, import callbacks, `LYD_VALIDATE_*` mapped to request fields (`context_options`,
   `validate_options` exist in the oracle); unknown-node policy already derived.
5. Tcl (`tests/yanglint/non-interactive`): trivial regex extraction of `ly_cmd "args" "regexp"`.

### 5.5 Acceptance gate for extracted fixtures (proposed)

A candidate enters the corpus (a `manifest.d` fragment) only if, running the oracle: (1) verdict == asserted verdict;
(2) every asserted `CHECK_LOG_CTX` message/path is found in the golden diagnostics (message text is
informational elsewhere, but here it doubles as a cross-check that extraction picked the right
schema/data pair); (3) the fixture carries the attribution block above. Failures go to a triage list,
each ending as extractor fix, `sequence` conversion, waiver, or a documented libyang-version
difference. The `assert.diagnostics` (vecode_name, data_path, apptag) are then filled from the
golden and reviewed, never from the C message text alone.

## 6. Risks and limits

- Asserted vs observed: a libyang test is *libyang's* expectation; when it contradicts the RFC the
  fixture keeps `assert` = C test and a deviation entry, per manifest v2.
- `CHECK_LOG_CTX` messages are libyang-specific text; do not require our engine to reproduce them.
  Compare code/path/app-tag, keep the message as provenance.
- Macros differ per file (`TEST_ERROR_XML` has a 3- and a 4-argument form across files): the
  extractor reads the file's own `#define`s, so a new dialect surfaces as `skipped`, never as a wrong
  case; keep the per-file `skipped` report in CI.
- Candidate counts are an upper bound (dedupe, waivers); re-baseline the denominator when the first
  full extraction is committed.

### 5.6 Extractor safety rules (from the review of PR #2)

- Verification distinguishes yanglint exit 0 (valid) and 1 (libyang error) from anything else (n/a);
  a yanglint that cannot be run, or reports another libyang version than `-tag`, aborts the run.
- Full C escape set (`\a \b \f \v \? \" \\ \' \n \t \r`, `\x..`, octal); `\0`, over-long `\x`,
  `\u` and unknown escapes make the literal unresolvable, so the case is skipped.
- Variables: a failed or conditional `ident = expr;` deletes the variable, `+=` deletes it;
  everything after `if/else/for/while/switch/goto` in a test function is skipped; `#define`s inside
  `#if` blocks, or defined twice, are not used.
- A skipped module load (YIN, unresolvable, context option/callback calls, wrong macro arity) taints
  the rest of the test function: its later cases would run against an unknown context.
- A rejected load restores a cloned module list; submodules are written as search-dir files but
  never listed as implemented modules; module names are unquoted and validated.
- `-out` inside a git work tree is refused (`-allow-in-repo` overrides); marshal and write errors
  are fatal; the libyang tag is one constant and a flag.
- Covered by table tests in `main_test.go`: escapes, stale variables, failed-load restore, skip
  paths, request building, verify exit codes, golden `case.json`.
