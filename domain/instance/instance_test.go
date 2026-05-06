package instance

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestStateConstants(t *testing.T) {
	states := []State{StateQueued, StateRunning, StateWaiting, StateCompleted, StateFailed, StateCancelled, StatePaused}
	expected := []string{"queued", "running", "waiting", "completed", "failed", "cancelled", "paused"}

	for i, s := range states {
		assert.Equal(t, expected[i], string(s))
	}
}

func TestStateWaitingConstant(t *testing.T) {
	assert.Equal(t, "waiting", string(StateWaiting))
}

func TestSentinelErrors(t *testing.T) {
	tests := []struct {
		err  error
		msg  string
	}{
		{ErrNotFound, "not found"},
		{ErrNotRunning, "instance is not running"},
		{ErrNotPaused, "instance is not paused"},
		{ErrNotFailed, "instance is not failed"},
		{ErrNotQueued, "instance is not queued"},
		{ErrNotWaiting, "instance is not waiting"},
		{ErrCannotCancel, "instance cannot be cancelled"},
		{ErrCannotRerun, "instance cannot be rerun"},
	}
	for _, tc := range tests {
		t.Run(tc.msg, func(t *testing.T) {
			assert.NotNil(t, tc.err)
			assert.Equal(t, tc.msg, tc.err.Error())
		})
	}
}

func TestPriorityConstants(t *testing.T) {
	priorities := []Priority{PriorityHigh, PriorityNormal, PriorityLow}
	expected := []string{"high", "normal", "low"}

	for i, p := range priorities {
		assert.Equal(t, expected[i], string(p))
	}
}

func TestNodeExecutionStatusConstants(t *testing.T) {
	statuses := []NodeExecutionStatus{NodeStatusSucceeded, NodeStatusFailed, NodeStatusSkipped}
	expected := []string{"succeeded", "failed", "skipped"}

	for i, s := range statuses {
		assert.Equal(t, expected[i], string(s))
	}
}

func TestInstanceConstruction(t *testing.T) {
	id := uuid.New()
	ownerID := uuid.New()
	now := time.Now()
	nodeID := "node-1"

	inst := Instance{
		ID:                id,
		OwnerID:           ownerID,
		WorkflowName:      "test-workflow",
		WorkflowSnapshot:  map[string]any{"name": "test-workflow"},
		State:             StateQueued,
		Priority:          PriorityNormal,
		Variables:         map[string]any{"key": "value"},
		OriginalVariables: map[string]any{"key": "value"},
		CurrentNodeID:     &nodeID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	assert.Equal(t, id, inst.ID)
	assert.Equal(t, ownerID, inst.OwnerID)
	assert.Equal(t, "test-workflow", inst.WorkflowName)
	assert.Equal(t, StateQueued, inst.State)
	assert.Equal(t, PriorityNormal, inst.Priority)
	assert.Equal(t, &nodeID, inst.CurrentNodeID)
	assert.Nil(t, inst.FailedNodeID)
	assert.Nil(t, inst.CompletedAt)
}

func TestInstancePhase2Fields(t *testing.T) {
	now := time.Now()
	token := "tok-abc"
	hash := "sha256-hash"
	waitUntil := now.Add(30 * time.Second)

	inst := Instance{
		ID:                uuid.New(),
		OwnerID:           uuid.New(),
		WorkflowName:      "webhook-workflow",
		WorkflowSnapshot:  map[string]any{},
		State:             StateWaiting,
		Priority:          PriorityNormal,
		Variables:         map[string]any{},
		OriginalVariables: map[string]any{},
		CreatedAt:         now,
		UpdatedAt:         now,
		QueuedAt:          &now,
		WaitingUntil:      &waitUntil,
		WebhookToken:      &token,
		WebhookSecretHash: &hash,
		WebhookReceivedAt: &now,
	}

	assert.Equal(t, StateWaiting, inst.State)
	assert.Equal(t, &now, inst.QueuedAt)
	assert.Equal(t, &waitUntil, inst.WaitingUntil)
	assert.Equal(t, &token, inst.WebhookToken)
	assert.Equal(t, &hash, inst.WebhookSecretHash)
	assert.Equal(t, &now, inst.WebhookReceivedAt)
}

func TestInstanceSummaryConstruction(t *testing.T) {
	id := uuid.New()
	now := time.Now()

	summary := InstanceSummary{
		ID:           id,
		WorkflowName: "test-workflow",
		State:        StateRunning,
		Priority:     PriorityHigh,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	assert.Equal(t, id, summary.ID)
	assert.Equal(t, StateRunning, summary.State)
	assert.Equal(t, PriorityHigh, summary.Priority)
	assert.Nil(t, summary.CurrentNodeID)
	assert.Nil(t, summary.CompletedAt)
}

func TestNodeExecutionConstruction(t *testing.T) {
	id := uuid.New()
	instanceID := uuid.New()
	model := "claude-3-sonnet"
	tokensIn := 100
	tokensOut := 50

	exec := NodeExecution{
		ID:              id,
		InstanceID:      instanceID,
		NodeID:          "llm-node",
		Attempt:         1,
		Status:          NodeStatusSucceeded,
		ResolvedInputs:  map[string]any{"prompt": "hello"},
		Output:          map[string]any{"result": "world"},
		LLMModel:        &model,
		LLMTokensIn:     &tokensIn,
		LLMTokensOut:    &tokensOut,
		StartedAt:       time.Now(),
	}

	assert.Equal(t, id, exec.ID)
	assert.Equal(t, instanceID, exec.InstanceID)
	assert.Equal(t, 1, exec.Attempt)
	assert.Equal(t, NodeStatusSucceeded, exec.Status)
	assert.Equal(t, &model, exec.LLMModel)
	assert.Nil(t, exec.Error)
	assert.Nil(t, exec.ScriptStdout)
	assert.Nil(t, exec.CompletedAt)
}

func TestVariableSnapshotConstruction(t *testing.T) {
	id := uuid.New()
	instanceID := uuid.New()
	now := time.Now()

	snap := VariableSnapshot{
		ID:         id,
		InstanceID: instanceID,
		NodeID:     "transform-1",
		Variables:  map[string]any{"result": "processed"},
		CapturedAt: now,
	}

	assert.Equal(t, id, snap.ID)
	assert.Equal(t, instanceID, snap.InstanceID)
	assert.Equal(t, "transform-1", snap.NodeID)
	assert.Equal(t, now, snap.CapturedAt)
}

func TestListFiltersDefaults(t *testing.T) {
	filters := ListFilters{
		Limit:  10,
		Offset: 0,
	}

	assert.Nil(t, filters.State)
	assert.Nil(t, filters.WorkflowName)
	assert.Equal(t, 10, filters.Limit)
	assert.Equal(t, 0, filters.Offset)
}

func TestListFiltersWithState(t *testing.T) {
	state := StateRunning
	filters := ListFilters{
		State:  &state,
		Limit:  20,
		Offset: 5,
	}

	assert.NotNil(t, filters.State)
	assert.Equal(t, StateRunning, *filters.State)
}

func TestListFiltersWithWorkflowName(t *testing.T) {
	name := "my-workflow"
	filters := ListFilters{
		WorkflowName: &name,
		Limit:        10,
	}

	assert.NotNil(t, filters.WorkflowName)
	assert.Equal(t, "my-workflow", *filters.WorkflowName)
	assert.Nil(t, filters.State)
}
