# Parallel Workflow Gathering — Phase 3 Task 5: Group-task terminal finalization and workspace retention

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Before a group task's run is marked completed, its workspace is cleaned up, or its group is notified, record the task's files as an immutable working-area ref and persist it on the roster — failing the task (never completing it) when that cannot be done — and keep task workspaces that the gather or a still-running descendant needs.
**Architecture:** A new port `GroupTaskFinalizer` (`FinalizeTerminal`, `RetainWorkspace`) is injected with `Engine.SetGroupTaskFinalizer`. `Engine.run`'s success tail calls it for group-task instances (`Instance.GroupRunID != nil`) BEFORE `SetCompleted`; an error records a failed `NodeExecution` ("group file finalization failed: …") on the last executed node and goes through `failInstance` (which still fires `OnChildTerminal`), unless the run was cancelled meanwhile. After `SetCompleted`, `cleanupWorkspace` is skipped when the finalizer says the gather still needs the workspace or the task has any non-terminal descendant (walked with P2-T12 `SpawnLinkRepository.ListChildren` through terminal intermediates). The production implementation `GroupFileFinalizer` finalizes only tasks of `results_and_files` runs that are still undecided and still blocking.
**Tech Stack:** Go 1.25.5, `log/slog`, `github.com/google/uuid`, testify; domain fakes only (no adapters).
**Spec / Design:** spec "Gathering file changes" (all-succeed combines all tasks' changes; first-success gathers only the winner; independent tasks cannot alter the parent's area); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-3.md` §1 bullet 2 ("Before a successful task terminal transition, finalize its working area into an immutable mergeable ref/tree … Persist the task's ref before terminal notification and before normal workspace removal"), §2 ("Add a terminal-finalization/retention hook **before** `Engine.run` calls `SetCompleted`, `cleanupWorkspace`, and `OnChildTerminal` … Snapshot/finalization failure cannot publish a successful file-gather candidate without its required files; record an error/non-success"), §5 (retain resources while descendants run), Data Provenance row "Final refs retained before task completion/cleanup" (**P3-B**, engine half).

**Interfaces**
- Consumes:
  - Existing `domain/engine/engine.go`: `Engine` struct (lines 23-64; field `workspaceManager WorkspaceManager` at line 40), `Engine.run` (lines 788-982; loop starts at 876, node execution at 945-947, success tail at 964-981), `failInstance` (992-1002: `SetFailed` + `warnWorkspaceRetained` + `OnChildTerminal`), `emitNotification` (1245), `EventTypeInstanceFailed`; `domain/engine/workspace.go:51-59` `cleanupWorkspace`, `:68-88` `warnWorkspaceRetained`; `domain/engine/ports.go:27-40` `WorkspaceManager.EnsureInstanceDir`; `domain/instance/ports.go:58-69` `NodeExecutionRepository.GetByInstanceAndNode`/`Create`; `domain/instance/spawn_link.go:76` `MaxSpawnDepth`; `domain/notification/notification.go:18` `TypeFailed`.
  - P1-T1 `domain/workflow`: `GroupGatherResultsAndFiles`.
  - P2-T1 `domain/instance`: `Instance.GroupRunID *uuid.UUID`, `GroupTask{Key, ChildInstanceID, Blocking}`, `GroupRun{ID, GatherMode, State, WorkspaceSnapshot, Tasks}`, states `GroupRunLaunching|Running|Decided|Done`, `GroupRunRepository.Get`.
  - P2-T12: `instance.ChildRef{ID, State}`, `SpawnLinkRepository.ListChildren(ctx, parentID) ([]ChildRef, error)`, `NoopSpawnLinks.ListChildren` (always empty); the engine's `e.spawnLinks` (never nil, `cancel_cascade.go:50-55` `SetSpawnLinks`).
  - P2-T13: `GroupTerminalHandler`, `Engine.SetGroupTerminalHandler` (tests use it to observe `OnChildTerminal`'s group dispatch).
  - P3-T2: `engine.WorkingArea.FinalizeTask(ctx, snapshotHandle string, taskInstanceID uuid.UUID, taskDir string) (string, error)`, `GatherHandle`, `MergeOutcome`.
  - P3-T4: `GroupRunRepository.SetTaskFinalizedRef(ctx, runID uuid.UUID, key, ref string) error`, `SetGatherReport` (fake only).
- Produces (package `engine`, file `domain/engine/group_finalize.go`):
  - `type GroupTaskFinalizer interface { FinalizeTerminal(ctx context.Context, inst *instance.Instance, succeeded bool) error; RetainWorkspace(ctx context.Context, inst *instance.Instance) bool }`
  - `func (e *Engine) SetGroupTaskFinalizer(f GroupTaskFinalizer)` (nil = disabled, today's behaviour)
  - `var ErrGroupFileFinalization = errors.New("group file finalization failed")`
  - `type GroupFileFinalizer struct{ … }`; `func NewGroupFileFinalizer(groups instance.GroupRunRepository, area WorkingArea, workspaces WorkspaceManager) *GroupFileFinalizer` (panics on any nil dependency)
  - `func (f *GroupFileFinalizer) FinalizeTerminal(ctx context.Context, inst *instance.Instance, succeeded bool) error`
  - `func (f *GroupFileFinalizer) RetainWorkspace(ctx context.Context, inst *instance.Instance) bool`
  - Engine-internal: field `groupFinalizer GroupTaskFinalizer`; `finalizeGroupTask`, `recordFinalizationFailure`, `retainGroupTaskWorkspace`, `hasActiveDescendants`.
  - Behaviour later tasks rely on: P3-T6 can trust that every task of a successful `results_and_files` decision that completed has a non-empty `GroupTask.FinalizedRef` (a missing one means the finalizer was not wired — P3-T6 routes failure); a task whose finalization failed is FAILED, so `GroupCoordinator` (P2-T10) sees a failure with error text `group file finalization failed: …` in its trace; files-mode task workspaces survive until the run is `done` (P3-T10 prunes them).

**Wiring & data provenance**
- Composition root: **P3-T11** adds, in `cmd/arbitron/main.go:initDomain` after the working area (P3-T2) and group-run repository (P2-T14) exist and before `eng.FreezeExecutors()`: `eng.SetGroupTaskFinalizer(engine.NewGroupFileFinalizer(groupRunRepo, workingArea, wsMgr))` (`wsMgr` is main.go:814's `filesystem.NewWorkspaceManager(cfg.Workspace.Path)`). P3-T11's `cmd/arbitron/group_files_e2e_test.go` fails without that line: the gatherer finds no `FinalizedRef` and the group routes `failure` with nothing merged.
- Producers of every input:
  - `inst` — `Engine.run`'s own `inst` (engine.go:824, `e.instances.Get`), whose `GroupRunID`/`GroupTaskKey` were written by P2-T7 `Engine.StartGroupTask` (persisted by P2-T2's instance columns).
  - `run` — `GroupRunRepository.Get(*inst.GroupRunID)` → `adapter/sqlite/group_run_repo.go:GroupRunRepo.Get` (P2-T3, hydrated by P3-T4).
  - `run.WorkspaceSnapshot` — P2-T8 `GroupLauncher.Launch` from P3-T2 `gitarea.WorkingArea.SnapshotParent`.
  - task row — the roster row whose `ChildInstanceID == inst.ID` (reserved by P2-T8 before enqueue); its `Blocking` is cleared by `Decide` (P2-T3/T10) for every task at decision time.
  - task directory — `WorkspaceManager.EnsureInstanceDir(inst.ID)` (`adapter/filesystem/workspace.go:59-75`), the same directory `Engine.prepareWorkspace` (`workspace.go:17-43`) seeded as `{{workspace}}` and P3-T2 `ForkTask` filled.
  - descendants — `e.spawnLinks.ListChildren` (`*adaptsqlite.InstanceRepo`, wired at main.go:886 `eng.SetSpawnLinks(instanceRepo)`; P2-T12).
- Provenance probes: `TestGroupFinalize_FilesTaskFinalizesAndPersistsRefBeforeCompleting` asserts the handle, task id and directory reaching `FinalizeTask` come from the run and the WorkspaceManager and that the SINK (the repo's `FinalizedRef`) is written before `SetCompleted`; `TestGroupFinalize_MissingSnapshotHandleFailsTask` fails if an empty producer value is silently accepted; `TestGroupFinalize_NoFinalizerWiredKeepsPlainCompletion` documents that the hook is live only through `SetGroupTaskFinalizer`.

**Decisions settled here**
- Eligibility: finalize iff `succeeded` ∧ `GroupRunID` set ∧ run `GatherMode == results_and_files` ∧ run state ∈ {`launching`, `running`} ∧ the task's roster row is still `Blocking`. A task finishing after its group decided (first-success loser continuing independently, or a cancel racing completion) can never be merged, so it is not finalized and is not failed.
- Failure recording: the error is stored as a failed `NodeExecution` on the LAST node the loop executed, attempt = that node's latest attempt + 1, `Error = {"error": "group file finalization failed: …", "phase": "group_file_finalization"}`; `FailedNodeID` = that node (so `Retry` re-runs the sink node and finalization again). Then `failInstance` (→ `OnChildTerminal` → group coordinator), a `failed` notification and an `instance.failed` event — the same trio `handleNodeError`'s halt branch emits (engine.go:1064-1073).
- Cancellation: if the run context is cancelled while finalizing (the task was cancelled), the engine halts WITHOUT changing state, mirroring `handleNodeError`'s cancellation guard (engine.go:1019-1026); the cancel path owns the terminal transition.
- Retention: `retainGroupTaskWorkspace` = finalizer's `RetainWorkspace` (files-mode run not yet done; unreadable run ⇒ retain) OR any non-terminal descendant. The descendant walk lives in the engine (it needs `e.spawnLinks`, which every composition root already wires) so `NewGroupFileFinalizer` keeps its three-argument form; lookup errors retain. Non-group instances and engines without a finalizer keep exactly today's cleanup.

### Task 5: GroupTaskFinalizer hook in Engine.run
**Files:**
- Create: `domain/engine/group_finalize.go`
- Modify: `domain/engine/engine.go` — struct field after line 40 (`workspaceManager WorkspaceManager`); `run`: declare `lastNodeID` before the loop (line 876), set it before `executeWithRetry` (lines 945-947), and the success tail (lines 964-977). Line numbers are those of the file before Phase 2; P2-T7/P2-T12/P2-T13 edit other parts of `engine.go`, so locate each edit by the quoted anchor text.
- Test:   `domain/engine/group_finalize_test.go` (package `engine_test`; finalizer unit tests + fakes)
- Test:   `domain/engine/group_finalize_engine_test.go` (package `engine_test`; through `Engine.run`)

Tests reuse existing package fakes: `newTestFixtures`, `mockInstanceRepo`, `mockNodeExecutionRepo`, `mockEventBus.eventsOfType`, `mockQueueManager`, `fakeWorkspaceManager` (`removedIDs`), `newMockNotificationRepo`/`getCreated`, `waitForInstance`, `linearWorkflow` (all `domain/engine/engine_test.go`), and `bgAdmin` (`domain/engine/engine_list_test.go:94`).

#### Cycle 1 — the finalizer

- [x] Step 1: Write the failing unit tests and fakes. Create `domain/engine/group_finalize_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errUnusedInFinalizeFake = errors.New("not used by the finalizer tests")

// callLog records cross-fake call order.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, s)
}

func (l *callLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// finalizeGroupRepo is an in-memory instance.GroupRunRepository keyed by run
// id; only the methods the finalizer uses are live, the rest fail loudly.
type finalizeGroupRepo struct {
	mu        sync.Mutex
	runs      map[uuid.UUID]*instance.GroupRun
	getErr    error
	setRefErr error
	log       *callLog
}

func newFinalizeGroupRepo(log *callLog, runs ...*instance.GroupRun) *finalizeGroupRepo {
	r := &finalizeGroupRepo{runs: map[uuid.UUID]*instance.GroupRun{}, log: log}
	for _, run := range runs {
		r.runs[run.ID] = run
	}
	return r
}

func (r *finalizeGroupRepo) Get(_ context.Context, id uuid.UUID) (*instance.GroupRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	run, ok := r.runs[id]
	if !ok {
		return nil, fmt.Errorf("group run %s: %w", id, instance.ErrGroupRunNotFound)
	}
	cp := *run
	cp.Tasks = append([]instance.GroupTask(nil), run.Tasks...)
	return &cp, nil
}

func (r *finalizeGroupRepo) SetTaskFinalizedRef(_ context.Context, runID uuid.UUID, key, ref string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log.add("SetTaskFinalizedRef:" + key + "=" + ref)
	if r.setRefErr != nil {
		return r.setRefErr
	}
	run, ok := r.runs[runID]
	if !ok {
		return instance.ErrGroupRunNotFound
	}
	for i := range run.Tasks {
		if run.Tasks[i].Key == key {
			run.Tasks[i].FinalizedRef = ref
			return nil
		}
	}
	return instance.ErrGroupRunNotFound
}

func (r *finalizeGroupRepo) finalizedRef(runID uuid.UUID, key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.runs[runID].Tasks {
		if t.Key == key {
			return t.FinalizedRef
		}
	}
	return ""
}

func (r *finalizeGroupRepo) CreateWithTasks(context.Context, *instance.GroupRun) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) GetByParentNode(context.Context, uuid.UUID, string) (*instance.GroupRun, error) {
	return nil, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) GetByChild(context.Context, uuid.UUID) (*instance.GroupRun, error) {
	return nil, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) ListByParent(context.Context, uuid.UUID) ([]instance.GroupRun, error) {
	return nil, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) ListUnsettled(context.Context) ([]uuid.UUID, error) {
	return nil, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) MarkTaskLaunched(context.Context, uuid.UUID, string) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) MarkTaskLaunchFailed(context.Context, uuid.UUID, string, string) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) TransitionState(context.Context, uuid.UUID, instance.GroupRunState, instance.GroupRunState) (bool, error) {
	return false, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) RecordTaskTerminal(context.Context, uuid.UUID, string, instance.GroupTaskStatus, map[string]any, map[string]any, time.Time) (int64, bool, error) {
	return 0, false, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) UpdateTaskLiveStatus(context.Context, uuid.UUID, string, instance.GroupTaskStatus) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) Decide(context.Context, uuid.UUID, instance.GroupDecision) (bool, error) {
	return false, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) MarkTaskCancelRequested(context.Context, uuid.UUID, string) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) MarkTaskContinuingIndependently(context.Context, uuid.UUID, string) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) MarkActionsApplied(context.Context, uuid.UUID) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) SetFinalOutput(context.Context, uuid.UUID, string, map[string]any, instance.GroupRunState) (bool, error) {
	return false, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) MarkContinuationDelivered(context.Context, uuid.UUID) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) SetGatherReport(context.Context, uuid.UUID, instance.GatherReport) error {
	return errUnusedInFinalizeFake
}

// finalizeCall is one FinalizeTask invocation.
type finalizeCall struct {
	Handle string
	TaskID uuid.UUID
	Dir    string
}

// fakeWorkingArea is an engine.WorkingArea whose FinalizeTask records its
// load-bearing arguments and returns refs/arbitron/tasks/<taskID>.
type fakeWorkingArea struct {
	mu          sync.Mutex
	calls       []finalizeCall
	finalizeErr error
	onFinalize  func(ctx context.Context) error
	log         *callLog
}

func (a *fakeWorkingArea) FinalizeTask(ctx context.Context, handle string, taskID uuid.UUID, dir string) (string, error) {
	a.mu.Lock()
	a.calls = append(a.calls, finalizeCall{Handle: handle, TaskID: taskID, Dir: dir})
	hook, ferr := a.onFinalize, a.finalizeErr
	a.mu.Unlock()
	a.log.add("FinalizeTask")
	if hook != nil {
		if err := hook(ctx); err != nil {
			return "", err
		}
	}
	if ferr != nil {
		return "", ferr
	}
	return "refs/arbitron/tasks/" + taskID.String(), nil
}

func (a *fakeWorkingArea) finalizeCalls() []finalizeCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]finalizeCall(nil), a.calls...)
}

func (a *fakeWorkingArea) SnapshotParent(context.Context, uuid.UUID, string) (string, error) {
	return "", errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) ForkTask(context.Context, string, string) error {
	return errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) ReleaseSnapshot(context.Context, string) error {
	return errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) BeginGather(context.Context, string, uuid.UUID, string) (engine.GatherHandle, error) {
	return engine.GatherHandle{}, errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) MergeTask(context.Context, engine.GatherHandle, string) (engine.MergeOutcome, error) {
	return engine.MergeOutcome{}, errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) ConflictPaths(context.Context, engine.GatherHandle) ([]string, error) {
	return nil, errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) CommitResolution(context.Context, engine.GatherHandle, string) error {
	return errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) Apply(context.Context, engine.GatherHandle) error {
	return errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) Rollback(context.Context, engine.GatherHandle) error {
	return errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) ReleaseRef(context.Context, string, string) error {
	return errUnusedInFinalizeFake
}

func TestNewGroupFileFinalizer_RequiresEveryDependency(t *testing.T) {
	log := &callLog{}
	groups, area, ws := newFinalizeGroupRepo(log), &fakeWorkingArea{log: log}, &fakeWorkspaceManager{}
	assert.Panics(t, func() { engine.NewGroupFileFinalizer(nil, area, ws) })
	assert.Panics(t, func() { engine.NewGroupFileFinalizer(groups, nil, ws) })
	assert.Panics(t, func() { engine.NewGroupFileFinalizer(groups, area, nil) })
	assert.NotPanics(t, func() { engine.NewGroupFileFinalizer(groups, area, ws) })
}

func TestGroupFileFinalizer_UnitCases(t *testing.T) {
	ctx := context.Background()
	log := &callLog{}
	runID, taskID := uuid.New(), uuid.New()
	run := &instance.GroupRun{ID: runID, GatherMode: workflow.GroupGatherResultsAndFiles, State: instance.GroupRunRunning,
		WorkspaceSnapshot: runID.String(), Tasks: []instance.GroupTask{{Key: "a", ChildInstanceID: taskID, Blocking: true}}}
	groups := newFinalizeGroupRepo(log, run)
	area := &fakeWorkingArea{log: log}
	fin := engine.NewGroupFileFinalizer(groups, area, &fakeWorkspaceManager{})
	key := "a"
	task := &instance.Instance{ID: taskID, GroupRunID: &runID, GroupTaskKey: &key}

	require.NoError(t, fin.FinalizeTerminal(ctx, task, false), "failed work is never finalized")
	require.NoError(t, fin.FinalizeTerminal(ctx, nil, true))
	require.NoError(t, fin.FinalizeTerminal(ctx, &instance.Instance{ID: uuid.New()}, true), "non-group instance")
	assert.Empty(t, area.finalizeCalls())

	stranger := &instance.Instance{ID: uuid.New(), GroupRunID: &runID, GroupTaskKey: &key}
	err := fin.FinalizeTerminal(ctx, stranger, true)
	require.ErrorIs(t, err, engine.ErrGroupFileFinalization, "an instance missing from the roster")
	assert.Contains(t, err.Error(), "not on the roster")

	groups.getErr = errors.New("db gone")
	require.ErrorIs(t, fin.FinalizeTerminal(ctx, task, true), engine.ErrGroupFileFinalization)
	assert.True(t, fin.RetainWorkspace(ctx, task), "an unreadable run is retained")
	groups.getErr = nil

	assert.True(t, fin.RetainWorkspace(ctx, task))
	assert.False(t, fin.RetainWorkspace(ctx, &instance.Instance{ID: uuid.New()}))
	assert.False(t, fin.RetainWorkspace(ctx, nil))
	groups.runs[runID].State = instance.GroupRunDone
	assert.False(t, fin.RetainWorkspace(ctx, task))
	groups.runs[runID].State = instance.GroupRunGathering
	groups.runs[runID].GatherMode = workflow.GroupGatherResults
	assert.False(t, fin.RetainWorkspace(ctx, task))

	area.finalizeErr = errors.New("git exploded")
	groups.runs[runID].GatherMode = workflow.GroupGatherResultsAndFiles
	groups.runs[runID].State = instance.GroupRunLaunching
	err = fin.FinalizeTerminal(ctx, task, true)
	require.ErrorIs(t, err, engine.ErrGroupFileFinalization)
	assert.Contains(t, err.Error(), "git exploded")
}
```

- [x] Step 2: Run them; confirm they fail to compile.

Confirmed undefined NewGroupFileFinalizer and ErrGroupFileFinalization before implementation.

```bash
go test ./domain/engine/ -count=1 -run 'GroupFileFinalizer'
```
Expected:
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/group_finalize_test.go:229:35: undefined: engine.NewGroupFileFinalizer
domain/engine/group_finalize_test.go:230:35: undefined: engine.NewGroupFileFinalizer
...
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Add the engine field. In `domain/engine/engine.go`, in `type Engine struct`, directly after the line `	workspaceManager WorkspaceManager` (line 40), add:

```go
	// groupFinalizer is the optional group-task terminal hook (P3-T5); nil
	// keeps the plain completion path.
	groupFinalizer GroupTaskFinalizer
```

- [x] Step 4: Implement the finalizer. Create `domain/engine/group_finalize.go`:

```go
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
// (the default) keeps today's completion path for every instance.
func (e *Engine) SetGroupTaskFinalizer(f GroupTaskFinalizer) {
	e.groupFinalizer = f
}

// GroupFileFinalizer is the production GroupTaskFinalizer: for a task of a
// results_and_files group that is still blocking an undecided run, it records
// the task directory as an immutable working-area ref and persists that ref on
// the roster row before the task completes.
type GroupFileFinalizer struct {
	groups     instance.GroupRunRepository
	area       WorkingArea
	workspaces WorkspaceManager
}

var _ GroupTaskFinalizer = (*GroupFileFinalizer)(nil)

// NewGroupFileFinalizer builds the finalizer. Every dependency is required: a
// nil one is a composition-root bug that would silently publish file-gather
// candidates without their files, so it panics.
func NewGroupFileFinalizer(groups instance.GroupRunRepository, area WorkingArea, workspaces WorkspaceManager) *GroupFileFinalizer {
	if groups == nil || area == nil || workspaces == nil {
		panic("engine: NewGroupFileFinalizer requires a GroupRunRepository, a WorkingArea and a WorkspaceManager")
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
	task := rosterTaskFor(run, inst.ID)
	if task == nil {
		return fmt.Errorf("%w: instance %s is not on the roster of group run %s", ErrGroupFileFinalization, inst.ID, run.ID)
	}
	if !task.Blocking || (run.State != instance.GroupRunLaunching && run.State != instance.GroupRunRunning) {
		return nil
	}
	if run.WorkspaceSnapshot == "" {
		return fmt.Errorf("%w: group run %s has no workspace snapshot to finalize task %q against", ErrGroupFileFinalization, run.ID, task.Key)
	}
	dir, err := f.workspaces.EnsureInstanceDir(inst.ID)
	if err != nil {
		return fmt.Errorf("%w: task %q workspace: %v", ErrGroupFileFinalization, task.Key, err)
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
// results_and_files run keeps its workspace until the run is done (the gather
// and any interactive resolution may still inspect it). An unreadable run is
// retained.
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
	return run.GatherMode == workflow.GroupGatherResultsAndFiles && run.State != instance.GroupRunDone
}

// rosterTaskFor returns the roster row whose reserved child id is childID.
func rosterTaskFor(run *instance.GroupRun, childID uuid.UUID) *instance.GroupTask {
	for i := range run.Tasks {
		if run.Tasks[i].ChildInstanceID == childID {
			return &run.Tasks[i]
		}
	}
	return nil
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
	if err == nil {
		return true
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return false
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
```

- [x] Step 5: Run the unit tests; confirm they pass.

```bash
go test ./domain/engine/ -count=1 -run 'GroupFileFinalizer'
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/engine	0.XXXs
```

#### Cycle 2 — the hook in Engine.run

- [x] Step 6: Write the failing engine tests. Create `domain/engine/group_finalize_engine_test.go`:

```go
package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// orderedInstanceRepo logs SetCompleted / SetFailed into the shared call log.
type orderedInstanceRepo struct {
	*mockInstanceRepo
	log *callLog
}

func (r *orderedInstanceRepo) SetCompleted(ctx context.Context, id uuid.UUID) error {
	r.log.add("SetCompleted")
	return r.mockInstanceRepo.SetCompleted(ctx, id)
}

func (r *orderedInstanceRepo) SetFailed(ctx context.Context, id uuid.UUID, node string) error {
	r.log.add("SetFailed:" + node)
	return r.mockInstanceRepo.SetFailed(ctx, id, node)
}

// finalizeGroupHandler is the P2-T13 GroupTerminalHandler seam.
type finalizeGroupHandler struct{ log *callLog }

func (h finalizeGroupHandler) OnTaskTerminal(_ context.Context, childID uuid.UUID) {
	h.log.add("OnTaskTerminal:" + childID.String())
}

// finalizeSpawnLinks serves ListChildren from a parent -> children map; every
// other SpawnLinkRepository method comes from the Null Object.
type finalizeSpawnLinks struct {
	engine.NoopSpawnLinks
	children map[uuid.UUID][]instance.ChildRef
	err      error
}

func (l finalizeSpawnLinks) ListChildren(_ context.Context, parentID uuid.UUID) ([]instance.ChildRef, error) {
	if l.err != nil {
		return nil, l.err
	}
	return l.children[parentID], nil
}

type finalizeFixture struct {
	*testFixtures
	log       *callLog
	groups    *finalizeGroupRepo
	area      *fakeWorkingArea
	ws        *fakeWorkspaceManager
	notifs    *mockNotificationRepo
	instances *orderedInstanceRepo
	run       *instance.GroupRun
	taskID    uuid.UUID
}

// newFinalizeFixture builds an engine (same mocks as newTestFixtures, with the
// ordering instance repo) carrying a group run with one task "a" whose child
// instance is pre-seeded at state queued, ready for StartInstance.
func newFinalizeFixture(t *testing.T, gatherMode string, state instance.GroupRunState, blocking bool) *finalizeFixture {
	t.Helper()
	base := newTestFixtures()
	log := &callLog{}
	ordered := &orderedInstanceRepo{mockInstanceRepo: base.instanceRepo, log: log}
	executors := map[workflow.NodeType]engine.NodeExecutor{workflow.NodeTypeScript: base.executor}
	eng := engine.NewEngine(base.workflowRepo, ordered, base.nodeExecRepo, base.snapshotRepo, base.userRepo,
		executors, base.eventBus, base.queueManager, base.queueRepo, "")
	base.engine = eng
	// newTestFixtures bound Enqueue to ITS engine; re-bind it to this one.
	base.queueManager.mu.Lock()
	base.queueManager.enqueueFn = func(ctx context.Context, id uuid.UUID, _ uuid.UUID) (bool, error) {
		if err := eng.StartInstance(ctx, id); err != nil {
			return false, err
		}
		return true, nil
	}
	base.queueManager.mu.Unlock()

	ws := &fakeWorkspaceManager{}
	eng.SetWorkspaceManager(ws)
	notifs := newMockNotificationRepo()
	eng.SetNotifications(notifs)
	eng.SetGroupTerminalHandler(finalizeGroupHandler{log: log})

	parentID, runID, taskID := uuid.New(), uuid.New(), uuid.New()
	run := &instance.GroupRun{
		ID: runID, ParentInstanceID: parentID, NodeID: "grp", Attempt: 1,
		GatherMode: gatherMode, State: state, WorkspaceSnapshot: runID.String(),
		Tasks: []instance.GroupTask{{GroupRunID: runID, Key: "a", Kind: instance.GroupTaskKindBranch,
			ChildInstanceID: taskID, Blocking: blocking}},
	}
	groups := newFinalizeGroupRepo(log, run)
	area := &fakeWorkingArea{log: log}
	eng.SetGroupTaskFinalizer(engine.NewGroupFileFinalizer(groups, area, ws))

	seedGroupTask(t, base.instanceRepo, linearWorkflow(), taskID, parentID, runID, "a")
	return &finalizeFixture{testFixtures: base, log: log, groups: groups, area: area, ws: ws,
		notifs: notifs, instances: ordered, run: run, taskID: taskID}
}

func seedGroupTask(t *testing.T, repo *mockInstanceRepo, wf *workflow.Workflow, id, parentID, runID uuid.UUID, key string) {
	t.Helper()
	raw, err := json.Marshal(wf)
	require.NoError(t, err)
	var snap map[string]any
	require.NoError(t, json.Unmarshal(raw, &snap))
	node := "grp"
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: uuid.New(), WorkflowName: wf.Name, WorkflowSnapshot: snap,
		State: instance.StateQueued, Priority: instance.PriorityNormal, Variables: map[string]any{},
		ParentInstanceID: &parentID, ParentNodeID: &node, SpawnMode: instance.SpawnModeGroupTask,
		GroupRunID: &runID, GroupTaskKey: &key, GroupTaskKind: instance.GroupTaskKindBranch,
		CreatedAt: now, UpdatedAt: now,
	}))
}

func (f *finalizeFixture) start(t *testing.T, states ...instance.State) *instance.Instance {
	t.Helper()
	require.NoError(t, f.engine.StartInstance(context.Background(), f.taskID))
	return waitForInstance(t, f.instanceRepo, f.taskID, 5*time.Second, states...)
}

// waitForLog waits until the call log holds want (the OnChildTerminal
// dispatch runs right after the terminal write).
func (f *finalizeFixture) waitForLog(t *testing.T, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, c := range f.log.snapshot() {
			if c == want {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "missing %q in %v", want, f.log.snapshot())
}

func TestGroupFinalize_FilesTaskFinalizesAndPersistsRefBeforeCompleting(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunRunning, true)
	f.start(t, instance.StateCompleted)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())

	ref := "refs/arbitron/tasks/" + f.taskID.String()
	assert.Equal(t, []string{
		"FinalizeTask",
		"SetTaskFinalizedRef:a=" + ref,
		"SetCompleted",
		"OnTaskTerminal:" + f.taskID.String(),
	}, f.log.snapshot(), "the ref is durable before the task completes and before its group is notified")

	require.Len(t, f.area.finalizeCalls(), 1)
	call := f.area.finalizeCalls()[0]
	assert.Equal(t, f.run.WorkspaceSnapshot, call.Handle, "finalized against the run's snapshot handle")
	assert.Equal(t, f.taskID, call.TaskID)
	assert.Equal(t, "/tmp/arbitron-test-ws/"+f.taskID.String(), call.Dir, "the task's own workspace from WorkspaceManager")
	assert.Equal(t, ref, f.groups.finalizedRef(f.run.ID, "a"))
	assert.Empty(t, f.ws.removedIDs(), "a files-mode task keeps its workspace until its run is done")
}

func TestGroupFinalize_FinalizeFailureFailsTaskAndStillNotifiesGroup(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunRunning, true)
	f.area.finalizeErr = errors.New("disk full")
	inst := f.start(t, instance.StateFailed, instance.StateCompleted)
	require.Equal(t, instance.StateFailed, inst.State)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())

	require.NotNil(t, inst.FailedNodeID)
	assert.Equal(t, "C", *inst.FailedNodeID, "recorded against the last node that ran")
	assert.NotContains(t, f.log.snapshot(), "SetCompleted")
	assert.Empty(t, f.groups.finalizedRef(f.run.ID, "a"), "no candidate ref is published")

	var finalizeExec *instance.NodeExecution
	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), f.taskID)
	require.NoError(t, err)
	for i := range execs {
		if execs[i].Status == instance.NodeStatusFailed {
			finalizeExec = &execs[i]
		}
	}
	require.NotNil(t, finalizeExec)
	assert.Equal(t, "C", finalizeExec.NodeID)
	assert.Equal(t, 2, finalizeExec.Attempt, "one attempt after C's successful run")
	msg, _ := finalizeExec.Error["error"].(string)
	assert.True(t, strings.HasPrefix(msg, "group file finalization failed: "), msg)
	assert.Contains(t, msg, "disk full")

	assert.Len(t, f.eventBus.eventsOfType(f.taskID, engine.EventTypeInstanceFailed), 1)
	assert.Empty(t, f.eventBus.eventsOfType(f.taskID, engine.EventTypeInstanceCompleted))
	var types []notification.Type
	for _, n := range f.notifs.getCreated() {
		types = append(types, n.Type)
	}
	assert.Equal(t, []notification.Type{notification.TypeFailed}, types)
	assert.Empty(t, f.ws.removedIDs(), "a failed task's workspace is retained for inspection")
}

func TestGroupFinalize_PersistFailureFailsTask(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunRunning, true)
	f.groups.setRefErr = errors.New("database is locked")
	inst := f.start(t, instance.StateFailed, instance.StateCompleted)
	assert.Equal(t, instance.StateFailed, inst.State)
	assert.NotContains(t, f.log.snapshot(), "SetCompleted")
}

func TestGroupFinalize_MissingSnapshotHandleFailsTask(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunRunning, true)
	f.groups.runs[f.run.ID].WorkspaceSnapshot = ""
	inst := f.start(t, instance.StateFailed, instance.StateCompleted)
	assert.Equal(t, instance.StateFailed, inst.State)
	assert.Empty(t, f.area.finalizeCalls())
	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), f.taskID)
	require.NoError(t, err)
	var msgs []string
	for _, e := range execs {
		if m, ok := e.Error["error"].(string); ok {
			msgs = append(msgs, m)
		}
	}
	assert.Contains(t, strings.Join(msgs, "|"), "no workspace snapshot")
}

func TestGroupFinalize_ResultsOnlyGroupIsUntouched(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResults, instance.GroupRunRunning, true)
	f.start(t, instance.StateCompleted)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())
	assert.Empty(t, f.area.finalizeCalls())
	assert.Equal(t, []uuid.UUID{f.taskID}, f.ws.removedIDs(), "results-only tasks are cleaned up as before")
}

func TestGroupFinalize_TaskFinishingAfterDecisionIsNotFinalized(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunDecided, false)
	f.start(t, instance.StateCompleted)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())
	assert.Empty(t, f.area.finalizeCalls(), "a late/independent task can never become a merge candidate")
	assert.Empty(t, f.groups.finalizedRef(f.run.ID, "a"))
}

func TestGroupFinalize_DoneFilesRunReleasesWorkspace(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunDone, false)
	f.start(t, instance.StateCompleted)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())
	assert.Equal(t, []uuid.UUID{f.taskID}, f.ws.removedIDs())
}

func TestGroupFinalize_ActiveDescendantRetainsWorkspace(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResults, instance.GroupRunRunning, true)
	terminalChild, grandchild := uuid.New(), uuid.New()
	f.engine.SetSpawnLinks(finalizeSpawnLinks{children: map[uuid.UUID][]instance.ChildRef{
		f.taskID:      {{ID: terminalChild, State: instance.StateCompleted}},
		terminalChild: {{ID: grandchild, State: instance.StateRunning}},
	}})
	f.start(t, instance.StateCompleted)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())
	assert.Empty(t, f.ws.removedIDs(), "a running grandchild below a completed intermediate keeps the task workspace")
}

func TestGroupFinalize_DescendantLookupFailureRetainsWorkspace(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResults, instance.GroupRunRunning, true)
	f.engine.SetSpawnLinks(finalizeSpawnLinks{err: errors.New("db down")})
	f.start(t, instance.StateCompleted)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())
	assert.Empty(t, f.ws.removedIDs())
}

func TestGroupFinalize_AllDescendantsTerminalReleasesWorkspace(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResults, instance.GroupRunRunning, true)
	child := uuid.New()
	f.engine.SetSpawnLinks(finalizeSpawnLinks{children: map[uuid.UUID][]instance.ChildRef{
		f.taskID: {{ID: child, State: instance.StateFailed}},
	}})
	f.start(t, instance.StateCompleted)
	f.waitForLog(t, "OnTaskTerminal:"+f.taskID.String())
	assert.Equal(t, []uuid.UUID{f.taskID}, f.ws.removedIDs())
}

func TestGroupFinalize_CancelDuringFinalizationLeavesTaskCancelled(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunRunning, true)
	f.area.onFinalize = func(ctx context.Context) error {
		if err := f.engine.Cancel(bgAdmin(), f.taskID); err != nil {
			return err
		}
		<-ctx.Done()
		return fmt.Errorf("git working area: finalize task: %w", ctx.Err())
	}
	inst := f.start(t, instance.StateCancelled, instance.StateFailed, instance.StateCompleted)
	assert.Equal(t, instance.StateCancelled, inst.State)
	assert.NotContains(t, f.log.snapshot(), "SetCompleted")
	for _, c := range f.log.snapshot() {
		assert.False(t, strings.HasPrefix(c, "SetFailed"), "cancellation is not a finalization failure: %v", f.log.snapshot())
	}
}

// Regression: a run that is not a group task never reaches the finalizer.
func TestGroupFinalize_NonGroupInstanceUnchanged(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunRunning, true)
	f.workflowRepo.add(linearWorkflow())
	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Empty(t, f.area.finalizeCalls())
	assert.Equal(t, []uuid.UUID{id}, f.ws.removedIDs())
	assert.Equal(t, []string{"SetCompleted"}, f.log.snapshot())
}

// Provenance: with no finalizer wired, a files-mode task completes exactly as
// before (and nothing is finalized) — the hook is live only through
// SetGroupTaskFinalizer, which P3-T11 calls in main.go.
func TestGroupFinalize_NoFinalizerWiredKeepsPlainCompletion(t *testing.T) {
	f := newFinalizeFixture(t, workflow.GroupGatherResultsAndFiles, instance.GroupRunRunning, true)
	f.engine.SetGroupTaskFinalizer(nil)
	f.start(t, instance.StateCompleted)
	assert.Empty(t, f.area.finalizeCalls())
	assert.Equal(t, []uuid.UUID{f.taskID}, f.ws.removedIDs())
}
```

- [x] Step 7: Run them; confirm the hook-dependent tests fail (the regression guards — results-only, decided run, done run, all descendants terminal, non-group instance, no finalizer — already pass, proving today's behaviour is the baseline):

```bash
go test ./domain/engine/ -count=1 -run 'GroupFinalize' 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
```
Expected:
```
--- FAIL: TestGroupFinalize_FilesTaskFinalizesAndPersistsRefBeforeCompleting (0.01s)
--- FAIL: TestGroupFinalize_FinalizeFailureFailsTaskAndStillNotifiesGroup (0.01s)
--- FAIL: TestGroupFinalize_PersistFailureFailsTask (0.01s)
--- FAIL: TestGroupFinalize_MissingSnapshotHandleFailsTask (0.01s)
--- FAIL: TestGroupFinalize_ActiveDescendantRetainsWorkspace (0.01s)
--- FAIL: TestGroupFinalize_DescendantLookupFailureRetainsWorkspace (0.01s)
--- FAIL: TestGroupFinalize_CancelDuringFinalizationLeavesTaskCancelled (0.01s)
FAIL
FAIL	github.com/arbitron/arbitron/domain/engine	0.XXXs
```
with, for the first, `expected: []string{"FinalizeTask", "SetTaskFinalizedRef:a=refs/arbitron/tasks/<id>", "SetCompleted", "OnTaskTerminal:<id>"}` / `actual  : []string{"SetCompleted", "OnTaskTerminal:<id>"}`.

- [x] Step 8: Edit `Engine.run` in `domain/engine/engine.go` (three edits).

Both planned TDD cycles were confirmed: unit tests failed on missing finalizer types; exactly the seven planned hook-dependent engine tests then failed before the run-loop edits. All became green after implementation.

  1. Immediately before the loop header `	for currentNodeID != "" {` (line 876), insert:
     ```go
     	// lastNodeID is the last node the loop executed; a group finalization
     	// failure after the loop is recorded against it.
     	var lastNodeID string

     ```
  2. Replace (lines 945-946)
     ```go
     		// Execute the node with retry logic. Each attempt is individually recorded.
     		execCtx := ContextWithInstanceID(ctx, instanceID)
     ```
     with
     ```go
     		// Execute the node with retry logic. Each attempt is individually recorded.
     		lastNodeID = currentNodeID
     		execCtx := ContextWithInstanceID(ctx, instanceID)
     ```
  3. Replace the success tail (lines 964-977)
     ```go
     	// All nodes executed successfully.
     	if err := e.instances.SetCompleted(ctx, instanceID); err != nil {
     		slog.Error("failed to set instance completed", "instanceID", instanceID, "error", err)
     		// Persistence failed: keep the workspace for debugging/retry. Log
     		// the retention with size/file-count so the operator can see how
     		// much sensitive data is sitting on disk.
     		e.warnWorkspaceRetained(instanceID, "set_completed_failed")
     	} else {
     		e.emitNotification(ctx, instanceID, workflowName, notification.TypeCompleted)
     		// Workspace is removed only after SetCompleted succeeds.
     		e.cleanupWorkspace(instanceID)
     		// A completed run may be somebody's awaited child.
     		e.OnChildTerminal(context.Background(), instanceID)
     	}
     ```
     with
     ```go
     	// All nodes executed successfully. A group task first records what its
     	// group needs from its working area (P3-T5): this MUST precede
     	// SetCompleted, cleanupWorkspace and OnChildTerminal, or a file gather
     	// would be told about a task whose files were already deleted.
     	if !e.finalizeGroupTask(ctx, inst, workflowName, lastNodeID) {
     		return
     	}
     	if err := e.instances.SetCompleted(ctx, instanceID); err != nil {
     		slog.Error("failed to set instance completed", "instanceID", instanceID, "error", err)
     		// Persistence failed: keep the workspace for debugging/retry. Log
     		// the retention with size/file-count so the operator can see how
     		// much sensitive data is sitting on disk.
     		e.warnWorkspaceRetained(instanceID, "set_completed_failed")
     	} else {
     		e.emitNotification(ctx, instanceID, workflowName, notification.TypeCompleted)
     		// Workspace is removed only after SetCompleted succeeds, and never
     		// while its group gather or a live descendant still needs it.
     		if e.retainGroupTaskWorkspace(ctx, inst) {
     			e.warnWorkspaceRetained(instanceID, "group_task_retained")
     		} else {
     			e.cleanupWorkspace(instanceID)
     		}
     		// A completed run may be somebody's awaited child.
     		e.OnChildTerminal(context.Background(), instanceID)
     	}
     ```
     The `e.eventBus.Publish(instanceID, Event{Type: EventTypeInstanceCompleted, …})` block after it (lines 978-981) is unchanged. `finalizeGroupTask` returns true immediately for non-group instances and when no finalizer is wired, so every other run walks exactly the old path.

- [x] Step 9: Run the new tests, the race detector on them, the whole engine package (regression), gofmt and vet.

Completion pass after P3-T11 (`62faa02`): the production root constructs `GroupFileFinalizer(groupRunRepo, workingArea, wsMgr)` and installs it on the engine. The same real Git WorkingArea feeds the launcher and gatherer. The all-succeed, first-success/late-loser, failed-group retention and nested-gather root tests pass with `-race`, proving actual task file writes reach persisted downstream parent-file readings, while late/failed work stays isolated and debugging workspaces survive.

Required verification passed: focused verbose tests, focused `-race -count=1` and `-race -count=10`, whole engine package, `go build ./...`, `gofmt -l domain/engine/`, `go vet ./domain/engine/`, and `git diff --check`. No production or test changes were necessary; this pass closes the previously blocked verification step only.

Completion self-review, in the required order:
1. Spec compliance: reviewed the finalizer and Engine.run against eligibility, ref-before-terminal ordering, failure trace/notification/event, cancellation, and gather/descendant retention contracts. All are covered; no extra behavior was added.
2. Anti-green-theater: temporarily removed only `initDomain`'s `SetGroupTaskFinalizer` call. The all-succeed root test failed: the parent routed failure instead of success, and the downstream file-reading node never ran. Restored the call byte-for-byte. These tests use actual workflow file producers, SQLite roster refs and persisted parent variables, not fabricated refs or empty inputs.
3. Code quality: checked neighboring workspace cleanup/error conventions and the startup setter; no cleanup was needed. After review and restoring the mutation, reran focused race tests ten times, the entire engine package and all four root sink tests with `-race`; all pass. Build, formatting, vet and diff checks are clean.

> RESOLVED (P3-T11; historical blocker follows): All specified local verifications pass, but the mission's wired-and-fed completion gate depends on P3-T11. `cmd/arbitron/main.go:990-1002` still constructs `filesystem.NewGroupWorkspace` and enables only `GroupSupportResults`; it neither constructs the Git WorkingArea nor calls `SetGroupTaskFinalizer`. The existing production Engine now feeds its own group-task metadata/workspace to the hook, and the finalizer writes `SetTaskFinalizedRef`, but that hook remains disabled at the real composition root until P3-T11 wires the shared WorkingArea into launcher/finalizer/gatherer and proves the downstream files/report sink. P3-T6's gatherer does not exist yet. Do not enable file mode prematurely or substitute domain fakes for the planned root test. Leave this completion gate unchecked until those dependencies land; skip re-failing it on intervening passes and proceed to independent P3-T6 steps.

Selection: skipped the already-recorded P3-T3/P3-T4 production-wiring blockers without repeating their failures. P2-T11 has no unchecked step (its historical prose only mentions checkbox syntax).

Verification this pass: focused verbose tests pass; focused `-race -count=10` passes; the entire engine package passes; `go build ./...`, `gofmt -l domain/engine/`, `go vet ./domain/engine/`, and `git diff --check` are clean.

Self-review, in the required order:
1. Spec compliance: matched the eligibility predicate, roster-child addressing, durable ref-before-completion ordering, last-node failure trace/attempt/phase, failure notification/event, and gather/descendant retention. A red/green regression exposed cancellation being overwritten when the finalizer returned success after cancellation; the engine now checks cancellation before accepting even a nil error. No extra modes/options or premature composition changes were added.
2. Anti-green-theater: engine tests assert the stored roster ref, final task state, failure trace, notifications/events, and workspace-removal sink. Added distinct run/key/child/snapshot/directory provenance cases and eligibility/workspace-error coverage; unused fake methods fail loudly and the ref fake rejects empty input. Temporarily disabled only Engine.run's finalization call: the existing engine success/sink test failed on missing finalization. Restored the call; no mutation remains. Real-root data provenance is explicitly blocked above, not claimed from these domain tests.
3. Code quality: reused `GroupRun.TaskByChild` instead of duplicating roster lookup, followed neighboring lifecycle/error handling, documented startup-only setter configuration, and registered engine shutdown for test fixtures. Reran the focused/race/package checks after review; no adapter dependencies were introduced into the domain tests.

```bash
go test ./domain/engine/ -count=1 -v -run 'GroupFinalize|GroupFileFinalizer' 2>&1 | grep -E '^(--- |ok|FAIL)'
go test ./domain/engine/ -count=1 -race -run 'GroupFinalize|GroupFileFinalizer'
go test ./domain/engine/ -count=1
gofmt -l domain/engine/
go vet ./domain/engine/
```
Expected (`gofmt -l` and `go vet` print nothing):
```
--- PASS: TestGroupFinalize_FilesTaskFinalizesAndPersistsRefBeforeCompleting (0.01s)
--- PASS: TestGroupFinalize_FinalizeFailureFailsTaskAndStillNotifiesGroup (0.01s)
--- PASS: TestGroupFinalize_PersistFailureFailsTask (0.01s)
--- PASS: TestGroupFinalize_MissingSnapshotHandleFailsTask (0.01s)
--- PASS: TestGroupFinalize_ResultsOnlyGroupIsUntouched (0.01s)
--- PASS: TestGroupFinalize_TaskFinishingAfterDecisionIsNotFinalized (0.01s)
--- PASS: TestGroupFinalize_DoneFilesRunReleasesWorkspace (0.01s)
--- PASS: TestGroupFinalize_ActiveDescendantRetainsWorkspace (0.01s)
--- PASS: TestGroupFinalize_DescendantLookupFailureRetainsWorkspace (0.01s)
--- PASS: TestGroupFinalize_AllDescendantsTerminalReleasesWorkspace (0.01s)
--- PASS: TestGroupFinalize_CancelDuringFinalizationLeavesTaskCancelled (0.01s)
--- PASS: TestGroupFinalize_NonGroupInstanceUnchanged (0.01s)
--- PASS: TestGroupFinalize_NoFinalizerWiredKeepsPlainCompletion (0.01s)
--- PASS: TestNewGroupFileFinalizer_RequiresEveryDependency (0.00s)
--- PASS: TestGroupFileFinalizer_UnitCases (0.00s)
ok  	github.com/arbitron/arbitron/domain/engine	0.XXXs
ok  	github.com/arbitron/arbitron/domain/engine	1.XXXs
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
```

- [x] Step 10: Commit.

The initial commit contained the unblocked implementation, domain regression tests and blocker evidence. This completion pass commits the resolved Step 9 verification and ordered self-review after the real composition-root dependency landed.

```bash
git add domain/engine/group_finalize.go domain/engine/group_finalize_test.go domain/engine/group_finalize_engine_test.go domain/engine/engine.go
git commit -m "feat(engine): finalize group-task files before completion and retain needed workspaces" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
