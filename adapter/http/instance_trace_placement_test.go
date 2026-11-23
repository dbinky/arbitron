package http

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToNodeExecutionResponse_Placement(t *testing.T) {
	runner, src, reason := "builtin", "builtin/lunaroute/lr", "first fit"
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	exec := &instance.NodeExecution{
		ID: uuid.New(), InstanceID: uuid.New(), NodeID: "B", Attempt: 1, Status: instance.NodeStatusSucceeded, StartedAt: at,
		RunnerID: &runner, SourceID: &src, PlacementReason: &reason,
		PlacementHistory: []instance.PlacementRecord{{JobID: "j1", RunnerID: "builtin", SourceID: src, Reason: reason, Outcome: "completed", Generation: 1, At: at}},
	}
	raw, err := json.Marshal(toNodeExecutionResponse(exec))
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Equal(t, "builtin", m["runnerId"])
	assert.Equal(t, src, m["sourceId"])
	assert.Equal(t, reason, m["placementReason"])
	hist, ok := m["placementHistory"].([]any)
	require.True(t, ok)
	require.Len(t, hist, 1)
	h0 := hist[0].(map[string]any)
	assert.Equal(t, "j1", h0["jobId"])
	assert.Equal(t, "completed", h0["outcome"])
	assert.EqualValues(t, 1, h0["generation"])
	assert.Equal(t, "2026-11-20T12:00:00Z", h0["at"])
}

func TestToNodeExecutionResponse_NoPlacementOmitted(t *testing.T) {
	raw, err := json.Marshal(toNodeExecutionResponse(&instance.NodeExecution{ID: uuid.New(), InstanceID: uuid.New(), NodeID: "T", StartedAt: time.Now()}))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "runnerId")
	assert.NotContains(t, string(raw), "placementHistory")
}
