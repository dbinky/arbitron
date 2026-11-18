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

// SetWebhookFields clears webhook_received_at: a receipt belongs to the token
// it was delivered to. The engine relies on this so a consumed delivery, or a
// stale receipt from an earlier pass of a loop, never pre-satisfies a new
// webhook_wait.
func TestSetWebhookFields_ClearsWebhookReceivedAt(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	inst := newProvenancedInstance(instance.StateWaiting, uuid.New(), uuid.New())
	require.NoError(t, repo.Create(ctx, inst))

	tok, hash := "tok-1", "hash-1"
	require.NoError(t, repo.SetWebhookFields(ctx, inst.ID, &tok, &hash))
	require.NoError(t, repo.SetWebhookReceived(ctx, inst.ID, time.Now()))
	got, err := repo.Get(ctx, inst.ID)
	require.NoError(t, err)
	require.NotNil(t, got.WebhookReceivedAt)

	// Clearing the binding clears the receipt.
	require.NoError(t, repo.SetWebhookFields(ctx, inst.ID, nil, nil))
	got, err = repo.Get(ctx, inst.ID)
	require.NoError(t, err)
	assert.Nil(t, got.WebhookToken)
	assert.Nil(t, got.WebhookReceivedAt)

	// Arming a new wait over a stale receipt starts unreceived.
	require.NoError(t, repo.SetWebhookReceived(ctx, inst.ID, time.Now()))
	tok2 := "tok-2"
	require.NoError(t, repo.SetWebhookFields(ctx, inst.ID, &tok2, nil))
	got, err = repo.Get(ctx, inst.ID)
	require.NoError(t, err)
	require.NotNil(t, got.WebhookToken)
	assert.Equal(t, tok2, *got.WebhookToken)
	assert.Nil(t, got.WebhookReceivedAt)
}
