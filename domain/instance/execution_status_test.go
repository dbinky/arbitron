package instance_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionStatus_JSONShape(t *testing.T) {
	reset := time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)
	s := instance.ExecutionStatus{RunID: uuid.New(), NodeID: "draft", OperationID: "node", JobID: uuid.New(),
		State: instance.ExecWaitingCapacity, Group: "anthropic_subscription:anthropic", Model: "claude-sonnet-4-5",
		Reason: "waiting · capacity: no eligible source", NextReset: &reset}
	raw, err := json.Marshal(s)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	for _, k := range []string{"runId", "nodeId", "operationId", "jobId", "state", "group", "model", "reason", "nextReset"} {
		assert.Contains(t, m, k)
	}
	for _, k := range []string{"runnerId", "sourceId", "progress", "homeRunnerId", "sessionPinRunnerId"} {
		assert.NotContains(t, m, k, "empty optional fields are omitted")
	}
	assert.Equal(t, "waiting_capacity", m["state"])

	assert.Equal(t, []string{"queued_slot", "waiting_capacity", "running", "moving_workspace", "recovering"},
		[]string{string(instance.ExecQueuedSlot), string(instance.ExecWaitingCapacity), string(instance.ExecRunning), string(instance.ExecMovingWorkspace), string(instance.ExecRecovering)})

	p, err := json.Marshal(instance.MoveProgress{BytesDone: 1, BytesTotal: 2})
	require.NoError(t, err)
	assert.JSONEq(t, `{"bytesDone":1,"bytesTotal":2}`, string(p))
}

func TestEnrichedInstanceSummary_ExecutionTag(t *testing.T) {
	row := instance.EnrichedInstanceSummary{Execution: []instance.ExecutionStatus{{State: instance.ExecRunning}}}
	raw, err := json.Marshal(row)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"execution":[`)
	empty, err := json.Marshal(instance.EnrichedInstanceSummary{})
	require.NoError(t, err)
	assert.NotContains(t, string(empty), `"execution"`)
}
