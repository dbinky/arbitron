# Parallel Workflow Gathering — Phase 1 Task 6: Group operations (UpsertGroup / GetGroup / ListGroups / DeleteGroup)

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Give authors focused, atomic group operations on the workflow service: upsert one `parallel_group` node with all its settings, task order/inputs and continuation targets; inspect one or all groups with computed bodies, nesting and advisories; delete a group and learn which nodes it leaves behind — all behind the existing `Load` visibility and `Update` authorization, with group validation rejecting bad results before anything is persisted.
**Architecture:** One new file `domain/workflow/group_ops.go`. Mutations are load → deep-clone → mutate in memory → `Service.Update` (which runs P1-T5's `rejectInvalidGroups` gate and the existing private-first / admin-shared-in-place `adminSharedWrite` routing), so a rejected mutation persists nothing. Reads are `Service.Load` + `AnalyzeGroups` (P1-T2) + `ValidateGroups` (P1-T5) and never write. Upsert rewrites the group node and ALL its outgoing edges in one replacement (task edges in author order, then `success`, then `failure`), inserted where the old outgoing edges were.
**Tech Stack:** Go 1.25.5, standard library (`context`, `encoding/json`, `errors`, `fmt`), testify.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` (MCP authoring: "create, inspect, and revise the complete feature through MCP ... Task order ... Completion rules and unfinished-task behavior ... Optional file gathering"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-1.md` §3 "Service operations and MCP" (upsert/get/delete behind `Service.Load`, `Service.Update`, `adminSharedWrite`; get is read-only; delete lists retained body nodes and rejects a deletion leaving another group invalid) and Data Provenance row "Typed group config, exclusive body ownership map, structured group view" (**P1-A**, consumed here).

**Interfaces**
- Consumes:
  - `domain/workflow/service.go:356` `func (s *Service) Load(ctx context.Context, name string) (*Workflow, error)` — caller-visible (private, then shared via `auditingFilter`); another user's private workflow ⇒ `ErrNotFound`; no identity ⇒ `identity.ErrUnauthenticated`.
  - `domain/workflow/service.go:271` `func (s *Service) Update(ctx context.Context, name string, wf *Workflow) error` — after P1-T5: group gate, then private write, then `adminSharedWrite` (non-admin on shared-only ⇒ `ErrSharedReadOnly`; admin ⇒ shared edit in place).
  - P1-T5 (`domain/workflow/service_group.go`): `func (s *Service) ValidateGroups(ctx context.Context, wf *Workflow) []ValidationError`; `type GroupValidationError struct{ Errors []ValidationError }`; `var ErrGroupInvalid`; `func (s *Service) SetGroupCheckers(ProfileChecker, OwnerFileChecker)`.
  - P1-T2: `func AnalyzeGroups(wf *Workflow) (*GroupGraph, []ValidationError)` (non-nil graph; `Groups[nodeID] *GroupScope` with `Config`, `Parent *BodyOwner`, `Bodies map[string][]string`, `Nested []string`, `SuccessTarget`, `FailureTarget`); `type BodyOwner struct{ GroupNodeID, TaskKey string }`.
  - P1-T1: `NodeTypeParallelGroup`, `GroupConfig`, `GroupTask`, `NodeConfig.Group`, `GroupFormBranches`, `GroupPortTask`, `GroupPortSuccess`, `GroupPortFailure`.
- Produces (exact, `domain/workflow/group_ops.go`):
  - `type GroupUpsert struct { NodeID string "json:\"nodeId\""; Label string "json:\"label,omitempty\""; Description string "json:\"description,omitempty\""; Entrypoint *bool "json:\"entrypoint,omitempty\""; Config GroupConfig "json:\"config\""; SuccessTarget string "json:\"successTarget\""; FailureTarget string "json:\"failureTarget\"" }`
  - `type GroupView struct { NodeID; Label; Entrypoint bool; Config GroupConfig; SuccessTarget; FailureTarget string; Bodies map[string][]string; Nested []string; Parent *BodyOwner; Advisories []ValidationError }` (JSON tags exactly as the contract)
  - `type GroupDeleteResult struct { NodeID string "json:\"nodeId\""; RetainedNodes []string "json:\"retainedNodes\"" }`
  - `func (s *Service) UpsertGroup(ctx context.Context, workflowName string, up GroupUpsert) (*GroupView, error)`
  - `func (s *Service) GetGroup(ctx context.Context, workflowName, nodeID string) (*GroupView, error)`
  - `func (s *Service) ListGroups(ctx context.Context, workflowName string) ([]GroupView, error)`
  - `func (s *Service) DeleteGroup(ctx context.Context, workflowName, nodeID string) (*GroupDeleteResult, error)`
  - `var ErrGroupNotFound = errors.New("parallel group not found")`
  - **Addition (backward-compatible, not in the contract):** `var ErrGroupUpsertInvalid = errors.New("invalid parallel group upsert")` — returned (wrapped, with an actionable message) for upsert ARGUMENT problems that Validate would only report with non-group codes and therefore would not gate: empty `nodeId`, `nodeId` already used by a non-group node, empty/unknown/self `successTarget` or `failureTarget`. P1-T7 maps it to a plain tool error.
  - Semantics later tasks rely on: `Bodies` is always a non-nil map and `Nested` a non-nil slice; `ListGroups` returns a non-nil slice in `wf.Nodes` order (= `GroupGraph.Order`); `Advisories` holds every group finding whose `NodeID` is the group (errors and warnings); `Label`/`Description` empty in an upsert keep the existing values; `Entrypoint == nil` keeps the existing flag (false for a new node) and `true` clears the flag on every other node; `RetainedNodes` lists former body nodes in task order, then the success and failure targets, de-duplicated, never the deleted node, always non-nil.

**Wiring & data provenance**
- Composition root: nothing new to wire here — the operations are methods on the `*workflow.Service` already built at `cmd/arbitron/main.go:510` and injected into MCP (`Deps.WorkflowSvc`, main.go:291) and HTTP (`RouterDeps.WorkflowSvc`, main.go:1270). They only become reachable to authors via the MCP tools registered in P1-T7, and they only pass group validation once P1-T8 calls `workflowSvc.SetGroupCheckers(profileChecker, fileReader)`.
- Producers:
  - `up` (all group settings, task keys/order/inputs, continuation targets) ⇐ P1-T7 `adapter/mcp/tools_workflow_group.go:workflowGroupUpsertHandler`, decoded from the author's `group` JSON argument.
  - The existing manifest (body nodes, incoming edges, other nodes) ⇐ `Service.Load` → `Repository.Load` (`adapter/filesystem/workflow_repo.go:WorkflowRepo.Load` in production).
  - Caller identity / mutation scope ⇐ `caller(ctx)` inside `Load`/`Update` (`domain/workflow/service.go:158`).
  - Bodies / nesting / parent / continuation targets ⇐ `AnalyzeGroups` (P1-T2).
  - Advisories and the write gate ⇐ `ValidateGroups` / `rejectInvalidGroups` (P1-T5), whose checkers come from `SetGroupCheckers` (P1-T8).
- Provenance probes: `TestGroupOps_Upsert_RoutesThroughUpdateValidation` fails if `UpsertGroup` bypasses `Update` (with no checkers wired it must be rejected with `GROUP_CHECKER_UNAVAILABLE` and write nothing); `TestGroupOps_Upsert_PreservesStoredManifest` fails if the upsert does not start from the persisted manifest.

### Task 6: Group operations on workflow.Service

**Files:**
- Create: `domain/workflow/group_ops.go`
- Test: `domain/workflow/group_ops_test.go` (package `workflow_test`; reuses the P1-T5 test helpers from `domain/workflow/service_group_test.go`: `sgRepo`/`newSGFixture`/`sgFixture`, `sgTransform`, `sgChild`, `sgTopic`, `sgCodes`, `requireGroupErr`, `sgAlice`, `sgBob`; and the existing `ctxWith` from `domain/workflow/service_test.go:203`)

(No existing file is modified.)

- [x] Step 1: Write the failing tests. Create `domain/workflow/group_ops_test.go`:

```go
package workflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func opsBool(b bool) *bool { return &b }

// opsBase is a group-free manifest holding the nodes the tests wire groups to.
// It has NO entrypoint: Create/Update never validated non-group workflows, and
// the first upsert makes the group the entrypoint.
func opsBase(name string) *workflow.Workflow {
	return &workflow.Workflow{
		Name: name, Description: "base", Author: "t",
		Nodes: []workflow.Node{
			sgTransform("a1"), sgTransform("b1"), sgTransform("c1"),
			sgTransform("done"), sgTransform("failed"),
			sgTransform("alt-done"), sgTransform("alt-failed"),
		},
	}
}

func opsBranchesCfg(tasks ...workflow.GroupTask) workflow.GroupConfig {
	return workflow.GroupConfig{
		Form:               workflow.GroupFormBranches,
		Tasks:              tasks,
		CompletionRule:     workflow.GroupRuleAllSucceed,
		UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining,
		GatherMode:         workflow.GroupGatherResults,
	}
}

func opsNode(t *testing.T, wf *workflow.Workflow, id string) workflow.Node {
	t.Helper()
	require.NotNil(t, wf)
	for _, n := range wf.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("node %q not found", id)
	return workflow.Node{}
}

func opsHasNode(wf *workflow.Workflow, id string) bool {
	for _, n := range wf.Nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

func opsOutgoing(wf *workflow.Workflow, id string) []workflow.Edge {
	var out []workflow.Edge
	for _, e := range wf.Edges {
		if e.Source == id {
			out = append(out, e)
		}
	}
	return out
}

func opsTaskKeys(cfg workflow.GroupConfig) []string {
	out := make([]string, 0, len(cfg.Tasks))
	for _, tk := range cfg.Tasks {
		out = append(out, tk.Key)
	}
	return out
}

// opsSeeded returns a wired fixture with opsBase("wf") in alice's private scope.
func opsSeeded(t *testing.T) (*sgFixture, context.Context) {
	t.Helper()
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Private(sgAlice), opsBase("wf"))
	return f, ctxWith(sgAlice, false)
}

func opsFanout() workflow.GroupUpsert {
	return workflow.GroupUpsert{
		NodeID:        "fanout",
		Label:         "Fan out",
		Description:   "two branches",
		Entrypoint:    opsBool(true),
		Config:        opsBranchesCfg(workflow.GroupTask{Key: "a", Entry: "a1"}, workflow.GroupTask{Key: "b", Entry: "b1"}),
		SuccessTarget: "done",
		FailureTarget: "failed",
	}
}

// ---------------------------------------------------------------------------
// UpsertGroup — create both forms
// ---------------------------------------------------------------------------

func TestGroupOps_Upsert_CreatesBranchesGroup(t *testing.T) {
	f, ctx := opsSeeded(t)

	view, err := f.svc.UpsertGroup(ctx, "wf", opsFanout())
	require.NoError(t, err)

	assert.Equal(t, "fanout", view.NodeID)
	assert.Equal(t, "Fan out", view.Label)
	assert.True(t, view.Entrypoint)
	assert.Equal(t, []string{"a", "b"}, opsTaskKeys(view.Config))
	assert.Equal(t, workflow.GroupFormBranches, view.Config.Form)
	assert.Equal(t, "done", view.SuccessTarget)
	assert.Equal(t, "failed", view.FailureTarget)
	assert.Equal(t, map[string][]string{"a": {"a1"}, "b": {"b1"}}, view.Bodies)
	assert.NotNil(t, view.Nested)
	assert.Empty(t, view.Nested)
	assert.Nil(t, view.Parent, "a top-level group sits on the main path")

	stored := f.repo.stored(workflow.Private(sgAlice), "wf")
	node := opsNode(t, stored, "fanout")
	assert.Equal(t, workflow.NodeTypeParallelGroup, node.Type)
	assert.True(t, node.Entrypoint)
	assert.Equal(t, "Fan out", node.Label)
	assert.Equal(t, "two branches", node.Description)
	require.NotNil(t, node.Config.Group)
	assert.Equal(t, opsFanout().Config, *node.Config.Group)
	assert.Equal(t, []workflow.Edge{
		{Source: "fanout", Target: "a1", Output: workflow.GroupPortTask, Label: "a"},
		{Source: "fanout", Target: "b1", Output: workflow.GroupPortTask, Label: "b"},
		{Source: "fanout", Target: "done", Output: workflow.GroupPortSuccess},
		{Source: "fanout", Target: "failed", Output: workflow.GroupPortFailure},
	}, opsOutgoing(stored, "fanout"))
	assert.Equal(t, 1, f.repo.updates, "exactly one atomic write")
}

func TestGroupOps_Upsert_CreatesWorkflowsGroupWithRepeatedChildAndDistinctInputs(t *testing.T) {
	f, ctx := opsSeeded(t)
	f.repo.seed(workflow.Private(sgAlice), sgChild("reviewer", sgTopic))

	up := workflow.GroupUpsert{
		NodeID:     "reviews",
		Entrypoint: opsBool(true),
		Config: workflow.GroupConfig{
			Form: workflow.GroupFormWorkflows,
			Tasks: []workflow.GroupTask{
				{Key: "alpha", Workflow: "reviewer", Inputs: map[string]any{"topic": "alpha"}},
				{Key: "beta", Workflow: "reviewer", Inputs: map[string]any{"topic": "beta"}},
			},
			CompletionRule:     workflow.GroupRuleFirstSuccess,
			UnfinishedBehavior: workflow.GroupUnfinishedFinishIndependently,
			GatherMode:         workflow.GroupGatherResults,
		},
		SuccessTarget: "done",
		FailureTarget: "failed",
	}
	view, err := f.svc.UpsertGroup(ctx, "wf", up)
	require.NoError(t, err)

	require.Len(t, view.Config.Tasks, 2)
	assert.Equal(t, "reviewer", view.Config.Tasks[0].Workflow)
	assert.Equal(t, "reviewer", view.Config.Tasks[1].Workflow)
	assert.Equal(t, "alpha", view.Config.Tasks[0].Inputs["topic"])
	assert.Equal(t, "beta", view.Config.Tasks[1].Inputs["topic"])
	assert.Empty(t, view.Bodies, "workflow tasks own no branch bodies")
	assert.NotNil(t, view.Bodies)

	stored := f.repo.stored(workflow.Private(sgAlice), "wf")
	g := opsNode(t, stored, "reviews").Config.Group
	require.NotNil(t, g)
	assert.Equal(t, map[string]any{"topic": "alpha"}, g.Tasks[0].Inputs)
	assert.Equal(t, map[string]any{"topic": "beta"}, g.Tasks[1].Inputs)
	assert.Equal(t, []workflow.Edge{
		{Source: "reviews", Target: "done", Output: workflow.GroupPortSuccess},
		{Source: "reviews", Target: "failed", Output: workflow.GroupPortFailure},
	}, opsOutgoing(stored, "reviews"), "a workflows group has no task edges")
}

func TestGroupOps_Upsert_PreservesAuthorTaskOrder(t *testing.T) {
	f, ctx := opsSeeded(t)
	up := opsFanout()
	up.Config.Tasks = []workflow.GroupTask{{Key: "zeta", Entry: "c1"}, {Key: "alpha", Entry: "a1"}, {Key: "mid", Entry: "b1"}}

	view, err := f.svc.UpsertGroup(ctx, "wf", up)
	require.NoError(t, err)
	assert.Equal(t, []string{"zeta", "alpha", "mid"}, opsTaskKeys(view.Config), "author order, never sorted")

	stored := f.repo.stored(workflow.Private(sgAlice), "wf")
	assert.Equal(t, []string{"zeta", "alpha", "mid"}, opsTaskKeys(*opsNode(t, stored, "fanout").Config.Group))
	out := opsOutgoing(stored, "fanout")
	require.Len(t, out, 5)
	assert.Equal(t, []string{"zeta", "alpha", "mid"}, []string{out[0].Label, out[1].Label, out[2].Label})
	assert.Equal(t, []string{"c1", "a1", "b1"}, []string{out[0].Target, out[1].Target, out[2].Target})

	got, err := f.svc.GetGroup(ctx, "wf", "fanout")
	require.NoError(t, err)
	assert.Equal(t, []string{"zeta", "alpha", "mid"}, opsTaskKeys(got.Config))
}

// ---------------------------------------------------------------------------
// UpsertGroup — revise
// ---------------------------------------------------------------------------

func TestGroupOps_Upsert_RevisesEverySetting(t *testing.T) {
	f, ctx := opsSeeded(t)
	_, err := f.svc.UpsertGroup(ctx, "wf", opsFanout())
	require.NoError(t, err)
	f.files.present[sgFileCall{owner: sgAlice, workflow: "wf", path: "prompts/resolve.md"}] = true

	revised := workflow.GroupUpsert{
		NodeID:      "fanout",
		Label:       "Fan out v2",
		Description: "three-way race",
		Config: workflow.GroupConfig{
			Form:               workflow.GroupFormBranches,
			Tasks:              []workflow.GroupTask{{Key: "c", Entry: "c1"}, {Key: "a", Entry: "a1"}},
			CompletionRule:     workflow.GroupRuleFirstSuccess,
			UnfinishedBehavior: workflow.GroupUnfinishedFinishIndependently,
			GatherMode:         workflow.GroupGatherResultsAndFiles,
			RepoPath:           "repo",
			ResolverProfile:    "agenty",
			ResolverPromptFile: "prompts/resolve.md",
		},
		SuccessTarget: "alt-done",
		FailureTarget: "alt-failed",
	}
	view, err := f.svc.UpsertGroup(ctx, "wf", revised)
	require.NoError(t, err)

	assert.Equal(t, "Fan out v2", view.Label)
	assert.True(t, view.Entrypoint, "nil Entrypoint keeps the existing flag")
	assert.Equal(t, []string{"c", "a"}, opsTaskKeys(view.Config))
	assert.Equal(t, workflow.GroupRuleFirstSuccess, view.Config.CompletionRule)
	assert.Equal(t, workflow.GroupUnfinishedFinishIndependently, view.Config.UnfinishedBehavior)
	assert.Equal(t, workflow.GroupGatherResultsAndFiles, view.Config.GatherMode)
	assert.Equal(t, "repo", view.Config.RepoPath)
	assert.Equal(t, "agenty", view.Config.ResolverProfile)
	assert.Equal(t, "prompts/resolve.md", view.Config.ResolverPromptFile)
	assert.Equal(t, "alt-done", view.SuccessTarget)
	assert.Equal(t, "alt-failed", view.FailureTarget)
	assert.Equal(t, map[string][]string{"c": {"c1"}, "a": {"a1"}}, view.Bodies)

	stored := f.repo.stored(workflow.Private(sgAlice), "wf")
	node := opsNode(t, stored, "fanout")
	assert.Equal(t, "Fan out v2", node.Label)
	assert.Equal(t, "three-way race", node.Description)
	assert.True(t, node.Entrypoint)
	assert.Equal(t, revised.Config, *node.Config.Group)
	assert.Equal(t, []workflow.Edge{
		{Source: "fanout", Target: "c1", Output: workflow.GroupPortTask, Label: "c"},
		{Source: "fanout", Target: "a1", Output: workflow.GroupPortTask, Label: "a"},
		{Source: "fanout", Target: "alt-done", Output: workflow.GroupPortSuccess},
		{Source: "fanout", Target: "alt-failed", Output: workflow.GroupPortFailure},
	}, opsOutgoing(stored, "fanout"), "the dropped task's edge and the old continuations are gone")
}

func TestGroupOps_Upsert_SwitchingFormDropsTaskEdges(t *testing.T) {
	f, ctx := opsSeeded(t)
	f.repo.seed(workflow.Private(sgAlice), sgChild("reviewer", sgTopic))
	_, err := f.svc.UpsertGroup(ctx, "wf", opsFanout())
	require.NoError(t, err)

	up := opsFanout()
	up.Config.Form = workflow.GroupFormWorkflows
	up.Config.Tasks = []workflow.GroupTask{{Key: "only", Workflow: "reviewer", Inputs: map[string]any{"topic": "t"}}}
	_, err = f.svc.UpsertGroup(ctx, "wf", up)
	require.NoError(t, err)

	out := opsOutgoing(f.repo.stored(workflow.Private(sgAlice), "wf"), "fanout")
	require.Len(t, out, 2)
	assert.Equal(t, workflow.GroupPortSuccess, out[0].Output)
	assert.Equal(t, workflow.GroupPortFailure, out[1].Output)
}

// opsWithStartNode upserts opsFanout as the entrypoint, then (full-manifest
// Update, like workflow_update) moves the entrypoint to a new "start" node with
// an incoming edge start->fanout, plus an unrelated done->alt-done edge. Every
// intermediate state keeps the group reachable.
func opsWithStartNode(t *testing.T, f *sgFixture, ctx context.Context) {
	t.Helper()
	_, err := f.svc.UpsertGroup(ctx, "wf", opsFanout())
	require.NoError(t, err)
	wf := f.repo.stored(workflow.Private(sgAlice), "wf")
	for i := range wf.Nodes {
		if wf.Nodes[i].ID == "fanout" {
			wf.Nodes[i].Entrypoint = false
		}
	}
	start := sgTransform("start")
	start.Entrypoint = true
	wf.Nodes = append(wf.Nodes, start)
	wf.Edges = append([]workflow.Edge{{Source: "start", Target: "fanout"}}, wf.Edges...)
	wf.Edges = append(wf.Edges, workflow.Edge{Source: "done", Target: "alt-done"})
	require.NoError(t, f.svc.Update(ctx, "wf", wf))
}

func TestGroupOps_Upsert_PreservesStoredManifest(t *testing.T) {
	f, ctx := opsSeeded(t)
	opsWithStartNode(t, f, ctx)
	var err error

	up := opsFanout()
	up.Entrypoint = nil
	up.Config.Tasks = []workflow.GroupTask{{Key: "b", Entry: "b1"}, {Key: "a", Entry: "a1"}}
	_, err = f.svc.UpsertGroup(ctx, "wf", up)
	require.NoError(t, err)

	stored := f.repo.stored(workflow.Private(sgAlice), "wf")
	assert.Equal(t, workflow.Edge{Source: "start", Target: "fanout"}, stored.Edges[0], "incoming edges are untouched")
	assert.Contains(t, stored.Edges, workflow.Edge{Source: "done", Target: "alt-done"}, "unrelated edges are untouched")
	assert.True(t, opsNode(t, stored, "start").Entrypoint)
	assert.False(t, opsNode(t, stored, "fanout").Entrypoint)
	for _, id := range []string{"a1", "b1", "c1", "done", "failed", "alt-done", "alt-failed", "start"} {
		assert.True(t, opsHasNode(stored, id), "stored node %s must survive the upsert", id)
	}
	out := opsOutgoing(stored, "fanout")
	require.Len(t, out, 4)
	assert.Equal(t, []string{"b", "a"}, []string{out[0].Label, out[1].Label})
	// Rewritten edges replace the old ones in place (directly after start->fanout).
	assert.Equal(t, out[0], stored.Edges[1])
}

func TestGroupOps_Upsert_EntrypointIsExclusive(t *testing.T) {
	f := newSGFixture(t, true)
	base := opsBase("wf")
	start := sgTransform("start")
	start.Entrypoint = true
	base.Nodes = append(base.Nodes, start)
	f.repo.seed(workflow.Private(sgAlice), base)
	ctx := ctxWith(sgAlice, false)

	_, err := f.svc.UpsertGroup(ctx, "wf", opsFanout()) // Entrypoint: true
	require.NoError(t, err)
	stored := f.repo.stored(workflow.Private(sgAlice), "wf")
	assert.True(t, opsNode(t, stored, "fanout").Entrypoint)
	assert.False(t, opsNode(t, stored, "start").Entrypoint, "exactly one entrypoint after moving it to the group")

	up := opsFanout()
	up.Entrypoint = nil
	up.Label = ""
	_, err = f.svc.UpsertGroup(ctx, "wf", up)
	require.NoError(t, err)
	stored = f.repo.stored(workflow.Private(sgAlice), "wf")
	assert.True(t, opsNode(t, stored, "fanout").Entrypoint, "nil Entrypoint keeps the flag")
	assert.False(t, opsNode(t, stored, "start").Entrypoint, "and never re-enables another node")
	assert.Equal(t, "Fan out", opsNode(t, stored, "fanout").Label, "an empty label keeps the existing one")
}

func TestGroupOps_Upsert_NestedGroups(t *testing.T) {
	f := newSGFixture(t, true)
	ctx := ctxWith(sgAlice, false)

	// One full-manifest create introduces the outer group, the nested group and
	// every body node at once — no invalid intermediate save is needed.
	nested := &workflow.Workflow{
		Name: "nested", Description: "nested", Author: "t",
		Nodes: []workflow.Node{
			{ID: "outer", Type: workflow.NodeTypeParallelGroup, Entrypoint: true, Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
				Form:               workflow.GroupFormBranches,
				Tasks:              []workflow.GroupTask{{Key: "a", Entry: "inner"}, {Key: "b", Entry: "b1"}},
				CompletionRule:     workflow.GroupRuleAllSucceed,
				UnfinishedBehavior: workflow.GroupUnfinishedCancel,
				GatherMode:         workflow.GroupGatherResults,
			}}},
			{ID: "inner", Type: workflow.NodeTypeParallelGroup, Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
				Form:               workflow.GroupFormBranches,
				Tasks:              []workflow.GroupTask{{Key: "x", Entry: "x1"}, {Key: "y", Entry: "y1"}},
				CompletionRule:     workflow.GroupRuleAllSucceed,
				UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining,
				GatherMode:         workflow.GroupGatherResults,
			}}},
			sgTransform("x1"), sgTransform("y1"), sgTransform("a2"), sgTransform("a3"),
			sgTransform("b1"), sgTransform("done"), sgTransform("failed"),
		},
		Edges: []workflow.Edge{
			{Source: "outer", Target: "inner", Output: workflow.GroupPortTask, Label: "a"},
			{Source: "outer", Target: "b1", Output: workflow.GroupPortTask, Label: "b"},
			{Source: "outer", Target: "done", Output: workflow.GroupPortSuccess},
			{Source: "outer", Target: "failed", Output: workflow.GroupPortFailure},
			{Source: "inner", Target: "x1", Output: workflow.GroupPortTask, Label: "x"},
			{Source: "inner", Target: "y1", Output: workflow.GroupPortTask, Label: "y"},
			{Source: "inner", Target: "a2", Output: workflow.GroupPortSuccess},
			{Source: "inner", Target: "a3", Output: workflow.GroupPortFailure},
		},
	}
	require.NoError(t, f.svc.Create(ctx, "nested", nested))

	inner, err := f.svc.GetGroup(ctx, "nested", "inner")
	require.NoError(t, err)
	require.NotNil(t, inner.Parent)
	assert.Equal(t, workflow.BodyOwner{GroupNodeID: "outer", TaskKey: "a"}, *inner.Parent)
	assert.Equal(t, map[string][]string{"x": {"x1"}, "y": {"y1"}}, inner.Bodies)

	outer, err := f.svc.GetGroup(ctx, "nested", "outer")
	require.NoError(t, err)
	assert.Equal(t, []string{"inner"}, outer.Nested)
	assert.Subset(t, outer.Bodies["a"], []string{"inner", "a2", "a3"}, "the nested group and its continuations belong to task a")
	assert.Equal(t, []string{"b1"}, outer.Bodies["b"])

	// Revise the NESTED group through upsert: order, rule, behaviour, targets.
	_, err = f.svc.UpsertGroup(ctx, "nested", workflow.GroupUpsert{
		NodeID: "inner",
		Config: workflow.GroupConfig{
			Form:               workflow.GroupFormBranches,
			Tasks:              []workflow.GroupTask{{Key: "y", Entry: "y1"}, {Key: "x", Entry: "x1"}},
			CompletionRule:     workflow.GroupRuleFirstSuccess,
			UnfinishedBehavior: workflow.GroupUnfinishedCancel,
			GatherMode:         workflow.GroupGatherResults,
		},
		SuccessTarget: "a3",
		FailureTarget: "a2",
	})
	require.NoError(t, err)

	stored := f.repo.stored(workflow.Private(sgAlice), "nested")
	g := opsNode(t, stored, "inner").Config.Group
	assert.Equal(t, []string{"y", "x"}, opsTaskKeys(*g))
	assert.Equal(t, workflow.GroupRuleFirstSuccess, g.CompletionRule)
	assert.Equal(t, []workflow.Edge{
		{Source: "inner", Target: "y1", Output: workflow.GroupPortTask, Label: "y"},
		{Source: "inner", Target: "x1", Output: workflow.GroupPortTask, Label: "x"},
		{Source: "inner", Target: "a3", Output: workflow.GroupPortSuccess},
		{Source: "inner", Target: "a2", Output: workflow.GroupPortFailure},
	}, opsOutgoing(stored, "inner"))
	assert.Len(t, opsOutgoing(stored, "outer"), 4, "the enclosing group's edges are untouched")

	again, err := f.svc.GetGroup(ctx, "nested", "inner")
	require.NoError(t, err)
	require.NotNil(t, again.Parent)
	assert.Equal(t, "outer", again.Parent.GroupNodeID, "nesting survives the revision")

	list, err := f.svc.ListGroups(ctx, "nested")
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "outer", list[0].NodeID)
	assert.Equal(t, "inner", list[1].NodeID)
}

// ---------------------------------------------------------------------------
// UpsertGroup — failures
// ---------------------------------------------------------------------------

func TestGroupOps_Upsert_InvalidConfigRejectedNothingPersisted(t *testing.T) {
	f, ctx := opsSeeded(t)
	before := f.repo.raw(workflow.Private(sgAlice), "wf")

	cases := map[string]struct {
		mutate func(*workflow.GroupUpsert)
		code   string
	}{
		"unknown form":          {func(u *workflow.GroupUpsert) { u.Config.Form = "fanout" }, "GROUP_INVALID_FORM"},
		"no tasks":              {func(u *workflow.GroupUpsert) { u.Config.Tasks = nil }, "GROUP_NO_TASKS"},
		"duplicate key":         {func(u *workflow.GroupUpsert) { u.Config.Tasks[1].Key = "a" }, "GROUP_DUPLICATE_TASK_KEY"},
		"duplicate entry":       {func(u *workflow.GroupUpsert) { u.Config.Tasks[1].Entry = "a1" }, "GROUP_DUPLICATE_ENTRY"},
		"unknown entry":         {func(u *workflow.GroupUpsert) { u.Config.Tasks[1].Entry = "nowhere" }, "GROUP_UNKNOWN_ENTRY"},
		"reserved input":        {func(u *workflow.GroupUpsert) { u.Config.Tasks[0].Inputs = map[string]any{"workspace": "/tmp"} }, "GROUP_RESERVED_INPUT"},
		"bad rule":              {func(u *workflow.GroupUpsert) { u.Config.CompletionRule = "quorum" }, "GROUP_INVALID_RULE"},
		"incompatible behavior": {func(u *workflow.GroupUpsert) { u.Config.UnfinishedBehavior = workflow.GroupUnfinishedFinishIndependently }, "GROUP_INVALID_UNFINISHED"},
		"bad gather":            {func(u *workflow.GroupUpsert) { u.Config.GatherMode = "files" }, "GROUP_INVALID_GATHER"},
		"files without resolver": {func(u *workflow.GroupUpsert) {
			u.Config.GatherMode = workflow.GroupGatherResultsAndFiles
		}, "GROUP_RESOLVER_PROFILE_REQUIRED"},
		"chat resolver": {func(u *workflow.GroupUpsert) {
			u.Config.GatherMode = workflow.GroupGatherResultsAndFiles
			u.Config.ResolverProfile = "chatty"
		}, "GROUP_RESOLVER_PROFILE_KIND"},
		"escaping repo path": {func(u *workflow.GroupUpsert) { u.Config.RepoPath = "../../etc" }, "GROUP_INVALID_REPO_PATH"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			up := opsFanout()
			tc.mutate(&up)
			view, err := f.svc.UpsertGroup(ctx, "wf", up)
			assert.Nil(t, view)
			gve := requireGroupErr(t, err)
			assert.Contains(t, sgCodes(gve.Errors), tc.code)
			assert.Equal(t, before, f.repo.raw(workflow.Private(sgAlice), "wf"), "nothing persisted")
			assert.Equal(t, 0, f.repo.writeCount())
		})
	}
}

func TestGroupOps_Upsert_ArgumentErrors(t *testing.T) {
	f, ctx := opsSeeded(t)
	cases := map[string]func(*workflow.GroupUpsert){
		"empty node id":                func(u *workflow.GroupUpsert) { u.NodeID = "" },
		"node id used by another node": func(u *workflow.GroupUpsert) { u.NodeID = "a1" },
		"empty success target":         func(u *workflow.GroupUpsert) { u.SuccessTarget = "" },
		"unknown failure target":       func(u *workflow.GroupUpsert) { u.FailureTarget = "nowhere" },
		"self success target":          func(u *workflow.GroupUpsert) { u.SuccessTarget = "fanout" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			up := opsFanout()
			mutate(&up)
			_, err := f.svc.UpsertGroup(ctx, "wf", up)
			require.Error(t, err)
			assert.True(t, errors.Is(err, workflow.ErrGroupUpsertInvalid), "got %v", err)
			assert.Equal(t, 0, f.repo.writeCount())
		})
	}
}

func TestGroupOps_Upsert_RoutesThroughUpdateValidation(t *testing.T) {
	f := newSGFixture(t, false) // no SetGroupCheckers
	f.repo.seed(workflow.Private(sgAlice), opsBase("wf"))

	_, err := f.svc.UpsertGroup(ctxWith(sgAlice, false), "wf", opsFanout())
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_CHECKER_UNAVAILABLE")
	assert.Equal(t, 0, f.repo.writeCount())
}

func TestGroupOps_Upsert_InvisibleChildRejected(t *testing.T) {
	f, ctx := opsSeeded(t)
	f.repo.seed(workflow.Private(sgBob), sgChild("bobs-child", sgTopic))

	_, err := f.svc.UpsertGroup(ctx, "wf", workflow.GroupUpsert{
		NodeID: "reviews", Entrypoint: opsBool(true),
		Config: workflow.GroupConfig{
			Form:               workflow.GroupFormWorkflows,
			Tasks:              []workflow.GroupTask{{Key: "a", Workflow: "bobs-child", Inputs: map[string]any{"topic": "x"}}},
			CompletionRule:     workflow.GroupRuleAllSucceed,
			UnfinishedBehavior: workflow.GroupUnfinishedCancel,
			GatherMode:         workflow.GroupGatherResults,
		},
		SuccessTarget: "done", FailureTarget: "failed",
	})
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_UNKNOWN_CHILD_WORKFLOW")
	assert.Equal(t, 0, f.repo.writeCount())
}

func TestGroupOps_Upsert_Authorization(t *testing.T) {
	t.Run("another user's private workflow is not found", func(t *testing.T) {
		f := newSGFixture(t, true)
		f.repo.seed(workflow.Private(sgBob), opsBase("wf"))
		_, err := f.svc.UpsertGroup(ctxWith(sgAlice, false), "wf", opsFanout())
		assert.True(t, errors.Is(err, workflow.ErrNotFound))
		assert.Equal(t, 0, f.repo.writeCount())
	})
	t.Run("non-admin cannot edit a shared-only workflow", func(t *testing.T) {
		f := newSGFixture(t, true)
		f.repo.seed(workflow.Shared(), opsBase("wf"))
		before := f.repo.raw(workflow.Shared(), "wf")
		_, err := f.svc.UpsertGroup(ctxWith(sgAlice, false), "wf", opsFanout())
		assert.True(t, errors.Is(err, workflow.ErrSharedReadOnly))
		assert.Equal(t, before, f.repo.raw(workflow.Shared(), "wf"))
	})
	t.Run("admin edits a shared-only workflow in place", func(t *testing.T) {
		f := newSGFixture(t, true)
		f.repo.seed(workflow.Shared(), opsBase("wf"))
		_, err := f.svc.UpsertGroup(ctxWith(sgAlice, true), "wf", opsFanout())
		require.NoError(t, err)
		assert.Equal(t, workflow.NodeTypeParallelGroup, opsNode(t, f.repo.stored(workflow.Shared(), "wf"), "fanout").Type)
		assert.Nil(t, f.repo.raw(workflow.Private(sgAlice), "wf"), "no private copy is created")
	})
	t.Run("no identity", func(t *testing.T) {
		f := newSGFixture(t, true)
		f.repo.seed(workflow.Private(sgAlice), opsBase("wf"))
		_, err := f.svc.UpsertGroup(context.Background(), "wf", opsFanout())
		assert.True(t, errors.Is(err, identity.ErrUnauthenticated))
	})
}

func TestGroupOps_Upsert_RepositoryFailures(t *testing.T) {
	t.Run("write failure", func(t *testing.T) {
		f, ctx := opsSeeded(t)
		f.repo.updateErr = errors.New("disk full")
		_, err := f.svc.UpsertGroup(ctx, "wf", opsFanout())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "disk full")
	})
	t.Run("load failure", func(t *testing.T) {
		f, ctx := opsSeeded(t)
		f.repo.loadErr["wf"] = errors.New("io error")
		_, err := f.svc.UpsertGroup(ctx, "wf", opsFanout())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "io error")
		assert.Equal(t, 0, f.repo.writeCount())
	})
}

// ---------------------------------------------------------------------------
// GetGroup / ListGroups
// ---------------------------------------------------------------------------

func TestGroupOps_Get_NeverPersists(t *testing.T) {
	f, ctx := opsSeeded(t)
	_, err := f.svc.UpsertGroup(ctx, "wf", opsFanout())
	require.NoError(t, err)
	writes, registers := f.repo.writeCount(), f.reg.registerCount()

	_, err = f.svc.GetGroup(ctx, "wf", "fanout")
	require.NoError(t, err)
	_, err = f.svc.ListGroups(ctx, "wf")
	require.NoError(t, err)

	assert.Equal(t, writes, f.repo.writeCount(), "reads must never write")
	assert.Equal(t, registers, f.reg.registerCount(), "reads must never claim registry rows")
}

func TestGroupOps_Get_NotFound(t *testing.T) {
	f, ctx := opsSeeded(t)
	_, err := f.svc.UpsertGroup(ctx, "wf", opsFanout())
	require.NoError(t, err)

	_, err = f.svc.GetGroup(ctx, "wf", "missing")
	assert.True(t, errors.Is(err, workflow.ErrGroupNotFound))
	_, err = f.svc.GetGroup(ctx, "wf", "a1")
	assert.True(t, errors.Is(err, workflow.ErrGroupNotFound), "a non-group node is not a group")
	_, err = f.svc.GetGroup(ctx, "nope", "fanout")
	assert.True(t, errors.Is(err, workflow.ErrNotFound))
}

func TestGroupOps_Get_Unauthorized(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Private(sgBob), opsBase("wf"))
	_, err := f.svc.UpsertGroup(ctxWith(sgBob, false), "wf", opsFanout())
	require.NoError(t, err)

	_, err = f.svc.GetGroup(ctxWith(sgAlice, false), "wf", "fanout")
	assert.True(t, errors.Is(err, workflow.ErrNotFound), "existence is not leaked across users")
	_, err = f.svc.ListGroups(ctxWith(sgAlice, false), "wf")
	assert.True(t, errors.Is(err, workflow.ErrNotFound))
	_, err = f.svc.GetGroup(context.Background(), "wf", "fanout")
	assert.True(t, errors.Is(err, identity.ErrUnauthenticated))
}

func TestGroupOps_Get_SharedReadableByNonAdmin(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Shared(), opsBase("wf"))
	_, err := f.svc.UpsertGroup(ctxWith(sgBob, true), "wf", opsFanout())
	require.NoError(t, err)

	view, err := f.svc.GetGroup(ctxWith(sgAlice, false), "wf", "fanout")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, opsTaskKeys(view.Config))
}

func TestGroupOps_Get_AdvisoriesSurfaceRuntimeInputChecks(t *testing.T) {
	f, ctx := opsSeeded(t)
	f.repo.seed(workflow.Private(sgAlice), sgChild("reviewer", sgTopic))
	_, err := f.svc.UpsertGroup(ctx, "wf", workflow.GroupUpsert{
		NodeID: "reviews", Entrypoint: opsBool(true),
		Config: workflow.GroupConfig{
			Form:               workflow.GroupFormWorkflows,
			Tasks:              []workflow.GroupTask{{Key: "a", Workflow: "reviewer"}},
			CompletionRule:     workflow.GroupRuleAllSucceed,
			UnfinishedBehavior: workflow.GroupUnfinishedCancel,
			GatherMode:         workflow.GroupGatherResults,
		},
		SuccessTarget: "done", FailureTarget: "failed",
	})
	require.NoError(t, err, "an inherited required input is an advisory, not a rejection")

	view, err := f.svc.GetGroup(ctx, "wf", "reviews")
	require.NoError(t, err)
	assert.Contains(t, sgCodes(view.Advisories), "GROUP_REQUIRED_INPUT_RUNTIME")
	for _, a := range view.Advisories {
		assert.Equal(t, "reviews", a.NodeID)
	}
}

func TestGroupOps_List_EmptyForGroupFreeWorkflow(t *testing.T) {
	f, ctx := opsSeeded(t)
	list, err := f.svc.ListGroups(ctx, "wf")
	require.NoError(t, err)
	assert.NotNil(t, list)
	assert.Empty(t, list)
}

// ---------------------------------------------------------------------------
// DeleteGroup
// ---------------------------------------------------------------------------

func TestGroupOps_Delete_RemovesNodeAndIncidentEdgesAndReportsRetained(t *testing.T) {
	f, ctx := opsSeeded(t)
	_, err := f.svc.UpsertGroup(ctx, "wf", opsFanout())
	require.NoError(t, err)

	res, err := f.svc.DeleteGroup(ctx, "wf", "fanout")
	require.NoError(t, err)
	assert.Equal(t, "fanout", res.NodeID)
	assert.Equal(t, []string{"a1", "b1", "done", "failed"}, res.RetainedNodes,
		"body nodes in task order, then the success and failure targets")

	stored := f.repo.stored(workflow.Private(sgAlice), "wf")
	assert.False(t, opsHasNode(stored, "fanout"))
	for _, e := range stored.Edges {
		assert.NotEqual(t, "fanout", e.Source)
		assert.NotEqual(t, "fanout", e.Target)
	}
	for _, id := range res.RetainedNodes {
		assert.True(t, opsHasNode(stored, id), "retained node %s is still in the manifest", id)
	}
}

func TestGroupOps_Delete_IncomingEdgesAlsoRemoved(t *testing.T) {
	f, ctx := opsSeeded(t)
	opsWithStartNode(t, f, ctx)

	_, err := f.svc.DeleteGroup(ctx, "wf", "fanout")
	require.NoError(t, err)
	stored := f.repo.stored(workflow.Private(sgAlice), "wf")
	assert.NotContains(t, stored.Edges, workflow.Edge{Source: "start", Target: "fanout"})
	assert.Contains(t, stored.Edges, workflow.Edge{Source: "done", Target: "alt-done"}, "edges not incident to the group stay")
	assert.True(t, opsHasNode(stored, "start"))
	assert.True(t, opsNode(t, stored, "start").Entrypoint)
}

func TestGroupOps_Delete_LeavingAnotherGroupInvalidIsRejected(t *testing.T) {
	f := newSGFixture(t, true)
	ctx := ctxWith(sgAlice, false)
	two := &workflow.Workflow{
		Name: "two", Description: "sequential groups", Author: "t",
		Nodes: []workflow.Node{
			{ID: "first", Type: workflow.NodeTypeParallelGroup, Entrypoint: true, Config: workflow.NodeConfig{Group: func() *workflow.GroupConfig {
				c := opsBranchesCfg(workflow.GroupTask{Key: "a", Entry: "a1"}, workflow.GroupTask{Key: "b", Entry: "b1"})
				return &c
			}()}},
			{ID: "second", Type: workflow.NodeTypeParallelGroup, Config: workflow.NodeConfig{Group: func() *workflow.GroupConfig {
				c := opsBranchesCfg(workflow.GroupTask{Key: "x", Entry: "x1"}, workflow.GroupTask{Key: "y", Entry: "y1"})
				return &c
			}()}},
			sgTransform("a1"), sgTransform("b1"), sgTransform("x1"), sgTransform("y1"),
			sgTransform("done"), sgTransform("failed"),
		},
		Edges: []workflow.Edge{
			{Source: "first", Target: "a1", Output: workflow.GroupPortTask, Label: "a"},
			{Source: "first", Target: "b1", Output: workflow.GroupPortTask, Label: "b"},
			{Source: "first", Target: "second", Output: workflow.GroupPortSuccess},
			{Source: "first", Target: "failed", Output: workflow.GroupPortFailure},
			{Source: "second", Target: "x1", Output: workflow.GroupPortTask, Label: "x"},
			{Source: "second", Target: "y1", Output: workflow.GroupPortTask, Label: "y"},
			{Source: "second", Target: "done", Output: workflow.GroupPortSuccess},
			{Source: "second", Target: "failed", Output: workflow.GroupPortFailure},
		},
	}
	require.NoError(t, f.svc.Create(ctx, "two", two))
	before := f.repo.raw(workflow.Private(sgAlice), "two")
	writes := f.repo.writeCount()

	res, err := f.svc.DeleteGroup(ctx, "two", "second")
	assert.Nil(t, res)
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_MISSING_PORT", "first would lose its success continuation")
	assert.Equal(t, before, f.repo.raw(workflow.Private(sgAlice), "two"), "nothing persisted")
	assert.Equal(t, writes, f.repo.writeCount())

	// Deleting the group that nothing else depends on is fine.
	_, err = f.svc.DeleteGroup(ctx, "two", "first")
	require.NoError(t, err)
}

func TestGroupOps_Delete_FailuresAndAuthorization(t *testing.T) {
	t.Run("unknown node", func(t *testing.T) {
		f, ctx := opsSeeded(t)
		_, err := f.svc.DeleteGroup(ctx, "wf", "fanout")
		assert.True(t, errors.Is(err, workflow.ErrGroupNotFound))
		assert.Equal(t, 0, f.repo.writeCount())
	})
	t.Run("non-group node", func(t *testing.T) {
		f, ctx := opsSeeded(t)
		_, err := f.svc.DeleteGroup(ctx, "wf", "a1")
		assert.True(t, errors.Is(err, workflow.ErrGroupNotFound))
	})
	t.Run("another user's workflow", func(t *testing.T) {
		f := newSGFixture(t, true)
		f.repo.seed(workflow.Private(sgBob), opsBase("wf"))
		_, err := f.svc.DeleteGroup(ctxWith(sgAlice, false), "wf", "fanout")
		assert.True(t, errors.Is(err, workflow.ErrNotFound))
	})
	t.Run("non-admin on shared-only", func(t *testing.T) {
		f := newSGFixture(t, true)
		f.repo.seed(workflow.Shared(), opsBase("wf"))
		_, err := f.svc.UpsertGroup(ctxWith(sgBob, true), "wf", opsFanout())
		require.NoError(t, err)
		before := f.repo.raw(workflow.Shared(), "wf")
		_, err = f.svc.DeleteGroup(ctxWith(sgAlice, false), "wf", "fanout")
		assert.True(t, errors.Is(err, workflow.ErrSharedReadOnly))
		assert.Equal(t, before, f.repo.raw(workflow.Shared(), "wf"))
	})
	t.Run("repository write failure", func(t *testing.T) {
		f, ctx := opsSeeded(t)
		_, err := f.svc.UpsertGroup(ctx, "wf", opsFanout())
		require.NoError(t, err)
		f.repo.updateErr = errors.New("disk full")
		_, err = f.svc.DeleteGroup(ctx, "wf", "fanout")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "disk full")
	})
}
```

- [x] Step 2: Run it, confirm it fails.

```bash
go test ./domain/workflow/ -run 'TestGroupOps' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/domain/workflow_test [github.com/arbitron/arbitron/domain/workflow.test]
domain/workflow/group_ops_test.go:...: undefined: workflow.GroupUpsert
domain/workflow/group_ops_test.go:...: f.svc.UpsertGroup undefined (type *workflow.Service has no field or method UpsertGroup)
domain/workflow/group_ops_test.go:...: undefined: workflow.ErrGroupNotFound
domain/workflow/group_ops_test.go:...: undefined: workflow.ErrGroupUpsertInvalid
...
FAIL	github.com/arbitron/arbitron/domain/workflow [build failed]
```

- [x] Step 3: Implement. Create `domain/workflow/group_ops.go`:

```go
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// GroupUpsert is the complete author-supplied definition of ONE parallel_group
// node: its settings, ordered tasks (with inputs) and continuation targets.
// Body nodes and edges INTO the group are authored with workflow_create /
// workflow_update; UpsertGroup owns only the group node and its outgoing edges.
type GroupUpsert struct {
	NodeID        string      `json:"nodeId"`
	Label         string      `json:"label,omitempty"`
	Description   string      `json:"description,omitempty"`
	Entrypoint    *bool       `json:"entrypoint,omitempty"` // nil => keep existing (false for new)
	Config        GroupConfig `json:"config"`
	SuccessTarget string      `json:"successTarget"`
	FailureTarget string      `json:"failureTarget"`
}

// GroupView is the read model of one parallel_group node.
type GroupView struct {
	NodeID        string              `json:"nodeId"`
	Label         string              `json:"label,omitempty"`
	Entrypoint    bool                `json:"entrypoint"`
	Config        GroupConfig         `json:"config"` // tasks in author order
	SuccessTarget string              `json:"successTarget"`
	FailureTarget string              `json:"failureTarget"`
	Bodies        map[string][]string `json:"bodies"`
	Nested        []string            `json:"nested"`
	Parent        *BodyOwner          `json:"parent,omitempty"`
	Advisories    []ValidationError   `json:"advisories,omitempty"`
}

// GroupDeleteResult reports what a DeleteGroup left behind.
type GroupDeleteResult struct {
	NodeID        string   `json:"nodeId"`
	RetainedNodes []string `json:"retainedNodes"` // former body nodes + continuation targets left in the manifest, for reconnection/removal
}

// ErrGroupNotFound: the workflow exists and is visible, but has no
// parallel_group node with the requested id.
var ErrGroupNotFound = errors.New("parallel group not found")

// ErrGroupUpsertInvalid: the upsert ARGUMENTS cannot produce a well-formed
// group (missing/colliding node id, missing/unknown/self continuation target).
// These are rejected here because Validate would only report them with
// non-group codes, which the group write gate does not block on.
var ErrGroupUpsertInvalid = errors.New("invalid parallel group upsert")

// UpsertGroup creates or revises one parallel_group node. It loads the
// caller-visible workflow, rewrites the node and ALL its outgoing edges in one
// in-memory replacement (task edges in author order, then success, then
// failure), and persists through Update — so authorization (private first,
// admin shared-in-place) and group validation apply, and a rejected upsert
// persists nothing.
func (s *Service) UpsertGroup(ctx context.Context, workflowName string, up GroupUpsert) (*GroupView, error) {
	loaded, err := s.Load(ctx, workflowName)
	if err != nil {
		return nil, err
	}
	wf, err := cloneWorkflow(loaded)
	if err != nil {
		return nil, err
	}
	wf.Name = workflowName
	if err := applyGroupUpsert(wf, up); err != nil {
		return nil, err
	}
	if err := s.Update(ctx, workflowName, wf); err != nil {
		return nil, err
	}
	graph, _ := AnalyzeGroups(wf)
	view, _ := buildGroupView(wf, graph, up.NodeID, s.ValidateGroups(ctx, wf))
	return view, nil
}

// GetGroup returns the view of one group. It is a pure read: it never writes
// the folder or the registry.
func (s *Service) GetGroup(ctx context.Context, workflowName, nodeID string) (*GroupView, error) {
	wf, err := s.Load(ctx, workflowName)
	if err != nil {
		return nil, err
	}
	if !isGroupNode(wf, nodeID) {
		return nil, fmt.Errorf("%w: %q in workflow %q", ErrGroupNotFound, nodeID, workflowName)
	}
	graph, _ := AnalyzeGroups(wf)
	view, _ := buildGroupView(wf, graph, nodeID, s.ValidateGroups(ctx, wf))
	return view, nil
}

// ListGroups returns every group of the workflow in definition (wf.Nodes)
// order — the same order as GroupGraph.Order. A group-free workflow yields an
// empty, non-nil slice. Pure read.
func (s *Service) ListGroups(ctx context.Context, workflowName string) ([]GroupView, error) {
	wf, err := s.Load(ctx, workflowName)
	if err != nil {
		return nil, err
	}
	out := []GroupView{}
	if !HasGroups(wf) {
		return out, nil
	}
	graph, _ := AnalyzeGroups(wf)
	findings := s.ValidateGroups(ctx, wf)
	for _, n := range wf.Nodes {
		if n.Type != NodeTypeParallelGroup {
			continue
		}
		if v, ok := buildGroupView(wf, graph, n.ID, findings); ok {
			out = append(out, *v)
		}
	}
	return out, nil
}

// DeleteGroup removes the group node and every edge incident to it, then
// persists through Update. When the remaining workflow still contains groups
// and any of them is now invalid (e.g. it lost a continuation), the write is
// rejected with *GroupValidationError and nothing is persisted.
func (s *Service) DeleteGroup(ctx context.Context, workflowName, nodeID string) (*GroupDeleteResult, error) {
	loaded, err := s.Load(ctx, workflowName)
	if err != nil {
		return nil, err
	}
	if !isGroupNode(loaded, nodeID) {
		return nil, fmt.Errorf("%w: %q in workflow %q", ErrGroupNotFound, nodeID, workflowName)
	}
	wf, err := cloneWorkflow(loaded)
	if err != nil {
		return nil, err
	}
	wf.Name = workflowName

	graph, _ := AnalyzeGroups(wf)
	retained := retainedAfterDelete(wf, graph, nodeID)

	nodes := make([]Node, 0, len(wf.Nodes))
	for _, n := range wf.Nodes {
		if n.ID != nodeID {
			nodes = append(nodes, n)
		}
	}
	wf.Nodes = nodes
	edges := make([]Edge, 0, len(wf.Edges))
	for _, e := range wf.Edges {
		if e.Source != nodeID && e.Target != nodeID {
			edges = append(edges, e)
		}
	}
	wf.Edges = edges

	if err := s.Update(ctx, workflowName, wf); err != nil {
		return nil, err
	}
	return &GroupDeleteResult{NodeID: nodeID, RetainedNodes: retained}, nil
}

// applyGroupUpsert mutates wf in memory. It never touches storage.
func applyGroupUpsert(wf *Workflow, up GroupUpsert) error {
	if up.NodeID == "" {
		return fmt.Errorf("%w: nodeId is required", ErrGroupUpsertInvalid)
	}
	idx := -1
	for i, n := range wf.Nodes {
		if n.ID == up.NodeID {
			idx = i
			break
		}
	}
	if idx >= 0 && wf.Nodes[idx].Type != NodeTypeParallelGroup {
		return fmt.Errorf("%w: node %q already exists as a %s node; choose another nodeId or remove that node with workflow_update",
			ErrGroupUpsertInvalid, up.NodeID, wf.Nodes[idx].Type)
	}
	if err := checkContinuationTarget(wf, up.NodeID, "successTarget", up.SuccessTarget); err != nil {
		return err
	}
	if err := checkContinuationTarget(wf, up.NodeID, "failureTarget", up.FailureTarget); err != nil {
		return err
	}
	cfg, err := copyGroupConfig(up.Config)
	if err != nil {
		return err
	}

	node := Node{ID: up.NodeID}
	if idx >= 0 {
		node = wf.Nodes[idx]
	}
	node.Type = NodeTypeParallelGroup
	node.Config = NodeConfig{Group: &cfg}
	if up.Label != "" {
		node.Label = up.Label
	}
	if up.Description != "" {
		node.Description = up.Description
	}
	if up.Entrypoint != nil {
		node.Entrypoint = *up.Entrypoint
		if *up.Entrypoint {
			for i := range wf.Nodes {
				if wf.Nodes[i].ID != up.NodeID {
					wf.Nodes[i].Entrypoint = false
				}
			}
		}
	}
	if idx >= 0 {
		wf.Nodes[idx] = node
	} else {
		wf.Nodes = append(wf.Nodes, node)
	}
	wf.Edges = rewriteGroupEdges(wf.Edges, up.NodeID, cfg, up.SuccessTarget, up.FailureTarget)
	return nil
}

func checkContinuationTarget(wf *Workflow, groupID, field, target string) error {
	switch {
	case target == "":
		return fmt.Errorf("%w: %s is required (the node the parent continues to)", ErrGroupUpsertInvalid, field)
	case target == groupID:
		return fmt.Errorf("%w: %s cannot be the group node %q itself", ErrGroupUpsertInvalid, field, groupID)
	}
	for _, n := range wf.Nodes {
		if n.ID == target {
			return nil
		}
	}
	return fmt.Errorf("%w: %s %q is not a node in workflow %q; add the node with workflow_update first",
		ErrGroupUpsertInvalid, field, target, wf.Name)
}

// rewriteGroupEdges drops every edge leaving groupID and inserts the fresh set
// where the first dropped edge was (appended when the group had none), so a
// revision keeps the manifest's edge order stable.
func rewriteGroupEdges(edges []Edge, groupID string, cfg GroupConfig, success, failure string) []Edge {
	var fresh []Edge
	if cfg.Form == GroupFormBranches {
		for _, t := range cfg.Tasks {
			if t.Entry == "" {
				continue // validation reports the missing entry
			}
			fresh = append(fresh, Edge{Source: groupID, Target: t.Entry, Output: GroupPortTask, Label: t.Key})
		}
	}
	fresh = append(fresh,
		Edge{Source: groupID, Target: success, Output: GroupPortSuccess},
		Edge{Source: groupID, Target: failure, Output: GroupPortFailure},
	)
	out := make([]Edge, 0, len(edges)+len(fresh))
	inserted := false
	for _, e := range edges {
		if e.Source == groupID {
			if !inserted {
				out = append(out, fresh...)
				inserted = true
			}
			continue
		}
		out = append(out, e)
	}
	if !inserted {
		out = append(out, fresh...)
	}
	return out
}

func isGroupNode(wf *Workflow, nodeID string) bool {
	if wf == nil {
		return false
	}
	for _, n := range wf.Nodes {
		if n.ID == nodeID {
			return n.Type == NodeTypeParallelGroup
		}
	}
	return false
}

// buildGroupView assembles the view from the ownership analysis, falling back
// to the raw node/edges when AnalyzeGroups could not build a scope (a malformed
// legacy definition) so inspection still works.
func buildGroupView(wf *Workflow, graph *GroupGraph, nodeID string, findings []ValidationError) (*GroupView, bool) {
	var node *Node
	for i := range wf.Nodes {
		if wf.Nodes[i].ID == nodeID {
			node = &wf.Nodes[i]
			break
		}
	}
	if node == nil || node.Type != NodeTypeParallelGroup {
		return nil, false
	}
	v := &GroupView{
		NodeID:     nodeID,
		Label:      node.Label,
		Entrypoint: node.Entrypoint,
		Bodies:     map[string][]string{},
		Nested:     []string{},
	}
	var scope *GroupScope
	if graph != nil {
		scope = graph.Groups[nodeID]
	}
	if scope != nil {
		if cfg, err := copyGroupConfig(scope.Config); err == nil {
			v.Config = cfg
		}
		v.SuccessTarget, v.FailureTarget = scope.SuccessTarget, scope.FailureTarget
		for k, ids := range scope.Bodies {
			v.Bodies[k] = append([]string{}, ids...)
		}
		v.Nested = append(v.Nested, scope.Nested...)
		if scope.Parent != nil {
			p := *scope.Parent
			v.Parent = &p
		}
	} else {
		if node.Config.Group != nil {
			if cfg, err := copyGroupConfig(*node.Config.Group); err == nil {
				v.Config = cfg
			}
		}
		v.SuccessTarget, v.FailureTarget = continuationTargets(wf, nodeID)
	}
	for _, f := range findings {
		if f.NodeID == nodeID {
			v.Advisories = append(v.Advisories, f)
		}
	}
	return v, true
}

func continuationTargets(wf *Workflow, nodeID string) (success, failure string) {
	for _, e := range wf.Edges {
		if e.Source != nodeID {
			continue
		}
		switch e.Output {
		case GroupPortSuccess:
			if success == "" {
				success = e.Target
			}
		case GroupPortFailure:
			if failure == "" {
				failure = e.Target
			}
		}
	}
	return success, failure
}

// retainedAfterDelete lists the nodes a deleted group leaves behind: its body
// nodes in task order, then its success and failure targets; de-duplicated,
// never the group itself.
func retainedAfterDelete(wf *Workflow, graph *GroupGraph, nodeID string) []string {
	out := []string{}
	seen := map[string]bool{nodeID: true}
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	var scope *GroupScope
	if graph != nil {
		scope = graph.Groups[nodeID]
	}
	if scope != nil {
		for _, t := range scope.Config.Tasks {
			for _, id := range scope.Bodies[t.Key] {
				add(id)
			}
		}
		add(scope.SuccessTarget)
		add(scope.FailureTarget)
		return out
	}
	for _, e := range wf.Edges {
		if e.Source == nodeID {
			add(e.Target)
		}
	}
	return out
}

// cloneWorkflow deep-copies a definition through its JSON form (the form it
// is persisted in), so in-memory mutation can never reach a repository-owned
// value before validation has passed.
func cloneWorkflow(wf *Workflow) (*Workflow, error) {
	if wf == nil {
		return nil, fmt.Errorf("%w: empty workflow", ErrNotFound)
	}
	b, err := json.Marshal(wf)
	if err != nil {
		return nil, fmt.Errorf("copying workflow %q: %w", wf.Name, err)
	}
	var out Workflow
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("copying workflow %q: %w", wf.Name, err)
	}
	return &out, nil
}

// copyGroupConfig deep-copies a GroupConfig (task Inputs included) so the
// stored node never aliases caller-owned maps.
func copyGroupConfig(cfg GroupConfig) (GroupConfig, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return GroupConfig{}, fmt.Errorf("%w: group config is not JSON-serializable: %v", ErrGroupUpsertInvalid, err)
	}
	var out GroupConfig
	if err := json.Unmarshal(b, &out); err != nil {
		return GroupConfig{}, fmt.Errorf("%w: group config is not JSON-serializable: %v", ErrGroupUpsertInvalid, err)
	}
	return out, nil
}
```

- [x] Step 4: Run the tests and the package.

```bash
go test ./domain/workflow/ -run 'TestGroupOps' -count=1 -v
go test ./domain/workflow/... -count=1
go vet ./domain/workflow/
```
Expected:
```
--- PASS: TestGroupOps_Upsert_CreatesBranchesGroup (0.00s)
--- PASS: TestGroupOps_Upsert_CreatesWorkflowsGroupWithRepeatedChildAndDistinctInputs (0.00s)
...
--- PASS: TestGroupOps_Delete_FailuresAndAuthorization (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/workflow	0.0XXs
```

- [x] Step 5: Commit.

```bash
git add domain/workflow/group_ops.go domain/workflow/group_ops_test.go
git commit -m "feat(workflow): UpsertGroup/GetGroup/ListGroups/DeleteGroup service ops

Atomic group node + outgoing-edge rewrite persisted through Service.Update
(authorization, admin shared-in-place and group validation apply; rejected
mutations persist nothing). Read-only get/list expose author-ordered tasks,
targets, bodies, nesting and advisories. Delete reports retained nodes and is
rejected when it would leave another group invalid.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
