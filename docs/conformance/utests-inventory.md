# libyang `tests/` inventory — backbone of the conformance corpus (M1-pre, stream C)

Source: libyang tag `v5.8.6` (`make libyang-src` -> `.cache/libyang`, gitignored). Counted with the
prototype in `conformance/tools/extract-utests/` plus plain `grep`; numbers below are reproducible
from a clean checkout.

**Attribution.** libyang's tests are BSD-3-Clause, (c) CESNET, z.s.p.o. (`tests/utests/**`,
`tests/yanglint/**`, `tests/modules/**`). Every schema, data document, asserted outcome and message
extracted from them is derived from those files: each extracted fixture must carry
`source: {url: <libyang file @ v5.8.6>, commit: 47351e59…, license: BSD-3-Clause}` in the manifest,
the CESNET copyright/licence text must be reproduced in `conformance/NOTICE` (PLAN §5), and the
prototype already stamps `libyang_tag`, `license`, `copyright` and `file:func:line` into every
`case.json`. Nothing here endorses or is endorsed by CESNET.

## 1. What is in the suite

| Part | Size | Notes |
|---|--:|---|
| `tests/utests/**/*.c` (cmocka) | 57 files, 525 test functions, 46,268 LOC | tests are *functions*; one function holds 1 to ~400 checks, so function counts understate volume. Unit of work below is the **case** (one schema-load or data-parse step with an asserted outcome). |
| `tests/yanglint/non-interactive/*.test` (Tcl) | 15 files, 87 tests | CLI behaviour: `ly_cmd "args" "regexp"`. Backed by `tests/yanglint/{modules,data}` (28 + 42 files). |
| `tests/yanglint/interactive/*.test` | 20 files, 112 tests | REPL of yanglint: not applicable (we ship a non-interactive `cmd/yanglint-go`). |
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
| `basic/test_context.c` | 10 | 1052 | context | M2 | in | C | 10 (8/10) | context.c (ly_ctx_new, searchdirs, options, import callback, module lookup) |
| `basic/test_hash_table.c` | 5 | 262 | internal helpers | - | out | N | - | hash_table.c |
| `basic/test_inout.c` | 9 | 407 | internal helpers | - | out | N | - | in.c, out.c (ly_in/ly_out) |
| `basic/test_json.c` | 5 | 773 | codec lexer | M5 | in | C | - | json.c (lyjson_ctx_*): token stream + error message per input string |
| `basic/test_plugins.c` | 4 | 232 | plugins | - | out | N | - | plugins.c (public plugin registration) |
| `basic/test_set.c` | 6 | 276 | internal helpers | - | out | N | - | set.c |
| `basic/test_xml.c` | 6 | 691 | codec lexer | M5 | in | C | - | xml.c (lyxml_ctx_*): token stream + error message per input string |
| `basic/test_xpath.c` | 16 | 1353 | xpath | M3 | in | B | 32 (1/32) | xpath.c (lyxp_eval, lyxp_atomize), plugins_types xpath1.0 canonical form |
| `basic/test_yanglib.c` | 1 | 144 | yang-library | M6 | in | C | - | context.c (ly_ctx_new_yldata), yanglib.c |
| `data/test_diff.c` | 25 | 1768 | diff | M6 | in | B | 59 (2/2) | diff.c (lyd_diff_siblings/tree, lyd_diff_apply_all, lyd_diff_merge_*, lyd_diff_reverse_all) |
| `data/test_lyb.c` | 13 | 2905 | lyb | - | out | N | - | parser_lyb.c, printer_lyb.c |
| `data/test_merge.c` | 11 | 756 | merge | M6 | in | B | 27 (26/26) | tree_data.c (lyd_merge_siblings/module/tree) |
| `data/test_new.c` | 4 | 580 | data tree API | M4 | in | C | 1 (0/1) | tree_data_new.c (lyd_new_*, lyd_new_path) |
| `data/test_parser_json.c` | 18 | 1084 | codec json | M5 | in | B | 71 (41/60) | parser_json.c |
| `data/test_parser_xml.c` | 20 | 1132 | codec xml | M5 | in | B | 35 (20/30) | parser_xml.c |
| `data/test_printer_json.c` | 4 | 170 | codec json | M5 | in | B | 8 (8/8) | printer_json.c |
| `data/test_printer_xml.c` | 2 | 347 | codec xml | M5 | in | B | 13 (13/13) | printer_xml.c |
| `data/test_tree_data.c` | 11 | 856 | data tree API | M4 | in | C | 56 (48/49) | tree_data.c, tree_data_common.c, tree_data_hash.c (find, dup, insert, path) |
| `data/test_tree_data_sorted.c` | 38 | 1691 | data tree sorted index | - | out | N | - | tree_data_sorted.c (sorted/hash child index: perf trick, PLAN 1 out until profiling) |
| `data/test_validation.c` | 21 | 1905 | validation | M4 | in | B | 78 (65/70) | validation.c (mandatory, min/max, unique, when, must, leafref, dup, choice, defaults, operational) |
| `extensions/test_metadata.c` | 2 | 205 | extensions | M5 | in | B | 7 (7/7) | plugins_exts/metadata.c (RFC 7952) |
| `extensions/test_nacm.c` | 2 | 124 | ext plugin nacm | - | out | N | - | plugins_exts/nacm.c (extension plugin outside yang-data/structure/metadata) |
| `extensions/test_openconfig.c` | 3 | 186 | ext plugin openconfig | - | out | N | - | plugins_exts/openconfig.c (extension plugin outside yang-data/structure/metadata) |
| `extensions/test_schema_mount.c` | 10 | 1966 | schema-mount | - | out | N | - | plugins_exts/schema_mount.c (RFC 8528) |
| `extensions/test_structure.c` | 4 | 462 | extensions | M2 | in | B | 16 (16/16) | plugins_exts/structure.c (RFC 8791) |
| `extensions/test_yangdata.c` | 3 | 266 | extensions | M2 | in | B | 13 (8/13) | plugins_exts/yangdata.c (RFC 8040 yang-data) |
| `node/list.c` | 7 | 1625 | list node | M4 | in | B | 62 (62/62) | schema_compile_node.c (lys_compile_node_list), parser_xml/json.c, validation.c (keys, unique, order) |
| `restriction/test_pattern.c` | 4 | 394 | types: restrictions | M2 | in | A | 9 (9/9) | plugins_types/string.c, schema_compile_amend.c (pattern, invert-match) |
| `restriction/test_range.c` | 4 | 424 | types: restrictions | M2 | in | A | 15 (15/15) | plugins_types/{integer,decimal64}.c, schema_compile_node.c (range/length) |
| `schema/test_printer_tree.c` | 32 | 2338 | tree printer | - | out | N | - | printer_tree.c (RFC 8340) |
| `schema/test_schema.c` | 20 | 2325 | schema API + printers | M2 | in | B | 30 (27/30) | tree_schema.c, tree_schema_common.c, printer_yang.c/yin/info (debug only), schema_features.c |
| `schema/test_tree_schema_compile.c` | 32 | 4182 | schema compile | M2 | in | B | 408 (315/408) | schema_compile.c, schema_compile_node.c, schema_compile_amend.c (uses/grouping/augment/refine/deviation/identity/feature) |
| `schema/test_yang.c` | 24 | 1744 | yang parser (internal fns) | M1 | in | C | 2 (2/2) | parser_yang.c (parse_* helpers; fragments, not whole modules) |
| `schema/test_yin.c` | 57 | 3579 | yin parser | - | out | N | - | parser_yin.c |
| `types/binary.c` | 5 | 359 | types: binary | M2 | in | A | 7 (5/5) | plugins_types/binary.c |
| `types/bits.c` | 13 | 1111 | types: bits | M2 | in | A | 62 (59/59) | plugins_types/bits.c |
| `types/boolean.c` | 2 | 109 | types: boolean | M2 | in | A | 9 (7/7) | plugins_types/boolean.c |
| `types/decimal64.c` | 2 | 130 | types: decimal64 | M2 | in | A | 14 (12/12) | plugins_types/decimal64.c |
| `types/empty.c` | 2 | 106 | types: empty | M2 | in | A | 7 (6/6) | plugins_types/empty.c |
| `types/enumeration.c` | 4 | 140 | types: enumeration | M2 | in | A | 10 (7/8) | plugins_types/enumeration.c |
| `types/identityref.c` | 2 | 134 | types: identityref | M2 | in | A | 10 (9/9) | plugins_types/identityref.c |
| `types/inet_types.c` | 4 | 338 | types: inet_types | M2 | in | A | 34 (25/25) | plugins_types/ietf_inet_types.c |
| `types/instanceid.c` | 2 | 292 | types: instance-identifier | M2 | in | A | 33 (32/33) | plugins_types/instanceid.c |
| `types/instanceid_keys.c` | 1 | 76 | types: instanceid_keys | M2 | in | A | 5 (5/5) | plugins_types/instanceid_keys.c |
| `types/int8.c` | 12 | 1762 | types: int8 | M2 | in | A | 121 (117/117) | plugins_types/integer.c |
| `types/int16.c` | 1 | 74 | types: int16 | M2 | in | A | 2 (2/2) | plugins_types/integer.c |
| `types/int32.c` | 1 | 74 | types: int32 | M2 | in | A | 2 (2/2) | plugins_types/integer.c |
| `types/int64.c` | 1 | 80 | types: int64 | M2 | in | A | 4 (4/4) | plugins_types/integer.c |
| `types/uint8.c` | 1 | 87 | types: uint8 | M2 | in | A | 4 (3/3) | plugins_types/integer.c |
| `types/uint16.c` | 1 | 74 | types: uint16 | M2 | in | A | 2 (2/2) | plugins_types/integer.c |
| `types/uint32.c` | 1 | 74 | types: uint32 | M2 | in | A | 2 (2/2) | plugins_types/integer.c |
| `types/uint64.c` | 1 | 80 | types: uint64 | M2 | in | A | 4 (4/4) | plugins_types/integer.c |
| `types/leafref.c` | 7 | 368 | types: leafref | M2 | in | A | 29 (24/28) | plugins_types/leafref.c |
| `types/string.c` | 12 | 1441 | types: string | M2 | in | A | 135 (132/132) | plugins_types/string.c |
| `types/union.c` | 6 | 370 | types: union | M2 | in | A | 21 (18/18) | plugins_types/union.c |
| `types/yang_types.c` | 5 | 369 | types: yang_types | M2 | in | A | 45 (36/37) | plugins_types/ietf_yang_types.c |

Most type files run three encodings of each value (schema, XML, JSON) and a LYB round trip; the LYB legs
are out of v1 and are skipped by the extractor (they show up as `n/a`, not as failures).

## 3. Totals per area and per milestone

Test functions (`UTEST`), v1 status as in PLAN §1.

| Milestone | Test fns | Tier A fns | Tier B fns | Tier C fns | Prototype cases A | Prototype cases B |
|---|--:|--:|--:|--:|--:|--:|
| M1 (parser internals; slice draws its fixtures from the M2 rows below) | 24 | 0 | 0 | 24 | 0 | 0 |
| M2 schema, types, restrictions, yang-data/structure, context | 163 | 94 | 59 | 10 | 586 | 467 |
| M3 xpath | 16 | 0 | 16 | 0 | 0 | 32 |
| M4 validation, list, data-tree API | 43 | 0 | 28 | 15 | 0 | 140 |
| M5 codecs (json/xml parse+print, lexers, metadata) | 57 | 0 | 46 | 11 | 0 | 134 |
| M6 diff, merge, yang-library | 37 | 0 | 36 | 1 | 0 | 86 |
| **In v1** | **340** | 94 | 185 | 61 | **586** | **859** |
| Out of v1 | 185 | - | - | - | - | - |
| **Total** | **525** | | | | | |

Out of v1 (185 functions): YIN parser 57, sorted-children index 38, tree printer 32, LYB 13,
schema-mount 10, C helpers (hash table, set, in/out, common) 26, plugin registry 4, nacm/openconfig
extension plugins 5.

Per area:

| Area | Test fns | Prototype cases (tier A/B) | v1 | Milestone |
|---|--:|--:|---|---|
| types (+restrictions) | 94 | 586 | in | M2 |
| yin parser | 57 | - | out | - |
| data tree sorted index | 38 | - | out | - |
| tree printer | 32 | - | out | - |
| schema compile | 32 | 408 | in | M2 |
| internal helpers | 26 | - | out | - |
| diff | 25 | 59 | in | M6 |
| yang parser (internal fns) | 24 | - | in | M1 |
| codec json | 22 | 79 | in | M5 |
| codec xml | 22 | 48 | in | M5 |
| validation | 21 | 78 | in | M4 |
| schema API + printers | 20 | 30 | in | M2 |
| xpath | 16 | 32 | in | M3 |
| data tree API | 15 | - | in | M4 |
| lyb | 13 | - | out | - |
| codec lexer | 11 | - | in | M5 |
| merge | 11 | 27 | in | M6 |
| context | 10 | - | in | M2 |
| schema-mount | 10 | - | out | - |
| extensions (metadata, structure, yang-data) | 9 | 36 | in | M2/M5 |
| list node | 7 | 62 | in | M4 |
| plugins | 4 | - | out | - |
| ext plugins nacm/openconfig | 5 | - | out | - |
| yang-library | 1 | - | in | M6 |

Reading the numbers: **340 of 525 test functions (65 %) are in v1**; of those 279 (82 %) are tier A/B,
i.e. reachable by extraction, and they hold **about 1,445 candidate cases** (586 tier A, 859 tier B,
generic mode, before dedupe; expect 10-20 % to collapse or be waived: identical schema+data appear
in several files, and stateful ones need the `sequence` op). Tier C is 61 functions (18 %): reuse
their input strings in Go unit tests of the owning package (`internal/parser`, `internal/xpath`,
codec lexers), not in the oracle corpus.

The Tcl tests add 87 non-interactive tests (CLI, tier N for oracle fixtures, but their **inputs**, the
28 modules and 42 data files under `tests/yanglint/`, are ready-made corpus documents; their
`ly_cmd` regexps become `cmd/yanglint-go` tests) and 21 yangre tests (pattern + string -> match,
directly usable as `xsdre` cases).

## 4. Coverage targets

Measured on a fixed denominator: the in-scope tier A+B candidate cases above (1,445 today, frozen
when the first full extraction lands, then re-counted in this file). A case counts as *converted* when
it is a manifest fixture with golden **and** an `assert` block (verdict + diagnostics subset) copied
from the C test and the oracle agrees with that assert (section 6 gate).

| End of | Cumulative in-scope A+B cases converted | What must be in |
|---|--:|---|
| M1 | >= 15 % (~220) | integer types (int8..uint64 = 141 cases), boolean, enumeration, empty (26), basic string, plus slice-relevant `list.c` and `test_validation.c` (when/must/mandatory/leafref) |
| M2 | >= 69 % (~1,000) | >= 95 % of all types/restriction cases, >= 90 % of `test_tree_schema_compile` (needs the multi-module `schema` step), structure, yang-data |
| M3 | >= 71 % | >= 95 % of `test_xpath` expressions (needs a result-carrying extractor, section 5.4) |
| M4 | >= 80 % | >= 95 % of `test_validation` and `list.c`; tier C data-tree API dispositioned |
| M5 | >= 89 % | >= 95 % of json/xml parser+printer cases and metadata |
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

### 5.3 Prototype results (chosen file: `types/int8.c`, the richest type test)

Independent accounting from `grep` of the call sites vs. what was extracted:

| Item | In the C file | Extracted | Skipped, with reason |
|---|--:|--:|---|
| schema load steps (`UTEST_ADD_MODULE` 54 + `UTEST_INVALID_MODULE` 32) | 86 | 59 | 25 YIN (out of v1), 2 `LY_EEXIST` (context-state dependent) |
| data parse call sites (5 direct + 57 through `TEST_*`/`LYD_TREE_CREATE` macros; the 6 further textual hits are inside `#define` bodies) | 62 | 62 | 0 |
| `CHECK_LOG_CTX` | 55 | 41 attached to their case | 14 belong to skipped steps (dropped on purpose, not mis-attached) |
| `lyd_parse_data_mem(..., LYD_LYB, ...)` round trips | 4 | 0 | out of v1 (LYB) |

Result: **121 cases extracted (59 schema + 62 data), 0 lost or mis-attributed by call-site
accounting; 41/41 invalid cases carry their asserted diagnostic.** Semantic check against libyang
v5.8.6 `yanglint`: **117/117 verifiable cases agree with the verdict asserted in the C test**
(4 n/a: parse-only LYB round trip legs). Precision on this file: 100 % structural, 100 % verdict.

Same tool, unchanged, over all 24 files under `tests/utests/types` and `restriction`
(tier A, 586 cases): **537/544 verifiable cases agree (98.7 %)**, 42 n/a. The 7 disagreements were not
root-caused one by one; the one inspected (`enumeration`, an `if-feature`-disabled enum) depends on the
`features` argument of `UTEST_ADD_MODULE`, which `-verify` extracts but does not pass to yanglint.
Over tier B files in generic mode the same check gives 607/777 (78 %): expected, because those tests
need multi-module ordering (`deviation`, `augment`, imports through `ly_ctx_set_module_imp_clb`),
non-verdict results (xpath value, diff text) or several parses on one tree; this is the extension
backlog, not noise.

Caveat on the verifier: yanglint `-t data` is operational (violations become warnings), so the check
uses `-t config -e [-n]` as the closest match to `lyd_parse_data` without `LYD_VALIDATE_OPERATIONAL`;
state-data cases can mis-verify. The real gate is the oracle (5.5), not yanglint.

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

A candidate enters `manifest.yaml` only if, running the oracle: (1) verdict == asserted verdict;
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
