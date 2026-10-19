package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The coordinator is what main.go hands the sweep.
var _ groupReconciler = (*engine.GroupCoordinator)(nil)

type countingGroupReconciler struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (c *countingGroupReconciler) Reconcile(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.err
}

func (c *countingGroupReconciler) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSweepGroupReconciler_RunsOnEveryTickAndStopsOnCancel(t *testing.T) {
	rec := &countingGroupReconciler{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sweepGroupReconciler(ctx, rec, 10*time.Millisecond, discardLogger())
		close(done)
	}()

	require.Eventually(t, func() bool { return rec.count() >= 3 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the sweep did not stop when its context was cancelled")
	}
	n := rec.count()
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, n, rec.count(), "no reconciliation may run after cancellation")
}

// A failing pass (a transient DB error) is logged and the loop keeps going.
func TestSweepGroupReconciler_ErrorDoesNotStopTheLoop(t *testing.T) {
	rec := &countingGroupReconciler{err: errors.New("database is locked")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sweepGroupReconciler(ctx, rec, 10*time.Millisecond, discardLogger())

	require.Eventually(t, func() bool { return rec.count() >= 3 }, 2*time.Second, 5*time.Millisecond)
}

// The startup pass is main.go's explicit Reconcile call; the ticker's first run
// is one interval later.
func TestSweepGroupReconciler_FirstRunWaitsOneInterval(t *testing.T) {
	rec := &countingGroupReconciler{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sweepGroupReconciler(ctx, rec, time.Hour, discardLogger())

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, rec.count())
}
