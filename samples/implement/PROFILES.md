# implement model profiles (DB-only — recreate after a profiles-table reseed)

`workflow.json` references these profiles by alias. They are **admin profiles
stored in the arbitron DB**, NOT seedable from `config.yaml` (the config
`ProfileConfig` shape has no `effort`/`context_window` fields, and the DB is
authoritative once non-empty). If the profiles table is ever emptied/reseeded,
recreate them with these exact axes (MCP `profile_create` / `arbitron` admin API).

| alias | kind | backend | model | effort | context | tools | used by |
|-------|------|---------|-------|--------|---------|-------|---------|
| coder-sonnet5-medium | agent | claude-code | claude-sonnet-5-5 | medium | 1m | Read,Write,Edit,Grep,Glob,Bash | baseline-tests |
| coder-pi-sol-high | agent | pi | openai-codex/gpt-6.1-sol | high | default | — | impl-task |
| approver-opus-medium | agent | claude-code | claude-opus-5-5 | medium | default | Read,Write,Edit,Grep,Glob,Bash | tamper-judge |
| assessor-opus-high | chat | (inherit) | claude-opus-5-5 | high | default | (chat: tool-suppressed) | impl-assess |

These are the profiles `samples/legacy-feature-pipeline/PROFILES.md` and
`samples/ralph-loop/PROFILES.md` already define; nothing new is needed if either
workflow is provisioned. `coder-pi-sol-high` names no connection: it needs the
one-time `pi` `/login` (ChatGPT Plus/Pro (Codex)) described in
`samples/legacy-feature-pipeline/PROFILES.md`.

## The doer and the judge are different families

The family of a profile is the vendor of its `model`, not its backend (spec
§3.4). `openai-codex/gpt-6.1-sol` strips to `gpt-6.1-sol`, so the doer is GPT
even though it runs on pi.

| doer | judge |
|------|-------|
| `impl-task` (`coder-pi-sol-high`) — GPT | `tamper-judge` (`approver-opus-medium`) — Claude |

Every pass crosses the deterministic tamper gate before it is pushed or
counted. Only judgeable findings reach the judge; a history rewrite inside the
pass is voided without one.

Completion is decided by `scripts/count-plan-steps.sh`, never by `impl-assess`:
`llm_call` `outputSchema` is not enforced, so only `all_remaining_blocked` is
read, and only when present and true. Before it, `scripts/stall-check.sh`
stops the loop as blocked once 3 consecutive passes tick no checkbox, naming
the unresolved `> BLOCKED` notes in the plans.

- **Following vs pinned** is an admin profile property. A following profile
  tracks its model family's newest release; a pinned one stays on its model id.
- **Each child run pins its models when it is spawned.** A run started by a
  parent pipeline keeps the models it resolved at spawn time.

Agents only commit; `checkpoint-push-baseline`, `checkpoint-push-impl`, `checkpoint-push-void` and
`finish` push.
