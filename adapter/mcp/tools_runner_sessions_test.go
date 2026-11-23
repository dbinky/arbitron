package mcp_test

import (
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerTools_GetRendersPinnedSessions(t *testing.T) {
	stub := sampleRunnerStub()
	run, leaked := uuid.New(), uuid.New()
	stub.detail.Sessions = []runner.SessionPinView{
		{RunID: &run, NodeID: "talk", Generation: 3},
		{RunID: &leaked, NodeID: "secret-node", Hidden: true, Generation: 1},
	}
	res := callToolWithIdentity(t, runnerToolServer(stub), "runner_get", map[string]any{"id": "builtin"}, runnerUser)
	require.False(t, res.IsError, extractText(t, res))
	text := extractText(t, res)
	var d struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &d))
	require.Len(t, d.Sessions, 2)
	assert.Equal(t, map[string]any{"runId": run.String(), "nodeId": "talk", "hidden": false, "generation": float64(3)}, d.Sessions[0])
	assert.Equal(t, map[string]any{"runId": nil, "nodeId": "", "hidden": true, "generation": float64(1)}, d.Sessions[1])
	assert.NotContains(t, text, leaked.String())
	assert.NotContains(t, text, "secret-node")
}

func TestRunnerParity_SessionsRESTAndMCP(t *testing.T) {
	stub := sampleRunnerStub()
	run := uuid.New()
	stub.detail.Sessions = []runner.SessionPinView{{RunID: &run, NodeID: "talk", Generation: 3}, {Hidden: true, Generation: 1}}
	stub.detail.Homes = []runner.HomeView{{RunID: &run, Generation: 2}}
	assert.Equal(t, restJSON(t, stub, "/api/v1/runners/builtin"), mcpJSON(t, "runner_get", map[string]any{"id": "builtin"}, stub))
}
