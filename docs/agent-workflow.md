# Agent workflow: Claude implements, codex/astra checks — continuously

Goal: **no big final review**. Every change is small, independently tested and independently
reviewed at the moment it lands, so the repository is always "already reviewed". The only
end-of-milestone review is a bounded drift check over already-reviewed PRs.

Roles: **Claude (lead, Opus)** plans, implements hard parts, integrates, merges. **porter** (Sonnet)
implements mechanical ports. **codex gpt-6-astra** is the independent critic (different model
family → less correlated blind spots). **codex gpt-5.6-sol/luna** writes independent tests.
**lyoracle** (libyang v5.8.6) is the arbiter of behaviour — not any model.

## Per task (one issue, ≤ ~500 Go lines)

```
            ┌─ (A) Claude/porter: implement on branch  task/<id> ─────────────┐
 plan ──────┤                                                                  ├─ PR → astra review → merge
 (+ astra   └─ (B) codex sol: independent fixtures on branch tests/<id> ──────┘      (CI + approve on
   design)        from RFC + libyang tests only; never reads the Go code                same head SHA)
```

1. **Plan (only when the task makes a design decision or touches public API).** Claude writes a
   ≤1-page plan (PR draft or `docs/design/`). `scripts/astra design <file>` critiques it; Claude
   accepts/rejects each finding in the plan. Mechanical ports skip this step.
   For contested designs the maintainer may instead run Claude and codex side by side in a
   terminal split and let them converge interactively; the agreed plan still goes into the PR.
2. **Parallel work.**
   - (A) implementation by Claude or `porter`, following the `port-libyang-file` skill;
   - (B) tests by codex in its own worktree, from the task text + RFC + libyang v5.8.6 tests:
     `codex exec -m gpt-5.6-sol --worktree -s workspace-write "Write oracle fixtures for <task> per conformance/AGENTS.md; do not read internal/ or data/."`
     Goldens come from `./dev make oracle-golden`, so neither model decides expected behaviour.
   (A) must pass (B)'s fixtures; disagreements are resolved by the oracle or recorded in
   `conformance/deviations.md` with an RFC reason.
3. **Review.** On a checkout of the PR head, under the maintainer's `gh` login,
   `ASTRA_PR=<n> bash <(git show origin/main:scripts/astra) review --trusted` posts a structured
   review of `main...HEAD` ending with `VERDICT: <approve|changes> <full HEAD sha> (codex-<model>)`.
   `--trusted` always covers the fetched `origin/main...HEAD` and takes the rules from main's
   `AGENTS.md`, so a PR can neither narrow its review nor rewrite its review rules. Claude fixes or rebuts each finding
   **in the PR**. Re-run until no `high` and verdict `approve`; after 3 rounds without convergence
   the maintainer decides. The automatic Claude (`ai-review`) and Codex-app reviews are extra
   eyes; their verdicts are advisory.
4. **Merge gate** (AGENTS.md, CONTRIBUTING.md "Merge gate"): `scripts/merge-pr <n>`, run as
   main's copy from a checkout of `origin/main`, checks that
   `ci.yml` succeeded on the PR's full head SHA and that the latest maintainer-authored attestation
   for that SHA is `approve`, then fast-forwards `main` with a lease. Only the lead merges. This is
   a script, not branch protection (Free private plan); rulesets get enabled when the repo is
   public.

## Triage hints (Jev, advisory)

`scripts/jev-triage` annotates the weekly `upstream-drift` issue (behaviour change?, touches a
ported file/function?, priority: now / next-milestone / later; sorted by priority) and, for
maintainer-authored issues, suggests `area:*`, `size:*` and suggested-worker `worker:*` labels
(`opus`, `sonnet`, `codex-sol`, `codex-luna`) when confidence is at least 0.6, else
`triage:needs-human`. Use them to pick the next task and the worker; they gate nothing. No
`TYPESAFE_API_KEY` = no hints. Data sent and details: CONTRIBUTING.md "Jev triage".

## Per milestone

- `scripts/astra review <previous-milestone-tag>` over the milestone diff, with the list of
  already-reviewed PRs: looks only for cross-PR drift (design notes vs code, duplicated helpers,
  API growth). Findings become issues, not a blocking mega-review.
- Oracle compatibility report per area is regenerated and diffed against the previous milestone.

## Why this minimises review

- Correctness is carried by the oracle + independent fixtures, not by reading code.
- A reviewer from another model family sees each diff while it is small.
- Every decision (plan critique, review findings, rebuttals) lives next to the change in the PR.

## Cost notes

- astra/sol/luna use the ChatGPT-plan quota; Claude quota is spent on implementation and
  integration. A PR review costs roughly one minute of wall time.
- astra always runs with reasoning effort `xhigh` (maintainer rule); skip step 1 for trivial PRs.
