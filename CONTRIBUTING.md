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
outgoing commit (added lines; every path a commit touches, including empty and later-deleted
files; commit messages; author/committer) and annotated tag
messages. If the denylist exists but is unreadable, empty or has an invalid regex, they fail
closed; so does a missing denylist in a clone with `git config vibe.requireDenylist true` (set it
in every maintainer clone). Patterns are never printed, and neither is matched text — except that
a hit in a file name prints that file name, since the name is the location. Without a denylist
(other contributors, CI) only the generic rules apply.

Trade-off, accepted on purpose: hooks and scanner run from the working tree, so a checked-out
branch can change what they do, and `--no-verify` skips them. They protect the maintainer from
mistakes, not from a malicious branch or a compromised workstation. Installing them outside the
checkout would close the first gap at the cost of a second copy to keep in sync. CI cannot replace
them because it must not see the personal patterns.

`scripts/test-gates` (part of `make ci`) self-tests the scanner, the hooks, `merge-pr`, the
ai-review publisher and `review-tier` against a stubbed `gh`/`curl` and throwaway repos.

## Policy files

These files decide what runs, what is checked and what reviewers are told:
`AGENTS.md` (any directory), `CLAUDE.md`, `.claude/`, `.codex/` and `.agents/` (any directory),
`Makefile`, `dev`, `Dockerfile`, `.devcontainer/`, `.gitattributes` (merge drivers), `scripts/`,
`.github/`, `.githooks/`. A PR touching them changes its own gate,
so it always needs a human read, `review-tier` sends it to the deep reviewer, and the gate tools
never run the PR's copy: `merge-pr` and `astra review --trusted` run main's version with main's
rules (see Merge gate).

## Review

Every non-draft PR by the maintainer is reviewed automatically:
- **Claude** (`ai-review` workflow): Opus for risky changes, Sonnet otherwise (`scripts/review-tier`
  on file paths + line counts; the optional Jev escalation sends only those).
- **Codex** (Codex GitHub app, automatic reviews).
- **astra** by the lead, on a checkout of the PR head, running main's copy:
  `ASTRA_PR=<n> bash <(git show origin/main:scripts/astra) review --trusted` — the merge-gate
  review. `--trusted` always reviews the freshly fetched `origin/main...HEAD` (no custom base;
  an empty diff is refused) and names that range in the report. It reads the rules
  (`AGENTS.md`) and output schema from `origin/main`, stops
  codex from loading the checkout's `AGENTS.md` and execpolicy rules (`--ignore-rules`), and
  refuses to run when the PR or checkout contains `.codex/` or `.agents/` (codex configuration a
  PR could plant — review those by hand); only `--trusted` runs post an attestation.

`ai-review` spends the maintainer's Claude plan token, so it never runs PR code:
- `pull_request_target`: the workflow and scripts come from `main`;
- only PRs whose author **and** triggering sender have the maintainer's numeric id
  (`vars.MAINTAINER_ID`, default 1056050), from this repository;
- checkout of `main` only. `actions/checkout` persists no credentials, but claude-code-action
  itself writes the workflow token (`contents: read`, `pull-requests: write`) into the checkout's
  git config — acceptable only because the model can't read files;
- the head SHA's diff is fetched with `gh api` and given to Claude as text; Claude runs with
  **no tools and no MCP servers** (`--disallowedTools "*"`, `--strict-mcp-config`,
  `--setting-sources user`);
- trusted code (`scripts/ai-review-publish`) requires the session's init record to show no tools
  and no MCP servers, validates the JSON answer, refuses to post anything that contains the token
  or a credential-shaped string, neutralises links, images, HTML and @mentions in model text, and
  posts one comment ending with `VERDICT: <approve|changes> <head sha> (<model>)`. A
  post-publication scan deletes or redacts any PR comment or review containing the token.

Label `no-ai-review` skips the review. Limits: the diff appears in the job log, and a diff can
still try to talk the model into a verdict, so the Claude verdict is advisory and not part of the
merge gate. Rotate `CLAUDE_CODE_OAUTH_TOKEN` every 90 days and immediately if the publisher or
scan reports a leak.

All reviewers apply "Code review rules" in AGENTS.md. Findings are fixed or answered in the PR.

## Merge gate

Run by the maintainer, always as main's copy from a checkout of `origin/main`:

```sh
git fetch origin main && git switch --detach origin/main
scripts/merge-pr <n>                                  # or: bash <(git show origin/main:scripts/merge-pr) <n>
```

It refuses to run when the checkout's `HEAD` is not `origin/main`, when `scripts/` or `.githooks/`
have local changes, or when the script file differs from `origin/main:scripts/merge-pr`, so a
PR's own copy (or its hooks, which the push runs) can't stand in for the gate.
It fast-forwards `main` to the PR head only if, for the PR's current **full** head SHA:
- the PR is open, not a draft, and targets `main` of `vibe-ports/yang` (= `origin`);
- the latest line `VERDICT: <approve|changes> <full sha> (<reviewer>)` naming that SHA says
  `approve`, counting only never-edited PR comments **authored by the maintainer's account**,
  lines outside fenced code blocks (CommonMark rules: a fence closes only on the same character
  at least as long), and reviewers `codex-*` or `port-reviewer-*`. `scripts/astra review
  --trusted` posts `(codex-<model>)`; after a local Opus `port-reviewer` review the lead posts the
  line signed `(port-reviewer-opus)`. `claude-*` is never accepted: it is the ai-review bot's
  signature, so a pasted bot line can't count. A later `changes` revokes; bot comments, quoted or
  fenced lines, edited comments and short SHAs don't count;
- every run of `.github/workflows/ci.yml` for exactly that SHA (cancelled ones aside, at least
  one) concluded `success`, read from the Actions runs API
  (CI checks out the PR head, not the merge commit);
- every commit has `+0000` dates and the head is a descendant of `main`.

It re-checks the PR head just before pushing and pushes with
`--force-with-lease=refs/heads/main:<checked main>`, so a concurrent change to `main` aborts.

What this does **not** guarantee: it is a script, not server-side enforcement. A private
repository on the Free plan has no branch protection or rulesets, so anyone with write access can
push to `main` directly or skip the script. A PR can modify `ci.yml` or the `make ci` it runs, so
changes to policy files need a human read. The API checks and the push are
not one transaction (the lease protects `main` only). Once the repository is public, enable a
ruleset on `main`: required `ci` status, linear history, no force pushes or deletion, restricted
updates.

## libyang updates

`libyang-sync` runs weekly: a new libyang release opens a PR that bumps the oracle pin and
regenerates goldens (each golden diff = upstream behaviour change to port or record); upstream
`master` drift is summarised in one issue labelled `upstream-drift`. Upstream tag names and SHAs
are validated (`vX.Y.Z`, 40-hex) before any use. The upstream build runs in a job with a
read-only token and hands over only a tarball of golden JSON files. A separate job with the write
token runs no build: it writes the pins itself from the validated tag/SHA, imports only regular,
non-executable files at `conformance/corpus/<group>/golden/<name>.json` from the tarball
(`scripts/import-goldens`; any other member, rename, deletion or mode change aborts) and opens the
PR. Still read the first libyang-sync PR after this change by hand.

## Jev triage (advisory)

Jev (TypeSafe) adds hints, never decisions: it is not part of the merge gate and no check depends
on it. Everything lives in `scripts/jev-triage` (and `review-tier`), run from the default branch.
- **Upstream drift** (`libyang-sync`): each commit line in the `upstream-drift` issue gets
  `behaviour` (Jev: changes observable behaviour vs refactor/test/doc), `touches_ported` (computed
  from `docs/port-map.md`, not by Jev; file level when no function names are known) and
  `priority` (Jev: now / next-milestone / later); the list is sorted by priority.
- **Issues** (`issue-triage` workflow, `issues: write` only): for issues **authored by the
  maintainer** (`vars.MAINTAINER_ID`), Jev proposes `area:*`, `size:*` and `worker:*` labels. A
  label is applied only when its top probability is at least 0.6; if any is lower the issue also
  gets `triage:needs-human`. Labels are created when missing; nothing else is posted. Re-edits
  only add labels, they never remove earlier ones, and edits by anyone else don't re-run triage,
  so labels can go stale: fix them by hand. Existing labels are never recoloured.
- **Data sent** (never repository code): drift = public upstream commit subject + touched `src/`
  paths; issues = title + first 4 KB of the body, exactly as the maintainer wrote it (so it can be
  non-public text: keep secrets and private details out of issues), only for maintainer-authored
  issues so outsiders can't feed it; `review-tier` = file paths + line counts.
  The drift pass has a total time budget (`JEV_BUDGET`, 300 s) and stops after 3 consecutive
  failed calls; the plain list is always the fallback.
- **Failure-safe**: no key, API error or malformed answer = exit 0 with no labels or annotations.
- **Disable**: delete the `TYPESAFE_API_KEY` secret; every Jev step becomes a no-op.

## Licensing and provenance

- Contributions are accepted under BSD-3-Clause (the project license).
- Only public sources: libyang, RFCs, public YANG models, public code under compatible licenses.
  Do not contribute code, models, data or knowledge belonging to an employer or client.
- No private details or secrets: generic `scripts/check-sensitive` rules and gitleaks run in CI,
  the personal rules in the maintainer's hooks.

## Maintainer setup (once)

In every maintainer clone, with the denylist in place:

```sh
git config core.hooksPath .githooks
git config vibe.requireDenylist true   # hooks fail if the personal denylist is missing
```

Repository secrets and variables:
| Name | Kind | Purpose | Required |
|---|---|---|---|
| `CLAUDE_CODE_OAUTH_TOKEN` | secret | Claude reviews (`claude setup-token`, uses the maintainer's Claude plan) | for ai-review |
| `SYNC_TOKEN` | secret | fine-grained PAT (this repo: Contents + Pull requests write) so bot PRs trigger CI | optional |
| `TYPESAFE_API_KEY` | secret | Jev (advisory only): `review-tier` escalation, upstream-drift annotations, issue labels; see "Jev triage" | optional |
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
