# PLAN.md review logs (archived from PLAN.md §10–§11, 2026-09-30)

Historical: how each finding of the two codex `gpt-6-astra` reviews of the plan and the M0
repository was answered. Not maintained; section and line references point to the plan as it was.

Reviewed states:
- §10 reviewed PLAN.md rev 0, which was never committed; rev 1, answering it, and the review text
  ([2026-09-30-astra-plan-rev0.md](2026-09-30-astra-plan-rev0.md)) landed in
  693afbdf7c18bba5fddbca33e7fc31194579cdc1.
- §11 reviewed the M0 repository at ea39c211c74da9b70b7cf66518c117eb580d5cc2; the fixes landed in
  d0ed97408b4e2fefa712f1af1f07c9ce31596132 and b9993ab213a604c0541332eee490a2e23e1829ef.

## 10. Review log — codex `gpt-6-astra`, 2026-09-30

Full text: [2026-09-30-astra-plan-rev0.md](2026-09-30-astra-plan-rev0.md) (25 findings on rev 0). Two factual claims were checked
against libyang v5.8.6 source before acting: `yanglint -f info` = `LYS_OUT_YANG_COMPILED`
(`tools/lint/yl_opt.c:109`) and `-t data` adds `LYD_VALIDATE_OPERATIONAL` (`yl_opt.c:198`). Both correct.

| # | Finding | Verdict | What changed / why |
|---|---|---|---|
| 1 | XSD→RE2 understated | accept | parser + char-set algebra prototype in M0, declared limits |
| 2 | `when` auto-delete underspecified | accept | fresh-invalid vs became-invalid rule, validation history in node model |
| 3 | NMDA reduced to config/state | accept | per-datastore policy; operational → warnings; origin inheritance |
| 4 | XPath context not defined | accept | §2a.3 design note before evaluator |
| 5 | defaults as isolated M3 feature | accept | defaults part of validation, provenance, report-all-tagged |
| 6 | no partial-data contract | accept | Parse / CheckLocal / Validate |
| 7 | operation validation inputs | accept | request/reply kinds, parent context, external operational tree |
| 8 | anydata/anyxml missing | accept | payload variants in node model |
| 9 | ordering/instance identity | accept | ordered storage, stable handles, keyless lists |
| 10 | value model | accept | §2a.1, exact numerics |
| 11 | context/yang-library incomplete | accept | schema sets, import-only modules, build Context from yang-library |
| 12 | edits vs diffs conflated | accept | separate ApplyEdit / ApplyDiff contracts; YANG Patch out of v1 |
| 13 | Diagnostic too thin | accept | severity, code, schema+data path, line |
| 14 | wrong oracle output for M2 | accept | helper dump; `-f info` for inspection |
| 15 | yanglint can't be the whole oracle | accept | test-only C helper; production remains cgo-free |
| 16 | oracle invocation policy | accept | per-fixture mode manifest, pinned build |
| 17 | byte equality | accept | semantic equality + separate printer suite |
| 18 | agreement % ≠ conformance | partial | RFC-section tags + coverage report + deviations file accepted; fully independent hand-written expected outcomes rejected as too costly for one developer — RFC tag + recorded disagreement resolution is the compromise |
| 19 | milestone order | accept | M1 = vertical slice |
| 20 | license rationale / "clean-room" wording | accept | BSD-3 is a choice, not an obligation; "source-informed port", provenance rules |
| 21 | goyang conflated with ygot | partial | parser-reuse spike G1 in M0 accepted; "compiler fixes upstream" rejected: refine/uses-augment/deviation-order gaps are structural and goyang gets ~3 commits/yr, so upstreaming would gate us on a slow review queue. The coverage figure is relabelled as a judgement |
| 22 | cambium evaluated too late | accept | gate G2 in M0 |
| 23 | estimate has no basis | accept | calendar removed; re-estimate from M1 actuals |
| 24 | premature public interfaces | accept | plugins internal; only ModuleLoader public |
| 25 | hardening deferred | accept | budgets, cancellation, fuzz from M1 |

## 11. Review log — codex `gpt-6-astra`, final M0 review, 2026-09-30

25 findings on the M0 repo state. Two claims checked in libyang v5.8.6 source first: the oracle
forces `LYD_PARSE_STRICT` (`lyoracle.c:369`, true); `when` cycles — first searched and missed by the
lead, then found by a later astra review: libyang rejects them at compile time
(`lys_compile_unres_when_cyclic`), design 03 rule 5 follows that.

| # | Finding | Verdict | Change |
|---|---|---|---|
| 1, 2, 8–13 | Go comparator missing; xsdre diff only logs; no stateful sequences; small corpus; printer-text goldens; STRICT forced; normative vs observed mixed | accept → **M1-pre** row in §4 | harness work before slice code |
| 3 | unprefixed names bind to instantiating context, not defining module | accept | design 03 rule 1 rewritten (two contexts) |
| 4 | dummy-`when` view incomplete | accept | design 03 rule 3: open M1 question |
| 5 | fixpoint can accept cycles | accept | design 03 rule 5: compile-time cycle rejection as libyang + fixture |
| 6 | compare ≠ canonical string; revision-aware type handlers | accept | design 01 |
| 7 | xsdre divergences not registered; limits mixed with deviations | accept | deviations D-0002…D-0008, U-0001; rerun on pinned pcre2 10.46 (same numbers) |
| 14 | harness failures could become goldens | accept | the Go harness (`conformance/cmd/golden`) fails on rc≠0 / request-error, 60 s timeout |
| 15 | all-tagged fixture had no tags | accept | loads `ietf-netconf-with-defaults` (IETF module added to corpus) |
| 16 | `data` ↔ `xpath` import cycle | accept | design 03: xpath owns a narrow Node interface |
| 17 | warnings inside a failure error | accept | §2: `Validate` returns `(Diagnostics, error)` |
| 18 | review/CI not tied to merged SHA; no branch protection | accept | AGENTS.md merge gate |
| 19 | shallow checkout defeats history secret scan | accept | `fetch-depth: 0` in all workflows |
| 20 | UID remap vs caches owned by uid 1000 | accept | all Go caches under `/home/dev` |
| 21 | fuzz minutes on Free plan | accept | weekly, 20 s/target on hosted CI, crashers uploaded; long runs on own hosts |
| 22 | Go 1.26 promised, untested | accept | `make test-go-min` (go1.26.8) in `make ci` |
| 23 | nocgo misses deps / tag-excluded files | accept | source grep + cgo-enabled dependency-closure check |
| 24 | mutable base image, installer from HEAD | partial | Go image pinned by digest, installer pinned to tag; dated Debian snapshot for apt deferred (pcre2 is version-pinned already) |
| 25 | any `v*` tag releases | partial | semver + on-main check added; `gorelease`/`apidiff` before first tag (v0.1.0) |

Also added after the review, on the maintainer's request: `scripts/check-sensitive` + pre-commit hook
+ `make sensitive` (private-details scan; personal patterns never stored in the repo) and
`docs/comparison.md` (now [../comparison-2026-09-30.md](../comparison-2026-09-30.md)).
