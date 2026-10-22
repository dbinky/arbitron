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
	assert.NotNil(t, deps.Layouts, "routerDeps must thread the production layout QueryService")
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
	row, err := ds.layout.store.Get(ctx, rev)
	require.NoError(t, err)
	assert.Equal(t, float64(row.AttemptLimit), body["attemptLimit"])
	assert.Positive(t, row.AttemptLimit)
	assert.Equal(t, float64(row.JobGeneration), body["jobGeneration"])

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
	require.NoError(t, ds.workflowSvc.Update(ctx, "http-inst", queryE2EWorkflow("http-inst", "Newer workflow")))
	currentRev := manifestRevisionOnDisk(t, ds, "http-inst")
	require.NotEqual(t, snapRev, currentRev)
	status, current := layoutE2EDo(t, router, http.MethodGet, "/api/v1/workflows/http-inst/layout", "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, currentRev, current["savedRevision"])

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
