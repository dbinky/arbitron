package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Detaching the locked callback from the request's cancellation lets a rejected
// request acknowledge and persist another generation. The hook runs only AFTER
// the real filesystem coordinator owns every destination; no store is replaced.
func TestLayoutSurfaces_CancellationAfterRetryLocksPreservesStore(t *testing.T) {
	for _, transport := range []string{"http", "mcp"} {
		for _, subject := range []string{"workflow", "instance"} {
			t.Run(transport+"/"+subject, func(t *testing.T) {
				e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
				ds, _ := e.boot(t)
				owner := adminCtxForLocalUser(t, ds)
				const name = "cancel-after-lock"
				require.NoError(t, ds.workflowSvc.Create(owner, name, queryE2EWorkflow(name, "Immutable input")))
				rev := manifestRevisionOnDisk(t, ds, name)
				layoutE2EWaitTerminal(t, ds, rev)
				ds.layout.worker.Stop()
				id, _, err := ds.eng.Trigger(owner, name, nil, "normal")
				require.NoError(t, err)
				require.Equal(t, rev, snapshotRevision(t, ds, id))
				before, err := ds.layout.store.Get(owner, rev)
				require.NoError(t, err)
				require.Equal(t, layout.StateFailed, before.State)
				manifest, err := os.ReadFile(e.privatePath(name))
				require.NoError(t, err)

				ctx, cancel := context.WithCancel(owner)
				defer cancel()
				locked := 0
				require.IsType(t, &filesystem.WorkflowRepo{}, ds.layout.mutations)
				coordinator := &queryPausingCoordinator{MutationCoordinator: ds.layout.mutations, locked: func() {
					locked++
					require.NoError(t, ctx.Err(), "initial authorization and lock acquisition must finish before cancellation")
					cancel()
				}}
				ds.layout.query = layout.NewQueryService(ds.layout.reader, ds.layout.requester, ds.workflowSvc, ds.eng, coordinator)
				var retry func(context.Context) (bool, string)
				if transport == "http" {
					router := newLayoutE2ERouter(t, ds)
					// Warm local authentication before the cancellation boundary.
					status, _ := layoutE2EDo(t, router, http.MethodGet, "/api/v1/workflows/"+name+"/layout", "")
					require.Equal(t, http.StatusOK, status)
					path := "/api/v1/workflows/" + name + "/layout/retry"
					if subject == "instance" {
						path = "/api/v1/instances/" + id.String() + "/layout/retry"
					}
					retry = func(ctx context.Context) (bool, string) {
						req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"expectedRevision":"`+rev+`"}`)).WithContext(ctx)
						req.Header.Set("Content-Type", "application/json")
						w := httptest.NewRecorder()
						router.ServeHTTP(w, req)
						return w.Code == http.StatusAccepted, w.Body.String()
					}
				} else {
					srv := newLayoutE2EMCPServer(t, ds)
					retry = func(ctx context.Context) (bool, string) {
						res := callMCPToolE2E(t, srv, ctx, subject+"_layout_retry", map[string]any{"name": name, "id": id.String(), "expectedRevision": rev})
						return !res.IsError, mcpE2EText(t, res)
					}
				}
				accepted, body := retry(ctx)
				assert.False(t, accepted, body)
				require.Equal(t, 1, locked, "the request must reach the post-acquisition boundary")
				require.ErrorIs(t, ctx.Err(), context.Canceled)
				assert.NotContains(t, body, "jobGeneration", "a canceled retry cannot acknowledge a generation")
				after, err := ds.layout.store.Get(owner, rev)
				require.NoError(t, err)
				assert.Equal(t, before, after, "cancellation preserves every stored field, including timestamps and leases")
				afterBytes, err := os.ReadFile(e.privatePath(name))
				require.NoError(t, err)
				assert.Equal(t, manifest, afterBytes)

				// Same identity, token, locks and sink with a live request: exactly
				// one bump. Also proves cancellation released the real lock set.
				coordinator.locked = func() {}
				live, stop := context.WithTimeout(owner, 5*time.Second)
				defer stop()
				accepted, body = retry(live)
				require.True(t, accepted, body)
				var ack layout.RetryAck
				require.NoError(t, json.Unmarshal([]byte(body), &ack))
				require.Equal(t, rev, ack.SavedRevision)
				require.Equal(t, before.JobGeneration+1, ack.JobGeneration)
				after, err = ds.layout.store.Get(owner, rev)
				require.NoError(t, err)
				assert.Equal(t, ack.JobGeneration, after.JobGeneration)
				assert.Equal(t, layout.StatePending, after.State)
				assert.Equal(t, before.Manifest, after.Manifest)
				afterBytes, err = os.ReadFile(e.privatePath(name))
				require.NoError(t, err)
				assert.Equal(t, manifest, afterBytes)
			})
		}
	}
}

// Seed an older saved input below authoring validation: current saves correctly
// reject it. Authorization, queryDefinition's invalid-group exception, worker,
// HTTP authentication, MCP dispatch and SQLite persistence all remain real.
// Removing shared visibility or shared-retry role guards must break this test.
func TestLayoutSurfaces_HiddenInvalidGroupsConcealInputAndPreserveStore(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	ds, _ := e.boot(t)
	owner := adminCtxForLocalUser(t, ds)
	const name, secret = "hidden-invalid-group", "Confidential malformed group input"
	wf := queryE2EWorkflow(name, secret)
	wf.Nodes[0].Type = workflow.NodeTypeParallelGroup // No group config: not a basic node/edge error.
	revision, err := uuid.NewV7()
	require.NoError(t, err)
	wf.ID = uuid.New()
	wf.Presentation = &workflow.Presentation{Revision: revision.String(), Name: name, Scope: workflow.ScopeLabelShared, Reason: workflow.SaveReasonUpdate, SavedAt: time.Now().UTC(), Retained: workflow.RetainedLayout{Source: workflow.RetainedNone}}
	_, err = layout.BuildGraph(wf)
	require.ErrorIs(t, err, layout.ErrInvalidGroups)
	require.NoError(t, ds.workflowRepo.Create(owner, workflow.Shared(), name, wf))
	require.NoError(t, ds.publishedRepo.CommitPublication(owner, name, workflow.Publication{Revision: wf.SavedRevision(), WorkflowID: wf.ID, Publisher: localDefaultUser, SourceName: "legacy-source", Visibility: workflow.VisibilityDepartment, Department: "engineering"}))
	manifest, err := os.ReadFile(e.sharedPath(name))
	require.NoError(t, err)
	require.NoError(t, ds.layout.requester.RequestArrangement(owner, workflow.ArrangementRequest{Revision: wf.SavedRevision(), Scope: workflow.Shared(), Name: name, WorkflowID: wf.ID, Manifest: manifest, Reason: wf.Presentation.Reason, Retained: wf.Presentation.Retained, RequestedAt: wf.Presentation.SavedAt}))
	layoutE2EWaitTerminal(t, ds, wf.SavedRevision())
	ds.layout.worker.Stop()
	before, err := ds.layout.store.Get(owner, wf.SavedRevision())
	require.NoError(t, err)
	require.Equal(t, layout.StateFailed, before.State)
	require.Contains(t, before.FailureReason, layout.ErrInvalidGroups.Error())
	require.Equal(t, manifest, before.Manifest)
	peer := &instance.User{ID: uuid.New(), Username: "outside-engineering", Role: identity.RoleUser}
	require.NoError(t, ds.userRepo.Create(owner, peer))
	outsider := identity.WithIdentity(context.Background(), identity.Identity{UserID: peer.ID, Role: peer.Role})
	token, _, err := ds.tokenSvc.Mint(outsider, "hidden-invalid-group")
	require.NoError(t, err)
	deps := ds.routerDeps(nil, nil, nil, nil)
	deps.Auth = adapthttp.NewAuthMiddleware(identity.ModeOrg, "session", ds.sessionRepo, ds.tokenRepo, ds.userRepo, identity.GroupConfig{DefaultGroup: "default"})
	t.Cleanup(deps.Auth.Stop)
	router := adapthttp.NewRouterFromDeps(deps)
	srv := newLayoutE2EMCPServer(t, ds)
	assertUnchanged := func(t *testing.T) {
		t.Helper()
		after, err := ds.layout.store.Get(owner, wf.SavedRevision())
		require.NoError(t, err)
		assert.Equal(t, before, after)
		afterBytes, err := os.ReadFile(e.sharedPath(name))
		require.NoError(t, err)
		assert.Equal(t, manifest, afterBytes)
	}
	assertConcealed := func(t *testing.T, body string) {
		t.Helper()
		for _, value := range []string{wf.SavedRevision(), wf.ID.String(), secret, "jobGeneration", "savedRevision", "invalid parallel group", "geometry"} {
			assert.NotContains(t, body, value)
		}
	}
	for _, action := range []string{"read", "retry"} {
		t.Run("http/"+action, func(t *testing.T) {
			bodies := make([]string, 0, 2)
			for _, subject := range []string{name, "absent-workflow"} {
				method, path, payload := http.MethodGet, "/api/v1/workflows/"+subject+"/layout", ""
				if action == "retry" {
					method, path, payload = http.MethodPost, path+"/retry", `{"expectedRevision":"`+wf.SavedRevision()+`"}`
				}
				req := httptest.NewRequest(method, path, strings.NewReader(payload))
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				want := http.StatusNotFound
				if action == "retry" && subject == name {
					want = http.StatusConflict // Shared destinations are read-only to non-admins.
				}
				assert.Equal(t, want, w.Code, w.Body.String())
				assertConcealed(t, w.Body.String())
				bodies = append(bodies, strings.ReplaceAll(w.Body.String(), subject, "subject"))
				assertUnchanged(t)
			}
			if action == "read" {
				assert.JSONEq(t, bodies[0], bodies[1], "hidden invalid input has exactly the absent read shape")
			}
		})
		t.Run("mcp/"+action, func(t *testing.T) {
			bodies := make([]string, 0, 2)
			for _, subject := range []string{name, "absent-workflow"} {
				tool := "workflow_get"
				if action == "retry" {
					tool = "workflow_layout_retry"
				}
				res := callMCPToolE2E(t, srv, outsider, tool, map[string]any{"name": subject, "expectedRevision": wf.SavedRevision()})
				assert.True(t, res.IsError, mcpE2EText(t, res))
				body := mcpE2EText(t, res)
				assertConcealed(t, body)
				bodies = append(bodies, strings.ReplaceAll(body, subject, "subject"))
				assertUnchanged(t)
			}
			if action == "read" {
				assert.Equal(t, bodies[0], bodies[1])
			}
		})
	}
	// The exception must still expose actionable failure to an authorized caller.
	local := newLayoutE2ERouter(t, ds)
	status, view := layoutE2EDo(t, local, http.MethodGet, "/api/v1/workflows/"+name+"/layout", "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, wf.SavedRevision(), view["savedRevision"])
	assert.Equal(t, "failed", view["displayStatus"])
	assert.Equal(t, true, view["retryEligible"])
	assert.NotContains(t, view, "geometry")
	res := callMCPToolE2E(t, srv, owner, "workflow_get", map[string]any{"name": name})
	require.False(t, res.IsError, mcpE2EText(t, res))
	assert.Contains(t, mcpE2EText(t, res), wf.SavedRevision())
	assertUnchanged(t)
	res = callMCPToolE2E(t, srv, owner, "workflow_layout_retry", map[string]any{"name": name, "expectedRevision": wf.SavedRevision()})
	require.False(t, res.IsError, mcpE2EText(t, res))
	after, err := ds.layout.store.Get(owner, wf.SavedRevision())
	require.NoError(t, err)
	assert.Equal(t, before.JobGeneration+1, after.JobGeneration)
	assert.Equal(t, before.Manifest, after.Manifest)
}
