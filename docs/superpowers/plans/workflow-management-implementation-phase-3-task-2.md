# Workflow Management — Phase 3, Task 2: Delete (guarded) + Unpublish

**Goal:** Add guarded deletion (including shared workflows) and unpublish, both
admin-gated, preserving run history.

**Architecture:** `Delete` refuses while non-terminal instances carry the workflow's id
(`ErrHasRunningInstances`), then removes folder + registry row + any publication; past
instances/audit rows are untouched. `Unpublish` removes the publication record + shared
folder, leaving any private copy. Both relax the shared-read-only rule for admins.

**Tech Stack:** Go, hexagonal, SQLite, uuid, testify. Depends on Phase 3 Task 1.

---

## Files
- Modify: `domain/workflow/errors.go` (add `ErrHasRunningInstances`)
- Modify: `domain/workflow/service.go` (`Delete`, `Unpublish`)
- Modify: `domain/instance` store/port (query: count non-terminal instances by `workflow_id`)
- Test:   `domain/workflow/delete_test.go`, `domain/workflow/unpublish_test.go`

## Steps

- [ ] **Step 1 — Failing test: delete refused while running.** Seed a non-terminal
  instance with idA; admin `Delete("alpha")` → `ErrHasRunningInstances`; folder + registry
  still present. FAIL first.

- [ ] **Step 2 — Running-instance guard.** Add an instance-store query
  `CountActiveByWorkflowID(id)` (states not in {completed,failed,cancelled}); `Delete`
  calls it after `requireAdmin`+resolve. PASS Step 1.

- [ ] **Step 3 — Failing test: delete removes everything, history preserved.** With no
  active instances, admin `Delete("alpha")`: folder gone, registry row gone, any
  publication gone; a previously-completed instance row for idA is STILL queryable
  (history preserved). FAIL → implement folder `Delete` + `registry.Delete(id)` +
  publication delete (ordered: publication, then folder, then registry row last). PASS.

- [ ] **Step 4 — Edge: delete a shared/published workflow.** Deleting a published
  workflow also clears its `published_workflows` row + `shared/<name>` folder. Test it
  (this is the case that's impossible today).

- [ ] **Step 5 — Failing test + impl: Unpublish.** Publish "alpha" (shared); admin
  `Unpublish("alpha")` removes the publication row + shared folder; if a private copy
  existed it remains; `ResolveID` still works for the private copy. Non-admin →
  `ErrForbidden`. PASS.

- [ ] **Step 6 — Error/edge tests:** delete a non-existent workflow → `ErrNotFound`;
  unpublish a workflow that isn't published → `ErrNotFound` (or a clear no-op error).

- [ ] **Step 7 — Gate + commit.** `go test ./...` green. `git add -A && git commit -m
  "feat(workflow): guarded delete (incl. shared) + unpublish, history preserved"`
