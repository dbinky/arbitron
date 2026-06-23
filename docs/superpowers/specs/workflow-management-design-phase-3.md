# Workflow Management — Design Phase 3: Admin Lifecycle Operations (Gate · Audit · MCP)

## Scope
Add the seven admin-gated lifecycle operations, a queryable audit trail, and their
MCP surface, on top of the stable-identity foundation (Phase 1) and decoupled
references (Phase 2).

## Architecture & components
- **Admin gate.** Each new `WorkflowService` lifecycle method begins with
  `id := identity.FromContext(ctx); if !id.IsAdmin() { return ErrForbidden }` — the
  `UserService` pattern. Local mode passes trivially (fabricated admin identity).
- **Operations** (new service methods; cross-store folder moves follow the existing
  `adapter/sqlite/department_renamer.go` coordination precedent):
  - **Rename(name, newName):** registry uniqueness check → move the name-keyed folder
    (old→new) → update the registry name + the `workflow.json` label. References are
    safe via the id (Phase 2).
  - **Delete(name):** guard — if any non-terminal instance carries this `WorkflowID`
    → `ErrHasRunningInstances`; otherwise remove the folder + registry row + any
    publication. Past instances and audit rows are preserved.
  - **Unpublish(name):** remove the `published_workflows` row + the `shared/<name>`
    folder; the owner's private copy (if any) remains. Relaxes the shared scope only
    for this admin operation.
  - **Duplicate(name, newName):** allocate a new id, `CopyTree` into the requester's
    private space, register the new unique name.
  - **Edit shared in place:** add an admin-shared write path so `Update` / `WriteFile`
    / `SaveNodePositions` may target the shared scope for an admin, instead of being
    globally closed by `translateWrite`→`ErrSharedReadOnly`.
  - **Archive(name) / Unarchive(name):** toggle `registry.archived`; the trigger path
    refuses an archived workflow (`ErrArchived`); reversible.
  - **TransferOwnership(name, newOwnerID):** update `registry.owner_id`; for a private
    workflow, move the folder between `users/<old>/` and `users/<new>/` trees
    (`CopyTree`+`Delete`, coordinated).
- **Audit.** Add action constants (`workflow.rename`, `.delete`, `.unpublish`,
  `.duplicate`, `.archive`, `.unarchive`, `.transfer_ownership`, `.edit_shared`) in
  `domain/audit`; emit through the already-wired `audit.Service.Record(ctx, action,
  target=name, detail)`. Add a target-scoped query (`audit.Service.ListByTarget`)
  backed by a new `audit_events` query.
- **MCP** (`adapter/mcp/tools_workflow.go`). New tools: `workflow_rename`,
  `workflow_archive`, `workflow_unarchive`, `workflow_unpublish`,
  `workflow_duplicate`, `workflow_transfer_ownership`, and an audit query tool; allow
  `workflow_delete` to act on shared. Handlers validate + `safeWorkflowName`, call the
  admin-gated service, and map the sentinels (`ErrForbidden`,
  `ErrHasRunningInstances`, `ErrArchived`, `ErrAlreadyExists`). The gate lives in the
  service; MCP just surfaces errors (existing pattern).

## Data flow
MCP tool → handler (validate, `safeWorkflowName`) → admin-gated service method →
registry / folder / publication coordination → `audit.Record` → result. The trigger
path consults `registry.archived` to refuse archived workflows.

## Integration seams
- **Reuses:** `WorkflowService`, `WorkflowRepository` (`CopyTree`/`Delete`/`Update`),
  the Phase-1 registry, the audit subsystem, the `identity.IsAdmin` gate, and
  `department_renamer.go` as the cross-store-move template.
- **Targeted improvement:** the shared-read-only enforcement gains an explicit
  admin-shared write path rather than being globally hard-closed.

## Error handling
- Non-admin → `ErrForbidden`. Duplicate target name (rename/duplicate) →
  `ErrAlreadyExists`. Delete while running → `ErrHasRunningInstances`. Triggering an
  archived workflow → `ErrArchived`.
- **Non-transactional FS+SQLite boundary:** order each op so the registry/publication
  commit is last; on a folder-move failure, abort before mutating the registry (no
  half-rename). Document the ordering and rollback, mirroring `department_renamer`.
- Audit is best-effort and never fails the operation (existing `audit.Service`
  contract).

## Testing strategy — strict TDD (REQUIRED, all five categories per operation)
- **Rename:** happy (renamed, resolves) · success (schedule/instance references still
  resolve after rename) · failure (target name taken → `ErrAlreadyExists`, no folder
  moved) · error (non-admin → `ErrForbidden`) · edge (renaming a shared workflow moves
  the shared folder and keeps the publication via id).
- **Delete:** happy (deleted) · success (history still queryable) · failure (running
  instance → `ErrHasRunningInstances`) · error (non-admin) · edge (deleting a
  published workflow also clears its publication).
- **Archive:** happy (archived) · success (unarchive restores) · failure (trigger →
  `ErrArchived`) · error (non-admin) · edge (archived workflow still listable/editable,
  just not triggerable).
- **Unpublish / Duplicate / TransferOwnership / EditShared:** each covered across the
  same five categories.
- **Audit:** each op emits exactly one event with correct actor/action/target;
  `ListByTarget` and the MCP audit query return them.

Test gate: `go test ./...`.
