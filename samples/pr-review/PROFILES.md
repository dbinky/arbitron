# pr-review model profiles (DB-only — recreate after a profiles-table reseed)

`workflow.json` references these per-stage profiles by alias. They are **admin
profiles stored in the arbitron DB**, NOT seedable from `config.yaml`: the config
`ProfileConfig` shape has no `effort`/`context_window` fields, so a config seed
would silently drop the effort levels below. If the profiles table is ever
emptied/reseeded, recreate them with these exact axes (MCP `profile_create` /
`arbitron` admin API). All reviewers use the read-only `gh`/`git` tool scope;
only the implementer (`coder-opus-medium`) can edit code.

| alias | kind | backend | model | effort | tools | used by |
|-------|------|---------|-------|--------|-------|---------|
| reviewer-codex-medium | agent | codex | gpt-5.5 | medium | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | architect-r1, architect-r2 |
| reviewer-ds4-pro | agent | opencode (connection: ollama-cloud) | deepseek-v4-pro | default | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | 10x-r1, 10x-r2 |
| reviewer-opus-high | agent | claude-code | claude-opus-4-8 | high | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | security |
| reviewer-sonnet-high | agent | claude-code | claude-sonnet-4-6 | high | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | manager |
| coder-opus-medium | agent | claude-code | claude-opus-4-8 | medium | Read,Write,Edit,Grep,Glob,Bash | senior-engineer |

NOTE: `deepseek-v4-pro` uses the BARE model id (no `:cloud` tag) for the
opencode/ollama-cloud path — the `:cloud` form is only for the `ollama run` CLI
and would surface as a useless `exit status 1` through opencode.
