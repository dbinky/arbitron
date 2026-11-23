package runner_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var evNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func TestEventRecorder_PersistsAndPublishesSystemScoped(t *testing.T) {
	log, bus := &memoryEventLog{}, &recordingBus{}
	r := runner.NewEventRecorder(log, bus, 100, func() time.Time { return evNow })
	r.Record(context.Background(), runner.PoolEvent{Kind: runner.EventSourceLoggedOut, RunnerID: runner.BuiltInRunnerID,
		SourceID: "builtin/anthropic_subscription/-", Cause: runner.Cause{Kind: runner.CauseOutcome, Outcome: runner.OutcomeLoggedOut}})

	require.Len(t, log.events, 1)
	stored := log.events[0]
	assert.NotEqual(t, uuid.Nil, stored.ID, "the recorder assigns an id")
	assert.Equal(t, evNow, stored.At, "and a timestamp from its clock")

	pub := bus.ofType("source.logged_out")
	require.Len(t, pub, 1)
	assert.Equal(t, uuid.Nil, pub[0].InstanceID, "system events are uuid.Nil-scoped")
	view, ok := pub[0].Event.Data.(runner.PoolEventView)
	require.True(t, ok)
	assert.Equal(t, stored.ID, view.ID)
	raw, err := json.Marshal(view)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"sourceId":"builtin/anthropic_subscription/-"`)
	assert.Contains(t, string(raw), `"cause":{"kind":"outcome","outcome":"logged_out"}`)
	assert.Equal(t, []int{100}, log.prunes, "retention is enforced on append")
}

func TestEventRecorder_RunScopedEventsUseTheRun(t *testing.T) {
	log, bus := &memoryEventLog{}, &recordingBus{}
	r := runner.NewEventRecorder(log, bus, 100, func() time.Time { return evNow })
	run := uuid.New()
	r.Record(context.Background(), runner.PoolEvent{Kind: runner.EventJobRerouted, RunID: &run, Cause: runner.Cause{Kind: runner.CauseOutcome, Outcome: runner.OutcomeExhausted}})
	pub := bus.ofType("job.rerouted")
	require.Len(t, pub, 1)
	assert.Equal(t, run, pub[0].InstanceID, "run-scoped events are visibility-filtered by the run")
}

func TestEventRecorder_AppendFailureNeverFailsCaller(t *testing.T) {
	log, bus := &memoryEventLog{appendErr: errDisk}, &recordingBus{}
	r := runner.NewEventRecorder(log, bus, 100, nil)
	assert.NotPanics(t, func() {
		r.Record(context.Background(), runner.PoolEvent{Kind: runner.EventRunnerUpdated, RunnerID: runner.BuiltInRunnerID})
	})
	assert.Len(t, bus.ofType("runner.updated"), 1, "the live stream still sees it")
	assert.Same(t, bus, r.Bus())
}

func TestNewEventRecorder_RejectsMissingDeps(t *testing.T) {
	assert.Panics(t, func() { runner.NewEventRecorder(nil, &recordingBus{}, 100, nil) })
	assert.Panics(t, func() { runner.NewEventRecorder(&memoryEventLog{}, nil, 100, nil) })
	assert.Panics(t, func() { runner.NewEventRecorder(&memoryEventLog{}, &recordingBus{}, 0, nil) })
}
