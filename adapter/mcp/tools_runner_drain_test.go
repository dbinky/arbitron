// adapter/mcp/tools_runner_drain_test.go
package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerParity_DrainRESTAndMCP(t *testing.T) {
	run := uuid.New()
	stub := sampleRunnerStub()
	stub.drain = runner.DrainProgress{ActiveSteps: 2, HomedWorkspaces: 1, Sessions: 1, Blocked: []runner.DrainBlocker{
		{Kind: runner.DrainKindWorkspace, RunID: &run, Group: "anthropic_subscription:anthropic", Reason: "no eligible runner"},
		{Kind: runner.DrainKindSession, Reason: "interactive session"},
	}}
	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{Config: &config.Config{}, Runners: stub})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/mac-1/drain", nil)
	req = req.WithContext(identity.WithIdentity(context.Background(), runnerAdmin))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var rest any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rest))
	res := callToolWithIdentity(t, runnerToolServer(stub), "runner_drain", map[string]any{"id": "mac-1"}, runnerAdmin)
	require.False(t, res.IsError, extractText(t, res))
	var mcp any
	require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &mcp))
	assert.Equal(t, rest, mcp)
	assert.EqualValues(t, 2, rest.(map[string]any)["activeSteps"])
	assert.Equal(t, run.String(), rest.(map[string]any)["blocked"].([]any)[0].(map[string]any)["runId"])
	stub.views[0].Drain, stub.detail.Drain = &stub.drain, &stub.drain
	assert.Equal(t, restJSON(t, stub, "/api/v1/runners"), mcpJSON(t, "runner_list", map[string]any{}, stub))
	assert.Equal(t, restJSON(t, stub, "/api/v1/runners/builtin"), mcpJSON(t, "runner_get", map[string]any{"id": "builtin"}, stub))
}

func TestRunnerTools_DrainAdminOnly(t *testing.T) {
	stub := sampleRunnerStub()
	stub.drain = runner.DrainProgress{HomedWorkspaces: 1, Blocked: []runner.DrainBlocker{{Kind: runner.DrainKindWorkspace, Group: "anthropic_subscription:anthropic", Reason: "no eligible runner"}}}
	srv := runnerToolServer(stub)

	res := callToolWithIdentity(t, srv, "runner_drain", map[string]any{"id": "mac-1"}, runnerUser)
	assert.True(t, res.IsError, "admin only")

	res = callToolWithIdentity(t, srv, "runner_drain", map[string]any{"id": "mac-1"}, runnerAdmin)
	require.False(t, res.IsError, extractText(t, res))
	assert.Equal(t, runner.RunnerID("mac-1"), stub.gotDrain)
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &out))
	assert.EqualValues(t, 1, out["homedWorkspaces"])
	assert.Equal(t, "anthropic_subscription:anthropic", out["blocked"].([]any)[0].(map[string]any)["group"])
}
