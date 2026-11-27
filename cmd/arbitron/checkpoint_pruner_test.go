package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingPruner struct {
	mu    sync.Mutex
	calls []time.Time
	err   error
	done  chan struct{}
}

func (p *recordingPruner) Prune(_ context.Context, now time.Time) (checkpoint.PruneReport, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, now)
	if len(p.calls) == 3 {
		close(p.done)
	}
	return checkpoint.PruneReport{Archives: 1}, p.err
}

func TestCheckpointPruner_RunsAtBootThenOnEveryTickAndSurvivesErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &recordingPruner{err: errors.New("disk busy"), done: make(chan struct{})}
	ticks := make(chan time.Time)
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	stopped := startCheckpointPruner(ctx, p, ticks, func() time.Time { return now }, nil)
	ticks <- now.Add(time.Hour)
	ticks <- now.Add(2 * time.Hour)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("pruner did not run at boot and on both ticks")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("pruner did not stop on context cancel")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	require.Len(t, p.calls, 3, "boot + two ticks, errors do not stop the loop")
	assert.Equal(t, []time.Time{now, now.Add(time.Hour), now.Add(2 * time.Hour)}, p.calls, "boot uses the clock and subsequent runs consume each real tick")
}

func TestCheckpointPruner_NilPrunerIsAStartupError(t *testing.T) {
	assert.Panics(t, func() { startCheckpointPruner(context.Background(), nil, make(chan time.Time), time.Now, nil) })
}
