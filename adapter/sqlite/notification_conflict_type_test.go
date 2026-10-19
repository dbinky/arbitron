package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/notification"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The conflict-resolution notification (P3-T8) must pass the notifications
// CHECK constraint and round-trip with its instance and workflow name.
func TestNotificationRepo_ConflictResolution_Persists(t *testing.T) {
	repo, db, owner := newNotifRepo(t)
	ctx := context.Background()
	instID := seedInstance(t, db, owner, "waiting")

	n := &notification.Notification{ID: uuid.New(), InstanceID: instID, Type: notification.TypeConflictResolution}
	require.NoError(t, repo.Create(ctx, n))

	got, err := repo.List(ctx, notification.ListFilters{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, notification.TypeConflictResolution, got[0].Type)
	assert.Equal(t, instID, got[0].InstanceID)
	assert.Equal(t, "wf", got[0].WorkflowName)
}

// Every pre-existing type still passes after the rebuild.
func TestNotificationRepo_ConflictResolution_RebuildKeepsOtherTypes(t *testing.T) {
	repo, db, owner := newNotifRepo(t)
	ctx := context.Background()
	instID := seedInstance(t, db, owner, "failed")
	for _, typ := range []notification.Type{
		notification.TypeNeedsInteraction, notification.TypeFailed, notification.TypeCompleted, notification.TypeConflictResolution,
	} {
		require.NoErrorf(t, repo.Create(ctx, &notification.Notification{ID: uuid.New(), InstanceID: instID, Type: typ}), "type %q", typ)
	}
	for _, typ := range []notification.Type{notification.TypeScheduleSkipped, notification.TypeScheduleMissed} {
		require.NoErrorf(t, repo.Create(ctx, &notification.Notification{ID: uuid.New(), WorkflowName: "sched", Type: typ}), "type %q", typ)
	}
	all, err := repo.List(ctx, notification.ListFilters{})
	require.NoError(t, err)
	assert.Len(t, all, 6)
}
