package layout_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readerGroupWorkflow(form string, nested bool) *workflow.Workflow {
	cfg := &workflow.GroupConfig{Form: form, CompletionRule: workflow.GroupRuleFirstSuccess, UnfinishedBehavior: workflow.GroupUnfinishedFinishIndependently, GatherMode: workflow.GroupGatherResults,
		Tasks: []workflow.GroupTask{{Key: "second", Workflow: "同じ子 ✅", Inputs: map[string]any{"nested": map[string]any{"value": "saved"}}}, {Key: "one", Workflow: "同じ子 ✅"}}}
	wf := &workflow.Workflow{Nodes: []workflow.Node{{ID: "fan", Type: workflow.NodeTypeParallelGroup, Entrypoint: true, Config: workflow.NodeConfig{Group: cfg}}, {ID: "done"}, {ID: "oops"}},
		Edges: []workflow.Edge{{Source: "fan", Target: "done", Output: "success"}, {Source: "fan", Target: "oops", Output: "failure"}}}
	if form == workflow.GroupFormBranches {
		for i, id := range []string{"a", "b"} {
			cfg.Tasks[i].Workflow = ""
			cfg.Tasks[i].Entry = id
			wf.Nodes = append(wf.Nodes, workflow.Node{ID: id})
			wf.Edges = append(wf.Edges, workflow.Edge{Source: "fan", Target: id, Output: "task"})
		}
	}
	if nested {
		inner := readerGroupWorkflow(workflow.GroupFormWorkflows, false)
		wf.Nodes[3].Type = workflow.NodeTypeParallelGroup
		wf.Nodes[3].Config = inner.Nodes[0].Config
		wf.Nodes = append(wf.Nodes, workflow.Node{ID: "inner-done"}, workflow.Node{ID: "inner-oops"})
		wf.Edges = append(wf.Edges, workflow.Edge{Source: "a", Target: "inner-done", Output: "success"}, workflow.Edge{Source: "a", Target: "inner-oops", Output: "failure"})
	}
	return wf
}

func groupsOnWire(t *testing.T, v layout.View) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	return fields["groups"]
}

func TestReader_GroupsBoundToInputEvenWithoutResult(t *testing.T) {
	for _, tc := range []struct {
		form   string
		nested bool
	}{{"branches", false}, {"workflows", false}, {"branches", true}} {
		for _, state := range []string{"legacy", "missing", "pending", "outage"} {
			t.Run(tc.form+"/"+state+map[bool]string{true: "/nested"}[tc.nested], func(t *testing.T) {
				wf := readerGroupWorkflow(tc.form, tc.nested)
				want, findings := workflow.AnalyzeGroups(wf)
				require.Empty(t, findings)
				_, err := layout.BuildGraph(wf)
				require.NoError(t, err, "fixture must pass config parsing as well as ownership analysis")
				m := layouttest.NewMemStore()
				req := savedRequest(t)
				if state != "legacy" {
					wf.Presentation = requestWorkflow(t, *req).Presentation
				}
				if state == "pending" {
					var err error
					req.Manifest, err = json.Marshal(wf)
					require.NoError(t, err)
					require.NoError(t, layout.NewRequester(m, 5, nil).RequestArrangement(context.Background(), *req))
				}
				if state == "outage" {
					m.SetFailure(errors.New("offline"))
				}
				reader := layout.NewReader(readOnlyStore{m, t}, nil, nil)
				before, err := json.Marshal(wf)
				require.NoError(t, err)
				view := reader.View(context.Background(), wf)
				assert.Equal(t, wf.SavedRevision(), view.SavedRevision)
				require.NotEmpty(t, groupsOnWire(t, view), "ownership is response data from the displayed input, not result/cache data")
				wantJSON, err := json.Marshal(want)
				require.NoError(t, err)
				assert.JSONEq(t, string(wantJSON), string(groupsOnWire(t, view)))
				assert.Nil(t, view.Geometry, "ownership alone is not reviewed geometry")
				assert.True(t, view.Unreviewed)
				after, err := json.Marshal(wf)
				require.NoError(t, err)
				assert.Equal(t, before, after)
				// A subsequent authoring change must not mutate a previously returned response.
				wf.Nodes[0].Config.Group.Tasks[0].Inputs["nested"].(map[string]any)["value"] = "later"
				assert.JSONEq(t, string(wantJSON), string(groupsOnWire(t, view)))
				view.Groups.Groups["fan"].Config.Tasks[0].Inputs["nested"].(map[string]any)["value"] = "response-only"
				assert.Equal(t, "later", wf.Nodes[0].Config.Group.Tasks[0].Inputs["nested"].(map[string]any)["value"], "returned ownership must not mutate the authorized input")
			})
		}
	}
}

func TestReader_InvalidGroupsHaveExplicitFailureWithoutOwnership(t *testing.T) {
	wf := readerGroupWorkflow("branches", false)
	wf.Presentation = requestWorkflow(t, *savedRequest(t)).Presentation
	wf.Nodes[0].Config.Group = nil
	reader := layout.NewReader(readOnlyStore{layouttest.NewMemStore(), t}, nil, nil)
	view := reader.View(context.Background(), wf)
	assert.Empty(t, groupsOnWire(t, view))
	assert.Contains(t, view.FailureReason, layout.ErrInvalidGroups.Error())
	assert.Equal(t, layout.DisplayFailed, view.DisplayStatus)
	assert.True(t, view.Unreviewed)
	assert.True(t, view.RetryEligible)
	assert.Nil(t, view.Geometry)
	wf.Presentation = nil
	legacy := reader.View(context.Background(), wf)
	assert.Equal(t, layout.DisplayFailed, legacy.DisplayStatus)
	assert.False(t, legacy.RetryEligible, "a layout Retry cannot target a revision that does not exist")

	// A captured invalid input can still be awaiting its worker. Preserve that
	// readable row's active status so the real UI polls until failure settles.
	req := savedRequest(t)
	wf.Presentation = requestWorkflow(t, *req).Presentation
	var err error
	req.Manifest, err = json.Marshal(wf)
	require.NoError(t, err)
	store := layouttest.NewMemStore()
	require.NoError(t, layout.NewRequester(store, 5, nil).RequestArrangement(context.Background(), *req))
	pending := layout.NewReader(readOnlyStore{store, t}, nil, nil).View(context.Background(), wf)
	assert.Equal(t, layout.DisplayPending, pending.DisplayStatus)
	assert.Contains(t, pending.FailureReason, layout.ErrInvalidGroups.Error())
	assert.False(t, pending.RetryEligible)
	assert.Empty(t, groupsOnWire(t, pending))
}

func TestReader_OrdinaryInputHasNoGroupOwnership(t *testing.T) {
	reader := layout.NewReader(readOnlyStore{layouttest.NewMemStore(), t}, nil, nil)
	for _, wf := range []*workflow.Workflow{nil, layouttest.SampleWorkflow()} {
		assert.Empty(t, groupsOnWire(t, reader.View(context.Background(), wf)))
	}
}
