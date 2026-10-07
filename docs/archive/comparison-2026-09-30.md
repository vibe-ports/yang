# How this project compares (state as of 2026-09-30)

> **Historical, not maintained.** A dated evaluation from M0 (2026-09-30), archived from
> `docs/comparison.md`. The "this repo" column is outdated: the parser, compiler, types, XPath
> and data tree it lists as "not yet" have since been written. Current scope and status:
> [PLAN.md](../../PLAN.md) and [README.md](../../README.md); measured compatibility:
> `conformance/`.

Facts about other projects come from their public repositories on that date (details and
evidence: `docs/decisions/0001-parser-reuse.md`, `0002-cambium.md`). Our column is **today's
state**, not the plan — see PLAN.md for the plan.

| | **vibe-ports/yang** (this repo) | signalbreak-labs/cambium | openconfig/goyang + ygot |
|---|---|---|---|
| Goal | pure-Go libyang port, libyang-compatible | ordered schema IR + typed codegen; libyang backend for data | schema parser + Go/proto codegen for OpenConfig/gNMI |
| cgo | none, enforced in CI | pure-Go schema tier; data reference = optional cgo libyang backend | none |
| License | BSD-3-Clause | Apache-2.0 | Apache-2.0 |
| YANG parser | not yet (M1, own parser — ADR 0001) | yes (vendors goyang's lexer) | yes; lenient: 20/45 parse-level cases agree with libyang |
| Schema compile (refine, uses-augment, deviations, if-feature) | not yet (M1–M2) | yes | partial (refine / uses-augment ignored, if-feature not evaluated) |
| Generic data tree | not yet (M1) | experimental `datatree` | no (validation only on generated structs) |
| XPath (must/when, YANG functions) | not yet (M1–M3) | subset; `deref()` skipped | none |
| Validation vs libyang | — | 16/26 of our adversarial cases genuinely agree | n/a |
| NMDA / validation modes | planned (M4) | no | no |
| XML + JSON (RFC 7951) | planned (M1, M5) | yes (datatree, anydata XML missing) | JSON only |
| XSD `pattern` regex | **done**: own XSD→RE2 compiler, IETF type patterns 1655/1655 agree with libyang | Go regexp | RE2 approximation |
| Compatibility evidence | pinned libyang oracle (`lyoracle`), goldens, deviation register | differential lane vs its libyang backend (190/222 cases) | — |
| Maturity | M0: foundations only | young project (about 3.5 months) | mature, widely used, low activity (goyang) |

Summary: today cambium and goyang/ygot are further along on schema handling; this project's
differentiators are a cgo-free *data* engine as the primary path, NMDA/validation modes, and
libyang compatibility measured and published per area. Re-checked at each milestone.
