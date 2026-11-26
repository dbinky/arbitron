package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// GroupTaskFinalizer is the terminal hook Engine.run calls for a group-task
// instance (Instance.GroupRunID set) that walked its graph to the end, BEFORE
// SetCompleted, the completion notification, workspace cleanup and
// OnChildTerminal. It exists because the success path removes the task's
// workspace before the parent is told the task finished: without the hook a
// results_and_files gather would find its file input already deleted.
type GroupTaskFinalizer interface {
	// FinalizeTerminal records whatever the task's group needs from its
	// working area. A non-nil error means the task must NOT be published as a
	// successful file-gather candidate: the engine fails the instance instead
	// of completing it. succeeded=false is a no-op (failed and cancelled work
	// is never eligible for merging; failInstance already retains it).
	FinalizeTerminal(ctx context.Context, inst *instance.Instance, succeeded bool) error
	// RetainWorkspace reports whether the task's workspace must survive the
	// completion cleanup. Unsure answers must be true: retaining is
	// recoverable (P3-T10 prunes), deleting is not.
	RetainWorkspace(ctx context.Context, inst *instance.Instance) bool
}

// ErrGroupFileFinalization prefixes every finalization failure; the failed
// task's recorded error reads "group file finalization failed: <cause>".
var ErrGroupFileFinalization = errors.New("group file finalization failed")

// SetGroupTaskFinalizer wires the terminal finalization/retention hook. nil
// (the default) keeps today's completion path for every instance. Configure it
// before starting instances, like SetWorkspaceManager.
func (e *Engine) SetGroupTaskFinalizer(f GroupTaskFinalizer) {
	e.groupFinalizer = f
}

// ExistingWorkspaceReader locates an instance workspace without creating it.
// A missing directory must be an error: recreating it would turn lost task
// content into intentional deletions when the files are gathered.
type ExistingWorkspaceReader interface {
	ExistingInstanceDir(instanceID uuid.UUID) (string, error)
}

// GroupFileFinalizer is the production GroupTaskFinalizer: for a task of a
// results_and_files group that is still blocking an undecided run, it records
// the task directory as an immutable working-area ref and persists that ref on
// the roster row before the task completes.
type GroupFileFinalizer struct {
	groups     instance.GroupRunRepository
	area       WorkingArea
	workspaces ExistingWorkspaceReader
	resource   WorkspaceResource
}

var _ GroupTaskFinalizer = (*GroupFileFinalizer)(nil)

// NewGroupFileFinalizer builds the finalizer. Every dependency is required: a
// nil one is a composition-root bug that would silently publish file-gather
// candidates without their files, so it panics.
func NewGroupFileFinalizer(groups instance.GroupRunRepository, area WorkingArea, workspaces ExistingWorkspaceReader) *GroupFileFinalizer {
	if groups == nil || area == nil || workspaces == nil {
		panic("engine: NewGroupFileFinalizer requires a GroupRunRepository, a WorkingArea and an ExistingWorkspaceReader")
	}
	return &GroupFileFinalizer{groups: groups, area: area, workspaces: workspaces}
}

// FinalizeTerminal implements GroupTaskFinalizer.
//
// It is a no-op unless ALL of: the task succeeded, it belongs to a group run,
// that run gathers files, the run is still undecided (launching/running) and
// the task is still blocking it. A task that finishes after its group decided
// (a first-success loser continuing independently, a task whose cancellation
// raced its completion) can never be merged, so it is not finalized.
func (f *GroupFileFinalizer) FinalizeTerminal(ctx context.Context, inst *instance.Instance, succeeded bool) error {
	if !succeeded || inst == nil || inst.GroupRunID == nil {
		return nil
	}
	run, err := f.groups.Get(ctx, *inst.GroupRunID)
	if err != nil {
		return fmt.Errorf("%w: load group run %s: %v", ErrGroupFileFinalization, *inst.GroupRunID, err)
	}
	if run.GatherMode != workflow.GroupGatherResultsAndFiles {
		return nil
	}
	task, ok := run.TaskByChild(inst.ID)
	if !ok {
		return fmt.Errorf("%w: instance %s is not on the roster of group run %s", ErrGroupFileFinalization, inst.ID, run.ID)
	}
	if !task.Blocking || (run.State != instance.GroupRunLaunching && run.State != instance.GroupRunRunning) {
		return nil
	}
	if run.WorkspaceSnapshot == "" {
		return fmt.Errorf("%w: group run %s has no workspace snapshot to finalize task %q against", ErrGroupFileFinalization, run.ID, task.Key)
	}
	dir, err := f.workspaces.ExistingInstanceDir(inst.ID)
	if err != nil {
		return fmt.Errorf("%w: task %q workspace: %v", ErrGroupFileFinalization, task.Key, err)
	}
	if f.resource != nil {
		claim, err := f.resource.Acquire(ctx, inst.ID, dir, "group finalize")
		if err != nil {
			return fmt.Errorf("%w: task %q: acquire its working area: %w", ErrGroupFileFinalization, task.Key, err)
		}
		defer releaseWorkspaceResource(ctx, f.resource, claim)
	}
	ref, err := f.area.FinalizeTask(ctx, run.WorkspaceSnapshot, inst.ID, dir)
	if err != nil {
		return fmt.Errorf("%w: task %q: %w", ErrGroupFileFinalization, task.Key, err)
	}
	if err := f.groups.SetTaskFinalizedRef(ctx, run.ID, task.Key, ref); err != nil {
		return fmt.Errorf("%w: task %q: persist ref %s: %v", ErrGroupFileFinalization, task.Key, ref, err)
	}
	return nil
}

// RetainWorkspace implements GroupTaskFinalizer: a task of a
// results_and_files run always keeps its workspace at completion. The group
// pruner (GroupResourcePruner.PruneRun) is the single owner of that
// workspace's retention: it removes it once the run is done and its gather was
// applied, and keeps it for debugging when the gather was not (failed, rolled
// back, or a task completing after a first-success gather). An unreadable run
// is retained.
func (f *GroupFileFinalizer) RetainWorkspace(ctx context.Context, inst *instance.Instance) bool {
	if inst == nil || inst.GroupRunID == nil {
		return false
	}
	run, err := f.groups.Get(ctx, *inst.GroupRunID)
	if err != nil {
		slog.Warn("group finalizer: cannot load group run; retaining task workspace",
			"instanceID", inst.ID, "groupRunID", *inst.GroupRunID, "error", err)
		return true
	}
	return run.GatherMode == workflow.GroupGatherResultsAndFiles
}

// finalizeGroupTask runs the hook for a group-task instance that reached the
// end of its graph. It returns false when the run loop must stop WITHOUT
// completing: the instance was failed here, or the run was cancelled while
// finalizing (the cancel path owns that terminal transition).
func (e *Engine) finalizeGroupTask(ctx context.Context, inst *instance.Instance, workflowName, lastNodeID string) bool {
	if e.groupFinalizer == nil || inst.GroupRunID == nil {
		return true
	}
	err := e.groupFinalizer.FinalizeTerminal(ctx, inst, true)
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return false
	}
	if err == nil {
		return true
	}
	msg := err.Error()
	if !errors.Is(err, ErrGroupFileFinalization) {
		msg = fmt.Errorf("%w: %v", ErrGroupFileFinalization, err).Error()
	}
	slog.Error("group task finalization failed; failing the task instead of completing it",
		"instanceID", inst.ID, "groupRunID", *inst.GroupRunID, "error", msg)
	e.recordFinalizationFailure(ctx, inst.ID, lastNodeID, msg)
	if e.failInstance(ctx, inst.ID, lastNodeID) {
		e.emitNotification(ctx, inst.ID, workflowName, notification.TypeFailed)
	}
	e.eventBus.Publish(inst.ID, Event{
		Type:      EventTypeInstanceFailed,
		Timestamp: time.Now(),
		Data:      map[string]any{"failedNodeId": lastNodeID, "error": msg},
	})
	return false
}

// recordFinalizationFailure writes a failed NodeExecution on the last node
// that ran, one attempt after its latest, so the failure trace (read by the
// group coordinator for the task's failure details, and by run inspection)
// carries the finalization error.
func (e *Engine) recordFinalizationFailure(ctx context.Context, instanceID uuid.UUID, nodeID, msg string) {
	attempt := 1
	if prev, err := e.nodeExecutions.GetByInstanceAndNode(ctx, instanceID, nodeID); err == nil && prev != nil {
		attempt = prev.Attempt + 1
	}
	now := time.Now()
	if err := e.nodeExecutions.Create(ctx, &instance.NodeExecution{
		ID:          uuid.New(),
		InstanceID:  instanceID,
		NodeID:      nodeID,
		Attempt:     attempt,
		Status:      instance.NodeStatusFailed,
		Error:       map[string]any{"error": msg, "phase": "group_file_finalization"},
		StartedAt:   now,
		CompletedAt: &now,
	}); err != nil {
		slog.Error("failed to record group finalization failure", "instanceID", instanceID, "error", err)
	}
}

// retainGroupTaskWorkspace reports whether a completed group task's workspace
// must be kept: the finalizer says the gather still needs it, or the task
// still has non-terminal descendants anywhere below it. Non-group instances
// and engines without a finalizer are never retained here (today's cleanup).
func (e *Engine) retainGroupTaskWorkspace(ctx context.Context, inst *instance.Instance) bool {
	if e.groupFinalizer == nil || inst.GroupRunID == nil {
		return false
	}
	if e.groupFinalizer.RetainWorkspace(ctx, inst) {
		return true
	}
	return e.hasActiveDescendants(ctx, inst.ID)
}

// hasActiveDescendants walks the spawn-link tree below rootID (through
// terminal intermediates) looking for any non-terminal run. A lookup error
// answers true (retain).
func (e *Engine) hasActiveDescendants(ctx context.Context, rootID uuid.UUID) bool {
	seen := map[uuid.UUID]bool{rootID: true}
	frontier := []uuid.UUID{rootID}
	for depth := 0; len(frontier) > 0 && depth <= instance.MaxSpawnDepth; depth++ {
		var next []uuid.UUID
		for _, id := range frontier {
			children, err := e.spawnLinks.ListChildren(ctx, id)
			if err != nil {
				slog.Warn("cannot list descendants; retaining group task workspace", "instanceID", rootID, "error", err)
				return true
			}
			for _, c := range children {
				if !c.State.IsTerminal() {
					return true
				}
				if !seen[c.ID] {
					seen[c.ID] = true
					next = append(next, c.ID)
				}
			}
		}
		frontier = next
	}
	return false
}
