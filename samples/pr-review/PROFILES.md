# pr-review model profiles (DB-only — recreate after a profiles-table reseed)

`workflow.json` references these per-stage profiles by alias. They are **admin
profiles stored in the arbitron DB**, NOT seedable from `config.yaml`: the config
`ProfileConfig` shape has no `effort`/`context_window` fields, so a config seed
would silently drop the effort levels below. If the profiles table is ever
emptied/reseeded, recreate them with these exact axes (MCP `profile_create` /
`arbitron` admin API).

| alias | kind | backend | model | effort | tools | used by |
|-------|------|---------|-------|--------|-------|---------|
| reviewer-codex-medium | agent | codex | gpt-5.5 | medium | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | architect-r1, architect-r2 |
| reviewer-GLM | agent | opencode (connection: ollama-cloud) | glm-5.2 | default | Read,Grep,Glob,**Bash** | 10x-r1, 10x-r2 |
| reviewer-opus-high | agent | claude-code | claude-opus-4-8 | high | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | security |
| reviewer-sonnet-high | agent | claude-code | claude-sonnet-4-6 | high | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | manager |
| coder-opus-medium | agent | claude-code | claude-opus-4-8 | medium | Read,Write,Edit,Grep,Glob,Bash | verify-fix |
| coder-GLM | agent | opencode (connection: ollama-cloud) | glm-5.2 | default | Read,Write,Edit,Grep,Glob,Bash | senior-engineer |

## CRITICAL: opencode-backed profiles must use plain `Bash`, NOT `Bash(gh:*)`
The opencode backend's tool map (`adapter/opencode/toolmap.go`) only knows whole
tokens (`bash → bash`); it does **not** parse the claude-code scoped-permission
syntax `Bash(gh:*)`/`Bash(git:*)` and **silently drops them**, leaving the agent
with no shell at all (read/grep/glob only). An opencode reviewer with scoped Bash
"finds bugs but can't post" and burns enormous token counts flailing. So:
- **opencode** profiles (`reviewer-GLM`, `coder-GLM`): use plain `Bash`.
- **claude-code / codex** profiles (`reviewer-codex-medium`, `reviewer-opus-high`,
  `reviewer-sonnet-high`, `coder-opus-medium`): scoped `Bash(gh:*)`/`Bash(git:*)`
  is honored, so least-privilege is fine there. (`coder-opus-medium` uses plain
  `Bash` because verify-fix must run arbitrary build/test commands, not just gh/git.)
- `reviewer-ds4-flash` / `reviewer-ds4-pro` (deepseek, opencode) also exist in the
  DB and were corrected to plain `Bash` for the same reason; they are not currently
  referenced by this workflow (10x moved to `reviewer-GLM`).

NOTE: `glm-5.2` (and `deepseek-v4-pro/flash`) use the BARE model id (no `:cloud`
tag) for the opencode/ollama-cloud path — the `:cloud` form is only for the
`ollama run` CLI and would surface as a useless `exit status 1` through opencode.
