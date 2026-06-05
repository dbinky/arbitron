# RBAC Design — Phase 2: Workflow Visibility (Three Levels)

> Implementation design for `docs/specs/rbac-spec.md`. Replaces today's binary
> private/shared model with private / department / public, represented as
> **metadata on a single shared tree** (the chosen architecture). Introduces the
> audit log used here and in Phase 3.

## Purpose

Let a workflow be private (owner only), department-visible (members of one
department), or public (everyone), and gate all read paths accordingly — without
physically partitioning the shared tree. Member self-service publishes to their
own department; promotion to public requires a lead or admin.

## Scope

In scope:
- `published_workflows` schema extension: `visibility` + `department`.
- Publish/visibility-change service methods with role gating.
- **Read-path gating** for `List`, `Load`, `LoadVisible` (trigger path), `Copy`.
- Data migration: existing shared rows → `public`.
- **Audit log** infrastructure (table, port, service) + visibility-change and
  admin-override events.

Out of scope: instances (Phase 3), dashboard (Phase 4).

## Data Model

The physical layout is unchanged: published workflow content remains a single
copy under `shared/<name>`. Visibility lives entirely in the publication record.

Migration `010_workflow_visibility`:
```sql
ALTER TABLE published_workflows ADD COLUMN visibility TEXT NOT NULL DEFAULT 'public'
    CHECK (visibility IN ('department','public'));
ALTER TABLE published_workflows ADD COLUMN department TEXT;  -- NULL for public
CREATE INDEX idx_published_visibility ON published_workflows(visibility, department);
```
The `DEFAULT 'public'` is the data migration: every pre-existing shared row becomes
public, preserving today's "everyone can see it" behavior (spec §Migration). A
`department` row must be non-NULL when `visibility='department'` (enforced in the
domain service; SQLite CHECK across two columns is added where portable).

`private` is not a row at all — a workflow with no publication record is private to
its owner, exactly as today.

## Domain Changes (domain/workflow)

### Visibility type
```
type Visibility string
const ( VisibilityPrivate = "private"; VisibilityDepartment = "department"; VisibilityPublic = "public" )
```
`PublishedStore`/`PublisherLookup` gain visibility+department:
- `PublisherOf` → also returns `Visibility` and `Department` (or a richer
  `PublicationOf(name) (Publication, bool, error)`).
- `Upsert(ctx, name, publisher, sourceName, visibility, department)`.

### Publish & visibility changes (service.go)
- `Publish(ctx, privateName, publishedName, visibility, department)`:
  - `visibility=department`: caller must be a **member of `department`**
    (`id.InDepartment(department)`); `department` required.
  - `visibility=public`: caller must be a **lead (of any of their depts) or admin**
    (`len(id.LedDepartments)>0 || id.IsAdmin()`).
  - Publisher-only-overwrite invariant and the copy-then-record ordering/rollback
    semantics are preserved from the current implementation.
- `SetVisibility(ctx, publishedName, visibility, department)`: changes an existing
  publication's level (e.g. lead promotes department→public, or demotes). Same role
  gates as Publish. This is the "column flip" the metadata model makes cheap; no
  tree copy.
- Every successful Publish/SetVisibility writes an **audit event** (below).

### Read-path gating (the core of this phase)
Today shared reads are universally open. Introduce a single predicate consulted by
every read that can surface a shared workflow:

```
func canSeePublication(id identity.Identity, p Publication) bool {
    return id.IsAdmin() ||
        p.Publisher == id.UserID ||
        p.Visibility == VisibilityPublic ||
        (p.Visibility == VisibilityDepartment && id.InDepartment(p.Department))
}
```

Apply it in:
- `List` — filter shared entries through `canSeePublication`; private entries
  unchanged. Returned `WorkflowSummary` carries `Visibility` and `Department`.
- `Load` / `LoadVisible` — when resolution falls through to the shared scope,
  require `canSeePublication`; otherwise return `ErrNotFound` (do **not** reveal
  existence of a department workflow to outsiders).
- `Copy` — a user may only copy a shared workflow they can see.
- The engine **trigger** path (`LoadVisible` in domain/engine/engine.go) — a user
  may only trigger a workflow they can see; this is what Phase 3 then stamps.

A workflow service constructed without a `PublishedStore` (read-only/local) treats
all shared entries as public — preserving local-mode openness.

## Audit Log (shared infrastructure, introduced here)

New domain port (domain/audit) + SQLite adapter, kept deliberately small:
```sql
-- migration 011_audit_log
CREATE TABLE audit_events (
    id          TEXT PRIMARY KEY,
    occurred_at TEXT NOT NULL,
    actor_id    TEXT NOT NULL,
    action      TEXT NOT NULL,   -- e.g. workflow.publish, workflow.visibility_change, admin.override
    target      TEXT NOT NULL,   -- workflow name / instance id
    detail      TEXT NOT NULL DEFAULT '{}'  -- JSON: {from,to,department,...}
);
CREATE INDEX idx_audit_occurred ON audit_events(occurred_at);
CREATE INDEX idx_audit_actor ON audit_events(actor_id);
```
`audit.Service.Record(ctx, action, target, detail)` reads the actor from context.
Recording failures must not fail the underlying operation (best-effort log + error
surfaced to observability), so audit is an enhancement, not a new failure mode on
the write path. Phase 3 reuses this for lead interventions.

Events emitted in Phase 2: `workflow.publish`, `workflow.visibility_change`, and
`admin.override` (when an admin reads/acts outside normal visibility — recorded at
the gate when `id.IsAdmin()` is the sole reason access was granted).

## Local Mode

Single default group, admin identity ⇒ `canSeePublication` always true; publish is
unrestricted; audit still records (harmless, single user).

## Postgres-Readiness

Visibility filtering is index-backed (`idx_published_visibility`); audit table uses
portable SQL; all access flows through the workflow service ports.

## Testing (strict TDD)

- **canSeePublication:** admin sees all; publisher sees own; public visible to all;
  department visible only to members; outsider denied.
- **Publish gating:** member→own department ok; member→other department rejected;
  member→public rejected; lead/admin→public ok; missing department on
  `visibility=department` rejected.
- **SetVisibility:** lead promotes department→public; member cannot; demote path;
  audit event emitted with from/to.
- **Read gating:** `Load`/`LoadVisible` returns ErrNotFound (not Forbidden-leak)
  for an unseeable department workflow; `Copy` denied for unseeable; trigger denied.
- **List:** mixed private/department/public filtered correctly per caller.
- **Migration:** pre-existing shared rows read back as `public`.
- **Audit:** record-failure does not fail publish; events carry actor from context.

## Dependencies & Order

Depends on **Phase 1** (`identity.Identity.Departments`, `LedDepartments`,
`IsAdmin`). Must precede **Phase 3**, which reuses the audit log and assumes the
trigger path is visibility-gated.
