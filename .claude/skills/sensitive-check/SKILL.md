---
name: sensitive-check
description: Check that nothing private would be exposed by this (future public) repo — infrastructure, personal data, employer references, local paths, secrets — before committing, opening a PR, or making the repo public; and fix leaks including ones already in git history.
---

# Sensitive-data check

The repo will be public. Anything committed — including in old commits — becomes public.

1. **Run the checks** (inside or outside the container):
   - `scripts/check-sensitive --staged` (also the pre-commit hook), `--all`, `--history`
   - `./dev make secrets` (gitleaks: credentials/tokens across history)
   - `.githooks/pre-push` scans every outgoing commit (lines, file names, messages)
   Personal patterns (host names, employer, e-mails, IP ranges) come only from
   `~/.config/vibe-ports/denylist` on the maintainer's host — not CI, not the dev container, so run
   the script on the host for the full check. **Never add such patterns to the repo or to CI** —
   the pattern list itself would be the leak.
2. **Judge what scripts can't** — read the diff for:
   - infrastructure: machine names, specs, network, VPN/mesh, internal tooling of the maintainer;
   - personal: names/e-mails/accounts beyond the commit identity, time zones, locations, finances;
   - employer: any name, product, internal model, schema or knowledge;
   - agent artefacts: absolute paths, scratch dirs, pasted tool output with local details.
3. **Fix.** Generalise the text (e.g. "an additional build host"), don't just delete meaning.
   If it was already committed: before the repo is public, rewrite history
   (`git filter-branch`/`git filter-repo` on the affected paths or an amend of the last commit),
   force-push only with the maintainer's OK, and re-run `--history`.
4. False positive of a generic rule → append `sensitive:allow` to that line with a reason.
   A personal rule can't be waived from the repo: add a regex for `<where><TAB><line>` to
   `~/.config/vibe-ports/allowlist` (maintainer only).
