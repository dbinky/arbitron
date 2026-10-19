package engine_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// graphStateWireFixture is the Go-serialized GraphState the dashboard's
// client.contract.test.ts decodes. The dashboard types are hand-written, so
// this golden file is the only thing that pins the Go JSON tags to the TS
// consumer: renaming a tag here fails this test until the fixture (and the TS
// types) are regenerated with UPDATE_GRAPH_STATE_WIRE=1.
const graphStateWireFixture = "../../dashboard/src/api/fixtures/graph-state.wire.json"

func TestGraphState_WireFixtureMatchesGoSerialization(t *testing.T) {
	got, err := json.MarshalIndent(wireGraphState(), "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')

	if os.Getenv("UPDATE_GRAPH_STATE_WIRE") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(graphStateWireFixture), 0o755))
		require.NoError(t, os.WriteFile(graphStateWireFixture, got, 0o644))
	}
	want, err := os.ReadFile(graphStateWireFixture)
	require.NoError(t, err, "regenerate with UPDATE_GRAPH_STATE_WIRE=1 go test ./domain/engine -run WireFixture")
	require.JSONEq(t, string(want), string(got),
		"engine.GraphState JSON drifted from the dashboard contract fixture; regenerate it and re-run the dashboard contract test")
}

// wireGraphState populates every field the dashboard consumes: a root with a
// branch group (decision, gather with a conflict and a resolution session), and
// a workflow-task child that carries its own nested-group layout.
func wireGraphState() engine.GraphState {
	root := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	branchA := uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	child := uuid.MustParse("00000000-0000-0000-0000-00000000000c")
	run := uuid.MustParse("00000000-0000-0000-0000-0000000000d1")
	decided := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	ended := decided.Add(time.Minute)
	session := &instance.ResolutionSessionRef{GroupRunID: run, MergeIndex: 1, Attempt: 2, InstanceID: root, NodeID: "g"}

	rootLayout := &workflow.GroupGraph{
		Groups: map[string]*workflow.GroupScope{"g": {
			NodeID: "g",
			Config: workflow.GroupConfig{
				Form: "branches", CompletionRule: "all_succeed", UnfinishedBehavior: "cancel_unfinished", GatherMode: "results_and_files",
				Tasks: []workflow.GroupTask{{Key: "a", Entry: "a1"}},
			},
			Bodies: map[string][]string{"a": {"a1"}}, Nested: []string{}, SuccessTarget: "done", FailureTarget: "oops",
		}},
		Owner: map[string]workflow.BodyOwner{"a1": {GroupNodeID: "g", TaskKey: "a"}},
		Order: []string{"g"},
	}
	childLayout := &workflow.GroupGraph{
		Groups: map[string]*workflow.GroupScope{"k": {
			NodeID: "k",
			Config: workflow.GroupConfig{
				Form: "workflows", CompletionRule: "first_success", UnfinishedBehavior: "finish_independently", GatherMode: "results",
				Tasks: []workflow.GroupTask{{Key: "w", Workflow: "sub", Inputs: map[string]any{"x": "1"}}},
			},
			Bodies: map[string][]string{}, Nested: []string{}, SuccessTarget: "end", FailureTarget: "bad",
		}},
		Owner: map[string]workflow.BodyOwner{},
		Order: []string{"k"},
	}

	return engine.GraphState{
		RootInstanceID: root,
		Version:        "v1",
		Active:         true,
		Layout:         rootLayout,
		Warnings:       []string{"one descendant unreadable"},
		Contexts: []engine.ExecutionContext{
			{
				InstanceID: root, Kind: engine.ContextKindRoot, State: "waiting", Blocking: true,
				Graph: map[string]any{"nodes": []any{}},
				Nodes: []engine.LiveNodeState{{InstanceID: root, NodeID: "g", State: engine.LiveStateWaiting, Reason: "waiting_for_tasks:a"}},
			},
			{
				InstanceID: branchA, ParentInstanceID: &root, GroupRunID: &run, TaskKey: "a", Kind: engine.ContextKindBranch,
				State: "running", Blocking: true,
				Nodes: []engine.LiveNodeState{{InstanceID: branchA, NodeID: "a1", State: engine.LiveStateRunning, Attempt: 2}},
			},
			{
				InstanceID: child, ParentInstanceID: &root, GroupRunID: &run, TaskKey: "w", Kind: engine.ContextKindWorkflow,
				WorkflowName: "sub", State: "running", Blocking: false,
				Graph:  map[string]any{"nodes": []any{}},
				Layout: childLayout,
				Nodes:  []engine.LiveNodeState{{InstanceID: child, NodeID: "k", State: engine.LiveStateWaiting}},
			},
			{Kind: engine.ContextKindWorkflow, State: engine.LiveStateUnknown, Redacted: true, Nodes: []engine.LiveNodeState{}},
		},
		Groups: []engine.GroupPanel{{
			GroupRunID: run, InstanceID: root, NodeID: "g", Form: "branches",
			CompletionRule: "all_succeed", UnfinishedBehavior: "cancel_unfinished", GatherMode: "results_and_files",
			State: "resolving_conflicts",
			Tasks: []engine.GroupPanelTask{{
				Key: "a", Kind: "branch", Origin: "a1", ChildInstanceID: &branchA, LiveStatus: "completed", FrozenStatus: "unfinished",
				Blocking: false, ContinuingIndependently: true, CancelRequested: true, LaunchError: "boom",
			}},
			Progress:       engine.GroupProgress{Total: 1, Completed: 1},
			WaitingReasons: []string{"waiting_for_tasks:a", "cancelling:a"},
			Winner:         "a",
			Decision: &instance.GroupDecision{
				Outcome: "success", Rule: "first_success", Behavior: "finish_independently", WinnerKey: "a", Reason: "task a completed", DecidedAt: decided,
				Tasks: []instance.FrozenTask{{Key: "a", Kind: "branch", Origin: "a1", ChildInstanceID: branchA.String(), Status: "completed"}},
			},
			FinalOutput: map[string]any{"winner": "a"},
			Gather: &instance.GatherReport{
				Mode: instance.GatherModeResultsAndFiles, State: instance.GatherStateResolving, MergeIndex: 1,
				Tasks: []instance.GatherTaskReport{{Key: "a", Merged: false, Reason: "conflict", ConflictPaths: []string{"f.txt"}}},
				Attempts: []instance.ResolutionAttempt{{
					MergeIndex: 1, Attempt: 2, TaskKey: "a", ConflictPaths: []string{"f.txt"}, Outcome: instance.ResolutionOutcomeResolved,
					StartedAt: decided, EndedAt: &ended,
				}},
				ActiveSession: session,
			},
			ActiveSession: session,
		}},
	}
}
