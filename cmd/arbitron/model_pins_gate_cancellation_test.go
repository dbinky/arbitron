package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Observe capture entry while delegating acquisition to the root's real gate.
type observedPinCaptureGate struct {
	gate     *provider.ExecutionGate
	captures atomic.Int64
}

func (g *observedPinCaptureGate) WithRead(ctx context.Context, capture func(context.Context) error) error {
	return g.gate.WithRead(ctx, func(ctx context.Context) error {
		g.captures.Add(1)
		return capture(ctx)
	})
}

func TestModelPinsTriggerGateDeadlineCreatesNothing(t *testing.T) {
	ds := bootSinkStack(t)
	sink := newModelSink(t)
	ctx := installLifecycleProfile(t, ds, sink)
	observer := &observedPinCaptureGate{gate: ds.executionGate}
	ds.eng.SetModelPinGate(observer)

	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unlock := func() { once.Do(func() { close(release) }) }
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- ds.executionGate.WithMutation(ctx, func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	t.Cleanup(func() { unlock(); require.NoError(t, <-mutationDone) })
	fallback := time.AfterFunc(2*time.Second, unlock)
	defer fallback.Stop()
	deadlineCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	id, started, err := ds.eng.Trigger(deadlineCtx, "pin-llm", nil, "normal")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, uuid.Nil, id)
	assert.False(t, started)
	assert.Zero(t, observer.captures.Load(), "an expired waiter must not enter pin capture")
	assert.Zero(t, countInstances(t, ds), "no durable row or enqueue after the deadline")
	ds.eng.Shutdown()
	assert.Empty(t, sink.seen(), "the refused trigger must not invoke a provider")
	select {
	case <-release:
		t.Fatal("trigger returned only after the mutation released, ignoring its deadline")
	default:
	}

	unlock()
	id, started, err = ds.eng.Trigger(ctx, "pin-llm", nil, "normal")
	require.NoError(t, err)
	assert.True(t, started)
	assert.Equal(t, int64(1), observer.captures.Load())
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
	assert.Equal(t, []string{lifecycleModelX, lifecycleModelX}, sink.seen())
	saved, err := ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"pin-alias": lifecycleModelX}, saved.ModelPins)
}
