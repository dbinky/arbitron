# feature-pipeline model profiles (DB-only — recreate after a profiles-table reseed)

`workflow.json` references these per-stage profiles by alias. They are **admin
profiles stored in the arbitron DB**, NOT seedable from `config.yaml` (the config
`ProfileConfig` shape has no `effort`/`context_window` fields, so a config seed
would silently drop the effort levels and the DB is authoritative once non-empty).
If the profiles table is ever emptied/reseeded, recreate them with these exact
axes (MCP `profile_create` / `arbitron` admin API).

## The rule the lineup encodes

**The family that writes never certifies.** Opus plans → GPT implements →
Claude approves → a cheap model refines under a Claude approver. Every seam has a
cross-family check, which is what makes the `approve-area` node meaningful: if the
approver shared a family with the author, the review plan's two independent
approvals would be one model grading itself twice.

| alias | kind | backend | model | effort | tools | used by |
|-------|------|---------|-------|--------|-------|---------|
| interviewer-codex | agent | codex | gpt-5.6-sol | default | Read,Write,Grep,Glob,Bash | brainstorm-spec |
| interviewer-opus-high | agent | claude-code | claude-opus-5 | high | Read,Write,Grep,Glob,Bash | brainstorm-design, resolve-deps |
| reviewer-codex-high | agent | codex | gpt-5.6-sol | high | Read,Write,Edit,Grep,Glob,Bash | align-designs, align-plans |
| coder-opus-high | agent | claude-code | claude-opus-5 | high | Read,Write,Edit,Grep,Glob,Bash | produce-plans |
| planner-opus-medium | agent | claude-code | claude-opus-5 | medium | Read,Write,Grep,Glob,Bash | seed-ralph |
| coder-sonnet5-medium | agent | claude-code | claude-sonnet-5 | medium (ctx 200k) | Read,Write,Edit,Grep,Glob,Bash | baseline-tests |
| coder-codex-high | agent | codex | gpt-5.6-sol | high | Read,Write,Edit,Grep,Glob,Bash | impl-task |
| assessor-opus-high | chat | claude-code | claude-opus-5 | high | (chat: tool-suppressed) | impl-assess, deps-assess |
| refine-flash | agent | opencode | deepseek/deepseek-v4-flash | default | Read,Write,Edit,Grep,Glob,Bash | refine |
| approver-sonnet5-high | agent | claude-code | claude-sonnet-5 | high (ctx 200k) | Read,Write,Edit,Grep,Glob,Bash | approve-area |
| refine-deep | agent | claude-code | claude-opus-5 | high | Read,Write,Edit,Grep,Glob,Bash | refine-rescue |
| assessor-sonnet5-high | chat | claude-code | claude-sonnet-5 | high (ctx 200k) | (chat: tool-suppressed) | refine-assess, disambiguate-docs |

## Prerequisite: the `refine-flash` connection

`refine-flash` is the only profile needing a connection. Create it first or the
profile cannot be created:

```
connection_create  name=openrouter  provider=openrouter
                   base_url=https://openrouter.ai/api/v1  api_key=<your key>
```

Note the model id keeps its org prefix (`deepseek/deepseek-v4-flash`); the
opencode adapter preserves a leading segment that is not the provider id, so it
resolves as `openrouter/deepseek/deepseek-v4-flash`. This differs from the
ollama-cloud convention, where the BARE model name is required.

## Axis constraints worth knowing

- **opencode-backed profiles accept only `default` context and `default` effort.**
  There is no effort dial on `refine-flash`; do not try to add one.
- **codex accepts effort up to `xhigh` but only `default` context.**
- The Sonnet 5 profiles pin `context_window: 200k`. Unlike Sonnet 4.6 (whose
  claude-code capability rule floored `xhigh → high` and disallowed some tiers),
  Sonnet 5 accepts the full claude-code effort range, so `medium`/`high` apply
  as-is with no flooring.
- `refine-deep` deliberately leaves `context_window: default` so it inherits
  Opus 5's native 1M window rather than being pinned to the 200k tier.
