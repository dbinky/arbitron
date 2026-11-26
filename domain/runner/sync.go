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
	"github.com/google/uuid"
)

// MaxPullFailures bounds how many pull failures one operation may spend on
// re-placement before the step fails.
const MaxPullFailures = 3

// WorkspaceBytesUnknown disables optional load moves when sizing fails.
const WorkspaceBytesUnknown int64 = -1

const (
	EventWorkspaceMoved       = "workspace.moved"        // run-scoped
	EventWorkspaceSizeUnknown = "workspace.size_unknown" // run-scoped diagnostic
)

var (
	ErrPushMissing      = errors.New("runner: the remote step finished without a prepared workspace push for this attempt")
	ErrNoTransferTokens = errors.New("runner: workspace transfers are not configured (no transfer token service)")
	ErrNoWorkspace      = errors.New("runner: workspace operation has no workspace service or working directory")
	ErrNoCompletionSink = errors.New("runner: workspace step has no completion sink, so it can never be committed")
	ErrUnknownReceipt   = errors.New("runner: completion receipt does not match a retained uncommitted operation")
)

// WorkspaceSizer reports a server-local run workspace size (engine.WorkspaceManager satisfies it).
type WorkspaceSizer interface {
	InstanceDirStats(instanceID uuid.UUID) (bytes int64, files int, err error)
}

// SyncDeps are WorkspaceSync's collaborators.
type SyncDeps struct {
	Local       LocalCheckpointStore
	Checkpoints CheckpointStore
	Homes       HomeStore
	Leases      ResourceLeaseStore
	Ledger      Ledger
	Tokens      TransferTokens // nil => PreparePull fails with ErrNoTransferTokens
	Events      *EventRecorder
	Status      *ExecutionStatusProjector
	Sizes       WorkspaceSizer // nil => server-local size unknown
	Clock       func() time.Time
	TokenTTL    time.Duration // <= 0 => DefaultTransferTokenTTL
}

// WorkspaceAttempt is one reserved attempt's workspace context.
type WorkspaceAttempt struct {
	RunID    uuid.UUID
	WorkDir  string
	Runner   RunnerID
	Base     CheckpointRef           // committed base the attempt started from (zero for a non-portable home)
	Resource engine.ResourceFenceRef // the exact writer claim
	Moved    bool
}

// PullPlan is what a remote runner needs: the exact committed ref to pull,
// the home generation, the claim, and tokens pinned to both.
type PullPlan struct {
	RunID     uuid.UUID
	Ref       CheckpointRef
	Home      Home
	Resource  engine.ResourceFenceRef
	PullToken TransferToken
	PushToken TransferToken
}

// preparedPush is a remote candidate accepted for one exact job fence.
type preparedPush struct {
	fence    Fence
	ref      CheckpointRef
	base     CheckpointRef
	resource engine.ResourceFenceRef
	staged   StagedCheckpoint
}

// WorkspaceSync keeps the committed server checkpoint authoritative for
// every workspace step. d holds the validated deps (P3 T16 extends them).
type WorkspaceSync struct {
	d        SyncDeps
	local    *LocalCheckpoints
	mu       sync.Mutex
	prepared map[uuid.UUID]preparedPush // by job id
}

// NewWorkspaceSync validates deps; a missing core collaborator panics.
func NewWorkspaceSync(d SyncDeps) *WorkspaceSync {
	var missing []string
	if d.Checkpoints == nil {
		missing = append(missing, "Checkpoints")
	}
	if d.Homes == nil {
		missing = append(missing, "Homes")
	}
	if d.Leases == nil {
		missing = append(missing, "Leases")
	}
	if d.Ledger == nil {
		missing = append(missing, "Ledger")
	}
	if d.Events == nil {
		missing = append(missing, "Events")
	}
	if d.Status == nil {
		missing = append(missing, "Status")
	}
	if d.Local == nil {
		missing = append(missing, "Local")
	}
	if len(missing) > 0 {
		panic("runner: NewWorkspaceSync requires " + strings.Join(missing, ", "))
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.TokenTTL <= 0 {
		d.TokenTTL = DefaultTransferTokenTTL
	}
	return &WorkspaceSync{d: d, prepared: map[uuid.UUID]preparedPush{},
		local: &LocalCheckpoints{d: LocalCheckpointDeps{Local: d.Local, Homes: d.Homes, Checkpoints: d.Checkpoints, Leases: d.Leases}}}
}

// isServerLocal reports whether a home's bytes are the server's own copy.
func isServerLocal(id RunnerID) bool { return id == ServerHome || id == BuiltInRunnerID }

func runKey(runID uuid.UUID) string { return "run:" + runID.String() }

func jobWorkDir(job *StepJob) string {
	switch {
	case job == nil:
		return ""
	case job.Agent != nil:
		return job.Agent.WorkDir
	case job.Process != nil:
		return job.Process.WorkDir
	}
	return ""
}

// BeginAttempt claims the run's workspace for this exact job fence, moves the
// home if needed (with the exact operation cause), restores the built-in copy
// when it is not the committed base, and marks the home dirty. It NEVER
// bootstraps: a missing committed baseline is ErrCheckpointNotFound.
func (s *WorkspaceSync) BeginAttempt(ctx context.Context, a Assignment, req ExecRequirements, dec Decision, job *StepJob) (WorkspaceAttempt, error) {
	runID, workDir := a.Job.Key.RunID, jobWorkDir(job)
	if runID == uuid.Nil || (req.RunID != uuid.Nil && req.RunID != runID) {
		return WorkspaceAttempt{}, fmt.Errorf("%w: assignment run %s does not match request run %s", ErrNoWorkspace, runID, req.RunID)
	}
	if workDir == "" {
		return WorkspaceAttempt{}, fmt.Errorf("%w: run %s", ErrNoWorkspace, runID)
	}
	if a.Fence != a.Job.Fence() || !a.Job.Key.Valid() {
		return WorkspaceAttempt{}, fmt.Errorf("%w: assignment fence does not match its job", ErrFenced)
	}
	if err := s.d.Ledger.FenceCheck(ctx, a.Fence); err != nil {
		return WorkspaceAttempt{}, err
	}
	home, err := s.d.Homes.GetHome(ctx, runID)
	if err != nil {
		return WorkspaceAttempt{}, fmt.Errorf("runner: read workspace home of run %s: %w", runID, err)
	}
	nonPortable := home.NonPortableReason != ""
	if nonPortable && a.Fence.RunnerID != BuiltInRunnerID {
		return WorkspaceAttempt{}, fmt.Errorf("%w: run %s is non-portable (%s) and runs only on %s",
			ErrHomeOwned, runID, home.NonPortableReason, BuiltInRunnerID)
	}
	claim, err := s.d.Leases.ClaimJob(ctx, runKey(runID), JobHolder(a.Job), a.Fence)
	if err != nil {
		return WorkspaceAttempt{}, fmt.Errorf("runner: claim workspace of run %s: %w", runID, err)
	}
	att, err := s.prepareAttempt(ctx, a, req, dec, home, claim, workDir, nonPortable)
	if err != nil {
		if rerr := s.d.Leases.Release(context.WithoutCancel(ctx), claim); rerr != nil {
			slog.Error("runner: releasing a failed attempt's workspace claim", "run", runID, "error", rerr)
		}
		return WorkspaceAttempt{}, err
	}
	return att, nil
}

func (s *WorkspaceSync) prepareAttempt(ctx context.Context, a Assignment, req ExecRequirements, dec Decision, home Home,
	claim engine.ResourceFenceRef, workDir string, nonPortable bool) (WorkspaceAttempt, error) {
	runID, target := a.Job.Key.RunID, a.Fence.RunnerID
	base, ok, err := s.d.Checkpoints.Latest(ctx, runID)
	if err != nil {
		return WorkspaceAttempt{}, fmt.Errorf("runner: read committed checkpoint of run %s: %w", runID, err)
	}
	if !ok && !nonPortable {
		return WorkspaceAttempt{}, fmt.Errorf("%w: run %s has no committed baseline (BeginAttempt never bootstraps)", ErrCheckpointNotFound, runID)
	}
	prevOwner, prevDirty := home.RunnerID, home.Dirty
	moved := false
	switch {
	case prevOwner == target:
	case prevOwner == ServerHome && target == BuiltInRunnerID:
		if home, err = s.d.Homes.AcquireResource(ctx, runID, target, home.Generation); err != nil {
			return WorkspaceAttempt{}, fmt.Errorf("runner: acquire workspace of run %s: %w", runID, err)
		}
	default:
		reason := dec.Reason
		if dec.Tier != "" && !strings.HasPrefix(reason, dec.Tier) {
			reason = dec.Tier + ": " + reason
		}
		bytes := req.WorkspaceBytes
		if bytes < 0 {
			bytes = 0
		}
		jobID, invID := a.Job.ID, a.Job.Key.InvocationID
		cause := HomeMoveCause{TargetJobID: &jobID, InvocationID: &invID, NodeID: a.Job.NodeID, OperationID: a.Job.Key.OperationID,
			FromGeneration: home.Generation, ToGeneration: home.Generation + 1}
		s.d.Status.PublishMove(ctx, a.Job, target, instance.MoveProgress{BytesTotal: bytes})
		if home, err = s.d.Homes.HandOff(ctx, runID, target, reason, bytes, home.Generation, cause); err != nil {
			return WorkspaceAttempt{}, fmt.Errorf("runner: move workspace of run %s to %s: %w", runID, target, err)
		}
		moved = true
		rid := runID
		s.d.Events.Record(ctx, PoolEvent{Kind: EventWorkspaceMoved, RunnerID: target, RunID: &rid, Cause: Cause{Kind: CausePlacement},
			Detail: map[string]any{"from": string(prevOwner), "to": string(target), "reason": reason, "tier": dec.Tier, "bytes": bytes,
				"generation": home.Generation, "jobId": jobID.String(), "invocationId": invID.String(),
				"nodeId": a.Job.NodeID, "operationId": a.Job.Key.OperationID}})
	}
	if err := s.d.Homes.MarkDirty(ctx, runID, true); err != nil {
		return WorkspaceAttempt{}, fmt.Errorf("runner: mark workspace of run %s dirty: %w", runID, err)
	}
	if target == BuiltInRunnerID && !nonPortable && (!isServerLocal(prevOwner) || prevDirty || home.CheckpointVersion != base.Version) {
		if err := s.d.Checkpoints.Restore(ctx, base, workDir); err != nil {
			return WorkspaceAttempt{}, fmt.Errorf("runner: restore committed base of run %s: %w", runID, err)
		}
	}
	return WorkspaceAttempt{RunID: runID, WorkDir: workDir, Runner: target, Base: base, Resource: claim, Moved: moved}, nil
}

// WorkspaceBytes is the move-size input for placement: the server-local size
// for server/built-in homes, the committed manifest size for remote homes,
// WorkspaceBytesUnknown (with a visible diagnostic) on any error.
func (s *WorkspaceSync) WorkspaceBytes(ctx context.Context, runID uuid.UUID) int64 {
	home, err := s.d.Homes.GetHome(ctx, runID)
	if err != nil {
		return s.sizeUnknown(ctx, runID, "home", err)
	}
	if !isServerLocal(home.RunnerID) {
		ref, ok, err := s.d.Checkpoints.Latest(ctx, runID)
		if err == nil && !ok {
			err = ErrCheckpointNotFound
		}
		if err != nil {
			return s.sizeUnknown(ctx, runID, "checkpoint", err)
		}
		m, err := s.d.Checkpoints.Manifest(ctx, ref)
		if err != nil {
			return s.sizeUnknown(ctx, runID, "manifest", err)
		}
		return m.TotalBytes()
	}
	if s.d.Sizes == nil {
		return s.sizeUnknown(ctx, runID, "sizer", errors.New("no workspace sizer configured"))
	}
	b, _, err := s.d.Sizes.InstanceDirStats(runID)
	if err != nil {
		return s.sizeUnknown(ctx, runID, "instance_dir", err)
	}
	return b
}

func (s *WorkspaceSync) sizeUnknown(ctx context.Context, runID uuid.UUID, source string, err error) int64 {
	slog.Warn("runner: workspace size unknown; optional load moves disabled", "run", runID, "source", source, "error", err)
	rid := runID
	s.d.Events.Record(ctx, PoolEvent{Kind: EventWorkspaceSizeUnknown, RunID: &rid, Cause: Cause{Kind: CauseSystem},
		Detail: map[string]any{"source": source}})
	return WorkspaceBytesUnknown
}
