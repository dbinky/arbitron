package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// GroupTaskStartRequest is everything StartGroupTask needs to create ONE
// parallel-group task child. TaskInstanceID is the id the group's roster
// RESERVED before anything was enqueued, which is what makes a task
// addressable (and recoverable) before it exists.
type GroupTaskStartRequest struct {
	TaskInstanceID   uuid.UUID
	ParentInstanceID uuid.UUID
	ParentNodeID     string
	GroupRunID       uuid.UUID
	TaskKey          string
	Kind             instance.GroupTaskKind
	// ChildWorkflow is the child workflow's name (workflow kind only).
	ChildWorkflow string
	// BranchEntry is the validated entry node of the branch body (branch kind only).
	BranchEntry string
	// Variables are the fully composed task variables. They never contain
	// "workspace": each task's own working area is seeded by its own run.
	Variables map[string]any
	// Priority is the parent's priority (workflow kind). Branch tasks always take
	// the parent's persisted priority.
	Priority string
}

// GroupTaskStarter creates one group task child. *Engine satisfies it.
type GroupTaskStarter interface {
	StartGroupTask(ctx context.Context, req GroupTaskStartRequest) error
}

var _ GroupTaskStarter = (*Engine)(nil)

// ErrInvalidGroupTaskRequest is returned when a group task request is
// incomplete or inconsistent, or the reserved id already belongs to another
// task. It is always returned BEFORE any row is created.
var ErrInvalidGroupTaskRequest = errors.New("invalid group task start request")

func (r GroupTaskStartRequest) validate() error {
	var problems []string
	if r.TaskInstanceID == uuid.Nil {
		problems = append(problems, "TaskInstanceID (the roster's reserved id) is unset")
	}
	if r.ParentInstanceID == uuid.Nil {
		problems = append(problems, "ParentInstanceID is unset")
	}
	if r.ParentNodeID == "" {
		problems = append(problems, "ParentNodeID is unset")
	}
	if r.GroupRunID == uuid.Nil {
		problems = append(problems, "GroupRunID is unset")
	}
	if r.TaskKey == "" {
		problems = append(problems, "TaskKey is unset")
	}
	switch r.Kind {
	case instance.GroupTaskKindWorkflow:
		if r.ChildWorkflow == "" {
			problems = append(problems, "a workflow task needs ChildWorkflow")
		}
		if r.BranchEntry != "" {
			problems = append(problems, "a workflow task must not name a BranchEntry")
		}
	case instance.GroupTaskKindBranch:
		if r.BranchEntry == "" {
			problems = append(problems, "a branch task needs BranchEntry")
		}
		if r.ChildWorkflow != "" {
			problems = append(problems, "a branch task must not name a ChildWorkflow")
		}
	default:
		problems = append(problems, fmt.Sprintf("Kind %q is neither %q nor %q",
			r.Kind, instance.GroupTaskKindBranch, instance.GroupTaskKindWorkflow))
	}
	var reserved []string
	for k := range r.Variables {
		if variable.ReservedInputNames[k] {
			reserved = append(reserved, k)
		}
	}
	sort.Strings(reserved)
	for _, k := range reserved {
		problems = append(problems, fmt.Sprintf("variable %q is engine-seeded and may not be supplied", k))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: task %q: %s", ErrInvalidGroupTaskRequest, r.TaskKey, strings.Join(problems, "; "))
	}
	return nil
}

// StartGroupTask creates one parallel-group task child under its reserved id
// and enqueues it.
//
// The caller (GroupLauncher) injects the parent OWNER's reconstructed identity
// on ctx, exactly as SpawnWorkflowExecutor.prepareAndSpawn does for Spawn; the
// identity is required and must be the parent's owner, so a task can never be
// created under anyone else's credentials or queue.
//
// IDEMPOTENT. A row that already exists under req.TaskInstanceID for the same
// group run and task is left alone (a still-queued one is re-offered to the
// queue evaluator) and nil is returned, so a re-call after a crash or a replay
// can never duplicate a child.
//
// Workflow kind reuses the trigger pipeline (owner from ctx, visibility gate,
// archived refusal, group-support guard, input validation, audience stamping)
// via triggerSpec. Branch kind copies the PARENT's definition snapshot and
// starts at the validated branch entry; it never reloads a definition by name.
func (e *Engine) StartGroupTask(ctx context.Context, req GroupTaskStartRequest) (startErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := req.validate(); err != nil {
		return err
	}
	caller, ok := identity.FromContext(ctx)
	if !ok {
		return fmt.Errorf("%w: task %q: the parent owner's reconstructed identity must be on the context",
			ErrInvalidGroupTaskRequest, req.TaskKey)
	}

	existing, err := e.instances.Get(ctx, req.TaskInstanceID)
	switch {
	case err == nil && existing != nil:
		return e.resumeExistingGroupTask(ctx, req, caller, existing)
	case err != nil && !errors.Is(err, instance.ErrNotFound):
		// A storage failure must never be mistaken for "not created yet": that
		// is exactly how a second child would be born.
		return fmt.Errorf("group task %q: look up reserved instance %s: %w", req.TaskKey, req.TaskInstanceID, err)
	}

	parent, err := e.instances.Get(ctx, req.ParentInstanceID)
	if err != nil {
		return fmt.Errorf("group task %q: load parent instance: %w", req.TaskKey, err)
	}
	if parent == nil {
		return fmt.Errorf("group task %q: parent instance %s: %w", req.TaskKey, req.ParentInstanceID, instance.ErrNotFound)
	}
	if caller.UserID != parent.OwnerID {
		return fmt.Errorf("group task %q: %w: the context identity %s is not the owner of parent run %s",
			req.TaskKey, identity.ErrForbidden, caller.UserID, parent.ID)
	}
	if parent.State.IsTerminal() {
		return fmt.Errorf("group task %q: parent instance %s is %s", req.TaskKey, parent.ID, parent.State)
	}
	// Cancel can finish its child listing after this read but before Create
	// commits. Check again AFTER creation/enqueue (including error returns):
	// either the walk can see the child, or we stop the late child ourselves.
	// A pre-create state read alone cannot close that window.
	defer func() { startErr = e.finishGroupTaskStart(ctx, req, startErr) }()

	// The same runtime recursion bound Engine.Spawn enforces (engine_spawn.go:77-87),
	// derived from the parent's PERSISTED depth.
	depth := parent.SpawnDepth + 1
	if depth > instance.MaxSpawnDepth {
		return fmt.Errorf("group task %q: %w (depth %d, limit %d); the run tree is %d generations deep, "+
			"which usually means a spawn or group cycle",
			req.TaskKey, instance.ErrSpawnDepthExceeded, depth, instance.MaxSpawnDepth, parent.SpawnDepth)
	}

	link := instance.SpawnLink{
		ParentInstanceID: req.ParentInstanceID,
		ParentNodeID:     req.ParentNodeID,
		Mode:             instance.SpawnModeGroupTask,
		SpawnDepth:       depth,
		GroupRunID:       req.GroupRunID,
		GroupTaskKey:     req.TaskKey,
		GroupTaskKind:    req.Kind,
	}
	if !link.Valid() {
		return fmt.Errorf("%w: task %q: incomplete group-task link %+v", ErrInvalidGroupTaskRequest, req.TaskKey, link)
	}

	if req.Kind == instance.GroupTaskKindWorkflow {
		_, _, err := e.trigger(ctx, triggerSpec{
			workflowName: req.ChildWorkflow,
			variables:    variable.DeepCopyMap(req.Variables),
			priority:     req.Priority,
			instanceID:   req.TaskInstanceID,
			link:         &link,
		})
		if err != nil {
			return fmt.Errorf("group task %q: start child workflow %q: %w", req.TaskKey, req.ChildWorkflow, err)
		}
		return nil
	}
	return e.startBranchTask(ctx, req, parent, link)
}

// finishGroupTaskStart compensates a start that raced its parent's termination
// or request cancellation. Keep the owner's identity, but shed cancellation so
// even an INSERT committed just as the parent context died can be cleaned up.
// A failed parent reload fails closed too: an unknown parent cannot admit work.
func (e *Engine) finishGroupTaskStart(ctx context.Context, req GroupTaskStartRequest, startErr error) error {
	cleanupCtx := context.WithoutCancel(ctx)
	parent, err := e.instances.Get(cleanupCtx, req.ParentInstanceID)
	switch {
	case err != nil:
		// Preserve the storage error for the caller.
	case parent == nil:
		err = instance.ErrNotFound
	case parent.State.IsTerminal():
		err = fmt.Errorf("parent instance %s is %s", parent.ID, parent.State)
	default:
		err = ctx.Err()
	}
	if err == nil {
		// The pause cascade has the same window Cancel does: a task created
		// after it walked the parent's children would run unsupervised.
		if startErr == nil {
			e.pauseIfParentPaused(cleanupCtx, req.ParentInstanceID, req.TaskInstanceID)
		}
		return startErr
	}
	startErr = errors.Join(startErr, fmt.Errorf("group task %q: parent no longer active: %w", req.TaskKey, err))
	if cancelErr := e.Cancel(cleanupCtx, req.TaskInstanceID); cancelErr != nil &&
		!errors.Is(cancelErr, instance.ErrNotFound) && !errors.Is(cancelErr, instance.ErrCannotCancel) {
		return errors.Join(startErr, fmt.Errorf("stop late group task: %w", cancelErr))
	}
	return startErr
}

// resumeExistingGroupTask is the idempotent path: the reserved row already
// exists. It is accepted only if it IS this task.
func (e *Engine) resumeExistingGroupTask(ctx context.Context, req GroupTaskStartRequest, caller identity.Identity, existing *instance.Instance) error {
	if existing.GroupRunID == nil || *existing.GroupRunID != req.GroupRunID ||
		existing.GroupTaskKey == nil || *existing.GroupTaskKey != req.TaskKey {
		return fmt.Errorf("%w: task %q: reserved instance id %s already belongs to a different run",
			ErrInvalidGroupTaskRequest, req.TaskKey, req.TaskInstanceID)
	}
	if existing.OwnerID != caller.UserID {
		return fmt.Errorf("group task %q: %w: the context identity %s does not own instance %s",
			req.TaskKey, identity.ErrForbidden, caller.UserID, existing.ID)
	}
	if existing.State == instance.StateQueued {
		// Already in the queue: re-offer it to the evaluator. A second Enqueue
		// could StartInstance a row the evaluator is starting concurrently.
		e.queueManager.NotifyCapacityFreed(ctx)
	}
	return nil
}

// startBranchTask creates a branch task: the parent's own definition, started
// at the validated branch entry, with the parent's ownership and audience.
func (e *Engine) startBranchTask(ctx context.Context, req GroupTaskStartRequest, parent *instance.Instance, link instance.SpawnLink) error {
	wf, nodeMap, _, err := parseWorkflowGraph(parent.WorkflowSnapshot)
	if err != nil {
		return fmt.Errorf("group task %q: parse the parent's workflow snapshot: %w", req.TaskKey, err)
	}
	if _, ok := nodeMap[req.BranchEntry]; !ok {
		return fmt.Errorf("group task %q: branch entry %q is not a node of the parent's workflow snapshot",
			req.TaskKey, req.BranchEntry)
	}
	if !isGroupTaskEdge(wf, req.ParentNodeID, req.BranchEntry) {
		return fmt.Errorf("group task %q: branch entry %q is not the target of a %q edge from group node %q in the parent's snapshot",
			req.TaskKey, req.BranchEntry, workflow.GroupPortTask, req.ParentNodeID)
	}
	if err := checkGroupSupport(wf, e.GroupSupportLevel()); err != nil {
		return fmt.Errorf("group task %q: %w", req.TaskKey, err)
	}

	source, scheduleID := triggerOrigin(ctx)
	parentID := link.ParentInstanceID
	parentNode := link.ParentNodeID
	runID := link.GroupRunID
	key := link.GroupTaskKey
	entry := req.BranchEntry
	vars := variable.DeepCopyMap(req.Variables)
	if vars == nil {
		vars = map[string]any{}
	}
	pins, err := e.branchTaskPins(ctx, parent, wf)
	if err != nil {
		return fmt.Errorf("group task %q: %w", req.TaskKey, err)
	}
	now := time.Now()

	inst := &instance.Instance{
		ModelPins:                pins.Models,
		ModelPinRoutes:           pins.OpaqueRoutes,
		ModelPinRouteConnections: pins.OpaqueConnections,
		ModelPinsVersion:         instance.ModelPinsVersionSnapshot,
		ID:                       req.TaskInstanceID,
		OwnerID:                  parent.OwnerID,
		WorkflowID:               parent.WorkflowID,
		WorkflowName:             parent.WorkflowName,
		WorkflowSnapshot:         variable.DeepCopyMap(parent.WorkflowSnapshot),
		Department:               parent.Department,
		Visibility:               parent.Visibility,
		TriggerSource:            source,
		ScheduleID:               scheduleID,
		ParentInstanceID:         &parentID,
		ParentNodeID:             &parentNode,
		SpawnMode:                instance.SpawnModeGroupTask,
		SpawnDepth:               link.SpawnDepth,
		GroupRunID:               &runID,
		GroupTaskKey:             &key,
		GroupTaskKind:            instance.GroupTaskKindBranch,
		State:                    instance.StateQueued,
		Priority:                 parent.Priority,
		Variables:                vars,
		OriginalVariables:        variable.DeepCopyMap(vars),
		CurrentNodeID:            &entry,
		QueuedAt:                 &now,
		CreatedAt:                now,
		UpdatedAt:                now,
	}
	if _, err := e.createAndEnqueue(ctx, inst); err != nil {
		return fmt.Errorf("group task %q: %w", req.TaskKey, err)
	}
	return nil
}

// branchTaskPins is the pin record a branch task starts with. A branch runs the
// parent's own definition, so it inherits the parent's snapshot exactly (D1: a
// run keeps its starting versions, including its branches), as independent
// copies. A legacy parent created before pins has none to inherit; its branch
// is still a new instance, so it captures a fresh snapshot like any trigger.
func (e *Engine) branchTaskPins(ctx context.Context, parent *instance.Instance, wf *workflow.Workflow) (provider.ModelPinSnapshot, error) {
	if parent.ModelPinsVersion != instance.ModelPinsVersionSnapshot {
		return e.captureModelPins(ctx, wf)
	}
	return provider.ModelPinSnapshot{
		Models:            maps.Clone(parent.ModelPins),
		OpaqueRoutes:      maps.Clone(parent.ModelPinRoutes),
		OpaqueConnections: maps.Clone(parent.ModelPinRouteConnections),
	}, nil
}

// isGroupTaskEdge reports whether the snapshot has a task edge from the group
// node to entry — the shape P1-T6 writes for every branch task.
func isGroupTaskEdge(wf *workflow.Workflow, groupNodeID, entry string) bool {
	for _, edge := range wf.Edges {
		if edge.Source == groupNodeID && edge.Target == entry && edge.Output == workflow.GroupPortTask {
			return true
		}
	}
	return false
}
