package engine_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A provider need not close its stream when its context ends. Removing the
// bridge's deadline or cancellation select must fail these sink assertions.
func TestGroupResolutionBridge_StartResolution_BoundsStalledAutonomousTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newBridgeFixture(t)
		stream := make(chan engine.ChatChunk)
		var providerCtx context.Context
		f.agent.streamCallFn = func(ctx context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
			providerCtx = ctx
			return stream, nil
		}
		// Release even a broken implementation; fake time makes this immediate.
		fallback := time.AfterFunc(2*time.Hour, func() { close(stream) })
		defer fallback.Stop()
		ref, err := f.start(1)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Nil(t, ref)
		assert.ErrorIs(t, providerCtx.Err(), context.DeadlineExceeded)
		assert.Empty(t, f.sessionRows(), "an incomplete turn must not become an open session")
		assert.Empty(t, f.notifier.all())
		assert.Empty(t, f.bus.eventsOfType(f.parentID, engine.EventTypeGroupConflict))
	})
}

func TestGroupResolutionBridge_StartResolution_ContextEndsBeforeStreamCompletion(t *testing.T) {
	for _, mode := range []string{"nil", "stalled", "closed", "error", "done"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newBridgeFixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				stream := make(chan engine.ChatChunk, 1)
				f.agent.streamCallFn = func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
					cancel()
					switch mode {
					case "nil":
						return nil, nil
					case "closed":
						close(stream)
					case "error":
						stream <- engine.ChatChunk{Error: context.Canceled}
					case "done":
						stream <- engine.ChatChunk{Done: true}
					}
					return stream, nil
				}
				// Prevent a regression from deadlocking the fake-time bubble.
				// The nil-stream case has no releasable channel; call it in a
				// goroutine so the test can report the failure after one second.
				result := make(chan error, 1)
				go func() {
					_, err := f.bridge.StartResolution(ctx, f.groups.snapshot(f.runID), f.parent(), f.request(1))
					result <- err
				}()
				select {
				case err := <-result:
					require.ErrorIs(t, err, context.Canceled)
				case <-time.After(time.Second):
					t.Fatal("cancelled autonomous turn is still consuming its stream")
				}
				assert.Empty(t, f.sessionRows())
				assert.Empty(t, f.bus.eventsOfType(f.parentID, engine.EventTypeGroupConflict))
			})
		})
	}
}

func TestFileGatherer_CancelledAutonomousTurnRollsBackAsCancelled(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	f.starter.err = context.Canceled
	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, instance.ResolutionOutcomeCancelled, f.report().Attempts[0].Outcome)
	assert.NotNil(t, f.report().Attempts[0].EndedAt)
	assert.Equal(t, "rolled_back", f.report().RollbackStatus)
	assert.Nil(t, f.report().ActiveSession)
	assert.Zero(t, f.area.applyCount())
	assert.Equal(t, map[string]string{"base.txt": "base", "shared.txt": "base"}, f.area.parentFiles(gatherParentDir))
}
