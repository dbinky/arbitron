# Plan — Phase 3, Task 1: composition-root wiring + selection/regression tests

**Slug:** `codex-interactive`
**Design:** `docs/superpowers/specs/codex-interactive-design-phase-3.md`
**Depends on:** Task 2.2 (`InteractiveProvider` + `NewInteractiveProvider`)
**Packages:** `cmd/arbitron`, `adapter/provider` (tests)

## Goal

Make the interactive provider live for codex-backed interactive nodes — and only
those — by swapping one map value in the composition root, supplying the
read-only-sandbox + auto-approval posture and the `docs/` boundary. Prove the
autonomous codex `exec` path and existing codex tests are untouched.

## The wiring change (`cmd/arbitron/main.go`, section 12c)

Replace the interactive `agentStreams["codex"]` value (currently
`codex.NewStreamProvider(...)`, the `codex exec --json` stream) with the Phase-2
interactive provider:

```
codexInteractive := codex.NewInteractiveProvider(codex.InteractiveConfig{
    Timeout:          time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
    MaxResponseBytes: cfg.LLM.MaxResponseBytes,
    CapabilityRules:  capabilityRules,
    WritableRoot:     "docs",
})
interactiveStreamSel := providersadapter.NewInteractiveStreamSelector(
    providerFactory,
    map[string]engine.ChatProvider{
        workflow.BackendClaudeCode: claudeStreamAgent,
        workflow.BackendCodex:      codexInteractive, // was codexStreamAgent
    },
    cfg.LLM.DefaultAgentBackend,
)
```

**Do not touch:** `codex.New`/`codex.Provider` (the batch `AgentProviderFor` codex
arm — autonomous `agent_call` stays `codex exec
--dangerously-bypass-approvals-and-sandbox`), the chat path, claude-cli, opencode,
any workflow definition, any profile. If `codexStreamAgent` (the exec stream)
becomes unused after the swap, remove its now-dead construction to keep the root
clean (it is NOT used elsewhere — the batch path builds its own `codex.Provider`).

## Tests (TDD — all five categories; `adapter/provider`)

Extend the selector tests (`interactive_stream_selector_test.go` style) using a
stub `engine.ChatProvider` for the codex interactive entry:

- **Happy path:** a codex agent-kind interactive binding → the interactive entry.
- **Success:** empty backend floored to default still routes to the interactive
  entry; `claude-code` still routes to the claude stream adapter.
- **Failure:** `opencode` interactive binding → `ErrUnsupportedAgentBackend`
  (unchanged).
- **Error:** unknown/typo backend (`"Codex"`) → fails closed (exact-match, no
  silent fallback) — unchanged.
- **Edge case:** the **batch** `AgentProviderFor` codex arm still returns the exec
  `Provider` (the swap did not leak into the batch path); chat-kind interactive
  bindings still route to the chat selector.

## Regression guard

- All existing `adapter/codex` exec tests pass unchanged (`provider_test.go`,
  `stream_adapter_test.go`, `args_test.go`, `parse_test.go`, `env_test.go`,
  `version_test.go`, `readiness_test.go`).
- `make build` / `go build ./...` succeeds (the root compiles with the new
  construction).

## Manual real-repro verification (spec §5 — acceptance, not a unit test)

Against real codex (codex-cli 0.139, logged in): re-run feature-pipeline-v2's
`brainstorm-spec` node (profile `interviewer-codex`) on "remove the Queue Status
section from Settings." Expect: codex asks clarifiers + waits, may inspect
files/CLIs, does **not** edit `Settings.tsx` or build, writes + commits the spec
under `docs/specs/` only on approval, and a human reply resumes the same session.
Confirm an autonomous codex `agent_call` node still edits + builds. Capture the
trace; any mid-interview non-`docs/` write attempt shows as a **denied** approval
step with no human notification.

## Verification

- `go test ./...` green; `make build` clean.
- A short operating-posture note appended to the codex backend docs.
