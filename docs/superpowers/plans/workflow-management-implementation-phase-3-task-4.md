# Workflow Management — Phase 3, Task 4: Audit trail extension + MCP surface

**Goal:** Make every lifecycle operation emit an audit event, add a queryable
target-scoped audit retrieval, and expose all operations + the audit query through MCP.

**Architecture:** Reuse the existing `audit.Service.Record` (best-effort, never fails
the op) and `audit_events` table; add new action constants + a `ListByTarget` query.
Add MCP tools mirroring `adapter/mcp/tools_workflow.go` patterns; the admin gate lives
in the service, MCP maps the sentinels.

**Tech Stack:** Go, hexagonal, SQLite, mark3labs mcp-go, testify. Depends on Phase 3
Tasks 1–3.

---

## Files
- Modify: `domain/audit/audit.go` (action constants: `workflow.rename/.delete/.unpublish/.duplicate/.archive/.unarchive/.transfer_ownership/.edit_shared`)
- Modify: `domain/audit/service.go` + `adapter/sqlite/audit_repo.go` + `queries/audit.sql` (`ListByTarget`)
- Modify: `domain/workflow/service.go` (emit `audit.Record` at the end of each op)
- Modify: `adapter/mcp/tools_workflow.go` (register new tools + handlers)
- Test:   `domain/workflow/audit_emit_test.go`, `adapter/mcp/tools_workflow_test.go`

## Steps

- [ ] **Step 1 — Audit emits (test-first).** Each op (rename/delete/unpublish/duplicate/
  archive/unarchive/transfer/edit-shared), on success, calls `audit.Record(ctx, action,
  target=name, detail)` exactly once with the right action + before/after detail. Use a
  fake audit recorder in the service tests. Implement the emits. PASS.

- [ ] **Step 2 — `ListByTarget` (test-first).** Add `ListByTarget(ctx, target)` to the
  audit store + service + a `ListAuditEventsByTarget` query; returns events for a
  workflow name, newest first. PASS.

- [ ] **Step 3 — MCP tools (test-first, per tool).** Register and handle:
  `workflow_rename`, `workflow_delete` (now also shared), `workflow_unpublish`,
  `workflow_duplicate`, `workflow_archive`, `workflow_unarchive`,
  `workflow_transfer_ownership`, and `workflow_audit` (query by target). Each handler:
  validate args + `safeWorkflowName`, call the admin-gated service method, map sentinels
  (`ErrForbidden`→error, `ErrHasRunningInstances`, `ErrArchived`, `ErrAlreadyExists`,
  `ErrNotFound`) to `gomcp.NewToolResultError`. Mirror existing handler tests in
  `tools_workflow_test.go` (fake service/registry). PASS.

- [ ] **Step 4 — Tool registration test.** Assert all new tools are registered by
  `RegisterWorkflowTools` with correct names + schemas (one test enumerating them).

- [ ] **Step 5 — Edge/error tests:** non-admin caller → MCP returns a forbidden error
  for each new tool; `workflow_audit` for an unknown workflow returns an empty list (not
  an error); rename via MCP rejects a taken name.

- [ ] **Step 6 — Full gate + commit.** `go test ./...` and `go vet ./...` green.
  `git add -A && git commit -m "feat(workflow): audit emits + ListByTarget + MCP lifecycle tools"`
