@AGENTS.md

## Claude Code
- Model routing (PLAN §2d): lead = Opus; well-specified file ports, tests, fixtures → `porter`
  subagent (Sonnet); search/manifests/summaries → Haiku; independent review → `port-reviewer`.
- Porting one libyang file: use the `port-libyang-file` skill.
- Plan/design reviews go to codex `gpt-6-astra`: `codex exec -m gpt-6-astra -s read-only "…" </dev/null`.
