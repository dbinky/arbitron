# Phase 5 — Task 3: REST parent selector and hierarchy fields

**Goal:** Expose the three-state parent selector on `GET /api/v1/instances` and surface `parentInstanceId` / `childCount` on the list JSON, without widening what a caller may see.

**Architecture:** `InstanceHandler.List` parses a single `parent` query parameter through the shared `instance.ParseParentScope` decoder (so REST and MCP cannot drift) and sets the resulting narrowing on the filters it already builds. The handler continues to route through `Engine.ListEnriched`, which service-sets the owner/department constraints from the request identity — the boundary `AGENTS.md` states directly. No new authorization surface is introduced at any level.

**Tech Stack:** Go 1.25.5, `chi`, standard library `testing`/`net/http/httptest`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 3: REST parent selector

**Files:**
- Modify: `adapter/http/instance_handler.go:171-214` (`summaryDTO` + `toSummaryDTO`), `:233-272` (`enrichedSummaryDTO` + `toEnrichedSummaryDTO`), `:359-400` (`List`)
- Test:   `adapter/http/instance_list_parent_test.go`

---

- [x] **Step 1: Write the failing test**

Create `adapter/http/instance_list_parent_test.go`:

```go
package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingListService captures the filters the handler produced and returns a
// canned page. It HONOURS the parent narrowing, so a handler that dropped the
// parameter would fail these tests.
type recordingListService struct {
	InstanceService
	got  instance.ListFilters
	rows []instance.EnrichedInstanceSummary
}

func (s *recordingListService) ListEnriched(_ contextType, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
	s.got = filters
	out := []instance.EnrichedInstanceSummary{}
	for _, r := range s.rows {
		switch filters.ParentScope {
		case instance.ParentScopeRoots:
			if r.ParentInstanceID == nil {
				out = append(out, r)
			}
		case instance.ParentScopeChildren:
			if filters.ParentInstanceID != nil && r.ParentInstanceID != nil &&
				*r.ParentInstanceID == *filters.ParentInstanceID {
				out = append(out, r)
			}
		default:
			out = append(out, r)
		}
	}
	return out, nil
}

// doList issues GET /instances with the given raw query string against a
// handler wired to svc, and returns the decoded rows.
func doList(t *testing.T, h http.Handler, query string) (int, []map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances"+query, nil)
	req = req.WithContext(adminRequestContext(req.Context()))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	return rec.Code, rows
}

// ---------- happy path ----------

func TestInstanceListREST_RootsOnly(t *testing.T) {
	parent := uuid.New()
	root := instance.EnrichedInstanceSummary{
		InstanceSummary: instance.InstanceSummary{ID: parent, WorkflowName: "parent"},
	}
	child := instance.EnrichedInstanceSummary{
		InstanceSummary: instance.InstanceSummary{
			ID: uuid.New(), WorkflowName: "child", ParentInstanceID: &parent,
		},
	}
	svc := &recordingListService{rows: []instance.EnrichedInstanceSummary{root, child}}
	h := newListTestHandler(t, svc)

	code, rows := doList(t, h, "?parent=roots")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, instance.ParentScopeRoots, svc.got.ParentScope)
	assert.Nil(t, svc.got.ParentInstanceID)
	require.Len(t, rows, 1)
	assert.Equal(t, parent.String(), rows[0]["id"])
}

func TestInstanceListREST_ChildrenOfAParent(t *testing.T) {
	parent := uuid.New()
	childID := uuid.New()
	rows := []instance.EnrichedInstanceSummary{
		{InstanceSummary: instance.InstanceSummary{ID: parent, WorkflowName: "parent"}},
		{InstanceSummary: instance.InstanceSummary{ID: childID, WorkflowName: "child", ParentInstanceID: &parent}},
	}
	svc := &recordingListService{rows: rows}
	h := newListTestHandler(t, svc)

	code, got := doList(t, h, "?parent="+parent.String())
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, instance.ParentScopeChildren, svc.got.ParentScope)
	require.NotNil(t, svc.got.ParentInstanceID)
	assert.Equal(t, parent, *svc.got.ParentInstanceID)
	require.Len(t, got, 1)
	assert.Equal(t, childID.String(), got[0]["id"])
	assert.Equal(t, parent.String(), got[0]["parentInstanceId"],
		"a child row must carry its parent id so the dashboard can nest it")
}

// ---------- success ----------

func TestInstanceListREST_NoParentParamDoesNoNarrowing(t *testing.T) {
	parent := uuid.New()
	svc := &recordingListService{rows: []instance.EnrichedInstanceSummary{
		{InstanceSummary: instance.InstanceSummary{ID: parent}},
		{InstanceSummary: instance.InstanceSummary{ID: uuid.New(), ParentInstanceID: &parent}},
	}}
	h := newListTestHandler(t, svc)

	code, got := doList(t, h, "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, instance.ParentScopeAny, svc.got.ParentScope)
	assert.Len(t, got, 2)
}

func TestInstanceListREST_ChildCountIsSurfaced(t *testing.T) {
	svc := &recordingListService{rows: []instance.EnrichedInstanceSummary{
		{InstanceSummary: instance.InstanceSummary{ID: uuid.New()}, ChildCount: 3},
		{InstanceSummary: instance.InstanceSummary{ID: uuid.New()}},
	}}
	h := newListTestHandler(t, svc)

	code, got := doList(t, h, "")
	require.Equal(t, http.StatusOK, code)
	require.Len(t, got, 2)
	assert.Equal(t, float64(3), got[0]["childCount"], "the expander state must reach the client")
	_, present := got[1]["childCount"]
	assert.False(t, present, "a zero count is omitted so a childless row renders no expander")
}

func TestInstanceListREST_RootRowOmitsParentInstanceID(t *testing.T) {
	svc := &recordingListService{rows: []instance.EnrichedInstanceSummary{
		{InstanceSummary: instance.InstanceSummary{ID: uuid.New()}},
	}}
	h := newListTestHandler(t, svc)

	code, got := doList(t, h, "")
	require.Equal(t, http.StatusOK, code)
	_, present := got[0]["parentInstanceId"]
	assert.False(t, present, "a root row must omit parentInstanceId entirely")
}

func TestInstanceListREST_ParentComposesWithStateFilter(t *testing.T) {
	parent := uuid.New()
	svc := &recordingListService{rows: []instance.EnrichedInstanceSummary{}}
	h := newListTestHandler(t, svc)

	code, _ := doList(t, h, "?parent="+parent.String()+"&state=running")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, instance.ParentScopeChildren, svc.got.ParentScope)
	require.NotNil(t, svc.got.State)
	assert.Equal(t, instance.StateRunning, *svc.got.State,
		"the parent selector must compose with the existing filters, not replace them")
}

// ---------- failure ----------

// A malformed selector is REJECTED rather than silently degrading to an
// unfiltered list, which would show a caller far more than they asked for.
func TestInstanceListREST_MalformedParentIsRejected(t *testing.T) {
	svc := &recordingListService{rows: []instance.EnrichedInstanceSummary{}}
	h := newListTestHandler(t, svc)

	for _, raw := range []string{"not-a-uuid", "ROOTS", "root", "%20"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances?parent="+raw, nil)
		req = req.WithContext(adminRequestContext(req.Context()))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "parent=%q must be rejected", raw)
	}
}

// ---------- edge case ----------

// Naming a run the caller cannot see returns an EMPTY list, identical to a
// parent with no children — existence is not disclosed.
func TestInstanceListREST_UnknownParentReturnsEmptyNot404(t *testing.T) {
	svc := &recordingListService{rows: []instance.EnrichedInstanceSummary{}}
	h := newListTestHandler(t, svc)

	code, got := doList(t, h, "?parent="+uuid.New().String())
	require.Equal(t, http.StatusOK, code)
	assert.Empty(t, got)
}
```

> **Adapting the harness.** `adapter/http` already has an instance-handler test
> harness. Before writing the file, open `adapter/http/instance_handler_test.go`
> and reuse its existing constructs instead of the placeholders above:
> - replace `InstanceService` / `contextType` with the exact service interface
>   and `context.Context` the handler depends on (the file's existing
>   `mockInstanceService` or equivalent shows the shape);
> - replace `newListTestHandler(t, svc)` with the file's existing router/handler
>   builder;
> - replace `adminRequestContext(ctx)` with the file's existing helper for
>   attaching an admin identity to a request context.
> Run `grep -n "func newTest\|func setupHandler\|identity.WithIdentity" adapter/http/instance_handler_test.go`
> to find them. Do not introduce a parallel harness.

- [x] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./adapter/http/ -run TestInstanceListREST
```

Expected output:

```
--- FAIL: TestInstanceListREST_RootsOnly (0.00s)
    instance_list_parent_test.go:...: Not equal: expected: "roots" actual: ""
--- FAIL: TestInstanceListREST_ChildCountIsSurfaced (0.00s)
    instance_list_parent_test.go:...: the expander state must reach the client
FAIL	github.com/arbitron/arbitron/adapter/http	0.4s
```

- [x] **Step 3: Surface the hierarchy fields on the DTOs**

In `adapter/http/instance_handler.go`, add to `summaryDTO` (after `ScheduleID`):

```go
	ScheduleID    *string `json:"scheduleId,omitempty"`
	// ParentInstanceID is the spawning parent run; omitted entirely for a root
	// run so the dashboard can treat presence as "this row nests under something".
	ParentInstanceID *string `json:"parentInstanceId,omitempty"`
```

and in `toSummaryDTO`, beside the `ScheduleID` block:

```go
	if s.ParentInstanceID != nil {
		v := s.ParentInstanceID.String()
		dto.ParentInstanceID = &v
	}
```

Add to `enrichedSummaryDTO` (after `FailedNodeID`):

```go
	FailedNodeID      *string `json:"failedNodeId,omitempty"`
	// ChildCount is the number of spawned children the CALLER may see. Omitted
	// when zero so a childless row renders no expander.
	ChildCount        int     `json:"childCount,omitempty"`
```

and in `toEnrichedSummaryDTO`:

```go
		FailedNodeID:  e.FailedNodeID,
		ChildCount:    e.ChildCount,
```

- [x] **Step 4: Parse the parent selector in `List`**

In `adapter/http/instance_handler.go`, inside `List`, after the `offset` block
and before the `ListEnriched` call, add:

```go
	// The parent selector is the ONE caller-supplied narrowing on this endpoint.
	// It composes with — and can never replace — the owner/department constraints
	// ListEnriched service-sets from the request identity. Decoding goes through
	// the shared domain parser so REST and the MCP tool cannot drift.
	//
	// A MALFORMED selector is rejected rather than silently degrading to an
	// unfiltered list, which would show the caller far more than they asked for.
	scope, parentID, perr := instance.ParseParentScope(r.URL.Query().Get("parent"))
	if perr != nil {
		WriteError(w, http.StatusBadRequest, "INVALID_PARENT", perr.Error())
		return
	}
	filters.ParentScope = scope
	filters.ParentInstanceID = parentID
```

- [x] **Step 5: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w adapter/http/
go test ./adapter/http/ -run TestInstanceListREST -v
```

Expected output:

```
--- PASS: TestInstanceListREST_RootsOnly (0.00s)
--- PASS: TestInstanceListREST_ChildrenOfAParent (0.00s)
--- PASS: TestInstanceListREST_NoParentParamDoesNoNarrowing (0.00s)
--- PASS: TestInstanceListREST_ChildCountIsSurfaced (0.00s)
--- PASS: TestInstanceListREST_RootRowOmitsParentInstanceID (0.00s)
--- PASS: TestInstanceListREST_ParentComposesWithStateFilter (0.00s)
--- PASS: TestInstanceListREST_MalformedParentIsRejected (0.00s)
--- PASS: TestInstanceListREST_UnknownParentReturnsEmptyNot404 (0.00s)
PASS
ok  	github.com/arbitron/arbitron/adapter/http	0.5s
```

- [x] **Step 6: Confirm the existing HTTP suite still passes**

```bash
go test ./adapter/http/
```

Expected: `ok  	github.com/arbitron/arbitron/adapter/http`

- [x] **Step 7: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add adapter/http/instance_handler.go adapter/http/instance_list_parent_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(http): parent selector and hierarchy fields on the instance list endpoint"
```
