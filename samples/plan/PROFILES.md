# plan model profiles (DB-only — recreate after a profiles-table reseed)

`workflow.json` references these profiles by alias. They are **admin profiles
stored in the arbitron DB**, NOT seedable from `config.yaml` (the config
`ProfileConfig` shape has no `effort`/`context_window` fields, and the DB is
authoritative once non-empty). If the profiles table is ever emptied/reseeded,
recreate them with these exact axes (MCP `profile_create` / `arbitron` admin API).

| alias | kind | backend | model | effort | context | tools | used by |
|-------|------|---------|-------|--------|---------|-------|---------|
| coder-opus-high | agent | claude-code | claude-opus-5-5 | high | default | Read,Write,Edit,Grep,Glob,Bash | produce-plans |
| reviewer-codex-high | agent | codex | gpt-6.1-sol | high | default | Read,Write,Edit,Grep,Glob,Bash | align-plans, align-plans-2 |
| assessor-opus-high | chat | (inherit) | claude-opus-5-5 | high | default | (chat: tool-suppressed) | deps-assess, deps-assess-2 |
| interviewer-opus-high | agent | claude-code | claude-opus-5-5 | high | default | Read,Write,Grep,Glob,Bash | resolve-deps |

These are the same four profiles `samples/legacy-feature-pipeline/PROFILES.md` defines
for its planning phase; nothing new is needed if that workflow is already
provisioned.

The plans are written by Claude Opus and aligned by GPT (spec §3.4: family is
the model vendor, not the backend), so the doer and the reviewer of the plans are
different families. The dependency gates route on `scripts/deps-count.sh`, never
on `deps-assess` fields, because `llm_call` `outputSchema` is not enforced.

Following vs pinned is an admin profile property. Each child run pins its models
when it is spawned. Agents and the resolve-deps session only commit; the
`checkpoint-push*` nodes and `finish` push.
