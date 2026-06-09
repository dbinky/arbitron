# Focus Areas Review Tracking

This document tracks the systematic review of the **final org-readiness gaps**
implementation (schedule "run now", SSO group→department mapping, schedule & run
visibility) for correctness, security, and robustness.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist produces a passing result for that area.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | Run-now domain core (`domain/schedule/runner.go` `Runner.Fire`, shared `resolveCreatorIdentity`, `Service.RunNow`/`WithFirer`/`HasFirer`, `ErrCreatorUnresolved`/`ErrRunNowUnavailable`) — never advances `LastFiredAt`; owner-or-admin authz; run attributed to creator | [x] | [x] | done |
| 2 | Run-now adapters & wiring (`adapter/http/schedule_handler.go` `POST /schedules/{id}/run` 202/200/409/503; `adapter/mcp/tools_schedule.go` conditional `schedule_run_now`; composition-root `NewRunner` from the Evaluator's collaborators) | [x] | [x] | done |
| 3 | Edge group extraction (`domain/identity/edge.go` `EdgeIdentity{Email,Groups}`; `adapter/edge/verifier.go` configured `GroupClaim`, string-or-array, missing⇒nil⇒NOT 401, raw token never logged) | [x] | [x] | done |
| 4 | Department resolution mapping (`domain/identity/groups.go` `ResolveDepartments` mapping▸verbatim▸default chokepoint + `GroupConfig.Mapping`; `EffectiveMembership`/`Leads` union preserved) | [x] | [x] | done |
| 5 | Provisioner edge scoping (`domain/instance/provisioner.go` `ResolveByEmail(ctx,email,groups)` threads groups; group claim NEVER confers admin; email allow-list authoritative; OIDC path unchanged) | [x] | [x] | done |
| 6 | Department remap reconciler (`domain/identity/remap.go` `ReconcileMappings`/`DepartmentRenamer`; `adapter/sqlite/department_renamer.go` four-table rewrite, `UPDATE OR IGNORE`+`DELETE` membership-collision; idempotent; fail-fast boot wiring) | [x] | [x] | done |
| 7 | Run-origin model & stamping (`domain/instance/origin.go` `Origin`/`TriggerSource`/`WithOrigin`/`OriginFromContext`; `domain/engine/engine.go` reads ctx, no `domain/schedule` import; cron `fireOne` + `Runner.Fire` inject origin) | [x] | [x] | done |
| 8 | Instance origin persistence (`adapter/sqlite` migration `014_instance_trigger_origin`, hand-maintained sqlc, repo read/write, backfill `manual`, deleted-schedule still lists; HTTP summary `triggerSource`/`scheduleId`) | [x] | [x] | done |
| 9 | Workflow-scoped schedule listing (`domain/schedule/service.go` variadic `List`/`ListOptions{WorkflowName}` — creator scoping service-set, never request input; `adapter/http` `GET /schedules?workflowName=`) | [x] | [x] | done |
| 10 | Dashboard read-only surfaces (`dashboard/src` `OriginBadge`, `SchedulesPanel`, `api.schedules.list`, TS types) — zero mutation controls; visibility-scoped; graceful empty/error | [x] | [x] | done |
| 11 | Config & headless wiring (`cfg.Auth.RBAC` `GroupClaim`/`DefaultGroup`/`Mapping` loading + `ARBITRON_AUTH_RBAC_MAPPING_*` env overlay; edge verifier `GroupClaim` wired; unset claim ⇒ today's behavior) | [ ] | [ ] | |
| 12 | Test coverage & embedded dist (five-category coverage for new code; injected clock/`httptest`/fakes; `adapter/dashboard/dist` synced with `dashboard/`) | [ ] | [ ] | |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 13 | Run-now core + adapters (§1+§2 — `RunNowResult{InstanceID,Fired}` fired-vs-skipped contract; sentinel→HTTP/MCP mapping; REST/MCP parity over one `Service`) | [ ] | |
| 14 | Edge groups + department resolution (§3+§4 — `Groups []string` flows verifier→`ResolveDepartments`; missing/malformed claim ⇒ default group, not 401) | [ ] | |
| 15 | Resolution + remap reconciler (§4+§6 — a verbatim department later mapped renames consistently across user_departments/instances/schedules/published_workflows with no orphan/dupe) | [ ] | |
| 16 | Origin model + persistence (§7+§8 — in-memory `Origin` fields round-trip the migration columns; `manual`/`schedule`/`run_now` survive write→read→summary) | [ ] | |
| 17 | Origin persistence + dashboard (§8+§10 — summary `triggerSource`/`scheduleId` JSON drives `OriginBadge`; deleted-schedule run still renders) | [ ] | |
| 18 | Workflow listing + SchedulesPanel (§9+§10 — `GET /schedules?workflowName=` contract matches `api.schedules.list`; owner-or-admin scope honored in UI) | [ ] | |
| 19 | Config + edge + resolution (§11+§3+§4 — configured `GroupClaim`/`Mapping` are actually consumed end-to-end; env overlay precedence correct) | [ ] | |
| 20 | Run-now origin slice (§1+§7+§8 — a run-now fire stamps `run_now`+`scheduleId` and reaches the DB + summary; does not advance the cron position) | [ ] | |

---

## Review Guidance

### Single Area Reviews — What To Check

**Pass 1 (Correctness & Coverage):**
- All public functions have test coverage (happy / success / failure / error / edge)
- The code does what the spec and phase design doc say it should
- Error handling is thorough — no silent failures; fail-closed where security-relevant
- Module boundaries are clean (hexagonal: domain has no infra import; engine never imports schedule; origin flows via `domain/instance` context)

**Pass 2 (Robustness & Extensibility):**
- Code aligns to the spirit of the design, not just surface conformance
- Authz is owner-or-admin and service-set from identity (never request input); group claims never confer admin
- Test assertions test real risks (remap consistency across all four tables; missing group claim ⇒ default group; run-now skip-vs-fire), not happy-path ceremony
- Edge cases handled (empty inputs, deleted schedule/workflow, membership collision, unset config)

### Paired Area Reviews — What To Check

- Data flows cleanly across the boundary (the cross-task contracts the plan-alignment review verified: `Runner.Fire`, `ResolveByEmail` signature, `Origin`/summary field names, `Service.List` variadic, sentinel mappings)
- Error propagation works end-to-end (401 vs 500 on edge; 409/503 on run-now; graceful UI degradation)
- No assumptions in one component the other doesn't satisfy
- Integration tests cover the seams between the pair, not just unit happy paths
