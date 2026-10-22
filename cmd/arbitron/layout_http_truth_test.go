package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Start with real saved, browser-reviewed geometry, not a fabricated passed row.
func TestLayoutHTTPE2E_RetryRepairsIncompatiblePersistedPass(t *testing.T) {
	browser := requireBrowserE2E(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	useLayoutE2EBrowser(t, browser)
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	name := "http-repair"
	require.NoError(t, ds.workflowSvc.Create(ctx, name, queryE2EWorkflow(name, "Persisted input")))
	rev := manifestRevisionOnDisk(t, ds, name)
	view := waitTerminalWorkflowView(t, ds, ctx, name, rev)
	require.Equal(t, layout.StatePassed, view.State)
	require.NotNil(t, view.Geometry)
	ds.layout.worker.Stop()
	router := newLayoutE2ERouter(t, ds)
	path := "/api/v1/workflows/" + name + "/layout"
	body := `{"expectedRevision":"` + rev + `"}`
	status, got := layoutE2EDo(t, router, http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "complete", got["displayStatus"])
	assert.Equal(t, false, got["retryEligible"])
	status, got = layoutE2EDo(t, router, http.MethodPost, path+"/retry", body)
	require.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "CONFLICT", got["code"])

	result, err := ds.layout.store.Result(ctx, rev, view.ResultGeneration)
	require.NoError(t, err)
	result.Geometry.Nodes[0].Text = "Not the saved label"
	raw, err := json.Marshal(result.Geometry)
	require.NoError(t, err)
	_, err = ds.db.Exec("UPDATE layout_results SET geometry=? WHERE revision=? AND generation=?", string(raw), rev, view.ResultGeneration)
	require.NoError(t, err)
	status, got = layoutE2EDo(t, router, http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "needs_attention", got["displayStatus"])
	assert.Equal(t, true, got["retryEligible"])
	assert.Nil(t, got["geometry"])
	retained := got["retained"].(map[string]any)
	assert.Len(t, retained["positions"], 2)

	// A real SQL outage retains the captured fallback and reports an explicit retry error.
	_, err = ds.db.Exec("ALTER TABLE layout_revisions RENAME TO layout_revisions_offline")
	require.NoError(t, err)
	status, got = layoutE2EDo(t, router, http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, false, got["available"])
	assert.Equal(t, true, got["retryEligible"])
	assert.Equal(t, retained, got["retained"])
	status, got = layoutE2EDo(t, router, http.MethodPost, path+"/retry", body)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, "UNAVAILABLE", got["code"])
	assert.Equal(t, "internal server error", got["error"])
	_, err = ds.db.Exec("ALTER TABLE layout_revisions_offline RENAME TO layout_revisions")
	require.NoError(t, err)

	status, got = layoutE2EDo(t, router, http.MethodPost, path+"/retry", body)
	require.Equal(t, http.StatusAccepted, status, "%v", got)
	assert.Equal(t, rev, got["savedRevision"])
	assert.Equal(t, float64(view.JobGeneration+1), got["jobGeneration"])
	row, err := ds.layout.store.Get(ctx, rev)
	require.NoError(t, err)
	assert.Equal(t, view.JobGeneration+1, row.JobGeneration)
	assert.Equal(t, layout.StatePending, row.State)
	assert.Equal(t, view.ResultGeneration, row.ResultGeneration, "retry preserves immutable result history")
	status, got = layoutE2EDo(t, router, http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "pending", got["displayStatus"])
	assert.Equal(t, false, got["retryEligible"])
	assert.Nil(t, got["geometry"], "an incompatible prior result must never be displayed")
	assert.Nil(t, got["evidence"])
	assert.Equal(t, retained, got["retained"])
	status, _ = layoutE2EDo(t, router, http.MethodPost, path+"/retry", body)
	assert.Equal(t, http.StatusConflict, status)
	after, err := ds.layout.store.Get(ctx, rev)
	require.NoError(t, err)
	assert.Equal(t, row, after, "a rejected duplicate retry must not change the pending repair")

	claimed, err := ds.layout.store.Claim(ctx, rev, "repair-test", time.Now(), time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, ds.layout.store.Progress(ctx, layout.Fence{Revision: rev, Generation: claimed.JobGeneration}, layout.StageRendering, 1, time.Now(), time.Now().Add(time.Minute)))
	status, got = layoutE2EDo(t, router, http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "rendering", got["displayStatus"])
	assert.Equal(t, false, got["retryEligible"])
	assert.Equal(t, float64(row.JobGeneration), got["jobGeneration"])
	assert.Nil(t, got["geometry"])
	assert.Nil(t, got["evidence"])
	assert.Equal(t, retained, got["retained"])
}

func TestLayoutHTTPE2E_UnwiredLayoutsAreExplicit(t *testing.T) {
	ds := bootQueryStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "http-nil", queryE2EWorkflow("http-nil", "Start")))
	id, _, err := ds.eng.Trigger(ctx, "http-nil", nil, "normal")
	require.NoError(t, err)
	rev := snapshotRevision(t, ds, id)
	deps := ds.routerDeps(nil, nil, nil, nil)
	deps.Layouts = nil
	router := adapthttp.NewRouterFromDeps(deps)
	for _, path := range []string{"/api/v1/workflows/http-nil/layout", "/api/v1/instances/" + id.String() + "/layout"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if method == http.MethodPost {
				path += "/retry"
			}
			status, body := layoutE2EDo(t, router, method, path, `{"expectedRevision":"`+rev+`"}`)
			assert.Equal(t, http.StatusServiceUnavailable, status)
			assert.Equal(t, "UNAVAILABLE", body["code"])
		}
	}
	status, body := layoutE2EDo(t, router, http.MethodGet, "/api/v1/instances/"+id.String(), "")
	require.Equal(t, http.StatusOK, status)
	got := body["layout"].(map[string]any)
	assert.Equal(t, rev, got["savedRevision"])
	assert.Equal(t, false, got["available"])
	assert.Equal(t, true, got["unreviewed"])
	assert.Equal(t, true, got["retryEligible"])
}

// Real bearer authentication, stored users/tokens and real domain policies: no
// handler fake or injected request identity may grant access on these paths.
func TestLayoutHTTPE2E_AuthorizationAndBodyBoundaries(t *testing.T) {
	ds := bootQueryStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "http-secret", queryE2EWorkflow("http-secret", "Secret")))
	rev := manifestRevisionOnDisk(t, ds, "http-secret")
	id, _, err := ds.eng.Trigger(ctx, "http-secret", nil, "normal")
	require.NoError(t, err)
	user := &instance.User{ID: uuid.New(), Username: "peer", Role: identity.RoleUser}
	require.NoError(t, ds.userRepo.Create(ctx, user))
	peerCtx := identity.WithIdentity(context.Background(), identity.Identity{UserID: user.ID, Role: user.Role})
	token, _, err := ds.tokenSvc.Mint(peerCtx, "http-layout")
	require.NoError(t, err)
	deps := ds.routerDeps(nil, nil, nil, nil)
	deps.Auth = adapthttp.NewAuthMiddleware(identity.ModeOrg, "session", ds.sessionRepo, ds.tokenRepo, ds.userRepo, identity.GroupConfig{DefaultGroup: "default"})
	t.Cleanup(deps.Auth.Stop)
	router := adapthttp.NewRouterFromDeps(deps)
	do := func(method, path, body, bearer string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		var got map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		return w.Code, got
	}
	instancePath := "/api/v1/instances/" + id.String() + "/layout"
	retry := `{"expectedRevision":"` + rev + `"}`
	for _, path := range []string{"/api/v1/workflows/http-secret/layout", instancePath} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if method == http.MethodPost {
				path += "/retry"
			}
			status, got := do(method, path, retry, "")
			assert.Equal(t, http.StatusUnauthorized, status)
			assert.Equal(t, "UNAUTHORIZED", got["code"])
			status, got = do(method, path, retry, token)
			assert.Equal(t, http.StatusNotFound, status)
			assert.Equal(t, "NOT_FOUND", got["code"])
		}
	}
	// A department peer may view the immutable run, but cannot intervene/retry.
	_, err = ds.db.Exec("UPDATE instances SET visibility='department', department='default' WHERE id=?", id.String())
	require.NoError(t, err)
	status, got := do(http.MethodGet, instancePath, "", token)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, rev, got["savedRevision"])
	status, got = do(http.MethodPost, instancePath+"/retry", retry, token)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "FORBIDDEN", got["code"])
	// Local admin through the production router exercises the parser at its real seam.
	local := newLayoutE2ERouter(t, ds)
	for _, body := range []string{retry + ` {}`, `{"expectedRevision":"` + strings.Repeat("r", 1<<16) + `"}`} {
		status, got := layoutE2EDo(t, local, http.MethodPost, instancePath+"/retry", body)
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "BAD_REQUEST", got["code"])
	}
}
