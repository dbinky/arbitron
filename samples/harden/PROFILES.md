# harden model profiles (DB-only — recreate after a profiles-table reseed)

`workflow.json` references these profiles by alias. They are **admin profiles
stored in the arbitron DB**, NOT seedable from `config.yaml` (the config
`ProfileConfig` shape has no `effort`/`context_window` fields, and the DB is
authoritative once non-empty). If the profiles table is ever emptied/reseeded,
recreate them with these exact axes (MCP `profile_create` / `arbitron` admin API).

| alias | kind | backend | model | effort | context | tools | used by |
|-------|------|---------|-------|--------|---------|-------|---------|
| coder-sonnet5-high | agent | claude-code | claude-sonnet-5-5 | high | 1m | Read,Write,Edit,Grep,Glob,Bash | strengthen |
| approver-codex-high | agent | codex | gpt-6.1-sol | high | default | Read,Write,Edit,Grep,Glob,Bash | tamper-judge, judge-equivalent |

These are the profiles `samples/ralph-loop/PROFILES.md` already defines; nothing
new is needed if ralph-loop is provisioned.

## The doer and the judges are different families

The family of a profile is the vendor of its `model`, not its backend (spec
§3.4).

| doer | judge |
|------|-------|
| `strengthen` (`coder-sonnet5-high`) — Claude | `tamper-judge` (`approver-codex-high`) — GPT |
| `strengthen` (`coder-sonnet5-high`) — Claude | `judge-equivalent` (`approver-codex-high`) — GPT |

Every round crosses the deterministic tamper gate in `--tests-only` mode, so a
source change voids the round without a judge. Only judgeable findings reach
`tamper-judge`. Equivalent-mutant claims reach `judge-equivalent`, which writes
a verdict file: `agent_call` does not enforce `outputSchema`, so the file is
what `record-equivalence` reads.

Mutation tools are not profiles: `scripts/mutation-tools.json` lists each
language's tool, probe and install hint. A missing tool for a changed language
fails the stage at `detect`.

- **Following vs pinned** is an admin profile property. A following profile
  tracks its model family's newest release; a pinned one stays on its model id.
- **Each child run pins its models when it is spawned.** A run started by a
  parent pipeline keeps the models it resolved at spawn time.

Agents only commit; `checkpoint-push-round` (after a round stands) and `finish` push.
