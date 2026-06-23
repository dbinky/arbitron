# Workflow Management — Design Phase 1: Stable Identity & Global Uniqueness Foundation

## Scope
Introduce a stable per-workflow identity (UUID) and a global-uniqueness-enforcing
name↔id registry. Existing name-addressed behavior is fully preserved. This phase
does NOT change how instances/schedules reference workflows (Phase 2) or add admin
lifecycle operations (Phase 3).

## Architecture & components
- **Domain identity.** Add an `ID` (UUID) to the `Workflow` domain type
  (`domain/workflow/workflow.go`) and persist it inside `workflow.json`. The name
  remains the human-facing label; the UUID is the durable identity.
- **Registry (new port + adapter).** A `WorkflowRegistry` port in `domain/workflow`
  with a SQLite adapter and a migration creating `workflow_identity
  (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, scope TEXT, owner_id TEXT,
  archived INTEGER NOT NULL DEFAULT 0, created_at …)`. The `UNIQUE(name)` constraint
  is the authoritative global-uniqueness rule; the row is the name↔id resolver. The
  filesystem definition store (`adapter/filesystem/workflow_repo.go`) is unchanged —
  still name-keyed folders.
- **Service wiring.** `WorkflowService` gains the registry dependency (wired at the
  composition root `cmd/arbitron/main.go`, mirroring the audit wiring). `create`/
  `publish` allocate an id and register the row; `update` keeps it in sync; a
  `resolveID(name)` / `resolveName(id)` helper backs internal lookups.
- **Backfill.** Legacy workflows have no id. On load, an id-less `workflow.json` is
  assigned a persisted id and registered (lazy migration); a one-time startup
  backfill scans both scopes and registers any unregistered workflow.

## Data flow
`create(name)` → allocate id → write name-keyed folder → register `(id,name,scope,
owner)` (reject on UNIQUE(name) violation). `load(name)` → read folder → ensure id
present (backfill) → return `Workflow{ID,Name,…}`. Name/id resolution always goes
through the registry.

## Integration seams
- `WorkflowRepository` port stays name-keyed CRUD; the registry is a **new sibling
  port** the service consults. No MCP/HTTP signature changes — callers stay
  name-addressed and resolution is internal.
- The registry is authoritative for **uniqueness + identity**; the folder remains
  authoritative for the **definition body**.

## Error handling
- Duplicate name on create/publish → `ErrAlreadyExists` (existing sentinel; existing
  MCP mapping).
- Legacy id-less workflow → backfilled, never errors.
- Registry/folder divergence (folder without a row, or vice versa) → reconciled by
  the startup backfill; uniqueness defers to the registry, definition to the folder.

## Testing strategy — strict TDD (REQUIRED, all five categories)
- **Happy-path:** create registers an id + unique name; load returns the id.
- **Success:** `resolveID(name)` / `resolveName(id)` round-trip; an existing workflow
  keeps a stable id across reloads.
- **Failure:** creating/publishing a name already in the registry is rejected
  (`ErrAlreadyExists`); no folder is created.
- **Error:** a legacy id-less `workflow.json` is backfilled with a persisted id on
  load and by the startup backfill.
- **Edge:** concurrent create of the same name — exactly one wins via `UNIQUE(name)`,
  the other gets `ErrAlreadyExists`; names differing only by case/whitespace follow
  the existing `safeWorkflowName` normalization.

Test gate: `go test ./...` (part of the project gate).
