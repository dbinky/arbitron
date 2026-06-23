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
| coder-GLM | agent | opencode (connection: ollama-cloud) | glm-5.2 | default | Read,Write,Edit,Grep,Glob,Bash | produce-plans, baseline-tests |
| coder-ds4-pro | agent | opencode (connection: ollama-cloud) | deepseek-v4-pro | default | Read,Write,Edit,Grep,Glob,Bash | impl-task |
| assessor-opus-low | chat | claude-code | claude-opus-4-8 | low | (chat: tool-suppressed) | impl-assess |
| coder-ds4 | agent | opencode (connection: ollama-cloud) | deepseek-v4-flash | default | Read,Write,Edit,Grep,Glob,Bash | refine |
| assessor-sonnet-high | chat | claude-code | claude-sonnet-4-6 | high (ctx 200k) | (chat: tool-suppressed) | refine-assess |

NOTE: the opencode/ollama-cloud models (`glm-5.2`, `deepseek-v4-pro`,
`deepseek-v4-flash`) use the BARE model id (no `:cloud` tag) — the `:cloud` form
is only for the `ollama run` CLI and would surface as a useless `exit status 1`
through opencode.

NOTE: arbitron's opencode path passes no reasoning/effort parameter and emits no
provider config block for `ollama-cloud`, so the `effort` axis is a no-op for
these profiles — a model's "thinking" mode is whatever opencode/ollama defaults
to, not something the profile controls.
