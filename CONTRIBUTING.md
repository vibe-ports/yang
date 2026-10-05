# Contributing

This is an AI-assisted port of libyang to Go. Humans and coding agents follow the same rules:
[AGENTS.md](AGENTS.md) (rules), [PLAN.md](PLAN.md) (scope, milestones),
[docs/agent-workflow.md](docs/agent-workflow.md) (how work and review are split).

## Setup

Requirements: Docker, git, bash. Everything else runs in the dev container.

```sh
git clone git@github.com:vibe-ports/yang.git && cd yang
git config core.hooksPath .githooks   # pre-commit + pre-push private-data checks
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

## Private-data checks

`scripts/check-sensitive` has two rule sets:
- **generic** rules (local paths) live in the script and run everywhere, CI included
  (`make sensitive`, plus gitleaks in `make secrets`). A false positive can be marked inline with
  `sensitive:allow`.
- **personal** rules (host names, employer, e-mails, network ranges) exist **only** on the
  maintainer's machine: `~/.config/vibe-ports/denylist` (one Perl regex per line; directory
  `0700`, file `0600`). They never go into the repo, CI secrets or the dev container: the list
  itself would be the leak. Exceptions go into `~/.config/vibe-ports/allowlist` (regexes matched
  against `<where><TAB><line>`); text in the repo cannot waive a personal rule.

The hooks in `.githooks/` apply both: `pre-commit` scans the staged files, `pre-push` every
outgoing commit (added lines, file names, commit messages). If the denylist exists but is
unreadable, empty or has an invalid regex, they fail closed; patterns and matched text are never
printed. Without a denylist (other contributors, CI) only the generic rules apply.

Trade-off, accepted on purpose: hooks and scanner run from the working tree, so a checked-out
branch can change what they do, and `--no-verify` skips them. They protect the maintainer from
mistakes, not from a malicious branch or a compromised workstation. Installing them outside the
checkout would close the first gap at the cost of a second copy to keep in sync. CI cannot replace
them because it must not see the personal patterns.

`scripts/test-gates` (host) self-tests the scanner, the hooks, `merge-pr` and the ai-review
publisher with a stubbed `gh`; run it after changing any of them.

## Review

Every non-draft PR by the maintainer is reviewed automatically:
- **Claude** (`ai-review` workflow): Opus for risky changes, Sonnet otherwise (`scripts/review-tier`
  on file paths + line counts; the optional Jev escalation sends only those).
- **Codex** (Codex GitHub app, automatic reviews).
- **astra** by the lead: `ASTRA_PR=<n> scripts/astra review` — this is the merge-gate review.

`ai-review` spends the maintainer's Claude plan token, so it never runs PR code:
`pull_request_target` (workflow and scripts come from `main`); only PRs authored by the
maintainer's numeric id (`vars.MAINTAINER_ID`, default 1056050) from this repository; checkout of
`main` only, without persisted credentials; the head SHA's diff is fetched with `gh api` and given
to Claude as text; Claude runs with **no tools** (`--disallowedTools "*"`). Trusted code
(`scripts/ai-review-publish`) validates the JSON answer, refuses to post anything that contains
the token or a credential-shaped string, and posts one comment ending with
`VERDICT: <approve|changes> <head sha> (<model>)`. A post-publication scan deletes or redacts any
PR comment or review containing the token. Label `no-ai-review` skips the review.
Limits: the diff appears in the job log, and a diff can still try to talk the model into a
verdict, so the Claude verdict is advisory and not part of the merge gate. Rotate
`CLAUDE_CODE_OAUTH_TOKEN` every 90 days and immediately if the publisher or scan reports a leak.

All reviewers apply "Code review rules" in AGENTS.md. Findings are fixed or answered in the PR.

## Merge gate

`scripts/merge-pr <n>` (run by the maintainer) fast-forwards `main` to the PR head only if, for
the PR's current **full** head SHA:
- the PR is open, not a draft, and targets `main` of `vibe-ports/yang` (= `origin`);
- the latest line `VERDICT: <approve|changes> <full sha> (<reviewer>)` naming that SHA, in a PR
  comment **authored by the maintainer's account**, says `approve`. `scripts/astra` posts it; a
  later `changes` revokes; bot comments, quoted lines and short SHAs don't count;
- the latest run of `.github/workflows/ci.yml` for exactly that SHA concluded `success`
  (CI checks out the PR head, not the merge commit);
- every commit has `+0000` dates and the head is a descendant of `main`.

It re-checks the PR head just before pushing and pushes with
`--force-with-lease=refs/heads/main:<checked main>`, so a concurrent change to `main` aborts.

What this does **not** guarantee: it is a script, not server-side enforcement. A private
repository on the Free plan has no branch protection or rulesets, so anyone with write access can
push to `main` directly or skip the script. A PR can modify `ci.yml` or the `make ci` it runs, so
changes to CI, workflows, hooks or `scripts/` need a human read. The API checks and the push are
not one transaction (the lease protects `main` only). Once the repository is public, enable a
ruleset on `main`: required `ci` status, linear history, no force pushes or deletion, restricted
updates.

## libyang updates

`libyang-sync` runs weekly: a new libyang release opens a PR that bumps the oracle pin and
regenerates goldens (each golden diff = upstream behaviour change to port or record); upstream
`master` drift is summarised in one issue labelled `upstream-drift`. Upstream tag names and SHAs
are validated (`vX.Y.Z`, 40-hex) before any use.

## Licensing and provenance

- Contributions are accepted under BSD-3-Clause (the project license).
- Only public sources: libyang, RFCs, public YANG models, public code under compatible licenses.
  Do not contribute code, models, data or knowledge belonging to an employer or client.
- No private details or secrets: generic `scripts/check-sensitive` rules and gitleaks run in CI,
  the personal rules in the maintainer's hooks.

## Maintainer setup (once)

Repository secrets and variables:
| Name | Kind | Purpose | Required |
|---|---|---|---|
| `CLAUDE_CODE_OAUTH_TOKEN` | secret | Claude reviews (`claude setup-token`, uses the maintainer's Claude plan) | for ai-review |
| `SYNC_TOKEN` | secret | fine-grained PAT (this repo: Contents + Pull requests write) so bot PRs trigger CI | optional |
| `TYPESAFE_API_KEY` | secret | Jev escalation in `review-tier` (sees file paths + line counts only) | optional |
| `MAINTAINER_ID` | variable | numeric GitHub user id whose PRs ai-review reviews (default 1056050) | optional |

Personal sensitive-data patterns are deliberately **not** a secret (see Private-data checks);
delete any old `SENSITIVE_PATTERNS` secret.

Apps: the Codex GitHub app with automatic reviews enabled for this repository (ChatGPT → Codex
settings). ai-review uses the workflow token, not the Claude GitHub app.

## Before making the repository public

1. On the host with the denylist: `scripts/check-sensitive --history` and `./dev make secrets` are
   clean on the branch to publish.
2. Publish into a **new** public repository from the cleaned history (not a visibility switch):
   unreachable old objects and CI logs of the private repo can still be fetched by SHA.
3. Re-check secrets: fork PRs never receive them; ai-review only reviews the maintainer's
   same-repo PRs and never executes PR code.
4. Enable the `main` ruleset described under Merge gate.
