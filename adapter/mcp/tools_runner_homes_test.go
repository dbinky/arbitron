package mcp_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/runner"
)

func TestRunnerTools_GetRendersHomes(t *testing.T) {
	stub := sampleRunnerStub()
	run, leaked := uuid.New(), uuid.New()
	stub.detail.Homes = []runner.HomeView{
		{RunID: &run, Generation: 3, Dirty: true},
		{RunID: &leaked, Hidden: true, Generation: 5},
	}
	res := callToolWithIdentity(t, runnerToolServer(stub), "runner_get", map[string]any{"id": "builtin"}, runnerUser)
	require.False(t, res.IsError, extractText(t, res))
	text := extractText(t, res)
	var d struct {
		Homes []map[string]any `json:"homes"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &d))
	require.Len(t, d.Homes, 2)
	assert.Equal(t, map[string]any{"runId": run.String(), "hidden": false, "generation": float64(3), "dirty": true}, d.Homes[0])
	assert.Equal(t, map[string]any{"runId": nil, "hidden": true, "generation": float64(5), "dirty": false}, d.Homes[1])
	assert.NotContains(t, text, leaked.String(), "a hidden item never renders its run id")
}

// REST and MCP render the same homes identically.
func TestRunnerParity_HomesRESTAndMCP(t *testing.T) {
	stub := sampleRunnerStub()
	run := uuid.New()
	stub.detail.Homes = []runner.HomeView{{RunID: &run, Generation: 3, Dirty: true}, {Hidden: true, Generation: 1}}
	assert.Equal(t, restJSON(t, stub, "/api/v1/runners/builtin"), mcpJSON(t, "runner_get", map[string]any{"id": "builtin"}, stub))
}
