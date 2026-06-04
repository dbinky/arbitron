# RBAC Design — Phase 3: Instance Visibility & Intervention

> Implementation design for `docs/specs/rbac-spec.md`. Carries a workflow's
> visibility onto the instances it produces, opens department runs to peer *view*
> while keeping *control* with owner/lead/admin, and stamps every instance with the
> department it ran under.

## Purpose

Make a run visible to the right audience (its department or everyone), so a
department can observe shared operational workflows (the triage queue), while
preserving clear control: only the owner, a department lead, or an admin may
intervene. Stamp the department at trigger time so reorganizations never rewrite
history and the Phase 4 dashboard can aggregate efficiently.

## Scope

In scope:
- Instance schema: `department` + `visibility`, stamped at trigger.
- Validated **per-trigger department override**.
- Rewrite of instance **list/read authorization** (peer view).
- **Split** of `canView` (read) from `canIntervene` (mutate).
- Lead-intervention audit events (reusing Phase 2 audit).

Out of scope: dashboard aggregation/UI (Phase 4).

## Data Model

Migration `012_instance_visibility`:
```sql
ALTER TABLE instances ADD COLUMN department TEXT;  -- NULL ⇒ default group / private
ALTER TABLE instances ADD COLUMN visibility TEXT NOT NULL DEFAULT 'private'
    CHECK (visibility IN ('private','department','public'));
-- Dashboard + list authorization index (also Postgres-ready):
CREATE INDEX idx_instances_dept_vis ON instances(department, visibility, state, created_at);
```
Existing rows default to `private` with NULL department, matching today's
owner-only behavior (spec §Migration).

`instance.Instance` gains `Department string` and `Visibility workflow.Visibility`
(snapshot fields, peers of `WorkflowSnapshot`). `instance.ListFilters` gains
`Departments []string` and `Visibilities []string` (service-set, never from
request input — same security posture as the existing service-set `OwnerID`).

## Trigger Stamping (domain/engine/engine.go)

In `Trigger`, after `LoadVisible` (now visibility-gated by Phase 2) resolves the
workflow and its publication:
1. **Resolve visibility/department for the run:**
   - If the workflow is **public** → `visibility=public`, `department` = the
     triggerer's stamping department (for dashboard attribution) or NULL.
   - If **department** → `visibility=department`, `department` = the publication's
     department.
   - If **private** → `visibility=private`, `department` = NULL.
2. **Per-trigger override:** the trigger API accepts an optional `department`. If
   present it must be one of the **triggering user's effective departments**
   (`id.InDepartment(dept)`); otherwise reject with a validation error. The
   override sets the stamped `department` (and, for a department-visible workflow,
   the run's audience) — it never lets a caller widen beyond their own departments.
   For non-interactive triggers (webhook/API token) the triggering user's persisted
   departments (Phase 1) supply both the default and the override allow-list.
3. Persist `Department` + `Visibility` on the instance alongside `OwnerID` and
   `WorkflowSnapshot`.

## Authorization Rewrite (domain/engine/authz.go)

Replace the single `canView`/`ownerConstraint` with two predicates plus a list
constraint.

### List constraint
`ListInstances` no longer forces `OwnerID = self` for non-admins. Instead it builds
a disjunctive visibility filter from the caller's identity:
- **admin** → unconstrained (nil filters), unchanged.
- **non-admin** → instances where `owner_id = self` **OR** `visibility = public`
  **OR** (`visibility = department` AND `department ∈ id.Departments`).

Expressed via `ListFilters{ OwnerID, Departments, IncludePublic }`, translated by
the sqlc query into the index-backed `WHERE` above. Missing identity → reveal
nothing (unchanged).

### Read gate — `canView` (extended)
```
canView(ctx, inst) = id.IsAdmin()
    || inst.OwnerID == id.UserID
    || inst.Visibility == public
    || (inst.Visibility == department && id.InDepartment(inst.Department))
```
Consumed by Get / GetTrace / GetVariables — peers can now read a department run.

### Mutate gate — `canIntervene` (new)
```
canIntervene(ctx, inst) = id.IsAdmin()
    || inst.OwnerID == id.UserID
    || id.LeadsDepartment(inst.Department)
```
Consumed by cancel / pause / resume / retry / rerun / set-priority. A department
**peer** (member, not owner, not lead) is denied → 403, even though they can view.
Each successful **lead** intervention on an instance they do not own records an
`instance.lead_intervention` audit event (Phase 2 audit service). Admin acting
outside ownership records `admin.override`.

## Adapter Surface

- HTTP `instance_handler` and MCP `tools_instance` gain an optional `department`
  trigger parameter; list endpoints need no new params (authz is service-derived).
  Intervention handlers map the new `canIntervene` denial to 403 (existing
  `ErrForbidden` mapping).
- Read endpoints surface `department` + `visibility` on instance summaries/detail
  so the UI can label peer-visible runs.

## Local Mode

Single admin identity ⇒ `canView`/`canIntervene` always true; every run stamped
into the default group; no behavioral change from today.

## Postgres-Readiness

The disjunctive list filter is backed by `idx_instances_dept_vis`; the SQL is a
standard `WHERE … OR …` with an `IN (…)` department set — portable to Postgres.
Department stamping (vs. live re-derivation) keeps the hot list query a single
indexed scan with no joins.

## Testing (strict TDD)

- **Trigger stamping:** public/department/private workflows stamp the right
  visibility+department; override within caller's departments accepted; override
  outside rejected; webhook/token trigger inherits persisted department.
- **List authz:** owner sees own private; peer sees department run; non-member does
  not; everyone sees public; admin sees all.
- **canView vs canIntervene:** peer can Get/Trace a department run but cancel/retry
  → 403; owner and lead can intervene; admin can intervene.
- **Audit:** lead intervention on a non-owned run logs `instance.lead_intervention`;
  admin override logs `admin.override`.
- **Migration:** pre-existing instances read back private/owner-only.

## Dependencies & Order

Depends on **Phase 1** (identity departments/lead) and **Phase 2** (visibility-gated
trigger path + audit log). Precedes **Phase 4**, which reads the stamped
`department`/`visibility` columns.
