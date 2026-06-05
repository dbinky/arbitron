# Focus Areas Review Tracking

This document tracks the systematic review of the **RBAC & Department Dashboard** feature (`docs/specs/rbac-spec.md`) for correctness, visibility isolation, role enforcement, and robustness.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist produces a passing result for that area.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | Group resolution helper (`domain/identity/groups.go` — NormalizeDept, ResolveDepartments default-group fallback, LedDepartments subject/email match, EffectiveMembership) | [x] | [x] | Done |
| 2 | RBAC config (`domain/config` RBACConfig + `adapter/config` ToGroupConfig — groupclaim/defaultgroup/leads, per-dept env overlay, normalization) | [x] | [x] | Done |
| 3 | Department persistence (`adapter/sqlite` user_departments migration 009 + repo ListDepartments/ReplaceDepartments — transactional replace, FK cascade) | [x] | [ ] | |
| 4 | Provisioner (`domain/instance/provisioner.go` — live admin re-eval promote/demote, department snapshot at login, verified-email gating) | [ ] | [ ] | |
| 5 | Claim extraction (`adapter/oidc/provider.go` + `identity.Claims.Groups` — configurable group claim, array-or-string tolerance) | [ ] | [ ] | |
| 6 | Identity enrichment + middleware (`domain/identity/identity.go` Departments/LedDepartments/InDepartment/LeadsDepartment; `adapter/http/auth_middleware.go` population, local-mode default-group admin) | [ ] | [ ] | |
| 7 | Whoami endpoint (`adapter/http/me_handler.go` — projection shape, []-not-null, no subject/email leak, 401 on no identity) | [ ] | [ ] | |
| 8 | Published-workflow visibility schema/repo (`adapter/sqlite` migration 010 + PublicationOf/Upsert — visibility+department columns, shared→public default migration) | [ ] | [ ] | |
| 9 | Visibility type + ports (`domain/workflow` Visibility, Publication, widened PublisherLookup/PublishedStore) | [ ] | [ ] | |
| 10 | Workflow read-path gating (`domain/workflow/service.go` canSeePublication on List/Load/LoadVisible/Copy/ListFiles + engine Trigger — ErrNotFound, no existence leak, nil-lookup local openness) | [ ] | [ ] | |
| 11 | Publish/SetVisibility role gating (`domain/workflow/service.go` + HTTP `PUT /workflows/{name}/visibility` + MCP `workflow_set_visibility` — member→dept, lead/admin→public, copy-then-record invariant) | [ ] | [ ] | |
| 12 | Audit infrastructure (`domain/audit` + `adapter/sqlite` migration 011 EventRepo — void best-effort Service.Record, nil-safe, actor-from-context) | [ ] | [ ] | |
| 13 | Workflow audit emission (`domain/workflow/service.go` — publish / visibility_change / admin.override only when admin is sole reason) | [ ] | [ ] | |
| 14 | Instance visibility schema/model (`adapter/sqlite` migration 012 + index idx_instances_dept_vis; `domain/instance` self-owned Visibility + Department fields, repo round-trip) | [ ] | [ ] | |
| 15 | Trigger stamping + override (`domain/engine` resolveStamp / TriggerWithDepartment — public/department/private stamping, validated per-trigger override, webhook/token persisted-dept) | [ ] | [ ] | |
| 16 | Instance list authorization (`domain/engine/authz.go` listConstraint + `adapter/sqlite` disjunctive query — owner OR public OR department-in-set, service-set filters) | [ ] | [ ] | |
| 17 | canView vs canIntervene split (`domain/engine/authz.go` + intervention path — peer read 403-not-404 vs non-viewer 404; owner/lead/admin intervene) | [ ] | [ ] | |
| 18 | Intervention audit emission (`domain/engine` auditActionFor + SetAuditRecorder — lead_intervention / admin.override, void recorder) | [ ] | [ ] | |
| 19 | Instance adapter surface (`adapter/http/instance_handler.go` + `adapter/mcp/tools_instance.go` — department+visibility on read/list, optional department trigger param, override→400, ErrForbidden→403) | [ ] | [ ] | |
| 20 | Dashboard stats service + clamp (`domain/dashboard` — department clamp admins-exempt/union-default/non-member-empty, window clamp [1h,30d]) | [ ] | [ ] | |
| 21 | Dashboard aggregates (`adapter/sqlite` DashboardRepo — queue health point-in-time, throughput buckets, outcomes incl p95, per-workflow ranked; visibility IN (department,public), empty-dept short-circuit) | [ ] | [ ] | |
| 22 | Dashboard endpoint + wiring (`adapter/http` GET /dashboard/stats thin handler, route in authenticated group, main.go composition) | [ ] | [ ] | |
| 23 | Dashboard frontend data layer (`dashboard/src/api` — Me/DashboardStats types, api.me(), api.dashboard.stats(); request URL/param contract) | [ ] | [ ] | |
| 24 | Dashboard tab + view (`dashboard/src` NavBar tab before Queue, /dashboard route, DashboardView tiles reusing UsageStats pattern, throughput strip, per-workflow table) | [ ] | [ ] | |
| 25 | Department selector + live refresh (`dashboard/src` DepartmentSelector single-vs-multi behavior, union default; debounced useSystemWebSocket refresh) | [ ] | [ ] | |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 26 | Group resolution + Provisioner + Middleware (claim → normalize → snapshot → context identity: a department resolved at login is the SAME set used for request-time authz) | [ ] | |
| 27 | Config + All RBAC predicates (groupclaim/leads/defaultgroup actually drive department/lead/admin decisions; missing claim ⇒ default group everywhere) | [ ] | |
| 28 | Workflow visibility + Instance visibility (a workflow's visibility transfers to the instance stamp; the three levels mean the same thing on both sides) | [ ] | |
| 29 | Read gating + List authz consistency (workflow canSeePublication and instance listConstraint enforce the SAME department/public/admin rule across surfaces) | [ ] | |
| 30 | canView/canIntervene + Adapter mapping (the 404-vs-403 distinction is preserved through HTTP and MCP, not collapsed at the adapter) | [ ] | |
| 31 | Audit infrastructure + emission sites (workflow + intervention) (every consequential mutation is recorded once, with the right actor and action, and never fails the operation) | [ ] | |
| 32 | Trigger override + List authz (an instance stamped via per-trigger override is visible to exactly the right department audience; override cannot widen scope) | [ ] | |
| 33 | Dashboard service clamp + aggregates + /me (the department set from /me drives the clamp, which drives the query; a non-member cannot pull another department's stats) | [ ] | |
| 34 | Local-mode inertness across all areas (departments, visibility gating, list authz, intervention split, dashboard selector all no-op as a single default-group admin) | [ ] | |
| 35 | Migration correctness (009–012) (existing shared→public, existing instances private/default-group; up/down pairs; no data loss; CHECK constraints) | [ ] | |

---

## Review Guidance

### Single Area Reviews — What To Check

**Pass 1 (Correctness & Coverage):**
- All public functions have test coverage (happy / success / failure / error / edge)
- The code does what the spec and design doc say it should
- Error handling is thorough — no silent failures; the right sentinel (`ErrNotFound` vs `ErrForbidden`) is returned
- Module boundaries are clean (no leaking types across boundaries; `domain/instance` does not import `domain/workflow`)

**Pass 2 (Robustness & Isolation):**
- Code aligns to the spirit of the design, not just surface conformance
- Visibility isolation holds — a user cannot see or confirm the existence of another department's data, and cannot widen scope by supplying input
- Local mode stays inert for this area
- Test assertions test real risks (cross-department denial, override rejection, admin override, demotion-on-login), not just happy-path ceremony

### Paired Area Reviews — What To Check

Paired reviews verify that two components integrate correctly:
- The same authorization rule is enforced on both sides of the boundary (domain service, not adapter)
- Data (department, visibility) flows cleanly and means the same thing across the boundary
- Error propagation works end-to-end (404 vs 403 preserved through REST and MCP)
- The integration tests cover the seams between the pair, including the local-mode no-op path
