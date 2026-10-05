# Known deviations from libyang v5.8.6

Every intentional difference between this port and libyang, and every libyang behaviour we consider
questionable. Format: id · area · libyang behaviour · ours · RFC reference · fixture.

| ID | Area | libyang | Ours | RFC | Fixture |
|---|---|---|---|---|---|
| D-0001 (candidate) | `when` + defaults | `when` reading a **top-level** default leaf rejects, same check one level down accepts (found in ADR 0002, cases 24/26) | undecided — mirror libyang until analysed | RFC 7950 §7.6.1, §7.21.5 | spike/g2-cambium/cases/24, 26 |
| D-0002 | pattern: class subtraction | `[a-z-[aeiou]]` mis-translated for PCRE | XSD semantics | RFC 7950 §9.4.5, XSD Part 2 App. F | internal/xsdre oracle_test |
| D-0003 | pattern: `\w` `\s` `\d` | Perl/PCRE definitions | XSD definitions (`\w` = all but P, Z, C) | XSD Part 2 §F.1.1 | internal/xsdre oracle_test |
| D-0004 | pattern: `.` | matches `\r` | excludes `\n` and `\r` | XSD Part 2 §F.1.1 | internal/xsdre oracle_test |
| D-0005 | pattern: `\C`, `\i`, `\c`, `\I`, `\P{IsX}`, non-BMP blocks | `\C` = any code unit; others rejected | XML-name classes, all blocks | XSD Part 2 §F.1.1 | internal/xsdre oracle_test |
| D-0006 | pattern: block names | prefix match (`IsGreekExtended` → Greek) | exact name | XSD Part 2 §F.1.1 | internal/xsdre oracle_test |
| D-0007 | pattern: escaped `\^` / `\$` | never matches `^` / accepts `\$` | literal `^`; `\$` is a syntax error | XSD Part 2 §F.1.1 | internal/xsdre oracle_test |
| D-0008 | pattern: invalid XSD | accepts PCRE-only syntax (`a+?`, `(?:`, `{`, `a{,3}`, `\b`, …) | `ErrSyntax` — **risk**: a public model libyang loads may fail; scan corpus before M2, opt-in compat mode if needed | XSD Part 2 App. F | internal/xsdre oracle_test |
| D-0020 | if-feature expression `a)(` | `lys_compile_iffeature` pops an empty operator stack: yanglint 5.8.6 crashes | rejected: "Invalid value … of if-feature - processing error." | RFC 7950 §7.20.2 (`if-feature-expr` grammar) | internal/parser build_test (TestParseIfFeature, FuzzIfFeature); excluded from oracle runs |
| D-0021 (candidate) | if-feature expression `not (not a)` | rejected ("processing error." on arm64, an empty message on x86_64 — the message is architecture-dependent, so oracle tests compare the verdict only): the first pass treats `not ( not` as a double `not`, the second does not | same as libyang (Goal 2), though RFC 7950 §7.20.2 allows it | RFC 7950 §7.20.2 | internal/parser build_test (iffErrors), oracle_test |
| D-0025 | ietf-yang-types `date-and-time` canonical form | a value with a known offset is printed in the host's local time zone (`ly_time_time2str` → `localtime_r`), so output depends on `TZ` | always UTC, printed `+00:00` (the oracle runs with `TZ=UTC`, so goldens agree) | RFC 6991 §3 / RFC 9911: any offset denotes the same instant; a host-independent canonical form | types/dt-known-zone |
| D-0026 (candidate) | `date-and-time` offset `-00:MM` | the offset sign is taken from the hour value, so `-00:30` is applied as `+00:30` (`ly_time_str2time`) | mirrored for now | RFC 3339 §5.6: `-00:30` is half an hour behind UTC | types/dt-minus-half-hour |
| D-0027 | ietf-inet-types addresses containing NUL | the value is cut at the first NUL before `inet_pton` (`strndup`), so `1.2.3.4\0junk` stores as `1.2.3.4` | rejected ("Failed to store …") | RFC 6991 §4: the pattern does not allow NUL | — (XML/JSON inputs cannot carry NUL; internal/types tests) |
| D-0010 | xpath: number precision | C `long double`, 80-bit x87 on the canonical amd64 oracle host: `string(9007199254740993)` = `9007199254740993`, `string(9223372036854775807)` exact, `string(number('1e-400'))` = `0.0` (tiny but non-zero), `number('1e400')` finite | IEEE double, as XPath 1.0 requires: `9007199254740992`, `9223372036854775808.0`, `0`, ±Infinity; values outside the long double range are NaN in both (strtold ERANGE). Number→string and number comparison round the 80-bit long double nearest to the double's shortest decimal (`string(0.15)` = `0.2` as libyang), so only results of arithmetic can still differ in the last bits | XPath 1.0 §3.5 | internal/xpath `knownDiff` (replay_test.go) |
| D-0044 (candidate) | status: main module <-> submodule reference | the uses->grouping status check compares parsed modules, so a current node using an obsolete grouping of an included submodule is accepted | mirrored for now; RFC says "within the same module" (a submodule is part of it) | RFC 7950 §7.21.2 | compile/status-submodule-uses-ok |
| D-0045 (candidate) | status: `when`/`must` referencing a deprecated/obsolete node | only a warning (`When condition ... may be referencing deprecated node`) | mirrored for now | RFC 7950 §7.21.2 | compile/status-when-warning |
| D-0046 (candidate) | grouping validation depends on compile history | an unused top-level grouping is validated only if no earlier compile used it (`LYS_USED_GRP` is never cleared), so a module's verdict depends on load order | mirrored for now; a module's validity should not depend on what was loaded before it | RFC 7950 §7.6.5 | compile/grp-used-elsewhere-first, grp-unused-top-level-invalid |
| D-0047 (candidate) | data `when` evaluation order | `when` conditions are resolved once, from the end of a queue; declaration order of leaves with dependent defaults decides the outcome (`a` kept vs both removed) | mirrored for now (design 03 rule 5) | RFC 7950 §7.21.5 | compile/when-order-a-after-b, when-order-b-after-a |
| D-0048 (candidate) | refine of the same target from nested uses | refines are merged by node-id text and module; the innermost uses wins; the merge ignores the context node, so a same-text inner refine collected while an outer refine is pending is applied to the outer target and lost for its own (refine-same-text-leak) | mirrored for now; RFC 7950 §7.13.2 does not define the nested case | RFC 7950 §7.13.2 | compile/refine-nested-same-target, refine-same-text, refine-same-text-leak |
| D-0049 (candidate) | list key with if-feature | a key leaf with `if-feature` is accepted when the feature is enabled (rejected only when disabled: `Key "k" is disabled.`) | mirrored for now | RFC 7950 §7.8.2 (a key MUST NOT have if-feature) | compile/list-key-iffeature-enabled |
| D-0011 | xpath: C integer conversions | `(long long)` / `(int32_t)` of NaN, ±Infinity and out-of-range values is UB; results depend on the host | pinned to the canonical oracle host (amd64, x87 `fistp`): NaN and out of range give the "integer indefinite" INT64_MIN / INT32_MIN, with libyang's arithmetic on top: `floor(number('1e30'))` = -9223372036854775808, `ceiling(0 div 0)` = -9223372036854775807, `substring('abc', 1, 2147483648)` = `""` | C11 §6.3.1.4 (UB) | protocol-v2/xpath-ceiling-number-1e30-0-5, xpath-ceiling-0-div-0 (identical on arm64); host-specific cases only in internal/xpath testdata |
| D-0012 | xpath: libyang crashes | see "libyang crash cases" below: lyoracle dies (NULL dereference) or fails internally | the defined answer listed below, asserted by the replay (`crashAnswers`) | — | internal/xpath testdata `"crash": true` cases |
| D-0013 | xpath: hash lookup of list instances | an unprefixed (JSON) child step to a list / leaf-list is restricted to the context node's module when its predicates compile for a hash lookup (`eval_name_test_try_compile_predicates`): `[key = value]` for every key in order (leaf-list `[. = value]`), key written as a NameTest without axis whose module is the key's, value without top-level `or`/`and` whose atoms (`lyxp_atomize`) reach no list/leaf-list other than current()'s node and no child of the list; otherwise names match in every module | the same rule, with the atoms found by walking the value's paths over `SchemaNode.Child` / `SchemaInfo.TopLevel` (absolute, current()-rooted and relative paths of plain child / `.` / `..` steps). Remaining differences, both directions: a value path through another primary expression (`deref()`, `(…)`), with a predicate, `//`, an explicit axis or `node()`/`text()` is rejected (libyang may compile it: we match in every module instead of one); a path through a schema node we cannot resolve stops the walk (accepted, as libyang). Only observable when another module adds a same-named list/leaf-list under the same parent | RFC 7950 §6.4.1, RFC 7951 §6.4 | protocol-v2/xpath-aug-key-* |

## libyang crash cases (D-0012)

| Input | libyang v5.8.6 | Ours |
|---|---|---|
| `deref(..)`, `enum-value(..)`, `bit-is-set(.., 'x')` (first node is the document root) | NULL dereference of `node->schema` | empty node-set / NaN / false |
| `derived-from(x, 'id')` with an unprefixed identity, JSON format, `current()` = root and no current module | NULL module dereference | `Identity "id" not found in module "".` |
| `string()` of an anydata/anyxml with empty content (`string(any)`, `string(.)`) | `strlen(NULL)` after `strtok_r` (oracle: crash) | `""` / `"\n"` |
| `string()` of a subtree containing an action node | `LOGINT` (internal error) in `cast_string_recursive` (from the source; lyoracle cannot load an action without its operational parent) | the action is dumped like a container (TestActionStringValue) |

## Known libyang behaviour (mirrored)

Questionable but reproduced on purpose, each confirmed by the oracle (fixtures `protocol-v2/xpath-*`,
`internal/xpath/testdata/oracle-pv2.jsonl`).

| Behaviour | Example |
|---|---|
| A predicate applies to the whole result of its step, not per context node | `l/k[2]` → one node |
| A numeric predicate is truncated | `l[1.5]` = `l[1]` |
| `following`/`preceding` are empty when the node has no sibling in that direction; `preceding` includes ancestors | `l[1]/k/preceding::*` = ∅ |
| `//` before `node()` / `text()` is ignored; `comment()` is `text()` | `count(.//text())` = 0 |
| `ancestor::*` matches the document root | `count(ancestor::*)` from `/pv2:c` = 1 |
| Unprefixed JSON names: the context node's module when it has such a child schema node (and, for a list, key predicates compile), else every module | `count(grouped)` = 1, `count(//grouped)` = 2 |
| Descendants of a when-false node stay reachable through `//name` (only the node itself is hidden) | — |
| Numbers compare as `printf("%Lf")` text: equal to 6 decimals, NaN compares as text | `0.0000001 = 0.0000002`, `0 div 0 = 0 div 0`, `0 div 0 < 1` |
| Number to string is `%lld` or `%03.1Lf` | `string(0.25)` = `0.2` |
| String to number is `strtold` over the whole string | `number('0x1A')` = 26, `number(' 12')` = 12, `number('12 ')` = NaN |
| `floor`, `round`, `ceiling` truncate; `floor` of NaN/Infinity returns the context set | `floor(-1.5)` = -1, `round(-2.7)` = -2, `ceiling(-1.5)` = 0 |
| String functions count bytes | `string-length('жж')` = 4, `substring('жabc', 2, 2)` splits a character |
| `normalize-space` changes nothing unless there is leading, trailing or repeated white space | `normalize-space('a\tb')` keeps the tab |
| string-value is an indented dump of the subtree's term values | `string(stats)` = `"\n  5\n"` |
| A literal compared with a node is canonized by the node's type first | `u2 = '050'`, `id = 'two'` |
| An even number of unary `-` leaves the operand uncast | `--'5'` is the string `5` |
| `deref()` returns targets in resolution order, unsorted (Release build) | — |
| `enum-value()` is NaN for an enumeration member of a union; `derived-from()` skips `text()` items | `enum-value(en)` = NaN |

## Unsupported (our limits, not libyang deviations)

| ID | Area | Limit | Reason |
|---|---|---|---|
| U-0001 | pattern | repeat count > 1000, nesting > 1000 → `ErrUnsupported` | Go RE2 limits; never approximated |
| U-0006 | YANG parser: warnings | libyang's parser warnings are not reported: empty argument (`CHECK_NONEMPTY`), control characters in enum names (revision order, duplicate revisions and includes in a YANG 1.1 submodule are reported by the loader, `internal/compile`) | no warning channel in `Parse`/`Build` yet; verdicts unaffected |
| U-0021 | loader: YIN | a `.yin` file chosen by the module search (`name.yin`, `name@rev.yin`) → `ErrUnsupported`; it is never skipped in favour of a `.yang` file, which would change the revision libyang loads. The `Loader` callback returns YANG text only | YIN parser out of v1 (PLAN §1); design 06 §1.2 |
| U-0022 | loader: search directories | one module search opens at most `Options.MaxSearchDirs` directories (default 10 000), then `ErrBudget`; libyang follows symlinked directories without cycle detection until a path exceeds `PATH_MAX`. Directory paths longer than 4096 bytes are skipped silently as libyang's failing `opendir`, but the length is that of the path inside the `fs.FS`, not the absolute path | two looping symlinks make libyang's walk exponential; design 06 §1.2, Go test `TestSymlinkCycle` |
| U-0010 | types: libyang type plugins not ported yet | `ietf-yang-types` `date`, `date-no-zone`, `time`, `time-no-zone`, `xpath1.0`; `libnetconf2-netconf-server` `time-period`; `ietf-netconf-acm` `node-instance-identifier`; `yang` `instance-identifier-keys`. These typedefs store as their base type (strings with the typedef's restrictions), so canonical forms and errors differ from libyang | not used by the M1 corpus; ported when a fixture needs them (xpath1.0 after internal/xpath) |
| U-0002 | xpath: metadata | the attribute axis (`@x`) and `lang()` see no metadata: `xpath.Node` exposes none yet | added when `data/` carries RFC 7952 metadata (M2) |
| U-0003 | xpath: expression size | more than `xpath.MaxTokens` (4 194 304) tokens → LYVE_XPATH error | libyang only limits the length to UINT32_MAX; the cap bounds AST memory |
| U-0023 | extensions: `yang-data`, `structure`, `augment-structure` | an instance in any parsed module makes `Load` fail with `ErrUnsupported` | the consumers (RESTCONF yang-data, structure-ext data trees) are a later milestone; libyang accepts them (fixture ext/yang-data-unsupported) |
