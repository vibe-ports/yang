---
name: sensitive-check
description: Check that nothing private would be exposed by this (future public) repo — infrastructure, personal data, employer references, local paths, secrets — before committing, opening a PR, or making the repo public; and fix leaks including ones already in git history.
---

# Sensitive-data check

The repo will be public. Anything committed — including in old commits — becomes public.

1. **Run the checks** (inside or outside the container):
   - `scripts/check-sensitive --staged` (also the pre-commit hook), `--all`, `--history`
   - `./dev make secrets` (gitleaks: credentials/tokens across history)
   Personal patterns (host names, employer, e-mails, IP ranges) come from
   `~/.config/vibe-ports/denylist` locally and the `SENSITIVE_PATTERNS` secret in CI. **Never add
   such patterns to the repo** — the pattern list itself would be the leak.
2. **Judge what scripts can't** — read the diff for:
   - infrastructure: machine names, specs, network, VPN/mesh, internal tooling of the maintainer;
   - personal: names/e-mails/accounts beyond the commit identity, time zones, locations, finances;
   - employer: any name, product, internal model, schema or knowledge;
   - agent artefacts: absolute paths, scratch dirs, pasted tool output with local details.
3. **Fix.** Generalise the text (e.g. "an additional build host"), don't just delete meaning.
   If it was already committed: before the repo is public, rewrite history
   (`git filter-branch`/`git filter-repo` on the affected paths or an amend of the last commit),
   force-push only with the maintainer's OK, and re-run `--history`.
4. False positive → append `sensitive:allow` to that line with a reason.
