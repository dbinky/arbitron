# Plan — Phase 1, Task 1: `mcpSession` transport port + test fake

**Slug:** `codex-interactive`
**Design:** `docs/superpowers/specs/codex-interactive-design-phase-1.md`
**Depends on:** nothing (foundation task — 1.2, 2.1, 2.2 all build on this)
**Package:** `adapter/codex`

## Goal

Define the narrow, codex-shaped transport seam the interactive provider depends
on, plus the hand-driven test fake that substrates every later test. No real
codex, no mcp-go usage yet (that's Task 1.2). Pure types + a fake + the fake's
contract tests.

## Files

- `adapter/codex/mcp_session.go` (new) — the port + value types.
- `adapter/codex/fake_mcp_session_test.go` (new) — the test fake.
- `adapter/codex/mcp_session_test.go` (new) — fake-contract + type tests.

## Types (mcp_session.go)

```
type mcpSession interface {
    Start(ctx context.Context, cfg turnConfig) (turnStream, error)
}

type turnConfig struct {
    Model    string
    Prompt   string
    WorkDir  string
    ResumeID string        // codex conversation id; "" = new conversation
    Approver approvalFunc
    Effort   string
}

type turnStream interface {
    Next(ctx context.Context) (turnEvent, error)
}

type turnEventKind int
const (
    turnToken  turnEventKind = iota // assistant-text increment
    turnDone                        // terminal success
    turnFailed                      // terminal failure (turn.failed/error)
)

type turnEvent struct {
    Kind      turnEventKind
    Text      string // turnToken
    SessionID string // turnDone — codex conversation id
    TokensIn  int
    TokensOut int
    Err       error  // turnFailed
}

type approvalRequest struct {
    Command    []string // argv for a command-exec request (nil for a patch)
    WritePaths []string // target paths for a patch/write request (nil for exec)
    Reason     string   // codex's stated reason (for the trace)
}

type approvalDecision struct {
    Allow  bool
    Reason string // recorded to the trace
}

type approvalFunc func(approvalRequest) approvalDecision
```

Doc-comment every exported-to-package type with the same fail-closed / no-API-key
framing the rest of `adapter/codex` uses. The interface references **no**
`domain/engine` type (hexagonal: it is consumed by the provider, which alone
imports `engine`).

## Fake (fake_mcp_session_test.go)

A `fakeMCPSession` mirroring `fake_runner_test.go`'s philosophy:

- Holds a scripted `[]turnEvent` to replay via `Next`, plus an optional scripted
  list of `approvalRequest`s to hand to `cfg.Approver` at `Start` time (recording
  each returned `approvalDecision`).
- Records the `turnConfig` it received (`gotCfg`) for argument assertions
  (prompt, resumeID, model, workdir).
- Supports a `startErr` (Start fails) and a `blockOnCtx` mode (honours ctx
  cancellation, then ends) — mirroring the runner fake.
- `Next` returns scripted events in order, then `io.EOF`-style terminal handling
  per the event kinds.

## Tests (TDD — write first; all five categories)

- **Happy path:** a script `[token "Hi", token " there", done{SessionID:"c1"}]`
  replays in order via `Next`; `gotCfg` captures the prompt + workdir.
- **Success:** `ResumeID` set on `turnConfig` is recorded; an approval script
  with one request invokes `Approver` and records its decision.
- **Failure:** a `turnFailed{Err}` terminal event surfaces the error from `Next`.
- **Error:** `startErr` makes `Start` return `(nil, err)` with no stream.
- **Edge case:** empty script → `Next` reports terminal immediately; `blockOnCtx`
  + cancelled ctx → `Next` returns the context error; an approval request before
  any token is still delivered to `Approver`.

## Verification

- `go test ./adapter/codex/...` green.
- `go vet ./adapter/codex/...` clean.
- The interface compiles with a `var _ mcpSession = (*fakeMCPSession)(nil)`
  assertion in the test file.

## Out of scope

- Any mcp-go / real subprocess code (Task 1.2).
- The provider and the policy (Phase 2).
