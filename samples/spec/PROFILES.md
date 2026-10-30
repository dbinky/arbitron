# spec model profiles (DB-only — recreate after a profiles-table reseed)

`workflow.json` references this profile by alias. It is an **admin profile
stored in the arbitron DB**, NOT seedable from `config.yaml` (the config
`ProfileConfig` shape has no `effort`/`context_window` fields, and the DB is
authoritative once non-empty). If the profiles table is ever emptied/reseeded,
recreate it with these exact axes (MCP `profile_create` / `arbitron` admin API).

| alias | kind | backend | model | effort | context | tools | used by |
|-------|------|---------|-------|--------|---------|-------|---------|
| interviewer-codex | agent | codex | gpt-6.1-sol | default | default | Read,Write,Grep,Glob,Bash | brainstorm-spec |

The spec interview is the only model step. It stays on `interviewer-codex`
(ruling P14): a live instance completed `brainstorm-spec` on this profile. A
codex interactive node has no network egress, so it only commits locally;
`finish` pushes. The tool list has no `Edit`, so the spec is written whole.

The family of the profile is the vendor of its `model`, not its backend (spec
§3.4): `gpt-*` is OpenAI. There is no doer/judge pair in this stage.

- **Following vs pinned** is an admin profile property. A following profile
  tracks its model family's newest release; a pinned one stays on its model id.
- **Each child run pins its models when it is spawned.** A run started by a
  parent pipeline keeps the models it resolved at spawn time.
