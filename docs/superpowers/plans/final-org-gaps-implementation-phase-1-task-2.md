# Schedule "Run Now" — Phase 1, Task 2: Adapters (REST + MCP) and Composition Root

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose `Service.RunNow` over REST (`POST /schedules/{id}/run`) and MCP (`schedule_run_now`), and wire a `schedule.Runner` into the management `Service` at the composition root so the surface is live.

**Architecture:** Both adapters are dumb translators over the shared `schedule.Service.RunNow` (built in Task 1), so REST and MCP cannot drift. The REST handler decodes an optional `{"skipWhenBusy": bool}` body and maps the result to 202 (fired) / 200 (skipped) and the new sentinels to 409/503. The MCP `schedule_run_now` tool is registered only when the wired service reports `HasFirer()`, consistent with the existing conditional schedule-tools registration. The composition root builds a `schedule.Runner` from the same `Trigger`/`IdentityResolver`/`ActiveRunChecker` the `Evaluator` already uses and passes it via `schedule.WithFirer`.

**Tech Stack:** Go, `github.com/go-chi/chi/v5`, `github.com/mark3labs/mcp-go`, `github.com/google/uuid`, testify.

**Prerequisite:** Task 1 (`final-org-gaps-implementation-phase-1-task-1.md`) is complete and `go test ./domain/schedule/...` is green. This task depends on `schedule.RunNowInput`, `schedule.RunNowResult`, `(*Service).RunNow`, `(*Service).HasFirer`, `schedule.NewRunner`, `schedule.ErrCreatorUnresolved`, and `schedule.ErrRunNowUnavailable` existing.

---

## Background: existing code this task touches

- `adapter/http/schedule_handler.go` — `ScheduleHandler{svc *schedule.Service}`; helpers `decodeScheduleBody(w, r, dst) bool`, `scheduleIDParam(w, r) (uuid.UUID, bool)`, `writeScheduleError(w, err)` (the sentinel→status switch). Existing handlers call `h.svc.X(...)` then `WriteJSON`/`writeScheduleError`. `WriteJSON(w, status, data)` and `WriteError(w, status, code, msg)` live in `adapter/http/errors.go`. `maxTriggerBodyBytes` is defined in `adapter/http/workflow_handler.go`.
- `adapter/http/server.go` — `Deps.ScheduleHandler *ScheduleHandler`; routes registered at `server.go:286-295` inside `r.Route("/schedules", ...)`.
- `adapter/mcp/tools_schedule.go` — `RegisterScheduleTools(srv)` guards on `srv.deps == nil || srv.deps.ScheduleService == nil`, then registers the five tools. `scheduleResultError(err)` maps sentinels to `gomcp.NewToolResultError(...)`. Handlers read `srv.deps.ScheduleService`. Helpers: `ParseUUID(request, "id")`, `MarshalResult(v)`, `gomcp.NewToolResultError(msg)`.
- `adapter/mcp/deps.go` — `Deps.ScheduleService *schedule.Service`.
- `cmd/arbitron/main.go:653-663` — builds `scheduleRepo`, `scheduleEval := schedule.NewEvaluator(scheduleRepo, eng, instanceRepo, scheduleIdentityResolver{...}, scheduleAlerter, ...)`, and `scheduleSvc := schedule.NewService(scheduleRepo, scheduleCanTrigger{publishers: publishedRepo})`. The three Runner collaborators are already in scope here: `eng` (Trigger), `instanceRepo` (ActiveRunChecker), and `scheduleIdentityResolver{users: userRepo, groups: groupCfg}` (IdentityResolver). `scheduleSvc` is stored on the `ds`/deps struct and handed to `adapthttp.NewScheduleHandler(ds.scheduleSvc)` at `main.go:871` and to the MCP `Deps.ScheduleService`.

---

### Task 2a: REST `POST /schedules/{id}/run`

**Files:**
- Modify: `adapter/http/schedule_handler.go` (add request struct, `RunNow` handler, extend `writeScheduleError`)
- Modify: `adapter/http/server.go:286-295` (register the route)
- Modify: `adapter/http/schedule_handler_test.go` (add the firer fixture + route in `schedRouter`, plus RunNow tests)

- [ ] **Step 1: Write the failing REST tests**

In `adapter/http/schedule_handler_test.go`, first add a stub firer and a way to build a firer-wired router. Add near the existing `allowCanTrigger` type:

```go
// stubHTTPFirer is an in-test schedule.Firer for the REST run-now tests.
type stubHTTPFirer struct {
	inst  uuid.UUID
	fired bool
	err   error
	calls int
}

func (f *stubHTTPFirer) Fire(_ context.Context, _ schedule.Schedule, _ bool) (uuid.UUID, bool, error) {
	f.calls++
	return f.inst, f.fired, f.err
}

// schedRouterWithFirer mirrors schedRouter but wires a Firer so the run-now
// route is exercised. It registers the /{id}/run route.
func schedRouterWithFirer(t *testing.T, repo schedule.ScheduleRepository, allow bool, id identity.Identity, firer schedule.Firer) http.Handler {
	t.Helper()
	svc := schedule.NewService(repo, allowCanTrigger{allow: allow}, schedule.WithFirer(firer))
	h := adapthttp.NewScheduleHandler(svc)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(identity.WithIdentity(req.Context(), id)))
		})
	})
	r.Route("/schedules", func(r chi.Router) {
		r.Post("/{id}/run", h.RunNow)
		r.Get("/{id}", h.Get)
	})
	return r
}

// seedSchedule inserts an enabled schedule owned by owner into repo and returns it.
func seedSchedule(repo *schedTestRepo, owner uuid.UUID) schedule.Schedule {
	s := schedule.Schedule{
		ID:           uuid.New(),
		WorkflowName: "report",
		CronExpr:     "0 9 * * *",
		CreatorID:    owner,
		Enabled:      true,
	}
	repo.m[s.ID] = &s
	return s
}
```

Then add the RunNow tests (the 5 TDD categories plus REST status mapping):

```go
// --- run-now (Capability A) ---

// Happy path: owner fires => 202 Accepted, body {instanceId, fired:true}.
func TestScheduleHandler_RunNow_202(t *testing.T) {
	repo := newSchedTestRepo()
	u := testUser()
	s := seedSchedule(repo, u.UserID)
	inst := uuid.New()
	router := schedRouterWithFirer(t, repo, true, u, &stubHTTPFirer{inst: inst, fired: true})

	w := do(t, router, http.MethodPost, "/schedules/"+s.ID.String()+"/run", `{"skipWhenBusy":false}`)

	require.Equal(t, http.StatusAccepted, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, inst.String(), body["instanceId"])
	assert.Equal(t, true, body["fired"])
}

// Empty body is allowed (skipWhenBusy defaults to false) => still 202.
func TestScheduleHandler_RunNow_EmptyBody_202(t *testing.T) {
	repo := newSchedTestRepo()
	u := testUser()
	s := seedSchedule(repo, u.UserID)
	router := schedRouterWithFirer(t, repo, true, u, &stubHTTPFirer{inst: uuid.New(), fired: true})

	w := do(t, router, http.MethodPost, "/schedules/"+s.ID.String()+"/run", "")

	require.Equal(t, http.StatusAccepted, w.Code)
}

// Skipped (busy + skipWhenBusy) => 200 OK, body {fired:false}, no instanceId.
func TestScheduleHandler_RunNow_Skipped_200(t *testing.T) {
	repo := newSchedTestRepo()
	u := testUser()
	s := seedSchedule(repo, u.UserID)
	router := schedRouterWithFirer(t, repo, true, u, &stubHTTPFirer{inst: uuid.Nil, fired: false})

	w := do(t, router, http.MethodPost, "/schedules/"+s.ID.String()+"/run", `{"skipWhenBusy":true}`)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, false, body["fired"])
}

// Input validation: malformed UUID in path => 400.
func TestScheduleHandler_RunNow_BadID_400(t *testing.T) {
	router := schedRouterWithFirer(t, newSchedTestRepo(), true, testUser(), &stubHTTPFirer{})
	w := do(t, router, http.MethodPost, "/schedules/not-a-uuid/run", `{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Input validation: malformed JSON body => 400.
func TestScheduleHandler_RunNow_BadBody_400(t *testing.T) {
	repo := newSchedTestRepo()
	u := testUser()
	s := seedSchedule(repo, u.UserID)
	router := schedRouterWithFirer(t, repo, true, u, &stubHTTPFirer{inst: uuid.New(), fired: true})
	w := do(t, router, http.MethodPost, "/schedules/"+s.ID.String()+"/run", `{not json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Authorization: a non-owner non-admin => 403, firer never called.
func TestScheduleHandler_RunNow_NonOwner_403(t *testing.T) {
	repo := newSchedTestRepo()
	owner := testUser()
	s := seedSchedule(repo, owner.UserID)
	firer := &stubHTTPFirer{inst: uuid.New(), fired: true}
	stranger := testUser() // different UserID, RoleUser
	router := schedRouterWithFirer(t, repo, true, stranger, firer)

	w := do(t, router, http.MethodPost, "/schedules/"+s.ID.String()+"/run", `{}`)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, 0, firer.calls)
}

// Not found => 404.
func TestScheduleHandler_RunNow_NotFound_404(t *testing.T) {
	router := schedRouterWithFirer(t, newSchedTestRepo(), true, testUser(), &stubHTTPFirer{})
	w := do(t, router, http.MethodPost, "/schedules/"+uuid.New().String()+"/run", `{}`)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// Failure: creator unresolved => 409 Conflict.
func TestScheduleHandler_RunNow_CreatorUnresolved_409(t *testing.T) {
	repo := newSchedTestRepo()
	u := testUser()
	s := seedSchedule(repo, u.UserID)
	router := schedRouterWithFirer(t, repo, true, u, &stubHTTPFirer{err: schedule.ErrCreatorUnresolved})

	w := do(t, router, http.MethodPost, "/schedules/"+s.ID.String()+"/run", `{}`)

	assert.Equal(t, http.StatusConflict, w.Code)
}

// Failure: firer unwired => 503 Service Unavailable. Build a router WITHOUT a
// firer but WITH the /run route.
func TestScheduleHandler_RunNow_Unwired_503(t *testing.T) {
	repo := newSchedTestRepo()
	u := testUser()
	s := seedSchedule(repo, u.UserID)
	svc := schedule.NewService(repo, allowCanTrigger{allow: true}) // no WithFirer
	h := adapthttp.NewScheduleHandler(svc)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(identity.WithIdentity(req.Context(), u)))
		})
	})
	r.Route("/schedules", func(r chi.Router) { r.Post("/{id}/run", h.RunNow) })

	w := do(t, r, http.MethodPost, "/schedules/"+s.ID.String()+"/run", `{}`)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}
```

- [ ] **Step 2: Run the REST tests to verify they fail to compile**

Run: `go test ./adapter/http/ -run TestScheduleHandler_RunNow`
Expected: FAIL — build error `h.RunNow undefined (type *ScheduleHandler has no field or method RunNow)`.

- [ ] **Step 3: Implement the REST handler and extend the error mapping**

In `adapter/http/schedule_handler.go`:

a) Add the request struct after `scheduleUpdateRequest`:

```go
// scheduleRunNowRequest is the POST /schedules/{id}/run body. The whole body is
// optional; an empty body leaves skipWhenBusy=false (fire anyway).
type scheduleRunNowRequest struct {
	SkipWhenBusy bool `json:"skipWhenBusy,omitempty"`
}
```

b) Extend `writeScheduleError` to map the two new sentinels. Add these cases to the existing `switch` (place the 409 and 503 cases before the `default`):

```go
	case errors.Is(err, schedule.ErrCreatorUnresolved):
		WriteError(w, http.StatusConflict, "CONFLICT", err.Error())
	case errors.Is(err, schedule.ErrRunNowUnavailable):
		WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", err.Error())
```

c) Add the handler (place after `Delete`):

```go
// RunNow handles POST /schedules/{id}/run. Body {"skipWhenBusy": bool} is
// optional (default false ⇒ fire even if a prior run is active). On a started
// run it returns 202 with {instanceId, fired:true}; when skipped-because-busy it
// returns 200 with {fired:false}.
func (h *ScheduleHandler) RunNow(w http.ResponseWriter, r *http.Request) {
	id, ok := scheduleIDParam(w, r)
	if !ok {
		return
	}
	var req scheduleRunNowRequest
	// The body is optional. An empty body must not be a 400, so decode only when
	// there is content; reuse decodeScheduleBody (which 400s on malformed JSON).
	if r.ContentLength != 0 {
		if !decodeScheduleBody(w, r, &req) {
			return
		}
	}
	res, err := h.svc.RunNow(r.Context(), id, schedule.RunNowInput{SkipWhenBusy: req.SkipWhenBusy})
	if err != nil {
		writeScheduleError(w, err)
		return
	}
	if !res.Fired {
		WriteJSON(w, http.StatusOK, map[string]any{"fired": false})
		return
	}
	WriteJSON(w, http.StatusAccepted, map[string]any{
		"instanceId": res.InstanceID.String(),
		"fired":      true,
	})
}
```

**Note on the empty-body branch:** `httptest.NewRequest` with an empty string body yields `ContentLength == 0`, so `TestScheduleHandler_RunNow_EmptyBody_202` exercises the skip-decode path. The `{not json` test has non-zero length and hits `decodeScheduleBody`'s 400.

- [ ] **Step 4: Run the REST tests to verify they pass**

Run: `go test ./adapter/http/ -run TestScheduleHandler_RunNow -v`
Expected: PASS for all `TestScheduleHandler_RunNow_*`.

- [ ] **Step 5: Register the route in `server.go`**

In `adapter/http/server.go`, inside the `r.Route("/schedules", ...)` block (around line 287-295), add the run route alongside the others:

```go
				r.Post("/{id}/run", d.ScheduleHandler.RunNow)
```

Place it next to the existing `r.Post("/{id}/enable", ...)` / `r.Post("/{id}/disable", ...)` lines. chi matches the static `/{id}/run` suffix unambiguously (same pattern as enable/disable).

- [ ] **Step 6: Run the full http adapter suite + vet**

Run: `go test ./adapter/http/... && go vet ./adapter/http/...`
Expected: PASS, no vet complaints; all pre-existing schedule handler tests stay green.

- [ ] **Step 7: Commit**

```bash
git add adapter/http/schedule_handler.go adapter/http/server.go adapter/http/schedule_handler_test.go
git commit -m "feat(http): add POST /schedules/{id}/run run-now endpoint

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2b: MCP `schedule_run_now`

**Files:**
- Modify: `adapter/mcp/tools_schedule.go` (conditional registration + `scheduleRunNowHandler` + extend `scheduleResultError`)
- Modify: `adapter/mcp/tools_schedule_test.go` (firer-wired server helper + tests)

- [ ] **Step 8: Write the failing MCP tests**

In `adapter/mcp/tools_schedule_test.go`, add a stub firer and a firer-wired server helper near `newScheduleServer`:

```go
// stubMCPFirer is an in-test schedule.Firer for the MCP run-now tests.
type stubMCPFirer struct {
	inst  uuid.UUID
	fired bool
	err   error
	calls int
}

func (f *stubMCPFirer) Fire(_ context.Context, _ schedule.Schedule, _ bool) (uuid.UUID, bool, error) {
	f.calls++
	return f.inst, f.fired, f.err
}

// newScheduleServerWithFirer wires a Firer so schedule_run_now is registered.
func newScheduleServerWithFirer(repo schedule.ScheduleRepository, allow bool, firer schedule.Firer) *adaptmcp.Server {
	svc := schedule.NewService(repo, mcpCanTrigger{allow: allow}, schedule.WithFirer(firer))
	deps := &adaptmcp.Deps{ScheduleService: svc}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterScheduleTools(srv)
	return srv
}

func seedMCPSchedule(repo *mcpSchedRepo, owner uuid.UUID) schedule.Schedule {
	s := schedule.Schedule{
		ID:           uuid.New(),
		WorkflowName: "report",
		CronExpr:     "0 9 * * *",
		CreatorID:    owner,
		Enabled:      true,
	}
	repo.m[s.ID] = &s
	return s
}
```

(Confirm the in-test repo constructor name with `grep -n "func newMCPSchedRepo\|func newScheduleServer" adapter/mcp/tools_schedule_test.go`; the helper above assumes `mcpSchedRepo.m` is the field, as the existing fixtures use.)

Then the tests:

```go
// --- run_now (Capability A) ---

// Conditional registration: with a Firer wired, schedule_run_now is advertised.
func TestScheduleTools_RunNow_Registered(t *testing.T) {
	srv := newScheduleServerWithFirer(newMCPSchedRepo(), true, &stubMCPFirer{})
	var found bool
	for _, tool := range srv.ListTools() {
		if tool.Name == "schedule_run_now" {
			found = true
		}
	}
	assert.True(t, found, "schedule_run_now must register when a firer is wired")
}

// Conditional registration: WITHOUT a firer, schedule_run_now is NOT advertised
// (the other schedule_* tools still are).
func TestScheduleTools_RunNow_NotRegisteredWhenUnwired(t *testing.T) {
	srv := newScheduleServer(newMCPSchedRepo(), true) // no firer
	var foundRunNow, foundList bool
	for _, tool := range srv.ListTools() {
		switch tool.Name {
		case "schedule_run_now":
			foundRunNow = true
		case "schedule_list":
			foundList = true
		}
	}
	assert.False(t, foundRunNow, "schedule_run_now must NOT register without a firer")
	assert.True(t, foundList, "the other schedule tools still register")
}

// Happy path: owner fires => not an error; result carries instanceId + fired:true.
func TestScheduleTools_RunNow_HappyPath(t *testing.T) {
	repo := newMCPSchedRepo()
	owner := uuid.New()
	s := seedMCPSchedule(repo, owner)
	inst := uuid.New()
	srv := newScheduleServerWithFirer(repo, true, &stubMCPFirer{inst: inst, fired: true})

	res := callSchedule(t, srv, userCtx(owner), "schedule_run_now", map[string]any{"id": s.ID.String()})

	require.False(t, res.IsError, schedTextOf(t, res))
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(schedTextOf(t, res)), &body))
	assert.Equal(t, inst.String(), body["instanceId"])
	assert.Equal(t, true, body["fired"])
}

// skipWhenBusy arg is passed through; skipped result => fired:false, not an error.
func TestScheduleTools_RunNow_Skipped(t *testing.T) {
	repo := newMCPSchedRepo()
	owner := uuid.New()
	s := seedMCPSchedule(repo, owner)
	srv := newScheduleServerWithFirer(repo, true, &stubMCPFirer{inst: uuid.Nil, fired: false})

	res := callSchedule(t, srv, userCtx(owner), "schedule_run_now", map[string]any{
		"id": s.ID.String(), "skipWhenBusy": true,
	})

	require.False(t, res.IsError, schedTextOf(t, res))
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(schedTextOf(t, res)), &body))
	assert.Equal(t, false, body["fired"])
}

// Input validation: missing/blank id => MCP error.
func TestScheduleTools_RunNow_BadID(t *testing.T) {
	srv := newScheduleServerWithFirer(newMCPSchedRepo(), true, &stubMCPFirer{})
	res := callSchedule(t, srv, userCtx(uuid.New()), "schedule_run_now", map[string]any{"id": "not-a-uuid"})
	assert.True(t, res.IsError)
}

// Authorization: non-owner non-admin => MCP error (forbidden), firer not called.
func TestScheduleTools_RunNow_NonOwnerForbidden(t *testing.T) {
	repo := newMCPSchedRepo()
	owner := uuid.New()
	s := seedMCPSchedule(repo, owner)
	firer := &stubMCPFirer{inst: uuid.New(), fired: true}
	srv := newScheduleServerWithFirer(repo, true, firer)

	res := callSchedule(t, srv, userCtx(uuid.New()), "schedule_run_now", map[string]any{"id": s.ID.String()})

	assert.True(t, res.IsError)
	assert.Contains(t, schedTextOf(t, res), "forbidden")
	assert.Equal(t, 0, firer.calls)
}

// Failure: creator unresolved => MCP error mentioning conflict.
func TestScheduleTools_RunNow_CreatorUnresolved(t *testing.T) {
	repo := newMCPSchedRepo()
	owner := uuid.New()
	s := seedMCPSchedule(repo, owner)
	srv := newScheduleServerWithFirer(repo, true, &stubMCPFirer{err: schedule.ErrCreatorUnresolved})

	res := callSchedule(t, srv, userCtx(owner), "schedule_run_now", map[string]any{"id": s.ID.String()})

	assert.True(t, res.IsError)
	assert.Contains(t, schedTextOf(t, res), "conflict")
}
```

- [ ] **Step 9: Run the MCP tests to verify they fail**

Run: `go test ./adapter/mcp/ -run TestScheduleTools_RunNow`
Expected: FAIL — `schedule_run_now` is never registered, so the happy-path/skip/authz calls return an "unknown tool"/error result and the registration assertions fail.

- [ ] **Step 10: Implement the MCP tool, conditional registration, and error mapping**

In `adapter/mcp/tools_schedule.go`:

a) At the end of `RegisterScheduleTools`, after the `schedule_delete` registration, add the conditional run-now registration gated on `HasFirer()`:

```go
	// schedule_run_now is advertised ONLY when a firer is wired (HasFirer). An
	// unwired deployment does not surface run-now, mirroring the conditional
	// registration of the schedule_* tools themselves.
	if srv.deps.ScheduleService.HasFirer() {
		srv.AddTool(
			gomcp.NewTool("schedule_run_now",
				gomcp.WithDescription("Fire an existing schedule immediately, out of its cron cadence, reusing its variables and creator attribution. You must be the schedule's owner (admins may run any). By default fires even if a prior run is active; set skipWhenBusy to skip when one is."),
				gomcp.WithString("id", gomcp.Required(), gomcp.Description("Schedule UUID")),
				gomcp.WithBoolean("skipWhenBusy", gomcp.Description("If true and a prior run of this schedule is still active, skip instead of starting a new run (default false)")),
			),
			scheduleRunNowHandler(srv),
		)
	}
```

b) Extend `scheduleResultError` to map the two new sentinels. Add these cases before the `default`:

```go
	case errors.Is(err, schedule.ErrCreatorUnresolved):
		return gomcp.NewToolResultError(fmt.Sprintf("conflict: %s", err))
	case errors.Is(err, schedule.ErrRunNowUnavailable):
		return gomcp.NewToolResultError(fmt.Sprintf("schedule operation failed: run-now unavailable: %s", err))
```

c) Add the handler (place after `scheduleDeleteHandler`):

```go
func scheduleRunNowHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		args := request.GetArguments()
		skip, _ := args["skipWhenBusy"].(bool) // default false
		res, err := srv.deps.ScheduleService.RunNow(ctx, id, schedule.RunNowInput{SkipWhenBusy: skip})
		if err != nil {
			return scheduleResultError(err), nil
		}
		out := map[string]any{"fired": res.Fired}
		if res.Fired {
			out["instanceId"] = res.InstanceID.String()
		}
		return MarshalResult(out)
	}
}
```

- [ ] **Step 11: Run the MCP tests to verify they pass**

Run: `go test ./adapter/mcp/ -run TestScheduleTools_RunNow -v`
Expected: PASS for all `TestScheduleTools_RunNow_*`.

- [ ] **Step 12: Run the full mcp adapter suite + vet (confirm the unwired-registration test still holds)**

Run: `go test ./adapter/mcp/... && go vet ./adapter/mcp/...`
Expected: PASS. `TestRegisterScheduleTools_NotRegisteredWhenUnwired` (nil deps) and the new unwired test stay green; the other schedule tools are unaffected.

- [ ] **Step 13: Commit**

```bash
git add adapter/mcp/tools_schedule.go adapter/mcp/tools_schedule_test.go
git commit -m "feat(mcp): add schedule_run_now tool (conditional on wired firer)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2c: Composition root wiring

**Files:**
- Modify: `cmd/arbitron/main.go` (around line 655-663): build a `schedule.Runner` and pass it via `schedule.WithFirer`.

- [ ] **Step 14: Wire the Runner into the management Service**

In `cmd/arbitron/main.go`, in the `12c-sched` block, build a `Runner` from the same three collaborators the `Evaluator` already receives, then pass it to `NewService`. Replace:

```go
	scheduleSvc := schedule.NewService(scheduleRepo, scheduleCanTrigger{publishers: publishedRepo})
```

with:

```go
	// Build the shared run-now fire core from the SAME collaborators the
	// evaluator uses (engine Trigger, instance-repo ActiveRunChecker, identity
	// resolver), and wire it into the management service so REST + MCP expose
	// run-now. The Runner never advances LastFiredAt (run-now is out of cadence).
	scheduleRunner := schedule.NewRunner(
		eng,
		scheduleIdentityResolver{users: userRepo, groups: groupCfg},
		instanceRepo,
	)
	scheduleSvc := schedule.NewService(
		scheduleRepo,
		scheduleCanTrigger{publishers: publishedRepo},
		schedule.WithFirer(scheduleRunner),
	)
```

**Verify the argument order matches `NewRunner(t Trigger, i IdentityResolver, a ActiveRunChecker)`** from Task 1: `eng` is the `Trigger`, `scheduleIdentityResolver{...}` is the `IdentityResolver`, `instanceRepo` is the `ActiveRunChecker`. Confirm the exact identifiers in scope:

```bash
grep -n "scheduleIdentityResolver{\|instanceRepo\b\|eng\b" ~/Repos/arbitron/cmd/arbitron/main.go | sed -n '1,20p'
```

Match the `scheduleIdentityResolver{users: ..., groups: ...}` literal exactly as it appears in the `NewEvaluator` call a few lines above (do not invent field names).

- [ ] **Step 15: Build the binary to confirm wiring compiles**

Run: `go build ./cmd/arbitron/`
Expected: success, no errors. The `_ = scheduleRunner` is not needed because it is consumed by `WithFirer`.

- [ ] **Step 16: Run the full test suite + vet for the whole module**

Run: `go test ./... && go vet ./...`
Expected: PASS across the module (domain, http, mcp, and any composition-root tests). No regressions.

- [ ] **Step 17: Commit**

```bash
git add cmd/arbitron/main.go
git commit -m "feat(schedule): wire run-now Runner into management service at composition root

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Definition of done (Task 2)

- REST: `POST /schedules/{id}/run` exists and is registered; body `{"skipWhenBusy": bool}` optional. 202 (fired, `{instanceId, fired:true}`), 200 (skipped, `{fired:false}`), 400 (bad id / bad body), 403 (non-owner / no identity), 404 (missing), 409 (`ErrCreatorUnresolved`), 503 (`ErrRunNowUnavailable`).
- MCP: `schedule_run_now` registered only when `Service.HasFirer()`; args `id` (required UUID), `skipWhenBusy` (optional bool); result `{fired}` (+`instanceId` when fired); sentinels mapped via `scheduleResultError` (forbidden/not found/conflict/failed).
- REST and MCP share `Service.RunNow`, so they cannot drift.
- Composition root builds a `schedule.Runner` from `eng` / `scheduleIdentityResolver` / `instanceRepo` and passes it via `WithFirer`; `go build ./cmd/arbitron/` and `go test ./...` are green.

---

## Cross-task self-review (spec coverage)

| Spec acceptance criterion (Capability A) | Covered by |
|---|---|
| Run-now starts the workflow immediately with the schedule's variables, attributed to creator + department | Task 1 Runner happy path + `TriggerWithDepartment` call; Task 2 wiring |
| Default collision fires anyway while a prior run is active | Task 1 `TestRunner_Fire_DefaultFiresWhileBusy`; `TestService_RunNow_HappyPath` |
| skip-when-busy + prior run active ⇒ no run, reported skipped | Task 1 `TestRunner_Fire_SkipWhenBusy_Active`, `TestService_RunNow_Skipped`; Task 2 REST 200 / MCP `fired:false` |
| Caller who may not start the schedule is denied; admin may run any | Task 1 `TestService_RunNow_NonOwnerForbidden` / `_AdminRunsOthersSchedule`; Task 2 REST 403 / MCP forbidden |
| Reachable over REST and MCP; absent as a dashboard control | Task 2a (REST) + Task 2b (MCP); no dashboard control added (out of scope) |
| Run-now occurrence attributed to its schedule for run history | Task 1 Phase-3 origin seam (comment) — stamping lands in Phase 3; creator attribution via injected identity is live now |
| LastFiredAt not advanced by run-now (cron cadence preserved) | Task 1 Runner holds no repo; `TestRunner_Fire_*` assert no `SetLastFired`; refactor-safety keeps evaluator advancing |
