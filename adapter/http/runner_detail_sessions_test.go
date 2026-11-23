package http

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sessionsOf(t *testing.T, body []byte) []any {
	t.Helper()
	var d map[string]any
	require.NoError(t, json.Unmarshal(body, &d))
	sessions, ok := d["sessions"].([]any)
	require.True(t, ok, "sessions is always a JSON array: %s", body)
	return sessions
}

func TestRunnerHandler_GetRendersPinnedSessions(t *testing.T) {
	fake := sampleRunnerFake()
	run := uuid.New()
	fake.detail.Sessions = []runner.SessionPinView{{RunID: &run, NodeID: "talk", Generation: 3}, {Hidden: true, Generation: 1}}
	w := doRunner(t, runnerRouter(fake, identity.RoleUser), http.MethodGet, "/api/v1/runners/builtin", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	sessions := sessionsOf(t, w.Body.Bytes())
	require.Len(t, sessions, 2)
	assert.Equal(t, map[string]any{"runId": run.String(), "nodeId": "talk", "hidden": false, "generation": float64(3)}, sessions[0])
	assert.Equal(t, map[string]any{"runId": nil, "nodeId": "", "hidden": true, "generation": float64(1)}, sessions[1])
}

func TestRunnerHandler_HiddenSessionNeverLeaksRunOrNode(t *testing.T) {
	fake := sampleRunnerFake()
	run := uuid.New()
	fake.detail.Sessions = []runner.SessionPinView{{RunID: &run, NodeID: "secret-node", Hidden: true, Generation: 2}}
	w := doRunner(t, runnerRouter(fake, identity.RoleUser), http.MethodGet, "/api/v1/runners/builtin", "")
	require.Equal(t, http.StatusOK, w.Code)
	sessions := sessionsOf(t, w.Body.Bytes())
	require.Len(t, sessions, 1)
	assert.Equal(t, map[string]any{"runId": nil, "nodeId": "", "hidden": true, "generation": float64(2)}, sessions[0])
	assert.NotContains(t, w.Body.String(), run.String())
	assert.NotContains(t, w.Body.String(), "secret-node")
}

func TestRunnerHandler_GetWithoutSessionsRendersEmptyList(t *testing.T) {
	w := doRunner(t, runnerRouter(sampleRunnerFake(), identity.RoleUser), http.MethodGet, "/api/v1/runners/builtin", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, sessionsOf(t, w.Body.Bytes()))
	assert.Contains(t, w.Body.String(), `"sessions":[]`)
}
