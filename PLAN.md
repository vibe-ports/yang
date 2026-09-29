# yang — plan

A native Go (no cgo) implementation of the YANG runtime that libyang provides: schema parsing and
compilation, a generic data tree, full RFC 7950 validation, RFC 7951 JSON / XML encoding, diff.
Behaviour is ported from libyang (CESNET, BSD-3-Clause); API is idiomatic Go, not a transliteration.

Status: v1 plan rev 1 (after astra review), 2026-09-30. Reference: libyang v5.8.6 (tag, 2026-06-22).

## Goals (in priority order)

1. **Remove cgo.** Replace cgo bindings to libyang in Go projects: no C toolchain, no libyang /
   libpcre2 shared libraries, static + cross-compiled binaries. Production code never imports cgo;
   the only C in the repo is the test-only oracle helper.
2. **libyang compatibility.** Same inputs → same accept/reject, verdict, diagnostic code + data path,
   JSON/XML/diff output as libyang v5.8.6. Measured, not claimed (§5).
3. **Auditability of an AI-assisted port.** Every ported file carries
   `// Ported from libyang v5.8.6 src/<file>.c (BSD-3, © CESNET)`; `docs/port-map.md` maps libyang
   functions → Go functions; every divergence from libyang is in `conformance/deviations.md` with an
   RFC reason; the oracle container and corpus manifest let anyone re-run the comparison; CI
   publishes the compatibility report per area (schema, types, XPath, validation, codecs, diff).

## 0. Why write it (survey summary, 2026-09-30)

| Project | What | Gap vs "libyang in Go" |
|---|---|---|
| openconfig/goyang (Apache-2.0, 3 commits/yr) | schema parser+compiler | `refine` never applied, `uses`-augment ignored, if-feature not evaluated, must/when strings only, XSD patterns unsupported (RE2 only) |
| openconfig/ygot (Apache-2.0, active) | codegen + validation of generated GoStructs | no generic data tree (`ytypes.Validate` rejects non-GoStruct), no XPath, no must/when, `mandatory`/`unique` unchecked, no XML, no NMDA |
| signalbreak-labs/cambium (Apache-2.0, 2026-06, 1 author) | pure-Go schema IR + codegen, experimental pure-Go datatree, libyang backend via cgo | datatree experimental: no `deref()`, must/when don't see defaults, no RPC/action/notification data, value model to be rewritten; 190/222 differential cases |
| sdcio/yang-parser + data-server (Apache-2.0) | fork of danos/yang + goyacc XPath VM | tied to SDC stack; no `when` validation; not RFC-complete |
| freeconf/yang (Apache-2.0, stalled 2024) | own runtime | XPath stub, no leafref/must engine |
| cgo bindings (mattiaswal/go-libyang, libyango, go-sysrepo, ydk-go) | thin/app-internal | abandoned or archived; none maintained |
| NETCONF clients (scrapligo, nemith/netconf, Juniper/go-netconf) | transport | no YANG layer at all |

ygot is a codegen runtime (validation only over generated GoStructs), so it cannot host a generic
data tree. goyang is a separable parser + resolver: its parser/AST may be reusable even though its
compiler semantics (refine, uses-augment, if-feature, XSD) are incomplete. By feature count
goyang+ygot cover well under the 80 % bar (judgement from the table, not a measured number) and
the missing part — generic tree, XPath, validation — is the core.

**Decision: write new runtime; decide parser reuse and cambium collaboration in M0, before any
porting starts** (§4 M0 gates G1/G2). Outcome of G2 may legitimately be "contribute to cambium
instead" — that is a valid result.

## 1. v1 scope

In:
- YANG 1.0/1.1 text parser (modules, submodules, include/import with revision-date).
- Schema compilation: typedef/grouping/uses, `refine`, `augment` (top-level and in `uses`),
  `deviation` (all four deviates), `feature`/`if-feature` expressions, identities, extension
  instances (stored generically), `status`.
- Context = explicit schema set: implemented vs import-only modules, multiple imported revisions,
  features, deviations; build a Context **from** a server's `ietf-yang-library` (client use case) and
  generate one (server use case).
- Value model (decided before any codec, §2a): exact integers/decimal64 (no float64), union member
  identity, namespace-aware identityref/instance-identifier values, canonical forms.
- Built-in types with restrictions; XSD regex (`pattern`, `modifier invert-match`) through an
  XSD-regex parser + char-set algebra → RE2 (not string rewriting); `ietf-yang-types`/
  `ietf-inet-types` canonical values.
- Generic data tree: ordered instance storage with stable handles (user-ordered moves, keyless
  state lists, duplicate state leaf-lists), path API, `anydata`/`anyxml` payload variants
  (typed subtree | opaque XML | JSON value, explicit error on lossy conversion).
- Defaults as part of validation (libyang behaviour): implicit nodes added/removed during validation,
  provenance flag for explicit-vs-default, `with-defaults` (RFC 6243) report-all / trim / explicit /
  report-all-tagged.
- Encodings: RFC 7951 JSON and XML (NETCONF-shaped), parse + print, incl. RFC 7952 metadata
  (`ietf-origin`, `nc:operation`, `yang:insert`/`key`/`value`).
- Three distinct operations (libyang parse-only / local checks / full validation): `Parse` (syntax +
  types, for filtered get replies, RESTCONF subtrees, edit payloads, with explicit unknown-node
  policy), `CheckLocal`, `Validate` (complete datastore).
- Validation: types, leafref (require-instance), instance-identifier, mandatory, min/max-elements,
  unique, choice/case, must, when, duplicate/key checks, config/state separation.
  `when` semantics follow libyang exactly: fresh data with false `when` → error; data that was valid
  and becomes false-`when` after an edit → auto-deleted (needs per-node validation history);
  same history rule for choice-case replacement.
- Operations: RPC/action/notification request and reply as distinct kinds; reply bound to its
  request; nested action/notification take parent-instance context; external operational tree
  for references (libyang `-O` equivalent).
- XPath 1.0 + YANG function library (`current`, `deref`, `derived-from[-or-self]`, `re-match`,
  `enum-value`, `bit-is-set`) with prefix/module-name resolution.
- Datastores (NMDA, RFC 8342): per-datastore validation policy — running/candidate/intended:
  config only, full constraints; operational: config true + config false, semantic violations
  reported as **warnings** (libyang `LYD_VALIDATE_OPERATIONAL`), `origin` metadata with inheritance.
- Three separate edit/diff representations, never conflated: NETCONF `edit-config` operations
  (`nc:operation`), YANG Patch (RFC 8072) is out of v1, and libyang-style diff trees
  (`yang:operation`, may violate schema constraints by design). v1: diff + apply-diff + merge with a
  documented input contract; NETCONF edit semantics as a separate `ApplyEdit`.
- `cmd/yanglint-go`: minimal yanglint-like CLI (used by the conformance harness).

Out of v1 (explicit): YIN input/output, LYB binary format, schema-mount (RFC 8528), tree printer
(RFC 8340), YANG printer beyond debugging, extension plugins beyond `yang-data`/`structure`
(RFC 8791)/metadata, sorted/hash performance tricks of libyang until profiling asks for them,
NETCONF/RESTCONF transport, YANG Patch, public plugin interfaces (§2).

## 2. Idiomatic-Go rules (non-negotiable)

- Errors are values: `error` return; validation returns `*ValidationError` aggregating
  `[]Diagnostic{Severity (error|warning), Code, DataPath, SchemaPath, Line, AppTag, Message}` —
  enough for a NETCONF `rpc-error`/RESTCONF error mapping. No global log callback.
- No global state: everything hangs off `*Context` (libyang `ly_ctx`). Context immutable after
  `Compile()` → safe for concurrent readers; data trees are not goroutine-safe (documented).
- Inputs are `io.Reader` / `fs.FS` (module search path = `fs.FS`, so `embed.FS` works).
- GC, no dictionary/refcount; strings interned only if profiling shows need.
- libyang callback tables → interfaces, but **internal until a second consumer exists**. Public in v1:
  only `ModuleLoader` (import callback). Type/extension plugins stay concrete and internal.
- Resource budgets from day one: max nesting depth, grouping-expansion size, regex program size,
  XPath step budget, `context.Context` cancellation on long validations.
- Options are typed structs, not bit-flags.

## 2a. Design notes that must exist before code (M0 deliverables)

1. Value model (numeric storage, union member, namespace-aware values).
2. Data-node model (ordered storage, handles, anydata variants, default provenance, validation history).
3. XPath context contract: context node, accessible tree per RFC 7950 §6.4.1 and per datastore/
   operation, `when` evaluated on a dummy node, prefix bindings from the *defining* module (groupings),
   dependency ordering of `when` evaluation.

## 3. Package layout

```
github.com/vibe-ports/yang   (module root; package yang — Context, Module, public schema API)
  internal/parser/     YANG lexer + parser → parsed AST (lysp_*)
  internal/compile/    AST → compiled schema (lysc_*): groupings, augment, deviation, features
  internal/types/      built-in + ietf types
  internal/xsdre/      XSD regex → Go RE2 translator (XSD regexes are regular, no backrefs)
  internal/xpath/      XPath 1.0 + YANG functions, evaluated over the data tree with §2a.3 context
  data/                data tree, paths, defaults, validation, JSON/XML codecs, diff/merge
  cmd/yanglint-go/     CLI
  conformance/         oracle harness + corpus manifests
  conformance/oracle/  test-only C helper linked to libyang (NOT imported by Go code; production stays cgo-free)
```
Public API = root `yang` + `data`; everything else `internal/` until a real consumer needs it. Split `data/` only when a file boundary stops being enough.

## 4. Porting order (milestones)

Vertical slice first, breadth after: architecture-breaking feedback (value model, defaults, XPath,
`when`) must arrive in month 1–2, not month 5.

| # | Milestone | libyang source mainly | Exit criterion |
|---|---|---|---|
| M0 | Decisions + harness. **G1:** 2-day spike — does goyang's parser/AST keep everything a compiler needs (statement order, extension args, source positions)? reuse vs own parser. **G2:** evaluate cambium with our adversarial cases + talk to its maintainer → contribute vs write. Design notes §2a. Oracle C helper + pinned container (libyang 5.8.6, pcre2 version, build flags). XSD-regex prototype with char-set algebra and declared limits. | `tools/lint`, `plugins_types/string.c` | G1/G2 recorded in `docs/decisions/`; oracle runs on corpus in CI |
| M1 | **Vertical slice** on a small real module set (e.g. ietf-interfaces + ietf-ip + an augmenting module): parser → compile (grouping/uses/augment/refine/if-feature) → value model → JSON+XML parse → defaults → XPath subset → must/when/leafref/mandatory validation. Fuzz + budgets on parsers from here on. | all of the above, shallow | slice cases agree with oracle; design notes revised from findings |
| M2 | Schema breadth: full compiler, deviations, identities, all types, XSD regex complete, YANG 1.0 vs 1.1 differences | `schema_compile*.c`, `schema_features.c`, `tree_schema*.c`, `plugins_types/` | compiled-schema dump from oracle helper (`LYS_OUT_YANG_COMPILED` / `yanglint -f info` for inspection) equal for all corpus modules |
| M3 | XPath complete + YANG functions (`xpath.c`, 10 kLOC) | `xpath.c` | expression results (typed: node-set/string/number/boolean) equal via oracle helper |
| M4 | Validation complete: all passes, datastore policies, when-history, operations + replies | `validation.c`, `tree_data*.c` | verdict + diagnostics (code, data path, app-tag, severity) agree |
| M5 | Codecs complete: anydata/anyxml, RFC 7952 metadata, with-defaults modes | `parser_json.c`, `parser_xml.c`, printers | semantic tree equality; separate exact-printer suite |
| M6 | Diff/merge/apply-diff, `ApplyEdit`, yang-library build + ingest | `diff.c`, `context.c` | diff trees equal via oracle helper |
| M7 | Release qualification: long fuzz runs (differential), benchmarks vs libyang, API review | — | v1.0.0 |

## 5. Test strategy — libyang as oracle

- **Oracle:** a small test-only C helper linked against libyang v5.8.6 (yanglint alone cannot emit
  compiled-schema structure for comparison, typed XPath results, structured diagnostics or diff
  trees). Pinned container: libyang tag + pcre2 version + build flags. Brew is only for manual poking.
  Upgrading the oracle is a deliberate PR with golden-file diff.
- **Invocation policy per fixture** (recorded in the manifest, never implicit): data type
  (config / data-operational / get / edit / rpc / reply / notif), parse vs validate options,
  enabled features, implemented-module set, with-defaults mode, external operational tree.
  Note: yanglint `-t data` sets `LYD_VALIDATE_OPERATIONAL` — config violations become warnings.
- **What is compared:** (a) module accepted/rejected + phase (parse vs compile), (b) compiled schema
  dump, (c) verdict + diagnostics (code, severity, data path, app-tag; not message text),
  (d) **semantic** tree equality after parse (typed values, instance order kept), with a separate
  exact-printer suite for byte compatibility, (e) diff trees, (f) stateful sequences
  (valid → edit → revalidate) for when-auto-delete, choice replacement, defaults.
- **Oracle ≠ spec:** each fixture carries an RFC section tag; disagreements with libyang are resolved
  against the RFC and recorded in `conformance/deviations.md` with justification.
- **Corpus (public only):**
  - libyang `tests/` — modules, yanglint data, and schemas/data extracted from `utests/*.c`
    (BSD-3, attribution kept in `conformance/NOTICE`).
  - RFC examples: 7950, 7951, 8342, 8525, 8791, 6241, 7952.
  - Public models: IETF (YangModels/yang `standard/ietf`, per-file IETF Trust / Simplified BSD),
    OpenConfig public models (Apache-2.0), IEEE 802.1 published models. Each imported set is
    listed with source URL + commit in a manifest.
  - Generated: schema-driven random instance generator + mutators (drop mandatory, break leafref,
    violate range/pattern/unique/must) → both engines, compare verdicts.
- Unit tests in plain `testing`; golden files under `testdata/`. CI: `go test ./...` on every PR;
  oracle lane on PR + nightly fuzz.
- Reported per milestone: oracle agreement **and** RFC-section coverage (which normative
  requirements have at least one fixture). Agreement % alone is not claimed as conformance.

## 6. License

- This is a **source-informed port**: code is translated from libyang source, so the CESNET
  BSD-3 notice must travel with it: keep copyright + conditions + disclaimer in source, reproduce
  them in docs of binary distributions, no CESNET/contributor names for endorsement.
- BSD-3 does not force our license; we **choose BSD-3-Clause** for symmetry with upstream and
  simplest downstream story. `LICENSE` = our copyright + the CESNET notice for ported portions;
  files translated from a specific libyang file carry the header from Goal 3. README says "port of libyang" factually, never "official"/"endorsed";
  names avoid "libyang".
- Test corpora keep their own licenses; redistribution rights verified **per file/set** before
  commit (IETF Trust TLP / Simplified BSD, Apache-2.0 for OpenConfig, IEEE per-file terms). No GPL/AGPL code (e.g. ze-software/ze) is read or copied.

## 7. Provenance rules (employer-clean)

Not a classic clean-room (we read libyang source on purpose); the "clean" requirement is about the
employer:
- Only public inputs: libyang source, RFCs, public YANG models, public Go projects (read, not copy,
  unless license-compatible and attributed).
- Nothing from any employer: no code, models, schemas, test data, bug reports, or internal
  knowledge-derived test cases. Work only on personal machines/accounts; commit identity is the
  personal address; no work-related names in repo, README, commits, issues.
- Every corpus file enters through a manifest with a public URL — the manifest is the audit trail.

## 8. Size estimate

- libyang `src/` = 121 kLOC C incl. headers/comments/plugins; `.c` code lines without comments/blank
  ≈ 70.6 kLOC. Out of v1: YIN 5.5k, LYB 3.8k, tree printer 4.1k, YANG printer 2.7k, schema-mount 1.6k
  (≈ 18k raw) → in scope ≈ 103k raw / ≈ 60k pure code lines.
- Go drops free/dict/refcount/manual-buffer code but adds error plumbing: ≈ 0.7–0.8× of pure code →
  **≈ 40–50 kLOC Go + comparable test code.**
- No calendar promise yet. LOC ratio is a sanity bound, not an estimate: ≈ 45k impl + ≈ 45k tests,
  plus XSD regex and oracle helper outside the ratio. First real estimate is derived from **M1
  hours actual** (vertical slice) and re-issued with an uncertainty range; order of magnitude today:
  many months part-time, full parity staged over v1.x releases.
- Largest risks: M2 (deviation/augment/refine order, if-feature), M3/M4 (XPath accessible tree,
  `when` history), XSD regex.

## 9. Risks

| Risk | Mitigation |
|---|---|
| cambium overlap | decided in M0 (G2) before any porting, not after sunk cost |
| Oracle behaviour is itself buggy/version-specific | pin version; allowlist of known libyang deviations with RFC justification |
| XSD regex → RE2 edge cases (`\p{Is…}` blocks, subtraction, `\i`/`\c`, RE2 1000-repeat cap) | parser + char-set algebra prototype in M0 with declared limits; unsupported → explicit error, never silent approximation |
| Hostile schemas/data (DoS) | budgets + cancellation from M1, fuzzing from M1 |
| Performance on large operational trees | benchmarks from M3; add libyang-style hash/sorted children only if needed |
| Scope creep (YIN, LYB, schema-mount) | explicit out-of-scope list above; v1.x issues |

## 10. Review log — codex `gpt-6-astra`, 2026-09-30

Full text: `docs/review-astra-2026-09-30.md` (25 findings on rev 0). Two factual claims were checked
against libyang v5.8.6 source before acting: `yanglint -f info` = `LYS_OUT_YANG_COMPILED`
(`tools/lint/yl_opt.c:109`) and `-t data` adds `LYD_VALIDATE_OPERATIONAL` (`yl_opt.c:198`). Both correct.

| # | Finding | Verdict | What changed / why |
|---|---|---|---|
| 1 | XSD→RE2 understated | accept | parser + char-set algebra prototype in M0, declared limits |
| 2 | `when` auto-delete underspecified | accept | fresh-invalid vs became-invalid rule, validation history in node model |
| 3 | NMDA reduced to config/state | accept | per-datastore policy; operational → warnings; origin inheritance |
| 4 | XPath context not defined | accept | §2a.3 design note before evaluator |
| 5 | defaults as isolated M3 feature | accept | defaults part of validation, provenance, report-all-tagged |
| 6 | no partial-data contract | accept | Parse / CheckLocal / Validate |
| 7 | operation validation inputs | accept | request/reply kinds, parent context, external operational tree |
| 8 | anydata/anyxml missing | accept | payload variants in node model |
| 9 | ordering/instance identity | accept | ordered storage, stable handles, keyless lists |
| 10 | value model | accept | §2a.1, exact numerics |
| 11 | context/yang-library incomplete | accept | schema sets, import-only modules, build Context from yang-library |
| 12 | edits vs diffs conflated | accept | separate ApplyEdit / ApplyDiff contracts; YANG Patch out of v1 |
| 13 | Diagnostic too thin | accept | severity, code, schema+data path, line |
| 14 | wrong oracle output for M2 | accept | helper dump; `-f info` for inspection |
| 15 | yanglint can't be the whole oracle | accept | test-only C helper; production remains cgo-free |
| 16 | oracle invocation policy | accept | per-fixture mode manifest, pinned build |
| 17 | byte equality | accept | semantic equality + separate printer suite |
| 18 | agreement % ≠ conformance | partial | RFC-section tags + coverage report + deviations file accepted; fully independent hand-written expected outcomes rejected as too costly for one developer — RFC tag + recorded disagreement resolution is the compromise |
| 19 | milestone order | accept | M1 = vertical slice |
| 20 | license rationale / "clean-room" wording | accept | BSD-3 is a choice, not an obligation; "source-informed port", provenance rules |
| 21 | goyang conflated with ygot | partial | parser-reuse spike G1 in M0 accepted; "compiler fixes upstream" rejected: refine/uses-augment/deviation-order gaps are structural and goyang gets ~3 commits/yr, so upstreaming would gate us on a slow review queue. The coverage figure is relabelled as a judgement |
| 22 | cambium evaluated too late | accept | gate G2 in M0 |
| 23 | estimate has no basis | accept | calendar removed; re-estimate from M1 actuals |
| 24 | premature public interfaces | accept | plugins internal; only ModuleLoader public |
| 25 | hardening deferred | accept | budgets, cancellation, fuzz from M1 |
