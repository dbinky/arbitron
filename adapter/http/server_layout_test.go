package http_test

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/instance"
)

func TestRouter_PutWorkflowLayout_MethodNotAllowed(t *testing.T) {
	q := newFakeLayoutQuery()
	router, _ := layoutRouter(t, q)
	w := routerDo(router, http.MethodPut, "/api/v1/workflows/my-wf/layout", `{"positions":{"A":{"x":1,"y":2}}}`)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Contains(t, w.Header().Get("Allow"), http.MethodGet)
	assert.Empty(t, q.recorded())
}

func TestWorkflowHandler_HasNoSaveLayout(t *testing.T) {
	_, ok := reflect.TypeOf(&adapthttp.WorkflowHandler{}).MethodByName("SaveLayout")
	assert.False(t, ok, "the manual layout write handler must be removed")
}

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
