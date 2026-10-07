@AGENTS.md

## Claude Code
- Model routing (docs/maintainers/agent-workflow.md "Work distribution"): lead = Opus;
  well-specified file ports, tests, fixtures → `porter` subagent (Sonnet); search/manifests/summaries →
  Haiku; independent review → `port-reviewer`.
- Porting one libyang file: use the `port-libyang-file` skill.
- A Go language server (gopls) may be available via the LSP tool: prefer it for find-references,
  definitions and type info across packages before large refactors.
- astra (codex gpt-6-astra) always with `model_reasoning_effort=xhigh`.
- Reviews: `scripts/astra design <file>` before non-trivial designs; on every PR (PR head checked
  out) `ASTRA_PR=<n> bash <(git show origin/main:scripts/astra) review --trusted` — main's copy and
  rules, see docs/maintainers/agent-workflow.md. libyang sources: `make libyang-src` → `.cache/libyang`.
