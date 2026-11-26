package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Gather report vocabulary. instance.GatherReport (P3-T4) owns these strings
// (and GatherReport.Validate checks them); the short local names alias them so
// the state machine and its persisted vocabulary cannot drift.
const (
	gatherStatePending       = instance.GatherStatePending
	gatherStateMerging       = instance.GatherStateMerging
	gatherStateResolving     = instance.GatherStateResolving
	gatherStateApplied       = instance.GatherStateApplied
	gatherStateFailed        = instance.GatherStateFailed
	gatherStateRolledBack    = instance.GatherStateRolledBack
	gatherStateNotApplicable = instance.GatherStateNotApplicable

	rollbackStatusRolledBack = instance.RollbackStatusRolledBack
	rollbackStatusFailed     = instance.RollbackStatusFailed

	// rollbackFailedSep joins the original abort reason and the rollback error
	// in GatherReport.Error, so a retried rollback recovers the reason.
	rollbackFailedSep = "; rollback failed: "
)

// Resolution attempt outcomes (instance.ResolutionAttempt.Outcome), re-exported
// from domain/instance (P3-T4) for engine callers (P3-T8, P3-T9, P3-T10).
const (
	ResolutionOutcomeResolved        = instance.ResolutionOutcomeResolved
	ResolutionOutcomeStillConflicted = instance.ResolutionOutcomeStillConflicted
	ResolutionOutcomeAbandoned       = instance.ResolutionOutcomeAbandoned
	ResolutionOutcomeCancelled       = instance.ResolutionOutcomeCancelled
	ResolutionOutcomeError           = instance.ResolutionOutcomeError
)

// ErrGroupGatherRollbackFailed means a gather was aborted and the parent's
// working area could NOT be restored. The run stays in `gathering` with
// GatherReport.RollbackStatus "rollback_failed": continuation is blocked, and
// the next re-entry (e.g. an operator Retry after repairing the area) retries
// the rollback before routing failure.
var ErrGroupGatherRollbackFailed = errors.New("parallel_group gather could not restore the parent's working area")

// ResolutionRequest is everything a resolution session needs about ONE
// conflicted merge: the staged gather (the agent works in Handle.StagingDir,
// never in a task's directory), the merge being resolved and its paths.
type ResolutionRequest struct {
	Handle        GatherHandle
	MergeIndex    int
	Attempt       int
	TaskKey       string
	TaskRef       string
	ConflictPaths []string
}

// GroupResolutionStarter opens a conflict-resolution session for a merge and
// returns its durable identity. All operations must be idempotent for a session
// identity so interrupted effects can be retried. Declared here by its consumer; P3-T8's
// GroupResolutionBridge implements it.
type GroupResolutionStarter interface {
	StartResolution(ctx context.Context, run *instance.GroupRun, parent *instance.Instance, req ResolutionRequest) (*instance.ResolutionSessionRef, error)
	CloseSession(ctx context.Context, ref instance.ResolutionSessionRef, reason string) error
	NotifyResolutionRequired(ctx context.Context, ref instance.ResolutionSessionRef) error
}

// FileGatherer implements GroupGatherer for gatherMode results_and_files.
//
// It is the single writer of the parent's working area for a group: it runs
// only inside the parent's executor (and, for a cancelled parent, from the
// reconciler via AbortGather), merges ONLY the eligible tasks' finalized refs
// — every task in author order for an all_succeed success, the winner alone
// for a first_success success, nothing for a failed decision — and persists a
// GatherReport after every step, so a crash at any boundary resumes from the
// persisted merge index without applying a delta twice.
type FileGatherer struct {
	groups   instance.GroupRunRepository
	area     WorkingArea
	bridge   GroupResolutionStarter
	bus      EventBus
	now      func() time.Time
	resource WorkspaceResource
}

var _ GroupGatherer = (*FileGatherer)(nil)

// NewGroupGatherer constructs the file gatherer. bridge may be nil only in
// configurations that never meet a conflict: a conflict with no bridge aborts
// the gather (rollback + failure route) rather than hanging the parent.
func NewGroupGatherer(groups instance.GroupRunRepository, area WorkingArea, bridge GroupResolutionStarter, bus EventBus) *FileGatherer {
	return &FileGatherer{groups: groups, area: area, bridge: bridge, bus: bus, now: time.Now}
}

// gatherPass is one invocation's working copy of the run and its report.
type gatherPass struct {
	run    instance.GroupRun
	parent *instance.Instance
	report instance.GatherReport
	handle *GatherHandle
	claim  *ResourceFenceRef
}

func (p *gatherPass) loadReport() {
	if p.run.Gather != nil {
		p.report = copyGatherReport(*p.run.Gather)
	} else {
		p.report = newGatherReport(&p.run)
	}
	p.handle = nil
	if h, ok := GatherHandleFromReport(&p.report); ok {
		p.handle = &h
	}
}

// Gather advances the run's file gather as far as it can and reports whether
// the group is done (Port/Output set) or must wait for a resolution session.
func (g *FileGatherer) Gather(ctx context.Context, run *instance.GroupRun, parent *instance.Instance) (*GatherStep, error) {
	if run == nil || parent == nil {
		return nil, errors.New("group gather: run and parent instance are required")
	}
	if run.GatherMode != workflow.GroupGatherResultsAndFiles {
		return nil, fmt.Errorf("group gather: run %s gathers %q; only %q gathers files",
			run.ID, run.GatherMode, workflow.GroupGatherResultsAndFiles)
	}
	if run.State == instance.GroupRunDone {
		return replayGatherStep(run), nil
	}
	if run.Decision == nil {
		return nil, fmt.Errorf("group gather: run %s has no decision (state %s)", run.ID, run.State)
	}
	p := &gatherPass{run: *run, parent: parent}
	switch run.State {
	case instance.GroupRunDecided:
		ok, err := g.groups.TransitionState(ctx, run.ID, instance.GroupRunDecided, instance.GroupRunGathering)
		if err != nil {
			return nil, fmt.Errorf("group gather: run %s: enter gathering: %w", run.ID, err)
		}
		if !ok {
			return g.reload(ctx, run.ID)
		}
		p.run.State = instance.GroupRunGathering
		p.report = newGatherReport(&p.run)
		return g.start(ctx, p)
	case instance.GroupRunGathering:
		p.loadReport()
		return g.resume(ctx, p)
	case instance.GroupRunResolvingConflicts:
		if run.Gather == nil {
			return nil, fmt.Errorf("group gather: run %s is resolving conflicts without a gather report", run.ID)
		}
		p.loadReport()
		return g.reenterResolution(ctx, p)
	default:
		return nil, fmt.Errorf("group gather: run %s is %s; it has not been decided yet", run.ID, run.State)
	}
}

// AbortGather aborts a gather from OUTSIDE the parent's run — the reconciler
// for a cancelled parent, or a lost staging area. Any open resolution attempt
// is closed with outcome, everything staged is rolled back, and the run
// settles on the failure route with reason. A run that is already done
// replays its stored result; a decided run settles without staging anything.
func (g *FileGatherer) AbortGather(ctx context.Context, run *instance.GroupRun, outcome, reason string) (*GatherStep, error) {
	if run == nil {
		return nil, errors.New("group gather: run is required")
	}
	if run.State == instance.GroupRunDone {
		return replayGatherStep(run), nil
	}
	if run.GatherMode != workflow.GroupGatherResultsAndFiles {
		return nil, fmt.Errorf("group gather: run %s gathers %q; there is no file gather to abort", run.ID, run.GatherMode)
	}
	if run.Decision == nil {
		return nil, fmt.Errorf("group gather: run %s has no decision (state %s)", run.ID, run.State)
	}
	p := &gatherPass{run: *run}
	switch run.State {
	case instance.GroupRunDecided:
		ok, err := g.groups.TransitionState(ctx, run.ID, instance.GroupRunDecided, instance.GroupRunGathering)
		if err != nil {
			return nil, fmt.Errorf("group gather: run %s: enter gathering: %w", run.ID, err)
		}
		if !ok {
			return g.reload(ctx, run.ID)
		}
		p.run.State = instance.GroupRunGathering
		p.report = newGatherReport(&p.run)
	case instance.GroupRunGathering, instance.GroupRunResolvingConflicts:
		p.loadReport()
	default:
		return nil, fmt.Errorf("group gather: run %s is %s; there is no gather to abort", run.ID, run.State)
	}
	return g.abort(ctx, p, reason, outcome)
}

// reload handles a lost state CAS. The executor is the single writer of a
// gathering run, so the only legitimate concurrent move is to done (replay).
func (g *FileGatherer) reload(ctx context.Context, id uuid.UUID) (*GatherStep, error) {
	fresh, err := g.groups.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("group gather: reload run %s: %w", id, err)
	}
	if fresh.State != instance.GroupRunDone {
		return nil, fmt.Errorf("group gather: run %s changed state concurrently (now %s)", id, fresh.State)
	}
	return replayGatherStep(fresh), nil
}

// start decides what is eligible and opens the staged gather.
func (g *FileGatherer) start(ctx context.Context, p *gatherPass) (*GatherStep, error) {
	run := &p.run
	if run.Decision.Outcome != workflow.GroupPortSuccess {
		for i := range p.report.Tasks {
			p.report.Tasks[i].Merged = false
			p.report.Tasks[i].Reason = "group failed: no tasks merged"
		}
		p.report.State = gatherStateNotApplicable
		if err := g.persist(ctx, p); err != nil {
			return nil, err
		}
		return g.finish(ctx, p, workflow.GroupPortFailure)
	}

	eligible, problem := eligibleGatherTasks(run)
	markIneligible(p, eligible)
	if problem != "" {
		return g.abort(ctx, p, problem, "")
	}
	parentDir := parentWorkspace(p.parent)
	if parentDir == "" {
		return g.abort(ctx, p, "parent instance has no working area to gather into", "")
	}
	if strings.TrimSpace(run.WorkspaceSnapshot) == "" {
		return g.abort(ctx, p, "group run has no working-area snapshot to compare task changes against", "")
	}

	if g.resource != nil {
		claim, err := g.resource.Acquire(ctx, p.parent.ID, parentDir, "group gather")
		if err != nil {
			return g.abort(ctx, p, fmt.Sprintf("acquire the parent working area: %v", err), "")
		}
		p.claim = &claim
		defer releaseWorkspaceResource(ctx, g.resource, claim)
	}
	p.report.State = gatherStateMerging
	p.report.Error = ""
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	g.publish(run, EventTypeGroupGathering, map[string]any{"state": gatherStateMerging, "eligible": len(eligible)})

	h, err := g.area.BeginGather(ctx, run.WorkspaceSnapshot, uuid.New(), parentDir)
	if err != nil {
		return g.abort(ctx, p, fmt.Sprintf("begin gather: %v", err), "")
	}
	p.handle = &h
	p.report.Handle = gatherHandleMap(h)
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	return g.mergeLoop(ctx, p, eligible)
}

// resume continues a run left in `gathering` by a crash or an earlier step.
func (g *FileGatherer) resume(ctx context.Context, p *gatherPass) (*GatherStep, error) {
	switch {
	case p.report.RollbackStatus == rollbackStatusFailed:
		reason, _, _ := strings.Cut(p.report.Error, rollbackFailedSep)
		return g.abort(ctx, p, reason, "")
	case p.report.State == gatherStateFailed && (p.report.ActiveSession != nil || (p.handle != nil && p.report.RollbackStatus != rollbackStatusRolledBack)):
		// Abort intent was saved before session cleanup or rollback completed.
		// A missing gather handle must not bypass outstanding session cleanup.
		return g.abort(ctx, p, p.report.Error, "")
	case p.report.State == gatherStateApplied:
		return g.finish(ctx, p, workflow.GroupPortSuccess)
	case p.report.State == gatherStateNotApplicable,
		p.report.State == gatherStateFailed,
		p.report.State == gatherStateRolledBack:
		return g.finish(ctx, p, workflow.GroupPortFailure)
	case p.report.State == gatherStateResolving:
		// Crash between recording a conflict and entering resolving_conflicts.
		ok, err := g.groups.TransitionState(ctx, p.run.ID, instance.GroupRunGathering, instance.GroupRunResolvingConflicts)
		if err != nil {
			return nil, fmt.Errorf("group gather: run %s: enter resolving_conflicts: %w", p.run.ID, err)
		}
		if !ok {
			return g.reload(ctx, p.run.ID)
		}
		p.run.State = instance.GroupRunResolvingConflicts
		return g.reenterResolution(ctx, p)
	case p.handle == nil:
		// Nothing was staged durably yet: stage afresh. The parent's area is
		// untouched before Apply, so a fresh BeginGather is always safe.
		return g.start(ctx, p)
	default:
		eligible, problem := eligibleGatherTasks(&p.run)
		if problem != "" {
			return g.abort(ctx, p, problem, "")
		}
		return g.mergeLoop(ctx, p, eligible)
	}
}

// mergeLoop merges eligible tasks from the persisted merge index onward.
func (g *FileGatherer) mergeLoop(ctx context.Context, p *gatherPass, eligible []instance.GroupTask) (*GatherStep, error) {
	for p.report.MergeIndex < len(eligible) {
		task := eligible[p.report.MergeIndex]
		outcome, err := g.area.MergeTask(ctx, *p.handle, task.FinalizedRef)
		if err != nil {
			return g.abort(ctx, p, fmt.Sprintf("merge task %s: %v", task.Key, err), "")
		}
		if !outcome.Clean {
			paths := outcome.ConflictPaths
			if len(paths) == 0 {
				if paths, err = g.area.ConflictPaths(ctx, *p.handle); err != nil {
					return g.abort(ctx, p, fmt.Sprintf("list conflicts of task %s: %v", task.Key, err), "")
				}
			}
			if len(paths) == 0 {
				return g.abort(ctx, p, fmt.Sprintf("merge of task %s reported a conflict but no conflicted paths", task.Key), "")
			}
			return g.openResolution(ctx, p, task, paths)
		}
		if ti := taskReportIndex(&p.report, task.Key); ti >= 0 {
			p.report.Tasks[ti].Merged = true
			p.report.Tasks[ti].Reason = "merged cleanly"
			p.report.Tasks[ti].ConflictPaths = nil
		}
		p.report.MergeIndex++
		if err := g.persist(ctx, p); err != nil {
			return nil, err
		}
	}
	if err := g.area.Apply(ctx, *p.handle); err != nil {
		return g.abort(ctx, p, fmt.Sprintf("apply gathered changes to the parent working area: %v", err), "")
	}
	if g.resource != nil {
		if p.claim == nil {
			return g.abort(ctx, p, "commit the gathered working area: missing resource claim", "")
		}
		if err := g.resource.Commit(ctx, *p.claim, p.parent.ID, p.handle.ParentDir); err != nil {
			return g.abort(ctx, p, fmt.Sprintf("commit the gathered working area: %v", err), "")
		}
	}
	p.report.State = gatherStateApplied
	p.report.Error = ""
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	return g.finish(ctx, p, workflow.GroupPortSuccess)
}

// openResolution records a new attempt for the current merge, moves the run
// to resolving_conflicts and starts the session.
func (g *FileGatherer) openResolution(ctx context.Context, p *gatherPass, task instance.GroupTask, paths []string) (*GatherStep, error) {
	attemptNo := nextAttemptNumber(&p.report, p.report.MergeIndex)
	if ti := taskReportIndex(&p.report, task.Key); ti >= 0 {
		p.report.Tasks[ti].ConflictPaths = append([]string(nil), paths...)
	}
	p.report.State = gatherStateResolving
	p.report.ActiveSession = nil
	p.report.Attempts = append(p.report.Attempts, instance.ResolutionAttempt{
		MergeIndex:    p.report.MergeIndex,
		Attempt:       attemptNo,
		TaskKey:       task.Key,
		ConflictPaths: append([]string(nil), paths...),
		StartedAt:     g.now().UTC(),
	})
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	if p.run.State != instance.GroupRunResolvingConflicts {
		ok, err := g.groups.TransitionState(ctx, p.run.ID, instance.GroupRunGathering, instance.GroupRunResolvingConflicts)
		if err != nil {
			return nil, fmt.Errorf("group gather: run %s: enter resolving_conflicts: %w", p.run.ID, err)
		}
		if !ok {
			return nil, fmt.Errorf("group gather: run %s left gathering concurrently", p.run.ID)
		}
		p.run.State = instance.GroupRunResolvingConflicts
	}
	return g.startSession(ctx, p, task, len(p.report.Attempts)-1)
}

// startSession asks the bridge to open the session for attempt idx and
// persists its identity. A failure to open aborts the gather.
func (g *FileGatherer) startSession(ctx context.Context, p *gatherPass, task instance.GroupTask, idx int) (*GatherStep, error) {
	att := p.report.Attempts[idx]
	if g.bridge == nil {
		g.endAttempt(p, idx, ResolutionOutcomeError)
		return g.abort(ctx, p, fmt.Sprintf("task %s has conflicts but no conflict-resolution bridge is configured", task.Key), "")
	}
	ref, err := g.bridge.StartResolution(ctx, &p.run, p.parent, ResolutionRequest{
		Handle:        *p.handle,
		MergeIndex:    att.MergeIndex,
		Attempt:       att.Attempt,
		TaskKey:       task.Key,
		TaskRef:       task.FinalizedRef,
		ConflictPaths: append([]string(nil), att.ConflictPaths...),
	})
	if err != nil {
		outcome := ResolutionOutcomeError
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			outcome = ResolutionOutcomeCancelled
		}
		g.endAttempt(p, idx, outcome)
		return g.abort(ctx, p, fmt.Sprintf("start conflict resolution for task %s: %v", task.Key, err), "")
	}
	if ref == nil {
		g.endAttempt(p, idx, ResolutionOutcomeError)
		return g.abort(ctx, p, fmt.Sprintf("start conflict resolution for task %s: no session was opened", task.Key), "")
	}
	active := *ref
	p.report.ActiveSession = &active
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	return g.advanceOpenSession(ctx, p, task, idx)
}

// advanceOpenSession also runs on replay: a crash after closing the session
// but before persisting the attempt end must not strand a verified resolution.
// CloseSession and NotifyResolutionRequired are idempotent for the full ref;
// their durable session state owns deduplication of those external effects.
func (g *FileGatherer) advanceOpenSession(ctx context.Context, p *gatherPass, task instance.GroupTask, idx int) (*GatherStep, error) {
	paths, err := g.area.ConflictPaths(ctx, *p.handle)
	if err != nil {
		if errors.Is(err, ErrPendingMergeChanged) {
			return g.abort(ctx, p, fmt.Sprintf("verify automatic resolution for task %s: %v", task.Key, err), ResolutionOutcomeError)
		}
		return nil, fmt.Errorf("verify automatic resolution for task %s: %w", task.Key, err)
	}
	if g.bridge == nil {
		return g.abort(ctx, p, "no conflict-resolution bridge is configured", ResolutionOutcomeError)
	}
	active := *p.report.ActiveSession
	if len(paths) > 0 {
		if err := g.bridge.NotifyResolutionRequired(ctx, active); err != nil {
			return nil, fmt.Errorf("notify unresolved gather: %w", err)
		}
		return &GatherStep{Done: false}, nil
	}
	if err := g.bridge.CloseSession(ctx, active, "completed"); err != nil {
		return nil, fmt.Errorf("close automatically resolved session: %w", err)
	}
	g.endAttempt(p, idx, ResolutionOutcomeResolved)
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	return g.reenterResolution(ctx, p)
}

func (g *FileGatherer) endAttempt(p *gatherPass, idx int, outcome string) {
	now := g.now().UTC()
	p.report.Attempts[idx].EndedAt = &now
	p.report.Attempts[idx].Outcome = outcome
}

// reenterResolution runs when the parent re-enters the group node while the
// run is resolving_conflicts: it verifies the latest attempt of the current
// merge and either keeps waiting, reopens a fresh attempt, aborts, or commits
// the resolution and continues merging.
func (g *FileGatherer) reenterResolution(ctx context.Context, p *gatherPass) (*GatherStep, error) {
	// The abort report may be durable before the state transition succeeds.
	// Recover it before looking at attempts: never replace the abort reason
	// or resume merging a tree that has already been rolled back.
	if p.report.State == gatherStateFailed || p.report.State == gatherStateRolledBack {
		if err := g.enterGathering(ctx, p); err != nil {
			return nil, err
		}
		return g.resume(ctx, p)
	}
	if p.handle == nil {
		return g.abort(ctx, p, "gather handle is missing while resolving conflicts; the staged merge cannot be verified", "")
	}
	eligible, problem := eligibleGatherTasks(&p.run)
	if problem != "" {
		return g.abort(ctx, p, problem, "")
	}
	if p.report.State == gatherStateMerging {
		// Crash after a resolved merge was recorded, before leaving resolving_conflicts.
		if err := g.enterGathering(ctx, p); err != nil {
			return nil, err
		}
		return g.mergeLoop(ctx, p, eligible)
	}
	if p.report.MergeIndex >= len(eligible) {
		return g.abort(ctx, p, fmt.Sprintf("merge index %d is past the %d eligible tasks", p.report.MergeIndex, len(eligible)), "")
	}
	task := eligible[p.report.MergeIndex]
	ti := taskReportIndex(&p.report, task.Key)
	idx := latestAttemptIndex(&p.report, p.report.MergeIndex)
	if idx < 0 {
		var paths []string
		if ti >= 0 {
			paths = p.report.Tasks[ti].ConflictPaths
		}
		if len(paths) == 0 {
			return g.abort(ctx, p, fmt.Sprintf("task %s is resolving conflicts but no conflicted paths were recorded", task.Key), "")
		}
		return g.openResolution(ctx, p, task, paths)
	}
	att := p.report.Attempts[idx]
	if att.EndedAt == nil {
		if p.report.ActiveSession == nil {
			// Crash between recording the attempt and opening its session.
			return g.startSession(ctx, p, task, idx)
		}
		return g.advanceOpenSession(ctx, p, task, idx)
	}
	switch att.Outcome {
	case ResolutionOutcomeAbandoned:
		return g.abort(ctx, p, fmt.Sprintf("conflict resolution for task %s was abandoned (attempt %d)", task.Key, att.Attempt), "")
	case ResolutionOutcomeCancelled:
		return g.abort(ctx, p, fmt.Sprintf("conflict resolution for task %s was cancelled (attempt %d)", task.Key, att.Attempt), "")
	case ResolutionOutcomeError:
		return g.abort(ctx, p, fmt.Sprintf("conflict resolution for task %s failed (attempt %d)", task.Key, att.Attempt), "")
	case ResolutionOutcomeStillConflicted:
		// Crash after recording still_conflicted, before the fresh attempt.
		paths := att.ConflictPaths
		if ti >= 0 && len(p.report.Tasks[ti].ConflictPaths) > 0 {
			paths = p.report.Tasks[ti].ConflictPaths
		}
		return g.openResolution(ctx, p, task, paths)
	}

	// The session ended normally ("" or a crash after "resolved"): verify.
	paths, err := g.area.ConflictPaths(ctx, *p.handle)
	if err != nil {
		if errors.Is(err, ErrPendingMergeChanged) {
			return g.abort(ctx, p, fmt.Sprintf("verify conflict resolution for task %s: %v", task.Key, err), ResolutionOutcomeError)
		}
		return nil, fmt.Errorf("verify conflict resolution for task %s: %w", task.Key, err)
	}
	if len(paths) > 0 {
		p.report.Attempts[idx].Outcome = ResolutionOutcomeStillConflicted
		p.report.ActiveSession = nil
		if ti >= 0 {
			p.report.Tasks[ti].ConflictPaths = append([]string(nil), paths...)
		}
		if err := g.persist(ctx, p); err != nil {
			return nil, err
		}
		return g.openResolution(ctx, p, task, paths)
	}
	p.report.Attempts[idx].Outcome = ResolutionOutcomeResolved
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	msg := fmt.Sprintf("arbitron: resolve conflicts of task %s (merge %d, attempt %d)", task.Key, att.MergeIndex, att.Attempt)
	if err := g.area.CommitResolution(ctx, *p.handle, msg); err != nil {
		return g.abort(ctx, p, fmt.Sprintf("record conflict resolution for task %s: %v", task.Key, err), "")
	}
	if ti >= 0 {
		p.report.Tasks[ti].Merged = true
		p.report.Tasks[ti].Reason = fmt.Sprintf("merged after conflict resolution (attempt %d)", att.Attempt)
	}
	p.report.MergeIndex++
	p.report.ActiveSession = nil
	p.report.State = gatherStateMerging
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	g.publish(&p.run, EventTypeGroupResolved, map[string]any{"taskKey": task.Key, "mergeIndex": att.MergeIndex, "attempt": att.Attempt})
	if err := g.enterGathering(ctx, p); err != nil {
		return nil, err
	}
	return g.mergeLoop(ctx, p, eligible)
}

// abort closes every open attempt, rolls the parent's area back to its exact
// pre-gather content, and settles the failure route. The decision is never
// touched. On rollback failure the run stays in gathering (continuation
// blocked) and ErrGroupGatherRollbackFailed is returned.
func (g *FileGatherer) abort(ctx context.Context, p *gatherPass, reason, openOutcome string) (*GatherStep, error) {
	// An abort must complete even while the run context is being torn down
	// (parent cancelled mid-merge): a rollback on a cancelled context would
	// fail every working-area call and strand the parent's area half-written.
	ctx = context.WithoutCancel(ctx)
	if openOutcome == "" {
		openOutcome = ResolutionOutcomeError
	}
	now := g.now().UTC()
	for i := range p.report.Attempts {
		a := &p.report.Attempts[i]
		if a.EndedAt == nil {
			ended := now
			a.EndedAt = &ended
			a.Outcome = openOutcome
		} else if a.Outcome == "" {
			a.Outcome = openOutcome
		}
	}
	p.report.Error = reason
	for i := range p.report.Tasks {
		t := &p.report.Tasks[i]
		switch {
		case t.Merged:
			t.Merged = false
			t.Reason = "rolled back: " + reason
		case t.Reason == "":
			t.Reason = "not merged: " + reason
		}
	}
	// Save abort intent before rollback: losing the later result write must
	// never let a restart re-apply the staged tree and publish success.
	p.report.State = gatherStateFailed
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	// Keep the active identity in the durable abort report until its session
	// record is closed. Either write may fail; replay must retry cleanup rather
	// than resume merging or freeze an output that strands an open session.
	if active := p.report.ActiveSession; active != nil {
		if g.bridge == nil {
			return nil, errors.New("group gather: cannot close aborted resolution session: no bridge is configured")
		}
		closeReason := openOutcome
		if att := findAttempt(&p.report, active.MergeIndex, active.Attempt); att != nil {
			switch att.Outcome {
			case ResolutionOutcomeCancelled, ResolutionOutcomeAbandoned:
				closeReason = att.Outcome
			}
		}
		if err := g.bridge.CloseSession(ctx, *active, closeReason); err != nil {
			return nil, fmt.Errorf("group gather: close aborted resolution session: %w", err)
		}
		p.report.ActiveSession = nil
		if err := g.persist(ctx, p); err != nil {
			return nil, err
		}
	}
	if p.handle != nil {
		if err := g.area.Rollback(ctx, *p.handle); err != nil {
			p.report.RollbackStatus = rollbackStatusFailed
			p.report.State = gatherStateFailed
			p.report.Error = reason + rollbackFailedSep + err.Error()
			failure := fmt.Errorf("%w: run %s: %v (gather was aborted because: %s)", ErrGroupGatherRollbackFailed, p.run.ID, err, reason)
			if perr := g.persist(ctx, p); perr != nil {
				return nil, errors.Join(failure, perr)
			}
			if err := g.enterGathering(ctx, p); err != nil {
				return nil, errors.Join(failure, err)
			}
			// No error text: the frame is authorized on the parent alone, and
			// the diagnostic may name a hidden task's paths. The client reads
			// it from the redacted graph-state panel on refetch.
			g.publish(&p.run, EventTypeGroupGathering, map[string]any{
				"state": gatherStateFailed, "rollbackStatus": rollbackStatusFailed,
			})
			return nil, failure
		}
		p.report.RollbackStatus = rollbackStatusRolledBack
		p.report.State = gatherStateRolledBack
	} else {
		p.report.State = gatherStateFailed
	}
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	if err := g.enterGathering(ctx, p); err != nil {
		return nil, err
	}
	return g.finish(ctx, p, workflow.GroupPortFailure)
}

// enterGathering moves a resolving_conflicts run back to gathering.
func (g *FileGatherer) enterGathering(ctx context.Context, p *gatherPass) error {
	if p.run.State != instance.GroupRunResolvingConflicts {
		return nil
	}
	ok, err := g.groups.TransitionState(ctx, p.run.ID, instance.GroupRunResolvingConflicts, instance.GroupRunGathering)
	if err != nil {
		return fmt.Errorf("group gather: run %s: leave resolving_conflicts: %w", p.run.ID, err)
	}
	if !ok {
		return fmt.Errorf("group gather: run %s left resolving_conflicts concurrently", p.run.ID)
	}
	p.run.State = instance.GroupRunGathering
	return nil
}

// finish freezes the final output — BuildGroupOutput with "gather" replaced
// by the report — and moves the run gathering -> done (continuation ready).
func (g *FileGatherer) finish(ctx context.Context, p *gatherPass, port string) (*GatherStep, error) {
	report := copyGatherReport(p.report)
	p.run.Gather = &report
	out := BuildGroupOutput(&p.run)
	out["taskOutcome"] = p.run.Decision.Outcome
	out["outcome"] = port
	out["gather"] = gatherReportMap(p.report)
	ok, err := g.groups.SetFinalOutput(ctx, p.run.ID, port, out, instance.GroupRunGathering)
	if err != nil {
		return nil, fmt.Errorf("group gather: run %s: record final output: %w", p.run.ID, err)
	}
	if !ok {
		return g.reload(ctx, p.run.ID)
	}
	g.publish(&p.run, EventTypeGroupCompleted, map[string]any{"port": port, "gatherState": p.report.State})
	return &GatherStep{Done: true, Port: port, Output: out}, nil
}

// persist writes the pass's report only over the one this pass last loaded or
// wrote (p.run.Gather): an attempt end recorded meanwhile by
// OnResolutionSessionEnded, which runs outside the gather gate, fails the
// pass with ErrGatherReportStale instead of being erased, and the next pass
// acts on it.
func (g *FileGatherer) persist(ctx context.Context, p *gatherPass) error {
	report := copyGatherReport(p.report)
	if err := g.groups.ReplaceGatherReport(ctx, p.run.ID, p.run.Gather, report); err != nil {
		return fmt.Errorf("group gather: run %s: persist gather report: %w", p.run.ID, err)
	}
	stored := copyGatherReport(p.report)
	p.run.Gather = &stored
	return nil
}

func (g *FileGatherer) publish(run *instance.GroupRun, eventType string, extra map[string]any) {
	if g.bus == nil {
		return
	}
	data := map[string]any{"groupRunId": run.ID.String(), "nodeId": run.NodeID}
	for k, v := range extra {
		data[k] = v
	}
	g.bus.Publish(run.ParentInstanceID, Event{Type: eventType, Timestamp: g.now(), Data: data})
}

// --- pure helpers -----------------------------------------------------------

func replayGatherStep(run *instance.GroupRun) *GatherStep {
	return &GatherStep{Done: true, Port: run.ContinuationPort, Output: variable.DeepCopyMap(run.FinalOutput)}
}

func newGatherReport(run *instance.GroupRun) instance.GatherReport {
	tasks := orderedGroupTasks(run)
	r := instance.GatherReport{
		Mode:     workflow.GroupGatherResultsAndFiles,
		State:    gatherStatePending,
		Tasks:    make([]instance.GatherTaskReport, 0, len(tasks)),
		Attempts: []instance.ResolutionAttempt{},
	}
	for _, t := range tasks {
		r.Tasks = append(r.Tasks, instance.GatherTaskReport{Key: t.Key})
	}
	return r
}

func orderedGroupTasks(run *instance.GroupRun) []instance.GroupTask {
	tasks := append([]instance.GroupTask(nil), run.Tasks...)
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].Position < tasks[j].Position })
	return tasks
}

// eligibleGatherTasks returns the tasks whose changes a SUCCESSFUL decision
// gathers (author order) and a problem that makes the gather impossible.
func eligibleGatherTasks(run *instance.GroupRun) ([]instance.GroupTask, string) {
	tasks := orderedGroupTasks(run)
	var eligible []instance.GroupTask
	if run.CompletionRule == workflow.GroupRuleFirstSuccess {
		winner := run.Decision.WinnerKey
		if winner == "" {
			return nil, "first_success decision names no winner; nothing can be gathered"
		}
		for _, t := range tasks {
			if t.Key == winner {
				eligible = append(eligible, t)
			}
		}
		if len(eligible) == 0 {
			return nil, fmt.Sprintf("winning task %q is not on the group roster", winner)
		}
	} else {
		eligible = tasks
	}
	for _, t := range eligible {
		if strings.TrimSpace(t.FinalizedRef) == "" {
			return eligible, fmt.Sprintf("task %s has no finalized working-area ref; its file changes cannot be gathered", t.Key)
		}
	}
	return eligible, ""
}

func markIneligible(p *gatherPass, eligible []instance.GroupTask) {
	in := make(map[string]bool, len(eligible))
	for _, t := range eligible {
		in[t.Key] = true
	}
	for i := range p.report.Tasks {
		t := &p.report.Tasks[i]
		if in[t.Key] || t.Reason != "" {
			continue
		}
		if p.run.CompletionRule == workflow.GroupRuleFirstSuccess {
			t.Reason = "not the winner: first_success gathers only the winning task's changes"
		} else {
			t.Reason = "not eligible for gathering"
		}
	}
}

func parentWorkspace(parent *instance.Instance) string {
	if parent == nil {
		return ""
	}
	s, _ := parent.Variables[ReservedVarWorkspace].(string)
	return strings.TrimSpace(s)
}

func taskReportIndex(r *instance.GatherReport, key string) int {
	for i := range r.Tasks {
		if r.Tasks[i].Key == key {
			return i
		}
	}
	return -1
}

// latestAttemptIndex returns the index of the newest attempt for mergeIndex, or -1.
func latestAttemptIndex(r *instance.GatherReport, mergeIndex int) int {
	idx, best := -1, 0
	for i, a := range r.Attempts {
		if a.MergeIndex == mergeIndex && a.Attempt >= best {
			idx, best = i, a.Attempt
		}
	}
	return idx
}

func nextAttemptNumber(r *instance.GatherReport, mergeIndex int) int {
	n := 0
	for _, a := range r.Attempts {
		if a.MergeIndex == mergeIndex && a.Attempt > n {
			n = a.Attempt
		}
	}
	return n + 1
}

// findAttempt returns a pointer into r.Attempts for (mergeIndex, attempt), or nil.
func findAttempt(r *instance.GatherReport, mergeIndex, attempt int) *instance.ResolutionAttempt {
	if r == nil {
		return nil
	}
	for i := range r.Attempts {
		if r.Attempts[i].MergeIndex == mergeIndex && r.Attempts[i].Attempt == attempt {
			return &r.Attempts[i]
		}
	}
	return nil
}

// copyGatherReport keeps the report's maps, slices and attempt/session pointers
// independent of the repository's snapshot.
func copyGatherReport(r instance.GatherReport) instance.GatherReport {
	return r.Clone()
}

func gatherReportMap(r instance.GatherReport) map[string]any {
	b, err := json.Marshal(r)
	if err != nil {
		return map[string]any{"mode": r.Mode, "state": r.State, "error": r.Error}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{"mode": r.Mode, "state": r.State, "error": r.Error}
	}
	return m
}

func gatherHandleMap(h GatherHandle) map[string]any {
	return map[string]any{
		"snapshotHandle": h.SnapshotHandle,
		"gatherId":       h.GatherID.String(),
		"stagingDir":     h.StagingDir,
		"rollbackRef":    h.RollbackRef,
		"parentDir":      h.ParentDir,
	}
}

// GatherHandleFromReport decodes the persisted gather handle. ok is false when
// no gather has been staged yet (or the handle is incomplete).
func GatherHandleFromReport(r *instance.GatherReport) (GatherHandle, bool) {
	if r == nil || len(r.Handle) == 0 {
		return GatherHandle{}, false
	}
	b, err := json.Marshal(r.Handle)
	if err != nil {
		return GatherHandle{}, false
	}
	var h GatherHandle
	if err := json.Unmarshal(b, &h); err != nil {
		return GatherHandle{}, false
	}
	if h.GatherID == uuid.Nil || h.SnapshotHandle == "" || h.RollbackRef == "" || h.StagingDir == "" || h.ParentDir == "" {
		return GatherHandle{}, false
	}
	return h, true
}
