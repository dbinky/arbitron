package runner

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rdsSessions struct {
	mu       sync.Mutex
	byRunner map[RunnerID][]RunnerSession
	err      error
	asked    []RunnerID
}

func (x *rdsSessions) ListByRunner(_ context.Context, id RunnerID) ([]RunnerSession, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.asked = append(x.asked, id)
	if x.err != nil {
		return nil, x.err
	}
	return append([]RunnerSession(nil), x.byRunner[id]...), nil
}
func (x *rdsSessions) askedIDs() []RunnerID {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]RunnerID(nil), x.asked...)
}
func rdsFixture(t *testing.T, idx RunnerSessionIndex) *svFixture {
	t.Helper()
	f := rdhFixture(t, nil)
	d := f.svc.d
	d.Sessions = idx
	f.svc = NewService(d)
	return f
}

var rdsMacRun = uuid.New()

func rdsIndex() *rdsSessions {
	return &rdsSessions{byRunner: map[RunnerID][]RunnerSession{
		BuiltInRunnerID: {{RunID: svVisRun, NodeID: "talk", Generation: 3}, {RunID: svHidRun, NodeID: "secret-chat", Generation: 2}},
		"mac-a":         {{RunID: rdsMacRun, NodeID: "chat", Generation: 1}},
	}}
}
func TestServiceGet_SessionsPinnedToTheRunnerWithVisibility(t *testing.T) {
	idx := rdsIndex()
	f := rdsFixture(t, idx)
	d, err := f.svc.Get(svCtx(identity.RoleUser), BuiltInRunnerID)
	require.NoError(t, err)
	require.Len(t, d.Sessions, 2)
	require.NotNil(t, d.Sessions[0].RunID)
	assert.Equal(t, svVisRun, *d.Sessions[0].RunID)
	assert.Equal(t, SessionPinView{RunID: d.Sessions[0].RunID, NodeID: "talk", Generation: 3}, d.Sessions[0])
	assert.Equal(t, SessionPinView{Hidden: true, Generation: 2}, d.Sessions[1])
	assert.Equal(t, []RunnerID{BuiltInRunnerID}, idx.askedIDs())
}
func TestServiceGet_SessionsOfOtherRunnersAreNotListed(t *testing.T) {
	idx := rdsIndex()
	f := rdsFixture(t, idx)
	d, err := f.svc.Get(svCtx(identity.RoleUser), "mac-a")
	require.NoError(t, err)
	require.Len(t, d.Sessions, 1)
	assert.True(t, d.Sessions[0].Hidden)
	assert.Equal(t, int64(1), d.Sessions[0].Generation)
	assert.Equal(t, []RunnerID{"mac-a"}, idx.askedIDs())
}
func TestServiceGet_FailuresNeverListSessions(t *testing.T) {
	idx := rdsIndex()
	f := rdsFixture(t, idx)
	_, err := f.svc.Get(context.Background(), BuiltInRunnerID)
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
	_, err = f.svc.Get(svCtx(identity.RoleAdmin), "laptop")
	assert.ErrorIs(t, err, ErrRunnerNotFound)
	assert.Empty(t, idx.askedIDs())
}

func TestServiceGet_NilSessionIndexLeavesSessionsEmpty(t *testing.T) {
	f := rdsFixture(t, nil)
	d, err := f.svc.Get(svCtx(identity.RoleUser), BuiltInRunnerID)
	require.NoError(t, err)
	require.NotNil(t, d.Sessions)
	assert.Empty(t, d.Sessions)
	assert.Len(t, d.Assignments, 2)
}

func TestServiceGet_SessionIndexErrorFailsTheRead(t *testing.T) {
	boom := errors.New("index unavailable")
	f := rdsFixture(t, &rdsSessions{err: boom})
	_, err := f.svc.Get(svCtx(identity.RoleUser), BuiltInRunnerID)
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "runner: list sessions pinned to builtin")
}
