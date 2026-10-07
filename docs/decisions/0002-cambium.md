# ADR 0002 — Gate G2: contribute to cambium or write our own runtime?

Status: proposed · Date: 2026-09-30 · Evaluated: signalbreak-labs/cambium @ `884a3fd` (2026-09-27)

## Context

PLAN §4 gate G2 (survey: [docs/archive/survey-2026-09-30.md](../archive/survey-2026-09-30.md)): are our goals (no cgo in the production import graph; libyang v5.8.6
compatibility; auditability) better served by contributing to cambium, the only active pure-Go
project with a generic data tree?

cambium has three tiers: a stable pure-Go schema IR + typed-struct codegen (its core product, built
around "declaration order is structural"); an **experimental** pure-Go `datatree`; and an optional
cgo libyang backend. Its docs are explicit that the libyang backend is "the reference data engine"
and that "full RFC-7950 data validation correctness is delegated to libyang; `datatree` … does not
yet replace it". The long-term goal is a complete pure-Go data tier, graduated by a differential
lane against its own libyang backend (190/222 corpus cases flagged).

## Evidence

**Architecture fit (read from docs + `go/datatree`, ~5.5 kLOC non-test)**

- *Value model*: leaf values are raw JSON tokens with XML layered on; a "neutral value model" is
  planned and will break the API. Our §2a (exact ints/decimal64, union member identity,
  namespace-aware identityref/instance-identifier) is the thing they have not built yet.
- *Data nodes*: ordered slices, keys first, user order preserved, system order canonicalised like
  libyang; keyless state lists keep input order. Strong and matches libyang. Anydata: opaque JSON
  only, no XML, no cross-format conversion.
- *Defaults*: `ApplyDefaults` exists, but `must`/`when` do not see implicit defaults or absent
  non-presence containers (documented; confirmed below).
- *XPath*: own evaluator, XPath 1.0 subset + `re-match`, `bit-is-set`, `derived-from(-or-self)`.
  Unimplemented functions (`deref`, `enum-value`, `translate`, …) make the check **silently
  skipped** — a false accept, the wrong direction for a validator.
- *Validation modes / NMDA*: none. No config vs operational vs edit distinction, no RPC / action /
  notification / reply data, no yang-library/NMDA datastore model anywhere in the repo.
- *Diagnostics*: one `ValidationError` of formatted strings with positional paths (`/l[1]`); no
  error codes, app-tags, libyang-style data paths or severities (warnings).
- *cgo boundary*: machine-checked by `scripts/check-go-default-pure.sh`; confirmed with `go list -deps`.
- *Primary vs fallback*: the pure-Go path is aspirational; libyang is primary for data today.

**Adversarial run.** 26 cases in `spike/g2-cambium/cases/`, runner `spike/g2-cambium/run.sh`,
raw output `spike/g2-cambium/results-2026-09-30.txt`. cambium ran through a 46-line harness (`spike/g2-cambium/dtcheck/`,
`ParseModules` → `Validate` → `ApplyDefaults` → `Serialize`, `CGO_ENABLED=0`); cambium's own CLI
is cgo-only. Oracle: `/opt/homebrew/bin/yanglint` 5.8.6.

| # | Case | yanglint | cambium | |
|---|---|---|---|---|
| 01 | `deref()` in must, target disabled | reject | **accept** | ✗ silent skip |
| 02 | when false, explicit data in NP container | reject | reject | ✓ |
| 03 | when false removes implicit default (output) | accept | accept | ✓ |
| 04 | must reads implicit default leaf | accept | **reject** | ✗ |
| 05 | must reads default in absent NP container | accept | **reject** | ✗ |
| 06 | leafref require-instance false, dangling | accept | accept | ✓ |
| 07 | same, value invalid for target type | reject | reject | ✓ |
| 08 | instance-identifier to missing entry | reject | reject | ✓ |
| 09 | identityref derived-from across 3 modules | accept | accept | ✓ |
| 10 | identity not derived from base | reject | reject | ✓ |
| 11 | union int32/boolean, JSON `"5"` | reject | reject | ✓ |
| 12 | int64 as JSON number | reject | reject | ✓ |
| 13 | decimal64 excess fraction digits | reject | reject | ✓ |
| 14 | decimal64 fd=18 overflow | reject | reject | ✓ |
| 15 | user-ordered list + system leaf-list (output) | accept | accept | ✓ same bytes |
| 16 | keyless state list with duplicates (`-t data`) | accept | accept | ✓ |
| 17 | choice default case default seen by must | accept | **reject** | ✗ |
| 18 | `unique "c/x"` over nested leaf | reject | reject | ✓ |
| 19 | must violation in operational (`-t data`) → warning | accept+warn | **reject** | ✗ no modes |
| 20 | RPC reply missing mandatory output | reject | reject | ~ unsupported (parse error) |
| 21 | anydata JSON | accept | accept | ✓ |
| 22 | anydata XML | accept | **reject** | ✗ unsupported |
| 23 | XSD `\p{IsBasicLatin}` pattern | reject | reject | ✓ |
| 24 | when reads top-level default | reject | reject | ~ see note |
| 25 | when false in `-t edit` (not evaluated) | accept | **reject** | ✗ no modes |
| 26 | when reads nested default | accept | **reject** | ✗ |

Verdicts agree 18/26, but two agreements are accidental (20: rejected as unknown member; 24:
cambium never sees defaults), so **16/26 genuine**. Every miss clusters in three areas: defaults
visible to XPath, validation modes (operational/edit/reply), and silent XPath skips. Types, value
canonicalisation, identities, leafref/instance-identifier, unique and ordering are solid.

Side finding for our corpus: libyang rejects case 24 but accepts the identical nested case 26 —
top-level implicit defaults are not visible to `when` at parse time. Candidate for our
known-libyang-deviation allowlist (PLAN §9) and exactly the kind of fact a shared corpus should hold.

**Project fit**

- An active, AI-assisted project with a well-gated workflow (TDD rule, conformance lanes); its
  contribution and governance processes were still taking shape when we evaluated it.
- License Apache-2.0; no CLA, no DCO, no CONTRIBUTING/governance file. Inbound=outbound Apache-2.0
  is the default. Our code is BSD-3: we may *consume* Apache-2.0 code (keep NOTICE) but could not
  relicense contributed work into our repo as BSD-3 without dual-licensing it ourselves.
- Scope priorities differ: cambium's centre is ordered schema IR + codegen for a downstream
  Terraform/NETCONF generator; libyang is its accepted data engine. Our centre is the data engine.

## Options

1. **Contribute to cambium.** Gains a schema tier and ordering discipline. But the value-model
   rewrite, defaults-in-XPath, modes, operations, NMDA and diagnostics are ~all of our M1–M6
   anyway, done in a design whose production data path is libyang.
   Its silent-skip policy conflicts with our "unsupported → explicit error" rule.
2. **Fork cambium.** Inherits the raw-JSON value model we would replace first; diverges on day one.
3. **Independent runtime + share the corpus.** Own engine per PLAN; publish the differential
   corpus in an engine-neutral format cambium can run; upstream findings where they fit.

## Recommendation

**Option 3.** cambium's pure-Go datatree is a fallback-in-progress, not a libyang-compatible
engine: the gaps our goals depend on (value model, defaults visible to must/when, datastore modes
and NMDA, operations, structured diagnostics, no silent skips) are unbuilt and would be designed by
someone else. Contribution does not save the work, only relocates it. The corpus is where the
projects genuinely overlap, and sharing it costs us nothing. Offer the shared corpus to cambium's
maintainer (a message the maintainer of this repository sends by hand); revisit G2 if that
maintainer wants a pure-Go-primary data tier with our value model — then Option 1 becomes live.

## Consequences

- Port proceeds to M1 on our own runtime; cambium is tracked as a peer, not a dependency.
- Cases in `spike/g2-cambium/cases/` seed our corpus; case 24/26 enters the libyang-deviation list.
- G1 should evaluate cambium's schema IR (ordered, cgo-free, Apache-2.0) next to goyang's parser.
- Risk: duplicated effort; mitigated by the shared corpus and an open offer to converge.
