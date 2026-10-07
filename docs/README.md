# Documentation

Start with the [README](../README.md). Scope, architecture and roadmap: [PLAN.md](../PLAN.md).
How to contribute: [CONTRIBUTING.md](../CONTRIBUTING.md). Rules for every contributor, human or
agent: [AGENTS.md](../AGENTS.md). Reporting a vulnerability: [SECURITY.md](../SECURITY.md).

## Current

| Document | What it is |
|---|---|
| [port-map.md](port-map.md) | Registry: every libyang v5.8.6 function → its Go symbol and status |
| [../conformance/deviations.md](../conformance/deviations.md) | Registry: intentional differences from libyang (D-ids) and unsupported features (U-ids) |
| [../conformance/AGENTS.md](../conformance/AGENTS.md) | How oracle fixtures and goldens are written |
| [conformance/utests-inventory.md](conformance/utests-inventory.md) | libyang `tests/` inventory and coverage targets for the corpus |

## Design notes

Contracts the code follows; each states its status and milestone in its header.

| Note | Topic |
|---|---|
| [01](design/01-value-model.md) | Value model: numeric storage, union members, namespace-aware values |
| [02](design/02-data-node-model.md) | Data node model: ordered storage, handles, anydata, defaults |
| [03](design/03-xpath-context.md) | XPath evaluation context |
| [04](design/04-oracle-protocol.md) | Oracle protocol and fixture manifest |
| [05](design/05-m1-slice.md) | M1 vertical slice: package graph, APIs, task split |
| [06](design/06-compile.md) | Schema compilation |
| [07](design/07-data.md) | Data tree, codecs, validation |

## Decisions (ADRs)

| ADR | Decision | Status |
|---|---|---|
| [0001](decisions/0001-parser-reuse.md) | Own YANG parser instead of reusing goyang (gate G1) | recorded as proposed; followed: `internal/parser` |
| [0002](decisions/0002-cambium.md) | Own runtime, shared corpus, instead of contributing to cambium (gate G2) | recorded as proposed; followed |
| [0003](decisions/0003-xsd-regex.md) | XSD regular expressions on Go RE2 | accepted |

## For maintainers

[maintainers/](maintainers/) holds the maintainer's process; outside contributors don't need it:
- [maintaining.md](maintainers/maintaining.md): automated review, merge gate, libyang updates, Jev
  triage, repository secrets, going public;
- [agent-workflow.md](maintainers/agent-workflow.md): how agents take, do and review work.

## Archive

Historical, not maintained; kept for the audit trail.
- [archive/survey-2026-09-30.md](archive/survey-2026-09-30.md): survey of Go YANG projects and the
  M0 gate decisions (former PLAN.md §0).
- [archive/comparison-2026-09-30.md](archive/comparison-2026-09-30.md): dated comparison with
  cambium and goyang/ygot at M0.
- [archive/reviews/](archive/reviews/): the astra review of PLAN.md rev 0 and the plan review logs
  (former PLAN.md §10–§11), with the reviewed commits.
