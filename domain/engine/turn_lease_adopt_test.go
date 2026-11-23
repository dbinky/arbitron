package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTurnLeaseHold_AdoptBumpedGenerationKeepsOwnership(t *testing.T) {
	store := NewMemoryTurnLeaseStore()
	_, hold, err := holdTurnLease(context.Background(), store, RunLeaseKey(uuid.New()), time.Minute)
	require.NoError(t, err)
	defer hold.Release()
	start := hold.Lease()
	bumped, err := store.BumpGeneration(context.Background(), start)
	require.NoError(t, err)
	hold.Adopt(bumped)
	assert.Equal(t, start.Generation+1, hold.Lease().Generation)
	assert.Equal(t, start.Token, hold.Lease().Token)
	_, err = store.BumpGeneration(context.Background(), hold.Lease())
	require.NoError(t, err)
	hold.Release()
	reacquired, err := store.Acquire(context.Background(), start.Key, time.Minute)
	require.NoError(t, err)
	assert.NotEqual(t, start.Token, reacquired.Token)
}

func TestTurnLeaseHold_AdoptIgnoresForeignTokenAndOlderGeneration(t *testing.T) {
	store := NewMemoryTurnLeaseStore()
	_, hold, err := holdTurnLease(context.Background(), store, "run:adopt", time.Minute)
	require.NoError(t, err)
	defer hold.Release()
	cur := hold.Lease()
	for _, change := range []func(*TurnLease){
		func(l *TurnLease) { l.Token = uuid.New(); l.Generation += 5 },
		func(l *TurnLease) { l.Key = "run:other"; l.Generation += 5 },
		func(l *TurnLease) { l.Generation-- },
	} {
		invalid := cur
		change(&invalid)
		hold.Adopt(invalid)
		assert.Equal(t, cur, hold.Lease())
	}
}

func TestTurnRecovery_AdoptRefreshesLeaseInAttemptContext(t *testing.T) {
	store := NewMemoryTurnLeaseStore()
	ctx, hold, err := holdTurnLease(context.Background(), store, "run:context", time.Minute)
	require.NoError(t, err)
	defer hold.Release()
	attemptCtx := context.WithValue(ctx, struct{}{}, "attempt")
	bumped, err := store.BumpGeneration(ctx, hold.Lease())
	require.NoError(t, err)
	hold.Adopt(bumped)
	got, ok := TurnLeaseFromContext(attemptCtx)
	require.True(t, ok)
	assert.Equal(t, bumped, got, "exporters and commits must see the reserved generation")
}

type adoptionRenewStore struct {
	TurnLeaseStore
	entered  chan TurnLease
	resume   chan struct{}
	finished chan struct{}
	started  bool
}

func (s *adoptionRenewStore) Renew(ctx context.Context, l TurnLease, ttl time.Duration) (TurnLease, error) {
	if !s.started {
		s.started = true
		s.entered <- l
		<-s.resume
		close(s.finished)
		return l, nil
	}
	return s.TurnLeaseStore.Renew(ctx, l, ttl)
}

func TestTurnLeaseHold_AdoptSurvivesInflightRenewal(t *testing.T) {
	store := &adoptionRenewStore{TurnLeaseStore: NewMemoryTurnLeaseStore(), entered: make(chan TurnLease, 1), resume: make(chan struct{}), finished: make(chan struct{})}
	ctx, hold, err := holdTurnLease(context.Background(), store, "run:renew", 30*time.Millisecond)
	require.NoError(t, err)
	defer hold.Release()
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(store.resume) }) }
	defer resume()
	var old TurnLease
	select {
	case old = <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("renewal did not start")
	}
	bumped, err := store.BumpGeneration(ctx, old)
	require.NoError(t, err)
	hold.Adopt(bumped)
	resume()
	<-store.finished
	// Release waits for the renewal goroutine, so no timing-based assertion is needed.
	hold.Release()
	assert.Equal(t, bumped.Generation, hold.Lease().Generation)
}

func TestSessionMoveRequiredError_IsAndUnwrap(t *testing.T) {
	cause := errors.New("usage limit")
	err := fmt.Errorf("conversation: stream error: %w", &SessionMoveRequiredError{Reason: "exhausted", From: "mac-a", Source: "source-a", Err: cause})
	assert.ErrorIs(t, err, ErrSessionMoveRequired)
	assert.ErrorIs(t, err, cause)
	var mv *SessionMoveRequiredError
	require.ErrorAs(t, err, &mv)
	assert.Equal(t, "mac-a", mv.From)
	assert.Equal(t, "source-a", mv.Source)
	assert.Contains(t, err.Error(), "exhausted")
}
