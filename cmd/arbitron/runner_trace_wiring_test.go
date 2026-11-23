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
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Read both public sinks, not constructor state or manually injected facts.
func rwTraceSurfaces(t *testing.T, ds *domainStack, ctx context.Context, id uuid.UUID) []instance.NodeExecution {
	t.Helper()
	isErr, text := mcpToolCallerAs(t, ds, ctx)("trace_get", map[string]any{"id": id.String()})
	require.False(t, isErr, text)
	var mcp []instance.NodeExecution
	require.NoError(t, json.Unmarshal([]byte(text), &mcp))
	srv := httptest.NewServer(adapthttp.NewRouterFromDeps(ds.httpDeps()))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/instances/" + id.String() + "/trace")
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	var rest []instance.NodeExecution
	require.NoError(t, json.Unmarshal(raw, &rest))
	require.Len(t, rest, len(mcp))
	for i := range mcp {
		assert.Equal(t, mcp[i].ID, rest[i].ID)
		assert.Equal(t, mcp[i].RunnerID, rest[i].RunnerID)
		assert.Equal(t, mcp[i].SourceID, rest[i].SourceID)
		require.Len(t, rest[i].PlacementHistory, len(mcp[i].PlacementHistory))
		for j, p := range mcp[i].PlacementHistory {
			r := rest[i].PlacementHistory[j]
			assert.Equal(t, p.JobID, r.JobID)
			assert.Equal(t, p.RunnerID, r.RunnerID)
			assert.Equal(t, p.SourceID, r.SourceID)
			assert.Equal(t, p.Outcome, r.Outcome)
			assert.Equal(t, p.Reason, r.Reason)
			assert.Equal(t, p.Generation, r.Generation)
			assert.WithinDuration(t, p.At, r.At, time.Second)
		}
	}
	return mcp
}

func rwRequireTraceJobs(t *testing.T, ds *domainStack, id uuid.UUID, trace []instance.NodeExecution, wantFacts int) {
	t.Helper()
	facts := 0
	for _, e := range trace {
		for _, p := range e.PlacementHistory {
			facts++
			assert.Equal(t, "builtin", p.RunnerID)
			assert.Equal(t, string(rwAnthropic), p.SourceID)
			assert.NotEmpty(t, p.Reason)
			assert.Positive(t, p.Generation)
			assert.False(t, p.At.IsZero())
			var runID, nodeID string
			require.NoError(t, ds.db.QueryRow(`SELECT run_id, node_id FROM runner_jobs WHERE id = ?`, p.JobID).Scan(&runID, &nodeID))
			assert.Equal(t, id.String(), runID)
			assert.Equal(t, e.NodeID, nodeID)
		}
	}
	require.Equal(t, wantFacts, facts, "every actual dispatch must survive persistence and both public trace conversions")
}

func TestRunnerTraceWiring_FailedFirstAttemptReachesPublicSinks(t *testing.T) {
	configPath, fake := rwWriteRoot(t, 8)
	ws := filepath.Join(filepath.Dir(configPath), "workspaces")
	writeWorkflow(t, ws, "rr-block", `{"name":"rr-block","inputs":[],"nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"errorBehavior":{"type":"retry","retries":1},"config":{"model_profile":"rr-chat","templateFile":"templates/p.md"}}],"edges":[]}`)
	bin := filepath.Join(fake, "claude")
	script, err := os.ReadFile(bin)
	require.NoError(t, err)
	script = []byte(strings.Replace(string(script), "cat > /dev/null", "cat > /dev/null\nif mkdir '"+fake+"/first-attempt' 2>/dev/null; then echo 'fixture execution failed' >&2; exit 1; fi", 1))
	require.NoError(t, os.WriteFile(bin, script, 0o700))
	rwTouch(t, fake, "release-all")
	ds, err := initDomainWithModelDependencies(context.Background(), configPath, ModelDependencies{
		FetchTransport: catalogHTTPLeaf(func(*http.Request) (*http.Response, error) { return nil, errOfflineCatalog }),
	})
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	ctx := adminCtxForLocalUser(t, ds)
	rwChatProfile(t, ctx, ds)
	id, _, err := ds.eng.Trigger(ctx, "rr-block", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
	trace := rwTraceSurfaces(t, ds, ctx, id)
	rwRequireTraceJobs(t, ds, id, trace, 2)
	require.Len(t, trace, 2)
	require.Len(t, trace[0].PlacementHistory, 1)
	require.Len(t, trace[1].PlacementHistory, 1)
	assert.Equal(t, instance.NodeStatusFailed, trace[0].Status)
	assert.NotEqual(t, "completed", trace[0].PlacementHistory[0].Outcome)
	assert.Equal(t, instance.NodeStatusSucceeded, trace[1].Status)
	assert.Equal(t, "completed", trace[1].PlacementHistory[0].Outcome)
	assert.NotEqual(t, trace[0].PlacementHistory[0].JobID, trace[1].PlacementHistory[0].JobID)
}

func TestRunnerTraceWiring_FollowupSuccessAndFailureReachPublicSinks(t *testing.T) {
	ds, fake := rwBoot(t, 8)
	rwTouch(t, fake, "release-all")
	// Only replace the external executable. The real selectors, dispatcher,
	// engine, repositories and API handlers remain installed by the root.
	bin := filepath.Join(fake, "claude")
	script, err := os.ReadFile(bin)
	require.NoError(t, err)
	script = []byte(strings.Replace(string(script), "cat > /dev/null", "cat > /dev/null\nif [ -f '"+fake+"/fail' ]; then echo 'fixture execution failed' >&2; exit 1; fi", 1))
	require.NoError(t, os.WriteFile(bin, script, 0o700))
	ctx := adminCtxForLocalUser(t, ds)
	_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "rr-agent", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "claude-opus-4-1"})
	require.NoError(t, err)
	id, _, err := ds.eng.Trigger(ctx, "rr-talk", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		i, err := ds.instanceRepo.Get(ctx, id)
		return err == nil && i.State == instance.StateWaiting
	}, 20*time.Second, 50*time.Millisecond)
	first := rwTraceSurfaces(t, ds, ctx, id)
	rwRequireTraceJobs(t, ds, id, first, 1)
	require.NoError(t, ds.conversationHandler.HandleMessage(ctx, id, "second turn"))
	second := rwTraceSurfaces(t, ds, ctx, id)
	rwRequireTraceJobs(t, ds, id, second, 2)
	require.Len(t, first, 1)
	require.Len(t, second, 1)
	assert.Equal(t, first[0].PlacementHistory, second[0].PlacementHistory[:1])
	beforeFailure, err := json.Marshal(second[0].LLMConversation)
	require.NoError(t, err)
	assert.Contains(t, string(beforeFailure), "second turn")
	rwTouch(t, fake, "fail")
	require.Error(t, ds.conversationHandler.HandleMessage(ctx, id, "failed turn must not be committed"))
	third := rwTraceSurfaces(t, ds, ctx, id)
	rwRequireTraceJobs(t, ds, id, third, 3)
	require.Len(t, third, 1)
	require.Len(t, third[0].PlacementHistory, 3)
	assert.Equal(t, second[0].PlacementHistory, third[0].PlacementHistory[:2])
	assert.NotEqual(t, "completed", third[0].PlacementHistory[2].Outcome)
	encoded, err := json.Marshal(third[0].LLMConversation)
	require.NoError(t, err)
	assert.JSONEq(t, string(beforeFailure), string(encoded), "a failed turn appends placement but not conversation text")
	assert.NotContains(t, string(encoded), "failed turn must not be committed")
}
