package sqlite

import (
	"context"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSessionPins_CurrentInvocationWinsAfterLoop(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	execs := NewNodeExecutionRepo(db.DB)
	inst, old := seedSessionExec(t, db, "chat", 8, time.Now().UTC().Add(-time.Hour))
	oldInv, currentInv := uuid.New(), uuid.New()
	require.NoError(t, execs.SetSessionState(ctx, old.ID, instance.SessionState{InvocationID: oldInv, PinRunner: strp("old"), Generation: 1}))
	current := &instance.NodeExecution{ID: uuid.New(), InstanceID: inst.ID, NodeID: "chat", Attempt: 1, Status: instance.NodeStatusSucceeded, StartedAt: time.Now().UTC(), LLMConversation: map[string]any{"messages": []any{}}}
	require.NoError(t, execs.Create(ctx, current))
	require.NoError(t, execs.SetSessionState(ctx, current.ID, instance.SessionState{InvocationID: currentInv, PinRunner: strp("current"), Generation: 1}))
	pin, ok, err := NewSessionPinRepo(db.DB).PinFor(ctx, inst.ID, "chat")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, runner.RunnerID("current"), pin)
}

func TestSessionPinRepo_ReadError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewSessionPinRepo(db.DB)
	require.NoError(t, db.DB.Close())
	pin, ok, err := repo.PinFor(context.Background(), uuid.New(), "chat")
	require.Error(t, err)
	assert.Empty(t, pin)
	assert.False(t, ok)
}
