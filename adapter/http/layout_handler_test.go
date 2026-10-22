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
// subject. Canceled contexts fail before lookup; other calls retain their
// arguments and original contexts for identity/propagation assertions.
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
	if err := ctx.Err(); err != nil {
		return layout.View{}, err
	}
	f.record(ctx, "WorkflowLayout:"+name)
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
	if err := ctx.Err(); err != nil {
		return layout.RetryAck{}, err
	}
	f.record(ctx, "RetryWorkflow:"+name+"@"+expected)
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
	if err := ctx.Err(); err != nil {
		return layout.View{}, err
	}
	f.record(ctx, "InstanceLayout:"+id.String())
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
	if err := ctx.Err(); err != nil {
		return layout.RetryAck{}, err
	}
	f.record(ctx, "RetryInstance:"+id.String()+"@"+expected)
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
		"trailing json": {`{"expectedRevision":"rev-1"} {}`, "invalid request body"},
		"trailing junk": {`{"expectedRevision":"rev-1"} junk`, "invalid request body"},
		"oversized":     {`{"expectedRevision":"` + strings.Repeat("r", 1<<16) + `"}`, "invalid request body"},
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
		{"mutation pending", workflow.ErrMutationPending, http.StatusServiceUnavailable, "UNAVAILABLE"},
		{"not-found precedence", errors.Join(workflow.ErrNotFound, workflow.ErrMutationPending), http.StatusNotFound, "NOT_FOUND"},
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
				body := decodeBody(t, w)
				assert.Equal(t, tc.code, body["code"])
				if tc.status == http.StatusServiceUnavailable && errors.Is(tc.err, workflow.ErrMutationPending) {
					assert.Equal(t, "workflow destination is awaiting durable mutation recovery; retry shortly", body["error"])
				} else if tc.status >= 500 {
					assert.Equal(t, "internal server error", body["error"])
				}
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
