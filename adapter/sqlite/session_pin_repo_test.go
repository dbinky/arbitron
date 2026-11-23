package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionPinRepo_PinForReadsNewestRecord(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	pins := NewSessionPinRepo(db.DB)
	execs := NewNodeExecutionRepo(db.DB)

	pin, ok, err := pins.PinFor(ctx, uuid.New(), "chat")
	require.NoError(t, err)
	assert.False(t, ok, "no record: first turn")
	assert.Empty(t, pin)

	inst, first := seedSessionExec(t, db, "chat", 1, time.Now().UTC().Add(-time.Hour))
	_, ok, err = pins.PinFor(ctx, inst.ID, "chat")
	require.NoError(t, err)
	assert.False(t, ok, "a record without a pin (pre-Phase-4 session)")

	require.NoError(t, execs.SetSessionState(ctx, first.ID, instance.SessionState{PinRunner: strp("mac-a"), Generation: 1}))
	pin, ok, err = pins.PinFor(ctx, inst.ID, "chat")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, runner.RunnerID("mac-a"), pin)

	second := &instance.NodeExecution{ID: uuid.New(), InstanceID: inst.ID, NodeID: "chat", Attempt: 2,
		Status: instance.NodeStatusSucceeded, StartedAt: time.Now().UTC(), LLMConversation: map[string]any{"messages": []any{}}}
	require.NoError(t, execs.Create(ctx, second))
	require.NoError(t, execs.SetSessionState(ctx, second.ID, instance.SessionState{PinRunner: strp("mac-b"), Generation: 1}))
	pin, _, err = pins.PinFor(ctx, inst.ID, "chat")
	require.NoError(t, err)
	assert.Equal(t, runner.RunnerID("mac-b"), pin, "the newest visit's pin wins")

	_, ok, err = pins.PinFor(ctx, inst.ID, "other-node")
	require.NoError(t, err)
	assert.False(t, ok)
}
