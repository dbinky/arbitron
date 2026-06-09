package schedule_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingNotifRepo struct {
	mu      sync.Mutex
	created []*notification.Notification
}

func (r *recordingNotifRepo) Create(ctx context.Context, n *notification.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, n)
	return nil
}
func (r *recordingNotifRepo) List(ctx context.Context, f notification.ListFilters) ([]notification.Notification, error) {
	return nil, nil
}
func (r *recordingNotifRepo) CountUnread(ctx context.Context, ownerID *uuid.UUID) (int, error) {
	return 0, nil
}
func (r *recordingNotifRepo) MarkAllRead(ctx context.Context, ownerID *uuid.UUID) error { return nil }
func (r *recordingNotifRepo) Dismiss(ctx context.Context, id uuid.UUID, ownerID *uuid.UUID) error {
	return nil
}

type recordingExternal struct {
	mu     sync.Mutex
	notifs []*notification.Notification
}

func (e *recordingExternal) Notify(ctx context.Context, n *notification.Notification) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notifs = append(e.notifs, n)
	return nil
}

func TestScheduleAlerter_CreatesNotificationAndExternalNotifies(t *testing.T) {
	repo := &recordingNotifRepo{}
	ext := &recordingExternal{}
	al := schedule.NewScheduleAlerter(repo, ext, nil)

	s := schedule.Schedule{ID: uuid.New(), WorkflowName: "report"}
	require.NoError(t, al.Alert(context.Background(), s, notification.TypeScheduleSkipped))

	require.Len(t, repo.created, 1)
	n := repo.created[0]
	assert.Equal(t, notification.TypeScheduleSkipped, n.Type)
	assert.Equal(t, "report", n.WorkflowName)
	assert.Equal(t, uuid.Nil, n.InstanceID, "schedule alerts have no instance")
	assert.NotEqual(t, uuid.Nil, n.ID)
	assert.False(t, n.CreatedAt.IsZero())

	require.Len(t, ext.notifs, 1)
	assert.Equal(t, notification.TypeScheduleSkipped, ext.notifs[0].Type)
}

func TestScheduleAlerter_NilExternalNotifier_OK(t *testing.T) {
	repo := &recordingNotifRepo{}
	al := schedule.NewScheduleAlerter(repo, nil, nil)
	s := schedule.Schedule{ID: uuid.New(), WorkflowName: "wf"}
	require.NoError(t, al.Alert(context.Background(), s, notification.TypeScheduleMissed))
	require.Len(t, repo.created, 1)
}

// A nil notification repo is a no-op (mirrors the engine's nil-notifications
// guard). Alert must return nil without panicking.
func TestScheduleAlerter_NilRepo_NoOp(t *testing.T) {
	al := schedule.NewScheduleAlerter(nil, nil, nil)
	err := al.Alert(context.Background(), schedule.Schedule{ID: uuid.New(), WorkflowName: "wf"}, notification.TypeScheduleSkipped)
	assert.NoError(t, err)
}

// External notifier failures are best-effort: Alert must return nil even when
// the external notifier returns an error.
func TestScheduleAlerter_ExternalNotifierFailure_NotPropagated(t *testing.T) {
	repo := &recordingNotifRepo{}
	ext := &failingExternal{}
	al := schedule.NewScheduleAlerter(repo, ext, nil)
	err := al.Alert(context.Background(), schedule.Schedule{ID: uuid.New(), WorkflowName: "wf"}, notification.TypeScheduleSkipped)
	assert.NoError(t, err, "external notifier failure must not be propagated")
	assert.Len(t, repo.created, 1, "notification must still be persisted")
}

// A notification repo failure is fatal: Alert must propagate the error so the
// evaluator can decide whether to log or retry.
func TestScheduleAlerter_RepoFailure_Propagated(t *testing.T) {
	repo := &failingNotifRepo{}
	al := schedule.NewScheduleAlerter(repo, nil, nil)
	err := al.Alert(context.Background(), schedule.Schedule{ID: uuid.New(), WorkflowName: "wf"}, notification.TypeScheduleSkipped)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo error")
}

// --- helpers for alerter error tests ---

type failingExternal struct{}

func (e *failingExternal) Notify(ctx context.Context, n *notification.Notification) error {
	return errors.New("external error")
}

type failingNotifRepo struct{}

func (r *failingNotifRepo) Create(ctx context.Context, n *notification.Notification) error {
	return errors.New("repo error")
}
func (r *failingNotifRepo) List(ctx context.Context, f notification.ListFilters) ([]notification.Notification, error) {
	return nil, nil
}
func (r *failingNotifRepo) CountUnread(ctx context.Context, ownerID *uuid.UUID) (int, error) {
	return 0, nil
}
func (r *failingNotifRepo) MarkAllRead(ctx context.Context, ownerID *uuid.UUID) error { return nil }
func (r *failingNotifRepo) Dismiss(ctx context.Context, id uuid.UUID, ownerID *uuid.UUID) error {
	return nil
}

var _ schedule.Alerter = (*schedule.ScheduleAlerter)(nil)
