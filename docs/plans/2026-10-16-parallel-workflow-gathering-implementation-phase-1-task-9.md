# Parallel Workflow Gathering — Phase 1 Task 9: Trigger guard (group execution support level) + dashboard tolerance

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Refuse to start any workflow containing a `parallel_group` node that this server cannot execute — before an instance row is created or anything is enqueued — with an actionable error naming the node, while the dashboard tolerates the new node type everywhere it renders graphs.
**Architecture:** A new `domain/engine/group_support.go` defines the engine's `GroupSupport` level (`None` default ⇒ refuse every group; `Results` ⇒ refuse only `results_and_files` groups; `ResultsAndFiles` ⇒ allow all) and the pure `checkGroupSupport`. `Engine.TriggerWithDepartment` calls it immediately after the visibility-gated load; every instance-creating path (REST/MCP trigger, `Rerun`, `Spawn`, schedules) funnels through that one function (`engine.go:570` is the only `instances.Create`). REST maps the sentinel to `422`. The dashboard gains a `NodeType` union including `'parallel_group'`, an accent colour for it, and a vitest proving a snapshot with group nodes renders through the default node component.
**Tech Stack:** Go 1.25.5, testify; React 19 + TypeScript 6 + vitest 4 + @testing-library/react (dashboard).
**Spec / Design:** `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-1.md` §4 "Staged execution guard" ("Phase 1 refuses a group-bearing snapshot before instance creation/enqueue through `Engine.TriggerWithDepartment` and any new snapshot trigger path. Phase 2 enables `results` and continues to reject `results_and_files` until Phase 3 is wired. Never run a files-requesting group silently as results only. Dashboard type support must at least tolerate the new type") and Data Provenance row "Trigger capability check" (**PLANNED DEPENDENCY P1-C**, resolved here).

**Interfaces**
- Consumes:
  - `domain/engine/engine.go:377` `func (e *Engine) TriggerWithDepartment(ctx, workflowName string, variables map[string]any, priority, departmentOverride string) (uuid.UUID, bool, error)`; its visibility-gated load at `engine.go:397-401` (`workflow.LoadVisibleFiltered(ctx, e.workflows, owner, workflowName, filter)`); `engine.go:23-64` `Engine` struct and its `mu sync.Mutex`.
  - `domain/engine/engine.go:1683` `Rerun` (re-triggers via `e.Trigger` → `TriggerWithDepartment`, engine.go:1722); `domain/engine/engine_spawn.go:52` `Spawn` (→ `TriggerWithDepartment`, engine_spawn.go:92); `domain/schedule/runner.go:68`, `evaluator.go:231` (→ `TriggerWithDepartment`).
  - P1-T1: `workflow.NodeTypeParallelGroup`, `workflow.NodeConfig.Group *GroupConfig`, `GroupConfig.GatherMode`, `workflow.GroupGatherResults`, `workflow.GroupGatherResultsAndFiles`.
  - P1-T8 test helpers (package `main`, `cmd/arbitron/group_authoring_e2e_test.go`): `bootGroupAuthoringStack`, `newGroupMCPServer`, `callMCPTool`, `mcpText`, `filesModeGroup`; existing `cmd/arbitron/spawn_e2e_test.go:87` `adminCtxForLocalUser`, `:104` `childrenOf`; `cmd/arbitron/spawn_wait_e2e_test.go:131` `waitForState`.
  - Engine test fixtures: `domain/engine/engine_test.go:794` `newTestFixtures`, `:845` `waitForInstance`, `:88` `(*mockWorkflowRepo).add`, `mockInstanceRepo.instances`/`mu` (engine_test.go:95-97); `domain/engine/engine_list_test.go:94` `bgAdmin`.
  - Dashboard: `dashboard/src/graph/nodes/ArbitronNode.tsx:8-17` `typeColors`; `dashboard/src/graph/useGraphLayout.ts:108-117` (every workflow node becomes `type: 'arbitronNode'`); `dashboard/src/graph/nodes/nodeTypes.ts` (`arbitronNode`, `loopContainer`); `dashboard/src/routes/ExecutionView.tsx:70` `GraphView` (fed `inst.workflowSnapshot` at ExecutionView.tsx:225-226).
- Produces (package `engine`, `domain/engine/group_support.go`):
  - `type GroupSupport int`; `const ( GroupSupportNone GroupSupport = iota; GroupSupportResults; GroupSupportResultsAndFiles )`; `func (l GroupSupport) String() string` ("none" / "results" / "results_and_files").
  - `var ErrGroupExecutionUnsupported = errors.New("parallel_group execution is not available on this server")`
  - `func (e *Engine) SetGroupSupport(level GroupSupport)` (field `e.groupSupport`, guarded by `e.mu`; zero value = None)
  - `func (e *Engine) GroupSupportLevel() GroupSupport`
  - `func checkGroupSupport(wf *workflow.Workflow, level GroupSupport) error` — nil when allowed; otherwise wraps `ErrGroupExecutionUnsupported` with workflow name, the first offending node id (in `wf.Nodes` order) and its gather mode, plus a corrective instruction. An unknown level fails closed (treated as None).
  - REST: `POST /workflows/{name}/trigger` returns `422 UNSUPPORTED_NODE_TYPE` with the actionable message for this sentinel.
  - Dashboard: `export type NodeType` in `dashboard/src/api/types.ts` (all 12 backend node types incl. `'parallel_group'`); `parallel_group` accent `#3498db` in `ArbitronNode`.

**Wiring & data provenance**
- Composition root: none in Phase 1 — the default (zero) level `GroupSupportNone` IS the Phase 1 configuration, so `cmd/arbitron/main.go` gains no line. **P2-T14** adds `eng.SetGroupSupport(engine.GroupSupportResults)` in `initDomain` once the results-only executor is wired, and **P3-T11** later replaces it with `eng.SetGroupSupport(engine.GroupSupportResultsAndFiles)`. P2-T7's internal `triggerSpec` path (`Engine.StartGroupTask`) reuses this guard; any future snapshot-based creation path must call `checkGroupSupport(wf, e.GroupSupportLevel())` before `instances.Create`.
- Producers: `wf` ⇐ `workflow.LoadVisibleFiltered` inside `TriggerWithDepartment` (engine.go:397) over `adapter/filesystem.WorkflowRepo` (main.go:465); `level` ⇐ `e.groupSupport` set only by `SetGroupSupport` (default zero). `Retry` (engine.go:1620) resumes an EXISTING instance from its stored snapshot and creates nothing; in Phase 1 no group-bearing instance can exist because creation is refused here, so `Retry` needs no guard.
- Provenance probes: `TestGroupTriggerE2E_Phase1DefaultRefusesEveryGroupWorkflow` (through `initDomain`) fails if the guard call in `TriggerWithDepartment` is removed (an instance row would appear); `TestEngine_GroupSupport_DefaultLevelIsNone` fails if a constructor default silently enables groups.
- **P2-T14 MUST UPDATE** `cmd/arbitron/group_trigger_e2e_test.go:TestGroupTriggerE2E_Phase1DefaultRefusesEveryGroupWorkflow` when it wires `GroupSupportResults`: the `results-group` and `spawned-results-group` subtests then expect an instance (and a child) to be created; the `files-group` subtest keeps expecting `ErrGroupExecutionUnsupported` with the file-gathering message until P3-T11 (which updates it again).

### Task 9: Group execution support level, trigger guard, dashboard tolerance

**Files:**
- Create: `domain/engine/group_support.go`
- Modify: `domain/engine/engine.go:23-64` (`Engine` struct: field `groupSupport`)
- Modify: `domain/engine/engine.go:397-401` (`TriggerWithDepartment`: guard right after the visibility-gated load)
- Modify: `adapter/http/workflow_handler.go` `Trigger` (the error branch after `ErrInvalidDepartmentOverride`, originally lines 258-261; P1-T7 shifted it down by the helpers it inserted after `Validate`)
- Modify: `dashboard/src/api/types.ts:15` (add `NodeType` after `TriggerSource`)
- Modify: `dashboard/src/graph/nodes/ArbitronNode.tsx:1-17` (import `NodeType`; `parallel_group` accent; `satisfies` guard)
- Test: `domain/engine/group_support_internal_test.go` (package `engine`)
- Test: `domain/engine/engine_group_support_test.go` (package `engine_test`)
- Test: `adapter/http/workflow_handler_group_trigger_test.go` (package `http_test`; reuses `errTriggerService`, `newTriggerReq` from `adapter/http/workflow_handler_trigger_dept_test.go:19,25`)
- Test: `cmd/arbitron/group_trigger_e2e_test.go` (package `main`)
- Test: `dashboard/src/graph/parallelGroupTolerance.test.tsx`

- [x] Step 1: Write the failing pure guard tests. Create `domain/engine/group_support_internal_test.go`:

```go
package engine

import (
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gsGroupWorkflow(name string, modes ...string) *workflow.Workflow {
	wf := &workflow.Workflow{Name: name, Nodes: []workflow.Node{{ID: "start", Type: workflow.NodeTypeTransform, Entrypoint: true}}}
	for i, m := range modes {
		id := []string{"g1", "g2", "g3"}[i]
		var cfg *workflow.GroupConfig
		if m != "<nil>" {
			cfg = &workflow.GroupConfig{Form: workflow.GroupFormBranches, GatherMode: m}
		}
		wf.Nodes = append(wf.Nodes, workflow.Node{ID: id, Type: workflow.NodeTypeParallelGroup, Config: workflow.NodeConfig{Group: cfg}})
	}
	return wf
}

func TestCheckGroupSupport_Matrix(t *testing.T) {
	results := gsGroupWorkflow("r", workflow.GroupGatherResults)
	files := gsGroupWorkflow("f", workflow.GroupGatherResultsAndFiles)
	unset := gsGroupWorkflow("u", "<nil>")
	plain := &workflow.Workflow{Name: "p", Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript, Entrypoint: true}}}

	cases := []struct {
		name  string
		wf    *workflow.Workflow
		level GroupSupport
		ok    bool
	}{
		{"plain/none", plain, GroupSupportNone, true},
		{"plain/results", plain, GroupSupportResults, true},
		{"plain/files", plain, GroupSupportResultsAndFiles, true},
		{"results/none", results, GroupSupportNone, false},
		{"results/results", results, GroupSupportResults, true},
		{"results/files", results, GroupSupportResultsAndFiles, true},
		{"files/none", files, GroupSupportNone, false},
		{"files/results", files, GroupSupportResults, false},
		{"files/files", files, GroupSupportResultsAndFiles, true},
		{"unset-config/none", unset, GroupSupportNone, false},
		{"unset-config/results", unset, GroupSupportResults, false},
		{"unset-config/files", unset, GroupSupportResultsAndFiles, true},
		{"results/unknown-level", results, GroupSupport(-1), false},
		{"nil workflow", nil, GroupSupportNone, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkGroupSupport(tc.wf, tc.level)
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrGroupExecutionUnsupported))
		})
	}
}

func TestCheckGroupSupport_NoneMessageIsActionable(t *testing.T) {
	err := checkGroupSupport(gsGroupWorkflow("review", workflow.GroupGatherResults), GroupSupportNone)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "parallel_group execution is not available on this server")
	assert.Contains(t, msg, `workflow "review"`)
	assert.Contains(t, msg, `node "g1"`)
	assert.Contains(t, msg, `gatherMode "results"`)
	assert.Contains(t, msg, "this server cannot execute parallel_group yet")
	assert.Contains(t, msg, "workflow_validate", "tells the author what still works")
}

func TestCheckGroupSupport_ResultsLevelNamesTheFilesNode(t *testing.T) {
	// A results group first, then a files group: the error must name the
	// files group (g2), never silently run it as results-only.
	err := checkGroupSupport(gsGroupWorkflow("mixed", workflow.GroupGatherResults, workflow.GroupGatherResultsAndFiles), GroupSupportResults)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `node "g2"`)
	assert.NotContains(t, err.Error(), `node "g1"`)
	assert.Contains(t, err.Error(), "file gathering is not available yet")
	assert.Contains(t, err.Error(), `set gatherMode to "results"`)
}

func TestCheckGroupSupport_ResultsLevelRejectsUnsetGatherMode(t *testing.T) {
	err := checkGroupSupport(gsGroupWorkflow("broken", "<nil>"), GroupSupportResults)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gatherMode <unset>")
	assert.Contains(t, err.Error(), "workflow_validate")
}

func TestGroupSupport_String(t *testing.T) {
	assert.Equal(t, "none", GroupSupportNone.String())
	assert.Equal(t, "results", GroupSupportResults.String())
	assert.Equal(t, "results_and_files", GroupSupportResultsAndFiles.String())
	assert.Equal(t, "GroupSupport(7)", GroupSupport(7).String())
}
```

- [x] Step 2: Write the failing engine-level tests. Create `domain/engine/engine_group_support_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gsWorkflow is an entrypoint group (mode = gatherMode) continuing to a script node.
func gsWorkflow(name, mode string) *workflow.Workflow {
	return &workflow.Workflow{
		Name: name,
		Nodes: []workflow.Node{
			{ID: "fanout", Type: workflow.NodeTypeParallelGroup, Entrypoint: true, Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
				Form:               workflow.GroupFormBranches,
				Tasks:              []workflow.GroupTask{{Key: "a", Entry: "a1"}},
				CompletionRule:     workflow.GroupRuleAllSucceed,
				UnfinishedBehavior: workflow.GroupUnfinishedCancel,
				GatherMode:         mode,
				ResolverProfile:    "default-agent",
			}}},
			{ID: "a1", Type: workflow.NodeTypeScript},
			{ID: "done", Type: workflow.NodeTypeScript},
			{ID: "failed", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "fanout", Target: "a1", Output: workflow.GroupPortTask, Label: "a"},
			{Source: "fanout", Target: "done", Output: workflow.GroupPortSuccess},
			{Source: "fanout", Target: "failed", Output: workflow.GroupPortFailure},
		},
	}
}

func gsInstanceCount(f *testFixtures) int {
	f.instanceRepo.mu.Lock()
	defer f.instanceRepo.mu.Unlock()
	return len(f.instanceRepo.instances)
}

func TestEngine_GroupSupport_DefaultLevelIsNone(t *testing.T) {
	f := newTestFixtures()
	assert.Equal(t, engine.GroupSupportNone, f.engine.GroupSupportLevel())
	f.engine.SetGroupSupport(engine.GroupSupportResults)
	assert.Equal(t, engine.GroupSupportResults, f.engine.GroupSupportLevel())
}

func TestEngine_Trigger_GroupWorkflowRefusedByDefault(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(gsWorkflow("group-wf", workflow.GroupGatherResults))

	id, started, err := f.engine.Trigger(context.Background(), "group-wf", nil, "normal")
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrGroupExecutionUnsupported))
	assert.Contains(t, err.Error(), `node "fanout"`)
	assert.Contains(t, err.Error(), "this server cannot execute parallel_group yet")
	assert.Equal(t, uuid.Nil, id)
	assert.False(t, started)
	assert.Equal(t, 0, gsInstanceCount(f), "no instance row may be created")
}

func TestEngine_Trigger_GuardRunsBeforeInputValidation(t *testing.T) {
	f := newTestFixtures()
	wf := gsWorkflow("group-with-inputs", workflow.GroupGatherResults)
	wf.Inputs = []workflow.InputField{{Name: "topic", Type: "string", Required: true}}
	f.workflowRepo.add(wf)

	_, _, err := f.engine.Trigger(context.Background(), "group-with-inputs", nil, "normal")
	assert.True(t, errors.Is(err, engine.ErrGroupExecutionUnsupported),
		"the capability refusal wins over 'missing required input' so the author fixes the real blocker")
}

func TestEngine_Trigger_ResultsLevelAllowsResultsGroups(t *testing.T) {
	f := newTestFixtures()
	f.engine.RegisterExecutor(workflow.NodeTypeParallelGroup, f.executor)
	f.engine.SetGroupSupport(engine.GroupSupportResults)
	f.workflowRepo.add(gsWorkflow("group-wf", workflow.GroupGatherResults))

	id, _, err := f.engine.Trigger(context.Background(), "group-wf", nil, "normal")
	require.NoError(t, err)
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted, instance.StateFailed)
	assert.Equal(t, "group-wf", inst.WorkflowName)
}

func TestEngine_Trigger_ResultsLevelRefusesFileGathering(t *testing.T) {
	f := newTestFixtures()
	f.engine.SetGroupSupport(engine.GroupSupportResults)
	f.workflowRepo.add(gsWorkflow("files-wf", workflow.GroupGatherResultsAndFiles))

	_, _, err := f.engine.Trigger(context.Background(), "files-wf", nil, "normal")
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrGroupExecutionUnsupported))
	assert.Contains(t, err.Error(), "file gathering is not available yet")
	assert.Equal(t, 0, gsInstanceCount(f), "a files-requesting group is never run as results-only")
}

func TestEngine_Trigger_ResultsAndFilesAllowsEveryGroup(t *testing.T) {
	f := newTestFixtures()
	f.engine.RegisterExecutor(workflow.NodeTypeParallelGroup, f.executor)
	f.engine.SetGroupSupport(engine.GroupSupportResultsAndFiles)
	f.workflowRepo.add(gsWorkflow("files-wf", workflow.GroupGatherResultsAndFiles))

	id, _, err := f.engine.Trigger(context.Background(), "files-wf", nil, "normal")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, id)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted, instance.StateFailed)
}

func TestEngine_Trigger_NonGroupWorkflowUnaffected(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
}

func TestEngine_Rerun_GroupWorkflowRefused(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(gsWorkflow("group-wf", workflow.GroupGatherResults))
	src := &instance.Instance{
		ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "group-wf",
		State: instance.StateCompleted, Priority: instance.Priority("normal"),
		Variables: map[string]any{}, OriginalVariables: map[string]any{},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, f.instanceRepo.Create(context.Background(), src))

	newID, err := f.engine.Rerun(bgAdmin(), src.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrGroupExecutionUnsupported))
	assert.Equal(t, uuid.Nil, newID)
	assert.Equal(t, 1, gsInstanceCount(f), "rerun creates nothing")
}

func TestEngine_Spawn_GroupChildRefused(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(gsWorkflow("group-child", workflow.GroupGatherResults))
	parent := &instance.Instance{
		ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "parent",
		State: instance.StateRunning, Priority: instance.Priority("normal"),
		Variables: map[string]any{}, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, f.instanceRepo.Create(context.Background(), parent))

	_, err := f.engine.Spawn(bgAdmin(), engine.SpawnRequest{
		ChildWorkflow: "group-child",
		Priority:      "normal",
		Link:          instance.SpawnLink{ParentInstanceID: parent.ID, ParentNodeID: "spawn-1", Mode: instance.SpawnModeFireAndForget},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrGroupExecutionUnsupported))
	assert.Equal(t, 1, gsInstanceCount(f), "no child row")
}
```

- [x] Step 3: Run them, confirm they fail.

```bash
go test ./domain/engine/ -run 'TestCheckGroupSupport|TestGroupSupport_String|TestEngine_GroupSupport|TestEngine_Trigger_Group|TestEngine_Trigger_Guard|TestEngine_Trigger_Results|TestEngine_Trigger_NonGroup|TestEngine_Rerun_Group|TestEngine_Spawn_Group' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/domain/engine [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/group_support_internal_test.go:...: undefined: GroupSupport
domain/engine/group_support_internal_test.go:...: undefined: checkGroupSupport
domain/engine/group_support_internal_test.go:...: undefined: ErrGroupExecutionUnsupported
...
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 4: Implement the guard. Create `domain/engine/group_support.go`:

```go
package engine

import (
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/workflow"
)

// GroupSupport is the level of parallel_group execution this engine has been
// wired for. It is raised by the composition root only once the matching
// executor is registered: Phase 2 (P2-T14) sets GroupSupportResults, Phase 3
// (P3-T11) GroupSupportResultsAndFiles. The zero value refuses every
// group-bearing workflow.
type GroupSupport int

const (
	// GroupSupportNone refuses every workflow containing a parallel_group node
	// (Phase 1 default: authoring and validation only).
	GroupSupportNone GroupSupport = iota
	// GroupSupportResults executes gatherMode "results" groups and refuses
	// "results_and_files" groups (Phase 2).
	GroupSupportResults
	// GroupSupportResultsAndFiles executes every group (Phase 3).
	GroupSupportResultsAndFiles
)

// String renders the level with the gatherMode vocabulary.
func (l GroupSupport) String() string {
	switch l {
	case GroupSupportNone:
		return "none"
	case GroupSupportResults:
		return "results"
	case GroupSupportResultsAndFiles:
		return "results_and_files"
	}
	return fmt.Sprintf("GroupSupport(%d)", int(l))
}

// ErrGroupExecutionUnsupported is returned (wrapped, with the node and a fix)
// when a trigger asks this server to run a parallel_group it cannot execute.
// No instance is created.
var ErrGroupExecutionUnsupported = errors.New("parallel_group execution is not available on this server")

// SetGroupSupport sets the parallel_group execution level. Call it only from
// the composition root, after registering the executor that level requires.
func (e *Engine) SetGroupSupport(level GroupSupport) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.groupSupport = level
}

// GroupSupportLevel reports the configured parallel_group execution level.
func (e *Engine) GroupSupportLevel() GroupSupport {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.groupSupport
}

// checkGroupSupport returns nil when every parallel_group node in wf can run at
// level, else an error wrapping ErrGroupExecutionUnsupported for the FIRST
// offending node in definition order. It never downgrades a files-requesting
// group to results-only. An unknown level fails closed (treated as None).
func checkGroupSupport(wf *workflow.Workflow, level GroupSupport) error {
	if wf == nil || level == GroupSupportResultsAndFiles {
		return nil
	}
	for _, n := range wf.Nodes {
		if n.Type != workflow.NodeTypeParallelGroup {
			continue
		}
		mode := ""
		if n.Config.Group != nil {
			mode = n.Config.Group.GatherMode
		}
		if level == GroupSupportResults && mode == workflow.GroupGatherResults {
			continue
		}
		return groupUnsupportedError(wf.Name, n.ID, mode, level)
	}
	return nil
}

func groupUnsupportedError(workflowName, nodeID, mode string, level GroupSupport) error {
	shown := fmt.Sprintf("%q", mode)
	if mode == "" {
		shown = "<unset>"
	}
	switch {
	case level == GroupSupportResults && mode == workflow.GroupGatherResultsAndFiles:
		return fmt.Errorf("%w: workflow %q node %q uses gatherMode %s, but this server can only execute gatherMode %q; "+
			"file gathering is not available yet — set gatherMode to %q with workflow_group_upsert, or wait until file gathering is enabled",
			ErrGroupExecutionUnsupported, workflowName, nodeID, shown, workflow.GroupGatherResults, workflow.GroupGatherResults)
	case level == GroupSupportResults:
		return fmt.Errorf("%w: workflow %q node %q has gatherMode %s, which this server cannot execute; "+
			"run workflow_validate and fix the group with workflow_group_upsert",
			ErrGroupExecutionUnsupported, workflowName, nodeID, shown)
	default:
		return fmt.Errorf("%w: workflow %q node %q (gatherMode %s): this server cannot execute parallel_group yet. "+
			"The definition is saved and can still be authored and checked (workflow_group_get, workflow_validate), "+
			"but it cannot be triggered until parallel group execution is enabled on this server",
			ErrGroupExecutionUnsupported, workflowName, nodeID, shown)
	}
}
```

In `domain/engine/engine.go`, in the `Engine` struct (lines 23-64), directly before `executorsFrozen bool` (line 60), add:

```go
	// groupSupport is the parallel_group execution level (see group_support.go).
	// Zero value GroupSupportNone refuses every group-bearing trigger; guarded
	// by mu and raised only by the composition root via SetGroupSupport.
	groupSupport GroupSupport
```

In `TriggerWithDepartment`, directly after the load (lines 397-401):

```go
	wf, scope, err := workflow.LoadVisibleFiltered(ctx, e.workflows, owner, workflowName, filter)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to load workflow: %w", err)
	}
```
insert:

```go

	// Refuse a parallel_group this server cannot execute (P1-C) BEFORE anything
	// is built, stamped, created or enqueued. It runs AFTER the visibility-gated
	// load so an invisible workflow still reports not-found. Every
	// instance-creating path — REST/MCP trigger, Rerun, Spawn, schedules —
	// funnels through this function, so this one check covers them all.
	if err := checkGroupSupport(wf, e.GroupSupportLevel()); err != nil {
		return uuid.Nil, false, err
	}
```

- [x] Step 5: Run the engine tests and the whole package.

```bash
go test ./domain/engine/ -run 'TestCheckGroupSupport|TestGroupSupport_String|TestEngine_GroupSupport|TestEngine_Trigger_Group|TestEngine_Trigger_Guard|TestEngine_Trigger_Results|TestEngine_Trigger_NonGroup|TestEngine_Rerun_Group|TestEngine_Spawn_Group' -count=1 -v
go test ./domain/engine/... -count=1
```
Expected: every listed test `--- PASS`, then `ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs`.

- [x] Step 6: Write the failing REST mapping test. Create `adapter/http/workflow_handler_group_trigger_test.go`:

```go
package http_test

import (
	"fmt"
	"net/http"
	"testing"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
)

func TestTrigger_GroupExecutionUnsupported_422(t *testing.T) {
	svc := &errTriggerService{err: fmt.Errorf("%w: workflow %q node %q (gatherMode %q): this server cannot execute parallel_group yet",
		engine.ErrGroupExecutionUnsupported, "wf", "fanout", "results")}
	h := adapthttp.NewWorkflowHandler(newMockWorkflowRepo(), svc, newMockFileReader(), nil)
	r, w := newTriggerReq(t, "wf", `{}`)
	h.Trigger(w, r)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "UNSUPPORTED_NODE_TYPE")
	assert.Contains(t, w.Body.String(), "this server cannot execute parallel_group yet", "the actionable message reaches the caller")
}
```

Run: `go test ./adapter/http/ -run TestTrigger_GroupExecutionUnsupported_422 -count=1` — expected `--- FAIL ... expected: 422 actual: 500`.

Implement: in `adapter/http/workflow_handler.go` `Trigger`, directly after the branch
```go
		if errors.Is(err, engine.ErrInvalidDepartmentOverride) {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
```
add:
```go
		if errors.Is(err, engine.ErrGroupExecutionUnsupported) {
			// A capability refusal, not a server fault: the message names the
			// parallel_group node and what the author can do.
			WriteError(w, http.StatusUnprocessableEntity, "UNSUPPORTED_NODE_TYPE", err.Error())
			return
		}
```
Re-run: `go test ./adapter/http/ -count=1` — expected `ok`.

- [x] Step 7: Write the composition-root e2e test (it passes immediately because the guard from Step 4 is live through `initDomain`; Step 8 proves it bites). Create `cmd/arbitron/group_trigger_e2e_test.go`:

```go
package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// instancesNamed reads instance rows for a workflow straight from storage —
// the SINK. Unconstrained is the explicit admin-read assertion the repository
// requires before admitting rows.
func instancesNamed(t *testing.T, ds *domainStack, name string) []instance.InstanceSummary {
	t.Helper()
	all, err := ds.instanceRepo.List(context.Background(), instance.ListFilters{Unconstrained: true, Limit: 500})
	require.NoError(t, err)
	var out []instance.InstanceSummary
	for _, s := range all {
		if s.WorkflowName == name {
			out = append(out, s)
		}
	}
	return out
}

// PHASE 1 EXPECTATION — P2-T14 MUST UPDATE THIS TEST when it wires
// eng.SetGroupSupport(engine.GroupSupportResults): "results-group" and
// "spawned-results-group" then create instances; "files-group" keeps being
// refused (with the file-gathering message) until P3-T11 wires
// GroupSupportResultsAndFiles and updates it again.
//
// PROVENANCE PROBE: remove the checkGroupSupport call from
// Engine.TriggerWithDepartment and every subtest fails — an instance row is
// created for a workflow no executor can run.
func TestGroupTriggerE2E_Phase1DefaultRefusesEveryGroupWorkflow(t *testing.T) {
	ds, _ := bootGroupAuthoringStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.Equal(t, engine.GroupSupportNone, ds.eng.GroupSupportLevel(),
		"the Phase 1 composition root wires no parallel_group executor")

	// Author both modes through the real service (validated with the wired
	// checkers from P1-T8): authoring works, triggering is refused.
	results := filesModeGroup("results-group", "")
	results.Nodes[0].Config.Group.GatherMode = workflow.GroupGatherResults
	require.NoError(t, ds.workflowSvc.Create(ctx, "results-group", results))
	require.NoError(t, ds.workflowSvc.Create(ctx, "files-group", filesModeGroup("files-group", "default-agent")))

	for _, tc := range []struct{ name, mode string }{
		{"results-group", "results"},
		{"files-group", "results_and_files"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, started, err := ds.eng.Trigger(ctx, tc.name, map[string]any{}, "normal")
			require.Error(t, err)
			assert.True(t, errors.Is(err, engine.ErrGroupExecutionUnsupported), "got %v", err)
			assert.Contains(t, err.Error(), `node "fanout"`)
			assert.Contains(t, err.Error(), `gatherMode "`+tc.mode+`"`)
			assert.Contains(t, err.Error(), "this server cannot execute parallel_group yet")
			assert.Equal(t, uuid.Nil, id)
			assert.False(t, started)
			assert.Empty(t, instancesNamed(t, ds, tc.name), "no instance row may be created")
		})
	}

	t.Run("instance_trigger over MCP", func(t *testing.T) {
		srv := newGroupMCPServer(ds)
		res := callMCPTool(t, srv, ctx, "instance_trigger", map[string]any{"workflowName": "results-group"})
		assert.True(t, res.IsError)
		assert.Contains(t, mcpText(t, res), "this server cannot execute parallel_group yet")
		assert.Empty(t, instancesNamed(t, ds, "results-group"))
	})

	t.Run("spawned-results-group", func(t *testing.T) {
		parent := &workflow.Workflow{
			Name: "spawns-group", Description: "spawns a group workflow", Author: "e2e",
			Nodes: []workflow.Node{
				{ID: "p1", Type: workflow.NodeTypeTransform, Entrypoint: true, Config: workflow.NodeConfig{
					Operations: []workflow.Operation{{Variable: "started", Operation: "set", Value: true}}}},
				{ID: "spawn-1", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{
					ChildWorkflow: "results-group", SpawnMode: "fire_and_forget"}},
			},
			Edges: []workflow.Edge{{Source: "p1", Target: "spawn-1"}},
		}
		require.NoError(t, ds.workflowSvc.Create(ctx, "spawns-group", parent))

		parentID, _, err := ds.eng.Trigger(ctx, "spawns-group", map[string]any{}, "normal")
		require.NoError(t, err, "the non-group parent itself is triggerable")
		waitForState(t, ds, parentID, instance.StateFailed)
		assert.Empty(t, childrenOf(t, ds, parentID), "the spawn path is refused before any child row exists")
		assert.Empty(t, instancesNamed(t, ds, "results-group"))
	})

	t.Run("non-group workflow still triggers", func(t *testing.T) {
		id, _, err := ds.eng.Trigger(ctx, "group-child", map[string]any{"topic": "x"}, "normal")
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			inst, gerr := ds.instanceRepo.Get(context.Background(), id)
			return gerr == nil && inst.State == instance.StateCompleted
		}, 20*time.Second, 100*time.Millisecond)
	})
}
```

Run: `go test ./cmd/arbitron/ -run TestGroupTriggerE2E -count=1 -v` — expected all subtests `--- PASS`.

- [x] Step 8: Prove the e2e probe bites: temporarily comment out the `if err := checkGroupSupport(wf, e.GroupSupportLevel()); err != nil { ... }` block in `TriggerWithDepartment`, re-run `go test ./cmd/arbitron/ -run TestGroupTriggerE2E -count=1`, confirm it fails with
```
--- FAIL: TestGroupTriggerE2E_Phase1DefaultRefusesEveryGroupWorkflow/results-group
        	Error:      An error is expected but got nil.
```
then restore the block and re-run to green.

- [x] Step 9: Write the failing dashboard tolerance test. Create `dashboard/src/graph/parallelGroupTolerance.test.tsx`:

```tsx
import { render, renderHook, screen } from '@testing-library/react'
import { describe, it, expect, vi } from 'vitest'
import type { ComponentType, ReactNode } from 'react'
import type { Instance, NodeExecution, NodeType } from '../api/types'
import type { WorkflowDefinition } from './types'

type CapturedNode = { id: string; type?: string; data: Record<string, unknown> }

const captured: { nodes?: CapturedNode[]; nodeTypes?: Record<string, unknown> } = {}

// Render every node GraphView hands React Flow through the component React Flow
// would choose for its `type`, so an unregistered type or a throwing node
// component fails the test (the real ReactFlow needs a measured DOM).
vi.mock('@xyflow/react', async () => {
  const actual = await vi.importActual<typeof import('@xyflow/react')>('@xyflow/react')
  return {
    ...actual,
    ReactFlow: (props: {
      nodes: CapturedNode[]
      nodeTypes: Record<string, ComponentType<{ id: string; data: unknown; selected: boolean }>>
    }) => {
      captured.nodes = props.nodes
      captured.nodeTypes = props.nodeTypes
      return (
        <div data-testid="rf">
          {props.nodes.map((n) => {
            const C = props.nodeTypes[n.type ?? '']
            return C ? (
              <C key={n.id} id={n.id} data={n.data} selected={false} />
            ) : (
              <div key={n.id} data-testid="unregistered-node" />
            )
          })}
        </div>
      )
    },
    ReactFlowProvider: ({ children }: { children: ReactNode }) => children,
    Background: () => null,
    Controls: () => null,
    useNodes: () => [],
    useReactFlow: () => ({ flowToScreenPosition: (p: unknown) => p, setCenter: () => {}, getZoom: () => 1 }),
    Handle: ({ type, position }: { type: string; position: string }) => (
      <div data-testid="handle" data-handle-type={type} data-position={position} />
    ),
  }
})

import { GraphView } from '../routes/ExecutionView'
import { useGraphLayout } from './useGraphLayout'
import ArbitronNode from './nodes/ArbitronNode'

// The shape the backend stores in instance.workflowSnapshot for a nested,
// two-form group workflow (P1 contract: config.group, task/success/failure ports).
const groupSnapshot: WorkflowDefinition = {
  name: 'parallel-review',
  description: '',
  author: '',
  nodes: [
    {
      id: 'fanout', type: 'parallel_group', label: 'Lint and audit', description: '', entrypoint: true,
      config: {
        group: {
          form: 'branches',
          tasks: [{ key: 'lint', entry: 'lint-run' }, { key: 'audit', entry: 'audit-fanout' }],
          completionRule: 'all_succeed', unfinishedBehavior: 'cancel_unfinished', gatherMode: 'results',
        },
      },
    },
    { id: 'lint-run', type: 'tool_call', label: 'Lint', description: '', config: { command: 'make' } },
    {
      id: 'audit-fanout', type: 'parallel_group', label: '', description: '',
      config: {
        group: {
          form: 'workflows',
          tasks: [{ key: 'security', workflow: 'pr-audit', inputs: { focus: 'security' } }],
          completionRule: 'first_success', unfinishedBehavior: 'finish_independently', gatherMode: 'results',
        },
      },
    },
    { id: 'audit-ok', type: 'transform', label: 'Audit ok', description: '', config: {} },
    { id: 'report', type: 'transform', label: 'Report', description: '', config: {} },
    { id: 'report-failure', type: 'transform', label: 'Report failure', description: '', config: {} },
  ],
  edges: [
    { source: 'fanout', target: 'lint-run', output: 'task', label: 'lint' },
    { source: 'fanout', target: 'audit-fanout', output: 'task', label: 'audit' },
    { source: 'fanout', target: 'report', output: 'success' },
    { source: 'fanout', target: 'report-failure', output: 'failure' },
    { source: 'audit-fanout', target: 'audit-ok', output: 'success' },
  ],
}

describe('parallel_group tolerance (Phase 1)', () => {
  it('NodeType includes parallel_group', () => {
    const t: NodeType = 'parallel_group'
    expect(t).toBe('parallel_group')
  })

  it('renders an instance snapshot containing parallel_group nodes without throwing', () => {
    const inst = {
      id: 'i1', state: 'running', currentNodeId: 'fanout', workflowSnapshot: groupSnapshot,
    } as unknown as Instance
    expect(() =>
      render(
        <GraphView
          instance={inst}
          workflowDef={groupSnapshot}
          trace={[] as NodeExecution[]}
          onNodeClick={() => {}}
          selectedNodeId={null}
        />,
      ),
    ).not.toThrow()

    const groups = captured.nodes!.filter((n) => n.data?.nodeType === 'parallel_group')
    expect(groups.map((n) => n.id).sort()).toEqual(['audit-fanout', 'fanout'])
    for (const n of groups) expect(n.type).toBe('arbitronNode')
    for (const n of captured.nodes!) expect(Object.keys(captured.nodeTypes!)).toContain(n.type)
    expect(screen.queryAllByTestId('unregistered-node')).toHaveLength(0)
    expect(screen.getAllByText('parallel group')).toHaveLength(2)
    expect(screen.getByText('Lint and audit')).toBeInTheDocument()
    expect(screen.getByText('audit-fanout')).toBeInTheDocument() // empty label falls back to the id
  })

  it('lays out task, success and failure edges with their labels', () => {
    const { result } = renderHook(() => useGraphLayout({ nodes: groupSnapshot.nodes, edges: groupSnapshot.edges }))
    const labels = result.current.rfEdges.map((e) => e.label)
    expect(labels).toEqual(['lint', 'audit', 'success', 'failure', 'success'])
    expect(result.current.rfNodes.some((n) => n.type === 'loopContainer')).toBe(false)
    expect(result.current.rfNodes.filter((n) => n.type === 'arbitronNode')).toHaveLength(6)
  })

  it('gives parallel_group its own accent instead of the unknown-type grey', () => {
    const props = {
      data: { label: 'Fan out', nodeType: 'parallel_group', executionState: 'running' },
      selected: false,
    } as unknown as Parameters<typeof ArbitronNode>[0]
    const { container } = render(<ArbitronNode {...props} />)
    const bar = container.querySelector('[class*="typeBar"]') as HTMLElement
    const label = container.querySelector('[class*="typeLabel"]') as HTMLElement
    expect(bar.style.background).toBe('rgb(52, 152, 219)')
    expect(label.textContent).toBe('parallel group')
  })
})
```

Run it:
```bash
cd dashboard && npx tsc --noEmit && npx vitest run src/graph/parallelGroupTolerance.test.tsx
```
Expected failure:
```
src/graph/parallelGroupTolerance.test.tsx(4,40): error TS2305: Module '"../api/types"' has no exported member 'NodeType'.
```
(and, if run without `tsc`, `npx vitest run src/graph/parallelGroupTolerance.test.tsx` fails `gives parallel_group its own accent ...` with `expected 'rgb(136, 136, 136)' to be 'rgb(52, 152, 219)'`).

- [x] Step 10: Implement dashboard tolerance. In `dashboard/src/api/types.ts`, after line 15 (`export type TriggerSource = ...`), add:

```ts
/**
 * Workflow node types the backend accepts (mirrors domain/workflow NodeType).
 * Graph code keeps WorkflowNode.type a plain string so an unknown or newer type
 * still renders through the default node; this union is for code that wants to
 * be exhaustive over the known set.
 */
export type NodeType =
  | 'llm_call'
  | 'agent_call'
  | 'script'
  | 'tool_call'
  | 'conditional'
  | 'transform'
  | 'timer_delay'
  | 'webhook_wait'
  | 'interactive_llm'
  | 'interactive_agent'
  | 'spawn_workflow'
  | 'parallel_group'
```

In `dashboard/src/graph/nodes/ArbitronNode.tsx`, replace lines 1-17 with:

```tsx
import { memo } from 'react'
import { Handle, Position } from '@xyflow/react'
import type { NodeProps, Node } from '@xyflow/react'
import type { NodeType } from '../../api/types'
import type { NodeExecutionState } from '../types'
import styles from './ArbitronNode.module.css'

/**
 * Accent colors keyed by workflow node type. `satisfies` rejects a key that is
 * not a known NodeType (a typo); any type without an entry — including one the
 * backend adds later — falls back to the default grey below.
 */
const typeColors: Record<string, string> = {
  llm_call: '#7c8cf5',
  script: '#e67e22',
  tool_call: '#1abc9c',
  conditional: '#f39c12',
  transform: '#9b59b6',
  timer_delay: '#00bcd4',
  webhook_wait: '#e91e63',
  spawn_workflow: '#2ecc71',
  parallel_group: '#3498db',
} satisfies Partial<Record<NodeType, string>>
```
(the rest of the file is unchanged; `parallel_group` renders as the label "parallel group" through the existing `replace(/_/g, ' ')`).

Do NOT run `make ui` here: the embedded build already tolerates the type (grey fallback); P4-T10 refreshes `adapter/dashboard/dist` once with the full group rendering.

- [x] Step 11: Run the dashboard checks and the existing graph/route suites.

```bash
cd dashboard && npx tsc --noEmit && npx vitest run src/graph/parallelGroupTolerance.test.tsx src/graph/nodes/ArbitronNode.test.tsx src/graph/useGraphLayout.test.ts src/routes/GraphView.loops.test.tsx
```
Expected: `tsc` exits 0; vitest `Test Files  4 passed (4)`.

- [x] Step 12: Full regression.

```bash
go build ./... && go test ./domain/engine/... ./adapter/http/... ./cmd/arbitron/... -count=1
cd dashboard && npx vitest run
```
Expected: `ok` for every Go package; vitest all files passed.

### Execution evidence and self-review

- Red → green: engine tests initially failed to compile (missing support API); REST test returned 500 instead of 422; dashboard failed the missing `NodeType` check and grey-accent assertion. Each passed after its specified implementation.
- Provenance probe: removing the trigger guard failed the composition-root group tests and exposed an instance row in SQLite; restoring it returned the test to green.
- Spec-compliance review: only the staged guard, REST mapping, and dashboard tolerance were added. Phase 1 retains the zero-value support level; no executor is enabled and no embedded assets are rebuilt.
- Anti-green-theater review: the e2e uses real service-authored definitions and storage sinks. Supplied MCP priority explicitly and asserted the persisted spawn error names the capability refusal, so unrelated trigger failures or missing spawn wiring cannot satisfy the test.
- Code-quality review: checked neighboring engine/handler/graph patterns, mutex protection, fail-closed levels, backend node-type parity, formatting, and diff whitespace.
- Verification: `go build ./...`, all three specified Go packages, TypeScript, four focused graph suites (35 tests), and all 51 dashboard suites (526 tests) passed.

- [x] Step 13: Commit.

```bash
git add domain/engine/group_support.go domain/engine/engine.go domain/engine/group_support_internal_test.go \
  domain/engine/engine_group_support_test.go adapter/http/workflow_handler.go adapter/http/workflow_handler_group_trigger_test.go \
  cmd/arbitron/group_trigger_e2e_test.go dashboard/src/api/types.ts dashboard/src/graph/nodes/ArbitronNode.tsx \
  dashboard/src/graph/parallelGroupTolerance.test.tsx
git commit -m "feat(engine): refuse parallel_group triggers until execution is wired

Engine.SetGroupSupport/GroupSupportLevel with default GroupSupportNone;
TriggerWithDepartment checks every group node right after the visibility-gated
load and before any instance is created (covers trigger, rerun, spawn and
schedules) with an actionable ErrGroupExecutionUnsupported naming the node.
Results level refuses only results_and_files groups. REST maps it to 422.
Dashboard: NodeType union incl. parallel_group, group accent, and a test that
a snapshot with group nodes renders through the default node.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
