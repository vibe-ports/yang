# Contributing

This is an AI-assisted port of libyang to Go. Humans and coding agents follow the same rules:
[AGENTS.md](AGENTS.md) (rules), [PLAN.md](PLAN.md) (scope, milestones),
[docs/agent-workflow.md](docs/agent-workflow.md) (how work and review are split).

## Setup

Requirements: Docker, git, bash. Everything else runs in the dev container.

```sh
git clone git@github.com:vibe-ports/yang.git && cd yang
git config core.hooksPath .githooks   # pre-commit private-data check
./dev make ci                          # full CI locally (first run builds the image, ~3 min)
```

VS Code / any devcontainer-aware editor: "Reopen in Container" uses the same image.

## Making a change

1. Pick or open an issue; porting tasks use the **Port task** template (≈ one libyang file,
   ≤ ~500 Go lines).
2. Branch from `main`: `feat/…`, `fix/…`, `port/<file>`.
3. For ported code: provenance header, `docs/port-map.md` row, oracle fixtures
   (`conformance/AGENTS.md`), deviations recorded. The `port-libyang-file` skill lists the steps.
4. `./dev make ci` must be green.
5. Commit: conventional subject (`feat(xpath): …`), UTC dates (`git ci` alias), no AI trailers.
6. Open a PR with the template checklist.

## Review

Every non-draft PR is reviewed automatically:
- **Claude** (`ai-review` workflow): Opus for risky changes, Sonnet otherwise
  (`scripts/review-tier`; optional Jev escalation on PR metadata only).
- **Codex** (Codex GitHub app, automatic reviews).
- **astra** on demand or by the lead: `ASTRA_PR=<n> scripts/astra review`.

All reviewers apply "Code review rules" in AGENTS.md. Findings are fixed or answered in the PR.
Merge gate: CI green + an approving review on the **same head SHA**; the maintainer merges.

## libyang updates

`libyang-sync` runs weekly: a new libyang release opens a PR that bumps the oracle pin and
regenerates goldens (each golden diff = upstream behaviour change to port or record); upstream
`master` drift is summarised in one issue labelled `upstream-drift`.

## Licensing and provenance

- Contributions are accepted under BSD-3-Clause (the project license).
- Only public sources: libyang, RFCs, public YANG models, public code under compatible licenses.
  Do not contribute code, models, data or knowledge belonging to an employer or client.
- No private details or secrets — `scripts/check-sensitive` and gitleaks run in CI.

## Maintainer setup (once)

Repository secrets:
| Secret | Purpose | Required |
|---|---|---|
| `CLAUDE_CODE_OAUTH_TOKEN` | Claude reviews (`claude setup-token`, uses the maintainer's Claude plan) | for ai-review |
| `SYNC_TOKEN` | fine-grained PAT (this repo: Contents + Pull requests write) so bot PRs trigger CI | recommended |
| `SENSITIVE_PATTERNS` | private patterns for `check-sensitive` (never in the repo) | recommended |
| `TYPESAFE_API_KEY` | Jev escalation in `review-tier` | optional |

Apps: Claude GitHub app (`/install-github-app` in Claude Code) and the Codex GitHub app with
automatic reviews enabled for this repository (ChatGPT → Codex settings).

## Before making the repository public

1. `scripts/check-sensitive --history` and `./dev make secrets` are clean on the branch to publish.
2. Publish into a **new** public repository from the cleaned history (not a visibility switch):
   unreachable old objects and CI logs of the private repo can still be fetched by SHA.
3. Re-check secrets: fork PRs never receive them; ai-review is limited to same-repo PRs.
