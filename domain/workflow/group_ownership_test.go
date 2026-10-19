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
	assert.NotNil(t, sc.Nested)
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

func TestAnalyzeGroups_TwoTasksShareAnEntry(t *testing.T) {
	wf := grpBasic()
	wf.Nodes[1].Config.Group.Tasks[1].Entry = "a1"
	wf.Edges = grpDropEdge(wf.Edges, "g", workflow.GroupPortTask)
	wf.Edges = append(wf.Edges, grpTaskE("g", "a1"))
	g, errs := workflow.AnalyzeGroups(wf)
	assert.True(t, grpHas(errs, "GROUP_BODY_OVERLAP", "g", "b"), grpCodes(errs))
	assert.Equal(t, []string{"a1", "a2"}, g.Groups["g"].Bodies["a"])
	assert.Equal(t, workflow.BodyOwner{GroupNodeID: "g", TaskKey: "a"}, g.Owner["a1"])
	assert.NotContains(t, g.Groups["g"].Bodies, "b", "the first task keeps the entry")
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
	assert.NotNil(t, g.Order)

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
	assert.Equal(t, workflow.GroupConfig{}, g.Groups["g"].Config)
}

func TestAnalyzeGroups_ConfigIsDeepCopied(t *testing.T) {
	for _, valid := range []bool{true, false} {
		name := "parsed"
		if !valid {
			name = "raw fallback"
		}
		t.Run(name, func(t *testing.T) {
			wf := grpBasic()
			cfg := wf.Nodes[1].Config.Group
			cfg.Tasks[0].Inputs = map[string]any{"payload": []any{map[string]any{"value": "original"}}}
			if !valid {
				cfg.GatherMode = "invalid"
			}
			g, errs := workflow.AnalyzeGroups(wf)
			require.Empty(t, errs, "config errors belong to validation, not ownership analysis")
			sc := g.Groups["g"]
			require.Equal(t, *cfg, sc.Config)
			sc.Config.Tasks[0].Inputs["payload"].([]any)[0].(map[string]any)["value"] = "changed"
			sc.Config.Tasks[0].Entry = "changed"
			assert.Equal(t, "original", cfg.Tasks[0].Inputs["payload"].([]any)[0].(map[string]any)["value"])
			assert.Equal(t, "a1", cfg.Tasks[0].Entry)
		})
	}
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
