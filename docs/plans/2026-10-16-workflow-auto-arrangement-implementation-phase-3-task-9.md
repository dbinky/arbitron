# Workflow Auto-Arrangement — Phase 3 Task 9: HTTP layout surfaces

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Execution discipline:** Expected outputs below are predictions, never recorded test results.
Where an initial run cannot compile/import a new symbol, add only its declaration and a
minimal compiling non-passing body, then rerun and observe the named behavioral assertion
fail before implementing behavior. Keep each subtask green before committing; do not commit
empty success stubs or knowingly failing tests. Test count examples are illustrative: record
the actual command/output at execution. Each data path needs an assertion at its consumer
and an absent/nil/empty/wrong-input negative control; constructor/call-count checks alone
cannot establish that the graph, status, evidence or retry was fed correctly.

**Alignment requirements:** Read contracts §6 (6.7) before executing this task. Those requirements and the corrective TDD steps below are part of this task; conflicting earlier example bodies must be updated, not copied unchanged.

**Goal:** Serve revision-scoped arrangement status over REST: `GET`/`POST retry` for workflows and instances, plus a `layout` block on `GET /instances/{id}`. All of it goes through the production `layout.QueryService` wired into the real router.

**Architecture:** A new `adapter/http/layout_handler.go` defines the consumer-owned `LayoutQueryService` port and `LayoutHandler`. Domain sentinels map to HTTP statuses in one place (`writeLayoutError`). `NewRouterFromDeps` always registers the four routes; a nil `RouterDeps.Layouts` answers 503. `InstanceHandler.Get` embeds the instance's snapshot-revision view. Router construction moves out of `runServe` into `(*domainStack).routerDeps`, so a test can build the real router from `initDomain` and assert the HTTP body (the SINK).

**Tech Stack:** Go 1.25.5, chi v5.2.5, `net/http/httptest`, testify, `domain/layout` (P2T5/P3T8), local-mode `AuthMiddleware`.

**Spec/Design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`; `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md` §2 "HTTP surfaces"; contracts §3 "HTTP (P3T9)" and §5 addendum 9.

Line numbers are from the base checkout. P2T9 and P3T8 edit `cmd/arbitron/main.go` earlier, so locate each edit by its quoted anchor text.

---

## Interfaces

**Consumes:**

```go
// domain/layout (P2T5 + P3T8)
type View struct {
	SavedRevision string `json:"savedRevision"`; JobGeneration int64 `json:"jobGeneration"`; ResultGeneration int64 `json:"resultGeneration"`
	State State `json:"state,omitempty"`; DisplayStatus DisplayStatus `json:"displayStatus"`; Stage Stage `json:"stage,omitempty"`
	AttemptsUsed int `json:"attemptsUsed"`; AttemptLimit int `json:"attemptLimit"`; Findings []Finding `json:"findings"`
	FailureStage Stage `json:"failureStage,omitempty"`; FailureReason string `json:"failureReason,omitempty"`
	Evidence *Evidence `json:"evidence,omitempty"`; Contract *RendererContract `json:"contract,omitempty"`; Geometry *Geometry `json:"geometry,omitempty"`
	Retained *workflow.RetainedLayout `json:"retained,omitempty"`; Provenance Provenance `json:"provenance"`
	Unreviewed bool `json:"unreviewed"`; RetryEligible bool `json:"retryEligible"`; Available bool `json:"available"`; AvailabilityError string `json:"availabilityError,omitempty"`
}
type RetryAck struct { SavedRevision string `json:"savedRevision"`; JobGeneration int64 `json:"jobGeneration"`; State State `json:"state"`; DisplayStatus DisplayStatus `json:"displayStatus"` }
var ErrRevisionConflict, ErrLegacyRevision, ErrRetryConflict, ErrStoreUnavailable error
const DisplayUnavailable DisplayStatus = "unavailable"; const DisplayPending DisplayStatus = "pending"
const ProvenanceRetainedPositions Provenance = "retained_positions"; const ProvenanceLegacy Provenance = "legacy"
type QueryService struct{ /* … */ } // implements the four methods below
// cmd/arbitron (P3T8)
func (ds *domainStack) layoutQuery() *layout.QueryService // nil when ds.layout is nil
func bootQueryStack(t *testing.T) *domainStack                                   // cmd/arbitron/layout_query_e2e_test.go
func queryE2EWorkflow(name, label string) *workflow.Workflow                      // same file
func manifestRevisionOnDisk(t *testing.T, ds *domainStack, name string) string    // same file
func snapshotRevision(t *testing.T, ds *domainStack, id uuid.UUID) string         // same file
func adminCtxForLocalUser(t *testing.T, ds *domainStack) context.Context          // cmd/arbitron/spawn_e2e_test.go:87
// domain/engine (P3T8)
func DecodeWorkflowSnapshot(snapshot map[string]any) (*workflow.Workflow, error)
// adapter/http (existing)
func safeWorkflowName(name string) bool                 // workflow_handler.go:151
func parseInstanceID(r *http.Request) (uuid.UUID, error) // instance_handler.go:309
func WriteError(w http.ResponseWriter, status int, code, message string); func WriteJSON(w http.ResponseWriter, status int, data any)
// test helpers (existing, package http_test): withChiURLParam (workflow_handler_test.go:224, also injects an identity),
// newRouterTestDeps (server_test.go:218), authedRouter (server_test.go:36), newInstanceHandler/testInstance (instance_handler_test.go:497-554)
```

**Produces:**

```go
// adapter/http/layout_handler.go
type LayoutQueryService interface {
	WorkflowLayout(ctx context.Context, name string) (layout.View, error)
	InstanceLayout(ctx context.Context, id uuid.UUID) (layout.View, error)
	RetryWorkflow(ctx context.Context, name, expectedRevision string) (layout.RetryAck, error)
	RetryInstance(ctx context.Context, id uuid.UUID, expectedRevision string) (layout.RetryAck, error)
}
type LayoutHandler struct{ layouts LayoutQueryService }
func NewLayoutHandler(layouts LayoutQueryService) *LayoutHandler
func (h *LayoutHandler) GetWorkflowLayout(w http.ResponseWriter, r *http.Request)   // GET  /api/v1/workflows/{name}/layout        → 200 View
func (h *LayoutHandler) RetryWorkflowLayout(w http.ResponseWriter, r *http.Request) // POST /api/v1/workflows/{name}/layout/retry  → 202 RetryAck
func (h *LayoutHandler) GetInstanceLayout(w http.ResponseWriter, r *http.Request)   // GET  /api/v1/instances/{id}/layout          → 200 View
func (h *LayoutHandler) RetryInstanceLayout(w http.ResponseWriter, r *http.Request) // POST /api/v1/instances/{id}/layout/retry    → 202 RetryAck
// Retry body: {"expectedRevision": "<savedRevision>"}; missing/empty ⇒ 400 BAD_REQUEST "expectedRevision is required".
// RouterDeps gains: Layouts LayoutQueryService
// adapter/http/instance_handler.go
func (h *InstanceHandler) SetLayouts(l LayoutQueryService)
// instanceResponse gains: Layout *layout.View `json:"layout,omitempty"`
// cmd/arbitron/main.go (final signature — matches addendum 9 unchanged)
func (ds *domainStack) routerDeps(mcpHandler http.Handler, dashHandler http.Handler, notifHandler *adapthttp.NotificationHandler, webhookHandler *adapthttp.WebhookHandler) adapthttp.RouterDeps
```

Status mapping (`writeLayoutError`, checked with `errors.Is` in this order):

| Error | Status | Code |
|---|---|---|
| `workflow.ErrNotFound`, `instance.ErrNotFound` | 404 | `NOT_FOUND` |
| `identity.ErrUnauthenticated` | 401 | `UNAUTHENTICATED` |
| `identity.ErrForbidden` (= `instance.ErrForbidden` = `workflow.ErrForbidden`), `workflow.ErrUnauthorized` | 403 | `FORBIDDEN` |
| `workflow.ErrSharedReadOnly`, `layout.ErrRevisionConflict`, `layout.ErrRetryConflict`, `layout.ErrLegacyRevision` | 409 | `CONFLICT` |
| `workflow.ErrMutationPending` | 503 | `UNAVAILABLE` (safe static recovery reason) |
| `layout.ErrStoreUnavailable`; nil service | 503 | `UNAVAILABLE` |
| anything else | 500 | `INTERNAL_ERROR` |

`WriteError` replaces the message of every 5xx response with "internal server error" and logs the real detail. The code still tells the client the cause.

`workflow_handler.go` needs no edit. `GET /workflows/{name}` already returns the stamped manifest, including `presentation.revision` (P2T2). Task 9.4 pins this through the real router so the dashboard can join manifest and layout by that token.

---

### Task 9.1: `LayoutHandler` + error mapping

**Files:**
- Create: `adapter/http/layout_handler.go`
- Test:   `adapter/http/layout_handler_test.go`

- [x] **Step 1: Write the failing test**

Create `adapter/http/layout_handler_test.go`:

```go
package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

// fakeLayoutQuery honours every load-bearing argument: views/revisions are
// keyed by the exact workflow name or instance id, a retry succeeds only when
// expectedRevision equals the subject's revision, and errs (keyed
// "workflow:<name>" / "instance:<id>") is returned by every method for that
// subject. Every call is recorded with its arguments.
type fakeLayoutQuery struct {
	mu            sync.Mutex
	workflowViews map[string]layout.View
	workflowRevs  map[string]string
	instanceViews map[uuid.UUID]layout.View
	instanceRevs  map[uuid.UUID]string
	errs          map[string]error
	calls         []string
	contexts      []context.Context
}

func newFakeLayoutQuery() *fakeLayoutQuery {
	return &fakeLayoutQuery{
		workflowViews: map[string]layout.View{},
		workflowRevs:  map[string]string{},
		instanceViews: map[uuid.UUID]layout.View{},
		instanceRevs:  map[uuid.UUID]string{},
		errs:          map[string]error{},
	}
}

func (f *fakeLayoutQuery) record(ctx context.Context, call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	f.contexts = append(f.contexts, ctx)
}

func (f *fakeLayoutQuery) recordedContexts() []context.Context {
    f.mu.Lock()
    defer f.mu.Unlock()
    return append([]context.Context(nil), f.contexts...)
}

func (f *fakeLayoutQuery) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeLayoutQuery) WorkflowLayout(ctx context.Context, name string) (layout.View, error) {
	if err := ctx.Err(); err != nil { return layout.View{}, err }
	f.record(ctx, "WorkflowLayout:" + name)
	if err := f.errs["workflow:"+name]; err != nil {
		return layout.View{}, err
	}
	v, ok := f.workflowViews[name]
	if !ok {
		return layout.View{}, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}
	return v, nil
}

func (f *fakeLayoutQuery) RetryWorkflow(ctx context.Context, name, expected string) (layout.RetryAck, error) {
	if err := ctx.Err(); err != nil { return layout.RetryAck{}, err }
	f.record(ctx, "RetryWorkflow:" + name + "@" + expected)
	if err := f.errs["workflow:"+name]; err != nil {
		return layout.RetryAck{}, err
	}
	rev, ok := f.workflowRevs[name]
	if !ok {
		return layout.RetryAck{}, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}
	if expected != rev {
		return layout.RetryAck{}, fmt.Errorf("%w: expected %s", layout.ErrRevisionConflict, expected)
	}
	return layout.RetryAck{SavedRevision: rev, JobGeneration: 2, State: layout.StatePending, DisplayStatus: layout.DisplayPending}, nil
}

func (f *fakeLayoutQuery) InstanceLayout(ctx context.Context, id uuid.UUID) (layout.View, error) {
	if err := ctx.Err(); err != nil { return layout.View{}, err }
	f.record(ctx, "InstanceLayout:" + id.String())
	if err := f.errs["instance:"+id.String()]; err != nil {
		return layout.View{}, err
	}
	v, ok := f.instanceViews[id]
	if !ok {
		return layout.View{}, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	return v, nil
}

func (f *fakeLayoutQuery) RetryInstance(ctx context.Context, id uuid.UUID, expected string) (layout.RetryAck, error) {
	if err := ctx.Err(); err != nil { return layout.RetryAck{}, err }
	f.record(ctx, "RetryInstance:" + id.String() + "@" + expected)
	if err := f.errs["instance:"+id.String()]; err != nil {
		return layout.RetryAck{}, err
	}
	rev, ok := f.instanceRevs[id]
	if !ok {
		return layout.RetryAck{}, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	if expected != rev {
		return layout.RetryAck{}, fmt.Errorf("%w: expected %s", layout.ErrRevisionConflict, expected)
	}
	return layout.RetryAck{SavedRevision: rev, JobGeneration: 3, State: layout.StatePending, DisplayStatus: layout.DisplayPending}, nil
}

var _ adapthttp.LayoutQueryService = (*fakeLayoutQuery)(nil)

func attentionView(rev string) layout.View {
	return layout.View{
		SavedRevision: rev,
		JobGeneration: 1,
		State:         layout.StateNeedsAttention,
		DisplayStatus: layout.DisplayNeedsAttention,
		AttemptsUsed:  5,
		AttemptLimit:  5,
		Findings: []layout.Finding{{
			Criterion: layout.CriterionNodeOverlap, Severity: layout.SeverityBlocking,
			Source: layout.SourceReview, NodeIDs: []string{"a", "b"}, Evidence: "a overlaps b",
		}},
		Provenance:    layout.ProvenanceArranged,
		RetryEligible: true,
		Available:     true,
	}
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&m))
	return m
}

func serveWorkflowLayout(h *adapthttp.LayoutHandler, name string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/workflows/x/layout", nil)
	req = withChiURLParam(req, "name", name)
	w := httptest.NewRecorder()
	h.GetWorkflowLayout(w, req)
	return w
}

func serveWorkflowRetry(h *adapthttp.LayoutHandler, name, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/workflows/x/layout/retry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withChiURLParam(req, "name", name)
	w := httptest.NewRecorder()
	h.RetryWorkflowLayout(w, req)
	return w
}

func serveInstanceLayout(h *adapthttp.LayoutHandler, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/instances/x/layout", nil)
	req = withChiURLParam(req, "id", id)
	w := httptest.NewRecorder()
	h.GetInstanceLayout(w, req)
	return w
}

func serveInstanceRetry(h *adapthttp.LayoutHandler, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/instances/x/layout/retry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withChiURLParam(req, "id", id)
	w := httptest.NewRecorder()
	h.RetryInstanceLayout(w, req)
	return w
}

// --- GET /workflows/{name}/layout ---

func TestLayoutHandler_GetWorkflowLayout_ReturnsView(t *testing.T) {
	q := newFakeLayoutQuery()
	q.workflowViews["wf"] = attentionView("rev-1")
	h := adapthttp.NewLayoutHandler(q)

	w := serveWorkflowLayout(h, "wf")
	require.Equal(t, http.StatusOK, w.Code)
	body := decodeBody(t, w)
	assert.Equal(t, "rev-1", body["savedRevision"])
	assert.Equal(t, "needs_attention", body["displayStatus"])
	assert.Equal(t, float64(5), body["attemptsUsed"])
	assert.Equal(t, true, body["retryEligible"])
	findings := body["findings"].([]any)
	require.Len(t, findings, 1)
	assert.Equal(t, "node_overlap", findings[0].(map[string]any)["criterion"])
	assert.Equal(t, []string{"WorkflowLayout:wf"}, q.recorded())
}

func TestLayoutHandler_GetWorkflowLayout_UnknownName_NotFound(t *testing.T) {
	q := newFakeLayoutQuery()
	q.workflowViews["wf"] = attentionView("rev-1")
	h := adapthttp.NewLayoutHandler(q)

	w := serveWorkflowLayout(h, "other")
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "NOT_FOUND", decodeBody(t, w)["code"])
	assert.Equal(t, []string{"WorkflowLayout:other"}, q.recorded())
}

func TestLayoutHandler_UnsafeWorkflowName_NotFoundWithoutQuery(t *testing.T) {
	for _, name := range []string{"../secret", ".", "..", "a/b", `a\b`, ""} {
		t.Run(name, func(t *testing.T) {
			q := newFakeLayoutQuery()
			h := adapthttp.NewLayoutHandler(q)
			assert.Equal(t, http.StatusNotFound, serveWorkflowLayout(h, name).Code)
			assert.Equal(t, http.StatusNotFound, serveWorkflowRetry(h, name, `{"expectedRevision":"r"}`).Code)
			assert.Empty(t, q.recorded(), "an unsafe name must never reach the query service")
		})
	}
}

// --- POST /workflows/{name}/layout/retry ---

func TestLayoutHandler_RetryWorkflow_Accepted(t *testing.T) {
	q := newFakeLayoutQuery()
	q.workflowRevs["wf"] = "rev-1"
	h := adapthttp.NewLayoutHandler(q)

	w := serveWorkflowRetry(h, "wf", `{"expectedRevision":"rev-1"}`)
	require.Equal(t, http.StatusAccepted, w.Code)
	body := decodeBody(t, w)
	assert.Equal(t, "rev-1", body["savedRevision"])
	assert.Equal(t, float64(2), body["jobGeneration"])
	assert.Equal(t, "pending", body["state"])
	assert.Equal(t, "pending", body["displayStatus"])
	assert.Equal(t, []string{"RetryWorkflow:wf@rev-1"}, q.recorded())
}

func TestLayoutHandler_RetryWorkflow_StaleRevision_Conflict(t *testing.T) {
	q := newFakeLayoutQuery()
	q.workflowRevs["wf"] = "rev-2"
	h := adapthttp.NewLayoutHandler(q)

	w := serveWorkflowRetry(h, "wf", `{"expectedRevision":"rev-1"}`)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "CONFLICT", decodeBody(t, w)["code"])
}

func TestLayoutHandler_RetryWorkflow_BadBodies_BadRequest(t *testing.T) {
	cases := map[string]struct {
		body    string
		message string
	}{
		"missing field": {`{}`, "expectedRevision is required"},
		"empty field":   {`{"expectedRevision":""}`, "expectedRevision is required"},
		"invalid json":  {`{not json`, "invalid request body"},
		"empty body":    {``, "invalid request body"},
		"wrong type":    {`{"expectedRevision":7}`, "invalid request body"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			q := newFakeLayoutQuery()
			q.workflowRevs["wf"] = "rev-1"
			h := adapthttp.NewLayoutHandler(q)

			w := serveWorkflowRetry(h, "wf", tc.body)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			body := decodeBody(t, w)
			assert.Equal(t, "BAD_REQUEST", body["code"])
			assert.Contains(t, body["error"], tc.message)
			assert.Empty(t, q.recorded(), "a bad body must not reach the query service")
		})
	}
}

// --- instance routes ---

func TestLayoutHandler_GetInstanceLayout_ReturnsView(t *testing.T) {
	q := newFakeLayoutQuery()
	id := uuid.New()
	q.instanceViews[id] = attentionView("rev-snap")
	h := adapthttp.NewLayoutHandler(q)

	w := serveInstanceLayout(h, id.String())
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "rev-snap", decodeBody(t, w)["savedRevision"])
	assert.Equal(t, []string{"InstanceLayout:" + id.String()}, q.recorded())
}

func TestLayoutHandler_GetInstanceLayout_UnknownID_NotFound(t *testing.T) {
	q := newFakeLayoutQuery()
	q.instanceViews[uuid.New()] = attentionView("rev-snap")
	h := adapthttp.NewLayoutHandler(q)

	assert.Equal(t, http.StatusNotFound, serveInstanceLayout(h, uuid.New().String()).Code)
}

func TestLayoutHandler_InvalidInstanceID_BadRequest(t *testing.T) {
	q := newFakeLayoutQuery()
	h := adapthttp.NewLayoutHandler(q)

	w := serveInstanceLayout(h, "not-a-uuid")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, decodeBody(t, w)["error"], "invalid instance ID")
	assert.Equal(t, http.StatusBadRequest, serveInstanceRetry(h, "not-a-uuid", `{"expectedRevision":"r"}`).Code)
	assert.Empty(t, q.recorded())
}

func TestLayoutHandler_RetryInstance_AcceptedAndConflicts(t *testing.T) {
	q := newFakeLayoutQuery()
	id := uuid.New()
	q.instanceRevs[id] = "rev-snap"
	h := adapthttp.NewLayoutHandler(q)

	w := serveInstanceRetry(h, id.String(), `{"expectedRevision":"rev-snap"}`)
	require.Equal(t, http.StatusAccepted, w.Code)
	body := decodeBody(t, w)
	assert.Equal(t, "rev-snap", body["savedRevision"])
	assert.Equal(t, float64(3), body["jobGeneration"])

	assert.Equal(t, http.StatusConflict, serveInstanceRetry(h, id.String(), `{"expectedRevision":"rev-current"}`).Code)
	assert.Equal(t, http.StatusBadRequest, serveInstanceRetry(h, id.String(), `{}`).Code)
	assert.Equal(t, []string{
		"RetryInstance:" + id.String() + "@rev-snap",
		"RetryInstance:" + id.String() + "@rev-current",
	}, q.recorded())
}

func TestLayoutHandler_RetryInstance_DepartmentPeer_Forbidden(t *testing.T) {
	q := newFakeLayoutQuery()
	id := uuid.New()
	q.instanceRevs[id] = "rev-snap"
	q.errs["instance:"+id.String()] = fmt.Errorf("instance %s: %w", id, instance.ErrForbidden)
	h := adapthttp.NewLayoutHandler(q)

	w := serveInstanceRetry(h, id.String(), `{"expectedRevision":"rev-snap"}`)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "FORBIDDEN", decodeBody(t, w)["code"])
}

// --- error mapping ---

func TestLayoutHandler_ErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"workflow not found", workflow.ErrNotFound, http.StatusNotFound, "NOT_FOUND"},
		{"instance not found", instance.ErrNotFound, http.StatusNotFound, "NOT_FOUND"},
		{"unauthenticated", identity.ErrUnauthenticated, http.StatusUnauthorized, "UNAUTHENTICATED"},
		{"forbidden", identity.ErrForbidden, http.StatusForbidden, "FORBIDDEN"},
		{"unauthorized", workflow.ErrUnauthorized, http.StatusForbidden, "FORBIDDEN"},
		{"shared read-only", workflow.ErrSharedReadOnly, http.StatusConflict, "CONFLICT"},
		{"revision conflict", layout.ErrRevisionConflict, http.StatusConflict, "CONFLICT"},
		{"retry conflict", layout.ErrRetryConflict, http.StatusConflict, "CONFLICT"},
		{"legacy revision", layout.ErrLegacyRevision, http.StatusConflict, "CONFLICT"},
		{"store unavailable", layout.ErrStoreUnavailable, http.StatusServiceUnavailable, "UNAVAILABLE"},
		{"unexpected", errors.New("boom"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := newFakeLayoutQuery()
			q.workflowRevs["wf"] = "rev-1"
			q.workflowViews["wf"] = attentionView("rev-1")
			q.errs["workflow:wf"] = fmt.Errorf("wrapped: %w", tc.err)
			h := adapthttp.NewLayoutHandler(q)

			for _, w := range []*httptest.ResponseRecorder{
				serveWorkflowLayout(h, "wf"),
				serveWorkflowRetry(h, "wf", `{"expectedRevision":"rev-1"}`),
			} {
				assert.Equal(t, tc.status, w.Code)
				assert.Equal(t, tc.code, decodeBody(t, w)["code"])
			}
		})
	}
}

// A handler built without a query service reports 503 rather than panicking
// or silently returning "no arrangement".
func TestLayoutHandler_NilService_Unavailable(t *testing.T) {
	h := adapthttp.NewLayoutHandler(nil)
	id := uuid.New().String()
	for _, w := range []*httptest.ResponseRecorder{
		serveWorkflowLayout(h, "wf"),
		serveWorkflowRetry(h, "wf", `{"expectedRevision":"r"}`),
		serveInstanceLayout(h, id),
		serveInstanceRetry(h, id, `{"expectedRevision":"r"}`),
	} {
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		assert.Equal(t, "UNAVAILABLE", decodeBody(t, w)["code"])
	}
}
```

- [x] **Step 2: Run it, confirm it fails**

```bash
go test ./adapter/http/ -run 'TestLayoutHandler' -v
```

Expected:

```
# github.com/arbitron/arbitron/adapter/http_test [github.com/arbitron/arbitron/adapter/http.test]
./layout_handler_test.go:...: undefined: adapthttp.LayoutQueryService
./layout_handler_test.go:...: undefined: adapthttp.LayoutHandler
./layout_handler_test.go:...: undefined: adapthttp.NewLayoutHandler
FAIL	github.com/arbitron/arbitron/adapter/http [build failed]
```

- [x] **Step 3: Minimal implementation**

Create `adapter/http/layout_handler.go`:

```go
package http

import (
	"context"
	"encoding/json"
	"errors"
    "io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

// maxLayoutRetryBodyBytes bounds the tiny {"expectedRevision": "..."} body.
const maxLayoutRetryBodyBytes = 1 << 16

// LayoutQueryService is the consumer-owned port onto the authorised layout
// query/retry seam (implemented by *layout.QueryService). Authorization lives
// behind it; this adapter only parses and maps errors.
type LayoutQueryService interface {
	WorkflowLayout(ctx context.Context, name string) (layout.View, error)
	InstanceLayout(ctx context.Context, id uuid.UUID) (layout.View, error)
	RetryWorkflow(ctx context.Context, name, expectedRevision string) (layout.RetryAck, error)
	RetryInstance(ctx context.Context, id uuid.UUID, expectedRevision string) (layout.RetryAck, error)
}

// LayoutHandler serves arrangement status and retry for workflows and instances.
type LayoutHandler struct {
	layouts LayoutQueryService
}

// NewLayoutHandler builds the handler. A nil service makes every route answer
// 503 so a mis-wired deployment is visible rather than silently layout-less.
func NewLayoutHandler(layouts LayoutQueryService) *LayoutHandler {
	return &LayoutHandler{layouts: layouts}
}

type layoutRetryRequest struct {
	ExpectedRevision string `json:"expectedRevision"`
}

func (h *LayoutHandler) configured(w http.ResponseWriter) bool {
	if h.layouts == nil {
		WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "layout service not configured")
		return false
	}
	return true
}

// GetWorkflowLayout handles GET /workflows/{name}/layout.
func (h *LayoutHandler) GetWorkflowLayout(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}
	if !h.configured(w) {
		return
	}
	view, err := h.layouts.WorkflowLayout(r.Context(), name)
	if err != nil {
		writeLayoutError(w, err, "workflow not found: "+name)
		return
	}
	WriteJSON(w, http.StatusOK, view)
}

// RetryWorkflowLayout handles POST /workflows/{name}/layout/retry.
func (h *LayoutHandler) RetryWorkflowLayout(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}
	if !h.configured(w) {
		return
	}
	expected, ok := decodeLayoutRetry(w, r)
	if !ok {
		return
	}
	ack, err := h.layouts.RetryWorkflow(r.Context(), name, expected)
	if err != nil {
		writeLayoutError(w, err, "workflow not found: "+name)
		return
	}
	WriteJSON(w, http.StatusAccepted, ack)
}

// GetInstanceLayout handles GET /instances/{id}/layout.
func (h *LayoutHandler) GetInstanceLayout(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID: "+chi.URLParam(r, "id"))
		return
	}
	if !h.configured(w) {
		return
	}
	view, err := h.layouts.InstanceLayout(r.Context(), id)
	if err != nil {
		writeLayoutError(w, err, "instance not found: "+id.String())
		return
	}
	WriteJSON(w, http.StatusOK, view)
}

// RetryInstanceLayout handles POST /instances/{id}/layout/retry.
func (h *LayoutHandler) RetryInstanceLayout(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID: "+chi.URLParam(r, "id"))
		return
	}
	if !h.configured(w) {
		return
	}
	expected, ok := decodeLayoutRetry(w, r)
	if !ok {
		return
	}
	ack, err := h.layouts.RetryInstance(r.Context(), id, expected)
	if err != nil {
		writeLayoutError(w, err, "instance not found: "+id.String())
		return
	}
	WriteJSON(w, http.StatusAccepted, ack)
}

// decodeLayoutRetry parses the retry body, writing a 400 and returning false
// when it is malformed or carries no expectedRevision.
func decodeLayoutRetry(w http.ResponseWriter, r *http.Request) (string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLayoutRetryBodyBytes)
	var req layoutRetryRequest
	decoder := json.NewDecoder(r.Body)
    if err := decoder.Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return "", false
	}
	if req.ExpectedRevision == "" {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "expectedRevision is required")
		return "", false
	}
	var extra any
    if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
        WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: expected one JSON object")
        return "", false
    }
    return req.ExpectedRevision, true
}

// writeLayoutError maps the layout/authorization sentinels to HTTP statuses.
// Not-found keeps the existing concealment wording (no existence leak).
func writeLayoutError(w http.ResponseWriter, err error, notFoundMessage string) {
	switch {
    case errors.Is(err, workflow.ErrMutationPending):
        WriteJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "workflow destination is awaiting durable mutation recovery; retry shortly", Code: "UNAVAILABLE"})
	case errors.Is(err, workflow.ErrNotFound), errors.Is(err, instance.ErrNotFound):
		WriteError(w, http.StatusNotFound, "NOT_FOUND", notFoundMessage)
	case errors.Is(err, identity.ErrUnauthenticated):
		WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
	case errors.Is(err, identity.ErrForbidden), errors.Is(err, workflow.ErrUnauthorized):
		WriteError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
	case errors.Is(err, workflow.ErrSharedReadOnly),
		errors.Is(err, layout.ErrRevisionConflict),
		errors.Is(err, layout.ErrRetryConflict),
		errors.Is(err, layout.ErrLegacyRevision):
		WriteError(w, http.StatusConflict, "CONFLICT", err.Error())
	case errors.Is(err, layout.ErrStoreUnavailable):
		WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", err.Error())
	default:
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	}
}
```

- [x] **Step 4: Run it, confirm it passes**

```bash
go test ./adapter/http/ -run 'TestLayoutHandler' -v
```

Expected: every `TestLayoutHandler_*` test and subtest `--- PASS`; `ok  	github.com/arbitron/arbitron/adapter/http`.

- [x] **Step 5: Commit**

```bash
git add adapter/http/layout_handler.go adapter/http/layout_handler_test.go && git commit -m "feat(http): add workflow and instance layout status/retry handler" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9.2: Instance `GET` carries its snapshot-revision layout

**Files:**
- Modify: `adapter/http/instance_handler.go:3-19` (imports: add `"github.com/arbitron/arbitron/domain/layout"`), `:41-49` (struct field), `:71-80` (add `SetLayouts` after `SetDismissals`), `:82-105` (`instanceResponse` field), `:474-493` (`Get`)
- Test:   `adapter/http/instance_handler_layout_test.go`

- [x] **Step 1: Write the failing test**

Create `adapter/http/instance_handler_layout_test.go`:

```go
package http_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
)

func getInstance(t *testing.T, h interface {
	Get(http.ResponseWriter, *http.Request)
}, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/instances/"+id, nil)
	req = withChiURLParam(req, "id", id)
	w := httptest.NewRecorder()
	h.Get(w, req)
	return w
}

func TestInstanceHandler_Get_IncludesSnapshotLayout(t *testing.T) {
	h, instRepo, _, _, _ := newInstanceHandler()
	inst := testInstance(instance.StateCompleted)
	instRepo.instances[inst.ID] = inst
	other := uuid.New()
	q := newFakeLayoutQuery()
	q.instanceViews[inst.ID] = attentionView("rev-snap")
	q.instanceViews[other] = attentionView("rev-other")
	h.SetLayouts(q)

	w := getInstance(t, h, inst.ID.String())
	require.Equal(t, http.StatusOK, w.Code)
	body := decodeBody(t, w)
	lay, ok := body["layout"].(map[string]any)
	require.True(t, ok, "GET /instances/{id} must embed the layout view")
	assert.Equal(t, "rev-snap", lay["savedRevision"], "the view must be the one for THIS instance id")
	assert.Equal(t, "needs_attention", lay["displayStatus"])
	assert.Equal(t, []string{"InstanceLayout:" + inst.ID.String()}, q.recorded())
}

// A layout query failure never fails the instance read and never pretends the
// run has no arrangement: it is reported in-band as unavailable, keyed by the
// snapshot's own revision with its retained fallback.
func TestInstanceHandler_Get_LayoutError_InBandUnavailable(t *testing.T) {
	h, instRepo, _, _, _ := newInstanceHandler()
	inst := testInstance(instance.StateRunning)
	inst.WorkflowSnapshot = map[string]any{
		"name":  "test-wf",
		"nodes": []any{map[string]any{"id": "a", "type": "transform", "entrypoint": true}},
		"edges": []any{},
		"presentation": map[string]any{
			"revision": "rev-snap", "reason": "create", "scope": "private", "name": "test-wf",
			"retained": map[string]any{"source": "manifest", "positions": map[string]any{"a": map[string]any{"x": 3, "y": 4}}},
		},
	}
	instRepo.instances[inst.ID] = inst
	q := newFakeLayoutQuery()
	q.errs["instance:"+inst.ID.String()] = fmt.Errorf("decode: %w", layout.ErrStoreUnavailable)
	h.SetLayouts(q)

	w := getInstance(t, h, inst.ID.String())
	require.Equal(t, http.StatusOK, w.Code)
	lay := decodeBody(t, w)["layout"].(map[string]any)
	assert.Equal(t, "rev-snap", lay["savedRevision"])
	assert.Equal(t, "unavailable", lay["displayStatus"])
	assert.Equal(t, false, lay["available"])
	assert.Contains(t, lay["availabilityError"], "status store unavailable")
	assert.Equal(t, "retained_positions", lay["provenance"])
	retained := lay["retained"].(map[string]any)
	assert.Equal(t, "manifest", retained["source"])
}

func TestInstanceHandler_Get_LegacySnapshotLayoutError_LegacyProvenance(t *testing.T) {
	h, instRepo, _, _, _ := newInstanceHandler()
	inst := testInstance(instance.StateRunning) // no snapshot at all
	instRepo.instances[inst.ID] = inst
	q := newFakeLayoutQuery()
	q.errs["instance:"+inst.ID.String()] = fmt.Errorf("boom")
	h.SetLayouts(q)

	lay := decodeBody(t, getInstance(t, h, inst.ID.String()))["layout"].(map[string]any)
	assert.Equal(t, "", lay["savedRevision"])
	assert.Equal(t, "legacy", lay["provenance"])
	assert.Equal(t, false, lay["available"])
}

func TestInstanceHandler_Get_NoLayoutService_ReportsUnavailable(t *testing.T) {
	h, instRepo, _, _, _ := newInstanceHandler()
	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	body := decodeBody(t, getInstance(t, h, inst.ID.String()))
	lay := body["layout"].(map[string]any)
    assert.Equal(t, "unavailable", lay["displayStatus"])
    assert.Equal(t, false, lay["available"])
}

func TestInstanceHandler_Get_NotFound_DoesNotQueryLayout(t *testing.T) {
	h, _, _, _, _ := newInstanceHandler()
	q := newFakeLayoutQuery()
	h.SetLayouts(q)

	assert.Equal(t, http.StatusNotFound, getInstance(t, h, uuid.New().String()).Code)
	assert.Empty(t, q.recorded())
}
```

- [x] **Step 2: Run it, confirm it fails**

```bash
go test ./adapter/http/ -run 'TestInstanceHandler_Get_' -v
```

Expected:

```
./instance_handler_layout_test.go:...: h.SetLayouts undefined (type *http.InstanceHandler has no field or method SetLayouts)
FAIL	github.com/arbitron/arbitron/adapter/http [build failed]
```

- [x] **Step 3: Minimal implementation**

In `adapter/http/instance_handler.go`:

1. Add `"github.com/arbitron/arbitron/domain/layout"` to the import block, after `"github.com/arbitron/arbitron/domain/instance"`.

2. Replace the `InstanceHandler` struct (lines 41-49) with:

```go
// InstanceHandler handles REST API requests for instance operations.
type InstanceHandler struct {
	instances   instance.InstanceRepository
	nodeExecs   instance.NodeExecutionRepository
	snapshots   instance.VariableSnapshotRepository
	service     InstanceService
	dismissals  instance.DismissalRepository
	currentUser CurrentUserResolver
	// layouts, when set, embeds the instance's snapshot-revision arrangement
	// view in GET /instances/{id}. Nil reports an explicit unavailable layout; it never silently omits status.
	layouts LayoutQueryService
}
```

3. Directly after `SetDismissals` (ends at line 80) add:

```go
// SetLayouts wires the authorised layout query seam used to embed the
// instance's snapshot-revision arrangement view in Get.
func (h *InstanceHandler) SetLayouts(l LayoutQueryService) {
	h.layouts = l
}
```

4. In `instanceResponse` (lines 82-105), after `InteractiveActive bool `json:"interactiveActive"``, add:

```go
	// Layout is the arrangement view for the revision captured in this
	// instance's snapshot (never the workflow's latest revision).
	Layout *layout.View `json:"layout,omitempty"`
```

5. Replace the whole `Get` method (lines 474-493) with the version below, and add `instanceLayout` directly after it:

```go
// Get handles GET /instances/{id} and returns the full instance, including the
// arrangement view of the revision captured in its snapshot when the layout
// seam is wired.
func (h *InstanceHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID: "+chi.URLParam(r, "id"))
		return
	}

	inst, err := h.service.GetInstance(r.Context(), id)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found: "+id.String())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get instance")
		return
	}

	resp := toInstanceResponse(inst)
	view := h.instanceLayout(r, inst)
    resp.Layout = &view
	WriteJSON(w, http.StatusOK, resp)
}

// instanceLayout returns the instance's arrangement view. A query failure is
// reported in-band (unavailable, keyed by the snapshot's own revision with its
// retained fallback) so the instance read never fails and never claims "no
// arrangement".
func (h *InstanceHandler) instanceLayout(r *http.Request, inst *instance.Instance) layout.View {
	var view layout.View
    var err error
    if h.layouts == nil { err = errors.New("layout service not configured") } else { view, err = h.layouts.InstanceLayout(r.Context(), inst.ID) }
    if err == nil { return view }
	out := layout.View{
		DisplayStatus:     layout.DisplayUnavailable,
		Findings:          []layout.Finding{},
		Provenance:        layout.ProvenanceLegacy,
		Available:         false,
		AvailabilityError: err.Error(),
	}
	if wf, derr := engine.DecodeWorkflowSnapshot(inst.WorkflowSnapshot); derr == nil && wf.Presentation != nil {
		retained := wf.Presentation.Retained
		out.SavedRevision = wf.SavedRevision()
        out.Unreviewed = true
        out.RetryEligible = true
		out.Retained = &retained
		out.Provenance = layout.ProvenanceRetainedPositions
	}
	return out
}
```

(`engine` is already imported by `instance_handler.go` for `engine.FindNodeByID`.)

- [x] **Step 4: Run it, confirm it passes**

```bash
go test ./adapter/http/ -run 'TestInstanceHandler' -v
```

Expected: the five new tests plus every existing `TestInstanceHandler_*` test `--- PASS`; `ok  	github.com/arbitron/arbitron/adapter/http`.

- [x] **Step 5: Commit**

```bash
git add adapter/http/instance_handler.go adapter/http/instance_handler_layout_test.go && git commit -m "feat(http): embed the snapshot-revision layout view in instance reads" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9.3: Router registration + `RouterDeps.Layouts`

**Files:**
- Modify: `adapter/http/server.go:53-176` (`RouterDeps`: add field after `BackendHealth`), `:203` (after `instHandler := NewInstanceHandler(...)`), `:269-286` (`/workflows` routes), `:310-338` (`/instances` routes)
- Test:   `adapter/http/server_layout_test.go`

- [x] **Step 1: Write the failing test**

Create `adapter/http/server_layout_test.go`:

```go
package http_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/instance"
)

func layoutRouter(t *testing.T, layouts adapthttp.LayoutQueryService) (http.Handler, *mockInstanceRepo) {
	t.Helper()
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	deps := adapthttp.RouterDeps{
		Service: svc, Workflows: wfRepo, Instances: instRepo, NodeExecs: nodeExecRepo, Snapshots: snapRepo,
		FileReader: fileReader, EventBus: bus, Config: cfg, QueueManager: qm, QueueRepo: qr,
		Layouts: layouts,
	}
	return authedRouter(adapthttp.NewRouterFromDeps(deps)), instRepo
}

func routerDo(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestRouter_LayoutRoutes_ServeThroughRouterDeps(t *testing.T) {
	q := newFakeLayoutQuery()
	q.workflowViews["my-wf"] = attentionView("rev-1")
	q.workflowRevs["my-wf"] = "rev-1"
	id := uuid.New()
	q.instanceViews[id] = attentionView("rev-snap")
	q.instanceRevs[id] = "rev-snap"
	router, _ := layoutRouter(t, q)

	w := routerDo(router, http.MethodGet, "/api/v1/workflows/my-wf/layout", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "rev-1", decodeBody(t, w)["savedRevision"])

	w = routerDo(router, http.MethodPost, "/api/v1/workflows/my-wf/layout/retry", `{"expectedRevision":"rev-1"}`)
	assert.Equal(t, http.StatusAccepted, w.Code)

	w = routerDo(router, http.MethodGet, "/api/v1/instances/"+id.String()+"/layout", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "rev-snap", decodeBody(t, w)["savedRevision"])

	w = routerDo(router, http.MethodPost, "/api/v1/instances/"+id.String()+"/layout/retry", `{"expectedRevision":"stale"}`)
	assert.Equal(t, http.StatusConflict, w.Code)

	assert.Equal(t, []string{
		"WorkflowLayout:my-wf",
		"RetryWorkflow:my-wf@rev-1",
		"InstanceLayout:" + id.String(),
		"RetryInstance:" + id.String() + "@stale",
	}, q.recorded())
}

func TestRouter_InstanceGet_EmbedsLayoutFromRouterDeps(t *testing.T) {
	q := newFakeLayoutQuery()
	router, instRepo := layoutRouter(t, q)
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "test-wf", State: instance.StateCompleted,
		Priority: instance.PriorityNormal, Variables: map[string]any{}, OriginalVariables: map[string]any{},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	instRepo.instances[inst.ID] = inst
	q.instanceViews[inst.ID] = attentionView("rev-snap")

	w := routerDo(router, http.MethodGet, "/api/v1/instances/"+inst.ID.String(), "")
	require.Equal(t, http.StatusOK, w.Code)
	lay := decodeBody(t, w)["layout"].(map[string]any)
	assert.Equal(t, "rev-snap", lay["savedRevision"])
}

// The routes are always registered; an unwired service answers 503 (never a
// silent 404/"no arrangement").
func TestRouter_LayoutRoutes_NilLayouts_Unavailable(t *testing.T) {
	router, _ := layoutRouter(t, nil)
	id := uuid.New().String()
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/workflows/my-wf/layout", ""},
		{http.MethodPost, "/api/v1/workflows/my-wf/layout/retry", `{"expectedRevision":"r"}`},
		{http.MethodGet, "/api/v1/instances/" + id + "/layout", ""},
		{http.MethodPost, "/api/v1/instances/" + id + "/layout/retry", `{"expectedRevision":"r"}`},
	} {
		w := routerDo(router, c.method, c.path, c.body)
		assert.Equal(t, http.StatusServiceUnavailable, w.Code, c.method+" "+c.path)
	}
}
```

- [x] **Step 2: Run it, confirm it fails**

```bash
go test ./adapter/http/ -run 'TestRouter_LayoutRoutes|TestRouter_InstanceGet_EmbedsLayout' -v
```

Expected:

```
./server_layout_test.go:...: unknown field Layouts in struct literal of type http.RouterDeps
FAIL	github.com/arbitron/arbitron/adapter/http [build failed]
```

- [x] **Step 3: Minimal implementation**

In `adapter/http/server.go`:

1. Inside `RouterDeps`, after the `BackendHealth BackendHealthChecker` field (line 175), add:

```go
	// Layouts is the authorised revision-scoped layout query/retry seam
	// (*layout.QueryService in production, built by the composition root). The
	// layout routes are ALWAYS registered; when nil they answer 503 so a
	// mis-wired deployment is visible. It also feeds the layout block of
	// GET /instances/{id}.
	Layouts LayoutQueryService
```

2. After `instHandler := NewInstanceHandler(d.Instances, d.NodeExecs, d.Snapshots, d.Service)` (line 203), add:

```go
	instHandler.SetLayouts(d.Layouts)
	layoutHandler := NewLayoutHandler(d.Layouts)
```

3. In the `/workflows` route block, directly after `r.Put("/{name}/layout", wfHandler.SaveLayout)` (line 281; P3T14 removes that PUT line), add:

```go
			r.Get("/{name}/layout", layoutHandler.GetWorkflowLayout)
			r.Post("/{name}/layout/retry", layoutHandler.RetryWorkflowLayout)
```

4. In the `/instances` route block, directly after `r.Get("/{id}/interventions", instHandler.GetInterventions)` (line 331), add:

```go
			r.Get("/{id}/layout", layoutHandler.GetInstanceLayout)
			r.Post("/{id}/layout/retry", layoutHandler.RetryInstanceLayout)
```

- [x] **Step 4: Run it, confirm it passes**

```bash
go test ./adapter/http/ -v -run 'TestRouter_|TestLayoutHandler|TestInstanceHandler'
```

Expected: the three new router tests `--- PASS`, and all existing `TestRouter_*` tests still pass; `ok  	github.com/arbitron/arbitron/adapter/http`.

```bash
go test ./adapter/http/ ./adapter/mcp/
```

Expected: `ok` for both packages. `adapter/mcp/parity_test.go` builds `adapthttp.NewInstanceHandler` without layouts and is unaffected.

- [x] **Step 5: Commit**

```bash
git add adapter/http/server.go adapter/http/server_layout_test.go && git commit -m "feat(http): register layout status and retry routes behind RouterDeps.Layouts" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9.4: Composition root — `routerDeps` extraction + e2e through the real router

**Files:**
- Modify: `cmd/arbitron/main.go:1243-1301` (`runServe`: the `dashHandler`/`notifHandler`/`webhookHandler` construction stays; the `adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{...})` literal moves into the new `routerDeps` method). Add the method directly after `mcpDeps` (ends at line 315).
- Create/Test: `cmd/arbitron/layout_http_e2e_test.go`

- [x] **Step 1: Write the failing test**

Create `cmd/arbitron/layout_http_e2e_test.go`:

```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/config"
)

// newLayoutE2ERouter builds the PRODUCTION router from the real domain stack,
// including the real (local-mode) auth middleware, exactly as runServe does.
func newLayoutE2ERouter(t *testing.T, ds *domainStack) http.Handler {
	t.Helper()
	deps := ds.routerDeps(nil, nil, nil, nil)
	require.NotNil(t, deps.Layouts, "routerDeps must thread the production layout QueryService")
	assert.Same(t, ds.layout.query, deps.Layouts)
	require.NotNil(t, deps.Auth, "the real auth middleware must wrap /api/v1")
	return adapthttp.NewRouterFromDeps(deps)
}

func layoutE2EDo(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var m map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m), "body: %s", w.Body.String())
	return w.Code, m
}

// SINK = the HTTP body. Fails if routerDeps drops Layouts (503), if the routes
// are not registered (404), or if the view is not keyed by the on-disk revision.
func TestLayoutHTTPE2E_WorkflowLayoutReturnsOnDiskRevision(t *testing.T) {
	ds := bootQueryStack(t)
	router := newLayoutE2ERouter(t, ds)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "http-wf", queryE2EWorkflow("http-wf", "Start")))
	rev := manifestRevisionOnDisk(t, ds, "http-wf")

	status, body := layoutE2EDo(t, router, http.MethodGet, "/api/v1/workflows/http-wf/layout", "")
	require.Equal(t, http.StatusOK, status, "body: %v", body)
	assert.Equal(t, rev, body["savedRevision"])
	assert.NotEqual(t, "legacy", body["displayStatus"])

	// The manifest read carries the same token, so a client can join the two.
	status, manifest := layoutE2EDo(t, router, http.MethodGet, "/api/v1/workflows/http-wf", "")
	require.Equal(t, http.StatusOK, status)
	pres, ok := manifest["presentation"].(map[string]any)
	require.True(t, ok, "GET /workflows/{name} must include presentation metadata")
	assert.Equal(t, rev, pres["revision"])
}

func TestLayoutHTTPE2E_RetryValidatesAndConflicts(t *testing.T) {
	ds := bootQueryStack(t)
	router := newLayoutE2ERouter(t, ds)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "http-retry", queryE2EWorkflow("http-retry", "Start")))

	status, body := layoutE2EDo(t, router, http.MethodPost, "/api/v1/workflows/http-retry/layout/retry", `{"expectedRevision":"stale-revision"}`)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "CONFLICT", body["code"])

	status, body = layoutE2EDo(t, router, http.MethodPost, "/api/v1/workflows/http-retry/layout/retry", `{}`)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "expectedRevision is required", body["error"])

	status, _ = layoutE2EDo(t, router, http.MethodGet, "/api/v1/workflows/does-not-exist/layout", "")
	assert.Equal(t, http.StatusNotFound, status)
}

func TestLayoutHTTPE2E_InstanceReadsCarrySnapshotRevision(t *testing.T) {
	ds := bootQueryStack(t)
	router := newLayoutE2ERouter(t, ds)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "http-inst", queryE2EWorkflow("http-inst", "Start")))
	id, _, err := ds.eng.Trigger(ctx, "http-inst", map[string]any{}, "normal")
	require.NoError(t, err)
	snapRev := snapshotRevision(t, ds, id)

	status, body := layoutE2EDo(t, router, http.MethodGet, "/api/v1/instances/"+id.String(), "")
	require.Equal(t, http.StatusOK, status)
	lay, ok := body["layout"].(map[string]any)
	require.True(t, ok, "GET /instances/{id} must embed the layout block in production")
	assert.Equal(t, snapRev, lay["savedRevision"])

	status, view := layoutE2EDo(t, router, http.MethodGet, "/api/v1/instances/"+id.String()+"/layout", "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, snapRev, view["savedRevision"])

	status, _ = layoutE2EDo(t, router, http.MethodPost, "/api/v1/instances/"+id.String()+"/layout/retry", `{"expectedRevision":"stale-revision"}`)
	assert.Equal(t, http.StatusConflict, status)
}

type sentinelHandler struct{ name string }

func (sentinelHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

// routerDeps threads the transport handlers runServe builds, and never stores a
// typed-nil Layouts when the stack has no layout subsystem.
func TestDomainStack_RouterDeps_ThreadsHandlersAndNilLayouts(t *testing.T) {
	ds := &domainStack{cfg: &config.Config{}}
	mcp := sentinelHandler{"mcp"}
	dash := sentinelHandler{"dash"}
	notif := &adapthttp.NotificationHandler{}
	webhook := &adapthttp.WebhookHandler{}

	deps := ds.routerDeps(mcp, dash, notif, webhook)
	assert.Equal(t, mcp, deps.MCPHandler)
	assert.Equal(t, dash, deps.DashboardHandler)
	assert.Same(t, notif, deps.NotifHandler)
	assert.Same(t, webhook, deps.WebhookHandler)
	assert.Nil(t, deps.Layouts, "no layout stack ⇒ a nil interface, not a typed nil")
}
```

- [x] **Step 2: Run it, confirm it fails**

```bash
go test ./cmd/arbitron/ -run 'TestLayoutHTTPE2E|TestDomainStack_RouterDeps' -v
```

Expected:

```
./layout_http_e2e_test.go:...: ds.routerDeps undefined (type *domainStack has no field or method routerDeps)
FAIL	github.com/arbitron/arbitron/cmd/arbitron [build failed]
```

- [x] **Step 3: Minimal implementation**

In `cmd/arbitron/main.go`, add directly after the `mcpDeps` method:

```go
// routerDeps builds the HTTP RouterDeps from the shared domain stack. runServe
// supplies the transport handlers it constructs (MCP-over-HTTP, embedded
// dashboard, notifications, webhooks); an e2e test may pass nil for any it does
// not exercise — the router skips nil optional handlers. Layouts is assigned
// only when the layout subsystem exists, so the interface is never a typed nil.
func (ds *domainStack) routerDeps(mcpHandler http.Handler, dashHandler http.Handler, notifHandler *adapthttp.NotificationHandler, webhookHandler *adapthttp.WebhookHandler) adapthttp.RouterDeps {
	deps := adapthttp.RouterDeps{
		Service:             ds.eng,
		Workflows:           ds.workflowRepo,
		WorkflowSvc:         ds.workflowSvc,
		Instances:           ds.instanceRepo,
		NodeExecs:           ds.nodeExecRepo,
		Snapshots:           ds.snapshotRepo,
		Dismissals:          ds.dismissalRepo,
		DismissalUsers:      ds.userRepo,
		FileReader:          ds.fileReader,
		EventBus:            ds.bus,
		Config:              ds.cfg,
		Settings:            ds.settings,
		QueueManager:        ds.queueMgr,
		QueueRepo:           ds.queueRepo,
		DashboardHandler:    dashHandler,
		NotifHandler:        notifHandler,
		ConversationHandler: ds.conversationHandler,
		WebhookHandler:      webhookHandler,
		MCPHandler:          mcpHandler,
		ProfileChecker:      ds.profileChecker,
		ConnChecker:         ds.connChecker,
		Auth:                ds.authMW,
		AuthHandler:         ds.authHandler,
		Tokens:              ds.tokenSvc,
		DeviceCodes:         ds.deviceSvc,
		PublicBaseURL:       ds.cfg.Server.PublicBaseURL,
		Users:               ds.userSvc,
		DashboardStats:      ds.dashboardSvc,
		ScheduleHandler:     adapthttp.NewScheduleHandler(ds.scheduleSvc),
		BackendHealth:       ds.backendHealth,
	}
	if q := ds.layoutQuery(); q != nil {
		deps.Layouts = q
	}
	return deps
}
```

In `runServe`, replace the whole statement

```go
	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:             ds.eng,
		...
		BackendHealth:       ds.backendHealth,
	})
```

(lines 1266-1298) with:

```go
	router := adapthttp.NewRouterFromDeps(ds.routerDeps(mcpSrv.HTTPHandler(), dashHandler, notifHandler, webhookHandler))
```

Keep the preceding `dashHandler`, `notifHandler` (with `SetEventBus`) and `webhookHandler` construction unchanged.

- [x] **Step 4: Run it, confirm it passes**

```bash
go build ./... && go test ./cmd/arbitron/ -run 'TestLayoutHTTPE2E|TestDomainStack_RouterDeps' -v
```

Expected: `--- PASS` for `TestLayoutHTTPE2E_WorkflowLayoutReturnsOnDiskRevision`, `TestLayoutHTTPE2E_RetryValidatesAndConflicts`, `TestLayoutHTTPE2E_InstanceReadsCarrySnapshotRevision`, `TestDomainStack_RouterDeps_ThreadsHandlersAndNilLayouts`; `ok  	github.com/arbitron/arbitron/cmd/arbitron`.

```bash
go vet ./adapter/http/ ./cmd/arbitron/ && go test ./adapter/http/ ./cmd/arbitron/
```

Expected: no vet findings; `ok` for both packages.

- [x] **Step 5: Commit**

```bash
git add cmd/arbitron/main.go cmd/arbitron/layout_http_e2e_test.go && git commit -m "feat(http): build the router from domainStack.routerDeps with the layout query service" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

| Component | Composition-root wiring | Data source |
|---|---|---|
| `LayoutHandler` (4 routes) | `adapter/http/server.go:NewRouterFromDeps` builds it from `RouterDeps.Layouts`. `RouterDeps` comes from `cmd/arbitron/main.go:(*domainStack).routerDeps`, called by `runServe` (this task, 9.3/9.4). | `ds.layoutQuery()` → `ds.layout.query`, set by `(*layoutStack).attachQuery(workflowSvc, eng)` in `initDomain` (**P3T8**). The views come from `layout.Reader` over `adaptsqlite.LayoutRepo` (**P2T5/P2T6**), whose rows are written by the save-path stamper (**P2T2/P2T3**) and `layout.Worker` (**P2T8**). |
| `InstanceHandler.Get` layout block | `instHandler.SetLayouts(d.Layouts)` in `NewRouterFromDeps` (9.3). | Same `QueryService.InstanceLayout`, keyed by `instance.Instance.WorkflowSnapshot`, which `engine.TriggerWithDepartment` writes. |
| Consumers | Dashboard API client `api.workflows.getLayout/retryLayout`, `api.instances.getLayout/retryLayout` (**P3T1**) → `useLayoutStatus` (**P3T11**) → WorkflowReview (**P3T12**) / ExecutionView (**P3T13**). | — |

Provenance probe: the `cmd/arbitron` e2e tests build the router from `initDomain` + `routerDeps` and send requests through the real local-mode `AuthMiddleware`, which injects the default admin identity. They assert the HTTP body equals the revision on disk and in the snapshot. If `Layouts` is dropped the routes answer 503 and the tests fail. If the instance block is unwired, `layout` is absent and the tests fail.

### Alignment TDD: Retry reaches the stored-result sink

- [x] Extend the real-router composition test with a passed row whose persisted geometry
  is incompatible: GET must show attention/fallback and Retry, POST retry must return 202,
  and the stored generation must increase. Use P2T5/P2T6 InvalidateResult through Requester,
  not a special HTTP repair path. A valid passed geometry returns 409, and store outage
  yields the explicit retry error while retaining fallback.
- [x] Run `go test ./adapter/http ./cmd/arbitron -run 'TestLayoutHandler|TestLayoutHTTPE2E' -count=1`
  RED before the domain correction and GREEN after it; commit the sink regression.

### HTTP source-truth regression cases

Add real-router tests for unauthorized workflow/instance reads and retries (404 concealment
versus 403 intervention policy), absence of Layouts (503 / embedded available=false), body
limit and trailing JSON. Assert actual error/code wire fields and WriteError's sanitized
5xx text. Client tests must consume that wire shape; fabricated {message} alone hides a
real integration defect. Instance fallback from an authorized snapshot sets Unreviewed and
RetryEligible for saved revisions; never lose Retry because the query itself failed.

### Task 9.5: Live peer-crash read settlement is visible through HTTP

**Files:** workflow_handler.go (+ focused tests), layout_handler.go/tests and real-root
layout_http_e2e_test.go. Consumes P2T2 workflow.ErrMutationPending, P2T7 WorkflowRepo
read settlement and P2T9 repo.SetReadSettler(commits).

- [x] RED: Launch two real roots/processes over one workspace. Kill a writer after staged
  shared/rename tree visibility but before exact revision/registry commit, then read
  through the still-running peer's authorized workflow GET and layout query. Assert no
  uncommitted manifest/revision/geometry leaks. An undecided relocation yields HTTP503
  UNAVAILABLE and the static recovery message below; poll/recovery then returns only
  the settled committed revision. Repeat a save while settlement is pending.

> Current plan correction (§6.18): the real FileReader now has SetSettledRepository and initDomain injects it; withExisting settles before existence/scope selection and holds the lock through raw reserved-byte reads. Rename also has its private active-operation context. Complete the required two-process HTTP matrix, including GET files/workflow.json as the FIRST surviving-peer read, private swap gap and before/after registry proof. Removing either read-settler injection must fail committed-byte/unavailable sink assertions. No unlocked preflight Load may substitute.

> Resolved (2026-10-19): the actual Service.Rename subprocess and already-running authorized HTTP peer now cover private/shared × before/after registry commit × raw/manifest/layout first-read order. Every undecided read and repeated save returns the fixed 503; recovery returns the exact winning manifest/revision and immutable Store input, never the losing candidate. A separate killed subprocess leaves durable private tree-swap evidence with a same-name legacy shared fallback; its first raw HTTP read restores exact private bytes and never enqueues the replacement. Removing either composition-root settlement injection fails the actual HTTP sink; both injections are restored.

- [x] GREEN: Map ErrMutationPending before generic errors in WorkflowHandler.Get and all
  workflow authoring handlers as well as writeLayoutError. Use a shared safe response:
  WriteJSON(503, ErrorResponse{Error: "workflow destination is awaiting durable mutation
  recovery; retry shortly", Code: "UNAVAILABLE"}). Keep the string one line in source.
  This sentinel intentionally uses a safe fixed message; never expose raw filesystem or
  registry errors, and preserve normal WriteError sanitization for every other5xx.
- [x] Run focused HTTP/root tests RED→GREEN. Client request reads this actual error/code
  wire; both graph routes retain last compatible graph and back off. No adapter recovery
  bypass or fake committed source may make the test pass. P3T14 repeats the browser sink.

Resolved on production-consumer revisit: P3T11–P3T13 now mount the real hook/panel. `TestLayoutSurfaces_HTTPRecoveryRetainsGraphAndBacksOff` uses initDomain, actual HTTP authoring, persisted Engine snapshot, real browser review/SQLite, authorized router and embedded Chrome routes. A durable covering intent produces the workflow's actual HTTP503 UNAVAILABLE/static recovery body; a recoverable SQLite table outage produces the instance's actual available=false view without geometry. Both routes retain the complete previously reviewed DOM geometry (boxes, text, full routes, markers, zoom), show actual Retry503 error text, poll after two then four seconds, and recover on their own next timer without navigation/focus/replay. The disposable cache is removed, the worker stopped, no response or geometry is fabricated, and failed retries leave generation/revision unchanged. This closes only the consumer gate; the separate rename/raw-file safety blocker above remains unchecked.

## Execution evidence (this pass)

- Ordered plan scan: P1 tasks are checked; earlier unfinished tasks carry recorded protocol/dependency/render acceptance blockers. Selected independent P3T9 after confirming P3T8's real QueryService/root producers now exist. Did not alter another task or repeat the already-characterized rename protocol failure.
- 9.1 RED: missing handler/port symbols, followed by compiling HTTP501 declarations and actual response/body failures (`/tmp/p3t9-1-behavioral-red.log`). GREEN: all 13 handler tests, including argument-keyed unknown-subject controls, wrapped errors, unsafe names/IDs, body limit/trailing JSON and sanitized 5xx. Commit 3ad80d9.
- 9.2 RED: missing SetLayouts, then a declaration-only setter left layout absent at the instance HTTP sink (`/tmp/p3t9-2-red.log`). GREEN: five new snapshot/fallback/nil/not-found tests plus existing instance tests. Saved-revision fallback explicitly asserts Unreviewed and RetryEligible. Commit 9ed5eae.
- 9.3 RED: missing RouterDeps.Layouts, then a field-only declaration yielded HTTP405 and the wrong embedded revision (`/tmp/p3t9-3-red.log`). GREEN: three real-router dependency tests; complete adapter/http and adapter/mcp packages passed. Commit c99069e.
- 9.4 RED: missing routerDeps, then an empty compiling dependency bundle failed real-root provenance assertions (`/tmp/p3t9-4-red.log`). Production runServe now calls the extracted method with its original transport handlers and non-nil QueryService. Tests assert HTTP bodies against actual filesystem/SQLite revisions and persisted budgets/generations. Updating the workflow to a distinct R2 before instance GET proves the run still returns snapshot R1, not the current definition.
- Alignment: actual initDomain authoring → real Chrome review → passed SQLite geometry → HTTP409 for a valid pass. Corrupting that stored geometry yields needs_attention, two captured fallback positions and Retry; a real SQL table outage preserves fallback and returns sanitized 503 UNAVAILABLE on retry. Recovery POST returns 202 and increments the persisted job generation, retaining immutable result history. The domain already implemented this repair: a temporary mutation refusing rejected passes caused the HTTP202 assertion to fail with 409 (`/tmp/p3t9-retry-mutation-red.log`), then restoring the existing Requester made it green. No domain repair path was added to HTTP. An initially over-strong test assertion requiring resultGeneration=0 was removed after inspecting the specified immutable-history protocol; it is not required by this task.
- Source truth: real persisted bearer tokens/users and org-mode AuthMiddleware prove anonymous 401, private concealment 404 and department-viewer read200/retry403. Anonymous middleware uses its existing UNAUTHORIZED code; the handler's domain-unauthenticated mapping remains UNAUTHENTICATED. Real-root nil Layouts controls assert all four routes503 and an embedded unavailable/unreviewed/retryable snapshot view. Existing dashboard client tests consume actual error/code wire fields (30 passed).
- 9.5 independent slices: two roots/processes, actual filesystem/SQLite saves and SIGKILL before/after shared publication proof; both manifest-first and layout-first HTTP bodies show only the settled committed revision. Parent worker is stopped before the crash so its first HTTP read, not a background sweep, performs settlement. Actual CommitSource recovery then proves receipt input is retained only for the committed candidate. Real durable covering intents expose static503 on workflow GET/validate/trigger/create/update/delete/publish/copy/visibility, layout GET/retry, and position/supporting-file writes; recovery by registry authority restores normal reads. HTTP500→503 behavioral RED was observed before mapping (`/tmp/p3t9-recovery-red.log`, `/tmp/p3t9-authoring-red.log`). Private rename and raw-file bypass acceptance remain explicitly blocked above.

### Self-review (in required order)

1. **Spec compliance:** checked contracts §3/§6.7/§6.16–17 and all Task9 additions. Preserved all original router collaborators and exact method signature, left manual layout PUT removal to P3T14, and added no flags/options/alternate repair behavior. Corrected joined-error precedence with behavioral RED (404 expected, 503 actual) then GREEN. The two incomplete combined acceptance gates above are not waived.
2. **Anti-green-theater:** dropping Layouts at the real root failed HTTP bodies with503, not merely a constructor/call-count assertion (`/tmp/p3t9-wiring-mutation-red.log`). Removing SetReadSettler injection made both shared pre-proof HTTP reads expose the wrong uncommitted revision (`/tmp/p3t9-settler-mutation-red.log`). Refusing incompatible-pass repair failed the real persisted-generation retry path. Historical/current revisions are deliberately different, fallback coordinates are nonempty, fake handler subjects/revisions are exact-keyed, and real authorization is exercised rather than faked. All production mutations were restored.
3. **Code quality:** read neighboring HTTP errors/handlers, workflow/engine access seams, Requester/Reader/SQLite invalidation and mcpDeps/runServe construction. Kept consumer-owned ports, bounded one-object retry decoding, centralized sentinel mapping/safe recovery response, and idiomatic formatting. No production fake, missing-input no-op, browser substitute or unrelated visual/generated work was added.

### Final verification

- `go build ./...` and `go vet ./adapter/http/ ./cmd/arbitron/`: pass.
- `ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/http/ ./cmd/arbitron/ -run 'TestRouter_|TestLayoutHandler|TestInstanceHandler|TestLayoutHTTPE2E|TestDomainStack_RouterDeps' -count=1`: pass (HTTP 1.105s, root 10.071s); 31 new top-level Go tests across handler/router/root suites, with additional table subtests.
- `ARBITRON_REQUIRE_BROWSER=1 go test -race ./adapter/http ./cmd/arbitron -run 'TestLayoutHandler|TestLayoutHTTPE2E|TestDomainStack_RouterDeps' -count=1`: pass (1.883s / 19.485s), no race reports and no browser skips.
- `go test ./adapter/http ./adapter/mcp`: pass (6.205s / cached).
- Complete prescribed `go test ./adapter/http/ ./cmd/arbitron/`: pass (HTTP cached, root 371.577s; `/tmp/p3t9-full-final.log`, exit0). An earlier concurrently running package command compiled the pre-correction resultGeneration test and failed that one assertion; the corrected full rerun is green.
- `cd dashboard && npx vitest run src/api/client.test.ts`: 30 passed; existing Node localstorage warning only. No dashboard source/build changes were needed.
- `gofmt -l` and `git diff --check`: clean. Pre-existing untracked `docs/visuals/` is preserved and excluded from commits.

## Production-consumer revisit: HTTP recovery retention/backoff

Selected the first independently unblocked gate after scanning the ordered plans: earlier unchecked gates retain their recorded protocol, unavailable-environment or whole-suite blockers. No repeated failing rename/release run and no next-task implementation was attempted.

Strict verification TDD for this already-implemented consumer gate:
- Wrote the real-root/browser regression, disconnected the existing outage timer, rebuilt embedded assets, and observed both subtests fail at `both outage forms must continue polling` (`/tmp/p3t9-backoff-red.log`). Restoring the timer and rebuilding made both pass (`/tmp/p3t9-backoff-green.log`). No new production behavior was needed.
- Independently removed same-revision geometry retention, rebuilt, and observed the instance DOM geometry sink fail (`/tmp/p3t9-retention-red.log`). Restored the guard and rebuilt again. All temporary production mutations and generated-asset differences are gone.

Ordered self-review before committing:
1. **Spec compliance:** implemented only this task's pending consumer-verification step. Actual durable settlement/storage errors, not a mock recovery implementation, feed both production routes. The instance read correctly uses the real in-band unavailable contract; its actual Retry route returns HTTP503 UNAVAILABLE. The private-rename/raw-file gate remains blocked, not waived.
2. **Anti-green-theater:** neither nil wiring nor empty input can pass the initial real reviewed-DOM assertions. Cache removal plus the actual geometry-free outage body requires client retention. Timer and retention mutations independently fail consumer sinks. Strengthened Retry checks to assert the actual error/code wire and exact DOM text (static recovery reason versus sanitized internal-server error), not merely a 503 status. Recovery requires an autonomous scheduled GET; persisted generation and manifest token cannot change.
3. **Code quality:** reused neighboring surface/browser/root helpers, synchronized immutable HTTP observations, forwarded real router responses unchanged, bounded cleanup, and named polling-policy timing tolerance. No duplicated production path, speculative protocol fix, new flag or unrelated user file was added.

After all review fixes and mutation restoration:
- `make ui`: pass; embedded tracked assets unchanged.
- `go build ./... && go vet ./adapter/http ./cmd/arbitron && go test ./adapter/http`: pass.
- `ARBITRON_REQUIRE_BROWSER=1 go test ./cmd/arbitron -run 'TestLayoutHTTPE2E|TestDomainStack_RouterDeps|TestLayoutSurfaces_HTTPRecovery' -count=1`: pass (60.685s).
- `ARBITRON_REQUIRE_BROWSER=1 go test -race ./adapter/http ./cmd/arbitron -run 'TestLayoutHandler|TestLayoutHTTPE2E|TestDomainStack_RouterDeps|TestLayoutSurfaces_HTTPRecovery' -count=1 -v`: pass (root 79.869s), no race reports/skips. New route subtests log actual backoff intervals and full persisted-geometry DOM parity.
- `npx tsc --noEmit` plus client/hook/panel/WorkflowReview/ExecutionView layout suites with `--maxWorkers=2`: 154 tests pass across five files.
- Whole-module/sample/rename blockers recorded in other tasks were not repeatedly re-failed. `test-render` already selects the new `LayoutSurfaces` test.

## Identity and cancellation are load-bearing test arguments

- [x] RED→GREEN/mutation: `TestLayoutHTTP_ForwardsIdentityAndCancellation` sends a request
  with the actual identity context and a private sentinel value through the handler/tool.
  `fakeLayoutQuery.recordedContexts` must retain that exact identity/value, not a
  Background context. Each fake method rejects canceled context before returning
  successful geometry/RetryAck; canceled requests cannot advance a real Store row.
  Exercise both workflow and instance reads/retries supported by this adapter. Keep
  real-root authorization/concealment and immutable-result sink tests as well.
- [x] Remove context forwarding or the fake cancellation check independently and
  observe behavioral failure, restore, then run the affected adapter tests and the
  existing production router/tool suite. Call-count assertions alone cannot prove
  the user identity reached the service authorization boundary.

## HTTP peer-crash and context acceptance revisit (2026-10-19)

- Worked only P3T9 after retaining P3T2's independent font-inventory blocker and P3T7's recorded whole-suite/race blockers. The earlier rename/raw-file dependency is now available; no stale dependency note was used to skip it.
- Added `cmd/arbitron/layout_http_peer_test.go`: twelve actual Service.Rename subprocess/authorized-router cases cover private/shared, pre/post registry identity commit and all three first-read orders. Workers are stopped before the writer starts. Pending reads/saves return the exact safe HTTP503 body, change no manifest bytes and create no candidate Store row. Real CommitSource recovery chooses the registry-backed winner; HTTP returns its full immutable manifest/revision and Store destination/input. The private swap-gap subprocess preserves real durable tree/journal evidence; the first raw HTTP read restores exact private bytes instead of a deliberately different same-name legacy shared tree, and the replacement never reaches Store.
- Characterization RED: removing `FileReader.SetSettledRepository` exposes HTTP200 staged rename bytes (expected503) and serves the wrong shared graph in the private gap (`/tmp/p3t9-peer-raw-red.log`, `/tmp/p3t9-peer-file-red.log`). Independently removing `repo.SetReadSettler` exposes the uncommitted manifest (`/tmp/p3t9-peer-repo-red.log`). Restored wiring passes; no production change was required.
- Added `adapter/http/layout_context_test.go` and `cmd/arbitron/layout_http_context_test.go`. Initial behavioral RED shows all four fake methods ignore identity/cancellation (`/tmp/p3t9-context-red.log`). The minimal helper correction retains actual contexts and rejects cancellation before successful views/acks. Active requests assert exact caller fields and a private sentinel; canceled requests cannot return success. Actual root/router workflow and historical-instance retries preserve the entire eligible failed SQLite record.
- Independent mutation controls: replacing handler contexts with Background fails all identity/cancellation cases (`/tmp/p3t9-forward-red.log`); deleting only fake cancellation checks fails all canceled response sinks (`/tmp/p3t9-fake-cancel-red.log`). Preserving identity but stripping cancellation with WithoutCancel advances the real Store generation and returns202, failing both root retry paths (`/tmp/p3t9-store-cancel-red.log`, `/tmp/p3t9-instance-cancel-red.log`). All mutations are restored.
- Ordered self-review: **(1) Spec:** only the three remaining P3T9 acceptance steps, no new option/feature/recovery bypass and no sibling task. **(2) Anti-green-theater:** actual authorized HTTP bytes/errors plus immutable SQLite inputs/generations are asserted, not merely constructor or call counts; missing wiring and ignored cancellation each fail independently. Strengthened pending-save byte equality and absent replacement-row assertions. **(3) Quality:** reused existing root/crash probes, bounded and joined subprocesses, fsynced the staged swap directory, copied context observations under the existing mutex, and removed a stale blocker comment.
- After review/restoration: `go build ./...` and `go vet ./adapter/http ./cmd/arbitron` pass. Browser-required focused HTTP/root suite under **-race passes** (HTTP1.523s, root29.764s), including the existing authorization/concealment, incompatible-pass retry and immutable-result regressions; no skips/race reports. Dashboard API client **43 tests pass** (existing Node localstorage warning only). Final full-package results are recorded below.
- Full-package verification: HTTP **PASS (6.550s)** and MCP **PASS (2.089s)**. The unchanged full cmd package reaches its **10-minute aggregate deadline**, during `TestLayoutRender_RealSurfaceReportDefectsReachSavedResults/hidden_marker` (parent21s/subtest1s); no behavioral test failure precedes the timeout. Log: `/tmp/p3t9-full-final.log`. This unrelated broad verification is not claimed green, retried indefinitely or hidden by changing timeouts. P3T9's own tests remain green as permitted by the mission's unrelated-suite-failure rule.
- Final post-timeout focused regression, with production restored and all review fixes applied: browser-required **-race PASS** for HTTP **1.853s**, MCP **1.511s**, root **38.612s**, including all HTTP acceptance tests plus the existing MCP summary/retry/root suite (`/tmp/p3t9-restored-final.log`). `git diff --check` passes. No full-module, release or sibling task completion is newly claimed.
