package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/notification"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newNotifRepo builds a NotificationRepo backed by a fresh in-memory DB.
func newNotifRepo(t *testing.T) (*NotificationRepo, testDB, uuid.UUID) {
	t.Helper()
	db := setupTestDB(t)
	repo := NewNotificationRepo(db.DB)
	owner := seedUser(t, db, "owner-"+uuid.NewString()[:8])
	return repo, db, owner
}

func instNotif(instanceID uuid.UUID) *notification.Notification {
	return &notification.Notification{
		ID:         uuid.New(),
		InstanceID: instanceID,
		Type:       notification.TypeCompleted,
		CreatedAt:  time.Now().UTC().Truncate(time.Second),
	}
}

func schedNotif(workflowName string) *notification.Notification {
	return &notification.Notification{
		ID:           uuid.New(),
		InstanceID:   uuid.Nil, // schedule alerts have no instance
		WorkflowName: workflowName,
		Type:         notification.TypeScheduleSkipped,
		CreatedAt:    time.Now().UTC().Truncate(time.Second),
	}
}

// --- Happy path ---

// Instance-based notification survives a Create + List round-trip. This
// verifies that migration 013 (which rebuilt the notifications table) did not
// break the pre-existing needs_interaction/failed/completed flow.
func TestNotificationRepo_InstanceNotification_RoundTrip(t *testing.T) {
	repo, db, owner := newNotifRepo(t)
	ctx := context.Background()

	instID := seedInstance(t, db, owner, "completed")
	n := instNotif(instID)
	require.NoError(t, repo.Create(ctx, n))

	notifs, err := repo.List(ctx, notification.ListFilters{})
	require.NoError(t, err)
	require.Len(t, notifs, 1)
	got := notifs[0]
	assert.Equal(t, n.ID, got.ID)
	assert.Equal(t, instID, got.InstanceID)
	assert.Equal(t, "wf", got.WorkflowName, "workflow_name resolved from instances JOIN (seedInstance uses 'wf')")
	assert.Equal(t, notification.TypeCompleted, got.Type)
	assert.False(t, got.Read)
	assert.False(t, got.Dismissed)
}

// Schedule alert is persisted with a NULL instance_id and the workflow name
// carried in the denormalized column. Retrievable via the admin List.
func TestNotificationRepo_ScheduleAlert_NullInstance(t *testing.T) {
	repo, _, _ := newNotifRepo(t)
	ctx := context.Background()

	n := schedNotif("daily-report")
	require.NoError(t, repo.Create(ctx, n))

	notifs, err := repo.List(ctx, notification.ListFilters{})
	require.NoError(t, err)
	require.Len(t, notifs, 1)
	got := notifs[0]
	assert.Equal(t, n.ID, got.ID)
	assert.Equal(t, uuid.Nil, got.InstanceID, "schedule alert instance_id must be nil (uuid.Nil)")
	assert.Equal(t, "daily-report", got.WorkflowName)
	assert.Equal(t, notification.TypeScheduleSkipped, got.Type)
}

// schedule_missed is also stored correctly.
func TestNotificationRepo_ScheduleMissed_Type(t *testing.T) {
	repo, _, _ := newNotifRepo(t)
	ctx := context.Background()
	n := &notification.Notification{
		ID:           uuid.New(),
		InstanceID:   uuid.Nil,
		WorkflowName: "daily-report",
		Type:         notification.TypeScheduleMissed,
	}
	require.NoError(t, repo.Create(ctx, n))
	notifs, err := repo.List(ctx, notification.ListFilters{})
	require.NoError(t, err)
	require.Len(t, notifs, 1)
	assert.Equal(t, notification.TypeScheduleMissed, notifs[0].Type)
}

// --- Input-validation (repo) ---

// All five post-migration notification types pass the DB CHECK constraint.
func TestNotificationRepo_Create_AllTypesPersist(t *testing.T) {
	repo, db, owner := newNotifRepo(t)
	ctx := context.Background()
	instID := seedInstance(t, db, owner, "failed")

	types := []notification.Type{
		notification.TypeNeedsInteraction,
		notification.TypeFailed,
		notification.TypeCompleted,
		notification.TypeScheduleSkipped,
		notification.TypeScheduleMissed,
	}
	for _, typ := range types {
		var instanceID uuid.UUID
		if typ != notification.TypeScheduleSkipped && typ != notification.TypeScheduleMissed {
			instanceID = instID
		}
		n := &notification.Notification{
			ID:         uuid.New(),
			InstanceID: instanceID,
			Type:       typ,
		}
		require.NoErrorf(t, repo.Create(ctx, n), "Create must not fail for type %q", typ)
	}
	all, err := repo.List(ctx, notification.ListFilters{})
	require.NoError(t, err)
	assert.Len(t, all, len(types))
}

// --- Error-handling ---

// A nil OwnerID (admin view) returns all notifications including schedule
// alerts. A non-nil OwnerID (user view) returns only instance-based
// notifications for that user; schedule alerts are excluded. This is the
// intentional design: schedule alerts are surfaced to admins/operators only,
// since the notifications table has no creator_id column for schedule alerts.
func TestNotificationRepo_List_AdminSeesScheduleAlerts_UserDoesNot(t *testing.T) {
	repo, db, owner := newNotifRepo(t)
	ctx := context.Background()

	instID := seedInstance(t, db, owner, "completed")
	require.NoError(t, repo.Create(ctx, instNotif(instID)))
	require.NoError(t, repo.Create(ctx, schedNotif("cron-report")))

	// Admin (nil ownerID) sees both.
	all, err := repo.List(ctx, notification.ListFilters{})
	require.NoError(t, err)
	assert.Len(t, all, 2)

	// User (non-nil ownerID) sees only the instance-backed notification.
	own, err := repo.List(ctx, notification.ListFilters{OwnerID: &owner})
	require.NoError(t, err)
	assert.Len(t, own, 1)
	assert.Equal(t, instID, own[0].InstanceID)
}

// CountUnread with a nil ownerID (admin) counts schedule alerts; with a
// non-nil ownerID it counts only instance-based notifications.
func TestNotificationRepo_CountUnread_AdminVsUser(t *testing.T) {
	repo, db, owner := newNotifRepo(t)
	ctx := context.Background()

	instID := seedInstance(t, db, owner, "completed")
	require.NoError(t, repo.Create(ctx, instNotif(instID)))    // unread by default
	require.NoError(t, repo.Create(ctx, schedNotif("report"))) // unread, no instance

	adminCount, err := repo.CountUnread(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, adminCount, "admin count must include schedule alerts")

	userCount, err := repo.CountUnread(ctx, &owner)
	require.NoError(t, err)
	assert.Equal(t, 1, userCount, "user count must exclude schedule alerts (no creator_id column)")
}

// --- Edge-cases / boundary ---

// MarkAllRead for nil ownerID (admin) marks schedule alerts as read.
// For a non-nil ownerID it cannot reach schedule alerts.
func TestNotificationRepo_MarkAllRead_AdminMarksScheduleAlerts(t *testing.T) {
	repo, _, _ := newNotifRepo(t)
	ctx := context.Background()

	n := schedNotif("cron-job")
	require.NoError(t, repo.Create(ctx, n))

	require.NoError(t, repo.MarkAllRead(ctx, nil))

	count, err := repo.CountUnread(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "MarkAllRead(nil) must mark schedule alerts as read")
}

// Dismiss with ownerID cannot dismiss a schedule alert (no instance_id to
// match). Returns ErrNotFound, not a server error.
func TestNotificationRepo_Dismiss_ByUser_ScheduleAlert_IsNotFound(t *testing.T) {
	repo, _, _ := newNotifRepo(t)
	ctx := context.Background()

	n := schedNotif("cron-job")
	require.NoError(t, repo.Create(ctx, n))

	someUser := uuid.New()
	err := repo.Dismiss(ctx, n.ID, &someUser)
	assert.ErrorIs(t, err, notification.ErrNotFound,
		"dismissing a schedule alert by user must return ErrNotFound")
}

// The WorkflowName for instance-based notifications is resolved via the
// instances JOIN, not the denormalized column. A schedule alert uses the
// denormalized column instead.
func TestNotificationRepo_List_WorkflowNameSourced(t *testing.T) {
	repo, db, owner := newNotifRepo(t)
	ctx := context.Background()

	instID := seedInstance(t, db, owner, "completed")
	in := &notification.Notification{
		ID:         uuid.New(),
		InstanceID: instID,
		Type:       notification.TypeCompleted,
	}
	require.NoError(t, repo.Create(ctx, in))

	notifs, err := repo.List(ctx, notification.ListFilters{})
	require.NoError(t, err)
	require.Len(t, notifs, 1)
	// workflow_name is seeded as "wf" by the seedInstance helper.
	assert.Equal(t, "wf", notifs[0].WorkflowName,
		"workflow name for instance notifications must come from the instances JOIN")
}
