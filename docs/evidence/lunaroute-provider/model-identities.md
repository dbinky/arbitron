# LunaRoute model identity evidence

This note records source evidence, not live execution verification. LR-03's
reviewed-alias decision is in
[`2026-11-10-lunaroute-provider-decisions.md`](../../designs/2026-11-10-lunaroute-provider-decisions.md#lr-03--fixed-executable-ids-and-alias-handling-under-pinning).

## claude-sonnet-5-lr1

- Source title: **Claude Code — The window describes ANTHROPIC_MODEL, not the aliases**.
- URL: https://docs.lunaroute.com/getting-started/claude-code/
- Markdown source: https://docs.lunaroute.com/getting-started/claude-code.md
- Accessed: 2026-11-11 (UTC).
- Source passage:

  > Claude Code applies `CLAUDE_CODE_MAX_CONTEXT_TOKENS` to the model id in `ANTHROPIC_MODEL`, and ignores it
  > for the Claude-shaped alias ids (`claude-sonnet-5-lr1`, …) — those resolve to a real Claude model, which
  > carries its own 1M window.

This confirms the exact alias spelling and its Claude vendor identity. Together
with LR-03, it supports the exact reviewed mapping
`lunaroute / claude-sonnet-5-lr1 → anthropic`. It does not establish an immutable
upstream version, any other `-lrN` alias, or live availability. The classifier
uses the canonical lab name from `domain/modelidentity/identity.go:families`.

P1-T6 collected this alias-specific prerequisite before populating its map.
P1-T11 still owns preparation for the other release ids and capability facts,
the evidence register, and live gateway verification; none is claimed here.
