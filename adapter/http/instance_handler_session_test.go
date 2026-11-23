package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstanceHandler_GetTrace_ExposesSessionState(t *testing.T) {
	h, instRepo, nodeExecRepo, _, _ := newInstanceHandler()
	inst := testInstance(instance.StateWaiting)
	instRepo.instances[inst.ID] = inst
	ref, pin := "session:abc", "mac-mini-1"
	invocationID := uuid.New()
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	nodeExecRepo.executions = []*instance.NodeExecution{{
		ID: uuid.New(), InstanceID: inst.ID, NodeID: "chat", Attempt: 1, Status: instance.NodeStatusSucceeded,
		SessionInvocationID: invocationID, StartedAt: at, SessionCheckpointRef: &ref, SessionPinRunner: &pin, SessionGeneration: 4,
		SessionMoves: []instance.SessionMoveRecord{{ID: uuid.New(), RunID: inst.ID, NodeID: "chat", FromRunner: "builtin",
			ToRunner: "mac-mini-1", Reason: "exhausted", Lossy: true, Notice: "tool details and reasoning may be lost", Generation: 4, At: at}},
	}}

	req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String()+"/trace", nil)
	req = withChiURLParam(req, "id", inst.ID.String())
	w := httptest.NewRecorder()
	h.GetTrace(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	assert.Equal(t, invocationID.String(), resp[0]["sessionInvocationId"])
	assert.Equal(t, "session:abc", resp[0]["sessionCheckpointRef"])
	assert.Equal(t, "mac-mini-1", resp[0]["sessionPinRunnerId"])
	assert.EqualValues(t, 4, resp[0]["sessionGeneration"])
	moves, ok := resp[0]["sessionMoves"].([]any)
	require.True(t, ok)
	require.Len(t, moves, 1)
	m := moves[0].(map[string]any)
	assert.Equal(t, "builtin", m["fromRunner"])
	assert.Equal(t, "exhausted", m["reason"])
	assert.Equal(t, true, m["lossy"])
	assert.Equal(t, "tool details and reasoning may be lost", m["notice"])
}

func TestInstanceHandler_GetTrace_OmitsEmptySessionState(t *testing.T) {
	h, instRepo, nodeExecRepo, _, _ := newInstanceHandler()
	inst := testInstance(instance.StateCompleted)
	instRepo.instances[inst.ID] = inst
	nodeExecRepo.executions = []*instance.NodeExecution{{ID: uuid.New(), InstanceID: inst.ID, NodeID: "a", Attempt: 1,
		Status: instance.NodeStatusSucceeded, StartedAt: time.Now()}}
	req := withChiURLParam(httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String()+"/trace", nil), "id", inst.ID.String())
	w := httptest.NewRecorder()
	h.GetTrace(w, req)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	for _, k := range []string{"sessionInvocationId", "sessionCheckpointRef", "sessionPinRunnerId", "sessionGeneration", "sessionMoves"} {
		_, present := resp[0][k]
		assert.False(t, present, k)
	}
}
