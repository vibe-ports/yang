@AGENTS.md

## Claude Code
- Model routing (PLAN §2d): lead = Opus; well-specified file ports, tests, fixtures → `porter`
  subagent (Sonnet); search/manifests/summaries → Haiku; independent review → `port-reviewer`.
- Porting one libyang file: use the `port-libyang-file` skill.
- Reviews: `scripts/astra design <file>` before non-trivial designs, `ASTRA_PR=<n> scripts/astra review`
  on every PR (see docs/agent-workflow.md). libyang sources: `make libyang-src` → `.cache/libyang`.
