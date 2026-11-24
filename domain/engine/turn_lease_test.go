package engine

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLeaseKeys(t *testing.T) {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	assert.Equal(t, "run:11111111-2222-3333-4444-555555555555", RunLeaseKey(id))
	assert.Equal(t, "resolution:11111111-2222-3333-4444-555555555555/2/3",
		ResolutionLeaseKey(GroupResolutionIdentity{GroupRunID: id, MergeIndex: 2, Attempt: 3}))
}

func TestMemoryTurnLeaseStore_Semantics(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryTurnLeaseStore()
	l, err := s.Acquire(ctx, "run:x", time.Minute)
	require.NoError(t, err)
	assert.EqualValues(t, 1, l.Generation)
	assert.NotEqual(t, uuid.Nil, l.Token)

	_, err = s.Acquire(ctx, "run:x", time.Minute)
	assert.ErrorIs(t, err, ErrInteractiveTurnInProgress)
	assert.ErrorIs(t, err, instance.ErrInteractiveTurnInProgress, "the engine sentinel IS the instance sentinel")

	cur, ok, err := s.Current(ctx, "run:x")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, l.Token, cur.Token)

	bumped, err := s.BumpGeneration(ctx, l)
	require.NoError(t, err)
	assert.EqualValues(t, 2, bumped.Generation)

	stranger := TurnLease{Key: "run:x", Token: uuid.New()}
	_, err = s.Renew(ctx, stranger, time.Minute)
	assert.ErrorIs(t, err, ErrTurnLeaseLost)
	_, err = s.BumpGeneration(ctx, stranger)
	assert.ErrorIs(t, err, ErrTurnLeaseLost)
	require.NoError(t, s.Release(ctx, stranger), "releasing a lease you do not hold is a no-op")

	require.NoError(t, s.Release(ctx, bumped))
	_, ok, err = s.Current(ctx, "run:x")
	require.NoError(t, err)
	assert.False(t, ok)
	again, err := s.Acquire(ctx, "run:x", time.Minute)
	require.NoError(t, err)
	assert.EqualValues(t, 2, again.Generation, "a clean release keeps the session generation")
}

func TestTurnLeaseContextCarrier(t *testing.T) {
	_, ok := TurnLeaseFromContext(context.Background())
	assert.False(t, ok)
	l := TurnLease{Key: "run:y", Token: uuid.New(), Generation: 7}
	got, ok := TurnLeaseFromContext(ContextWithTurnLease(context.Background(), l))
	require.True(t, ok)
	assert.Equal(t, l, got)
}

// renewSpy wraps the memory store, counts renewals and can be told to lose the lease.
type renewSpy struct {
	TurnLeaseStore
	renewals atomic.Int32
	lose     atomic.Bool
	mu       sync.Mutex
	released []TurnLease
}

func (s *renewSpy) Renew(ctx context.Context, l TurnLease, ttl time.Duration) (TurnLease, error) {
	s.renewals.Add(1)
	if s.lose.Load() {
		return TurnLease{}, ErrTurnLeaseLost
	}
	return s.TurnLeaseStore.Renew(ctx, l, ttl)
}

func (s *renewSpy) Release(ctx context.Context, l TurnLease) error {
	s.mu.Lock()
	s.released = append(s.released, l)
	s.mu.Unlock()
	return s.TurnLeaseStore.Release(ctx, l)
}

func TestHoldTurnLease_RenewsEveryThirdOfTTLAndReleases(t *testing.T) {
	spy := &renewSpy{TurnLeaseStore: NewMemoryTurnLeaseStore()}
	turnCtx, hold, err := holdTurnLease(context.Background(), spy, "run:z", 60*time.Millisecond)
	require.NoError(t, err)
	l, ok := TurnLeaseFromContext(turnCtx)
	require.True(t, ok, "the held lease travels on the turn context")
	assert.Equal(t, hold.Lease().Token, l.Token)

	require.Eventually(t, func() bool { return spy.renewals.Load() >= 3 }, 2*time.Second, 5*time.Millisecond)
	assert.NoError(t, turnCtx.Err(), "a renewed lease never cancels the turn")

	hold.Release()
	hold.Release() // idempotent
	spy.mu.Lock()
	assert.Len(t, spy.released, 1)
	spy.mu.Unlock()
	n := spy.renewals.Load()
	time.Sleep(80 * time.Millisecond)
	assert.Equal(t, n, spy.renewals.Load(), "renewal stops at release")
	_, err = spy.Acquire(context.Background(), "run:z", time.Minute)
	assert.NoError(t, err, "released lease is acquirable")
}

func TestHoldTurnLease_LostLeaseCancelsTheTurn(t *testing.T) {
	spy := &renewSpy{TurnLeaseStore: NewMemoryTurnLeaseStore()}
	spy.lose.Store(true)
	turnCtx, hold, err := holdTurnLease(context.Background(), spy, "run:lost", 30*time.Millisecond)
	require.NoError(t, err)
	defer hold.Release()
	select {
	case <-turnCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a lost lease must cancel the running turn")
	}
	assert.True(t, errors.Is(context.Cause(turnCtx), ErrTurnLeaseLost))
	assert.ErrorIs(t, turnLeaseErr(turnCtx, errors.New("stream cancelled")), ErrTurnLeaseLost)
}

func TestTurnLeaseErr_LostLeaseRejectsOtherwiseSuccessfulTurn(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrTurnLeaseLost)
	assert.ErrorIs(t, turnLeaseErr(ctx, nil), ErrTurnLeaseLost)
	assert.NoError(t, turnLeaseErr(context.Background(), nil))
	adapterErr := errors.New("adapter failed")
	assert.Same(t, adapterErr, turnLeaseErr(context.Background(), adapterErr))
}

func TestHoldTurnLease_BusyKeyRejected(t *testing.T) {
	s := NewMemoryTurnLeaseStore()
	_, h1, err := holdTurnLease(context.Background(), s, "run:busy", time.Minute)
	require.NoError(t, err)
	defer h1.Release()
	_, h2, err := holdTurnLease(context.Background(), s, "run:busy", time.Minute)
	assert.Nil(t, h2)
	assert.ErrorIs(t, err, ErrInteractiveTurnInProgress)
}

func TestHoldTurnLease_NilStoreIsLocalNoop(t *testing.T) {
	ctx, hold, err := holdTurnLease(context.Background(), nil, "run:none", time.Minute)
	require.NoError(t, err)
	_, carried := TurnLeaseFromContext(ctx)
	assert.False(t, carried, "no store, no lease in context")
	assert.Equal(t, TurnLease{Key: "run:none"}, hold.Lease(), "the key is still known (intents record it)")
	hold.Release()
	hold.Release() // idempotent

	released := 0
	local := localTurnHold("run:local", func() { released++ })
	local.Release()
	local.Release()
	assert.Equal(t, 1, released)
}

func TestTurnRecoveryProof_Validate(t *testing.T) {
	job := JobFenceRef{JobID: uuid.New(), RunnerID: "mac-a", Generation: 2}
	res := &ResourceFenceRef{Key: "run:x", Owner: "mac-a", Generation: 1, Token: uuid.New()}
	assert.NoError(t, TurnRecoveryProof{IntentID: uuid.New(), Adopted: true, Jobs: []JobFenceRef{job}}.Validate())
	assert.NoError(t, TurnRecoveryProof{IntentID: uuid.New(), Jobs: []JobFenceRef{job}, Resource: res}.Validate())
	for name, p := range map[string]TurnRecoveryProof{
		"no intent":           {Jobs: []JobFenceRef{job}},
		"ttl only":            {IntentID: uuid.New()},
		"adopt two jobs":      {IntentID: uuid.New(), Adopted: true, Jobs: []JobFenceRef{job, job}},
		"adopt resource only": {IntentID: uuid.New(), Adopted: true, Resource: res},
		"incomplete job":      {IntentID: uuid.New(), Jobs: []JobFenceRef{{JobID: uuid.New()}}},
		"incomplete resource": {IntentID: uuid.New(), Jobs: []JobFenceRef{job}, Resource: &ResourceFenceRef{Key: "k"}},
	} {
		assert.ErrorIs(t, p.Validate(), ErrTurnRecoveryProofInvalid, name)
	}
	assert.NoError(t, TurnRecoveryProof{IntentID: uuid.New(), StaleHolder: true}.Validate())
	for name, p := range map[string]TurnRecoveryProof{
		"stale holder with a job":      {IntentID: uuid.New(), StaleHolder: true, Jobs: []JobFenceRef{job}},
		"stale holder with a resource": {IntentID: uuid.New(), StaleHolder: true, Resource: res},
		"stale holder adoption":        {IntentID: uuid.New(), StaleHolder: true, Adopted: true},
		"stale holder without intent":  {StaleHolder: true},
	} {
		assert.ErrorIs(t, p.Validate(), ErrTurnRecoveryProofInvalid, name)
	}
}
