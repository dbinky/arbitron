package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tickSeedRetry drives the production startScheduled closure over a real
// coordinator. It waits for the loop's immediate first pass, then counts seed
// retries from zero. Ticks are unbuffered and accepted only when the loop is
// back in its select, so each send proves the previous tick's refresh and any
// seedPending retry have returned.
func tickSeedRetry(t *testing.T, coordinator *modelcatalog.RefreshCoordinator) (send func(), retries *atomic.Int32, stop func()) {
	t.Helper()
	ticks := make(chan time.Time)
	retries = new(atomic.Int32)
	m := &modelRefresh{
		coordinator: coordinator,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		newTicker:   func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
		seedPending: func(context.Context) { retries.Add(1) },
	}
	m.startScheduled(context.Background())
	select {
	case <-m.initialPass():
	case <-time.After(2 * time.Second):
		t.Fatal("no immediate first pass")
	}
	retries.Store(0)
	return func() {
		select {
		case ticks <- time.Now():
		case <-time.After(2 * time.Second):
			t.Fatal("scheduled loop did not accept the tick")
		}
	}, retries, m.stop
}

func TestScheduledSeedRetrySkipsBusyCycle(t *testing.T) {
	cycles := new(atomic.Int32)
	coordinator := modelcatalog.NewRefreshCoordinator(func(context.Context) (modelcatalog.RefreshReport, error) {
		cycles.Add(1)
		return modelcatalog.RefreshReport{}, errors.New("catalog fetch failed")
	})
	send, retries, stop := tickSeedRetry(t, coordinator)
	defer stop()
	base := cycles.Load() // the immediate first pass

	held, release := make(chan struct{}), make(chan struct{})
	doErr := make(chan error, 1)
	go func() {
		doErr <- coordinator.Do(context.Background(), func(context.Context) error { close(held); <-release; return nil })
	}()
	<-held
	send()
	send() // the first tick has returned while the slot is still held
	assert.Equal(t, base, cycles.Load(), "a busy admission must not run a cycle")
	assert.Zero(t, retries.Load(), "ErrRefreshBusy must not start a seed retry alongside work the loop did not join")

	close(release)
	require.NoError(t, <-doErr)
	send()
	require.Eventually(t, func() bool { return retries.Load() >= 1 }, 2*time.Second, time.Millisecond,
		"a completed but failed cycle still retries pending seeds")
}

func TestScheduledSeedRetrySkipsClosedCoordinator(t *testing.T) {
	cycles := new(atomic.Int32)
	coordinator := modelcatalog.NewRefreshCoordinator(func(context.Context) (modelcatalog.RefreshReport, error) {
		cycles.Add(1)
		return modelcatalog.RefreshReport{}, nil
	})
	send, retries, stop := tickSeedRetry(t, coordinator)
	defer stop()
	base := cycles.Load() // the immediate first pass

	send()
	require.Eventually(t, func() bool { return retries.Load() == 1 }, 2*time.Second, time.Millisecond,
		"control: a successful cycle retries pending seeds")
	coordinator.Close()
	send()
	send() // the first closed tick has returned
	assert.Equal(t, base+1, cycles.Load())
	assert.Equal(t, int32(1), retries.Load(), "ErrRefreshClosed must not start a seed retry after shutdown began")
}
