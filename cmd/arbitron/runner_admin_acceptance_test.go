package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

// Acceptance coverage for the service installed by the application root, not a
// separately assembled service or an adapter stub.
func TestRunnerAdminRoot_UncordonAuditedAndWakesDispatch(t *testing.T) {
	ds, fake := rwBoot(t, 8)
	ctx := adminCtxForLocalUser(t, ds)
	rwChatProfile(t, ctx, ds)
	rwTouch(t, fake, "release-all")
	call := mcpToolCallerAs(t, ds, ctx)
	isErr, text := call("runner_cordon", map[string]any{"id": "builtin"})
	require.False(t, isErr, text)
	id, _, err := ds.eng.Trigger(ctx, "rr-block", map[string]any{}, "normal")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ds.eng.Cancel(ctx, id) })
	require.Eventually(t, func() bool {
		st, ok := rwExecutionOf(ctx, ds, id)
		return ok && st.State == instance.ExecWaitingCapacity
	}, 15*time.Second, 20*time.Millisecond)
	require.Zero(t, rwCalls(fake), "cordoned dispatch must not launch")

	ch, stop := ds.bus.SubscribeAll()
	defer stop()
	srv := httptest.NewServer(adapthttp.NewRouterFromDeps(ds.httpDeps()))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/v1/runners/builtin/uncordon", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(raw))
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
	assert.Equal(t, 1, rwCalls(fake))
	assert.True(t, rwEveryOpPlacedOnBuiltin(rwJobs(t, ds.db, id), map[string]string{"ask": "chat"}))

	var detail string
	require.NoError(t, ds.db.QueryRow(`SELECT detail FROM audit_events WHERE action = ? AND target = 'builtin'`, string(audit.ActionRunnerUncordon)).Scan(&detail))
	assert.JSONEq(t, `{"cordoned":false}`, detail)
	var count int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM pool_events WHERE kind = ? AND runner_id = 'builtin'`, runner.EventRunnerUncordoned).Scan(&count))
	assert.Equal(t, 1, count)
	found := false
	for !found {
		select {
		case ev := <-ch:
			found = ev.Event.Type == runner.EventRunnerUncordoned
		case <-time.After(5 * time.Second):
			t.Fatal("uncordon must publish the persisted operator event on the root bus")
		}
	}
}

func TestRunnerAdminRoot_UnavailableReasonSurvivesRESTAndMCP(t *testing.T) {
	ds, _ := rwBoot(t, 8) // isolated PATH has no Codex installation
	ctx := adminCtxForLocalUser(t, ds)
	src, err := ds.runners.registry.GetSource(ctx, runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredOpenAISubscription, ""))
	require.NoError(t, err)
	require.Equal(t, "harness_not_found", string(src.UnavailableReason))
	srv := httptest.NewServer(adapthttp.NewRouterFromDeps(ds.httpDeps()))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/runners/builtin")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var rest map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rest))
	isErr, text := mcpToolCallerAs(t, ds, ctx)("runner_get", map[string]any{"id": "builtin"})
	require.False(t, isErr, text)
	var mcp map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &mcp))
	assert.Equal(t, rest, mcp)
	// Check a nonempty source, not just surface parity (two empty maps also match).
	sources, ok := rest["sources"].([]any)
	require.True(t, ok, "%v", rest)
	found := false
	for _, raw := range sources {
		s := raw.(map[string]any)
		if s["id"] == string(src.ID) {
			found = true
			assert.Equal(t, "harness_not_found", s["unavailableReason"])
		}
	}
	require.True(t, found, "missing persisted OpenAI source in API detail")
}

func TestRunnerAdminRoot_PrivateAssignmentRedactedAcrossSurfaces(t *testing.T) {
	ds, fake := rwBoot(t, 8)
	admin := adminCtxForLocalUser(t, ds)
	rwChatProfile(t, admin, ds)
	require.NoError(t, os.Mkdir(filepath.Join(fake, "token"), 0o700))
	id, _, err := ds.eng.Trigger(admin, "rr-block", map[string]any{}, "normal")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ds.eng.Cancel(admin, id) })
	require.Eventually(t, func() bool { return rwCalls(fake) == 1 }, 15*time.Second, 20*time.Millisecond)
	_, err = ds.db.Exec(`UPDATE instances SET visibility = 'private' WHERE id = ?`, id.String())
	require.NoError(t, err)
	viewer := identity.Identity{UserID: uuid.New(), Role: identity.RoleUser}
	ctx := identity.WithIdentity(context.Background(), viewer)
	deps := ds.httpDeps()
	deps.Auth = nil // trusted identity at the boundary; keep root service/visibility/repositories
	router := adapthttp.NewRouterFromDeps(deps)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(identity.WithIdentity(r.Context(), viewer)))
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/runners/builtin")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var detail struct {
		Assignments []struct {
			Hidden bool    `json:"hidden"`
			RunID  *string `json:"runId"`
			NodeID string  `json:"nodeId"`
		} `json:"assignments"`
	}
	require.NoError(t, json.Unmarshal(raw, &detail))
	require.Len(t, detail.Assignments, 1, "real active job must remain counted")
	assert.True(t, detail.Assignments[0].Hidden)
	assert.Nil(t, detail.Assignments[0].RunID)
	assert.Empty(t, detail.Assignments[0].NodeID)
	assert.NotContains(t, string(raw), id.String())
	isErr, text := mcpToolCallerAs(t, ds, ctx)("runner_get", map[string]any{"id": "builtin"})
	require.False(t, isErr, text)
	assert.JSONEq(t, string(raw), text)
	isErr, ownerText := mcpToolCallerAs(t, ds, admin)("runner_get", map[string]any{"id": "builtin"})
	require.False(t, isErr, ownerText)
	assert.Contains(t, ownerText, id.String(), "visibility must not hide the owner's real assignment")

	socketCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(socketCtx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/ws", nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")
	ownerServer := httptest.NewServer(adapthttp.NewRouterFromDeps(ds.httpDeps()))
	defer ownerServer.Close()
	ownerConn, _, err := websocket.Dial(socketCtx, "ws"+strings.TrimPrefix(ownerServer.URL, "http")+"/api/v1/ws", nil)
	require.NoError(t, err)
	defer ownerConn.Close(websocket.StatusNormalClosure, "")
	// Two ordered operator writes bracket actual completion events from the run.
	require.NoError(t, ds.runners.service.Cordon(admin, runner.BuiltInRunnerID))
	_, first, err := conn.Read(socketCtx)
	require.NoError(t, err)
	require.Contains(t, string(first), runner.EventRunnerCordoned)
	rwTouch(t, fake, "release-all")
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
	require.NoError(t, ds.runners.service.Uncordon(admin, runner.BuiltInRunnerID))
	for {
		_, raw, err := ownerConn.Read(socketCtx)
		require.NoError(t, err)
		var frame struct {
			InstanceID uuid.UUID `json:"instanceId"`
		}
		require.NoError(t, json.Unmarshal(raw, &frame))
		if frame.InstanceID == id {
			break
		}
	}
	for {
		_, next, err := conn.Read(socketCtx)
		require.NoError(t, err)
		assert.NotContains(t, string(next), id.String(), "private run events must not precede the public barrier")
		var frame struct {
			InstanceID uuid.UUID `json:"instanceId"`
			Type       string    `json:"type"`
		}
		require.NoError(t, json.Unmarshal(next, &frame))
		require.Equal(t, uuid.Nil, frame.InstanceID, "only system-scoped events may reach this viewer")
		if frame.Type == runner.EventRunnerUncordoned {
			break
		}
	}
}
