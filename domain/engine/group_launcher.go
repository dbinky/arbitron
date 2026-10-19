package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// ErrGroupPreLaunch wraps every problem found BEFORE a group run is persisted
// (bad inputs, a missing reference, a shared path, a contract violation, a
// snapshot failure, ...). Nothing exists yet, so the parallel_group node fails
// like any other node and its ErrorBehavior applies.
var ErrGroupPreLaunch = errors.New("parallel_group could not be prepared")

// GroupLaunchRequest is one first entry of a parallel_group node.
type GroupLaunchRequest struct {
	// Parent is the instance whose graph contains the group node (for a nested
	// group, the enclosing task's own instance).
	Parent *instance.Instance
	// Node is the group node being executed.
	Node workflow.Node
	// Config is workflow.ParseGroupConfig(Node.Config).
	Config workflow.GroupConfig
	// Ownership is workflow.AnalyzeGroups(...).Groups[Node.ID].
	Ownership *workflow.GroupScope
	// Vars is the parent's live store at the group node.
	Vars *variable.VariableStore
}

// GroupLauncher launches a parallel group exactly once.
type GroupLauncher struct {
	groups     instance.GroupRunRepository
	starter    GroupTaskStarter
	contracts  ChildWorkflowReader
	workspaces WorkspaceManager
	groupWS    GroupWorkspace
	fileArea   WorkingArea // results_and_files; results-only groups keep groupWS
	identities IdentityResolver
	canceller  RunCanceller
	bus        EventBus
	now        func() time.Time
	// maxLaunchBytes and meter bound a launch's eager copies (SetLaunchByteBudget).
	maxLaunchBytes int64
	meter          GroupAreaMeter
}

// NewGroupLauncher constructs the launcher. groups, starter, identities,
// canceller and bus are required (nil panics: a missing one is a wiring bug
// that would surface as a hung or unauthenticated group). contracts,
// workspaces and groupWS may be nil; a launch that needs one refuses with
// ErrGroupPreLaunch instead of degrading.
func NewGroupLauncher(
	groups instance.GroupRunRepository,
	starter GroupTaskStarter,
	contracts ChildWorkflowReader,
	workspaces WorkspaceManager,
	groupWS GroupWorkspace,
	identities IdentityResolver,
	canceller RunCanceller,
	bus EventBus,
) *GroupLauncher {
	switch {
	case groups == nil:
		panic("engine: NewGroupLauncher requires a non-nil GroupRunRepository")
	case starter == nil:
		panic("engine: NewGroupLauncher requires a non-nil GroupTaskStarter")
	case identities == nil:
		panic("engine: NewGroupLauncher requires a non-nil IdentityResolver")
	case canceller == nil:
		panic("engine: NewGroupLauncher requires a non-nil RunCanceller")
	case bus == nil:
		panic("engine: NewGroupLauncher requires a non-nil EventBus")
	}
	return &GroupLauncher{
		groups:     groups,
		starter:    starter,
		contracts:  contracts,
		workspaces: workspaces,
		groupWS:    groupWS,
		identities: identities,
		canceller:  canceller,
		bus:        bus,
		now:        time.Now,
	}
}

// launchTask is one task fully prepared for launch.
type launchTask struct {
	spec workflow.GroupTask
	row  instance.GroupTask
	vars map[string]any
	dir  string
}

// launchPrep tracks pre-launch side effects so a refusal leaves nothing behind.
type launchPrep struct {
	l        *GroupLauncher
	ws       GroupWorkspace // the capability that took the snapshot
	snapshot string
	dirs     []uuid.UUID
}

func (p *launchPrep) cleanup(ctx context.Context) {
	for _, id := range p.dirs {
		if err := p.l.workspaces.RemoveInstanceDir(id); err != nil {
			slog.Warn("parallel_group: removing a prepared task area failed", "taskInstanceID", id, "error", err)
		}
	}
	if p.snapshot != "" && p.ws != nil {
		if err := p.ws.ReleaseSnapshot(ctx, p.snapshot); err != nil {
			slog.Warn("parallel_group: releasing an unused snapshot failed", "snapshot", p.snapshot, "error", err)
		}
	}
}

func preLaunchError(err error) error {
	return fmt.Errorf("%w: %w", ErrGroupPreLaunch, err)
}

// Launch prepares and starts every task of the group described by req.
//
// PRE-LAUNCH (nothing persisted; any problem returns an error wrapping
// ErrGroupPreLaunch after removing what was prepared):
//
//	owner identity -> start variables captured ONCE -> byte budget ->
//	parent area snapshot ->
//	per task: reserved id, own directory, composed variables, child contract ->
//	fork the snapshot into every task directory
//
// POST-PERSIST (the run and its full roster exist; never an error):
//
//	CreateWithTasks -> per task, in author order: StartGroupTask + MarkTaskLaunched
//	-> TransitionState(launching -> running)   // the launch barrier opens LAST
//
// A launch failure part-way records the task launch_failed, decides failure
// (DecideLaunchFailure), cancels the tasks already started under the owner
// identity and returns the run: the group's failure path is routed once.
func (l *GroupLauncher) Launch(ctx context.Context, req GroupLaunchRequest) (*instance.GroupRun, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := checkLaunchRequest(req); err != nil {
		return nil, preLaunchError(err)
	}
	parent := req.Parent
	nodeID := req.Node.ID

	// Reconstruct the PARENT OWNER's identity, as SpawnWorkflowExecutor does. No
	// fallback: a system identity would change what the tasks may see and reach.
	ownerIdentity, err := l.identities.IdentityForUser(ctx, parent.OwnerID)
	if err != nil {
		return nil, preLaunchError(fmt.Errorf("resolve the parent owner's identity: %w", err))
	}
	ownerCtx := identity.WithIdentity(ctx, ownerIdentity)

	start := captureStartVariables(req.Vars)
	parentWS, _ := WorkDirFromVars(req.Vars)
	ws, repoRel, err := l.launchWorkspace(ctx, req, parentWS)
	if err != nil {
		return nil, preLaunchError(err)
	}
	if parentWS != "" && (l.workspaces == nil || ws == nil) {
		return nil, preLaunchError(errors.New("the parent run has a working area but no workspace snapshot/fork " +
			"capability is wired; refusing to start tasks that would share the parent's files"))
	}

	if err := l.checkLaunchBudget(ctx, parentWS, len(req.Config.Tasks)); err != nil {
		return nil, preLaunchError(err)
	}

	runID := uuid.New()
	prep := &launchPrep{l: l, ws: ws}
	if parentWS != "" {
		handle, err := ws.SnapshotParent(ctx, runID, parentWS)
		if err != nil {
			return nil, preLaunchError(fmt.Errorf("snapshot the parent's working area: %w", err))
		}
		prep.snapshot = handle
	}

	tasks := make([]launchTask, 0, len(req.Config.Tasks))
	for i, spec := range req.Config.Tasks {
		lt, err := l.prepareTask(ownerCtx, req, runID, i, spec, start, parentWS, prep)
		if err != nil {
			prep.cleanup(ctx)
			return nil, preLaunchError(err)
		}
		tasks = append(tasks, lt)
	}
	if prep.snapshot != "" {
		for _, lt := range tasks {
			if err := ws.ForkTask(ctx, prep.snapshot, lt.dir); err != nil {
				prep.cleanup(ctx)
				return nil, preLaunchError(fmt.Errorf("task %q: fork the working area: %w", lt.row.Key, err))
			}
		}
	}

	run, err := newGroupRun(runID, req, start, prep.snapshot, tasks, l.now())
	if err != nil {
		prep.cleanup(ctx)
		return nil, preLaunchError(err)
	}
	if repoRel != "" {
		run.Config[GroupConfigKeyResolvedRepoPath] = repoRel
	}
	if err := l.groups.CreateWithTasks(ctx, run); err != nil {
		prep.cleanup(ctx)
		if errors.Is(err, instance.ErrGroupRunExists) {
			return l.existingRun(ctx, parent.ID, nodeID, err)
		}
		return nil, preLaunchError(fmt.Errorf("persist the group run and its roster: %w", err))
	}
	return l.launchAll(ctx, ownerCtx, run, tasks, string(parent.Priority)), nil
}

func checkLaunchRequest(req GroupLaunchRequest) error {
	switch {
	case req.Parent == nil || req.Parent.ID == uuid.Nil:
		return errors.New("no parent instance with an id was supplied")
	case req.Node.ID == "":
		return errors.New("no group node id was supplied")
	case req.Vars == nil:
		return errors.New("no variable store was supplied (the parent's live store at the group node is required)")
	case req.Ownership == nil:
		return errors.New("no validated group ownership (workflow.AnalyzeGroups) was supplied")
	case req.Ownership.NodeID != req.Node.ID:
		return fmt.Errorf("the supplied ownership describes group %q, not %q", req.Ownership.NodeID, req.Node.ID)
	case len(req.Config.Tasks) == 0:
		return workflow.ErrGroupNoTasks
	case len(req.Config.Tasks) > workflow.MaxGroupTasks:
		// ParseGroupConfig already refuses this; re-checked here because every
		// task below costs a full copy of the parent's working area.
		return fmt.Errorf("%w: %d tasks, at most %d", workflow.ErrGroupTooManyTasks, len(req.Config.Tasks), workflow.MaxGroupTasks)
	}
	return nil
}

// captureStartVariables is the ONE capture of the group-start variables: a
// deep copy of the store's snapshot (Snapshot is shallow) minus reserved names.
func captureStartVariables(vars *variable.VariableStore) map[string]any {
	start := variable.DeepCopyMap(vars.Snapshot())
	for k := range variable.ReservedInputNames {
		delete(start, k)
	}
	return start
}

func (l *GroupLauncher) prepareTask(ownerCtx context.Context, req GroupLaunchRequest, runID uuid.UUID, position int,
	spec workflow.GroupTask, start map[string]any, parentWS string, prep *launchPrep) (launchTask, error) {
	row := instance.GroupTask{
		GroupRunID:      runID,
		Position:        position,
		Key:             spec.Key,
		ChildInstanceID: uuid.New(),
		LaunchState:     instance.GroupTaskPending,
		LiveStatus:      instance.GroupTaskStatusNotStarted,
		Blocking:        true,
	}
	switch req.Config.Form {
	case workflow.GroupFormBranches:
		if spec.Entry == "" || spec.Workflow != "" {
			return launchTask{}, fmt.Errorf("task %q: %w: a branches task names an entry and no workflow", spec.Key, workflow.ErrGroupTaskTargetInvalid)
		}
		if !containsString(req.Ownership.Bodies[spec.Key], spec.Entry) {
			return launchTask{}, fmt.Errorf("task %q: entry %q is not part of the task's validated body", spec.Key, spec.Entry)
		}
		row.Kind = instance.GroupTaskKindBranch
		row.Origin = spec.Entry
	case workflow.GroupFormWorkflows:
		if spec.Workflow == "" || spec.Entry != "" {
			return launchTask{}, fmt.Errorf("task %q: %w: a workflows task names a workflow and no entry", spec.Key, workflow.ErrGroupTaskTargetInvalid)
		}
		row.Kind = instance.GroupTaskKindWorkflow
		row.Origin = spec.Workflow
	default:
		return launchTask{}, fmt.Errorf("%w: %q", workflow.ErrGroupFormInvalid, req.Config.Form)
	}

	dir := ""
	if parentWS != "" {
		// Ensure may create the directory before failing (e.g. on chmod).
		prep.dirs = append(prep.dirs, row.ChildInstanceID)
		d, err := l.workspaces.EnsureInstanceDir(row.ChildInstanceID)
		if err != nil {
			return launchTask{}, fmt.Errorf("task %q: prepare its working area: %w", spec.Key, err)
		}
		dir = d
	}

	vars, err := ComposeTaskVariables(start, spec, dir, parentWS)
	if err != nil {
		return launchTask{}, err
	}

	if row.Kind == instance.GroupTaskKindWorkflow {
		if l.contracts == nil {
			return launchTask{}, fmt.Errorf("task %q: no child-contract reader is wired, so the declared inputs of %q cannot be checked before launch",
				spec.Key, spec.Workflow)
		}
		fields, err := l.contracts.ChildInputs(ownerCtx, spec.Workflow)
		if err != nil {
			return launchTask{}, fmt.Errorf("task %q: load child workflow %q: %w", spec.Key, spec.Workflow, err)
		}
		if err := checkChildContract(spec.Workflow, fields, vars); err != nil {
			return launchTask{}, fmt.Errorf("task %q: %w", spec.Key, err)
		}
	}
	return launchTask{spec: spec, row: row, vars: vars, dir: dir}, nil
}

func newGroupRun(id uuid.UUID, req GroupLaunchRequest, start map[string]any, snapshot string, tasks []launchTask, now time.Time) (*instance.GroupRun, error) {
	raw, err := json.Marshal(req.Config)
	if err != nil {
		return nil, fmt.Errorf("snapshot the group config: %w", err)
	}
	var cfgSnap map[string]any
	if err := json.Unmarshal(raw, &cfgSnap); err != nil {
		return nil, fmt.Errorf("snapshot the group config: %w", err)
	}
	bodies := make(map[string][]string, len(req.Ownership.Bodies))
	for k, v := range req.Ownership.Bodies {
		bodies[k] = append([]string(nil), v...)
	}
	roster := make([]instance.GroupTask, len(tasks))
	for i, lt := range tasks {
		roster[i] = lt.row
	}
	return &instance.GroupRun{
		ID:                 id,
		ParentInstanceID:   req.Parent.ID,
		NodeID:             req.Node.ID,
		Attempt:            1,
		Form:               req.Config.Form,
		CompletionRule:     req.Config.CompletionRule,
		UnfinishedBehavior: req.Config.UnfinishedBehavior,
		GatherMode:         req.Config.GatherMode,
		Config:             cfgSnap,
		BodyOwnership:      bodies,
		StartVariables:     start,
		WorkspaceSnapshot:  snapshot,
		State:              instance.GroupRunLaunching,
		CreatedAt:          now,
		UpdatedAt:          now,
		Tasks:              roster,
	}, nil
}

// existingRun handles losing the (parent, node, attempt) uniqueness race: the
// other launch's run is the one that counts, and this call starts nothing.
func (l *GroupLauncher) existingRun(ctx context.Context, parentID uuid.UUID, nodeID string, cause error) (*instance.GroupRun, error) {
	existing, err := l.groups.GetByParentNode(ctx, parentID, nodeID)
	if err != nil {
		return nil, preLaunchError(fmt.Errorf("parallel_group %q: %w, and reading it back failed: %w", nodeID, cause, err))
	}
	if existing == nil {
		return nil, preLaunchError(fmt.Errorf("parallel_group %q: %w, but it could not be read back", nodeID, cause))
	}
	return existing, nil
}

func (l *GroupLauncher) launchAll(ctx, ownerCtx context.Context, run *instance.GroupRun, tasks []launchTask, priority string) *instance.GroupRun {
	for i, lt := range tasks {
		req := GroupTaskStartRequest{
			TaskInstanceID:   lt.row.ChildInstanceID,
			ParentInstanceID: run.ParentInstanceID,
			ParentNodeID:     run.NodeID,
			GroupRunID:       run.ID,
			TaskKey:          lt.row.Key,
			Kind:             lt.row.Kind,
			Variables:        lt.vars,
			Priority:         priority,
		}
		if lt.row.Kind == instance.GroupTaskKindWorkflow {
			req.ChildWorkflow = lt.spec.Workflow
		} else {
			req.BranchEntry = lt.spec.Entry
		}
		if err := l.starter.StartGroupTask(ownerCtx, req); err != nil {
			return l.failLaunch(ctx, ownerCtx, run, tasks, i, fmt.Errorf("task %q: %w", lt.row.Key, err))
		}
		if err := l.groups.MarkTaskLaunched(ctx, run.ID, lt.row.Key); err != nil {
			return l.failLaunch(ctx, ownerCtx, run, tasks, i,
				fmt.Errorf("task %q started but its launch could not be recorded: %w", lt.row.Key, err))
		}
		if decided := l.decidedElsewhere(ctx, run); decided != nil {
			return l.stopAfterDecision(ctx, ownerCtx, decided, tasks, i)
		}
	}
	// The launch barrier opens only now: every task is registered and started.
	if _, err := l.groups.TransitionState(ctx, run.ID, instance.GroupRunLaunching, instance.GroupRunRunning); err != nil {
		slog.Error("parallel_group: opening the launch barrier failed; the group coordinator completes it",
			"groupRunID", run.ID, "error", err)
	}
	l.releaseResultsSnapshot(ctx, run)
	publishGroupEvent(l.bus, run, EventTypeGroupLaunched, map[string]any{"tasks": len(tasks)}, l.now())
	return l.reload(ctx, run)
}

// decidedElsewhere re-reads the run after a task's launch is recorded and
// returns it if another writer decided it on a partial roster (an interrupted
// launch seen by the coordinator, or a dead parent). The coordinator cancels
// only the tasks its post-decision read shows. A task recorded after that read
// is invisible to it, but this re-read follows the record, so it sees the
// decision: between the two, every started task is cancelled by one side or
// the other. A decision frozen from the complete roster (taken after the last
// record opened the barrier) is the group's own: its unfinished-task behaviour,
// applied by the coordinator, stands and the launch simply ends.
func (l *GroupLauncher) decidedElsewhere(ctx context.Context, run *instance.GroupRun) *instance.GroupRun {
	current, err := l.groups.Get(ctx, run.ID)
	if err != nil {
		slog.Warn("parallel_group: re-reading the roster during launch failed; continuing the launch",
			"groupRunID", run.ID, "error", err)
		return nil
	}
	if !decidedOnPartialRoster(current.Decision) {
		return nil
	}
	return current
}

// stopAfterDecision ends a launch that another writer decided while tasks were
// still starting: no further task starts, and the tasks this launcher started
// are cancelled here because the decider may not have seen them.
func (l *GroupLauncher) stopAfterDecision(ctx, ownerCtx context.Context, run *instance.GroupRun, tasks []launchTask, last int) *instance.GroupRun {
	ctx = context.WithoutCancel(ctx)
	ownerCtx = context.WithoutCancel(ownerCtx)
	slog.Warn("parallel_group: the group was decided while it was still launching; stopping the launch",
		"groupRunID", run.ID, "startedTasks", last+1, "rosterSize", len(tasks))
	for _, lt := range tasks[:last+1] {
		if t, ok := run.TaskByKey(lt.row.Key); !ok || !t.CancelRequested {
			if err := l.groups.MarkTaskCancelRequested(ctx, run.ID, lt.row.Key); err != nil {
				slog.Error("parallel_group: recording a cancellation request failed", "groupRunID", run.ID, "taskKey", lt.row.Key, "error", err)
			}
		}
		l.cancelTask(ownerCtx, run, lt.row)
	}
	for _, lt := range tasks[last+1:] {
		if lt.dir == "" {
			continue
		}
		if err := l.workspaces.RemoveInstanceDir(lt.row.ChildInstanceID); err != nil {
			slog.Warn("parallel_group: removing a never-started task's area failed", "taskInstanceID", lt.row.ChildInstanceID, "error", err)
		}
	}
	l.releaseResultsSnapshot(ctx, run)
	return l.reload(ctx, run)
}

// failLaunch turns a partial launch into a durable failure decision.
func (l *GroupLauncher) failLaunch(ctx, ownerCtx context.Context, run *instance.GroupRun, tasks []launchTask, failed int, cause error) *instance.GroupRun {
	// A parent cancellation also cancels Launch's context. The roster already
	// exists, so compensation must finish with the same owner identity even
	// after that context/deadline ends, rather than defer leaked work to a sweep.
	ctx = context.WithoutCancel(ctx)
	ownerCtx = context.WithoutCancel(ownerCtx)
	failedRow := tasks[failed].row
	reason := "launch failed: " + cause.Error()
	if err := l.groups.MarkTaskLaunchFailed(ctx, run.ID, failedRow.Key, cause.Error()); err != nil {
		slog.Error("parallel_group: recording a task launch failure failed", "groupRunID", run.ID, "taskKey", failedRow.Key, "error", err)
	}
	// The failed task's reserved child may exist (created, then enqueue failed;
	// or started but unrecorded). Stop it; an absent row is the normal case.
	l.cancelTask(ownerCtx, run, failedRow)

	current, err := l.groups.Get(ctx, run.ID)
	if err != nil {
		slog.Error("parallel_group: re-reading the roster after a launch failure failed; deciding from memory",
			"groupRunID", run.ID, "error", err)
		current = launchFailureFallback(run, failed, cause.Error())
	}
	decision := DecideLaunchFailure(current, reason, l.now())
	won, err := l.groups.Decide(ctx, run.ID, *decision)
	switch {
	case err != nil:
		slog.Error("parallel_group: recording the launch-failure decision failed; the group coordinator decides it",
			"groupRunID", run.ID, "error", err)
	case won:
		publishGroupEvent(l.bus, current, EventTypeGroupDecided, decisionEventData(decision), l.now())
	}

	for _, lt := range tasks[:failed] {
		if err := l.groups.MarkTaskCancelRequested(ctx, run.ID, lt.row.Key); err != nil {
			slog.Error("parallel_group: recording a cancellation request failed", "groupRunID", run.ID, "taskKey", lt.row.Key, "error", err)
		}
		l.cancelTask(ownerCtx, run, lt.row)
	}
	for _, lt := range tasks[failed+1:] {
		if lt.dir == "" {
			continue
		}
		if err := l.workspaces.RemoveInstanceDir(lt.row.ChildInstanceID); err != nil {
			slog.Warn("parallel_group: removing a never-started task's area failed", "taskInstanceID", lt.row.ChildInstanceID, "error", err)
		}
	}
	l.releaseResultsSnapshot(ctx, run)
	return l.reload(ctx, run)
}

// launchFailureFallback reconstructs the roster in memory when it cannot be
// re-read: tasks before failed launched, failed launch_failed, the rest pending.
func launchFailureFallback(run *instance.GroupRun, failed int, msg string) *instance.GroupRun {
	cp := *run
	cp.Tasks = append([]instance.GroupTask(nil), run.Tasks...)
	for i := range cp.Tasks {
		switch {
		case i < failed:
			cp.Tasks[i].LaunchState = instance.GroupTaskLaunched
		case i == failed:
			cp.Tasks[i].LaunchState = instance.GroupTaskLaunchFailed
			cp.Tasks[i].LaunchError = msg
		}
	}
	return &cp
}

func (l *GroupLauncher) cancelTask(ownerCtx context.Context, run *instance.GroupRun, t instance.GroupTask) {
	err := l.canceller.Cancel(ownerCtx, t.ChildInstanceID)
	if err == nil || errors.Is(err, instance.ErrCannotCancel) || errors.Is(err, instance.ErrNotFound) {
		return
	}
	slog.Warn("parallel_group: cancelling a task after a failed launch failed; the group coordinator retries",
		"groupRunID", run.ID, "taskKey", t.Key, "taskInstanceID", t.ChildInstanceID, "error", err)
}

// releaseResultsSnapshot frees the group-start copy once forking is over. A
// file-gathering group keeps it: Phase 3 merges against that baseline.
func (l *GroupLauncher) releaseResultsSnapshot(ctx context.Context, run *instance.GroupRun) {
	if run.WorkspaceSnapshot == "" || l.groupWS == nil || run.GatherMode == workflow.GroupGatherResultsAndFiles {
		return
	}
	if err := l.groupWS.ReleaseSnapshot(ctx, run.WorkspaceSnapshot); err != nil {
		slog.Warn("parallel_group: releasing the group-start snapshot failed", "groupRunID", run.ID, "error", err)
	}
}

func (l *GroupLauncher) reload(ctx context.Context, run *instance.GroupRun) *instance.GroupRun {
	fresh, err := l.groups.Get(ctx, run.ID)
	if err != nil {
		slog.Warn("parallel_group: re-reading the launched run failed", "groupRunID", run.ID, "error", err)
		return run
	}
	return fresh
}

// checkChildContract verifies, before enqueue, that a workflow task's composed
// variables satisfy the child's DECLARED inputs: every required input present
// and non-null (validateChildInputs, shared with the spawn preparer), and every
// supplied declared input of a recognized type carrying that type.
func checkChildContract(childWorkflow string, fields []workflow.InputField, vars map[string]any) error {
	if err := validateChildInputs(childWorkflow, fields, vars); err != nil {
		return err
	}
	for _, f := range fields {
		v, ok := vars[f.Name]
		if !ok || v == nil {
			continue
		}
		if !inputTypeMatches(f.Type, v) {
			return fmt.Errorf("%w: child %q input %q is declared %s but the task supplies %s",
				ErrChildContractUnsatisfied, childWorkflow, f.Name, f.Type, describeInputType(v))
		}
	}
	return nil
}

func inputTypeMatches(declared string, v any) bool {
	switch declared {
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number":
		switch reflect.ValueOf(v).Kind() {
		case reflect.Float32, reflect.Float64,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return true
		}
		return false
	case "object":
		return reflect.ValueOf(v).Kind() == reflect.Map
	case "array":
		k := reflect.ValueOf(v).Kind()
		return k == reflect.Slice || k == reflect.Array
	default:
		// An undeclared or unrecognized type carries no runtime check.
		return true
	}
}

func describeInputType(v any) string {
	switch reflect.ValueOf(v).Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Map:
		return "an object"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Float32, reflect.Float64, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a number"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
