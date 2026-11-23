package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
)

func restJSON(t *testing.T, svc adapthttp.RunnerService, target string) any {
	t.Helper()
	r := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{Config: &config.Config{}, Runners: svc})
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req = req.WithContext(identity.WithIdentity(context.Background(), runnerUser))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var v any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &v))
	return v
}

func mcpJSON(t *testing.T, srvTool string, args map[string]any, stub *stubRunnerService) any {
	t.Helper()
	res := callToolWithIdentity(t, runnerToolServer(stub), srvTool, args, runnerUser)
	require.False(t, res.IsError, extractText(t, res))
	var v any
	require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &v))
	return v
}

// REST and MCP render the same service result to the same JSON (field names,
// nulls, hidden assignments), so the two surfaces cannot drift.
func TestRunnerParity_RESTAndMCP(t *testing.T) {
	stub := sampleRunnerStub()
	stub.views[0].Sources[0].UnavailableReason = "codex login required"
	list := mcpJSON(t, "runner_list", map[string]any{}, stub).(map[string]any)
	source := list["runners"].([]any)[0].(map[string]any)["sources"].([]any)[0].(map[string]any)
	assert.Equal(t, "codex login required", source["unavailableReason"])
	assert.Equal(t, restJSON(t, stub, "/api/v1/runners"), mcpJSON(t, "runner_list", map[string]any{}, stub))
	assert.Equal(t, restJSON(t, stub, "/api/v1/runners/builtin"), mcpJSON(t, "runner_get", map[string]any{"id": "builtin"}, stub))
	assert.Equal(t, restJSON(t, stub, "/api/v1/runners/events"), mcpJSON(t, "runner_events", map[string]any{}, stub))
}
