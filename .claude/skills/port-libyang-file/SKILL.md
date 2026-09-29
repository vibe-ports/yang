---
name: port-libyang-file
description: Port one libyang C source file (or one function group) to Go in this repo, with provenance header, port-map rows, oracle fixtures and a green `./dev make ci`. Use when asked to "port <file>.c", "port lys_/lyd_/lyxp_ function", or to pick up a port-task issue.
---

# Port one libyang file

1. **Scope.** Read the task, `PLAN.md` §1/§4 (is it in v1, which milestone), the relevant
   `docs/design/*.md`, and existing rows in `docs/port-map.md` for this file. Keep the PR to one file
   or ≈500 Go lines; split otherwise.
2. **Read the C.** libyang v5.8.6 sources (clone the tag outside the repo). List the functions,
   their callers and the behaviour they encode (error codes, messages' vecode, flags).
3. **Fixtures first.** For each behaviour add fixtures (valid + invalid) to
   `conformance/corpus/manifest.yaml`, then `./dev make oracle-golden` and inspect the goldens.
4. **Write Go.** Header per AGENTS.md; idiomatic Go per PLAN §2; internal unless the API needs it.
   Table tests next to the code; a `Fuzz*` target for every parser-like function.
5. **Compare.** Run the Go implementation on the new fixtures against the goldens. Differences are
   bugs unless justified in `conformance/deviations.md`.
6. **Record.** Add/refresh `docs/port-map.md` rows (C function → Go symbol, status).
7. **Gate.** `./dev make ci` green. PR description: what was ported, fixtures added, deviations,
   anything skipped and why. Use the PR template checklist.
