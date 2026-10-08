# Contributing

This is an AI-assisted port of libyang to Go, but contributing needs neither AI tools nor any of
the maintainer's accounts: a fork, Docker, git and Perl are enough. Humans and coding agents follow
the same rules: [AGENTS.md](AGENTS.md) (rules), [PLAN.md](PLAN.md) (scope, architecture,
milestones), [docs/](docs/README.md) (designs, decisions, registries).

Security problems are not reported in issues: see [SECURITY.md](SECURITY.md).

## Prerequisites

Contributors need, on the host:
- Docker (everything else, Go included, runs in the dev container);
- git and bash;
- Perl 5, for the git hooks (`scripts/check-sensitive`, `scripts/check-registries` run on the host).

The maintainer's extra tools (`gh`, `jq`, `python3`, `codex`) are listed in
[docs/maintainers/maintaining.md](docs/maintainers/maintaining.md); contributors don't need them.

## Setup

Fork `vibe-ports/yang` on GitHub, then:

```sh
git clone git@github.com:<you>/yang.git && cd yang
git config core.hooksPath .githooks   # pre-commit + pre-push private-data and registry checks
./dev make ci                          # full CI locally (first run builds the image, ~3 min)
```

VS Code / any devcontainer-aware editor: "Reopen in Container" uses the same image.

The canonical oracle architecture is linux/amd64: CI runs amd64, and libyang's `long double` and C
integer conversions differ on arm64. On an arm64 machine (Apple silicon included) run anything that
touches oracle results under amd64 emulation:

```sh
DEV_PLATFORM=linux/amd64 ./dev make ci
DEV_PLATFORM=linux/amd64 ./dev make test-oracle oracle-check
DEV_PLATFORM=linux/amd64 ./dev make oracle-golden   # only ever generate goldens this way
```

## Making a change

1. Open an issue, or comment on an existing one that you are working on it. Bug reports and
   feature requests have their own forms. The `agent-ready` / `up-for-grabs` queue and
   `scripts/claim` are tooling for the maintainer's own agents: only claims by trusted
   accounts count there (the maintainer and `github-actions[bot]`), so a claim by anyone else is
   ignored. You don't need to claim anything; say on the issue what you are doing.
2. Branch in your fork from `main`: `feat/…`, `fix/…`, `port/<file>`.
3. For ported code: provenance header, `docs/port-map.md` row, oracle fixtures
   (`conformance/AGENTS.md`), deviations recorded in `conformance/deviations.md`. The checklist
   in `.claude/skills/port-libyang-file/SKILL.md` lists the steps; it is plain text and needs
   no AI tool.
4. `./dev make ci` must be green (`DEV_PLATFORM=linux/amd64` when oracle results change, see Setup).
5. Commit: conventional subject (`feat(xpath): …`, `fix:`, `docs:`, `test:`, `build:`), UTC dates
   (`TZ=UTC git commit`), no AI trailers.
6. Open a PR from your fork to `main` and fill in the template checklist.

## Using an AI coding agent

Agents follow [AGENTS.md](AGENTS.md), the same rules as humans: provenance headers, port-map rows,
oracle fixtures as proof, the deviations registry, append-only manifest, no AI trailers.

- Start from issues labelled `good first issue` or `help wanted`. Most task issues name the
  libyang functions to port and the fixtures that prove it.
- Comment on the issue to take it. `up-for-grabs` and `agent-ready` with `scripts/claim` are the
  maintainer's own agent queue, not for outside contributors.
- Run `./dev make ci` before opening the PR (`DEV_PLATFORM=linux/amd64` for oracle work).
- The maintainer reviews and merges the PR as for any contributor.
- Disclosing AI assistance is fine; the [README](README.md) already says the port is AI-assisted.
- The private-data and secrets checks below still apply.

## Review and merge

The maintainer manages review and merge. A PR from a fork gets the same CI as any other (it may
wait for the maintainer's approval to run), then the maintainer reviews it, or has it reviewed,
against "Code review rules" in [AGENTS.md](AGENTS.md). Findings are fixed or answered in the PR. The
maintainer merges with a fast-forward of the reviewed head, so keep your branch rebased on `main`
and its commit dates in UTC. Automated reviewers and merge tooling run only for the maintainer:
how they work is in [docs/maintainers/](docs/maintainers/maintaining.md).

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
ai-review publisher and `review-tier` against a stubbed `gh`/`curl` and throwaway repos;
`scripts/test-claim` does the same for the task queue (`scripts/claim`).

## Policy files

These files decide what runs, what is checked and what reviewers are told:
`AGENTS.md` (any directory), `CLAUDE.md`, `.claude/`, `.codex/` and `.agents/` (any directory),
`Makefile`, `dev`, `Dockerfile`, `.devcontainer/`, `.gitattributes` (merge drivers), `scripts/`,
`.github/`, `.githooks/` (owners: `.github/CODEOWNERS`). A PR touching them changes its own gate,
so it always needs a human read, `review-tier` sends it to the deep reviewer, and the gate tools
never run the PR's copy: `merge-pr` and `astra review --trusted` run main's version with main's
rules (see [docs/maintainers/maintaining.md](docs/maintainers/maintaining.md) "Merge gate").

## Licensing and provenance

- Contributions are accepted under BSD-3-Clause (the project license).
- Only public sources: libyang, RFCs, public YANG models, public code under compatible licenses.
  Do not contribute code, models, data or knowledge belonging to an employer or client.
- No private details or secrets: generic `scripts/check-sensitive` rules and gitleaks run in CI,
  the personal rules in the maintainer's hooks.
