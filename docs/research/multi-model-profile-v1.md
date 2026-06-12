# Multi-Model Profile Control — v1 Research

**Status:** Research / pre-spec seed for `feat/model-control`
**Date:** 2026-06-11
**Author:** dbinky + Claude

## Goal

Let each LLM profile (and ideally each node) pin not just a **model**, but two
additional axes that Arbitron does not currently express:

1. **Context window** — 200K vs 1M for Anthropic models (the 1M window is a
   metered/credit-gated extended context).
2. **Reasoning effort** — medium / high.

Today a profile only carries `model`, `backend`, `kind`, and `allowedtools`
(see `domain/config/config.go:ProfileConfig`, `domain/provider/provider.go:Profile`).
Neither context-window nor effort exists in the schema, and neither is emitted to
any backend. This doc captures the **target roster** and the **per-backend
mechanics** (with the proven 200K technique from RalphFactory) so the
`feat/model-control` build has a concrete reference.

## Target profile roster

| Alias | Kind | Backend | Model | Context | Effort |
|-------|------|---------|-------|---------|--------|
| `default-chat` | chat | claude-cli | Sonnet 4.6 | **200K** | medium |
| `assessor` | chat | claude-cli | Opus 4.8 | **1M** | medium |
| `default-agent` | agent | claude-code | Sonnet 4.6 | **200K** | medium |
| `interviewer` | agent | claude-code | Opus 4.8 | **1M** | high |
| `reviewer` | agent | claude-code | Opus 4.8 | **1M** | high |
| `reviewer-codex` 🆕 | agent | codex | gpt-5.5 | n/a (codex) | **medium** |
| `coder` | agent | claude-code | Opus 4.8 | **1M** | high |
| `refiner` 🆕 | agent | claude-code | Sonnet 4.6 | **200K** | high |
| `planner` | agent | claude-code | Opus 4.8 | **1M** | high |
| `interviewer-codex` | agent | codex | gpt-5.5 | n/a (codex) | **high** |

🆕 = new profile in this round. The two codex effort levels (`reviewer-codex` =
medium, `interviewer-codex` = high) were the corrections added after the initial
list.

Notes:
- **Codex / gpt-5.5** context is managed by the ChatGPT-subscription codex CLI;
  the Anthropic 200K/1M technique below does not apply. Only model + effort are
  meaningful knobs there.
- Model-name selection already works today (`Profile.Model` → claude-cli
  `--model` / codex `-m`). Context + effort are the new axes.

## Per-axis control mechanics

### 1. Model selection — already supported
- **claude-cli / claude-code:** `buildArgs` emits `--model <model>`
  (`adapter/claudecli/claude.go:83`). RalphFactory instead sets it via the
  `ANTHROPIC_MODEL` env var (`internal/executor/claude.go:78`) — relevant if we
  move the claude backends to env-driven model selection.
- **codex:** `-m <model>` (`adapter/codex/args.go`, `modelFlag`). A model is
  mandatory (fail-closed, `ErrModelRequired`).

### 2. Context window (200K vs 1M) — Anthropic, NEW

**Proven technique (RalphFactory `internal/executor/claude.go:102-118`):** force
the standard 200K window with **two** env vars on the `claude` subprocess —

```
CLAUDE_CODE_DISABLE_1M_CONTEXT=1
CLAUDE_CODE_AUTO_COMPACT_WINDOW=200000
```

Why **both** are required (from RalphFactory's verified comment):
- `CLAUDE_CODE_DISABLE_1M_CONTEXT=1` removes 1M variants from the model picker,
  but on its own does **not** stop Claude Code from reaching for the 1M window:
  auto-compaction is still sized to the model's max window (1M for extended-context
  models like Sonnet 4.6), so it never compacts at 200K and instead **requests 1M**.
- `CLAUDE_CODE_AUTO_COMPACT_WINDOW=200000` caps that budget to 200K, so the session
  auto-compacts at ~95% **before** 1M is ever needed.

Without this, a resumed session that grows past 200K fails with
`"Usage credits required for 1M context"`.

**Mapping to a profile knob:** a per-profile/per-node boolean — call it
`disable_1m_context` (RalphFactory's name) or a `contextWindow: 200k | 1m` enum.
- **200K** → set both env vars.
- **1M** → omit them (Claude Code's default reaches the model's max window), or
  explicitly set `CLAUDE_CODE_AUTO_COMPACT_WINDOW=1000000`.

RalphFactory also strips inherited `ANTHROPIC_*` vars before injecting its own
(`claude.go:67-74`) to prevent cross-backend key/model leakage — worth mirroring.

> Aside: this harness's own Opus 4.8 model id is rendered `claude-opus-4-8[1m]`,
> i.e. a `[1m]` id suffix is *one* way to request 1M. We are deliberately
> standardizing on the **env-var** technique above (per RalphFactory) because it
> also controls auto-compaction, which the bare id suffix does not.

### 3. Reasoning effort (medium / high) — NEW, needs verification

- **codex:** `codex exec` accepts config overrides via `-c key=value`; the
  reasoning knob is `model_reasoning_effort` (`low|medium|high`). Proposed:
  append `-c model_reasoning_effort=<effort>` in `adapter/codex/args.go`.
  ⚠️ **Verify** against the pinned codex-cli (0.137.0) `codex exec --help`, the
  same way the existing codex flags were verified and localized.
- **claude-code / claude-cli:** Claude Code has no clean `low|med|high` effort
  flag. The closest lever is the **thinking-token budget** via env
  (`MAX_THINKING_TOKENS`) or thinking-trigger phrasing. Proposed: map
  `medium`/`high` → two `MAX_THINKING_TOKENS` budgets.
  ⚠️ **Verify** the exact env var name + accepted range before committing to it;
  this is the least-certain part of the design.

## Current Arbitron gap (what feat/model-control must add)

1. **Schema:** add `contextWindow` (or `disable1MContext bool`) and `effort` to
   `ProfileConfig` (`domain/config/config.go`), `Profile` + `ResolvedBinding`
   (`domain/provider/provider.go`), and the resolver mapping
   (`adapter/provider/static.go`).
2. **Node-level override:** the user wants these settable **per node**, not only
   per profile. `node.Config` already carries `MaxTokens`/`Temperature`
   (`domain/engine/executor_llm.go:89`, `executor_interactive.go:135`) but those
   are output-token / sampling knobs consumed only by the Azure API adapter, and
   are **not** the context-window or effort axes. New node fields + precedence
   (node > profile > deployment default) are required, mirroring how the agent
   backend axis already resolves.
3. **Backend env/flag plumbing:**
   - claude-cli/claude-code: inject the two `CLAUDE_CODE_*` env vars (200K case)
     and the thinking-budget env (effort) where the subprocess env is built.
     Today `buildArgs` only sets argv; env injection is a new seam for these
     backends (RalphFactory builds a full env slice — see `BuildEnv`).
   - codex: append `-c model_reasoning_effort=<effort>` in `args.go`.
4. **Validation:** reject `contextWindow: 1m` / effort on backends that don't
   support it (e.g. context on codex), fail-closed, with a clear message.

## Open questions

1. Exact `MAX_THINKING_TOKENS` (or equivalent) name + values for claude effort —
   **needs verification**.
2. Does the **chat** path (`adapter/claudecli` chat adapter) honor the same
   `CLAUDE_CODE_*` env vars as the agent path? It's the same `claude` binary, so
   presumably yes — confirm.
3. Is `model_reasoning_effort` the correct codex config key on 0.137.0? Verify
   via `--help`.
4. Per-node UI: should context/effort be editable in the node inspector, or
   profile-only for v1? (Roster above is profile-level; node override is the
   stretch goal the user called for.)

## References

- RalphFactory 200K technique: `../RalphFactory/internal/executor/claude.go:102-118`
  (env build), config key `disable_1m_context` in `internal/models/config.go:182`,
  tests in `internal/executor/claude_test.go:24-49`.
- Arbitron profile schema: `domain/config/config.go:ProfileConfig`,
  `domain/provider/provider.go:Profile`/`ResolvedBinding`.
- Arbitron claude args: `adapter/claudecli/claude.go:buildArgs`.
- Arbitron codex args: `adapter/codex/args.go`.
- Live profile registry: `~/.arbitron/config.yaml`.
