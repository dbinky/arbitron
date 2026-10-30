# ideate model profiles (DB-only — recreate after a profiles-table reseed)

`workflow.json` references this profile by alias. It is an **admin profile
stored in the arbitron DB**, NOT seedable from `config.yaml` (the config
`ProfileConfig` shape has no `effort`/`context_window` fields, so a config seed
would silently drop the effort level and the DB is authoritative once
non-empty). If the profiles table is ever emptied/reseeded, recreate it with
these exact axes (MCP `profile_create` / `arbitron` admin API).

| alias | kind | backend | model | effort | context | tools | used by |
|-------|------|---------|-------|--------|---------|-------|---------|
| interviewer-opus-high | agent | claude-code | claude-opus-5-5 | high | default | Read,Write,Grep,Glob,Bash | jam |

The jam is the only model step. It runs on an agent profile, not a chat one,
because it reads the repo to ground the conversation and, on request, writes
and commits the idea doc. `Bash` covers `git log`/`git show` and the local
commit; the push happens in the `finish` script. The tool list has no `Edit`,
so the doc is written whole and rewritten whole on each revision.

The family of the profile is the vendor of its `model`, not its backend (spec
§3.4): `claude-*` is Claude. There is no doer/judge pair in this stage.

- **Following vs pinned** is an admin profile property. A following profile
  tracks its model family's newest release; a pinned one stays on its model id.
  `interviewer-opus-high` is pinned.
- **Each child run pins its models when it is spawned.** A run started by a
  parent pipeline keeps the models it resolved at spawn time, even if a profile
  is edited while it runs.
- The Opus 5.5 profile leaves `context_window: default`, so it inherits the
  model's native 1M window rather than being pinned to a tier. A long jam needs
  that headroom.
