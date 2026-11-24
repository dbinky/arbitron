package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

var _ engine.StepCompletionReleaser = (*StepDispatcher)(nil)

// SetWorkspace installs the workspace protocol before the first dispatch.
func (s *StepDispatcher) SetWorkspace(w StepWorkspace) {
	if w == nil {
		panic("runner: SetWorkspace requires a StepWorkspace")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Workspace = w
	s.d.LegacyBuiltInWorkspace = false
}

func (s *StepDispatcher) workspace() (StepWorkspace, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.Workspace, s.d.LegacyBuiltInWorkspace
}

// normalizeWorkspace fills placement size from the actual workspace producer.
func (s *StepDispatcher) normalizeWorkspace(ctx context.Context, req ExecRequirements, job StepJob) (ExecRequirements, error) {
	if req.Affinity != AffinityWorkspace {
		return req, nil
	}
	if job.Op == OpChat || job.Op == OpStream {
		return req, fmt.Errorf("%w: %s operations never write a workspace in Phase 3", ErrNoWorkspace, job.Op)
	}
	w, legacy := s.workspace()
	if w == nil {
		if legacy {
			req.Affinity = AffinityBuiltIn
			return req, nil
		}
		return req, fmt.Errorf("%w: no workspace service is installed", ErrNoWorkspace)
	}
	if jobWorkDir(&job) == "" {
		return req, fmt.Errorf("%w: the operation has no working directory", ErrNoWorkspace)
	}
	req.WorkspaceBytes = w.WorkspaceBytes(ctx, req.RunID)
	return req, nil
}

func (s *StepDispatcher) beginWorkspace(ctx context.Context, a Assignment, req ExecRequirements, dec Decision, job *StepJob) (*WorkspaceAttempt, error) {
	if req.Affinity != AffinityWorkspace {
		return nil, nil
	}
	w, _ := s.workspace()
	att, err := w.BeginAttempt(ctx, a, req, dec, job)
	if err != nil {
		return nil, fmt.Errorf("runner: prepare workspace for %s: %w", a.Job.ID, err)
	}
	if att.Moved && att.Runner == BuiltInRunnerID {
		s.d.Status.Publish(ctx, a.Job, instance.ExecRunning)
	}
	return &att, nil
}

// failWorkspace rolls back after the runner call has stopped. A failed rollback
// is logged; the next attempt must restore the committed base again.
func (s *StepDispatcher) failWorkspace(ctx context.Context, a Assignment, att *WorkspaceAttempt) {
	if att == nil {
		return
	}
	w, _ := s.workspace()
	if err := w.FinishFailure(ctx, a, *att); err != nil {
		slog.Error("runner: rolling back a workspace attempt", "job", a.Job.ID, "run", att.RunID, "error", err)
	}
}

func (s *StepDispatcher) retryWorkspace(ctx context.Context, a Assignment, req ExecRequirements, dec Decision, job *StepJob, att *WorkspaceAttempt) (*WorkspaceAttempt, error) {
	if att == nil {
		return nil, nil
	}
	s.failWorkspace(context.WithoutCancel(ctx), a, att)
	return s.beginWorkspace(ctx, a, req, dec, job)
}

type pendingStep struct {
	a   Assignment
	att *WorkspaceAttempt
}

// completeStep records the exact receipt once, leaving the job running for the
// validated engine commit. Earlier-phase dispatch and synthetic probes retain
// their separate immediate completion boundary.
func (s *StepDispatcher) completeStep(ctx context.Context, a Assignment, att *WorkspaceAttempt, out *StepOutput, sink *engine.PlacementSink) error {
	bg := context.WithoutCancel(ctx)
	fail := func(err error) error {
		s.fact(sink, a, string(OutcomeOther))
		s.failWorkspace(bg, a, att)
		s.release(bg, a, OutcomeOther)
		return err
	}
	csink, deferred := engine.StepCompletionSinkFromContext(ctx)
	if !deferred {
		id, _ := engine.StepIdentityFromContext(ctx)
		w, _ := s.workspace()
		if att != nil || (w != nil && id.Kind != engine.OperationProbe) {
			return fail(ErrNoCompletionSink)
		}
		return s.complete(ctx, a, sink)
	}
	rec := engine.StepCompletion{JobFence: engine.JobFenceRef{JobID: a.Fence.JobID, RunnerID: string(a.Fence.RunnerID), Generation: a.Fence.Generation}}
	if att != nil {
		w, _ := s.workspace()
		wc, err := w.FinishSuccess(bg, a, *att)
		if err != nil {
			return fail(fmt.Errorf("runner: prepare the workspace result of %s: %w", a.Job.ID, err))
		}
		rec.Workspace = &wc
	}
	if err := csink.Record(rec); err != nil {
		return fail(fmt.Errorf("runner: record the receipt of %s: %w", a.Job.ID, err))
	}
	out.Completion = &rec
	s.retain(a, att)
	s.fact(sink, a, "completed")
	return nil
}

func (s *StepDispatcher) retain(a Assignment, att *WorkspaceAttempt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = map[uuid.UUID]pendingStep{}
	}
	for id, p := range s.pending {
		if p.a.Job.Key.RunID == a.Job.Key.RunID && p.a.Job.Key.InvocationID != a.Job.Key.InvocationID {
			delete(s.pending, id)
		}
	}
	s.pending[a.Fence.JobID] = pendingStep{a: a, att: att}
}

func (s *StepDispatcher) takePending(f engine.JobFenceRef) (pendingStep, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[f.JobID]
	if !ok || string(p.a.Fence.RunnerID) != f.RunnerID || p.a.Fence.Generation != f.Generation {
		return pendingStep{}, false
	}
	delete(s.pending, f.JobID)
	return p, true
}

// ReleaseCompletions rolls back receipts rejected by executor validation or
// durable commit. Exact fences prevent releasing a replacement assignment.
func (s *StepDispatcher) ReleaseCompletions(ctx context.Context, completions []engine.StepCompletion, cause error) error {
	bg := context.WithoutCancel(ctx)
	var errs []error
	for _, c := range completions {
		p, ok := s.takePending(c.JobFence)
		if !ok {
			errs = append(errs, fmt.Errorf("%w: job %s generation %d", ErrUnknownReceipt, c.JobFence.JobID, c.JobFence.Generation))
			continue
		}
		slog.Info("runner: releasing an uncommitted operation", "job", p.a.Job.ID, "node", p.a.Job.NodeID, "cause", cause)
		s.failWorkspace(bg, p.a, p.att)
		if err := s.d.Ledger.Fail(bg, p.a.Fence, OutcomeOther); err != nil && !errors.Is(err, ErrFenced) {
			errs = append(errs, fmt.Errorf("runner: fail uncommitted job %s: %w", p.a.Job.ID, err))
		}
	}
	s.wake()
	return errors.Join(errs...)
}
