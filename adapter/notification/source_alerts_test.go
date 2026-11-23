package notification

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type saRepo struct {
	mu      sync.Mutex
	created []*notification.Notification
	err     error
}

func (r *saRepo) Create(_ context.Context, n *notification.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.created = append(r.created, n)
	return nil
}
func (r *saRepo) List(context.Context, notification.ListFilters) ([]notification.Notification, error) {
	return nil, nil
}
func (r *saRepo) CountUnread(context.Context, *uuid.UUID) (int, error) { return 0, nil }
func (r *saRepo) MarkAllRead(context.Context, *uuid.UUID) error        { return nil }
func (r *saRepo) Dismiss(context.Context, uuid.UUID, *uuid.UUID) error { return nil }

type saExternal struct {
	mu  sync.Mutex
	got []*notification.Notification
}

func (e *saExternal) Notify(_ context.Context, n *notification.Notification) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got = append(e.got, n)
	return nil
}

func saSource() runner.Source {
	return runner.Source{ID: runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredAnthropicSubscription, ""), RunnerID: runner.BuiltInRunnerID, Kind: runner.CredAnthropicSubscription}
}

func TestSourceAlerts_LoggedOutCreatesNotificationAndTeams(t *testing.T) {
	repo, ext := &saRepo{}, &saExternal{}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	a := NewSourceAlerts(repo, ext, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	require.NoError(t, a.SourceLoggedOut(context.Background(), saSource()))
	a.Wait()
	require.Len(t, repo.created, 1)
	n := repo.created[0]
	assert.Equal(t, notification.TypeSourceLoggedOut, n.Type)
	assert.Equal(t, "runner builtin source anthropic_subscription", n.WorkflowName)
	assert.Equal(t, uuid.Nil, n.InstanceID, "instance-less")
	assert.Equal(t, now, n.CreatedAt)
	require.Len(t, ext.got, 1)
	assert.Same(t, n, ext.got[0])
}

func TestSourceAlerts_RepoFailureReturnedAndNoTeams(t *testing.T) {
	repo, ext := &saRepo{err: errors.New("db down")}, &saExternal{}
	a := NewSourceAlerts(repo, ext, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	assert.Error(t, a.SourceLoggedOut(context.Background(), saSource()))
	a.Wait()
	assert.Empty(t, ext.got)
}

func TestSourceAlerts_RecoveredCreatesNothing(t *testing.T) {
	repo, ext := &saRepo{}, &saExternal{}
	a := NewSourceAlerts(repo, ext, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	require.NoError(t, a.SourceRecovered(context.Background(), saSource()))
	a.Wait()
	assert.Empty(t, repo.created)
	assert.Empty(t, ext.got)
}

func TestSourceAlerts_NilExternalAllowedNilRepoPanics(t *testing.T) {
	a := NewSourceAlerts(&saRepo{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	require.NoError(t, a.SourceLoggedOut(context.Background(), saSource()))
	assert.Panics(t, func() { NewSourceAlerts(nil, nil, nil, time.Now) })
}

var _ runner.SourceAlertNotifier = (*SourceAlerts)(nil)
