# Phase 1 / Task 1 — Backend vocabulary: `BackendCodex`

**Design:** `docs/superpowers/specs/codex-backend-design-phase-1.md`
**Depends on:** nothing (foundational — every other task references `BackendCodex`).

## Goal
Add `codex` to the recognized agent-backend vocabulary, the single source of
truth shared by the workflow validator and the adapter factory dispatch.

## Files
- `domain/workflow/workflow.go` (edit)
- `domain/workflow/workflow_test.go` (or the existing backend test file) (edit)

## TDD steps
1. **RED** — add table cases to the `IsValidAgentBackend` test:
   - `"codex"` → `true`
   - `"Codex"` / `" codex "` / `"codex "` → `false` (exact-match lowercase,
     fail-closed near-miss rejection, consistent with existing cases).
2. **GREEN** — in `domain/workflow/workflow.go`:
   - Add `BackendCodex = "codex"` to the backend const block (with a doc
     comment mirroring `BackendOpencode`).
   - Add `BackendCodex` to the `IsValidAgentBackend` switch's valid case.
3. **REFACTOR** — none expected; keep the comment block listing the allowed set
   accurate (now `"" | "claude-code" | "opencode" | "codex"`). Update the
   `NodeConfig.Backend` field comment likewise.

## Acceptance
- `go test ./domain/workflow/...` passes.
- A workflow JSON with `"backend": "codex"` on an agent node passes validation;
  `"backend": "Codex"` is rejected at load.
