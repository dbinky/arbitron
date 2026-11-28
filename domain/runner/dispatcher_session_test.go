// domain/runner/dispatcher_session_test.go
package runner_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type dsHealth struct {
	mu  sync.Mutex
	got map[runner.SourceID]runner.OutcomeClass
	reg *mvRegistry
	err error
}

func (h *dsHealth) ApplyOutcome(_ context.Context, src runner.SourceID, o runner.SourceOutcome) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.got == nil {
		h.got = map[runner.SourceID]runner.OutcomeClass{}
	}
	h.got[src] = o.Class
	if h.err != nil {
		return h.err
	}
	for i := range h.reg.snap.Sources {
		s := &h.reg.snap.Sources[i]
		if s.ID == src {
			switch o.Class {
			case runner.OutcomeExhausted:
				s.Health.Capacity = runner.CapacityExhausted
			case runner.OutcomeLoggedOut:
				s.Health.Auth = runner.AuthLoggedOut
			}
		}
	}
	return nil
}

type dsTelemetry struct{}

func (dsTelemetry) Latest() map[runner.RunnerID]runner.Telemetry { return nil }

// dsStreamRunner streams scripted chunks and records assignments.
type dsStreamRunner struct {
	id      runner.RunnerID
	mu      sync.Mutex
	assigns []runner.Assignment
	chunks  []engine.ChatChunk
	runErr  error
}

func (s *dsStreamRunner) Run(_ context.Context, a runner.Assignment, _ runner.StepJob) (runner.StepOutput, error) {
	s.mu.Lock()
	s.assigns = append(s.assigns, a)
	s.mu.Unlock()
	if a.Job.RunnerID != s.id {
		return runner.StepOutput{}, errors.New("wrong runner")
	}
	if s.runErr != nil {
		return runner.StepOutput{}, s.runErr
	}
	ch := make(chan engine.ChatChunk, len(s.chunks))
	for _, c := range s.chunks {
		ch <- c
	}
	close(ch)
	return runner.StepOutput{Stream: ch}, nil
}

type dsLedger struct{ *mvLedger }

func (l dsLedger) Requeue(_ context.Context, f runner.Fence, class runner.OutcomeClass) (runner.Job, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, err := l.check(f)
	if err != nil {
		return runner.Job{}, err
	}
	j.State, j.Reason = runner.JobQueued, string(class)
	return *j, nil
}

func dsDispatcher(t *testing.T, f *mvFixture, a, b *dsStreamRunner, health *dsHealth) *runner.StepDispatcher {
	t.Helper()
	health.reg = f.reg
	// mac-a is healthy at placement time here: the failure happens during the run.
	f.reg.snap.Sources[0].Health.Capacity = runner.CapacityState("available")
	d := runner.NewStepDispatcher(runner.DispatcherDeps{
		Registry: f.reg, Ledger: dsLedger{f.ledger}, Runners: mvRunnerSet{"mac-a": a, "mac-b": b},
		Events: runner.NewEventRecorder(f.eventLog, f.bus, 1000, time.Now), Status: runner.NewExecutionStatusProjector(f.ledger),
		Health: health, Telemetry: dsTelemetry{}, Clock: time.Now, TransientMaxAttempts: 1,
		BackoffMin: time.Millisecond, BackoffMax: time.Millisecond, Wake: make(chan struct{}),
	})
	t.Cleanup(d.Close)
	return d
}

func dsCtx(runID uuid.UUID) context.Context {
	return engine.ContextWithStepIdentity(context.Background(), engine.StepIdentity{RunID: runID, NodeID: "talk",
		InvocationID: uuid.New(), OperationID: "turn/2", Kind: engine.OperationTurn, Attempt: 1})
}

func dsReq(runID uuid.UUID, pin runner.RunnerID, aff runner.Affinity) runner.ExecRequirements {
	fingerprint, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli", Backend: "claude-code"})
	if err != nil {
		panic(err)
	}
	return runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: anthropic},
		Model: "claude-opus-4-5", Backend: "claude-code", Provider: "claude-cli", EndpointFingerprint: fingerprint,
		Tools: []string{"Read"}, Op: runner.OpStream, Affinity: aff, RunID: runID, PinnedRunner: pin}
}

func drain(ch <-chan engine.ChatChunk) (tokens []string, terminal error) {
	for c := range ch {
		if c.Token != "" {
			tokens = append(tokens, c.Token)
		}
		if c.Error != nil {
			terminal = c.Error
		}
	}
	return tokens, terminal
}

func TestDispatcher_SessionMidStreamExhaustionSurfacesMoveRequired(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	exhausted := runner.WithOutcome(errors.New("usage limit"), runner.SourceOutcome{Class: runner.OutcomeExhausted})
	a := &dsStreamRunner{id: "mac-a", chunks: []engine.ChatChunk{{Token: "partial"}, {Error: exhausted}}}
	b := &dsStreamRunner{id: "mac-b", chunks: []engine.ChatChunk{{Token: "never"}, {Done: true}}}
	health := &dsHealth{}
	d := dsDispatcher(t, f, a, b, health)

	out, err := d.Dispatch(dsCtx(f.runID), dsReq(f.runID, "mac-a", runner.AffinitySession), runner.StepJob{Op: runner.OpType("stream"), Chat: &engine.ChatRequest{Prompt: "hi"}})
	require.NoError(t, err)
	tokens, terminal := drain(out.Stream)
	assert.Equal(t, []string{"partial"}, tokens)
	require.Error(t, terminal)
	assert.True(t, errors.Is(terminal, runner.ErrSessionMoveRequired))
	var mv *engine.SessionMoveRequiredError
	require.True(t, errors.As(terminal, &mv))
	assert.Equal(t, "exhausted", mv.Reason)
	assert.Equal(t, "mac-a", mv.From)
	assert.Equal(t, runner.OutcomeExhausted, runner.ClassifyError(terminal).Class, "outcome preserved through the wrapper")
	assert.Equal(t, runner.OutcomeExhausted, health.got[runner.MakeSourceID("mac-a", runner.CredAnthropicSubscription, "")])
	assert.Empty(t, b.assigns, "a session is never re-placed transparently")
	require.Len(t, a.assigns, 1)
	assert.ErrorIs(t, f.ledger.FenceCheck(context.Background(), a.assigns[0].Fence), runner.ErrFenced, "the interrupted attempt is fenced")
}

func TestDispatcher_SessionHealthFailureStillFencesInterruptedAttempt(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			f := newMvFixture(t, "claude-code")
			exhausted := runner.WithOutcome(errors.New("usage limit"), runner.SourceOutcome{Class: runner.OutcomeExhausted})
			a := &dsStreamRunner{id: "mac-a", runErr: exhausted}
			if streaming {
				a.runErr = nil
				a.chunks = []engine.ChatChunk{{Token: "partial"}, {Error: exhausted}}
			}
			healthErr := errors.New("health write failed")
			b := &dsStreamRunner{id: "mac-b"}
			d := dsDispatcher(t, f, a, b, &dsHealth{err: healthErr})
			out, err := d.Dispatch(dsCtx(f.runID), dsReq(f.runID, "mac-a", runner.AffinitySession), runner.StepJob{Op: runner.OpStream, Chat: &engine.ChatRequest{}})
			if streaming {
				require.NoError(t, err)
				_, err = drain(out.Stream)
			}
			require.ErrorIs(t, err, healthErr)
			require.Len(t, a.assigns, 1)
			assert.ErrorIs(t, f.ledger.FenceCheck(context.Background(), a.assigns[0].Fence), runner.ErrFenced)
			assert.Empty(t, b.assigns)
		})
	}
}

func TestDispatcher_SessionExhaustionBeforeAnyTokenStillRequiresMove(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	exhausted := runner.WithOutcome(errors.New("usage limit"), runner.SourceOutcome{Class: runner.OutcomeExhausted})
	a := &dsStreamRunner{id: "mac-a", chunks: []engine.ChatChunk{{Error: exhausted}}}
	b := &dsStreamRunner{id: "mac-b", chunks: []engine.ChatChunk{{Token: "never"}, {Done: true}}}
	d := dsDispatcher(t, f, a, b, &dsHealth{})
	out, err := d.Dispatch(dsCtx(f.runID), dsReq(f.runID, "mac-a", runner.AffinitySession), runner.StepJob{Op: runner.OpType("stream"), Chat: &engine.ChatRequest{}})
	require.NoError(t, err)
	tokens, terminal := drain(out.Stream)
	assert.Empty(t, tokens)
	assert.True(t, errors.Is(terminal, runner.ErrSessionMoveRequired), "the session lives on mac-a: only the mover may change the pin")
	assert.Empty(t, b.assigns)
}

func TestDispatcher_SessionRunnerLostBeforeStreamSurfacesMoveRequired(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	a := &dsStreamRunner{id: "mac-a", runErr: runner.ErrRunnerLost}
	b := &dsStreamRunner{id: "mac-b"}
	health := &dsHealth{}
	d := dsDispatcher(t, f, a, b, health)
	_, err := d.Dispatch(dsCtx(f.runID), dsReq(f.runID, "mac-a", runner.AffinitySession), runner.StepJob{Op: runner.OpType("stream"), Chat: &engine.ChatRequest{}})
	require.Error(t, err)
	var mv *engine.SessionMoveRequiredError
	require.True(t, errors.As(err, &mv))
	assert.Equal(t, "runner_lost", mv.Reason)
	assert.Empty(t, health.got, "runner loss is not a source outcome")
	assert.Empty(t, b.assigns)
}

func TestDispatcher_SessionTransientErrorIsNotAMove(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	transient := runner.WithOutcome(errors.New("503"), runner.SourceOutcome{Class: runner.OutcomeTransient})
	a := &dsStreamRunner{id: "mac-a", runErr: transient}
	d := dsDispatcher(t, f, a, &dsStreamRunner{id: "mac-b"}, &dsHealth{})
	_, err := d.Dispatch(dsCtx(f.runID), dsReq(f.runID, "mac-a", runner.AffinitySession), runner.StepJob{Op: runner.OpType("stream"), Chat: &engine.ChatRequest{}})
	require.Error(t, err)
	assert.False(t, errors.Is(err, runner.ErrSessionMoveRequired), "transient errors back off on the pin; they never move a session")
}

func TestDispatcher_NonSessionExhaustionBeforeTokensStillReplaces(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	exhausted := runner.WithOutcome(errors.New("usage limit"), runner.SourceOutcome{Class: runner.OutcomeExhausted})
	a := &dsStreamRunner{id: "mac-a", runErr: exhausted}
	b := &dsStreamRunner{id: "mac-b", chunks: []engine.ChatChunk{{Token: "ok"}, {Done: true}}}
	// Rank mac-a first without workspace affinity (stateless streams do not
	// own a workspace). Exhaustion must then move this same job to mac-b.
	headroom := 1.0
	f.reg.snap.Sources[0].Health.Headroom = &headroom
	d := dsDispatcher(t, f, a, b, &dsHealth{})
	out, err := d.Dispatch(dsCtx(f.runID), dsReq(f.runID, "", runner.AffinityNone), runner.StepJob{Op: runner.OpStream, Chat: &engine.ChatRequest{}})
	require.NoError(t, err)
	tokens, _ := drain(out.Stream)
	assert.Equal(t, []string{"ok"}, tokens, "Phase 1 transparent re-place is unchanged for non-session work")
	require.Len(t, a.assigns, 1)
	require.Len(t, b.assigns, 1)
	assert.Equal(t, a.assigns[0].Fence.JobID, b.assigns[0].Fence.JobID)
	assert.Greater(t, b.assigns[0].Fence.Generation, a.assigns[0].Fence.Generation)
}
