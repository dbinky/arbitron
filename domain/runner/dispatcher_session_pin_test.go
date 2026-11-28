package runner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pinSnapshotRegistry struct {
	Registry
	snap PoolSnapshot
}

func (r pinSnapshotRegistry) Snapshot(context.Context) (PoolSnapshot, error) { return r.snap, nil }

// pinIntentLedger implements only EnsureIntent: a must-move decision must
// return before any Reserve/SetWait (the embedded nil Ledger panics if called).
type pinIntentLedger struct {
	Ledger
	intents int
}

func (l *pinIntentLedger) EnsureIntent(_ context.Context, key JobKey, nodeID string, req ExecRequirements) (Job, error) {
	l.intents++
	return Job{ID: uuid.New(), Key: key, NodeID: nodeID, Op: req.Op, Group: req.Group, Model: req.Model, State: JobState("queued")}, nil
}

type noPinTelemetry struct{}

func (noPinTelemetry) Latest() map[RunnerID]Telemetry { return map[RunnerID]Telemetry{} }

type unexpectedPinHealth struct{ HealthTransitioner }

type neverRun struct{ calls int }

func (r *neverRun) Run(context.Context, Assignment, StepJob) (StepOutput, error) {
	r.calls++
	return StepOutput{}, errors.New("a must-move session must not run anywhere")
}

func TestDispatch_SessionMustMoveReturnsPinMoveErrorWithoutRunning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mut    func(*PoolSnapshot)
		reason string
	}{
		{"draining", func(s *PoolSnapshot) { sessRunnerAt(s, "mac-a").Draining = true }, "drain"},
		{"lost", func(s *PoolSnapshot) { sessRunnerAt(s, "mac-a").Liveness = Liveness("lost") }, "runner_lost"},
		{"exhausted", func(s *PoolSnapshot) { sessSourceOf(s, "mac-a").Health.Capacity = CapacityState("exhausted") }, "exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := sessPool()
			tc.mut(&snap)
			a, b := &neverRun{}, &neverRun{}
			led := &pinIntentLedger{}
			d := NewStepDispatcher(DispatcherDeps{
				Registry: pinSnapshotRegistry{snap: snap}, Ledger: led,
				Events: &EventRecorder{}, Status: &ExecutionStatusProjector{}, Health: unexpectedPinHealth{},
				Runners: StaticRunnerSet{"mac-a": a, "mac-b": b}, Telemetry: noPinTelemetry{},
				Clock:                func() time.Time { return sessNow },
				TransientMaxAttempts: 1, BackoffMin: time.Millisecond, BackoffMax: time.Second,
			})
			runID := uuid.New()
			ctx := engine.ContextWithStepIdentity(context.Background(), engine.StepIdentity{
				RunID: runID, NodeID: "chat", InvocationID: uuid.New(), OperationID: "turn/3", Kind: engine.OperationTurn, Attempt: 1,
			})
			req := sessReq("mac-a")
			req.RunID = runID
			_, err := d.Dispatch(ctx, req, StepJob{Op: OpType("stream"), Chat: &engine.ChatRequest{Prompt: "next", SessionID: "s1"}})
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrSessionPinUnavailable)
			var interrupted *engine.SessionMoveRequiredError
			require.True(t, errors.As(err, &interrupted), "must-move must reach the engine mover loop")
			var mv *SessionPinMoveError
			require.True(t, errors.As(err, &mv))
			assert.Equal(t, RunnerID("mac-a"), mv.Runner)
			assert.Equal(t, tc.reason, mv.Reason)
			assert.Zero(t, a.calls+b.calls, "never re-placed transparently, never run on the pin")
			assert.Equal(t, 1, led.intents)
		})
	}
}

func TestSessionPinMoveError_Message(t *testing.T) {
	err := &SessionPinMoveError{Runner: "mac-a", Reason: "exhausted", Detail: "must_move: pinned runner=mac-a reason=exhausted"}
	assert.Equal(t, "runner: session pinned to mac-a must move (exhausted)", err.Error())
	assert.True(t, errors.Is(err, ErrSessionPinUnavailable))
}
