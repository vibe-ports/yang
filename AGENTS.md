# AGENTS.md — rules for every coding agent (Claude Code, Codex, agy) and humans

Project: `github.com/vibe-ports/yang` — AI-assisted pure-Go port of libyang v5.8.6.
Plan and rationale: `PLAN.md`. Decisions: `docs/decisions/`. Designs: `docs/design/`.

## Goals (in priority order)
1. **No cgo** in anything under the root module. The only C is `conformance/oracle/` (test-only).
2. **libyang compatibility**: same inputs → same verdict, diagnostics (code, data path, app-tag),
   JSON/XML/diff output as libyang v5.8.6. Measured with the oracle, never assumed.
3. **Auditability**: every ported line can be traced to its libyang source.

## Commands (always inside the dev container)
- `./dev make ci` — everything CI runs: fmt, tidy, vet, lint, nocgo, race tests, 386 tests,
  govulncheck, oracle-check. **Must be green before you say "done".**
- `./dev make oracle-check` / `./dev make oracle-golden` — compare with / regenerate goldens.
- `./dev make fuzz FUZZTIME=30s` — run all fuzz targets.
- `./dev go test ./internal/xpath/ -run TestX` — any single command.
- Never install Go tools on the host; add them to `Dockerfile` instead.

## Porting rules
- Port behaviour, not C idioms: errors as values, no globals, no manual memory, `io.Reader`/`fs.FS`
  input, iterators via `iter.Seq`. See PLAN §2 and `docs/design/`.
- Every file that ports libyang code starts with:
  ```go
  // SPDX-License-Identifier: BSD-3-Clause
  // Ported from libyang v5.8.6 src/<file>.c (BSD-3-Clause, © CESNET).
  ```
  New (non-ported) files carry only the SPDX line.
- Add a row to `docs/port-map.md` for every libyang function you port (C function → Go symbol).
- Any intentional behaviour difference from libyang goes to `conformance/deviations.md` with an RFC
  reason. An unexplained difference is a bug.
- Every behaviour you port gets at least one oracle fixture (`conformance/AGENTS.md`).
- Public API stays minimal: new exported symbols need a reason in the PR. Default to `internal/`.

## Hard limits
- No cgo, no `import "C"`, no new dependencies without a note in the PR (stdlib first).
- Public sources only: libyang, RFCs, public YANG models, public Go code with compatible license.
  Never add code, models, data or knowledge from any employer. Never name an employer anywhere.
- No secrets in the repo. `make secrets` (gitleaks) runs in CI.
- **No private details** — this repo will be public: no host names, hardware, IPs, network layout,
  local paths, personal e-mails, account names beyond the commit identity, or tooling of the
  maintainer's own infrastructure. `make sensitive` + the pre-commit hook check it
  (`git config core.hooksPath .githooks` once per clone). See the `sensitive-check` skill.
- Do not hand-edit files under `conformance/corpus/**/golden/`.
- Do not edit `LICENSE` or provenance headers of existing files.

## Workflow
Claude/porter implement, codex writes independent fixtures in parallel, astra reviews every PR
(`scripts/astra review`), the oracle arbitrates. Details: `docs/agent-workflow.md`.

## Git
- Work on a branch, open a PR to `main`. Small PRs (≈ one C file or one feature).
- Merge gate (the repo has no branch protection, so this is a rule, not a setting): CI green on the
  PR's **exact head SHA** + a `port-reviewer` (or codex astra) review of that SHA, linked in the PR.
  Only the lead merges; worker agents never push to `main` or hold merge rights.
- Conventional-commit subjects (`feat(xpath): …`, `fix:`, `docs:`, `test:`, `build:`).
- Commit with `TZ=UTC` (`git ci` alias) as the configured author. **No AI trailers**
  (`Co-Authored-By`, `Assisted-by`) — AI assistance is disclosed once, in README.

## Layout
`internal/parser` YANG text → statement tree · `internal/compile` → compiled schema ·
`internal/types` · `internal/xsdre` XSD regex → RE2 · `internal/xpath` · `data/` data tree,
validation, codecs, diff · `cmd/yanglint-go` · `conformance/` oracle, corpus, goldens (own rules).
