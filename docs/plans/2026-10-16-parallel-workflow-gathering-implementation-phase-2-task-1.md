# Parallel Workflow Gathering — Phase 2 Task 1: Group-run domain model, ports and group-task link fields

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Define the durable domain vocabulary of a parallel_group execution in `domain/instance` — the group run, its ordered task roster, the frozen decision, the `GroupRunRepository` and `NodeAddressedWaker` ports — and extend the child's spawn link (`SpawnMode`, `SpawnLink`, `Instance`) with the internal `group_task` mode and its group fields.
**Architecture:** One new pure file `domain/instance/group_run.go` (types, predicates, sentinels, two ports) plus additive edits to `spawn_link.go` (third `SpawnMode`, group fields on `SpawnLink`, stricter `Valid`) and `instance.go` (three group fields, `IsGroupTask`). `domain/instance` still imports nothing from `domain/workflow`: workflow vocabulary (form/rule/behaviour/gather mode) is carried as plain strings validated upstream by `workflow.ParseGroupConfig` (P1-T1). No storage, engine or adapter code changes here.
**Tech Stack:** Go 1.25.5, standard library (`context`, `errors`, `time`, `encoding/json` in tests), `github.com/google/uuid`, testify.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` (Parallel groups, Completion rules, Gathering results); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-2.md` §1 "Durable group-run record and repository" and Data Provenance row "Group persistence, first-terminal order, decision/side-effect claims" (PLANNED DEPENDENCY P2-A — this task supplies its domain half) and row "Prepare-before-enqueue, reserved IDs, snapshot branch trigger, group-task links" (P2-C — this task supplies `SpawnMode.Valid` + link fields).

## Atomic-terminal dependency

P2-T16 is the owner of production terminal capture: it extends migration 019 with a
single durable clock and terminal-state triggers and adapts `RecordTaskTerminal` to the
same allocator. Finish it before P2-T10/T13/T14. The callback is a wake accelerator;
wall-clock timestamp sorting and callback receipt order cannot determine first success.
No extra public field or port is introduced. P3-T5 still finalizes file refs BEFORE the
successful terminal write, so the atomic receipt cannot advertise success without files.


**Interfaces**
- Consumes (existing code only):
  - `domain/instance/instance.go:31-49` `type State string` + `StateQueued…StatePaused` + `func (s State) IsTerminal() bool`.
  - `domain/instance/spawn_link.go:16-32` `type SpawnMode string`, `SpawnModeFireAndForget`, `SpawnModeWaitForChild`, `func (m SpawnMode) Valid() bool`.
  - `domain/instance/spawn_link.go:49-59` `type SpawnLink struct { ParentInstanceID uuid.UUID; ParentNodeID string; Mode SpawnMode; SpawnDepth int }` and `:90-92` `func (l SpawnLink) Valid() bool`.
  - `domain/instance/instance.go:101-160` `type Instance struct` (`ParentInstanceID *uuid.UUID`, `ParentNodeID *string`, `SpawnMode SpawnMode`, `SpawnDepth int`), `:165-167` `func (i Instance) IsRoot() bool`.
- Produces (exact signatures; every name below is the contract's, plus the backward-compatible helpers marked *(added)*):
  - `const SpawnModeGroupTask SpawnMode = "group_task"`; `SpawnMode.Valid()` now admits it.
  - `type GroupTaskKind string`; `const GroupTaskKindBranch GroupTaskKind = "branch"`, `GroupTaskKindWorkflow GroupTaskKind = "workflow"`; *(added)* `func (k GroupTaskKind) Valid() bool`.
  - `SpawnLink` gains `GroupRunID uuid.UUID; GroupTaskKey string; GroupTaskKind GroupTaskKind`. `SpawnLink.Valid()`: for `Mode == SpawnModeGroupTask` additionally requires `GroupRunID != uuid.Nil && GroupTaskKey != "" && GroupTaskKind.Valid()`; for the two legacy modes it now also requires all three group fields to be zero.
  - `Instance` gains `GroupRunID *uuid.UUID; GroupTaskKey *string; GroupTaskKind GroupTaskKind`; *(added)* `func (i Instance) IsGroupTask() bool`.
  - `type GroupRunState string` with `GroupRunLaunching "launching"`, `GroupRunRunning "running"`, `GroupRunDecided "decided"`, `GroupRunGathering "gathering"`, `GroupRunResolvingConflicts "resolving_conflicts"`, `GroupRunDone "done"`; *(added)* `func (s GroupRunState) Valid() bool`, `func (s GroupRunState) IsDecidable() bool` (launching|running).
  - `type GroupTaskLaunchState string` with `GroupTaskPending "pending"`, `GroupTaskLaunched "launched"`, `GroupTaskLaunchFailed "launch_failed"`; *(added)* `func (s GroupTaskLaunchState) Valid() bool`.
  - `type GroupTaskStatus string` with `GroupTaskStatusNotStarted/Queued/Running/Waiting/Paused/Completed/Failed/Cancelled/Unfinished`; *(added)* `func (s GroupTaskStatus) Valid() bool`, `func (s GroupTaskStatus) IsLive() bool` (every status except `unfinished`), `func (s GroupTaskStatus) IsTerminal() bool` (completed|failed|cancelled — `unfinished` is NOT terminal).
  - `func GroupTaskStatusFromState(s State) GroupTaskStatus` (unknown/empty ⇒ `not_started`).
  - `type GroupTask struct`, `type FrozenTask struct` (JSON tags as contract), `type GroupDecision struct` (JSON tags as contract), `type GroupRun struct` — fields exactly as the contract; *(added)* `func (r *GroupRun) TaskByKey(key string) (*GroupTask, bool)`, `func (r *GroupRun) TaskByChild(childID uuid.UUID) (*GroupTask, bool)` (pointers INTO `r.Tasks`).
  - `var ErrGroupRunExists = errors.New("group run already exists for this parent node")`, `var ErrGroupRunNotFound = errors.New("group run not found")`, *(added)* `var ErrGroupTaskNotFound = errors.New("group task not found")`.
  - `type GroupRunRepository interface` — the 17 methods of the contract, verbatim.
  - `type NodeAddressedWaker interface { SetQueuedIfWaitingAtNode(ctx context.Context, id uuid.UUID, nodeID string, queuedAt time.Time) (bool, error) }` — declared here so P2-T10 can depend on it; implemented by `*sqlite.QueueRepo` in P2-T4.
- Deliberately NOT produced here: `ChildRef` and `SpawnLinkRepository.ListChildren` (owned by P2-T12 — declaring `ChildRef` here would collide with that task); `SpawnLinkRepository` is unchanged so every existing in-memory fake keeps compiling.

**Wiring & data provenance**
- Composition root: none of its own (pure domain types). They become live when P2-T14 wires `sqlite.NewGroupRunRepo` (P2-T3), the `*sqlite.QueueRepo` waker (P2-T4) and the group launcher/coordinator/executor into `cmd/arbitron/main.go:initDomain`.
- Producers of every field consumers will read:
  - `GroupRun` / `GroupTask` roster: built by `GroupLauncher.Launch` (P2-T8, `domain/engine/group_launcher.go`) from `workflow.ParseGroupConfig` (P1-T1) for `Form/CompletionRule/UnfinishedBehavior/GatherMode/Config/Tasks`, `workflow.AnalyzeGroups` (P1-T2) for `BodyOwnership`, `variable.DeepCopyMap(VariableStore.Snapshot())` (P2-T6, `domain/variable/deepcopy.go`; `variable.go:127`) for `StartVariables`, `engine.GroupWorkspace.SnapshotParent` (P2-T5) for `WorkspaceSnapshot`; persisted by `GroupRunRepository.CreateWithTasks` (P2-T3).
  - `GroupDecision` / `FrozenTask`: `engine.DecideGroup` / `DecideLaunchFailure` (P2-T9), persisted by `Decide` (P2-T3) from `GroupCoordinator.Evaluate` (P2-T10).
  - `Instance.GroupRunID/GroupTaskKey/GroupTaskKind` and `SpawnMode == SpawnModeGroupTask`: stamped by `Engine.StartGroupTask` (P2-T7) through `instance.WithSpawnLink` (`domain/instance/spawn_context.go:24`) and the `Instance` literal in `domain/engine/engine.go:505-560` (P2-T7 must copy the three group fields there), persisted/read by `adapter/sqlite/instance_repo.go:Create` / `Get` (P2-T2).
  - `GroupTaskStatusFromState` input: the child's `Instance.State` from `InstanceRepository.Get` (`domain/instance/ports.go:16`), read by the coordinator (P2-T10).
- Existing consumers affected by `SpawnMode.Valid()` admitting `group_task` (all verified):
  - `adapter/sqlite/instance_repo.go:84` (`Create`): until P2-T2 lands, a group-task `Instance` would try to write `spawn_mode='group_task'` and hit migration 018's CHECK. No caller creates one before P2-T7, and P2-T2 is the next task.
  - `adapter/sqlite/convert.go:253` (`nullStringToSpawnLink`): the column CHECK means `'group_task'` can never be read back; unaffected.
  - `domain/engine/engine_spawn.go:56` (`Engine.Spawn`): its links come from `SpawnWorkflowExecutor`, whose mode is validated by `workflow.IsValidSpawnMode` (`domain/workflow/workflow.go:131`) — only the two public modes — so a group-task link never reaches it; and a group-task link without group fields is now invalid anyway.
  - `domain/engine/engine.go:525` (`TriggerWithDepartment`): stamps parent/node/mode from a valid link; P2-T7 extends it with the group fields.
- Provenance probes: `TestSpawnLinkValid_GroupTaskRequiresGroupFields` fails if a producer (P2-T7) can build a group-task link without its run id / key / kind; `TestGroupTaskStatusFromStateTotality` fails if a new `State` lands unmapped.

### Task 1: Group-run domain model and group-task link fields
**Files:**
- Create: `domain/instance/group_run.go`
- Modify: `domain/instance/spawn_link.go:18-32` (`SpawnMode` const block + `Valid`), `:49-59` (`SpawnLink` struct), `:83-92` (`SpawnLink.Valid` + its doc comment)
- Modify: `domain/instance/instance.go:140-145` (the `SpawnDepth` field and its comment, inside `type Instance struct` at `:101-160`), after `:165-167` (`IsRoot`)
- Modify (test): `domain/instance/spawn_link_test.go:25-27` (`TestSpawnModeValid` cases), `:38-51` (`TestSpawnModeTotality`)
- Test: `domain/instance/group_run_test.go`

(Test files in this package mix stdlib `testing` and testify; the new file uses testify like `instance_test.go`.)

- [x] Step 1: Write the failing domain tests. Create `domain/instance/group_run_test.go`:

```go
package instance

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- wire values ----------

// The string values are persisted (group_runs.state, group_tasks.*,
// instances.group_task_kind) and exposed through the group output and Phase 4
// read models. A rename would orphan every stored row, so pin them.
func TestGroupVocabularyWireValues(t *testing.T) {
	assert.Equal(t, SpawnMode("group_task"), SpawnModeGroupTask)

	assert.Equal(t, GroupTaskKind("branch"), GroupTaskKindBranch)
	assert.Equal(t, GroupTaskKind("workflow"), GroupTaskKindWorkflow)

	assert.Equal(t, GroupRunState("launching"), GroupRunLaunching)
	assert.Equal(t, GroupRunState("running"), GroupRunRunning)
	assert.Equal(t, GroupRunState("decided"), GroupRunDecided)
	assert.Equal(t, GroupRunState("gathering"), GroupRunGathering)
	assert.Equal(t, GroupRunState("resolving_conflicts"), GroupRunResolvingConflicts)
	assert.Equal(t, GroupRunState("done"), GroupRunDone)

	assert.Equal(t, GroupTaskLaunchState("pending"), GroupTaskPending)
	assert.Equal(t, GroupTaskLaunchState("launched"), GroupTaskLaunched)
	assert.Equal(t, GroupTaskLaunchState("launch_failed"), GroupTaskLaunchFailed)

	assert.Equal(t, GroupTaskStatus("not_started"), GroupTaskStatusNotStarted)
	assert.Equal(t, GroupTaskStatus("queued"), GroupTaskStatusQueued)
	assert.Equal(t, GroupTaskStatus("running"), GroupTaskStatusRunning)
	assert.Equal(t, GroupTaskStatus("waiting"), GroupTaskStatusWaiting)
	assert.Equal(t, GroupTaskStatus("paused"), GroupTaskStatusPaused)
	assert.Equal(t, GroupTaskStatus("completed"), GroupTaskStatusCompleted)
	assert.Equal(t, GroupTaskStatus("failed"), GroupTaskStatusFailed)
	assert.Equal(t, GroupTaskStatus("cancelled"), GroupTaskStatusCancelled)
	assert.Equal(t, GroupTaskStatus("unfinished"), GroupTaskStatusUnfinished)
}

// ---------- validity predicates ----------

func TestGroupTaskKindValid(t *testing.T) {
	assert.True(t, GroupTaskKindBranch.Valid())
	assert.True(t, GroupTaskKindWorkflow.Valid())
	for _, k := range []GroupTaskKind{"", "Branch", "workflows", "branches", "spawn"} {
		assert.False(t, k.Valid(), "kind %q must be invalid", k)
	}
}

func TestGroupRunStateValidAndDecidable(t *testing.T) {
	all := []GroupRunState{GroupRunLaunching, GroupRunRunning, GroupRunDecided,
		GroupRunGathering, GroupRunResolvingConflicts, GroupRunDone}
	for _, s := range all {
		assert.True(t, s.Valid(), "%q must be valid", s)
	}
	for _, s := range []GroupRunState{"", "Done", "cancelled", "failed"} {
		assert.False(t, s.Valid(), "%q must be invalid", s)
	}

	decidable := map[GroupRunState]bool{GroupRunLaunching: true, GroupRunRunning: true}
	for _, s := range all {
		assert.Equal(t, decidable[s], s.IsDecidable(), "IsDecidable(%q)", s)
	}
}

func TestGroupTaskLaunchStateValid(t *testing.T) {
	for _, s := range []GroupTaskLaunchState{GroupTaskPending, GroupTaskLaunched, GroupTaskLaunchFailed} {
		assert.True(t, s.Valid(), "%q", s)
	}
	for _, s := range []GroupTaskLaunchState{"", "failed", "LAUNCHED"} {
		assert.False(t, s.Valid(), "%q", s)
	}
}

func TestGroupTaskStatusPredicates(t *testing.T) {
	cases := []struct {
		s        GroupTaskStatus
		valid    bool
		live     bool
		terminal bool
	}{
		{GroupTaskStatusNotStarted, true, true, false},
		{GroupTaskStatusQueued, true, true, false},
		{GroupTaskStatusRunning, true, true, false},
		{GroupTaskStatusWaiting, true, true, false},
		{GroupTaskStatusPaused, true, true, false},
		{GroupTaskStatusCompleted, true, true, true},
		{GroupTaskStatusFailed, true, true, true},
		{GroupTaskStatusCancelled, true, true, true},
		// unfinished is a decision-time verdict: valid in the frozen snapshot,
		// never a live child status, and NOT terminal.
		{GroupTaskStatusUnfinished, true, false, false},
		{"", false, false, false},
		{"done", false, false, false},
		{"Completed", false, false, false},
	}
	for _, c := range cases {
		assert.Equal(t, c.valid, c.s.Valid(), "Valid(%q)", c.s)
		assert.Equal(t, c.live, c.s.IsLive(), "IsLive(%q)", c.s)
		assert.Equal(t, c.terminal, c.s.IsTerminal(), "IsTerminal(%q)", c.s)
	}
}

// IsTerminal on the task status must agree with State.IsTerminal through the
// mapping, so the coordinator and the spawn code cannot disagree about "done".
func TestGroupTaskStatusTerminalAgreesWithState(t *testing.T) {
	for _, s := range allStates() {
		assert.Equal(t, s.IsTerminal(), GroupTaskStatusFromState(s).IsTerminal(), "state %q", s)
	}
}

// ---------- GroupTaskStatusFromState ----------

func allStates() []State {
	return []State{StateQueued, StateRunning, StateWaiting, StateCompleted,
		StateFailed, StateCancelled, StatePaused}
}

func TestGroupTaskStatusFromState(t *testing.T) {
	want := map[State]GroupTaskStatus{
		StateQueued:    GroupTaskStatusQueued,
		StateRunning:   GroupTaskStatusRunning,
		StateWaiting:   GroupTaskStatusWaiting,
		StatePaused:    GroupTaskStatusPaused,
		StateCompleted: GroupTaskStatusCompleted,
		StateFailed:    GroupTaskStatusFailed,
		StateCancelled: GroupTaskStatusCancelled,
	}
	for s, w := range want {
		assert.Equal(t, w, GroupTaskStatusFromState(s), "state %q", s)
	}
	assert.Equal(t, GroupTaskStatusNotStarted, GroupTaskStatusFromState(""),
		"no child row yet reads as not_started")
	assert.Equal(t, GroupTaskStatusNotStarted, GroupTaskStatusFromState("bogus"),
		"an unknown state never claims activity")
}

// Totality: a new State must not land without extending the mapping. Every
// declared state must map to a LIVE status other than not_started.
func TestGroupTaskStatusFromStateTotality(t *testing.T) {
	states := allStates()
	if len(states) != 7 {
		t.Fatalf("instance.State vocabulary changed to %d values: extend GroupTaskStatusFromState and this test", len(states))
	}
	for _, s := range states {
		got := GroupTaskStatusFromState(s)
		assert.True(t, got.IsLive(), "state %q must map to a live status", s)
		assert.NotEqual(t, GroupTaskStatusNotStarted, got, "state %q must not map to not_started", s)
	}
}

// ---------- SpawnLink validity for group tasks ----------

func validGroupLink() SpawnLink {
	return SpawnLink{
		ParentInstanceID: uuid.New(),
		ParentNodeID:     "group-1",
		Mode:             SpawnModeGroupTask,
		GroupRunID:       uuid.New(),
		GroupTaskKey:     "a",
		GroupTaskKind:    GroupTaskKindBranch,
	}
}

func TestSpawnLinkValid_GroupTaskComplete(t *testing.T) {
	l := validGroupLink()
	assert.True(t, l.Valid())
	l.GroupTaskKind = GroupTaskKindWorkflow
	assert.True(t, l.Valid())
}

func TestSpawnLinkValid_GroupTaskRequiresGroupFields(t *testing.T) {
	cases := map[string]func(*SpawnLink){
		"nil group run id":  func(l *SpawnLink) { l.GroupRunID = uuid.Nil },
		"empty task key":    func(l *SpawnLink) { l.GroupTaskKey = "" },
		"empty task kind":   func(l *SpawnLink) { l.GroupTaskKind = "" },
		"bogus task kind":   func(l *SpawnLink) { l.GroupTaskKind = "branches" },
		"nil parent":        func(l *SpawnLink) { l.ParentInstanceID = uuid.Nil },
		"empty parent node": func(l *SpawnLink) { l.ParentNodeID = "" },
	}
	for name, mutate := range cases {
		l := validGroupLink()
		mutate(&l)
		assert.False(t, l.Valid(), "%s: a group-task link must be invalid", name)
	}
}

// A legacy-mode link that carries group fields is contradictory: persistence
// writes the group columns only for group tasks, so it must be rejected
// rather than silently half-written.
func TestSpawnLinkValid_LegacyModeRejectsGroupFields(t *testing.T) {
	for _, mode := range []SpawnMode{SpawnModeFireAndForget, SpawnModeWaitForChild} {
		base := SpawnLink{ParentInstanceID: uuid.New(), ParentNodeID: "spawn-1", Mode: mode}
		require.True(t, base.Valid(), "legacy %q link without group fields stays valid", mode)

		withRun := base
		withRun.GroupRunID = uuid.New()
		assert.False(t, withRun.Valid(), "%q with a group run id", mode)

		withKey := base
		withKey.GroupTaskKey = "a"
		assert.False(t, withKey.Valid(), "%q with a task key", mode)

		withKind := base
		withKind.GroupTaskKind = GroupTaskKindWorkflow
		assert.False(t, withKind.Valid(), "%q with a task kind", mode)
	}
}

// ---------- Instance.IsGroupTask ----------

func TestInstanceIsGroupTask(t *testing.T) {
	parent := uuid.New()
	node := "group-1"
	run := uuid.New()
	key := "a"

	inst := Instance{
		ID: uuid.New(), ParentInstanceID: &parent, ParentNodeID: &node,
		SpawnMode: SpawnModeGroupTask, GroupRunID: &run, GroupTaskKey: &key,
		GroupTaskKind: GroupTaskKindBranch,
	}
	assert.True(t, inst.IsGroupTask())
	assert.False(t, inst.IsRoot(), "a group task has a parent")

	noRun := inst
	noRun.GroupRunID = nil
	assert.False(t, noRun.IsGroupTask())

	emptyKey := inst
	e := ""
	emptyKey.GroupTaskKey = &e
	assert.False(t, emptyKey.IsGroupTask())

	legacy := inst
	legacy.SpawnMode = SpawnModeWaitForChild
	assert.False(t, legacy.IsGroupTask(), "a legacy awaited child is not a group task")

	assert.False(t, Instance{ID: uuid.New()}.IsGroupTask(), "a root is not a group task")
}

// ---------- GroupRun lookups ----------

func TestGroupRunTaskLookups(t *testing.T) {
	ca, cb := uuid.New(), uuid.New()
	run := &GroupRun{Tasks: []GroupTask{
		{Position: 0, Key: "a", ChildInstanceID: ca},
		{Position: 1, Key: "b", ChildInstanceID: cb},
	}}

	got, ok := run.TaskByKey("b")
	require.True(t, ok)
	assert.Equal(t, cb, got.ChildInstanceID)
	got.CancelRequested = true
	assert.True(t, run.Tasks[1].CancelRequested, "TaskByKey returns a pointer INTO the roster")

	got, ok = run.TaskByChild(ca)
	require.True(t, ok)
	assert.Equal(t, "a", got.Key)

	_, ok = run.TaskByKey("zz")
	assert.False(t, ok)
	_, ok = run.TaskByChild(uuid.New())
	assert.False(t, ok)
	_, ok = (&GroupRun{}).TaskByKey("a")
	assert.False(t, ok, "an empty roster finds nothing")
}

// ---------- decision JSON shape ----------

// The decision is stored as JSON and surfaced by Phase 4; its field names are
// part of the contract.
func TestGroupDecisionJSONShape(t *testing.T) {
	d := GroupDecision{
		Outcome: "success", Rule: "first_success", Behavior: "cancel_unfinished",
		WinnerKey: "b", Reason: "task b completed first",
		DecidedAt: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC),
		Tasks: []FrozenTask{{
			Key: "a", Kind: GroupTaskKindWorkflow, Origin: "child-wf",
			ChildInstanceID: "00000000-0000-0000-0000-0000000000aa",
			Status:          GroupTaskStatusUnfinished, CancelRequested: true,
		}, {
			Key: "b", Kind: GroupTaskKindWorkflow, Origin: "child-wf",
			ChildInstanceID: "00000000-0000-0000-0000-0000000000bb",
			Status:          GroupTaskStatusCompleted, Result: map[string]any{"x": float64(1)}, Winner: true,
		}},
	}
	b, err := json.Marshal(d)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(b, &raw))
	for _, k := range []string{"outcome", "rule", "behavior", "winnerKey", "reason", "decidedAt", "tasks"} {
		assert.Contains(t, raw, k)
	}
	task0 := raw["tasks"].([]any)[0].(map[string]any)
	for _, k := range []string{"key", "kind", "origin", "childInstanceId", "status", "cancelRequested", "winner"} {
		assert.Contains(t, task0, k)
	}
	assert.NotContains(t, task0, "result", "an unfinished task has no result")

	var back GroupDecision
	require.NoError(t, json.Unmarshal(b, &back))
	assert.Equal(t, d, back)
}
```

- [x] Step 2: Extend the existing spawn-mode tests for the third mode. In `domain/instance/spawn_link_test.go`, replace the case list of `TestSpawnModeValid` (lines 25-27, from `{SpawnModeFireAndForget, true},` through `{"", false},`) with:

```go
		{SpawnModeFireAndForget, true},
		{SpawnModeWaitForChild, true},
		{SpawnModeGroupTask, true},
		{"", false},
		{"Group_Task", false},
```

and replace `TestSpawnModeTotality`'s doc comment and opening (lines 38-45, from `// TestSpawnModeTotality guards` through the closing `}` of the `if len(all) != 2` block) with:

```go
// TestSpawnModeTotality guards against a fourth mode landing without the
// consumers (validation, executor short-circuit, wake match, group dispatch,
// SQLite link persistence) being extended. group_task is internal: it is
// never an author-visible spawn_workflow mode.
func TestSpawnModeTotality(t *testing.T) {
	all := []SpawnMode{SpawnModeFireAndForget, SpawnModeWaitForChild, SpawnModeGroupTask}
	if len(all) != 3 {
		t.Fatalf("spawn mode vocabulary changed to %d values: extend SpawnMode.Valid, "+
			"the executor short-circuit, the child-terminal wake match, the group "+
			"terminal dispatch and the SQLite link read/write", len(all))
	}
```

(the trailing `for _, m := range all { ... }` loop at lines 46-50 stays unchanged).

- [x] Step 3: Run the tests, confirm they fail.

```bash
go test ./domain/instance/ -run 'TestGroup|TestSpawnLinkValid|TestInstanceIsGroupTask|TestSpawnMode' -count=1
```
Expected (compile failure — none of the new symbols exist yet):
```
# github.com/arbitron/arbitron/domain/instance [github.com/arbitron/arbitron/domain/instance.test]
domain/instance/group_run_test.go:19:43: undefined: SpawnModeGroupTask
domain/instance/group_run_test.go:21:18: undefined: GroupTaskKind
domain/instance/group_run_test.go:21:43: undefined: GroupTaskKindBranch
domain/instance/group_run_test.go:22:18: undefined: GroupTaskKind
domain/instance/group_run_test.go:22:45: undefined: GroupTaskKindWorkflow
domain/instance/group_run_test.go:24:18: undefined: GroupRunState
...
domain/instance/group_run_test.go:26:18: too many errors
FAIL	github.com/arbitron/arbitron/domain/instance [build failed]
```

- [x] Step 4: Create `domain/instance/group_run.go`:

```go
package instance

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// This file holds the durable domain model of ONE execution of a
// parallel_group node: the group run, its ordered task roster, the frozen
// decision, and the repository port that persists them.
//
// It deliberately does NOT import domain/workflow (the same one-directional
// domain edge SpawnMode and Visibility preserve). The workflow-side vocabulary
// (form, completion rule, unfinished behaviour, gather mode) is carried here as
// plain strings whose values are workflow.GroupForm* / GroupRule* /
// GroupUnfinished* / GroupGather* — the Phase 1 parser (workflow.ParseGroupConfig)
// is the single authority that validates them before a run is created.

// GroupTaskKind distinguishes the two group forms at the TASK level: a branch
// task runs a body of the parent's own definition snapshot; a workflow task
// runs a separately named child workflow.
type GroupTaskKind string

const (
	// GroupTaskKindBranch: the task is a branch of the parent's definition,
	// started at the branch entry node (workflow form "branches").
	GroupTaskKindBranch GroupTaskKind = "branch"
	// GroupTaskKindWorkflow: the task is a child workflow (workflow form
	// "workflows").
	GroupTaskKindWorkflow GroupTaskKind = "workflow"
)

// Valid reports whether k is one of the two task kinds. The empty value is
// INVALID: a group-task child without a kind could not be told apart from the
// other form by any read path.
func (k GroupTaskKind) Valid() bool {
	return k == GroupTaskKindBranch || k == GroupTaskKindWorkflow
}

// GroupRunState is the lifecycle of one group run.
//
//	launching → running → decided → done
//
// Phase 3 inserts gathering → (resolving_conflicts) between decided and done
// for results_and_files groups. A launch failure may decide straight from
// launching.
type GroupRunState string

const (
	GroupRunLaunching          GroupRunState = "launching"
	GroupRunRunning            GroupRunState = "running"
	GroupRunDecided            GroupRunState = "decided"
	GroupRunGathering          GroupRunState = "gathering"           // Phase 3
	GroupRunResolvingConflicts GroupRunState = "resolving_conflicts" // Phase 3
	GroupRunDone               GroupRunState = "done"
)

// Valid reports whether s is one of the six run states.
func (s GroupRunState) Valid() bool {
	switch s {
	case GroupRunLaunching, GroupRunRunning, GroupRunDecided,
		GroupRunGathering, GroupRunResolvingConflicts, GroupRunDone:
		return true
	default:
		return false
	}
}

// IsDecidable reports whether a run in state s may still take its decision
// (the GroupRunRepository.Decide CAS precondition).
func (s GroupRunState) IsDecidable() bool {
	return s == GroupRunLaunching || s == GroupRunRunning
}

// GroupTaskLaunchState records whether the task's child has been handed to the
// engine. It is persisted BEFORE enqueue (pending) so a crash between roster
// registration and launch is recoverable without duplicating children.
type GroupTaskLaunchState string

const (
	GroupTaskPending      GroupTaskLaunchState = "pending"
	GroupTaskLaunched     GroupTaskLaunchState = "launched"
	GroupTaskLaunchFailed GroupTaskLaunchState = "launch_failed"
)

// Valid reports whether s is one of the three launch states.
func (s GroupTaskLaunchState) Valid() bool {
	return s == GroupTaskPending || s == GroupTaskLaunched || s == GroupTaskLaunchFailed
}

// GroupTaskStatus is the shared vocabulary of a task's LIVE status (what its
// child is doing now) and its FROZEN status (what the decision recorded).
type GroupTaskStatus string

const (
	GroupTaskStatusNotStarted GroupTaskStatus = "not_started" // live only: never launched
	GroupTaskStatusQueued     GroupTaskStatus = "queued"
	GroupTaskStatusRunning    GroupTaskStatus = "running"
	GroupTaskStatusWaiting    GroupTaskStatus = "waiting"
	GroupTaskStatusPaused     GroupTaskStatus = "paused"
	GroupTaskStatusCompleted  GroupTaskStatus = "completed"
	GroupTaskStatusFailed     GroupTaskStatus = "failed"
	GroupTaskStatusCancelled  GroupTaskStatus = "cancelled"
	GroupTaskStatusUnfinished GroupTaskStatus = "unfinished" // frozen only
)

// Valid reports whether s is one of the nine statuses (live or frozen).
func (s GroupTaskStatus) Valid() bool {
	switch s {
	case GroupTaskStatusNotStarted, GroupTaskStatusQueued, GroupTaskStatusRunning,
		GroupTaskStatusWaiting, GroupTaskStatusPaused, GroupTaskStatusCompleted,
		GroupTaskStatusFailed, GroupTaskStatusCancelled, GroupTaskStatusUnfinished:
		return true
	default:
		return false
	}
}

// IsLive reports whether s may appear as a task's LIVE status. "unfinished" is
// a decision-time verdict about a task that had not finished; no child is ever
// "unfinished", so it is frozen-only.
func (s GroupTaskStatus) IsLive() bool {
	return s.Valid() && s != GroupTaskStatusUnfinished
}

// IsTerminal reports whether s is a final child outcome (completed, failed or
// cancelled). It mirrors State.IsTerminal so the group code and the spawn code
// agree on "done". "unfinished" is NOT terminal: it describes a task that was
// still active when the decision froze it.
func (s GroupTaskStatus) IsTerminal() bool {
	return s == GroupTaskStatusCompleted || s == GroupTaskStatusFailed || s == GroupTaskStatusCancelled
}

// GroupTaskStatusFromState maps a child instance's lifecycle state to the live
// task status. It is the ONE place that conversion lives. The empty state (no
// child row yet) and any unrecognized value map to not_started: a task is only
// reported as active once its child is observably so. A totality test over
// every State value guards against a new State landing unmapped.
func GroupTaskStatusFromState(s State) GroupTaskStatus {
	switch s {
	case StateQueued:
		return GroupTaskStatusQueued
	case StateRunning:
		return GroupTaskStatusRunning
	case StateWaiting:
		return GroupTaskStatusWaiting
	case StatePaused:
		return GroupTaskStatusPaused
	case StateCompleted:
		return GroupTaskStatusCompleted
	case StateFailed:
		return GroupTaskStatusFailed
	case StateCancelled:
		return GroupTaskStatusCancelled
	default:
		return GroupTaskStatusNotStarted
	}
}

// GroupTask is one row of a group run's ordered task roster. The roster is
// persisted in full BEFORE any child is enqueued, with ChildInstanceID already
// reserved, so launch is idempotent across crashes.
type GroupTask struct {
	GroupRunID      uuid.UUID
	Position        int // author order, 0-based
	Key             string
	Kind            GroupTaskKind
	Origin          string    // branch entry node id, or child workflow name
	ChildInstanceID uuid.UUID // reserved BEFORE enqueue
	LaunchState     GroupTaskLaunchState
	LaunchError     string
	LiveStatus      GroupTaskStatus
	// TerminalSeq is the global, monotonically increasing order assigned ONCE
	// at terminal intake (GroupRunRepository.RecordTaskTerminal). It is what
	// lets first_success pick the earliest success deterministically even when
	// terminal callbacks arrive out of order or after a restart. nil until the
	// task's terminal is recorded.
	TerminalSeq *int64
	TerminalAt  *time.Time
	// Result is the child's final variables minus reserved names; recorded for
	// failed/cancelled children too (available variables are retained).
	Result map[string]any
	// Failure is {"state","failedNodeId","error","attempt"} for failed/cancelled.
	Failure                 map[string]any
	CancelRequested         bool
	Blocking                bool // true until a decision releases it
	ContinuingIndependently bool
	FinalizedRef            string // Phase 3 (always empty in Phase 2)
}

// FrozenTask is a task's entry in the immutable decision snapshot.
type FrozenTask struct {
	Key             string          `json:"key"`
	Kind            GroupTaskKind   `json:"kind"`
	Origin          string          `json:"origin"`
	ChildInstanceID string          `json:"childInstanceId"`
	Status          GroupTaskStatus `json:"status"` // completed|failed|cancelled|unfinished
	Result          map[string]any  `json:"result,omitempty"`
	Failure         map[string]any  `json:"failure,omitempty"`
	CancelRequested bool            `json:"cancelRequested"`
	Winner          bool            `json:"winner"`
}

// GroupDecision is the atomic, immutable verdict of a group run: the rule that
// fired, the outcome, the winner (first_success) and the ordered task
// snapshot. Once written (GroupRunRepository.Decide) it never changes; later
// task terminals update live status only.
type GroupDecision struct {
	Outcome   string       `json:"outcome"` // "success" | "failure"
	Rule      string       `json:"rule"`
	Behavior  string       `json:"behavior"`
	WinnerKey string       `json:"winnerKey,omitempty"`
	Reason    string       `json:"reason"` // e.g. "all tasks completed", "task b failed", "launch failed: ..."
	DecidedAt time.Time    `json:"decidedAt"`
	Tasks     []FrozenTask `json:"tasks"` // author order
}

// GroupRun is the durable record of one execution of a parallel_group node.
type GroupRun struct {
	ID                 uuid.UUID
	ParentInstanceID   uuid.UUID
	NodeID             string
	Attempt            int // 1
	Form               string
	CompletionRule     string
	UnfinishedBehavior string
	GatherMode         string
	Config             map[string]any      // JSON snapshot of workflow.GroupConfig
	BodyOwnership      map[string][]string // taskKey -> body node ids (branches)
	StartVariables     map[string]any      // deep copy, reserved names excluded
	// WorkspaceSnapshot is the handle returned by
	// engine.GroupWorkspace.SnapshotParent ("" when the parent had no workspace).
	WorkspaceSnapshot     string
	State                 GroupRunState
	Decision              *GroupDecision
	FinalOutput           map[string]any // set once; immutable afterwards
	ContinuationPort      string         // "success"|"failure"; set with FinalOutput
	ContinuationReady     bool
	ContinuationDelivered bool
	ActionsApplied        bool // unfinished-task actions fully applied after decision
	LaunchError           string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	Tasks                 []GroupTask // ordered by Position
}

// TaskByKey returns a pointer to the roster entry with the given key.
func (r *GroupRun) TaskByKey(key string) (*GroupTask, bool) {
	for i := range r.Tasks {
		if r.Tasks[i].Key == key {
			return &r.Tasks[i], true
		}
	}
	return nil, false
}

// TaskByChild returns a pointer to the roster entry whose reserved child id is
// childID.
func (r *GroupRun) TaskByChild(childID uuid.UUID) (*GroupTask, bool) {
	for i := range r.Tasks {
		if r.Tasks[i].ChildInstanceID == childID {
			return &r.Tasks[i], true
		}
	}
	return nil, false
}

// Sentinel errors of the group-run repository.
var (
	ErrGroupRunExists   = errors.New("group run already exists for this parent node")
	ErrGroupRunNotFound = errors.New("group run not found")
	// ErrGroupTaskNotFound is returned by the per-task repository operations
	// when the run exists but has no task with the given key.
	ErrGroupTaskNotFound = errors.New("group task not found")
)

// GroupRunRepository persists group runs and their rosters. Every mutating
// method that can race (TransitionState, RecordTaskTerminal, Decide,
// SetFinalOutput) is a compare-and-swap whose boolean result reports whether
// THIS caller won; losing is not an error.
type GroupRunRepository interface {
	// CreateWithTasks atomically inserts the run and its full roster.
	// ErrGroupRunExists on a (parent, node, attempt) conflict.
	CreateWithTasks(ctx context.Context, run *GroupRun) error
	// Get loads a run with its roster ordered by Position. ErrGroupRunNotFound.
	Get(ctx context.Context, id uuid.UUID) (*GroupRun, error)
	// GetByParentNode returns the latest attempt for (parent, node), or
	// (nil, nil) when there is none.
	GetByParentNode(ctx context.Context, parentID uuid.UUID, nodeID string) (*GroupRun, error)
	// GetByChild returns the run owning the task whose child is childID, or
	// (nil, nil) when childID is not a group task child.
	GetByChild(ctx context.Context, childID uuid.UUID) (*GroupRun, error)
	// ListByParent returns every run of parentID, oldest first.
	ListByParent(ctx context.Context, parentID uuid.UUID) ([]GroupRun, error)
	// ListUnsettled returns the ids of runs with state != done OR
	// !continuation_delivered OR !actions_applied, oldest first.
	ListUnsettled(ctx context.Context) ([]uuid.UUID, error)
	MarkTaskLaunched(ctx context.Context, runID uuid.UUID, key string) error
	MarkTaskLaunchFailed(ctx context.Context, runID uuid.UUID, key, reason string) error
	// TransitionState is a CAS from -> to.
	TransitionState(ctx context.Context, runID uuid.UUID, from, to GroupRunState) (bool, error)
	// RecordTaskTerminal assigns TerminalSeq ONCE and sets LiveStatus/result/
	// failure/terminal time. Idempotent: a second call returns the existing
	// seq with first=false and changes nothing.
	RecordTaskTerminal(ctx context.Context, runID uuid.UUID, key string, status GroupTaskStatus, result, failure map[string]any, at time.Time) (seq int64, first bool, err error)
	// UpdateTaskLiveStatus never overwrites a terminal live status.
	UpdateTaskLiveStatus(ctx context.Context, runID uuid.UUID, key string, status GroupTaskStatus) error
	// Decide is a CAS: state IN (launching, running) -> decided AND decision IS
	// NULL, in ONE transaction that also sets Blocking=false for every task.
	Decide(ctx context.Context, runID uuid.UUID, d GroupDecision) (bool, error)
	MarkTaskCancelRequested(ctx context.Context, runID uuid.UUID, key string) error
	MarkTaskContinuingIndependently(ctx context.Context, runID uuid.UUID, key string) error
	MarkActionsApplied(ctx context.Context, runID uuid.UUID) error
	// SetFinalOutput is a CAS: state from -> done AND final_output IS NULL;
	// it also records the port and sets continuation_ready=true.
	SetFinalOutput(ctx context.Context, runID uuid.UUID, port string, output map[string]any, from GroupRunState) (bool, error)
	MarkContinuationDelivered(ctx context.Context, runID uuid.UUID) error
}

// NodeAddressedWaker re-queues a waiting parent ONLY IF it is still waiting at
// the given node. It is the group coordinator's wake primitive: the existing
// QueueRepository.SetQueuedIfWaiting checks state alone, so a parent that has
// moved on to (and is waiting at) a LATER node could be woken by a group it
// has already left. Satisfied by *sqlite.QueueRepo (P2-T4).
type NodeAddressedWaker interface {
	SetQueuedIfWaitingAtNode(ctx context.Context, id uuid.UUID, nodeID string, queuedAt time.Time) (bool, error)
}
```

- [x] Step 5: Add the third spawn mode and the group fields to the link. In `domain/instance/spawn_link.go`:

(a) Replace lines 18-32 (the `const (` block declaring `SpawnModeFireAndForget` / `SpawnModeWaitForChild`, its doc comment on `Valid`, and `func (m SpawnMode) Valid() bool`) with:

```go
const (
	// SpawnModeFireAndForget: the parent continued immediately after starting
	// the child. The child's outcome never affects the parent.
	SpawnModeFireAndForget SpawnMode = "fire_and_forget"
	// SpawnModeWaitForChild: the parent suspended at the spawn node and resumes
	// through its success/failure port when the child reaches a terminal state.
	SpawnModeWaitForChild SpawnMode = "wait_for_child"
	// SpawnModeGroupTask: the child is one task of a parallel_group node's
	// group run (see GroupRun). It is INTERNAL — never an author-visible
	// spawn_workflow mode (workflow.IsValidSpawnMode still admits only the two
	// modes above). The parent is woken by the group coordinator, never by the
	// single-child wake in Engine.OnChildTerminal.
	//
	// Persistence: the 018 spawn_mode CHECK cannot admit this value without
	// rebuilding the instances table, so a group-task row stores spawn_mode
	// NULL plus group_run_id / group_task_key / group_task_kind (migration
	// 019); the SQLite read side maps that shape back to this mode. Legacy
	// spawn queries keep filtering on spawn_mode and therefore never match a
	// group task.
	SpawnModeGroupTask SpawnMode = "group_task"
)

// Valid reports whether m is one of the three recognized modes. The empty
// value is INVALID: an unset mode on a link would make the wake match and the
// resumption short-circuit ambiguous.
func (m SpawnMode) Valid() bool {
	return m == SpawnModeFireAndForget || m == SpawnModeWaitForChild || m == SpawnModeGroupTask
}
```

(b) Replace the `SpawnLink` struct body (lines 49-59, `type SpawnLink struct {` … closing `}`; the doc comment above it at lines 34-48 stays) with:

```go
type SpawnLink struct {
	ParentInstanceID uuid.UUID
	ParentNodeID     string
	Mode             SpawnMode

	// SpawnDepth is how many spawn hops separate the CHILD from its root run: a
	// root is 0, its child 1, and so on. It is DERIVED, never caller-supplied —
	// Engine.Spawn reads the parent's stored depth and adds one, so no executor
	// can forget to set it and no request can inflate it.
	SpawnDepth int

	// GroupRunID / GroupTaskKey / GroupTaskKind are set ONLY for
	// Mode == SpawnModeGroupTask: the group run that owns the child, the
	// task's key within that run, and the task's form. All three are required
	// for a group-task link and forbidden on every other mode (see Valid).
	GroupRunID    uuid.UUID
	GroupTaskKey  string
	GroupTaskKind GroupTaskKind
}
```

(c) Replace `SpawnLink.Valid` and its doc comment (lines 83-92, from `// Valid reports whether the link is fully populated.` through the closing `}`) with:

```go
// Valid reports whether the link is fully populated. A partial link is never
// written: a child with a parent id but no node id could not be matched by the
// wake and would strand its parent forever.
//
// SpawnDepth is deliberately NOT checked: 0 is meaningful (it is what a link
// carries before Engine.Spawn derives the real depth), and validity here is
// about matchability by the wake.
//
// A group-task link (Mode == SpawnModeGroupTask) additionally requires its
// group run id, task key and a valid task kind: without them the coordinator
// could not attribute the child's terminal to a roster row. Conversely a
// legacy-mode link carrying ANY group field is invalid — persistence writes
// the group columns only for group tasks, so such a link would silently lose
// half of what the caller asked for.
func (l SpawnLink) Valid() bool {
	if l.ParentInstanceID == uuid.Nil || l.ParentNodeID == "" || !l.Mode.Valid() {
		return false
	}
	if l.Mode == SpawnModeGroupTask {
		return l.GroupRunID != uuid.Nil && l.GroupTaskKey != "" && l.GroupTaskKind.Valid()
	}
	return l.GroupRunID == uuid.Nil && l.GroupTaskKey == "" && l.GroupTaskKind == ""
}
```

- [x] Step 6: Add the group fields and the predicate to `Instance`. In `domain/instance/instance.go`, replace lines 140-145 (the `// SpawnDepth is how many spawn hops…` comment and the `SpawnDepth        int` field) with the block below — the following `State             State` line (146) and the rest of the struct are unchanged:

```go
	// SpawnDepth is how many spawn hops separate this run from its root: 0 for a
	// root run, 1 for its child, and so on. It is the persisted counter behind
	// the MaxSpawnDepth runtime ceiling — see MaxSpawnDepth for why the bound
	// cannot be computed at author time. Derived by Engine.Spawn from the
	// parent's stored value; never caller-supplied.
	SpawnDepth int
	// GroupRunID / GroupTaskKey / GroupTaskKind are the group-task half of the
	// link, set ONLY when SpawnMode == SpawnModeGroupTask (a child created by a
	// parallel_group node). nil/empty for roots and for legacy spawn children.
	// Like ParentInstanceID there is no foreign key on the stored group run id.
	GroupRunID        *uuid.UUID
	GroupTaskKey      *string
	GroupTaskKind     GroupTaskKind
```

Then insert immediately after `IsRoot` (after line 167):

```go
// IsGroupTask reports whether this run is a task child of a parallel_group
// node: a parent link whose mode is SpawnModeGroupTask with the group run id
// and task key present. It is the ONE predicate the terminal dispatch and the
// group coordinator use to tell group tasks from legacy spawn children.
func (i Instance) IsGroupTask() bool {
	return i.ParentInstanceID != nil && i.SpawnMode == SpawnModeGroupTask &&
		i.GroupRunID != nil && i.GroupTaskKey != nil && *i.GroupTaskKey != ""
}
```

- [x] Step 7: Run the tests, confirm they pass.

```bash
go test ./domain/instance/ -run 'TestGroup|TestSpawnLinkValid|TestInstanceIsGroupTask|TestSpawnMode|TestSpawnLink' -count=1 -v
```
Expected:
```
--- PASS: TestGroupVocabularyWireValues (0.00s)
--- PASS: TestGroupTaskKindValid (0.00s)
--- PASS: TestGroupRunStateValidAndDecidable (0.00s)
--- PASS: TestGroupTaskLaunchStateValid (0.00s)
--- PASS: TestGroupTaskStatusPredicates (0.00s)
--- PASS: TestGroupTaskStatusTerminalAgreesWithState (0.00s)
--- PASS: TestGroupTaskStatusFromState (0.00s)
--- PASS: TestGroupTaskStatusFromStateTotality (0.00s)
--- PASS: TestSpawnLinkValid_GroupTaskComplete (0.00s)
--- PASS: TestSpawnLinkValid_GroupTaskRequiresGroupFields (0.00s)
--- PASS: TestSpawnLinkValid_LegacyModeRejectsGroupFields (0.00s)
--- PASS: TestInstanceIsGroupTask (0.00s)
--- PASS: TestGroupRunTaskLookups (0.00s)
--- PASS: TestGroupDecisionJSONShape (0.00s)
--- PASS: TestSpawnLinkContext_RoundTrip (0.00s)
--- PASS: TestSpawnLinkContext_AbsentReportsNotOk (0.00s)
--- PASS: TestSpawnLinkContext_IndependentOfOrigin (0.00s)
--- PASS: TestSpawnLinkContext_InnerOverridesOuter (0.00s)
--- PASS: TestSpawnModeConstants (0.00s)
--- PASS: TestSpawnModeValid (0.00s)
--- PASS: TestSpawnModeTotality (0.00s)
--- PASS: TestSpawnLinkShape (0.00s)
--- PASS: TestSpawnLinkValidRejectsIncomplete (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/instance	0.2XXs
```

- [x] Step 8: Confirm nothing else regressed (the new fields are additive; every existing `SpawnLink{...}` / `Instance{...}` literal in the tree is keyed), then gofmt and vet.

```bash
go build ./...
go test ./domain/... ./adapter/sqlite/ ./adapter/filesystem/ ./cmd/... -count=1
gofmt -l domain/instance
go vet ./domain/instance/
```
Expected: every package `ok` (or `[no test files]`); `gofmt -l` and `go vet` print nothing. (A full `go test ./...` additionally reports `FAIL github.com/arbitron/arbitron/adapter/config` in `TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` — that failure is pre-existing on the base commit, unrelated to this task, and must not be "fixed" here.)

### Execution evidence and self-review

- Red → green: the Step 3 command failed to compile on the new group symbols before implementation; every focused test then passed.
- Spec-compliance FIRST: checked the Phase 2 design and binding alignment contract. Added only the domain vocabulary, 17-method repository port, node-addressed wake port and link/instance fields. Left storage, engine execution, public workflow spawn modes and `SpawnLinkRepository` unchanged. P2-T7/T8 produce the runtime data; P2-T14 owns composition-root wiring.
- Anti-green-theater SECOND: tests assert persisted JSON field names, nonempty decision data, rejection of missing/contradictory link inputs and mutations reaching both roster lookup sinks. Strengthened state totality to inspect the actual declared State constants rather than merely count a test fixture. A temporary eighth unmapped State made that test fail; removing the probe restored green. No fake or intermediate-hop integration claim is used for this pure-domain slice.
- Code-quality THIRD: followed neighboring instance/port conventions, clarified the internal-versus-public spawn vocabulary and atomic terminal-order documentation, and ran gofmt, vet and whitespace checks.
- Final verification after review: focused tests, `go build ./...`, `go test ./domain/... ./adapter/sqlite/ ./adapter/filesystem/ ./cmd/... -count=1`, `gofmt -l domain/instance` and `go vet ./domain/instance/` all passed. No unrelated suite exemption was needed.

- [x] Step 9: Commit.

```bash
git add domain/instance/group_run.go domain/instance/group_run_test.go domain/instance/spawn_link.go domain/instance/spawn_link_test.go domain/instance/instance.go
git commit -m "feat(instance): parallel group-run domain model, repository ports and group-task link fields" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
