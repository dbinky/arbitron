# Final Org-Readiness Gaps — Product Specification

## Summary

This spec closes the next slice of org-readiness work for the shared Arbitron
deployment. Three capabilities were explicitly deferred from the original
org-readiness effort (`docs/specs/org-readiness-spec.md`) so the first shared
instance could ship; they are captured as gaps in `docs/specs/final-org-gaps.md`.
This effort builds three of them:

1. **Schedule "run now"** — fire a defined schedule immediately, on demand.
2. **SSO group/department mapping to RBAC** — scope each authenticated user to
   the department(s) their SSO group claim names, instead of the see-everything
   default.
3. **Schedule & run visibility in the dashboard** — surface schedules and their
   run history in the dashboard, read-only, while management stays on the API.

Each capability builds on infrastructure already shipped (Phase 3 scheduling,
Phase 6 RBAC, and the edge-auth identity model). The spec is deliberately
outcome-focused; implementation design lives in companion design documents.

## Background & motivation

The shared deployment ships today with: schedules manageable only over REST and
MCP and only firing on their cron cadence; edge authentication that recognizes
every non-admin user as a member of a single default group that can see
everything; and no dashboard view of schedules or scheduled-run history. The
department/visibility (RBAC) model exists and is honored by manual and scheduled
triggers, but is not yet fed from the SSO edge's group claims.

This spec defines the product behavior that closes those three gaps. It does not
re-architect scheduling, authentication, or RBAC — each capability extends a
surface that already exists.

## Goals

- An authorized user can start a defined schedule on demand without waiting for
  its next cron tick.
- A non-admin user authenticated at the edge is scoped to the department(s)
  carried by their SSO group claim, seeing only their scope.
- Users can view schedules and scheduled/run-now run history from the dashboard.

## Non-goals (this effort)

- **Multi-instance / horizontal scale-out** (gap #3). The deployment remains a
  single instance on the embedded database. The coordination-safe scheduler and
  queue, leader election, and datastore migration that scale-out implies are out
  of scope and revisited only when single-instance capacity becomes the
  constraint.
- **Additional provider adapters** (gap #5). The existing provider families
  cover the deployment; no new adapter is added unless a concrete provider
  outside those families is required.
- **Dashboard-driven schedule management.** All schedule creation, editing,
  enabling/disabling, deletion, and run-now stay on REST + MCP. The dashboard
  surface added here is strictly read-only.
- **Group claims conferring administrator.** Administrators continue to be
  designated solely by the email allow-list. Group claims drive department
  scoping only.

---

## Capability A — Schedule "run now" / manual fire

### Outcome

An authorized user fires a defined schedule immediately, on demand, without
waiting for its next cron tick. The resulting run is indistinguishable from a
cron-fired run of the same schedule except for its trigger time.

### Requirements

- **On-demand fire of an existing schedule.** A "run now" action starts the
  workflow named by an existing schedule immediately, outside its cron cadence.
- **Reuses the schedule's configuration.** The run uses the schedule's
  configured variables and is attributed to the schedule's creator — carrying
  the creator's identity and department exactly as a cron-fired occurrence does.
- **Collision is caller-controlled, defaulting to fire-anyway.** By default,
  run-now starts a new run even if a previous run of that schedule is still
  active. The caller may opt into skip-when-busy; when that option is chosen and
  a prior run is active, the action does not start a run and reports that the
  occurrence was skipped.
- **Authorization matches scheduling.** A caller may run-now a schedule only if
  they are permitted to start its workflow — the same "can schedule" == "can
  trigger" gate used to create the schedule. Administrators can run-now any
  schedule.
- **Available on the existing management surfaces.** Run-now is offered through
  the same programmatic surfaces as the rest of schedule management — REST and
  the tool/automation (MCP) interface. It is not a dashboard control.
- **Run-now occurrences are attributed and traceable.** A run-now occurrence is
  recorded as originating from its schedule and creator, so it appears in run
  history alongside cron-fired occurrences (see Capability C).

### Acceptance criteria

- Calling run-now on an enabled schedule starts its workflow immediately with
  the schedule's variables, attributed to the schedule's creator and department.
- With the default collision behavior, run-now starts a new run even while a
  prior run of the same schedule is active.
- With skip-when-busy requested and a prior run active, run-now starts no run
  and reports the occurrence as skipped.
- A caller who may not start the schedule's workflow is denied; an administrator
  may run-now any schedule.
- Run-now is reachable over both REST and MCP and is absent from the dashboard
  as a control.
- A run-now occurrence is visible in run history as originating from its
  schedule.

---

## Capability B — SSO group / department mapping to RBAC

### Outcome

A non-admin user authenticated at the edge is placed into the department(s) the
SSO edge forwards for them and sees only their scope, driven by the group claim
in the edge-issued identity token. A user for whom no group information is
forwarded continues to fall back to the single default group that can see
everything.

### Requirements

- **The group claim is read at the edge.** When the edge-issued identity token
  carries the configured group claim, its value(s) are taken as the user's group
  membership and flow into Arbitron's existing department-resolution path. The
  group claim is read only from the same verified token that establishes
  identity.
- **Multiple groups map to multiple departments.** A token may carry more than
  one group; the user's effective scope is the union of the departments those
  groups resolve to.
- **Mapping with verbatim fallback.** The deployment may be configured with a
  map from group-claim value to department name (suppliable via
  environment/secrets), so opaque claim values (e.g. directory GUIDs) become
  friendly department names. A forwarded group with no mapping entry becomes a
  department named from the claim value verbatim (normalized). A user with no
  group claim at all falls back to the existing default-everything group.
- **Remap consistency.** When an operator adds or changes the mapping for a
  department that was previously created verbatim from a raw claim value, the
  resulting friendly department name is reflected consistently everywhere the
  department appears — across all dashboard views, existing schedules, and
  historical runs already attributed to that department. Remapping produces no
  orphaned or duplicated departments: runs and schedules stamped under the old
  verbatim value are seen under the new mapped name.
- **Scoping replaces see-everything for claimed users.** A non-admin user who
  carries one or more group claims is scoped to the resolved department(s) and
  no longer sees everything; only claimless users retain the default-everything
  fallback.
- **Administrators are unchanged.** Administrators remain designated solely by
  the email allow-list. A group claim never confers administrator privileges,
  and an administrator retains full visibility regardless of any group claim.
- **Configuration is headless.** Enabling group mapping requires only the group
  claim name, the optional value→department map, and the default-group fallback
  — all suppliable through environment/secrets, consistent with the rest of edge
  mode.

### Acceptance criteria

- A non-admin user whose verified token carries a mapped group sees only that
  mapped department's scope.
- A non-admin user whose token carries an unmapped group sees only the
  department named verbatim from that claim value.
- A user carrying multiple groups sees the union of the corresponding
  departments.
- A non-admin user whose token carries no group claim retains the
  default-everything group.
- After an operator adds a mapping for a department previously created verbatim,
  that department appears under its new friendly name across all views, existing
  schedules, and historical runs, with no duplicate or orphaned department.
- A user on the email admin list is an administrator with full visibility
  regardless of group claims; no group claim grants administrator.

---

## Capability C — Schedule & run visibility in the dashboard

### Outcome

Users can view schedules and scheduled/run-now run history from the dashboard,
while every schedule management action stays on REST + MCP. The dashboard
surface is strictly read-only.

### Requirements

- **Schedules are visible on the workflow surfaces.** A workflow's defined
  schedules are shown, read-only, where users view that workflow (the workflow
  catalog and the workflow review/detail views). Each schedule displays at least
  its cron expression, timezone, creator, enabled/disabled state, and last-fired
  time.
- **Scheduled and run-now runs are annotated in run history.** In the
  execution/queue run history, a run that originated from a schedule (by cron
  tick or run-now) is marked with its originating schedule and the creator /
  department it was attributed to, distinguishing it from manually triggered
  runs.
- **Read-only only.** The dashboard presents no control to create, edit, enable,
  disable, delete, or run-now a schedule. Those actions remain exclusively on
  REST + MCP.
- **Visibility respects the user's scope.** A user sees only the schedules and
  runs for workflows and departments within their own visibility scope, exactly
  as the rest of the dashboard already scopes what they can see (including the
  department scoping introduced in Capability B).

### Acceptance criteria

- A workflow's schedules appear, read-only, in the workflow catalog and review
  views with cron, timezone, creator, enabled state, and last-fired time.
- A scheduled or run-now run is shown in run history annotated with its
  originating schedule and attributed creator/department.
- The dashboard exposes no schedule create/edit/enable/disable/delete/run-now
  control.
- A user viewing schedules and runs sees only those within their own visibility
  scope.

---

## Success criteria (overall)

- An authorized user can fire any schedule they may trigger on demand, over REST
  or MCP, with the schedule's variables and creator attribution, choosing
  whether to skip when a run is already active.
- A non-admin user authenticated at the edge is scoped to the department(s)
  their SSO group claim resolves to — mapped or verbatim — while claimless users
  and administrators are unaffected, and remapping a verbatim department renames
  it consistently everywhere.
- Users can view schedules and scheduled/run-now run history from the dashboard,
  scoped to what they may see, with all schedule management remaining on the API.

## Open questions / future work

- **Multi-instance scale-out** (gap #3) — horizontal scaling and the datastore
  changes it implies remain deferred until single-instance capacity is the
  constraint.
- **Additional provider adapters** (gap #5) — added only on demand for a
  concrete provider outside the existing families.
