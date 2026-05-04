# Phase 3 Task 1: REST API Handlers

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement all REST API handlers using chi: workflow endpoints (list, get, validate, trigger), instance endpoints (list, get, pause, resume, cancel, retry, rerun, trace, variables), and system endpoints (health, config).

**Architecture:** Handlers are thin adapters in `adapter/http/`. They translate HTTP requests into domain calls and domain results into HTTP responses. No business logic in handlers.

**Tech Stack:** Go 1.23+, chi, httptest for testing

---

### Task 1: HTTP Server Setup & Error Helpers

**Files:**
- Create: `adapter/http/server.go`
- Create: `adapter/http/errors.go`
- Create: `adapter/http/errors_test.go`

- [ ] **Step 1: Install chi**

```bash
go get github.com/go-chi/chi/v5
go get github.com/go-chi/chi/v5/middleware
```

- [ ] **Step 2: Create error response helper**

Create `adapter/http/errors.go`:

```go
package http

import (
	"encoding/json"
	"net/http"
)

type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{Error: message, Code: code})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
```

- [ ] **Step 3: Write tests for error helpers**

Create `adapter/http/errors_test.go`:

```go
package http_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
)

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	adapthttp.WriteError(w, 404, "NOT_FOUND", "workflow not found")

	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Error("expected application/json content type")
	}

	var resp adapthttp.ErrorResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Code != "NOT_FOUND" {
		t.Errorf("expected NOT_FOUND, got %s", resp.Code)
	}
	if resp.Error != "workflow not found" {
		t.Errorf("expected 'workflow not found', got %s", resp.Error)
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	adapthttp.WriteJSON(w, 200, map[string]string{"status": "ok"})

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("expected ok, got %s", resp["status"])
	}
}
```

Note: The exported function names should be `WriteError` and `WriteJSON` in the implementation. Update `errors.go` to export them.

- [ ] **Step 4: Run tests**

```bash
go test ./adapter/http/ -v -run TestWrite
```

Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add adapter/http/errors* adapter/http/server.go
git commit -m "feat: HTTP error helpers and server setup"
```

---

### Task 2: Workflow Handlers

**Files:**
- Create: `adapter/http/workflow_handler.go`
- Create: `adapter/http/workflow_handler_test.go`

- [ ] **Step 6: Write failing tests for workflow handlers**

Create `adapter/http/workflow_handler_test.go` using `httptest` with mock repositories. Tests:

- `GET /api/v1/workflows` → returns JSON array of workflow summaries
- `GET /api/v1/workflows` with empty workspace → returns empty array
- `GET /api/v1/workflows/{name}` → returns full workflow definition
- `GET /api/v1/workflows/{name}` for missing workflow → 404 with error body
- `POST /api/v1/workflows/{name}/validate` for valid workflow → 200 `{valid: true, errors: []}`
- `POST /api/v1/workflows/{name}/validate` for invalid workflow → 200 `{valid: false, errors: [...]}`
- `POST /api/v1/workflows/{name}/validate` for missing workflow → 404
- `POST /api/v1/workflows/{name}/trigger` with valid body → 201 with instance ID
- `POST /api/v1/workflows/{name}/trigger` with missing body fields → 400
- `POST /api/v1/workflows/{name}/trigger` for missing workflow → 404

- [ ] **Step 7: Implement workflow handlers**

Create `adapter/http/workflow_handler.go` with handler functions that accept the workflow repository, engine, and file reader as dependencies. Wire into the chi router.

- [ ] **Step 8: Run tests**

```bash
go test ./adapter/http/ -v -run TestWorkflow
```

Expected: all PASS

- [ ] **Step 9: Commit**

```bash
git add adapter/http/workflow_handler*
git commit -m "feat: workflow REST API handlers"
```

---

### Task 3: Instance Handlers

**Files:**
- Create: `adapter/http/instance_handler.go`
- Create: `adapter/http/instance_handler_test.go`

- [ ] **Step 10: Write failing tests for instance handlers**

Create `adapter/http/instance_handler_test.go`. Tests:

- `GET /api/v1/instances` → returns list, respects filters/pagination
- `GET /api/v1/instances` with no instances → empty array
- `GET /api/v1/instances/{id}` → returns full details
- `GET /api/v1/instances/{id}` with bad UUID → 400
- `GET /api/v1/instances/{id}` not found → 404
- `POST /api/v1/instances/{id}/pause` → 200
- `POST /api/v1/instances/{id}/pause` wrong state → 409
- `POST /api/v1/instances/{id}/resume` → 200
- `POST /api/v1/instances/{id}/cancel` → 200
- `POST /api/v1/instances/{id}/retry` with variable edits → 200
- `POST /api/v1/instances/{id}/retry` without body → 200
- `POST /api/v1/instances/{id}/retry` wrong state → 409
- `POST /api/v1/instances/{id}/rerun` → 201 with new ID
- `POST /api/v1/instances/{id}/rerun` wrong state → 409
- `GET /api/v1/instances/{id}/trace` → ordered node execution records
- `GET /api/v1/instances/{id}/trace/{nodeId}` → single record, 404 if missing
- `GET /api/v1/instances/{id}/variables` → current variables
- `GET /api/v1/instances/{id}/variables/history` → ordered snapshots

- [ ] **Step 11: Implement instance handlers**

Create `adapter/http/instance_handler.go` with all handlers. Parse UUID from chi URL params. Map domain errors to HTTP status codes: not found → 404, invalid state → 409.

- [ ] **Step 12: Run tests**

```bash
go test ./adapter/http/ -v -run TestInstance
```

Expected: all PASS

- [ ] **Step 13: Create the full router in server.go**

Update `adapter/http/server.go` to wire all handlers:

```go
package http

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

func NewRouter(
	eng *engine.Engine,
	workflows workflow.WorkflowRepository,
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	snapshots instance.VariableSnapshotRepository,
	fileReader engine.FileReader,
	eventBus engine.EventBus,
) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	wh := NewWorkflowHandler(eng, workflows, fileReader)
	ih := NewInstanceHandler(eng, instances, nodeExecs, snapshots)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", healthHandler)

		r.Route("/workflows", func(r chi.Router) {
			r.Get("/", wh.List)
			r.Get("/{name}", wh.Get)
			r.Post("/{name}/validate", wh.Validate)
			r.Post("/{name}/trigger", wh.Trigger)
		})

		r.Route("/instances", func(r chi.Router) {
			r.Get("/", ih.List)
			r.Get("/{id}", ih.Get)
			r.Post("/{id}/pause", ih.Pause)
			r.Post("/{id}/resume", ih.Resume)
			r.Post("/{id}/cancel", ih.Cancel)
			r.Post("/{id}/retry", ih.Retry)
			r.Post("/{id}/rerun", ih.Rerun)
			r.Get("/{id}/trace", ih.GetTrace)
			r.Get("/{id}/trace/{nodeId}", ih.GetNodeTrace)
			r.Get("/{id}/variables", ih.GetVariables)
			r.Get("/{id}/variables/history", ih.GetVariableHistory)
			// WebSocket — Phase 3 Task 2
		})
	})

	return r
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, 200, map[string]string{"status": "ok"})
}
```

- [ ] **Step 14: Commit**

```bash
git add adapter/http/
git commit -m "feat: instance REST API handlers and full router"
```
