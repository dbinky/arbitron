# 1M Context Cleanup — retire a tier that no longer means anything

**Status:** idea / not started.
**Written:** 2026-10-23
**Scope:** the `context_window` profile axis (`default | 200k | 1m`), its claude-code emission, its docs and comments, and the live profiles pinned to `1m`.
**Explicitly out of scope:** the `200k` tier, which still works and still matters, and codex/opencode/pi context handling.

---

## 1. Why this exists

The context-window axis was designed when 1M context was an opt-in, metered extension. That's no longer true for the models Arbitron drives. Per the Claude Code docs, Fable 5.1, Fable 5, Sonnet 5 and later, Haiku 5.5, and Opus 4.7 and later all **run with the 1M window by default**, with no `[1m]` suffix needed. That includes Bedrock, Foundry and Agent Platform sessions. `CLAUDE_CODE_DISABLE_1M_CONTEXT=1` is now the way *down* to 200K.

So on current models:
- **`default` already means 1M.**
- **`200k` is the only tier that changes anything.**
- **`1m` is at best a no-op,** and at worst slightly harmful (§2).

Source: <https://code.claude.com/docs/en/model-config>

## 2. What exists today (grounding)

- **`domain/provider/axes.go:8,17`.** The comments say `1m` "opts into the metered extended 1M window". That's stale.
- **`domain/provider/capability.go:211-224`.** `emitClaudeContext`:
  - `200k` sets `CLAUDE_CODE_DISABLE_1M_CONTEXT=1` plus `CLAUDE_CODE_AUTO_COMPACT_WINDOW=200000`. This is correct and still needed.
  - `1m` sets only `CLAUDE_CODE_AUTO_COMPACT_WINDOW=1000000`. The docs say native-1M models auto-compact at **about 967K by default**. Forcing the threshold to 1,000,000 moves compaction *past* the safe point, so a long session can hit the hard window before it compacts.
  - `default` emits nothing. On current models that is the correct 1M behavior.
- **`adapter/config/capabilities.default.yaml:10,18`.** This lists `context_tiers: [default, 200k, 1m]` for claude-code.
- **`adapter/mcp/tools_profile.go:66,80`.** The MCP profile tools advertise `default | 200k | 1m`.
- **Live profiles pinned to `1m`** (DB-only, so not in git): `approver-sonnet5-high`, `assessor-sonnet-high`, `assessor-sonnet5-high`, `coder-sonnet-high`, `coder-sonnet-medium`, `coder-sonnet5-high`, `coder-sonnet5-medium`, `reviewer-sonnet-high`, `reviewer-sonnet5-high`. These are a leftover from the "Sonnet profiles off the 200k pin" change, when `1m` was the way to escape 200K.

## 3. The idea

Treat `default` as "the model's native window" (1M on everything current) and `200k` as the one real override. Then do one of two things with `1m`:

- **(a) Remove it.** Drop it from the enum, the capability YAML and the MCP descriptions. Migrate stored `1m` profiles to `default`.
- **(b) Keep it as an alias for `default`.** It emits nothing, so old manifests and profiles keep validating, and it's marked deprecated in the MCP descriptions and the dashboard.

Either way, `1m` must stop emitting `CLAUDE_CODE_AUTO_COMPACT_WINDOW=1000000`.

## 4. Proposal sketch

1. Change `emitClaudeContext` so `1m` emits nothing, the same as `default`. This is the behavior fix and is safe on its own.
2. Rewrite the comments in `axes.go` and `capability.go` to describe current semantics: default means native (1M), 200k means pinned down.
3. Migrate the nine live `1m` profiles to `default` via `profile_update`.
4. Decide between (a) and (b). If (a), add a migration and a validation error that says what to use instead.
5. Update tests around `emitClaudeContext` and the capability ruleset.

## 5. Design tensions and gotchas

- **Older models.** If anything still drives a pre-4.7 Opus or pre-5 Sonnet, `default` there is 200K and `1m` might still be meaningful. Check the capability ruleset before removing the tier. It may need to stay model-scoped.
- **Profile axes are DB-only** (config.yaml `ProfileConfig` lacks them), so the profile migration is a live-data operation, not a code change.
- **Non-Anthropic backends** ignore the axis today. Make sure removing `1m` doesn't break validation for them.

## 6. Open questions

1. Remove `1m`, or keep it as a deprecated alias? Leaning toward the alias.
2. Is any profile or workflow still on a model where `default` is below 1M?

## Appendix — related

- Memory: model-control feature (feat/model-control), profile-editing feature (DB-driven axes).
