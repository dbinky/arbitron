# Workflow Management — Phase 3, Task 3: Duplicate + Edit-shared + Archive + Transfer ownership

**Goal:** Add the remaining four admin-gated operations.

**Architecture:** All gated via `requireAdmin`. Duplicate allocates a new id + unique
name and copies the tree into the requester's private space. Edit-shared opens an
admin-only write path to the shared scope. Archive toggles `registry.archived` and the
trigger path refuses archived workflows. Transfer reassigns `registry.owner_id` and
moves a private workflow between user trees.

**Tech Stack:** Go, hexagonal, SQLite, uuid, testify. Depends on Phase 3 Task 1.

---

## Files
- Modify: `domain/workflow/errors.go` (`ErrArchived`)
- Modify: `domain/workflow/service.go` (`Duplicate`, `Archive`, `Unarchive`, `TransferOwnership`; admin-shared write path)
- Modify: `domain/engine/engine.go` (trigger refuses archived: check `registry` / loaded entry)
- Test:   `domain/workflow/{duplicate,archive,transfer,edit_shared}_test.go`, engine archived-trigger test

## Steps

- [ ] **Step 1 — Duplicate (test-first).** Admin `Duplicate("alpha","alpha-copy")`:
  new workflow exists at the requester's private scope with a NEW id and the definition
  copied (`CopyTree`); registry has both names; duplicate to an existing name →
  `ErrAlreadyExists`; non-admin → `ErrForbidden`. Implement. PASS.

- [ ] **Step 2 — Edit shared in place (test-first).** Admin `Update`/`WriteFile` against
  a shared workflow succeeds (today → `ErrSharedReadOnly`); non-admin still gets
  `ErrSharedReadOnly`. Add an admin-shared branch in `translateWrite`/the write methods
  guarded by `requireAdmin`. Assert an in-flight instance (snapshot) is unaffected and
  future triggers see the edit. PASS.

- [ ] **Step 3 — Archive/Unarchive (test-first).** Admin `Archive("alpha")` sets
  `registry.archived=1`; `Unarchive` clears it; non-admin → `ErrForbidden`. Implement
  via `registry.SetArchived`. PASS.

- [ ] **Step 4 — Trigger refuses archived (test-first).** Triggering an archived
  workflow → `ErrArchived` (engine resolves the entry / checks archived before building
  the instance); an archived workflow is still listable and editable. Implement the
  engine check. PASS.

- [ ] **Step 5 — Transfer ownership (test-first).** Admin
  `TransferOwnership("alpha", newOwnerID)` updates `registry.owner_id`; for a private
  workflow the folder moves from `users/<old>/alpha`→`users/<new>/alpha` (`CopyTree`+
  `Delete`, ordered with rollback); non-admin → `ErrForbidden`. PASS.

- [ ] **Step 6 — Edge tests:** transfer of a shared workflow updates publisher/owner
  without a private folder move; duplicate of a shared workflow lands in the caller's
  private space; archive is idempotent.

- [ ] **Step 7 — Gate + commit.** `go test ./...` green. `git add -A && git commit -m
  "feat(workflow): duplicate, edit-shared, archive/unarchive, transfer ownership"`
