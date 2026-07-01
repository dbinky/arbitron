# feature-pipeline model profiles (DB-only — recreate after a profiles-table reseed)

`workflow.json` references these per-stage profiles by alias. They are **admin
profiles stored in the arbitron DB**, NOT seedable from `config.yaml` (the config
`ProfileConfig` shape has no `effort`/`context_window` fields, so a config seed
would silently drop the effort levels and the DB is authoritative once non-empty).
If the profiles table is ever emptied/reseeded, recreate them with these exact
axes (MCP `profile_create` / `arbitron` admin API).

| alias | kind | backend | model | effort | tools | used by |
|-------|------|---------|-------|--------|-------|---------|
| interviewer-codex | agent | codex | gpt-5.5 | default | Read,Write,Grep,Glob,Bash | brainstorm-spec |
| interviewer-opus-high | agent | claude-code | claude-opus-4-8 | high | Read,Write,Grep,Glob,Bash | brainstorm-design |
| planner-opus-medium | agent | claude-code | claude-opus-4-8 | medium | Read,Write,Grep,Glob,Bash | align-designs, align-plans, seed-ralph |
| coder-opus-high | agent | claude-code | claude-opus-4-8 | high | Read,Write,Edit,Grep,Glob,Bash | produce-plans |
| coder-sonnet5-medium | agent | claude-code | claude-sonnet-5 | medium (ctx 200k) | Read,Write,Edit,Grep,Glob,Bash | baseline-tests |
| coder-opus-medium | agent | claude-code | claude-opus-4-8 | medium | Read,Write,Edit,Grep,Glob,Bash | impl-task |
| assessor-opus-low | chat | claude-code | claude-opus-4-8 | low | (chat: tool-suppressed) | impl-assess |
| coder-sonnet5-high | agent | claude-code | claude-sonnet-5 | high (ctx 200k) | Read,Write,Edit,Grep,Glob,Bash | refine |
| assessor-sonnet5-high | chat | claude-code | claude-sonnet-5 | high (ctx 200k) | (chat: tool-suppressed) | refine-assess |

NOTE: the Sonnet 5 profiles pin `context_window: 200k`. Unlike Sonnet 4.6 (whose
claude-code capability rule floored `xhigh → high` and disallowed some tiers),
Sonnet 5 accepts the full claude-code effort range, so `medium`/`high` apply
as-is with no flooring.
