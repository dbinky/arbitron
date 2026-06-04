# RBAC Phase 4 — Department Dashboard: Implementation Plan Overview

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement these tasks. Each `rbac-implementation-phase-4-task-{N}.md` is a self-contained, TDD-structured task with checkbox steps.

**Goal:** Add the tile-based **Dashboard** tab (positioned before Queue) that shows how a department's department-visible and public workflows are operating — live queue health, throughput over time, aggregate outcomes, and a per-workflow breakdown — backed by a department-scoped aggregate stats endpoint over the Phase 3 department-stamped instances.

**Architecture:** Go hexagonal backend — a `domain/dashboard` service composes four aggregates behind a `StatsRepository` port and clamps requested departments to the caller's effective set (admins exempt); a SQLite adapter implements the aggregates over `idx_instances_dept_vis`; a thin `GET /api/v1/dashboard/stats` handler parses params and delegates. React 19 + TypeScript frontend (embedded in the Go binary) — a Dashboard tab/route, a `/me`-driven department selector, tiles reusing the `UsageStats` pattern, and debounced live refresh via `useSystemWebSocket`.

**Tech Stack:** Go, SQLite (raw aggregate SQL, matching the existing `InstanceRepo.Stats` precedent), chi, `domain/identity` context; React 19, TypeScript, Vite, react-router-dom, Vitest + Testing Library. Strict TDD throughout.

**Assumed present (dependencies):**
- **Phase 1:** `identity.Identity.Departments`, `LedDepartments`, `InDepartment(dept)`, `LeadsDepartment(dept)`, `IsAdmin()`, and the `GET /api/v1/me` endpoint returning `{ userId, username, role, departments[], ledDepartments[], isAdmin }`.
- **Phase 3:** `instances.department` + `instances.visibility` columns and `idx_instances_dept_vis(department, visibility, state, created_at)`.

---

## Task list & dependency graph

| Task | File | Summary | Depends on |
|------|------|---------|-----------|
| 1 | `...-task-1.md` | `domain/dashboard` service + `StatsRepository` port; department clamp (admins exempt) + window clamp | Phase 1 |
| 2 | `...-task-2.md` | SQLite `DashboardRepo`: four department-scoped aggregates over `idx_instances_dept_vis` | Task 1, Phase 3 |
| 3 | `...-task-3.md` | `GET /api/v1/dashboard/stats` handler + router + `main.go` wiring | Tasks 1, 2 |
| 4 | `...-task-4.md` | Frontend types + `api.me()` / `api.dashboard.stats()` client methods | Backend Task 3 (shape), Phase 1 `/me` |
| 5 | `...-task-5.md` | Dashboard tab (before Queue) + `/dashboard` route + view shell | none |
| 6 | `...-task-6.md` | `DepartmentSelector` component (none for single dept; union default + dropdown for multi) | none |
| 7 | `...-task-7.md` | `DashboardView`: tiles + per-workflow table + selector + debounced live refresh | Tasks 4, 5, 6 |

**Suggested execution order:** 1 → 2 → 3 (backend vertical slice), then 4 → 5 → 6 → 7 (frontend). Tasks 5 and 6 are independent of the backend and may be done in parallel with 1-3.

```
Phase1 ─┬─► T1 ─► T2 ─► T3 ─► T4 ─┐
        │                         ├─► T7
Phase3 ─┘   T5 ───────────────────┤
            T6 ───────────────────┘
```

---

## Spec coverage map

| Spec / design requirement | Covered by |
|---------------------------|-----------|
| Aggregate department-visible + public runs in caller's department(s); exclude private | T1 (scope), T2 (`visibility IN ('department','public')`) |
| Server-side clamp to caller's effective departments; admins exempt; omitted ⇒ union default | T1 (clamp + tests), T3 (param parse) |
| Live queue health (running/queued/waiting/paused/failures) | T2 (queue health), T7 (tiles) |
| Throughput over time | T2 (hourly buckets), T7 (bar strip) |
| Success/failure rate + typical/p95 duration | T2 (outcomes + p95), T7 (tiles) |
| Per-workflow ranked breakdown | T2 (ranked query), T7 (table) |
| `GET /api/v1/dashboard/stats?departments=&window=` | T3 |
| Dashboard tab BEFORE Queue + `/dashboard` route | T5 |
| Department selector: none if single/default; union default + dropdown if multi; driven by `/me` | T6 (selector), T7 (wiring from `/me`) |
| Live refresh via `useSystemWebSocket` with debounce | T7 |
| Local mode: default group, no selector, all runs visible | T1 (admin identity ⇒ unconstrained), T6 (single-dept ⇒ no selector), T7 (single-element `me.departments`) |

---

## Final verification (run after all tasks complete)

- [ ] Backend: `go build ./... && go vet ./... && go test ./domain/dashboard/ ./adapter/sqlite/ ./adapter/http/`
- [ ] Frontend: `cd dashboard && npx tsc --noEmit && npx vitest run && npm run build`
- [ ] Rebuild the binary so the embedded SPA is current (per `adapter/dashboard/embed.go`, the dist is embedded at Go build time): `cd dashboard && npm run build` then `go build ./...`.
- [ ] Manual smoke (org mode, multi-department user): the Dashboard tab appears first, the selector defaults to "All my departments", focusing a single department refetches, and queue-health tiles update as instances change.
- [ ] Manual smoke (local mode): the Dashboard tab appears, shows the default group, and renders **no** selector.

---

## Notes for the implementer

- **Raw SQL vs sqlc (Task 2):** the dashboard aggregates use raw parameterized `database/sql` (not sqlc) because the `department IN (…)` list is dynamic. This deliberately follows the existing `InstanceRepo.Stats` precedent; the design doc's "Queries (adapter/sqlite/queries)" describes intent, not a hard sqlc requirement.
- **Where authorization lives:** the department clamp is a domain-service concern (Task 1), not the HTTP handler (Task 3). The handler only parses request input. This keeps REST/MCP/future surfaces sharing one rule, matching the codebase's existing authz posture.
- **Selector contract:** `null` selection ⇒ omit the `departments` query param ⇒ backend union default. A single-element array focuses one department. This contract is shared by Tasks 4, 6, and 7 — keep it consistent.
