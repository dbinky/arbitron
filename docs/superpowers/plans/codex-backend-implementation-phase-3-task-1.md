# Phase 3 / Task 1 — Codex readiness check

**Design:** `docs/superpowers/specs/codex-backend-design-phase-3.md`
**Depends on:** Phase 1 Task 2 (`adapter/codex` constants + runner usage).

## Goal
A backend-scoped readiness check reporting `ready` / `not-installed` /
`not-logged-in`, surfaced through the existing system/health path. NOT a
connection `ProbeSelector` entry (codex has no connection/key).

## Files
- `adapter/codex/readiness.go` + `readiness_test.go`
- the system/health aggregator that already reports backend status (locate the
  `system_config` / health-report assembler in `app/`/`adapter/`) (edit) +
  its test.

## Contracts
- `type ReadinessState string` with `Ready`, `NotInstalled`, `NotLoggedIn`.
- `Readiness(ctx, Config) (ReadinessState, error)` — subprocess calls go through
  the shared `claudecli.CommandRunner` fake for testability. A clean
  "not ready" host returns a STATE, not an error; only genuine probe faults
  return an error.

## TDD steps
1. **RED→GREEN (state matrix):**
   - binary resolves + `codex --version` ok + valid login → `Ready`.
   - binary not found → `NotInstalled`.
   - version ok + no/invalid `~/.codex/auth.json` (or `codex login status`
     negative) → `NotLoggedIn`.
2. **error handling:** runner fault (non-login failure) → typed error, distinct
   from the three states.
3. **security:** the check sets no `OPENAI_API_KEY`, reads only login presence,
   never logs token contents.
4. **surface (RED→GREEN):** the health/system-config output includes a codex
   backend entry carrying the readiness state.

## Acceptance
- `go test ./adapter/codex/...` and the health/system test pass.
- Health output distinguishes ready / not-installed / not-logged-in for codex.
