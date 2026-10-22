package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing the destination binding lets an ordinary user's pre-existing private
// shadow advance (or invalidate) the shared row merely by retaining its token.
func TestLayoutRetryE2E_PreExistingPrivateShadowCannotRetrySharedRow(t *testing.T) {
	for _, state := range []string{"failed", "invalid-passed"} {
		t.Run(state, func(t *testing.T) {
			e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
			ds, _ := e.boot(t)
			admin := adminCtxForLocalUser(t, ds)
			const name = "shadow-retry"
			require.NoError(t, ds.workflowSvc.Create(admin, "source", queryE2EWorkflow("source", "Shared input")))
			require.NoError(t, ds.workflowSvc.Publish(admin, "source", name, workflow.VisibilityPublic, ""))
			wf, err := ds.workflowRepo.Load(admin, workflow.Shared(), name)
			require.NoError(t, err)
			layoutE2EWaitTerminal(t, ds, wf.SavedRevision())
			ds.layout.worker.Stop()
			if state == "invalid-passed" {
				// Missing result evidence must not allow invalidation before binding.
				_, err = ds.db.Exec("UPDATE layout_revisions SET state='passed',verdict='passed' WHERE revision=?", wf.SavedRevision())
				require.NoError(t, err)
			}
			peer := &instance.User{ID: uuid.New(), Username: "shadow-owner", Role: identity.RoleUser}
			require.NoError(t, ds.userRepo.Create(admin, peer))
			ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: peer.ID, Role: peer.Role})
			// Simulate a legacy/copied manifest, bypassing new-save token stamping.
			require.NoError(t, ds.workflowRepo.Create(ctx, workflow.Private(peer.ID), name, wf))
			resolved, scope, err := ds.workflowSvc.AuthorizeLayoutRetry(ctx, name)
			require.NoError(t, err)
			require.Equal(t, workflow.Private(peer.ID), scope)
			require.Equal(t, wf.SavedRevision(), resolved.SavedRevision())
			before, err := ds.layout.store.Get(admin, wf.SavedRevision())
			require.NoError(t, err)
			shared, err := os.ReadFile(e.sharedPath(name))
			require.NoError(t, err)
			private, err := ds.workflowRepo.Load(ctx, scope, name)
			require.NoError(t, err)

			token, _, err := ds.tokenSvc.Mint(ctx, "shadow-retry")
			require.NoError(t, err)
			deps := ds.routerDeps(nil, nil, nil, nil)
			deps.Auth = adapthttp.NewAuthMiddleware(identity.ModeOrg, "session", ds.sessionRepo, ds.tokenRepo, ds.userRepo, identity.GroupConfig{DefaultGroup: "default"})
			t.Cleanup(deps.Auth.Stop)
			router := adapthttp.NewRouterFromDeps(deps)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/"+name+"/layout/retry", strings.NewReader(`{"expectedRevision":"`+wf.SavedRevision()+`"}`))
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, "CONFLICT", body["code"])
			after, err := ds.layout.store.Get(admin, wf.SavedRevision())
			require.NoError(t, err)
			assert.Equal(t, before, after)

			srv := newLayoutE2EMCPServer(t, ds)
			result := callMCPToolE2E(t, srv, ctx, "workflow_layout_retry", map[string]any{"name": name, "expectedRevision": wf.SavedRevision()})
			assert.True(t, result.IsError, mcpE2EText(t, result))
			assert.Contains(t, mcpE2EText(t, result), "revision conflict")
			after, err = ds.layout.store.Get(admin, wf.SavedRevision())
			require.NoError(t, err)
			assert.Equal(t, before, after)
			sharedAfter, err := os.ReadFile(e.sharedPath(name))
			require.NoError(t, err)
			assert.Equal(t, shared, sharedAfter)
			privateAfter, err := ds.workflowRepo.Load(ctx, scope, name)
			require.NoError(t, err)
			assert.Equal(t, private, privateAfter)
		})
	}
}
