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

## Unsupported (our limits, not libyang deviations)

| ID | Area | Limit | Reason |
|---|---|---|---|
| U-0001 | pattern | repeat count > 1000, nesting > 1000 → `ErrUnsupported` | Go RE2 limits; never approximated |
