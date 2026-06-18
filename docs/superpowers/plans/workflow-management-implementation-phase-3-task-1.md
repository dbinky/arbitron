# Workflow Management — Phase 3, Task 1: Admin gate + Rename

**Goal:** Add the admin authorization gate shared by all lifecycle ops, and the first
operation — **Rename** — which moves the name-keyed folder, updates the registry name,
and refreshes the `workflow.json` label, with references staying safe via the id.

**Architecture:** New admin-gated methods on `WorkflowService` following the
`UserService` pattern (`identity.FromContext` → `!IsAdmin()` → `ErrForbidden`). Rename
coordinates a folder move + registry rename, mirroring `adapter/sqlite/department_renamer.go`.

**Tech Stack:** Go, hexagonal, SQLite, uuid, testify. Depends on Phases 1–2.

---

## Files
- Modify: `domain/workflow/errors.go` (add `ErrForbidden` alias if not already; reuse `ErrAlreadyExists`)
- Modify: `domain/workflow/service.go` (add `requireAdmin`, `Rename`)
- Test:   `domain/workflow/rename_test.go`

## Steps

- [ ] **Step 1 — Failing test: non-admin is forbidden.** With a non-admin identity in
  ctx, `service.Rename(ctx,"alpha","beta")` returns `ErrForbidden`; with an admin (or
  local-mode) identity it proceeds. FAIL first.

- [ ] **Step 2 — `requireAdmin(ctx)` helper.** `id := identity.FromContext(ctx); if id ==
  nil || !id.IsAdmin() { return ErrForbidden }`. Mirror `UserService` sentinels. Make
  the non-admin half of Step 1 pass.

- [ ] **Step 3 — Failing test: rename moves folder + updates registry, references intact.**
  Admin renames "alpha"→"beta": `Load("beta")` succeeds, `Load("alpha")` is not-found,
  the registry resolves "beta"→idA (same id as before), and an instance/schedule
  carrying idA still resolves. FAIL first.

- [ ] **Step 4 — Implement `Rename(ctx, name, newName)`:**
  1. `requireAdmin`.
  2. Resolve `id := registry.ResolveID(name)` (else `ErrNotFound`).
  3. Uniqueness: if `newName` resolves to an existing id → `ErrAlreadyExists`.
  4. Move the folder (`CopyTree(old→new)` then `Delete(old)`) within the same scope —
     order: copy, then registry `Rename(id,newName)`, then delete old folder; on any
     step failure, roll back the copy (delete new) and return the error (no half-rename).
  5. Update the `id`/label inside the new `workflow.json` if it stores the name.
  Make Step 3 pass.

- [ ] **Step 5 — Edge: rename a *shared* workflow.** Renaming a shared workflow moves
  `shared/<old>`→`shared/<new>` and keeps the publication (it's linked by `workflow_id`,
  not name). Test it.

- [ ] **Step 6 — Failure test: target name taken → `ErrAlreadyExists`, nothing moved.**
  Assert the old folder still exists and the registry is unchanged.

- [ ] **Step 7 — Gate + commit.** `go test ./...` green. `git add -A && git commit -m
  "feat(workflow): admin gate + rename (folder move + registry, refs safe via id)"`
