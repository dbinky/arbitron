package runner

import (
	"context"
	"errors"
	"fmt"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"log/slog"
)

// StepWorkspace is the dispatcher's port onto workspace sync.
type StepWorkspace interface {
	WorkspaceBytes(ctx context.Context, runID uuid.UUID) int64
	BeginAttempt(ctx context.Context, a Assignment, req ExecRequirements, dec Decision, job *StepJob) (WorkspaceAttempt, error)
	FinishSuccess(ctx context.Context, a Assignment, att WorkspaceAttempt) (engine.WorkspaceCompletion, error)
	FinishFailure(ctx context.Context, a Assignment, att WorkspaceAttempt) error
}

var _ StepWorkspace = (*WorkspaceSync)(nil)

func (s *WorkspaceSync) storePrepared(p preparedPush) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepared[p.fence.JobID] = p
}

func (s *WorkspaceSync) takePrepared(f Fence) (preparedPush, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.prepared[f.JobID]
	if !ok || p.fence != f {
		return preparedPush{}, false
	}
	delete(s.prepared, f.JobID)
	return p, true
}

// prepareBuiltIn stages the built-in working copy as a PREPARED candidate
// under the exact job and resource fences. On a promisor miss it marks the
// home non-portable and returns the ErrPromisorObjectsMissing error.
func (s *WorkspaceSync) prepareBuiltIn(ctx context.Context, f Fence, runID uuid.UUID, dir string, resource engine.ResourceFenceRef) (CheckpointRef, error) {
	if f.RunnerID != BuiltInRunnerID {
		return CheckpointRef{}, fmt.Errorf("%w: built-in preparation for runner %q", ErrHomeOwned, f.RunnerID)
	}
	if err := resource.Validate(); err != nil {
		return CheckpointRef{}, fmt.Errorf("%w: %v", ErrResourceFenced, err)
	}
	if err := s.d.Ledger.FenceCheck(ctx, f); err != nil {
		return CheckpointRef{}, err
	}
	if err := s.d.Leases.Check(ctx, resource); err != nil {
		return CheckpointRef{}, err
	}
	home, err := s.d.Homes.GetHome(ctx, runID)
	if err != nil {
		return CheckpointRef{}, fmt.Errorf("runner: read workspace home of run %s: %w", runID, err)
	}
	if !isServerLocal(home.RunnerID) {
		return CheckpointRef{}, fmt.Errorf("%w: run %s is homed on %q", ErrHomeOwned, runID, home.RunnerID)
	}
	ref, _, err := s.d.Checkpoints.StageBuiltIn(ctx, runID, dir, f, resource)
	if errors.Is(err, ErrPromisorObjectsMissing) {
		if serr := s.d.Homes.SetNonPortable(ctx, runID, NonPortablePromisorReason); serr != nil {
			return CheckpointRef{}, fmt.Errorf("runner: mark run %s non-portable: %w", runID, serr)
		}
		return CheckpointRef{}, err
	}
	if err != nil {
		return CheckpointRef{}, fmt.Errorf("runner: prepare built-in checkpoint of run %s: %w", runID, err)
	}
	return ref, nil
}

// CommitBuiltIn keeps its contract name but PREPARES only: it looks up this
// operation's exact active claim and stages a candidate under it.
func (s *WorkspaceSync) CommitBuiltIn(ctx context.Context, f Fence, runID uuid.UUID, dir string) (CheckpointRef, error) {
	rec, ok, err := s.d.Leases.Active(ctx, runKey(runID))
	if err != nil {
		return CheckpointRef{}, fmt.Errorf("runner: read workspace claim of run %s: %w", runID, err)
	}
	if !ok || rec.Job == nil || *rec.Job != f {
		return CheckpointRef{}, fmt.Errorf("%w: job %s generation %d does not hold run %s's workspace", ErrResourceFenced, f.JobID, f.Generation, runID)
	}
	return s.prepareBuiltIn(ctx, f, runID, dir, rec.Fence)
}

// FinishSuccess returns the exact prepared candidate of THIS attempt. It never
// publishes, clears dirty, completes the job or consults Latest.
func (s *WorkspaceSync) FinishSuccess(ctx context.Context, a Assignment, att WorkspaceAttempt) (engine.WorkspaceCompletion, error) {
	f := a.Job.Fence()
	if att.RunID == uuid.Nil || att.RunID != a.Job.Key.RunID {
		return engine.WorkspaceCompletion{}, fmt.Errorf("%w: attempt run %s does not match its job", ErrNoWorkspace, att.RunID)
	}
	if att.Runner == BuiltInRunnerID {
		ref, err := s.prepareBuiltIn(ctx, f, att.RunID, att.WorkDir, att.Resource)
		if errors.Is(err, ErrPromisorObjectsMissing) {
			localRef, err := s.local.Take(ctx, att.RunID, att.WorkDir, att.Resource)
			if err != nil {
				return engine.WorkspaceCompletion{}, err
			}
			return engine.WorkspaceCompletion{NonPortable: true, LocalRef: localRef.String(), Resource: att.Resource}, nil
		}
		if err != nil {
			return engine.WorkspaceCompletion{}, err
		}
		return engine.WorkspaceCompletion{Ref: ref.String(), Resource: att.Resource}, nil
	}
	if err := s.d.Ledger.FenceCheck(ctx, f); err != nil {
		return engine.WorkspaceCompletion{}, err
	}
	if err := s.d.Leases.Check(ctx, att.Resource); err != nil {
		return engine.WorkspaceCompletion{}, err
	}
	// The durable catalog row for THIS job fence + resource is the authority
	// (an adopted job after a server restart commits its pushed candidate);
	// the in-memory staged cache is only used to discard staging on failure.
	ref, ok, err := s.d.Checkpoints.PreparedFor(ctx, att.RunID, f, att.Resource)
	if err != nil {
		return engine.WorkspaceCompletion{}, fmt.Errorf("runner: read prepared push of run %s: %w", att.RunID, err)
	}
	if !ok {
		return engine.WorkspaceCompletion{}, fmt.Errorf("%w: run %s job %s generation %d", ErrPushMissing, att.RunID, f.JobID, f.Generation)
	}
	if ref.RunID != att.RunID || ref.Version <= att.Base.Version {
		return engine.WorkspaceCompletion{}, fmt.Errorf("%w: prepared candidate %s does not follow base %s", ErrPushMissing, ref, att.Base)
	}
	if p, cached := s.takePrepared(f); cached && p.ref != ref {
		return engine.WorkspaceCompletion{}, fmt.Errorf("%w: cached push %s disagrees with the durable candidate %s", ErrCheckpointConflict, p.ref, ref)
	}
	return engine.WorkspaceCompletion{Ref: ref.String(), Resource: att.Resource}, nil
}

// FinishFailure rolls back this attempt after its process stopped: discard
// its unconsumed push, restore the built-in copy to the committed base (a
// remote copy stays dirty and is restored before reuse), then release ONLY
// this attempt's exact claim.
func (s *WorkspaceSync) FinishFailure(ctx context.Context, a Assignment, att WorkspaceAttempt) error {
	f := a.Job.Fence()
	if att.RunID != a.Job.Key.RunID || a.Fence != f {
		return ErrFenced
	}
	if err := s.d.Ledger.FenceCheck(ctx, f); err != nil {
		return err
	}
	rec, ok, err := s.d.Leases.Active(ctx, runKey(att.RunID))
	if err != nil {
		return err
	}
	if !ok || rec.Job == nil || *rec.Job != f || rec.Fence != att.Resource {
		return ErrResourceFenced
	}
	if err := s.d.Leases.Check(ctx, att.Resource); err != nil {
		return err
	}
	var errs []error
	if p, ok := s.takePrepared(f); ok {
		if err := s.d.Checkpoints.Discard(ctx, p.staged); err != nil {
			errs = append(errs, fmt.Errorf("runner: discard push of run %s: %w", att.RunID, err))
		}
	}
	if att.Runner == BuiltInRunnerID {
		home, err := s.d.Homes.GetHome(ctx, att.RunID)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("runner: read workspace home of run %s: %w", att.RunID, err))
		case home.NonPortableReason != "":
			if err := s.local.Restore(ctx, att.RunID, att.WorkDir); err != nil {
				errs = append(errs, fmt.Errorf("runner: roll back non-portable run %s: %w", att.RunID, err))
			}
		default:
			if err := s.restoreCommitted(ctx, att.RunID, att.WorkDir); err != nil {
				errs = append(errs, fmt.Errorf("runner: roll back run %s: %w", att.RunID, err))
			}
		}
	}
	if att.Resource.Validate() == nil {
		if err := s.d.Leases.Release(ctx, att.Resource); err != nil && !errors.Is(err, ErrResourceFenced) {
			errs = append(errs, fmt.Errorf("runner: release workspace claim of run %s: %w", att.RunID, err))
		}
	}
	return errors.Join(errs...)
}

// restoreCommitted rewrites dir to the run's committed checkpoint and clears
// dirty for a server-local home.
func (s *WorkspaceSync) restoreCommitted(ctx context.Context, runID uuid.UUID, dir string) error {
	ref, ok, err := s.d.Checkpoints.Latest(ctx, runID)
	if err != nil {
		return fmt.Errorf("runner: read committed checkpoint of run %s: %w", runID, err)
	}
	if !ok {
		return fmt.Errorf("%w: run %s", ErrCheckpointNotFound, runID)
	}
	if err := s.d.Checkpoints.Restore(ctx, ref, dir); err != nil {
		return fmt.Errorf("runner: restore run %s: %w", runID, err)
	}
	home, err := s.d.Homes.GetHome(ctx, runID)
	if err != nil {
		return fmt.Errorf("runner: read workspace home of run %s: %w", runID, err)
	}
	if isServerLocal(home.RunnerID) {
		if err := s.d.Homes.MarkDirty(ctx, runID, false); err != nil {
			return fmt.Errorf("runner: clear dirty of run %s: %w", runID, err)
		}
	}
	return nil
}

// RestoreBuiltIn restores the run's committed recovery base.
func (s *WorkspaceSync) RestoreBuiltIn(ctx context.Context, runID uuid.UUID, dir string) error {
	home, err := s.d.Homes.GetHome(ctx, runID)
	if err != nil {
		return err
	}
	if home.NonPortableReason != "" {
		return s.local.Restore(ctx, runID, dir)
	}
	return s.restoreCommitted(ctx, runID, dir)
}

// AcceptPush prepares a remote candidate for the exact job fence and the run's
// active claim. Scope comes from the trusted session (staged.Resource is set by
// P3 T7), never from archive bytes. It never clears dirty or moves Latest.
func (s *WorkspaceSync) AcceptPush(ctx context.Context, f Fence, runID uuid.UUID, staged StagedCheckpoint) (CheckpointRef, error) {
	ref, err := s.acceptPush(ctx, f, runID, staged)
	if err != nil {
		if derr := s.d.Checkpoints.Discard(context.WithoutCancel(ctx), staged); derr != nil {
			slog.Warn("runner: discarding a refused push", "run", runID, "error", derr)
		}
		return CheckpointRef{}, err
	}
	return ref, nil
}

func (s *WorkspaceSync) acceptPush(ctx context.Context, f Fence, runID uuid.UUID, staged StagedCheckpoint) (CheckpointRef, error) {
	if staged.RunID != runID {
		return CheckpointRef{}, fmt.Errorf("%w: staged checkpoint is for run %s, not %s", ErrCheckpointConflict, staged.RunID, runID)
	}
	if err := staged.Resource.Validate(); err != nil {
		return CheckpointRef{}, fmt.Errorf("%w: %v", ErrResourceFenced, err)
	}
	if err := s.d.Ledger.FenceCheck(ctx, f); err != nil {
		return CheckpointRef{}, err
	}
	rec, ok, err := s.d.Leases.Active(ctx, runKey(runID))
	if err != nil {
		return CheckpointRef{}, fmt.Errorf("runner: read workspace claim of run %s: %w", runID, err)
	}
	if !ok || rec.Fence != staged.Resource || rec.Job == nil || *rec.Job != f {
		return CheckpointRef{}, fmt.Errorf("%w: job %s generation %d is not run %s's active writer", ErrResourceFenced, f.JobID, f.Generation, runID)
	}
	ref, err := s.d.Checkpoints.Publish(ctx, staged, f)
	if err != nil {
		return CheckpointRef{}, fmt.Errorf("runner: prepare pushed checkpoint of run %s: %w", runID, err)
	}
	s.storePrepared(preparedPush{fence: f, ref: ref, base: staged.Base, resource: staged.Resource, staged: staged})
	return ref, nil
}

// PreparePull returns the pull plan of a remote attempt: the committed ref
// read once under the held claim, pinned with the claim into both tokens.
func (s *WorkspaceSync) PreparePull(ctx context.Context, a Assignment, runID uuid.UUID) (PullPlan, error) {
	if s.d.Tokens == nil {
		return PullPlan{}, ErrNoTransferTokens
	}
	f := a.Fence
	if runID != a.Job.Key.RunID {
		return PullPlan{}, fmt.Errorf("%w: assignment run %s is not %s", ErrNoWorkspace, a.Job.Key.RunID, runID)
	}
	if err := s.d.Ledger.FenceCheck(ctx, f); err != nil {
		return PullPlan{}, err
	}
	rec, ok, err := s.d.Leases.Active(ctx, runKey(runID))
	if err != nil {
		return PullPlan{}, fmt.Errorf("runner: read workspace claim of run %s: %w", runID, err)
	}
	if !ok || rec.Job == nil || *rec.Job != f {
		return PullPlan{}, fmt.Errorf("%w: job %s generation %d does not hold run %s's workspace", ErrResourceFenced, f.JobID, f.Generation, runID)
	}
	if err := s.d.Leases.Check(ctx, rec.Fence); err != nil {
		return PullPlan{}, err
	}
	home, err := s.d.Homes.GetHome(ctx, runID)
	if err != nil {
		return PullPlan{}, fmt.Errorf("runner: read workspace home of run %s: %w", runID, err)
	}
	if home.RunnerID != f.RunnerID {
		return PullPlan{}, fmt.Errorf("%w: run %s is homed on %q, not %q", ErrHomeOwned, runID, home.RunnerID, f.RunnerID)
	}
	ref, ok, err := s.d.Checkpoints.Latest(ctx, runID)
	if err != nil {
		return PullPlan{}, fmt.Errorf("runner: read committed checkpoint of run %s: %w", runID, err)
	}
	if !ok {
		return PullPlan{}, fmt.Errorf("%w: run %s", ErrCheckpointNotFound, runID)
	}
	scope := TransferScope{Fence: f, RunID: runID, BaseRef: ref.String(), Resource: rec.Fence}
	pull, push := scope, scope
	pull.Direction, push.Direction = TransferPull, TransferPush
	pullTok, err := s.d.Tokens.Issue(ctx, pull, s.d.TokenTTL)
	if err != nil {
		return PullPlan{}, fmt.Errorf("runner: issue pull token for run %s: %w", runID, err)
	}
	pushTok, err := s.d.Tokens.Issue(ctx, push, s.d.TokenTTL)
	if err != nil {
		return PullPlan{}, fmt.Errorf("runner: issue push token for run %s: %w", runID, err)
	}
	return PullPlan{RunID: runID, Ref: ref, Home: home, Resource: rec.Fence, PullToken: pullTok, PushToken: pushTok}, nil
}

// WorkspaceAdopter rebuilds an adopted job's workspace attempt (P3 T10).
type WorkspaceAdopter interface {
	AdoptAttempt(ctx context.Context, a Assignment, job *StepJob) (WorkspaceAttempt, error)
}

var _ WorkspaceAdopter = (*WorkspaceSync)(nil)

// AdoptAttempt rebuilds the workspace context without moving, restoring or
// claiming it. The job must still hold its exact original claim.
func (s *WorkspaceSync) AdoptAttempt(ctx context.Context, a Assignment, job *StepJob) (WorkspaceAttempt, error) {
	runID, workDir := a.Job.Key.RunID, jobWorkDir(job)
	if runID == uuid.Nil || workDir == "" {
		return WorkspaceAttempt{}, fmt.Errorf("%w: adopted job %s", ErrNoWorkspace, a.Job.ID)
	}
	if a.Fence != a.Job.Fence() {
		return WorkspaceAttempt{}, fmt.Errorf("%w: assignment fence does not match its job", ErrFenced)
	}
	if err := s.d.Ledger.FenceCheck(ctx, a.Fence); err != nil {
		return WorkspaceAttempt{}, err
	}
	rec, ok, err := s.d.Leases.Active(ctx, runKey(runID))
	if err != nil {
		return WorkspaceAttempt{}, fmt.Errorf("runner: read workspace claim of run %s: %w", runID, err)
	}
	if !ok || rec.Job == nil || *rec.Job != a.Fence {
		return WorkspaceAttempt{}, fmt.Errorf("%w: adopted job %s generation %d does not hold run %s's workspace",
			ErrResourceFenced, a.Fence.JobID, a.Fence.Generation, runID)
	}
	base, _, err := s.d.Checkpoints.Latest(ctx, runID)
	if err != nil {
		return WorkspaceAttempt{}, fmt.Errorf("runner: read committed checkpoint of run %s: %w", runID, err)
	}
	return WorkspaceAttempt{RunID: runID, WorkDir: workDir, Runner: a.Fence.RunnerID, Base: base, Resource: rec.Fence}, nil
}

// ReportProgress publishes real transfer bytes for a live attempt (P3 T7).
// Invalid counts and stale fences publish nothing.
func (s *WorkspaceSync) ReportProgress(ctx context.Context, f Fence, runID uuid.UUID, done, total int64) {
	if done < 0 || total < 0 || (total > 0 && done > total) {
		return
	}
	job, err := s.d.Ledger.Get(ctx, f.JobID)
	if err != nil || job.Fence() != f || !job.State.HoldsSlot() || job.Key.RunID != runID {
		return
	}
	s.d.Status.PublishMove(ctx, job, f.RunnerID, instance.MoveProgress{BytesDone: done, BytesTotal: total})
}
