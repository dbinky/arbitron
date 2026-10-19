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
	up.Description = ""
	_, err = f.svc.UpsertGroup(ctx, "wf", up)
	require.NoError(t, err)
	stored = f.repo.stored(workflow.Private(sgAlice), "wf")
	assert.True(t, opsNode(t, stored, "fanout").Entrypoint, "nil Entrypoint keeps the flag")
	assert.False(t, opsNode(t, stored, "start").Entrypoint, "and never re-enables another node")
	assert.Equal(t, "Fan out", opsNode(t, stored, "fanout").Label, "an empty label keeps the existing one")
	assert.Equal(t, "two branches", opsNode(t, stored, "fanout").Description, "an empty description keeps the existing one")
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
		"unknown form":    {func(u *workflow.GroupUpsert) { u.Config.Form = "fanout" }, "GROUP_INVALID_FORM"},
		"no tasks":        {func(u *workflow.GroupUpsert) { u.Config.Tasks = nil }, "GROUP_NO_TASKS"},
		"duplicate key":   {func(u *workflow.GroupUpsert) { u.Config.Tasks[1].Key = "a" }, "GROUP_DUPLICATE_TASK_KEY"},
		"duplicate entry": {func(u *workflow.GroupUpsert) { u.Config.Tasks[1].Entry = "a1" }, "GROUP_DUPLICATE_ENTRY"},
		"unknown entry":   {func(u *workflow.GroupUpsert) { u.Config.Tasks[1].Entry = "nowhere" }, "GROUP_UNKNOWN_ENTRY"},
		"reserved input":  {func(u *workflow.GroupUpsert) { u.Config.Tasks[0].Inputs = map[string]any{"workspace": "/tmp"} }, "GROUP_RESERVED_INPUT"},
		"bad rule":        {func(u *workflow.GroupUpsert) { u.Config.CompletionRule = "quorum" }, "GROUP_INVALID_RULE"},
		"incompatible behavior": {func(u *workflow.GroupUpsert) {
			u.Config.UnfinishedBehavior = workflow.GroupUnfinishedFinishIndependently
		}, "GROUP_INVALID_UNFINISHED"},
		"bad gather": {func(u *workflow.GroupUpsert) { u.Config.GatherMode = "files" }, "GROUP_INVALID_GATHER"},
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

// opsAliasingRepo models a repository that caches successful loads. Scope/name
// lookup still goes through sgRepo; the cached definition is deliberately shared
// so a missing clone cannot hide behind sgRepo's usual fresh JSON decoding.
type opsAliasingRepo struct {
	*sgRepo
	loaded *workflow.Workflow
}

func (r *opsAliasingRepo) Load(ctx context.Context, scope workflow.Scope, name string) (*workflow.Workflow, error) {
	wf, err := r.sgRepo.Load(ctx, scope, name)
	if err != nil || name != r.loaded.Name {
		return wf, err
	}
	return r.loaded, nil
}

func TestGroupOps_RejectedMutationsDoNotChangeLoadedDefinition(t *testing.T) {
	for _, operation := range []string{"upsert", "delete"} {
		t.Run(operation, func(t *testing.T) {
			base := sgBranches("wf")
			repo := &opsAliasingRepo{sgRepo: newSGRepo(), loaded: base}
			repo.seed(workflow.Private(sgAlice), base)
			svc := workflow.NewWorkflowService(repo, nil)
			svc.SetGroupCheckers(sgProfiles{}, newSGFiles())
			before := sgEncode(base)
			repo.updateErr = errors.New("disk full")
			ctx := ctxWith(sgAlice, false)

			var err error
			if operation == "upsert" {
				up := opsFanout()
				up.Config.Tasks[0], up.Config.Tasks[1] = up.Config.Tasks[1], up.Config.Tasks[0]
				_, err = svc.UpsertGroup(ctx, "wf", up)
			} else {
				_, err = svc.DeleteGroup(ctx, "wf", "fanout")
			}
			require.ErrorContains(t, err, "disk full")
			assert.Equal(t, before, sgEncode(base), "a rejected mutation must not alter a repository-owned pointer")
			assert.Equal(t, before, repo.raw(workflow.Private(sgAlice), "wf"))
			assert.Zero(t, repo.writeCount())
		})
	}
}

func TestGroupOps_GetAndList_ExposeErrorsWithoutWriting(t *testing.T) {
	f, ctx := opsSeeded(t)
	legacy := sgBranches("wf")
	legacy.Nodes[0].Config.Group.GatherMode = "invalid"
	f.repo.seed(workflow.Private(sgAlice), legacy)
	before := f.repo.raw(workflow.Private(sgAlice), "wf")

	view, err := f.svc.GetGroup(ctx, "wf", "fanout")
	require.NoError(t, err)
	assert.Contains(t, sgCodes(view.Advisories), "GROUP_INVALID_GATHER")
	assert.Equal(t, "done", view.SuccessTarget)
	assert.Equal(t, "failed", view.FailureTarget)
	assert.Equal(t, map[string][]string{"a": {"a1"}, "b": {"b1"}}, view.Bodies)
	list, err := f.svc.ListGroups(ctx, "wf")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, *view, list[0])
	assert.Equal(t, before, f.repo.raw(workflow.Private(sgAlice), "wf"))
	assert.Zero(t, f.repo.writeCount())
	assert.Zero(t, f.reg.registerCount())
}

func TestGroupOps_Delete_DeduplicatesContinuationTargets(t *testing.T) {
	f, ctx := opsSeeded(t)
	up := opsFanout()
	up.FailureTarget = up.SuccessTarget
	_, err := f.svc.UpsertGroup(ctx, "wf", up)
	require.NoError(t, err)

	res, err := f.svc.DeleteGroup(ctx, "wf", "fanout")
	require.NoError(t, err)
	assert.Equal(t, []string{"a1", "b1", "done"}, res.RetainedNodes)
	stored := f.repo.stored(workflow.Private(sgAlice), "wf")
	assert.False(t, opsHasNode(stored, "fanout"))
	for _, id := range res.RetainedNodes {
		assert.True(t, opsHasNode(stored, id))
	}
}
