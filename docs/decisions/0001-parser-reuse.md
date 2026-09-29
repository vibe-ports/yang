# ADR 0001 — YANG text parser: reuse goyang or port libyang

Status: proposed (gate G1, M0) · Date: 2026-09-30 · Spike: `spike/g1-goyang/` (`go run . cases|fidelity|bench <dir>`)

## Context

We need a YANG text front end that matches libyang v5.8.6 on accept/reject, keeps every statement
(incl. extension instances) in order with positions, and yields byte-identical argument strings.
Can openconfig/goyang `pkg/yang` (a80f279, 2025-11-13) replace a port of `parser_yang.c`? goyang has
two layers: `yang.Parse` (lex.go + parse.go → generic `*Statement` tree) and `Modules.Parse`, which
adds `ast.go` (reflection mapping onto typed structs).

## Options

1. **Reuse**: import goyang and use `yang.Parse`, plus our own typed layer on top.
2. **Vendor + fork**: copy lex.go/parse.go under `internal/`, fix the divergences, keep Apache-2.0 notices.
3. **Own port**: port libyang's tokenizer (`parser_yang.c` lines 130–918) and write a typed statement builder driven by the RFC 7950 §14 grammar.

## Evidence

### Strictness (45 cases, `go run . cases`)

`[ly]` = adapted from libyang `tests/utests/schema/test_yang.c` (BSD-3, © CESNET); the rest from RFC 7950 §6/§7/§14. A = accept, R = reject.

| cases | Parse | +AST | yanglint |
|---|---|---|---|
| valid baseline, `'a\x'` [ly], `yang-version "1.1"`, `"a" + 'b'` [ly], CRLF, defined extension instance (1, 6, 9, 19, 23, 41) | A | A | A |
| bad escapes `"\x"` 1.1 / 1.0, `"\s"` [ly]; stray `;`, `};`; `"a" + b` [ly]; `hello"x"` [ly]; open `/*` [ly]; quoted keyword; missing/extra `}` (2–4, 21–22, 24–26, 28, 31–32) | R | R | R |
| unknown bare keyword [ly]; duplicate `description`/`type`/`yang-version` [ly]; leaf without `type`; module without `namespace` [ly]; `position` in `enum` [ly]; `input` in container (7, 11–14, 20, 38, 42) | **A** | R | R |
| `pattern "\d+"` 1.1; identifiers `1abc`, `a#b`, `m:m:string` [ly]; `yang-version 2`; unquoted `abc//def`; missing / empty `""` argument [ly]; second module in file [ly]; `mandatory maybe`; `config True`; `revision 2020-13-45`; `min-elements -1` [ly]; `import` after `container`; U+0001 in string (5, 15–18, 27, 29–30, 33–37, 39–40) | **A** | **A** | R |
| extension with un-imported prefix / undefined extension (8, 10)¹ | A | A | R |
| **valid** 1.1: two `augment` in one `uses`; `choice` as shorthand case; `when { reference }` (43–45) | A | **R** | A |

¹ compile-level in libyang, listed for completeness.

Agreement with yanglint: **`Parse` 20/45, `Parse+AST` 25/45**. The raw parser is a tokenizer: any
keyword, argument or cardinality passes. The AST layer adds some checks but **falsely rejects valid
RFC 7950 input** (43–45) because its structs model a grammar subset (e.g. `Uses.Augment` is a single
`*Augment`).

### Argument fidelity (11 strings, compared with libyang's YIN `<text>`; `go run . fidelity`)

8/11 equal (concatenation, escapes, indent stripping, single quotes, Unicode). Differences:

- tab crossing the indent column: goyang `"hello\n\t     world!"`, libyang `"hello\n      world!"` (libyang expands the tab to spaces);
- escaped `\t` before newline [ly]: goyang trims it (it trims after unescaping), libyang keeps `"hello \t\n…"`;
- CRLF inside a double-quoted string: goyang keeps `\r`, libyang yields `\n`.

### Tree shape and positions

Good: every statement in source order, incl. prefixed extension instances with concatenated
arguments and nested substatements. Gaps: only the keyword position (no argument/end position);
columns count a tab as 1; `line`/`col` are unexported (only the `Location()` string); no quote style
(needed for 1.0/1.1 escape rules). Errors are one newline-joined string, capped at 8, no codes; some
AST errors have no location (`description: already set`).

### Corpus: YangModels/yang `standard/ietf/RFC` (commit 0e28ed8, 492 files, 9.5 MB, Apple M-series, best of 5)

| | time | throughput | rejects |
|---|---|---|---|
| goyang `Parse` | 93 ms | 102 MB/s | 0 |
| goyang `Parse+AST` | 132 ms | 72 MB/s | 14 (all valid: `augment: already set` ×10 (same error as case 43), choice-in-choice ×2, `when{reference}` ×2) |
| yanglint, per file (includes compilation and process start) | 5.1 s | — | 2 at parse level: `ietf-template` (`revision date-revision`), which goyang `Parse` accepts; the other 92 failures are missing imports, standalone submodules, or compile errors |

Speed does not separate the options.

### API, maintenance, licence

- `pkg/yang` deps: stdlib + `go-cmp` (yangtype.go). lex.go + parse.go = **597 code lines** (860 raw);
  no global state besides default `errout = os.Stderr`; `ast.go` builds reflection maps in `init()`.
  Upstream ~3 commits/yr.
- Apache-2.0, no NOTICE file. Vendoring requires shipping its LICENSE, keeping the "Copyright 2015
  Google Inc." headers, adding "modified" notices (§4b), and declaring BSD-3 + Apache-2.0 files —
  at odds with Goal 3's single "ported from libyang" provenance.

### Size of the alternative (libyang v5.8.6)

| libyang part | raw lines | code lines |
|---|---|---|
| `parser_yang.c`, total | 4999 | 3707 |
| — tokenizer (buffering, comments, `read_qstring`, `get_argument`, `get_keyword`) | 789 | 539 |
| — typed statement parsers (`parse_module` … `parse_deviate`), with cardinality, order and argument checks | ~4100 | ~3150 |
| `parser_common.c` `lysp_stmt_*` (generic stmt → lysp, for extension instances; duplicates the above) | ~3070 | ~2210 |
| helpers in `tree_schema_common.c` (`lysp_check_identifierchar`, `lysp_check_stringchar`, date/enum checks) | ~150 | — |

Go estimate: tokenizer → generic tree with positions and quote style **600–700 lines** (goyang's
size); one table-driven typed builder (§14 substatement tables: cardinality, order, argument kind)
replacing the typed halves of `parser_yang.c`, `parser_yin.c` and `lysp_stmt_*` **2000–2500 lines**.
The builder is needed under every option: goyang's AST layer is disqualified (43–45, 14 corpus false
rejects).

## Decision (recommendation)

**Own port (option 3)**: port libyang's tokenizer, add our own table-driven typed builder; do not
import goyang. Reuse saves only the tokenizer (~600 of ~3000 lines), and that part needs fixes for
fidelity (tab expansion, escaped-whitespace trim order, CR), strictness (1.1 escapes in `pattern`,
identifier/keyword syntax, `//` in unquoted strings, control chars, required arguments, trailing
garbage) and API (argument/end positions, quote style, structured errors) — a fork in all but name,
still carrying Apache obligations. Porting `get_argument`/`read_qstring` gives libyang's exact
behaviour plus port-map traceability. We keep goyang's design idea: generic `Statement` tree, then
typed build, so one builder serves YANG, YIN and extension-instance substatements.

## Consequences

- M1: `internal/parser/yang` (tokenizer ported from `parser_yang.c`) + `internal/parser/stmt` (typed
  builder), both with the "Ported from libyang" header.
- The 45 strictness cases and 11 fidelity strings become the first oracle-differential fixtures;
  `[ly]` rows keep their libyang attribution.
- goyang never enters `go.mod`; the spike module can be deleted once this ADR is accepted.
- Risk: tab/indent arithmetic and UTF-8 checks are edge-case dense; fuzz the tokenizer against the
  oracle from M1.
- If option 2 is chosen instead, the fix list above is the minimum fork delta, plus Apache notices.
