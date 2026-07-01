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
| reviewer-opus-high | agent | claude-code | claude-opus-4-8 | high | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | 10x-r1, security |
| reviewer-opus-medium | agent | claude-code | claude-opus-4-8 | medium | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | 10x-r2 |
| reviewer-sonnet5-high | agent | claude-code | claude-sonnet-5 | high (ctx 200k) | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | manager |
| coder-opus-medium | agent | claude-code | claude-opus-4-8 | medium | Read,Write,Edit,Grep,Glob,Bash | senior-engineer, verify-fix |

NOTE: this lineup is entirely claude-code + codex backends — no opencode profiles
are referenced, so the claude-code scoped-Bash syntax `Bash(gh:*)`/`Bash(git:*)`
is honored on every reviewer and least-privilege holds. (`coder-opus-medium` uses
plain `Bash` because senior-engineer/verify-fix must run arbitrary build/test
commands, not just gh/git.) If you ever swap a stage back to an opencode-backed
profile, remember the opencode tool map (`adapter/opencode/toolmap.go`) does NOT
parse scoped `Bash(...)` tokens and silently drops them — such profiles must use
plain `Bash` or the agent gets no shell at all.

NOTE: `reviewer-sonnet5-high` pins `context_window: 200k`; Sonnet 5 accepts the
full claude-code effort range (no `xhigh → high` flooring that Sonnet 4.6 had).
