# RBAC Phase 4 — Task 3: GET /api/v1/dashboard/stats handler + wiring

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose `GET /api/v1/dashboard/stats?departments=a,b&window=24h`, a thin HTTP handler that parses the query params, delegates to the `dashboard.Service` (which performs the department clamp), and serializes the `dashboard.Stats` envelope. Wire it into the router and `cmd/arbitron/main.go`.

**Architecture:** A new `adapter/http/dashboard_handler.go` `DashboardHandler` holding a small consumer-side interface (`dashboardStatsService`) — accept interfaces, return structs, matching `workflowStatsReader`. Param parsing (`departments` CSV → []string; `window` Go duration string → `time.Duration`) lives at the edge; security clamping stays in the domain service. The route sits inside the authenticated `/api/v1` group so the identity is present.

**Tech Stack:** Go, chi router, `domain/dashboard`, `domain/identity` (via the service), strict TDD with `httptest`.

**Dependencies:** Task 1 (`dashboard.Service`, `dashboard.Request`, `dashboard.Stats`) and Task 2 (a concrete `StatsRepository` to wire in `main.go`). No dependency on the frontend tasks.

---

## Context You Need

- `adapter/http/workflow_handler.go` shows the consumer-side interface pattern (`workflowStatsReader`) and the param-parsing style for `Stats` (parse query, default, delegate, `WriteJSON`).
- `adapter/http/errors.go`: `WriteJSON(w, status, data)` and `WriteError(w, status, code, message)`.
- `adapter/http/server.go`:
  - `RouterDeps` struct (add a `DashboardStats` field).
  - The `/api/v1` group (line ~223) already has `r.Use(d.Auth.Handler)`; register the route there next to `/system/config`.
- `cmd/arbitron/main.go` line ~700 constructs `RouterDeps{...}`; line ~259 constructs `instanceRepo := adaptsqlite.NewInstanceRepo(db)`. Add a `dashboardRepo` next to it and pass a `dashboard.NewService(dashboardRepo)` into deps.
- The handler must NOT itself read identity; the domain service does (`MustFromContext`). The handler only parses request input.

## File Structure

- Create `adapter/http/dashboard_handler.go` — `DashboardHandler` + `dashboardStatsService` interface + param parsing.
- Create `adapter/http/dashboard_handler_test.go` — TDD scenarios with a fake service.
- Modify `adapter/http/server.go` — add `RouterDeps.DashboardStats`, construct the handler, register the route.
- Modify `cmd/arbitron/main.go` — construct `DashboardRepo` + `dashboard.Service`, pass into `RouterDeps`.

---

### Task 3a: The handler + param parsing

**Files:**
- Create: `adapter/http/dashboard_handler.go`
- Test: `adapter/http/dashboard_handler_test.go`

- [ ] **Step 1: Write the failing test — happy path serializes the envelope and parses params**

Create `adapter/http/dashboard_handler_test.go`:

```go
package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/dashboard"
)

// fakeStatsService captures the parsed Request and returns canned Stats.
type fakeStatsService struct {
	got  dashboard.Request
	out  dashboard.Stats
	err  error
}

func (f *fakeStatsService) GetStats(_ context.Context, req dashboard.Request) (dashboard.Stats, error) {
	f.got = req
	return f.out, f.err
}

func TestDashboardHandler_ParsesParamsAndSerializesEnvelope(t *testing.T) {
	svc := &fakeStatsService{out: dashboard.Stats{
		Departments: []string{"engineering"},
		Queue:       dashboard.QueueHealth{Running: 2, FailedNeedingAttention: 1},
		Outcomes:    dashboard.Outcomes{TotalRuns: 5, SuccessRate: 0.8},
	}}
	h := adapthttp.NewDashboardHandler(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stats?departments=engineering,sales&window=6h", nil)
	rec := httptest.NewRecorder()
	h.Stats(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"engineering", "sales"}, svc.got.Departments, "CSV split into slice")
	assert.Equal(t, 6*time.Hour, svc.got.Window, "Go duration parsed")

	var body dashboard.Stats
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, 2, body.Queue.Running)
	assert.Equal(t, 1, body.Queue.FailedNeedingAttention)
	assert.InDelta(t, 0.8, body.Outcomes.SuccessRate, 0.001)
}
```

- [ ] **Step 2: Run it to verify it fails to compile**

Run: `go test ./adapter/http/ -run TestDashboardHandler_ParsesParamsAndSerializesEnvelope -v`
Expected: FAIL — `undefined: adapthttp.NewDashboardHandler`.

- [ ] **Step 3: Implement the handler**

Create `adapter/http/dashboard_handler.go`:

```go
package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/dashboard"
)

// dashboardStatsService is the consumer-side interface the handler depends on
// (accept interfaces, return structs). The concrete *dashboard.Service from the
// domain satisfies it; tests inject a fake.
type dashboardStatsService interface {
	GetStats(ctx context.Context, req dashboard.Request) (dashboard.Stats, error)
}

// DashboardHandler serves the department-scoped operational dashboard stats.
type DashboardHandler struct {
	svc dashboardStatsService
}

// NewDashboardHandler builds the handler over the stats service.
func NewDashboardHandler(svc dashboardStatsService) *DashboardHandler {
	return &DashboardHandler{svc: svc}
}

// Stats handles GET /dashboard/stats. It parses the (unvalidated) query params
// and delegates to the service, which clamps departments to the caller's
// allowed set and the window to a sane range. The identity is read inside the
// service via context; the auth middleware guarantees it is present.
func (h *DashboardHandler) Stats(w http.ResponseWriter, r *http.Request) {
	req := dashboard.Request{
		Departments: parseDepartments(r.URL.Query().Get("departments")),
	}

	if raw := r.URL.Query().Get("window"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid window parameter: "+err.Error())
			return
		}
		req.Window = d
	}

	stats, err := h.svc.GetStats(r.Context(), req)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load dashboard stats")
		return
	}

	// Ensure JSON arrays serialize as [] not null for empty results.
	if stats.Throughput == nil {
		stats.Throughput = []dashboard.ThroughputBucket{}
	}
	if stats.PerWorkflow == nil {
		stats.PerWorkflow = []dashboard.WorkflowBreakdown{}
	}
	if stats.Departments == nil {
		stats.Departments = []string{}
	}

	WriteJSON(w, http.StatusOK, stats)
}

// parseDepartments splits a comma-separated department list, trimming blanks.
// An empty/absent value yields nil so the service applies the union default.
func parseDepartments(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
```

- [ ] **Step 4: Run the happy-path test to verify it passes**

Run: `go test ./adapter/http/ -run TestDashboardHandler_ParsesParamsAndSerializesEnvelope -v`
Expected: PASS

- [ ] **Step 5: Add edge/error-path tests**

Append to `adapter/http/dashboard_handler_test.go`:

```go
func TestDashboardHandler_InvalidWindowIs400(t *testing.T) {
	svc := &fakeStatsService{}
	h := adapthttp.NewDashboardHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stats?window=notaduration", nil)
	rec := httptest.NewRecorder()
	h.Stats(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardHandler_OmittedDepartmentsAreNil(t *testing.T) {
	svc := &fakeStatsService{}
	h := adapthttp.NewDashboardHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stats", nil)
	rec := httptest.NewRecorder()
	h.Stats(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, svc.got.Departments, "absent param ⇒ nil ⇒ service applies union default")
	assert.Zero(t, svc.got.Window, "absent window ⇒ zero ⇒ service applies DefaultWindow")
}

func TestDashboardHandler_EmptyResultsSerializeAsArrays(t *testing.T) {
	svc := &fakeStatsService{out: dashboard.Stats{}} // all nil slices
	h := adapthttp.NewDashboardHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stats", nil)
	rec := httptest.NewRecorder()
	h.Stats(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"throughput":[]`)
	assert.Contains(t, body, `"perWorkflow":[]`)
	assert.Contains(t, body, `"departments":[]`)
}

func TestDashboardHandler_ServiceErrorIs500(t *testing.T) {
	svc := &fakeStatsService{err: context.DeadlineExceeded}
	h := adapthttp.NewDashboardHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stats", nil)
	rec := httptest.NewRecorder()
	h.Stats(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
```

- [ ] **Step 6: Run the handler test suite**

Run: `go test ./adapter/http/ -run TestDashboardHandler -v`
Expected: PASS (all scenarios green)

- [ ] **Step 7: Commit the handler**

```bash
git add adapter/http/dashboard_handler.go adapter/http/dashboard_handler_test.go
git commit -m "feat(dashboard): GET /dashboard/stats handler (thin param parse + delegate)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3b: Router wiring

**Files:**
- Modify: `adapter/http/server.go`

- [ ] **Step 8: Add the dependency field to RouterDeps**

In `adapter/http/server.go`, inside the `RouterDeps` struct (after the `Users` field, before the closing brace), add:

```go
	// DashboardStats powers GET /api/v1/dashboard/stats. The service clamps
	// requested departments to the caller's effective set (admins exempt). Nil
	// disables the route (legacy/tests).
	DashboardStats dashboardStatsService
```

- [ ] **Step 9: Register the route inside the authenticated /api/v1 group**

In `NewRouterFromDeps`, inside the `r.Route("/api/v1", ...)` block, after the `r.Put("/system/config/teams", ...)` line (the system endpoints), add:

```go
		// Department dashboard stats (Phase 4). Inside the auth group so the
		// service reads the caller identity from context for the department clamp.
		if d.DashboardStats != nil {
			dashHandler := NewDashboardHandler(d.DashboardStats)
			r.Get("/dashboard/stats", dashHandler.Stats)
		}
```

> NOTE: the local variable `dashHandler` here is the API stats handler, distinct from the SPA `d.DashboardHandler` field used at the bottom of the file. If shadowing concerns the linter, name it `dashStatsHandler`.

- [ ] **Step 10: Build to confirm wiring compiles**

Run: `go build ./adapter/http/ && go test ./adapter/http/ -run TestDashboard -v`
Expected: exit 0; handler tests still PASS.

- [ ] **Step 11: Commit the wiring**

```bash
git add adapter/http/server.go
git commit -m "feat(dashboard): wire /dashboard/stats route into authenticated /api/v1 group

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3c: main.go composition

**Files:**
- Modify: `cmd/arbitron/main.go`

- [ ] **Step 12: Construct the dashboard repo + service**

In `cmd/arbitron/main.go`, near line 259 where `instanceRepo := adaptsqlite.NewInstanceRepo(db)` is constructed, add (using the package alias already in use for the sqlite adapter — grep `adaptsqlite "` or the actual alias at the top of the file first):

```go
	dashboardRepo := adaptsqlite.NewDashboardRepo(db)
	dashboardSvc := dashboard.NewService(dashboardRepo)
```

Add the import `"github.com/arbitron/arbitron/domain/dashboard"` to the import block.

- [ ] **Step 13: Pass the service into RouterDeps**

In the `adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{...})` literal near line 700, add a field after `Users: ds.userSvc,`:

```go
		DashboardStats: dashboardSvc,
```

> If `db`, `dashboardRepo`, and `dashboardSvc` live in different scopes than the `RouterDeps` construction (e.g. a `ds` struct is assembled earlier), thread `dashboardSvc` through that struct the same way `instanceRepo` is exposed as `ds.instanceRepo`. Mirror whatever pattern the surrounding code uses; do not introduce a new global.

- [ ] **Step 14: Build the whole binary**

Run: `go build ./... && go vet ./...`
Expected: exit 0

- [ ] **Step 15: Run the backend test suite to confirm no regressions**

Run: `go test ./domain/dashboard/ ./adapter/sqlite/ ./adapter/http/`
Expected: PASS

- [ ] **Step 16: Commit the composition**

```bash
git add cmd/arbitron/main.go
git commit -m "feat(dashboard): compose DashboardRepo + service in main wiring

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review Notes

- Spec coverage: `GET /api/v1/dashboard/stats?departments=&window=` with the clamp delegated to the domain service (admins exempt) — endpoint + wiring done; clamp itself is tested in Task 1.
- Local mode: in local mode the auth middleware injects an admin identity, so `GetStats` runs unclamped over the default group — no selector implications; verified end-to-end once the frontend lands (Task 7) and via Task 1's admin test.
- TDD categories: happy-path (parse + serialize), edge (omitted params, empty-array serialization), error (bad window 400, service error 500). The authorization category (clamp) is owned by the service (Task 1) — the handler deliberately holds no authz logic.
- "Accept interfaces, return structs": `dashboardStatsService` is consumer-defined; `*dashboard.Service` satisfies it structurally.
