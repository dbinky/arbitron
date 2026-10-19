package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// GroupGatherer merges a decided file-gathering group into the parent's
// working area (Phase 3, P3-T6). The executor is the single writer of the
// parent's area, so gathering runs here, in the parent's own goroutine.
type GroupGatherer interface {
	Gather(ctx context.Context, run *instance.GroupRun, parent *instance.Instance) (*GatherStep, error)
}

// GatherStep is one gather pass's outcome. Done=false means gathering is not
// finished (e.g. waiting on interactive conflict resolution): the parent waits.
type GatherStep struct {
	Done   bool
	Port   string
	Output map[string]any
}

// pendingGroupWaitError marks a failed wait write after the roster exists.
// The parent must stay at this node: only group completion/reconciliation may
// resume it, never node skip/route/retry behavior.
type pendingGroupWaitError struct{ cause error }

func (e *pendingGroupWaitError) Error() string { return e.cause.Error() }
func (e *pendingGroupWaitError) Unwrap() error { return e.cause }

// ParallelGroupExecutor executes one parallel_group node. Every collaborator
// is a port or a group component; the executor holds no engine.
type ParallelGroupExecutor struct {
	instances instance.InstanceRepository
	nodeExecs instance.NodeExecutionRepository
	groups    instance.GroupRunRepository
	launcher  *GroupLauncher
	coord     *GroupCoordinator
	claims    instance.SpawnWaitRepository
	queueMgr  QueueManager
	gatherer  GroupGatherer
	gate      gatherGate
}

var _ NodeExecutor = (*ParallelGroupExecutor)(nil)

// NewParallelGroupExecutor constructs the executor. Every argument is
// required; a nil one panics (a missing claim port, for instance, would
// silently reopen the lost-wake window).
func NewParallelGroupExecutor(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	groups instance.GroupRunRepository,
	launcher *GroupLauncher,
	coord *GroupCoordinator,
	claims instance.SpawnWaitRepository,
	queueMgr QueueManager,
) *ParallelGroupExecutor {
	switch {
	case instances == nil:
		panic("engine: NewParallelGroupExecutor requires a non-nil InstanceRepository")
	case nodeExecs == nil:
		panic("engine: NewParallelGroupExecutor requires a non-nil NodeExecutionRepository")
	case groups == nil:
		panic("engine: NewParallelGroupExecutor requires a non-nil GroupRunRepository")
	case launcher == nil:
		panic("engine: NewParallelGroupExecutor requires a non-nil GroupLauncher")
	case coord == nil:
		panic("engine: NewParallelGroupExecutor requires a non-nil GroupCoordinator")
	case claims == nil:
		panic("engine: NewParallelGroupExecutor requires a non-nil SpawnWaitRepository (ClaimWaitingParent)")
	case queueMgr == nil:
		panic("engine: NewParallelGroupExecutor requires a non-nil QueueManager")
	}
	return &ParallelGroupExecutor{
		instances: instances,
		nodeExecs: nodeExecs,
		groups:    groups,
		launcher:  launcher,
		coord:     coord,
		claims:    claims,
		queueMgr:  queueMgr,
	}
}

// SetGatherer enables file gathering (Phase 3 wiring). It must be called during
// composition, before FreezeExecutors, never concurrently with Execute.
func (x *ParallelGroupExecutor) SetGatherer(g GroupGatherer) {
	x.gatherer = g
}

// Execute runs one parallel_group node. Every failure before the group run is
// persisted returns a plain error so the node's ErrorBehavior applies.
func (x *ParallelGroupExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error) {
	instanceID, ok := InstanceIDFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("parallel_group %q: instance ID not found in context", node.ID)
	}

	// The SAME parser author-time validation uses: an instance executes from a
	// snapshot that may predate that validation.
	cfg, err := workflow.ParseGroupConfig(node.Config)
	if err != nil {
		return nil, fmt.Errorf("parallel_group %q: %w", node.ID, err)
	}
	filesMode := cfg.GatherMode == workflow.GroupGatherResultsAndFiles
	if filesMode && x.gatherer == nil {
		return nil, fmt.Errorf("parallel_group %q: gatherMode %q: %w; this server gathers task results only — "+
			"use gatherMode %q until file gathering is enabled", node.ID, cfg.GatherMode, ErrGroupExecutionUnsupported, workflow.GroupGatherResults)
	}

	run, err := x.groups.GetByParentNode(ctx, instanceID, node.ID)
	if err != nil {
		// Never mistaken for "no run yet": that is how a second roster is born.
		return nil, fmt.Errorf("parallel_group %q: look up the group run: %w", node.ID, err)
	}
	if run != nil {
		return x.reenter(ctx, node, filesMode, run, instanceID)
	}
	return x.firstEntry(ctx, node, cfg, vars, instanceID, filesMode)
}

// firstEntry launches the group and suspends the parent.
//
// ORDER IS LOAD-BEARING, exactly as for an awaited spawn (executor_spawn.go:127-193):
// launch every task -> write the wait trace -> transition to waiting ->
// lost-wake check -> release capacity. A waiting parent holds no slot, so with
// a per-owner cap of one the tasks can still run.
func (x *ParallelGroupExecutor) firstEntry(ctx context.Context, node workflow.Node, cfg workflow.GroupConfig, vars *variable.VariableStore, instanceID uuid.UUID, filesMode bool) (*NodeResult, error) {
	parent, err := x.instances.Get(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("parallel_group %q: load parent instance: %w", node.ID, err)
	}
	scope, err := revalidateGroup(node.ID, parent.WorkflowSnapshot)
	if err != nil {
		return nil, err
	}

	startedAt := time.Now()
	run, err := x.launcher.Launch(ctx, GroupLaunchRequest{Parent: parent, Node: node, Config: cfg, Ownership: scope, Vars: vars})
	if err != nil {
		return nil, fmt.Errorf("parallel_group %q: %w", node.ID, err)
	}
	if err := x.coord.Evaluate(ctx, run.ID); err != nil {
		slog.Warn("parallel_group: evaluating the freshly launched group failed; the group sweep retries",
			"nodeID", node.ID, "groupRunID", run.ID, "error", err)
	}
	run = x.reread(ctx, run)
	output := groupWaitOutput(run)

	// executeWithRetry skips writing a node execution on the wait path, so the
	// executor writes its own — otherwise the group node leaves no trace.
	completedAt := time.Now()
	if err := x.nodeExecs.Create(ctx, &instance.NodeExecution{
		ID:          uuid.New(),
		InstanceID:  instanceID,
		NodeID:      node.ID,
		Attempt:     1,
		Status:      instance.NodeStatusSucceeded,
		Output:      output,
		StartedAt:   startedAt,
		CompletedAt: &completedAt,
	}); err != nil {
		return nil, &pendingGroupWaitError{cause: fmt.Errorf("parallel_group %q: write node execution record: %w", node.ID, err)}
	}
	return x.suspend(ctx, node, filesMode, run.ID, instanceID, output)
}

// reenter handles every later execution of the node: never a second launch.
func (x *ParallelGroupExecutor) reenter(ctx context.Context, node workflow.Node, filesMode bool, run *instance.GroupRun, instanceID uuid.UUID) (*NodeResult, error) {
	if res, err := x.resolve(ctx, node, filesMode, run, instanceID); err != nil || res != nil {
		return res, err
	}
	// Not ready: let the coordinator catch up (a terminal may have been missed).
	if err := x.coord.Evaluate(ctx, run.ID); err != nil {
		slog.Warn("parallel_group: re-evaluating the group on re-entry failed; the group sweep retries",
			"nodeID", node.ID, "groupRunID", run.ID, "error", err)
	}
	run = x.reread(ctx, run)
	if res, err := x.resolve(ctx, node, filesMode, run, instanceID); err != nil || res != nil {
		return res, err
	}
	return x.suspend(ctx, node, filesMode, run.ID, instanceID, groupWaitOutput(run))
}

// resolve returns the routed result for a run that can continue the parent
// now (the parent is running in this goroutine), or nil when it must wait.
func (x *ParallelGroupExecutor) resolve(ctx context.Context, node workflow.Node, filesMode bool, run *instance.GroupRun, instanceID uuid.UUID) (*NodeResult, error) {
	if run.State == instance.GroupRunDone {
		return x.deliver(ctx, node, run)
	}
	if !filesMode || !fileGatherReady(run) {
		return nil, nil
	}
	if x.gatherer == nil {
		return nil, fmt.Errorf("parallel_group %q: group run %s is ready to gather files: %w", node.ID, run.ID, ErrGroupExecutionUnsupported)
	}
	return x.continueFileGather(ctx, node, run, instanceID)
}

// deliver routes a done run: the frozen output, on the frozen port, every time.
func (x *ParallelGroupExecutor) deliver(ctx context.Context, node workflow.Node, run *instance.GroupRun) (*NodeResult, error) {
	port := run.ContinuationPort
	if port != workflow.GroupPortSuccess && port != workflow.GroupPortFailure {
		return nil, fmt.Errorf("parallel_group %q: group run %s is done but carries continuation port %q",
			node.ID, run.ID, port)
	}
	if err := x.groups.MarkContinuationDelivered(ctx, run.ID); err != nil {
		slog.Warn("parallel_group: recording delivery failed; a replay re-delivers the same output",
			"nodeID", node.ID, "groupRunID", run.ID, "error", err)
	}
	return &NodeResult{OutputPort: port, Output: variable.DeepCopyMap(run.FinalOutput)}, nil
}

// suspend transitions the parent to waiting, closes the lost-wake window, and
// releases its queue slot.
func (x *ParallelGroupExecutor) suspend(ctx context.Context, node workflow.Node, filesMode bool, runID uuid.UUID, instanceID uuid.UUID, output map[string]any) (*NodeResult, error) {
	// waiting_until stays NULL: an untimed wait, invisible to the timer sweep.
	if err := x.park(ctx, node, instanceID); err != nil {
		return nil, err
	}
	res, claimed, err := x.recoverLostWake(ctx, node, filesMode, runID, instanceID)
	if claimed {
		if err != nil || res != nil {
			return res, err
		}
		// Claimed back, but the gatherer has more to do: wait again; the
		// gatherer owns the next wake.
		if err := x.park(ctx, node, instanceID); err != nil {
			return nil, err
		}
	}
	x.queueMgr.NotifyCapacityFreed(ctx)
	return &NodeResult{IsWait: true, Output: output}, nil
}

// park moves the parent from running to waiting with a CAS. Engine.Cancel
// commits `cancelled` before it cancels this run's context, so an unconditional
// write here could revive a cancelled parent as waiting, and the coordinator
// would then wake it through the failure port. A lost CAS halts like a
// cancellation: whoever moved the parent owns its state.
func (x *ParallelGroupExecutor) park(ctx context.Context, node workflow.Node, instanceID uuid.UUID) error {
	parked, err := x.claims.SuspendRunning(ctx, instanceID)
	if err != nil {
		return &pendingGroupWaitError{cause: fmt.Errorf("parallel_group %q: set waiting state: %w", node.ID, err)}
	}
	if !parked {
		return fmt.Errorf("parallel_group %q: parent %s left running before it could wait: %w", node.ID, instanceID, context.Canceled)
	}
	return nil
}

// recoverLostWake re-reads the run AFTER the waiting write is durable. If the
// group became ready while the parent was still running (the coordinator could
// not wake a parent that was not yet waiting), it claims the parent back to
// running with the state-and-node CAS and resolves in-process. Losing the
// claim means the coordinator's wake won and re-queued the parent; every error
// degrades to "keep waiting" — the group sweep wakes it.
func (x *ParallelGroupExecutor) recoverLostWake(ctx context.Context, node workflow.Node, filesMode bool, runID uuid.UUID, instanceID uuid.UUID) (*NodeResult, bool, error) {
	run, err := x.groups.Get(ctx, runID)
	if err != nil {
		slog.Error("parallel_group: re-reading the group after suspending failed; relying on the group sweep",
			"nodeID", node.ID, "groupRunID", runID, "error", err)
		return nil, false, nil
	}
	ready := run.State == instance.GroupRunDone ||
		(filesMode && x.gatherer != nil && fileGatherReady(run))
	if !ready {
		return nil, false, nil
	}
	claimed, err := x.claims.ClaimWaitingParent(ctx, instanceID, node.ID)
	if err != nil {
		slog.Error("parallel_group: claiming the parent back from waiting failed; relying on the group sweep",
			"nodeID", node.ID, "instanceID", instanceID, "error", err)
		return nil, false, nil
	}
	if !claimed {
		return nil, false, nil
	}
	res, err := x.resolve(ctx, node, filesMode, run, instanceID)
	if err != nil {
		slog.Error("parallel_group: resolving the claimed group failed; returning to waiting for the group sweep",
			"nodeID", node.ID, "groupRunID", runID, "error", err)
		return nil, true, nil
	}
	return res, true, nil
}

func (x *ParallelGroupExecutor) reread(ctx context.Context, run *instance.GroupRun) *instance.GroupRun {
	fresh, err := x.groups.Get(ctx, run.ID)
	if err != nil {
		slog.Warn("parallel_group: re-reading the group run failed", "groupRunID", run.ID, "error", err)
		return run
	}
	return fresh
}

// fileGatherReady reports whether a files-mode run may gather now: decided (or
// mid-gather) AND its unfinished-task actions applied, the same condition
// GroupCoordinator.WakeParent and the gather reconciler wake on. Gathering
// before the actions land could merge into the parent while a cancel_unfinished
// loser is still running and writing.
func fileGatherReady(run *instance.GroupRun) bool {
	return gatherableGroupState(run.State) && run.ActionsApplied
}

func gatherableGroupState(s instance.GroupRunState) bool {
	return s == instance.GroupRunDecided || s == instance.GroupRunGathering || s == instance.GroupRunResolvingConflicts
}

func groupWaitOutput(run *instance.GroupRun) map[string]any {
	return map[string]any{"groupRunId": run.ID.String(), "state": string(run.State)}
}

// revalidateGroup re-runs the Phase 1 ownership analysis over the run's own
// snapshot. Any non-advisory problem refuses the node before anything starts.
func revalidateGroup(nodeID string, snapshot map[string]any) (*workflow.GroupScope, error) {
	wf, _, _, err := parseWorkflowGraph(snapshot)
	if err != nil {
		return nil, fmt.Errorf("parallel_group %q: parse workflow snapshot: %w", nodeID, err)
	}
	graph, problems := workflow.AnalyzeGroups(wf)
	var hard []string
	for _, p := range problems {
		if p.Severity == "warning" {
			continue
		}
		hard = append(hard, fmt.Sprintf("%s (node %q): %s", p.Code, p.NodeID, p.Message))
	}
	if len(hard) > 0 {
		return nil, fmt.Errorf("parallel_group %q: the run's workflow snapshot fails parallel-group validation, so no task was started: %s",
			nodeID, strings.Join(hard, "; "))
	}
	if graph == nil || graph.Groups[nodeID] == nil {
		return nil, fmt.Errorf("parallel_group %q: the run's workflow snapshot has no group analysis for this node", nodeID)
	}
	return graph.Groups[nodeID], nil
}
