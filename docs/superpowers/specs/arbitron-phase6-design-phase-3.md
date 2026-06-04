# Phase 6 Implementation Design — Phase 3: Workspaces & Publishing

## Overview

This phase introduces the private/shared workflow model. Today `WorkflowRepo` stores every workflow flat under one `basePath` with no owner dimension. This phase restructures storage into per-user private workspaces plus one shared directory, threads a storage **scope** through the `WorkflowRepository` port, and implements publish / copy / run-shared-directly. It depends on Phase 1's context identity (to resolve scope) but not on Phase 2.

Folders remain the unit of storage and distribution (publish copies a folder), preserving the future `.arb` packaging path.

---

## 1. On-Disk Layout

```
basePath/
  users/<userID>/<workflowName>/workflow.json   # private workspaces
  shared/<workflowName>/workflow.json           # published, read-only via the API
```

A one-time **startup migration** (in `cmd/arbitron/main.go`, guarded by a marker file) moves any pre-existing top-level workflow folders into `users/<default-user>/`. This makes the local default user's existing store their private workspace with zero user action and no data loss.

---

## 2. Scope-Aware `WorkflowRepository` Port

Introduce a `Scope` value in `domain/workflow`:

```
type ScopeKind int  // Private, Shared
type Scope struct { Kind ScopeKind; OwnerID uuid.UUID }  // OwnerID ignored for Shared
```

The port methods gain a `Scope` argument:

```
Load(ctx, scope Scope, name string) (*Workflow, error)
List(ctx, scope Scope) ([]WorkflowSummary, error)
Save(ctx, scope Scope, *Workflow) error
Delete(ctx, scope Scope, name string) error
// file operations (read/write/list/delete) likewise take Scope
Exists(ctx, scope Scope, name string) (bool, error)
```

### Adapter

`WorkflowRepo.resolveWorkflowDir` becomes scope-aware: `Private` → `users/<ownerID>/<name>`, `Shared` → `shared/<name>`. The existing path-traversal guard (clean + prefix check) is retained and now also asserts the resolved path stays within the correct scope root. The per-user directory is created `0o700` (matching the workspace-manager security precedent).

---

## 3. Workflow Service Scope Resolution

A thin `WorkflowService` (or the existing engine workflow methods) resolves `Scope` from the context identity rather than callers passing it:

- **Create / edit / delete** → always `Private{OwnerID: caller}`. Shared workflows are never edited in place; an attempt returns a domain error.
- **List** → the caller's private workflows **plus** all shared workflows, tagged with their scope and (for shared) publisher, so the dashboard/MCP can distinguish them.
- **Load / read files** → try private (caller-owned) first, then shared; admins may additionally load any user's private workflow by explicit owner (used by Phase 4 admin views).

Authorization lives here, consistent with the Phase 1 decision (authz in domain services).

---

## 4. Publisher Tracking

To enforce "only the original publisher can overwrite," add a `published_workflows` table (migration `006`): `name` (PK, unique in shared), `publisher_id` (FK users), `published_at`, `source_name`. This is the authority for the unique-name and publisher-only-overwrite rules; the shared folder is the artifact.

---

## 5. Publish / Copy Operations

`WorkflowService.Publish(ctx, privateName, publishedName)`:
1. Load the caller's private workflow.
2. If `published_workflows[publishedName]` exists and `publisher_id != caller` → reject (name taken).
3. Copy the entire folder `users/<caller>/<privateName>` → `shared/<publishedName>` (atomic: write to a temp dir, then rename).
4. Upsert the `published_workflows` row (caller as publisher).
Re-publishing by the same publisher overwrites the shared folder but, because instances snapshot their definition at trigger time, **running and queued instances are unaffected** — no special handling needed.

`WorkflowService.Copy(ctx, sharedName, newPrivateName)`:
1. Reject if the caller already has a private workflow `newPrivateName`.
2. Copy `shared/<sharedName>` → `users/<caller>/<newPrivateName>`. The copy is fully independent — no back-link to the shared original.

---

## 6. Run Shared Directly

Triggering resolves the workflow by name across the caller's visible scopes (private first, then shared). When a shared workflow is triggered, the engine snapshots the shared definition into the instance exactly as it already does — the instance's `ownerID` is the **triggering** user (so it shows up in *their* queue), independent of the publisher. No copy is required to run.

---

## 7. Surfaces

- REST: `POST /api/v1/workflows/{name}/publish`, `POST /api/v1/workflows/{name}/copy`, and the existing CRUD/file/trigger routes now operate scope-aware against the caller's private space (plus shared for reads).
- MCP: `workflow_publish`, `workflow_copy`; existing workflow tools become scope-aware via the same service.
- Listing distinguishes `private` vs `shared` entries and surfaces the publisher for shared ones.

---

## 8. Testing Strategy

Each unit covers happy path, validation, error/failure, boundary, and isolation/security:

- **Scope path resolution:** private vs shared resolve to the right roots; traversal (`../`) and cross-scope escapes rejected; per-user dir is `0o700`.
- **Startup migration:** pre-existing flat workflows land under `users/<default-user>/`; marker prevents re-running; empty store is a no-op.
- **Publish:** copies the full folder; second publish of the same name by a different user rejected; re-publish by the original publisher succeeds and a running instance keeps its snapshot.
- **Copy:** produces an independent private workflow; editing the copy doesn't touch the shared original; duplicate private name rejected.
- **Scope resolution from identity:** create/edit/delete always private; editing a shared workflow rejected; list returns own-private + all-shared, correctly tagged.
- **Run shared directly:** triggering a shared workflow creates an instance owned by the triggering user with a correct snapshot; no private copy is created.
