# RBAC Design — Phase 4: Department Dashboard

> Implementation design for `docs/specs/rbac-spec.md`. Adds the tile-based
> **Dashboard** tab (before Queue) showing how a department's workflows are
> operating, backed by an aggregate stats endpoint over the now
> department-stamped instances.

## Purpose

Give each department an at-a-glance operational view: live queue health, throughput
over time, aggregate success/failure/duration, and a per-workflow breakdown —
scoped to department-visible and public workflows in the user's department(s), with
a union-by-default view and a per-department selector.

## Scope

In scope:
- Backend department-scoped aggregate stats query + endpoint.
- React Dashboard route/tab and tiles (reusing the `UsageStats` pattern).
- Department selector driven by `/me` (Phase 1).
- Live refresh via existing `useSystemWebSocket`.

Out of scope: per-department quotas/fairness (explicit non-goal, future spec).

## Backend

### Aggregation scope
Statistics aggregate instances that are **department-visible or public** within the
requested department(s) — i.e. `visibility IN ('department','public')` AND
`department ∈ requested-set` — regardless of which member triggered them. Members'
private runs are excluded (spec §Dashboard). The requested set is intersected with
the caller's effective departments server-side, so a caller can never request a
department they are not in (admins may request any).

### Endpoint
`GET /api/v1/dashboard/stats?departments=a,b&window=24h` →
```
{
  departments: ["a","b"],
  queue:      { running, queued, waiting, paused, failedNeedingAttention },
  throughput: [ { bucket: RFC3339, runs } ],          // hourly/daily buckets over window
  outcomes:   { totalRuns, successRate, failureRate, avgDurationSeconds, p95DurationSeconds },
  perWorkflow:[ { workflowName, runs, failureRate, avgDurationSeconds } ]  // ranked
}
```
- `departments` omitted ⇒ the caller's full effective set (union default).
- The handler clamps the requested departments to the caller's allowed set
  (admins exempt) before querying.

### Queries (adapter/sqlite/queries)
Four read-only aggregates, all filtered by `(department IN (…), visibility IN
('department','public'))` and a `created_at >= window` bound, served by the
`idx_instances_dept_vis` index from Phase 3:
- **queue health:** counts grouped by `state` (current, no window).
- **throughput:** count grouped by time bucket over the window.
- **outcomes:** total/success/failure counts and duration aggregates over the window.
- **perWorkflow:** the above grouped by `workflow_name`, ordered by runs/failure.

A small `domain/dashboard` service composes these behind a `StatsRepository` port
and applies the caller-department clamp; the HTTP handler is thin.

### Live mode
Local mode: single default group; the endpoint returns that group with no selector
implications. Org mode unchanged otherwise.

## Frontend (dashboard/, React + TS)

### Navigation
`NavBar.tsx`: add a **Dashboard** `NavLink` to route `/dashboard`, positioned
**before** the Queue link. `App.tsx`: add `<Route path="/dashboard"
element={<DashboardView/>} />`.

### Data
- New API client methods: `api.me()` → `/api/v1/me`; `api.dashboard.stats(depts,
  window)` → `/api/v1/dashboard/stats`. Typed interfaces added to `api/types.ts`.
- `DashboardView.tsx` loads `/me` once for the department list + role, then loads
  stats for the active selection.

### Layout
Reuse the `UsageStats` tile pattern/CSS (`.grid`, `.stat`, `.statValue`):
- **Queue health** tiles: Running, Queued/Backlog, Waiting/Paused, Failures needing
  attention (the last visually emphasized).
- **Throughput** tile: a compact runs-over-time sparkline/bar over the window.
- **Outcomes** tiles: Total runs, Success rate, Failure rate, Avg (and p95) duration.
- **Per-workflow breakdown:** a ranked table beneath the tiles.

### Department selector
- If the user has **one** effective department (or only the default group): no
  selector; show that group.
- If **multiple**: a dropdown with **"All my departments" (union, default)** plus
  each department. Selection drives the `departments` query param.
- Admins: selector lists all observed departments (from a roster the endpoint can
  surface, or from `/me` for non-admins) — admins may pick any.

### Live refresh
Subscribe via `useSystemWebSocket`; on `instance.*` events, refetch (debounced) the
active department's stats so queue-health tiles stay current under high-frequency
load. A modest client-side debounce avoids refetch storms when thousands of events
arrive.

## Postgres-Readiness

All aggregates are standard SQL grouped queries over an index designed in Phase 3;
no SQLite-only constructs. Under future Postgres these become the natural
partition/rollup targets if retention grows.

## Testing (strict TDD)

- **Department clamp:** non-admin requesting a foreign department is reduced to
  their allowed set; admin may request any; omitted param ⇒ union default.
- **Aggregation scope:** private runs excluded; department + public runs included;
  window boundary correct; per-workflow ranking.
- **Queue health:** state counts reflect current instances irrespective of window.
- **Frontend:** single-department user sees no selector; multi-department user gets
  union default + dropdown; tiles render from the typed response; websocket event
  triggers a debounced refetch.
- **Local mode:** default group shown, no selector, all runs visible.

## Dependencies & Order

Depends on **Phase 1** (`/me`, departments) and **Phase 3** (stamped
`department`/`visibility` columns + index). Final phase.
