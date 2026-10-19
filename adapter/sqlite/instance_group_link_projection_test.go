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

// Every full-row projection must carry the group-task link, not just Get.
func TestListRunning_PreservesGroupLink(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))
	runID := uuid.New()
	createGroupTask(t, repo, parent.ID, "group-1", runID, "a", instance.StateRunning)
	rows, err := repo.ListRunning(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assertGroupLink(t, &rows[0], parent.ID, "group-1", runID, "a", instance.GroupTaskKindBranch)
	assert.Equal(t, 1, rows[0].SpawnDepth)
}

func TestNextEligible_PreservesGroupLink(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	ctx := context.Background()
	parent := newLinkedInstance(instance.SpawnLink{})
	parent.State = instance.StateWaiting
	require.NoError(t, repo.Create(ctx, parent))
	runID := uuid.New()
	child := createGroupTask(t, repo, parent.ID, "group-1", runID, "a", instance.StateQueued)
	require.NoError(t, queueRepo.SetQueued(ctx, child, time.Now().UTC()))
	got, err := queueRepo.NextEligible(ctx, 0, 0)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, child, got.ID)
	assertGroupLink(t, got, parent.ID, "group-1", runID, "a", instance.GroupTaskKindBranch)
	assert.Equal(t, 1, got.SpawnDepth)
}

func TestNextEligible_HandWrittenKeepsCapsAndEmptySemantics(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	ctx := context.Background()
	got, err := queueRepo.NextEligible(ctx, 0, 0)
	require.NoError(t, err)
	assert.Nil(t, got)
	running := newLinkedInstance(instance.SpawnLink{})
	running.State = instance.StateRunning
	require.NoError(t, repo.Create(ctx, running))
	queued := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, queued))
	require.NoError(t, queueRepo.SetQueued(ctx, queued.ID, time.Now().UTC()))
	got, err = queueRepo.NextEligible(ctx, 1, 0)
	require.NoError(t, err)
	assert.Nil(t, got, "global cap")
	got, err = queueRepo.NextEligible(ctx, 0, 1)
	require.NoError(t, err)
	assert.Nil(t, got, "per-user cap")
	got, err = queueRepo.NextEligible(ctx, 2, 2)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, queued.ID, got.ID)
}

func TestListWaitingProjections_PreserveGroupLink(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	ctx := context.Background()
	parent := newLinkedInstance(instance.SpawnLink{})
	require.NoError(t, repo.Create(ctx, parent))
	runID := uuid.New()
	timer := createGroupTask(t, repo, parent.ID, "group-1", runID, "timer", instance.StateQueued)
	until := time.Now().UTC().Add(time.Hour)
	require.NoError(t, repo.SetWaiting(ctx, timer, &until))
	hook := createGroupTask(t, repo, parent.ID, "group-1", runID, "hook", instance.StateWaiting)
	tok, hash := "tok-group", "hash-group"
	require.NoError(t, repo.SetWebhookFields(ctx, hook, &tok, &hash))
	timers, err := queueRepo.ListWaitingWithTimer(ctx)
	require.NoError(t, err)
	require.Len(t, timers, 1)
	assertGroupLink(t, &timers[0], parent.ID, "group-1", runID, "timer", instance.GroupTaskKindBranch)
	assert.Equal(t, 1, timers[0].SpawnDepth)
	hooks, err := queueRepo.ListWaitingWithWebhook(ctx)
	require.NoError(t, err)
	require.Len(t, hooks, 1)
	assertGroupLink(t, &hooks[0], parent.ID, "group-1", runID, "hook", instance.GroupTaskKindBranch)
	assert.Equal(t, 1, hooks[0].SpawnDepth)
}

func TestGroupLinkProjections_ClosedDBIsAnError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	queueRepo := NewQueueRepo(db.DB)
	require.NoError(t, db.DB.Close())
	ctx := context.Background()
	_, err := repo.ListRunning(ctx)
	assert.Error(t, err)
	_, err = queueRepo.NextEligible(ctx, 0, 0)
	assert.Error(t, err)
	_, err = queueRepo.ListWaitingWithTimer(ctx)
	assert.Error(t, err)
	_, err = queueRepo.ListWaitingWithWebhook(ctx)
	assert.Error(t, err)
	err = repo.Create(ctx, newGroupTaskInstance(uuid.New(), "group-1", uuid.New(), "a", instance.GroupTaskKindBranch))
	assert.Error(t, err)
}
