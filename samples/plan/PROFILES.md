# plan model profiles (DB-only — recreate after a profiles-table reseed)

`workflow.json` references these profiles by alias. They are **admin profiles
stored in the arbitron DB**, NOT seedable from `config.yaml` (the config
`ProfileConfig` shape has no `effort`/`context_window` fields, and the DB is
authoritative once non-empty). If the profiles table is ever emptied/reseeded,
recreate them with these exact axes (MCP `profile_create` / `arbitron` admin API).

| alias | kind | backend | model | effort | context | tools | used by |
|-------|------|---------|-------|--------|---------|-------|---------|
| coder-opus-high | agent | claude-code | claude-opus-5-5 | high | default | Read,Write,Edit,Grep,Glob,Bash | produce-plans, restore-steps, restore-steps-2, restore-steps-3 |
| reviewer-codex-high | agent | codex | gpt-6.1-sol | high | default | Read,Write,Edit,Grep,Glob,Bash | align-plans, align-plans-2, align-plans-3 |
| assessor-opus-high | chat | (inherit) | claude-opus-5-5 | high | default | (chat: tool-suppressed) | deps-assess, deps-assess-2, deps-assess-3 |
| interviewer-opus-high | agent | claude-code | claude-opus-5-5 | high | default | Read,Write,Grep,Glob,Bash | resolve-deps, resolve-deps-2 |

These are the same four profiles `samples/legacy-feature-pipeline/PROFILES.md` defines
for its planning phase; nothing new is needed if that workflow is already
provisioned.

The plans are written by Claude Opus and aligned by GPT (spec §3.4: family is
the model vendor, not the backend), so the doer and the reviewer of the plans are
different families. The dependency gates route on `scripts/deps-count.sh`, never
on `deps-assess` fields, because `llm_call` `outputSchema` is not enforced.

## Timeouts

Agent timeouts are sized for features with 5+ design phases. produce-plans
needed about 2.5h on lunaroute and timed out three times at 3600s, so it is
14400s (4h); align-plans*, restore-steps* are 7200s. `timeoutSeconds` is a
static int on the node (it cannot be templated from a computed value), so the
values are generous rather than derived from the phase count. Raise them here
and in `workflow.json` together if larger features still time out.

Following vs pinned is an admin profile property. Each child run pins its models
when it is spawned. Agents and the resolve-deps session only commit; the
`checkpoint-push*` nodes and `finish` push.
