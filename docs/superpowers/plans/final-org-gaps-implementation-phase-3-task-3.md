# Final Org Gaps — Phase 3, Task 3: Schedule List Filtered by Workflow (Service + REST)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the dashboard fetch the schedules for one workflow. Add an optional workflow-name filter to `schedule.Service.List`, threading it through `ListFilters.WorkflowName` while keeping the owner-or-admin creator scoping SERVICE-set from identity (never from request input), and accept `GET /schedules?workflowName=<name>` in the REST handler.

**Architecture:** No new authz model — the existing owner-or-admin creator scoping is preserved exactly; the workflow name is an ADDITIONAL narrowing filter applied on top. The service signature change is backward-compatible via a variadic options pattern (so existing `svc.List(ctx)` callers and the MCP surface are untouched). The SQLite repo already supports `ListFilters.WorkflowName` (`adapter/sqlite/schedule_repo.go:90`), so no repo change is needed.

**Tech Stack:** Go, chi router, `schedule.Service`, `schedule.ListFilters`.

---

## DEPENDENCY

Independent of Phase 1, Phase 2, and Phase 3 Tasks 1–2. Can run in parallel with them. (It only touches `domain/schedule/service.go` and `adapter/http/schedule_handler.go`.)

## Context (existing code you must match)

- `domain/schedule/service.go` — `List(ctx)` (line 259) sets `filters.CreatorID` from identity for non-admins and passes empty filters to `s.repo.List`. `caller(ctx)` (line 145) reads identity; `authorize` is owner-or-admin.
- `domain/schedule/ports.go` — `ListFilters{ CreatorID *uuid.UUID; WorkflowName *string; Enabled *bool }` (line 12). `WorkflowName` is already present and already honored by the SQLite repo.
- `adapter/http/schedule_handler.go` — `List` (line 114) calls `h.svc.List(r.Context())` and renders `s.WireMap()`. Query params are read via `r.URL.Query()` elsewhere in the codebase (see other handlers).
- The MCP schedule tools also call `svc.List(ctx)` — they must keep compiling. A VARIADIC option keeps the existing call sites valid.

The SECURITY invariant (from `ports.go` and `service.go` comments): `CreatorID` is set by the service from identity, NEVER from request input. `WorkflowName`, by contrast, is a NON-security narrowing filter — safe to take from the request query because it only ever SHRINKS the already-creator-scoped result set. A non-admin asking for workflow X still only sees THEIR schedules for X.

---

### Task 1: Add an optional workflow filter to `Service.List`

**Files:**
- Modify: `domain/schedule/service.go` (`List`, line 259)
- Test: `domain/schedule/service_test.go` (add cases — inspect for the existing `List` tests + fake repo)

- [ ] **Step 1: Write the failing service tests (happy path, validation, authorization)**

Inspect `domain/schedule/service_test.go` for the fake `ScheduleRepository` and identity-context helpers it already uses for `List` tests. Add tests asserting:

```go
// Use a fake repo that records the ListFilters it receives and returns canned
// schedules. Build identities via the same helper existing List tests use.
//
// 1. Happy path (admin): svc.List(adminCtx, schedule.ListOptions{WorkflowName: "wf-a"})
//    -> repo received ListFilters{ CreatorID: nil, WorkflowName: ptr("wf-a") }.
// 2. Happy path (non-admin): svc.List(userCtx, schedule.ListOptions{WorkflowName: "wf-a"})
//    -> repo received ListFilters{ CreatorID: ptr(user.UserID), WorkflowName: ptr("wf-a") }.
//    (creator scoping still applied; workflow filter added.)
// 3. No options (back-compat): svc.List(userCtx) -> repo received
//    ListFilters{ CreatorID: ptr(user.UserID), WorkflowName: nil }.
// 4. Empty workflow name: svc.List(userCtx, schedule.ListOptions{WorkflowName: ""})
//    -> WorkflowName left nil (empty == "no filter", caller's full owner-scoped list).
// 5. No identity in ctx -> svc.List(ctx, ...) returns schedule.ErrForbidden (never leaks).
```

Implement these as real subtests against the fake repo capturing its received `ListFilters`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./domain/schedule/ -run ListForWorkflow -v` (name your tests accordingly)
Expected: FAIL — `ListOptions` undefined; `List` takes no options arg.

- [ ] **Step 3: Add `ListOptions` and the variadic `List` signature**

In `domain/schedule/service.go`, replace the `List` method (lines 257–270) with:

```go
// ListOptions are optional, NON-security narrowing filters for List. They only
// ever shrink the already-creator-scoped result set, so they are safe to take
// from request input (unlike CreatorID, which is service-set from identity).
type ListOptions struct {
	// WorkflowName, when non-empty, restricts the result to schedules for that
	// workflow. Empty means no workflow narrowing (the caller's full scoped list).
	WorkflowName string
}

// List returns the caller's own schedules (non-admin) or all (admin), optionally
// narrowed to one workflow. The creator filter is SERVICE-set from the identity,
// never from request input; the workflow filter (when provided) only shrinks
// that already-scoped set, so a non-admin still sees only THEIR schedules.
func (s *Service) List(ctx context.Context, opts ...ListOptions) ([]Schedule, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	var filters ListFilters
	if !c.IsAdmin() {
		uid := c.UserID
		filters.CreatorID = &uid
	}
	if len(opts) > 0 && opts[0].WorkflowName != "" {
		wf := opts[0].WorkflowName
		filters.WorkflowName = &wf
	}
	return s.repo.List(ctx, filters)
}
```

- [ ] **Step 4: Run to verify the service tests pass**

Run: `go test ./domain/schedule/ -run ListForWorkflow -v`
Expected: PASS (all five subtests).

- [ ] **Step 5: Confirm existing `svc.List(ctx)` callers still compile (MCP + handler)**

Run: `go build ./...`
Expected: exit 0. The variadic option means `svc.List(ctx)` is unchanged for the MCP tools and the current REST handler.

- [ ] **Step 6: Commit**

```bash
git add domain/schedule/service.go domain/schedule/service_test.go
git commit -m "feat(schedule): optional workflow-name filter on Service.List"
```

---

### Task 2: REST handler reads `?workflowName=` and passes it through

**Files:**
- Modify: `adapter/http/schedule_handler.go` (`List`, line 114)
- Test: `adapter/http/schedule_handler_test.go` (create or extend — inspect for an existing schedule handler test harness; if none, model on another handler test that builds a chi router + records the service call)

- [ ] **Step 7: Write the failing handler test**

Inspect `adapter/http/` for an existing schedule handler test or a pattern that wires a `schedule.Service` (backed by a fake repo) into the handler and issues requests. Add tests asserting:

```go
// 1. GET /schedules?workflowName=wf-a -> the underlying repo (via the real
//    Service) receives ListFilters with WorkflowName = "wf-a" and CreatorID
//    set from the request identity for a non-admin; response is 200 with a
//    {"schedules":[...]} body containing only wf-a schedules.
// 2. GET /schedules (no query) -> WorkflowName nil; full owner-scoped list.
// 3. GET /schedules?workflowName=  (empty value) -> treated as no filter.
// 4. GET /schedules?workflowName=does-not-exist -> 200 with {"schedules":[]}
//    (unknown workflow is an empty list, NOT an error).
```

Drive these through the real `schedule.Service` over a fake `ScheduleRepository` that records its `ListFilters` and returns canned data, with an injected request identity (reuse the package's identity-injection test helper).

- [ ] **Step 8: Run to verify it fails**

Run: `go test ./adapter/http/ -run ScheduleList -v`
Expected: FAIL — handler ignores `workflowName`; filter not threaded.

- [ ] **Step 9: Read the query param in the handler `List`**

In `adapter/http/schedule_handler.go`, replace `List` (lines 113–125) with:

```go
// List handles GET /schedules. An optional ?workflowName= query narrows the
// result to one workflow (read-only dashboard surface). The creator scoping is
// applied by the service from the request identity; the workflow filter only
// shrinks that already-scoped set, so it is safe to take from the query.
func (h *ScheduleHandler) List(w http.ResponseWriter, r *http.Request) {
	opts := schedule.ListOptions{
		WorkflowName: r.URL.Query().Get("workflowName"),
	}
	schedules, err := h.svc.List(r.Context(), opts)
	if err != nil {
		writeScheduleError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(schedules))
	for _, s := range schedules {
		out = append(out, s.WireMap())
	}
	WriteJSON(w, http.StatusOK, map[string]any{"schedules": out})
}
```

(An empty `workflowName` query yields `ListOptions{WorkflowName: ""}`, which the service treats as no filter — identical to the previous behavior.)

- [ ] **Step 10: Run to verify it passes**

Run: `go test ./adapter/http/ -run ScheduleList -v`
Expected: PASS.

- [ ] **Step 11: Commit**

```bash
git add adapter/http/schedule_handler.go adapter/http/schedule_handler_test.go
git commit -m "feat(http): GET /schedules?workflowName= filters schedules by workflow"
```

---

### Task 3: Full gate

- [ ] **Step 12: Build, vet, test**

Run: `go build ./... && go vet ./... && go test ./domain/schedule/... ./adapter/http/...`
Expected: all PASS, exit 0.

---

## Self-review checklist

- [ ] `CreatorID` is still set ONLY from identity (non-admin => own; admin => nil); the query param can never widen scope.
- [ ] Empty / missing `workflowName` => caller's full owner-scoped list (no filter), not an error.
- [ ] Unknown workflow name => empty `{"schedules":[]}`, HTTP 200, not an error.
- [ ] Existing `svc.List(ctx)` callers (MCP tools, any other caller) still compile via the variadic option.
- [ ] No-identity request => `ErrForbidden` (403), leaks nothing.
