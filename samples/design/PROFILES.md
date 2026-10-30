# design model profiles (DB-only — recreate after a profiles-table reseed)

`workflow.json` references these profiles by alias. They are **admin profiles
stored in the arbitron DB**, NOT seedable from `config.yaml` (the config
`ProfileConfig` shape has no `effort`/`context_window` fields, and the DB is
authoritative once non-empty). If the profiles table is ever emptied/reseeded,
recreate them with these exact axes (MCP `profile_create` / `arbitron` admin API).

| alias | kind | backend | model | effort | context | tools | used by |
|-------|------|---------|-------|--------|---------|-------|---------|
| interviewer-opus-high | agent | claude-code | claude-opus-5-5 | high | default | Read,Write,Grep,Glob,Bash | brainstorm-design |
| reviewer-codex-high | agent | codex | gpt-6.1-sol | high | default | Read,Write,Edit,Grep,Glob,Bash | align-designs |

These are the same two profiles `samples/legacy-feature-pipeline/PROFILES.md` defines;
nothing new is needed if that workflow is already provisioned.

The design brainstorm stays on Claude Opus and the alignment reviewer on GPT
(spec §3.4: family is the model vendor, not the backend), so the doer and the
reviewer of the design docs are different families. The brainstorm session has no
network egress and only commits locally; `finish` pushes. The alignment agent
also only commits.
