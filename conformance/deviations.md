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
| D-0010 | xpath: number precision | C `long double`, 80-bit x87 on the canonical amd64 oracle host: `string(9007199254740993)` = `9007199254740993`, `number('1e400')` finite | IEEE double, as XPath 1.0 requires: `9007199254740992`, ±Infinity; values outside the long double range are NaN in both (strtold ERANGE) | XPath 1.0 §3.5 | internal/xpath `knownDiff` |
| D-0044 (candidate) | status: main module <-> submodule reference | the uses->grouping status check compares parsed modules, so a current node using an obsolete grouping of an included submodule is accepted | mirrored for now; RFC says "within the same module" (a submodule is part of it) | RFC 7950 §7.21.2 | compile/status-submodule-uses-ok |
| D-0045 (candidate) | status: `when`/`must` referencing a deprecated/obsolete node | only a warning (`When condition ... may be referencing deprecated node`) | mirrored for now | RFC 7950 §7.21.2 | compile/status-when-warning |
| D-0046 (candidate) | grouping validation depends on compile history | an unused top-level grouping is validated only if no earlier compile used it (`LYS_USED_GRP` is never cleared), so a module's verdict depends on load order | mirrored for now; a module's validity should not depend on what was loaded before it | RFC 7950 §7.6.5 | compile/grp-used-elsewhere-first, grp-unused-top-level-invalid |
| D-0047 (candidate) | data `when` evaluation order | `when` conditions are resolved once, from the end of a queue; declaration order of leaves with dependent defaults decides the outcome (`a` kept vs both removed) | mirrored for now (design 03 rule 5) | RFC 7950 §7.21.5 | compile/when-order-a-after-b, when-order-b-after-a |
| D-0048 (candidate) | refine of the same target from nested uses | refines are merged by node-id text and module; the innermost uses wins; the merge ignores the context node, so a same-text inner refine collected while an outer refine is pending is applied to the outer target and lost for its own (refine-same-text-leak) | mirrored for now; RFC 7950 §7.13.2 does not define the nested case | RFC 7950 §7.13.2 | compile/refine-nested-same-target, refine-same-text, refine-same-text-leak |
| D-0049 (candidate) | list key with if-feature | a key leaf with `if-feature` is accepted when the feature is enabled (rejected only when disabled: `Key "k" is disabled.`) | mirrored for now | RFC 7950 §7.8.2 (a key MUST NOT have if-feature) | compile/list-key-iffeature-enabled |

## Unsupported (our limits, not libyang deviations)

| ID | Area | Limit | Reason |
|---|---|---|---|
| U-0001 | pattern | repeat count > 1000, nesting > 1000 → `ErrUnsupported` | Go RE2 limits; never approximated |
| U-0006 | YANG parser: warnings | libyang's parser warnings are not reported: empty argument (`CHECK_NONEMPTY`), control characters in enum names, revision order, includes in a YANG 1.1 submodule | no warning channel in `Parse`/`Build` yet; verdicts unaffected |
| U-0010 | types: libyang type plugins not ported yet | `ietf-yang-types` `date`, `date-no-zone`, `time`, `time-no-zone`, `xpath1.0`; `libnetconf2-netconf-server` `time-period`; `ietf-netconf-acm` `node-instance-identifier`; `yang` `instance-identifier-keys`. These typedefs store as their base type (strings with the typedef's restrictions), so canonical forms and errors differ from libyang | not used by the M1 corpus; ported when a fixture needs them (xpath1.0 after internal/xpath) |
| U-0003 | xpath: expression size | more than `xpath.MaxTokens` (4 194 304) tokens → LYVE_XPATH error | libyang only limits the length to UINT32_MAX; the cap bounds AST memory |
| U-0023 | extensions: `yang-data`, `structure`, `augment-structure` | an instance in any parsed module makes `Load` fail with `ErrUnsupported` | the consumers (RESTCONF yang-data, structure-ext data trees) are a later milestone; libyang accepts them (fixture ext/yang-data-unsupported) |
