package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodeExecutionRepo_UpdateIfSessionOpen_NeverOverwritesAnEndedSession(t *testing.T) {
	td := setupTestDB(t)
	ctx := context.Background()
	ownerID, err := NewUserRepo(td.DB).GetDefaultUser(ctx)
	require.NoError(t, err)
	inst := newTestInstance(t, ownerID)
	require.NoError(t, NewInstanceRepo(td.DB).Create(ctx, inst))
	repo := NewNodeExecutionRepo(td.DB)

	exec := &instance.NodeExecution{ID: uuid.New(), InstanceID: inst.ID, NodeID: "g1", Attempt: 1, Status: instance.NodeStatusSucceeded,
		StartedAt: time.Now().UTC(), LLMConversation: map[string]any{"kind": "group_resolution"}}
	require.NoError(t, repo.Create(ctx, exec))

	// Open: the guarded write applies.
	exec.LLMConversation = map[string]any{"kind": "group_resolution", "notificationSent": true}
	ok, err := repo.UpdateIfSessionOpen(ctx, exec)
	require.NoError(t, err)
	assert.True(t, ok)

	// A live end lands; a stale copy read before it must not reopen it.
	ended := &instance.NodeExecution{ID: exec.ID, Status: instance.NodeStatusSucceeded,
		LLMConversation: map[string]any{"kind": "group_resolution", "endedAt": "2026-10-17T00:00:00Z", "endReason": "abandoned"}}
	require.NoError(t, repo.Update(ctx, ended))
	stale := &instance.NodeExecution{ID: exec.ID, Status: instance.NodeStatusSucceeded,
		LLMConversation: map[string]any{"kind": "group_resolution", "endedAt": "2026-10-17T00:00:01Z", "endReason": "cancelled"}}
	ok, err = repo.UpdateIfSessionOpen(ctx, stale)
	require.NoError(t, err)
	assert.False(t, ok, "an ended session is never written over")

	got, err := repo.GetByInstanceAndNode(ctx, inst.ID, "g1")
	require.NoError(t, err)
	assert.Equal(t, "abandoned", got.LLMConversation["endReason"])

	// Unknown row: nothing applied, no error.
	ok, err = repo.UpdateIfSessionOpen(ctx, &instance.NodeExecution{ID: uuid.New(), Status: instance.NodeStatusSucceeded})
	require.NoError(t, err)
	assert.False(t, ok)
}

// Two writers on an OPEN session: a turn is saved between another writer's
// read and its write. The field patch must keep the new messages and provider
// session ID, where a whole-record write from the stale read reverted them.
func TestNodeExecutionRepo_PatchSessionFields_KeepsATurnSavedAfterTheRead(t *testing.T) {
	td := setupTestDB(t)
	ctx := context.Background()
	ownerID, err := NewUserRepo(td.DB).GetDefaultUser(ctx)
	require.NoError(t, err)
	inst := newTestInstance(t, ownerID)
	require.NoError(t, NewInstanceRepo(td.DB).Create(ctx, inst))
	repo := NewNodeExecutionRepo(td.DB)

	exec := &instance.NodeExecution{ID: uuid.New(), InstanceID: inst.ID, NodeID: "g1", Attempt: 1, Status: instance.NodeStatusSucceeded,
		StartedAt: time.Now().UTC(), LLMConversation: map[string]any{"kind": "group_resolution", "sessionId": "s1",
			"messages": []any{map[string]any{"role": "assistant", "content": "first"}}}}
	require.NoError(t, repo.Create(ctx, exec))

	for _, tc := range []struct {
		name  string
		patch map[string]any
	}{
		{"notification marker", map[string]any{"notificationSent": true}},
		{"close", map[string]any{"endedAt": "2026-10-17T00:00:00Z", "endReason": "cancelled"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The turn lands after the patching writer's read.
			saved := &instance.NodeExecution{ID: exec.ID, Status: instance.NodeStatusSucceeded,
				LLMConversation: map[string]any{"kind": "group_resolution", "sessionId": "s2-" + tc.name,
					"messages": []any{map[string]any{"role": "assistant", "content": "first"}, map[string]any{"role": "user", "content": "second"}}}}
			ok, err := repo.UpdateIfSessionOpen(ctx, saved)
			require.NoError(t, err)
			require.True(t, ok)

			ok, err = repo.PatchSessionFields(ctx, exec.ID, tc.patch)
			require.NoError(t, err)
			assert.True(t, ok)

			got, err := repo.GetByInstanceAndNode(ctx, inst.ID, "g1")
			require.NoError(t, err)
			assert.Equal(t, "s2-"+tc.name, got.LLMConversation["sessionId"], "provider session ID survives")
			assert.Len(t, got.LLMConversation["messages"], 2, "saved turn survives")
			for k, v := range tc.patch {
				assert.Equal(t, v, got.LLMConversation[k])
			}
		})
	}

	// Ended (by the close subtest): a later patch is refused and changes nothing.
	ok, err := repo.PatchSessionFields(ctx, exec.ID, map[string]any{"endReason": "abandoned"})
	require.NoError(t, err)
	assert.False(t, ok)
	got, err := repo.GetByInstanceAndNode(ctx, inst.ID, "g1")
	require.NoError(t, err)
	assert.Equal(t, "cancelled", got.LLMConversation["endReason"])

	ok, err = repo.PatchSessionFields(ctx, uuid.New(), map[string]any{"notificationSent": true})
	require.NoError(t, err)
	assert.False(t, ok, "unknown row")
}
