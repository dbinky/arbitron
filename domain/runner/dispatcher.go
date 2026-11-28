package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
)

// StepJob is the operation payload. Binding is server-side only and is NEVER
// serialized to remote runners.
type StepJob struct {
	Op           OpType
	Binding      provider.ResolvedBinding
	Chat         *engine.ChatRequest // chat & stream
	Agent        *engine.AgentMission
	Process      *engine.ProcessSpec
	Confirmation *ConfirmationRequest // OpConfirm
	Session      *SessionOp           // OpSessionImport
}

// Assignment is a reserved, fenced job handed to a StepRunner.
type Assignment struct {
	Job   Job
	Fence Fence
}

// StepOutput is a StepRunner's result; exactly one field is set.
type StepOutput struct {
	Chat         *engine.ChatResponse
	Stream       <-chan engine.ChatChunk
	Agent        *engine.AgentResult
	Process      *engine.ProcessResult
	Confirmation *ConfirmationResult
	// Completion is the exact operation receipt, not an output payload.
	Completion *engine.StepCompletion
	Session    *SessionOpResult // OpSessionImport
}

// StepRunner executes an assignment on one runner. Errors carry a classified
// outcome via WithOutcome when the adapter knows one.
type StepRunner interface {
	Run(ctx context.Context, a Assignment, job StepJob) (StepOutput, error)
}

// RunnerSet resolves a runner ID to its StepRunner.
type RunnerSet interface {
	For(id RunnerID) (StepRunner, bool)
}

// StaticRunnerSet is a fixed RunnerSet (Phase 1: just the built-in runner).
type StaticRunnerSet map[RunnerID]StepRunner

// For returns the runner registered for id.
func (s StaticRunnerSet) For(id RunnerID) (StepRunner, bool) {
	r, ok := s[id]
	return r, ok && r != nil
}

// HealthTransitioner applies a classified outcome to a source (HealthMonitor).
type HealthTransitioner interface {
	ApplyOutcome(ctx context.Context, src SourceID, o SourceOutcome) error
}

// TelemetrySource supplies the latest machine telemetry per runner.
type TelemetrySource interface {
	Latest() map[RunnerID]Telemetry
}

// EmptyTelemetry is the Phase 1 TelemetrySource: nothing is known, and
// placement treats unknown telemetry as neutral.
type EmptyTelemetry struct{}

// Latest returns an empty map.
func (EmptyTelemetry) Latest() map[RunnerID]Telemetry { return map[RunnerID]Telemetry{} }

// DispatcherDeps wires a StepDispatcher.
type DispatcherDeps struct {
	Registry Registry
	Ledger   Ledger
	Runners  RunnerSet
	Events   *EventRecorder
	Status   *ExecutionStatusProjector
	Health   HealthTransitioner
	// ProbeCommits owns synthetic quota confirmations, separately from node commits.
	ProbeCommits ProbeCommitter
	Telemetry    TelemetrySource
	Policy       PlacementPolicy
	Clock        func() time.Time

	TransientMaxAttempts   int
	BackoffMin, BackoffMax time.Duration

	Wake        <-chan struct{} // pool-change notifications (PoolNotifier.Subscribe)
	SessionPins SessionPins
	Workspace   StepWorkspace
	// LegacyBuiltInWorkspace explicitly permits pre-Phase-3 built-in execution.
	LegacyBuiltInWorkspace bool
	// Sleep waits d or until ctx ends. nil => a real timer. Tests inject a fake
	// clock's sleep so backoff is observable and time is actually gated.
	Sleep func(ctx context.Context, d time.Duration) error
}

const (
	// assignmentLease is recorded on every reservation; enforced from Phase 2.
	assignmentLease = 10 * time.Minute
	// waitRecheck bounds a wait even if a wake is missed.
	waitRecheck = 30 * time.Second
)

// StepDispatcher places, records, executes and classifies dispatched operations.
type StepDispatcher struct {
	adopter         *JobAdopter
	agentToolPolicy AgentToolPolicy
	d               DispatcherDeps
	mu              sync.Mutex
	wakeCh          chan struct{} // closed and replaced on every pool change
	stop            chan struct{}
	wakeDone        chan struct{}
	closeOnce       sync.Once
	pending         map[uuid.UUID]pendingStep // guarded by mu
}

// NewStepDispatcher validates deps and starts the wake fan-out.
func NewStepDispatcher(d DispatcherDeps) *StepDispatcher {
	if d.Registry == nil || d.Ledger == nil || d.Runners == nil || d.Events == nil || d.Status == nil || d.Health == nil {
		panic("runner: NewStepDispatcher requires Registry, Ledger, Runners, Events, Status and Health")
	}
	if d.TransientMaxAttempts < 1 {
		panic("runner: NewStepDispatcher requires TransientMaxAttempts >= 1")
	}
	if d.BackoffMin <= 0 || d.BackoffMax < d.BackoffMin {
		panic("runner: NewStepDispatcher requires 0 < BackoffMin <= BackoffMax")
	}
	if d.Telemetry == nil {
		d.Telemetry = EmptyTelemetry{}
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = sleepCtx
	}
	if d.Status.attachedBus() == nil {
		d.Status.AttachBus(d.Events.Bus())
	}
	s := &StepDispatcher{d: d, wakeCh: make(chan struct{}), stop: make(chan struct{}), wakeDone: make(chan struct{})}
	if d.Wake != nil {
		go func() {
			defer close(s.wakeDone)
			for {
				select {
				case _, ok := <-d.Wake:
					if !ok {
						return
					}
					s.wake()
				case <-s.stop:
					return
				}
			}
		}()
	} else {
		close(s.wakeDone)
	}
	return s
}

func (s *StepDispatcher) Close() {
	s.closeOnce.Do(func() { close(s.stop); <-s.wakeDone })
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (s *StepDispatcher) waitChan() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wakeCh
}

// wake releases every current waiter.
func (s *StepDispatcher) wake() {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.wakeCh)
	s.wakeCh = make(chan struct{})
}

// SetAdopter installs the restart adoption branch. Call before use.
func (s *StepDispatcher) SetAdopter(a *JobAdopter) { s.adopter = a }

// Dispatch runs job under the identity on ctx. See the package flow (design P1 §3.5).
func (s *StepDispatcher) Dispatch(ctx context.Context, req ExecRequirements, job StepJob) (StepOutput, error) {
	id, ok := engine.StepIdentityFromContext(ctx)
	if !ok || !id.Valid() {
		return StepOutput{}, engine.ErrMissingStepIdentity
	}
	if req.RunID == uuid.Nil {
		req.RunID = id.RunID
	}
	if req.Op == "" {
		req.Op = job.Op
	}
	if req.RunID != id.RunID || req.Op != job.Op {
		return StepOutput{}, errors.New("runner: request identity/operation mismatch")
	}
	req, err := s.normalizeWorkspace(ctx, req, job)
	if err != nil {
		return StepOutput{}, err
	}
	key := JobKey{RunID: id.RunID, InvocationID: id.InvocationID, OperationID: id.OperationID, Attempt: id.Attempt}
	j, err := s.d.Ledger.EnsureIntent(ctx, key, id.NodeID, req)
	if err != nil {
		return StepOutput{}, fmt.Errorf("runner: record intent for %s/%s: %w", id.NodeID, id.OperationID, err)
	}
	if j.State.HoldsSlot() {
		if s.adopter == nil {
			// Left offered/running by a crashed server or lost runner: close it first.
			orphan := j.ID
			if j, err = s.d.Ledger.Requeue(context.WithoutCancel(ctx), j.Fence(), OutcomeRunnerLost); err != nil {
				return StepOutput{}, fmt.Errorf("runner: recover orphaned job %s: %w", orphan, err)
			}
		} else {
			out, done, rerr := s.adopter.Resolve(ctx, j, job)
			if done {
				return s.finishAdopted(ctx, j, req, job, out, rerr)
			}
			if rerr != nil {
				return StepOutput{}, rerr
			}
			if j, err = s.d.Ledger.Get(ctx, j.ID); err != nil {
				return StepOutput{}, fmt.Errorf("runner: reload recovered job: %w", err)
			}
		}
		s.wake()
	}
	sink, _ := engine.PlacementSinkFromContext(ctx)
	tries := 0
	a, out, att, err := s.runOnce(ctx, &j, req, job, sink, &tries, &exclusions{})
	if err != nil {
		return StepOutput{}, err
	}
	if job.Op == OpStream && out.Stream != nil {
		ch := make(chan engine.ChatChunk)
		go s.pump(ctx, j, a, req, job, out.Stream, sink, ch, tries)
		return StepOutput{Stream: ch}, nil
	}
	if err := s.completeStep(ctx, a, att, &out, sink); err != nil {
		return StepOutput{}, err
	}
	return out, nil
}

// runOnce places and runs until a runner call returns without error (the
// assignment is returned, still running) or the operation fails terminally.
func (s *StepDispatcher) runOnce(ctx context.Context, j *Job, req ExecRequirements, job StepJob, sink *engine.PlacementSink, tries *int, ex *exclusions) (Assignment, StepOutput, *WorkspaceAttempt, error) {
	bg := context.WithoutCancel(ctx)
outer:
	for {
		a, dec, err := s.acquire(ctx, j, req, ex)
		if err != nil {
			return Assignment{}, StepOutput{}, nil, err
		}
		r, ok := s.d.Runners.For(a.Job.RunnerID)
		if !ok {
			s.fact(sink, a, string(OutcomeOther))
			s.release(bg, a, OutcomeOther)
			return Assignment{}, StepOutput{}, nil, fmt.Errorf("runner: no step runner for %q", a.Job.RunnerID)
		}
		if err := s.d.Ledger.MarkRunning(ctx, a.Fence); err != nil {
			s.release(bg, a, OutcomeOther)
			return Assignment{}, StepOutput{}, nil, fmt.Errorf("runner: mark running %s: %w", a.Job.ID, err)
		}
		s.d.Status.Publish(ctx, a.Job, instance.ExecRunning)
		att, err := s.beginWorkspace(ctx, a, req, dec, &job)
		if err != nil {
			s.fact(sink, a, string(OutcomeOther))
			s.release(bg, a, OutcomeOther)
			return Assignment{}, StepOutput{}, nil, err
		}

		for try := 1; ; try++ {
			*tries = try
			out, runErr := r.Run(ctx, a, job)
			if runErr == nil {
				if !validOutput(job.Op, out) {
					s.fact(sink, a, string(OutcomeOther))
					s.failWorkspace(bg, a, att)
					s.release(bg, a, OutcomeOther)
					return Assignment{}, StepOutput{}, nil, errors.New("runner: malformed step output")
				}
				return a, out, att, nil
			}
			o := ClassifyError(runErr)
			o.Model = req.Model
			switch o.Class {
			case OutcomeTransient:
				s.fact(sink, a, string(OutcomeTransient))
				if try >= s.d.TransientMaxAttempts {
					s.failWorkspace(bg, a, att)
					s.release(bg, a, OutcomeTransient)
					return Assignment{}, StepOutput{}, nil, runErr
				}
				if err := s.d.Sleep(ctx, s.backoff(try, o.RetryAfter)); err != nil {
					s.failWorkspace(bg, a, att)
					s.release(bg, a, OutcomeOther)
					return Assignment{}, StepOutput{}, nil, err
				}
				if att, err = s.retryWorkspace(ctx, a, req, dec, &job, att); err != nil {
					s.release(bg, a, OutcomeOther)
					return Assignment{}, StepOutput{}, nil, err
				}
				continue
			case OutcomeExhausted, OutcomeLoggedOut, OutcomeRunnerLost:
				s.fact(sink, a, string(o.Class))
				s.failWorkspace(bg, a, att)
				if err := s.reroute(ctx, j, a, o); err != nil {
					return Assignment{}, StepOutput{}, nil, err
				}
				continue outer
			case OutcomePullFailed, OutcomePreparationFailed:
				s.fact(sink, a, string(o.Class))
				s.failWorkspace(bg, a, att)
				if err := s.excludeAndRequeue(ctx, j, a, o, ex, runErr); err != nil {
					return Assignment{}, StepOutput{}, nil, err
				}
				continue outer
			default:
				s.fact(sink, a, string(OutcomeOther))
				s.failWorkspace(bg, a, att)
				s.release(bg, a, OutcomeOther)
				return Assignment{}, StepOutput{}, nil, runErr
			}
		}
	}
}

// acquire places the job, waiting visibly until it can be reserved.
func (s *StepDispatcher) acquire(ctx context.Context, j *Job, req ExecRequirements, ex *exclusions) (Assignment, Decision, error) {
	var lastState instance.ExecutionState
	var lastDetail string
	for {
		wakeCh := s.waitChan() // taken BEFORE the snapshot: no lost wake-ups
		snap, err := s.d.Registry.Snapshot(ctx)
		if err != nil {
			s.abandon(ctx, *j)
			return Assignment{}, Decision{}, fmt.Errorf("runner: read pool: %w", err)
		}
		snap.Now = s.d.Clock()
		snap.Telemetry = s.d.Telemetry.Latest()
		snap = ex.apply(snap)
		dec := Place(req, snap, s.d.Policy)
		if dec.MoveRequired {
			return Assignment{}, Decision{}, &SessionPinMoveError{Runner: req.PinnedRunner, Reason: dec.MoveReason, Detail: dec.Reason}
		}
		if !dec.Placed {
			if len(ex.runners) > 0 {
				dec.Wait.Detail = strings.TrimPrefix(dec.Wait.Detail+"; "+ex.detail(), "; ")
			}
			if err := s.d.Ledger.SetWait(ctx, j.ID, dec.Wait); err != nil {
				s.abandon(ctx, *j)
				return Assignment{}, Decision{}, fmt.Errorf("runner: record wait: %w", err)
			}
			j.Wait = dec.Wait
			state := instance.ExecQueuedSlot
			if dec.Wait.Kind == WaitCapacity {
				state = instance.ExecWaitingCapacity
			}
			if state != lastState || dec.Wait.Detail != lastDetail {
				s.d.Status.Publish(ctx, *j, state)
				lastState, lastDetail = state, dec.Wait.Detail
			}
			t := time.NewTimer(waitRecheck)
			select {
			case <-wakeCh:
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				s.abandon(ctx, *j)
				return Assignment{}, Decision{}, ctx.Err()
			}
			t.Stop()
			continue
		}
		reserved, err := s.d.Ledger.Reserve(ctx, j.ID, dec, stepLimitOf(snap, dec.Runner), s.d.Clock().Add(assignmentLease))
		if errors.Is(err, ErrSlotTaken) {
			continue
		}
		if err != nil {
			s.abandon(ctx, *j)
			return Assignment{}, Decision{}, fmt.Errorf("runner: reserve slot: %w", err)
		}
		*j = reserved
		return Assignment{Job: reserved, Fence: reserved.Fence()}, dec, nil
	}
}

func stepLimitOf(snap PoolSnapshot, id RunnerID) int {
	for _, r := range snap.Runners {
		if r.ID == id {
			return r.StepLimit
		}
	}
	return 0
}

// reroute transitions the source (not for runner_lost), requeues with a new
// generation and records a run-scoped job.rerouted event.
func (s *StepDispatcher) reroute(ctx context.Context, j *Job, a Assignment, o SourceOutcome) error {
	bg := context.WithoutCancel(ctx)
	if o.Class != OutcomeRunnerLost && a.Job.SourceID != "" {
		if err := s.d.Health.ApplyOutcome(bg, a.Job.SourceID, o); err != nil {
			s.release(bg, a, o.Class)
			return fmt.Errorf("runner: record %s on %s: %w", o.Class, a.Job.SourceID, err)
		}
	}
	nj, err := s.d.Ledger.Requeue(bg, a.Fence, o.Class)
	if err != nil {
		return fmt.Errorf("runner: requeue %s: %w", a.Job.ID, err)
	}
	*j = nj
	run := nj.Key.RunID
	s.d.Events.Record(bg, PoolEvent{Kind: EventJobRerouted, RunnerID: a.Job.RunnerID, SourceID: a.Job.SourceID, RunID: &run,
		Cause: Cause{Kind: CauseOutcome, Outcome: o.Class},
		Detail: map[string]any{"jobId": nj.ID.String(), "nodeId": nj.NodeID, "operationId": nj.Key.OperationID,
			"fromGeneration": a.Fence.Generation, "group": nj.Group.String()}})
	s.wake()
	return nil
}

// complete records success. A completion write failure fails the step: the
// result would otherwise be unrecorded. Success is reported to health by the
// RunnerSet wrapper (P1 T13 NewHealthReportingRunnerSet), not here.
func (s *StepDispatcher) complete(ctx context.Context, a Assignment, sink *engine.PlacementSink) error {
	err := s.d.Ledger.Complete(context.WithoutCancel(ctx), a.Fence, a.Job.ID.String())
	s.wake()
	if err != nil {
		s.fact(sink, a, string(OutcomeOther))
		s.release(context.WithoutCancel(ctx), a, OutcomeOther)
		return fmt.Errorf("runner: complete %s: %w", a.Job.ID, err)
	}
	s.fact(sink, a, "completed")
	return nil
}

func (s *StepDispatcher) release(ctx context.Context, a Assignment, class OutcomeClass) {
	if err := s.d.Ledger.Fail(ctx, a.Fence, class); err != nil {
		slog.Error("runner: releasing failed job", "job", a.Job.ID, "error", err)
	}
	s.wake()
}

func (s *StepDispatcher) abandon(ctx context.Context, j Job) {
	if ab, ok := s.d.Ledger.(IntentAbandoner); ok {
		if err := ab.Abandon(context.WithoutCancel(ctx), j.ID); err != nil {
			slog.Warn("runner: abandoning queued intent", "job", j.ID, "error", err)
		}
	}
}

func (s *StepDispatcher) fact(sink *engine.PlacementSink, a Assignment, outcome string) {
	if sink == nil {
		return
	}
	sink.Record(engine.PlacementFact{JobID: a.Job.ID, RunnerID: string(a.Job.RunnerID), SourceID: string(a.Job.SourceID),
		Reason: a.Job.Reason, Generation: a.Fence.Generation, Outcome: outcome, At: s.d.Clock()})
}

// backoff caps min·2^(n-1) at BackoffMax, then honors RetryAfter as a minimum.
func (s *StepDispatcher) backoff(try int, retryAfter time.Duration) time.Duration {
	d := s.d.BackoffMin
	for i := 1; i < try && d < s.d.BackoffMax; i++ {
		d *= 2
	}
	if d > s.d.BackoffMax {
		d = s.d.BackoffMax
	}
	if retryAfter > d {
		d = retryAfter
	}
	return d
}

func validOutput(op OpType, out StepOutput) bool {
	count := 0
	for _, present := range []bool{out.Chat != nil, out.Stream != nil, out.Agent != nil, out.Process != nil, out.Confirmation != nil, out.Session != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return false
	}
	switch op {
	case OpChat:
		return out.Chat != nil
	case OpStream:
		return out.Stream != nil
	case OpAgent:
		return out.Agent != nil
	case OpProcess:
		return out.Process != nil
	case OpConfirm:
		return out.Confirmation != nil
	case OpSessionImport:
		return out.Session != nil
	}
	return false
}
func drain(in <-chan engine.ChatChunk) {
	for range in {
	}
}

func classOrOther(c OutcomeClass) OutcomeClass {
	if c == OutcomeNone {
		return OutcomeOther
	}
	return c
}

func send(ctx context.Context, out chan<- engine.ChatChunk, c engine.ChatChunk) bool {
	select {
	case out <- c:
		return true
	case <-ctx.Done():
		return false
	}
}

// pump forwards a stream, holding the slot until the terminal chunk. A
// re-placeable error before the first token re-places transparently; after a
// token the outcome is applied to the source and the error is forwarded.
func (s *StepDispatcher) pump(ctx context.Context, j Job, a Assignment, req ExecRequirements, job StepJob, in <-chan engine.ChatChunk, sink *engine.PlacementSink, out chan<- engine.ChatChunk, tries int) {
	defer close(out)
	bg := context.WithoutCancel(ctx)
	forwarded := false
	for {
		var chunk engine.ChatChunk
		var ok bool
		select {
		case chunk, ok = <-in:
		case <-ctx.Done():
			go drain(in)
			s.fact(sink, a, string(OutcomeOther))
			s.release(bg, a, OutcomeOther)
			return
		}
		if !ok {
			// A closed channel without a terminal success is truncated output.
			s.fact(sink, a, string(OutcomeOther))
			s.release(bg, a, OutcomeOther)
			send(ctx, out, engine.ChatChunk{Error: errors.New("runner: stream closed before terminal result"), Done: true})
			return
		}
		if chunk.Error != nil {
			o := ClassifyError(chunk.Error)
			o.Model = req.Model
			if o.Class == OutcomeTransient && !forwarded {
				go drain(in)
				s.fact(sink, a, string(OutcomeTransient))
				r, _ := s.d.Runners.For(a.Job.RunnerID)
				for {
					if tries >= s.d.TransientMaxAttempts {
						s.release(bg, a, OutcomeTransient)
						send(ctx, out, chunk)
						return
					}
					if err := s.d.Sleep(ctx, s.backoff(tries, o.RetryAfter)); err != nil {
						s.release(bg, a, OutcomeOther)
						send(ctx, out, engine.ChatChunk{Error: err, Done: true})
						return
					}
					tries++
					nout, err := r.Run(ctx, a, job)
					if err == nil {
						if !validOutput(job.Op, nout) {
							s.fact(sink, a, string(OutcomeOther))
							s.release(bg, a, OutcomeOther)
							send(ctx, out, engine.ChatChunk{Error: errors.New("runner: missing or malformed step output"), Done: true})
							return
						}
						in = nout.Stream
						break
					}
					o = ClassifyError(err)
					o.Model = req.Model
					chunk = engine.ChatChunk{Error: err, Done: true}
					if o.Class != OutcomeTransient {
						in = terminalStream(chunk)
						break
					}
					s.fact(sink, a, string(OutcomeTransient))
				}
				continue
			}
			replaceable := o.Class == OutcomeExhausted || o.Class == OutcomeLoggedOut || o.Class == OutcomeRunnerLost
			if replaceable && !forwarded {
				go drain(in)
				s.fact(sink, a, string(o.Class))
				if err := s.reroute(ctx, &j, a, o); err != nil {
					send(ctx, out, engine.ChatChunk{Error: err, Done: true})
					return
				}
				na, nout, _, err := s.runOnce(ctx, &j, req, job, sink, &tries, &exclusions{})
				if err != nil {
					send(ctx, out, engine.ChatChunk{Error: err, Done: true})
					return
				}
				a, in = na, nout.Stream
				continue
			}
			go drain(in)
			s.fact(sink, a, string(classOrOther(o.Class)))
			if o.Class == OutcomeExhausted || o.Class == OutcomeLoggedOut {
				if err := s.d.Health.ApplyOutcome(bg, a.Job.SourceID, o); err != nil {
					slog.Error("runner: applying stream outcome", "source", a.Job.SourceID, "error", err)
				}
			}
			s.release(bg, a, classOrOther(o.Class))
			send(ctx, out, chunk)
			return
		}
		if chunk.Done {
			// Facts and completion land BEFORE the terminal chunk is visible.
			if err := s.complete(ctx, a, sink); err != nil {
				send(ctx, out, engine.ChatChunk{Error: err, Done: true})
				go drain(in)
				return
			}
			send(ctx, out, chunk)
			go drain(in)
			return
		}
		if !send(ctx, out, chunk) {
			go drain(in)
			s.fact(sink, a, string(OutcomeOther))
			s.release(bg, a, OutcomeOther)
			return
		}
		if chunk.Token != "" {
			forwarded = true
		}
	}
}

func terminalStream(c engine.ChatChunk) <-chan engine.ChatChunk {
	ch := make(chan engine.ChatChunk, 1)
	ch <- c
	close(ch)
	return ch
}
