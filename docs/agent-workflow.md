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
3. **Review.** `ASTRA_PR=<n> scripts/astra review` posts a structured review of `main...HEAD` to
   the PR (commit SHA in the header). Claude fixes or rebuts each finding **in the PR**. Re-run
   until no `high` and verdict `approve`; after 3 rounds without convergence the maintainer decides.
4. **Merge gate** (AGENTS.md): CI green and astra `approve` on the **same head SHA**. Only the lead
   merges.

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
