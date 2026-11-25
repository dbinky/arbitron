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

func TestInstanceAndTraceDTOs_CarryRecoveryNotice(t *testing.T) {
	n := instance.RecoveryNotice{NodeID: "B", JobID: uuid.New(), RunnerID: "mac-1", Reason: "runner_lost", At: time.Now().UTC(), Text: instance.RecoveryNoticeText}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: uuid.New(), State: instance.StateRunning, Recovery: []instance.RecoveryNotice{n}}
	b, err := json.Marshal(toInstanceResponse(inst))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"recovery":[{"nodeId":"B"`)
	assert.Contains(t, string(b), instance.RecoveryNoticeText)

	ex := &instance.NodeExecution{ID: uuid.New(), InstanceID: inst.ID, NodeID: "B", Status: instance.NodeStatusSucceeded, StartedAt: time.Now(), Recovery: []instance.RecoveryNotice{n}}
	b, err = json.Marshal(toNodeExecutionResponse(ex))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"recovery":[`)

	b, err = json.Marshal(toInstanceResponse(&instance.Instance{ID: uuid.New(), OwnerID: uuid.New()}))
	require.NoError(t, err)
	assert.NotContains(t, string(b), "recovery", "omitted when there is nothing to disclose")
}
