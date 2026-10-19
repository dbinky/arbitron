# Parallel Workflow Gathering — Phase 1 Task 2: Group ownership analysis (AnalyzeGroups)

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Compute, for every `parallel_group`, the exclusive node ownership of each branch body (recursively through nested groups) and enforce the group graph rules — ports, task edges, body ownership (overlap / external entry / escape) and no group on a cycle — in one shared, never-panicking analysis.

**Architecture:** A new pure file `domain/workflow/group_ownership.go` exports `BodyOwner`, `GroupScope`, `GroupGraph`, `HasGroups` and `AnalyzeGroups`. Regions (main path + one per task body) are grown in waves by BFS over continuation edges with nested groups treated atomically; growth stops at earlier-wave claims and at other bodies' entries, and each stopped edge is classified once the region tree is known. Cycle membership reuses `domain/workflow/lint.go:buildAdjacency`/`nodeOnCycle` over ALL edges.

**Tech Stack:** Go 1.25.5, standard library (`sort`, `strconv`, `strings`), testify.

**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` (Parallel groups: nesting, fixed task order); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-1.md` §2 "Validation" (ownership bullet list, "everything reachable is insufficient", cycle rule) — resolves the ownership half of **PLANNED DEPENDENCY P1-A**.

## Interfaces

**Consumes:**
- P1-T1 (`domain/workflow/group.go`, `workflow.go`, `validation.go`):
  - `const NodeTypeParallelGroup NodeType = "parallel_group"`
  - `type GroupConfig struct { Form string; Tasks []GroupTask; CompletionRule, UnfinishedBehavior, GatherMode, RepoPath, ResolverProfile, ResolverPromptFile string }`
  - `type GroupTask struct { Key, Entry, Workflow string; Inputs map[string]any }`
  - `GroupFormBranches`, `GroupFormWorkflows`, `GroupPortTask`, `GroupPortSuccess`, `GroupPortFailure`, all `GroupRule*`/`GroupUnfinished*`/`GroupGather*` constants
  - `NodeConfig.Group *GroupConfig`; `ValidationError.TaskKey string`
  - `func ParseGroupConfig(cfg NodeConfig) (GroupConfig, error)`; unexported `func cloneGroupConfig(g GroupConfig) GroupConfig`
  - Test helpers from `domain/workflow/group_test.go` are NOT used; this task's test file defines its own `grp*` builders.
- Existing: `domain/workflow/lint.go:45` `func buildAdjacency(wf *Workflow) map[string][]string`; `domain/workflow/lint.go:58` `func nodeOnCycle(start string, adj map[string][]string) bool`; `domain/workflow/workflow.go:160` `Workflow{Nodes []Node; Edges []Edge}`, `:195` `Node{ID; Type; Config; Entrypoint}`, `:250` `Edge{Source, Target, Output, Label}`; `domain/workflow/validation.go:76` `ValidationError`.

**Produces (exact signatures):**
```go
type BodyOwner struct {
	GroupNodeID string `json:"groupNodeId"`
	TaskKey     string `json:"taskKey"`
}
type GroupScope struct {
	NodeID        string              `json:"nodeId"`
	Config        GroupConfig         `json:"config"`
	Parent        *BodyOwner          `json:"parent,omitempty"`
	Bodies        map[string][]string `json:"bodies"`
	Nested        []string            `json:"nested"`
	SuccessTarget string              `json:"successTarget"`
	FailureTarget string              `json:"failureTarget"`
}
type GroupGraph struct {
	Groups map[string]*GroupScope `json:"groups"`
	Owner  map[string]BodyOwner   `json:"owner"`
	Order  []string               `json:"order"`
}
func AnalyzeGroups(wf *Workflow) (*GroupGraph, []ValidationError)
func HasGroups(wf *Workflow) bool
```
Guarantees: the returned graph is never nil and its maps/slices are never nil (`Groups`, `Owner`, `Order`, every scope's `Bodies` and `Nested`); `Config` is the parsed config when valid, else a deep copy of the raw config (zero when `group` is absent); `Bodies[key]` lists the task's exclusively owned nodes in `wf.Nodes` order (entry first when it is first in the manifest), including nested group nodes and their success/failure targets but never the nested groups' own bodies; `Owner` maps body nodes to their IMMEDIATE (group, task); `Parent == nil` for groups on the main path or unreachable from the entrypoint; `SuccessTarget`/`FailureTarget` are the targets of the first `success`/`failure` edge ("" if absent). Findings carry `NodeID` = the group node id and, when task-scoped, `TaskKey`.

Codes produced here (stable strings): `GROUP_UNKNOWN_ENTRY`, `GROUP_TASK_EDGE_MISMATCH`, `GROUP_MISSING_PORT`, `GROUP_DUPLICATE_PORT`, `GROUP_EXTRA_PORT`, `GROUP_BODY_OVERLAP`, `GROUP_BODY_EXTERNAL_EDGE`, `GROUP_BODY_ESCAPE`, `GROUP_ON_CYCLE`. Config codes are NOT produced here (P1-T3 maps `ParseGroupConfig` problems), so nothing is double-reported.

Test helpers produced in `domain/workflow/group_ownership_test.go` (package `workflow_test`) and reused by the P1-T3 and P1-T4 test files:
```go
func grpT(id string) workflow.Node                                   // transform node with one set operation
func grpEntryT(id string) workflow.Node                              // grpT marked Entrypoint
func grpGroup(id string, cfg *workflow.GroupConfig) workflow.Node    // parallel_group node
func grpBranches(tasks ...[2]string) *workflow.GroupConfig           // valid branches config; pairs {key, entry}
func grpWorkflows(tasks ...[2]string) *workflow.GroupConfig          // valid workflows config; pairs {key, child}
func grpE(src, tgt string) workflow.Edge
func grpTaskE(g, entry string) workflow.Edge                         // Output "task"
func grpOK(g, tgt string) workflow.Edge                              // Output "success"
func grpFail(g, tgt string) workflow.Edge                            // Output "failure"
func grpBasic() *workflow.Workflow                                   // start -> g; g =task=> a1 -> a2; g =task=> b1; g -> done/fail; Name "fan"
func grpDeep() *workflow.Workflow                                    // 3-level nesting, both forms
func grpHas(errs []workflow.ValidationError, code, nodeID, taskKey string) bool
func grpCodes(errs []workflow.ValidationError) []string              // "CODE@node/task" for failure messages
func grpDropEdge(edges []workflow.Edge, src, output string) []workflow.Edge
```

## Wiring & data provenance

- **No composition root of its own** — a pure function. It becomes live through:
  - `Validate` → `validateGroups` (**P1-T3**, `domain/workflow/validation_group.go`), which appends `AnalyzeGroups` findings for every group-bearing workflow;
  - `Service.ValidateGroups` / Create / Update rejection (**P1-T5**) and `Service.UpsertGroup` / `GetGroup` / `ListGroups` / `DeleteGroup` (**P1-T6**: `GroupView.Bodies`, `Nested`, `Parent` are copied from `GroupScope`; `GroupDeleteResult.RetainedNodes` from `Bodies`), exposed by MCP `workflow_group_*` (**P1-T7**) and wired in `cmd/arbitron/main.go` (**P1-T8**);
  - run time: the branch launcher `GroupLauncher.Launch` (**P2-T8**, `GroupLaunchRequest.Ownership *workflow.GroupScope`) and `ParallelGroupExecutor` (**P2-T11**) analyse the parent's `WorkflowSnapshot`; `GroupRun.BodyOwnership` (**P2-T1**) persists `GroupScope.Bodies`;
  - Phase 4: `GraphState.Layout` (**P4-T1**) and `GET /api/workflows/{name}/group-layout` (**P4-T3**) return `GroupGraph` as JSON.
- **Producers of every input:** `wf *Workflow` from `domain/workflow/service.go:Service.Load` (line 356) → scoped `Repository.Load` → `adapter/filesystem/workflow_repo.go:WorkflowRepo.Load` (line 99); at write time the manifest passed to `Service.Create` (line 234) / `Service.Update` (line 271) by `adapter/mcp/tools_workflow.go:workflowCreateHandler`/`workflowUpdateHandler`; at run time the instance's `WorkflowSnapshot`. Group configs inside it are read through P1-T1 `ParseGroupConfig`.
- **Provenance probe:** `TestAnalyzeGroups_NoTaskEdgesIsReportedPerTask` fails if a branches group whose `task` edges are absent is analysed into silently "valid" bodies; `TestAnalyzeGroups_EscapedContinuationIsNotAbsorbed` fails if bodies are computed as plain reachability (verified by mutation: disabling the stop-at-claimed rule fails it and two other escape tests).

### Task 2: AnalyzeGroups ownership analysis

**Files:**
- Create: `domain/workflow/group_ownership.go`
- Test: `domain/workflow/group_ownership_test.go` (package `workflow_test`, testify)

No existing file is modified. `buildAdjacency`/`nodeOnCycle` (`domain/workflow/lint.go:45-76`) are reused unchanged.

#### The rules being implemented (read before coding)

1. Ports: a group has exactly one `success` and one `failure` edge (`GROUP_MISSING_PORT` / `GROUP_DUPLICATE_PORT`) and no other output names, the empty name included (`GROUP_EXTRA_PORT`).
2. Task edges: branches form — exactly one `task` edge per task, matched by its UNIQUE entry target (every task edge has the same port name `task`); a task edge to a non-entry is a mismatch. Workflows form — no task edges. All → `GROUP_TASK_EDGE_MISMATCH`. A task entry that is not a node → `GROUP_UNKNOWN_ENTRY`.
3. Regions: the main path region is grown from the entrypoint; each task body from its entry. Growth follows CONTINUATION edges only (all edges of ordinary nodes, `success`/`failure` of group nodes); a nested group is atomic. Regions are grown wave by wave (main → bodies of main-path groups → bodies of groups in those bodies …); growth stops at nodes claimed by an earlier wave and at other bodies' entries, recording the stopped edge.
4. Classification of stopped edges: into an enclosing group node → `GROUP_BODY_OVERLAP`; from a body to anywhere not inside it (enclosing path, continuation, sibling) → `GROUP_BODY_ESCAPE`; otherwise (main path or an enclosing body entering a body without the owning task edge) → `GROUP_BODY_EXTERNAL_EDGE`.
5. Overlap: a node reached by two regions of the same wave (sibling bodies) → `GROUP_BODY_OVERLAP` on the later region; the first task in author order keeps it. An entry that is the workflow entrypoint, its own group node, or another task's entry → `GROUP_BODY_OVERLAP`.
6. Cycle: a group node on a cycle over ALL edges (task edges included) → `GROUP_ON_CYCLE`. Loops that do not pass through a group stay valid.

- [x] Step 1: Write the failing test. Create `domain/workflow/group_ownership_test.go`:

```go
package workflow_test

import (
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- fixture builders ----------

func grpT(id string) workflow.Node {
	return workflow.Node{ID: id, Type: workflow.NodeTypeTransform,
		Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: "v_" + strings.ReplaceAll(id, "-", "_"), Operation: "set", Value: 1}}}}
}

func grpEntryT(id string) workflow.Node {
	n := grpT(id)
	n.Entrypoint = true
	return n
}

func grpGroup(id string, cfg *workflow.GroupConfig) workflow.Node {
	return workflow.Node{ID: id, Type: workflow.NodeTypeParallelGroup, Config: workflow.NodeConfig{Group: cfg}}
}

// grpBranches builds a valid branches config; each pair is {key, entry}.
func grpBranches(tasks ...[2]string) *workflow.GroupConfig {
	cfg := &workflow.GroupConfig{
		Form:               workflow.GroupFormBranches,
		CompletionRule:     workflow.GroupRuleAllSucceed,
		UnfinishedBehavior: workflow.GroupUnfinishedCancel,
		GatherMode:         workflow.GroupGatherResults,
	}
	for _, t := range tasks {
		cfg.Tasks = append(cfg.Tasks, workflow.GroupTask{Key: t[0], Entry: t[1]})
	}
	return cfg
}

// grpWorkflows builds a valid workflows config; each pair is {key, child}.
func grpWorkflows(tasks ...[2]string) *workflow.GroupConfig {
	cfg := &workflow.GroupConfig{
		Form:               workflow.GroupFormWorkflows,
		CompletionRule:     workflow.GroupRuleFirstSuccess,
		UnfinishedBehavior: workflow.GroupUnfinishedFinishIndependently,
		GatherMode:         workflow.GroupGatherResults,
	}
	for _, t := range tasks {
		cfg.Tasks = append(cfg.Tasks, workflow.GroupTask{Key: t[0], Workflow: t[1]})
	}
	return cfg
}

func grpE(src, tgt string) workflow.Edge { return workflow.Edge{Source: src, Target: tgt} }
func grpTaskE(g, entry string) workflow.Edge {
	return workflow.Edge{Source: g, Target: entry, Output: workflow.GroupPortTask}
}
func grpOK(g, tgt string) workflow.Edge {
	return workflow.Edge{Source: g, Target: tgt, Output: workflow.GroupPortSuccess}
}
func grpFail(g, tgt string) workflow.Edge {
	return workflow.Edge{Source: g, Target: tgt, Output: workflow.GroupPortFailure}
}

// grpBasic is the canonical valid branches workflow:
//
//	start -> g ; g =task=> a1 -> a2 ; g =task=> b1 ; g -success-> done ; g -failure-> fail
func grpBasic() *workflow.Workflow {
	return &workflow.Workflow{
		Name: "fan",
		Nodes: []workflow.Node{
			grpEntryT("start"),
			grpGroup("g", grpBranches([2]string{"a", "a1"}, [2]string{"b", "b1"})),
			grpT("a1"), grpT("a2"), grpT("b1"), grpT("done"), grpT("fail"),
		},
		Edges: []workflow.Edge{
			grpE("start", "g"),
			grpTaskE("g", "a1"), grpE("a1", "a2"),
			grpTaskE("g", "b1"),
			grpOK("g", "done"), grpFail("g", "fail"),
		},
	}
}

// grpHas reports whether errs has a finding with exactly this code, node and
// task. Asserting all three is what makes a finding actionable.
func grpHas(errs []workflow.ValidationError, code, nodeID, taskKey string) bool {
	for _, e := range errs {
		if e.Code == code && e.NodeID == nodeID && e.TaskKey == taskKey {
			return true
		}
	}
	return false
}

func grpCodes(errs []workflow.ValidationError) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Code+"@"+e.NodeID+"/"+e.TaskKey)
	}
	return out
}

// ---------- HasGroups ----------

func TestHasGroups(t *testing.T) {
	assert.False(t, workflow.HasGroups(nil))
	assert.False(t, workflow.HasGroups(&workflow.Workflow{Nodes: []workflow.Node{grpEntryT("x")}}))
	assert.True(t, workflow.HasGroups(grpBasic()))
}

// ---------- happy path ----------

func TestAnalyzeGroups_BranchesHappyPath(t *testing.T) {
	g, errs := workflow.AnalyzeGroups(grpBasic())
	require.Empty(t, errs, grpCodes(errs))
	require.Contains(t, g.Groups, "g")
	sc := g.Groups["g"]
	assert.Equal(t, "g", sc.NodeID)
	assert.Nil(t, sc.Parent, "a group on the main path has no parent")
	assert.Equal(t, map[string][]string{"a": {"a1", "a2"}, "b": {"b1"}}, sc.Bodies)
	assert.Equal(t, "done", sc.SuccessTarget)
	assert.Equal(t, "fail", sc.FailureTarget)
	assert.Empty(t, sc.Nested)
	assert.Equal(t, workflow.GroupFormBranches, sc.Config.Form)
	assert.Equal(t, []string{"g"}, g.Order)
	assert.Equal(t, workflow.BodyOwner{GroupNodeID: "g", TaskKey: "a"}, g.Owner["a2"])
	assert.Equal(t, workflow.BodyOwner{GroupNodeID: "g", TaskKey: "b"}, g.Owner["b1"])
	for _, mainNode := range []string{"start", "g", "done", "fail"} {
		_, owned := g.Owner[mainNode]
		assert.False(t, owned, "%s is on the main path and must not be owned by a body", mainNode)
	}
}

func TestAnalyzeGroups_WorkflowsFormHasNoBodies(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "fan",
		Nodes: []workflow.Node{
			grpEntryT("start"),
			grpGroup("g", grpWorkflows([2]string{"east", "deploy"}, [2]string{"west", "deploy"})),
			grpT("done"), grpT("fail"),
		},
		Edges: []workflow.Edge{grpE("start", "g"), grpOK("g", "done"), grpFail("g", "fail")},
	}
	g, errs := workflow.AnalyzeGroups(wf)
	require.Empty(t, errs, grpCodes(errs))
	require.NotNil(t, g.Groups["g"].Bodies)
	assert.Empty(t, g.Groups["g"].Bodies)
	assert.Empty(t, g.Owner)
}

// A group may be the workflow entrypoint: results-only fan-out needs no setup node.
func TestAnalyzeGroups_EntrypointGroupIsValid(t *testing.T) {
	wf := grpBasic()
	wf.Nodes = wf.Nodes[1:]
	wf.Nodes[0].Entrypoint = true
	wf.Edges = wf.Edges[1:]
	g, errs := workflow.AnalyzeGroups(wf)
	require.Empty(t, errs, grpCodes(errs))
	assert.Equal(t, []string{"a1", "a2"}, g.Groups["g"].Bodies["a"])
}

// Edge case: one task whose body is a single sink node.
func TestAnalyzeGroups_OneTaskSinkBody(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "one",
		Nodes: []workflow.Node{
			grpEntryT("start"), grpGroup("g", grpBranches([2]string{"only", "s1"})),
			grpT("s1"), grpT("done"), grpT("fail"),
		},
		Edges: []workflow.Edge{grpE("start", "g"), grpTaskE("g", "s1"), grpOK("g", "done"), grpFail("g", "fail")},
	}
	g, errs := workflow.AnalyzeGroups(wf)
	require.Empty(t, errs, grpCodes(errs))
	assert.Equal(t, map[string][]string{"only": {"s1"}}, g.Groups["g"].Bodies)
}

// Edge case: every task edge carries the SAME port name "task" (and here no
// label); tasks are matched by their unique entry, whatever the edge order.
func TestAnalyzeGroups_UniqueEntriesWithIdenticalTaskPortNames(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "ports",
		Nodes: []workflow.Node{
			grpEntryT("start"),
			grpGroup("g", grpBranches([2]string{"x", "x1"}, [2]string{"y", "y1"}, [2]string{"z", "z1"})),
			grpT("x1"), grpT("y1"), grpT("z1"), grpT("done"), grpT("fail"),
		},
		Edges: []workflow.Edge{
			grpE("start", "g"),
			grpTaskE("g", "z1"), grpTaskE("g", "x1"), grpTaskE("g", "y1"),
			grpOK("g", "done"), grpFail("g", "fail"),
		},
	}
	g, errs := workflow.AnalyzeGroups(wf)
	require.Empty(t, errs, grpCodes(errs))
	assert.Equal(t, map[string][]string{"x": {"x1"}, "y": {"y1"}, "z": {"z1"}}, g.Groups["g"].Bodies)
}

// Ordinary loops inside a body and on the main path stay valid; only a group
// ON a cycle is rejected.
func TestAnalyzeGroups_OrdinaryLoopsElsewhereStayValid(t *testing.T) {
	wf := grpBasic()
	wf.Nodes = append(wf.Nodes, grpT("retry"))
	wf.Edges = append(wf.Edges,
		grpE("a2", "a1"),                             // loop inside body a
		grpE("done", "retry"), grpE("retry", "done"), // loop on the main path after the group
	)
	g, errs := workflow.AnalyzeGroups(wf)
	require.Empty(t, errs, grpCodes(errs))
	assert.Equal(t, []string{"a1", "a2"}, g.Groups["g"].Bodies["a"])
}

// ---------- deep nesting: three levels, both forms nested ----------

// grpDeep: g1 (branches, main) -> task a body contains g2 (branches) -> task x
// body contains g3 (workflows). Task b's entry is itself a nested group g4.
func grpDeep() *workflow.Workflow {
	return &workflow.Workflow{
		Name: "deep",
		Nodes: []workflow.Node{
			grpEntryT("start"),
			grpGroup("g1", grpBranches([2]string{"a", "a1"}, [2]string{"b", "g4"})),
			grpT("a1"),
			grpGroup("g2", grpBranches([2]string{"x", "x1"})),
			grpT("a_ok"), grpT("a_fail"),
			grpT("x1"),
			grpGroup("g3", grpWorkflows([2]string{"w1", "child"}, [2]string{"w2", "child"})),
			grpT("x_ok"), grpT("x_fail"),
			grpGroup("g4", grpWorkflows([2]string{"only", "other"})),
			grpT("b_ok"), grpT("b_fail"),
			grpT("done"), grpT("fail"),
		},
		Edges: []workflow.Edge{
			grpE("start", "g1"),
			grpTaskE("g1", "a1"), grpTaskE("g1", "g4"),
			grpOK("g1", "done"), grpFail("g1", "fail"),
			grpE("a1", "g2"),
			grpTaskE("g2", "x1"),
			grpOK("g2", "a_ok"), grpFail("g2", "a_fail"),
			grpE("x1", "g3"),
			grpOK("g3", "x_ok"), grpFail("g3", "x_fail"),
			grpOK("g4", "b_ok"), grpFail("g4", "b_fail"),
		},
	}
}

func TestAnalyzeGroups_DeepNestingBothForms(t *testing.T) {
	g, errs := workflow.AnalyzeGroups(grpDeep())
	require.Empty(t, errs, grpCodes(errs))

	assert.Equal(t, []string{"g1", "g2", "g3", "g4"}, g.Order)
	assert.Nil(t, g.Groups["g1"].Parent)
	assert.Equal(t, &workflow.BodyOwner{GroupNodeID: "g1", TaskKey: "a"}, g.Groups["g2"].Parent)
	assert.Equal(t, &workflow.BodyOwner{GroupNodeID: "g2", TaskKey: "x"}, g.Groups["g3"].Parent)
	assert.Equal(t, &workflow.BodyOwner{GroupNodeID: "g1", TaskKey: "b"}, g.Groups["g4"].Parent)

	assert.Equal(t, []string{"g2", "g4"}, g.Groups["g1"].Nested)
	assert.Equal(t, []string{"g3"}, g.Groups["g2"].Nested)
	assert.Empty(t, g.Groups["g3"].Nested)

	// Nested groups' continuations stay inside the enclosing body; their own
	// task bodies are owned recursively, never by the outer task.
	assert.Equal(t, []string{"a1", "g2", "a_ok", "a_fail"}, g.Groups["g1"].Bodies["a"])
	assert.Equal(t, []string{"g4", "b_ok", "b_fail"}, g.Groups["g1"].Bodies["b"])
	assert.Equal(t, []string{"x1", "g3", "x_ok", "x_fail"}, g.Groups["g2"].Bodies["x"])
	assert.Equal(t, workflow.BodyOwner{GroupNodeID: "g2", TaskKey: "x"}, g.Owner["x1"])
	assert.Equal(t, workflow.BodyOwner{GroupNodeID: "g2", TaskKey: "x"}, g.Owner["g3"])
	assert.Equal(t, workflow.BodyOwner{GroupNodeID: "g1", TaskKey: "a"}, g.Owner["g2"])
	assert.Equal(t, workflow.BodyOwner{GroupNodeID: "g1", TaskKey: "b"}, g.Owner["g4"])
}

func TestAnalyzeGroups_EdgeIntoNestedBodyFromEnclosingBody(t *testing.T) {
	wf := grpDeep()
	wf.Edges = append(wf.Edges, grpE("a1", "x1")) // bypasses g2's task edge
	_, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_BODY_EXTERNAL_EDGE", "g2", "x"), grpCodes(errs))
}

func TestAnalyzeGroups_NestedBodyEscapesToEnclosingContinuation(t *testing.T) {
	wf := grpDeep()
	wf.Edges = append(wf.Edges, grpE("x_ok", "a_ok")) // a_ok is g2's success target
	g, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_BODY_ESCAPE", "g2", "x"), grpCodes(errs))
	assert.NotContains(t, g.Groups["g2"].Bodies["x"], "a_ok")
}

// ---------- port and task-edge rules ----------

// Provenance probe: a group whose task edges are absent must be REPORTED, not
// analysed into an empty body that a launcher would happily start.
func TestAnalyzeGroups_NoTaskEdgesIsReportedPerTask(t *testing.T) {
	wf := grpBasic()
	var kept []workflow.Edge
	for _, e := range wf.Edges {
		if e.Output != workflow.GroupPortTask {
			kept = append(kept, e)
		}
	}
	wf.Edges = kept
	_, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_TASK_EDGE_MISMATCH", "g", "a"), grpCodes(errs))
	assert.True(t, grpHas(errs, "GROUP_TASK_EDGE_MISMATCH", "g", "b"), grpCodes(errs))
}

func TestAnalyzeGroups_TaskEdgeToUndeclaredEntry(t *testing.T) {
	wf := grpBasic()
	wf.Edges = append(wf.Edges, grpTaskE("g", "done"))
	_, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_TASK_EDGE_MISMATCH", "g", ""), grpCodes(errs))
}

func TestAnalyzeGroups_DuplicateTaskEdgeForOneTask(t *testing.T) {
	wf := grpBasic()
	wf.Edges = append(wf.Edges, grpTaskE("g", "a1"))
	_, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_TASK_EDGE_MISMATCH", "g", "a"), grpCodes(errs))
}

func TestAnalyzeGroups_WorkflowsFormRejectsTaskEdges(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "fan",
		Nodes: []workflow.Node{
			grpEntryT("start"), grpGroup("g", grpWorkflows([2]string{"east", "deploy"})),
			grpT("stray"), grpT("done"), grpT("fail"),
		},
		Edges: []workflow.Edge{grpE("start", "g"), grpTaskE("g", "stray"), grpOK("g", "done"), grpFail("g", "fail")},
	}
	_, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_TASK_EDGE_MISMATCH", "g", ""), grpCodes(errs))
}

func TestAnalyzeGroups_UnknownEntry(t *testing.T) {
	wf := grpBasic()
	wf.Nodes[1].Config.Group.Tasks[1].Entry = "ghost"
	_, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_UNKNOWN_ENTRY", "g", "b"), grpCodes(errs))
}

func TestAnalyzeGroups_PortRules(t *testing.T) {
	cases := []struct {
		name string
		mut  func(wf *workflow.Workflow)
		code string
	}{
		{"missing success", func(wf *workflow.Workflow) { wf.Edges = grpDropEdge(wf.Edges, "g", workflow.GroupPortSuccess) }, "GROUP_MISSING_PORT"},
		{"missing failure", func(wf *workflow.Workflow) { wf.Edges = grpDropEdge(wf.Edges, "g", workflow.GroupPortFailure) }, "GROUP_MISSING_PORT"},
		{"duplicate failure", func(wf *workflow.Workflow) { wf.Edges = append(wf.Edges, grpFail("g", "done")) }, "GROUP_DUPLICATE_PORT"},
		{"unnamed edge", func(wf *workflow.Workflow) { wf.Edges = append(wf.Edges, grpE("g", "done")) }, "GROUP_EXTRA_PORT"},
		{"unknown port", func(wf *workflow.Workflow) {
			wf.Edges = append(wf.Edges, workflow.Edge{Source: "g", Target: "done", Output: "partial"})
		}, "GROUP_EXTRA_PORT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf := grpBasic()
			tc.mut(wf)
			_, errs := workflow.AnalyzeGroups(wf)
			assert.True(t, grpHas(errs, tc.code, "g", ""), grpCodes(errs))
		})
	}
}

func grpDropEdge(edges []workflow.Edge, src, output string) []workflow.Edge {
	var out []workflow.Edge
	for _, e := range edges {
		if e.Source == src && e.Output == output {
			continue
		}
		out = append(out, e)
	}
	return out
}

// ---------- ownership rules ----------

func TestAnalyzeGroups_ExternalIncomingEdgeIntoBodyEntry(t *testing.T) {
	wf := grpBasic()
	wf.Edges = append(wf.Edges, grpE("start", "a1")) // main path enters body a
	g, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_BODY_EXTERNAL_EDGE", "g", "a"), grpCodes(errs))
	_, mainOwned := g.Owner["start"]
	assert.False(t, mainOwned)
}

// THE reason bodies are not "everything reachable from the entry": a body edge
// back to the group's success target would make naive reachability absorb
// the main-path node "done" (and everything after it) into body a.
func TestAnalyzeGroups_EscapedContinuationIsNotAbsorbed(t *testing.T) {
	wf := grpBasic()
	wf.Nodes = append(wf.Nodes, grpT("after"))
	wf.Edges = append(wf.Edges, grpE("done", "after"), grpE("a2", "done"))
	g, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_BODY_ESCAPE", "g", "a"), grpCodes(errs))
	assert.Equal(t, []string{"a1", "a2"}, g.Groups["g"].Bodies["a"], "done/after must stay on the main path")
	_, owned := g.Owner["done"]
	assert.False(t, owned)
}

func TestAnalyzeGroups_EscapeDeeperIntoMainPath(t *testing.T) {
	wf := grpBasic()
	wf.Nodes = append(wf.Nodes, grpT("after"))
	wf.Edges = append(wf.Edges, grpE("done", "after"), grpE("a1", "after"))
	g, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_BODY_ESCAPE", "g", "a"), grpCodes(errs))
	assert.NotContains(t, g.Groups["g"].Bodies["a"], "after")
}

func TestAnalyzeGroups_EscapeToSiblingEntry(t *testing.T) {
	wf := grpBasic()
	wf.Edges = append(wf.Edges, grpE("a2", "b1"))
	_, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_BODY_ESCAPE", "g", "a"), grpCodes(errs))
}

func TestAnalyzeGroups_SiblingOverlap(t *testing.T) {
	wf := grpBasic()
	wf.Nodes = append(wf.Nodes, grpT("shared"))
	wf.Edges = append(wf.Edges, grpE("a2", "shared"), grpE("b1", "shared"))
	g, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_BODY_OVERLAP", "g", "b"), grpCodes(errs))
	assert.Equal(t, workflow.BodyOwner{GroupNodeID: "g", TaskKey: "a"}, g.Owner["shared"], "the first task in author order keeps the node")
}

func TestAnalyzeGroups_BodyReentersItsOwnGroupNode(t *testing.T) {
	wf := grpBasic()
	wf.Edges = append(wf.Edges, grpE("a2", "g"))
	_, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_BODY_OVERLAP", "g", "a"), grpCodes(errs))
	assert.True(t, grpHas(errs, "GROUP_ON_CYCLE", "g", ""), grpCodes(errs))
}

func TestAnalyzeGroups_EntryIsTheWorkflowEntrypoint(t *testing.T) {
	wf := grpBasic()
	wf.Nodes[1].Config.Group.Tasks[0].Entry = "start"
	wf.Edges = append(wf.Edges, grpTaskE("g", "start"))
	_, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_BODY_OVERLAP", "g", "a"), grpCodes(errs))
}

func TestAnalyzeGroups_EntryIsTheGroupItself(t *testing.T) {
	wf := grpBasic()
	wf.Nodes[1].Config.Group.Tasks[0].Entry = "g"
	wf.Edges = append(wf.Edges, grpTaskE("g", "g"))
	_, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_BODY_OVERLAP", "g", "a"), grpCodes(errs))
}

func TestAnalyzeGroups_TwoGroupsShareAnEntry(t *testing.T) {
	wf := grpBasic()
	wf.Nodes = append(wf.Nodes, grpGroup("h", grpBranches([2]string{"c", "a1"})), grpT("h_ok"), grpT("h_fail"))
	wf.Edges = append(wf.Edges, grpE("done", "h"), grpTaskE("h", "a1"), grpOK("h", "h_ok"), grpFail("h", "h_fail"))
	_, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_BODY_OVERLAP", "h", "c"), grpCodes(errs))
}

func TestAnalyzeGroups_GroupOnCycle(t *testing.T) {
	wf := grpBasic()
	wf.Edges = append(wf.Edges, grpE("done", "start")) // start -> g -> done -> start
	_, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_ON_CYCLE", "g", ""), grpCodes(errs))
}

// ---------- malformed input never panics ----------

func TestAnalyzeGroups_NilAndEmpty(t *testing.T) {
	g, errs := workflow.AnalyzeGroups(nil)
	require.NotNil(t, g)
	assert.Empty(t, errs)
	assert.NotNil(t, g.Groups)
	assert.NotNil(t, g.Owner)

	g, errs = workflow.AnalyzeGroups(&workflow.Workflow{})
	require.NotNil(t, g)
	assert.Empty(t, errs)
}

func TestAnalyzeGroups_MissingConfigStillChecksPorts(t *testing.T) {
	wf := grpBasic()
	wf.Nodes[1].Config.Group = nil
	wf.Edges = grpDropEdge(wf.Edges, "g", workflow.GroupPortFailure)
	var g *workflow.GroupGraph
	var errs []workflow.ValidationError
	require.NotPanics(t, func() { g, errs = workflow.AnalyzeGroups(wf) })
	assert.True(t, grpHas(errs, "GROUP_MISSING_PORT", "g", ""), grpCodes(errs))
	assert.Empty(t, g.Groups["g"].Bodies)
}

func TestAnalyzeGroups_MalformedConfigDoesNotPanic(t *testing.T) {
	wf := grpBasic()
	cfg := wf.Nodes[1].Config.Group
	cfg.Tasks = append(cfg.Tasks, workflow.GroupTask{Key: "a", Entry: "b1"}, workflow.GroupTask{Key: "", Entry: ""})
	cfg.Form = "branches"
	cfg.GatherMode = ""
	wf.Nodes = append(wf.Nodes, grpGroup("g", nil)) // duplicate id, nil config
	wf.Edges = append(wf.Edges, grpE("ghost", "a1"), grpE("a1", "nowhere"))
	require.NotPanics(t, func() { workflow.AnalyzeGroups(wf) })
}

func TestAnalyzeGroups_NoEntrypointDoesNotPanic(t *testing.T) {
	wf := grpBasic()
	wf.Nodes[0].Entrypoint = false
	var g *workflow.GroupGraph
	require.NotPanics(t, func() { g, _ = workflow.AnalyzeGroups(wf) })
	assert.Contains(t, g.Groups, "g")
}
```

- [x] Step 2: Run it and confirm it fails to compile.

```bash
go test ./domain/workflow/ -run 'TestAnalyzeGroups|TestHasGroups' -v
```
Expected:
```
# github.com/arbitron/arbitron/domain/workflow_test [github.com/arbitron/arbitron/domain/workflow.test]
domain/workflow/group_ownership_test.go:110:27: undefined: workflow.HasGroups
domain/workflow/group_ownership_test.go:111:27: undefined: workflow.HasGroups
domain/workflow/group_ownership_test.go:112:26: undefined: workflow.HasGroups
domain/workflow/group_ownership_test.go:118:22: undefined: workflow.AnalyzeGroups
domain/workflow/group_ownership_test.go:130:27: undefined: workflow.BodyOwner
...
FAIL	github.com/arbitron/arbitron/domain/workflow [build failed]
```

- [x] Step 3: Minimal implementation. Create `domain/workflow/group_ownership.go`:

```go
package workflow

import (
	"sort"
	"strconv"
	"strings"
)

// BodyOwner identifies one task body: the (group node, task key) pair.
type BodyOwner struct {
	GroupNodeID string `json:"groupNodeId"`
	TaskKey     string `json:"taskKey"`
}

// GroupScope is the analysed structure of one parallel_group node.
type GroupScope struct {
	NodeID string      `json:"nodeId"`
	Config GroupConfig `json:"config"`
	// Parent is the task body that contains this group node; nil when the
	// group sits on the main path (or is unreachable from the entrypoint).
	Parent *BodyOwner `json:"parent,omitempty"`
	// Bodies maps task key -> the node ids exclusively owned by that task's
	// body, in wf.Nodes order (branches form only; empty for workflows form).
	Bodies map[string][]string `json:"bodies"`
	// Nested lists the group node ids whose Parent.GroupNodeID == NodeID, in
	// wf.Nodes order.
	Nested        []string `json:"nested"`
	SuccessTarget string   `json:"successTarget"`
	FailureTarget string   `json:"failureTarget"`
}

// GroupGraph is the ownership analysis of every parallel_group in a workflow.
// It is the ONE structural model shared by validation, the MCP group view, the
// Phase 2 branch launcher and the Phase 4 layout.
type GroupGraph struct {
	Groups map[string]*GroupScope `json:"groups"`
	// Owner maps a body node id to its IMMEDIATE owning (group, task). Nested
	// group nodes are owned by their enclosing task. Main-path nodes are absent.
	Owner map[string]BodyOwner `json:"owner"`
	// Order lists every group node id in wf.Nodes order.
	Order []string `json:"order"`
}

// HasGroups reports whether wf contains any parallel_group node.
func HasGroups(wf *Workflow) bool {
	if wf == nil {
		return false
	}
	for _, n := range wf.Nodes {
		if n.Type == NodeTypeParallelGroup {
			return true
		}
	}
	return false
}

// AnalyzeGroups computes the exclusive ownership of every branch body and
// checks the group graph rules: ports, task edges, body ownership and the
// no-group-on-a-cycle rule. It reports ONLY graph findings; config findings
// (ParseGroupConfig sentinels) are mapped by Validate. It never panics on a
// malformed graph and always returns a non-nil graph.
//
// # Ownership algorithm
//
// The workflow is partitioned into REGIONS: the main path, and one body per
// branches-form task. A region grows by BFS from its seed (the entrypoint for
// the main path, the task's entry for a body) along CONTINUATION edges only:
// every edge of an ordinary node, and the success/failure edges of a group
// node. A nested group is atomic — its task edges are never followed while
// growing the region that contains it; its bodies become regions of their own.
//
// Regions are grown in waves: the main path first, then the bodies of groups
// on the main path, then the bodies of groups inside those bodies, and so on.
// Growth STOPS (recording a "crossing" edge) at a node already claimed by an
// earlier wave and at the entry of any other body. Regions within one wave
// grow independently; a node reached by two of them is an overlap.
//
// Why not define a body as "everything reachable from its entry"? A body edge
// back to the group's success target (or anywhere after it) would make the
// main-path continuation reachable from the entry, so naive reachability would
// silently absorb the escaped main-path node — and everything after it — into
// the body, and the launcher would run it inside the task. Growing the
// enclosing region FIRST and stopping at its claims keeps the continuation on
// the main path and turns the stray edge into a GROUP_BODY_ESCAPE finding.
//
// Crossings are classified once the region tree is known:
//   - into an enclosing group node (the body loops back into its own group or
//     an ancestor group): GROUP_BODY_OVERLAP;
//   - from a body to a region that is not inside it (the enclosing path, a
//     continuation, a sibling or an unrelated body): GROUP_BODY_ESCAPE;
//   - otherwise (from the main path, or from a body into a body nested inside
//     it, bypassing the owning group's task edge): GROUP_BODY_EXTERNAL_EDGE.
func AnalyzeGroups(wf *Workflow) (*GroupGraph, []ValidationError) {
	graph := &GroupGraph{
		Groups: map[string]*GroupScope{},
		Owner:  map[string]BodyOwner{},
		Order:  []string{},
	}
	if wf == nil {
		return graph, nil
	}
	a := newGroupAnalysis(wf, graph)
	a.collectGroups()
	if len(graph.Order) == 0 {
		return graph, nil
	}
	a.checkPortsAndTaskEdges()
	a.buildAnchors()
	a.growRegions()
	a.classifyCrossings()
	a.linkNested()
	a.checkCycles()
	return graph, a.errs
}

// regionID names one region: the main path is the zero value; a body is
// (group node id, task key).
type regionID struct {
	group string
	task  string
}

func (r regionID) isMain() bool { return r.group == "" }

type flowEdge struct {
	target string
	idx    int
}

type crossing struct {
	from    regionID
	src     string
	dst     string
	edgeIdx int
}

type groupAnalysis struct {
	wf    *Workflow
	graph *GroupGraph

	nodeIndex map[string]int         // node id -> index of its FIRST occurrence in wf.Nodes
	flow      map[string][]flowEdge  // continuation adjacency (task edges of groups excluded)
	tasks     map[string][]GroupTask // group id -> analysable branch tasks
	anchors   map[string]regionID    // body entry node id -> the body it seeds
	skipBody  map[regionID]bool      // bodies that must not be grown (bad entry)
	claimed   map[string]regionID    // node id -> owning region
	parentOf  map[regionID]regionID  // body -> region containing its group node
	crossings []crossing
	errs      []ValidationError
}

func newGroupAnalysis(wf *Workflow, graph *GroupGraph) *groupAnalysis {
	a := &groupAnalysis{
		wf:        wf,
		graph:     graph,
		nodeIndex: make(map[string]int, len(wf.Nodes)),
		flow:      map[string][]flowEdge{},
		tasks:     map[string][]GroupTask{},
		anchors:   map[string]regionID{},
		skipBody:  map[regionID]bool{},
		claimed:   map[string]regionID{},
		parentOf:  map[regionID]regionID{},
	}
	for i, n := range wf.Nodes {
		if _, dup := a.nodeIndex[n.ID]; !dup {
			a.nodeIndex[n.ID] = i
		}
	}
	for i, e := range wf.Edges {
		if _, ok := a.nodeIndex[e.Source]; !ok {
			continue
		}
		if _, ok := a.nodeIndex[e.Target]; !ok {
			continue
		}
		if a.isGroup(e.Source) && e.Output != GroupPortSuccess && e.Output != GroupPortFailure {
			continue // task edges (and invalid extra ports) never extend a region
		}
		a.flow[e.Source] = append(a.flow[e.Source], flowEdge{target: e.Target, idx: i})
	}
	return a
}

func (a *groupAnalysis) isGroup(id string) bool {
	i, ok := a.nodeIndex[id]
	return ok && a.wf.Nodes[i].Type == NodeTypeParallelGroup
}

// analysisConfig returns the config to analyse: the parsed config when valid,
// otherwise a deep copy of the raw config so a partially invalid group is
// still analysed (its config problems are reported separately by Validate).
func analysisConfig(n Node) GroupConfig {
	if cfg, err := ParseGroupConfig(n.Config); err == nil {
		return cfg
	}
	if n.Config.Group == nil {
		return GroupConfig{}
	}
	return cloneGroupConfig(*n.Config.Group)
}

// collectGroups creates one scope per group node (first occurrence of an id)
// and selects its analysable branch tasks: nonempty key and entry, first
// occurrence of each key and entry, entry naming an existing node.
func (a *groupAnalysis) collectGroups() {
	for i, n := range a.wf.Nodes {
		if n.Type != NodeTypeParallelGroup || a.nodeIndex[n.ID] != i {
			continue
		}
		scope := &GroupScope{
			NodeID: n.ID,
			Config: analysisConfig(n),
			Bodies: map[string][]string{},
			Nested: []string{},
		}
		a.graph.Groups[n.ID] = scope
		a.graph.Order = append(a.graph.Order, n.ID)

		if scope.Config.Form != GroupFormBranches {
			continue
		}
		seenKey := map[string]bool{}
		seenEntry := map[string]bool{}
		for _, t := range scope.Config.Tasks {
			if t.Key == "" || t.Entry == "" || seenKey[t.Key] || seenEntry[t.Entry] {
				continue
			}
			seenKey[t.Key] = true
			seenEntry[t.Entry] = true
			if _, exists := a.nodeIndex[t.Entry]; !exists {
				a.errs = append(a.errs, ValidationError{
					Code: "GROUP_UNKNOWN_ENTRY",
					Message: "parallel_group " + n.ID + " task " + strconv.Quote(t.Key) + " names entry " +
						strconv.Quote(t.Entry) + ", which is not a node in this workflow; add the node or fix the entry",
					NodeID:  n.ID,
					TaskKey: t.Key,
				})
				continue
			}
			a.tasks[n.ID] = append(a.tasks[n.ID], t)
		}
	}
}

// checkPortsAndTaskEdges enforces: exactly one success and one failure edge;
// no other outgoing port; for branches form exactly one "task" edge per task,
// matched by its unique entry target; for workflows form no task edges.
func (a *groupAnalysis) checkPortsAndTaskEdges() {
	for _, id := range a.graph.Order {
		scope := a.graph.Groups[id]
		var success, failure []int
		taskEdges := map[string][]int{} // target -> edge indexes
		var taskOrder []string
		for i, e := range a.wf.Edges {
			if e.Source != id {
				continue
			}
			switch e.Output {
			case GroupPortSuccess:
				success = append(success, i)
			case GroupPortFailure:
				failure = append(failure, i)
			case GroupPortTask:
				if _, seen := taskEdges[e.Target]; !seen {
					taskOrder = append(taskOrder, e.Target)
				}
				taskEdges[e.Target] = append(taskEdges[e.Target], i)
			default:
				a.errs = append(a.errs, ValidationError{
					Code: "GROUP_EXTRA_PORT",
					Message: "parallel_group " + id + " has an edge to " + e.Target + " on port " + strconv.Quote(e.Output) +
						"; a group only has \"task\", \"success\" and \"failure\" ports",
					NodeID:  id,
					EdgeIdx: i,
				})
			}
		}
		for _, p := range []struct {
			port  string
			edges []int
		}{{GroupPortSuccess, success}, {GroupPortFailure, failure}} {
			switch len(p.edges) {
			case 0:
				a.errs = append(a.errs, ValidationError{
					Code:    "GROUP_MISSING_PORT",
					Message: "parallel_group " + id + " is missing its " + strconv.Quote(p.port) + " edge; connect it to the node that runs when the group ends in " + p.port,
					NodeID:  id,
				})
			case 1:
			default:
				a.errs = append(a.errs, ValidationError{
					Code:    "GROUP_DUPLICATE_PORT",
					Message: "parallel_group " + id + " has " + strconv.Itoa(len(p.edges)) + " " + strconv.Quote(p.port) + " edges; keep exactly one",
					NodeID:  id,
					EdgeIdx: p.edges[1],
				})
			}
		}
		if len(success) > 0 {
			scope.SuccessTarget = a.wf.Edges[success[0]].Target
		}
		if len(failure) > 0 {
			scope.FailureTarget = a.wf.Edges[failure[0]].Target
		}

		switch scope.Config.Form {
		case GroupFormWorkflows:
			for _, tgt := range taskOrder {
				a.errs = append(a.errs, ValidationError{
					Code:    "GROUP_TASK_EDGE_MISMATCH",
					Message: "parallel_group " + id + " (workflows form) has a \"task\" edge to " + tgt + "; workflow tasks run child workflows and take no task edges — remove it",
					NodeID:  id,
					EdgeIdx: taskEdges[tgt][0],
				})
			}
		case GroupFormBranches:
			declared := map[string]bool{}
			for _, t := range scope.Config.Tasks {
				if t.Entry != "" {
					declared[t.Entry] = true
				}
			}
			for _, t := range a.tasks[id] {
				switch n := len(taskEdges[t.Entry]); n {
				case 0:
					a.errs = append(a.errs, ValidationError{
						Code: "GROUP_TASK_EDGE_MISMATCH",
						Message: "parallel_group " + id + " task " + strconv.Quote(t.Key) + " has no \"task\" edge to its entry " +
							t.Entry + "; add {source: " + id + ", target: " + t.Entry + ", output: \"task\"}",
						NodeID:  id,
						TaskKey: t.Key,
					})
				case 1:
				default:
					a.errs = append(a.errs, ValidationError{
						Code:    "GROUP_TASK_EDGE_MISMATCH",
						Message: "parallel_group " + id + " task " + strconv.Quote(t.Key) + " has " + strconv.Itoa(n) + " \"task\" edges to " + t.Entry + "; keep exactly one",
						NodeID:  id,
						TaskKey: t.Key,
						EdgeIdx: taskEdges[t.Entry][1],
					})
				}
			}
			for _, tgt := range taskOrder {
				if declared[tgt] {
					continue
				}
				a.errs = append(a.errs, ValidationError{
					Code:    "GROUP_TASK_EDGE_MISMATCH",
					Message: "parallel_group " + id + " has a \"task\" edge to " + tgt + ", which is not the entry of any of its tasks; remove the edge or add a task with entry " + tgt,
					NodeID:  id,
					EdgeIdx: taskEdges[tgt][0],
				})
			}
		}
	}
}

// entrypoint returns the first entrypoint node id, or "".
func (a *groupAnalysis) entrypoint() string {
	for _, n := range a.wf.Nodes {
		if n.Entrypoint {
			return n.ID
		}
	}
	return ""
}

// buildAnchors registers every analysable task entry as the seed of its body.
// An entry that is the workflow entrypoint, the task's own group node, or the
// entry of an earlier task of ANOTHER group cannot seed an exclusive body.
func (a *groupAnalysis) buildAnchors() {
	entry := a.entrypoint()
	for _, id := range a.graph.Order {
		for _, t := range a.tasks[id] {
			r := regionID{group: id, task: t.Key}
			var problem string
			switch {
			case t.Entry == id:
				problem = "is the group node itself; a body must start at a node inside the task"
			case t.Entry == entry:
				problem = "is the workflow entrypoint, which belongs to the main path"
			default:
				if other, taken := a.anchors[t.Entry]; taken {
					problem = "is already the entry of task " + strconv.Quote(other.task) + " of parallel_group " + other.group
				}
			}
			if problem != "" {
				a.skipBody[r] = true
				a.errs = append(a.errs, ValidationError{
					Code:    "GROUP_BODY_OVERLAP",
					Message: "parallel_group " + id + " task " + strconv.Quote(t.Key) + " entry " + t.Entry + " " + problem,
					NodeID:  id,
					TaskKey: t.Key,
				})
				continue
			}
			a.anchors[t.Entry] = r
		}
	}
}

type pendingRegion struct {
	id   regionID
	seed string
}

// growRegions runs the wave algorithm described on AnalyzeGroups.
func (a *groupAnalysis) growRegions() {
	entry := a.entrypoint()
	if entry == "" {
		return
	}
	wave := []pendingRegion{{id: regionID{}, seed: entry}}
	for len(wave) > 0 {
		members := make([]map[string]bool, len(wave))
		for i, pr := range wave {
			members[i] = a.grow(pr.id, pr.seed)
		}

		var placedGroups []string
		placedIn := map[string]regionID{}
		for i, pr := range wave {
			var shared []string
			var sharedWith regionID
			for _, id := range a.inNodeOrder(members[i]) {
				if other, taken := a.claimed[id]; taken {
					if len(shared) == 0 {
						sharedWith = other
					}
					shared = append(shared, id)
					continue
				}
				a.claimed[id] = pr.id
				if !pr.id.isMain() {
					scope := a.graph.Groups[pr.id.group]
					scope.Bodies[pr.id.task] = append(scope.Bodies[pr.id.task], id)
					a.graph.Owner[id] = BodyOwner{GroupNodeID: pr.id.group, TaskKey: pr.id.task}
				}
				if _, isScope := a.graph.Groups[id]; isScope {
					placedGroups = append(placedGroups, id)
					placedIn[id] = pr.id
				}
			}
			if len(shared) > 0 {
				a.errs = append(a.errs, ValidationError{
					Code: "GROUP_BODY_OVERLAP",
					Message: "parallel_group " + pr.id.group + " task " + strconv.Quote(pr.id.task) + " shares node(s) " +
						strings.Join(shared, ", ") + " with " + a.describe(sharedWith) +
						"; each node may belong to exactly one task body — give each task its own nodes",
					NodeID:  pr.id.group,
					TaskKey: pr.id.task,
				})
			}
		}

		var next []pendingRegion
		for _, gid := range placedGroups {
			parent := placedIn[gid]
			if !parent.isMain() {
				a.graph.Groups[gid].Parent = &BodyOwner{GroupNodeID: parent.group, TaskKey: parent.task}
			}
			for _, t := range a.tasks[gid] {
				r := regionID{group: gid, task: t.Key}
				if a.skipBody[r] {
					continue
				}
				a.parentOf[r] = parent
				if _, ok := a.graph.Groups[gid].Bodies[t.Key]; !ok {
					a.graph.Groups[gid].Bodies[t.Key] = []string{}
				}
				if owner, taken := a.claimed[t.Entry]; taken {
					a.errs = append(a.errs, ValidationError{
						Code:    "GROUP_BODY_OVERLAP",
						Message: "parallel_group " + gid + " task " + strconv.Quote(t.Key) + " entry " + t.Entry + " already belongs to " + a.describe(owner),
						NodeID:  gid,
						TaskKey: t.Key,
					})
					continue
				}
				next = append(next, pendingRegion{id: r, seed: t.Entry})
			}
		}
		wave = next
	}
}

// grow returns the members of region r grown from seed (see AnalyzeGroups),
// recording every crossing edge where growth stopped.
func (a *groupAnalysis) grow(r regionID, seed string) map[string]bool {
	members := map[string]bool{seed: true}
	queue := []string{seed}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, fe := range a.flow[cur] {
			t := fe.target
			if members[t] {
				continue
			}
			if owner, taken := a.claimed[t]; taken && owner != r {
				a.crossings = append(a.crossings, crossing{from: r, src: cur, dst: t, edgeIdx: fe.idx})
				continue
			}
			if anchor, isEntry := a.anchors[t]; isEntry && anchor != r {
				a.crossings = append(a.crossings, crossing{from: r, src: cur, dst: t, edgeIdx: fe.idx})
				continue
			}
			members[t] = true
			queue = append(queue, t)
		}
	}
	return members
}

// classifyCrossings turns every stopped edge into exactly one finding.
func (a *groupAnalysis) classifyCrossings() {
	sort.SliceStable(a.crossings, func(i, j int) bool { return a.crossings[i].edgeIdx < a.crossings[j].edgeIdx })
	seen := map[int]bool{}
	for _, c := range a.crossings {
		if seen[c.edgeIdx] {
			continue
		}
		seen[c.edgeIdx] = true

		to, known := a.claimed[c.dst]
		if !known {
			anchor, isEntry := a.anchors[c.dst]
			if !isEntry {
				continue
			}
			to = anchor
		}
		edge := c.src + " -> " + c.dst
		switch {
		case a.isEnclosingGroup(c.dst, c.from):
			a.errs = append(a.errs, ValidationError{
				Code: "GROUP_BODY_OVERLAP",
				Message: "edge " + edge + " in the body of parallel_group " + c.from.group + " task " + strconv.Quote(c.from.task) +
					" re-enters enclosing group node " + c.dst + "; a body must end at a sink, never loop back into its group",
				NodeID:  c.from.group,
				TaskKey: c.from.task,
				EdgeIdx: c.edgeIdx,
			})
		case !c.from.isMain() && !a.isInside(to, c.from):
			a.errs = append(a.errs, ValidationError{
				Code: "GROUP_BODY_ESCAPE",
				Message: "edge " + edge + " leaves the body of parallel_group " + c.from.group + " task " + strconv.Quote(c.from.task) +
					" into " + a.describe(to) + "; a body ends at a sink — the group continues through its success/failure edges",
				NodeID:  c.from.group,
				TaskKey: c.from.task,
				EdgeIdx: c.edgeIdx,
			})
		case !to.isMain():
			a.errs = append(a.errs, ValidationError{
				Code: "GROUP_BODY_EXTERNAL_EDGE",
				Message: "edge " + edge + " enters the body of parallel_group " + to.group + " task " + strconv.Quote(to.task) +
					" from " + a.describe(c.from) + "; only the group's own \"task\" edge may enter a body",
				NodeID:  to.group,
				TaskKey: to.task,
				EdgeIdx: c.edgeIdx,
			})
		}
	}
}

// isInside reports whether region r is anc or nested (at any depth) inside it.
func (a *groupAnalysis) isInside(r, anc regionID) bool {
	for steps := 0; steps <= len(a.parentOf)+1; steps++ {
		if r == anc {
			return true
		}
		if r.isMain() {
			return false
		}
		p, ok := a.parentOf[r]
		if !ok {
			return false
		}
		r = p
	}
	return false
}

// isEnclosingGroup reports whether node id is the group owning region r or
// the group owning any region that encloses r.
func (a *groupAnalysis) isEnclosingGroup(id string, r regionID) bool {
	for steps := 0; steps <= len(a.parentOf)+1 && !r.isMain(); steps++ {
		if r.group == id {
			return true
		}
		p, ok := a.parentOf[r]
		if !ok {
			return false
		}
		r = p
	}
	return false
}

func (a *groupAnalysis) describe(r regionID) string {
	if r.isMain() {
		return "the enclosing main path"
	}
	return "the body of parallel_group " + r.group + " task " + strconv.Quote(r.task)
}

// inNodeOrder returns set's ids sorted by their first position in wf.Nodes.
func (a *groupAnalysis) inNodeOrder(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return a.nodeIndex[out[i]] < a.nodeIndex[out[j]] })
	return out
}

// linkNested fills GroupScope.Nested in wf.Nodes order.
func (a *groupAnalysis) linkNested() {
	for _, id := range a.graph.Order {
		if p := a.graph.Groups[id].Parent; p != nil {
			parent := a.graph.Groups[p.GroupNodeID]
			parent.Nested = append(parent.Nested, id)
		}
	}
}

// checkCycles rejects a group on a cycle over ALL edges (task edges included):
// re-entering a group would reuse its persisted group run. Loops that do not
// pass through a group node are unaffected.
func (a *groupAnalysis) checkCycles() {
	adj := buildAdjacency(a.wf)
	for _, id := range a.graph.Order {
		if nodeOnCycle(id, adj) {
			a.errs = append(a.errs, ValidationError{
				Code:    "GROUP_ON_CYCLE",
				Message: "parallel_group " + id + " sits on a cycle; a group runs once per instance, so no path may lead from it back to itself — move the loop inside a task body or before the group",
				NodeID:  id,
			})
		}
	}
}
```

Implementation notes the tests pin:
- `newGroupAnalysis` builds the continuation adjacency once, skipping edges whose source or target is not a node (edge integrity is `validateEdgeIntegrity`'s finding) and every group edge that is not `success`/`failure` — task edges are only ever traversed by the launcher.
- Claims are applied in `wf.Nodes` order AFTER a whole wave has grown, so siblings never see each other's claims while growing — that is what turns a shared node into an overlap rather than an order-dependent escape.
- A body's `Bodies[key]` entry is created (possibly empty) as soon as its group is placed, so a placed group lists every analysable task whose entry was not rejected by `buildAnchors`.
- Crossings are de-duplicated by edge index and classified in edge order, so findings are deterministic.
- `isInside` / `isEnclosingGroup` walk `parentOf` with a step bound, so a malformed parent chain cannot loop.

- [x] Step 4: Run the tests and confirm they pass.

```bash
gofmt -l domain/workflow/
go test ./domain/workflow/ -run 'TestAnalyzeGroups|TestHasGroups' -v
```
Expected (`gofmt -l` prints nothing):
```
--- PASS: TestHasGroups (0.00s)
--- PASS: TestAnalyzeGroups_BranchesHappyPath (0.00s)
--- PASS: TestAnalyzeGroups_WorkflowsFormHasNoBodies (0.00s)
--- PASS: TestAnalyzeGroups_EntrypointGroupIsValid (0.00s)
--- PASS: TestAnalyzeGroups_OneTaskSinkBody (0.00s)
--- PASS: TestAnalyzeGroups_UniqueEntriesWithIdenticalTaskPortNames (0.00s)
--- PASS: TestAnalyzeGroups_OrdinaryLoopsElsewhereStayValid (0.00s)
--- PASS: TestAnalyzeGroups_DeepNestingBothForms (0.00s)
--- PASS: TestAnalyzeGroups_EdgeIntoNestedBodyFromEnclosingBody (0.00s)
--- PASS: TestAnalyzeGroups_NestedBodyEscapesToEnclosingContinuation (0.00s)
--- PASS: TestAnalyzeGroups_NoTaskEdgesIsReportedPerTask (0.00s)
--- PASS: TestAnalyzeGroups_TaskEdgeToUndeclaredEntry (0.00s)
--- PASS: TestAnalyzeGroups_DuplicateTaskEdgeForOneTask (0.00s)
--- PASS: TestAnalyzeGroups_WorkflowsFormRejectsTaskEdges (0.00s)
--- PASS: TestAnalyzeGroups_UnknownEntry (0.00s)
--- PASS: TestAnalyzeGroups_PortRules (0.00s)
    --- PASS: TestAnalyzeGroups_PortRules/missing_success (0.00s)
    --- PASS: TestAnalyzeGroups_PortRules/missing_failure (0.00s)
    --- PASS: TestAnalyzeGroups_PortRules/duplicate_failure (0.00s)
    --- PASS: TestAnalyzeGroups_PortRules/unnamed_edge (0.00s)
    --- PASS: TestAnalyzeGroups_PortRules/unknown_port (0.00s)
--- PASS: TestAnalyzeGroups_ExternalIncomingEdgeIntoBodyEntry (0.00s)
--- PASS: TestAnalyzeGroups_EscapedContinuationIsNotAbsorbed (0.00s)
--- PASS: TestAnalyzeGroups_EscapeDeeperIntoMainPath (0.00s)
--- PASS: TestAnalyzeGroups_EscapeToSiblingEntry (0.00s)
--- PASS: TestAnalyzeGroups_SiblingOverlap (0.00s)
--- PASS: TestAnalyzeGroups_BodyReentersItsOwnGroupNode (0.00s)
--- PASS: TestAnalyzeGroups_EntryIsTheWorkflowEntrypoint (0.00s)
--- PASS: TestAnalyzeGroups_EntryIsTheGroupItself (0.00s)
--- PASS: TestAnalyzeGroups_TwoGroupsShareAnEntry (0.00s)
--- PASS: TestAnalyzeGroups_GroupOnCycle (0.00s)
--- PASS: TestAnalyzeGroups_NilAndEmpty (0.00s)
--- PASS: TestAnalyzeGroups_MissingConfigStillChecksPorts (0.00s)
--- PASS: TestAnalyzeGroups_MalformedConfigDoesNotPanic (0.00s)
--- PASS: TestAnalyzeGroups_NoEntrypointDoesNotPanic (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/workflow	(cached)
```

- [x] Step 5: Confirm the package, vet and the module build.

```bash
go vet ./domain/workflow/
go test ./domain/workflow/
go build ./...
```
Expected: `go vet` prints nothing; `ok  	github.com/arbitron/arbitron/domain/workflow`; the build succeeds.

- [x] Step 6: Commit.

```bash
git add domain/workflow/group_ownership.go domain/workflow/group_ownership_test.go && git commit -m "feat(workflow): parallel_group ownership analysis (AnalyzeGroups)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Execution and self-review

- Red: the supplied tests failed to compile on the missing analysis API before implementation.
- Spec compliance: added a red/green regression for two tasks in the same group sharing an entry (rule 5). Unlike the sample implementation, collection retains distinct-key tasks with shared entries so `buildAnchors` reports overlap on the later task; the first task keeps ownership. Added assertions for non-nil result containers and deep-copied parsed/raw configs.
- Anti-green-theater: temporarily disabled the stop-at-earlier-claims guard. All three escaped-continuation probes failed on the required `GROUP_BODY_ESCAPE` finding; restored the guard before final verification. Happy-path and nested tests assert the resulting ownership maps and bodies, not intermediate calls. Production consumers are assigned to P1-T3 and subsequent service/runtime/layout tasks in the provenance section above; this task adds only the pure analysis API.
- Code quality: reviewed against neighboring workflow code; reused the existing parser/cloner and unchanged cycle helpers, kept deterministic manifest/edge ordering, and formatted both new files.
- Final verification: focused tests (`-count=1 -v`), `go vet ./domain/workflow/`, package tests (`-count=1`), `go build ./...`, `gofmt -l domain/workflow/`, and `git diff --check` all passed.
