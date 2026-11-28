package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
)

// SessionMoveCommit publishes a move and its session metadata atomically.
type SessionMoveCommit struct {
	ExecutionID    uuid.UUID
	Move           instance.SessionMoveRecord
	SessionID      string
	PendingRebuild bool
	Provisional    bool
	Lease          engine.TurnLease
	InvocationID   uuid.UUID
	IntentID       uuid.UUID
	Resource       engine.ResourceFenceRef
	Completions    []engine.StepCompletion
}

// MoveState records the durable progress of one move.
type MoveState string

const (
	MoveStatePreparing MoveState = "preparing"
	MoveStateRestoring MoveState = "restoring"
	MoveStateImporting MoveState = "importing"
	MoveStateCommitted MoveState = "committed"
	MoveStateFailed    MoveState = "failed"
)

func (s MoveState) Open() bool {
	return s == MoveStatePreparing || s == MoveStateRestoring || s == MoveStateImporting
}

// SessionMoveState persists the selected target and progress before effects.
type SessionMoveState struct {
	Move             instance.SessionMoveRecord
	State            MoveState
	IntentID         uuid.UUID
	OwnsIntent       bool
	ExecutionID      uuid.UUID
	TargetSource     SourceID
	JobID            uuid.UUID
	Requirements     ExecRequirements
	WorkDir          string
	ResourceID       uuid.UUID
	RestoreWorkspace bool
	OldJobs          []engine.JobFenceRef
	NewResource      *engine.ResourceFenceRef
	Failure          string
}

type SessionMoveBegin struct {
	State  SessionMoveState
	Intent *engine.TurnIntent
}

// SessionMoveStore publishes pin, history, job completion and restore release
// in one transaction after validating the held lease and exact operation.
type SessionMoveStore interface {
	BeginMove(ctx context.Context, b SessionMoveBegin) (SessionMoveState, error)
	MoveForIntent(ctx context.Context, intentID uuid.UUID) (SessionMoveState, bool, error)
	AdvanceMove(ctx context.Context, st SessionMoveState) error
	FailMove(ctx context.Context, moveID uuid.UUID, cause string) error
	CommitMove(ctx context.Context, c SessionMoveCommit) error
}

// OpSessionImport installs native state as a fenced, slot-counted move job.
const OpSessionImport OpType = "session_import"

// SessionOp is the payload of a session_import StepJob.
type SessionOp struct {
	Backend   string
	SessionID string
	WorkDir   string
	Bundle    *engine.SessionBundle // native bytes to install
}

// SessionOpResult identifies the imported native session.
type SessionOpResult struct {
	SessionID string
}

// MoveRequest (contracts §17) plus the P4 T8 fields the mover needs.
type MoveRequest struct {
	RunID, InvocationID uuid.UUID
	NodeID              string
	Reason              string // exhausted|logged_out|drain|runner_lost
	From                RunnerID
	Requirements        ExecRequirements
	ExecutionID         uuid.UUID
	TurnIndex           int
	Binding             provider.ResolvedBinding
	WorkDir             string
	ResourceID          uuid.UUID
	RestoreWorkspace    bool
	FirstTurn           bool

	RequestID uuid.UUID
}

// MoveResult (contracts §17) plus PendingRebuild.
type MoveResult struct {
	To             RunnerID
	SessionID      string
	Lossy          bool
	Notice         string
	Generation     int64
	PendingRebuild bool
}

// WorkspaceRestorer restores the last committed checkpoint of a workspace
// resource into dir. *WorkspaceSync (P3 T6) satisfies it.
type WorkspaceRestorer interface {
	RestoreBuiltIn(ctx context.Context, runID uuid.UUID, dir string) error
}

// MoverDeps are the mover's collaborators. All are required except Telemetry.
type MoverDeps struct {
	Registry    Registry
	Ledger      Ledger
	Runners     RunnerSet
	Telemetry   TelemetrySource
	Leases      engine.TurnLeaseStore
	Intents     engine.TurnIntentStore // P4 T3 TurnIntentRepo: the in-flight turn's intent; idempotent completion
	Checkpoints engine.SessionCheckpointStore
	Executions  instance.NodeExecutionRepository
	Moves       SessionMoveStore
	Homes       HomeStore
	Restorer    WorkspaceRestorer
	Resources   ResourceLeaseStore // P3 T3 ResourceLeaseRepo: the restore runs only under the move's own claim
	Portability *SessionPortabilityRegistry
	Events      *EventRecorder
	Status      *ExecutionStatusProjector
	Bus         engine.EventBus
	Policy      PlacementPolicy
	Clock       func() time.Time
	OpLease     time.Duration // ledger lease for the import op; <= 0 means 2 minutes
}

type SessionMover struct{ d MoverDeps }

var _ engine.SessionMover = (*SessionMover)(nil)

// NewSessionMover panics on a missing required collaborator (composition-root bug).
func NewSessionMover(d MoverDeps) *SessionMover {
	if d.Registry == nil || d.Ledger == nil || d.Runners == nil || d.Leases == nil || d.Intents == nil || d.Checkpoints == nil ||
		d.Executions == nil || d.Moves == nil || d.Homes == nil || d.Restorer == nil || d.Resources == nil || d.Portability == nil ||
		d.Events == nil || d.Status == nil || d.Bus == nil {
		panic("runner: NewSessionMover requires every collaborator")
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.OpLease <= 0 {
		d.OpLease = 2 * time.Minute
	}
	return &SessionMover{d: d}
}

// MoveSession adapts the engine port: it derives the exact model group and
// requirements from the accepted binding (same family only, GroupFor).
func (m *SessionMover) MoveSession(ctx context.Context, lease engine.TurnLease, req engine.SessionMoveRequest) (engine.SessionMoveResult, error) {
	group, err := GroupFor(req.Binding, OpType("stream"))
	if err != nil {
		return engine.SessionMoveResult{}, fmt.Errorf("session move: %w", err)
	}
	fingerprint, err := EndpointFingerprint(req.Binding)
	if err != nil {
		return engine.SessionMoveResult{}, fmt.Errorf("session move: %w", err)
	}
	res, err := m.Move(ctx, lease, MoveRequest{
		RequestID: req.RequestID,
		RunID:     req.RunID, InvocationID: req.InvocationID, NodeID: req.NodeID, Reason: req.Reason, From: RunnerID(req.From),
		Requirements: ExecRequirements{Group: group, Model: req.Binding.Model, Backend: req.Binding.Backend,
			Provider: req.Binding.Provider, EndpointFingerprint: fingerprint, ContextWindow: req.Binding.ContextWindow, Effort: req.Binding.Effort,
			RequireExecutedModel: req.Binding.RequiresModelEvidence(), Tools: req.Binding.AllowedTools},
		ExecutionID: req.ExecutionID, TurnIndex: req.TurnIndex, Binding: req.Binding, WorkDir: req.WorkDir,
		ResourceID: req.ResourceID, RestoreWorkspace: req.RestoreWorkspace, FirstTurn: req.FirstTurn,
	})
	if err != nil {
		return engine.SessionMoveResult{}, err
	}
	return engine.SessionMoveResult{To: string(res.To), SessionID: res.SessionID, Lossy: res.Lossy, Notice: res.Notice,
		Generation: res.Generation, PendingRebuild: res.PendingRebuild}, nil
}

// moveFirstTurn: no bundle exists before the first success, so the step
// repeats as a whole on whichever runner the dispatcher places next. Fence the
// interrupted attempt, reserve the next generation, restore the committed
// workspace (discarding partial edits). There is no session record yet, so
// nothing is committed to session_moves.
func (m *SessionMover) moveFirstTurn(ctx context.Context, lease engine.TurnLease, req MoveRequest) (res MoveResult, err error) {
	if _, err := m.fenceOldOwner(ctx, req); err != nil {
		return MoveResult{}, err
	}
	lease, err = m.d.Leases.BumpGeneration(ctx, lease)
	if err != nil {
		return MoveResult{}, fmt.Errorf("session move: reserve session generation: %w", err)
	}
	if req.RestoreWorkspace {
		claim, claimErr := m.d.Resources.Claim(ctx, moveResourceKey(req), "session-first-turn:"+req.InvocationID.String(), BuiltInRunnerID)
		if claimErr != nil {
			return MoveResult{}, fmt.Errorf("session move: restore claim: %w", claimErr)
		}
		defer func() { err = errors.Join(err, m.d.Resources.Release(ctx, claim)) }()
		if err := m.restoreWorkspace(ctx, req, ""); err != nil {
			return MoveResult{}, fmt.Errorf("session move: restore committed workspace: %w: %v", engine.ErrSessionRestoreFailed, err)
		}
	}
	return MoveResult{Generation: lease.Generation}, nil
}

func (m *SessionMover) visibleWait(ctx context.Context, job Job, w *WaitReason, req ExecRequirements) error {
	if w == nil {
		w = &WaitReason{Kind: WaitKind("capacity"), Group: req.Group, Model: req.Model}
	}
	if err := m.d.Ledger.SetWait(ctx, job.ID, w); err != nil {
		return fmt.Errorf("session move: record wait: %w", err)
	}
	job.Wait = w
	state := instance.ExecutionState("waiting_capacity")
	if w.Kind == WaitKind("slot") {
		state = instance.ExecutionState("queued_slot")
	}
	m.d.Status.Publish(ctx, job, state)
	return &engine.SessionWaitError{Kind: string(w.Kind), Group: w.Group.String(), Model: w.Model, NextReset: w.NextReset}
}

// restoreWorkspace discards interrupted edits on built-in and remote runners
// alike: the server copy is rewritten from the last committed checkpoint, the
// resource home is handed to the target, and a remote target is marked dirty so
// its next dispatch pulls a full committed snapshot. target "" = unknown (first turn).
func (m *SessionMover) restoreWorkspace(ctx context.Context, req MoveRequest, target RunnerID) error {
	resource := req.ResourceID
	if resource == uuid.Nil {
		resource = req.RunID
	}
	if err := m.d.Restorer.RestoreBuiltIn(ctx, resource, req.WorkDir); err != nil {
		return err
	}
	home, err := m.d.Homes.GetHome(ctx, resource)
	if err != nil {
		return err
	}
	if target != "" && home.RunnerID != target {
		cause := HomeMoveCause{InvocationID: &req.InvocationID, NodeID: req.NodeID,
			FromGeneration: home.Generation, ToGeneration: home.Generation + 1}
		if _, err := m.d.Homes.HandOff(ctx, resource, target, "session_"+req.Reason, 0, home.Generation, cause); err != nil {
			return err
		}
	}
	if target == BuiltInRunnerID {
		return m.d.Homes.MarkDirty(ctx, resource, false)
	}
	return m.d.Homes.MarkDirty(ctx, resource, true)
}

// importOrRebuild falls back only when native restoration is incompatible.
// Storage and transport failures remain visible and retryable.
func (m *SessionMover) importOrRebuild(ctx context.Context, a Assignment, req MoveRequest, exec *instance.NodeExecution) (string, bool, error) {
	if ref := strPtr(exec.SessionCheckpointRef); ref != "" {
		bundle, err := m.d.Checkpoints.Get(ctx, ref)
		if err != nil {
			return "", false, fmt.Errorf("session move: load bundle: %w", err)
		}
		sr, ok := m.d.Runners.For(a.Job.RunnerID)
		if !ok {
			return "", false, fmt.Errorf("session move: %w: %s", ErrRunnerLost, a.Job.RunnerID)
		}
		out, err := sr.Run(ctx, a, StepJob{Op: OpSessionImport, Binding: req.Binding,
			Session: &SessionOp{Backend: bundle.Backend, SessionID: bundle.SessionID, WorkDir: req.WorkDir, Bundle: &bundle}})
		if err == nil && out.Session != nil && out.Session.SessionID != "" {
			return out.Session.SessionID, false, nil
		}
		if err != nil && !errors.Is(err, engine.ErrBundleRequiresRebuild) {
			return "", false, fmt.Errorf("session move: import bundle: %w", err)
		}
	}
	if _, err := engine.BuildRebuildPrompt(exec.LLMConversation, "", engine.RebuildLimitBytes(req.Binding.ContextWindow)); err != nil {
		return "", false, fmt.Errorf("session move: transcript rebuild: %w: %w", engine.ErrSessionRestoreFailed, err)
	}
	return "", true, nil
}

func (m *SessionMover) alwaysLossy(req MoveRequest) bool {
	backend := req.Requirements.Backend
	if backend == "" {
		backend = "claude-code"
	}
	p, ok := m.d.Portability.For(backend)
	if !ok {
		return true
	}
	l, ok := p.(engine.LossyImporter)
	return ok && l.AlwaysLossy()
}

func (m *SessionMover) loadExecution(ctx context.Context, runID, execID uuid.UUID) (*instance.NodeExecution, error) {
	execs, err := m.d.Executions.ListByInstance(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("session move: load session record: %w", err)
	}
	for i := range execs {
		if execs[i].ID == execID {
			cp := execs[i]
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("session move: session record %s not found for run %s: %w", execID, runID, engine.ErrSessionRestoreFailed)
}

func jobActive(s JobState) bool {
	return s == JobState("queued") || s == JobState("offered") || s == JobState("running")
}

func previousSource(exec *instance.NodeExecution) SourceID { return SourceID(strPtr(exec.SourceID)) }

func strPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func outcomeFor(reason string) OutcomeClass {
	switch reason {
	case engine.MoveReasonExhausted:
		return OutcomeExhausted
	case engine.MoveReasonLoggedOut:
		return OutcomeLoggedOut
	case engine.MoveReasonRunnerLost:
		return OutcomeRunnerLost
	}
	return OutcomeNone
}

func causeKindFor(reason string) string {
	switch reason {
	case engine.MoveReasonDrain:
		return "operator"
	case engine.MoveReasonRunnerLost:
		return "liveness"
	}
	return "outcome"
}

// ErrNotMoveIntent: ResumeMove only finishes kind=move intents.
var ErrNotMoveIntent = errors.New("runner: intent is not a session move")

// Move runs a durable session move under the caller's HELD lease (never
// re-acquired). The move intent, target and planned generation are persisted
// BEFORE the first effect; every later step is idempotent against that row, so
// a retry or restart finishes the SAME move.
func (m *SessionMover) Move(ctx context.Context, lease engine.TurnLease, req MoveRequest) (MoveResult, error) {
	if !engine.AllowedMoveReason(req.Reason) {
		return MoveResult{}, fmt.Errorf("session move %q: %w", req.Reason, engine.ErrSessionMoveNotAllowed)
	}
	if req.FirstTurn {
		return m.moveFirstTurn(ctx, lease, req)
	}
	exec, err := m.loadExecution(ctx, req.RunID, req.ExecutionID)
	if err != nil {
		return MoveResult{}, err
	}
	pending, err := m.d.Intents.PendingFor(ctx, lease.Key)
	if err != nil {
		return MoveResult{}, fmt.Errorf("session move: read pending intent: %w", err)
	}
	if pending != nil {
		open, found, err := m.d.Moves.MoveForIntent(ctx, pending.ID)
		if err != nil {
			return MoveResult{}, fmt.Errorf("session move: read move state: %w", err)
		}
		if found && open.State.Open() {
			res, err := m.drive(ctx, lease, open, *pending, exec) // never a second target or generation while the target lives
			var gone *moveTargetGoneError
			if !errors.As(err, &gone) {
				return res, err
			}
			// Decision (unstick 2026-11-24): the failed move is final. Plan ONE
			// replacement now, past its reserved generation (the extra bump is
			// accepted). FailMove closed an owned move intent; a mid-turn turn
			// intent stays pending and binds the replacement.
			lease = gone.lease
			if pending, err = m.d.Intents.PendingFor(ctx, lease.Key); err != nil {
				return MoveResult{}, fmt.Errorf("session move: read pending intent: %w", err)
			}
		} else if pending.Kind == engine.TurnIntentMove {
			// Its move finished but the intent completion was lost: close it first.
			if err := m.d.Intents.Complete(ctx, pending.ID); err != nil {
				return MoveResult{}, fmt.Errorf("session move: close finished move intent: %w", err)
			}
			pending = nil
		}
	}

	if pending != nil {
		// A mid-turn move uses the accepted policy, never a newly resolved profile.
		req.Binding = bindingFromAccepted(pending.Binding)
		group, err := GroupFor(req.Binding, OpStream)
		if err != nil {
			return MoveResult{}, fmt.Errorf("session move: accepted group: %w", err)
		}
		fingerprint, err := EndpointFingerprint(req.Binding)
		if err != nil {
			return MoveResult{}, fmt.Errorf("session move: accepted route: %w", err)
		}
		req.Requirements.Group, req.Requirements.Model, req.Requirements.Backend = group, req.Binding.Model, req.Binding.Backend
		req.Requirements.Provider, req.Requirements.EndpointFingerprint = req.Binding.Provider, fingerprint
		req.Requirements.ContextWindow, req.Requirements.Effort = req.Binding.ContextWindow, req.Binding.Effort
		req.Requirements.Tools = append([]string(nil), req.Binding.AllowedTools...)
		req.Requirements.RequireExecutedModel = pending.Binding.RequireExecutedModel
	}
	placeReq := req.Requirements
	placeReq.Op, placeReq.Affinity, placeReq.RunID, placeReq.PinnedRunner = OpStream, AffinityNone, req.RunID, ""
	jobReq := placeReq
	jobReq.Op = OpSessionImport
	planned := lease.Generation + 1
	op := fmt.Sprintf("move/%d", planned)
	job, err := m.d.Ledger.EnsureIntent(ctx, JobKey{RunID: req.RunID, InvocationID: req.InvocationID, OperationID: op, Attempt: 1}, req.NodeID, jobReq)
	if err != nil {
		return MoveResult{}, fmt.Errorf("session move: record import job intent: %w", err)
	}
	snap, err := m.d.Registry.Snapshot(ctx)
	if err != nil {
		return MoveResult{}, fmt.Errorf("session move: pool snapshot: %w", err)
	}
	if m.d.Telemetry != nil {
		snap.Telemetry = m.d.Telemetry.Latest()
	}
	snap.Now = m.d.Clock()
	dec := Place(placeReq, snap, m.d.Policy)
	if !dec.Placed {
		return MoveResult{}, m.visibleWait(ctx, job, dec.Wait, placeReq)
	}

	begin := SessionMoveBegin{State: SessionMoveState{
		Move: instance.SessionMoveRecord{ID: uuid.New(), RunID: req.RunID, InvocationID: req.InvocationID, NodeID: req.NodeID,
			FromRunner: string(req.From), ToRunner: string(dec.Runner), Reason: req.Reason, Generation: planned,
			At: m.d.Clock().UTC(), RequestID: req.RequestID, TurnIndex: req.TurnIndex},
		State: MoveStatePreparing, ExecutionID: req.ExecutionID, TargetSource: dec.Source, JobID: job.ID,
		Requirements: jobReq, WorkDir: req.WorkDir, ResourceID: req.ResourceID, RestoreWorkspace: req.RestoreWorkspace,
	}}
	intent := pending
	if intent != nil {
		// Mid-turn: the turn's intent is the resend source and its turn commit completes it.
		begin.State.IntentID = intent.ID
	} else {
		accepted, err := engine.CaptureAcceptedTurnBinding(req.Binding)
		if err != nil {
			return MoveResult{}, fmt.Errorf("session move: %w", err)
		}
		requestID := req.RequestID
		if requestID == uuid.Nil {
			requestID = uuid.New()
		}
		intent = &engine.TurnIntent{ID: uuid.New(), RequestID: requestID, Binding: accepted, LeaseKey: lease.Key,
			RunID: req.RunID, InvocationID: req.InvocationID, NodeID: req.NodeID, TurnIndex: req.TurnIndex,
			Kind: engine.TurnIntentMove, Model: req.Binding.Model, Tools: append([]string(nil), req.Binding.AllowedTools...),
			PriorCheckpointRef: strPtr(exec.SessionCheckpointRef), SessionGeneration: lease.Generation, OperationID: op,
			CreatedAt: m.d.Clock().UTC()}
		begin.Intent, begin.State.IntentID, begin.State.OwnsIntent = intent, intent.ID, true
	}
	st, err := m.d.Moves.BeginMove(ctx, begin)
	if err != nil {
		return MoveResult{}, fmt.Errorf("session move: persist move intent: %w", err)
	}
	return m.drive(ctx, lease, st, *intent, exec)
}

// ResumeMove implements SessionMoveResumer (P4 T10): it finishes the persisted
// move of a pending kind=move intent exactly once, under the lease the
// recovery coordinator placed in ctx. A committed or failed move only closes
// the intent (idempotent); nothing is re-run or re-announced.
func (m *SessionMover) ResumeMove(ctx context.Context, in engine.TurnIntent) error {
	if in.Kind != engine.TurnIntentMove {
		return fmt.Errorf("session move resume %s (%s): %w", in.ID, in.Kind, ErrNotMoveIntent)
	}
	lease, ok := engine.TurnLeaseFromContext(ctx)
	if !ok || lease.Key != in.LeaseKey || lease.Token == uuid.Nil {
		return fmt.Errorf("session move resume %s: no recovered lease for %s: %w", in.ID, in.LeaseKey, engine.ErrTurnLeaseLost)
	}
	st, found, err := m.d.Moves.MoveForIntent(ctx, in.ID)
	if err != nil {
		return fmt.Errorf("session move resume %s: %w", in.ID, err)
	}
	if !found {
		return fmt.Errorf("session move resume %s: no persisted move: %w", in.ID, instance.ErrNotFound)
	}
	if !st.State.Open() {
		return m.d.Intents.Complete(ctx, in.ID)
	}
	exec, err := m.loadExecution(ctx, st.Move.RunID, st.ExecutionID)
	if err != nil {
		return err
	}
	_, err = m.drive(ctx, lease, st, in, exec)
	return err
}

// drive advances one persisted move to committed (or failed). Every step is
// idempotent against the row: the old owner is fenced before target writes,
// the planned generation is reserved once, the reserved slot is reused, the
// restore runs under the move's own claim, and the import job completes only
// inside CommitMove. A retryable failure requeues the slot and releases the
// claim; a deterministic one fails the move. Neither leaves them held.
func (m *SessionMover) drive(ctx context.Context, lease engine.TurnLease, st SessionMoveState, in engine.TurnIntent, exec *instance.NodeExecution) (MoveResult, error) {
	req := moveRequestFrom(st, in)
	if st.State == MoveStatePreparing {
		fenced, err := m.fenceOldOwner(ctx, req)
		if err != nil {
			return MoveResult{}, err
		}
		st.OldJobs = mergeJobFences(st.OldJobs, fenced)
	}
	if lease.Generation < st.Move.Generation {
		bumped, err := m.d.Leases.BumpGeneration(ctx, lease)
		if err != nil {
			return MoveResult{}, fmt.Errorf("session move: reserve session generation: %w", err)
		}
		lease = bumped
	}
	if lease.Generation < st.Move.Generation {
		return MoveResult{}, fmt.Errorf("session move %s: lease generation %d is below the planned %d: %w",
			st.Move.ID, lease.Generation, st.Move.Generation, engine.ErrTurnFenced)
	}
	st.Move.Generation = lease.Generation // a replay recovery may already have reserved a newer one: adopt, never bump twice
	// Decision (unstick 2026-11-24): a gone target fails the move; it is never
	// retried there. Checked on every entry (first run, retry, restart).
	if why, err := m.targetGone(ctx, RunnerID(st.Move.ToRunner)); err != nil {
		return MoveResult{}, err
	} else if why != "" {
		return MoveResult{}, m.failGoneTarget(ctx, st, lease, why)
	}

	job, err := m.reserveTarget(ctx, &st)
	if err != nil {
		_ = m.d.Moves.AdvanceMove(ctx, st) // keep fenced jobs and the reserved generation durable
		return MoveResult{}, err
	}
	fence := job.Fence()
	if st.State == MoveStatePreparing {
		st.State = MoveStateImporting
		if st.RestoreWorkspace {
			st.State = MoveStateRestoring
		}
	}
	if err := m.d.Moves.AdvanceMove(ctx, st); err != nil {
		return MoveResult{}, fmt.Errorf("session move: record progress: %w", err)
	}
	if st.State == MoveStateRestoring {
		if err := m.restoreUnderClaim(ctx, &st, req); err != nil {
			cleanupErr := m.releaseForRetry(ctx, &st, fence)
			return MoveResult{}, errors.Join(fmt.Errorf("session move: restore committed workspace: %w: %w", engine.ErrSessionRestoreFailed, err), cleanupErr)
		}
		st.State = MoveStateImporting
		if err := m.d.Moves.AdvanceMove(ctx, st); err != nil {
			return MoveResult{}, fmt.Errorf("session move: record progress: %w", err)
		}
	}
	sessionID, rebuilt, err := m.importOrRebuild(ctx, Assignment{Job: job, Fence: fence}, req, exec)
	if err != nil {
		if errors.Is(err, engine.ErrSessionRestoreFailed) {
			return MoveResult{}, errors.Join(err, m.failMove(ctx, st, fence, err))
		}
		return MoveResult{}, errors.Join(err, m.releaseForRetry(ctx, &st, fence))
	}
	lossy := rebuilt || m.alwaysLossy(req) || st.TargetSource != previousSource(exec)
	notice := ""
	if lossy {
		notice = engine.SessionLossNotice
	}
	mv := st.Move
	mv.Lossy, mv.Notice, mv.At = lossy, notice, m.d.Clock().UTC()
	commit := SessionMoveCommit{ExecutionID: st.ExecutionID, Move: mv, SessionID: sessionID, PendingRebuild: rebuilt,
		Provisional: !rebuilt, Lease: lease, InvocationID: mv.InvocationID, IntentID: st.IntentID,
		Completions: []engine.StepCompletion{{JobFence: engine.JobFenceRef{JobID: fence.JobID, RunnerID: string(fence.RunnerID), Generation: fence.Generation}}}}
	if st.NewResource != nil {
		commit.Resource = *st.NewResource
	}
	if err := m.d.Moves.CommitMove(ctx, commit); err != nil {
		err = fmt.Errorf("session move: commit: %w", err)
		if errors.Is(err, engine.ErrTurnFenced) || errors.Is(err, instance.ErrStaleSessionGeneration) {
			return MoveResult{}, errors.Join(err, m.failMove(ctx, st, fence, err))
		}
		return MoveResult{}, err // transient: the move stays open and resumes with the same ID
	}
	m.announce(ctx, req, mv, st.TargetSource)
	return MoveResult{To: RunnerID(mv.ToRunner), SessionID: sessionID, Lossy: lossy, Notice: notice,
		Generation: mv.Generation, PendingRebuild: rebuilt}, nil
}

// reserveTarget returns the move's RUNNING import job on its persisted target.
// After a crash the reserved slot is reused; a reaped or terminal job is
// replaced by the next attempt of the SAME operation on the SAME target; a
// slot held anywhere else is released, never leaked.
func (m *SessionMover) reserveTarget(ctx context.Context, st *SessionMoveState) (Job, error) {
	target := RunnerID(st.Move.ToRunner)
	job, err := m.d.Ledger.Get(ctx, st.JobID)
	if err != nil {
		return Job{}, fmt.Errorf("session move: load import job: %w", err)
	}
	if job.State.HoldsSlot() && job.RunnerID == target {
		if job.State == JobOffered {
			if err := m.d.Ledger.MarkRunning(ctx, job.Fence()); err != nil {
				return Job{}, fmt.Errorf("session move: mark import running: %w", err)
			}
			job.State = JobRunning
		}
		return job, nil
	}
	if job.State.HoldsSlot() {
		if err := m.d.Ledger.Fail(ctx, job.Fence(), OutcomeOther); err != nil && !errors.Is(err, ErrFenced) {
			return Job{}, fmt.Errorf("session move: release stray import slot: %w", err)
		}
		job.State = JobFailed
	}
	if !job.State.Active() {
		key := job.Key
		key.Attempt++
		if job, err = m.d.Ledger.EnsureIntent(ctx, key, st.Move.NodeID, st.Requirements); err != nil {
			return Job{}, fmt.Errorf("session move: next import attempt: %w", err)
		}
		st.JobID = job.ID
	}
	snap, err := m.d.Registry.Snapshot(ctx)
	if err != nil {
		return Job{}, fmt.Errorf("session move: pool snapshot: %w", err)
	}
	dec := Decision{Placed: true, Runner: target, Source: st.TargetSource, Reason: "session_" + st.Move.Reason}
	job, err = m.d.Ledger.Reserve(ctx, job.ID, dec, stepLimitOf(snap, target), m.d.Clock().Add(m.d.OpLease))
	if errors.Is(err, ErrSlotTaken) {
		return Job{}, &engine.SessionWaitError{Kind: "slot", Group: st.Requirements.Group.String(), Model: st.Requirements.Model}
	}
	if err != nil {
		return Job{}, fmt.Errorf("session move: reserve target %s: %w", target, err)
	}
	m.d.Status.Publish(ctx, job, instance.ExecutionState("recovering"))
	if err := m.d.Ledger.MarkRunning(ctx, job.Fence()); err != nil {
		return Job{}, fmt.Errorf("session move: mark import running: %w", err)
	}
	job.State = JobRunning
	return job, nil
}

// restoreUnderClaim restores the committed workspace only while the move
// holds its own exact resource claim (P3 handoff: the old owner's claim
// blocks it with ErrResourceBusy). The claim is recorded before the write.
func (m *SessionMover) restoreUnderClaim(ctx context.Context, st *SessionMoveState, req MoveRequest) error {
	key := moveResourceKey(req)
	holder := "session-move:" + st.Move.ID.String()
	if st.NewResource != nil && m.d.Resources.Check(ctx, *st.NewResource) != nil {
		st.NewResource = nil
	}
	if st.NewResource == nil {
		rec, held, err := m.d.Resources.Active(ctx, key)
		if err != nil {
			return err
		}
		if held && rec.Holder == holder {
			ref := rec.Fence // claimed just before a crash, not yet recorded
			st.NewResource = &ref
		} else {
			ref, err := m.d.Resources.Claim(ctx, key, holder, BuiltInRunnerID)
			if err != nil {
				return err
			}
			st.NewResource = &ref
		}
		if err := m.d.Moves.AdvanceMove(ctx, *st); err != nil {
			return err
		}
	}
	return m.restoreWorkspace(ctx, req, RunnerID(st.Move.ToRunner))
}

// releaseForRetry: a transient restore failure stays visible and retryable
// (the move remains open), but its slot and claim are released.
func (m *SessionMover) releaseForRetry(ctx context.Context, st *SessionMoveState, f Fence) error {
	var errs []error
	if _, err := m.d.Ledger.Requeue(ctx, f, OutcomeOther); err != nil && !errors.Is(err, ErrFenced) {
		errs = append(errs, fmt.Errorf("session move: requeue import: %w", err))
	}
	if st.NewResource != nil {
		err := m.d.Resources.Release(ctx, *st.NewResource)
		if err == nil || errors.Is(err, ErrResourceFenced) {
			st.NewResource = nil
		} else {
			errs = append(errs, fmt.Errorf("session move: release restore claim: %w", err))
		}
	}
	if st.RestoreWorkspace && st.NewResource == nil {
		st.State = MoveStateRestoring
	}
	if err := m.d.Moves.AdvanceMove(ctx, *st); err != nil {
		errs = append(errs, fmt.Errorf("session move: record retry: %w", err))
	}
	return errors.Join(errs...)
}

// failMove closes a move that can never commit: slot and claim released,
// row failed and an owned intent completed (the failure stays visible).
func (m *SessionMover) failMove(ctx context.Context, st SessionMoveState, f Fence, cause error) error {
	var errs []error
	if err := m.d.Ledger.Fail(ctx, f, OutcomeOther); err != nil && !errors.Is(err, ErrFenced) {
		errs = append(errs, fmt.Errorf("session move: release import slot: %w", err))
	}
	if st.NewResource != nil {
		if err := m.d.Resources.Release(ctx, *st.NewResource); err != nil && !errors.Is(err, ErrResourceFenced) {
			errs = append(errs, fmt.Errorf("session move: release restore claim: %w", err))
		}
	}
	if err := m.d.Moves.FailMove(ctx, st.Move.ID, cause.Error()); err != nil {
		errs = append(errs, fmt.Errorf("session move: record failure: %w", err))
	}
	return errors.Join(errs...)
}

// announce publishes the committed move once; PoolEvent.ID = move ID.
func (m *SessionMover) announce(ctx context.Context, req MoveRequest, mv instance.SessionMoveRecord, source SourceID) {
	runID := mv.RunID
	m.d.Events.Record(ctx, PoolEvent{
		ID: mv.ID, At: mv.At, Kind: engine.EventTypeSessionMoved, RunnerID: RunnerID(mv.ToRunner), SourceID: source, RunID: &runID,
		Cause:  Cause{Kind: causeKindFor(mv.Reason), Outcome: outcomeFor(mv.Reason)},
		Detail: map[string]any{"from": mv.FromRunner, "to": mv.ToRunner, "reason": mv.Reason, "lossy": mv.Lossy, "nodeId": mv.NodeID},
	})
	if mv.Lossy {
		engine.PublishContextNotice(m.d.Bus, engine.InteractiveTurnIdentity{RunID: mv.RunID, NodeID: mv.NodeID,
			InvocationID: mv.InvocationID, RequestID: req.RequestID, TurnIndex: req.TurnIndex, Generation: mv.Generation}, mv.Notice)
	}
}

// fenceOldOwner fails every still-active non-move job of this session's node
// (idempotent: an already-fenced job is skipped) and returns what it fenced.
func (m *SessionMover) fenceOldOwner(ctx context.Context, req MoveRequest) ([]engine.JobFenceRef, error) {
	jobs, err := m.d.Ledger.ListByRun(ctx, req.RunID)
	if err != nil {
		return nil, fmt.Errorf("session move: list run jobs: %w", err)
	}
	var fenced []engine.JobFenceRef
	for _, j := range jobs {
		if j.NodeID != req.NodeID || j.Key.InvocationID != req.InvocationID || j.Op == OpSessionImport || !jobActive(j.State) {
			continue
		}
		f := j.Fence()
		if err := m.d.Ledger.Fail(ctx, f, outcomeFor(req.Reason)); err != nil && !errors.Is(err, ErrFenced) {
			return nil, fmt.Errorf("session move: fence old owner job %s: %w", j.ID, err)
		}
		fenced = append(fenced, engine.JobFenceRef{JobID: f.JobID, RunnerID: string(f.RunnerID), Generation: f.Generation})
	}
	return fenced, nil
}

func mergeJobFences(have, add []engine.JobFenceRef) []engine.JobFenceRef {
	seen := make(map[uuid.UUID]bool, len(have))
	for _, f := range have {
		seen[f.JobID] = true
	}
	for _, f := range add {
		if !seen[f.JobID] {
			have, seen[f.JobID] = append(have, f), true
		}
	}
	return have
}

// moveRequestFrom rebuilds the request of a persisted move. The binding is
// the accepted snapshot of the intent, never the current profile.
func moveRequestFrom(st SessionMoveState, in engine.TurnIntent) MoveRequest {
	return MoveRequest{RunID: st.Move.RunID, InvocationID: st.Move.InvocationID, NodeID: st.Move.NodeID, Reason: st.Move.Reason,
		From: RunnerID(st.Move.FromRunner), Requirements: st.Requirements, ExecutionID: st.ExecutionID, TurnIndex: in.TurnIndex,
		Binding: bindingFromAccepted(in.Binding), WorkDir: st.WorkDir, ResourceID: st.ResourceID,
		RestoreWorkspace: st.RestoreWorkspace, RequestID: in.RequestID}
}

// bindingFromAccepted is a KEYLESS binding: session ops never call a provider
// (import selects by Backend; rebuild sizes by ContextWindow). Turns restore
// keys through P4 T3's TurnBindingRestorer.
func bindingFromAccepted(a engine.AcceptedTurnBinding) provider.ResolvedBinding {
	return provider.ResolvedBinding{Kind: a.Kind, Provider: a.Provider, Model: a.Model, Backend: a.Backend,
		ConnectionRef: a.ConnectionRef, BackendExplicit: a.BackendExplicit, AllowedTools: append([]string(nil), a.AllowedTools...),
		ContextWindow: a.ContextWindow, Effort: a.Effort, Pinned: a.Pinned, OpaquePinRoute: a.OpaquePinRoute}
}

// moveResourceKey: the run workspace, or the resolution resource (P4 T11
// replaces this with the MoveRequest.Resource descriptor's key).
func moveResourceKey(req MoveRequest) string {
	if req.ResourceID == uuid.Nil || req.ResourceID == req.RunID {
		return runKey(req.RunID)
	}
	return engine.ResolutionResourceKey(req.ResourceID)
}

// ErrMoveTargetGone (decision unstick 2026-11-24): the persisted target of an
// open move is lost, offline or unregistered. The move is failed, never
// retried on that target; a NEW move replaces it.
var ErrMoveTargetGone = errors.New("runner: session move target is gone")

// moveTargetGoneError carries the lease at the failed move's reserved
// generation, so the replacement plans past it.
type moveTargetGoneError struct {
	move   uuid.UUID
	target RunnerID
	why    string
	lease  engine.TurnLease
}

func (e *moveTargetGoneError) Error() string {
	return fmt.Sprintf("%v: session move %s: target %s is %s", ErrMoveTargetGone, e.move, e.target, e.why)
}

func (e *moveTargetGoneError) Is(target error) bool { return target == ErrMoveTargetGone }

// targetGone reports why a persisted target can no longer finish a move, or
// "". Suspect is inside the liveness grace and is NOT gone. Revocation marks
// the runner lost (Hub.RevokeRunner -> markLost). The built-in runner is never gone.
func (m *SessionMover) targetGone(ctx context.Context, target RunnerID) (string, error) {
	if target == BuiltInRunnerID {
		return "", nil
	}
	snap, err := m.d.Registry.Snapshot(ctx)
	if err != nil {
		return "", fmt.Errorf("session move: pool snapshot: %w", err)
	}
	for _, r := range snap.Runners {
		if r.ID != target {
			continue
		}
		switch r.Liveness {
		case LivenessLost:
			return "lost", nil
		case LivenessOffline:
			return "offline", nil
		}
		return "", nil
	}
	return "unregistered", nil
}

// failGoneTarget fails a move whose target is gone. It releases the import
// slot (or abandons a queued import intent), the move's own resource claim,
// and records the failure; FailMove closes an owned move intent.
func (m *SessionMover) failGoneTarget(ctx context.Context, st SessionMoveState, lease engine.TurnLease, why string) error {
	gone := &moveTargetGoneError{move: st.Move.ID, target: RunnerID(st.Move.ToRunner), why: why, lease: lease}
	errs := []error{gone}
	job, err := m.d.Ledger.Get(ctx, st.JobID)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("session move: load import job: %w", err))
	case job.State.HoldsSlot():
		if err := m.d.Ledger.Fail(ctx, job.Fence(), OutcomeRunnerLost); err != nil && !errors.Is(err, ErrFenced) {
			errs = append(errs, fmt.Errorf("session move: release import slot: %w", err))
		}
	case job.State == JobQueued:
		if a, ok := m.d.Ledger.(IntentAbandoner); ok {
			if err := a.Abandon(ctx, job.ID); err != nil {
				errs = append(errs, fmt.Errorf("session move: abandon import intent: %w", err))
			}
		}
	}
	if st.NewResource != nil {
		if err := m.d.Resources.Release(ctx, *st.NewResource); err != nil && !errors.Is(err, ErrResourceFenced) {
			errs = append(errs, fmt.Errorf("session move: release restore claim: %w", err))
		}
	}
	if err := m.d.Moves.FailMove(ctx, st.Move.ID, gone.Error()); err != nil {
		errs = append(errs, fmt.Errorf("session move: record failure: %w", err))
	}
	return errors.Join(errs...)
}
