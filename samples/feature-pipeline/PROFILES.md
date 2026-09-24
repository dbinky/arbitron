# feature-pipeline model profiles (DB-only — recreate after a profiles-table reseed)

`workflow.json` references these per-stage profiles by alias. They are **admin
profiles stored in the arbitron DB**, NOT seedable from `config.yaml` (the config
`ProfileConfig` shape has no `effort`/`context_window` fields, so a config seed
would silently drop the effort levels and the DB is authoritative once non-empty).
If the profiles table is ever emptied/reseeded, recreate them with these exact
axes (MCP `profile_create` / `arbitron` admin API).

## The rule the lineup encodes

**The family that writes never certifies.** The refinement loop routes each
review to its own cross-family pair, so an area's two independent approvals can
never be one model grading itself twice:

| lane | refines | approves |
|------|---------|----------|
| single review 1 (wiring lens) | `coder-sonnet5-high` — Sonnet 5 | `approver-codex-high` — gpt-6-sol |
| single review 2 (failure lens) | `coder-codex-high` — gpt-6-sol | `approver-opus-medium` — Opus 5.5 |
| paired integration seam | `coder-opus-medium` — Opus 5.5 | `approver-codex-high` — gpt-6-sol |

`refine-rescue` (`refine-deep`, Opus 5.5) is the stagnation escape hatch. It
records its commit directly rather than routing through a lane approver — it
breaks a stall rather than working a numbered review row, and the next iteration
re-routes through `select-work` and is assessed normally.

| alias | kind | backend | model | effort | tools | used by |
|-------|------|---------|-------|--------|-------|---------|
| approver-codex-high | agent | codex | gpt-6-sol | high | Read,Write,Edit,Grep,Glob,Bash | assess-single-1, assess-paired |
| approver-opus-medium | agent | claude-code | claude-opus-5-5 | medium | Read,Write,Edit,Grep,Glob,Bash | assess-single-2 |
| assessor-opus-high | chat | (inherit) | claude-opus-5-5 | high | (chat: tool-suppressed) | deps-assess, impl-assess |
| assessor-sonnet5-high | chat | (inherit) | claude-sonnet-5 | high | (chat: tool-suppressed) | disambiguate-docs, refine-assess |
| coder-codex-high | agent | codex | gpt-6-sol | high | Read,Write,Edit,Grep,Glob,Bash | refine-single-2 |
| coder-opus-high | agent | claude-code | claude-opus-5-5 | high | Read,Write,Edit,Grep,Glob,Bash | produce-plans |
| coder-opus-medium | agent | claude-code | claude-opus-5-5 | medium | Read,Write,Edit,Grep,Glob,Bash | refine-paired |
| coder-pi-sol-high | agent | pi | openai-codex/gpt-6-sol | high | — | impl-task |
| coder-sonnet5-high | agent | claude-code | claude-sonnet-5 | high | Read,Write,Edit,Grep,Glob,Bash | refine-single-1 |
| coder-sonnet5-medium | agent | claude-code | claude-sonnet-5 | medium | Read,Write,Edit,Grep,Glob,Bash | baseline-tests |
| critic-codex-high | agent | codex | gpt-6-astra | high | Read,Write,Edit,Grep,Glob,Bash | critique-seed |
| interviewer-codex | agent | codex | gpt-6-sol | default | Read,Write,Grep,Glob,Bash | brainstorm-spec |
| interviewer-opus-high | agent | claude-code | claude-opus-5-5 | high | Read,Write,Grep,Glob,Bash | brainstorm-design, resolve-deps |
| planner-opus-high | agent | claude-code | claude-opus-5-5 | high | Read,Write,Edit,Grep,Glob,Bash | seed-ralph |
| refine-deep | agent | claude-code | claude-opus-5-5 | high | Read,Write,Edit,Grep,Glob,Bash | refine-rescue |
| reviewer-codex-high | agent | codex | gpt-6-sol | high | Read,Write,Edit,Grep,Glob,Bash | align-designs, align-plans |

## Prerequisite: none

Every profile here runs on a subscription or on pi's own OAuth store, so none
needs a `connection_create` first. `coder-pi-sol-high` is the one to understand:
it names **no connection**, because a connection requires an `api_key` and a
`base_url` and a ChatGPT subscription has neither. The pi adapter resolves the
provider from the model's own prefix (`openai-codex/…`) instead, and that
fallback is restricted to OAuth providers — a key-based provider named only by a
prefix fails closed rather than borrowing whatever credential pi has on hand.

`coder-pi-sol-high` therefore requires a one-time interactive login on the host:

```
pi              # launches the TUI
/login          # select "ChatGPT Plus/Pro (Codex)"
```

Verify with `pi auth check --provider openai-codex --json` — it must report
`{"status":"ready","authType":"oauth"}`. pi keeps its own credential store at
`~/.pi/agent/auth.json` and does **not** read `~/.codex/auth.json`, so a working
codex CLI login does nothing for pi.

## Axis constraints worth knowing

- **pi accepts the full graded effort range** (its thinking levels are
  off/minimal/low/medium/high/xhigh/max, so nothing is floored) but only
  `default` context — the window is the model's own, and pi has no dial for it.
- **codex accepts effort up to `xhigh` but only `default` context.**
- **opencode-backed profiles accept only `default` context and `default` effort.**
  No profile here uses opencode any more — `refine` was retired when the single
  refine node became the three-lane structure — but the constraint still applies
  if one is added back.
- The Sonnet 5 and Opus 5.5 profiles leave `context_window: default` so each
  inherits its model's native 1M window rather than being pinned to a tier.
- Opus 5.5's own API default effort is `medium`, so the `high` profiles here are
  a deliberate step up, not a restatement of the default.
