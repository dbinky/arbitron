# Phase 1 / Task 3 — Factory dispatch + composition wiring + acceptance

**Design:** `docs/superpowers/specs/codex-backend-design-phase-1.md`
**Depends on:** Task 1 (`BackendCodex`), Task 2 (`codex.New`).

## Goal
Route `backend: codex` agent bindings to the codex adapter through
`AgentProviderFor`, wire codex deps in the composition root, and prove an
`agent_call` mission runs on codex end-to-end (fake runner).

## Files
- `adapter/provider/factory.go` (edit) + `adapter/provider/factory_backend_test.go`
  (edit)
- `cmd/arbitron/main.go` (edit)
- `test/acceptance/scenario_03_both_agent_backends_test.go` (edit — extend to a
  third backend) or a new `scenario_NN_codex_backend_test.go`.

## TDD steps
1. **factory (RED→GREEN):**
   - add a `case workflow.BackendCodex:` arm in `AgentProviderFor` returning
     `codex.New(codex.Config{Binding: b, BinaryPath: f.deps.CodexBinaryPath,
     Runner: f.deps.Runner, Timeout: f.deps.CodexTimeout, MaxResponseBytes:
     f.deps.MaxResponseBytes})`.
   - extend `Deps` with `CodexBinaryPath string` and `CodexTimeout
     time.Duration`.
   - test: `AgentProviderFor` with `Backend: "codex"` returns a `*codex.Provider`
     (type-assert); `Backend: "Codex"`/unknown still returns `ErrUnknownBackend`.
   - keep the INVARIANT doc comment accurate (agent tier dispatches on backend;
     now `claude-code | opencode | codex`).
2. **composition (GREEN):** in `main.go`, populate `Deps.CodexBinaryPath` /
   `Deps.CodexTimeout` from config (reuse the opencode binary-path/timeout config
   pattern; add koanf keys if needed, e.g. `llm.codexbinarypath`,
   `llm.codextimeout`, defaulting empty/0).
3. **acceptance (RED→GREEN):** extend the both-agent-backends scenario to a third
   backend: an `agent_call` node with a codex profile runs a mission through the
   fake runner and produces the expected `AgentResult`/trace, alongside the
   existing claude-code and opencode arms.

## Acceptance
- `go test ./adapter/provider/... ./test/acceptance/...` passes.
- `make build` succeeds with codex wired into the factory.
- An `agent_call` node with `backend: codex` resolves and runs (fake runner) with
  no API key configured.
