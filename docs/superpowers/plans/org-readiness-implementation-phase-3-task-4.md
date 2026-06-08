# Org-Readiness — Phase 3, Task 4: schedule management surface (REST + MCP + authz)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Strict TDD: handler/service tests with fakes, then implementation.

**Goal:** Expose schedule lifecycle through the same programmatic surfaces as other trigger operations: REST routes (create / list / get / update / enable-disable / delete) parallel to `adapter/http/workflow_handler.go`'s trigger handlers, and MCP tools (`schedule_create` / `schedule_list` / `schedule_get` / `schedule_update` / `schedule_delete`) parallel to `adapter/mcp/tools_instance.go`'s `instance_trigger`. Enforce authz: a user may create/manage a schedule for a workflow they are permitted to trigger; a user manages **their own** schedules; admins manage all.

**Architecture:** A thin application service `domain/schedule/service.go` holds the authz + validation logic so REST and MCP share ONE enforcement path (no duplicated checks in two adapters). The service depends on `ScheduleRepository`, a "can-trigger" port (the workflow visibility/publication check the trigger path already uses), and reads the request `identity.Identity` from context. REST/MCP handlers are dumb translators (decode → call service → map errors). Authz is identity-derived and **service-set** (handlers MUST NOT take `creatorId` from request input).

**Tech stack:** Go 1.25.5, chi (REST), the gomcp tool API, `testing` + `testify`.

---

## Dependencies

- **Task 1** (entity/port/`Validate`), **Task 2** (`ScheduleRepo`). Independent of Task 3 (evaluator) — they share the repo but not code. Can proceed in parallel with Task 3 once Tasks 1–2 land.

## Spec / design references

- Spec `docs/specs/org-readiness-spec.md` (Phase 3) — "Authorized users can self-serve; runs are attributed to them"; "A user who is permitted to start a given workflow may create a schedule for it"; "A non-administrator can manage their own schedules but not others'; an administrator can manage all"; "Schedules are fully manageable through the API ... the same programmatic surfaces (REST and the tool/automation interface)."
- Design `docs/superpowers/specs/org-readiness-design-phase-3.md` §"Management surface — REST + MCP" and §"Authz".

## Anchors (read before coding)

- `adapter/http/workflow_handler.go` — `Trigger` (ll. 209-259) for the body-decode + `maxTriggerBodyBytes` + `WriteError`/`WriteJSON` + sentinel→status mapping idiom; `userIDFromContext`/`identity.MustFromContext(ctx).UserID` (l. 27).
- `adapter/http/server.go` — `r.Route("/workflows", ...)` block (ll. 260-277) and `/instances` block (ll. 295-...) for where/how to register routes.
- `adapter/mcp/tools_instance.go` — `RegisterInstanceTools` (ll. 22-100) + `instanceTriggerHandler` (ll. 144-178) for tool registration, arg parsing (`args["..."].(string)`, JSON-decoded `variables`), and `srv.deps.Engine` access + identity-in-context (MCP handlers already run behind the same auth).
- `domain/engine/engine.go` — `TriggerWithDepartment`'s visibility gate (ll. 242-258, using `e.publishers.PublicationOf` + `workflow.CanSeePublication`) is the canonical "can this caller trigger this workflow" check. The service reuses the SAME predicate so "can schedule" == "can trigger."
- `domain/identity/identity.go` — `Identity.IsAdmin()`, `FromContext`, `InDepartment`.
- `domain/workflow/` — `CanSeePublication`, `PublicationOf` (the publisher lookup port) for the can-trigger check.

## Files

- **Create:** `domain/schedule/service.go`
  - Ports:
    ```go
    // CanTrigger reports whether the caller may start workflowName (same gate the
    // engine trigger path uses). The published-workflow repo + workflow.CanSeePublication
    // satisfy it; the wiring lives in the composition root.
    type CanTrigger interface {
        CanTrigger(ctx context.Context, id identity.Identity, workflowName string) (bool, error)
    }
    ```
  - `type Service struct { repo ScheduleRepository; canTrigger CanTrigger; clock Clock }` (clock reused from Task 3, or a small `now func() time.Time`; for create timestamps).
  - Sentinels: reuse `schedule.ErrNotFound`; add `var ErrForbidden = errors.New("schedule: forbidden")` (maps to 403) and `var ErrNotAuthorizedWorkflow = errors.New("schedule: cannot schedule a workflow you may not trigger")` (maps to 403).
  - Methods (each reads `identity.FromContext(ctx)`; absence ⇒ `ErrForbidden`):
    - `Create(ctx, input CreateInput) (Schedule, error)` — build a `Schedule` with `CreatorID = caller.UserID` (NEVER from input), `ID = uuid.New()`, timestamps from clock; run `schedule.Validate`; run `canTrigger.CanTrigger(ctx, caller, input.WorkflowName)` → false ⇒ `ErrNotAuthorizedWorkflow`; if `input.Department != ""` it must be one the caller belongs to (`caller.InDepartment`) else a validation error (mirrors `resolveStamp`/`ErrInvalidDepartmentOverride` semantics); then `repo.Create`.
    - `Get(ctx, id) (Schedule, error)` — load; `authorize(caller, s)` (owner or admin) else `ErrForbidden`.
    - `List(ctx) ([]Schedule, error)` — non-admin ⇒ `repo.List(ListFilters{CreatorID: &caller.UserID})`; admin ⇒ `repo.List(ListFilters{})`. (Service-set creator filter; never from request.)
    - `Update(ctx, id, input UpdateInput) (Schedule, error)` — load + `authorize`; apply allowed mutable fields (cron, tz, department, variables, enabled — NOT creator/id); re-`Validate`; re-check `canTrigger` if workflow name changed; `repo.Update`.
    - `SetEnabled(ctx, id, enabled bool) (Schedule, error)` — load + `authorize`; flip `Enabled`; `repo.Update`. (The enable/disable convenience.)
    - `Delete(ctx, id) error` — load + `authorize`; `repo.Delete`.
  - `authorize(caller identity.Identity, s Schedule) bool { return caller.IsAdmin() || s.CreatorID == caller.UserID }`.
- **Create:** `adapter/http/schedule_handler.go`
  - `type ScheduleHandler struct { svc *schedule.Service }` + `NewScheduleHandler`.
  - Handlers, each decoding behind `http.MaxBytesReader(w, r.Body, maxTriggerBodyBytes)` and using `WriteError`/`WriteJSON`:
    - `Create` (`POST /schedules`), `List` (`GET /schedules`), `Get` (`GET /schedules/{id}`), `Update` (`PUT /schedules/{id}`), `SetEnabled` (`POST /schedules/{id}/enable` + `/disable`, or `PUT /schedules/{id}/enabled` with a `{ "enabled": bool }` body), `Delete` (`DELETE /schedules/{id}`).
  - Error mapping (mirror `workflow_handler.go` Trigger): `schedule.ErrNotFound`→404; `ErrForbidden`/`ErrNotAuthorizedWorkflow`→403; `ErrInvalidCron`/`ErrInvalidTimezone`/`ErrEmptyWorkflow`/department-override validation→400; else 500.
- **Modify:** `adapter/http/server.go` — add a `r.Route("/schedules", ...)` block beside `/workflows` (ll. 260-277), registering the six routes; register static `/enable`/`/disable` (or `/{id}/enabled`) carefully relative to `{id}` (same static-before-wildcard caution as the `/instances/dismiss-all` comment, l. 296-300). Add the `ScheduleHandler` to the server `Deps` and construct it in `cmd/arbitron/main.go` server wiring.
- **Create:** `adapter/mcp/tools_schedule.go`
  - `RegisterScheduleTools(srv *Server)` registering: `schedule_create` (workflowName required; cronExpr required; timezone, department, variables JSON, enabled optional), `schedule_list` (optional workflowName/enabled filters), `schedule_get` (id required), `schedule_update` (id required + mutable fields), `schedule_delete` (id required). Mirror `instance_trigger` arg parsing (`args["..."].(string)`, JSON-decode `variables`).
  - Each handler calls `srv.deps.ScheduleService` (add to `adapter/mcp/Deps`), returns `gomcp.NewToolResultError` on `ErrForbidden`/`ErrNotAuthorizedWorkflow`/validation, JSON result on success — mirroring `instanceTriggerHandler`'s `invalid department` mapping (ll. 166-178).
  - Call `RegisterScheduleTools(srv)` wherever `RegisterInstanceTools` is called (find via `grep -rn "RegisterInstanceTools" adapter/mcp cmd`).
- **Modify:** `cmd/arbitron/main.go` — construct the `schedule.Service` (with the `ScheduleRepo` from Task 2/3 and a `CanTrigger` adapter wrapping `publishedRepo` + `workflow.CanSeePublication`); pass it into both the HTTP server `Deps` (`ScheduleHandler`) and the MCP `Deps` (`ScheduleService`). The `CanTrigger` adapter: for a private/unpublished workflow the caller owns or that is open, return true; for a published one, `workflow.CanSeePublication(id, pub)` — i.e. the exact gate `TriggerWithDepartment` applies (ll. 246-257).
- **Test:** `domain/schedule/service_test.go`, `adapter/http/schedule_handler_test.go`, `adapter/mcp/tools_schedule_test.go`.

## TDD test list (five categories)

Service tests use a `fakeRepo`, a `fakeCanTrigger` (programmable bool/error), and inject identity via `identity.WithIdentity(ctx, ...)`. Handler tests use `httptest` + a real/fake service and set identity in the request context (mirror `workflow_handler_trigger_dept_test.go`).

### Happy-path

- [ ] `TestService_Create_AttributesToCaller` — caller `u1`; `fakeCanTrigger` true. Created schedule has `CreatorID == u1`, an `ID`, timestamps set, `Validate` passed. Assert input cannot override `CreatorID` (pass a bogus creator in input and confirm it is ignored).
- [ ] `TestService_List_NonAdmin_OwnOnly` — caller `u1` (non-admin) ⇒ `repo.List` called with `CreatorID == &u1`. `TestService_List_Admin_All` — admin ⇒ `CreatorID == nil`.
- [ ] `TestService_SetEnabled_Toggles` — owner disables then enables their schedule; repo reflects each.
- [ ] `TestScheduleHandler_Create_201` / `TestScheduleHandler_List_200` — REST happy paths produce the right status + JSON body.

### Input-validation

- [ ] `TestService_Create_InvalidCron_400Sentinel` — bad cron ⇒ `ErrInvalidCron` (handler maps to 400).
- [ ] `TestService_Create_DepartmentNotMember_Rejected` — `input.Department` not in caller's departments ⇒ validation error (400), no repo write.
- [ ] `TestScheduleHandler_Create_BadBody_400` — malformed JSON ⇒ 400.

### Error-handling / authz

- [ ] `TestService_Create_CannotTriggerWorkflow_Forbidden` — `fakeCanTrigger` false ⇒ `ErrNotAuthorizedWorkflow`; no schedule created. (Spec: "may create a schedule for a workflow they are permitted to trigger.")
- [ ] `TestService_Get_NonOwnerNonAdmin_Forbidden` — `u2` requests `u1`'s schedule ⇒ `ErrForbidden`. `TestService_Update_NonOwner_Forbidden`, `TestService_Delete_NonOwner_Forbidden` — same.
- [ ] `TestService_Admin_ManagesAnyones` — admin Get/Update/Delete on `u1`'s schedule succeeds. (Spec: "an administrator can manage all.")
- [ ] `TestService_AnyMethod_NoIdentity_Forbidden` — no identity in context ⇒ `ErrForbidden` (never leaks).
- [ ] `TestService_Get_NotFound` ⇒ `schedule.ErrNotFound` (handler 404).

### Edge-cases / boundary

- [ ] `TestService_Update_CannotChangeCreatorOrID` — an update payload attempting to change `CreatorID`/`ID` leaves them unchanged.
- [ ] `TestService_Update_WorkflowChange_RechecksCanTrigger` — changing `WorkflowName` to one the caller cannot trigger ⇒ `ErrNotAuthorizedWorkflow`.
- [ ] `TestScheduleHandler_Enable_StaticRouteNotShadowedById` — `POST /schedules/{id}/enable` resolves to `SetEnabled`, not captured by `GET /schedules/{id}` (route-ordering regression guard).

### Integration (REST + MCP parity)

- [ ] `TestScheduleHandler_FullLifecycle` — create → list (sees it) → get → disable → update → delete → get(404), through the chi router with identity middleware, asserting each status.
- [ ] `TestScheduleTools_Create_And_List_MCP` — `schedule_create` then `schedule_list` through the MCP handlers return consistent results with the REST surface (same service underneath). Assert a non-admin caller only lists their own (set a different identity on a second call).
- [ ] `TestScheduleTools_Create_Forbidden_MCP` — `schedule_create` for a workflow the caller cannot trigger returns an MCP error result (mirrors `instanceTriggerHandler`'s error result).

## Implementation steps (after each test goes red)

1. [ ] `service.go` Create + authorize + Validate + canTrigger gate.
2. [ ] List (own/all), Get, Update, SetEnabled, Delete with authz.
3. [ ] `schedule_handler.go` + error mapping; register routes in `server.go`.
4. [ ] `tools_schedule.go` + register in MCP; add `ScheduleService` to MCP `Deps`.
5. [ ] Wire `CanTrigger` adapter + service construction in `cmd/arbitron/main.go`.
6. [ ] `make test` green; `go build ./...` green; `./arbitron serve` exposes the routes (and MCP tools after reconnect).

## Acceptance criteria

- A non-admin can create/list/get/update/enable-disable/delete **only their own** schedules; an admin can manage **all** — enforced in ONE service path shared by REST and MCP.
- A user may only schedule a workflow they are permitted to trigger (same gate the engine trigger path uses); otherwise 403 / MCP error.
- Each schedule is attributed to its creator (`CreatorID` = the request identity, never request input).
- Invalid cron / timezone / non-member department override are rejected at create/update with 400 and never persisted.
- The full lifecycle is exercisable over both REST and MCP and the two surfaces agree (same service).

## Notes

- **One authz path:** putting authz + validation in `domain/schedule/Service` (not in each handler) prevents REST/MCP drift — the same lesson as the engine routing all trigger-time visibility through `workflow.CanSeePublication`. Handlers only translate transport ⇄ service and map sentinels → status/result.
- **"Can schedule" == "can trigger":** the `CanTrigger` port deliberately reuses the publication/visibility predicate the engine's `TriggerWithDepartment` applies, so authorization to schedule never diverges from authorization to run.
- **Department override:** validated at create/update against the caller's departments here (fast-fail, 400), AND re-validated at fire time by `resolveStamp` inside the engine (Task 3's trigger call) — defense in depth; a department the caller loses membership of later still fails closed at fire time.
- **No "run now" in this task:** the optional on-demand fire convenience is explicitly deferred per spec/design; the surface here is create/list/get/update/enable-disable/delete only.
