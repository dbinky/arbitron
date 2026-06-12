# Plan — Phase 2, Task 2: `InteractiveProvider` (engine.ChatProvider) + constructor

**Slug:** `codex-interactive`
**Design:** `docs/superpowers/specs/codex-interactive-design-phase-2.md` (§2, §4)
**Depends on:** Task 1.1 (port + fake), Task 2.1 (the `decide` policy)
**Package:** `adapter/codex`

## Goal

The interactive `engine.ChatProvider` that runs a codex interview turn over the
`mcpSession` seam, wiring the Task-2.1 policy as the `approvalFunc`, folding the
turn stream into `ChatChunk`s, round-tripping `SessionID` for resume, and failing
closed. Plus the `InteractiveConfig` + `NewInteractiveProvider` constructor (owned
here, used by Phase 3).

## Files

- `adapter/codex/interactive_provider.go` (new) — provider + config + constructor.
- `adapter/codex/interactive_provider_test.go` (new) — five-category coverage on
  the fake transport.

## Implementation

```
type InteractiveConfig struct {
    Timeout          time.Duration
    MaxResponseBytes int64
    CapabilityRules  provider.CapabilityRules
    WritableRoot     string // default "docs"
    // session: a factory for the mcpSession (real in prod, fake in tests)
}

type InteractiveProvider struct {
    core                       // shared binding/probe/timeout (newCore)
    newSession func() mcpSession
    writableRoot string
}

func NewInteractiveProvider(cfg InteractiveConfig) *InteractiveProvider
var _ engine.ChatProvider = (*InteractiveProvider)(nil)
```

- `WritableRoot` defaults to `"docs"` when empty.
- Build `core` via `newCore` so construction + the version probe cannot drift
  from the exec adapters.

### `StreamChat`

1. Guard: empty `WorkDir` → `ErrWorkDirRequired` (reuse sentinel); resolve model
   (`req.Model` else `p.binding.Model`); empty model fails closed with no stream.
2. Build `turnConfig{Model, Prompt, WorkDir, ResumeID: req.SessionID, Effort: …,
   Approver: approvalPolicy{workDir: req.WorkDir, writableRoot: p.writableRoot}.decide}`.
3. `Start` the session; error → `nil, err` (no half-open).
4. Goroutine folds `turnStream`: `turnToken` → `ChatChunk{Token}`; `turnDone` →
   `ChatChunk{Done, SessionID, tokens}`; `turnFailed`/transport err →
   `ChatChunk{Error}` with **no Done**. Always drain + reap. Reuse the existing
   `makeSendChunk` non-blocking pattern (ctx-raced send) so a gone consumer never
   blocks the drainer.
5. Record each approval decision to the node trace (kind `approval`, summary,
   allow/deny + reason). Denials are the only guardrail signal; never surface to
   the human.

### `Chat`

Fold the same turn to completion → `ChatResponse{Output, SessionID, Model,
tokens}`; a failed turn / transport break is a hard error (no silent empty
result). Mirrors the exec adapter's Chat/StreamChat symmetry.

## Tests (TDD — all five categories; fake transport only; NO live codex)

- **Happy path:** scripted `[token,token,done{c1}]` → ordered tokens then one
  terminal `Done` with `SessionID=c1`; `turnConfig` carries WorkDir + model; the
  Approver is the policy.
- **Success:** `req.SessionID="c1"` → `ResumeID="c1"` on the turn; an
  informational-command approval → allowed; a `docs/specs/…` write → allowed; the
  deliverable commit → allowed.
- **Failure:** a `turnFailed` → terminal `Error` chunk, no `Done`.
- **Error:** `Start` error → `StreamChat` returns `nil, err`; malformed/transport
  break mid-stream → `Error` chunk, drainer reaps.
- **Edge case (mandatory guardrail scenario):** a patch to
  `domain/.../Settings.tsx` (outside `docs/`) → **denied**, decision traced,
  stream continues (no spurious `Done`, no human notice); a `..`-escape path →
  denied; an unknown approval kind → denied; empty `WorkDir` → `ErrWorkDirRequired`
  with no stream; ctx cancelled mid-stream → sender unblocks, child reaped.

## Verification

- `go test ./adapter/codex/...` green; `go vet` clean.
- No `OPENAI_API_KEY` in any recorded env; `binding.RevealKey` never consulted.

## Out of scope

- Composition-root wiring + concrete sandbox config values (Phase 3).
