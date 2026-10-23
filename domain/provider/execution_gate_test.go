package provider

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixedRevision(id string, err error) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return id, err }
}

func TestExecutionGateWithCurrentRunsWhenRevisionMatches(t *testing.T) {
	g := NewExecutionGate()
	g.SetCurrentRevision(fixedRevision("rev-1", nil))
	ran := false
	require.NoError(t, g.WithCurrent(context.Background(), "rev-1", func(context.Context) error {
		ran = true
		return nil
	}))
	assert.True(t, ran)
}

func TestExecutionGateWithCurrentRejectsStaleRevisionWithoutRunning(t *testing.T) {
	g := NewExecutionGate()
	g.SetCurrentRevision(fixedRevision("rev-2", nil))
	err := g.WithCurrent(context.Background(), "rev-1", func(context.Context) error {
		t.Fatal("callback must not run against a stale revision")
		return nil
	})
	assert.ErrorIs(t, err, ErrExecutionRevisionStale)
}

func TestExecutionGateWithCurrentFailsClosedWithoutRevisionSource(t *testing.T) {
	g := NewExecutionGate()
	err := g.WithCurrent(context.Background(), "rev-1", func(context.Context) error {
		t.Fatal("callback must not run without a revision source")
		return nil
	})
	assert.ErrorIs(t, err, ErrExecutionRevisionUnavailable)
}

func TestExecutionGateWithCurrentFailsClosedOnEmptyExpectedOrCurrent(t *testing.T) {
	g := NewExecutionGate()
	g.SetCurrentRevision(fixedRevision("", nil))
	for _, expected := range []string{"", "rev-1"} {
		err := g.WithCurrent(context.Background(), expected, func(context.Context) error {
			t.Fatal("empty revisions are never a match")
			return nil
		})
		assert.ErrorIs(t, err, ErrExecutionRevisionUnavailable, "expected=%q", expected)
	}
}

func TestExecutionGateWithCurrentPropagatesRevisionReadFault(t *testing.T) {
	g := NewExecutionGate()
	boom := errors.New("fingerprint read failed")
	g.SetCurrentRevision(fixedRevision("", boom))
	err := g.WithCurrent(context.Background(), "rev-1", func(context.Context) error {
		t.Fatal("callback must not run when the inventory is incomplete")
		return nil
	})
	assert.ErrorIs(t, err, boom)
	assert.ErrorIs(t, err, ErrExecutionRevisionUnavailable)
}

func TestExecutionGateWithCurrentReturnsCallbackError(t *testing.T) {
	g := NewExecutionGate()
	g.SetCurrentRevision(fixedRevision("rev-1", nil))
	boom := errors.New("commit failed")
	assert.ErrorIs(t, g.WithCurrent(context.Background(), "rev-1", func(context.Context) error { return boom }), boom)
}

func TestExecutionGateMutationWaitsForReadLease(t *testing.T) {
	g := NewExecutionGate()
	g.SetCurrentRevision(fixedRevision("rev-1", nil))
	inRead := make(chan struct{})
	releaseRead := make(chan struct{})
	readDone := make(chan error, 1)
	go func() {
		readDone <- g.WithCurrent(context.Background(), "rev-1", func(context.Context) error {
			close(inRead)
			<-releaseRead
			return nil
		})
	}()
	<-inRead

	var mutated atomic.Bool
	mutDone := make(chan error, 1)
	go func() {
		mutDone <- g.WithMutation(context.Background(), func(context.Context) error {
			mutated.Store(true)
			return nil
		})
	}()
	select {
	case <-mutDone:
		t.Fatal("mutation ran while a WithCurrent lease was held")
	case <-time.After(50 * time.Millisecond):
	}
	assert.False(t, mutated.Load())
	close(releaseRead)
	require.NoError(t, <-readDone)
	require.NoError(t, <-mutDone)
	assert.True(t, mutated.Load())
}

func TestExecutionGateReadWaitsForMutation(t *testing.T) {
	g := NewExecutionGate()
	inMutation := make(chan struct{})
	releaseMutation := make(chan struct{})
	mutDone := make(chan error, 1)
	go func() {
		mutDone <- g.WithMutation(context.Background(), func(context.Context) error {
			close(inMutation)
			<-releaseMutation
			return nil
		})
	}()
	<-inMutation

	var read atomic.Bool
	readDone := make(chan error, 1)
	go func() {
		readDone <- g.WithRead(context.Background(), func(context.Context) error {
			read.Store(true)
			return nil
		})
	}()
	select {
	case <-readDone:
		t.Fatal("snapshot read ran during a mutation")
	case <-time.After(50 * time.Millisecond):
	}
	assert.False(t, read.Load())
	close(releaseMutation)
	require.NoError(t, <-mutDone)
	require.NoError(t, <-readDone)
	assert.True(t, read.Load())
}

func TestExecutionGateWithReadNeedsNoRevisionSource(t *testing.T) {
	g := NewExecutionGate()
	require.NoError(t, g.WithRead(context.Background(), func(context.Context) error { return nil }))
}

func TestExecutionGateRefusesCancelledContext(t *testing.T) {
	g := NewExecutionGate()
	g.SetCurrentRevision(fixedRevision("rev-1", nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := func(context.Context) error { t.Fatal("callback must not run"); return nil }
	assert.ErrorIs(t, g.WithRead(ctx, body), context.Canceled)
	assert.ErrorIs(t, g.WithMutation(ctx, body), context.Canceled)
	assert.ErrorIs(t, g.WithCurrent(ctx, "rev-1", body), context.Canceled)
}

// A deadline must release a waiting caller while the conflicting lease is
// still held, without running either its revision source or its callback.
func TestExecutionGateWaitingLeaseHonorsDeadline(t *testing.T) {
	type operation func(context.Context, func(context.Context) error) error
	for _, mode := range []string{"read", "current", "mutation"} {
		t.Run(mode, func(t *testing.T) {
			g := NewExecutionGate()
			revisionReads := 0
			g.SetCurrentRevision(func(context.Context) (string, error) {
				revisionReads++
				return "rev-1", nil
			})
			var wait operation = g.WithRead
			var hold operation = g.WithMutation
			switch mode {
			case "current":
				wait = func(ctx context.Context, fn func(context.Context) error) error {
					return g.WithCurrent(ctx, "rev-1", fn)
				}
			case "mutation":
				wait, hold = g.WithMutation, g.WithRead
			}
			held, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unlock := func() { once.Do(func() { close(release) }) }
			holderDone := make(chan error, 1)
			go func() {
				holderDone <- hold(context.Background(), func(context.Context) error {
					close(held)
					<-release
					return nil
				})
			}()
			<-held
			t.Cleanup(func() { unlock(); require.NoError(t, <-holderDone) })
			// Unblock the old, non-cancellable implementation so a regression
			// fails this assertion instead of hanging the test process.
			fallback := time.AfterFunc(time.Second, unlock)
			defer fallback.Stop()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			calls := 0
			err := wait(ctx, func(context.Context) error { calls++; return nil })
			assert.ErrorIs(t, err, context.DeadlineExceeded)
			assert.Zero(t, calls)
			assert.Zero(t, revisionReads)
			select {
			case <-release:
				t.Fatal("deadline did not return until the conflicting lease was released")
			default:
			}
			unlock()
			// Cancelled acquisition must not retain any part of the lease.
			retryCtx, retryCancel := context.WithTimeout(context.Background(), time.Second)
			defer retryCancel()
			require.NoError(t, wait(retryCtx, func(context.Context) error { calls++; return nil }))
			assert.Equal(t, 1, calls)
		})
	}
}

func TestExecutionGateNilReceiverOrCallbackFailsClosed(t *testing.T) {
	var nilGate *ExecutionGate
	assert.Error(t, nilGate.WithMutation(context.Background(), func(context.Context) error { return nil }))
	g := NewExecutionGate()
	assert.Error(t, g.WithMutation(context.Background(), nil))
	assert.Error(t, g.WithRead(context.Background(), nil))
}

func TestExecutionRevisionCloneIsIndependent(t *testing.T) {
	r := ExecutionRevision{ID: "a", PolicyRevision: 3, Connections: map[string]int64{"c": 1}, WorkflowHashes: map[string]string{"w": "h"}}
	c := r.Clone()
	c.Connections["c"] = 9
	c.WorkflowHashes["w"] = "x"
	assert.Equal(t, int64(1), r.Connections["c"])
	assert.Equal(t, "h", r.WorkflowHashes["w"])
	assert.Equal(t, r.ID, c.ID)
}
