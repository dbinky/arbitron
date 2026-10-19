# Parallel Workflow Gathering — Phase 2 Task 8: GroupLauncher and ComposeTaskVariables

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Launch one parallel group exactly once: capture the group-start variables once, compose each task's own deep-copied, typed, task-locally-bound variables, refuse writable shared paths and child-contract violations before anything is persisted, snapshot and fork the parent's working area per task, persist the complete roster with reserved child ids BEFORE any enqueue, start every task through `Engine.StartGroupTask`, and only then open the decision barrier — deciding a durable `failure` (and cancelling what was started) if a launch fails part-way.
**Architecture:** `domain/engine/group_task_vars.go` holds the pure `ComposeTaskVariables` (deep copy of the start snapshot minus reserved names, overlaid with `variable.ResolveTypedMap` of the task inputs against a store whose `workspace` is the TASK's directory, then shared-path rejection). `domain/engine/group_launcher.go` holds `GroupLauncher.Launch`, split into a pre-launch phase (identity → snapshot → per-task dir/compose/contract → fork; any failure cleans up and returns `ErrGroupPreLaunch`) and a post-persist phase (`CreateWithTasks` → per-task `StartGroupTask` + `MarkTaskLaunched` → `TransitionState(launching→running)`; partial failure → `MarkTaskLaunchFailed`, `DecideLaunchFailure` + `Decide`, cancel started tasks under the owner identity). `domain/engine/group_events.go` defines the `group.*` event types and the one publish helper.
**Tech Stack:** Go 1.25.5, standard library (`encoding/json`, `path/filepath`, `reflect`, `sort`, `strconv`, `strings`, `log/slog`), `github.com/google/uuid`, testify.

**Prerequisites (must be merged first):** P1-T2 (`workflow.GroupScope`), P2-T17 (linked-worktree isolation), P1-T1 (`workflow.GroupConfig`, `GroupTask`, `ParseGroupConfig` sentinels `ErrGroupFormInvalid`, `ErrGroupNoTasks`, `ErrGroupTaskTargetInvalid`, `ErrGroupReservedInput`, constants), P2-T1 (`instance.GroupRun`, `GroupTask`, `GroupRunRepository`, state/launch/status constants, `ErrGroupRunExists`, `ErrGroupRunNotFound`), P2-T5 (`engine.GroupWorkspace`), P2-T6 (`variable.DeepCopyMap`, `ResolveTypedMap`, `ErrUnresolvedReference`), P2-T7 (`GroupTaskStartRequest`, `GroupTaskStarter`), **P2-T9** (`DecideLaunchFailure`, `isTerminalGroupStatus`).

---

## Interfaces

**Consumes (exact):**
- `domain/variable/variable.go:22` `ReservedInputNames`, `:38` `NewStore`, `:100` `SetInput`, `:111` `SetReserved`, `:127` `Snapshot`; P2-T6 `DeepCopyMap`, `ResolveTypedMap`, `ErrUnresolvedReference`.
- `domain/engine/conventions.go:15` `ReservedVarWorkspace`, `:30` `WorkDirFromVars(*variable.VariableStore) (string, bool)`.
- `domain/engine/ports.go:27-40` `WorkspaceManager` (`EnsureInstanceDir`, `RemoveInstanceDir`), `:300` `IdentityResolver`, `:323` `ChildWorkflowReader.ChildInputs(ctx, name) ([]workflow.InputField, error)`, `:279` `EventBus`; `domain/engine/orphan_reconciler.go:20` `RunCanceller.Cancel(ctx, id) error`.
- `domain/engine/child_input_contract.go:17` `ErrChildContractUnsatisfied`, `:87` `validateChildInputs(childWorkflow, fields, produced) error` (required presence; reused verbatim).
- P2-T5 `GroupWorkspace{SnapshotParent(ctx, groupRunID, parentDir) (string, error); ForkTask(ctx, handle, taskDir) error; ReleaseSnapshot(ctx, handle) error}`.
- P2-T7 `GroupTaskStarter.StartGroupTask(ctx, GroupTaskStartRequest) error`.
- P2-T9 `DecideLaunchFailure(run, reason, now) *instance.GroupDecision`.
- P2-T1 `GroupRunRepository.CreateWithTasks`, `Get`, `GetByParentNode`, `MarkTaskLaunched`, `MarkTaskLaunchFailed`, `TransitionState`, `Decide`, `MarkTaskCancelRequested`.

**Produces (exact):**
```go
// group_task_vars.go
var ErrGroupSharedPath = errors.New("task variable points into the parent's working area")
func ComposeTaskVariables(start map[string]any, task workflow.GroupTask, taskWorkspace, parentWorkspace string) (map[string]any, error)

// group_launcher.go
var ErrGroupPreLaunch = errors.New("parallel_group could not be prepared")
type GroupLaunchRequest struct {
	Parent    *instance.Instance
	Node      workflow.Node
	Config    workflow.GroupConfig
	Ownership *workflow.GroupScope
	Vars      *variable.VariableStore
}
type GroupLauncher struct{ /* groups, starter, contracts, workspaces, groupWS, identities, canceller, bus, now */ }
func NewGroupLauncher(groups instance.GroupRunRepository, starter GroupTaskStarter, contracts ChildWorkflowReader, workspaces WorkspaceManager, groupWS GroupWorkspace, identities IdentityResolver, canceller RunCanceller, bus EventBus) *GroupLauncher
func (l *GroupLauncher) Launch(ctx context.Context, req GroupLaunchRequest) (*instance.GroupRun, error)

// group_events.go (defined HERE — see Deviations)
const (
	EventTypeGroupLaunched    = "group.launched"
	EventTypeGroupTaskUpdated = "group.task_updated"
	EventTypeGroupDecided     = "group.decided"
	EventTypeGroupGathering   = "group.gathering"
	EventTypeGroupConflict    = "group.conflict"
	EventTypeGroupResolved    = "group.resolved"
	EventTypeGroupCompleted   = "group.completed"
)
func publishGroupEvent(bus EventBus, run *instance.GroupRun, eventType string, extra map[string]any, at time.Time) // unexported, reused by P2-T10
func decisionEventData(d *instance.GroupDecision) map[string]any                                                // unexported, reused by P2-T10
```

`ComposeTaskVariables` contract: result = `DeepCopyMap(start)` with every `variable.ReservedInputNames` key removed, overlaid with `ResolveTypedMap(task.Inputs, store)` where `store` holds the (reserved-free) start values plus `workspace = taskWorkspace` (only when non-empty). Inputs resolve against the START values, never against each other. A task input named `workspace` ⇒ `workflow.ErrGroupReservedInput`. A missing reference ⇒ error wrapping `variable.ErrUnresolvedReference` naming the task. When `parentWorkspace != ""`, any inherited or explicit value (recursively, through `map[string]any`, `[]any`, `[]string`, `[]map[string]any`) that is an absolute path equal to or inside the cleaned `parentWorkspace` ⇒ `ErrGroupSharedPath` naming the key path. Never returns a nil map on success.

`Launch` contract:
- Pre-launch (`ErrGroupPreLaunch`-wrapped error; NOTHING persisted, created task directories removed, the snapshot released): incomplete request; owner identity resolution failure (no fallback); parent has a working area but `workspaces` or `groupWS` is nil; snapshot failure; per-task target/body mismatch; directory creation failure; composition failure (missing reference, reserved input, shared path); workflow task with no contract reader, an invisible/absent child (`workflow.ErrNotFound` preserved), a missing required input, or a declared type mismatch (`ErrChildContractUnsatisfied`); fork failure; roster persistence failure.
- `ErrGroupRunExists` from `CreateWithTasks` ⇒ local preparation cleaned up and the EXISTING run returned (no second roster, no child started).
- Post-persist: tasks start in author order under the owner identity with their reserved ids; the run is `running` only after every task is `launched`; one `group.launched` event. A `StartGroupTask` or `MarkTaskLaunched` error ⇒ that task `launch_failed`, its reserved child cancelled if it exists, `Decide(DecideLaunchFailure(run, "launch failed: <cause>"))`, every earlier task `MarkTaskCancelRequested` + `RunCanceller.Cancel` under the owner identity, later tasks' prepared directories removed, `group.decided` published when this call won the decision; the run is returned with a nil error. Results-mode snapshots are released once launching ends (the files-mode baseline is kept for Phase 3).

## Wiring & data provenance

- **Composition root:** **P2-T14** (`cmd/arbitron/main.go:initDomain`) constructs `engine.NewGroupLauncher(groupRunRepo, eng, eng, wsMgr, groupWS, identityResolver, eng, bus)` and hands it to `engine.NewParallelGroupExecutor` (P2-T11). Exact producers:
  - `groups` ← `adapter/sqlite.NewGroupRunRepo(db)` (P2-T3).
  - `starter` ← `eng` (`*engine.Engine`, `engine_group_task.go:Engine.StartGroupTask`, P2-T7).
  - `contracts` ← `eng` (`engine_spawn.go:24 Engine.ChildInputs`).
  - `workspaces` ← `wsMgr` from `filesystem.NewWorkspaceManager(cfg.Workspace.Path)` (`cmd/arbitron/main.go:814`, the same value `eng.SetWorkspaceManager` receives at main.go:820).
  - `groupWS` ← `adapter/filesystem.NewGroupWorkspace(cfg.Workspace.Path)` (P2-T5).
  - `identities` ← `identityResolver` = `userIdentityResolver{users: userRepo, groups: groupCfg}` (`cmd/arbitron/main.go:926`, `IdentityForUser` at main.go:1455).
  - `canceller` ← `eng` (`engine.go:1530 Engine.Cancel`).
  - `bus` ← `bus := eventbus.New()` (`cmd/arbitron/main.go:618`).
- **Request producers (P2-T11 builds the request):** `Parent` ← `InstanceRepository.Get(InstanceIDFromContext)`; `Config` ← `workflow.ParseGroupConfig(node.Config)` (P1-T1); `Ownership` ← `workflow.AnalyzeGroups(wf).Groups[node.ID]` (P1-T2) over `Instance.WorkflowSnapshot`; `Vars` ← the store `domain/engine/engine.go:868 Engine.run` reconstructs with `variable.FromSnapshotWithNodeIDs` and seeds with `workspace` in `workspace.go:32 prepareWorkspace`.
- **Provenance probes:** `TestGroupLauncher_NilGroupWorkspaceWithAParentWorkspaceRefuses` (fails if the fork capability input is absent and the launcher silently shares the parent's area); `TestGroupLauncher_WorkflowKind_RequiredInputFromBaseAccepted` asserts the contract read is made under the reconstructed OWNER identity; `TestGroupLauncher_StartVariablesAndFilesAreIdenticalInBothForms` asserts the file content reaching each task directory came from the parent's working area via the snapshot/fork port.

**Deviations (backward-compatible):**
- The `EventTypeGroup*` constants are defined in THIS task's `group_events.go` (not P2-T10) because the launcher is their first publisher (`group.launched`, `group.decided`); P2-T10 and P2-T13 consume them unchanged. Values are exactly the contract's.
- This task must run after P2-T9 (it calls `DecideLaunchFailure`).
- `NewGroupLauncher` panics on a nil `groups`, `starter`, `identities`, `canceller` or `bus` (a missing one is a wiring bug that would otherwise surface as a hung or unauthenticated group); `contracts`, `workspaces` and `groupWS` may be nil and are refused per-launch exactly when they are needed.

---

### Task 8: GroupLauncher and ComposeTaskVariables

**Files:**
- Create: `domain/engine/group_task_vars.go`
- Create: `domain/engine/group_events.go`
- Create: `domain/engine/group_launcher.go`
- Test: `domain/engine/group_task_vars_test.go`
- Test: `domain/engine/group_fakes_test.go` (shared in-memory fakes; P2-T10 and P2-T11 reuse `memGroupRuns`, `fakeGroupTaskStarter`, `recordingCanceller`)
- Test: `domain/engine/group_launcher_test.go`

All test files are `package engine_test` and reuse `newMockInstanceRepo`/`mockInstanceRepo`, `newMockEventBus`/`mockEventBus.eventsOfType` (`engine_test.go:95-725`) and `fakeIdentityResolver` (`executor_spawn_test.go:25-43`). No existing file is modified.

#### Cycle 1 — ComposeTaskVariables

- [x] Step 1: Write the failing composition tests. Create `domain/engine/group_task_vars_test.go`:

> BLOCKED (resolved by the P2-T9 pass): At selection, `DecideLaunchFailure` was absent. The alignment index requires T9 before T8, so this pass completed independent T9 instead. T8 remains unchecked and is now eligible for the next pass.

```go
package engine_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	gvParentWS = "/ws/instances/parent"
	gvTaskWS   = "/ws/instances/task-a"
)

func gvStart() map[string]any {
	return map[string]any{
		"repo":      "acme/widgets",
		"count":     float64(3),
		"cfg":       map[string]any{"k": map[string]any{"leaf": "v"}},
		"init":      map[string]any{"ticket": "T-1"},
		"workspace": gvParentWS,
	}
}

func TestComposeTaskVariables_BaseCopyMinusReservedPlusTypedInputs(t *testing.T) {
	task := workflow.GroupTask{Key: "a", Entry: "a1", Inputs: map[string]any{
		"n":     "{{count}}",
		"whole": "{{cfg}}",
		"flag":  true,
		"label": "r={{repo}}",
		"repo":  "override/repo",
	}}

	got, err := engine.ComposeTaskVariables(gvStart(), task, gvTaskWS, gvParentWS)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"repo":  "override/repo",
		"count": float64(3),
		"cfg":   map[string]any{"k": map[string]any{"leaf": "v"}},
		"init":  map[string]any{"ticket": "T-1"},
		"n":     float64(3),
		"whole": map[string]any{"k": map[string]any{"leaf": "v"}},
		"flag":  true,
		"label": "r=acme/widgets",
	}, got, "inputs resolve against the START values, not against each other; reserved names never pass through")
	assert.IsType(t, float64(0), got["n"], "a whole-value reference keeps its type")
	assert.IsType(t, map[string]any{}, got["whole"])
	assert.NotContains(t, got, "workspace")
}

func TestComposeTaskVariables_WorkspaceTemplatesBindToTheTasksOwnArea(t *testing.T) {
	task := workflow.GroupTask{Key: "a", Entry: "a1", Inputs: map[string]any{
		"work_dir": "{{workspace}}/repo",
		"ws":       "{{workspace}}",
	}}

	got, err := engine.ComposeTaskVariables(gvStart(), task, gvTaskWS, gvParentWS)
	require.NoError(t, err)
	assert.Equal(t, gvTaskWS+"/repo", got["work_dir"])
	assert.Equal(t, gvTaskWS, got["ws"])
}

func TestComposeTaskVariables_EveryCompositionIsDeeplyIndependent(t *testing.T) {
	start := gvStart()
	task := workflow.GroupTask{Key: "a", Entry: "a1", Inputs: map[string]any{"whole": "{{cfg}}"}}

	a, err := engine.ComposeTaskVariables(start, task, gvTaskWS, gvParentWS)
	require.NoError(t, err)
	b, err := engine.ComposeTaskVariables(start, task, "/ws/instances/task-b", gvParentWS)
	require.NoError(t, err)

	a["cfg"].(map[string]any)["k"].(map[string]any)["leaf"] = "A"
	a["whole"].(map[string]any)["k"].(map[string]any)["leaf"] = "A"

	assert.Equal(t, "v", b["cfg"].(map[string]any)["k"].(map[string]any)["leaf"])
	assert.Equal(t, "v", b["whole"].(map[string]any)["k"].(map[string]any)["leaf"])
	assert.Equal(t, "v", start["cfg"].(map[string]any)["k"].(map[string]any)["leaf"], "the stored start snapshot is never reachable from a task")
	assert.Equal(t, gvParentWS, start["workspace"], "the input map is not modified")
}

func TestComposeTaskVariables_MissingReferenceNamesTheTask(t *testing.T) {
	task := workflow.GroupTask{Key: "a", Entry: "a1", Inputs: map[string]any{"x": "{{nope.path}}"}}

	got, err := engine.ComposeTaskVariables(gvStart(), task, gvTaskWS, gvParentWS)
	require.Error(t, err)
	assert.Nil(t, got)
	assert.ErrorIs(t, err, variable.ErrUnresolvedReference)
	assert.Contains(t, err.Error(), `task "a"`)
	assert.Contains(t, err.Error(), "{{nope.path}}")
}

func TestComposeTaskVariables_WorkspaceReferenceWithoutATaskAreaIsMissing(t *testing.T) {
	task := workflow.GroupTask{Key: "a", Entry: "a1", Inputs: map[string]any{"work_dir": "{{workspace}}/repo"}}

	_, err := engine.ComposeTaskVariables(gvStart(), task, "", "")
	assert.ErrorIs(t, err, variable.ErrUnresolvedReference,
		"the parent's workspace is never a fallback for a task's {{workspace}}")
}

func TestComposeTaskVariables_ReservedInputRejected(t *testing.T) {
	task := workflow.GroupTask{Key: "a", Entry: "a1", Inputs: map[string]any{"workspace": "/elsewhere"}}

	_, err := engine.ComposeTaskVariables(gvStart(), task, gvTaskWS, gvParentWS)
	assert.ErrorIs(t, err, workflow.ErrGroupReservedInput)
}

func TestComposeTaskVariables_NilStartAndInputsYieldAnEmptyMap(t *testing.T) {
	got, err := engine.ComposeTaskVariables(nil, workflow.GroupTask{Key: "a", Entry: "a1"}, "", "")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Empty(t, got)
}

func TestComposeTaskVariables_SharedPathRejection(t *testing.T) {
	cases := []struct {
		name    string
		extra   map[string]any
		inputs  map[string]any
		parent  string
		wantErr bool
		wantAt  string
	}{
		{name: "inherited top-level path inside the parent's area", extra: map[string]any{"work_dir": gvParentWS + "/repo"},
			parent: gvParentWS, wantErr: true, wantAt: `"work_dir"`},
		{name: "inherited path equal to the parent's area", extra: map[string]any{"root": gvParentWS},
			parent: gvParentWS, wantErr: true, wantAt: `"root"`},
		{name: "unclean inherited path still detected", extra: map[string]any{"work_dir": gvParentWS + "/./repo/"},
			parent: gvParentWS, wantErr: true, wantAt: `"work_dir"`},
		{name: "explicit nested input inside the parent's area",
			inputs: map[string]any{"opts": map[string]any{"paths": []any{"/tmp/ok", gvParentWS + "/data"}}},
			parent: gvParentWS, wantErr: true, wantAt: `"opts.paths.1"`},
		{name: "explicit string-slice input inside the parent's area",
			inputs: map[string]any{"dirs": []string{gvParentWS + "/x"}}, parent: gvParentWS, wantErr: true, wantAt: `"dirs.0"`},
		{name: "an explicit override of the inherited path fixes it", extra: map[string]any{"work_dir": gvParentWS + "/repo"},
			inputs: map[string]any{"work_dir": "{{workspace}}/repo"}, parent: gvParentWS},
		{name: "a sibling directory sharing a name prefix is not inside", extra: map[string]any{"other": gvParentWS + "-other/x"},
			parent: gvParentWS},
		{name: "relative paths are not inferred to be paths", extra: map[string]any{"rel": "repo/sub"}, parent: gvParentWS},
		{name: "inherited nested map path is rejected",
			extra: map[string]any{"meta": map[string]any{"dir": gvParentWS + "/x"}}, parent: gvParentWS, wantErr: true, wantAt: `"meta.dir"`},
		{name: "inherited array path is rejected",
			extra: map[string]any{"meta": []any{map[string]any{"dir": gvParentWS + "/x"}}}, parent: gvParentWS, wantErr: true, wantAt: `"meta.0.dir"`},
		{name: "explicit task-local replacement repairs an inherited nested path",
			extra: map[string]any{"meta": map[string]any{"dir": gvParentWS + "/x"}},
			inputs: map[string]any{"meta": map[string]any{"dir": "{{workspace}}/x"}}, parent: gvParentWS},
		{name: "no parent area disables the check", extra: map[string]any{"work_dir": gvParentWS + "/repo"}, parent: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := gvStart()
			for k, v := range c.extra {
				start[k] = v
			}
			taskWS := gvTaskWS
			if c.parent == "" {
				taskWS = ""
			}
			got, err := engine.ComposeTaskVariables(start, workflow.GroupTask{Key: "a", Entry: "a1", Inputs: c.inputs}, taskWS, c.parent)
			if !c.wantErr {
				require.NoError(t, err)
				require.NotNil(t, got)
				return
			}
			require.Error(t, err)
			assert.Nil(t, got)
			assert.ErrorIs(t, err, engine.ErrGroupSharedPath)
			assert.Contains(t, err.Error(), c.wantAt)
			assert.Contains(t, err.Error(), "{{workspace}}", "the error tells the author how to fix it")
		})
	}
}
```

- [x] Step 2: Run it, confirm it fails.

```bash
go test ./domain/engine/ -run 'TestComposeTaskVariables' -count=1
```
Expected:
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/group_task_vars_test.go:38:14: undefined: engine.ComposeTaskVariables
domain/engine/group_task_vars_test.go:180:27: undefined: engine.ErrGroupSharedPath
...
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Implement. Create `domain/engine/group_task_vars.go`:

```go
package engine

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ErrGroupSharedPath is returned when a task would start with a variable that
// points INTO the parent's working area. Every task gets its own fork of that
// area; a path back into the parent's mutable copy would let parallel tasks
// write the same files and defeat the isolation the group promises.
var ErrGroupSharedPath = errors.New("task variable points into the parent's working area")

// ComposeTaskVariables builds ONE task's starting variables.
//
//  1. A deep copy of the group-start snapshot, minus every reserved name: the
//     engine re-seeds "workspace" for the task's own run, so the parent's path
//     must never ride along.
//  2. Overlaid with the task's inputs, resolved by the typed resolver against
//     the START values plus workspace = taskWorkspace. A whole-value
//     "{{path}}" keeps its type; "{{workspace}}/repo" binds to the TASK's own
//     area (task-local path binding); a missing reference is an error.
//  3. When parentWorkspace is set, refused if any composed value
//     (including nested inherited values) is an absolute path equal to or
//     inside the parent's area. Arbitrary strings are not rewritten — the
//     author is told to use {{workspace}} instead.
//
// The returned map shares nothing with start or with any other task's map.
func ComposeTaskVariables(start map[string]any, task workflow.GroupTask, taskWorkspace, parentWorkspace string) (map[string]any, error) {
	for _, k := range sortedMapKeys(task.Inputs) {
		if variable.ReservedInputNames[k] {
			return nil, fmt.Errorf("task %q: %w: input %q is seeded by the engine for every task", task.Key, workflow.ErrGroupReservedInput, k)
		}
	}

	composed := variable.DeepCopyMap(start)
	if composed == nil {
		composed = map[string]any{}
	}
	for k := range variable.ReservedInputNames {
		delete(composed, k)
	}

	store := variable.NewStore()
	for k, v := range composed {
		if err := store.SetInput(k, v); err != nil {
			return nil, fmt.Errorf("task %q: seed resolution store: %w", task.Key, err)
		}
	}
	if taskWorkspace != "" {
		store.SetReserved(ReservedVarWorkspace, taskWorkspace)
	}

	resolved, err := variable.ResolveTypedMap(task.Inputs, store)
	if err != nil {
		return nil, fmt.Errorf("task %q inputs: %w", task.Key, err)
	}
	for k, v := range resolved {
		composed[k] = v
	}

	if parentWorkspace != "" {
		if err := rejectSharedPaths(task.Key, composed, parentWorkspace); err != nil {
			return nil, err
		}
	}
	return composed, nil
}

// rejectSharedPaths enforces step 3 of ComposeTaskVariables. Keys are visited
// in sorted order so the reported key is deterministic.
func rejectSharedPaths(taskKey string, composed map[string]any, parentWorkspace string) error {
	root := filepath.Clean(parentWorkspace)
	for _, k := range sortedMapKeys(composed) {
		if at, p, found := findSharedPath(composed[k], root, k); found {
			return sharedPathError(taskKey, at, p, root)
		}
	}
	return nil
}

func findSharedPath(v any, root, at string) (string, string, bool) {
	switch t := v.(type) {
	case string:
		if pathWithin(t, root) {
			return at, t, true
		}
	case map[string]any:
		for _, k := range sortedMapKeys(t) {
			if a, p, ok := findSharedPath(t[k], root, at+"."+k); ok {
				return a, p, true
			}
		}
	case []any:
		for i, e := range t {
			if a, p, ok := findSharedPath(e, root, at+"."+strconv.Itoa(i)); ok {
				return a, p, true
			}
		}
	case []string:
		for i, e := range t {
			if pathWithin(e, root) {
				return at + "." + strconv.Itoa(i), e, true
			}
		}
	case []map[string]any:
		for i, m := range t {
			if a, p, ok := findSharedPath(m, root, at+"."+strconv.Itoa(i)); ok {
				return a, p, true
			}
		}
	}
	return "", "", false
}

// pathWithin reports whether s is an absolute path equal to or below root.
// Only absolute paths are considered: provenance is never inferred from an
// arbitrary relative string.
func pathWithin(s, root string) bool {
	if !filepath.IsAbs(s) {
		return false
	}
	c := filepath.Clean(s)
	return c == root || strings.HasPrefix(c, root+string(filepath.Separator))
}

func sharedPathError(taskKey, at, value, root string) error {
	return fmt.Errorf("%w: task %q variable %q = %q is inside the parent's working area %q; every task works in its own copy — "+
		"set it from the task's inputs relative to {{workspace}} (e.g. \"{{workspace}}/repo\") instead",
		ErrGroupSharedPath, taskKey, at, value, root)
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
```

- [x] Step 4: Run it, confirm it passes.

```bash
go test ./domain/engine/ -run 'TestComposeTaskVariables' -count=1 -v
```
Expected:
```
--- PASS: TestComposeTaskVariables_BaseCopyMinusReservedPlusTypedInputs (0.00s)
...
--- PASS: TestComposeTaskVariables_SharedPathRejection (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
```

- [x] Step 5: Commit.

```bash
git add domain/engine/group_task_vars.go domain/engine/group_task_vars_test.go
git commit -m "feat(engine): compose isolated, typed, task-local parallel-group task variables" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### Cycle 2 — shared fakes, events and the launcher

- [x] Step 6: Create the shared fakes. Create `domain/engine/group_fakes_test.go`:

```go
package engine_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// memGroupRuns: an in-memory instance.GroupRunRepository that honours every
// compare-and-set the contract specifies (keyed by run id and task key), deep
// copies on every read and write, and supports per-method error injection.
// ---------------------------------------------------------------------------

type memGroupRuns struct {
	mu         sync.Mutex
	runs       map[uuid.UUID]*instance.GroupRun
	order      []uuid.UUID
	seq        int64
	failNext   map[string]error // method -> error returned ONCE, before any mutation
	failAll    map[string]error // method -> error returned on every call
	calls      []string         // "Method" or "Method:detail", in call order
	createdRaw []*instance.GroupRun
	onCall     func(method string)
}

var _ instance.GroupRunRepository = (*memGroupRuns)(nil)

func newMemGroupRuns() *memGroupRuns {
	return &memGroupRuns{
		runs:     map[uuid.UUID]*instance.GroupRun{},
		failNext: map[string]error{},
		failAll:  map[string]error{},
	}
}

func (m *memGroupRuns) failOnce(method string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failNext[method] = err
}

func (m *memGroupRuns) failAlways(method string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failAll[method] = err
}

func (m *memGroupRuns) begin(method, detail string) error {
	m.mu.Lock()
	hook := m.onCall
	m.mu.Unlock()
	if hook != nil {
		hook(method)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := method
	if detail != "" {
		entry += ":" + detail
	}
	m.calls = append(m.calls, entry)
	if err, ok := m.failNext[method]; ok {
		delete(m.failNext, method)
		return err
	}
	if err, ok := m.failAll[method]; ok {
		return err
	}
	return nil
}

func (m *memGroupRuns) callCount(entry string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if c == entry {
			n++
		}
	}
	return n
}

func (m *memGroupRuns) seedRun(run *instance.GroupRun) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := copyGroupRun(run)
	m.runs[cp.ID] = cp
	m.order = append(m.order, cp.ID)
	for _, t := range cp.Tasks {
		if t.TerminalSeq != nil && *t.TerminalSeq > m.seq {
			m.seq = *t.TerminalSeq
		}
	}
}

func (m *memGroupRuns) get(id uuid.UUID) *instance.GroupRun {
	m.mu.Lock()
	defer m.mu.Unlock()
	return copyGroupRun(m.runs[id])
}

func (m *memGroupRuns) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.runs)
}

func copyGroupRun(r *instance.GroupRun) *instance.GroupRun {
	if r == nil {
		return nil
	}
	cp := *r
	cp.Config = variable.DeepCopyMap(r.Config)
	cp.StartVariables = variable.DeepCopyMap(r.StartVariables)
	cp.FinalOutput = variable.DeepCopyMap(r.FinalOutput)
	if r.BodyOwnership != nil {
		cp.BodyOwnership = make(map[string][]string, len(r.BodyOwnership))
		for k, v := range r.BodyOwnership {
			cp.BodyOwnership[k] = append([]string(nil), v...)
		}
	}
	if r.Decision != nil {
		d := copyDecision(*r.Decision)
		cp.Decision = &d
	}
	cp.Tasks = make([]instance.GroupTask, len(r.Tasks))
	for i, t := range r.Tasks {
		t.Result = variable.DeepCopyMap(t.Result)
		t.Failure = variable.DeepCopyMap(t.Failure)
		if t.TerminalSeq != nil {
			s := *t.TerminalSeq
			t.TerminalSeq = &s
		}
		if t.TerminalAt != nil {
			a := *t.TerminalAt
			t.TerminalAt = &a
		}
		cp.Tasks[i] = t
	}
	sort.SliceStable(cp.Tasks, func(i, j int) bool { return cp.Tasks[i].Position < cp.Tasks[j].Position })
	return &cp
}

func copyDecision(d instance.GroupDecision) instance.GroupDecision {
	tasks := make([]instance.FrozenTask, len(d.Tasks))
	for i, ft := range d.Tasks {
		ft.Result = variable.DeepCopyMap(ft.Result)
		ft.Failure = variable.DeepCopyMap(ft.Failure)
		tasks[i] = ft
	}
	d.Tasks = tasks
	return d
}

func memTerminal(s instance.GroupTaskStatus) bool {
	return s == instance.GroupTaskStatusCompleted || s == instance.GroupTaskStatusFailed || s == instance.GroupTaskStatusCancelled
}

// taskLocked returns the stored run and task. Caller holds m.mu.
func (m *memGroupRuns) taskLocked(runID uuid.UUID, key string) (*instance.GroupRun, *instance.GroupTask, error) {
	run, ok := m.runs[runID]
	if !ok {
		return nil, nil, fmt.Errorf("group run %s: %w", runID, instance.ErrGroupRunNotFound)
	}
	for i := range run.Tasks {
		if run.Tasks[i].Key == key {
			return run, &run.Tasks[i], nil
		}
	}
	return run, nil, fmt.Errorf("group run %s has no task %q", runID, key)
}

func (m *memGroupRuns) runLocked(runID uuid.UUID) (*instance.GroupRun, error) {
	run, ok := m.runs[runID]
	if !ok {
		return nil, fmt.Errorf("group run %s: %w", runID, instance.ErrGroupRunNotFound)
	}
	return run, nil
}

func (m *memGroupRuns) CreateWithTasks(_ context.Context, run *instance.GroupRun) error {
	if err := m.begin("CreateWithTasks", ""); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runs {
		if r.ParentInstanceID == run.ParentInstanceID && r.NodeID == run.NodeID && r.Attempt == run.Attempt {
			return fmt.Errorf("group run for %s/%s attempt %d: %w", run.ParentInstanceID, run.NodeID, run.Attempt, instance.ErrGroupRunExists)
		}
	}
	m.createdRaw = append(m.createdRaw, run)
	cp := copyGroupRun(run)
	m.runs[cp.ID] = cp
	m.order = append(m.order, cp.ID)
	return nil
}

func (m *memGroupRuns) Get(_ context.Context, id uuid.UUID) (*instance.GroupRun, error) {
	if err := m.begin("Get", ""); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.runLocked(id)
	if err != nil {
		return nil, err
	}
	return copyGroupRun(run), nil
}

func (m *memGroupRuns) GetByParentNode(_ context.Context, parentID uuid.UUID, nodeID string) (*instance.GroupRun, error) {
	if err := m.begin("GetByParentNode", nodeID); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *instance.GroupRun
	for _, id := range m.order {
		r := m.runs[id]
		if r.ParentInstanceID == parentID && r.NodeID == nodeID && (latest == nil || r.Attempt > latest.Attempt) {
			latest = r
		}
	}
	return copyGroupRun(latest), nil
}

func (m *memGroupRuns) GetByChild(_ context.Context, childID uuid.UUID) (*instance.GroupRun, error) {
	if err := m.begin("GetByChild", ""); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.order {
		for _, t := range m.runs[id].Tasks {
			if t.ChildInstanceID == childID {
				return copyGroupRun(m.runs[id]), nil
			}
		}
	}
	return nil, nil
}

func (m *memGroupRuns) ListByParent(_ context.Context, parentID uuid.UUID) ([]instance.GroupRun, error) {
	if err := m.begin("ListByParent", ""); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []instance.GroupRun
	for _, id := range m.order {
		if m.runs[id].ParentInstanceID == parentID {
			out = append(out, *copyGroupRun(m.runs[id]))
		}
	}
	return out, nil
}

func (m *memGroupRuns) ListUnsettled(_ context.Context) ([]uuid.UUID, error) {
	if err := m.begin("ListUnsettled", ""); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []uuid.UUID{}
	for _, id := range m.order {
		r := m.runs[id]
		if r.State != instance.GroupRunDone || !r.ContinuationDelivered || !r.ActionsApplied {
			out = append(out, id)
		}
	}
	return out, nil
}

func (m *memGroupRuns) MarkTaskLaunched(_ context.Context, runID uuid.UUID, key string) error {
	if err := m.begin("MarkTaskLaunched", key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, t, err := m.taskLocked(runID, key)
	if err != nil {
		return err
	}
	t.LaunchState = instance.GroupTaskLaunched
	if t.LiveStatus == instance.GroupTaskStatusNotStarted {
		t.LiveStatus = instance.GroupTaskStatusQueued
	}
	run.UpdatedAt = time.Now()
	return nil
}

func (m *memGroupRuns) MarkTaskLaunchFailed(_ context.Context, runID uuid.UUID, key, reason string) error {
	if err := m.begin("MarkTaskLaunchFailed", key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, t, err := m.taskLocked(runID, key)
	if err != nil {
		return err
	}
	t.LaunchState = instance.GroupTaskLaunchFailed
	t.LaunchError = reason
	run.UpdatedAt = time.Now()
	return nil
}

func (m *memGroupRuns) TransitionState(_ context.Context, runID uuid.UUID, from, to instance.GroupRunState) (bool, error) {
	if err := m.begin("TransitionState", string(from)+"->"+string(to)); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.runLocked(runID)
	if err != nil {
		return false, err
	}
	if run.State != from {
		return false, nil
	}
	run.State = to
	run.UpdatedAt = time.Now()
	return true, nil
}

func (m *memGroupRuns) RecordTaskTerminal(_ context.Context, runID uuid.UUID, key string, status instance.GroupTaskStatus, result, failure map[string]any, at time.Time) (int64, bool, error) {
	if err := m.begin("RecordTaskTerminal", key); err != nil {
		return 0, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, t, err := m.taskLocked(runID, key)
	if err != nil {
		return 0, false, err
	}
	if t.TerminalSeq != nil {
		return *t.TerminalSeq, false, nil
	}
	m.seq++
	s := m.seq
	when := at
	t.TerminalSeq = &s
	t.TerminalAt = &when
	t.LiveStatus = status
	t.Result = variable.DeepCopyMap(result)
	t.Failure = variable.DeepCopyMap(failure)
	run.UpdatedAt = time.Now()
	return s, true, nil
}

func (m *memGroupRuns) UpdateTaskLiveStatus(_ context.Context, runID uuid.UUID, key string, status instance.GroupTaskStatus) error {
	if err := m.begin("UpdateTaskLiveStatus", key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, t, err := m.taskLocked(runID, key)
	if err != nil {
		return err
	}
	if memTerminal(t.LiveStatus) && !memTerminal(status) {
		return nil
	}
	t.LiveStatus = status
	return nil
}

func (m *memGroupRuns) Decide(_ context.Context, runID uuid.UUID, d instance.GroupDecision) (bool, error) {
	if err := m.begin("Decide", ""); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.runLocked(runID)
	if err != nil {
		return false, err
	}
	if run.Decision != nil || (run.State != instance.GroupRunLaunching && run.State != instance.GroupRunRunning) {
		return false, nil
	}
	cp := copyDecision(d)
	run.Decision = &cp
	run.State = instance.GroupRunDecided
	for i := range run.Tasks {
		run.Tasks[i].Blocking = false
	}
	run.UpdatedAt = time.Now()
	return true, nil
}

func (m *memGroupRuns) MarkTaskCancelRequested(_ context.Context, runID uuid.UUID, key string) error {
	if err := m.begin("MarkTaskCancelRequested", key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, t, err := m.taskLocked(runID, key)
	if err != nil {
		return err
	}
	t.CancelRequested = true
	return nil
}

func (m *memGroupRuns) MarkTaskContinuingIndependently(_ context.Context, runID uuid.UUID, key string) error {
	if err := m.begin("MarkTaskContinuingIndependently", key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, t, err := m.taskLocked(runID, key)
	if err != nil {
		return err
	}
	t.ContinuingIndependently = true
	return nil
}

func (m *memGroupRuns) MarkActionsApplied(_ context.Context, runID uuid.UUID) error {
	if err := m.begin("MarkActionsApplied", ""); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.runLocked(runID)
	if err != nil {
		return err
	}
	run.ActionsApplied = true
	return nil
}

func (m *memGroupRuns) SetFinalOutput(_ context.Context, runID uuid.UUID, port string, output map[string]any, from instance.GroupRunState) (bool, error) {
	if err := m.begin("SetFinalOutput", port); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.runLocked(runID)
	if err != nil {
		return false, err
	}
	if run.State != from || run.FinalOutput != nil {
		return false, nil
	}
	run.FinalOutput = variable.DeepCopyMap(output)
	run.ContinuationPort = port
	run.ContinuationReady = true
	run.State = instance.GroupRunDone
	run.UpdatedAt = time.Now()
	return true, nil
}

func (m *memGroupRuns) MarkContinuationDelivered(_ context.Context, runID uuid.UUID) error {
	if err := m.begin("MarkContinuationDelivered", ""); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.runLocked(runID)
	if err != nil {
		return err
	}
	run.ContinuationDelivered = true
	return nil
}

// ---------------------------------------------------------------------------
// fakeGroupTaskStarter: records every StartGroupTask call with the identity on
// ctx, fails per TASK KEY, and (when instances is set) creates one child row
// per reserved id — idempotently, exactly like Engine.StartGroupTask.
// ---------------------------------------------------------------------------

type fakeGroupTaskStarter struct {
	mu         sync.Mutex
	reqs       []engine.GroupTaskStartRequest
	idents     []identity.Identity
	errByKey   map[string]error
	onStart    func(req engine.GroupTaskStartRequest)
	instances  *mockInstanceRepo
	childState instance.State
}

var _ engine.GroupTaskStarter = (*fakeGroupTaskStarter)(nil)

func (s *fakeGroupTaskStarter) StartGroupTask(ctx context.Context, req engine.GroupTaskStartRequest) error {
	who, _ := identity.FromContext(ctx)
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.idents = append(s.idents, who)
	hook := s.onStart
	err := s.errByKey[req.TaskKey]
	repo := s.instances
	state := s.childState
	s.mu.Unlock()
	if hook != nil {
		hook(req)
	}
	if err != nil {
		return err
	}
	if repo == nil {
		return nil
	}
	if state == "" {
		state = instance.StateQueued
	}
	repo.mu.Lock()
	_, exists := repo.instances[req.TaskInstanceID]
	repo.mu.Unlock()
	if exists {
		return nil
	}
	parentID, parentNode, runID, key := req.ParentInstanceID, req.ParentNodeID, req.GroupRunID, req.TaskKey
	now := time.Now()
	return repo.Create(ctx, &instance.Instance{
		ID: req.TaskInstanceID, OwnerID: who.UserID, WorkflowName: req.ChildWorkflow,
		ParentInstanceID: &parentID, ParentNodeID: &parentNode, SpawnMode: instance.SpawnModeGroupTask,
		GroupRunID: &runID, GroupTaskKey: &key, GroupTaskKind: req.Kind,
		State: state, Priority: instance.Priority(req.Priority),
		Variables: variable.DeepCopyMap(req.Variables), CreatedAt: now, UpdatedAt: now,
	})
}

func (s *fakeGroupTaskStarter) requests() []engine.GroupTaskStartRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]engine.GroupTaskStartRequest(nil), s.reqs...)
}

func (s *fakeGroupTaskStarter) identities() []identity.Identity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]identity.Identity(nil), s.idents...)
}

// ---------------------------------------------------------------------------
// recordingCanceller: an engine.RunCanceller that records (id, identity) and,
// when instances is set, behaves like Engine.Cancel's state outcome: an absent
// row is ErrNotFound, a terminal row ErrCannotCancel, otherwise cancelled.
// ---------------------------------------------------------------------------

type cancelCall struct {
	ID       uuid.UUID
	Identity identity.Identity
}

type recordingCanceller struct {
	mu        sync.Mutex
	calls     []cancelCall
	errByID   map[uuid.UUID]error
	failNext  error
	instances *mockInstanceRepo
	onTerminal func(id uuid.UUID) // simulates the atomic persistence boundary
	onCancel  func(id uuid.UUID)
}

var _ engine.RunCanceller = (*recordingCanceller)(nil)

func (c *recordingCanceller) Cancel(ctx context.Context, id uuid.UUID) error {
	who, _ := identity.FromContext(ctx)
	c.mu.Lock()
	c.calls = append(c.calls, cancelCall{ID: id, Identity: who})
	err := c.errByID[id]
	if c.failNext != nil {
		err = c.failNext
		c.failNext = nil
	}
	repo := c.instances
	terminalHook := c.onTerminal
	hook := c.onCancel
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if repo == nil {
		return nil
	}
	repo.mu.Lock()
	inst, ok := repo.instances[id]
	if !ok {
		repo.mu.Unlock()
		return fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	if inst.State.IsTerminal() {
		repo.mu.Unlock()
		return fmt.Errorf("instance %s cannot be cancelled (state: %s): %w", id, inst.State, instance.ErrCannotCancel)
	}
	now := time.Now()
	inst.State = instance.StateCancelled
	inst.CompletedAt = &now
	inst.UpdatedAt = now
	repo.mu.Unlock()
	if terminalHook != nil { terminalHook(id) }
	if hook != nil {
		hook(id)
	}
	return nil
}

func (c *recordingCanceller) callsFor(id uuid.UUID) []cancelCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []cancelCall
	for _, call := range c.calls {
		if call.ID == id {
			out = append(out, call)
		}
	}
	return out
}

func (c *recordingCanceller) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

// ---------------------------------------------------------------------------
// fakeChildContracts: an engine.ChildWorkflowReader keyed by workflow NAME that
// records the identity each lookup was made under.
// ---------------------------------------------------------------------------

type fakeChildContracts struct {
	mu     sync.Mutex
	byName map[string][]workflow.InputField
	err    error
	asked  []identity.Identity
	names  []string
}

var _ engine.ChildWorkflowReader = (*fakeChildContracts)(nil)

func (f *fakeChildContracts) ChildInputs(ctx context.Context, name string) ([]workflow.InputField, error) {
	who, _ := identity.FromContext(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, who)
	f.names = append(f.names, name)
	if f.err != nil {
		return nil, f.err
	}
	fields, ok := f.byName[name]
	if !ok {
		return nil, fmt.Errorf("child inputs %q: %w", name, workflow.ErrNotFound)
	}
	return fields, nil
}

// ---------------------------------------------------------------------------
// dirWorkspaces: a real-directory engine.WorkspaceManager under a temp root,
// laid out like adapter/filesystem (<root>/instances/<id>).
// ---------------------------------------------------------------------------

type dirWorkspaces struct {
	root      string
	mu        sync.Mutex
	ensured   []uuid.UUID
	removed   []uuid.UUID
	ensureErr error
}

var _ engine.WorkspaceManager = (*dirWorkspaces)(nil)

func newDirWorkspaces(t *testing.T) *dirWorkspaces {
	t.Helper()
	return &dirWorkspaces{root: t.TempDir()}
}

func (w *dirWorkspaces) path(id uuid.UUID) string {
	return filepath.Join(w.root, "instances", id.String())
}

func (w *dirWorkspaces) EnsureInstanceDir(id uuid.UUID) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ensureErr != nil {
		return "", w.ensureErr
	}
	p := w.path(id)
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", err
	}
	w.ensured = append(w.ensured, id)
	return p, nil
}

func (w *dirWorkspaces) RemoveInstanceDir(id uuid.UUID) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.removed = append(w.removed, id)
	return os.RemoveAll(w.path(id))
}

func (w *dirWorkspaces) InstanceDirStats(id uuid.UUID) (int64, int, error) {
	var size int64
	files := 0
	err := filepath.WalkDir(w.path(id), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, ierr := d.Info()
			if ierr != nil {
				return ierr
			}
			size += info.Size()
			files++
		}
		return nil
	})
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	return size, files, err
}

func (w *dirWorkspaces) ensuredIDs() []uuid.UUID {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]uuid.UUID(nil), w.ensured...)
}

// ---------------------------------------------------------------------------
// copyGroupWorkspace: an engine.GroupWorkspace that really copies directory
// trees, so tests assert file content at the sink (the task directory).
// ---------------------------------------------------------------------------

type copyGroupWorkspace struct {
	root     string
	mu       sync.Mutex
	snapErr  error
	forkErr  error
	snaps    []string
	forks    []string
	released []string
}

var _ engine.GroupWorkspace = (*copyGroupWorkspace)(nil)

func newCopyGroupWorkspace(t *testing.T) *copyGroupWorkspace {
	t.Helper()
	return &copyGroupWorkspace{root: t.TempDir()}
}

func (g *copyGroupWorkspace) SnapshotParent(_ context.Context, groupRunID uuid.UUID, parentDir string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.snapErr != nil {
		return "", g.snapErr
	}
	dst := filepath.Join(g.root, ".group-snapshots", groupRunID.String())
	if err := copyDirTree(parentDir, dst); err != nil {
		return "", err
	}
	g.snaps = append(g.snaps, dst)
	return dst, nil
}

func (g *copyGroupWorkspace) ForkTask(_ context.Context, handle string, taskDir string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.forkErr != nil {
		return g.forkErr
	}
	if err := copyDirTree(handle, taskDir); err != nil {
		return err
	}
	g.forks = append(g.forks, taskDir)
	return nil
}

func (g *copyGroupWorkspace) ReleaseSnapshot(_ context.Context, handle string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.released = append(g.released, handle)
	return os.RemoveAll(handle)
}

func (g *copyGroupWorkspace) snapshotHandles() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.snaps...)
}

func (g *copyGroupWorkspace) releasedHandles() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.released...)
}

func copyDirTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}
```

- [x] Step 7: Write the failing launcher tests. Create `domain/engine/group_launcher_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type launchFixture struct {
	groups     *memGroupRuns
	starter    *fakeGroupTaskStarter
	contracts  *fakeChildContracts
	workspaces *dirWorkspaces
	groupWS    *copyGroupWorkspace
	identities *fakeIdentityResolver
	canceller  *recordingCanceller
	bus        *mockEventBus
	instances  *mockInstanceRepo
	launcher   *engine.GroupLauncher
	parent     *instance.Instance
	owner      uuid.UUID
	parentWS   string
}

func newLaunchFixture(t *testing.T) *launchFixture {
	t.Helper()
	owner := uuid.New()
	instances := newMockInstanceRepo()
	ws := newDirWorkspaces(t)
	parentID := uuid.New()
	parentWS := ws.path(parentID)
	require.NoError(t, os.MkdirAll(filepath.Join(parentWS, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(parentWS, "README.md"), []byte("hello group"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(parentWS, "src", "main.go"), []byte("package main\n"), 0o644))
	g := "g"
	parent := &instance.Instance{
		ID: parentID, OwnerID: owner, WorkflowName: "parent-wf", State: instance.StateRunning,
		Priority: instance.PriorityHigh, CurrentNodeID: &g,
	}
	require.NoError(t, instances.Create(context.Background(), parent))

	f := &launchFixture{
		groups:  newMemGroupRuns(),
		starter: &fakeGroupTaskStarter{instances: instances},
		contracts: &fakeChildContracts{byName: map[string][]workflow.InputField{
			"child-wf": {
				{Name: "repo", Type: "string", Required: true},
				{Name: "ticket", Type: "string", Required: true},
				{Name: "count", Type: "number"},
			},
		}},
		workspaces: ws,
		groupWS:    newCopyGroupWorkspace(t),
		identities: &fakeIdentityResolver{byUser: map[uuid.UUID]identity.Identity{
			owner: {UserID: owner, Role: identity.RoleUser, Departments: []string{"engineering"}},
		}},
		canceller: &recordingCanceller{instances: instances},
		bus:       newMockEventBus(),
		instances: instances,
		parent:    parent,
		owner:     owner,
		parentWS:  parentWS,
	}
	f.launcher = engine.NewGroupLauncher(f.groups, f.starter, f.contracts, f.workspaces, f.groupWS, f.identities, f.canceller, f.bus)
	return f
}

func glStore(t *testing.T, parentWS string) *variable.VariableStore {
	t.Helper()
	s := variable.NewStore()
	require.NoError(t, s.SetInput("repo", "acme/widgets"))
	require.NoError(t, s.SetInput("count", float64(3)))
	require.NoError(t, s.SetInput("cfg", map[string]any{"depth": map[string]any{"leaf": "v"}, "list": []any{"a"}}))
	s.SetNodeOutput("init", map[string]any{"ticket": "T-1"})
	if parentWS != "" {
		s.SetReserved("workspace", parentWS)
	}
	return s
}

func glExpectedStart() map[string]any {
	return map[string]any{
		"repo":  "acme/widgets",
		"count": float64(3),
		"cfg":   map[string]any{"depth": map[string]any{"leaf": "v"}, "list": []any{"a"}},
		"init":  map[string]any{"ticket": "T-1"},
	}
}

func glBranchConfig() workflow.GroupConfig {
	return workflow.GroupConfig{
		Form: workflow.GroupFormBranches,
		Tasks: []workflow.GroupTask{
			{Key: "a", Entry: "a1", Inputs: map[string]any{
				"n": "{{count}}", "cfg_copy": "{{cfg}}", "work_dir": "{{workspace}}/repo",
				"label": "task-{{repo}}", "flag": true,
			}},
			{Key: "b", Entry: "b1"},
		},
		CompletionRule:     workflow.GroupRuleAllSucceed,
		UnfinishedBehavior: workflow.GroupUnfinishedCancel,
		GatherMode:         workflow.GroupGatherResults,
	}
}

func glWorkflowsConfig() workflow.GroupConfig {
	return workflow.GroupConfig{
		Form: workflow.GroupFormWorkflows,
		Tasks: []workflow.GroupTask{
			{Key: "x", Workflow: "child-wf", Inputs: map[string]any{"ticket": "{{init.ticket}}"}},
			{Key: "y", Workflow: "child-wf", Inputs: map[string]any{"ticket": "T-2", "count": float64(7)}},
		},
		CompletionRule:     workflow.GroupRuleFirstSuccess,
		UnfinishedBehavior: workflow.GroupUnfinishedFinishIndependently,
		GatherMode:         workflow.GroupGatherResults,
	}
}

func glScope(cfg workflow.GroupConfig) *workflow.GroupScope {
	bodies := map[string][]string{}
	for _, task := range cfg.Tasks {
		if task.Entry != "" {
			bodies[task.Key] = []string{task.Entry}
		}
	}
	return &workflow.GroupScope{
		NodeID: "g", Config: cfg, Bodies: bodies, Nested: []string{},
		SuccessTarget: "after", FailureTarget: "fail",
	}
}

func (f *launchFixture) requestWith(t *testing.T, cfg workflow.GroupConfig, store *variable.VariableStore) engine.GroupLaunchRequest {
	t.Helper()
	c := cfg
	return engine.GroupLaunchRequest{
		Parent:    f.parent,
		Node:      workflow.Node{ID: "g", Type: workflow.NodeTypeParallelGroup, Config: workflow.NodeConfig{Group: &c}},
		Config:    cfg,
		Ownership: glScope(cfg),
		Vars:      store,
	}
}

func (f *launchFixture) request(t *testing.T, cfg workflow.GroupConfig) engine.GroupLaunchRequest {
	t.Helper()
	return f.requestWith(t, cfg, glStore(t, f.parentWS))
}

func (f *launchFixture) assertNothingPersisted(t *testing.T) {
	t.Helper()
	assert.Equal(t, 0, f.groups.count(), "no group run may be persisted by a pre-launch failure")
	assert.Empty(t, f.starter.requests(), "no task may be started by a pre-launch failure")
	for _, id := range f.workspaces.ensuredIDs() {
		assert.NoDirExists(t, f.workspaces.path(id), "prepared task directories are removed")
	}
	assert.ElementsMatch(t, f.groupWS.snapshotHandles(), f.groupWS.releasedHandles(), "a snapshot taken is released")
}

// ---------- happy path ----------

func TestGroupLauncher_PersistsTheRosterBeforeLaunchingAndOpensTheBarrierLast(t *testing.T) {
	f := newLaunchFixture(t)
	cfg := glBranchConfig()
	var atFirst, atSecond *instance.GroupRun
	f.starter.onStart = func(req engine.GroupTaskStartRequest) {
		snap := f.groups.get(req.GroupRunID)
		if req.TaskKey == "a" {
			atFirst = snap
		} else {
			atSecond = snap
		}
	}

	run, err := f.launcher.Launch(context.Background(), f.request(t, cfg))
	require.NoError(t, err)
	require.NotNil(t, run)

	require.NotNil(t, atFirst, "the roster must be persisted BEFORE the first task is started")
	assert.Equal(t, instance.GroupRunLaunching, atFirst.State)
	require.Len(t, atFirst.Tasks, 2)
	for _, task := range atFirst.Tasks {
		assert.Equal(t, instance.GroupTaskPending, task.LaunchState)
		assert.NotEqual(t, uuid.Nil, task.ChildInstanceID, "child ids are reserved in the roster before enqueue")
	}
	require.NotNil(t, atSecond)
	assert.Equal(t, instance.GroupRunLaunching, atSecond.State, "decisions stay disabled until every task is launched")
	assert.Equal(t, instance.GroupTaskLaunched, atSecond.Tasks[0].LaunchState)

	assert.Equal(t, instance.GroupRunRunning, run.State)
	assert.Equal(t, f.parent.ID, run.ParentInstanceID)
	assert.Equal(t, "g", run.NodeID)
	assert.Equal(t, 1, run.Attempt)
	assert.Equal(t, workflow.GroupFormBranches, run.Form)
	assert.Equal(t, workflow.GroupRuleAllSucceed, run.CompletionRule)
	assert.Equal(t, workflow.GroupUnfinishedCancel, run.UnfinishedBehavior)
	assert.Equal(t, workflow.GroupGatherResults, run.GatherMode)
	assert.Equal(t, "branches", run.Config["form"])
	assert.Equal(t, map[string][]string{"a": {"a1"}, "b": {"b1"}}, run.BodyOwnership)
	assert.Equal(t, glExpectedStart(), run.StartVariables, "start variables are captured once, minus reserved names")
	assert.NotEmpty(t, run.WorkspaceSnapshot)

	reqs := f.starter.requests()
	require.Len(t, reqs, 2)
	idents := f.starter.identities()
	for i, key := range []string{"a", "b"} {
		task := run.Tasks[i]
		assert.Equal(t, key, task.Key)
		assert.Equal(t, i, task.Position)
		assert.Equal(t, instance.GroupTaskKindBranch, task.Kind)
		assert.Equal(t, key+"1", task.Origin)
		assert.Equal(t, instance.GroupTaskLaunched, task.LaunchState)
		assert.True(t, task.Blocking)
		assert.Equal(t, task.ChildInstanceID, reqs[i].TaskInstanceID, "the started child uses the id the roster reserved")
		assert.Equal(t, key, reqs[i].TaskKey)
		assert.Equal(t, instance.GroupTaskKindBranch, reqs[i].Kind)
		assert.Equal(t, key+"1", reqs[i].BranchEntry)
		assert.Empty(t, reqs[i].ChildWorkflow)
		assert.Equal(t, f.parent.ID, reqs[i].ParentInstanceID)
		assert.Equal(t, "g", reqs[i].ParentNodeID)
		assert.Equal(t, run.ID, reqs[i].GroupRunID)
		assert.Equal(t, "high", reqs[i].Priority, "tasks inherit the parent's priority")
		assert.Equal(t, f.owner, idents[i].UserID, "tasks start under the reconstructed OWNER identity")
		assert.Equal(t, []string{"engineering"}, idents[i].Departments)
	}

	events := f.bus.eventsOfType(f.parent.ID, engine.EventTypeGroupLaunched)
	require.Len(t, events, 1)
	data := events[0].Data.(map[string]any)
	assert.Equal(t, run.ID.String(), data["groupRunId"])
	assert.Equal(t, "g", data["nodeId"])
	assert.Equal(t, 2, data["tasks"])
}

func TestGroupLauncher_StartVariablesAndFilesAreIdenticalInBothForms(t *testing.T) {
	cases := []struct {
		name string
		cfg  workflow.GroupConfig
		want func(taskDir string) []map[string]any
	}{
		{name: "branches", cfg: glBranchConfig(), want: func(dirA string) []map[string]any {
			a := glExpectedStart()
			a["n"] = float64(3)
			a["cfg_copy"] = map[string]any{"depth": map[string]any{"leaf": "v"}, "list": []any{"a"}}
			a["work_dir"] = dirA + "/repo"
			a["label"] = "task-acme/widgets"
			a["flag"] = true
			return []map[string]any{a, glExpectedStart()}
		}},
		{name: "workflows (one child workflow reused with different inputs)", cfg: glWorkflowsConfig(), want: func(string) []map[string]any {
			x := glExpectedStart()
			x["ticket"] = "T-1"
			y := glExpectedStart()
			y["ticket"] = "T-2"
			y["count"] = float64(7)
			return []map[string]any{x, y}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newLaunchFixture(t)
			_, err := f.launcher.Launch(context.Background(), f.request(t, c.cfg))
			require.NoError(t, err)

			reqs := f.starter.requests()
			require.Len(t, reqs, 2)
			want := c.want(f.workspaces.path(reqs[0].TaskInstanceID))
			for i, r := range reqs {
				assert.Equal(t, want[i], r.Variables, "task %s", r.TaskKey)
				assert.NotContains(t, r.Variables, "workspace", "each task's own run seeds its workspace")

				dir := f.workspaces.path(r.TaskInstanceID)
				readme, rerr := os.ReadFile(filepath.Join(dir, "README.md"))
				require.NoError(t, rerr, "task %s must start with the parent's files", r.TaskKey)
				assert.Equal(t, "hello group", string(readme))
				src, serr := os.ReadFile(filepath.Join(dir, "src", "main.go"))
				require.NoError(t, serr, "relative layout is preserved")
				assert.Equal(t, "package main\n", string(src))
			}
		})
	}
}

func TestGroupLauncher_EveryTaskStartsFromAnIsolatedCopy(t *testing.T) {
	f := newLaunchFixture(t)
	store := glStore(t, f.parentWS)
	_, err := f.launcher.Launch(context.Background(), f.requestWith(t, glBranchConfig(), store))
	require.NoError(t, err)
	reqs := f.starter.requests()
	require.Len(t, reqs, 2)

	// Variables: task A's nested write reaches neither task B nor the stored start snapshot.
	reqs[0].Variables["cfg"].(map[string]any)["depth"].(map[string]any)["leaf"] = "task-a"
	assert.Equal(t, "v", reqs[1].Variables["cfg"].(map[string]any)["depth"].(map[string]any)["leaf"])
	require.Len(t, f.groups.createdRaw, 1)
	assert.Equal(t, "v", f.groups.createdRaw[0].StartVariables["cfg"].(map[string]any)["depth"].(map[string]any)["leaf"])

	// A later write to the parent's live store reaches nothing that was captured.
	live, ok := store.Get("cfg")
	require.True(t, ok)
	live.(map[string]any)["depth"].(map[string]any)["leaf"] = "parent-later"
	assert.Equal(t, "v", reqs[1].Variables["cfg"].(map[string]any)["depth"].(map[string]any)["leaf"])
	assert.Equal(t, "v", f.groups.createdRaw[0].StartVariables["cfg"].(map[string]any)["depth"].(map[string]any)["leaf"])

	// Files: task A's writes reach neither task B nor the parent.
	dirA := f.workspaces.path(reqs[0].TaskInstanceID)
	dirB := f.workspaces.path(reqs[1].TaskInstanceID)
	require.NoError(t, os.WriteFile(filepath.Join(dirA, "new.txt"), []byte("a only"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dirA, "README.md"), []byte("changed by a"), 0o644))
	assert.NoFileExists(t, filepath.Join(dirB, "new.txt"))
	assert.NoFileExists(t, filepath.Join(f.parentWS, "new.txt"))
	b, err := os.ReadFile(filepath.Join(dirB, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "hello group", string(b))
	p, err := os.ReadFile(filepath.Join(f.parentWS, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "hello group", string(p))
}

func TestGroupLauncher_TypedInputsArePreserved(t *testing.T) {
	f := newLaunchFixture(t)
	_, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.NoError(t, err)
	a := f.starter.requests()[0].Variables

	assert.IsType(t, float64(0), a["n"], "a number stays a number")
	assert.IsType(t, map[string]any{}, a["cfg_copy"], "a map stays a map")
	assert.IsType(t, true, a["flag"], "a literal bool stays a bool")
}

func TestGroupLauncher_WorkflowKind_RequiredInputFromBaseAccepted(t *testing.T) {
	f := newLaunchFixture(t)
	run, err := f.launcher.Launch(context.Background(), f.request(t, glWorkflowsConfig()))
	require.NoError(t, err)
	assert.Equal(t, instance.GroupRunRunning, run.State)
	assert.Equal(t, instance.GroupTaskKindWorkflow, run.Tasks[0].Kind)
	assert.Equal(t, "child-wf", run.Tasks[0].Origin)
	reqs := f.starter.requests()
	assert.Equal(t, "child-wf", reqs[0].ChildWorkflow)
	assert.Empty(t, reqs[0].BranchEntry)
	assert.Equal(t, "acme/widgets", reqs[0].Variables["repo"], "the required input comes from the inherited base")

	assert.Equal(t, []string{"child-wf", "child-wf"}, f.contracts.names)
	for _, who := range f.contracts.asked {
		assert.Equal(t, f.owner, who.UserID, "the child contract is read under the OWNER's identity, never the ambient one")
	}
}

// ---------- error: pre-launch, nothing persisted ----------

func TestGroupLauncher_MissingTemplateReferenceIsPreLaunch(t *testing.T) {
	f := newLaunchFixture(t)
	cfg := glBranchConfig()
	cfg.Tasks[1].Inputs = map[string]any{"x": "{{nope.path}}"}

	run, err := f.launcher.Launch(context.Background(), f.request(t, cfg))
	require.Error(t, err)
	assert.Nil(t, run)
	assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.ErrorIs(t, err, variable.ErrUnresolvedReference)
	assert.Contains(t, err.Error(), `task "b"`)
	assert.Len(t, f.workspaces.ensuredIDs(), 2)
	f.assertNothingPersisted(t)
}

func TestGroupLauncher_SharedPathIsRejectedPreLaunch(t *testing.T) {
	t.Run("inherited work_dir inside the parent's area", func(t *testing.T) {
		f := newLaunchFixture(t)
		store := glStore(t, f.parentWS)
		require.NoError(t, store.SetInput("work_dir", filepath.Join(f.parentWS, "repo")))

		_, err := f.launcher.Launch(context.Background(), f.requestWith(t, glBranchConfig(), store))
		require.ErrorIs(t, err, engine.ErrGroupSharedPath, "task b inherits work_dir unchanged")
		assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
		assert.Contains(t, err.Error(), `"work_dir"`)
		f.assertNothingPersisted(t)
	})
	t.Run("explicit nested input inside the parent's area", func(t *testing.T) {
		f := newLaunchFixture(t)
		cfg := glBranchConfig()
		cfg.Tasks[0].Inputs["out"] = map[string]any{"dir": filepath.Join(f.parentWS, "x")}

		_, err := f.launcher.Launch(context.Background(), f.request(t, cfg))
		require.ErrorIs(t, err, engine.ErrGroupSharedPath)
		assert.Contains(t, err.Error(), `"out.dir"`)
		f.assertNothingPersisted(t)
	})
}

func TestGroupLauncher_WorkflowKind_ContractViolationsArePreLaunch(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(f *launchFixture, cfg *workflow.GroupConfig)
		wantIs  error
		wantMsg string
	}{
		{name: "missing required input", wantIs: engine.ErrChildContractUnsatisfied, wantMsg: "approver",
			mutate: func(f *launchFixture, _ *workflow.GroupConfig) {
				f.contracts.byName["child-wf"] = append(f.contracts.byName["child-wf"],
					workflow.InputField{Name: "approver", Type: "string", Required: true})
			}},
		{name: "declared type mismatch", wantIs: engine.ErrChildContractUnsatisfied, wantMsg: `"count"`,
			mutate: func(_ *launchFixture, cfg *workflow.GroupConfig) { cfg.Tasks[1].Inputs["count"] = "seven" }},
		{name: "invisible or absent child workflow", wantIs: workflow.ErrNotFound, wantMsg: "secret-wf",
			mutate: func(_ *launchFixture, cfg *workflow.GroupConfig) { cfg.Tasks[0].Workflow = "secret-wf" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newLaunchFixture(t)
			cfg := glWorkflowsConfig()
			c.mutate(f, &cfg)

			_, err := f.launcher.Launch(context.Background(), f.request(t, cfg))
			require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
			assert.ErrorIs(t, err, c.wantIs)
			assert.Contains(t, err.Error(), c.wantMsg)
			f.assertNothingPersisted(t)
		})
	}
}

func TestGroupLauncher_SnapshotFailureIsPreLaunch(t *testing.T) {
	f := newLaunchFixture(t)
	f.groupWS.snapErr = errors.New("disk full")

	_, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.Contains(t, err.Error(), "snapshot")
	assert.Contains(t, err.Error(), "disk full")
	assert.Empty(t, f.workspaces.ensuredIDs(), "no task directory is prepared once the snapshot fails")
	f.assertNothingPersisted(t)
}

func TestGroupLauncher_ForkFailureIsPreLaunch(t *testing.T) {
	f := newLaunchFixture(t)
	f.groupWS.forkErr = errors.New("permission denied")

	_, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.Contains(t, err.Error(), "fork")
	f.assertNothingPersisted(t)
}

func TestGroupLauncher_RosterPersistFailureIsPreLaunch(t *testing.T) {
	f := newLaunchFixture(t)
	f.groups.failOnce("CreateWithTasks", errors.New("disk I/O error"))

	_, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.Contains(t, err.Error(), "disk I/O error")
	f.assertNothingPersisted(t)
}

func TestGroupLauncher_IdentityResolutionFailureIsPreLaunchWithNoFallback(t *testing.T) {
	f := newLaunchFixture(t)
	f.identities.err = errors.New("user deleted")

	_, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.Contains(t, err.Error(), "identity")
	assert.Empty(t, f.groupWS.snapshotHandles())
	f.assertNothingPersisted(t)
}

// Provenance: the fork capability is a required input whenever the parent has
// a working area. Without it the launcher refuses rather than letting tasks
// share the parent's mutable directory.
func TestGroupLauncher_NilGroupWorkspaceWithAParentWorkspaceRefuses(t *testing.T) {
	for _, c := range []struct {
		name  string
		build func(f *launchFixture) *engine.GroupLauncher
	}{
		{"nil GroupWorkspace", func(f *launchFixture) *engine.GroupLauncher {
			return engine.NewGroupLauncher(f.groups, f.starter, f.contracts, f.workspaces, nil, f.identities, f.canceller, f.bus)
		}},
		{"nil WorkspaceManager", func(f *launchFixture) *engine.GroupLauncher {
			return engine.NewGroupLauncher(f.groups, f.starter, f.contracts, nil, f.groupWS, f.identities, f.canceller, f.bus)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newLaunchFixture(t)
			_, err := c.build(f).Launch(context.Background(), f.request(t, glBranchConfig()))
			require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
			assert.Contains(t, err.Error(), "fork")
			assert.Empty(t, f.workspaces.ensuredIDs())
			f.assertNothingPersisted(t)
		})
	}
}

func TestGroupLauncher_WorkflowFormWithoutAContractReaderRefuses(t *testing.T) {
	f := newLaunchFixture(t)
	l := engine.NewGroupLauncher(f.groups, f.starter, nil, f.workspaces, f.groupWS, f.identities, f.canceller, f.bus)

	_, err := l.Launch(context.Background(), f.request(t, glWorkflowsConfig()))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.Contains(t, err.Error(), "child-contract reader")
	f.assertNothingPersisted(t)
}

func TestGroupLauncher_IncompleteRequestIsPreLaunch(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(r *engine.GroupLaunchRequest)
	}{
		{"no parent", func(r *engine.GroupLaunchRequest) { r.Parent = nil }},
		{"no variable store", func(r *engine.GroupLaunchRequest) { r.Vars = nil }},
		{"no ownership", func(r *engine.GroupLaunchRequest) { r.Ownership = nil }},
		{"ownership of another group", func(r *engine.GroupLaunchRequest) { r.Ownership.NodeID = "other" }},
		{"no tasks", func(r *engine.GroupLaunchRequest) { r.Config.Tasks = nil }},
		{"entry outside the validated body", func(r *engine.GroupLaunchRequest) { r.Ownership.Bodies["b"] = []string{"elsewhere"} }},
		{"branches task naming a workflow", func(r *engine.GroupLaunchRequest) { r.Config.Tasks[1].Workflow = "child-wf" }},
		{"unknown form", func(r *engine.GroupLaunchRequest) { r.Config.Form = "matrix" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newLaunchFixture(t)
			req := f.request(t, glBranchConfig())
			c.mutate(&req)

			_, err := f.launcher.Launch(context.Background(), req)
			require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
			f.assertNothingPersisted(t)
		})
	}
}

// ---------- edge: no parent working area ----------

func TestGroupLauncher_NoParentWorkspaceRunsWithoutAFork(t *testing.T) {
	f := newLaunchFixture(t)
	l := engine.NewGroupLauncher(f.groups, f.starter, f.contracts, nil, nil, f.identities, f.canceller, f.bus)
	cfg := glBranchConfig()
	delete(cfg.Tasks[0].Inputs, "work_dir")

	run, err := l.Launch(context.Background(), f.requestWith(t, cfg, glStore(t, "")))
	require.NoError(t, err)
	assert.Equal(t, instance.GroupRunRunning, run.State)
	assert.Empty(t, run.WorkspaceSnapshot, "no parent area: no snapshot handle")
	assert.Len(t, f.starter.requests(), 2)

	cfg.Tasks[0].Inputs["work_dir"] = "{{workspace}}/repo"
	f2 := newLaunchFixture(t)
	l2 := engine.NewGroupLauncher(f2.groups, f2.starter, f2.contracts, nil, nil, f2.identities, f2.canceller, f2.bus)
	_, err = l2.Launch(context.Background(), f2.requestWith(t, cfg, glStore(t, "")))
	require.ErrorIs(t, err, variable.ErrUnresolvedReference, "without a task area {{workspace}} cannot resolve")
	f2.assertNothingPersisted(t)
}

func TestGroupLauncher_ResultsModeReleasesTheSnapshotOnceLaunched(t *testing.T) {
	f := newLaunchFixture(t)
	run, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.NoError(t, err)
	assert.Equal(t, []string{run.WorkspaceSnapshot}, f.groupWS.releasedHandles(),
		"a results-only group needs its baseline only to fork the tasks")
}

// ---------- failure: post-persist partial launch ----------

func TestGroupLauncher_PartialLaunchFailureDecidesFailureAndCancelsLaunchedTasks(t *testing.T) {
	f := newLaunchFixture(t)
	cfg := glBranchConfig()
	cfg.Tasks = append(cfg.Tasks, workflow.GroupTask{Key: "c", Entry: "c1"})
	f.starter.errByKey = map[string]error{"b": errors.New("queue full")}

	run, err := f.launcher.Launch(context.Background(), f.request(t, cfg))
	require.NoError(t, err, "a post-persist launch failure is DECIDED, not returned")
	require.NotNil(t, run)

	assert.Equal(t, instance.GroupRunDecided, run.State)
	require.NotNil(t, run.Decision)
	assert.Equal(t, "failure", run.Decision.Outcome)
	assert.Equal(t, `launch failed: task "b": queue full`, run.Decision.Reason)

	a, b, c := run.Tasks[0], run.Tasks[1], run.Tasks[2]
	assert.Equal(t, instance.GroupTaskLaunched, a.LaunchState)
	assert.True(t, a.CancelRequested)
	assert.Equal(t, instance.GroupTaskLaunchFailed, b.LaunchState)
	assert.Equal(t, `task "b": queue full`, b.LaunchError)
	assert.Equal(t, instance.GroupTaskPending, c.LaunchState, "a task after the failure is never started")

	frozen := run.Decision.Tasks
	assert.Equal(t, instance.GroupTaskStatusUnfinished, frozen[0].Status)
	assert.True(t, frozen[0].CancelRequested)
	assert.Equal(t, instance.GroupTaskStatusUnfinished, frozen[1].Status)
	assert.Empty(t, frozen[1].ChildInstanceID)
	assert.Equal(t, `not launched: task "b": queue full`, frozen[1].Failure["error"])
	assert.Equal(t, `not launched: launch failed: task "b": queue full`, frozen[2].Failure["error"])

	keys := []string{}
	for _, r := range f.starter.requests() {
		keys = append(keys, r.TaskKey)
	}
	assert.Equal(t, []string{"a", "b"}, keys)

	calls := f.canceller.callsFor(a.ChildInstanceID)
	require.Len(t, calls, 1, "the already-launched task is cancelled")
	assert.Equal(t, f.owner, calls[0].Identity.UserID, "under the OWNER's identity")
	assert.Equal(t, instance.StateCancelled, f.instances.get(a.ChildInstanceID).State)
	assert.Len(t, f.canceller.callsFor(b.ChildInstanceID), 1, "the failed task's reserved child is stopped if it exists")
	assert.NoDirExists(t, f.workspaces.path(c.ChildInstanceID), "the never-started task's prepared area is removed")
	assert.Len(t, f.bus.eventsOfType(f.parent.ID, engine.EventTypeGroupDecided), 1)
	assert.Empty(t, f.bus.eventsOfType(f.parent.ID, engine.EventTypeGroupLaunched))

	// Re-call (retry / re-entry): the existing run is returned and no child is
	// started twice.
	again, err := f.launcher.Launch(context.Background(), f.request(t, cfg))
	require.NoError(t, err)
	assert.Equal(t, run.ID, again.ID)
	assert.Len(t, f.starter.requests(), 2, "no duplicate children")
	assert.Equal(t, 1, f.groups.count())
}

func TestGroupLauncher_UnrecordedLaunchIsTreatedAsALaunchFailure(t *testing.T) {
	f := newLaunchFixture(t)
	f.groups.failOnce("MarkTaskLaunched", errors.New("database is locked"))

	run, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.NoError(t, err)
	assert.Equal(t, instance.GroupRunDecided, run.State)
	assert.Contains(t, run.Decision.Reason, "started but its launch could not be recorded")
	a := run.Tasks[0]
	assert.Equal(t, instance.StateCancelled, f.instances.get(a.ChildInstanceID).State,
		"the child that DID start is stopped even though its launch was never recorded")
	assert.Len(t, f.starter.requests(), 1)
}

func TestGroupLauncher_ExistingRunIsReturnedWithoutRelaunching(t *testing.T) {
	f := newLaunchFixture(t)
	first, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.NoError(t, err)

	second, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Len(t, f.starter.requests(), 2)
	for _, id := range f.workspaces.ensuredIDs()[2:] {
		assert.NoDirExists(t, f.workspaces.path(id), "the losing preparation is cleaned up")
	}
}

func TestNewGroupLauncher_PanicsOnAMissingRequiredCollaborator(t *testing.T) {
	f := newLaunchFixture(t)
	assert.Panics(t, func() {
		engine.NewGroupLauncher(nil, f.starter, f.contracts, f.workspaces, f.groupWS, f.identities, f.canceller, f.bus)
	})
	assert.Panics(t, func() {
		engine.NewGroupLauncher(f.groups, nil, f.contracts, f.workspaces, f.groupWS, f.identities, f.canceller, f.bus)
	})
	assert.Panics(t, func() {
		engine.NewGroupLauncher(f.groups, f.starter, f.contracts, f.workspaces, f.groupWS, nil, f.canceller, f.bus)
	})
	assert.Panics(t, func() {
		engine.NewGroupLauncher(f.groups, f.starter, f.contracts, f.workspaces, f.groupWS, f.identities, nil, f.bus)
	})
	assert.Panics(t, func() {
		engine.NewGroupLauncher(f.groups, f.starter, f.contracts, f.workspaces, f.groupWS, f.identities, f.canceller, nil)
	})
}

func TestGroupEventTypes_MatchTheContract(t *testing.T) {
	assert.Equal(t, "group.launched", engine.EventTypeGroupLaunched)
	assert.Equal(t, "group.task_updated", engine.EventTypeGroupTaskUpdated)
	assert.Equal(t, "group.decided", engine.EventTypeGroupDecided)
	assert.Equal(t, "group.gathering", engine.EventTypeGroupGathering)
	assert.Equal(t, "group.conflict", engine.EventTypeGroupConflict)
	assert.Equal(t, "group.resolved", engine.EventTypeGroupResolved)
	assert.Equal(t, "group.completed", engine.EventTypeGroupCompleted)
}
```

- [x] Step 8: Run it, confirm it fails.

```bash
go test ./domain/engine/ -run 'TestGroupLauncher|TestNewGroupLauncher|TestGroupEventTypes' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/group_launcher_test.go:27:22: undefined: engine.GroupLauncher
domain/engine/group_launcher_test.go:73:21: undefined: engine.NewGroupLauncher
domain/engine/group_launcher_test.go:137:63: undefined: engine.GroupLaunchRequest
...
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 9: Implement the events. Create `domain/engine/group_events.go`:

```go
package engine

import (
	"time"

	"github.com/arbitron/arbitron/domain/instance"
)

// Parallel-group lifecycle events. They are published on the PARENT instance
// id (the run whose graph contains the group node), so a viewer of the parent
// sees its groups move. Data always carries "groupRunId" and "nodeId"; task
// events add "taskKey" and "status".
const (
	EventTypeGroupLaunched    = "group.launched"
	EventTypeGroupTaskUpdated = "group.task_updated"
	EventTypeGroupDecided     = "group.decided"
	EventTypeGroupGathering   = "group.gathering" // Phase 3
	EventTypeGroupConflict    = "group.conflict"  // Phase 3
	EventTypeGroupResolved    = "group.resolved"  // Phase 3
	EventTypeGroupCompleted   = "group.completed"
)

// publishGroupEvent is the ONE place a group event is shaped. A nil bus or run
// publishes nothing.
func publishGroupEvent(bus EventBus, run *instance.GroupRun, eventType string, extra map[string]any, at time.Time) {
	if bus == nil || run == nil {
		return
	}
	data := map[string]any{"groupRunId": run.ID.String(), "nodeId": run.NodeID}
	for k, v := range extra {
		data[k] = v
	}
	bus.Publish(run.ParentInstanceID, Event{Type: eventType, Timestamp: at, Data: data})
}

// decisionEventData is the payload of group.decided.
func decisionEventData(d *instance.GroupDecision) map[string]any {
	if d == nil {
		return map[string]any{}
	}
	return map[string]any{"outcome": d.Outcome, "winner": d.WinnerKey, "reason": d.Reason}
}
```

- [x] Step 10: Implement the launcher. Create `domain/engine/group_launcher.go`:

```go
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
	identities IdentityResolver
	canceller  RunCanceller
	bus        EventBus
	now        func() time.Time
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
	snapshot string
	dirs     []uuid.UUID
}

func (p *launchPrep) cleanup(ctx context.Context) {
	for _, id := range p.dirs {
		if err := p.l.workspaces.RemoveInstanceDir(id); err != nil {
			slog.Warn("parallel_group: removing a prepared task area failed", "taskInstanceID", id, "error", err)
		}
	}
	if p.snapshot != "" {
		if err := p.l.groupWS.ReleaseSnapshot(ctx, p.snapshot); err != nil {
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
//	owner identity -> start variables captured ONCE -> parent area snapshot ->
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
	if parentWS != "" && (l.workspaces == nil || l.groupWS == nil) {
		return nil, preLaunchError(errors.New("the parent run has a working area but no workspace snapshot/fork " +
			"capability is wired; refusing to start tasks that would share the parent's files"))
	}

	runID := uuid.New()
	prep := &launchPrep{l: l}
	if parentWS != "" {
		handle, err := l.groupWS.SnapshotParent(ctx, runID, parentWS)
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
			if err := l.groupWS.ForkTask(ctx, prep.snapshot, lt.dir); err != nil {
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
	case req.Parent == nil:
		return errors.New("no parent instance was supplied")
	case req.Vars == nil:
		return errors.New("no variable store was supplied (the parent's live store at the group node is required)")
	case req.Ownership == nil:
		return errors.New("no validated group ownership (workflow.AnalyzeGroups) was supplied")
	case req.Ownership.NodeID != req.Node.ID:
		return fmt.Errorf("the supplied ownership describes group %q, not %q", req.Ownership.NodeID, req.Node.ID)
	case len(req.Config.Tasks) == 0:
		return workflow.ErrGroupNoTasks
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
		d, err := l.workspaces.EnsureInstanceDir(row.ChildInstanceID)
		if err != nil {
			return launchTask{}, fmt.Errorf("task %q: prepare its working area: %w", spec.Key, err)
		}
		prep.dirs = append(prep.dirs, row.ChildInstanceID)
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
		return nil, fmt.Errorf("parallel_group %q: %w, and reading it back failed: %w", nodeID, cause, err)
	}
	if existing == nil {
		return nil, fmt.Errorf("parallel_group %q: %w, but it could not be read back", nodeID, cause)
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

// failLaunch turns a partial launch into a durable failure decision.
func (l *GroupLauncher) failLaunch(ctx, ownerCtx context.Context, run *instance.GroupRun, tasks []launchTask, failed int, cause error) *instance.GroupRun {
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
```

- [x] Step 11: Run the new tests, then the whole package, gofmt and vet (with the race detector: the fakes are shared across goroutines in P2-T10/T11).

> Verification concern: focused tests pass with and without `-race`; the full engine package passes without `-race`; gofmt and vet are clean. The full-package race run detects existing races in `engine_spawn_stamp_test.go:65,82` against `mockInstanceRepo` updates. Reproduced on unchanged base `b6c89b2` using `go test ./domain/engine/ -race -run 'TestTrigger_NoSpawnLinkCreatesRoot|TestTrigger_PartialSpawnLinkIsNotStamped' -count=10`. These unrelated tests are left unchanged.

> Self-review (in order): (1) Spec compliance: covered incomplete ids, reserved inputs, partial directory creation cleanup, duplicate-run read errors, and retained files-mode baselines; regression tests were red before the fixes. (2) Anti-green-theater: added both-form tests through real `Engine.StartGroupTask`, `Engine.ChildInputs`, ownership analysis, and filesystem adapters, asserting linked child variables at queue intake and copied file bytes at task-directory sinks under a reconstructed owner identity. P2-T14 remains the assigned production composition-root task. (3) Code quality: reviewed neighboring engine/contract/workspace code, preserved existing ports and patterns, formatted all six new files, and ran vet.

```bash
go test ./domain/engine/ -run 'TestGroupLauncher|TestNewGroupLauncher|TestGroupEventTypes|TestComposeTaskVariables' -count=1 -v
go test ./domain/engine/ -race -count=1
gofmt -l domain/engine/group_events.go domain/engine/group_launcher.go domain/engine/group_task_vars.go domain/engine/group_fakes_test.go domain/engine/group_launcher_test.go domain/engine/group_task_vars_test.go
go vet ./domain/engine/
```
Expected:
```
--- PASS: TestGroupLauncher_PersistsTheRosterBeforeLaunchingAndOpensTheBarrierLast (0.00s)
--- PASS: TestGroupLauncher_StartVariablesAndFilesAreIdenticalInBothForms (0.01s)
...
--- PASS: TestGroupEventTypes_MatchTheContract (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
```
(`gofmt -l` and `go vet` print nothing.)

- [x] Step 12: Commit.

```bash
git add domain/engine/group_events.go domain/engine/group_launcher.go domain/engine/group_fakes_test.go domain/engine/group_launcher_test.go
git commit -m "feat(engine): GroupLauncher persists the roster before enqueue and launches isolated tasks once" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
