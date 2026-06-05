# RBAC Phase 3 — Task 6: Adapter Surface (HTTP + MCP department param, 403 mapping, response fields)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose the optional per-trigger `department` parameter on the HTTP trigger endpoint and the MCP `instance_trigger` tool; map the new `canIntervene` denial (`ErrForbidden`) to HTTP 403 on intervention handlers; and surface `department` + `visibility` on instance read responses so the UI can label peer-visible runs.

**Architecture:** The HTTP `WorkflowHandler.Trigger` and MCP `instanceTriggerHandler` call the engine; they switch from `Trigger` to `TriggerWithDepartment` (Task 2), threading an optional `department`. The HTTP instance intervention handlers (`Pause/Resume/Cancel/Retry/Rerun/SetPriority`) already map errors via `mapEngineError`; that mapper gains an `ErrForbidden → 403` arm. The `InstanceService` interface gains a `TriggerWithDepartment` method (or the workflow trigger path uses it directly). Read DTOs (`instanceResponse`, `summaryDTO`) gain `department` + `visibility` fields, and the validation error from an out-of-department override maps to 400.

**Tech Stack:** Go 1.23+, chi, mcp-go, testify.

**Cross-task dependencies:**
- Task 2: `engine.TriggerWithDepartment`, `engine.ErrInvalidDepartmentOverride`.
- Task 4: `instance.ErrForbidden` returned by intervention paths.
- Task 1: `Instance.Department`/`Instance.Visibility`.

---

### Task 6.1: Map `ErrForbidden` → 403 in the instance error mapper

**Files:**
- Modify: `adapter/http/instance_handler.go` (`mapEngineError`, `mapEngineErrorCode`)
- Test: `adapter/http/instance_handler_forbidden_test.go`

- [ ] **Step 1: Write the failing test**

Create `adapter/http/instance_handler_forbidden_test.go`:

```go
package http

import (
	"net/http"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
)

func TestMapEngineError_Forbidden_403(t *testing.T) {
	assert.Equal(t, http.StatusForbidden, mapEngineError(instance.ErrForbidden))
}

func TestMapEngineErrorCode_Forbidden(t *testing.T) {
	assert.Equal(t, "FORBIDDEN", mapEngineErrorCode(instance.ErrForbidden))
}

// Not-found still maps to 404 (existence-hiding posture preserved).
func TestMapEngineError_NotFound_Still404(t *testing.T) {
	assert.Equal(t, http.StatusNotFound, mapEngineError(instance.ErrNotFound))
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./adapter/http/ -run 'TestMapEngineError_Forbidden|TestMapEngineErrorCode_Forbidden' -v`
Expected: FAIL — `ErrForbidden` currently falls through to 500/`INTERNAL_ERROR`.

- [ ] **Step 3: Add the 403 arm to `mapEngineError`**

In `adapter/http/instance_handler.go`, in `mapEngineError`, before the final `return http.StatusInternalServerError`, add:

```go
	if errors.Is(err, instance.ErrForbidden) {
		return http.StatusForbidden
	}
```

- [ ] **Step 4: Add the `FORBIDDEN` code to `mapEngineErrorCode`**

In `mapEngineErrorCode`, add a case to the switch:

```go
	case http.StatusForbidden:
		return "FORBIDDEN"
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./adapter/http/ -run 'TestMapEngineError|TestMapEngineErrorCode' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add adapter/http/instance_handler.go adapter/http/instance_handler_forbidden_test.go
git commit -m "feat(rbac): map instance ErrForbidden to HTTP 403 on intervention handlers"
```

---

### Task 6.2: Surface `department` + `visibility` on read responses

**Files:**
- Modify: `adapter/http/instance_handler.go` (`instanceResponse` + `toInstanceResponse`; `summaryDTO` + `toSummaryDTO`)
- Modify: `domain/instance/instance.go` (`InstanceSummary` gains `Department`/`Visibility`)
- Modify: `adapter/sqlite/queries/instances.sql` (`ListInstances` SELECT adds the two columns), regenerate, map in repo `List`
- Test: `adapter/http/instance_handler_response_test.go`, extend `adapter/sqlite/instance_repo_list_visibility_test.go`

- [ ] **Step 1: Write the failing response test**

Create `adapter/http/instance_handler_response_test.go`:

```go
package http

import (
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestToInstanceResponse_IncludesVisibility(t *testing.T) {
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "wf",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
		Department: "engineering", Visibility: instance.VisibilityDepartment,
	}
	resp := toInstanceResponse(inst)
	assert.Equal(t, "engineering", resp.Department)
	assert.Equal(t, "department", resp.Visibility)
}

func TestToSummaryDTO_IncludesVisibility(t *testing.T) {
	s := instance.InstanceSummary{
		ID: uuid.New(), WorkflowName: "wf", State: instance.StateRunning,
		Priority: instance.PriorityNormal, Department: "engineering",
		Visibility: instance.VisibilityDepartment,
	}
	dto := toSummaryDTO(s)
	assert.Equal(t, "engineering", dto.Department)
	assert.Equal(t, "department", dto.Visibility)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./adapter/http/ -run 'TestToInstanceResponse_IncludesVisibility|TestToSummaryDTO_IncludesVisibility' -v`
Expected: FAIL — `resp.Department` undefined; `InstanceSummary` has no `Department`/`Visibility`.

- [ ] **Step 3: Add `Department`/`Visibility` to `InstanceSummary`**

In `domain/instance/instance.go`, in `InstanceSummary`, add:

```go
	Department string     `json:"department,omitempty"`
	Visibility Visibility `json:"visibility,omitempty"`
```

- [ ] **Step 4: Add the fields to the HTTP DTOs**

In `adapter/http/instance_handler.go`:

In `instanceResponse`, add:
```go
	Department string `json:"department,omitempty"`
	Visibility string `json:"visibility,omitempty"`
```
In `toInstanceResponse`, set them on `resp`:
```go
	resp.Department = inst.Department
	resp.Visibility = string(inst.Visibility)
```

In `summaryDTO`, add:
```go
	Department string `json:"department,omitempty"`
	Visibility string `json:"visibility,omitempty"`
```
In `toSummaryDTO`, set them on `dto`:
```go
	dto.Department = s.Department
	dto.Visibility = string(s.Visibility)
```

- [ ] **Step 5: Carry the columns through the list query + repo**

In `adapter/sqlite/queries/instances.sql`, in the `ListInstances` SELECT list (from Task 3.3), add `department, visibility`:

```sql
SELECT id, workflow_name, state, priority, current_node_id, created_at, updated_at, completed_at, queued_at, department, visibility
```

Regenerate: `cd adapter/sqlite && sqlc generate && cd -` (the `ListInstancesRow` gains `Department sql.NullString` + `Visibility string`).

In `adapter/sqlite/instance_repo.go`, in `List`, set the summary fields in the row loop:

```go
		summaries[i] = instance.InstanceSummary{
			// ... existing fields ...
			Department: nullStringToString(row.Department),
			Visibility: instance.Visibility(row.Visibility),
		}
```

(`nullStringToString` was added in Task 1.3.)

- [ ] **Step 6: Append the repo list test for the surfaced fields**

Append to `adapter/sqlite/instance_repo_list_visibility_test.go`:

```go
func TestInstanceRepoList_SurfacesDepartmentVisibility(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	id := mkInst(t, repo, owner, instance.VisibilityDepartment, "engineering")

	got, err := repo.List(context.Background(), instance.ListFilters{Limit: 100})
	require.NoError(t, err)
	var found bool
	for _, s := range got {
		if s.ID == id {
			found = true
			assert.Equal(t, "engineering", s.Department)
			assert.Equal(t, instance.VisibilityDepartment, s.Visibility)
		}
	}
	require.True(t, found)
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./adapter/http/ -run 'TestToInstanceResponse|TestToSummaryDTO' -v && go test ./adapter/sqlite/ -run 'TestInstanceRepoList_Surfaces' -v`
Expected: PASS.

- [ ] **Step 8: Build + full suites for touched packages**

Run: `go build ./... && go test ./domain/instance/... ./adapter/http/... ./adapter/sqlite/...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add domain/instance/instance.go adapter/http/instance_handler.go \
        adapter/sqlite/queries/instances.sql adapter/sqlite/generated/ \
        adapter/sqlite/instance_repo.go \
        adapter/http/instance_handler_response_test.go \
        adapter/sqlite/instance_repo_list_visibility_test.go
git commit -m "feat(rbac): surface department+visibility on instance read and list responses"
```

---

### Task 6.3: HTTP trigger `department` param

**Files:**
- Modify: `adapter/http/workflow_handler.go` (`triggerRequest` + `Trigger`)
- Modify: the `WorkflowHandler`'s engine service interface (the trigger method it calls) to use `TriggerWithDepartment` — locate the interface in `adapter/http/workflow_handler.go`
- Test: `adapter/http/workflow_handler_trigger_dept_test.go`

- [ ] **Step 1: Write the failing handler test**

Create `adapter/http/workflow_handler_trigger_dept_test.go`. Mirror the existing workflow-handler test setup (find a sibling test that constructs a `WorkflowHandler` with a fake service; reuse its fake). The fake's trigger method records the department it was handed:

```go
package http

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTriggerService records the department passed to the trigger call.
type fakeTriggerService struct {
	gotDepartment string
	gotName       string
}

func (f *fakeTriggerService) TriggerWithDepartment(_ context.Context, name string, _ map[string]any, _ string, department string) (uuid.UUID, bool, error) {
	f.gotName = name
	f.gotDepartment = department
	return uuid.New(), true, nil
}

// ... implement the rest of the WorkflowService interface with panics/no-ops as
// the sibling fake does ...

func newTriggerReq(t *testing.T, name, body string) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/"+name+"/trigger", bytes.NewBufferString(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", name)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	return r, httptest.NewRecorder()
}

// The optional department field is threaded to the engine.
func TestTrigger_PassesDepartment(t *testing.T) {
	svc := &fakeTriggerService{}
	h := NewWorkflowHandler(/* deps + */ svc) // match the real constructor
	r, w := newTriggerReq(t, "wf", `{"department":"engineering"}`)
	h.Trigger(w, r)
	require.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "engineering", svc.gotDepartment)
}

// An out-of-department override maps to 400 (validation, not 403/500).
func TestTrigger_InvalidDepartment_400(t *testing.T) {
	svc := &errTriggerService{err: engine.ErrInvalidDepartmentOverride}
	h := NewWorkflowHandler(/* deps + */ svc)
	r, w := newTriggerReq(t, "wf", `{"department":"finance"}`)
	h.Trigger(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
```

Add `errTriggerService` returning the override error from `TriggerWithDepartment`. Adapt `NewWorkflowHandler(...)` and the `WorkflowService` interface to whatever the file actually declares (read it first).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./adapter/http/ -run 'TestTrigger_PassesDepartment|TestTrigger_InvalidDepartment' -v`
Expected: FAIL — `triggerRequest` has no `Department`; the service interface method is `Trigger`, not `TriggerWithDepartment`; the override error is not mapped to 400.

- [ ] **Step 3: Add `Department` to `triggerRequest`**

In `adapter/http/workflow_handler.go`, in `triggerRequest` (lines 127–130), add:

```go
	Department string `json:"department"`
```

- [ ] **Step 4: Switch the service interface + call to `TriggerWithDepartment`**

In `adapter/http/workflow_handler.go`, change the trigger method on the `WorkflowService` interface (the one the handler calls) from:

```go
	Trigger(ctx context.Context, name string, variables map[string]any, priority string) (uuid.UUID, bool, error)
```

to:

```go
	TriggerWithDepartment(ctx context.Context, name string, variables map[string]any, priority string, department string) (uuid.UUID, bool, error)
```

and update the call in `Trigger`:

```go
	id, started, err := h.service.TriggerWithDepartment(r.Context(), name, req.Variables, req.Priority, req.Department)
```

(`*engine.Engine` satisfies this because Task 2 added `TriggerWithDepartment`.)

- [ ] **Step 5: Map the override validation error to 400**

In `adapter/http/workflow_handler.go`, in `Trigger`'s error handling, add a branch before the generic 500:

```go
		if errors.Is(err, engine.ErrInvalidDepartmentOverride) {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
```

Import `github.com/arbitron/arbitron/domain/engine` in the handler if not already imported.

- [ ] **Step 6: Run the handler tests to verify they pass**

Run: `go test ./adapter/http/ -run 'TestTrigger_PassesDepartment|TestTrigger_InvalidDepartment' -v`
Expected: PASS.

- [ ] **Step 7: Run the full http package — confirm no other trigger caller broke**

Run: `go test ./adapter/http/...`
Expected: PASS. If other `WorkflowService` fakes in the package implement `Trigger`, rename their method to `TriggerWithDepartment` (add the trailing `department string` param) so they satisfy the updated interface.

- [ ] **Step 8: Commit**

```bash
git add adapter/http/workflow_handler.go adapter/http/workflow_handler_trigger_dept_test.go
git commit -m "feat(rbac): optional department param on HTTP workflow trigger; override error -> 400"
```

---

### Task 6.4: MCP `instance_trigger` department param

**Files:**
- Modify: `adapter/mcp/tools_instance.go` (`instance_trigger` tool def + `instanceTriggerHandler`)
- Test: `adapter/mcp/tools_instance_trigger_dept_test.go`

- [ ] **Step 1: Write the failing MCP handler test**

Create `adapter/mcp/tools_instance_trigger_dept_test.go`. Mirror an existing MCP tool test for the harness (`grep -rn "instanceTriggerHandler\|func.*Server.*test\|deps.Engine" adapter/mcp/*_test.go` to find the fake engine). The fake engine records the department arg:

```go
package mcp

import (
	"context"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The instance_trigger handler threads the optional department arg to the engine.
func TestInstanceTrigger_PassesDepartment(t *testing.T) {
	fakeEng := &fakeMcpEngine{} // implements the engine surface deps.Engine needs
	srv := newTestServer(fakeEng) // match the existing MCP test harness constructor

	req := gomcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"workflowName": "wf",
		"department":   "engineering",
	}
	res, err := instanceTriggerHandler(srv)(context.Background(), req)
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Equal(t, "engineering", fakeEng.gotDepartment)
}
```

The `fakeMcpEngine.TriggerWithDepartment(ctx, name, vars, priority, department)` records `gotDepartment` and returns `(uuid.New(), true, nil)`. Adapt to the real MCP test harness (server constructor + engine interface) found in the existing tests.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./adapter/mcp/ -run TestInstanceTrigger_PassesDepartment -v`
Expected: FAIL — the tool has no `department` arg; the handler calls `Trigger`, not `TriggerWithDepartment`.

- [ ] **Step 3: Add the `department` arg to the tool definition**

In `adapter/mcp/tools_instance.go`, in the `instance_trigger` tool (lines 39–46), add a string option:

```go
			gomcp.WithString("department", gomcp.Description("Optional: stamp the run under one of your departments (must be one you belong to)")),
```

- [ ] **Step 4: Thread the arg through the handler**

In `instanceTriggerHandler`, read the arg and call `TriggerWithDepartment`:

```go
		department, _ := args["department"].(string)
		// ... existing wfName/vars/priority parsing ...
		id, queued, err := srv.deps.Engine.TriggerWithDepartment(ctx, wfName, vars, priority, department)
```

Update the engine interface declared in the MCP deps (the type behind `srv.deps.Engine`) to include `TriggerWithDepartment(ctx, string, map[string]any, string, string) (uuid.UUID, bool, error)`. If the deps interface still references `Trigger`, replace that method (or add the new one and switch the call). Find it with `grep -rn "Trigger(ctx" adapter/mcp/*.go`.

- [ ] **Step 5: Run the MCP test to verify it passes**

Run: `go test ./adapter/mcp/ -run TestInstanceTrigger_PassesDepartment -v`
Expected: PASS.

- [ ] **Step 6: Run the full mcp package**

Run: `go test ./adapter/mcp/...`
Expected: PASS. Update any other fake engine in the package to the new interface method.

- [ ] **Step 7: Build the whole module**

Run: `go build ./...`
Expected: success — all trigger call sites now use `TriggerWithDepartment`.

- [ ] **Step 8: Commit**

```bash
git add adapter/mcp/tools_instance.go adapter/mcp/tools_instance_trigger_dept_test.go
git commit -m "feat(rbac): optional department param on MCP instance_trigger tool"
```

---

## Definition of done (Task 6)

- `ErrForbidden` maps to HTTP 403 (`FORBIDDEN`) on intervention handlers; `ErrNotFound` still 404.
- `department` + `visibility` appear on instance Get and List responses.
- HTTP trigger accepts an optional `department`; an out-of-department override returns 400.
- MCP `instance_trigger` accepts an optional `department`, threaded to `TriggerWithDepartment`.
- `go build ./...` and the http/mcp/sqlite/instance test suites are green.
